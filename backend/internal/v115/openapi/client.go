package v115open

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

// OpenClient HTTP 客户端
type OpenClient struct {
	AccountId   uint // 账号 ID
	client      *resty.Client
	credentials atomic.Pointer[clientCredentials]
	playback    bool // 播放编排使用单次请求策略，构造后不再修改。
}

// clientCredentials 发布后不可修改，应用 ID 与令牌必须属于同一快照。
type clientCredentials struct {
	appID        string
	accessToken  string
	refreshToken string
}

type requestLogSecretsContextKey struct{}

// requestLogSecrets 为响应后的校验保留实际请求凭据，缓存更新不影响该副本。
func requestLogSecrets(req *resty.Request) []string {
	secrets, _ := req.Context().Value(requestLogSecretsContextKey{}).([]string)
	return append(append([]string(nil), secrets...), req.AuthToken)
}

// 全局 HTTP 客户端实例
var cachedClients map[string]*OpenClient = make(map[string]*OpenClient, 0)
var cachedClientsMutex sync.RWMutex

func UpdateToken(accountId uint, token string, refreshToken string) {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	for key, client := range cachedClients {
		if client.AccountId == accountId {
			client.SetAuthToken(token, refreshToken)
			helpers.AppLogger.Infof("更新 115 客户端 %s 的 Token 成功", key)
		}
	}
}

// UpdateTokenIfCurrent 仅当缓存客户端仍持有预期凭据时更新令牌。
// 远端刷新完成后，授权替换可能已经刷新了同一账号的共享客户端。
func UpdateTokenIfCurrent(accountId uint, expectedToken string, expectedRefreshToken string, token string, refreshToken string) bool {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	updated := false
	for key, client := range cachedClients {
		if client.AccountId != accountId {
			continue
		}
		if !client.setAuthTokenIfCurrent(
			expectedToken,
			expectedRefreshToken,
			token,
			refreshToken,
		) {
			continue
		}
		helpers.AppLogger.Debugf("条件更新 115 客户端 %s 的 Token 成功", key)
		updated = true
	}
	return updated
}

// NewClient 创建不进入共享缓存的 115 HTTP 客户端。
func NewClient(accountId uint, appId string, token string, refreshToken string) *OpenClient {
	client := resty.New()
	openClient := &OpenClient{
		client:    client,
		AccountId: accountId,
	}
	openClient.credentials.Store(&clientCredentials{
		appID:        appId,
		accessToken:  token,
		refreshToken: refreshToken,
	})
	return openClient
}

// SetTransport 在首次请求前配置当前实例的 HTTP 传输层。
// 缓存客户端由同账号调用方共享，开始请求后不得替换。
func (c *OpenClient) SetTransport(transport http.RoundTripper) {
	c.client.SetTransport(transport)
}

// GetClient 获取按账号 ID 缓存的 HTTP 客户端。
func GetClient(accountId uint, appId string, token string, refreshToken string) *OpenClient {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	clientKey := fmt.Sprintf("%d", accountId)
	if client, exists := cachedClients[clientKey]; exists {
		client.credentials.Store(&clientCredentials{
			appID:        appId,
			accessToken:  token,
			refreshToken: refreshToken,
		})
		return client
	}

	openClient := NewClient(accountId, appId, token, refreshToken)
	cachedClients[clientKey] = openClient
	return openClient
}

// GetCachedClient 获取账号共享客户端，但不使用调用方的旧账号快照覆盖已有凭据。
func GetCachedClient(accountId uint, appId string, token string, refreshToken string) *OpenClient {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	clientKey := fmt.Sprintf("%d", accountId)
	if client, exists := cachedClients[clientKey]; exists {
		return client
	}

	openClient := NewClient(accountId, appId, token, refreshToken)
	cachedClients[clientKey] = openClient
	return openClient
}

func (c *OpenClient) credentialSnapshot() clientCredentials {
	if current := c.credentials.Load(); current != nil {
		return *current
	}
	return clientCredentials{}
}

// SetAuthToken 保留当前应用 ID，原子替换访问和刷新令牌。
func (c *OpenClient) SetAuthToken(token string, refreshToken string) {
	for {
		current := c.credentials.Load()
		next := &clientCredentials{accessToken: token, refreshToken: refreshToken}
		if current != nil {
			next.appID = current.appID
		}
		if c.credentials.CompareAndSwap(current, next) {
			return
		}
	}
}

func (c *OpenClient) setAuthTokenIfCurrent(expectedToken, expectedRefreshToken, token, refreshToken string) bool {
	current := c.credentials.Load()
	if current == nil {
		return false
	}
	if current.accessToken != expectedToken || current.refreshToken != expectedRefreshToken {
		return false
	}
	next := &clientCredentials{
		appID:        current.appID,
		accessToken:  token,
		refreshToken: refreshToken,
	}
	return c.credentials.CompareAndSwap(current, next)
}

// doRequest 带重试的请求方法（使用全局队列）
func (c *OpenClient) doRequest(url string, req *resty.Request, options *RequestConfig, secrets ...string) (response *resty.Response, data *RespBase[json.RawMessage], err error) {
	credentials := c.credentialSnapshot()
	logSecrets := append([]string{credentials.accessToken, credentials.refreshToken}, secrets...)
	req.SetContext(context.WithValue(req.Context(), requestLogSecretsContextKey{}, append([]string(nil), logSecrets...)))
	var lastRequest *RequestResponse
	defer func() {
		c.logRequestFailure(req.Method, url, lastRequest, requestLogFailureCause(lastRequest, err), logSecrets...)
		err = helpers.WithLogSecrets(err, logSecrets...)
	}()
	// 设置超时时间
	req.SetTimeout(options.Timeout)
	// 设置默认头
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", DEFAULTUA)
	}

	var lastErr error
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		// 使用全局队列执行器处理请求
		executor := GetGlobalExecutor()
		respChan := make(chan *RequestResponse, 1)

		queuedReq := &QueuedRequest{
			AccountID:       c.AccountId,
			URL:             url,
			Method:          req.Method,
			Request:         req,
			BypassRateLimit: options.BypassRateLimit,
			ResponseChan:    respChan,
			CreatedAt:       time.Now(),
			Ctx:             context.Background(),
			logSecrets:      logSecrets,
		}

		// 将请求加入队列
		executor.EnqueueRequest(queuedReq)

		// 等待响应
		queueResp := <-respChan
		lastRequest = queueResp

		if queueResp.Error == nil && queueResp.RespData != nil {
			// 请求成功，转换为 RespBase 格式
			respBase := &RespBase[json.RawMessage]{
				State:   0,
				Code:    queueResp.RespData.Code,
				Errno:   queueResp.RespData.Errno,
				Message: queueResp.RespData.Message,
				Error:   queueResp.RespData.Error,
				Data:    queueResp.RespData.Data,
			}
			if queueResp.RespData.State {
				respBase.State = 1
			}
			return queueResp.Response, respBase, nil
		}

		lastErr = queueResp.Error

		// Token 相关错误不重试
		if queueResp.RespData != nil {
			switch queueResp.RespData.Code {
			case REFRESH_TOKEN_FORMAT_INVALID, REFRESH_TOKEN_SIGN_INVALID, REFRESH_TOKEN_INVALID, REFRESH_TOKEN_EXPIRED, REFRESH_TOKEN_CHECK_FAILED, REFRESH_TOO_FREQUENT:
				// 转换为 RespBase 格式返回
				respBase := &RespBase[json.RawMessage]{
					State:   0,
					Code:    queueResp.RespData.Code,
					Errno:   queueResp.RespData.Errno,
					Message: queueResp.RespData.Message,
					Error:   queueResp.RespData.Error,
					Data:    queueResp.RespData.Data,
				}
				if queueResp.RespData.State {
					respBase.State = 1
				}
				return queueResp.Response, respBase, lastErr
			}
		}

		// 如果是限流错误，不重试
		if queueResp.IsThrottled {
			helpers.V115Log.Warn("检测到限流，停止重试")
			if queueResp.RespData != nil {
				respBase := &RespBase[json.RawMessage]{
					State:   0,
					Code:    queueResp.RespData.Code,
					Errno:   queueResp.RespData.Errno,
					Message: queueResp.RespData.Message,
					Error:   queueResp.RespData.Error,
					Data:    queueResp.RespData.Data,
				}
				if queueResp.RespData.State {
					respBase.State = 1
				}
				return queueResp.Response, respBase, lastErr
			}
			return queueResp.Response, nil, lastErr
		}

		// 其他错误开始重试
		if attempt < options.MaxRetries && lastErr != nil {
			helpers.V115Log.Warnf("%s %s 请求失败，错误类型=%T", req.Method, url, lastErr)
			helpers.V115Log.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试）", req.Method, url, options.RetryDelay.Seconds(), attempt+1)
			time.Sleep(options.RetryDelay)
		}
	}
	return nil, nil, lastErr
}

// doAuthRequest 带重试的认证请求方法（使用全局队列）
func (c *OpenClient) doAuthRequest(ctx context.Context, url string, req *resty.Request, options *RequestConfig, respData any) (response *resty.Response, raw []byte, err error) {
	defer beginRequestDirectoryWrite(c.AccountId, url, req)()
	credentials := c.credentialSnapshot()
	logSecrets := []string{credentials.accessToken, credentials.refreshToken}
	ctx = context.WithValue(ctx, requestLogSecretsContextKey{}, append([]string(nil), logSecrets...))
	req.SetContext(ctx)
	var lastRequest *RequestResponse
	defer func() {
		cause := requestLogFailureCause(lastRequest, err)
		if options != nil && options.DeletionDetail && lastRequest != nil {
			cause = deletionDetailAbsenceError(lastRequest.RespBytes, cause)
		}
		if !c.playback && !isPreloadOperation(ctx) && !(options != nil && options.DeletionDetail && IsAlreadyDeleted(cause)) {
			c.logRequestFailure(req.Method, url, lastRequest, cause, logSecrets...)
		}
		err = helpers.WithLogSecrets(err, logSecrets...)
	}()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if c.playback {
		return c.doPlaybackRequest(ctx, url, req, options, respData)
	}
	if credentials.accessToken == "" {
		// 没有 Token，直接报错
		return nil, nil, NewOpenAPIError(ACCESS_AUTH_INVALID, "115 账号授权失效，请在网盘账号管理中重新授权")
	}
	req.SetTimeout(options.Timeout)
	// 设置默认头
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", DEFAULTUA)
	}
	req.SetAuthToken(credentials.accessToken).SetResponseDoNotParse(true)

	var lastErr error
	var lastRespBytes []byte
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		// 使用全局队列执行器处理请求
		executor := GetGlobalExecutor()
		respChan := make(chan *RequestResponse, 1)

		queuedReq := &QueuedRequest{
			AccountID:       c.AccountId,
			URL:             url,
			Method:          req.Method,
			Request:         req,
			BypassRateLimit: options.BypassRateLimit,
			ResponseChan:    respChan,
			CreatedAt:       time.Now(),
			Ctx:             ctx,
			BeforeSend:      options.BeforeSend,
			DeletionDetail:  options.DeletionDetail,
			logSecrets:      logSecrets,
		}

		// 将请求加入队列
		executor.EnqueueRequest(queuedReq)

		// 等待响应
		var queueResp *RequestResponse
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case queueResp = <-respChan:
		}
		lastRequest = queueResp
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		if queueResp.Error == nil && queueResp.RespData != nil {
			// 请求成功
			if respData != nil && queueResp.RespData.State {
				// 解包响应数据
				if unmarshalErr := json.Unmarshal(queueResp.RespData.Data, respData); unmarshalErr != nil {
					helpers.V115Log.Errorf("115 %s %s 解包响应数据失败，账号=%d，原因=%q，错误类型=%T",
						req.Method, requestLogEndpoint(url), c.AccountId, helpers.SafeLogError(unmarshalErr, logSecrets...), unmarshalErr)
					return queueResp.Response, queueResp.RespBytes, nil
				}
			}
			return queueResp.Response, queueResp.RespBytes, nil
		}

		lastErr = queueResp.Error

		// Token 相关错误处理
		if queueResp.RespData != nil {
			switch queueResp.RespData.Code {
			case ACCESS_TOKEN_AUTH_FAIL, ACCESS_AUTH_INVALID, ACCESS_TOKEN_EXPIRY_CODE:
				helpers.V115Log.Errorf("访问凭证过期，等待自动刷新后下次重试")
				lastErr = NewOpenAPIError(queueResp.RespData.Code, "访问凭证（Token）过期")
			case REFRESH_TOKEN_INVALID:
				lastErr = NewOpenAPIError(queueResp.RespData.Code, "访问凭证（Token）无效，请重新登录")
				return queueResp.Response, queueResp.RespBytes, lastErr
			}
		}

		if options.RetryIf != nil && lastErr != nil && !options.RetryIf(lastErr) {
			return queueResp.Response, queueResp.RespBytes, lastErr
		}

		// 如果是限流错误，不重试
		if queueResp.IsThrottled {
			helpers.V115Log.Warn("检测到限流，等待 1 分钟后重试")
			// 等待 1 分钟后重试
			if err := sleepWithTimer(ctx, time.Minute); err != nil {
				return nil, nil, err
			}
			continue
		}

		// 其他错误开始重试
		if attempt < options.MaxRetries && lastErr != nil {
			helpers.V115Log.Warnf("%s %s 请求失败，错误类型=%T", req.Method, url, lastErr)
			helpers.V115Log.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试）", req.Method, url, options.RetryDelay.Seconds(), attempt+1)
			if err := sleepWithTimer(ctx, options.RetryDelay); err != nil {
				return nil, nil, err
			}
		}
		lastRespBytes = queueResp.RespBytes
	}
	return nil, lastRespBytes, lastErr
}

type responseLogFields struct {
	State json.RawMessage `json:"state"`
	Code  *int            `json:"code"`
	Errno *int            `json:"errno"`
}

func parseResponseLogFields(raw []byte) responseLogFields {
	var fields responseLogFields
	if json.Unmarshal(raw, &fields) != nil {
		return responseLogFields{}
	}
	return fields
}

func logResponseCode(code *int) any {
	if code == nil {
		return "unknown"
	}
	return *code
}

// requestLogFailureCause 只补诊断，不把缺失 state 的默认 false 当成失败或改变返回值。
func requestLogFailureCause(result *RequestResponse, err error) error {
	if err != nil || result == nil || result.RespData == nil {
		return err
	}
	fields := parseResponseLogFields(result.RespBytes)
	state := strings.TrimSpace(string(fields.State))
	if state != "false" && state != "0" &&
		(fields.Code == nil || *fields.Code == 0) && (fields.Errno == nil || *fields.Errno == 0) {
		return nil
	}
	return NewOpenAPIResponseError(result.RespData.Code, result.RespData.Errno, result.RespData.Message, result.RespData.Error, "115 开放平台请求未成功")
}

func (c *OpenClient) logRequestFailure(method, url string, result *RequestResponse, err error, secrets ...string) {
	if err == nil {
		return
	}
	var status int
	var code, errno any = "unknown", "unknown"
	if result != nil {
		if result.Response != nil {
			status = result.Response.StatusCode()
		}
		fields := parseResponseLogFields(result.RespBytes)
		code, errno = logResponseCode(fields.Code), logResponseCode(fields.Errno)
	}
	if apiErr, ok := errors.AsType[*OpenAPIError](err); ok {
		if apiErr.Code != 0 && (result == nil || result.RespData == nil) {
			code = apiErr.Code
		}
		if status == 0 {
			status = apiErr.HTTPStatus
		}
	}
	category := "请求失败"
	logf := helpers.V115Log.Errorf
	if errors.Is(err, context.Canceled) {
		category, logf = "已取消", helpers.V115Log.Debugf
	} else if networkErr, ok := errors.AsType[net.Error](err); ok {
		category = "网络错误"
		if networkErr.Timeout() || errors.Is(err, context.DeadlineExceeded) {
			category = "超时"
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		category = "超时"
	} else if _, ok := errors.AsType[*json.SyntaxError](err); ok {
		category = "响应解析失败"
	} else if _, ok := errors.AsType[*json.UnmarshalTypeError](err); ok {
		category = "响应解析失败"
	} else if status != 0 && (status < http.StatusOK || status >= http.StatusMultipleChoices) {
		category = "HTTP 拒绝"
	} else if _, ok := errors.AsType[*OpenAPIError](err); ok {
		category = "业务拒绝"
	}
	logf("115 %s %s 最终失败，账号=%d，HTTP=%d，code=%v，errno=%v，类别=%s，原因=%q，错误类型=%T",
		method, requestLogEndpoint(url), c.AccountId, status, code, errno, category, helpers.SafeLogError(err, secrets...), err)
}

// requestLogEndpoint 保留接口来源及路径，参数由单独的 DEBUG 记录提供。
func requestLogEndpoint(rawURL string) string {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid-url>"
	}
	endpoint.User, endpoint.RawQuery, endpoint.Fragment = nil, "", ""
	return endpoint.String()
}
