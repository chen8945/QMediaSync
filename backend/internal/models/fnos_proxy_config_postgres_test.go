//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"
)

func TestFnosProxyConfigPostgres(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	testFnosProxyConfigPersistence(t, conn)
}

func TestFnosProxyConfigExtraColumnsPostgres(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	testFnosProxyConfigExtraColumns(t, conn)
}

func TestFnosProxyMigrationPostgres(t *testing.T) {
	for _, scenario := range []string{"upgrade", "table_failure", "version_failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn, schema := setupEmbySnapshotPostgres(t)
			testFnosProxyMigration(t, conn, func() *gorm.DB {
				conn = reopenEmbyWebhookPostgres(t, conn, schema)
				return conn
			}, scenario)
		})
	}
}
