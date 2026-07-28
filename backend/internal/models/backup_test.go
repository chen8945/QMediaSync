package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/helpers"
)

func TestBackupConfigReadDTOExcludesScheduledPasswordCiphertext(t *testing.T) {
	config := BackupConfig{
		BaseModel:                         BaseModel{ID: 1, CreatedAt: 2, UpdatedAt: 3},
		BackupEnabled:                     1,
		ScheduledBackupPasswordCiphertext: "gcm:secret-ciphertext",
	}

	modelJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("序列化备份配置模型失败: %v", err)
	}
	responseJSON, err := json.Marshal(config.ToReadDTO())
	if err != nil {
		t.Fatalf("序列化备份配置读取 DTO 失败: %v", err)
	}
	for _, payload := range []string{string(modelJSON), string(responseJSON)} {
		if strings.Contains(payload, "scheduled_backup_password_ciphertext") || strings.Contains(payload, "secret-ciphertext") {
			t.Fatalf("备份配置读取 JSON 不应包含定时备份密码密文: %s", payload)
		}
	}
	if !config.ToReadDTO().BackupEncryptionEnabled {
		t.Fatal("存在密文时读取 DTO 应标识已启用加密")
	}
}

// TestScheduledBackupPasswordRoundTripAndClear 覆盖定时备份密码的存储边界：
// 密文只由本机密钥包装，明文绝不落库，清空密码必须同步删除密文。
func TestScheduledBackupPasswordRoundTripAndClear(t *testing.T) {
	helpers.ConfigDir = t.TempDir()
	if err := helpers.InitEncryptionKey(); err != nil {
		t.Fatalf("InitEncryptionKey() error = %v", err)
	}

	config := BackupConfig{}
	const password = "BackupPass123"
	if err := config.SetScheduledBackupPassword(password); err != nil {
		t.Fatalf("SetScheduledBackupPassword() error = %v", err)
	}
	if config.ScheduledBackupPasswordCiphertext == "" || strings.Contains(config.ScheduledBackupPasswordCiphertext, password) {
		t.Fatalf("密文 = %q，不应为空或包含明文", config.ScheduledBackupPasswordCiphertext)
	}

	decrypted, err := config.ScheduledBackupPassword()
	if err != nil {
		t.Fatalf("ScheduledBackupPassword() error = %v", err)
	}
	if decrypted != password {
		t.Fatalf("ScheduledBackupPassword() = %q, want %q", decrypted, password)
	}

	if err := config.SetScheduledBackupPassword(""); err != nil {
		t.Fatalf("SetScheduledBackupPassword(\"\") error = %v", err)
	}
	if config.ScheduledBackupPasswordCiphertext != "" {
		t.Fatal("清空密码后必须删除本机密文")
	}
	if config.ToReadDTO().BackupEncryptionEnabled {
		t.Fatal("清空密码后读取 DTO 不应标识已启用加密")
	}
}

func TestBackupConfigDoesNotDropHistoricalColumns(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := database.Exec(`CREATE TABLE backup_config (
		id integer primary key,
		backup_path text,
		backup_compress integer
	)`).Error; err != nil {
		t.Fatalf("create historical backup_config: %v", err)
	}
	if err := database.AutoMigrate(&BackupConfig{}); err != nil {
		t.Fatalf("AutoMigrate(BackupConfig): %v", err)
	}
	for _, column := range []string{"backup_path", "backup_compress"} {
		if !database.Migrator().HasColumn("backup_config", column) {
			t.Fatalf("AutoMigrate must preserve historical backup_config.%s", column)
		}
	}
}

func TestBackupConfigRequiresUnencryptedConfirmation(t *testing.T) {
	password := "BackupPass123"
	emptyPassword := ""
	tests := []struct {
		name          string
		config        BackupConfig
		backupEnabled int
		password      *string
		expected      bool
	}{
		{name: "disabled", backupEnabled: 0, expected: false},
		{name: "existing encrypted password", config: BackupConfig{ScheduledBackupPasswordCiphertext: "ciphertext"}, backupEnabled: 1, expected: false},
		{name: "new password", backupEnabled: 1, password: &password, expected: false},
		{name: "cleared password", config: BackupConfig{ScheduledBackupPasswordCiphertext: "ciphertext"}, backupEnabled: 1, password: &emptyPassword, expected: true},
		{name: "no password", backupEnabled: 1, expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.config.RequiresUnencryptedConfirmation(tt.backupEnabled, tt.password); got != tt.expected {
				t.Fatalf("RequiresUnencryptedConfirmation() = %t, want %t", got, tt.expected)
			}
		})
	}
}
