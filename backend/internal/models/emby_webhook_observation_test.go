package models

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

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
			result := ExecuteEmbyDeletionTarget(t.Context(), plan, target, &embyDeleteTestProvider{}, func(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error {
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
