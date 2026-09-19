package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v2"
)

func setupAuthSecurityTest(t *testing.T) (*gin.Engine, *models.User, *models.UserSession, string) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.GlobalConfig = helpers.Config{JwtSecret: "test-secret"}
	setupControllerTestDB(t, &models.User{}, &models.UserSession{}, &models.ApiKey{})
	user := &models.User{Username: "admin", Password: "hashed"}
	if err := db.Db.Create(user).Error; err != nil {
		t.Fatalf("创建用户失败: %v", err)
	}
	session, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
		UserID:    user.ID,
		Username:  user.Username,
		UserAgent: "test-agent",
		IPAddress: "127.0.0.1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}

	r := gin.New()
	r.Use(JWTAuthMiddleware())
	r.POST("/protected", func(c *gin.Context) {
		user, ok := CurrentUser(c)
		if !ok || user.ID == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{"message": "missing user"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"username": user.Username, "auth_method": CurrentAuthMethod(c)})
	})
	return r, user, session, csrfToken
}

func TestCorsRestrictsCredentialedOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	helpers.GlobalConfig = helpers.Config{}
	r := gin.New()
	r.Use(Cors())
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Host = "localhost:12333"
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("不可信 Origin 不应被允许，got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("不可信 Origin 不应允许携带凭证，got %q", got)
	}
}

func TestCorsAllowsConfiguredTrustedOrigin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	helpers.GlobalConfig = helpers.Config{}
	if err := yaml.Unmarshal([]byte("trustedOrigins:\n  - https://qms.example.com\n"), &helpers.GlobalConfig); err != nil {
		t.Fatalf("解析 trustedOrigins 失败: %v", err)
	}
	r := gin.New()
	r.Use(Cors())
	r.GET("/protected", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Host = "api.example.com"
	req.Header.Set("Origin", "https://qms.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://qms.example.com" {
		t.Fatalf("配置的可信 Origin 应被允许，got %q", got)
	}
	if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("配置的可信 Origin 应允许携带凭证，got %q", got)
	}
}

func TestCorsAllowsConfiguredTrustedOriginWithDefaultPort(t *testing.T) {
	cases := []struct {
		name            string
		trustedOrigin   string
		browserOrigin   string
		requestHost     string
		wantAllowOrigin string
	}{
		{
			name:            "HTTPS 默认端口",
			trustedOrigin:   "https://qms.example.com:443",
			browserOrigin:   "https://qms.example.com",
			requestHost:     "api.example.com",
			wantAllowOrigin: "https://qms.example.com",
		},
		{
			name:            "HTTP 默认端口",
			trustedOrigin:   "http://localhost:80",
			browserOrigin:   "http://localhost",
			requestHost:     "api.example.com",
			wantAllowOrigin: "http://localhost",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			helpers.GlobalConfig = helpers.Config{}
			if err := yaml.Unmarshal([]byte("trustedOrigins:\n  - "+tc.trustedOrigin+"\n"), &helpers.GlobalConfig); err != nil {
				t.Fatalf("解析 trustedOrigins 失败: %v", err)
			}
			r := gin.New()
			r.Use(Cors())
			r.GET("/protected", func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"ok": true})
			})

			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			req.Host = tc.requestHost
			req.Header.Set("Origin", tc.browserOrigin)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if got := w.Header().Get("Access-Control-Allow-Origin"); got != tc.wantAllowOrigin {
				t.Fatalf("带默认端口的可信 Origin 配置应被允许，got %q", got)
			}
		})
	}
}

func TestCookieSessionRequiresCSRFForUnsafeMethod(t *testing.T) {
	r, _, session, csrfToken := setupAuthSecurityTest(t)
	tokenString := buildSessionCookieTokenForTest(t, session)

	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Host = "localhost:12333"
	req.Header.Set("Origin", "http://localhost:12333")
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: tokenString})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("缺少 X-CSRF-Token 时 HTTP = %d, want 403", w.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Host = "localhost:12333"
	req.Header.Set("Origin", "http://localhost:12333")
	req.Header.Set(csrfHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: tokenString})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("带 CSRF 时 HTTP = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestCookieSessionWithoutCredentialsReturnsUnauthorized(t *testing.T) {
	r, _, _, _ := setupAuthSecurityTest(t)
	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("未认证业务请求 HTTP = %d, want 401", w.Code)
	}
}

func TestCookieSessionAllowsDefaultViteOrigin(t *testing.T) {
	r, _, session, csrfToken := setupAuthSecurityTest(t)
	tokenString := buildSessionCookieTokenForTest(t, session)

	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Host = "localhost:12333"
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set(csrfHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: tokenString})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("默认 Vite 开发来源应通过 CSRF，HTTP = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestCookieSessionAllowsConfiguredTrustedOrigin(t *testing.T) {
	r, _, session, csrfToken := setupAuthSecurityTest(t)
	if err := yaml.Unmarshal([]byte("trustedOrigins:\n  - https://qms.example.com\n"), &helpers.GlobalConfig); err != nil {
		t.Fatalf("解析 trustedOrigins 失败: %v", err)
	}
	tokenString := buildSessionCookieTokenForTest(t, session)

	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Host = "api.example.com"
	req.Header.Set("Origin", "https://qms.example.com")
	req.Header.Set(csrfHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: tokenString})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("配置的可信来源应通过 CSRF，HTTP = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestAPIKeyHeaderSkipsCSRFAndWinsOverQuery(t *testing.T) {
	r, user, _, _ := setupAuthSecurityTest(t)
	_, rawHeaderKey, err := models.CreateAPIKey(user.ID, "header")
	if err != nil {
		t.Fatalf("创建 header api key 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/protected?api_key=invalid-query-key", nil)
	req.Header.Set(apiKeyHeaderName, rawHeaderKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); !strings.Contains(got, `"username":"admin"`) {
		t.Fatalf("header api key 未优先: %s", got)
	}
}

func TestAPIKeyAuthCanRunBackgroundUpdateSynchronouslyInTests(t *testing.T) {
	r, user, _, _ := setupAuthSecurityTest(t)
	setAuthBackgroundTaskRunnerForTest(t, func(fn func()) {
		fn()
	})
	apiKey, rawKey, err := models.CreateAPIKey(user.ID, "sync-update")
	if err != nil {
		t.Fatalf("创建 api key 失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Header.Set(apiKeyHeaderName, rawKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var updated models.ApiKey
	if err := db.Db.First(&updated, apiKey.ID).Error; err != nil {
		t.Fatalf("读取 api key 失败: %v", err)
	}
	if updated.LastUsedAt == 0 {
		t.Fatalf("同步后台执行器未更新 api key last_used_at")
	}
}

func TestCookieAuthCanRunSessionTouchSynchronouslyInTests(t *testing.T) {
	r, _, session, csrfToken := setupAuthSecurityTest(t)
	setAuthBackgroundTaskRunnerForTest(t, func(fn func()) {
		fn()
	})
	oldLastSeenAt := time.Now().Add(-2 * time.Minute).Unix()
	if err := db.Db.Model(session).Update("last_seen_at", oldLastSeenAt).Error; err != nil {
		t.Fatalf("设置旧 session last_seen_at 失败: %v", err)
	}
	tokenString := buildSessionCookieTokenForTest(t, session)

	req := httptest.NewRequest(http.MethodPost, "/protected", nil)
	req.Host = "localhost:12333"
	req.Header.Set("Origin", "http://localhost:12333")
	req.Header.Set(csrfHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: tokenString})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var updated models.UserSession
	if err := db.Db.First(&updated, session.ID).Error; err != nil {
		t.Fatalf("读取 session 失败: %v", err)
	}
	if updated.LastSeenAt == oldLastSeenAt {
		t.Fatalf("同步后台执行器未更新 session last_seen_at")
	}
}

func buildSessionCookieTokenForTest(t *testing.T, session *models.UserSession) string {
	t.Helper()

	tokenString, err := SignSessionJWT(SessionClaimsInput{
		UserID:    session.UserID,
		Username:  session.Username,
		SessionID: session.SessionID,
		TokenID:   session.TokenID,
		ExpiresAt: session.ExpiresAt,
	})
	if err != nil {
		t.Fatalf("签发测试 JWT 失败: %v", err)
	}
	return tokenString
}

func setAuthBackgroundTaskRunnerForTest(t *testing.T, runner func(func())) {
	t.Helper()
	oldRunner := runAuthBackgroundTask
	runAuthBackgroundTask = runner
	t.Cleanup(func() {
		runAuthBackgroundTask = oldRunner
	})
}

func TestAuthRequestErrorCodes(t *testing.T) {
	cases := []struct {
		name       string
		origin     string
		host       string
		proto      string
		mutation   string
		wantStatus int
		wantCode   string
	}{
		{name: "缺少登录凭证", mutation: "missing_auth", wantStatus: 401, wantCode: ErrorCodeAuthenticationRequired},
		{name: "无效登录凭证", mutation: "invalid_auth", wantStatus: 401, wantCode: ErrorCodeAuthenticationInvalid},
		{name: "撤销会话", mutation: "revoked", wantStatus: 401, wantCode: ErrorCodeSessionInvalid},
		{name: "数据库会话过期", mutation: "expired_session", wantStatus: 401, wantCode: ErrorCodeSessionInvalid},
		{name: "会话不存在", mutation: "missing_session", wantStatus: 401, wantCode: ErrorCodeSessionInvalid},
		{name: "会话用户不一致", mutation: "session_user", wantStatus: 401, wantCode: ErrorCodeSessionInvalid},
		{name: "登录用户不存在", mutation: "missing_user", wantStatus: 401, wantCode: ErrorCodeAuthenticationInvalid},
		{name: "会话查询故障不标记具体原因", mutation: "database", wantStatus: 401},
		{name: "来源缺失", wantStatus: 403, wantCode: ErrorCodeRequestOriginInvalid},
		{name: "来源格式无效", origin: "null", wantStatus: 403, wantCode: ErrorCodeRequestOriginInvalid},
		{name: "代理改写域名", origin: "https://qms.example.com", host: "127.0.0.1", proto: "https", wantStatus: 403, wantCode: ErrorCodeRequestOriginInvalid},
		{name: "代理丢失协议", origin: "https://qms.example.com", host: "qms.example.com", wantStatus: 403, wantCode: ErrorCodeRequestOriginInvalid},
		{name: "代理丢失端口", origin: "https://qms.example.com:8443", host: "qms.example.com", proto: "https", wantStatus: 403, wantCode: ErrorCodeRequestOriginInvalid},
		{name: "代理保留完整地址", origin: "https://qms.example.com:8443", host: "qms.example.com:8443", proto: "https", wantStatus: 200},
		{name: "缺少CSRF头", origin: "http://localhost:12333", mutation: "missing_csrf", wantStatus: 403, wantCode: ErrorCodeCSRFTokenInvalid},
		{name: "缺少CSRF Cookie", origin: "http://localhost:12333", mutation: "missing_csrf_cookie", wantStatus: 403, wantCode: ErrorCodeCSRFTokenInvalid},
		{name: "CSRF头与Cookie不一致", origin: "http://localhost:12333", mutation: "mismatched_csrf", wantStatus: 403, wantCode: ErrorCodeCSRFTokenInvalid},
		{name: "CSRF与会话不一致", origin: "http://localhost:12333", mutation: "stale_csrf", wantStatus: 403, wantCode: ErrorCodeCSRFTokenInvalid},
		{name: "无效APIKey", mutation: "api_key", wantStatus: 401, wantCode: ErrorCodeAuthenticationInvalid},
		{name: "API Key 用户不存在", mutation: "api_key_missing_user", wantStatus: 401, wantCode: ErrorCodeAuthenticationInvalid},
		{name: "API Key 豁免来源和CSRF", mutation: "valid_api_key", wantStatus: 200},
		{name: "API Key 查询故障不标记具体原因", mutation: "api_key_db", wantStatus: 401},
		{name: "API Key 用户查询故障不标记具体原因", mutation: "api_key_user_db", wantStatus: 401},
		{name: "Cookie 用户查询故障不标记具体原因", mutation: "user_db", wantStatus: 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, user, session, csrfToken := setupAuthSecurityTest(t)
			r.POST("/api/user/change", ChangePassword)
			token := buildSessionCookieTokenForTest(t, session)
			apiKey := ""
			switch tc.mutation {
			case "invalid_auth":
				token = "invalid"
			case "revoked":
				if err := db.Db.Model(session).Update("revoked_at", time.Now().Unix()).Error; err != nil {
					t.Fatal(err)
				}
			case "expired_session":
				if err := db.Db.Model(session).Update("expires_at", time.Now().Add(-time.Hour).Unix()).Error; err != nil {
					t.Fatal(err)
				}
			case "missing_session":
				if err := db.Db.Delete(session).Error; err != nil {
					t.Fatal(err)
				}
			case "session_user":
				if err := db.Db.Model(session).Update("user_id", user.ID+1).Error; err != nil {
					t.Fatal(err)
				}
			case "missing_user":
				if err := db.Db.Delete(user).Error; err != nil {
					t.Fatal(err)
				}
			case "api_key":
				apiKey = "invalid"
			case "api_key_missing_user", "valid_api_key", "api_key_db", "api_key_user_db":
				_, rawKey, err := models.CreateAPIKey(user.ID, "request-errors")
				if err != nil {
					t.Fatal(err)
				}
				apiKey = rawKey
				switch tc.mutation {
				case "api_key_missing_user":
					if err := db.Db.Delete(user).Error; err != nil {
						t.Fatal(err)
					}
				case "api_key_db":
					if err := db.Db.Migrator().DropTable(&models.ApiKey{}); err != nil {
						t.Fatal(err)
					}
				case "api_key_user_db":
					if err := db.Db.Migrator().DropTable(&models.User{}); err != nil {
						t.Fatal(err)
					}
				}
			case "database":
				if err := db.Db.Migrator().DropTable(&models.UserSession{}); err != nil {
					t.Fatal(err)
				}
			case "user_db":
				if err := db.Db.Migrator().DropTable(&models.User{}); err != nil {
					t.Fatal(err)
				}
			case "stale_csrf":
				csrfToken = "old-token"
			}
			req := httptest.NewRequest(http.MethodPost, "/api/user/change", strings.NewReader(`{"username":"updatedadmin"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Host = "localhost:12333"
			if tc.host != "" {
				req.Host = tc.host
			}
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-Forwarded-Proto", tc.proto)
			if tc.mutation != "missing_auth" {
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
			}
			if tc.mutation != "missing_csrf_cookie" && tc.mutation != "valid_api_key" {
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
			}
			if tc.mutation != "missing_csrf" && tc.mutation != "valid_api_key" {
				req.Header.Set(csrfHeaderName, csrfToken)
			}
			if tc.mutation == "mismatched_csrf" {
				req.Header.Set(csrfHeaderName, "different-token")
			}
			if apiKey != "" {
				req.Header.Set(apiKeyHeaderName, apiKey)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("HTTP = %d，want %d，body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.wantCode == "" {
				if _, ok := body["error_code"]; ok {
					t.Fatalf("不应带有具体错误码：%s", w.Body.String())
				}
			} else if body["error_code"] != tc.wantCode {
				t.Fatalf("错误码 = %v，want %s", body["error_code"], tc.wantCode)
			}
			if w.Code != http.StatusOK && (body["code"] != float64(BadRequest) || body["data"] != nil) {
				t.Fatalf("错误响应必须保留 code=500 和 data=null：%s", w.Body.String())
			}
			if w.Code == http.StatusOK && (body["code"] != float64(Success) || body["data"] != true) {
				t.Fatalf("成功请求应执行凭据修改并保留成功响应：%s", w.Body.String())
			}
		})
	}
}

func TestSessionSuccessOmitsRequestErrorCode(t *testing.T) {
	_, _, session, _ := setupAuthSecurityTest(t)
	r := gin.New()
	r.GET("/api/session", SessionAction)
	for _, authenticated := range []bool{false, true} {
		t.Run(fmt.Sprintf("authenticated=%t", authenticated), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
			if authenticated {
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: buildSessionCookieTokenForTest(t, session)})
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			var body struct {
				Code      APIResponseCode `json:"code"`
				ErrorCode *string         `json:"error_code"`
				Data      struct {
					Authenticated bool `json:"authenticated"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || body.Code != Success || body.ErrorCode != nil || body.Data.Authenticated != authenticated {
				t.Fatalf("会话状态契约错误：HTTP=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
