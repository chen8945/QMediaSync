package requests

import "testing"

func TestUserSettingsRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     ChangeUserCredentialRequest
		wantErr bool
	}{
		{name: "只修改用户名通过", req: ChangeUserCredentialRequest{Username: "admin"}},
		{name: "修改用户名和密码通过", req: ChangeUserCredentialRequest{Username: "admin", NewPassword: "secret1"}},
		{name: "用户名为空失败", req: ChangeUserCredentialRequest{Username: " "}, wantErr: true},
		{name: "用户名过短失败", req: ChangeUserCredentialRequest{Username: "ab"}, wantErr: true},
		{name: "用户名过长失败", req: ChangeUserCredentialRequest{Username: "abcdefghijklmnopqrstu"}, wantErr: true},
		{name: "中文用户名失败", req: ChangeUserCredentialRequest{Username: "管理员"}, wantErr: true},
		{name: "符号用户名失败", req: ChangeUserCredentialRequest{Username: "admin_user"}, wantErr: true},
		{name: "密码过短失败", req: ChangeUserCredentialRequest{Username: "admin", NewPassword: "12345"}, wantErr: true},
		{name: "纯数字密码失败", req: ChangeUserCredentialRequest{Username: "admin", NewPassword: "123456"}, wantErr: true},
		{name: "纯字母密码失败", req: ChangeUserCredentialRequest{Username: "admin", NewPassword: "secret"}, wantErr: true},
		{name: "新增 Unicode 字母仍拒绝纯字母密码", req: ChangeUserCredentialRequest{Username: "admin", NewPassword: "\u1c89\u1c89\u1c89\u1c89\u1c89\u1c89"}, wantErr: true},
		{name: "新增 Unicode 数字仍拒绝纯数字密码", req: ChangeUserCredentialRequest{Username: "admin", NewPassword: "\U00011bf0\U00011bf1\U00011bf2\U00011bf3\U00011bf4\U00011bf5"}, wantErr: true},
		{name: "Unicode 字母数字混合密码通过", req: ChangeUserCredentialRequest{Username: "admin", NewPassword: "\u1c89\u1c89\u1c89\U00011bf0\U00011bf1\U00011bf2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoginRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     LoginRequest
		wantErr bool
	}{
		{name: "用户名和密码通过", req: LoginRequest{Username: "admin", Password: "secret"}},
		{name: "用户名会去除空白后校验", req: LoginRequest{Username: " admin ", Password: "secret"}},
		{name: "兼容旧版两字符用户名", req: LoginRequest{Username: "ab", Password: "secret"}},
		{name: "兼容旧版短密码", req: LoginRequest{Username: "admin", Password: "12345"}},
		{name: "Unicode 分类更新不拦截旧密码登录", req: LoginRequest{Username: "admin", Password: "\u1c89\u1c89\u1c89\u1c89\u1c89\u1c89"}},
		{name: "登录允许二十字符用户名", req: LoginRequest{Username: "abcdefghijklmnopqrst", Password: "secret"}},
		{name: "登录保留用户名长度上限", req: LoginRequest{Username: "abcdefghijklmnopqrstu", Password: "secret"}, wantErr: true},
		{name: "密码只校验非空", req: LoginRequest{Username: "admin", Password: " "}},
		{name: "用户名为空失败", req: LoginRequest{Username: " ", Password: "secret"}, wantErr: true},
		{name: "密码为空失败", req: LoginRequest{Username: "admin", Password: ""}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEnableTwoFactorRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     EnableTwoFactorRequest
		wantErr bool
	}{
		{name: "验证码通过", req: EnableTwoFactorRequest{TOTPCode: "123456"}},
		{name: "验证码为空失败", req: EnableTwoFactorRequest{TOTPCode: " "}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDisableTwoFactorRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     DisableTwoFactorRequest
		wantErr bool
	}{
		{name: "密码和验证码通过", req: DisableTwoFactorRequest{Password: "secret", TOTPCode: "123456"}},
		{name: "密码为空失败", req: DisableTwoFactorRequest{Password: "", TOTPCode: "123456"}, wantErr: true},
		{name: "验证码为空失败", req: DisableTwoFactorRequest{Password: "secret", TOTPCode: " "}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
