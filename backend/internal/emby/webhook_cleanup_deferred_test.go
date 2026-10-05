package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/models"
)

type cleanupDeferredBudgetProvider struct {
	*cleanupWorkerProvider
	maxBytes int
}

func (p *cleanupDeferredBudgetProvider) BatchLimits() (int, int) { return p.maxFiles, p.maxBytes }

func (p *cleanupDeferredBudgetProvider) DeleteBatch(ctx context.Context, files []models.EmbyFrozenFile, guard func() error) (bool, error) {
	size, err := models.EmbyDeleteBatchPayloadSize(files)
	if err != nil || len(files) > p.maxFiles || size > p.maxBytes {
		p.t.Fatalf("发送超出批次预算: files=%d bytes=%d limit=%d err=%v", len(files), size, p.maxBytes, err)
	}
	return p.cleanupWorkerProvider.DeleteBatch(ctx, files, guard)
}

type cleanupDeferredFixture struct {
	*cleanupWorkerFixture
	independent []models.SyncFile
	dependent   []models.SyncFile
	sidecars    []models.SyncFile
}

func setupCleanupDeferredTest(t *testing.T, count int) *cleanupDeferredFixture {
	t.Helper()
	f := &cleanupDeferredFixture{cleanupWorkerFixture: setupCleanupWorkerTest(t, "Season", false)}
	if err := db.Db.Where("file_id IN ?", []string{"nfo", "image"}).Delete(&models.SyncFile{}).Error; err != nil {
		t.Fatal(err)
	}
	clear(f.cloud.files)
	addRemote := func(file models.SyncFile) {
		f.cloud.files[file.FileId] = models.EmbyRemoteFile{FileID: file.FileId, ParentID: file.ParentId, FileName: file.FileName, Path: file.Path, PickCode: file.PickCode, SHA1: file.Sha1, FileSize: file.FileSize, MTime: file.MTime}
	}
	var snapshots []models.EmbyItemSnapshot
	for i := range 2 * count {
		file := f.file
		length := 10
		if i >= count {
			length = 18
		}
		file.FileId = strings.Repeat(fmt.Sprint(i+1), length)
		if i == 0 {
			if err := db.Db.Save(&file).Error; err != nil {
				t.Fatal(err)
			}
			f.file = file
		} else {
			file.BaseModel = models.BaseModel{}
			file.FileName, file.PickCode, file.Sha1 = fmt.Sprintf("episode-%d.mkv", i), fmt.Sprintf("p%d", i+1), fmt.Sprintf("sha-%d", i)
			file.LocalFilePath = filepath.Join(filepath.Dir(f.file.LocalFilePath), fmt.Sprintf("episode-%d.strm", i))
			if err := db.Db.Create(&file).Error; err != nil {
				t.Fatal(err)
			}
		}
		addRemote(file)
		if i < count {
			f.independent = append(f.independent, file)
		} else {
			f.dependent = append(f.dependent, file)
			metadata := file
			metadata.BaseModel = models.BaseModel{}
			metadata.FileId, metadata.FileName, metadata.PickCode = strings.Repeat(fmt.Sprint(i+4), 5), fmt.Sprintf("episode-%d.nfo", i), ""
			metadata.Sha1, metadata.FileSize, metadata.IsVideo, metadata.IsMeta = "nfo-"+file.Sha1, 10, false, true
			metadata.LocalFilePath = filepath.Join(filepath.Dir(file.LocalFilePath), metadata.FileName)
			if err := db.Db.Create(&metadata).Error; err != nil {
				t.Fatal(err)
			}
			addRemote(metadata)
			f.sidecars = append(f.sidecars, metadata)
		}
		id := fmt.Sprint(101 + i)
		snapshots = append(snapshots, models.EmbyItemSnapshot{
			Item:            models.EmbyMediaItem{ItemId: id, Type: "Episode", Path: file.LocalFilePath, PickCode: file.PickCode, SeriesId: "300", SeasonId: "301"},
			Sources:         []models.EmbySnapshotSource{{ID: "source-" + id, ItemID: id, Path: "http://qms/stream?pickcode=" + file.PickCode, PickCode: file.PickCode}},
			MembersComplete: true,
		})
	}
	token, err := models.BeginEmbyIndexRead("server-a", &f.config)
	if err != nil {
		t.Fatal(err)
	}
	if err := models.ApplyEmbySnapshots(token, snapshots); err != nil {
		t.Fatal(err)
	}
	// 42 字节容纳一个长 ID，或一个短视频 ID 加一个附件 ID；三个附件必须重新分包。
	budget, err := models.EmbyDeleteBatchPayloadSize([]models.EmbyFrozenFile{{SourceType: f.file.SourceType, FileID: f.dependent[0].FileId, ParentID: f.file.ParentId}})
	if err != nil {
		t.Fatal(err)
	}
	provider := &cleanupDeferredBudgetProvider{cleanupWorkerProvider: f.cloud, maxBytes: budget}
	f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
	return f
}

func (f *cleanupDeferredFixture) verificationRetry(t *testing.T) models.EmbyWebhookRecord {
	t.Helper()
	record := f.receive(t)
	verify := f.worker.verify
	f.worker.verify = func(context.Context, models.EmbyDeletionInput, models.EmbyDeletionTarget) error {
		return errors.New("temporary Emby verification failure")
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	f.worker.verify = verify
	first := readWebhookTest(t, record.ID)
	rows := cleanupWorkerRows(t, record.ID)
	if first.Status != models.EmbyWebhookRetry || first.PlanJSON == "" || len(rows) != len(f.sidecars)*3 || len(f.cloud.batchCalls) != 0 || len(f.cloud.scalarCalls) != 0 {
		t.Fatalf("核验故障未建立未发送的完整重试计划: status=%s targets=%d batches=%v", first.Status, len(rows), f.cloud.batchCalls)
	}
	for id, row := range rows {
		if row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != 0 {
			t.Fatalf("核验失败被记为发送或完成: %s %+v", id, row)
		}
	}
	countWebhookTest(t, &models.EmbyMediaSyncFile{}, int64(len(f.independent)+len(f.dependent)))
	var plan models.EmbyDeletionPlan
	if err := json.Unmarshal([]byte(first.PlanJSON), &plan); err != nil {
		t.Fatal(err)
	}
	groups, err := models.GroupEmbyDeletionTargets(plan.Targets, f.worker.provider)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Issues) != 0 || len(groups) != len(f.sidecars)*2 {
		t.Fatalf("未形成完整的跨批计划: issues=%v groups=%v", plan.Issues, groups)
	}
	groupByKey := make(map[string]int, len(plan.Targets))
	for i, group := range groups {
		for _, target := range group {
			groupByKey[target.Key] = i
		}
	}
	earlyGroups := map[int]bool{}
	for _, target := range plan.Targets {
		if target.Kind != "sidecar" {
			continue
		}
		sidecarGroup := groupByKey[target.Key]
		if len(target.Owners) != 1 || len(groups[sidecarGroup]) != 2 {
			t.Fatalf("附件未回填独立视频的早期组: %+v", target)
		}
		earlyGroups[sidecarGroup] = true
		if !slices.ContainsFunc(plan.Targets, func(video models.EmbyDeletionTarget) bool {
			return video.Kind == "video" && slices.Contains(video.Owners, target.Owners[0]) && groupByKey[video.Key] > sidecarGroup
		}) {
			t.Fatalf("附件依赖视频未排在后续批次: %+v", target)
		}
	}
	if len(earlyGroups) != len(f.sidecars) {
		t.Fatalf("附件没有来自不同的早期批次: %v", earlyGroups)
	}
	return first
}

func TestWebhookCleanupDeferredRetryCompletes(t *testing.T) {
	for _, tt := range []struct {
		name  string
		count int
	}{{"one_sidecar", 1}, {"regroup_sidecars_within_byte_budget", 3}} {
		t.Run(tt.name, func(t *testing.T) {
			f := setupCleanupDeferredTest(t, tt.count)
			first := f.verificationRetry(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			final := readWebhookTest(t, first.ID)
			if final.Status != models.EmbyWebhookDone || final.NextAttemptAt != 0 || final.PlanJSON != first.PlanJSON {
				t.Fatalf("跨批附件重试未完成原冻结计划: status=%s reason=%s batches=%v", final.Status, final.Reason, f.cloud.batchCalls)
			}
			if len(f.cloud.files) != 0 || len(f.cloud.scalarCalls) != 0 {
				t.Fatalf("附件未补齐或退化为单文件接口: remaining=%v scalar=%v", f.cloud.files, f.cloud.scalarCalls)
			}
			for id, row := range cleanupWorkerRows(t, first.ID) {
				if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
					t.Fatalf("跨批恢复结果或实际发送次数错误: %s %+v", id, row)
				}
			}
			videoBatches := 2 * tt.count
			if len(f.cloud.batchCalls) != videoBatches+(tt.count+1)/2 {
				t.Fatalf("暂缓附件未汇总后按预算重新分组: %v", f.cloud.batchCalls)
			}
			var sentSidecars, wantSidecars []string
			for i, batch := range f.cloud.batchCalls {
				if i < videoBatches {
					if len(batch) != 1 {
						t.Fatalf("附件未暂缓至所有原视频批次之后: %v", f.cloud.batchCalls)
					}
					continue
				}
				sentSidecars = append(sentSidecars, batch...)
			}
			for _, file := range f.sidecars {
				wantSidecars = append(wantSidecars, file.FileId)
			}
			slices.Sort(sentSidecars)
			slices.Sort(wantSidecars)
			if !slices.Equal(sentSidecars, wantSidecars) || tt.count == 3 && len(f.cloud.batchCalls[videoBatches]) != 2 {
				t.Fatalf("暂缓附件逐项发送或遗漏: %v", f.cloud.batchCalls)
			}
			countWebhookTest(t, &models.EmbyMediaSyncFile{}, 0)
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			if next, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Add(24*time.Hour).Unix()); err != nil || next != nil {
				t.Fatalf("完成记录重启后重新进入执行: next=%+v err=%v", next, err)
			}
		})
	}
}

func TestWebhookCleanupDeferredPreservesBlockedDependency(t *testing.T) {
	for _, tt := range []struct {
		name         string
		videoOutcome models.EmbyDeletionOutcome
		videoReason  string
		attempts     int
	}{
		{"identity_conflict", models.EmbyDeletionUnresolved, "远端文件身份或内容已变化", 0},
		{"live_shared", models.EmbyDeletionUnresolved, "Emby 新条目仍使用原文件", 0},
		{"delete_failed", models.EmbyDeletionFailed, "deferred dependency delete failed", 1},
		{"temporary_verification", models.EmbyDeletionUnresolved, "temporary dependent video verification failure", 0},
		{"last_delete_attempt_failed", models.EmbyDeletionFailed, "deferred dependency delete failed", models.EmbyWebhookTargetMaxAttempts},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := setupCleanupDeferredTest(t, 1)
			first := f.verificationRetry(t)
			video, metadata := f.dependent[0], f.sidecars[0]
			verify := f.worker.verify
			claim := claimWebhookTest(t)
			if tt.name == "last_delete_attempt_failed" {
				var target models.EmbyDeletionTarget
				if err := json.Unmarshal([]byte(cleanupWorkerRows(t, first.ID)[video.FileId].TargetJSON), &target); err != nil {
					t.Fatal(err)
				}
				// 预置既有发送历史；上轮核验失败留下 unresolved，本轮仍有最后一次发送额度。
				for range models.EmbyWebhookTargetMaxAttempts - 1 {
					attempt, err := models.BeginEmbyWebhookAttempt(t.Context(), claim, []models.EmbyDeletionTarget{target})
					if err != nil {
						t.Fatal(err)
					}
					if err := models.SaveEmbyWebhookBatchResults(t.Context(), claim, attempt, []models.EmbyDeletionResult{{Key: target.Key, Outcome: models.EmbyDeletionUnresolved, Reason: "previous verification failure"}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			switch tt.name {
			case "identity_conflict":
				remote := f.cloud.files[video.FileId]
				remote.SHA1 = "replacement-generation"
				f.cloud.files[video.FileId] = remote
			case "live_shared":
				f.live = []embyclientrestgo.BaseItemDtoV2{{
					Id: "202", Type: "Episode", Path: video.LocalFilePath, SeriesId: "300", SeasonId: "301",
					MediaSources: []embyclientrestgo.MediaSource{{ID: "shared-source", ItemID: "202", Path: "http://qms/stream?pickcode=" + video.PickCode}},
				}}
			case "temporary_verification":
				f.worker.verify = func(ctx context.Context, input models.EmbyDeletionInput, target models.EmbyDeletionTarget) error {
					if target.File.FileID == video.FileId {
						return errors.New(tt.videoReason)
					}
					return verify(ctx, input, target)
				}
			case "delete_failed", "last_delete_attempt_failed":
				f.cloud.beforeBatch = func(_ context.Context, files []models.EmbyFrozenFile) (bool, error) {
					if slices.ContainsFunc(files, func(file models.EmbyFrozenFile) bool { return file.FileID == video.FileId }) {
						return false, errors.New(tt.videoReason)
					}
					for _, file := range files {
						delete(f.cloud.files, file.FileID)
					}
					return true, nil
				}
			}
			f.worker.process(t.Context(), claim)
			rows := cleanupWorkerRows(t, first.ID)
			if row := rows[video.FileId]; row.Outcome != tt.videoOutcome || row.Attempts != tt.attempts || !strings.Contains(row.Reason, tt.videoReason) {
				t.Fatalf("依赖视频未反映本轮失败原因: %+v", row)
			}
			if row := rows[metadata.FileId]; row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != 0 || !strings.Contains(row.Reason, "保留本轮暂缓的元数据") {
				t.Fatalf("受保护的暂缓附件被发送或完成: %+v", row)
			}
			for _, id := range []string{video.FileId, metadata.FileId} {
				if _, present := f.cloud.files[id]; !present {
					t.Fatalf("依赖未成功仍删除文件: %s", id)
				}
			}
			independent := f.independent[0].FileId
			if row := rows[independent]; row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
				t.Fatalf("保护分支阻止了独立视频: %+v", row)
			}
			if _, present := f.cloud.files[independent]; present {
				t.Fatal("独立视频仍在云端")
			}
			if len(f.cloud.batchCalls) != 1+min(tt.attempts, 1) || !slices.Equal(f.cloud.batchCalls[0], []string{independent}) || len(f.cloud.scalarCalls) != 0 {
				t.Fatalf("发送范围或次数错误: batches=%v scalar=%v", f.cloud.batchCalls, f.cloud.scalarCalls)
			}
			for _, batch := range f.cloud.batchCalls {
				if slices.Contains(batch, metadata.FileId) {
					t.Fatalf("依赖视频未成功仍发送附件: %v", batch)
				}
			}
			wantStatus := models.EmbyWebhookUnresolved
			if tt.videoOutcome == models.EmbyDeletionFailed || tt.name == "temporary_verification" {
				wantStatus = models.EmbyWebhookRetry
			}
			if got := readWebhookTest(t, first.ID); got.Status != wantStatus || got.PlanJSON != first.PlanJSON || got.InputJSON != first.InputJSON || (got.NextAttemptAt > 0) != (wantStatus == models.EmbyWebhookRetry) {
				t.Fatalf("保护分支的持久状态错误: status=%s reason=%s", got.Status, got.Reason)
			}
			var links int64
			if err := db.Db.Model(&models.EmbyMediaSyncFile{}).Where("emby_item_id = ?", "101").Count(&links).Error; err != nil || links != 0 {
				t.Fatalf("成功的独立 owner 未完成收尾: links=%d err=%v", links, err)
			}
			wantLinks := int64(1)
			if tt.name == "live_shared" {
				wantLinks = 0 // 旧 Emby ID 已消失，仅清关联，仍保留共享云端文件。
			}
			if err := db.Db.Model(&models.EmbyMediaSyncFile{}).Where("emby_item_id = ?", "102").Count(&links).Error; err != nil || links != wantLinks {
				t.Fatalf("未完成的依赖 owner 收尾错误: links=%d want=%d err=%v", links, wantLinks, err)
			}
			if wantStatus != models.EmbyWebhookRetry {
				if next, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Add(24*time.Hour).Unix()); err != nil || next != nil {
					t.Fatalf("终结的保护结果重新进入执行: next=%+v err=%v", next, err)
				}
				return
			}

			f.worker.verify, f.cloud.beforeBatch = verify, nil
			writes := len(f.cloud.batchCalls)
			f.worker.process(t.Context(), claimWebhookTest(t))
			final := readWebhookTest(t, first.ID)
			finalRows := cleanupWorkerRows(t, first.ID)
			if final.PlanJSON != first.PlanJSON || final.InputJSON != first.InputJSON || final.NextAttemptAt != 0 || finalRows[independent].TargetJSON != rows[independent].TargetJSON {
				t.Fatal("恢复改写冻结身份、已成功 attempt 或留下重试待办")
			}
			if tt.name == "last_delete_attempt_failed" {
				if final.Status != models.EmbyWebhookUnresolved || len(f.cloud.batchCalls) != writes || len(f.cloud.files) != 2 || len(f.cloud.scalarCalls) != 0 {
					t.Fatalf("耗尽后仍发送或错误完成: status=%s batches=%v files=%v", final.Status, f.cloud.batchCalls, f.cloud.files)
				}
				if row := finalRows[video.FileId]; row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != models.EmbyWebhookTargetMaxAttempts || row.TargetJSON != rows[video.FileId].TargetJSON || !strings.Contains(row.Reason, "原对象仍存在") {
					t.Fatalf("耗尽没有按原身份只读确认: %+v", row)
				}
				if row := finalRows[metadata.FileId]; row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != 0 {
					t.Fatalf("耗尽视频的附件被删除或完成: %+v", row)
				}
				countWebhookTest(t, &models.EmbyMediaSyncFile{}, 1)
				return
			}
			if final.Status != models.EmbyWebhookDone || len(f.cloud.files) != 0 || len(f.cloud.scalarCalls) != 0 || len(f.cloud.batchCalls) != writes+2 || !slices.Equal(f.cloud.batchCalls[writes], []string{video.FileId}) || !slices.Equal(f.cloud.batchCalls[writes+1], []string{metadata.FileId}) {
				t.Fatalf("临时故障恢复未仅补齐未完成项: status=%s batches=%v files=%v", final.Status, f.cloud.batchCalls, f.cloud.files)
			}
			for id, row := range finalRows {
				wantAttempts := 1
				if id == video.FileId {
					wantAttempts += tt.attempts
				}
				if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != wantAttempts {
					t.Fatalf("恢复结果或实际发送次数错误: %s %+v", id, row)
				}
			}
			countWebhookTest(t, &models.EmbyMediaSyncFile{}, 0)
		})
	}
}

func TestWebhookCleanupDeferredRecoversAfterVideosComplete(t *testing.T) {
	f := setupCleanupDeferredTest(t, 1)
	first := f.verificationRetry(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	provider := f.worker.provider
	interrupted := false
	f.worker.provider = func(file models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) {
		if file.FileID == f.sidecars[0].FileId && len(f.cloud.batchCalls) == 2 {
			// 附件重新分组前，两批视频结果必须已经落库；取消不发生在删除结果保存之前。
			rows := cleanupWorkerRows(t, first.ID)
			for _, video := range []models.SyncFile{f.independent[0], f.dependent[0]} {
				if row := rows[video.FileId]; row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
					t.Fatalf("中断点的视频结果尚未持久完成: %+v", row)
				}
			}
			interrupted = true
			cancel()
			return nil, ctx.Err()
		}
		return provider(file)
	}
	claim := claimWebhookTest(t)
	f.worker.process(ctx, claim)
	paused := readWebhookTest(t, first.ID)
	before := cleanupWorkerRows(t, first.ID)
	if !interrupted || paused.Status != models.EmbyWebhookRunning || paused.ClaimToken != claim.ClaimToken || paused.Attempts != first.Attempts || paused.PlanJSON != first.PlanJSON || paused.InputJSON != first.InputJSON {
		t.Fatalf("取消未保留原领取与冻结状态: interrupted=%v status=%s attempts=%d", interrupted, paused.Status, paused.Attempts)
	}
	if row := before[f.sidecars[0].FileId]; row.Outcome != models.EmbyDeletionUnresolved || row.Attempts != 0 || len(f.cloud.batchCalls) != 2 || len(f.cloud.files) != 1 {
		t.Fatalf("中断前附件已发送或视频未完成: row=%+v batches=%v files=%v", row, f.cloud.batchCalls, f.cloud.files)
	}
	countWebhookTest(t, &models.EmbyMediaSyncFile{}, 2)
	f.worker.provider = provider
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered := readWebhookTest(t, first.ID)
	if recovered.Status != models.EmbyWebhookRetry || recovered.ClaimToken != "" || recovered.PlanJSON != first.PlanJSON || recovered.Attempts != first.Attempts {
		t.Fatalf("启动恢复丢失持久状态: status=%s attempts=%d", recovered.Status, recovered.Attempts)
	}
	next := claimWebhookTest(t)
	if next.ClaimToken == claim.ClaimToken || !errors.Is(models.CheckEmbyWebhookClaim(t.Context(), claim), models.ErrEmbyWebhookClaimLost) {
		t.Fatal("重新领取未使旧 claim 失效")
	}
	f.worker.process(t.Context(), next)
	final := readWebhookTest(t, first.ID)
	if final.Status != models.EmbyWebhookDone || final.NextAttemptAt != 0 || final.Attempts != first.Attempts || final.PlanJSON != first.PlanJSON || final.InputJSON != first.InputJSON {
		t.Fatalf("恢复后未完成原计划: status=%s reason=%s attempts=%d", final.Status, final.Reason, final.Attempts)
	}
	if len(f.cloud.batchCalls) != 3 || !slices.Equal(f.cloud.batchCalls[2], []string{f.sidecars[0].FileId}) || len(f.cloud.scalarCalls) != 0 || len(f.cloud.files) != 0 {
		t.Fatalf("恢复重发成功视频或遗漏附件: batches=%v scalar=%v files=%v", f.cloud.batchCalls, f.cloud.scalarCalls, f.cloud.files)
	}
	for id, row := range cleanupWorkerRows(t, first.ID) {
		if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 || id != f.sidecars[0].FileId && row.TargetJSON != before[id].TargetJSON {
			t.Fatalf("恢复改写成功结果或发送次数错误: %s %+v", id, row)
		}
	}
	countWebhookTest(t, &models.EmbyMediaSyncFile{}, 0)
	if pending, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Add(24*time.Hour).Unix()); err != nil || pending != nil {
		t.Fatalf("恢复完成后仍有重复待办: pending=%+v err=%v", pending, err)
	}
}
