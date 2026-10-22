//go:build integration

package backup

import "testing"

func TestRestoreFnosProxyCompatibilityPostgres(t *testing.T) {
	testRestoreFnosProxyCompatibility(t, setupHistoryRestorePostgres)
}

func TestFnosProxyBackupExtraColumnsPostgres(t *testing.T) {
	testFnosProxyBackupExtraColumns(t, setupHistoryRestorePostgres)
}
