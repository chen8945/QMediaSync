package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/requests"
	v115open "qmediasync/internal/v115/openapi"
	"qmediasync/internal/validation"
)

func Proxy115(c *gin.Context) {
	var proxyReq requests.Proxy115Request
	if err := c.ShouldBindQuery(&proxyReq); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if proxyReq.URL == "" {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "缺少 URL 参数", FieldErrors: []httpapi.FieldError{{Field: "url", Message: "缺少 URL 参数"}}})
		return
	}
	if err := proxyReq.Validate(); err != nil {
		httpapi.Failure(c, http.StatusForbidden, httpapi.Error{Code: ErrorCodeForbidden, Message: "只允许反代 115 或百度网盘下载链接", Cause: err})
		return
	}
	target := proxyReq.URL
	targetURL, _ := url.Parse(target) // Validate 已确保下载 URL 合法。
	helpers.AppLogger.Infof("反代网盘下载链接：host=%s", targetURL.Hostname())
	// 创建请求
	method := http.MethodGet
	if c.Request.Method == http.MethodHead {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(c.Request.Context(), method, target, nil)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求创建失败", Cause: err})
		return
	}
	// 仅转发 Range、Referer，避免将浏览器 Cookie 泄露给网盘。
	for k, v := range c.Request.Header {
		if k == "Range" || k == "Referer" {
			// helpers.AppLogger.Infof("响应头：%s=%s", k, v)
			req.Header[k] = v
		}
	}
	if proxyReq.BaiduPan != "" {
		req.Header.Set("User-Agent", "pan.baidu.com")
	} else {
		req.Header.Set("User-Agent", v115open.DEFAULTUA)
	}
	// 发起请求，重定向后的 URL 仍需保持在网盘下载域名白名单内。
	client := &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: validateProxy115Redirect,
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		status, code := http.StatusBadGateway, "DOWNLOAD_PROXY_FAILED"
		if !errors.Is(err, context.Canceled) {
			// Do 返回的 url.Error 也实现 net.Error；先解开它，避免遮住底层超时。
			if timeout, ok := errors.AsType[net.Error](errors.Unwrap(err)); errors.Is(err, context.DeadlineExceeded) || ok && timeout.Timeout() {
				status, code = http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT"
			}
		}
		httpapi.Failure(c, status, httpapi.Error{Code: code, Message: "反代请求失败", Cause: err})
		return
	}
	defer resp.Body.Close()
	// 复制响应头
	for k, v := range resp.Header {
		for _, vv := range v {
			c.Header(k, vv)
		}
	}
	c.Status(resp.StatusCode)
	if method == http.MethodHead {
		return
	}
	// 复制响应内容
	_, _ = io.Copy(c.Writer, resp.Body)
}

func validateProxy115Redirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("重定向次数过多")
	}
	if err := validation.DownloadProxyURL("url", req.URL.String()); err != nil {
		return fmt.Errorf("拒绝重定向到非 115/百度网盘下载链接：%w", err)
	}
	return nil
}

func validateProxy115Target(target string) (bool, string) {
	parsedURL, err := url.Parse(target)
	if err != nil || parsedURL.Host == "" {
		return false, ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsedURL.Hostname(), "."))
	if err := validation.DownloadProxyURL("url", target); err != nil {
		return false, host
	}
	return true, host
}
