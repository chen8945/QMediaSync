//go:build integration

package models

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestBackupTextColumnsPostgres(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	createBackupTextTables(t, conn)
	assertBackupTextPersistence(t, conn)
}

func TestMigrateBackupTextColumnsPostgresPreservesExistingValuesAndConstraints(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	createLegacyBackupTextTables(t, conn)
	if err := conn.Exec("ALTER TABLE account ALTER COLUMN username SET DEFAULT 'historical-user'").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("ALTER TABLE account ALTER COLUMN username SET NOT NULL").Error; err != nil {
		t.Fatal(err)
	}
	account := &Account{
		BaseModel:         BaseModel{ID: 7},
		Name:              "既有账号",
		UserId:            "existing-user",
		Username:          "既有用户",
		Token:             "existing-token",
		RefreshToken:      "existing-refresh",
		Password:          "existing-password",
		BaseUrl:           "https://existing.invalid",
		TokenFailedReason: "旧错误",
	}
	if err := conn.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Table("account").Create(map[string]any{"id": 8, "name": "保留空值", "token": nil}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := conn.Transaction(MigrateBackupTextColumns); err != nil {
			t.Fatal(err)
		}
	}
	assertBackupTextColumnTypes(t, conn, true)
	var restored Account
	if err := conn.First(&restored, account.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, *account) {
		t.Fatal("迁移改写了已有账号")
	}
	var historical struct {
		Username  string
		NullToken bool
	}
	if err := conn.Raw("SELECT username, token IS NULL AS null_token FROM account WHERE id = ?", 8).Scan(&historical).Error; err != nil {
		t.Fatal(err)
	}
	if historical.Username != "historical-user" || !historical.NullToken {
		t.Fatal("迁移改写了已有默认值或 NULL")
	}
	if err := conn.Table("account").Create(map[string]any{"id": 9, "name": "默认值"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Raw("SELECT username FROM account WHERE id = ?", 9).Scan(&historical.Username).Error; err != nil {
		t.Fatal(err)
	}
	if historical.Username != "historical-user" {
		t.Fatal("迁移丢失了已有列的默认值")
	}
	if err := conn.Table("account").Create(map[string]any{"id": 10, "username": nil}).Error; err == nil {
		t.Fatal("迁移丢失了已有列的 NOT NULL 约束")
	}
	if err := conn.Create(&Account{UserId: "existing-user", Username: "其他用户"}).Error; err == nil {
		t.Fatal("迁移丢失了账号用户 ID 唯一约束")
	}
	if err := conn.Table("account").Create(map[string]any{
		"id": 11, "username": "有限长字段", "auth_provider": strings.Repeat("a", 65),
	}).Error; err == nil {
		t.Fatal("文本迁移不应放宽授权来源的有限长度")
	}
	assertBackupTextPersistence(t, conn)
}

func TestMigrateBackupTextColumnsPostgresRollsBackAndRetries(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	createLegacyBackupTextTables(t, conn)
	if err := conn.AutoMigrate(&Migrator{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&Migrator{BaseModel: BaseModel{ID: 1}, VersionCode: 66}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("CREATE VIEW backup_text_blocked AS SELECT emby_url FROM emby_config").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(); err == nil {
		t.Fatal("依赖视图应阻止修改列类型")
	}
	assertBackupTextColumnTypes(t, conn, false)
	assertBackupTextSchemaVersion(t, conn, 66)
	if err := conn.Exec("DROP VIEW backup_text_blocked").Error; err != nil {
		t.Fatal(err)
	}
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	assertBackupTextColumnTypes(t, conn, true)
	assertBackupTextSchemaVersion(t, conn, MaxVersionCode)
	assertBackupTextPersistence(t, conn)
}

func TestMigrateBackupTextColumnsPostgresSkipsMissingTablesAndColumns(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	if err := conn.Exec("CREATE TABLE account (id bigint PRIMARY KEY, token varchar(512))").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("INSERT INTO account (id, token) VALUES (?, ?)", 1, "existing-token").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Transaction(MigrateBackupTextColumns); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("INSERT INTO account (id, token) VALUES (?, ?)", 2, strings.Repeat("test-token-", 128)).Error; err != nil {
		t.Fatal(err)
	}
	if conn.Migrator().HasColumn(&Account{}, "Username") || conn.Migrator().HasTable(&EmbyConfig{}) {
		t.Fatal("文本迁移不应顺带补齐缺失的表或字段")
	}
}

func legacyBackupTextColumns() []struct {
	table  string
	column string
	length int
} {
	return []struct {
		table  string
		column string
		length int
	}{
		{"account", "token", 512},
		{"account", "refresh_token", 512},
		{"account", "username", 32},
		{"account", "password", 256},
		{"account", "base_url", 1024},
		{"account", "token_failed_reason", 256},
		{"emby_config", "emby_url", 500},
		{"emby_config", "emby_api_key", 200},
		{"emby_config", "sync_cron", 100},
		{"request_stats", "url", 512},
		{"emby_library_refresh_tasks", "library_name", 255},
		{"emby_library_refresh_tasks", "fallback_library_name", 255},
	}
}

func createLegacyBackupTextTables(t *testing.T, conn *gorm.DB) {
	t.Helper()
	createBackupTextTables(t, conn)
	for _, column := range legacyBackupTextColumns() {
		query := fmt.Sprintf("ALTER TABLE ? ALTER COLUMN ? TYPE varchar(%d)", column.length)
		if err := conn.Exec(query, clause.Table{Name: column.table}, clause.Column{Name: column.column}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func assertBackupTextColumnTypes(t *testing.T, conn *gorm.DB, migrated bool) {
	t.Helper()
	want := "character varying"
	if migrated {
		want = "text"
	}
	for _, column := range legacyBackupTextColumns() {
		var dataType string
		if err := conn.Raw(`SELECT data_type FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`,
			column.table, column.column).Scan(&dataType).Error; err != nil {
			t.Fatal(err)
		}
		if dataType != want {
			t.Errorf("%s.%s 类型 = %s，期望 %s", column.table, column.column, dataType, want)
		}
	}
}
