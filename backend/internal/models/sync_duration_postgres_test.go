//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"
)

func TestSyncDurationMigrationPostgres(t *testing.T) {
	for _, scenario := range []string{"upgrade", "column_failure", "version_failure", "existing_column", "missing_table"} {
		t.Run(scenario, func(t *testing.T) {
			conn, schema := setupEmbySnapshotPostgres(t)
			testSyncDurationMigration(t, conn, func() *gorm.DB { return reopenEmbyWebhookPostgres(t, conn, schema) }, scenario)
		})
	}
}
