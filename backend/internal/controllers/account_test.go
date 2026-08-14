package controllers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115auth"
	"qmediasync/internal/v115open"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
)

type apiMessageResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func setupAccountControllerTest(t *testing.T) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	v115auth.ResetAuthorizationSessionsForTest()
	t.Cleanup(v115auth.ResetAuthorizationSessionsForTest)
	setupControllerTestDB(t, &models.Account{})
}

func decodeAPIMessage(t *testing.T, body *bytes.Buffer) apiMessageResponse {
	t.Helper()
	var resp apiMessageResponse
	if err := json.Unmarshal(body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v，响应: %s", err, body.String())
	}
	return resp
}

func TestFriendlyAccountValidationMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "账号 ID 错误",
			err:  validation.New("id", "必须大于 0"),
			want: "请选择要操作的账号",
		},
		{
			name: "账号备注包含控制字符",
			err:  validation.New("name", "不能包含控制字符"),
			want: "账号备注不能包含特殊控制字符",
		},
		{
			name: "自定义应用名包含控制字符",
			err:  validation.New("custom_app_name", "不能包含控制字符"),
			want: "自定义应用名不能包含特殊控制字符",
		},
		{
			name: "未知字段不暴露字段名",
			err:  validation.New("unknown", "不能为空"),
			want: "请求参数不正确，请检查后再试",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := friendlyAccountValidationMessage(tt.err)
			if got != tt.want {
				t.Fatalf("friendlyAccountValidationMessage() = %q，期望 %q", got, tt.want)
			}
			if strings.Contains(got, "：") {
				t.Fatalf("响应不应暴露字段级错误: %s", got)
			}
		})
	}
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
	if !strings.Contains(w.Body.String(), `"code":200`) {
		t.Fatalf("响应未返回成功 code: %s", w.Body.String())
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

			if w.Code != http.StatusOK {
				t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
			}
			if !strings.Contains(w.Body.String(), `"code":200`) {
				t.Fatalf("响应未返回成功 code: %s", w.Body.String())
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

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
	}
	if !strings.Contains(w.Body.String(), `"code":500`) {
		t.Fatalf("不支持的授权来源未返回 BadRequest: %s", w.Body.String())
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
	saveOpen115AuthState(account.ID, &v115open.QrCodeDataReturn{QrCodeData: v115open.QrCodeData{Uid: "legacy-qr"}})
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

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":200`) {
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
	if duplicateRecorder.Code != http.StatusBadRequest || !strings.Contains(duplicateRecorder.Body.String(), "授权流程进行中") {
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
	if crossRecorder.Code != http.StatusBadRequest || !strings.Contains(crossRecorder.Body.String(), "相同的网盘类型") {
		t.Fatalf("跨来源请求未被拒绝: status=%d body=%s", crossRecorder.Code, crossRecorder.Body.String())
	}
}

func TestGetV115AuthorizationTargetKeepsLegacyDeprecatedSourceButRejectsReplacement(t *testing.T) {
	account := &models.Account{
		BaseModel:  models.BaseModel{ID: 9},
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
	if err == nil || !strings.Contains(err.Error(), "废弃") {
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
	if urlRecorder.Code != http.StatusBadRequest || !strings.Contains(urlRecorder.Body.String(), "不是百度网盘账号") {
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
	if confirmRecorder.Code != http.StatusBadRequest || !strings.Contains(confirmRecorder.Body.String(), "不是百度网盘账号") {
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
	saveOpen115AuthState(account.ID, &v115open.QrCodeDataReturn{QrCodeData: v115open.QrCodeData{Uid: "cancel-qr"}}, session.ID)
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

	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"code":200`) {
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

	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "授权会话不匹配") {
		t.Fatalf("未绑定授权会话未被拒绝: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateTmpAccountReturnsFriendlyValidationMessage(t *testing.T) {
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

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
	}
	resp := decodeAPIMessage(t, w.Body)
	if resp.Message != "请填写账号备注" {
		t.Fatalf("Message = %q，期望 请填写账号备注", resp.Message)
	}
	if strings.Contains(resp.Message, "name：") {
		t.Fatalf("响应不应暴露字段级错误: %s", w.Body.String())
	}
}

func TestCreateOpenListAccountReturnsFriendlyValidationMessage(t *testing.T) {
	setupAccountControllerTest(t)
	cases := []struct {
		name        string
		payload     map[string]any
		wantMessage string
	}{
		{
			name: "缺少访问地址",
			payload: map[string]any{
				"auth_type": "password",
				"username":  "admin",
				"password":  "pass",
			},
			wantMessage: "请填写 OpenList 访问地址",
		},
		{
			name: "用户名密码方式缺少用户名",
			payload: map[string]any{
				"base_url":  "http://openlist.example.com",
				"auth_type": "password",
				"password":  "pass",
			},
			wantMessage: "请填写 OpenList 用户名",
		},
		{
			name: "用户名密码方式缺少密码",
			payload: map[string]any{
				"base_url":  "http://openlist.example.com",
				"auth_type": "password",
				"username":  "admin",
			},
			wantMessage: "请填写 OpenList 密码",
		},
		{
			name: "Token 方式缺少令牌",
			payload: map[string]any{
				"base_url":  "http://openlist.example.com",
				"auth_type": "token",
			},
			wantMessage: "请填写 OpenList 令牌",
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
			if resp.Message != tt.wantMessage {
				t.Fatalf("Message = %q，期望 %q", resp.Message, tt.wantMessage)
			}
			if strings.Contains(resp.Message, "：") {
				t.Fatalf("响应不应暴露字段级错误: %s", w.Body.String())
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
