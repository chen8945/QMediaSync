package emby

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

type exhaustedCleanupProvider struct {
	*cleanupWorkerProvider
	directoryFailures int
	skipFileWrites    bool
	readErr           error
	afterRead         func()
}

func (p *exhaustedCleanupProvider) DeleteBatch(ctx context.Context, files []models.EmbyFrozenFile, guard func() error) (bool, error) {
	if p.skipFileWrites {
		return false, errors.New("file batch unavailable before send")
	}
	return p.cleanupWorkerProvider.DeleteBatch(ctx, files, guard)
}

func (p *exhaustedCleanupProvider) Stat(ctx context.Context, file models.EmbyFrozenFile) (models.EmbyRemoteFile, error) {
	remote, err := p.cleanupWorkerProvider.Stat(ctx, file)
	if p.afterRead != nil {
		p.afterRead()
	}
	if p.readErr != nil {
		return models.EmbyRemoteFile{}, p.readErr
	}
	return remote, err
}

func (p *exhaustedCleanupProvider) StatDirectory(ctx context.Context, scope models.EmbyDirectoryScope) (models.EmbyRemoteFile, error) {
	remote, err := p.cleanupWorkerProvider.StatDirectory(ctx, scope)
	if p.afterRead != nil {
		p.afterRead()
	}
	if p.readErr != nil {
		return models.EmbyRemoteFile{}, p.readErr
	}
	return remote, err
}

func (p *exhaustedCleanupProvider) DeleteDirectory(ctx context.Context, scope models.EmbyDirectoryScope, guard func() error) (bool, error) {
	if p.directoryFailures > 0 {
		if err := guard(); err != nil {
			return false, err
		}
		p.assertAttemptSaved([]string{"directory:" + models.EmbyDirectoryScopeKey(scope)})
		p.directoryCalls++
		p.directoryFailures--
		return false, errors.New("directory response uncertain")
	}
	return p.cleanupWorkerProvider.DeleteDirectory(ctx, scope, guard)
}

// 前三次失败后在收件状态收尾前重启；第四次远端已删，可选择结果未保存或已保存 failed。
func setupExhaustedCleanupRecovery(t *testing.T, directory, finished bool) (*cleanupWorkerFixture, *exhaustedCleanupProvider, models.EmbyWebhookRecord) {
	t.Helper()
	f := setupCleanupWorkerTest(t, "Movie", directory)
	provider := &exhaustedCleanupProvider{cleanupWorkerProvider: f.cloud, directoryFailures: 3, skipFileWrites: directory}
	f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
	f.cloud.beforeBatch = func(context.Context, []models.EmbyFrozenFile) (bool, error) {
		return false, errors.New("batch response uncertain")
	}
	record := f.receive(t)
	for range 3 {
		if err := f.worker.processDeletion(t.Context(), claimWebhookTest(t)); err == nil {
			t.Fatal("删除失败未保留重试")
		}
		if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	afterDelete := func() error {
		if finished {
			provider.readErr = errors.New("confirmation read failed")
			return provider.readErr
		}
		cancel()
		return context.Canceled
	}
	f.cloud.beforeBatch = func(_ context.Context, files []models.EmbyFrozenFile) (bool, error) {
		for _, file := range files {
			delete(f.cloud.files, file.FileID)
		}
		return false, afterDelete()
	}
	f.cloud.afterDirectory = afterDelete
	claim := claimWebhookTest(t)
	if err := f.worker.processDeletion(ctx, claim); err == nil {
		t.Fatal("第四次删除后未复现待确认状态")
	}
	current := readWebhookTest(t, record.ID)
	if current.Status != models.EmbyWebhookRunning || current.Attempts != 0 || len(f.cloud.files) != 0 {
		t.Fatalf("中断夹具无效: status=%s attempts=%d files=%d", current.Status, current.Attempts, len(f.cloud.files))
	}
	rows := cleanupWorkerRows(t, record.ID)
	id := "f1"
	writes := len(f.cloud.batchCalls)
	if directory {
		id = "media-root"
		writes = f.cloud.directoryCalls
	}
	if writes != models.EmbyWebhookTargetMaxAttempts {
		t.Fatalf("未复现第四次实际删除: %d", writes)
	}
	row := rows[id]
	var execution models.EmbyWebhookTargetExecution
	if err := json.Unmarshal([]byte(row.TargetJSON), &execution); err != nil {
		t.Fatal(err)
	}
	if row.Attempts != models.EmbyWebhookTargetMaxAttempts || len(execution.ExecutionAttempts) != models.EmbyWebhookTargetMaxAttempts || row.Outcome != models.EmbyDeletionFailed {
		t.Fatalf("第四次发送未持久记录: %+v", row)
	}
	last := execution.ExecutionAttempts[len(execution.ExecutionAttempts)-1]
	if (last.FinishedAt != 0) != finished || last.ClaimToken != claim.ClaimToken {
		t.Fatalf("第四次 attempt 状态不符: %+v", last)
	}
	f.cloud.beforeBatch, f.cloud.afterDirectory, provider.readErr = nil, nil, nil
	return f, provider, claim
}

func TestWebhookCleanupExhaustedTargetsConfirmAbsence(t *testing.T) {
	for _, tc := range []struct {
		name      string
		directory bool
		finished  bool
	}{
		{name: "files_result_not_saved"},
		{name: "files_failed_result_saved", finished: true},
		{name: "directory_result_not_saved", directory: true},
		{name: "directory_failed_result_saved", directory: true, finished: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, previousClaim := setupExhaustedCleanupRecovery(t, tc.directory, tc.finished)
			before := cleanupWorkerRows(t, previousClaim.ID)
			fileReads, directoryReads, lists := len(f.cloud.statCalls), len(f.cloud.directoryStats), f.cloud.listCalls
			writes := len(f.cloud.batchCalls) + f.cloud.directoryCalls
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			claim := claimWebhookTest(t)
			if claim.ClaimToken == previousClaim.ClaimToken {
				t.Fatal("恢复没有重新领取")
			}
			f.worker.process(t.Context(), claim)
			assertCleanupWorkerDone(t, claim.ID)
			if len(f.cloud.batchCalls)+f.cloud.directoryCalls != writes || f.cloud.listCalls != lists {
				t.Fatal("耗尽后的只读确认重新发送删除或列目录")
			}
			if got := f.cloud.statCalls[fileReads:]; len(got) != 3 || !slices.Contains(got, "f1") || !slices.Contains(got, "nfo") || !slices.Contains(got, "image") {
				t.Fatalf("未分别确认全部已知原文件一次: %v", got)
			}
			wantDirectoryReads := 0
			if tc.directory {
				wantDirectoryReads = 1
			}
			if len(f.cloud.directoryStats)-directoryReads != wantDirectoryReads {
				t.Fatal("原目录确认次数不符")
			}
			for id, row := range cleanupWorkerRows(t, claim.ID) {
				if row.Outcome != models.EmbyDeletionAlreadyAbsent || row.Attempts != before[id].Attempts || row.TargetJSON != before[id].TargetJSON {
					t.Fatalf("缺失未确认或旧 attempt/claim 被改写: id=%s outcome=%s attempts=%d", id, row.Outcome, row.Attempts)
				}
			}
			if current := readWebhookTest(t, claim.ID); current.Attempts != 0 || current.PlanJSON != claim.PlanJSON {
				t.Fatal("只读成功消耗重试或改写冻结计划")
			}
		})
	}
}

func TestWebhookCleanupExhaustedAbsentRootRetainsMovedMember(t *testing.T) {
	f, _, record := setupExhaustedCleanupRecovery(t, true, false)
	moved := models.EmbyRemoteFile{FileID: "f1", ParentID: "other-root", Path: "/movies/Other", FileName: "movie.mkv", SHA1: "sha1", FileSize: 100, MTime: 123}
	f.cloud.files["f1"] = moved
	before := cleanupWorkerRows(t, record.ID)
	writes := len(f.cloud.batchCalls) + f.cloud.directoryCalls
	fileReads := len(f.cloud.statCalls)
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.worker.process(t.Context(), claimWebhookTest(t))
	rows := cleanupWorkerRows(t, record.ID)
	if rows["media-root"].Outcome != models.EmbyDeletionAlreadyAbsent || rows["f1"].Outcome != models.EmbyDeletionUnresolved || rows["nfo"].Outcome != models.EmbyDeletionAlreadyAbsent || rows["image"].Outcome != models.EmbyDeletionAlreadyAbsent {
		t.Fatalf("目录缺失掩盖已知成员状态: root=%s video=%s nfo=%s image=%s", rows["media-root"].Outcome, rows["f1"].Outcome, rows["nfo"].Outcome, rows["image"].Outcome)
	}
	if current := readWebhookTest(t, record.ID); current.Status != models.EmbyWebhookUnresolved || current.Attempts != 0 {
		t.Fatalf("仍在的原对象未停止: %+v", current)
	}
	if f.cloud.files["f1"] != moved || len(f.cloud.batchCalls)+f.cloud.directoryCalls != writes || len(f.cloud.statCalls)-fileReads != 3 {
		t.Fatal("未逐原成员确认或追删了已移动对象")
	}
	for id, row := range rows {
		if row.TargetJSON != before[id].TargetJSON || row.Attempts != before[id].Attempts {
			t.Fatal("只读恢复修改了发送历史")
		}
	}
}

func TestWebhookCleanupExhaustedCoveredMembersBlockDirectorySend(t *testing.T) {
	for _, name := range []string{"original_members_present", "members_absent_then_directory_retries"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", true)
			provider := &exhaustedCleanupProvider{cleanupWorkerProvider: f.cloud, directoryFailures: 2}
			f.worker.provider = func(models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) { return provider, nil }
			f.cloud.beforeBatch = func(context.Context, []models.EmbyFrozenFile) (bool, error) {
				return false, errors.New("file batch response uncertain")
			}
			record := f.receive(t)
			for range 2 {
				f.worker.process(t.Context(), claimWebhookTest(t))
			}
			before := cleanupWorkerRows(t, record.ID)
			if before["media-root"].Attempts != 2 || before["f1"].Attempts != models.EmbyWebhookTargetMaxAttempts || f.cloud.directoryCalls != 2 || len(f.cloud.batchCalls) != 2 {
				t.Fatal("目录与文件回退没有按实际参与成员分别累计")
			}
			absent := name == "members_absent_then_directory_retries"
			if absent {
				clear(f.cloud.files)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			if f.cloud.directoryCalls != 2 || len(f.cloud.batchCalls) != 2 || f.cloud.directory == nil {
				t.Fatal("未耗尽目录向已耗尽成员发送了第五次覆盖删除")
			}
			for id, row := range cleanupWorkerRows(t, record.ID) {
				if row.Attempts != before[id].Attempts || row.TargetJSON != before[id].TargetJSON {
					t.Fatal("发送被拒绝后仍增加了 attempt 或修改历史")
				}
				if id == "media-root" {
					continue
				}
				wantOutcome := models.EmbyDeletionUnresolved
				if absent {
					wantOutcome = models.EmbyDeletionAlreadyAbsent
				}
				if row.Outcome != wantOutcome {
					t.Fatalf("已耗尽成员没有只读确认: id=%s outcome=%s", id, row.Outcome)
				}
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			rows := cleanupWorkerRows(t, record.ID)
			if absent {
				assertCleanupWorkerDone(t, record.ID)
				if f.cloud.directoryCalls != 3 || f.cloud.directory != nil || rows["media-root"].Attempts != 3 {
					t.Fatal("已确认缺失的成员仍阻止剩余目录删除")
				}
			} else if f.cloud.directoryCalls != 2 || f.cloud.directory == nil || readWebhookTest(t, record.ID).Status != models.EmbyWebhookUnresolved {
				t.Fatal("仍存在的已耗尽成员未阻止后续目录发送")
			}
			for id, row := range rows {
				if id != "media-root" && (row.Attempts != before[id].Attempts || row.TargetJSON != before[id].TargetJSON) {
					t.Fatal("后续目录操作重新计入了已完成或已耗尽成员")
				}
			}
		})
	}
}

func TestWebhookCleanupFourthAttemptGuardReusesOnlyCurrentClaim(t *testing.T) {
	for _, tc := range []struct {
		name      string
		directory bool
	}{
		{name: "files"},
		{name: "directory", directory: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, record := setupExhaustedCleanupRecovery(t, tc.directory, false)
			before := cleanupWorkerRows(t, record.ID)
			var plan models.EmbyDeletionPlan
			if err := json.Unmarshal([]byte(record.PlanJSON), &plan); err != nil {
				t.Fatal(err)
			}
			targets := plan.Targets
			id := "f1"
			if tc.directory {
				for _, target := range plan.Targets {
					if target.Kind == "directory" {
						targets = []models.EmbyDeletionTarget{target}
					}
				}
				id = "media-root"
			}
			var execution models.EmbyWebhookTargetExecution
			if err := json.Unmarshal([]byte(before[id].TargetJSON), &execution); err != nil {
				t.Fatal(err)
			}
			last := execution.ExecutionAttempts[len(execution.ExecutionAttempts)-1]
			attempt, err := models.BeginEmbyWebhookAttempt(t.Context(), record, targets)
			if err != nil || attempt != last.ID {
				t.Fatalf("同一第四次发送的重复 guard 未复用登记: attempt=%s err=%v", attempt, err)
			}
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			attempt, err = models.BeginEmbyWebhookAttempt(t.Context(), claimWebhookTest(t), targets)
			if attempt != "" || !errors.Is(err, models.ErrEmbyWebhookAttemptLimit) {
				t.Fatalf("新 claim 可登记第五次发送: attempt=%s err=%v", attempt, err)
			}
			for id, row := range cleanupWorkerRows(t, record.ID) {
				if row.TargetJSON != before[id].TargetJSON || row.Attempts != before[id].Attempts {
					t.Fatal("重复 guard 或拒绝新发送修改了历史")
				}
			}
		})
	}
}

func TestWebhookCleanupExhaustedConfirmationErrorsUseRecordRetryLimit(t *testing.T) {
	f, provider, record := setupExhaustedCleanupRecovery(t, false, true)
	before := cleanupWorkerRows(t, record.ID)
	writes, fileReads := len(f.cloud.batchCalls), len(f.cloud.statCalls)
	provider.readErr = errors.New("temporary detail failure")
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= webhookMaxAttempts; attempt++ {
		f.worker.process(t.Context(), claimWebhookTest(t))
		wantStatus := models.EmbyWebhookRetry
		if attempt == webhookMaxAttempts {
			wantStatus = models.EmbyWebhookUnresolved
		}
		if current := readWebhookTest(t, record.ID); current.Status != wantStatus || current.Attempts != attempt {
			t.Fatalf("确认错误没有遵守收件重试上限: %+v", current)
		}
	}
	if len(f.cloud.batchCalls) != writes || len(f.cloud.statCalls)-fileReads != 3*webhookMaxAttempts {
		t.Fatal("查询错误重发了删除或跳过原身份确认")
	}
	for id, row := range cleanupWorkerRows(t, record.ID) {
		if row.Outcome != models.EmbyDeletionFailed || row.TargetJSON != before[id].TargetJSON || row.Attempts != before[id].Attempts {
			t.Fatal("确认失败被当作缺失或修改了发送历史")
		}
	}
}

func TestWebhookCleanupExhaustedConfirmationKeepsClaimAndAccountGuards(t *testing.T) {
	for _, name := range []string{"claim_lost_before_read", "claim_lost_after_read", "account_changed_before_read", "account_changed_after_read"} {
		t.Run(name, func(t *testing.T) {
			f, provider, record := setupExhaustedCleanupRecovery(t, false, false)
			before := cleanupWorkerRows(t, record.ID)
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			claim := claimWebhookTest(t)
			change := func() {
				if name == "claim_lost_before_read" || name == "claim_lost_after_read" {
					if err := db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", claim.ID).Update("claim_token", "replacement-claim").Error; err != nil {
						t.Fatal(err)
					}
				} else if err := db.Db.Model(&models.Account{}).Where("id = ?", f.file.AccountId).Update("user_id", "replacement-account").Error; err != nil {
					t.Fatal(err)
				}
			}
			if name == "claim_lost_after_read" || name == "account_changed_after_read" {
				provider.afterRead = change
			} else {
				change()
			}
			fileReads, writes := len(f.cloud.statCalls), len(f.cloud.batchCalls)
			f.worker.process(t.Context(), claim)
			if len(f.cloud.batchCalls) != writes {
				t.Fatal("领取或账号变化后发送了删除")
			}
			if name == "claim_lost_before_read" || name == "account_changed_before_read" {
				if len(f.cloud.statCalls) != fileReads {
					t.Fatal("身份失效后读取了原账号对象")
				}
			} else if len(f.cloud.statCalls)-fileReads != 1 {
				t.Fatal("未覆盖远端确认期间的身份变化")
			}
			for id, row := range cleanupWorkerRows(t, record.ID) {
				if row.Outcome == models.EmbyDeletionAlreadyAbsent || row.TargetJSON != before[id].TargetJSON || row.Attempts != before[id].Attempts {
					t.Fatal("失效的确认结果被保存或篡改发送记录")
				}
			}
		})
	}
}
