package models

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbyObservedEvidenceUsesNewestSuccessfulRead(t *testing.T) {
	for _, tc := range []struct {
		name             string
		latestObservedAt int64
	}{
		{name: "equal observation timestamps", latestObservedAt: 200},
		{name: "observation clock moved backwards", latestObservedAt: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, token, file := setupEmbyWebhookModelTest(t)
			olderReceipt := saveEmbyObservationReceipt(t, file)
			now := time.Now().Unix()
			if err := MarkEmbyWebhookObserved(t.Context(), olderReceipt); err != nil {
				t.Fatal(err)
			}
			if err := FinishEmbyWebhook(t.Context(), olderReceipt, EmbyWebhookRetry, "temporary GET failure", now+3600, true); err != nil {
				t.Fatal(err)
			}
			newerReceipt := saveEmbyObservationReceipt(t, file)
			if err := SaveEmbyObservedEvidence(t.Context(), newerReceipt, token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			if err := FinishEmbyWebhook(t.Context(), newerReceipt, EmbyWebhookRetry, "index busy", now+7200, false); err != nil {
				t.Fatal(err)
			}
			file.FileId, file.PickCode, file.Sha1 = "f2", "p2", "sha2"
			if err := db.Db.Save(&file).Error; err != nil {
				t.Fatal(err)
			}
			recovered, err := ClaimEmbyWebhook(t.Context(), now+3601)
			if err != nil || recovered == nil || recovered.ID != olderReceipt.ID {
				t.Fatalf("wrong recovered receipt: %+v err=%v", recovered, err)
			}
			fresh, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			if err := SaveEmbyObservedEvidence(t.Context(), *recovered, fresh, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			if err := FinishEmbyWebhook(t.Context(), *recovered, EmbyWebhookRetry, "index busy", now+7200, false); err != nil {
				t.Fatal(err)
			}
			for id, timestamp := range map[uint]int64{olderReceipt.ID: tc.latestObservedAt, newerReceipt.ID: 200} {
				if err := db.Db.Model(&EmbyWebhookRecord{}).Where("id = ?", id).Update("observed_at", timestamp).Error; err != nil {
					t.Fatal(err)
				}
			}
			var before []EmbyItemEvidence
			if err := db.Db.Order("id").Find(&before).Error; err != nil {
				t.Fatal(err)
			}
			deleted, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
			if err != nil {
				t.Fatal(err)
			}
			input := webhookModelInput(t, deleted)
			if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
				t.Fatalf("owner count=%d, expected one frozen file", len(input.Owners))
			}
			if got := input.Owners[0].Files[0].FileID; got != "f2" {
				t.Fatalf("adopted file=%q want latest observation=f2", got)
			}
			var after []EmbyItemEvidence
			if err := db.Db.Order("id").Find(&after).Error; err != nil || !slices.Equal(before, after) {
				t.Fatalf("adoption rewrote immutable evidence: before=%d rows after=%d rows err=%v", len(before), len(after), err)
			}
			assertWebhookCount(t, &EmbyMediaItem{}, 0)
			assertWebhookCount(t, &EmbyMediaSyncFile{}, 0)
		})
	}
}

func saveEmbyObservationReceipt(t *testing.T, file SyncFile) EmbyWebhookRecord {
	t.Helper()
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.modified", file)); err != nil {
		t.Fatal(err)
	}
	return webhookModelClaim(t)
}

func TestEmbyObservedEvidenceAdoptionRebindsExistingIndex(t *testing.T) {
	for _, failReceipt := range []bool{false, true} {
		name := "new physical identity"
		if failReceipt {
			name = "receipt rollback"
		}
		t.Run(name, func(t *testing.T) {
			config, token, originalFile := setupEmbyWebhookModelTest(t)
			snapshot := snapshotForFile(originalFile)
			snapshot.Item.LastSeenSyncRun, snapshot.Item.LastSeenAt = "full-1", 123
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			var before EmbyMediaItem
			if err := db.Db.First(&before).Error; err != nil {
				t.Fatal(err)
			}
			file := originalFile
			file.BaseModel = BaseModel{}
			file.FileId, file.PickCode, file.Sha1 = "f2", "p2", "sha2"
			if err := db.Db.Create(&file).Error; err != nil {
				t.Fatal(err)
			}
			token, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			observed := snapshotForFile(file)
			observed.Sources[0].ID = "new-source"
			if err := SaveEmbyObservedEvidence(t.Context(), saveEmbyObservationReceipt(t, file), token, []EmbyItemSnapshot{observed}); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("receipt failed after adoption")
			if failReceipt {
				if err := db.Db.Callback().Create().Before("gorm:create").Register("test:observation_receipt_fail", func(tx *gorm.DB) {
					if tx.Statement.Table == "emby_webhook_records" {
						tx.AddError(injected)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Create().Remove("test:observation_receipt_fail") })
			}
			deleted, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
			if failReceipt && !errors.Is(err, injected) || !failReceipt && err != nil {
				t.Fatal(err)
			}
			var current EmbyMediaItem
			var state EmbyItemState
			var links []EmbyMediaSyncFile
			for _, model := range []any{&current, &state, &links} {
				if err := db.Db.Find(model).Error; err != nil {
					t.Fatal(err)
				}
			}
			if current.LastSeenSyncRun != before.LastSeenSyncRun || current.LastSeenAt != before.LastSeenAt {
				t.Fatal("adoption changed ordinary sync markers")
			}
			if failReceipt {
				if current != before || state.Deleted || state.SnapshotID != before.SnapshotID || len(links) != 1 || links[0].SyncFileId != originalFile.ID || links[0].SnapshotID != before.SnapshotID {
					t.Fatalf("receipt failure left partial adoption: item=%+v state=%+v links=%+v", current, state, links)
				}
				return
			}
			input := webhookModelInput(t, deleted)
			if len(input.Owners) != 1 || len(input.Owners[0].Links) != 1 {
				t.Fatalf("adopted owner did not capture its existing link: %+v", input.Owners)
			}
			owner := input.Owners[0]
			if current.SnapshotID != owner.Evidence.ID || current.Generation != before.Generation+1 || current.Generation != state.Generation {
				t.Fatalf("index identity differs from adopted evidence: item=%+v evidence=%+v", current, owner.Evidence)
			}
			if len(links) != 1 || links[0].SnapshotID != current.SnapshotID || links[0].SyncFileId != file.ID || links[0].PickCode != file.PickCode || links[0].SourceID != "new-source" {
				t.Fatalf("old file association survived adoption: %+v", links)
			}
			plan := webhookModelPlan(t, deleted)
			if len(plan.Targets) != 1 {
				t.Fatalf("unexpected targets: %+v", plan.Targets)
			}
			if err := validateEmbyDeletionTarget(t.Context(), plan, plan.Targets[0]); err != nil {
				t.Fatalf("adopted item blocks its own deletion: %v", err)
			}
		})
	}
}

func TestEmbyObservedEvidenceInvalidCandidateDoesNotHideValidRead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*embyObservedRef)
	}{
		{name: "state row", mutate: func(ref *embyObservedRef) { ref.StateID++ }},
		{name: "state revision", mutate: func(ref *embyObservedRef) { ref.StateRevision++ }},
		{name: "snapshot", mutate: func(ref *embyObservedRef) { ref.SnapshotID++ }},
		{name: "generation", mutate: func(ref *embyObservedRef) { ref.Generation++ }},
		{name: "identity", mutate: func(ref *embyObservedRef) { ref.IdentityKey = "different-identity" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, token, file := setupEmbyWebhookModelTest(t)
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			fresh, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			valid := snapshotForFile(file)
			valid.Item.Name = "valid observation"
			if err := SaveEmbyObservedEvidence(t.Context(), saveEmbyObservationReceipt(t, file), fresh, []EmbyItemSnapshot{valid}); err != nil {
				t.Fatal(err)
			}
			invalid := snapshotForFile(file)
			invalid.Item.Name = "invalid observation"
			record := saveEmbyObservationReceipt(t, file)
			if err := SaveEmbyObservedEvidence(t.Context(), record, fresh, []EmbyItemSnapshot{invalid}); err != nil {
				t.Fatal(err)
			}
			if err := db.Db.First(&record, record.ID).Error; err != nil {
				t.Fatal(err)
			}
			var refs []embyObservedRef
			if err := json.Unmarshal([]byte(record.ObservationJSON), &refs); err != nil || len(refs) != 1 {
				t.Fatalf("invalid fixture refs=%+v err=%v", refs, err)
			}
			tc.mutate(&refs[0])
			if err := db.Db.Model(&record).Update("observation_json", embyJSON(refs)).Error; err != nil {
				t.Fatal(err)
			}
			deleted, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
			if err != nil {
				t.Fatal(err)
			}
			input := webhookModelInput(t, deleted)
			if len(input.Owners) != 1 {
				t.Fatalf("owner count=%d want=1", len(input.Owners))
			}
			if got := input.Owners[0].Item.Name; got != valid.Item.Name {
				t.Fatalf("invalid candidate hid the valid observation: got=%q want=%q", got, valid.Item.Name)
			}
		})
	}
}

func TestEmbyObservedEvidenceNewestMembershipPreventsOlderAdoption(t *testing.T) {
	for _, itemType := range []string{"Movie", "Season", "Series"} {
		t.Run(itemType, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			olderReceipt := saveEmbyObservationReceipt(t, file)
			newerReceipt := saveEmbyObservationReceipt(t, file)
			old := snapshotForFile(file)
			old.Item.Type = "Episode"
			switch itemType {
			case "Movie":
				old.Item.Type, old.Item.PartOfItemID = "Video", "201"
			case "Season":
				old.Item.SeasonId = "201"
			case "Series":
				old.Item.SeriesId = "201"
			}
			if err := SaveEmbyObservedEvidence(t.Context(), newerReceipt, token, []EmbyItemSnapshot{old}); err != nil {
				t.Fatal(err)
			}
			latest := old
			latest.Item.PartOfItemID, latest.Item.SeasonId, latest.Item.SeriesId = "202", "202", "202"
			if err := SaveEmbyObservedEvidence(t.Context(), olderReceipt, token, []EmbyItemSnapshot{latest}); err != nil {
				t.Fatal(err)
			}
			deleted, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{
				Event: "library.deleted", ServerID: "server-a", ItemID: "201", ItemType: itemType, Source: "official",
			})
			if err != nil {
				t.Fatal(err)
			}
			input := webhookModelInput(t, deleted)
			if len(input.Owners) != 0 {
				t.Fatalf("latest observation moved out of scope but old membership was adopted: item=%s", input.Owners[0].Item.ItemId)
			}
			var count int64
			if err := db.Db.Model(&EmbyItemState{}).Where("item_id = ?", "101").Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("out-of-scope observation changed item state: count=%d err=%v", count, err)
			}
		})
	}
}

func prepareEmbyObservedSurvivor(t *testing.T, event string) (*EmbyConfig, SyncFile, EmbyWebhookRecord, EmbyWebhookRecord, EmbyIndexToken) {
	t.Helper()
	config, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
		t.Fatal(err)
	}
	deleted := webhookModelClaim(t)
	if err := FinishEmbyWebhook(t.Context(), deleted, EmbyWebhookUnresolved, "API unavailable after bounded retries", 0, false); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&deleted, deleted.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope(event, file)); err != nil {
		t.Fatal(err)
	}
	observed := webhookModelClaim(t)
	beforeGET, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	return config, file, deleted, observed, beforeGET
}

func TestEmbyObservedSurvivorAdmissionRequiresFreshReadAfterRelease(t *testing.T) {
	for _, event := range []string{"library.new", "library.modified"} {
		t.Run(event, func(t *testing.T) {
			config, file, deleted, observed, _ := prepareEmbyObservedSurvivor(t, event)
			if err := db.Db.Transaction(func(tx *gorm.DB) error {
				_, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"102"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			beforeGET, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			admitted, err := AdmitEmbyObservedSurvivors(t.Context(), observed, beforeGET, []string{"101"})
			if err != nil || !admitted {
				t.Fatalf("confirmed main item did not release old barrier: admitted=%v err=%v", admitted, err)
			}
			var main, other EmbyItemState
			if err := db.Db.Where("item_id = ?", "101").First(&main).Error; err != nil || main.Deleted {
				t.Fatalf("main barrier not released: %+v, %v", main, err)
			}
			if err := db.Db.Where("item_id = ?", "102").First(&other).Error; err != nil || !other.Deleted {
				t.Fatalf("unrelated barrier released: %+v, %v", other, err)
			}
			for _, err := range []error{
				SaveEmbyObservedEvidence(t.Context(), observed, beforeGET, []EmbyItemSnapshot{snapshotForFile(file)}),
				ApplyEmbySnapshots(beforeGET, []EmbyItemSnapshot{snapshotForFile(file)}),
			} {
				if !errors.Is(err, ErrEmbySnapshotStale) {
					t.Fatalf("pre-release data accepted: %v", err)
				}
			}
			fresh, err := BeginEmbyIndexRead("server-a", config)
			if err != nil || fresh.Revision <= beforeGET.Revision {
				t.Fatalf("admission did not advance revision: %+v, %v", fresh, err)
			}
			if err := ApplyEmbySnapshots(fresh, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal("fresh sync still blocked", err)
			}
			var unchanged EmbyWebhookRecord
			if err := db.Db.First(&unchanged, deleted.ID).Error; err != nil || unchanged != deleted {
				t.Fatalf("admission changed old deletion status/authorization: %+v, %v", unchanged, err)
			}
		})
	}
}

func TestEmbyObservedSurvivorAdmissionRejectsConflicts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  string
		mutate func(*testing.T, *EmbyWebhookRecord, *EmbyIndexToken, SyncFile)
	}{
		{name: "deleted event", event: "library.deleted"},
		{name: "lost claim", mutate: func(t *testing.T, _ *EmbyWebhookRecord, _ *EmbyIndexToken, _ SyncFile) {
			if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			_ = webhookModelClaim(t)
		}},
		{name: "newer deletion while GET", mutate: func(t *testing.T, _ *EmbyWebhookRecord, _ *EmbyIndexToken, file SyncFile) {
			if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "snapshot beyond read watermark", mutate: func(t *testing.T, _ *EmbyWebhookRecord, token *EmbyIndexToken, _ SyncFile) {
			if err := db.Db.Model(&EmbyItemState{}).Where("item_id = ?", "101").Update("snapshot_id", token.EvidenceHighWatermark+1).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "server mismatch", mutate: func(_ *testing.T, _ *EmbyWebhookRecord, token *EmbyIndexToken, _ SyncFile) {
			token.ServerID = "server-b"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := tc.event
			if event == "" {
				event = "library.new"
			}
			_, file, deleted, record, token := prepareEmbyObservedSurvivor(t, event)
			if tc.mutate != nil {
				tc.mutate(t, &record, &token, file)
			}
			if admitted, err := AdmitEmbyObservedSurvivors(t.Context(), record, token, []string{"101"}); err == nil || admitted {
				t.Fatalf("conflicting admission succeeded: %v, %v", admitted, err)
			}
			var state EmbyItemState
			if err := db.Db.Where("item_id = ?", "101").First(&state).Error; err != nil || !state.Deleted {
				t.Fatalf("conflict released barrier: %+v, %v", state, err)
			}
			var unchanged EmbyWebhookRecord
			if err := db.Db.First(&unchanged, deleted.ID).Error; err != nil || unchanged != deleted {
				t.Fatalf("old receipt changed: %+v, %v", unchanged, err)
			}
		})
	}
}

func TestEmbyVerifiedSurvivorsGroupAndBarrierPagination(t *testing.T) {
	config, _, _ := setupEmbyWebhookModelTest(t)
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		_, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"101", "102", "103"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := LoadEmbyDeletionBarriers(t.Context(), token, 0, 2)
	if err != nil || len(first) != 2 || first[0].ItemID != "101" || first[1].ItemID != "102" {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := LoadEmbyDeletionBarriers(t.Context(), token, first[1].ID, 2)
	if err != nil || len(second) != 1 || second[0].ItemID != "103" {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	if admitted, err := AdmitEmbyVerifiedSurvivors(t.Context(), token, []string{"101", "102", "101"}); err != nil || !admitted {
		t.Fatalf("group admit=%v err=%v", admitted, err)
	}
	fresh, err := BeginEmbyIndexRead("server-a", config)
	if err != nil || fresh.Revision != token.Revision+1 {
		t.Fatalf("group must advance once: %+v err=%v", fresh, err)
	}
	remaining, err := LoadEmbyDeletionBarriers(t.Context(), fresh, 0, 2)
	if err != nil || len(remaining) != 1 || remaining[0].ItemID != "103" {
		t.Fatalf("unverified IDs released or verified IDs retained: %+v err=%v", remaining, err)
	}
	if _, err := LoadEmbyDeletionBarriers(t.Context(), token, 0, 2); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("old scan token accepted", err)
	}
	assertWebhookCount(t, &EmbyWebhookRecord{}, 0)
}

func TestEmbyVerifiedSurvivorsGroupRollsBackOnAnyConflict(t *testing.T) {
	for _, conflict := range []string{"watermark", "second update failure"} {
		t.Run(conflict, func(t *testing.T) {
			config, _, _ := setupEmbyWebhookModelTest(t)
			if err := db.Db.Transaction(func(tx *gorm.DB) error {
				_, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"101", "102"})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			token, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			if conflict == "watermark" {
				if err := db.Db.Model(&EmbyItemState{}).Where("item_id = ?", "102").Update("snapshot_id", token.EvidenceHighWatermark+1).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				updates := 0
				if err := db.Db.Callback().Update().Before("gorm:update").Register("test:group_admit_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "emby_item_states" {
						updates++
						if updates == 2 {
							tx.AddError(errors.New("second member failed"))
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove("test:group_admit_failure") })
			}
			if admitted, err := AdmitEmbyVerifiedSurvivors(t.Context(), token, []string{"101", "102"}); err == nil || admitted {
				t.Fatalf("partial group reported admitted=%v err=%v", admitted, err)
			}
			barriers, err := LoadEmbyDeletionBarriers(t.Context(), token, 0, 2)
			if err != nil || len(barriers) != 2 {
				t.Fatalf("failed group partially released barrier/global revision: %+v err=%v", barriers, err)
			}
		})
	}
}

func TestEmbyObservedSurvivorsRequireMainIDInVerifiedGroup(t *testing.T) {
	_, _, _, record, token := prepareEmbyObservedSurvivor(t, "library.new")
	if admitted, err := AdmitEmbyObservedSurvivors(t.Context(), record, token, []string{"102"}); err == nil || admitted {
		t.Fatalf("group without main ID authorized: %v, %v", admitted, err)
	}
	barriers, err := LoadEmbyDeletionBarriers(t.Context(), token, 0, 2)
	if err != nil || len(barriers) != 1 || barriers[0].ItemID != "101" {
		t.Fatalf("incorrect group changed barrier: %+v err=%v", barriers, err)
	}
}

func prepareEmbyHiddenBarrier(t *testing.T) (*EmbyConfig, SyncFile, SyncFile, EmbyWebhookRecord, EmbyDeletionPlan) {
	t.Helper()
	config, token, rootFile := setupEmbyWebhookModelTest(t)
	partFile := rootFile
	partFile.BaseModel = BaseModel{}
	partFile.FileId, partFile.PickCode, partFile.FileName = "part-file", "part-code", "movie-part2.mkv"
	partFile.LocalFilePath = strings.TrimSuffix(rootFile.LocalFilePath, ".strm") + "-part2.strm"
	if err := db.Db.Create(&partFile).Error; err != nil {
		t.Fatal(err)
	}
	root, part := snapshotForFile(rootFile), snapshotForFile(partFile)
	root.Item.PartCount = 2
	part.Item.ItemId, part.Item.Type, part.Item.PartOfItemID = "102", "Video", "101"
	part.Sources[0].ItemID = "102"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{root, part}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("deep.delete", rootFile)); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	plan := webhookModelPlan(t, record)
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyWebhook(t.Context(), record, EmbyWebhookUnresolved, "verification unavailable", 0, false); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&record, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		return AdmitEmbyItemGenerationTx(tx, "server-a", "101", record.DeletionRevision)
	}); err != nil {
		t.Fatal(err)
	}
	return config, rootFile, partFile, record, plan
}

func TestEmbyDeletionBarrierReadIDsUseOnlyReliableHistory(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		removeIndex, wrongConnection bool
	}{
		{name: "current index"},
		{name: "index already cleaned", removeIndex: true},
		{name: "untrusted history connection", wrongConnection: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, _, _, _, _ := prepareEmbyHiddenBarrier(t)
			if tc.removeIndex {
				if err := db.Db.Where("id > 0").Delete(&EmbyMediaItem{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tc.wrongConnection {
				if err := db.Db.Model(&EmbyItemEvidence{}).Where("item_id = ?", "102").Update("server_config_key", "different-connection").Error; err != nil {
					t.Fatal(err)
				}
			}
			token, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			barriers, err := LoadEmbyDeletionBarriers(t.Context(), token, 0, 100)
			if err != nil || len(barriers) != 1 {
				t.Fatalf("barriers=%+v err=%v", barriers, err)
			}
			ids, err := LoadEmbyDeletionBarrierReadIDs(t.Context(), token, barriers)
			want := []string{"102", "101"}
			if tc.wrongConnection {
				want = []string{"102"}
			}
			if err != nil || !slices.Equal(ids, want) {
				t.Fatalf("read IDs=%v want=%v err=%v", ids, want, err)
			}
			var state EmbyItemState
			if err := db.Db.First(&state, barriers[0].ID).Error; err != nil || state != barriers[0] {
				t.Fatalf("read hint changed barrier: %+v err=%v", state, err)
			}
		})
	}
}

func TestEmbyCompleteParentDetachesOnlyCurrentDeletedMemberEdge(t *testing.T) {
	config, rootFile, _, record, plan := prepareEmbyHiddenBarrier(t)
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	var beforeState EmbyItemState
	if err := db.Db.Where("item_id = ?", "102").First(&beforeState).Error; err != nil {
		t.Fatal(err)
	}
	var beforeEvidence EmbyItemEvidence
	if err := db.Db.First(&beforeEvidence, beforeState.SnapshotID).Error; err != nil {
		t.Fatal(err)
	}
	var beforeLinks []EmbyMediaSyncFile
	if err := db.Db.Where("emby_item_id = ?", 102).Find(&beforeLinks).Error; err != nil {
		t.Fatal(err)
	}
	currentRoot := snapshotForFile(rootFile)
	currentRoot.Item.PartCount = 1
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{currentRoot}); err != nil {
		t.Fatal("complete surviving parent still blocked by absent part", err)
	}
	var member EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "102").First(&member).Error; err != nil || member.PartOfItemID != "" || member.SnapshotID != beforeState.SnapshotID || member.Generation != beforeState.Generation {
		t.Fatalf("current member edge not safely detached: %+v err=%v", member, err)
	}
	var state EmbyItemState
	if err := db.Db.First(&state, beforeState.ID).Error; err != nil || state != beforeState || !state.Deleted {
		t.Fatalf("absent part admitted or state replaced: %+v err=%v", state, err)
	}
	var evidence EmbyItemEvidence
	if err := db.Db.First(&evidence, beforeEvidence.ID).Error; err != nil || evidence != beforeEvidence {
		t.Fatalf("historical member evidence changed: %+v err=%v", evidence, err)
	}
	var historicMember EmbyMediaItem
	if err := json.Unmarshal([]byte(evidence.ItemJSON), &historicMember); err != nil || historicMember.PartOfItemID != "101" {
		t.Fatalf("history lost parent relation: %+v err=%v", historicMember, err)
	}
	var links []EmbyMediaSyncFile
	if err := db.Db.Where("emby_item_id = ?", 102).Find(&links).Error; err != nil || !slices.Equal(links, beforeLinks) {
		t.Fatalf("pending links changed: %+v err=%v", links, err)
	}
	var unchanged EmbyWebhookRecord
	if err := db.Db.First(&unchanged, record.ID).Error; err != nil || unchanged != record {
		t.Fatalf("old deletion plan/status/authorization changed: %+v err=%v", unchanged, err)
	}
	verified := 0
	for _, target := range plan.Targets {
		if len(target.Owners) == 1 && target.Owners[0].ItemID == "102" {
			result := executeEmbyDeletionTestTarget(t.Context(), plan, target, &embyDeleteTestProvider{}, func(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error {
				verified++
				return ErrEmbyDeleteUnverified
			})
			if result.Outcome != EmbyDeletionUnresolved {
				t.Fatalf("old fixed part target lost safe verification path: %+v", result)
			}
		}
	}
	if verified != 1 {
		t.Fatalf("pending part identity no longer reaches independent verifier: %d", verified)
	}
}
