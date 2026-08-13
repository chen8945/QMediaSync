package openlist

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

var (
	errTokenExpired   = errors.New("token expired")
	errTokenRefreshed = errors.New("token refreshed")
)

// Client OpenList 客户端
type Client struct {
	AccountId   uint
	BaseUrl     string
	Username    string
	Password    string
	AccessToken string
	client      *resty.Client
}

// 全局 HTTP 客户端实例
var cachedClients map[string]*Client = make(map[string]*Client, 0)
var cachedClientsMutex sync.RWMutex

// NewClient 创建新的客户端
func NewClient(accountId uint, url, username, password, accessToken string) *Client {
	cachedClientsMutex.RLock()
	defer cachedClientsMutex.RUnlock()
	clientKey := fmt.Sprintf("%d", accountId)
	if client, exists := cachedClients[clientKey]; exists {
		client.BaseUrl = url
		client.Username = username
		client.Password = password
		client.SetAuthToken(accessToken)
		client.client.SetBaseURL(url)
		return client
	}
	restyClient := resty.New()
	restyClient.SetTimeout(time.Duration(DEFAULT_TIMEOUT) * time.Second).SetBaseURL(url)
	// 设置代理
	// restyClient.SetProxy("http://127.0.0.1:10808")

	client := &Client{
		AccountId:   accountId,
		BaseUrl:     url,
		Username:    username,
		Password:    password,
		AccessToken: accessToken,
		client:      restyClient,
	}
	cachedClients[clientKey] = client
	return client
}

func (c *Client) SetAuthToken(accessToken string) {
	c.AccessToken = accessToken
}

// doRequest 执行 HTTP 请求
func (c *Client) doRequest(url string, req *resty.Request, options *RequestConfig) (*resty.Response, error) {
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
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		resp, err := c.request(url, req)
		if err == nil {
			// 正常返回
			return resp, nil
		}
		lastErr = err
		// 只有凭据刷新成功后才重试，避免无效 Token 被重复请求。
		if errors.Is(err, errTokenRefreshed) {
			helpers.OpenListLog.Warn("访问凭证已过期，正在刷新")
			continue
		}
		if errors.Is(err, errTokenExpired) {
			return nil, lastErr
		}
		// 其他错误开始重试
		if attempt < options.MaxRetries {
			// helpers.OpenListLog.Warnf("%s %s 请求失败：%+v", req.Method, req.URL, lastErr)
			helpers.OpenListLog.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试），错误：%+v", req.Method, req.URL, options.RetryDelay.Seconds(), attempt+1, lastErr)
			time.Sleep(options.RetryDelay)
		}
	}
	return nil, lastErr
}

func (c *Client) request(url string, req *resty.Request) (*resty.Response, error) {
	// req.SetResponseForceContentType("application/json")
	var response *resty.Response
	var err error
	if c.AccessToken != "" {
		req.SetHeader("Authorization", c.AccessToken)
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
	result := response.Result()
	data, err := json.Marshal(result)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 请求 %s %s 序列化失败：%+v", req.Method, req.URL, err)
		return response, err
	}
	var jsonResult map[string]interface{}
	err = json.Unmarshal(data, &jsonResult)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 请求 %s %s 反序列化失败：%+v", req.Method, req.URL, err)
		return response, err
	}
	// helpers.OpenListLog.Infof("认证访问 %s %s\nstate=%v, code=%d, msg=%s, data=%s\n", req.Method, req.URL, resp.State, resp.Code, resp.Message, string(resp.Data))
	helpers.OpenListLog.Infof("%s %s 请求数据：%+v 返回值：%s\n", req.Method, req.URL, req.Body, string(data))
	if data != nil && jsonResult != nil {
		switch jsonResult["code"].(float64) {
		case http.StatusUnauthorized:
			// Token 过期时，只有用户名和密码登录才尝试刷新凭据。
			if c.Username == "" || c.Password == "" {
				return response, errTokenExpired
			}
			if _, err := c.GetToken(); err != nil {
				return response, errTokenExpired
			}
			return response, errTokenRefreshed
		}
		if jsonResult["code"].(float64) != http.StatusOK {
			helpers.OpenListLog.Errorf("OpenList 请求 %s %s 失败：%s", req.Method, req.URL, jsonResult["message"].(string))
			return response, fmt.Errorf("%s", jsonResult["message"].(string))
		}
	}
	return response, nil
}
