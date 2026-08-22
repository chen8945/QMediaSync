package v115open

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

// refreshStubTransport 拦截刷新请求：networkErr 非空时返回网络错误，否则返回固定 JSON 响应
type refreshStubTransport struct {
	response   string
	networkErr error
	requests   atomic.Int32
}

func (t *refreshStubTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests.Add(1)
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
	return &OpenClient{
		AppId:           "test-app-id",
		AccountId:       7,
		client:          resty.New().SetTransport(transport),
		AccessToken:     "old-access-token",
		RefreshTokenStr: "old-refresh-token",
	}
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
	if client.AccessToken != "old-access-token" || client.RefreshTokenStr != "old-refresh-token" {
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
	if client.AccessToken != "" || client.RefreshTokenStr != "" {
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
	if client.AccessToken != "old-access-token" || client.RefreshTokenStr != "old-refresh-token" {
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
	if client.AccessToken != "old-access-token" || client.RefreshTokenStr != "old-refresh-token" {
		t.Fatal("可重试失败后不应清空客户端凭据")
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("可重试失败不应发布凭证失效事件，实际发布 %d 次", got)
	}
	if got := transport.requests.Load(); got != int32(refreshTokenRetryCount+1) {
		t.Fatalf("40140121 应请求内重试共 %d 次，实际 %d 次", refreshTokenRetryCount+1, got)
	}
}
