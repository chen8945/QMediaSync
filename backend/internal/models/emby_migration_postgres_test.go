//go:build integration

package models

import "testing"

func TestEmbyDeletionMigrationPostgres(t *testing.T) {
	for _, scenario := range []string{"fresh", "upgrade", "table_failure", "version_failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn, _ := setupEmbySnapshotPostgres(t)
			testEmbyDeletionMigration(t, conn, scenario)
		})
	}
}
