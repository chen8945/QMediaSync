//go:build integration

package backup

import (
	"os"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestBackupRetentionPolicyPostgres(t *testing.T) {
	testBackupRetentionPolicy(t, setupPostgresBackupRetentionTest)
}

func TestBackupRetentionConcurrentDeletionPostgres(t *testing.T) {
	testBackupRetentionConcurrentDeletion(t, func(t *testing.T) *gorm.DB {
		conn := setupPostgresBackupRetentionTest(t)
		if err := conn.AutoMigrate(&backupTestItem{}, &models.Migrator{}); err != nil {
			t.Fatal(err)
		}
		if err := conn.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
			t.Fatal(err)
		}
		return conn
	})
}

func TestBackupRetentionErrorsPostgres(t *testing.T) {
	testBackupRetentionErrors(t, setupPostgresBackupRetentionTest)
}

func TestBackupDeleteSafetyPostgres(t *testing.T) {
	testBackupDeleteSafety(t, setupPostgresBackupRetentionTest)
}

func TestBackupReconcileInterruptedPostgres(t *testing.T) {
	testBackupReconcileInterrupted(t, setupPostgresBackupRetentionTest)
}

func TestBackupReconcileFailurePostgres(t *testing.T) {
	testBackupReconcileFailure(t, setupPostgresBackupRetentionTest)
}

func setupPostgresBackupRetentionTest(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for isolated PostgreSQL tests")
	}
	setupBackupTest(t)
	conn := openCrossEngineDatabase(t, "postgres")
	if err := conn.AutoMigrate(&models.BackupRecord{}, &models.BackupConfig{}); err != nil {
		t.Fatal(err)
	}
	db.Db = conn
	return conn
}
