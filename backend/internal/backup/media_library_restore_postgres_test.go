//go:build integration

package backup

import (
	"testing"

	"gorm.io/gorm"
)

func TestMediaLibraryRestorePostgres(t *testing.T) {
	testMediaLibraryRestore(t, func(t *testing.T) *gorm.DB {
		conn, _, _ := setupEmbyObservationBackupPostgres(t)
		return conn
	})
}
