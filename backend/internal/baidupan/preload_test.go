package baidupan

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type preloadTransportFunc func(*http.Request) (*http.Response, error)

func (f preloadTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPreloadDownloadURLBoundedSingleRequest(t *testing.T) {
	for _, tc := range []struct {
		name, body, location, encoding string
		status                         int
		wantOK, transient, risk, auth  bool
		code                           int
	}{
		{name: "valid", body: `{"errno":0,"list":[{"fs_id":123,"dlink":"https://d.pcs.baidu.com/file?key=a%2Bb"}]}`, wantOK: true},
		{name: "empty", body: `{"errno":0,"list":[{"fs_id":123,"dlink":""}]}`},
		{name: "relative", body: `{"errno":0,"list":[{"fs_id":123,"dlink":"/file"}]}`},
		{name: "unknown-domain", body: `{"errno":0,"list":[{"fs_id":123,"dlink":"https://media.invalid/file"}]}`},
		{name: "115-domain", body: `{"errno":0,"list":[{"fs_id":123,"dlink":"https://a.115cdn.net/file"}]}`},
		{name: "userinfo", body: `{"errno":0,"list":[{"fs_id":123,"dlink":"https://secret@d.pcs.baidu.com/file"}]}`},
		{name: "fragment", body: `{"errno":0,"list":[{"fs_id":123,"dlink":"https://d.pcs.baidu.com/file#part"}]}`},
		{name: "wrong-file", body: `{"errno":0,"list":[{"fs_id":124,"dlink":"https://d.pcs.baidu.com/file"}]}`},
		{name: "missing-file-id", body: `{"errno":0,"list":[{"dlink":"https://d.pcs.baidu.com/file"}]}`},
		{name: "zero-file-id", body: `{"errno":0,"list":[{"fs_id":0,"dlink":"https://d.pcs.baidu.com/file"}]}`},
		{name: "directory", body: `{"errno":0,"list":[{"fs_id":123,"isdir":1,"dlink":"https://d.pcs.baidu.com/file"}]}`},
		{name: "missing-state", body: `{"list":[{"dlink":"https://d.pcs.baidu.com/file"}]}`},
		{name: "invalid-tail", body: `{"errno":0,"list":[{"dlink":"https://d.pcs.baidu.com/file"}]}garbage`},
		{name: "empty-list", body: `{"errno":0,"list":[]}`},
		{name: "html", body: `<html>do-not-log-secret</html>`},
		{name: "gzip", body: `{"errno":0,"list":[{"dlink":"https://d.pcs.baidu.com/file"}]}`, encoding: "gzip"},
		{name: "large", body: `{"errno":0,"padding":"` + strings.Repeat("x", 65536) + `","list":[{"dlink":"https://d.pcs.baidu.com/file"}]}`},
		{name: "redirect", status: 302, location: "https://d.pcs.baidu.com/media", body: `{}`},
		{name: "temporary", status: 503, body: `{}`, transient: true},
		{name: "unknown-business-first", status: 503, body: `{"errno":991122}`, code: 991122},
		{name: "risk", status: 429, body: `{}`, risk: true},
		{name: "risk-business", body: `{"errno":31034}`, risk: true, code: 31034},
		{name: "auth", body: `{"errno":-6}`, auth: true, code: -6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := NewBaiDuPanClientWithToken("token&private")
			originalConfig := client.client.GetConfig()
			originalConfig.Debug = true
			originalConfig.HTTPClient = &http.Client{Transport: preloadTransportFunc(func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/rest/2.0/xpan/multimedia" ||
					r.URL.Query().Get("method") != "filemetas" || r.URL.Query().Get("fsids") != "[123]" ||
					r.URL.Query().Get("dlink") != "1" || r.URL.Query().Get("access_token") != "token&private" ||
					r.UserAgent() != "OpenAPI-Generator/1.0.0/go" || r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Cookie") != "" {
					t.Error("后台未沿用安全文件元数据请求")
				}
				status := tc.status
				if status == 0 {
					status = 200
				}
				return &http.Response{StatusCode: status, Header: http.Header{
					"Content-Type": {"application/json"}, "Location": {tc.location}, "Content-Encoding": {tc.encoding}, "Retry-After": {"1800"},
				}, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			})}
			result, err := client.GetPreloadDownloadURL(t.Context(), "123")
			if tc.wantOK {
				parsed, parseErr := url.Parse(result)
				if err != nil || parseErr != nil || parsed.Query().Get("access_token") != "token&private" || !strings.Contains(result, "key=a%2Bb") {
					t.Fatalf("有效签名未完整保留：%v", err)
				}
			} else {
				failure, ok := errors.AsType[*PreloadDownloadError](err)
				if !ok || result != "" || failure.Code != tc.code || failure.Transient != tc.transient || failure.Risk != tc.risk || failure.Unauthorized != tc.auth || failure.RetryAfter != 30*time.Minute {
					t.Fatalf("分类不符：%+v", failure)
				}
				if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "http") {
					t.Fatal("错误泄漏了外部文字")
				}
			}
			if calls.Load() != 1 || !originalConfig.Debug || originalConfig.HTTPClient.CheckRedirect != nil || originalConfig.DefaultHeader["Accept-Encoding"] != "" {
				t.Fatal("出现隐式重试、跟随跳转或共享配置修改")
			}
		})
	}
}

func TestPreloadDownloadURLNetworkAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		client := NewBaiDuPanClientWithToken("private")
		client.client.GetConfig().HTTPClient = &http.Client{Transport: preloadTransportFunc(func(r *http.Request) (*http.Response, error) {
			if canceled {
				return nil, context.Canceled
			}
			return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("temporary-private")}
		})}
		ctx, cancel := context.WithCancel(t.Context())
		if canceled {
			cancel()
		}
		_, err := client.GetPreloadDownloadURL(ctx, "123")
		cancel()
		if canceled {
			if !errors.Is(err, context.Canceled) {
				t.Fatal("未保留取消")
			}
		} else if failure, ok := errors.AsType[*PreloadDownloadError](err); !ok || !failure.Transient {
			t.Fatal("未分类连接错误")
		}
	}
}
