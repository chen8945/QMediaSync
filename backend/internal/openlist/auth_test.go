package openlist

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
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

func TestGetTokenPreservesReplacedConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		basePath   string
		username   string
		password   string
		token      string
		wantToken  string
		wantResult string
		wantSaves  int64
	}{
		{name: "配置未变化", basePath: "/old", username: "user", password: "password", token: "old-token", wantToken: "login-token", wantResult: "login-token", wantSaves: 1},
		{name: "地址变化但 Token 相同", basePath: "/new", username: "user", password: "password", token: "old-token", wantToken: "old-token", wantResult: "login-token"},
		{name: "密码变化但 Token 相同", basePath: "/old", username: "user", password: "new-password", token: "old-token", wantToken: "old-token", wantResult: "login-token"},
		{name: "用户名变化但 Token 相同", basePath: "/old", username: "new-user", password: "password", token: "old-token", wantToken: "old-token", wantResult: "login-token"},
		{name: "同配置已有新 Token", basePath: "/old", username: "user", password: "password", token: "new-token", wantToken: "new-token", wantResult: "new-token"},
		{name: "地址和 Token 都变化", basePath: "/new", username: "user", password: "password", token: "new-token", wantToken: "new-token", wantResult: "login-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupConcurrentClientTest(t)
				helpers.InitEventBus()
				t.Cleanup(helpers.InitEventBus)
				var logins, saves atomic.Int64
				gate := make(chan struct{})
				client := NewClient(1, "http://openlist.invalid/old", "user", "password", "old-token")
				client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/old/api/auth/login":
						logins.Add(1)
						<-gate
						_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"token":"login-token"}}`)
					case tc.basePath + "/api/me":
						if got := r.Header.Get("Authorization"); got != tc.wantToken {
							t.Errorf("新配置请求 Token = %q，期望 %q", got, tc.wantToken)
						}
						_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"id":1,"username":"user"}}`)
					default:
						t.Errorf("意外请求：%s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(helpers.Event) helpers.EventResult {
					saves.Add(1)
					return helpers.EventResult{Success: true}
				})
				done := make(chan struct{})
				go func() {
					defer close(done)
					result, err := client.GetToken()
					if err != nil {
						t.Errorf("原配置登录失败：%v", err)
					} else if result.Token != tc.wantResult {
						t.Errorf("原配置登录结果 = %q，期望 %q", result.Token, tc.wantResult)
					}
				}()
				synctest.Wait()
				if logins.Load() != 1 {
					t.Error("登录未进入受控边界")
				}
				NewClient(1, "http://openlist.invalid"+tc.basePath, tc.username, tc.password, tc.token)
				close(gate)
				<-done
				if got := client.GetAuthToken(); got != tc.wantToken {
					t.Errorf("当前配置 Token = %q，期望 %q", got, tc.wantToken)
				}
				if _, err := client.GetUserInfo(""); err != nil {
					t.Errorf("新配置请求失败：%v", err)
				}
				if got := saves.Load(); got != tc.wantSaves {
					t.Errorf("Token 保存次数 = %d，期望 %d", got, tc.wantSaves)
				}
			})
		})
	}
}

func TestClientLateUnauthorizedReusesNewToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupConcurrentClientTest(t)
		helpers.InitEventBus()
		t.Cleanup(helpers.InitEventBus)
		var logins, userRequests atomic.Int64
		gate := make(chan struct{})
		client := NewClient(1, "http://openlist.invalid", "user", "password", "old-token")
		client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/auth/login":
				logins.Add(1)
				_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"token":"new-token"}}`)
			case "/api/me":
				if userRequests.Add(1) == 1 {
					<-gate
					_, _ = io.WriteString(w, `{"code":401,"message":"expired","data":null}`)
					return
				}
				if r.Header.Get("Authorization") != "new-token" {
					t.Error("迟到的 401 未复用当前 Token")
				}
				_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"id":1,"username":"user"}}`)
			default:
				t.Errorf("意外请求：%s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		done := make(chan error, 1)
		go func() { _, err := client.GetUserInfo(""); done <- err }()
		synctest.Wait()
		if userRequests.Load() != 1 {
			t.Error("首个用户信息请求未进入受控边界")
		}
		client.SetAuthToken("new-token")
		close(gate)
		if err := <-done; err != nil {
			t.Fatalf("复用新 Token 后请求失败：%v", err)
		}
		if got := logins.Load(); got != 0 {
			t.Errorf("迟到的 401 触发了 %d 次额外登录", got)
		}
	})
}

func TestGetTokenDoesNotLogCredentials(t *testing.T) {
	setupConcurrentClientTest(t)
	var output bytes.Buffer
	logger := &helpers.QLogger{Logger: log.New(&output, "", 0)}
	helpers.AppLogger, helpers.OpenListLog = logger, logger
	helpers.InitEventBus()
	t.Cleanup(helpers.InitEventBus)
	client := NewClient(1, "http://openlist.invalid", "user", "private-password", "private-old-token")
	client.client.SetTransport(handlerTransport(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"token":"private-new-token"}}`)
	}))
	if _, err := client.GetToken(); err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{"private-password", "private-old-token", "private-new-token"} {
		if strings.Contains(output.String(), credential) {
			t.Error("登录日志泄露了密码或 Token")
		}
	}
}
