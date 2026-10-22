package controllers

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
)

func TestProxy115只允许115和百度网盘下载域名(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		allowed bool
		host    string
	}{
		{
			name:    "允许 115 CDN 域名",
			target:  "https://cdnfhnfile.115cdn.net/example/video.mp4",
			allowed: true,
			host:    "cdnfhnfile.115cdn.net",
		},
		{
			name:    "允许 115 CDN 子域名",
			target:  "https://sub.cdnfhnfile.115cdn.net/example/video.mp4",
			allowed: true,
			host:    "sub.cdnfhnfile.115cdn.net",
		},
		{
			name:    "允许百度网盘 PCS 下载域名",
			target:  "https://d.pcs.baidu.com/file/example",
			allowed: true,
			host:    "d.pcs.baidu.com",
		},
		{
			name:    "允许百度网盘 PCS CDN 域名",
			target:  "https://thumbnail0.baidupcs.com/thumbnail/example",
			allowed: true,
			host:    "thumbnail0.baidupcs.com",
		},
		{
			name:    "拒绝伪造 115 后缀域名",
			target:  "https://evil115cdn.net/example",
			allowed: false,
			host:    "evil115cdn.net",
		},
		{
			name:    "拒绝非网盘域名",
			target:  "https://example.com/video.mp4",
			allowed: false,
			host:    "example.com",
		},
		{
			name:    "拒绝本机地址",
			target:  "http://127.0.0.1:12333/api/version",
			allowed: false,
			host:    "127.0.0.1",
		},
		{
			name:    "拒绝非 HTTP 协议",
			target:  "file:///etc/passwd",
			allowed: false,
		},
		{
			name:    "拒绝非法 URL",
			target:  "://bad-url",
			allowed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAllowed, gotHost := validateProxy115Target(tc.target)
			if gotAllowed != tc.allowed {
				t.Fatalf("validateProxy115Target(%q) allowed = %v, want %v", tc.target, gotAllowed, tc.allowed)
			}
			if gotHost != tc.host {
				t.Fatalf("validateProxy115Target(%q) host = %q, want %q", tc.target, gotHost, tc.host)
			}
		})
	}
}

func TestProxy115拒绝重定向到非网盘域名(t *testing.T) {
	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}

	originalTransport := http.DefaultTransport
	defer func() {
		http.DefaultTransport = originalTransport
	}()

	requestedURLs := make([]string, 0)
	http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestedURLs = append(requestedURLs, req.URL.String())
		switch req.URL.Hostname() {
		case "d.pcs.baidu.com":
			return proxy115TestResponse(req, http.StatusFound, "", http.Header{
				"Location": []string{"http://127.0.0.1/private"},
			}), nil
		case "127.0.0.1":
			return proxy115TestResponse(req, http.StatusOK, "leaked", nil), nil
		default:
			t.Fatalf("收到未预期的请求地址：%s", req.URL.String())
			return nil, nil
		}
	})

	r := gin.New()
	r.GET("/proxy-115", Proxy115)

	target := "https://d.pcs.baidu.com/file/example"
	req := httptest.NewRequest(http.MethodGet, "/proxy-115?url="+url.QueryEscape(target), nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("HTTP = %d, want %d, body=%s", w.Code, http.StatusBadGateway, w.Body.String())
	}
	if len(requestedURLs) != 1 {
		t.Fatalf("请求次数 = %d, want 1, urls=%v", len(requestedURLs), requestedURLs)
	}
}

func TestProxy115DoesNotForwardBrowserCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = originalLogger })

	for _, tc := range []struct {
		name         string
		target       string
		redirectHost string
		baiduPan     string
		ua           string
	}{
		{
			name:         "115",
			target:       "https://cdn.115cdn.net/start",
			redirectHost: "other.115cdn.net",
			ua:           v115open.DEFAULTUA,
		},
		{
			name:         "Baidu",
			target:       "https://d.pcs.baidu.com/start",
			redirectHost: "download.baidupcs.com",
			baiduPan:     "1",
			ua:           "pan.baidu.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			originalTransport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = originalTransport })
			requests := 0
			http.DefaultTransport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
				requests++
				if cookies := req.Header.Values("Cookie"); len(cookies) != 0 {
					t.Errorf("hop %d forwarded browser Cookie", requests)
				}
				for header, want := range map[string]string{
					"Range":      "bytes=2-4",
					"Referer":    "https://qms.example/library",
					"User-Agent": tc.ua,
				} {
					if got := req.Header.Get(header); got != want {
						t.Errorf("hop %d %s = %q, want %q", requests, header, got, want)
					}
				}
				switch req.URL.Path {
				case "/start":
					return proxy115TestResponse(req, http.StatusFound, "", http.Header{
						"Location": []string{"/same-host"},
					}), nil
				case "/same-host":
					return proxy115TestResponse(req, http.StatusTemporaryRedirect, "", http.Header{
						"Location": []string{"https://" + tc.redirectHost + "/file"},
					}), nil
				default:
					return proxy115TestResponse(req, http.StatusPartialContent, "abc", http.Header{
						"Content-Range": []string{"bytes 2-4/5"},
					}), nil
				}
			})

			router := gin.New()
			router.GET("/proxy-115", Proxy115)
			query := url.Values{"url": {tc.target}, "baidupan": {tc.baiduPan}}
			req := httptest.NewRequest(http.MethodGet, "/proxy-115?"+query.Encode(), nil)
			req.Header.Add("Cookie", "auth_token=browser-auth; csrf_token=browser-csrf")
			req.Header.Add("Cookie", "other_session=browser-session")
			req.Header.Set("Range", "bytes=2-4")
			req.Header.Set("Referer", "https://qms.example/library")
			req.Header.Set("User-Agent", "Browser/1.0")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if requests != 3 {
				t.Errorf("upstream requests = %d, want 3", requests)
			}
			if w.Code != http.StatusPartialContent || w.Body.String() != "abc" {
				t.Fatalf("HTTP = %d, body = %q; want 206, abc", w.Code, w.Body.String())
			}
			if got := w.Header().Get("Content-Range"); got != "bytes 2-4/5" {
				t.Errorf("Content-Range = %q, want bytes 2-4/5", got)
			}
		})
	}
}

func TestProxy115PreservesHEADAndRequestContext(t *testing.T) {
	previousTransport, previousLogger := http.DefaultTransport, helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { http.DefaultTransport, helpers.AppLogger = previousTransport, previousLogger })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var upstreamRequest *http.Request
	http.DefaultTransport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		upstreamRequest = request
		if request.Method != http.MethodHead {
			t.Fatalf("HEAD probe became %s video download", request.Method)
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("Range") != "bytes=0-1" {
			t.Fatalf("unexpected upstream headers: %v", request.Header)
		}
		return proxy115TestResponse(request, http.StatusOK, "must not be read", http.Header{"Content-Length": {"1024"}}), nil
	})
	router := gin.New()
	router.HEAD("/proxy-115", Proxy115)
	request := httptest.NewRequest(http.MethodHead, "/proxy-115?url=https%3A%2F%2Fcdn.115cdn.net%2Ffile", nil).WithContext(ctx)
	request.Header.Set("Cookie", "auth_token=private")
	request.Header.Set("Range", "bytes=0-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if upstreamRequest == nil || response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Length") != "1024" {
		t.Fatalf("HEAD response: %d %v %q", response.Code, response.Header(), response.Body)
	}
	cancel()
	if upstreamRequest.Context().Err() == nil {
		t.Fatal("upstream request lost caller cancellation")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func proxy115TestResponse(req *http.Request, statusCode int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
