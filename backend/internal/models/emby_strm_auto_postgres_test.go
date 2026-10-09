//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"
)

func TestEmbyAutoSTRMPostgres(t *testing.T) {
	setup := func(t *testing.T) (*gorm.DB, func() *gorm.DB) {
		conn, schema := setupEmbySnapshotPostgres(t)
		return conn, func() *gorm.DB {
			conn = reopenEmbyWebhookPostgres(t, conn, schema)
			return conn
		}
	}
	t.Run("references", func(t *testing.T) {
		testEmbyAutoSTRMExternalReferencePersistsWithoutLedger(t, setup)
	})
	t.Run("deletion", func(t *testing.T) {
		testEmbyAutoSTRMDeletionAfterReopen(t, setup)
	})
}
