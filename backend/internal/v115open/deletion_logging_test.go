package v115open

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
)

func TestDeletionFailureDiagnostics(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name, body           string
		status               int
		requestErr, guardErr error
		want                 []string
	}{
		{name: "deletion busy", status: 200, body: `{"state":false,"code":990019,"message":"private-response-secret"}`, want: []string{"code=990019", "类别=上一项删除尚未完成"}},
		{name: "business", status: 200, body: `{"state":false,"code":123456,"errno":7,"message":"private-response-secret"}`, want: []string{"HTTP=200", "code=123456", "errno=7", "类别=业务拒绝"}},
		{name: "rate limit", status: 429, body: `{"state":false,"errno":590075}`, want: []string{"HTTP=429", "errno=590075", "类别=限流"}},
		{name: "malformed", status: 200, body: `private-response-secret`, want: []string{"HTTP=200", "响应可解析=false", "类别=响应解析失败"}},
		{name: "transport", requestErr: &url.Error{Op: "Post", URL: "https://example.test/private-response-secret", Err: errors.New("private-response-secret")}, want: []string{"HTTP=0", "类别=网络错误"}},
		{name: "guard", guardErr: errors.New("private-response-secret"), want: []string{"HTTP=0", "类别=请求失败或发送前拒绝"}},
		{name: "server error", status: 503, body: `{"state":false}`, want: []string{"HTTP=503", "类别=HTTP 错误"}},
		{name: "timeout", requestErr: &url.Error{Op: "Post", URL: "https://example.test/private-response-secret", Err: context.DeadlineExceeded}, want: []string{"HTTP=0", "类别=超时"}},
		{name: "canceled", guardErr: context.Canceled, want: []string{"HTTP=0", "类别=已取消"}},
		{name: "success", status: 200, body: `{"state":true}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := helpers.V115Log
			helpers.V115Log = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			t.Cleanup(func() { helpers.V115Log = previous })
			client := NewClient(1, "app", "token", "refresh")
			calls := 0
			client.client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if tt.requestErr != nil {
					return nil, tt.requestErr
				}
				return playbackTestResponse(req, tt.status, tt.body), nil
			}))
			ok, err := client.DelOnceGuarded(t.Context(), []string{"9001"}, "7001", func() error { return tt.guardErr })
			wantCalls := 1
			if tt.guardErr != nil {
				wantCalls = 0
			}
			if calls != wantCalls || ok != (tt.name == "success") || (err == nil) != ok {
				t.Fatalf("calls=%d ok=%v err=%v", calls, ok, err)
			}
			var diagnostic string
			for line := range strings.SplitSeq(logs.String(), "\n") {
				if strings.Contains(line, "115 删除诊断") {
					diagnostic += line
				}
			}
			if ok {
				if diagnostic != "" {
					t.Fatal("success emitted failure diagnostic")
				}
				return
			}
			for _, want := range append(tt.want, `file_ids="9001"`, `parent_id="7001"`, "账号=1", "耗时_ms=") {
				if !strings.Contains(diagnostic, want) {
					t.Errorf("missing %q in %s", want, diagnostic)
				}
			}
			if strings.Contains(diagnostic, "private-response-secret") || strings.Contains(diagnostic, "https://") {
				t.Fatal("diagnostic exposed upstream text")
			}
		})
	}
}
