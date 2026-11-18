package models

import (
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestSyncDurationMigrationSQLite(t *testing.T) {
	for _, scenario := range []string{"upgrade", "column_failure", "version_failure", "existing_column", "missing_table"} {
		t.Run(scenario, func(t *testing.T) {
			reopen := setupFreshInitializationSQLite(t)
			testSyncDurationMigration(t, db.Db, reopen, scenario)
		})
	}
}

func testSyncDurationMigration(t *testing.T, conn *gorm.DB, reopen func() *gorm.DB, scenario string) {
	t.Helper()
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&Sync{BaseModel: BaseModel{ID: 1, CreatedAt: 100}, Status: SyncStatusCompleted, FinishAt: 110, Total: 7}).Error; err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"NetFileDurationMs", "LocalFileDurationMs", "GenerationDurationMs", "TotalDurationMs"} {
		if err := conn.Migrator().DropColumn(&Sync{}, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Model(&Migrator{}).Where("id = ?", 1).UpdateColumn("version_code", 71).Error; err != nil {
		t.Fatal(err)
	}
	if scenario == "missing_table" {
		if err := conn.Migrator().DropTable(&Sync{}); err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "existing_column" {
		if err := conn.Migrator().AddColumn(&Sync{}, "NetFileDurationMs"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Model(&Sync{}).Where("id = ?", 1).UpdateColumn("net_file_duration_ms", 0).Error; err != nil {
			t.Fatal(err)
		}
	}
	if scenario == "column_failure" || scenario == "version_failure" {
		var remove func()
		reached := new(bool)
		if scenario == "column_failure" {
			const callback = "test:sync_duration_column"
			if err := conn.Callback().Raw().Before("gorm:raw").Register(callback, func(tx *gorm.DB) {
				if strings.Contains(tx.Statement.SQL.String(), "ADD") && strings.Contains(tx.Statement.SQL.String(), "total_duration_ms") {
					*reached = true
					tx.AddError(errors.New("injected duration column failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			remove = func() { conn.Callback().Raw().Remove(callback) }
			t.Cleanup(remove)
		} else {
			remove, reached, _ = failFreshInitialization(t, conn, freshInitializationFailure{name: scenario, operation: "update", model: &Migrator{}})
		}
		if err := Migrate(); err == nil || !*reached {
			t.Fatalf("迁移未传播失败：reached=%t err=%v", *reached, err)
		}
		remove()
		assertEmbyMigrationVersion(t, conn, 71)
		if conn.Migrator().HasColumn(&Sync{}, "NetFileDurationMs") {
			t.Fatal("失败迁移没有回滚新增列")
		}
	}
	conn = reopen()
	for range 2 {
		if err := Migrate(); err != nil {
			t.Fatal(err)
		}
		assertEmbyMigrationVersion(t, conn, MaxVersionCode)
		if scenario == "missing_table" {
			var count int64
			if err := conn.Model(&Sync{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("缺表迁移失败或补造了记录：%d %v", count, err)
			}
			conn = reopen()
			continue
		}
		var stored Sync
		if err := conn.First(&stored, 1).Error; err != nil {
			t.Fatal(err)
		}
		if stored.CreatedAt != 100 || stored.FinishAt != 110 || stored.Total != 7 || stored.Status != SyncStatusCompleted {
			t.Fatalf("迁移改写历史记录：%+v", stored)
		}
		if scenario == "existing_column" {
			assertSyncDuration(t, stored.NetFileDurationMs, 0)
		} else if stored.NetFileDurationMs != nil {
			t.Fatal("迁移补造了历史网盘耗时")
		}
		if stored.LocalFileDurationMs != nil || stored.GenerationDurationMs != nil || stored.TotalDurationMs != nil {
			t.Fatal("迁移补造了历史耗时")
		}
		conn = reopen()
	}
}
