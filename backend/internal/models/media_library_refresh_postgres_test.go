//go:build integration

package models

import "testing"

func TestMediaLibraryRefreshScopesPostgres(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	testMediaLibraryRefreshScopes(t, conn)
}
