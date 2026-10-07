//go:build integration

package backup

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func setupEmbyObservationBackupPostgres(t *testing.T) (*gorm.DB, models.EmbyWebhookRecord, []models.EmbyItemEvidence) {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for isolated PostgreSQL backup tests")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil || parsed.Host == "" {
		t.Fatal("QMS_TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	setupBackupTest(t)
	adminSQL, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adminSQL.Close() })
	admin, err := gorm.Open(postgres.New(postgres.Config{DriverName: "postgres", Conn: adminSQL}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("qms_emby_backup_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("statement_timeout", "15000")
	parsed.RawQuery = query.Encode()
	sqlDB, err := sql.Open("postgres", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	conn, err := gorm.Open(postgres.New(postgres.Config{DriverName: "postgres", Conn: sqlDB}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	db.Db = conn
	if err := conn.AutoMigrate(&models.BackupConfig{}, &models.BackupRecord{}, &models.Migrator{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
		t.Fatal(err)
	}
	return seedEmbyObservationBackup(t, conn)
}

func TestRestoreEmbyObservationIndexPostgres(t *testing.T) {
	t.Run("originals", func(t *testing.T) {
		testRestoreEmbyObservationIndexRebuildsFromOriginals(t, setupEmbyObservationBackupPostgres)
	})
	t.Run("rebuild failure", func(t *testing.T) {
		testRestoreEmbyObservationIndexFailureRollsBack(t, setupEmbyObservationBackupPostgres)
	})
	t.Run("partial import", func(t *testing.T) {
		testRestoreEmbyObservationPartialImportRollsBack(t, setupEmbyObservationBackupPostgres)
	})
	t.Run("partial state evidence import", func(t *testing.T) {
		testRestoreEmbyMembershipPartialEvidenceRollsBack(t, setupEmbyObservationBackupPostgres)
	})
}

func TestRestoreAuthenticationAndSessionPolicyPostgres(t *testing.T) {
	conn, _, _ := setupEmbyObservationBackupPostgres(t)
	testRestoreAuthenticationAndSessionPolicy(t, conn)
}

func TestRestoreLegacySchema66JSONSerializerPostgres(t *testing.T) {
	conn, _, _ := setupEmbyObservationBackupPostgres(t)
	testRestoreLegacySchema66JSONSerializer(t, conn)
}

func TestRestoreLegacySchema66DownloadHiddenFieldsPostgres(t *testing.T) {
	conn, _, _ := setupEmbyObservationBackupPostgres(t)
	testRestoreLegacySchema66DownloadHiddenFields(t, conn)
}
