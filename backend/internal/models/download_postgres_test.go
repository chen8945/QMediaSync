//go:build integration

package models

import (
	"testing"

	"qmediasync/internal/db"
)

func TestDownloadQueueRetriesAfterDatabaseErrorPostgres(t *testing.T) {
	setupEmbySnapshotPostgres(t)
	testDownloadQueueRetriesAfterDatabaseError(t)
}

func TestDownloadQueueClaimsEachEndpointOncePostgres(t *testing.T) {
	setupEmbySnapshotPostgres(t)
	if err := db.Db.AutoMigrate(&DbDownloadTask{}, &EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatal(err)
	}
	testDownloadQueueClaimsEachEndpointOnce(t)
}
