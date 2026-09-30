package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"qmediasync/internal/requests"
)

func TestLoginFailureMessageIsGeneric(t *testing.T) {
	msg := loginFailureMessage()
	if msg != "登录失败" {
		t.Fatalf("登录失败信息应统一为登录失败，实际为 %s", msg)
	}
}

func TestDisableTwoFactorRequiresPasswordAndCode(t *testing.T) {
	req := requests.DisableTwoFactorRequest{}
	if req.Validate() == nil {
		t.Fatal("空密码和空验证码不应允许关闭 2FA")
	}
	req.Password = "admin123"
	if req.Validate() == nil {
		t.Fatal("缺少 TOTP 验证码不应允许关闭 2FA")
	}
	req.TOTPCode = "123456"
	if req.Validate() != nil {
		t.Fatal("同时提供密码和 TOTP 验证码后请求格式应有效")
	}
}

func TestLoginFailuresHaveIdenticalResponses(t *testing.T) {
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
			const want = `{"code":500,"message":"登录失败","data":null}`
			if w.Code != http.StatusOK || w.Body.String() != want {
				t.Fatalf("登录失败响应不一致：HTTP=%d body=%s", w.Code, w.Body.String())
			}
			if len(w.Result().Cookies()) != 0 {
				t.Fatal("登录失败不得设置会话 Cookie")
			}
		})
	}
}
