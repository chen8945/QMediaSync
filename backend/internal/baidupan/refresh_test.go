package baidupan

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

// failingRefreshTransport 让刷新请求始终返回网络错误，并统计尝试次数
type failingRefreshTransport struct {
	networkErr error
	requests   atomic.Int32
}

func (t *failingRefreshTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.requests.Add(1)
	return nil, t.networkErr
}

func setupRefreshRelayTest(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	oldKey := helpers.OAuthRelayEncryptionKey
	oldAuthServer := helpers.GlobalConfig.AuthServer
	oldCount, oldDelay := refreshRetryCount, refreshRetryDelay
	refreshRetryCount, refreshRetryDelay = 5, time.Millisecond
	t.Cleanup(func() {
		helpers.OAuthRelayEncryptionKey = oldKey
		helpers.GlobalConfig.AuthServer = oldAuthServer
		refreshRetryCount, refreshRetryDelay = oldCount, oldDelay
	})
	helpers.OAuthRelayEncryptionKey = "test-relay-key"
	if handler != nil {
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		helpers.GlobalConfig.AuthServer = server.URL
	} else {
		helpers.GlobalConfig.AuthServer = "http://refresh.test.invalid"
	}
}

func TestIsRefreshTokenDead(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "无错误", err: nil, want: false},
		{name: "网络错误", err: fmt.Errorf(`Get "http://relay/baidupan/oauth-url": dial tcp: lookup relay: server misbehaving`), want: false},
		{name: "响应格式异常", err: fmt.Errorf("百度刷新访问凭证响应格式异常：<html>"), want: false},
		{name: "invalid_client", err: &OAuthError{Code: "invalid_client"}, want: false},
		{name: "invalid_request", err: &OAuthError{Code: "invalid_request"}, want: false},
		{name: "invalid_grant", err: &OAuthError{Code: "invalid_grant", Description: "Refresh Token invalid"}, want: true},
		{name: "expired_token", err: &OAuthError{Code: "expired_token"}, want: true},
		{name: "包装后的失效错误", err: fmt.Errorf("刷新失败: %w", &OAuthError{Code: "invalid_grant"}), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsRefreshTokenDead(tt.err); got != tt.want {
				t.Fatalf("IsRefreshTokenDead() = %v，期望 %v", got, tt.want)
			}
		})
	}
}

func TestRefreshTokenReturnsRelayCredentials(t *testing.T) {
	setupRefreshRelayTest(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("action"); got != "refresh" {
			t.Errorf("刷新请求 action 参数 = %q，期望 refresh", got)
		}
		if r.URL.Query().Get("state") == "" {
			t.Error("刷新请求应携带非空 state 参数")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access-token","refresh_token":"new-refresh-token","expires_in":2592000}`))
	})

	resp, err := RefreshToken(1, "old-refresh-token")
	if err != nil {
		t.Fatalf("刷新成功不应返回错误: %v", err)
	}
	if resp.AccessToken != "new-access-token" || resp.RefreshToken != "new-refresh-token" || resp.ExpiresIn != 2592000 {
		t.Fatalf("刷新结果字段异常：%+v", resp)
	}
}

func TestRefreshTokenKeepsOldRefreshTokenWhenRelayOmitsIt(t *testing.T) {
	setupRefreshRelayTest(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access-token","expires_in":2592000}`))
	})

	resp, err := RefreshToken(1, "old-refresh-token")
	if err != nil {
		t.Fatalf("刷新成功不应返回错误: %v", err)
	}
	if resp.RefreshToken != "old-refresh-token" {
		t.Fatalf("中转未返回新 refresh_token 时应沿用旧值，实际：%q", resp.RefreshToken)
	}
}

func TestRefreshTokenOAuthErrorDoesNotRetry(t *testing.T) {
	var requests atomic.Int32
	setupRefreshRelayTest(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Refresh Token invalid"}`))
	})

	_, err := RefreshToken(1, "old-refresh-token")
	if err == nil {
		t.Fatal("OAuth 错误应返回错误")
	}
	if !IsRefreshTokenDead(err) {
		t.Fatalf("invalid_grant 应判定为刷新令牌失效，实际错误：%v", err)
	}
	var oauthErr *OAuthError
	if !errors.As(err, &oauthErr) || oauthErr.Description != "Refresh Token invalid" {
		t.Fatalf("应返回携带原始描述的 OAuthError，实际：%v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("OAuth 错误不应重试，实际请求 %d 次", got)
	}
}

func TestRefreshTokenNetworkErrorRetries(t *testing.T) {
	setupRefreshRelayTest(t, nil)
	oldClient := refreshTokenHTTPClient
	transport := &failingRefreshTransport{
		networkErr: fmt.Errorf(`Get "http://refresh.test.invalid/baidupan/oauth-url": dial tcp: lookup refresh.test.invalid: server misbehaving`),
	}
	refreshTokenHTTPClient = &http.Client{Timeout: 5 * time.Second, Transport: transport}
	t.Cleanup(func() {
		refreshTokenHTTPClient = oldClient
	})

	_, err := RefreshToken(1, "old-refresh-token")
	if err == nil {
		t.Fatal("网络错误应返回错误")
	}
	if IsRefreshTokenDead(err) {
		t.Fatal("网络错误不应判定为刷新令牌失效")
	}
	if got := transport.requests.Load(); got != int32(refreshRetryCount+1) {
		t.Fatalf("期望请求内重试共 %d 次，实际 %d 次", refreshRetryCount+1, got)
	}
}

func TestRefreshTokenNonJSONResponseRetries(t *testing.T) {
	var requests atomic.Int32
	setupRefreshRelayTest(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html><body>502 Bad Gateway</body></html>")
	})

	_, err := RefreshToken(1, "old-refresh-token")
	if err == nil {
		t.Fatal("非 JSON 响应应返回错误")
	}
	if IsRefreshTokenDead(err) {
		t.Fatal("响应格式异常不应判定为刷新令牌失效")
	}
	if got := requests.Load(); got != int32(refreshRetryCount+1) {
		t.Fatalf("响应格式异常应请求内重试共 %d 次，实际 %d 次", refreshRetryCount+1, got)
	}
}

func TestRefreshTokenMissingCredentialFieldsRetries(t *testing.T) {
	var requests atomic.Int32
	setupRefreshRelayTest(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"expires_in":2592000}`))
	})

	_, err := RefreshToken(1, "old-refresh-token")
	if err == nil {
		t.Fatal("缺少凭据字段的响应应返回错误")
	}
	if IsRefreshTokenDead(err) {
		t.Fatal("缺少凭据字段不应判定为刷新令牌失效")
	}
	if got := requests.Load(); got != int32(refreshRetryCount+1) {
		t.Fatalf("缺少凭据字段应请求内重试共 %d 次，实际 %d 次", refreshRetryCount+1, got)
	}
}
