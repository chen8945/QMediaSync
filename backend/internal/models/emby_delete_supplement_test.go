package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func seedEmbyExternalDeletion(t *testing.T, conn *gorm.DB, sameObject bool) (EmbyWebhookRecord, EmbyDeletionPlan, *embyJointTestProvider) {
	t.Helper()
	if err := conn.AutoMigrate(&EmbyConfig{}, &Account{}, &SyncPath{}, &SyncFile{}, &SyncRootEvidence{}, &Settings{}, &EmbyLibrarySyncPath{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateEmbyDeletionSchema(conn); err != nil {
		t.Fatal(err)
	}
	config := EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "external-test", SyncEnabled: 1, EnableDeleteNetdisk: 1,
		PathMappings: []EmbyPathMapping{{EmbyRoot: "/emby", LocalRoot: t.TempDir()}},
	}
	config.PathMappingsJSON = embyJSON(config.PathMappings)
	account := Account{SourceType: SourceType115, UserId: "external-owner"}
	for _, value := range []any{&config, &account, &Settings{SettingStrm: SettingStrm{StrmBaseUrl: "https://qms.test"}}} {
		if err := conn.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	token, err := BeginEmbyIndexRead("server-a", &config)
	if err != nil {
		t.Fatal(err)
	}
	var snapshots []EmbyItemSnapshot
	for i := range 2 {
		id, code := fmt.Sprint(101+i), fmt.Sprintf("external-%d", i)
		source := EmbySnapshotSource{ID: "source-" + id, ItemID: id, PickCode: code, Path: "https://qms.test/115/newurl?userid=external-owner&pickcode=" + code}
		snapshots = append(snapshots, EmbyItemSnapshot{Item: EmbyMediaItem{ItemId: id, Type: "Episode", SeasonId: "777", SeriesId: "888", Path: "/emby/" + id + ".strm", PickCode: code, MediaSourcePath: source.Path}, Sources: []EmbySnapshotSource{source}, MembersComplete: true})
	}
	if err := ApplyEmbySnapshots(token, snapshots); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), EmbyWebhookEnvelope{Event: "library.deleted", ServerID: "server-a", ItemID: "777", ItemType: "Season", Source: "official"}); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	input := webhookModelInput(t, record)
	if len(input.Owners) != 2 {
		t.Fatalf("missing external owners: %+v", input)
	}
	provider := &embyJointTestProvider{deletionMatrix: &deletionMatrix{remote: map[string]EmbyRemoteFile{}, fail: map[string]bool{}}}
	var supplements []EmbyDeletionSupplement
	for i, owner := range input.Owners {
		if len(owner.Files) != 0 || len(owner.Links) != 0 {
			t.Fatal("external fixture acquired managed files")
		}
		var sources []EmbySnapshotSource
		if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &sources); err != nil || len(sources) != 1 {
			t.Fatalf("sources=%+v err=%v", sources, err)
		}
		source := sources[0]
		object := 1000 + i
		if sameObject {
			object = 1000
		}
		file := EmbyFrozenFile{ExternalSource: true, SourceType: source.SourceType, AccountID: source.AccountID, AccountIdentity: source.AccountIdentity,
			FileID: fmt.Sprint(object), ParentID: "0", Path: "/cloud/Season", FileName: fmt.Sprintf("%d.mkv", object), PickCode: source.ObjectID, SHA1: "original-content", FileSize: 0, MTime: 123,
			SourceID: source.ID, Reference: source.Reference,
		}
		// 替身详情携带本次真实读取时刻；计划构造和重开不能改写它。
		supplements = append(supplements, EmbyDeletionSupplement{Owner: embyOwnerRef(owner), Source: source, File: file, ReadAt: time.Now().UTC()})
		key := deletionMatrixKey(file.AccountID, file.FileID)
		if _, exists := provider.remote[key]; !exists {
			provider.remote[key] = embyRemoteFromFrozen(file)
		}
	}
	plan, err := BuildEmbyDeletionPlanWithSupplements(t.Context(), input, supplements, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }, allowEmbyDeleteTest)
	if err != nil || len(plan.Issues) != 0 {
		t.Fatalf("external plan: issues=%v err=%v", plan.Issues, err)
	}
	if embyJSON(plan.Input) != record.InputJSON {
		t.Fatal("supplement changed receipt input")
	}
	return record, plan, provider
}

func TestEmbyExternalDeletionMergesReferencesByStableObject(t *testing.T) {
	conn, _ := setupCleanupSQLite(t)
	_, plan, provider := seedEmbyExternalDeletion(t, conn, true)
	if len(plan.Targets) != 1 || len(plan.Targets[0].Owners) != 2 || len(plan.Targets[0].SourceBindings) != 2 {
		t.Fatalf("same physical file was not merged: %+v", plan.Targets)
	}
	checks := 0
	results := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, func(_ context.Context, input EmbyDeletionInput, target EmbyDeletionTarget) error {
		checks++
		if len(target.SourceBindings) != 2 || len(target.Owners) != 2 || len(input.Owners[0].Files) != 0 {
			t.Fatal("verifier lost source references or input was changed")
		}
		return nil
	}, func([]EmbyDeletionTarget) error { return nil })
	if len(results) != 1 || results[0].Outcome != EmbyDeletionDeleted || len(provider.batches) != 1 || len(provider.batches[0]) != 1 || checks < 2 {
		t.Fatalf("result=%+v batches=%v checks=%d", results, provider.batches, checks)
	}
	if err := FinalizeEmbyDeletionPlan(t.Context(), plan, results); err != nil {
		t.Fatal(err)
	}
	assertWebhookCount(t, &EmbyMediaItem{}, 0)
	assertWebhookCount(t, &EmbyItemEvidence{}, 2)
	assertWebhookCount(t, &SyncFile{}, 0)
	assertWebhookCount(t, &SyncPath{}, 0)
}

func TestEmbyExternalDeletionRejectsConflictingEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*testing.T, *EmbyDeletionPlan)
	}{
		{name: "account principal changed", mutate: func(t *testing.T, plan *EmbyDeletionPlan) {
			if err := db.Db.Model(&Account{}).Where("id = ?", plan.Targets[0].File.AccountID).Update("user_id", "new-owner").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "account source changed", mutate: func(t *testing.T, plan *EmbyDeletionPlan) {
			if err := db.Db.Model(&Account{}).Where("id = ?", plan.Targets[0].File.AccountID).Update("source_type", SourceTypeBaiduPan).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "snapshot replaced", mutate: func(t *testing.T, plan *EmbyDeletionPlan) {
			if err := db.Db.Model(&EmbyItemState{}).Where("item_id = ?", plan.Targets[0].Owners[0].ItemID).Update("snapshot_id", 999).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "source no longer original", mutate: func(_ *testing.T, plan *EmbyDeletionPlan) {
			plan.Supplements[0].Source.ObjectID = "replacement"
			plan.Targets[0].SourceBindings[0] = plan.Supplements[0]
		}},
		{name: "dropped shared reference", mutate: func(_ *testing.T, plan *EmbyDeletionPlan) {
			plan.Targets[0].SourceBindings = plan.Targets[0].SourceBindings[:1]
			plan.Targets[0].Owners = plan.Targets[0].Owners[:1]
		}},
		{name: "forged managed root", mutate: func(_ *testing.T, plan *EmbyDeletionPlan) {
			plan.Supplements[0].File.RemoteRoot = "/"
			plan.Targets[0].SourceBindings[0] = plan.Supplements[0]
		}},
		{name: "missing real read time", mutate: func(_ *testing.T, plan *EmbyDeletionPlan) {
			plan.Supplements[0].ReadAt = time.Time{}
			plan.Targets[0].SourceBindings[0] = plan.Supplements[0]
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, _ := setupCleanupSQLite(t)
			_, plan, provider := seedEmbyExternalDeletion(t, conn, true)
			tc.mutate(t, &plan)
			result := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })
			if len(result) != 1 || result[0].Outcome != EmbyDeletionUnresolved || len(provider.batches) != 0 {
				t.Fatalf("conflict accepted: %+v batches=%v", result, provider.batches)
			}
		})
	}
}

func TestEmbyExternalDeletionIncompleteOwnerDoesNotBlockIndependentCompletion(t *testing.T) {
	conn, _ := setupCleanupSQLite(t)
	record, full, provider := seedEmbyExternalDeletion(t, conn, false)
	plan, err := BuildEmbyDeletionPlanWithSupplements(t.Context(), full.Input, full.Supplements[:1], func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil || !slices.Contains(plan.Issues, "source_unresolved:"+full.Input.Owners[1].Item.ItemId) {
		t.Fatalf("missing unresolved owner: %+v %v", plan.Issues, err)
	}
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	results := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, allowEmbyDeleteTest, func([]EmbyDeletionTarget) error { return nil })
	if len(results) != 1 || results[0].Outcome != EmbyDeletionDeleted {
		t.Fatalf("results=%+v", results)
	}
	if err := FinalizeEmbyDeletionPlan(t.Context(), plan, results); err != nil {
		t.Fatal(err)
	}
	var remaining []EmbyMediaItem
	if err := conn.Find(&remaining).Error; err != nil || len(remaining) != 1 || remaining[0].ItemId != full.Input.Owners[1].Item.ItemId {
		t.Fatalf("wrong owner finalized: %+v %v", remaining, err)
	}
}

func TestEmbyExternalDeletionSourcePreflightUsesOriginalState(t *testing.T) {
	conn, _ := setupCleanupSQLite(t)
	_, plan, _ := seedEmbyExternalDeletion(t, conn, false)
	owner, source := plan.Input.Owners[0], plan.Supplements[0].Source
	if err := ValidateEmbyDeletionSource(t.Context(), plan.Input, owner, source); err != nil {
		t.Fatal(err)
	}
	forged := source
	forged.ObjectID = "different-object"
	if err := ValidateEmbyDeletionSource(t.Context(), plan.Input, owner, forged); !errors.Is(err, ErrEmbyIdentityAmbiguous) {
		t.Fatalf("preflight accepted source outside original snapshot: %v", err)
	}
	if err := conn.Model(&EmbyItemState{}).Where("item_id = ?", owner.Item.ItemId).Update("deleted", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := ValidateEmbyDeletionSource(t.Context(), plan.Input, owner, source); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("preflight accepted released deletion barrier: %v", err)
	}
}

func TestEmbyExternalDeletionSQLiteResume(t *testing.T) {
	testEmbyExternalDeletionResume(t, setupCleanupSQLite)
}

func testEmbyExternalDeletionResume(t *testing.T, setup cleanupPersistenceDatabase) {
	conn, reopen := setup(t)
	record, plan, provider := seedEmbyExternalDeletion(t, conn, false)
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	provider.fail[deletionMatrixKey(plan.Targets[1].File.AccountID, plan.Targets[1].File.FileID)] = true
	provider.responseErr = errors.New("response interrupted")
	var attempt string
	results := ExecuteEmbyDeletionBatch(t.Context(), plan, plan.Targets, provider, func(_ context.Context, _ EmbyDeletionInput, merged EmbyDeletionTarget) error {
		if len(merged.SourceBindings) != 2 {
			t.Fatal("batch lost second external reference")
		}
		return nil
	}, func(targets []EmbyDeletionTarget) error {
		var err error
		attempt, err = BeginEmbyWebhookAttempt(t.Context(), record, targets)
		return err
	})
	if len(results) != 2 || results[0].Outcome != EmbyDeletionDeleted || results[1].Outcome != EmbyDeletionFailed {
		t.Fatalf("partial result=%+v", results)
	}
	if err := SaveEmbyWebhookBatchResults(t.Context(), record, attempt, results); err != nil {
		t.Fatal(err)
	}
	reopen()
	if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered := webhookModelClaim(t)
	var restored EmbyDeletionPlan
	if err := json.Unmarshal([]byte(recovered.PlanJSON), &restored); err != nil {
		t.Fatal(err)
	}
	if recovered.InputJSON != record.InputJSON || recovered.PlanJSON != embyJSON(plan) || embyJSON(restored.Supplements) != embyJSON(plan.Supplements) {
		t.Fatal("resume changed input, original objects or read time")
	}
	if _, err := BeginEmbyWebhookAttempt(t.Context(), record, restored.Targets[1:]); err == nil {
		t.Fatal("old claim reused")
	}
	if _, err := BeginEmbyWebhookAttempt(t.Context(), recovered, restored.Targets); err == nil {
		t.Fatal("completed member replayed")
	}
	clear(provider.fail)
	provider.responseErr = nil
	retry := ExecuteEmbyDeletionBatch(t.Context(), restored, restored.Targets[1:], provider, allowEmbyDeleteTest, func(targets []EmbyDeletionTarget) error {
		var err error
		attempt, err = BeginEmbyWebhookAttempt(t.Context(), recovered, targets)
		return err
	})
	if len(retry) != 1 || retry[0].Outcome != EmbyDeletionDeleted || len(provider.batches) != 2 || len(provider.batches[1]) != 1 || provider.batches[1][0] != restored.Targets[1].File.FileID {
		t.Fatalf("retry=%+v batches=%v", retry, provider.batches)
	}
	if err := SaveEmbyWebhookBatchResults(t.Context(), recovered, attempt, retry); err != nil {
		t.Fatal(err)
	}
	stored, err := LoadEmbyWebhookTargets(t.Context(), recovered.ID)
	if err != nil || len(stored) != 2 || stored[0].Attempts != 1 || stored[1].Attempts != 2 {
		t.Fatalf("attempts=%+v err=%v", stored, err)
	}
	results[1] = retry[0]
	if err := FinalizeEmbyDeletionPlan(t.Context(), restored, results); err != nil {
		t.Fatal(err)
	}
	assertWebhookCount(t, &EmbyMediaItem{}, 0)
	for _, owner := range restored.Input.Owners {
		var evidence EmbyItemEvidence
		if err := db.Db.First(&evidence, owner.Evidence.ID).Error; err != nil || evidence != owner.Evidence {
			t.Fatal("immutable evidence changed", err)
		}
	}
}
