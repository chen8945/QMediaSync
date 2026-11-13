//go:build integration

package models

import "testing"

func TestNotificationPersistencePostgres(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	useNotificationPersistenceDB(t, conn)
	testNotificationPersistence(t, conn)
}
