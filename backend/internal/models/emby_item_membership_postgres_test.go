//go:build integration

package models

import "testing"

func TestEmbyItemMembershipPostgresHistoryDoesNotScaleReceiptReads(t *testing.T) {
	testEmbyItemMembershipHistoryReads(t, func(t *testing.T) (EmbyIndexToken, SyncFile) {
		conn, _, _, token, _ := setupEmbyWebhookPostgres(t)
		var file SyncFile
		if err := conn.First(&file).Error; err != nil {
			t.Fatal(err)
		}
		return token, file
	})
}
