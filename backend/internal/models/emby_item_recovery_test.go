package models

import (
	"errors"
	"slices"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbyItemRecoveryReadIDsUseExactCurrentEvidence(t *testing.T) {
	for _, name := range []string{"current", "index cleaned", "wrong connection", "wrong identity", "corrupt history", "missing target"} {
		t.Run(name, func(t *testing.T) {
			config, token, file := setupEmbySnapshotModelTest(t)
			snapshot := snapshotForFile(file)
			snapshot.Item.Type = "Video"
			snapshot.Item.PartOfItemID = "100"
			snapshot.Item.VersionOfItemID = "50"
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			var state EmbyItemState
			if err := db.Db.Where("item_id = ?", "101").First(&state).Error; err != nil {
				t.Fatal(err)
			}
			want := []string{"100", "50"}
			itemID := "101"
			switch name {
			case "index cleaned":
				if err := db.Db.Where("id > 0").Delete(&EmbyMediaItem{}).Error; err != nil {
					t.Fatal(err)
				}
			case "wrong connection", "wrong identity", "corrupt history":
				column, value := "server_config_key", "different"
				if name == "wrong identity" {
					column = "identity_key"
				} else if name == "corrupt history" {
					column, value = "item_json", "{"
				}
				if err := db.Db.Model(&EmbyItemEvidence{}).Where("id = ?", state.SnapshotID).Update(column, value).Error; err != nil {
					t.Fatal(err)
				}
				want = nil
			case "missing target":
				itemID, want = "999", nil
			}
			token, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			ids, err := LoadEmbyItemRecoveryReadIDs(t.Context(), token, itemID)
			if name == "corrupt history" {
				if err == nil {
					t.Fatal("corrupt history accepted")
				}
			} else if err != nil || !slices.Equal(ids, want) {
				t.Fatalf("hints=%v want=%v err=%v", ids, want, err)
			}
			var after EmbyItemState
			if err := db.Db.First(&after, state.ID).Error; err != nil || after != state {
				t.Fatalf("read hint mutated state: %+v err=%v", after, err)
			}
		})
	}
}

func TestEmbyItemRecoveryReadIDsRejectStaleToken(t *testing.T) {
	config, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.PartOfItemID = "100"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEmbyItemRecoveryReadIDs(t.Context(), token, "101"); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("evidence newer than pre-GET token accepted: %v", err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		_, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"101"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadEmbyItemRecoveryReadIDs(t.Context(), token, "101"); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("pre-deletion token accepted: %v", err)
	}
}
