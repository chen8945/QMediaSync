package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	v115auth "qmediasync/internal/v115/auth"
	v115open "qmediasync/internal/v115/openapi"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
)

func setupAccountControllerTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	v115auth.ResetAuthorizationSessionsForTest()
	t.Cleanup(v115auth.ResetAuthorizationSessionsForTest)
	setupControllerTestDB(t, &models.Account{})
}

func decodeAPIMessage(t *testing.T, body *bytes.Buffer) httpapi.APIResponse[any] {
	t.Helper()
	var resp httpapi.APIResponse[any]
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, body.String())
	}
	return resp
}

func TestGetAccountList_ReturnsCustom115AppNameAndAppId(t *testing.T) {
	setupAccountControllerTest(t)
	account := models.Account{
		Name:       "家庭账号",
		SourceType: models.SourceType115,
		AppId:      "custom-app-id",
		AppIdName:  "家庭影音",
	}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/account/list", nil)

	GetAccountList(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"app_id_name":"家庭影音"`) {
		t.Fatalf("自定义应用名未返回: %s", body)
	}
	if !strings.Contains(body, `"app_id":"custom-app-id"`) {
		t.Fatalf("自定义 APP ID 未返回: %s", body)
	}
}

func TestGetAccountListReturnsAuthorizationStateWithoutToken(t *testing.T) {
	setupAccountControllerTest(t)
	accounts := []models.Account{
		{
			Name:       "已授权账号",
			SourceType: models.SourceType115,
			AppId:      "custom-app-id",
			AppIdName:  "家庭影音",
			Token:      "secret-token",
		},
		{
			Name:              "失效账号",
			SourceType:        models.SourceType115,
			AppId:             "custom-app-id-2",
			AppIdName:         "家庭影音2",
			TokenFailedReason: "访问凭证已失效",
		},
	}
	for i := range accounts {
		if err := db.Db.Create(&accounts[i]).Error; err != nil {
			t.Fatalf("创建测试账号失败: %v", err)
		}
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/account/list", nil)

	GetAccountList(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
	}
	var response struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析账号列表响应失败: %v，响应: %s", err, w.Body.String())
	}
	if len(response.Data) != 2 {
		t.Fatalf("账号列表数量 = %d，期望 2", len(response.Data))
	}
	byName := make(map[string]map[string]any, len(response.Data))
	for _, item := range response.Data {
		if _, ok := item["token"]; ok {
			t.Fatalf("账号列表不应返回原始 Token: %#v", item)
		}
		name, ok := item["name"].(string)
		if !ok {
			t.Fatalf("账号列表缺少账号名称: %#v", item)
		}
		byName[name] = item
	}
	if authorized, ok := byName["已授权账号"]["authorized"].(bool); !ok || !authorized {
		t.Fatalf("已授权账号 authorized = %#v，期望 true", byName["已授权账号"]["authorized"])
	}
	if authorized, ok := byName["失效账号"]["authorized"].(bool); !ok || authorized {
		t.Fatalf("失效账号 authorized = %#v，期望 false", byName["失效账号"]["authorized"])
	}
}

func TestUpdateAccountInfo_UpdatesRemarkAndCustomAppNameOnly(t *testing.T) {
	setupAccountControllerTest(t)
	account := models.Account{
		Name:         "旧备注",
		SourceType:   models.SourceType115,
		AppId:        "custom-app-id",
		AppIdName:    "旧应用",
		Token:        "token",
		RefreshToken: "refresh-token",
		UserId:       "115-user",
		Username:     "115-name",
	}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	payload, err := json.Marshal(map[string]any{
		"id":          account.ID,
		"name":        "新备注",
		"app_id_name": "新应用",
	})
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/account/update", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")

	UpdateAccountInfo(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), `"status":200`) {
		t.Fatalf("响应未返回成功 status: %s", w.Body.String())
	}

	var updated models.Account
	if err := db.Db.First(&updated, account.ID).Error; err != nil {
		t.Fatalf("查询更新后账号失败: %v", err)
	}
	if updated.Name != "新备注" {
		t.Fatalf("账号备注 = %q，期望 新备注", updated.Name)
	}
	if updated.AppIdName != "新应用" {
		t.Fatalf("应用名 = %q，期望 新应用", updated.AppIdName)
	}
	if updated.AppId != "custom-app-id" || updated.Token != "token" ||
		updated.RefreshToken != "refresh-token" || updated.UserId != "115-user" ||
		updated.Username != "115-name" {
		t.Fatalf("更新账号资料影响了授权字段: %#v", updated)
	}
}

func TestDeleteAccount(t *testing.T) {
	for _, tc := range []struct {
		name         string
		hasSyncPath  bool
		failDelete   bool
		wantStatus   int
		wantCode     string
		wantAccounts int
	}{
		{name: "linked sync directory", hasSyncPath: true, wantStatus: http.StatusConflict, wantCode: "ACCOUNT_HAS_SYNC_PATHS", wantAccounts: 1},
		{name: "no linked directory", wantStatus: http.StatusOK},
		{name: "database delete failure", failDelete: true, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL_ERROR", wantAccounts: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupAccountControllerTest(t)
			if err := db.Db.AutoMigrate(&models.SyncPath{}); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			oldLogger := helpers.AppLogger
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			t.Cleanup(func() { helpers.AppLogger = oldLogger })
			account := models.Account{Name: "delete-account", SourceType: models.SourceType115, Token: "existing-token"}
			if err := db.Db.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			path := models.SyncPath{AccountId: account.ID, SourceType: account.SourceType, BaseCid: "linked-directory"}
			if tc.hasSyncPath {
				if err := db.Db.Create(&path).Error; err != nil {
					t.Fatal(err)
				}
			}
			const databaseError = "account-delete-database-fault-sentinel"
			if tc.failDelete {
				if err := db.Db.Exec(`CREATE TRIGGER fail_account_delete BEFORE DELETE ON account
					BEGIN SELECT RAISE(FAIL, '` + databaseError + `'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			payload, err := json.Marshal(map[string]any{"id": account.ID})
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/api/account/delete", DeleteAccount)
			request := httptest.NewRequest(http.MethodPost, "/api/account/delete", bytes.NewReader(payload))
			request.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, request)
			response := decodeAPIMessage(t, w.Body)
			if w.Code != tc.wantStatus || response.Status != w.Code || response.ErrorCode != tc.wantCode {
				t.Errorf("账号删除错误分类不正确: HTTP=%d response=%+v", w.Code, response)
			}
			if tc.wantCode != "" && (response.Message == "" || response.Instance == "") {
				t.Errorf("删除失败缺少安全说明或诊断关联: %+v", response)
			}
			if tc.hasSyncPath {
				if !strings.Contains(response.Message, "同步目录") || !strings.Contains(response.Message, "先删除") {
					t.Errorf("关联目录拒绝缺少原因和处理提示: %q", response.Message)
				}
				if strings.Count(logs.String(), response.Instance) != 1 || strings.Count(logs.String(), "\n") != 1 {
					t.Errorf("关联目录拒绝应只记录一次请求诊断: %s", logs.String())
				}
			}
			if strings.Contains(w.Body.String(), databaseError) {
				t.Error("删除失败响应泄露数据库诊断")
			}
			var accounts []models.Account
			if err := db.Db.Find(&accounts).Error; err != nil {
				t.Fatal(err)
			}
			if len(accounts) != tc.wantAccounts {
				t.Fatalf("删除后的账号数量 = %d，期望 %d", len(accounts), tc.wantAccounts)
			}
			if len(accounts) == 1 && (accounts[0].ID != account.ID || accounts[0].Name != account.Name || accounts[0].Token != account.Token) {
				t.Error("拒绝删除改变了原账号")
			}
			var paths []models.SyncPath
			if err := db.Db.Find(&paths).Error; err != nil {
				t.Fatal(err)
			}
			if tc.hasSyncPath && (len(paths) != 1 || paths[0].ID != path.ID || paths[0].AccountId != account.ID || paths[0].BaseCid != path.BaseCid) {
				t.Fatalf("拒绝删除改变了关联目录: %+v", paths)
			}
		})
	}
}

func TestCreateTmpAccountV115AuthSource(t *testing.T) {
	setupAccountControllerTest(t)
	cases := []struct {
		name          string
		payload       map[string]any
		wantAppID     string
		wantAppIDName string
		wantProvider  string
	}{
		{
			name: "内置 APP ID 保存数字 APP ID",
			payload: map[string]any{
				"source_type":      "115",
				"name":             "qms",
				"auth_source_type": "built_in_appid",
				"auth_provider":    "official_pkce",
				"app_id":           "100197849",
				"app_id_name":      "QMediaSync",
			},
			wantAppID:     "100197849",
			wantAppIDName: "QMediaSync",
			wantProvider:  "official_pkce",
		},
		{
			name: "内置中转保存兼容字符串",
			payload: map[string]any{
				"source_type":      "115",
				"name":             "relay",
				"auth_source_type": "built_in_relay",
				"auth_provider":    "qmediasync",
				"app_id_name":      "QMediaSync",
			},
			wantAppID:     "QMediaSync",
			wantAppIDName: "QMediaSync",
			wantProvider:  "qmediasync",
		},
		{
			name: "自定义 APP ID 保存用户输入",
			payload: map[string]any{
				"source_type":      "115",
				"name":             "custom",
				"auth_source_type": "custom_appid",
				"auth_provider":    "official_pkce",
				"app_id":           "100000000",
				"custom_app_name":  "我的应用",
			},
			wantAppID:     "100000000",
			wantAppIDName: "我的应用",
			wantProvider:  "official_pkce",
		},
		{
			name: "第三方授权服务保存显式 provider",
			payload: map[string]any{
				"source_type":      "115",
				"name":             "moviepilot",
				"auth_source_type": "third_party_service",
				"auth_provider":    "moviepilot",
				"app_id":           "100197847",
				"app_id_name":      "MoviePilot-115",
			},
			wantAppID:     "100197847",
			wantAppIDName: "MoviePilot-115",
			wantProvider:  "moviepilot",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("构造请求失败: %v", err)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/account/add", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			CreateTmpAccount(c)

			if w.Code != http.StatusCreated {
				t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusCreated)
			}
			if !strings.Contains(w.Body.String(), `"status":201`) {
				t.Fatalf("响应未返回成功 status: %s", w.Body.String())
			}
			var account models.Account
			if err := db.Db.Where("name = ?", tt.payload["name"]).First(&account).Error; err != nil {
				t.Fatalf("查询创建后账号失败: %v", err)
			}
			if account.AppId != tt.wantAppID {
				t.Fatalf("app_id = %q，期望 %q", account.AppId, tt.wantAppID)
			}
			if account.AppIdName != tt.wantAppIDName {
				t.Fatalf("app_id_name = %q，期望 %q", account.AppIdName, tt.wantAppIDName)
			}
			if string(account.AuthProvider) != tt.wantProvider {
				t.Fatalf("auth_provider = %q，期望 %q", account.AuthProvider, tt.wantProvider)
			}
		})
	}

	body, err := json.Marshal(map[string]any{
		"source_type":      "115",
		"name":             "bad",
		"auth_source_type": "unknown",
		"auth_provider":    "official_pkce",
	})
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/account/add", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	CreateTmpAccount(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusBadRequest)
	}
	if decodeAPIMessage(t, w.Body).ErrorCode != "INVALID_REQUEST" {
		t.Fatalf("不支持的授权来源未返回 INVALID_REQUEST: %s", w.Body.String())
	}
}

func TestPrepareAccountAuthorizationBindsSessionAndRejectsCrossSource(t *testing.T) {
	setupAccountControllerTest(t)
	v115auth.ResetOAuthStatesForTest()
	t.Cleanup(v115auth.ResetOAuthStatesForTest)
	account := models.Account{Name: "原账号", SourceType: models.SourceType115, AppId: "old-app", Token: "old-token", RefreshToken: "old-refresh"}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}
	saveOpen115AuthState(account.ID, &v115open.QrCodeDataReturn{Uid: "legacy-qr"})
	v115auth.SaveOAuthState(v115auth.OAuthState{
		State:     "legacy-oauth",
		AccountID: account.ID,
		Provider:  v115auth.ProviderMoviePilot,
		Source:    v115auth.Source{SourceType: v115auth.SourceTypeThirdPartyService, Provider: v115auth.ProviderMoviePilot, AppID: "100197847"},
	})

	payload, err := json.Marshal(map[string]any{
		"account_id":       account.ID,
		"source_type":      "115",
		"auth_source_type": "built_in_appid",
		"auth_provider":    "official_pkce",
		"app_id":           "100197849",
		"app_id_name":      "QMediaSync",
		"confirmed":        true,
	})
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/account/authorization/prepare", bytes.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json")
	PrepareAccountAuthorization(c)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":200`) {
		t.Fatalf("准备授权失败: status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			AuthorizationID string `json:"authorization_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析授权会话响应失败: %v", err)
	}
	if response.Data.AuthorizationID == "" {
		t.Fatal("响应缺少 authorization_id")
	}
	if session, ok := v115auth.GetAuthorizationSession(response.Data.AuthorizationID, account.ID); !ok || session.Source.AppID != "100197849" {
		t.Fatalf("授权会话未绑定目标来源: %#v, %v", session, ok)
	}
	if _, ok := getOpen115AuthState(account.ID, "legacy-qr"); ok {
		t.Fatal("准备更换授权后，旧二维码状态应被清理")
	}
	if _, ok := v115auth.GetOAuthState("legacy-oauth", v115auth.ProviderMoviePilot); ok {
		t.Fatal("准备更换授权后，旧 OAuth 状态应被清理")
	}

	duplicateRecorder := httptest.NewRecorder()
	duplicateContext, _ := gin.CreateTestContext(duplicateRecorder)
	duplicateContext.Request = httptest.NewRequest(http.MethodPost, "/account/authorization/prepare", bytes.NewReader(payload))
	duplicateContext.Request.Header.Set("Content-Type", "application/json")
	PrepareAccountAuthorization(duplicateContext)
	if duplicateRecorder.Code != http.StatusConflict || decodeAPIMessage(t, duplicateRecorder.Body).ErrorCode != "AUTHORIZATION_IN_PROGRESS" {
		t.Fatalf("同一账号的重复授权流程未被拒绝: status=%d body=%s", duplicateRecorder.Code, duplicateRecorder.Body.String())
	}

	crossSourcePayload := map[string]any{
		"account_id":  account.ID,
		"source_type": "baidupan",
		"confirmed":   true,
	}
	crossBody, err := json.Marshal(crossSourcePayload)
	if err != nil {
		t.Fatalf("构造跨来源请求失败: %v", err)
	}
	crossRecorder := httptest.NewRecorder()
	crossContext, _ := gin.CreateTestContext(crossRecorder)
	crossContext.Request = httptest.NewRequest(http.MethodPost, "/account/authorization/prepare", bytes.NewReader(crossBody))
	crossContext.Request.Header.Set("Content-Type", "application/json")
	PrepareAccountAuthorization(crossContext)
	if crossRecorder.Code != http.StatusBadRequest || decodeAPIMessage(t, crossRecorder.Body).ErrorCode != "INVALID_REQUEST" {
		t.Fatalf("跨来源请求未被拒绝: status=%d body=%s", crossRecorder.Code, crossRecorder.Body.String())
	}
}

func TestGetV115AuthorizationTargetKeepsLegacyDeprecatedSourceButRejectsReplacement(t *testing.T) {
	account := &models.Account{
		ID:         9,
		SourceType: models.SourceType115,
		AppId:      "100197665",
		AppIdName:  "Q115-STRM",
		Token:      "old-token",
	}

	source, client, err := getV115AuthorizationTarget(account, "")
	if err != nil || client == nil || !source.Deprecated {
		t.Fatalf("历史账号无会话授权路径不应被拒绝: source=%#v client=%#v err=%v", source, client, err)
	}

	v115auth.ResetAuthorizationSessionsForTest()
	t.Cleanup(v115auth.ResetAuthorizationSessionsForTest)
	session, err := v115auth.CreateAuthorizationSession(account.ID, source)
	if err != nil {
		t.Fatalf("创建测试授权会话失败: %v", err)
	}
	_, replacementClient, err := getV115AuthorizationTarget(account, session.ID)
	fieldErr, ok := errors.AsType[validation.Error](err)
	if !ok || fieldErr.Field != "auth_source_type" {
		t.Fatalf("更换授权目标应拒绝废弃来源: client=%#v err=%v", replacementClient, err)
	}
	if replacementClient != nil {
		t.Fatal("拒绝废弃更换目标时不应创建客户端")
	}
}

func TestBaiduOAuthRejectsNonBaiduAccount(t *testing.T) {
	setupAccountControllerTest(t)
	account := models.Account{Name: "115 账号", SourceType: models.SourceType115, AppId: "100197849"}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	urlRecorder := httptest.NewRecorder()
	urlContext, _ := gin.CreateTestContext(urlRecorder)
	urlContext.Request = httptest.NewRequest(http.MethodGet, "/baidupan/oauth-url?account_id=1&redirect_url=http%3A%2F%2Flocalhost%2Fcallback", nil)
	GetBaiDuPanOAuthUrl(urlContext)
	if urlRecorder.Code != http.StatusBadRequest || decodeAPIMessage(t, urlRecorder.Body).ErrorCode != "INVALID_REQUEST" {
		t.Fatalf("百度 OAuth 地址接口未拒绝 115 账号: status=%d body=%s", urlRecorder.Code, urlRecorder.Body.String())
	}

	body, err := json.Marshal(map[string]any{"account_id": account.ID, "data": "not-a-baidu-token"})
	if err != nil {
		t.Fatalf("构造百度 OAuth 确认请求失败: %v", err)
	}
	confirmRecorder := httptest.NewRecorder()
	confirmContext, _ := gin.CreateTestContext(confirmRecorder)
	confirmContext.Request = httptest.NewRequest(http.MethodPost, "/baidupan/oauth-confirm", bytes.NewReader(body))
	confirmContext.Request.Header.Set("Content-Type", "application/json")
	ConfirmBaiDuPanOAuthCode(confirmContext)
	if confirmRecorder.Code != http.StatusBadRequest || decodeAPIMessage(t, confirmRecorder.Body).ErrorCode != "INVALID_REQUEST" {
		t.Fatalf("百度 OAuth 确认接口未拒绝 115 账号: status=%d body=%s", confirmRecorder.Code, confirmRecorder.Body.String())
	}
}

func TestCancelAccountAuthorizationInvalidatesAllPendingState(t *testing.T) {
	setupAccountControllerTest(t)
	account := models.Account{Name: "待取消账号", SourceType: models.SourceType115, AppId: "old-app"}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}
	source := v115auth.Source{SourceType: v115auth.SourceTypeBuiltInAppID, Provider: v115auth.ProviderOfficialPKCE, AppID: "100197849"}
	session, err := v115auth.CreateAuthorizationSession(account.ID, source)
	if err != nil {
		t.Fatalf("创建授权会话失败: %v", err)
	}
	saveOpen115AuthState(account.ID, &v115open.QrCodeDataReturn{Uid: "cancel-qr"}, session.ID)
	v115auth.SaveOAuthState(v115auth.OAuthState{
		State:           "cancel-oauth",
		AccountID:       account.ID,
		Provider:        v115auth.ProviderMoviePilot,
		AuthorizationID: session.ID,
		Source:          source,
	})

	body, err := json.Marshal(map[string]any{"account_id": account.ID, "authorization_id": session.ID})
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/account/authorization/cancel", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	CancelAccountAuthorization(c)

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":200`) {
		t.Fatalf("取消授权失败: status=%d body=%s", w.Code, w.Body.String())
	}
	if _, ok := v115auth.GetAuthorizationSession(session.ID, account.ID); ok {
		t.Fatal("取消后授权会话不应继续存在")
	}
	if _, ok := getOpen115AuthState(account.ID, "cancel-qr"); ok {
		t.Fatal("取消后 QR 状态不应继续存在")
	}
	if _, ok := v115auth.GetOAuthState("cancel-oauth", v115auth.ProviderMoviePilot); ok {
		t.Fatal("取消后 OAuth 状态不应继续存在")
	}
}

func TestGetOAuthStatusRejectsUnboundAuthorizationID(t *testing.T) {
	setupAccountControllerTest(t)
	account := models.Account{Name: "OAuth 账号", SourceType: models.SourceType115, AppId: "old-app", Token: "old-token"}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}
	v115auth.SaveOAuthState(v115auth.OAuthState{
		State:     "legacy-oauth-state",
		AccountID: account.ID,
		Provider:  v115auth.ProviderMoviePilot,
		Source:    account.V115AuthSource(),
	})
	t.Cleanup(v115auth.ResetOAuthStatesForTest)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/115/oauth-status?account_id=1&state=legacy-oauth-state&authorization_id=unbound", nil)
	GetOAuthStatus(c)

	if w.Code != http.StatusBadRequest || decodeAPIMessage(t, w.Body).ErrorCode != "AUTHORIZATION_MISMATCH" {
		t.Fatalf("未绑定授权会话未被拒绝: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateTmpAccountReturnsValidationField(t *testing.T) {
	setupAccountControllerTest(t)
	body, err := json.Marshal(map[string]any{
		"source_type": "115",
		"name":        " ",
	})
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/account/add", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	CreateTmpAccount(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusBadRequest)
	}
	resp := decodeAPIMessage(t, w.Body)
	if resp.Status != w.Code || resp.ErrorCode != "INVALID_REQUEST" || resp.Message == "" ||
		len(resp.FieldErrors) != 1 || resp.FieldErrors[0].Field != "name" || resp.FieldErrors[0].Message == "" {
		t.Fatalf("账号校验失败未返回字段定位: %s", w.Body.String())
	}
}

func TestCreateOpenListAccountReturnsValidationFields(t *testing.T) {
	setupAccountControllerTest(t)
	cases := []struct {
		name      string
		payload   map[string]any
		wantField string
	}{
		{
			name: "缺少访问地址",
			payload: map[string]any{
				"auth_type": "password",
				"username":  "admin",
				"password":  "pass",
			},
			wantField: "base_url",
		},
		{
			name: "用户名密码方式缺少用户名",
			payload: map[string]any{
				"base_url":  "http://openlist.example.com",
				"auth_type": "password",
				"password":  "pass",
			},
			wantField: "username",
		},
		{
			name: "用户名密码方式缺少密码",
			payload: map[string]any{
				"base_url":  "http://openlist.example.com",
				"auth_type": "password",
				"username":  "admin",
			},
			wantField: "password",
		},
		{
			name: "Token 方式缺少令牌",
			payload: map[string]any{
				"base_url":  "http://openlist.example.com",
				"auth_type": "token",
			},
			wantField: "token",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatalf("构造请求失败: %v", err)
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(
				http.MethodPost,
				"/account/openlist",
				bytes.NewReader(body),
			)
			c.Request.Header.Set("Content-Type", "application/json")

			CreateOpenListAccount(c)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusBadRequest)
			}
			resp := decodeAPIMessage(t, w.Body)
			if resp.Status != w.Code || resp.ErrorCode != "INVALID_REQUEST" || resp.Message == "" ||
				len(resp.FieldErrors) != 1 || resp.FieldErrors[0].Field != tt.wantField || resp.FieldErrors[0].Message == "" {
				t.Fatalf("OpenList 校验失败未定位到 %s: %s", tt.wantField, w.Body.String())
			}
		})
	}
}

func TestGetAccountListKeepsThirdPartyProvider(t *testing.T) {
	setupAccountControllerTest(t)
	account := models.Account{
		Name:           "MoviePilot",
		SourceType:     models.SourceType115,
		AppId:          "100197847",
		AppIdName:      "MoviePilot-115",
		AuthSourceType: "third_party_service",
		AuthProvider:   "moviepilot",
	}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/account/list", nil)

	GetAccountList(c)

	body := w.Body.String()
	if !strings.Contains(body, `"auth_source_type":"third_party_service"`) {
		t.Fatalf("第三方来源未保留: %s", body)
	}
	if !strings.Contains(body, `"auth_provider":"moviepilot"`) {
		t.Fatalf("第三方 provider 未保留: %s", body)
	}
}

func TestAccountLookupSeparatesMissingFromDatabaseFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dropTable  bool
		wantStatus int
		wantCode   string
	}{
		{name: "missing account", wantStatus: http.StatusNotFound, wantCode: "ACCOUNT_NOT_FOUND"},
		{name: "database failure", dropTable: true, wantStatus: http.StatusInternalServerError, wantCode: "INTERNAL_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupAccountControllerTest(t)
			var logs bytes.Buffer
			oldLogger := helpers.AppLogger
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			t.Cleanup(func() { helpers.AppLogger = oldLogger })
			if tc.dropTable {
				if err := db.Db.Migrator().DropTable(&models.Account{}); err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/115/status?account_id=1", nil)
			Get115Status(c)
			response := decodeAPIMessage(t, w.Body)
			if w.Code != tc.wantStatus || response.Status != w.Code || response.ErrorCode != tc.wantCode ||
				response.Instance == "" || response.Message == "" || strings.Contains(response.Message, "no such table") {
				t.Fatalf("账号查询错误分类不正确或泄露数据库诊断: HTTP=%d response=%+v", w.Code, response)
			}
			if strings.Count(logs.String(), response.Instance) != 1 || strings.Count(logs.String(), "\n") != 1 {
				t.Fatalf("账号查询失败应只记录一次请求诊断：%s", logs.String())
			}
		})
	}
}

func TestCreateTmpAccountNameConflictPreservesExistingAccount(t *testing.T) {
	setupAccountControllerTest(t)
	existing := models.Account{Name: "duplicate-name", SourceType: models.SourceType115, Token: "existing-token"}
	if err := db.Db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/account/add", strings.NewReader(`{"source_type":"115","name":"duplicate-name","auth_source_type":"built_in_appid","auth_provider":"official_pkce","app_id":"100197849"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	CreateTmpAccount(c)
	response := decodeAPIMessage(t, w.Body)
	if w.Code != http.StatusConflict || response.Status != w.Code || response.ErrorCode != "ACCOUNT_NAME_TAKEN" {
		t.Fatalf("重复账号未返回冲突: HTTP=%d response=%+v", w.Code, response)
	}
	var accounts []models.Account
	if err := db.Db.Find(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != existing.ID || accounts[0].Token != existing.Token {
		t.Fatalf("冲突请求改变了原账号: %#v", accounts)
	}
}

func TestOpenListVerificationFailureUsesSafeRequestDiagnostic(t *testing.T) {
	for _, test := range []struct {
		name            string
		updating, reuse bool
	}{
		{name: "create"},
		{name: "update", updating: true},
		{name: "reuse credential", updating: true, reuse: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			token := "request-token-sentinel"
			if test.reuse {
				token = "fixture-token"
			}
			account := setupOpenListFileOperationTest(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 500, "message": "fixture verification failed: " + token})
			})
			var logs bytes.Buffer
			oldLogger := helpers.AppLogger
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			t.Cleanup(func() { helpers.AppLogger = oldLogger })
			payload := map[string]any{"base_url": account.BaseUrl, "auth_type": "token", "token": token}
			if test.updating {
				payload["id"] = account.ID
			}
			if test.reuse {
				delete(payload, "token")
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/account/openlist", CreateOpenListAccount)
			request := httptest.NewRequest(http.MethodPost, "/account/openlist", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			result := decodeAPIMessage(t, response.Body)
			if response.Code != http.StatusBadGateway || result.ErrorCode != "UPSTREAM_ERROR" || result.Instance == "" {
				t.Fatalf("验证失败分类错误：%d %+v", response.Code, result)
			}
			if strings.Contains(logs.String(), token) || strings.Contains(response.Body.String(), token) {
				t.Fatal("请求失败日志或响应泄露已知凭据")
			}
			if strings.Count(logs.String(), result.Instance) != 1 || strings.Count(logs.String(), "\n") != 1 {
				t.Fatalf("失败应只由公共出口记录关联诊断：%s", logs.String())
			}
		})
	}
}
