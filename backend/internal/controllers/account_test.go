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
