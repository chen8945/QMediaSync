//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"
)

func TestPreloadSettingsMigrationPostgres(t *testing.T) {
	for _, scenario := range []string{"upgrade", "column_failure", "default_failure", "version_failure", "existing_column"} {
		t.Run(scenario, func(t *testing.T) {
			conn, schema := setupEmbySnapshotPostgres(t)
			testPreloadSettingsMigration(t, conn, func() *gorm.DB {
				conn = reopenEmbyWebhookPostgres(t, conn, schema)
				return conn
			}, scenario)
		})
	}
}

func TestPreloadSettingsAtomicPublicationPostgres(t *testing.T) {
	setupEmbySnapshotPostgres(t)
	initializePreloadSettings(t)
	testPreloadSettingsAtomicPublication(t)
}
