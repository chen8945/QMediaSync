package v115open

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

// OpenClient HTTP 客户端
type OpenClient struct {
	AppId           string // 应用 ID
	AccountId       uint   // 账号 ID
	client          *resty.Client
	AccessToken     string // 访问令牌
	RefreshTokenStr string // 刷新令牌
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
		if client.AccountId != accountId || client.AccessToken != expectedToken || client.RefreshTokenStr != expectedRefreshToken {
			continue
		}
		client.SetAuthToken(token, refreshToken)
		helpers.AppLogger.Infof("条件更新 115 客户端 %s 的 Token 成功", key)
		updated = true
	}
	return updated
}

// NewClient 创建不进入共享缓存的 115 HTTP 客户端。
func NewClient(accountId uint, appId string, token string, refreshToken string) *OpenClient {
	client := resty.New()
	openClient := &OpenClient{
		client:    client,
		AppId:     appId,
		AccountId: accountId,
	}
	openClient.SetAuthToken(token, refreshToken)
	return openClient
}

// GetClient 获取按账号 ID 缓存的 HTTP 客户端。
func GetClient(accountId uint, appId string, token string, refreshToken string) *OpenClient {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	clientKey := fmt.Sprintf("%d", accountId)
	if client, exists := cachedClients[clientKey]; exists {
		client.AppId = appId
		client.SetAuthToken(token, refreshToken)
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

// SetAuthToken 设置认证令牌
func (c *OpenClient) SetAuthToken(token string, refreshToken string) {
	c.AccessToken = token
	c.RefreshTokenStr = refreshToken
}

// doRequest 带重试的请求方法（使用全局队列）
func (c *OpenClient) doRequest(url string, req *resty.Request, options *RequestConfig) (*resty.Response, *RespBase[json.RawMessage], error) {
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
			URL:             url,
			Method:          req.Method,
			Request:         req,
			BypassRateLimit: options.BypassRateLimit,
			ResponseChan:    respChan,
			CreatedAt:       time.Now(),
			Ctx:             context.Background(),
		}

		// 将请求加入队列
		executor.EnqueueRequest(queuedReq)

		// 等待响应
		queueResp := <-respChan

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
			case REFRESH_TOKEN_INVALID:
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
			helpers.V115Log.Warnf("%s %s 请求失败：%+v", req.Method, url, lastErr)
			helpers.V115Log.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试）", req.Method, url, options.RetryDelay.Seconds(), attempt+1)
			time.Sleep(options.RetryDelay)
		}
	}
	return nil, nil, lastErr
}

// doAuthRequest 带重试的认证请求方法（使用全局队列）
func (c *OpenClient) doAuthRequest(ctx context.Context, url string, req *resty.Request, options *RequestConfig, respData any) (*resty.Response, []byte, error) {
	if c.AccessToken == "" {
		// 没有 Token，直接报错
		return nil, nil, fmt.Errorf("115 账号授权失效，请在网盘账号管理中重新授权")
	}
	req.SetTimeout(options.Timeout)
	// 设置默认头
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", DEFAULTUA)
	}
	req.SetAuthToken(c.AccessToken).SetResponseDoNotParse(true)

	var lastErr error
	var lastRespBytes []byte
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		// 使用全局队列执行器处理请求
		executor := GetGlobalExecutor()
		respChan := make(chan *RequestResponse, 1)

		queuedReq := &QueuedRequest{
			URL:             url,
			Method:          req.Method,
			Request:         req,
			BypassRateLimit: options.BypassRateLimit,
			ResponseChan:    respChan,
			CreatedAt:       time.Now(),
			Ctx:             ctx,
		}

		// 将请求加入队列
		executor.EnqueueRequest(queuedReq)

		// 等待响应
		queueResp := <-respChan

		if queueResp.Error == nil && queueResp.RespData != nil {
			// 请求成功
			if respData != nil && queueResp.RespData.State {
				// 解包响应数据
				helpers.V115Log.Debugf("解包 %s", string(queueResp.RespData.Data))
				if unmarshalErr := json.Unmarshal(queueResp.RespData.Data, respData); unmarshalErr != nil {
					helpers.V115Log.Errorf("解包响应数据失败：%s", unmarshalErr.Error())
					return queueResp.Response, queueResp.RespBytes, nil
				}
			}
			helpers.V115Log.Debugf("请求 %s %s 响应：%s", req.Method, url, string(queueResp.RespBytes))
			return queueResp.Response, queueResp.RespBytes, nil
		}

		lastErr = queueResp.Error

		// Token 相关错误处理
		if queueResp.RespData != nil {
			switch queueResp.RespData.Code {
			case ACCESS_TOKEN_AUTH_FAIL, ACCESS_AUTH_INVALID, ACCESS_TOKEN_EXPIRY_CODE:
				helpers.V115Log.Errorf("访问凭证过期，等待自动刷新后下次重试")
				lastErr = fmt.Errorf("访问凭证（Token）过期")
			case REFRESH_TOKEN_INVALID:
				lastErr = fmt.Errorf("访问凭证（Token）无效，请重新登录")
				return queueResp.Response, queueResp.RespBytes, lastErr
			}
		}

		// 如果是限流错误，不重试
		if queueResp.IsThrottled {
			helpers.V115Log.Warn("检测到限流，等待 1 分钟后重试")
			// 等待 1 分钟后重试
			time.Sleep(1 * time.Minute)
			continue
		}

		// 其他错误开始重试
		if attempt < options.MaxRetries && lastErr != nil {
			helpers.V115Log.Warnf("%s %s 请求失败：%+v", req.Method, url, lastErr)
			helpers.V115Log.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试）", req.Method, url, options.RetryDelay.Seconds(), attempt+1)
			time.Sleep(options.RetryDelay)
		}
		lastRespBytes = queueResp.RespBytes
	}
	return nil, lastRespBytes, lastErr
}
