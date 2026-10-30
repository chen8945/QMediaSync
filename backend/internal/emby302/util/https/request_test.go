package https

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRequestWithoutContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	resp, err := Get(server.URL).DoSingle()
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("响应 = %d %q，错误 = %v", resp.StatusCode, body, err)
	}
}

func TestProxyRequestCancellation(t *testing.T) {
	for _, tt := range []struct {
		name          string
		redirect      bool
		streamingBody bool
	}{
		{name: "waiting_for_headers"},
		{name: "after_redirect", redirect: true},
		{name: "streaming_body", streamingBody: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirect" {
					http.Redirect(w, r, "/stream", http.StatusTemporaryRedirect)
					return
				}
				if tt.streamingBody {
					_, _ = io.WriteString(w, "first chunk")
					w.(http.Flusher).Flush()
				}
				close(started)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
				}
			}))
			defer server.Close()
			defer close(release)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			target := "/stream"
			if tt.redirect {
				target = "/redirect"
			}
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			responseReceived := make(chan struct{})
			result := make(chan error, 1)
			go func() {
				resp, err := ProxyRequest(req, server.URL)
				if err == nil {
					defer resp.Body.Close()
					close(responseReceived)
					_, err = io.Copy(io.Discard, resp.Body)
				}
				result <- err
			}()

			waitForRequestEvent(t, started, "回源请求开始")
			if tt.streamingBody {
				waitForRequestEvent(t, responseReceived, "响应头接收完成")
			}
			cancel()
			waitForRequestEvent(t, canceled, "回源请求取消")
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("代理取消错误 = %v，期望 context.Canceled", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("取消后代理请求没有结束")
			}
		})
	}
}

func waitForRequestEvent(t *testing.T, event <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(5 * time.Second):
		t.Fatalf("等待%s超时", description)
	}
}
