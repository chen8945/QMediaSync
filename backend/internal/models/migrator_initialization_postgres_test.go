//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"
)

func TestMigrateFreshPostgresRollsBackAndRetries(t *testing.T) {
	for _, failure := range freshInitializationFailures() {
		t.Run(failure.name, func(t *testing.T) {
			conn, schema := setupEmbySnapshotPostgres(t)
			testFreshInitializationFailure(t, conn, func() *gorm.DB {
				return reopenEmbyWebhookPostgres(t, conn, schema)
			}, failure)
		})
	}
}

func TestMigrateFreshPostgresDefaultsAndRepeatedStartup(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	assertFreshInitializationDefaults(t, conn)
	assertFreshInitializationPreservesSettings(t, conn)
}
