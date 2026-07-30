package helpers

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"qmediasync/internal/validation"
)

// checkProxyScheme 校验代理协议是否受支持，白名单和文案都取自 validation 包，保持与请求校验层一致。
func checkProxyScheme(scheme string) error {
	if validation.ProxySchemeSupported(scheme) {
		return nil
	}
	return fmt.Errorf("不支持的代理协议：%s，%s", scheme, validation.ProxySchemeHint)
}

// proxyParseError 剥掉 url.Error 中回显的原始地址。
// url.Error.Error() 会打印 parse "<原串>"，代理地址常带用户名密码，直接外抛会把凭据写进日志和接口响应。
func proxyParseError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("代理 URL 格式无效：%v", urlErr.Err)
	}
	return fmt.Errorf("代理 URL 格式无效：%v", err)
}

// TestHttpProxy 测试代理连接
func TestHttpProxy(proxyURL string) (bool, error) {
	if proxyURL == "" {
		return false, fmt.Errorf("代理 URL 不能为空")
	}

	// 验证代理 URL 格式
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		return false, proxyParseError(err)
	}

	// 检查协议
	if err := checkProxyScheme(parsedProxy.Scheme); err != nil {
		return false, err
	}

	// 创建 HTTP 客户端，使用配置的代理
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(parsedProxy),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
		Timeout: 30 * time.Second,
	}

	// 日志和回显都使用脱敏地址，避免代理 URL 里的用户名密码明文落盘
	redactedProxy := parsedProxy.Redacted()

	// 测试 URL 列表，按优先级排序
	testURLs := []string{
		"https://api.github.com",  // GitHub API，稳定可靠
		"https://www.google.com",  // Google 首页
		"http://www.baidu.com",    // 百度首页，国内访问
		"https://httpstat.us/200", // HTTP 状态测试服务
	}

	var lastError error

	for _, testURL := range testURLs {
		AppLogger.Infof("使用代理 %s 测试连接到 %s", redactedProxy, testURL)

		req, err := http.NewRequest("GET", testURL, nil)
		if err != nil {
			lastError = fmt.Errorf("创建请求失败：%v", err)
			continue
		}

		// 设置请求头，模拟正常浏览器请求
		req.Header.Set("User-Agent", "qmediasync/1.0 (Proxy Test)")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("Accept-Language", "en-US,en;q=0.5")
		req.Header.Set("Accept-Encoding", "gzip, deflate")
		req.Header.Set("Connection", "keep-alive")

		resp, err := client.Do(req)
		if err != nil {
			lastError = fmt.Errorf("请求失败 [%s]：%v", testURL, err)
			AppLogger.Warnf("代理测试失败 [%s]：%v", testURL, err)
			continue
		}
		defer resp.Body.Close()

		// 检查响应状态
		if resp.StatusCode >= 200 && resp.StatusCode < 400 {
			AppLogger.Infof("代理连接测试成功 [%s]：HTTP %d", testURL, resp.StatusCode)
			return true, nil
		} else {
			lastError = fmt.Errorf("HTTP 响应异常 [%s]：%d %s", testURL, resp.StatusCode, resp.Status)
			AppLogger.Warnf("代理测试响应异常 [%s]：%d", testURL, resp.StatusCode)
		}
	}

	// 所有测试 URL 都失败了
	if lastError != nil {
		return false, fmt.Errorf("代理连接测试失败：%v", lastError)
	}

	return false, fmt.Errorf("代理连接测试失败：所有测试 URL 都无法访问")
}

// TestHttpProxyAdvanced 高级代理测试，返回更详细的信息
func TestHttpProxyAdvanced(proxyURL string) (*ProxyTestResult, error) {
	result := &ProxyTestResult{
		TestTime:    time.Now(),
		TestResults: make([]TestURLResult, 0),
	}

	if proxyURL == "" {
		result.Success = false
		result.ErrorMessage = "代理 URL 不能为空"
		return result, fmt.Errorf("代理 URL 不能为空")
	}

	// 验证代理 URL 格式
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		parseErr := proxyParseError(err)
		result.Success = false
		result.ErrorMessage = parseErr.Error()
		return result, parseErr
	}

	// 回显脱敏后的地址，Host 本身不含 userinfo，可直接使用
	result.ProxyURL = parsedProxy.Redacted()
	result.ProxyScheme = parsedProxy.Scheme
	result.ProxyHost = parsedProxy.Host

	// 检查协议
	if err := checkProxyScheme(parsedProxy.Scheme); err != nil {
		result.Success = false
		result.ErrorMessage = err.Error()
		return result, err
	}

	// 创建 HTTP 客户端，使用配置的代理
	client := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsedProxy),
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
		Timeout: 30 * time.Second,
	}

	// 测试 URL 列表
	testURLs := []TestURL{
		{URL: "http://httpbin.org/ip", Description: "IP 检测服务"},
		{URL: "https://api.github.com", Description: "GitHub API"},
		{URL: "https://www.google.com", Description: "Google 首页"},
		{URL: "http://www.baidu.com", Description: "百度首页"},
		{URL: "https://httpstat.us/200", Description: "HTTP 状态测试"},
	}

	successCount := 0

	for _, testURL := range testURLs {
		testResult := TestURLResult{
			URL:         testURL.URL,
			Description: testURL.Description,
			StartTime:   time.Now(),
		}

		req, err := http.NewRequest("GET", testURL.URL, nil)
		if err != nil {
			testResult.Success = false
			testResult.ErrorMessage = fmt.Sprintf("创建请求失败：%v", err)
			testResult.Duration = time.Since(testResult.StartTime)
			result.TestResults = append(result.TestResults, testResult)
			continue
		}

		// 设置请求头
		req.Header.Set("User-Agent", "qmediasync/1.0 (Proxy Test)")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err := client.Do(req)
		testResult.Duration = time.Since(testResult.StartTime)

		if err != nil {
			testResult.Success = false
			testResult.ErrorMessage = fmt.Sprintf("请求失败：%v", err)
		} else {
			defer resp.Body.Close()
			testResult.StatusCode = resp.StatusCode
			testResult.StatusText = resp.Status

			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				testResult.Success = true
				successCount++
			} else {
				testResult.Success = false
				testResult.ErrorMessage = fmt.Sprintf("HTTP 响应异常：%d %s", resp.StatusCode, resp.Status)
			}
		}

		result.TestResults = append(result.TestResults, testResult)
	}

	// 如果至少有一个测试成功，则认为代理可用
	if successCount > 0 {
		result.Success = true
		result.SuccessCount = successCount
		result.TotalCount = len(testURLs)
	} else {
		result.Success = false
		result.ErrorMessage = "所有测试 URL 都无法通过代理访问"
	}

	return result, nil
}

// createProxyTransport 创建代理传输
func createProxyTransport(proxyURL string) (*http.Transport, error) {
	if proxyURL == "" {
		return &http.Transport{
			// 默认传输配置
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			// 自定义 Dialer
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		}, nil
	}

	// 解析代理 URL
	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		return nil, proxyParseError(err)
	}

	// 检查代理协议
	if err := checkProxyScheme(parsedProxy.Scheme); err != nil {
		return nil, err
	}

	// 创建代理传输配置
	transport := &http.Transport{
		Proxy: http.ProxyURL(parsedProxy),
		// TLS 设置
		TLSHandshakeTimeout: 60 * time.Second, // 增加 TLS 握手超时
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false, // 保持证书验证
		},
		// HTTP 设置
		ResponseHeaderTimeout: 60 * time.Second, // 增加响应头超时
		ExpectContinueTimeout: 5 * time.Second,  // 增加 ExpectContinue 超时
		// 连接池设置
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		// 启用 HTTP/2
		ForceAttemptHTTP2: true,
		// 自定义 Dialer，以支持更好的网络控制
		DialContext: (&net.Dialer{
			Timeout:   60 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	return transport, nil
}

// GetProxyTransport 获取代理传输配置的便捷函数
func GetProxyTransport(proxyURL string) *http.Transport {
	transport, err := createProxyTransport(proxyURL)
	if err != nil {
		AppLogger.Warnf("创建代理传输失败：%v", err)
		return &http.Transport{} // 返回默认传输
	}
	return transport
}

// ProxyTestResult 代理测试结果
type ProxyTestResult struct {
	ProxyURL     string          `json:"proxy_url"`
	ProxyScheme  string          `json:"proxy_scheme"`
	ProxyHost    string          `json:"proxy_host"`
	Success      bool            `json:"success"`
	SuccessCount int             `json:"success_count"`
	TotalCount   int             `json:"total_count"`
	ErrorMessage string          `json:"error_message,omitempty"`
	TestTime     time.Time       `json:"test_time"`
	TestResults  []TestURLResult `json:"test_results"`
}

// TestURL 测试 URL
type TestURL struct {
	URL         string `json:"url"`
	Description string `json:"description"`
}

// TestURLResult 单个 URL 测试结果
type TestURLResult struct {
	URL          string        `json:"url"`
	Description  string        `json:"description"`
	Success      bool          `json:"success"`
	StatusCode   int           `json:"status_code,omitempty"`
	StatusText   string        `json:"status_text,omitempty"`
	ErrorMessage string        `json:"error_message,omitempty"`
	StartTime    time.Time     `json:"start_time"`
	Duration     time.Duration `json:"duration"`
}
