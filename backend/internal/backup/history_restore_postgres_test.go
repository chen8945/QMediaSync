//go:build integration

package backup

import (
	"os"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func setupHistoryRestorePostgres(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for isolated PostgreSQL backup history tests")
	}
	setupBackupTest(t)
	conn := openCrossEngineDatabase(t, "postgres")
	db.Db = conn
	return conn
}

func TestRestoreLocalBackupHistoryPostgres(t *testing.T) {
	for _, format := range []string{"current", "legacy"} {
		t.Run(format+"_preserved", func(t *testing.T) {
			testRestorePreservesLocalBackupHistory(t, setupHistoryRestorePostgres(t), format)
		})
		t.Run(format+"_rollback", func(t *testing.T) {
			testRestoreFailurePreservesLocalBackupHistory(t, setupHistoryRestorePostgres(t), format)
		})
		t.Run(format+"_invalid_history", func(t *testing.T) {
			testRestoreValidatesArchivedLocalHistory(t, setupHistoryRestorePostgres(t), format, "row_type")
		})
	}
}
