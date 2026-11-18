//go:build integration

package backup

import "testing"

func TestSyncDurationBackupCompatibilityPostgres(t *testing.T) {
	testSyncDurationBackupCompatibility(t, setupHistoryRestorePostgres)
}
