//go:build integration

package syncstrm

import (
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestGenerationSkippedPostgres(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	for _, kind := range []string{"all", "mixed", "failed", "refresh_recovery"} {
		t.Run(kind, func(t *testing.T) {
			conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { sqlDB.Close() })
			schema := fmt.Sprintf("qms_d_skipped_%d", time.Now().UnixNano())
			if err := conn.Exec("CREATE SCHEMA " + schema).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { conn.Exec("DROP SCHEMA " + schema + " CASCADE") })
			if err := conn.Exec("SET search_path TO " + schema).Error; err != nil {
				t.Fatal(err)
			}
			previousDB, previousSettings := db.Db, models.SettingsGlobal
			previousLogger, previous115Logger := helpers.AppLogger, helpers.V115Log
			t.Cleanup(func() {
				db.Db, models.SettingsGlobal = previousDB, previousSettings
				helpers.AppLogger, helpers.V115Log = previousLogger, previous115Logger
			})
			account, sp := setupStrmGenerationServiceTestDB(t)
			sqliteDB, err := db.Db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqliteDB.Close() })
			db.Db = conn
			if err := conn.AutoMigrate(&models.Migrator{}, &models.Account{}, &models.SyncPath{}, &models.Sync{}, &models.SyncFile{}, &models.StrmGenerationTask{}, &models.Settings{}, &models.DbUploadTask{}); err != nil {
				t.Fatal(err)
			}
			for _, row := range []any{account, sp, models.SettingsGlobal} {
				if err := conn.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			historical := models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeFile, Status: models.StrmGenerationStatusCompleted, AcceptedItems: 1}
			if err := conn.Create(&historical).Error; err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"SkipReason", "SkippedItems"} {
				if err := conn.Migrator().DropColumn(&models.StrmGenerationTask{}, field); err != nil {
					t.Fatal(err)
				}
			}
			if err := conn.Create(&models.Migrator{VersionCode: 64}).Error; err != nil {
				t.Fatal(err)
			}
			models.Migrate()
			var version models.Migrator
			if err := conn.First(&version).Error; err != nil || version.VersionCode != models.MaxVersionCode {
				t.Fatalf("migration: version=%+v err=%v", version, err)
			}
			for _, field := range []string{"SkipReason", "SkippedItems"} {
				if !conn.Migrator().HasColumn(&models.StrmGenerationTask{}, field) {
					t.Fatalf("missing migrated column: %s", field)
				}
			}
			if err := conn.First(&historical, historical.ID).Error; err != nil {
				t.Fatal(err)
			}
			if historical.Status != models.StrmGenerationStatusCompleted || historical.AcceptedItems != 1 || historical.SkippedItems != 0 || historical.SkipReason != "" {
				t.Fatalf("migration changed historical result: %+v", historical)
			}
			if kind == "refresh_recovery" {
				testGenerationSkippedRefreshRecovery(t, account, sp)
			} else {
				testGenerationSkipped(t, account, sp, kind)
			}
		})
	}
}
