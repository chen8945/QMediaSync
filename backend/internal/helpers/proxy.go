package helpers

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"qmediasync/internal/validation"
)

// proxyTransportField 传输层复用 validation 规则时的字段名，直接面向用户，用中文而非请求字段名。
const proxyTransportField = "代理地址"

// proxyTestBodyLimit 代理测试只关心状态码，读掉少量响应体即可让连接回到连接池。
const proxyTestBodyLimit = 32 * 1024

// checkProxyScheme 校验代理协议是否受支持，白名单和文案都取自 validation 包，保持与请求校验层一致。
func checkProxyScheme(scheme string) error {
	if validation.ProxySchemeSupported(scheme) {
		return nil
	}
	return fmt.Errorf("不支持的代理协议：%s，%s", scheme, validation.ProxySchemeHint)
}

// TestHttpProxyWithContext 测试代理连接，探测请求跟随 ctx 取消。
func TestHttpProxyWithContext(ctx context.Context, proxyURL string) (bool, error) {
	if proxyURL == "" {
		return false, fmt.Errorf("代理 URL 不能为空")
	}

	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		return false, validation.ProxyParseError(err)
	}

	// 先单独查协议：validation.ProxyURL 的协议错误只给白名单，这里的文案会带上用户填的协议名
	if err := checkProxyScheme(parsedProxy.Scheme); err != nil {
		return false, err
	}

	// 复用生产路径的传输构造：协议白名单、地址格式和端口范围与请求校验层同源
	transport, err := createProxyTransport(proxyURL)
	if err != nil {
		return false, err
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	defer transport.CloseIdleConnections()

	// 日志和回显都使用脱敏地址：url.URL.Redacted() 只遮蔽密码，用户名仍是明文
	redactedProxy := validation.RedactParsedProxyURL(parsedProxy)

	testURLs := []string{
		"https://api.github.com",  // GitHub API，稳定可靠
		"https://www.google.com",  // Google 首页
		"http://www.baidu.com",    // 百度首页，国内访问
		"https://httpstat.us/200", // HTTP 状态测试服务
	}

	var lastError error

	for _, testURL := range testURLs {
		// 调用方已取消时不再往下探测，否则每个 URL 都要再等一次超时
		if err := ctx.Err(); err != nil {
			if lastError == nil {
				lastError = err
			}
			break
		}

		AppLogger.Infof("使用代理 %s 测试连接到 %s", redactedProxy, testURL)

		req, err := http.NewRequestWithContext(ctx, "GET", testURL, nil)
		if err != nil {
			lastError = fmt.Errorf("创建请求失败：%v", err)
			continue
		}

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

		statusCode, status := resp.StatusCode, resp.Status
		// 就地读掉并关闭 body：defer 会把每个响应体持有到函数返回；
		// 读到 EOF 才能让连接回到连接池，供后续探测复用，函数返回时统一 CloseIdleConnections 释放
		drainAndCloseProxyTestBody(resp)

		if statusCode >= 200 && statusCode < 400 {
			AppLogger.Infof("代理连接测试成功 [%s]：HTTP %d", testURL, statusCode)
			return true, nil
		}
		lastError = fmt.Errorf("HTTP 响应异常 [%s]：%d %s", testURL, statusCode, status)
		AppLogger.Warnf("代理测试响应异常 [%s]：%d", testURL, statusCode)
	}

	if lastError != nil {
		return false, fmt.Errorf("代理连接测试失败：%v", lastError)
	}

	return false, fmt.Errorf("代理连接测试失败：所有测试 URL 都无法访问")
}

// TestHttpProxyAdvancedWithContext 高级代理测试，探测请求跟随 ctx 取消。
//
// 首个 URL 成功即停止，不再跑完全部列表：单个 URL 超时 30 秒，跑满最坏要等两分半。
// 因此 SuccessCount 与 TotalCount 描述的是"实际探测过的 URL"，不是列表长度；
// TestResults 的长度与 TotalCount 一致，可直接用于展示。
func TestHttpProxyAdvancedWithContext(ctx context.Context, proxyURL string) (*ProxyTestResult, error) {
	result := &ProxyTestResult{
		TestTime:    time.Now(),
		TestResults: make([]TestURLResult, 0),
	}

	if proxyURL == "" {
		result.Success = false
		result.ErrorMessage = "代理 URL 不能为空"
		return result, fmt.Errorf("代理 URL 不能为空")
	}

	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		parseErr := validation.ProxyParseError(err)
		result.Success = false
		result.ErrorMessage = parseErr.Error()
		return result, parseErr
	}

	// ProxyURL 会作为 JSON 回传接口，必须遮蔽用户名和密码；
	// url.URL.Host 不含 userinfo，可直接回显
	result.ProxyURL = validation.RedactParsedProxyURL(parsedProxy)
	result.ProxyScheme = parsedProxy.Scheme
	result.ProxyHost = parsedProxy.Host

	// 先单独查协议：validation.ProxyURL 的协议错误只给白名单，这里的文案会带上用户填的协议名
	if err := checkProxyScheme(parsedProxy.Scheme); err != nil {
		result.Success = false
		result.ErrorMessage = err.Error()
		return result, err
	}

	// 复用生产路径的传输构造：协议白名单、地址格式和端口范围与请求校验层同源
	transport, err := createProxyTransport(proxyURL)
	if err != nil {
		result.Success = false
		result.ErrorMessage = err.Error()
		return result, err
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	defer transport.CloseIdleConnections()

	testURLs := []TestURL{
		{URL: "http://httpbin.org/ip", Description: "IP 检测服务"},
		{URL: "https://api.github.com", Description: "GitHub API"},
		{URL: "https://www.google.com", Description: "Google 首页"},
		{URL: "http://www.baidu.com", Description: "百度首页"},
		{URL: "https://httpstat.us/200", Description: "HTTP 状态测试"},
	}

	successCount := 0

	for _, testURL := range testURLs {
		// 调用方已取消时停止探测，剩余 URL 不再计入统计
		if ctx.Err() != nil {
			break
		}

		testResult := TestURLResult{
			URL:         testURL.URL,
			Description: testURL.Description,
			StartTime:   time.Now(),
		}

		req, err := http.NewRequestWithContext(ctx, "GET", testURL.URL, nil)
		if err != nil {
			testResult.Success = false
			testResult.ErrorMessage = fmt.Sprintf("创建请求失败：%v", err)
			testResult.Duration = time.Since(testResult.StartTime)
			result.TestResults = append(result.TestResults, testResult)
			continue
		}

		req.Header.Set("User-Agent", "qmediasync/1.0 (Proxy Test)")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

		resp, err := client.Do(req)
		testResult.Duration = time.Since(testResult.StartTime)

		if err != nil {
			testResult.Success = false
			testResult.ErrorMessage = fmt.Sprintf("请求失败：%v", err)
		} else {
			testResult.StatusCode = resp.StatusCode
			testResult.StatusText = resp.Status
			// 就地读掉并关闭 body，避免 defer 把每个响应体持有到函数返回
			drainAndCloseProxyTestBody(resp)

			if testResult.StatusCode >= 200 && testResult.StatusCode < 400 {
				testResult.Success = true
				successCount++
			} else {
				testResult.Success = false
				testResult.ErrorMessage = fmt.Sprintf("HTTP 响应异常：%d %s", testResult.StatusCode, testResult.StatusText)
			}
		}

		result.TestResults = append(result.TestResults, testResult)

		// 一个成功就足以判定代理可用，短路避免把剩余 URL 的超时叠加进来
		if testResult.Success {
			break
		}
	}

	// 统计只覆盖实际探测过的 URL，短路和取消都会让它小于列表长度
	result.SuccessCount = successCount
	result.TotalCount = len(result.TestResults)

	if successCount > 0 {
		result.Success = true
	} else {
		result.Success = false
		if ctx.Err() != nil {
			result.ErrorMessage = "代理测试被取消"
			return result, ctx.Err()
		}
		result.ErrorMessage = "所有测试 URL 都无法通过代理访问"
	}

	return result, nil
}

// drainAndCloseProxyTestBody 读掉少量响应体后关闭，让底层连接能回到连接池被后续探测复用。
func drainAndCloseProxyTestBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, proxyTestBodyLimit))
	_ = resp.Body.Close()
}

// createProxyTransport 构造出站传输：空地址返回直连传输，非空地址先过 validation.ProxyURL 的完整校验。
func createProxyTransport(proxyURL string) (*http.Transport, error) {
	if proxyURL == "" {
		return &http.Transport{
			TLSHandshakeTimeout:   30 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   10,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
		}, nil
	}

	// 复用请求校验层的完整规则：协议白名单、地址格式和端口范围。
	// 只查 scheme 会放行 socks5://h:99999 这类端口越界地址，出站时才失败。
	if err := validation.ProxyURL(proxyTransportField, proxyURL, false); err != nil {
		return nil, err
	}

	parsedProxy, err := url.Parse(proxyURL)
	if err != nil {
		return nil, validation.ProxyParseError(err)
	}

	transport := &http.Transport{
		Proxy:               http.ProxyURL(parsedProxy),
		TLSHandshakeTimeout: 60 * time.Second,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: false,
		},
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
		DialContext: (&net.Dialer{
			Timeout:   60 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
	}

	return transport, nil
}

// GetProxyTransport 获取代理传输配置。
//
// 代理地址非空但无效时必须返回错误，调用方不得回退到默认传输：裸 Transport 的 Proxy 为 nil
// 又不走 ProxyFromEnvironment，等于让配了代理的用户静默直连出网，对靠代理规避 ISP 或地域暴露的用户是隐私问题。
func GetProxyTransport(proxyURL string) (*http.Transport, error) {
	transport, err := createProxyTransport(proxyURL)
	if err != nil {
		AppLogger.Errorf("创建代理传输失败，已阻止回退直连（代理：%s）：%v", validation.RedactProxyURL(proxyURL), err)
		return nil, err
	}
	return transport, nil
}

// ProxyTestResult 代理测试结果。
// ProxyURL 已脱敏；SuccessCount 和 TotalCount 只统计实际探测过的 URL，
// 首个成功即短路，所以 TotalCount 通常小于内置测试列表长度。
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
