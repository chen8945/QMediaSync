package openlist

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

func TestValidateToken_InvalidToken(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			// 如果 panic，说明日志系统未初始化，这是预期的
			t.Logf("预期 Panic 发生 (日志系统未初始化): %v", r)
		}
	}()
	client := NewClient(0, "http://localhost:8080", "", "", "invalid_token")
	_, err := client.GetUserInfo("invalid_token")
	if err == nil {
		t.Errorf("ValidateToken 应该返回错误，但返回了 nil")
	}
}

func TestValidateToken_EmptyToken(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			// 如果 panic，说明日志系统未初始化，这是预期的
			t.Logf("预期 Panic 发生 (日志系统未初始化): %v", r)
		}
	}()
	client := NewClient(0, "http://localhost:8080", "", "", "")
	_, err := client.GetUserInfo("")
	if err == nil {
		t.Errorf("ValidateToken 应该返回错误，但返回了 nil")
	}
}

func TestGetUserInfoTokenAuthDoesNotRetryAfterUnauthorized(t *testing.T) {
	oldOpenListLog := helpers.OpenListLog
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		helpers.OpenListLog = oldOpenListLog
	})

	var requestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":401,"message":"token expired","data":null}`))
	}))
	defer server.Close()

	client := &Client{
		AccessToken: "invalid-token",
		client:      resty.New().SetBaseURL(server.URL),
	}
	_, err := client.GetUserInfo("invalid-token")
	if err == nil {
		t.Fatal("无效 Token 应该返回错误")
	}
	if requestCount != 1 {
		t.Fatalf("无效 Token 收到 401 后请求次数 = %d，期望 1", requestCount)
	}
}

func TestGetUserInfoPasswordAuthDoesNotRetryWhenTokenRefreshFails(t *testing.T) {
	oldOpenListLog := helpers.OpenListLog
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		helpers.OpenListLog = oldOpenListLog
	})

	var userInfoRequestCount int
	var loginRequestCount int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/me":
			userInfoRequestCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":401,"message":"token expired","data":null}`))
		case "/api/auth/login":
			loginRequestCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":400,"message":"invalid credentials","data":null}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := &Client{
		Username:    "user",
		Password:    "invalid-password",
		AccessToken: "expired-token",
		client:      resty.New().SetBaseURL(server.URL),
	}
	_, err := client.GetUserInfo("expired-token")
	if err == nil {
		t.Fatal("刷新 Token 失败时应该返回错误")
	}
	if userInfoRequestCount != 1 {
		t.Fatalf("刷新 Token 失败后用户信息请求次数 = %d，期望 1", userInfoRequestCount)
	}
	if loginRequestCount != 1 {
		t.Fatalf("刷新 Token 请求次数 = %d，期望 1", loginRequestCount)
	}
}
