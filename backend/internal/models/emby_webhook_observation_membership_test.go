package models

import (
	"context"
	"path"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbyObservedEvidenceExcludesMovedCurrentMembership(t *testing.T) {
	for _, itemType := range []string{"Movie", "Season", "Series"} {
		t.Run(itemType, func(t *testing.T) {
			config, _, seed := setupEmbyWebhookModelTest(t)
			assertEmbyObservedCurrentMembership(t, config, seed, itemType, "moved", true)
		})
	}
}

func TestEmbyObservedEvidencePreservesUnconfirmedCurrentMembership(t *testing.T) {
	for _, scenario := range []string{"no observation", "unknown membership", "newer membership restored", "state changed", "invalid expected state", "other connection", "other server"} {
		t.Run(scenario, func(t *testing.T) {
			config, _, seed := setupEmbyWebhookModelTest(t)
			assertEmbyObservedCurrentMembership(t, config, seed, "Season", scenario, false)
		})
	}
}

func assertEmbyObservedCurrentMembership(t *testing.T, config *EmbyConfig, seed SyncFile, itemType, scenario string, wantExcluded bool) {
	t.Helper()
	withScopedEvidence := wantExcluded && itemType == "Season"
	ids := []string{"101", "102"}
	if itemType == "Movie" {
		ids = append(ids, "201")
	}
	var snapshots []EmbyItemSnapshot
	for _, id := range ids {
		file := seed
		file.BaseModel = BaseModel{}
		file.FileId, file.PickCode, file.Sha1 = "member-"+id, "pick-"+id, "sha-"+id
		file.FileName = "member-" + id + ".mkv"
		file.LocalFilePath = filepath.Join(filepath.Dir(seed.LocalFilePath), "member-"+id+".strm")
		if withScopedEvidence && id == "101" {
			file.Path, file.ParentId = path.Join(seed.Path, "Moved", "Season 1"), "moved-season-dir"
			file.LocalFilePath = filepath.Join(filepath.Dir(seed.LocalFilePath), "Moved", "Season 1", "member-101.strm")
		}
		if err := db.Db.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
		snapshot := snapshotForFile(file)
		snapshot.Item.ItemId, snapshot.Sources[0].ItemID = id, id
		snapshot.Sources[0].ID = "source-" + id
		if itemType == "Movie" {
			if id == "201" {
				snapshot.Item.PartCount = 3
			} else {
				snapshot.Item.Type, snapshot.Item.PartOfItemID = "Video", "201"
			}
		} else {
			snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "201", "201"
			if scenario == "unknown membership" && id == "101" {
				snapshot.Item.Type, snapshot.Item.PartOfItemID = "Video", "102"
			}
		}
		if withScopedEvidence && id == "101" {
			// 只有待移出成员提供季目录、剧目录和独立季海报的历史证据。
			snapshot.Item.ParentIndexNumber = 1
			poster := file
			poster.BaseModel = BaseModel{}
			poster.FileId, poster.FileName, poster.ParentId = "moved-season-poster", "season01-poster.jpg", "moved-series-dir"
			poster.PickCode, poster.Sha1, poster.IsVideo, poster.IsMeta = "poster-pick", "poster-sha", false, true
			poster.Path = path.Dir(file.Path)
			poster.LocalFilePath = filepath.Join(filepath.Dir(filepath.Dir(file.LocalFilePath)), poster.FileName)
			if err := db.Db.Create(&poster).Error; err != nil {
				t.Fatal(err)
			}
			frozen, err := freezeEmbyFile(db.Db, file, "", file.LocalFilePath)
			if err != nil {
				t.Fatal(err)
			}
			nodes := []EmbyDirectoryAncestor{
				{FileID: "0", Path: "/"}, {FileID: frozen.RootFileID, ParentID: "0", Path: seed.Path},
				{FileID: "moved-series-dir", ParentID: frozen.RootFileID, Path: poster.Path},
				{FileID: file.ParentId, ParentID: "moved-series-dir", Path: file.Path},
			}
			key := embyDigest([]any{frozen.SourceType, frozen.AccountIdentity, frozen.AccountID, frozen.ParentID, frozen.Path})
			collector := EmbyDirectoryEvidenceCollector{chains: map[string]embyObservedDirectoryChain{key: {Nodes: nodes, At: time.Now()}}}
			seasonNumber := 1
			if err := collector.Enrich(t.Context(), &snapshot, []EmbyMediaDirectory{
				{ItemID: "201", MediaType: "Season", LocalPath: filepath.Dir(file.LocalFilePath), SeasonNumber: &seasonNumber},
				{ItemID: "201", MediaType: "Series", LocalPath: filepath.Dir(poster.LocalFilePath)},
			}); err != nil {
				t.Fatal(err)
			}
		}
		snapshots = append(snapshots, snapshot)
		if id == "201" {
			continue
		}
		sidecar := file
		sidecar.BaseModel = BaseModel{}
		sidecar.FileId, sidecar.FileName = "member-"+id+"-nfo", "member-"+id+".nfo"
		sidecar.PickCode, sidecar.Sha1 = "nfo-pick-"+id, "nfo-sha-"+id
		sidecar.LocalFilePath = filepath.Join(filepath.Dir(file.LocalFilePath), sidecar.FileName)
		sidecar.IsVideo, sidecar.IsMeta = false, true
		if err := db.Db.Create(&sidecar).Error; err != nil {
			t.Fatal(err)
		}
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, snapshots); err != nil {
		t.Fatal(err)
	}
	token, err = BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	var baseline EmbyDeletionInput
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		baseline, err = CaptureEmbyDeletionTx(tx, "server-a", "201", itemType)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(baseline.Owners) != len(ids) || len(baseline.Sidecars) != 2 {
		t.Fatalf("incomplete historical fixture: owners=%d sidecars=%d", len(baseline.Owners), len(baseline.Sidecars))
	}
	if withScopedEvidence && (len(baseline.DirectoryScopes) != 1 || baseline.DirectoryScopes[0].Root.FileID != "moved-season-dir" || len(baseline.ScopedFiles) != 1 || baseline.ScopedFiles[0].File.FileID != "moved-season-poster") {
		t.Fatalf("missing historical directory or scoped metadata: directories=%+v scoped=%+v", baseline.DirectoryScopes, baseline.ScopedFiles)
	}
	if scenario != "no observation" {
		moved := snapshots[0]
		moved.Item.PartOfItemID, moved.Item.SeasonId, moved.Item.SeriesId = "202", "202", "202"
		if scenario == "unknown membership" {
			moved.Item.PartOfItemID, moved.Item.SeasonId, moved.Item.SeriesId = "102", "", ""
		}
		observed := saveEmbyMembershipObservation(t, token, moved)
		switch scenario {
		case "newer membership restored":
			saveEmbyMembershipObservation(t, token, snapshots[0])
		case "state changed":
			updated := snapshots[0]
			updated.Item.Name = "updated by normal sync"
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{updated}); err != nil {
				t.Fatal(err)
			}
		case "invalid expected state":
			if err := db.Db.Model(&EmbyItemState{}).Where("server_id = ? AND item_id = ?", "server-a", "101").UpdateColumn("revision", gorm.Expr("revision + 1")).Error; err != nil {
				t.Fatal(err)
			}
		case "other connection":
			if err := db.Db.Model(&observed).Update("server_config_key", "previous-connection").Error; err != nil {
				t.Fatal(err)
			}
		case "other server":
			if err := db.Db.Model(&observed).Update("server_id", "server-b").Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	var beforeState EmbyItemState
	if err := db.Db.Where("server_id = ? AND item_id = ?", "server-a", "101").First(&beforeState).Error; err != nil {
		t.Fatal(err)
	}
	var beforeItem EmbyMediaItem
	if err := db.Db.Where("server_id = ? AND item_id = ?", "server-a", "101").First(&beforeItem).Error; err != nil {
		t.Fatal(err)
	}
	deleted, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{Event: "library.deleted", ServerID: "server-a", ItemID: "201", ItemType: itemType, Source: "official"})
	if err != nil {
		t.Fatal(err)
	}
	input := webhookModelInput(t, deleted)
	var gotIDs []string
	for _, owner := range input.Owners {
		gotIDs = append(gotIDs, owner.Item.ItemId)
	}
	wantIDs := slices.Clone(ids)
	if wantExcluded {
		wantIDs = wantIDs[1:]
	}
	slices.Sort(gotIDs)
	slices.Sort(wantIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("captured owners=%v want=%v", gotIDs, wantIDs)
	}
	var afterState EmbyItemState
	if err := db.Db.First(&afterState, beforeState.ID).Error; err != nil {
		t.Fatal(err)
	}
	if wantExcluded && afterState != beforeState {
		t.Fatalf("excluded member acquired a barrier or changed state: before=%+v after=%+v", beforeState, afterState)
	}
	if !wantExcluded && !afterState.Deleted {
		t.Fatal("unconfirmed move suppressed the historical member's barrier")
	}
	var afterItem EmbyMediaItem
	if err := db.Db.First(&afterItem, beforeItem.ID).Error; err != nil {
		t.Fatal(err)
	}
	wantItem := beforeItem
	if scenario == "newer membership restored" {
		if afterState.SnapshotID == beforeState.SnapshotID {
			t.Fatal("latest restored membership observation was not adopted")
		}
		wantItem.SnapshotID, wantItem.Generation = afterState.SnapshotID, afterState.Generation
		wantItem.UpdatedAt = afterItem.UpdatedAt
	}
	if afterItem != wantItem {
		t.Fatal("adoption changed index business fields or left inconsistent identity")
	}
	if !wantExcluded {
		return
	}
	if len(input.Sidecars) != 1 || input.Sidecars[0].FileID != "member-102-nfo" {
		t.Fatalf("excluded member's sidecar remained: %+v", input.Sidecars)
	}
	if withScopedEvidence && (len(input.DirectoryScopes) != 0 || len(input.ScopedFiles) != 0) {
		t.Fatalf("excluded member retained directory or scoped metadata authority: directories=%+v scoped=%+v", input.DirectoryScopes, input.ScopedFiles)
	}
	var remaining EmbyItemState
	if err := db.Db.Where("server_id = ? AND item_id = ?", "server-a", "102").First(&remaining).Error; err != nil || !remaining.Deleted {
		t.Fatalf("remaining member has no deletion barrier: err=%v", err)
	}
	provider := &embyDeleteTestProvider{files: map[string]EmbyRemoteFile{}}
	for _, owner := range baseline.Owners {
		for _, file := range owner.Files {
			provider.files[file.FileID] = embyRemoteFromFrozen(file)
		}
	}
	for _, file := range baseline.Sidecars {
		provider.files[file.FileID] = embyRemoteFromFrozen(file)
	}
	for _, scoped := range baseline.ScopedFiles {
		provider.files[scoped.File.FileID] = embyRemoteFromFrozen(scoped.File)
	}
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	if record.ID != deleted.ID {
		t.Fatalf("claimed receipt=%d want deletion=%d", record.ID, deleted.ID)
	}
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	rows, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil || len(rows) != len(wantIDs)+1 {
		t.Fatalf("saved targets=%d want=%d err=%v", len(rows), len(wantIDs)+1, err)
	}
	for _, target := range plan.Targets {
		if withScopedEvidence && (target.Kind == "directory" || target.Kind == "scoped_metadata") {
			t.Fatalf("excluded member added a directory or scoped metadata target: %+v", target)
		}
		if slices.ContainsFunc(target.Owners, func(ref EmbyDeletionOwnerRef) bool { return ref.ItemID == "101" }) || target.File.FileID == "member-101" || target.File.FileID == "member-101-nfo" {
			t.Fatalf("excluded member retained a deletion target: %s", target.Key)
		}
	}
	groups, err := GroupEmbyDeletionTargets(plan.Targets, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range groups {
		attempt := ""
		results := ExecuteEmbyDeletionBatch(t.Context(), plan, group, provider, func(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error { return nil }, func(targets []EmbyDeletionTarget) error {
			var err error
			attempt, err = BeginEmbyWebhookAttempt(t.Context(), record, targets)
			return err
		})
		for _, result := range results {
			if result.Outcome != EmbyDeletionDeleted {
				t.Fatalf("remaining member target did not execute: result=%+v", result)
			}
		}
		if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, results); err != nil {
			t.Fatal(err)
		}
	}
	if _, exists := provider.files["member-101"]; !exists {
		t.Fatal("moved member's video was deleted")
	}
	if _, exists := provider.files["member-101-nfo"]; !exists {
		t.Fatal("moved member's sidecar was deleted")
	}
	if withScopedEvidence {
		if _, exists := provider.files["moved-season-poster"]; !exists {
			t.Fatal("moved member's scoped metadata was deleted")
		}
	}
}

func saveEmbyMembershipObservation(t *testing.T, token EmbyIndexToken, snapshot EmbyItemSnapshot) EmbyWebhookRecord {
	t.Helper()
	if _, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{Event: "library.modified", ServerID: token.ServerID, ItemID: snapshot.Item.ItemId, ItemType: snapshot.Item.Type, ItemPath: snapshot.Item.Path, Source: "official"}); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	if err := SaveEmbyObservedEvidence(t.Context(), record, token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestEmbyObservedEvidenceDirectDeletionIgnoresOldParent(t *testing.T) {
	for _, itemType := range []string{"Episode", "Video"} {
		t.Run(itemType, func(t *testing.T) {
			config, token, file := setupEmbyWebhookModelTest(t)
			snapshot := snapshotForFile(file)
			snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = itemType, "201", "201"
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			token, err := BeginEmbyIndexRead("server-a", config)
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Item.SeasonId, snapshot.Item.SeriesId = "202", "202"
			saveEmbyMembershipObservation(t, token, snapshot)
			envelope := webhookModelEnvelope("library.deleted", file)
			envelope.ItemType = itemType
			record, err := SaveEmbyWebhook(t.Context(), envelope)
			if err != nil {
				t.Fatal(err)
			}
			input := webhookModelInput(t, record)
			if len(input.Owners) != 1 || input.Owners[0].Item.ItemId != "101" || input.Owners[0].Item.SeasonId != "202" {
				t.Fatal("direct item deletion was suppressed by parent membership")
			}
		})
	}
}
