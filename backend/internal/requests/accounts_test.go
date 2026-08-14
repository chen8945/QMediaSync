package requests

import (
	"testing"

	"qmediasync/internal/models"
	"qmediasync/internal/v115auth"
)

func TestAccountRequestValidate(t *testing.T) {
	t.Run("115 自定义 APP ID 账号通过", func(t *testing.T) {
		req := CreateAccountRequest{
			SourceType:     models.SourceType115,
			Name:           "main",
			AppID:          "appid",
			AuthSourceType: v115auth.SourceTypeCustomAppID,
			CustomAppName:  "custom",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("OpenList 账号必须使用独立接口", func(t *testing.T) {
		req := CreateAccountRequest{SourceType: models.SourceTypeOpenList, Name: "openlist"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("账号名称为空失败", func(t *testing.T) {
		req := CreateAccountRequest{SourceType: models.SourceTypeBaiduPan, Name: " "}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("更新账号 ID 为空失败", func(t *testing.T) {
		req := UpdateAccountInfoRequest{Name: "main"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestPrepareAccountAuthorizationRequestValidate(t *testing.T) {
	base := PrepareAccountAuthorizationRequest{
		AccountID:      12,
		SourceType:     models.SourceType115,
		AuthSourceType: v115auth.SourceTypeBuiltInAppID,
		AuthProvider:   v115auth.ProviderOfficialPKCE,
		AppID:          "100197849",
		AppIDName:      "QMediaSync",
		Confirmed:      true,
	}

	tests := []struct {
		name    string
		request PrepareAccountAuthorizationRequest
		wantErr bool
	}{
		{name: "有效目标", request: base},
		{name: "未确认风险", request: func() PrepareAccountAuthorizationRequest { req := base; req.Confirmed = false; return req }(), wantErr: true},
		{name: "已废弃内置应用", request: func() PrepareAccountAuthorizationRequest {
			req := base
			req.AppID = "100197665"
			req.AppIDName = "Q115-STRM"
			return req
		}(), wantErr: true},
		{name: "跨来源目标仍需由控制器拒绝但请求格式有效", request: func() PrepareAccountAuthorizationRequest {
			req := base
			req.SourceType = models.SourceTypeBaiduPan
			return req
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.request.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCancelAccountAuthorizationRequestValidate(t *testing.T) {
	valid := CancelAccountAuthorizationRequest{AccountID: 12, AuthorizationID: "authorization-id"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("有效取消请求校验失败: %v", err)
	}
	for _, request := range []CancelAccountAuthorizationRequest{
		{AccountID: 0, AuthorizationID: "authorization-id"},
		{AccountID: 12},
	} {
		if err := request.Validate(); err == nil {
			t.Fatalf("无效取消请求未被拒绝: %+v", request)
		}
	}
}

func TestOpenListAccountRequestValidate(t *testing.T) {
	t.Run("Token 认证通过并补协议", func(t *testing.T) {
		req := CreateOpenListAccountRequest{BaseURL: "openlist.example.com/", Token: "token"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if req.BaseURL != "http://openlist.example.com" {
			t.Fatalf("BaseURL = %q", req.BaseURL)
		}
	})

	t.Run("用户名密码认证通过", func(t *testing.T) {
		req := CreateOpenListAccountRequest{BaseURL: "https://openlist.example.com", Username: "user", Password: "pass"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("缺少认证信息失败", func(t *testing.T) {
		req := CreateOpenListAccountRequest{BaseURL: "https://openlist.example.com"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("新建 Token 认证账号缺少令牌失败", func(t *testing.T) {
		req := CreateOpenListAccountRequest{
			BaseURL:  "https://openlist.example.com",
			AuthType: "token",
		}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("更新 Token 认证账号可复用已有凭据", func(t *testing.T) {
		req := CreateOpenListAccountRequest{
			ID:       1,
			BaseURL:  "https://openlist.example.com",
			AuthType: "token",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("更新用户名密码账号可省略密码以复用已有凭据", func(t *testing.T) {
		req := CreateOpenListAccountRequest{
			ID:       1,
			BaseURL:  "https://openlist.example.com",
			AuthType: "password",
			Username: "user",
		}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

func TestAPIKeyRequestValidate(t *testing.T) {
	t.Run("创建 API Key 名称通过", func(t *testing.T) {
		req := CreateAPIKeyRequest{Name: "media-sync"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("创建 API Key 空名称失败", func(t *testing.T) {
		req := CreateAPIKeyRequest{Name: " "}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("状态 false 显式传入通过", func(t *testing.T) {
		active := false
		req := UpdateAPIKeyStatusRequest{IsActive: &active}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("状态未传失败", func(t *testing.T) {
		req := UpdateAPIKeyStatusRequest{}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}
