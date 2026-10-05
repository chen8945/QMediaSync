//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"
)

func TestEmbyCleanupPostgresPersistence(t *testing.T) {
	testCleanupPersistence(t, func(t *testing.T) (*gorm.DB, func() *gorm.DB) {
		conn, schema := setupEmbySnapshotPostgres(t)
		return conn, func() *gorm.DB {
			conn = reopenEmbyWebhookPostgres(t, conn, schema)
			return conn
		}
	})
}
