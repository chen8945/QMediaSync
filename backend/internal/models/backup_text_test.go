package models

import (
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/validation"
)

func TestBackupTextColumnsSQLite(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&Migrator{}).Where("id = ?", 1).Update("version_code", 66).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	assertBackupTextSchemaVersion(t, db.Db, MaxVersionCode)
	assertBackupTextPersistence(t, db.Db)
}

func assertBackupTextSchemaVersion(t *testing.T, conn *gorm.DB, want int) {
	t.Helper()
	var version Migrator
	if err := conn.First(&version).Error; err != nil {
		t.Fatal(err)
	}
	if version.VersionCode != want {
		t.Fatalf("schema 版本 = %d，期望 %d", version.VersionCode, want)
	}
}

func createBackupTextTables(t *testing.T, conn *gorm.DB) {
	t.Helper()
	if err := conn.AutoMigrate(&Account{}, &EmbyConfig{}, &RequestStat{}, &EmbyLibraryRefreshTask{}); err != nil {
		t.Fatal(err)
	}
}

func assertBackupTextPersistence(t *testing.T, conn *gorm.DB) {
	t.Helper()
	// 合法 Cron 可以使用分钟列表；该输入超过旧 varchar(100) 的上限。
	longCron := strings.Repeat("0,", 59) + "0 * * * *"
	if err := validation.Cron("sync_cron", longCron, false); err != nil {
		t.Fatal(err)
	}
	base := BaseModel{ID: 42, CreatedAt: 1700000000, UpdatedAt: 1700000001}
	tests := []struct {
		name   string
		value  any
		loaded any
	}{
		{
			name: "account_credentials_and_remote_error",
			value: &Account{
				BaseModel:         base,
				Name:              "可迁移账号",
				SourceType:        SourceTypeOpenList,
				Token:             strings.Repeat("token-", 1024),
				RefreshToken:      strings.Repeat("refresh-", 1024),
				Username:          strings.Repeat("用户𝄞", 64),
				Password:          strings.Repeat("test-password-", 256),
				BaseUrl:           "https://openlist.invalid/" + strings.Repeat("prefix/", 256),
				TokenFailedReason: strings.Repeat("远端授权失败：请求尚未完成。", 128),
			},
			loaded: &Account{},
		},
		{
			name: "emby_configuration",
			value: &EmbyConfig{
				BaseModel:  base,
				EmbyUrl:    "https://emby.invalid/" + strings.Repeat("prefix/", 128),
				EmbyApiKey: strings.Repeat("test-key-", 128),
				SyncCron:   longCron,
			},
			loaded: &EmbyConfig{},
		},
		{
			name: "request_url",
			value: &RequestStat{
				BaseModel: base,
				URL:       "https://request.invalid/?names=" + strings.Repeat("media%20file,", 256),
				Method:    "GET",
			},
			loaded: &RequestStat{},
		},
		{
			name: "emby_remote_names",
			value: &EmbyLibraryRefreshTask{
				BaseModel:           base,
				TaskKey:             "test-library-refresh",
				LibraryName:         strings.Repeat("媒体条目名称𝄞", 128),
				FallbackLibraryName: strings.Repeat("媒体库名称𝄞", 128),
			},
			loaded: &EmbyLibraryRefreshTask{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := conn.Create(tt.value).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.First(tt.loaded, base.ID).Error; err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tt.loaded, tt.value) {
				t.Fatal("文本及其他持久字段未原样保存")
			}
		})
	}
	if err := conn.Create(&Account{Name: "可迁移账号"}).Error; err == nil {
		t.Fatal("扩展文本列后仍须拒绝重复账号备注")
	}
	defaultConfig := &EmbyConfig{}
	if err := conn.Create(defaultConfig).Error; err != nil {
		t.Fatal(err)
	}
	if defaultConfig.SyncCron != "0 * * * *" || defaultConfig.SyncEnabled != 1 {
		t.Fatal("扩展文本列改变了默认配置")
	}
}
