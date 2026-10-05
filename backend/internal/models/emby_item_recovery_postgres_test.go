//go:build integration

package models

import (
	"errors"
	"slices"
	"testing"

	"gorm.io/gorm"
)

func TestEmbyItemRecoveryPostgresHiddenHintSurvivesAdmissionAndIndexCleanup(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	config, token, snapshot := seedEmbyPostgresSnapshot(t, conn)
	snapshot.Item.Type = "Video"
	snapshot.Item.PartOfItemID = "9100"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Transaction(func(tx *gorm.DB) error {
		_, err := RegisterEmbyDeletionTx(tx, token.ServerID, []string{"9101"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Where("id > 0").Delete(&EmbyMediaItem{}).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead(token.ServerID, config)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := LoadEmbyItemRecoveryReadIDs(t.Context(), token, "9101")
	if err != nil || !slices.Equal(ids, []string{"9100"}) {
		t.Fatalf("hidden hint after cleanup=%v err=%v", ids, err)
	}
	if admitted, err := AdmitEmbyVerifiedSurvivors(t.Context(), token, []string{"9101"}); err != nil || !admitted {
		t.Fatalf("admission failed: admitted=%t err=%v", admitted, err)
	}
	if _, err := LoadEmbyItemRecoveryReadIDs(t.Context(), token, "9101"); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("old token accepted after admission: %v", err)
	}
	token, err = BeginEmbyIndexRead(token.ServerID, config)
	if err != nil {
		t.Fatal(err)
	}
	ids, err = LoadEmbyItemRecoveryReadIDs(t.Context(), token, "9101")
	if err != nil || !slices.Equal(ids, []string{"9100"}) {
		t.Fatalf("reread lost admitted hidden item: ids=%v err=%v", ids, err)
	}
}
