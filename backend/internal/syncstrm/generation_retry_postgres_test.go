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
	"qmediasync/internal/models"
)

func TestGenerationRetryPostgres(t *testing.T) {
	account, sp := setupGenerationRetryPostgresTestDB(t)
	testGenerationRetryFairness(t, account, sp)
}

func TestGenerationOrphanPostgres(t *testing.T) {
	for _, kind := range []string{"pending", "finalizing_due", "finalizing_backoff", "skipped_finalizing", "more_than_page"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupGenerationRetryPostgresTestDB(t)
			testGenerationOrphanedSyncPath(t, account, sp, kind)
		})
	}
	for _, kind := range []string{"pending", "finalizing"} {
		t.Run("rollback_"+kind, func(t *testing.T) {
			account, sp := setupGenerationRetryPostgresTestDB(t)
			testGenerationOrphanRetirementRollback(t, account, sp, kind)
		})
	}
	for _, kind := range []string{"pending_video", "pending_metadata", "finalizing_due", "finalizing_backoff"} {
		t.Run("missing_upload_"+kind, func(t *testing.T) {
			account, sp := setupGenerationRetryPostgresTestDB(t)
			testGenerationMissingUpload(t, account, sp, kind)
		})
	}
}

func setupGenerationRetryPostgresTestDB(t *testing.T) (*models.Account, *models.SyncPath) {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	schema := fmt.Sprintf("qms_c1_retry_%d", time.Now().UnixNano())
	if err := conn.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Exec("DROP SCHEMA " + schema + " CASCADE"); sqlDB.Close() })
	if err := conn.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	account, sp := setupStrmGenerationServiceTestDB(t)
	sqliteDB, _ := db.Db.DB()
	t.Cleanup(func() { sqliteDB.Close() })
	db.Db = conn
	if err := conn.AutoMigrate(&models.Account{}, &models.SyncPath{}, &models.Sync{}, &models.SyncFile{}, &models.StrmGenerationTask{}, &models.Settings{}, &models.DbUploadTask{}); err != nil {
		t.Fatal(err)
	}
	sp.ID = 0
	for _, row := range []any{account, sp, models.SettingsGlobal} {
		if err := conn.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return account, sp
}
