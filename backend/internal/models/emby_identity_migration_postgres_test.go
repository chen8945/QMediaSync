//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbyIdentityMigrationPostgres(t *testing.T) {
	testEmbyIdentityMigration(t, func(t *testing.T) (*gorm.DB, func() *gorm.DB) {
		conn, _ := setupEmbySnapshotPostgres(t)
		return conn, func() *gorm.DB {
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			conn, err = gorm.Open(conn.Dialector, &gorm.Config{Logger: conn.Logger})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err = conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqlDB.Close() })
			db.Db = conn
			return conn
		}
	})
}
