//go:build integration

package models

import (
	"fmt"
	"testing"
	"time"
)

func TestEmbyObservedIndexPostgresBatchesAndRebuild(t *testing.T) {
	for _, count := range []int{500, 501} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			_, _, _, token, _ := setupEmbyWebhookPostgres(t)
			assertEmbyObservedIndexBatches(t, token, count)
		})
	}
}

func TestEmbyObservedIndexPostgresHistoryDoesNotScaleReceiptReads(t *testing.T) {
	testEmbyObservedIndexHistoryReads(t, func(t *testing.T) (EmbyIndexToken, SyncFile) {
		conn, _, _, token, _ := setupEmbyWebhookPostgres(t)
		var file SyncFile
		if err := conn.First(&file).Error; err != nil {
			t.Fatal(err)
		}
		// 共用收件 fixture 的远端主 ID，保留 PostgreSQL 真实事务与批量加载。
		if err := conn.Where("1 = 1").Delete(&EmbyItemState{}).Error; err != nil {
			t.Fatal(err)
		}
		return token, file
	})
}

func TestEmbyWebhookPostgresObservationExcludesMovedCurrentMembership(t *testing.T) {
	for _, itemType := range []string{"Movie", "Season", "Series"} {
		t.Run(itemType, func(t *testing.T) {
			conn, _, config, _, _ := setupEmbyWebhookPostgres(t)
			var seed SyncFile
			if err := conn.First(&seed).Error; err != nil {
				t.Fatal(err)
			}
			assertEmbyObservedCurrentMembership(t, config, seed, itemType, "moved", true)
		})
	}
}

func TestEmbyWebhookPostgresObservationUsesSuccessfulReadOrder(t *testing.T) {
	conn, _, config, _, snapshot := setupEmbyWebhookPostgres(t)
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix() + 10
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.modified", snapshot.Item.ItemId)); err != nil {
		t.Fatal(err)
	}
	older := claimEmbyWebhookPostgres(t, now)
	if err := MarkEmbyWebhookObserved(t.Context(), older); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyWebhook(t.Context(), older, EmbyWebhookRetry, "temporary GET failure", now+3600, true); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.modified", snapshot.Item.ItemId)); err != nil {
		t.Fatal(err)
	}
	newer := claimEmbyWebhookPostgres(t, now)
	if err := SaveEmbyObservedEvidence(t.Context(), newer, token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyWebhook(t.Context(), newer, EmbyWebhookRetry, "index busy", now+7200, false); err != nil {
		t.Fatal(err)
	}
	var file SyncFile
	if err := conn.First(&file).Error; err != nil {
		t.Fatal(err)
	}
	file.FileId, file.PickCode, file.Sha1 = "video-2", "pick-2", "generation-2"
	if err := conn.Save(&file).Error; err != nil {
		t.Fatal(err)
	}
	snapshot.Sources[0].PickCode = file.PickCode
	recovered := claimEmbyWebhookPostgres(t, now+3601)
	if recovered.ID != older.ID {
		t.Fatalf("recovered receipt=%d want=%d", recovered.ID, older.ID)
	}
	fresh, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyObservedEvidence(t.Context(), recovered, fresh, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyWebhook(t.Context(), recovered, EmbyWebhookRetry, "index busy", now+7200, false); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&EmbyWebhookRecord{}).Where("id IN ?", []uint{older.ID, newer.ID}).Update("observed_at", now).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", snapshot.Item.ItemId))
	if err != nil {
		t.Fatal(err)
	}
	input := loadEmbyWebhookInputPostgres(t, deleted)
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
		t.Fatalf("owner count=%d, expected one frozen file", len(input.Owners))
	}
	if got := input.Owners[0].Files[0].FileID; got != file.FileId {
		t.Fatalf("adopted file=%q want latest observation=%q", got, file.FileId)
	}
	var current EmbyMediaItem
	if err := conn.First(&current).Error; err != nil || current.SnapshotID != input.Owners[0].Evidence.ID || current.Generation != input.Owners[0].Evidence.Generation {
		t.Fatalf("observation adoption left old index identity: snapshot=%d generation=%d err=%v", current.SnapshotID, current.Generation, err)
	}
	if links := input.Owners[0].Links; len(links) != 1 || links[0].SnapshotID != current.SnapshotID || links[0].SyncFileId != file.ID || links[0].PickCode != file.PickCode {
		t.Fatalf("observation adoption left old file association: %+v", links)
	}
}
