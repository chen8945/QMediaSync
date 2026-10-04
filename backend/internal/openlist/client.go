package openlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

// ErrTokenExpired 表示访问凭证失效，或既有认证恢复后仍无法继续请求。
var ErrTokenExpired = errors.New("token expired")

// Client OpenList 客户端
// 共享后通过 NewClient 和 Token 方法读写配置，避免直接访问可变字段。
type Client struct {
	AccountId    uint
	BaseUrl      string
	Username     string
	Password     string
	AccessToken  string
	client       *resty.Client
	stateMu      sync.RWMutex
	refreshGroup singleflight.Group
}

// 全局 HTTP 客户端实例
var cachedClients map[string]*Client = make(map[string]*Client, 0)

// 客户端构造共用短临界区；出现构造争用后再拆分缓存获取和配置更新。
var cachedClientsMutex sync.Mutex

// NewClient 创建新的客户端
func NewClient(accountId uint, url, username, password, accessToken string) *Client {
	cachedClientsMutex.Lock()
	defer cachedClientsMutex.Unlock()
	clientKey := fmt.Sprintf("%d", accountId)
	if client, exists := cachedClients[clientKey]; exists {
		client.stateMu.Lock()
		defer client.stateMu.Unlock()
		client.BaseUrl = url
		client.Username = username
		client.Password = password
		client.AccessToken = accessToken
		return client
	}
	client := NewTemporaryClient(url, username, password, accessToken)
	client.AccountId = accountId
	cachedClients[clientKey] = client
	return client
}

// NewTemporaryClient 创建不进入共享缓存、不发布账号保存事件的临时验证客户端。
func NewTemporaryClient(url, username, password, accessToken string) *Client {
	restyClient := resty.New()
	restyClient.SetTimeout(time.Duration(DEFAULT_TIMEOUT) * time.Second).SetBaseURL(url)
	// 设置代理
	// restyClient.SetProxy("http://127.0.0.1:10808")

	return &Client{
		BaseUrl:     url,
		Username:    username,
		Password:    password,
		AccessToken: accessToken,
		client:      restyClient,
	}
}

// Close 释放客户端资源；临时验证完成后调用，共享客户端不能在单次请求后关闭。
func (c *Client) Close() error {
	return c.client.Close()
}

// SetAuthToken 更新访问凭证，允许与请求并发调用。
func (c *Client) SetAuthToken(accessToken string) {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	c.AccessToken = accessToken
}

// GetAuthToken 返回当前访问凭证，允许与配置更新及 Token 刷新并发调用。
func (c *Client) GetAuthToken() string {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.AccessToken
}

type clientState struct {
	accountID   uint
	baseURL     string
	username    string
	password    string
	accessToken string
}

func (state clientState) sameLogin(other clientState) bool {
	return state.accountID == other.accountID && state.baseURL == other.baseURL &&
		state.username == other.username && state.password == other.password
}

func (c *Client) snapshot() clientState {
	c.stateMu.RLock()
	defer c.stateMu.RUnlock()
	return c.snapshotLocked()
}

// snapshotLocked 的调用方必须持有 stateMu。
func (c *Client) snapshotLocked() clientState {
	return clientState{
		accountID:   c.AccountId,
		baseURL:     c.BaseUrl,
		username:    c.Username,
		password:    c.Password,
		accessToken: c.AccessToken,
	}
}

// doRequest 执行 HTTP 请求
func (c *Client) doRequest(path string, req *resty.Request, options *RequestConfig) (*resty.Response, error) {
	return c.doRequestWithState(path, req, options, c.snapshot())
}

func (c *Client) doRequestWithState(path string, req *resty.Request, options *RequestConfig, state clientState) (*resty.Response, error) {
	if options == nil {
		options = DefaultRequestConfig()
	}
	// 设置超时时间
	req.SetTimeout(options.Timeout)
	// 设置默认头
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", DEFAULTUA)
	}
	var lastErr error
	authRetried := false
	for attempt := 0; attempt <= options.MaxRetries; {
		if err := req.Context().Err(); err != nil {
			return nil, err
		}
		// 保留未执行的模板，让 multipart 每次重新打开文件；Clone 默认会新建 Result。
		attemptReq := req.Clone(req.Context())
		attemptReq.Result = req.Result
		resp, err := c.request(path, attemptReq, &state)
		if contextErr := req.Context().Err(); contextErr != nil {
			return nil, contextErr
		}
		if err == nil {
			// 正常返回
			return resp, nil
		}
		lastErr = err
		if errors.Is(err, ErrTokenExpired) {
			if options.skipAuthRetry {
				return resp, ErrTokenExpired
			}
			if path == "/api/auth/login" || state.username == "" || state.password == "" {
				return nil, err
			}
			// 每个业务请求仅允许一次认证恢复，不消耗普通重试预算。
			if authRetried {
				return nil, fmt.Errorf("访问凭证刷新后仍被拒绝：%w", ErrTokenExpired)
			}
			// 同一客户端按地址和登录凭据合并并发刷新，避免跨地址复用 Token。
			key := strings.TrimRight(state.baseURL, "/") + "\x00" + state.username + "\x00" + state.password
			refresh := c.refreshGroup.DoChan(key, func() (any, error) {
				current := c.snapshot()
				if current.sameLogin(state) && current.accessToken != "" && current.accessToken != state.accessToken {
					return &TokenData{Token: current.accessToken}, nil
				}
				return c.getToken(state)
			})
			// 共享登录继续服务其他等待者；取消当前浏览只结束当前等待。
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case refreshed := <-refresh:
				if refreshed.Err != nil {
					return nil, ErrTokenExpired
				}
				state.accessToken = refreshed.Val.(*TokenData).Token
			}
			authRetried = true
			helpers.OpenListLog.Warn("访问凭证已更新，重新发送请求")
			continue
		}
		// 其他错误开始重试
		if attempt < options.MaxRetries {
			// helpers.OpenListLog.Warnf("%s %s 请求失败：%+v", req.Method, req.URL, lastErr)
			helpers.OpenListLog.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试），错误：%+v", attemptReq.Method, attemptReq.URL, options.RetryDelay.Seconds(), attempt+1, lastErr)
			timer := time.NewTimer(options.RetryDelay)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}
		}
		attempt++
	}
	return nil, lastErr
}

func (c *Client) request(path string, req *resty.Request, state *clientState) (*resty.Response, error) {
	// req.SetResponseForceContentType("application/json")
	var response *resty.Response
	var err error
	req.SetResponseBodyUnlimitedReads(true)
	// URL 和凭据来自同一快照，锁不跨 HTTP 请求或同步事件回调。
	url := strings.TrimRight(state.baseURL, "/") + path
	if state.accessToken != "" {
		req.SetHeader("Authorization", state.accessToken)
	}
	switch req.Method {
	case "GET":
		response, err = req.Get(url)
	case "POST":
		response, err = req.Post(url)
	case "PUT":
		response, err = req.Put(url)
	default:
		return nil, fmt.Errorf("不支持的 HTTP 方法：%s", req.Method)
	}
	if err != nil {
		return response, err
	}
	if response.StatusCode() == http.StatusUnauthorized {
		return response, ErrTokenExpired
	}
	if response.StatusCode() < http.StatusOK || response.StatusCode() >= http.StatusMultipleChoices {
		return response, fmt.Errorf("OpenList HTTP 状态异常：%d", response.StatusCode())
	}
	data := response.Bytes()
	var envelope struct {
		Code    *int   `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return response, fmt.Errorf("OpenList 响应格式无效：%w", err)
	}
	if path != "/api/auth/login" {
		helpers.OpenListLog.Infof("%s %s 请求数据：%+v 返回值：%s\n", req.Method, req.URL, req.Body, string(data))
	}
	if envelope.Code == nil {
		return response, fmt.Errorf("OpenList 响应缺少业务状态")
	}
	if *envelope.Code == http.StatusUnauthorized {
		return response, ErrTokenExpired
	}
	if *envelope.Code != http.StatusOK {
		if envelope.Message != "" {
			return response, fmt.Errorf("%s", envelope.Message)
		}
		return response, fmt.Errorf("OpenList 业务状态异常：%d", *envelope.Code)
	}

	return response, nil
}
