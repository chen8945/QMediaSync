package validation

import "testing"

func TestBackupPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{name: "空密码允许", password: ""},
		{name: "符合规则的 Unicode 密码允许", password: "密碼Abc12345"},
		{name: "长度不足拒绝", password: "Abc123", wantErr: true},
		{name: "缺少大写字母拒绝", password: "abc1234567", wantErr: true},
		{name: "缺少小写字母拒绝", password: "ABC1234567", wantErr: true},
		{name: "缺少数字拒绝", password: "Abcdefghij", wantErr: true},
		{name: "普通空格拒绝", password: "Abc 123456", wantErr: true},
		{name: "全角空格拒绝", password: "Abc　123456", wantErr: true},
		{name: "制表符拒绝", password: "Abc\t123456", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := BackupPassword("backup_password", tt.password)
			if (err != nil) != tt.wantErr {
				t.Fatalf("BackupPassword() error = %v，wantErr %v", err, tt.wantErr)
			}
		})
	}
}
