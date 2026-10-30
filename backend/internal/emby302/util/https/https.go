package https

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (

	// MaxRedirectDepth 重定向的最大深度
	MaxRedirectDepth = 10
)

var client *http.Client

// RedirectCodes 有重定向含义的 HTTP 响应码
var RedirectCodes = [4]int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect}

// ClientOptions 控制 Emby 302 出站 HTTP 客户端行为。
type ClientOptions struct {
	InsecureSkipVerify bool
}

func init() {
	ConfigureClient(ClientOptions{})
}

// ConfigureClient 配置 Emby 302 共享 HTTP 客户端。
func ConfigureClient(options ClientOptions) {
	client = newHTTPClient(options)
}

func newHTTPClient(options ClientOptions) *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   time.Minute,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
		// 接收数据 5 分钟超时
		ResponseHeaderTimeout: time.Minute * 5,
	}
	if options.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// IsRedirectCode 判断 HTTP 状态码是否表示重定向
//
// 301, 302, 307, 308
func IsRedirectCode(code int) bool {
	for _, valid := range RedirectCodes {
		if code == valid {
			return true
		}
	}
	return false
}

// IsSuccessCode 判断 HTTP 状态码是否为成功状态
func IsSuccessCode(code int) bool {
	codeStr := strconv.Itoa(code)
	return strings.HasPrefix(codeStr, "2")
}

// IsErrorCode 判断 HTTP 状态码是否为错误状态
func IsErrorCode(code int) bool {
	codeStr := strconv.Itoa(code)
	return strings.HasPrefix(codeStr, "4") || strings.HasPrefix(codeStr, "5")
}

// MapBody 将 map 转换为 ReadCloser 流
func MapBody(body map[string]any) io.ReadCloser {
	if body == nil {
		return nil
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		log.Printf("MapBody 转换失败, body: %v, 错误: %v", body, err)
		return nil
	}
	return io.NopCloser(bytes.NewBuffer(bodyBytes))
}
