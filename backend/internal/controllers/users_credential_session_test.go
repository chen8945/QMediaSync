package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type credentialChangeResponse struct {
	Code    APIResponseCode `json:"code"`
	Message string          `json:"message"`
	Data    bool            `json:"data"`
}

type credentialSessionResponse struct {
	Code APIResponseCode `json:"code"`
	Data []struct {
		SessionID string `json:"session_id"`
		Current   bool   `json:"current"`
	} `json:"data"`
}

type credentialChangeFixture struct {
	router      *gin.Engine
	user        *models.User
	current     *models.UserSession
	other       *models.UserSession
	expired     *models.UserSession
	currentCSRF string
	otherCSRF   string
}

func setupCredentialChangeRouter(t *testing.T) *credentialChangeFixture {
	t.Helper()

	gin.SetMode(gin.TestMode)
	helpers.GlobalConfig = helpers.Config{JwtSecret: "test-secret"}
	setupConcurrentControllerTestDB(t, &models.User{}, &models.UserSession{}, &models.ApiKey{})

	passwordHash, err := models.HashUserPassword("old-password")
	if err != nil {
		t.Fatalf("生成测试密码哈希失败: %v", err)
	}
	user := &models.User{Username: "admin", Password: passwordHash}
	if err := db.Db.Create(user).Error; err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}
	current, csrfToken, err := models.CreateUserSession(models.CreateUserSessionInput{
		UserID:    user.ID,
		Username:  user.Username,
		UserAgent: "current-device",
		IPAddress: "127.0.0.1",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("创建当前会话失败: %v", err)
	}
	other, otherCSRFToken, err := models.CreateUserSession(models.CreateUserSessionInput{
		UserID:    user.ID,
		Username:  user.Username,
		UserAgent: "other-device",
		IPAddress: "127.0.0.2",
		ExpiresAt: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("创建其他会话失败: %v", err)
	}
	expired, _, err := models.CreateUserSession(models.CreateUserSessionInput{
		UserID:    user.ID,
		Username:  user.Username,
		UserAgent: "expired-device",
		IPAddress: "127.0.0.3",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("创建过期会话失败: %v", err)
	}

	r := gin.New()
	r.POST("/login", LoginAction)
	r.Use(JWTAuthMiddleware())
	r.POST("/user/change", ChangePassword)
	r.GET("/user/sessions", ListUserSessions)
	return &credentialChangeFixture{
		router:      r,
		user:        user,
		current:     current,
		other:       other,
		expired:     expired,
		currentCSRF: csrfToken,
		otherCSRF:   otherCSRFToken,
	}
}

func newCredentialChangeRequest(t *testing.T, current *models.UserSession, csrfToken, username, newPassword string) *http.Request {
	t.Helper()

	body, err := json.Marshal(map[string]string{"username": username, "new_password": newPassword})
	if err != nil {
		t.Fatalf("编码修改凭据请求失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/user/change", bytes.NewReader(body))
	req.Host = "localhost:12333"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:12333")
	req.Header.Set(csrfHeaderName, csrfToken)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: buildSessionCookieTokenForTest(t, current)})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: csrfToken})
	return req
}

func TestChangeCredentials_RevokesAllPreviousSessions(t *testing.T) {
	tests := []struct {
		name         string
		username     string
		newPassword  string
		oldLoginName string
		oldLoginPass string
		newLoginName string
		newLoginPass string
	}{
		{
			name:         "仅修改密码",
			username:     "admin",
			newPassword:  "new-password",
			oldLoginName: "admin",
			oldLoginPass: "old-password",
			newLoginName: "admin",
			newLoginPass: "new-password",
		},
		{
			name:         "仅修改用户名",
			username:     "newadmin",
			oldLoginName: "admin",
			oldLoginPass: "old-password",
			newLoginName: "newadmin",
			newLoginPass: "old-password",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := setupCredentialChangeRouter(t)
			oldSessionTokens := map[string]string{
				fixture.current.SessionID: buildSessionCookieTokenForTest(t, fixture.current),
				fixture.other.SessionID:   buildSessionCookieTokenForTest(t, fixture.other),
			}

			changeResponse := httptest.NewRecorder()
			fixture.router.ServeHTTP(changeResponse, newCredentialChangeRequest(t, fixture.current, fixture.currentCSRF, tt.username, tt.newPassword))

			if changeResponse.Code != http.StatusOK {
				t.Fatalf("修改凭据 HTTP = %d，body=%s", changeResponse.Code, changeResponse.Body.String())
			}
			var changed credentialChangeResponse
			if err := json.Unmarshal(changeResponse.Body.Bytes(), &changed); err != nil {
				t.Fatalf("解析修改凭据响应失败: %v", err)
			}
			if changed.Code != Success || !changed.Data {
				t.Fatalf("修改凭据响应 = %+v，期望成功", changed)
			}
			if !hasClearedSessionCookies(changeResponse.Result().Cookies()) {
				t.Fatalf("修改凭据成功后应清除认证 Cookie: %#v", changeResponse.Result().Cookies())
			}
			if _, err := models.CheckLogin(tt.oldLoginName, tt.oldLoginPass); err == nil {
				t.Fatal("旧凭据不应仍可登录")
			}
			if _, err := models.CheckLogin(tt.newLoginName, tt.newLoginPass); err != nil {
				t.Fatalf("新凭据应可登录: %v", err)
			}

			for sessionID, token := range oldSessionTokens {
				oldSessionRequest := httptest.NewRequest(http.MethodGet, "/user/sessions", nil)
				oldSessionRequest.AddCookie(&http.Cookie{Name: authCookieName, Value: token})
				oldSessionResponse := httptest.NewRecorder()
				fixture.router.ServeHTTP(oldSessionResponse, oldSessionRequest)
				if oldSessionResponse.Code != http.StatusUnauthorized {
					t.Fatalf("旧会话 %s 请求受保护接口 HTTP = %d，期望 %d，body=%s", sessionID, oldSessionResponse.Code, http.StatusUnauthorized, oldSessionResponse.Body.String())
				}
			}

			loginRequest := newLoginRequest(t, tt.newLoginName, tt.newLoginPass)
			loginResponse := httptest.NewRecorder()
			fixture.router.ServeHTTP(loginResponse, loginRequest)
			if loginResponse.Code != http.StatusOK {
				t.Fatalf("重新登录 HTTP = %d，body=%s", loginResponse.Code, loginResponse.Body.String())
			}
			authCookie := findCookieValue(loginResponse.Result().Cookies(), authCookieName)
			if authCookie == "" {
				t.Fatalf("重新登录未返回 %s Cookie: %#v", authCookieName, loginResponse.Result().Cookies())
			}

			listRequest := httptest.NewRequest(http.MethodGet, "/user/sessions", nil)
			listRequest.AddCookie(&http.Cookie{Name: authCookieName, Value: authCookie})
			listResponse := httptest.NewRecorder()
			fixture.router.ServeHTTP(listResponse, listRequest)
			if listResponse.Code != http.StatusOK {
				t.Fatalf("查询登录设备 HTTP = %d，body=%s", listResponse.Code, listResponse.Body.String())
			}
			var sessions credentialSessionResponse
			if err := json.Unmarshal(listResponse.Body.Bytes(), &sessions); err != nil {
				t.Fatalf("解析登录设备响应失败: %v", err)
			}
			if sessions.Code != Success || len(sessions.Data) != 1 || !sessions.Data[0].Current {
				t.Fatalf("重新登录后的登录设备 = %+v，期望仅有当前设备", sessions)
			}

			active, err := models.ListActiveUserSessions(fixture.user.ID, sessions.Data[0].SessionID, time.Now().Unix())
			if err != nil {
				t.Fatalf("查询活动会话失败: %v", err)
			}
			if len(active) != 1 || active[0].SessionID != sessions.Data[0].SessionID {
				t.Fatalf("活动会话 = %#v，期望仅保留重新登录会话 %s", active, sessions.Data[0].SessionID)
			}
			var allSessions []models.UserSession
			if err := db.Db.Where("user_id = ?", fixture.user.ID).Find(&allSessions).Error; err != nil {
				t.Fatalf("查询全部会话失败: %v", err)
			}
			if len(allSessions) != 4 {
				t.Fatalf("全部会话数量 = %d，期望 4", len(allSessions))
			}
			for _, session := range allSessions {
				if session.SessionID == sessions.Data[0].SessionID {
					continue
				}
				if session.RevokedAt == 0 || session.RevokeReason != "credential_changed" {
					t.Fatalf("旧会话未按凭据变更撤销: %#v", session)
				}
			}
			var expiredSession models.UserSession
			if err := db.Db.First(&expiredSession, fixture.expired.ID).Error; err != nil {
				t.Fatalf("读取过期会话失败: %v", err)
			}
			if expiredSession.RevokedAt == 0 || expiredSession.RevokeReason != "credential_changed" {
				t.Fatalf("过期旧会话未按凭据变更撤销: %#v", expiredSession)
			}
		})
	}
}

func TestChangeCredentialsRejectsUnchangedCredentials(t *testing.T) {
	tests := []struct {
		name        string
		username    string
		newPassword string
		message     string
	}{
		{
			name:        "新密码与当前密码相同",
			username:    "admin",
			newPassword: "old-password",
			message:     "新密码不能与当前密码相同",
		},
		{
			name:     "用户名和密码均未变化",
			username: "admin",
			message:  "用户名和密码未发生变化",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := setupCredentialChangeRouter(t)

			response := httptest.NewRecorder()
			fixture.router.ServeHTTP(response, newCredentialChangeRequest(t, fixture.current, fixture.currentCSRF, tt.username, tt.newPassword))

			if response.Code != http.StatusOK {
				t.Fatalf("修改凭据 HTTP = %d，body=%s", response.Code, response.Body.String())
			}
			var changed credentialChangeResponse
			if err := json.Unmarshal(response.Body.Bytes(), &changed); err != nil {
				t.Fatalf("解析修改凭据响应失败: %v", err)
			}
			if changed.Code == Success || changed.Data || changed.Message != tt.message {
				t.Fatalf("修改未变化凭据响应 = %+v，期望业务失败且提示 %q", changed, tt.message)
			}
			if hasClearedSessionCookies(response.Result().Cookies()) {
				t.Fatalf("拒绝未变化凭据时不应清除 Cookie: %#v", response.Result().Cookies())
			}
			if _, err := models.CheckLogin("admin", "old-password"); err != nil {
				t.Fatalf("拒绝未变化凭据后原密码应仍可登录: %v", err)
			}
			if _, err := models.GetActiveUserSession(fixture.current.SessionID, time.Now().Unix()); err != nil {
				t.Fatalf("拒绝未变化凭据后当前会话应保持有效: %v", err)
			}
			if _, err := models.GetActiveUserSession(fixture.other.SessionID, time.Now().Unix()); err != nil {
				t.Fatalf("拒绝未变化凭据后其他会话应保持有效: %v", err)
			}
		})
	}
}

func TestChangeCredentials_RollsBackWhenSessionRevocationFails(t *testing.T) {
	fixture := setupCredentialChangeRouter(t)
	const callbackName = "test:fail_credential_session_revocation"
	if err := db.Db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.UserSession{}).TableName() {
			tx.AddError(errors.New("simulate session revocation failure"))
		}
	}); err != nil {
		t.Fatalf("注册测试回调失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Db.Callback().Update().Remove(callbackName); err != nil {
			t.Fatalf("移除测试回调失败: %v", err)
		}
	})

	changeResponse := httptest.NewRecorder()
	fixture.router.ServeHTTP(changeResponse, newCredentialChangeRequest(t, fixture.current, fixture.currentCSRF, "admin", "new-password"))

	var changed credentialChangeResponse
	if err := json.Unmarshal(changeResponse.Body.Bytes(), &changed); err != nil {
		t.Fatalf("解析修改凭据响应失败: %v", err)
	}
	if changed.Code == Success || changed.Data {
		t.Fatalf("会话撤销失败时修改凭据响应 = %+v，期望失败", changed)
	}
	if strings.Contains(changeResponse.Body.String(), "simulate session revocation failure") {
		t.Fatalf("响应不应暴露数据库错误: %s", changeResponse.Body.String())
	}
	if _, err := models.CheckLogin("admin", "old-password"); err != nil {
		t.Fatalf("事务回滚后旧密码应可登录: %v", err)
	}
	if _, err := models.CheckLogin("admin", "new-password"); err == nil {
		t.Fatal("事务回滚后新密码不应可登录")
	}
	if _, err := models.GetActiveUserSession(fixture.current.SessionID, time.Now().Unix()); err != nil {
		t.Fatalf("事务回滚后原会话应保持有效: %v", err)
	}
	if _, err := models.GetActiveUserSession(fixture.other.SessionID, time.Now().Unix()); err != nil {
		t.Fatalf("事务回滚后其他活动会话应保持有效: %v", err)
	}
	var expiredSession models.UserSession
	if err := db.Db.First(&expiredSession, fixture.expired.ID).Error; err != nil {
		t.Fatalf("读取事务回滚后的过期会话失败: %v", err)
	}
	if expiredSession.RevokedAt != 0 || expiredSession.RevokeReason != "" {
		t.Fatalf("事务回滚后过期会话不应被撤销: %#v", expiredSession)
	}
	if hasClearedSessionCookies(changeResponse.Result().Cookies()) {
		t.Fatalf("事务失败不应清除 Cookie: %#v", changeResponse.Result().Cookies())
	}
	if fixture.user.Username != "admin" {
		t.Fatalf("测试用户用户名 = %q，期望 admin", fixture.user.Username)
	}
}

func TestChangeCredentialsRejectsSessionRevokedWhileWaitingForLock(t *testing.T) {
	fixture := setupCredentialChangeRouter(t)

	const updateCallbackName = "test:block_first_credential_update"
	firstUpdateStarted := make(chan struct{})
	releaseFirstUpdate := make(chan struct{})
	var updateOnce sync.Once
	if err := db.Db.Callback().Update().Before("gorm:update").Register(updateCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.User{}).TableName() {
			updateOnce.Do(func() {
				close(firstUpdateStarted)
				<-releaseFirstUpdate
			})
		}
	}); err != nil {
		t.Fatalf("注册用户更新阻塞回调失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Db.Callback().Update().Remove(updateCallbackName); err != nil {
			t.Fatalf("移除用户更新阻塞回调失败: %v", err)
		}
	})

	const queryCallbackName = "test:observe_second_session_authentication"
	secondSessionAuthenticated := make(chan struct{})
	var queryOnce sync.Once
	if err := db.Db.Callback().Query().Before("gorm:query").Register(queryCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.UserSession{}).TableName() {
			queryOnce.Do(func() {
				close(secondSessionAuthenticated)
			})
		}
	}); err != nil {
		t.Fatalf("注册会话查询观察回调失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Db.Callback().Query().Remove(queryCallbackName); err != nil {
			t.Fatalf("移除会话查询观察回调失败: %v", err)
		}
	})

	firstResponse := httptest.NewRecorder()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		fixture.router.ServeHTTP(firstResponse, newCredentialChangeRequest(t, fixture.current, fixture.currentCSRF, "admin", "new-password"))
	}()
	waitForCredentialTestSignal(t, "首个凭据更新进入事务", firstUpdateStarted)

	secondResponse := httptest.NewRecorder()
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		fixture.router.ServeHTTP(secondResponse, newCredentialChangeRequest(t, fixture.other, fixture.otherCSRF, "admin", "another-password"))
	}()
	waitForCredentialTestSignal(t, "第二个会话完成中间件鉴权", secondSessionAuthenticated)
	close(releaseFirstUpdate)
	waitForCredentialTestSignal(t, "首个凭据更新结束", firstDone)
	waitForCredentialTestSignal(t, "第二个凭据更新结束", secondDone)

	if firstResponse.Code != http.StatusOK {
		t.Fatalf("首个凭据更新 HTTP = %d，body=%s", firstResponse.Code, firstResponse.Body.String())
	}
	if secondResponse.Code != http.StatusUnauthorized {
		t.Fatalf("已撤销会话的并发凭据更新 HTTP = %d，期望 %d，body=%s", secondResponse.Code, http.StatusUnauthorized, secondResponse.Body.String())
	}
	if _, err := models.CheckLogin(fixture.user.Username, "new-password"); err != nil {
		t.Fatalf("首个凭据更新后的密码应可登录: %v", err)
	}
}

func TestChangeCredentialsRevokesConcurrentOldPasswordLogin(t *testing.T) {
	fixture := setupCredentialChangeRouter(t)

	const createCallbackName = "test:block_old_password_login_session_creation"
	loginSessionCreationStarted := make(chan struct{})
	releaseLoginSessionCreation := make(chan struct{})
	var createOnce sync.Once
	if err := db.Db.Callback().Create().Before("gorm:create").Register(createCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.UserSession{}).TableName() {
			createOnce.Do(func() {
				close(loginSessionCreationStarted)
				<-releaseLoginSessionCreation
			})
		}
	}); err != nil {
		t.Fatalf("注册登录会话创建阻塞回调失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Db.Callback().Create().Remove(createCallbackName); err != nil {
			t.Fatalf("移除登录会话创建阻塞回调失败: %v", err)
		}
	})

	loginResponse := httptest.NewRecorder()
	loginDone := make(chan struct{})
	loginRequest := newLoginRequest(t, "admin", "old-password")
	go func() {
		defer close(loginDone)
		fixture.router.ServeHTTP(loginResponse, loginRequest)
	}()
	waitForCredentialTestSignal(t, "旧密码登录开始创建会话", loginSessionCreationStarted)

	const queryCallbackName = "test:observe_credential_change_authentication"
	credentialChangeAuthenticated := make(chan struct{})
	var queryOnce sync.Once
	if err := db.Db.Callback().Query().Before("gorm:query").Register(queryCallbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == (models.UserSession{}).TableName() {
			queryOnce.Do(func() {
				close(credentialChangeAuthenticated)
			})
		}
	}); err != nil {
		t.Fatalf("注册凭据修改鉴权观察回调失败: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Db.Callback().Query().Remove(queryCallbackName); err != nil {
			t.Fatalf("移除凭据修改鉴权观察回调失败: %v", err)
		}
	})

	changeResponse := httptest.NewRecorder()
	changeDone := make(chan struct{})
	changeRequest := newCredentialChangeRequest(t, fixture.current, fixture.currentCSRF, "admin", "new-password")
	go func() {
		defer close(changeDone)
		fixture.router.ServeHTTP(changeResponse, changeRequest)
	}()
	waitForCredentialTestSignal(t, "凭据修改完成中间件鉴权", credentialChangeAuthenticated)
	close(releaseLoginSessionCreation)
	waitForCredentialTestSignal(t, "旧密码登录结束", loginDone)
	waitForCredentialTestSignal(t, "凭据修改结束", changeDone)

	if loginResponse.Code != http.StatusOK {
		t.Fatalf("旧密码并发登录 HTTP = %d，body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	if changeResponse.Code != http.StatusOK {
		t.Fatalf("凭据修改 HTTP = %d，body=%s", changeResponse.Code, changeResponse.Body.String())
	}
	authCookie := findCookieValue(loginResponse.Result().Cookies(), authCookieName)
	if authCookie == "" {
		t.Fatalf("旧密码并发登录未返回 %s Cookie: %#v", authCookieName, loginResponse.Result().Cookies())
	}

	oldLoginRequest := httptest.NewRequest(http.MethodGet, "/user/sessions", nil)
	oldLoginRequest.AddCookie(&http.Cookie{Name: authCookieName, Value: authCookie})
	oldLoginResponse := httptest.NewRecorder()
	fixture.router.ServeHTTP(oldLoginResponse, oldLoginRequest)
	if oldLoginResponse.Code != http.StatusUnauthorized {
		t.Fatalf("旧密码并发登录产生的会话 HTTP = %d，期望 %d，body=%s", oldLoginResponse.Code, http.StatusUnauthorized, oldLoginResponse.Body.String())
	}
	active, err := models.ListActiveUserSessions(fixture.user.ID, "", time.Now().Unix())
	if err != nil {
		t.Fatalf("查询活动会话失败: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("凭据修改后不应保留旧密码并发登录的活动会话: %#v", active)
	}
}

func TestChangeCredentialsKeepsAPIKeyUsable(t *testing.T) {
	fixture := setupCredentialChangeRouter(t)
	_, rawAPIKey, err := models.CreateAPIKey(fixture.user.ID, "automation")
	if err != nil {
		t.Fatalf("创建 API Key 失败: %v", err)
	}

	changeResponse := httptest.NewRecorder()
	fixture.router.ServeHTTP(changeResponse, newCredentialChangeRequest(t, fixture.current, fixture.currentCSRF, "admin", "new-password"))
	if changeResponse.Code != http.StatusOK {
		t.Fatalf("修改凭据 HTTP = %d，body=%s", changeResponse.Code, changeResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodGet, "/user/sessions", nil)
	request.Header.Set(apiKeyHeaderName, rawAPIKey)
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("凭据修改后的 API Key 请求 HTTP = %d，body=%s", response.Code, response.Body.String())
	}
	var sessions credentialSessionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("解析 API Key 会话列表响应失败: %v", err)
	}
	if sessions.Code != Success || len(sessions.Data) != 0 {
		t.Fatalf("API Key 会话列表 = %+v，期望不显示已撤销浏览器会话", sessions)
	}
}

func newLoginRequest(t *testing.T, username, password string) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]string{"username": username, "password": password})
	if err != nil {
		t.Fatalf("编码登录请求失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func findCookieValue(cookies []*http.Cookie, name string) string {
	for _, cookie := range cookies {
		if cookie.Name == name {
			return cookie.Value
		}
	}
	return ""
}

func waitForCredentialTestSignal(t *testing.T, name string, signal <-chan struct{}) {
	t.Helper()
	waitUntil := time.Now().Add(30 * time.Second)
	if deadline, ok := t.Deadline(); ok {
		testLimit := deadline.Add(-time.Second)
		if testLimit.Before(waitUntil) {
			waitUntil = testLimit
		}
	}
	waitDuration := time.Until(waitUntil)
	if waitDuration <= 0 {
		t.Fatalf("等待%s超时", name)
	}
	timer := time.NewTimer(waitDuration)
	defer timer.Stop()
	select {
	case <-signal:
	case <-timer.C:
		t.Fatalf("等待%s超时", name)
	}
}
