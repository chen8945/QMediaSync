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
