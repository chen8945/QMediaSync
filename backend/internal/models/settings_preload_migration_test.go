package models

import (
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestPreloadSettingsMigrationSQLite(t *testing.T) {
	for _, scenario := range []string{"upgrade", "column_failure", "default_failure", "version_failure", "existing_column"} {
		t.Run(scenario, func(t *testing.T) {
			reopen := setupFreshInitializationSQLite(t)
			testPreloadSettingsMigration(t, db.Db, reopen, scenario)
		})
	}
}

func testPreloadSettingsMigration(t *testing.T, conn *gorm.DB, reopen func() *gorm.DB, scenario string) {
	t.Helper()
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&Settings{}).Where("id = ?", 1).Updates(map[string]any{"local_proxy": 1, "multi_playback_enabled": 1, "strm_base_url": "http://preserved.invalid"}).Error; err != nil {
		t.Fatal(err)
	}
	if scenario != "existing_column" {
		if err := conn.Migrator().DropColumn(&Settings{}, "StrmPreloadEnabled"); err != nil {
			t.Fatal(err)
		}
	} else if err := conn.Model(&Settings{}).Where("id = ?", 1).UpdateColumn("strm_preload_enabled", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&Migrator{}).Where("id = ?", 1).UpdateColumn("version_code", 69).Error; err != nil {
		t.Fatal(err)
	}
	if scenario != "upgrade" && scenario != "existing_column" {
		failure := freshInitializationFailure{name: scenario, operation: "raw", model: &Settings{}, sql: "ALTER TABLE"}
		if scenario == "version_failure" {
			failure.operation, failure.model = "update", &Migrator{}
		} else if scenario == "default_failure" {
			failure.operation = "update"
		}
		remove, reached, _ := failFreshInitialization(t, conn, failure)
		if err := Migrate(); err == nil || !*reached {
			t.Fatalf("迁移应传播 %s：reached=%t err=%v", scenario, *reached, err)
		}
		remove()
		assertEmbyMigrationVersion(t, conn, 69)
		if conn.Migrator().HasColumn(&Settings{}, "StrmPreloadEnabled") {
			t.Fatal("失败迁移留下了新列")
		}
	}
	conn = reopen()
	for range 2 {
		if err := Migrate(); err != nil {
			t.Fatal(err)
		}
		assertEmbyMigrationVersion(t, conn, 70)
		var settings Settings
		if err := conn.First(&settings).Error; err != nil || settings.StrmPreloadEnabled != 0 ||
			settings.LocalProxy != 1 || settings.MultiPlaybackEnabled != 1 || settings.StrmBaseUrl != "http://preserved.invalid" {
			t.Fatalf("迁移覆盖了原设置或没有默认关闭：%+v %v", settings, err)
		}
		if conn.Migrator().HasColumn(&SyncPath{}, "strm_preload_enabled") {
			t.Fatal("全局开关不应扩散到目录")
		}
		conn = reopen()
	}
}
