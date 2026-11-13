//go:build integration

package models

import "testing"

func TestDownloadQueueRetriesAfterDatabaseErrorPostgres(t *testing.T) {
	setupEmbySnapshotPostgres(t)
	testDownloadQueueRetriesAfterDatabaseError(t)
}
