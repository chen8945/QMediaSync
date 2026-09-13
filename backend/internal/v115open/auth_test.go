package v115open

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

// refreshStubTransport 拦截刷新请求：networkErr 非空时返回网络错误，否则返回固定 JSON 响应
type refreshStubTransport struct {
	response   string
	networkErr error
	requests   atomic.Int32
	onRequest  func(*http.Request)
}

func (t *refreshStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests.Add(1)
	if t.onRequest != nil {
		t.onRequest(req)
	}
	if t.networkErr != nil {
		return nil, t.networkErr
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.response)),
		Request:    req,
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func newRefreshTestClient(transport http.RoundTripper) *OpenClient {
	ensureOpenAPITestLoggers()
	client := NewClient(
		7,
		"test-app-id",
		"old-access-token",
		"old-refresh-token",
	)
	client.client.SetTransport(transport)
	return client
}

func withFastRefreshRetry(t *testing.T) {
	t.Helper()
	oldCount, oldDelay := refreshTokenRetryCount, refreshTokenRetryDelay
	refreshTokenRetryCount, refreshTokenRetryDelay = 5, time.Millisecond
	t.Cleanup(func() {
		refreshTokenRetryCount, refreshTokenRetryDelay = oldCount, oldDelay
	})
}

type invalidEventRecorder struct {
	mu     sync.Mutex
	events []map[string]any
}

func (r *invalidEventRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

func subscribeInvalidEventRecorder(t *testing.T) *invalidEventRecorder {
	t.Helper()
	ensureOpenAPITestLoggers()
	helpers.InitEventBus()
	rec := &invalidEventRecorder{}
	helpers.SubscribeSync(helpers.V115TokenInValidEvent, func(event helpers.Event) helpers.EventResult {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if data, ok := event.Data.(map[string]any); ok {
			rec.events = append(rec.events, data)
		}
		return helpers.EventResult{Success: true}
	})
	return rec
}

func TestIsRefreshTokenDead(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "无错误", err: nil, want: false},
		{name: "网络错误", err: fmt.Errorf(`Post "https://passportapi.115.com/open/refreshToken": dial tcp: lookup passportapi.115.com: server misbehaving`), want: false},
		{name: "刷新太频繁", err: NewOpenAPIError(REFRESH_TOO_FREQUENT, "刷新太频繁"), want: false},
		{name: "刷新失败可重试", err: NewOpenAPIError(TOKEN_REFRESH_FAIL, "刷新失败"), want: false},
		{name: "refresh_token 无效", err: NewOpenAPIError(REFRESH_TOKEN_INVALID, "no auth"), want: true},
		{name: "refresh_token 已过期", err: NewOpenAPIError(REFRESH_TOKEN_EXPIRED, "expired"), want: true},
		{name: "refresh_token 格式错误", err: NewOpenAPIError(REFRESH_TOKEN_FORMAT_INVALID, "format"), want: true},
		{name: "refresh_token 签名失败", err: NewOpenAPIError(REFRESH_TOKEN_SIGN_INVALID, "sign"), want: true},
		{name: "refresh_token 校验失败", err: NewOpenAPIError(REFRESH_TOKEN_CHECK_FAILED, "check"), want: true},
		{name: "包装后的凭证失效错误", err: fmt.Errorf("刷新失败: %w", NewOpenAPIError(REFRESH_TOKEN_INVALID, "no auth")), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRefreshTokenDead(tt.err); got != tt.want {
				t.Fatalf("IsRefreshTokenDead() = %v，期望 %v", got, tt.want)
			}
		})
	}
}

func TestRefreshTokenNetworkErrorKeepsCredentials(t *testing.T) {
	withFastRefreshRetry(t)
	rec := subscribeInvalidEventRecorder(t)
	transport := &refreshStubTransport{
		networkErr: fmt.Errorf(`Post "https://passportapi.115.com/open/refreshToken": dial tcp: lookup passportapi.115.com on 127.0.0.11:53: server misbehaving`),
	}
	client := newRefreshTestClient(transport)

	_, err := client.RefreshToken("old-refresh-token")
	if err == nil {
		t.Fatal("网络错误应返回错误")
	}
	if IsRefreshTokenDead(err) {
		t.Fatal("网络错误不应判定为刷新令牌失效")
	}
	credentials := client.credentialSnapshot()
	if credentials.accessToken != "old-access-token" || credentials.refreshToken != "old-refresh-token" {
		t.Fatal("网络错误后不应清空客户端凭据")
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("网络错误不应发布凭证失效事件，实际发布 %d 次", got)
	}
	if got := transport.requests.Load(); got != int32(refreshTokenRetryCount+1) {
		t.Fatalf("期望请求内重试共 %d 次，实际 %d 次", refreshTokenRetryCount+1, got)
	}
}

func TestRefreshTokenDeadCodeClearsCredentialsAndPublishesEvent(t *testing.T) {
	rec := subscribeInvalidEventRecorder(t)
	transport := &refreshStubTransport{response: `{"state":false,"code":40140116,"message":"no auth"}`}
	client := newRefreshTestClient(transport)

	_, err := client.RefreshToken("old-refresh-token")
	if err == nil {
		t.Fatal("凭证失效应返回错误")
	}
	if !IsRefreshTokenDead(err) {
		t.Fatal("40140116 应判定为刷新令牌失效")
	}
	credentials := client.credentialSnapshot()
	if credentials.accessToken != "" || credentials.refreshToken != "" {
		t.Fatal("凭证失效后应清空客户端凭据")
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("期望发布 1 次凭证失效事件，实际 %d 次", got)
	}
}

func TestRefreshTokenThrottledKeepsCredentialsWithoutRetry(t *testing.T) {
	withFastRefreshRetry(t)
	rec := subscribeInvalidEventRecorder(t)
	transport := &refreshStubTransport{response: `{"state":false,"code":40140117,"message":"刷新太频繁"}`}
	client := newRefreshTestClient(transport)

	_, err := client.RefreshToken("old-refresh-token")
	if err == nil {
		t.Fatal("频控应返回错误")
	}
	if IsRefreshTokenDead(err) {
		t.Fatal("频控不应判定为刷新令牌失效")
	}
	credentials := client.credentialSnapshot()
	if credentials.accessToken != "old-access-token" || credentials.refreshToken != "old-refresh-token" {
		t.Fatal("频控后不应清空客户端凭据")
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("频控不应发布凭证失效事件，实际发布 %d 次", got)
	}
	if got := transport.requests.Load(); got != 1 {
		t.Fatalf("频控错误不应请求内重试，实际请求 %d 次", got)
	}
}

func TestRefreshTokenRetryableFailureKeepsCredentials(t *testing.T) {
	withFastRefreshRetry(t)
	rec := subscribeInvalidEventRecorder(t)
	transport := &refreshStubTransport{response: `{"state":false,"code":40140121,"message":"刷新失败"}`}
	client := newRefreshTestClient(transport)

	_, err := client.RefreshToken("old-refresh-token")
	if err == nil {
		t.Fatal("可重试失败应返回错误")
	}
	if IsRefreshTokenDead(err) {
		t.Fatal("40140121 不应判定为刷新令牌失效")
	}
	credentials := client.credentialSnapshot()
	if credentials.accessToken != "old-access-token" || credentials.refreshToken != "old-refresh-token" {
		t.Fatal("可重试失败后不应清空客户端凭据")
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("可重试失败不应发布凭证失效事件，实际发布 %d 次", got)
	}
	if got := transport.requests.Load(); got != int32(refreshTokenRetryCount+1) {
		t.Fatalf("40140121 应请求内重试共 %d 次，实际 %d 次", refreshTokenRetryCount+1, got)
	}
}

// 刷新与直接设置凭据并发时，失效事件必须保留实际请求使用的整对凭据。
func TestRefreshTokenUsesRequestCredentialSnapshot(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	tests := []struct {
		name         string
		refreshToken string
	}{
		{name: "空参数使用整组快照"},
		{name: "显式参数保留请求语义", refreshToken: "explicit-refresh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := subscribeInvalidEventRecorder(t)
			sentRefreshTokens := make(chan string, 1)
			transport := &refreshStubTransport{
				response: `{"state":false,"code":40140116,"message":"no auth"}`,
				onRequest: func(req *http.Request) {
					if err := req.ParseForm(); err != nil {
						t.Error(err)
					}
					sentRefreshTokens <- req.Form.Get("refresh_token")
				},
			}
			client := newRefreshTestClient(transport)
			client.SetAuthToken("version-0", "version-0")
			start := make(chan struct{})
			var updates sync.WaitGroup
			updates.Go(func() {
				<-start
				for i := range 800 {
					token := fmt.Sprintf("version-%d", i+1)
					client.SetAuthToken(token, token)
					runtime.Gosched()
				}
			})
			defer updates.Wait()
			close(start)
			for i := range 40 {
				if _, err := client.RefreshToken(tt.refreshToken); !IsRefreshTokenDead(err) {
					t.Fatalf("期望凭据失效错误，实际为 %v", err)
				}
				if rec.count() != i+1 {
					t.Fatalf("刷新失败应发布一次凭据失效事件，实际 %d 次", rec.count())
				}
				rec.mu.Lock()
				event := rec.events[i]
				expectedToken := event["token"].(string)
				expectedRefresh := event["refresh_token"].(string)
				rec.mu.Unlock()
				if sent := <-sentRefreshTokens; expectedRefresh != sent {
					t.Fatalf("事件刷新令牌 %q 与实际请求 %q 不一致", expectedRefresh, sent)
				}
				if tt.refreshToken == "" {
					if expectedToken != expectedRefresh {
						t.Fatalf("事件拼接了不同版本的凭据：access=%q refresh=%q", expectedToken, expectedRefresh)
					}
				} else if expectedRefresh != tt.refreshToken {
					t.Fatalf("显式刷新令牌被客户端快照覆盖：%q", expectedRefresh)
				}
			}
		})
	}
}
