package tmdb

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"qmediasync/internal/helpers"
	v115open "qmediasync/internal/v115/openapi"

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
	proxyUrl    string
	rateLimiter *rate.Limiter
}

// 单个客户端不能重置整个进程的 TMDB 请求配额。
var requestLimiter = rate.NewLimiter(rate.Every(time.Second), 40)

// NewClient 创建持有固定配置和私有连接池的 TMDB 客户端。
func NewClient(apiKey, accessToken, baseUrl, language, proxyUrl string) (*Client, error) {
	transport, err := helpers.GetProxyTransport(proxyUrl)
	if err != nil {
		return nil, err
	}
	rc := resty.NewWithClient(&http.Client{Transport: transport})
	rc.SetHeader("User-Agent", v115open.DEFAULTUA)
	rc.SetBaseURL(fmt.Sprintf("%s/3", baseUrl))
	return &Client{
		resty:       rc,
		apiKey:      apiKey,
		proxyUrl:    proxyUrl,
		rateLimiter: requestLimiter,
	}, nil
}

// Close 释放本客户端持有的私有连接资源。
func (c *Client) Close() error {
	return helpers.CloseRestyClient(c.resty)
}

// ProxyURL 返回本客户端构造时捕获的代理地址。
func (c *Client) ProxyURL() string { return c.proxyUrl }

func (c *Client) TestToken(ctx context.Context) bool {
	type testResp struct {
		Success bool `json:"success"`
	}
	respResult := testResp{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
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
func (c *Client) GetConfiguration(ctx context.Context) (*Configuration, error) {
	respResult := Configuration{}
	req := c.resty.R().SetContext(ctx).SetMethod("GET").SetResult(&respResult)
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
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		// 如果是 Token 过期错误，等待 Token 刷新完成后重试
		if err.Error() == "token expired" {
			helpers.TMDBLog.Warn("TMDB Token 已失效")
			return nil, fmt.Errorf("TMDB Token 已失效")
		}
		// 其他错误开始重试
		if attempt < options.MaxRetries {
			// helpers.TMDBLog.Warnf("%s %s 请求失败：%+v", req.Method, req.URL, lastErr)
			helpers.TMDBLog.Warnf("%s %s 请求失败，%.0f 秒后重试（第 %d 次尝试），错误：%+v", req.Method, req.URL, options.RetryDelay.Seconds(), attempt+1, lastErr)
			timer := time.NewTimer(options.RetryDelay)
			select {
			case <-req.Context().Done():
				timer.Stop()
				return nil, req.Context().Err()
			case <-timer.C:
			}
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
