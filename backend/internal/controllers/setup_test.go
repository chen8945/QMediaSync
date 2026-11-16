package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func setupInitialSetupControllerTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	setupControllerTestDB(t, &models.User{})
	configureInitialSetupTokenForTest("setup-token")
	t.Cleanup(func() {
		DisableInitialSetup()
	})
}

func TestSetupStatusActionRequired(t *testing.T) {
	setupInitialSetupControllerTest(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)

	SetupStatusAction(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d，期望 200", w.Code)
	}
	var response httpapi.APIResponse[setupStatusResponse]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != w.Code || !response.Data.Required {
		t.Fatalf("初始化状态响应错误: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "setup-token") {
		t.Fatalf("初始化状态不应泄露 token: %s", w.Body.String())
	}
}

func TestCreateInitialAdminActionRejectsBadToken(t *testing.T) {
	setupInitialSetupControllerTest(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := strings.NewReader(`{"setup_token":"bad-token","username":"admin","password":"admin123"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/setup/admin", body)
	c.Request.Header.Set("Content-Type", "application/json")

	CreateInitialAdminAction(c)

	if w.Code != http.StatusForbidden {
		t.Fatalf("HTTP = %d，期望 %d", w.Code, http.StatusForbidden)
	}
	var response httpapi.APIResponse[any]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != w.Code || response.ErrorCode != "SETUP_TOKEN_INVALID" || response.Instance == "" || response.Data != nil {
		t.Fatalf("错误信息不正确: %s", w.Body.String())
	}
	var count int64
	if err := db.Db.Model(&models.User{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("非法初始化码创建了用户：count=%d err=%v", count, err)
	}
}

func TestCreateInitialAdminActionCreatesUserAndDisablesSetup(t *testing.T) {
	setupInitialSetupControllerTest(t)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := strings.NewReader(`{"setup_token":"setup-token","username":"admin","password":"admin123"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/setup/admin", body)
	c.Request.Header.Set("Content-Type", "application/json")

	CreateInitialAdminAction(c)

	if w.Code != http.StatusCreated {
		t.Fatalf("HTTP = %d，期望 %d", w.Code, http.StatusCreated)
	}
	var response httpapi.APIResponse[struct {
		Username string `json:"username"`
	}]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != w.Code || response.Data.Username != "admin" || response.ErrorCode != "" {
		t.Fatalf("创建首个管理员响应错误: %s", w.Body.String())
	}

	var count int64
	if err := db.Db.Model(&models.User{}).Count(&count).Error; err != nil {
		t.Fatalf("统计用户失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("用户数量 = %d，期望 1", count)
	}

	w = httptest.NewRecorder()
	c, _ = gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
	SetupStatusAction(c)
	var status httpapi.APIResponse[setupStatusResponse]
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != http.StatusOK || status.Data.Required {
		t.Fatalf("创建后初始化状态应关闭: %s", w.Body.String())
	}
}
