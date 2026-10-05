package models

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

type embyDeleteTestProvider struct {
	files        map[string]EmbyRemoteFile
	beforeDelete func()
	statErr      error
	deleteErr    error
	deleteFalse  bool
	calls        int
}

func (p *embyDeleteTestProvider) Stat(_ context.Context, file EmbyFrozenFile) (EmbyRemoteFile, error) {
	if p.statErr != nil {
		return EmbyRemoteFile{}, p.statErr
	}
	remote, ok := p.files[file.FileID]
	if !ok {
		return remote, ErrEmbyRemoteFileAbsent
	}
	return remote, nil
}

func (p *embyDeleteTestProvider) List(_ context.Context, file EmbyFrozenFile) ([]EmbyRemoteFile, error) {
	var list []EmbyRemoteFile
	for _, remote := range p.files {
		if remote.Path == file.Path {
			list = append(list, remote)
		}
	}
	return list, nil
}

func (p *embyDeleteTestProvider) Delete(_ context.Context, file EmbyFrozenFile, guard func() error) (bool, error) {
	if p.beforeDelete != nil {
		p.beforeDelete()
	}
	if err := guard(); err != nil {
		return false, err
	}
	p.calls++
	if p.deleteErr != nil {
		return false, p.deleteErr
	}
	if p.deleteFalse {
		return false, nil
	}
	delete(p.files, file.FileID)
	return true, nil
}

func setupEmbyDeleteTest(t *testing.T) (EmbyDeletionPlan, *embyDeleteTestProvider) {
	t.Helper()
	previous := db.Db
	t.Cleanup(func() { db.Db = previous })
	config, token, file := setupEmbySnapshotModelTest(t)
	if err := db.Db.Model(config).Update("enable_delete_netdisk", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	var input EmbyDeletionInput
	err := db.Db.Transaction(func(tx *gorm.DB) error {
		var err error
		input, err = CaptureEmbyDeletionTx(tx, token.ServerID, "101", "Movie")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
		t.Fatalf("capture: %+v", input)
	}
	provider := &embyDeleteTestProvider{files: map[string]EmbyRemoteFile{file.FileId: embyRemoteFromFrozen(input.Owners[0].Files[0])}}
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil || len(plan.Targets) != 1 || len(plan.Issues) != 0 {
		t.Fatalf("plan: %+v, %v", plan, err)
	}
	return plan, provider
}

func allowEmbyDeleteTest(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error { return nil }

// 单成员回归仍通过共同批次执行器，复用相同的范围、身份和结果确认边界。
func executeEmbyDeletionTestTarget(ctx context.Context, plan EmbyDeletionPlan, target EmbyDeletionTarget, provider EmbyDeleteProvider, verify EmbyDeletionVerifier) EmbyDeletionResult {
	return ExecuteEmbyDeletionBatch(ctx, plan, []EmbyDeletionTarget{target}, provider, verify, func([]EmbyDeletionTarget) error { return nil })[0]
}

func TestEmbyDeletionAuthorizationAndResultBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(EmbyDeletionPlan, *embyDeleteTestProvider) EmbyDeletionPlan
		verify EmbyDeletionVerifier
		want   EmbyDeletionOutcome
		calls  int
	}{
		{name: "success", verify: allowEmbyDeleteTest, want: EmbyDeletionDeleted, calls: 1},
		{name: "nil verifier", want: EmbyDeletionUnresolved},
		{name: "surviving original", verify: func(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error { return ErrEmbyDeleteUnverified }, want: EmbyDeletionUnresolved},
		{name: "disabled when received", verify: allowEmbyDeleteTest, change: func(p EmbyDeletionPlan, _ *embyDeleteTestProvider) EmbyDeletionPlan {
			p.Input.Authorized = false
			return p
		}, want: EmbyDeletionUnresolved},
		{name: "disabled during provider read", verify: allowEmbyDeleteTest, change: func(p EmbyDeletionPlan, r *embyDeleteTestProvider) EmbyDeletionPlan {
			r.beforeDelete = func() { db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("enable_delete_netdisk", 0) }
			return p
		}, want: EmbyDeletionFailed},
		{name: "provider false", verify: allowEmbyDeleteTest, change: func(p EmbyDeletionPlan, r *embyDeleteTestProvider) EmbyDeletionPlan { r.deleteFalse = true; return p }, want: EmbyDeletionFailed, calls: 1},
		{name: "unknown request result", verify: allowEmbyDeleteTest, change: func(p EmbyDeletionPlan, r *embyDeleteTestProvider) EmbyDeletionPlan {
			r.deleteErr = context.DeadlineExceeded
			return p
		}, want: EmbyDeletionFailed, calls: 1},
		{name: "stat error is not absence", verify: allowEmbyDeleteTest, change: func(p EmbyDeletionPlan, r *embyDeleteTestProvider) EmbyDeletionPlan {
			clear(r.files)
			r.statErr = errors.New("network error")
			return p
		}, want: EmbyDeletionFailed},
		{name: "confirmed absence", verify: allowEmbyDeleteTest, change: func(p EmbyDeletionPlan, r *embyDeleteTestProvider) EmbyDeletionPlan { clear(r.files); return p }, want: EmbyDeletionAlreadyAbsent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, provider := setupEmbyDeleteTest(t)
			if tc.change != nil {
				plan = tc.change(plan, provider)
			}
			result := executeEmbyDeletionTestTarget(t.Context(), plan, plan.Targets[0], provider, tc.verify)
			if result.Outcome != tc.want || provider.calls != tc.calls {
				t.Fatalf("result=%+v calls=%d", result, provider.calls)
			}
			if err := FinalizeEmbyDeletionPlan(t.Context(), plan, []EmbyDeletionResult{result}); err != nil {
				t.Fatal(err)
			}
			var count int64
			db.Db.Model(&EmbyMediaSyncFile{}).Count(&count)
			want := int64(1)
			if tc.want == EmbyDeletionDeleted || tc.want == EmbyDeletionAlreadyAbsent {
				want = 0
			}
			if count != want {
				t.Fatalf("unfinished association count=%d want=%d", count, want)
			}
			db.Db.Model(&EmbyItemEvidence{}).Count(&count)
			if count != 1 {
				t.Fatalf("immutable evidence lost: %d", count)
			}
		})
	}
}

func TestEmbyDeletionRejectsReassignedOrReplacedIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, EmbyDeletionPlan)
	}{
		{name: "file ID reused", change: func(t *testing.T, p EmbyDeletionPlan) {
			db.Db.Model(&SyncFile{}).Where("id = ?", p.Targets[0].File.SyncFileID).Update("sha1", "new-sha")
		}},
		{name: "ledger row generation", change: func(t *testing.T, p EmbyDeletionPlan) {
			db.Db.Model(&SyncFile{}).Where("id = ?", p.Targets[0].File.SyncFileID).Update("created_at", gorm.Expr("created_at + 1"))
		}},
		{name: "account reassigned", change: func(t *testing.T, p EmbyDeletionPlan) {
			db.Db.Model(&Account{}).Where("id = ?", p.Targets[0].File.AccountID).Update("user_id", "new-owner")
		}},
		{name: "remote root reassigned", change: func(t *testing.T, p EmbyDeletionPlan) {
			db.Db.Model(&SyncPath{}).Where("id = ?", p.Targets[0].File.SyncPathID).Update("remote_path", "/new-root")
		}},
		{name: "connection changed", change: func(t *testing.T, p EmbyDeletionPlan) {
			db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("emby_url", "http://other.invalid")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, provider := setupEmbyDeleteTest(t)
			tc.change(t, plan)
			result := executeEmbyDeletionTestTarget(t.Context(), plan, plan.Targets[0], provider, allowEmbyDeleteTest)
			if result.Outcome != EmbyDeletionUnresolved || provider.calls != 0 {
				t.Fatalf("%+v calls=%d", result, provider.calls)
			}
		})
	}
}

func TestEmbyDeletionCannotReplaceFrozenFilesInsideOriginalEvidence(t *testing.T) {
	plan, provider := setupEmbyDeleteTest(t)
	file := plan.Targets[0].File
	file.SHA1 = "new-object"
	if err := db.Db.Model(&SyncFile{}).Where("id = ?", file.SyncFileID).Update("sha1", file.SHA1).Error; err != nil {
		t.Fatal(err)
	}
	provider.files[file.FileID] = embyRemoteFromFrozen(file)
	plan.Input.Owners[0].Files[0] = file
	plan.Targets[0].File = file
	plan.Targets[0].Key = EmbyDeletionFileKey(file)
	result := executeEmbyDeletionTestTarget(t.Context(), plan, plan.Targets[0], provider, allowEmbyDeleteTest)
	if result.Outcome != EmbyDeletionUnresolved || provider.calls != 0 {
		t.Fatalf("tampered history was trusted: %+v", result)
	}
}

func TestEmbyDeletionRecoversEvidenceAfterCurrentIndexCleanup(t *testing.T) {
	plan, provider := setupEmbyDeleteTest(t)
	if err := db.Db.Where("item_id = ?", "101").Delete(&EmbyMediaItem{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Where("emby_item_id = ?", 101).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
		t.Fatal(err)
	}
	var captured EmbyDeletionInput
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		var err error
		captured, err = CaptureEmbyDeletionTx(tx, plan.Input.ServerID, "101", "Movie")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(captured.Owners) != 1 || len(captured.Owners[0].Files) != 1 || len(captured.Owners[0].Links) != 0 {
		t.Fatalf("historical capture failed: %+v", captured)
	}
	rebuilt, err := BuildEmbyDeletionPlan(t.Context(), captured, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	result := executeEmbyDeletionTestTarget(t.Context(), rebuilt, rebuilt.Targets[0], provider, allowEmbyDeleteTest)
	if result.Outcome != EmbyDeletionDeleted {
		t.Fatalf("historical file cannot be processed: %+v", result)
	}
}

func TestEmbyDeletionFinalizeRollsBackAndPreservesNewSnapshot(t *testing.T) {
	plan, provider := setupEmbyDeleteTest(t)
	result := executeEmbyDeletionTestTarget(t.Context(), plan, plan.Targets[0], provider, allowEmbyDeleteTest)
	if result.Outcome != EmbyDeletionDeleted {
		t.Fatalf("delete: %+v", result)
	}
	if err := db.Db.Callback().Delete().Before("gorm:delete").Register("test:emby_finalize_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_media_items" {
			tx.AddError(errors.New("injected failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := FinalizeEmbyDeletionPlan(t.Context(), plan, []EmbyDeletionResult{result}); err == nil {
		t.Fatal("expected finalize failure")
	}
	db.Db.Callback().Delete().Remove("test:emby_finalize_failure")
	var count int64
	db.Db.Model(&EmbyMediaSyncFile{}).Count(&count)
	if count != 1 {
		t.Fatal("transaction failure erased retry association")
	}
	if err := db.Db.Model(&EmbyItemState{}).Where("item_id = ?", "101").Update("snapshot_id", plan.Input.Owners[0].Evidence.ID+100).Error; err != nil {
		t.Fatal(err)
	}
	if err := FinalizeEmbyDeletionPlan(t.Context(), plan, []EmbyDeletionResult{result}); err != nil {
		t.Fatal(err)
	}
	db.Db.Model(&EmbyMediaSyncFile{}).Count(&count)
	if count != 1 {
		t.Fatal("old result erased newer item state")
	}
}

func TestEmbyLibraryCleanupUsesRemoteIDs(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "unselected", true: "all"}[all], func(t *testing.T) {
			previous := db.Db
			t.Cleanup(func() { db.Db = previous })
			setupEmbyMediaTestDB(t)
			if err := db.Db.AutoMigrate(&EmbyLibrary{}, &EmbyLibrarySyncPath{}); err != nil {
				t.Fatal(err)
			}
			items := []EmbyMediaItem{{BaseModel: BaseModel{ID: 9}, ItemId: "101", ItemIdInt: 101, LibraryId: "a"}, {BaseModel: BaseModel{ID: 101}, ItemId: "9", ItemIdInt: 9, LibraryId: "b"}}
			for _, value := range []any{&items, &[]EmbyLibrary{{LibraryId: "a"}, {LibraryId: "b"}}, &[]EmbyMediaSyncFile{{EmbyItemId: 101, SyncFileId: 1}, {EmbyItemId: 9, SyncFileId: 2}}} {
				if err := db.Db.Create(value).Error; err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if all {
				err = CleanupAllEmbyLibraryData()
			} else {
				err = CleanupUnselectedEmbyLibraryData([]string{"b"})
			}
			if err != nil {
				t.Fatal(err)
			}
			var links []EmbyMediaSyncFile
			db.Db.Find(&links)
			if all && len(links) != 0 || !all && (len(links) != 1 || links[0].EmbyItemId != 9) {
				t.Fatalf("wrong remote links: %+v", links)
			}
		})
	}
}
