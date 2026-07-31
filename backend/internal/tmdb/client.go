package tmdb

import (
	"fmt"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"

	"golang.org/x/time/rate"
	"resty.dev/v3"
)

const (
	// DefaultBaseURL is the default TMDB API base URL
	DefaultBaseURL = "https://api.tmdb.org"
	// 重试配置
	DEFAULT_MAX_RETRIES = 3
	DEFAULT_RETRY_DELAY = 1

	// 超时配置
	DEFAULT_TIMEOUT = 30 // 秒
)

// Client represents a TMDB API client

type Client struct {
	resty       *resty.Client
	apiKey      string
	accessToken string
	baseURL     string
	language    string
	proxyUrl    string
	rateLimiter *rate.Limiter
}

var GlobalTmdbClient *Client

// NewClient creates a new TMDB API client with the given configuration
func NewClient(apiKey, accessToken, baseUrl, language, proxyUrl string) *Client {
	if GlobalTmdbClient != nil {
		// if accessToken != "" {
		// 	GlobalTmdbClient.SetAuthToken(accessToken)
		// }
		GlobalTmdbClient.SetProxyUrl(proxyUrl)
		GlobalTmdbClient.SetBaseUrl(baseUrl)
		GlobalTmdbClient.apiKey = apiKey
		GlobalTmdbClient.language = language
		return GlobalTmdbClient
	}

	// Create resty client
	rc := resty.New()
	rc.SetHeader("User-Agent", v115open.DEFAULTUA)
	GlobalTmdbClient = &Client{
		resty:       rc,
		apiKey:      apiKey,
		accessToken: accessToken,
		baseURL:     baseUrl,
		language:    language,
		proxyUrl:    proxyUrl,
	}
	// if accessToken != "" {
	// 	GlobalTmdbClient.SetAuthToken(accessToken)
	// }
	GlobalTmdbClient.SetProxyUrl(proxyUrl)
	GlobalTmdbClient.SetBaseUrl(baseUrl)
	// 设置限流
	GlobalTmdbClient.rateLimiter = rate.NewLimiter(rate.Every(time.Second), 40) // 每秒 40 个请求
	return GlobalTmdbClient
}

func (c *Client) SetAuthToken(accessToken string) {
	c.accessToken = accessToken
	c.resty.SetAuthToken(c.accessToken)
}

// SetProxyUrl 设置或清除客户端代理。
// 必须有 else 分支：Client 是全局单例，只在非空时 SetProxy 会让"清空代理"和"关闭 TMDB 代理开关"变成空操作，
// 请求会继续带 API Key 走用户已撤销的代理隧道。
func (c *Client) SetProxyUrl(proxyUrl string) {
	c.proxyUrl = proxyUrl
	if proxyUrl != "" {
		c.resty.SetProxy(proxyUrl)
	} else {
		c.resty.RemoveProxy()
	}
}

// ProxyURL 返回底层 HTTP 客户端实际生效的代理地址，空字符串表示直连。
// 读的是 resty 的传输配置而非本结构体字段，代理是否真的被清除只能由它证明。
func (c *Client) ProxyURL() string {
	if proxy := c.resty.ProxyURL(); proxy != nil {
		return proxy.String()
	}
	return ""
}

func (c *Client) SetBaseUrl(baseUrl string) {
	c.baseURL = baseUrl
	baseURL := fmt.Sprintf("%s/3", c.baseURL)
	c.resty.SetBaseURL(baseURL)
}

func (c *Client) TestToken() bool {
	type testResp struct {
		Success bool `json:"success"`
	}
	respResult := testResp{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest("/authentication", req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("测试 Token 失败：%+v", err)
		return false
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("测试 Token 失败：%s", resp.String())
		return false
	}
	return respResult.Success
}

type ImageConfig struct {
	BaseURL       string `json:"base_url"`
	SecureBaseURL string `json:"secure_base_url"`
}

type Configuration struct {
	Images *ImageConfig `json:"images"`
}

// GetConfiguration 获取 TMDB 配置，包含图片基础 URL 等信息
func (c *Client) GetConfiguration() (*Configuration, error) {
	respResult := Configuration{}
	req := c.resty.R().SetMethod("GET").SetResult(&respResult)
	// req.SetQueryParam("api_key", c.apiKey)
	resp, err := c.doRequest("/configuration", req, MakeRequestConfig(2, 5, 5))
	if err != nil {
		helpers.TMDBLog.Errorf("获取配置失败：%+v", err)
		return nil, err
	}
	if !resp.IsStatusSuccess() {
		helpers.TMDBLog.Errorf("获取配置失败：%s", resp.String())
		return nil, fmt.Errorf("获取配置失败：%s", resp.String())
	}
	return &respResult, nil
}

// doRequest 执行 HTTP 请求
func (c *Client) doRequest(url string, req *resty.Request, options *RequestConfig) (*resty.Response, error) {
	if options == nil {
		options = DefaultRequestConfig()
	}
	// 设置超时时间
	req.SetTimeout(options.Timeout)
	var lastErr error
	for attempt := 0; attempt <= options.MaxRetries; attempt++ {
		resp, err := c.request(url, req)
		if err == nil {
			// 正常返回
			return resp, nil
		}
		lastErr = err
		// 如果是 Token 过期错误，等待 Token 刷新完成后重试
		if err.Error() == "token expired" {
			helpers.TMDBLog.Warn("TMDB Token 已失效")
			return nil, fmt.Errorf("TMDB Token 已失效")
		}
		// 其他错误开始重试
		if attempt < options.MaxRetries {
			// helpers.TMDBLog.Warnf("%s %s 请求失败：%+v", req.Method, req.URL, lastErr)
			helpers.TMDBLog.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试），错误：%+v", req.Method, req.URL, options.RetryDelay.Seconds(), attempt+1, lastErr)
			time.Sleep(options.RetryDelay)
		}
	}
	return nil, lastErr
}

func (c *Client) request(url string, req *resty.Request) (*resty.Response, error) {
	// 等待限流令牌
	if err := c.rateLimiter.Wait(req.Context()); err != nil {
		return nil, fmt.Errorf("等待限流令牌失败：%w", err)
	}
	req.SetHeader("Accept", "application/json")
	if c.apiKey != "" {
		req.SetQueryParam("api_key", c.apiKey)
	}
	var response *resty.Response
	var err error
	switch req.Method {
	case "GET":
		response, err = req.Get(url)
	case "POST":
		response, err = req.Post(url)
	default:
		return nil, fmt.Errorf("不支持的 HTTP 方法：%s", req.Method)
	}
	helpers.TMDBLog.Infof("%s %s 请求", req.Method, req.URL)
	if err != nil {
		return response, err
	}
	return response, nil
}
