package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func setupSessionActionTest(t *testing.T) *models.User {
	t.Helper()
	gin.SetMode(gin.TestMode)
	helpers.GlobalConfig.JwtSecret = "test-secret"
	setupControllerTestDB(t, &models.User{}, &models.UserSession{})
	user := &models.User{Username: "admin", Password: "hashed"}
	if err := db.Db.Create(user).Error; err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	return user
}

func setupLoginActionTestWithPassword(t *testing.T, username string, password string) *models.User {
	t.Helper()

	user := setupSessionActionTest(t)
	hash, err := models.HashUserPassword(password)
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	user.Username = username
	user.Password = hash
	if err := db.Db.Save(user).Error; err != nil {
		t.Fatalf("保存用户失败: %v", err)
	}
	return user
}

func TestSessionAction_FromCookie(t *testing.T) {
	user := setupSessionActionTest(t)
	session, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
		UserID:    user.ID,
		Username:  user.Username,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	tokenString := buildSessionCookieTokenForTest(t, session)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: tokenString})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	c.Request = req

	SessionAction(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
	}
	if cacheControl := w.Header().Get("Cache-Control"); cacheControl != "no-store, private" {
		t.Fatalf("Cache-Control = %q，期望 no-store, private", cacheControl)
	}
	body := w.Body.String()
	if strings.Contains(body, `"token"`) {
		t.Fatalf("会话响应不应返回 JWT token: %s", body)
	}
	if !strings.Contains(body, `"csrf_token"`) || !strings.Contains(body, `"username":"admin"`) {
		t.Fatalf("响应未返回用户和 csrf: %s", body)
	}
	var response httpapi.APIResponse[sessionActionResponseData]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析会话响应失败: %v", err)
	}
	if response.Status != w.Code || !response.Data.Authenticated {
		t.Fatalf("有效会话应返回 authenticated=true: %s", body)
	}
}

type sessionActionResponseData struct {
	Authenticated bool `json:"authenticated"`
}

func TestSessionAction_ReportsAnonymousStateForMissingAndInvalidSessions(t *testing.T) {
	tests := []struct {
		name        string
		prepare     func(t *testing.T, user *models.User) *http.Request
		wantCleared bool
	}{
		{
			name: "无 Cookie",
			prepare: func(_ *testing.T, _ *models.User) *http.Request {
				return httptest.NewRequest(http.MethodGet, "/api/session", nil)
			},
			wantCleared: false,
		},
		{
			name: "无效 JWT",
			prepare: func(_ *testing.T, _ *models.User) *http.Request {
				req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: "invalid-jwt"})
				return req
			},
			wantCleared: true,
		},
		{
			name: "已撤销会话",
			prepare: func(t *testing.T, user *models.User) *http.Request {
				session, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
					UserID:    user.ID,
					Username:  user.Username,
					ExpiresAt: time.Now().Add(time.Hour).Unix(),
				})
				if err != nil {
					t.Fatalf("创建 session 失败: %v", err)
				}
				if err := models.RevokeUserSession(user.ID, session.SessionID, "test"); err != nil {
					t.Fatalf("撤销 session 失败: %v", err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: buildSessionCookieTokenForTest(t, session)})
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
				return req
			},
			wantCleared: true,
		},
		{
			name: "过期 JWT",
			prepare: func(t *testing.T, user *models.User) *http.Request {
				session, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
					UserID:    user.ID,
					Username:  user.Username,
					ExpiresAt: time.Now().Add(time.Hour).Unix(),
				})
				if err != nil {
					t.Fatalf("创建 session 失败: %v", err)
				}
				token, err := SignSessionJWT(SessionClaimsInput{
					UserID:    user.ID,
					Username:  user.Username,
					SessionID: session.SessionID,
					TokenID:   session.TokenID,
					ExpiresAt: time.Now().Add(-time.Minute).Unix(),
				})
				if err != nil {
					t.Fatalf("签发过期 JWT 失败: %v", err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
				return req
			},
			wantCleared: true,
		},
		{
			name: "会话不属于 JWT 用户",
			prepare: func(t *testing.T, user *models.User) *http.Request {
				session, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
					UserID:    user.ID,
					Username:  user.Username,
					ExpiresAt: time.Now().Add(time.Hour).Unix(),
				})
				if err != nil {
					t.Fatalf("创建 session 失败: %v", err)
				}
				token, err := SignSessionJWT(SessionClaimsInput{
					UserID:    user.ID + 1,
					Username:  "other",
					SessionID: session.SessionID,
					TokenID:   session.TokenID,
					ExpiresAt: time.Now().Add(time.Hour).Unix(),
				})
				if err != nil {
					t.Fatalf("签发 JWT 失败: %v", err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
				return req
			},
			wantCleared: true,
		},
		{
			name: "登录用户已删除",
			prepare: func(t *testing.T, user *models.User) *http.Request {
				session, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
					UserID:    user.ID,
					Username:  user.Username,
					ExpiresAt: time.Now().Add(time.Hour).Unix(),
				})
				if err != nil {
					t.Fatalf("创建 session 失败: %v", err)
				}
				if err := db.Db.Delete(user).Error; err != nil {
					t.Fatalf("删除测试用户失败: %v", err)
				}
				req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
				req.AddCookie(&http.Cookie{Name: authCookieName, Value: buildSessionCookieTokenForTest(t, session)})
				req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
				return req
			},
			wantCleared: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user := setupSessionActionTest(t)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = tt.prepare(t, user)

			SessionAction(c)

			if w.Code != http.StatusOK {
				t.Fatalf("HTTP 状态码 = %d，期望 %d，body=%s", w.Code, http.StatusOK, w.Body.String())
			}
			if cacheControl := w.Header().Get("Cache-Control"); cacheControl != "no-store, private" {
				t.Fatalf("Cache-Control = %q，期望 no-store, private", cacheControl)
			}
			var response httpapi.APIResponse[sessionActionResponseData]
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatalf("解析会话响应失败: %v", err)
			}
			if response.Status != http.StatusOK || response.Data.Authenticated {
				t.Fatalf("会话响应 = %+v，期望成功匿名状态", response)
			}
			if got := hasClearedSessionCookies(w.Result().Cookies()); got != tt.wantCleared {
				t.Fatalf("清理 Cookie = %t，期望 %t，cookies=%#v", got, tt.wantCleared, w.Result().Cookies())
			}
		})
	}
}

func TestSessionAction_ReturnsServerErrorForDatabaseFailure(t *testing.T) {
	user := setupSessionActionTest(t)
	session, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
		UserID:    user.ID,
		Username:  user.Username,
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("创建 session 失败: %v", err)
	}
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatalf("读取底层数据库失败: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/api/session", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: buildSessionCookieTokenForTest(t, session)})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	c.Request = req

	SessionAction(c)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP 状态码 = %d，期望 %d，body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	var response httpapi.APIResponse[any]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != w.Code || response.ErrorCode != "INTERNAL_ERROR" || response.Instance == "" || response.Data != nil {
		t.Fatalf("数据库故障应返回内部错误: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"authenticated":false`) {
		t.Fatalf("数据库故障不能伪装成匿名状态: %s", w.Body.String())
	}
	if hasClearedSessionCookies(w.Result().Cookies()) {
		t.Fatalf("数据库故障不应清理 Cookie: %#v", w.Result().Cookies())
	}
}

func hasClearedSessionCookies(cookies []*http.Cookie) bool {
	cleared := make(map[string]bool)
	for _, cookie := range cookies {
		if (cookie.Name == authCookieName || cookie.Name == csrfCookieName) && cookie.Value == "" && cookie.MaxAge < 0 {
			cleared[cookie.Name] = true
		}
	}
	return cleared[authCookieName] && cleared[csrfCookieName]
}

func TestLoginActionDoesNotReturnJWTToken(t *testing.T) {
	setupLoginActionTestWithPassword(t, "admin", "admin123")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := strings.NewReader(`{"username":"admin","password":"admin123","rememberMe":false}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/login", body)
	c.Request.Header.Set("Content-Type", "application/json")

	LoginAction(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"token"`) {
		t.Fatalf("登录响应不应返回 JWT token: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"csrf_token"`) {
		t.Fatalf("登录响应应返回 csrf_token: %s", w.Body.String())
	}
}

func TestLogoutAction_ClearsCookie(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/logout", nil)

	LogoutAction(c)

	cookies := w.Result().Cookies()
	if len(cookies) == 0 || cookies[0].Name != authCookieName || cookies[0].Value != "" || cookies[0].MaxAge >= 0 {
		t.Fatalf("退出登录未清理 auth_token Cookie: %#v", cookies)
	}
}
