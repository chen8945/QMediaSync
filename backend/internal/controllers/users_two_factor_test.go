package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
)

func TestLoginFailuresDistinguishCredentialRejectionFromInternalError(t *testing.T) {
	oldConfigDir := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = oldConfigDir })
	secret := "JBSWY3DPEHPK3PXP"
	encrypted, err := helpers.EncryptLocalSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := models.HashUserPassword("password123")
	if err != nil {
		t.Fatal(err)
	}
	// 避免固定六位错误码偶然成为当前窗口的有效验证码。
	validCodes := map[string]bool{}
	for offset := -60; offset <= 60; offset += 30 {
		code, err := helpers.GenerateTOTPCodeForTest(secret, time.Now().Add(time.Duration(offset)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		validCodes[code] = true
	}
	invalidCode := ""
	for i := range 10 {
		code := fmt.Sprintf("%06d", i)
		if !validCodes[code] {
			invalidCode = code
			break
		}
	}
	cases := []struct {
		name        string
		username    string
		password    string
		totp        string
		twoFactor   bool
		failSession bool
	}{
		{name: "用户名不存在", username: "missing", password: "password123"},
		{name: "密码错误", username: "admin", password: "wrong123"},
		{name: "验证码缺失", username: "admin", password: "password123", twoFactor: true},
		{name: "验证码错误", username: "admin", password: "password123", totp: invalidCode, twoFactor: true},
		{name: "创建会话失败", username: "admin", password: "password123", failSession: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, user, _, _ := setupAuthSecurityTest(t)
			oldLimiter := defaultLoginRateLimiter
			defaultLoginRateLimiter = NewLoginRateLimiter(5, 15*time.Minute, 15*time.Minute)
			t.Cleanup(func() { defaultLoginRateLimiter = oldLimiter })
			if err := db.Db.Model(user).Updates(map[string]any{
				"password": hash, "two_factor_enabled": tc.twoFactor, "two_factor_secret": encrypted,
			}).Error; err != nil {
				t.Fatal(err)
			}
			if tc.failSession {
				if err := db.Db.Migrator().DropTable(&models.UserSession{}); err != nil {
					t.Fatal(err)
				}
			}
			r := gin.New()
			r.POST("/api/login", LoginAction)
			payload, err := json.Marshal(map[string]string{"username": tc.username, "password": tc.password, "totp_code": tc.totp})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/login", bytes.NewReader(payload))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			var response httpapi.APIResponse[any]
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCode := http.StatusUnauthorized, "AUTHENTICATION_INVALID"
			if tc.failSession {
				wantStatus, wantCode = http.StatusInternalServerError, "INTERNAL_ERROR"
			}
			if w.Code != wantStatus || response.Status != w.Code || response.ErrorCode != wantCode || response.Data != nil || len(response.FieldErrors) != 0 || !strings.HasPrefix(response.Instance, "/request-errors/") {
				t.Fatalf("登录失败分类错误：HTTP=%d body=%s", w.Code, w.Body.String())
			}
			if !tc.failSession && (response.Message != "登录失败" || w.Header().Get("WWW-Authenticate") != `QMediaSyncSession realm="QMediaSync"`) {
				t.Fatalf("凭据拒绝暴露匹配细节或缺少 challenge：%s", w.Body.String())
			}
			if tc.failSession && (w.Header().Get("WWW-Authenticate") != "" || strings.Contains(w.Body.String(), "no such table")) {
				t.Fatalf("内部登录故障被误分类或泄露数据库信息：%s", w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatal("登录失败响应不应被缓存")
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("登录失败不得设置会话 Cookie")
			}
		})
	}
}

func TestLoginInvalidInputIsNotCredentialRejection(t *testing.T) {
	for _, tc := range []struct {
		name, payload, field string
	}{
		{name: "JSON 不完整", payload: `{"username":`},
		{name: "密码缺失", payload: `{"username":"admin"}`, field: "password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/api/login", LoginAction)
			request := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(tc.payload))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var body httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusBadRequest || body.Status != response.Code || body.ErrorCode != "INVALID_REQUEST" || body.Data != nil || body.Instance == "" || response.Header().Get("WWW-Authenticate") != "" {
				t.Fatalf("输入错误被误认为凭据失效：%d %s", response.Code, response.Body)
			}
			if tc.field != "" && (len(body.FieldErrors) != 1 || body.FieldErrors[0].Field != tc.field) {
				t.Fatalf("校验错误缺少字段定位：%s", response.Body)
			}
		})
	}
}

func TestLoginRateLimitResponseCarriesWaitHeader(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wait    time.Duration
		seconds int
	}{
		{name: "不足一秒", wait: 200 * time.Millisecond, seconds: 1},
		{name: "非整秒向上取整", wait: 1500 * time.Millisecond, seconds: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(response)
			context.Request = httptest.NewRequest(http.MethodPost, "/api/login", nil)
			writeLoginRateLimited(context, tc.wait)
			seconds, err := strconv.Atoi(response.Header().Get("Retry-After"))
			var body httpapi.APIResponse[any]
			if jsonErr := json.Unmarshal(response.Body.Bytes(), &body); jsonErr != nil {
				t.Fatal(jsonErr)
			}
			if err != nil || seconds != tc.seconds || response.Code != http.StatusTooManyRequests || body.Status != response.Code || body.ErrorCode != "RATE_LIMITED" {
				t.Fatalf("限流等待协议错误：%v %s", response.Header(), response.Body)
			}
		})
	}
}
