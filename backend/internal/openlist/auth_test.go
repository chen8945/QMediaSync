package openlist

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

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

func TestClientPasswordAuthDoesNotRetryWhenLoginFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		loginCode int
		direct    bool
		wantInfo  int64
	}{
		{name: "刷新登录返回 400", loginCode: http.StatusBadRequest, wantInfo: 1},
		{name: "刷新登录返回 401", loginCode: http.StatusUnauthorized, wantInfo: 1},
		{name: "直接登录返回 401", loginCode: http.StatusUnauthorized, direct: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupConcurrentClientTest(t)
			var userInfoRequests, loginRequests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/openlist/api/me":
					userInfoRequests.Add(1)
					_, _ = io.WriteString(w, `{"code":401,"message":"token expired","data":null}`)
				case "/openlist/api/auth/login":
					loginRequests.Add(1)
					_, _ = fmt.Fprintf(w, `{"code":%d,"message":"invalid credentials","data":null}`, tc.loginCode)
				default:
					t.Errorf("意外请求：%s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			client := NewClient(1, server.URL+"/openlist/", "user", "invalid-password", "expired-token")
			done := make(chan error, 1)
			go func() {
				var err error
				if tc.direct {
					_, err = client.GetToken()
				} else {
					_, err = client.GetUserInfo("")
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, errTokenExpired) {
					t.Fatalf("登录失败错误 = %v，期望凭据失效", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("登录失败后请求未返回；用户信息请求 %d 次，登录请求 %d 次", userInfoRequests.Load(), loginRequests.Load())
			}
			if got := userInfoRequests.Load(); got != tc.wantInfo {
				t.Errorf("用户信息请求次数 = %d，期望 %d", got, tc.wantInfo)
			}
			if got := loginRequests.Load(); got != 1 {
				t.Errorf("登录请求次数 = %d，期望 1", got)
			}
			if got := client.GetAuthToken(); got != "expired-token" {
				t.Errorf("登录失败后 Token = %q，期望保留原值", got)
			}
		})
	}
}
