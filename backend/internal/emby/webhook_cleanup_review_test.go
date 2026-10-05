package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

type cleanupPreflightFailureProvider struct {
	*cleanupWorkerProvider
	failed bool
}

func (p *cleanupPreflightFailureProvider) List(ctx context.Context, file models.EmbyFrozenFile) ([]models.EmbyRemoteFile, error) {
	listing, err := p.cleanupWorkerProvider.List(ctx, file)
	// 计划已经冻结后，模拟列表暂时不包含图片，随后原 ID 查询失败。
	if p.failed && p.listCalls > 1 {
		listing = slices.DeleteFunc(listing, func(file models.EmbyRemoteFile) bool { return file.FileID == "image" })
	}
	return listing, err
}

func (p *cleanupPreflightFailureProvider) Stat(ctx context.Context, file models.EmbyFrozenFile) (models.EmbyRemoteFile, error) {
	if p.failed && file.FileID == "image" {
		return models.EmbyRemoteFile{}, errors.New("temporary preflight detail failure")
	}
	return p.cleanupWorkerProvider.Stat(ctx, file)
}

func TestWebhookCleanupPreflightFailurePreservesSuccessfulBatchMembers(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	provider := &cleanupPreflightFailureProvider{cleanupWorkerProvider: f.cloud, failed: true}
	f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	rows := cleanupWorkerRows(t, record.ID)
	if readWebhookTest(t, record.ID).Status != models.EmbyWebhookRetry || rows["image"].Outcome != models.EmbyDeletionFailed || rows["image"].Attempts != 0 {
		t.Fatalf("未发送的预检失败成员没有保留重试: %+v", rows)
	}
	for _, id := range []string{"f1", "nfo"} {
		if rows[id].Outcome != models.EmbyDeletionDeleted || rows[id].Attempts != 1 {
			t.Fatalf("预检失败阻止已发送成员结果落库: %s %+v", id, rows[id])
		}
	}
	if len(f.cloud.batchCalls) != 1 || len(f.cloud.batchCalls[0]) != 2 || slices.Contains(f.cloud.batchCalls[0], "image") {
		t.Fatalf("预检失败成员进入删除写入: %v", f.cloud.batchCalls)
	}
	provider.failed = false
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if len(f.cloud.batchCalls) != 2 || !slices.Equal(f.cloud.batchCalls[1], []string{"image"}) {
		t.Fatalf("恢复重放已完成成员: %v", f.cloud.batchCalls)
	}
}

func TestWebhookCleanupVerificationRetryKeepsJointBatch(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	record := f.receive(t)
	verify := f.worker.verify
	f.worker.verify = func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
		return errors.New("temporary Emby verification failure")
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	first := readWebhookTest(t, record.ID)
	if first.Status != models.EmbyWebhookRetry || len(f.cloud.batchCalls) != 0 {
		t.Fatalf("核验故障未保留未发送批次: status=%s reason=%s batches=%v", first.Status, first.Reason, f.cloud.batchCalls)
	}
	for id, row := range cleanupWorkerRows(t, record.ID) {
		if row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != 0 {
			t.Fatalf("核验故障后的目标状态错误: %s %+v", id, row)
		}
	}
	assertCleanupOriginalOwnerRetained(t)

	f.worker.verify = verify
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	if len(f.cloud.batchCalls) != 1 || len(f.cloud.scalarCalls) != 0 || len(f.cloud.files) != 0 {
		t.Fatalf("重试没有共同删除视频与旁车: batches=%v scalar=%v remaining=%v", f.cloud.batchCalls, f.cloud.scalarCalls, f.cloud.files)
	}
	ids := slices.Clone(f.cloud.batchCalls[0])
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"f1", "image", "nfo"}) {
		t.Fatalf("重试批次遗漏成员: %v", ids)
	}
	for id, row := range cleanupWorkerRows(t, record.ID) {
		if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
			t.Fatalf("重试结果或发送次数错误: %s %+v", id, row)
		}
	}
	if readWebhookTest(t, record.ID).PlanJSON != first.PlanJSON {
		t.Fatal("重试改写了冻结计划")
	}
	var remaining int64
	if err := db.Db.Model(&models.EmbyMediaSyncFile{}).Where("emby_item_id = ?", "101").Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("全部完成后原 owner 未收尾: remaining=%d err=%v", remaining, err)
	}
}

func TestWebhookCleanupRetryChecksOtherVideoTargetForSameOwner(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	other := f.file
	other.BaseModel = models.BaseModel{}
	other.FileId, other.ParentId, other.FileName, other.PickCode = "f2", "other-parent", "other.mkv", "p2"
	other.Path = path.Join(other.Path, "Other")
	// 同一物理条目的多个来源共享 Item.Path，但云端目标位于不同目录。
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	f.cloud.files[other.FileId] = models.EmbyRemoteFile{FileID: other.FileId, ParentID: other.ParentId, FileName: other.FileName, Path: other.Path, PickCode: other.PickCode, SHA1: other.Sha1, FileSize: other.FileSize, MTime: other.MTime}
	snapshot := workerSnapshot(f.file)
	snapshot.Sources = append(snapshot.Sources, models.EmbySnapshotSource{ID: "source-other", ItemID: "101", Path: "http://qms/stream?pickcode=p2", PickCode: "p2"})
	if err := models.ApplyEmbySnapshots(f.token, []models.EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	record := f.receive(t)
	verify := f.worker.verify
	f.worker.verify = func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
		return errors.New("temporary Emby verification failure")
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	first := readWebhookTest(t, record.ID)
	var plan models.EmbyDeletionPlan
	if err := json.Unmarshal([]byte(first.PlanJSON), &plan); err != nil {
		t.Fatal(err)
	}
	if first.Status != models.EmbyWebhookRetry || len(plan.Input.Owners) != 1 || len(plan.Input.Owners[0].Files) != 2 || len(plan.Targets) != 4 {
		t.Fatalf("未建立同 owner 多视频的冻结计划: status=%s owners=%d targets=%d", first.Status, len(plan.Input.Owners), len(plan.Targets))
	}
	for _, target := range plan.Targets {
		if target.Reason != "" || target.File.Reason != "" {
			t.Fatalf("冻结目标缺少有效身份: %s reason=%s fileReason=%s", target.File.FileID, target.Reason, target.File.Reason)
		}
	}
	for id, row := range cleanupWorkerRows(t, record.ID) {
		if row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != 0 {
			t.Fatalf("核验故障后的目标状态错误: %s %+v", id, row)
		}
	}
	f.worker.verify = verify
	remote := f.cloud.files["f2"]
	remote.SHA1 = "replacement-generation"
	f.cloud.files["f2"] = remote
	f.worker.process(t.Context(), claimWebhookTest(t))
	if got := readWebhookTest(t, record.ID); got.Status != models.EmbyWebhookUnresolved || len(f.cloud.batchCalls) != 1 || !slices.Equal(f.cloud.batchCalls[0], []string{"f1"}) {
		t.Fatalf("同批视频错误豁免了同 owner 的另一个目标: status=%s reason=%s batches=%v", got.Status, got.Reason, f.cloud.batchCalls)
	}
	rows := cleanupWorkerRows(t, record.ID)
	if rows["f1"].Outcome != models.EmbyDeletionDeleted || rows["f1"].Attempts != 1 {
		t.Fatalf("独立视频未完成: %+v", rows["f1"])
	}
	for _, id := range []string{"f2", "nfo", "image"} {
		if rows[id].Outcome != models.EmbyDeletionUnresolved || rows[id].Attempts != 0 {
			t.Fatalf("跨批保留目标取得完成结果或 attempt: %s %+v", id, rows[id])
		}
		if _, exists := f.cloud.files[id]; !exists {
			t.Fatalf("跨批保护失效: %s", id)
		}
	}
}

func TestWebhookCleanupReappearedSuccessfulVideoPreservesUnfinishedSidecars(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	first := f.receive(t)
	f.envelope.Event = "deep.delete"
	second := f.receive(t)
	original := f.cloud.files["f1"]
	f.cloud.beforeBatch = func(context.Context, []models.EmbyFrozenFile) (bool, error) {
		delete(f.cloud.files, "f1")
		return false, errors.New("partial provider failure")
	}
	// 首个收件仅视频成功，模拟重启前仍持有领取；下个收件必须复用这份成功证据。
	if err := f.worker.processDeletion(t.Context(), claimWebhookTest(t)); err == nil {
		t.Fatal("未建立附件待恢复的部分删除")
	}
	rows := cleanupWorkerRows(t, first.ID)
	if rows["f1"].Outcome != models.EmbyDeletionDeleted || rows["nfo"].Outcome != models.EmbyDeletionFailed || rows["image"].Outcome != models.EmbyDeletionFailed {
		t.Fatalf("部分删除结果错误: %+v", rows)
	}
	f.cloud.beforeBatch = nil
	f.cloud.files["f1"] = original
	f.worker.process(t.Context(), claimWebhookTest(t))
	if got := readWebhookTest(t, second.ID); got.Status != models.EmbyWebhookUnresolved || len(f.cloud.batchCalls) != 1 || len(f.cloud.files) != 3 {
		t.Fatalf("历史成功视频重现后附件被删除: status=%s reason=%s batches=%v files=%v", got.Status, got.Reason, f.cloud.batchCalls, f.cloud.files)
	}
	for id, row := range cleanupWorkerRows(t, second.ID) {
		if row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != 0 {
			t.Fatalf("重现视频或依赖附件获得新删除权限: %s %+v", id, row)
		}
	}
	assertCleanupOriginalOwnerRetained(t)
}

func TestWebhookCleanupPermanentPersistenceErrorsDoNotRetry(t *testing.T) {
	for _, cause := range []error{models.ErrEmbyWebhookClaimLost, models.ErrEmbyIdentityAmbiguous, models.ErrEmbySnapshotStale, models.ErrEmbyWebhookTooLarge} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			calls := 0
			err := persistWebhook(ctx, func() error {
				calls++
				return fmt.Errorf("持久化失败: %w", cause)
			})
			if !errors.Is(err, cause) || calls != 1 || ctx.Err() != nil {
				t.Fatalf("确定错误被当作可恢复存储故障: calls=%d err=%v ctx=%v", calls, err, ctx.Err())
			}
		})
	}
}

type cleanupChangedBeforeSendProvider struct {
	*cleanupWorkerProvider
	change func()
}

func (p cleanupChangedBeforeSendProvider) DeleteBatch(ctx context.Context, files []models.EmbyFrozenFile, guard func() error) (bool, error) {
	p.change()
	return p.cleanupWorkerProvider.DeleteBatch(ctx, files, guard)
}

func TestWebhookCleanupSendGuardRejectsChangedAuthority(t *testing.T) {
	for _, name := range []string{"disabled", "account_changed", "new_generation", "claim_lost"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			record := f.receive(t)
			provider := cleanupChangedBeforeSendProvider{cleanupWorkerProvider: f.cloud, change: func() {
				if f.fullScans.Load() != 1 {
					t.Fatal("fixture must change authority after a complete live scan")
				}
				var result *gorm.DB
				switch name {
				case "disabled":
					result = db.Db.Model(&models.EmbyConfig{}).Where("id = ?", f.config.ID).Update("enable_delete_netdisk", 0)
				case "account_changed":
					result = db.Db.Model(&models.Account{}).Where("id = ?", f.file.AccountId).Update("user_id", "different-account-owner")
				case "new_generation":
					result = db.Db.Model(&models.EmbyItemState{}).Where("server_id = ? AND item_id = ?", "server-a", "101").Update("generation", gorm.Expr("generation + 1"))
				case "claim_lost":
					result = db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", record.ID).Update("claim_token", "new-worker-claim")
				}
				if result.Error != nil || result.RowsAffected != 1 {
					t.Fatalf("authority change failed: %v rows=%d", result.Error, result.RowsAffected)
				}
			}}
			f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
			f.worker.process(t.Context(), claimWebhookTest(t))
			if len(f.cloud.batchCalls) != 0 || len(f.cloud.files) != 3 {
				t.Fatalf("changed authority still allowed a write: calls=%v files=%v", f.cloud.batchCalls, f.cloud.files)
			}
			for _, row := range cleanupWorkerRows(t, record.ID) {
				if row.Attempts != 0 || row.Outcome == models.EmbyDeletionDeleted || row.Outcome == models.EmbyDeletionAlreadyAbsent {
					t.Fatalf("unsent targets acquired an attempt or completion: %+v", row)
				}
			}
		})
	}
}

func TestWebhookCleanupMixedOwnersKeepConflictAndDeleteIndependentBatch(t *testing.T) {
	for _, name := range []string{"local_reassigned", "local_reassigned_split", "live_shared", "live_survivor", "video_exhausted"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Season", false)
			if name == "local_reassigned_split" {
				f.cloud.maxFiles = 1
			}
			other := f.file
			other.BaseModel = models.BaseModel{}
			other.FileId, other.FileName, other.PickCode, other.Sha1 = "f2", "other.mkv", "p2", "other-sha"
			other.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), "other.strm")
			if err := db.Db.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			f.cloud.files[other.FileId] = models.EmbyRemoteFile{FileID: other.FileId, ParentID: other.ParentId, FileName: other.FileName, Path: other.Path, PickCode: other.PickCode, SHA1: other.Sha1, FileSize: other.FileSize, MTime: other.MTime}
			metadata := other
			metadata.BaseModel = models.BaseModel{}
			metadata.FileId, metadata.FileName, metadata.PickCode = "other-nfo", "other.nfo", ""
			metadata.IsVideo, metadata.IsMeta = false, true
			metadata.LocalFilePath = filepath.Join(filepath.Dir(other.LocalFilePath), metadata.FileName)
			if err := db.Db.Create(&metadata).Error; err != nil {
				t.Fatal(err)
			}
			f.cloud.files[metadata.FileId] = models.EmbyRemoteFile{FileID: metadata.FileId, ParentID: metadata.ParentId, FileName: metadata.FileName, Path: metadata.Path, SHA1: metadata.Sha1, FileSize: metadata.FileSize, MTime: metadata.MTime}
			token, err := models.BeginEmbyIndexRead("server-a", &f.config)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "102", Type: "Episode", Path: other.LocalFilePath, PickCode: other.PickCode, SeriesId: "300", SeasonId: "301"}, Sources: []models.EmbySnapshotSource{{ID: "source-102", ItemID: "102", Path: "http://qms/stream?pickcode=p2", PickCode: "p2"}}, MembersComplete: true}
			if err := models.ApplyEmbySnapshots(token, []models.EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			record := f.receive(t)
			verify := f.worker.verify
			f.worker.verify = func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
				return errors.New("temporary Emby verification failure")
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			if readWebhookTest(t, record.ID).Status != models.EmbyWebhookRetry || len(f.cloud.batchCalls) != 0 {
				t.Fatal("未建立核验失败后的重试状态")
			}
			f.worker.verify = verify
			if name == "video_exhausted" {
				video := cleanupWorkerRows(t, record.ID)["f1"]
				if err := db.Db.Model(&models.EmbyWebhookTarget{}).Where("id = ?", video.ID).Update("attempts", models.EmbyWebhookTargetMaxAttempts).Error; err != nil {
					t.Fatal(err)
				}
			} else if name == "local_reassigned" || name == "local_reassigned_split" {
				if err := db.Db.Model(&models.SyncFile{}).Where("id = ?", f.file.ID).Update("sha1", "replacement-generation").Error; err != nil {
					t.Fatal(err)
				}
			} else {
				id := "202"
				if name == "live_survivor" {
					id = "101"
				}
				f.live = []embyclientrestgo.BaseItemDtoV2{{Id: id, Type: "Episode", Path: f.file.LocalFilePath, SeriesId: "300", SeasonId: "301", MediaSources: []embyclientrestgo.MediaSource{{ID: "source-101", ItemID: id, Path: "http://qms/stream?pickcode=p1"}}}}
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			wantWrites := 1
			if name == "local_reassigned_split" {
				wantWrites = 2
			}
			if len(f.cloud.batchCalls) != wantWrites {
				t.Fatalf("independent owner did not finish: batches=%v record=%+v", f.cloud.batchCalls, readWebhookTest(t, record.ID))
			}
			var ids []string
			for _, batch := range f.cloud.batchCalls {
				ids = append(ids, batch...)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, []string{"f2", "other-nfo"}) || f.fullScans.Load() != 1 {
				t.Fatalf("conflict subset changed authority or repeated complete scan: batch=%v scans=%d", ids, f.fullScans.Load())
			}
			rows := cleanupWorkerRows(t, record.ID)
			for _, id := range []string{"f1", "nfo", "image"} {
				wantAttempts := 0
				if name == "video_exhausted" && id == "f1" {
					wantAttempts = models.EmbyWebhookTargetMaxAttempts
				}
				if rows[id].Outcome != models.EmbyDeletionUnresolved || rows[id].Attempts != wantAttempts {
					t.Fatalf("retained owner or dependent metadata acquired attempt/completion: %s %+v", id, rows[id])
				}
				if _, exists := f.cloud.files[id]; !exists {
					t.Fatalf("retained file removed: %s", id)
				}
			}
			for _, id := range ids {
				if rows[id].Outcome != models.EmbyDeletionDeleted || rows[id].Attempts != 1 {
					t.Fatalf("independent member result lost: %s %+v", id, rows[id])
				}
			}
		})
	}
}

type cleanupMissingParentProvider struct{ *cleanupWorkerProvider }

func (p cleanupMissingParentProvider) List(ctx context.Context, file models.EmbyFrozenFile) ([]models.EmbyRemoteFile, error) {
	if p.directory == nil {
		p.listCalls++
		return nil, errors.New("original parent directory no longer exists")
	}
	return p.cleanupWorkerProvider.List(ctx, file)
}

func TestWebhookCleanupInitialMissingParentConfirmsKnownOriginalIDs(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	provider := cleanupMissingParentProvider{f.cloud}
	f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
	clear(f.cloud.files)
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	rows := cleanupWorkerRows(t, record.ID)
	for _, id := range []string{"f1", "nfo", "image"} {
		if rows[id].Outcome != models.EmbyDeletionAlreadyAbsent || rows[id].Attempts != 0 || !slices.Contains(f.cloud.statCalls, id) {
			t.Fatalf("首次父目录缺失没有按原 ID 确认目标: %s %+v stats=%v", id, rows[id], f.cloud.statCalls)
		}
	}
	if len(f.cloud.batchCalls) != 0 || len(f.cloud.scalarCalls) != 0 || f.cloud.directoryCalls != 0 || f.fullScans.Load() != 1 {
		t.Fatalf("确认缺失路径发送删除或跳过 Emby 核验: batch=%v scalar=%v dirs=%d scans=%d", f.cloud.batchCalls, f.cloud.scalarCalls, f.cloud.directoryCalls, f.fullScans.Load())
	}
	var remaining int64
	if err := db.Db.Model(&models.EmbyMediaSyncFile{}).Where("emby_item_id = ?", "101").Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("原 owner 未完成本地收尾: remaining=%d err=%v", remaining, err)
	}
}

type cleanupInitialInventoryProvider struct {
	*cleanupWorkerProvider
	unavailable bool
	directory   string
	statErrorID string
}

func (p *cleanupInitialInventoryProvider) List(ctx context.Context, file models.EmbyFrozenFile) ([]models.EmbyRemoteFile, error) {
	if p.unavailable && (p.directory == "" || p.directory == file.Path) {
		p.listCalls++
		return nil, errors.New("original directory inventory unavailable")
	}
	return p.cleanupWorkerProvider.List(ctx, file)
}

func (p *cleanupInitialInventoryProvider) Stat(ctx context.Context, file models.EmbyFrozenFile) (models.EmbyRemoteFile, error) {
	if file.FileID == p.statErrorID {
		p.statCalls = append(p.statCalls, file.FileID)
		return models.EmbyRemoteFile{}, errors.New("original ID query failed")
	}
	return p.cleanupWorkerProvider.Stat(ctx, file)
}

func TestWebhookCleanupInitialMissingInventoryRetriesWithoutFreezingPartialPlan(t *testing.T) {
	for _, name := range []string{"temporary_list_error", "temporary_original_id_error"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			provider := &cleanupInitialInventoryProvider{cleanupWorkerProvider: f.cloud, unavailable: true}
			if name == "temporary_original_id_error" {
				clear(f.cloud.files)
				provider.statErrorID = "image"
			}
			f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			got := readWebhookTest(t, record.ID)
			if got.Status != models.EmbyWebhookRetry || got.PlanJSON != "" || len(cleanupWorkerRows(t, record.ID)) != 0 || len(f.cloud.statCalls) == 0 {
				t.Fatalf("首次读取失败冻结残缺计划或没有核验原 ID: record=%+v stats=%v", got, f.cloud.statCalls)
			}
			assertCleanupOriginalOwnerRetained(t)
			if len(f.cloud.batchCalls) != 0 || len(f.cloud.scalarCalls) != 0 || f.cloud.directoryCalls != 0 {
				t.Fatal("清单不足时发送了删除")
			}
			provider.unavailable, provider.statErrorID = false, ""
			if name == "temporary_original_id_error" {
				provider.unavailable = true
			}
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			rows := cleanupWorkerRows(t, record.ID)
			for _, id := range []string{"f1", "nfo", "image"} {
				want := models.EmbyDeletionDeleted
				if name == "temporary_original_id_error" {
					want = models.EmbyDeletionAlreadyAbsent
				}
				if rows[id].Outcome != want {
					t.Fatalf("重试丢失已知旁车或视频: %s %+v", id, rows[id])
				}
			}
		})
	}
}

func TestWebhookCleanupInitialMissingParentDoesNotFollowMovedOriginal(t *testing.T) {
	for _, id := range []string{"f1", "image"} {
		t.Run(id, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			provider := cleanupMissingParentProvider{f.cloud}
			f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
			original := f.cloud.files[id]
			clear(f.cloud.files)
			original.Path, original.ParentID = "/moved", "other-parent"
			f.cloud.files[id] = original
			replacement := original
			replacement.FileID, replacement.Path, replacement.ParentID = "replacement", f.file.Path, f.file.ParentId
			f.cloud.files[replacement.FileID] = replacement
			record := f.receive(t)
			for range webhookMaxAttempts {
				if err := db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", record.ID).Update("next_attempt_at", 0).Error; err != nil {
					t.Fatal(err)
				}
				f.worker.process(t.Context(), claimWebhookTest(t))
			}
			got := readWebhookTest(t, record.ID)
			if got.Status != models.EmbyWebhookUnresolved || got.PlanJSON != "" || !slices.Contains(f.cloud.statCalls, id) {
				t.Fatalf("移动的原 ID 被视为缺失: status=%s plan=%s stats=%v", got.Status, got.PlanJSON, f.cloud.statCalls)
			}
			if len(f.cloud.batchCalls) != 0 || len(f.cloud.scalarCalls) != 0 || f.cloud.directoryCalls != 0 || f.cloud.files[id] != original || f.cloud.files[replacement.FileID] != replacement {
				t.Fatal("追删移动对象或同路径替代对象")
			}
			assertCleanupOriginalOwnerRetained(t)
		})
	}
}

func TestWebhookCleanupInitialMissingParentRequiresOriginalOwnerChecks(t *testing.T) {
	for _, name := range []string{"original_emby_survives", "original_strm_survives", "server_changed", "server_query_failed", "generation_changed"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			if name == "server_changed" || name == "server_query_failed" {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if name == "server_query_failed" {
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
						return
					}
					fmt.Fprint(w, `{"Id":"different-server"}`)
				}))
				t.Cleanup(server.Close)
				f.config.EmbyUrl = server.URL
				if err := db.Db.Save(&f.config).Error; err != nil {
					t.Fatal(err)
				}
				token, err := models.BeginEmbyIndexRead("server-a", &f.config)
				if err != nil {
					t.Fatal(err)
				}
				if err := models.ApplyEmbySnapshots(token, []models.EmbyItemSnapshot{workerSnapshot(f.file)}); err != nil {
					t.Fatal(err)
				}
			}
			provider := cleanupMissingParentProvider{f.cloud}
			f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
			clear(f.cloud.files)
			record := f.receive(t)
			switch name {
			case "original_emby_survives":
				f.live = []embyclientrestgo.BaseItemDtoV2{{Id: "101", Type: "Movie", Path: f.file.LocalFilePath}}
			case "original_strm_survives":
				if err := os.WriteFile(f.file.LocalFilePath, []byte("http://qms/stream?pickcode=p1"), 0600); err != nil {
					t.Fatal(err)
				}
			case "generation_changed":
				if err := db.Db.Model(&models.EmbyItemState{}).Where("item_id = ?", "101").Update("generation", gorm.Expr("generation + 1")).Error; err != nil {
					t.Fatal(err)
				}
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			if got := readWebhookTest(t, record.ID); got.Status == models.EmbyWebhookDone || got.PlanJSON != "" || len(f.cloud.statCalls) != 0 {
				t.Fatalf("未通过原 owner 核验仍确认成功: record=%+v stats=%v", got, f.cloud.statCalls)
			}
			assertCleanupOriginalOwnerRetained(t)
		})
	}
}

func assertCleanupOriginalOwnerRetained(t *testing.T) {
	t.Helper()
	var remaining int64
	if err := db.Db.Model(&models.EmbyMediaSyncFile{}).Where("emby_item_id = ?", "101").Count(&remaining).Error; err != nil || remaining != 1 {
		t.Fatalf("未完成的原 owner 关联被清理: remaining=%d err=%v", remaining, err)
	}
}

func TestWebhookCleanupInitialMissingParentAllowsIndependentDirectory(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Season", false)
	provider := &cleanupInitialInventoryProvider{cleanupWorkerProvider: f.cloud, unavailable: true, directory: f.file.Path}
	f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
	clear(f.cloud.files)
	other := f.file
	other.BaseModel = models.BaseModel{}
	other.FileId, other.ParentId, other.FileName, other.PickCode, other.Sha1 = "f2", "other-parent", "other.mkv", "p2", "other-sha"
	other.Path = path.Join(other.Path, "Other")
	other.LocalFilePath = filepath.Join(filepath.Dir(other.LocalFilePath), "Other", "other.strm")
	if err := os.MkdirAll(filepath.Dir(other.LocalFilePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	f.cloud.files[other.FileId] = models.EmbyRemoteFile{FileID: other.FileId, ParentID: other.ParentId, FileName: other.FileName, Path: other.Path, PickCode: other.PickCode, SHA1: other.Sha1, FileSize: other.FileSize, MTime: other.MTime}
	metadata := other
	metadata.BaseModel = models.BaseModel{}
	metadata.FileId, metadata.FileName, metadata.PickCode = "other-nfo", "other.nfo", ""
	metadata.IsVideo, metadata.IsMeta = false, true
	metadata.LocalFilePath = filepath.Join(filepath.Dir(other.LocalFilePath), metadata.FileName)
	if err := db.Db.Create(&metadata).Error; err != nil {
		t.Fatal(err)
	}
	f.cloud.files[metadata.FileId] = models.EmbyRemoteFile{FileID: metadata.FileId, ParentID: metadata.ParentId, FileName: metadata.FileName, Path: metadata.Path, SHA1: metadata.Sha1, FileSize: metadata.FileSize, MTime: metadata.MTime}
	token, err := models.BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "102", Type: "Episode", Path: other.LocalFilePath, PickCode: other.PickCode, SeriesId: "300", SeasonId: "301"}, Sources: []models.EmbySnapshotSource{{ID: "source-102", ItemID: "102", Path: "http://qms/stream?pickcode=p2", PickCode: "p2"}}, MembersComplete: true}
	if err := models.ApplyEmbySnapshots(token, []models.EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	assertCleanupWorkerDone(t, record.ID)
	rows := cleanupWorkerRows(t, record.ID)
	for _, id := range []string{"f1", "nfo", "image"} {
		if rows[id].Outcome != models.EmbyDeletionAlreadyAbsent || rows[id].Attempts != 0 {
			t.Fatalf("缺失目录成员未独立确认: %s %+v", id, rows[id])
		}
	}
	if len(f.cloud.batchCalls) != 1 || !slices.Equal(f.cloud.batchCalls[0], []string{"f2", "other-nfo"}) || f.fullScans.Load() != 1 {
		t.Fatalf("完整独立目录未继续执行: batches=%v scans=%d", f.cloud.batchCalls, f.fullScans.Load())
	}
	for _, id := range []string{"f2", "other-nfo"} {
		if rows[id].Outcome != models.EmbyDeletionDeleted || rows[id].Attempts != 1 {
			t.Fatalf("独立目录结果未保存: %s %+v", id, rows[id])
		}
	}
}

func TestWebhookCleanupMissingParentStillConfirmsKnownOriginalIDs(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprintf("moved_%t", moved), func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Series", true)
			provider := cleanupMissingParentProvider{f.cloud}
			f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			original := f.cloud.files["f1"]
			f.cloud.afterDirectory = func() error {
				if moved {
					original.Path, original.ParentID = "/moved", "other-parent"
					f.cloud.files["f1"] = original
				}
				cancel()
				return context.Canceled
			}
			record := f.receive(t)
			f.worker.process(ctx, claimWebhookTest(t))
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			rows := cleanupWorkerRows(t, record.ID)
			if rows["media-root"].Outcome != models.EmbyDeletionAlreadyAbsent || rows["nfo"].Outcome != models.EmbyDeletionAlreadyAbsent || rows["image"].Outcome != models.EmbyDeletionAlreadyAbsent {
				t.Fatalf("missing parent listing prevented original ID confirmations: %+v", rows)
			}
			if moved {
				if rows["f1"].Outcome != models.EmbyDeletionUnresolved || f.cloud.files["f1"].ParentID != "other-parent" {
					t.Fatalf("moved original object was followed or falsely completed: %+v", rows["f1"])
				}
			} else {
				assertCleanupWorkerDone(t, record.ID)
				if rows["f1"].Outcome != models.EmbyDeletionAlreadyAbsent {
					t.Fatal("original absent video was not independently confirmed")
				}
			}
			if f.cloud.directoryCalls != 1 || len(f.cloud.batchCalls) != 0 || f.cloud.listCalls != 2 || len(f.cloud.statCalls) != 3 {
				t.Fatalf("recovery replayed a write or skipped original ID checks: dirs=%d batch=%v list=%d stat=%v", f.cloud.directoryCalls, f.cloud.batchCalls, f.cloud.listCalls, f.cloud.statCalls)
			}
		})
	}
}
