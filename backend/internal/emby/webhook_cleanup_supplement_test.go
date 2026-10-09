package emby

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestWebhookCleanupExternalAccountConflictIsolated(t *testing.T) {
	for _, test := range []struct {
		name      string
		sameOwner bool
		missing   bool
	}{
		{name: "changed_account_other_owner"},
		{name: "missing_account_other_owner", missing: true},
		{name: "changed_account_same_owner", sameOwner: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setupExternalDeletionTest(t)
			f.remote.addFile("10")
			account, cloud := newExternalDeletionAccount(t, "conflicting-owner")
			cloud.addFile("20")
			failed, healthy := externalDeletionSnapshot(account, "101", "20"), f.snapshot("102", "10")
			if test.sameOwner {
				healthy = f.snapshot("101", "10")
				failed.Sources[0].ID = "conflicting-source"
				healthy.Sources = append(failed.Sources, healthy.Sources...)
				f.apply(t, healthy)
			} else {
				for _, snapshot := range []*models.EmbyItemSnapshot{&failed, &healthy} {
					snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
				}
				f.apply(t, failed, healthy)
				f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
			}
			record := f.receive(t)
			reason := "source_account_changed"
			if test.missing {
				if err := db.Db.Delete(&account).Error; err != nil {
					t.Fatal(err)
				}
				reason = "source_account_missing"
			} else if err := db.Db.Model(&account).Update("user_id", "replacement-owner").Error; err != nil {
				t.Fatal(err)
			}
			logs := captureEmbySupplementLogs(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			logReason := "网盘账号身份已变化：1 个"
			if test.missing {
				logReason = "网盘账号已不存在：1 个"
			}
			assertEmbySupplementSummary(t, logs.String(), logReason)
			current := readWebhookTest(t, record.ID)
			if current.Status != models.EmbyWebhookUnresolved || current.Attempts != 0 || current.PlanJSON == "" || current.InputJSON != record.InputJSON {
				t.Fatalf("账号冲突阻断独立目标或改写输入：status=%s attempts=%d reason=%s", current.Status, current.Attempts, current.Reason)
			}
			plan := externalDeletionPlan(t, current)
			if len(plan.Supplements) != 1 || len(plan.Targets) != 1 || plan.Targets[0].File.AccountID != f.account.ID || !slices.ContainsFunc(plan.Issues, func(issue string) bool {
				return strings.HasPrefix(issue, "source_unresolved:"+reason+":") && strings.HasSuffix(issue, ":101")
			}) {
				t.Fatalf("未分别保存健康来源和具体冲突：supplements=%+v targets=%+v issues=%v", plan.Supplements, plan.Targets, plan.Issues)
			}
			rows := cleanupWorkerRows(t, record.ID)
			if len(rows) != 1 || rows["10"].Outcome != models.EmbyDeletionDeleted || rows["10"].Attempts != 1 || len(f.remote.writes) != 1 || !slices.Equal(f.remote.writes[0], []string{"10"}) {
				t.Fatalf("健康来源未独立完成：rows=%+v writes=%v", rows, f.remote.writes)
			}
			if len(cloud.lookups)+len(cloud.details)+len(cloud.lists)+len(cloud.writes) != 0 {
				t.Fatal("账号冲突仍发出了网盘请求")
			}
			var remaining []models.EmbyMediaItem
			if err := db.Db.Where("item_id IN ?", []string{"101", "102"}).Find(&remaining).Error; err != nil || len(remaining) != 1 || remaining[0].ItemId != "101" {
				t.Fatalf("来源问题影响了其他 owner 收尾：remaining=%+v err=%v", remaining, err)
			}
		})
	}
}

func TestWebhookCleanupExternalLookupFailureKeepsRetryBudget(t *testing.T) {
	for _, recoverLookup := range []bool{false, true} {
		name := "exhausted_failure_keeps_independent_success"
		if recoverLookup {
			name = "temporary_failure_recovers_before_limit"
		}
		t.Run(name, func(t *testing.T) {
			f := setupExternalDeletionTest(t)
			f.remote.addFile("10")
			account, cloud := newExternalDeletionAccount(t, "unavailable-owner")
			cloud.addFile("20")
			cloud.lookupUnavailable = true
			failed, healthy := externalDeletionSnapshot(account, "101", "20"), f.snapshot("102", "10")
			for _, snapshot := range []*models.EmbyItemSnapshot{&failed, &healthy} {
				snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
			}
			f.apply(t, failed, healthy)
			f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
			record := f.receive(t)
			failures := webhookMaxAttempts
			if recoverLookup {
				failures = 1
			}
			logs := captureEmbySupplementLogs(t)
			for attempt := 1; attempt <= failures; attempt++ {
				f.worker.process(t.Context(), claimWebhookTest(t))
				current := readWebhookTest(t, record.ID)
				if attempt < webhookMaxAttempts && (current.Status != models.EmbyWebhookRetry || current.Attempts != attempt || current.NextAttemptAt == 0 || current.PlanJSON != "" || len(f.remote.writes)+len(cloud.writes) != 0) {
					t.Fatalf("临时定位失败提前耗尽重试或持久化残缺计划：attempt=%d status=%s count=%d plan=%s", attempt, current.Status, current.Attempts, current.PlanJSON)
				}
			}
			if recoverLookup {
				cloud.lookupUnavailable = false
				f.worker.process(t.Context(), claimWebhookTest(t))
				assertCleanupWorkerDone(t, record.ID)
				current := readWebhookTest(t, record.ID)
				plan := externalDeletionPlan(t, current)
				if current.Attempts != 1 || len(plan.Supplements) != 2 || len(plan.Issues) != 0 || len(f.remote.writes) != 1 || len(cloud.writes) != 1 || len(cloud.lookups) != 2 {
					t.Fatalf("定位恢复后未完成原两个来源：attempts=%d supplements=%d issues=%v writes=%v/%v lookup=%v", current.Attempts, len(plan.Supplements), plan.Issues, f.remote.writes, cloud.writes, cloud.lookups)
				}
				return
			}
			before := readWebhookTest(t, record.ID)
			assertEmbySupplementSummary(t, logs.String(), "文件信息查询仍失败，已用完 4 次处理机会并停止本次重试：1 个")
			plan := externalDeletionPlan(t, before)
			if before.Status != models.EmbyWebhookUnresolved || before.Attempts != webhookMaxAttempts || len(plan.Supplements) != 1 || len(plan.Targets) != 1 || !slices.ContainsFunc(plan.Issues, func(issue string) bool {
				return strings.HasPrefix(issue, "source_unresolved:source_lookup_retries_exhausted:") && strings.HasSuffix(issue, ":101")
			}) {
				t.Fatalf("最后一次定位失败吞掉独立目标：status=%s attempts=%d supplements=%d targets=%d issues=%v", before.Status, before.Attempts, len(plan.Supplements), len(plan.Targets), plan.Issues)
			}
			rows := cleanupWorkerRows(t, record.ID)
			if len(rows) != 1 || rows["10"].Outcome != models.EmbyDeletionDeleted || rows["10"].Attempts != 1 || len(f.remote.writes) != 1 || len(cloud.writes) != 0 || len(cloud.lookups) != webhookMaxAttempts {
				t.Fatalf("未保存独立部分成功：rows=%+v writes=%v/%v lookup=%v", rows, f.remote.writes, cloud.writes, cloud.lookups)
			}
			// 模拟已保存部分结果、尚未完成通知收尾时重启。来源随后恢复也
			// 不得改写原计划补证或重新发送已成功目标。
			if err := db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", record.ID).Update("status", models.EmbyWebhookRunning).Error; err != nil {
				t.Fatal(err)
			}
			externalDeletionReopenSQLite(t)
			cloud.lookupUnavailable = false
			f.worker = &webhookWorker{verify: verifyEmbyDeletion, provider: models.NewEmbyDeleteProvider}
			if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			after := readWebhookTest(t, record.ID)
			reopened := cleanupWorkerRows(t, record.ID)
			if after.Status != models.EmbyWebhookUnresolved || after.PlanJSON != before.PlanJSON || after.InputJSON != record.InputJSON || reopened["10"].TargetJSON != rows["10"].TargetJSON || reopened["10"].Outcome != models.EmbyDeletionDeleted || reopened["10"].Attempts != 1 || len(f.remote.writes) != 1 || len(cloud.writes) != 0 || len(cloud.lookups) != webhookMaxAttempts {
				t.Fatalf("重启改写来源/结果或重复定位/发送：status=%s rows=%+v writes=%v/%v lookup=%v", after.Status, reopened, f.remote.writes, cloud.writes, cloud.lookups)
			}
		})
	}
}

func TestWebhookCleanupExternalGlobalFailureNeverSavesPartialPlan(t *testing.T) {
	for _, name := range []string{"server_stale", "input_generation_changed", "unknown_verification", "other_owner_conflict", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			f := setupExternalDeletionTest(t)
			f.remote.addFile("10")
			f.remote.addFile("20")
			first, second := f.snapshot("101", "10"), f.snapshot("102", "20")
			for _, snapshot := range []*models.EmbyItemSnapshot{&first, &second} {
				snapshot.Item.Type, snapshot.Item.SeasonId, snapshot.Item.SeriesId = "Episode", "301", "300"
			}
			f.apply(t, first, second)
			f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
			record := f.receive(t)
			if err := db.Db.Model(&models.EmbyWebhookRecord{}).Where("id = ?", record.ID).Update("attempts", webhookMaxAttempts-1).Error; err != nil {
				t.Fatal(err)
			}
			if name == "input_generation_changed" {
				if err := db.Db.Model(&models.EmbyItemState{}).Where("item_id = ?", "102").Update("generation", gorm.Expr("generation + 1")).Error; err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			verify := f.worker.verify
			f.worker.verify = func(ctx context.Context, input models.EmbyDeletionInput, target models.EmbyDeletionTarget) error {
				if slices.ContainsFunc(target.Owners, func(owner models.EmbyDeletionOwnerRef) bool { return owner.ItemID == "102" }) {
					switch name {
					case "server_stale":
						return models.ErrEmbySnapshotStale
					case "unknown_verification":
						return errors.New("global verification unavailable")
					case "other_owner_conflict":
						return &models.EmbyDeletionOwnerConflict{IDs: []string{"301"}, Err: models.ErrEmbyDeleteUnverified}
					case "cancelled":
						cancel()
						return context.Canceled
					}
				}
				return verify(ctx, input, target)
			}
			f.worker.process(ctx, claimWebhookTest(t))
			current := readWebhookTest(t, record.ID)
			wantStatus := models.EmbyWebhookUnresolved
			if name == "cancelled" {
				wantStatus = models.EmbyWebhookRunning
			}
			if current.Status != wantStatus || current.PlanJSON != "" || current.InputJSON != record.InputJSON || len(cleanupWorkerRows(t, record.ID)) != 0 || len(f.remote.writes) != 0 || !slices.Equal(f.remote.lookups, []string{"external-10"}) {
				t.Fatalf("最后一次全局失败被降级为部分来源问题：status=%s plan=%s writes=%v lookup=%v", current.Status, current.PlanJSON, f.remote.writes, f.remote.lookups)
			}
		})
	}
}

func TestWebhookCleanupExternalRetryIsolatesConfirmedAccountConflict(t *testing.T) {
	for _, name := range []string{"account_changed", "account_missing", "factory_unverified", "factory_not_found", "factory_database_error", "factory_snapshot_stale"} {
		t.Run(name, func(t *testing.T) {
			f := setupExternalDeletionTest(t)
			f.remote.addFile("10")
			f.remote.addFile("11")
			account, cloud := newExternalDeletionAccount(t, "retry-owner")
			cloud.addFile("20")
			snapshots := []models.EmbyItemSnapshot{f.snapshot("101", "10"), externalDeletionSnapshot(account, "102", "20"), f.snapshot("103", "11")}
			for i := range snapshots {
				snapshots[i].Item.Type, snapshots[i].Item.SeasonId, snapshots[i].Item.SeriesId = "Episode", "301", "300"
			}
			f.apply(t, snapshots...)
			f.envelope.ItemID, f.envelope.ItemType = "301", "Season"
			f.remote.partial, f.remote.keepID = true, "11"
			cloud.partial, cloud.keepID = true, "20"
			record := f.receive(t)
			f.worker.process(t.Context(), claimWebhookTest(t))
			before := readWebhookTest(t, record.ID)
			beforeRows := cleanupWorkerRows(t, record.ID)
			if before.Status != models.EmbyWebhookRetry || len(beforeRows) != 3 || beforeRows["10"].Outcome != models.EmbyDeletionDeleted || beforeRows["11"].Outcome != models.EmbyDeletionFailed || beforeRows["20"].Outcome != models.EmbyDeletionFailed {
				t.Fatalf("首次未形成跨账号部分结果：status=%s rows=%+v", before.Status, beforeRows)
			}
			f.remote.mu.Lock()
			f.remote.partial = false
			f.remote.mu.Unlock()
			cloud.mu.Lock()
			cloud.partial = false
			cloud.mu.Unlock()
			var factoryErr error
			wantReason := "source_account_changed"
			switch name {
			case "account_changed":
				if err := db.Db.Model(&account).Update("user_id", "replacement-owner").Error; err != nil {
					t.Fatal(err)
				}
			case "account_missing":
				if err := db.Db.Delete(&account).Error; err != nil {
					t.Fatal(err)
				}
				wantReason = "source_account_missing"
			case "factory_unverified":
				factoryErr = models.ErrEmbyDeleteUnverified
			case "factory_not_found":
				factoryErr = gorm.ErrRecordNotFound
			case "factory_database_error":
				factoryErr = errors.New("provider database query unavailable")
			case "factory_snapshot_stale":
				factoryErr = models.ErrEmbySnapshotStale
			}
			if factoryErr != nil {
				provider := f.worker.provider
				f.worker.provider = func(file models.EmbyFrozenFile) (models.EmbyDeleteProvider, error) {
					if file.AccountID == account.ID {
						return nil, factoryErr
					}
					return provider(file)
				}
			}
			lookups, otherLookups := len(f.remote.lookups), len(cloud.lookups)
			f.worker.process(t.Context(), claimWebhookTest(t))
			after := readWebhookTest(t, record.ID)
			rows := cleanupWorkerRows(t, record.ID)
			if after.PlanJSON != before.PlanJSON || after.InputJSON != record.InputJSON || len(f.remote.lookups) != lookups || len(cloud.lookups) != otherLookups || rows["10"].TargetJSON != beforeRows["10"].TargetJSON || rows["10"].Outcome != models.EmbyDeletionDeleted || rows["10"].Attempts != 1 || len(cloud.writes) != 1 {
				t.Fatalf("重试改写原计划、重复补证或重发已完成目标：rows=%+v writes=%v/%v", rows, f.remote.writes, cloud.writes)
			}
			if factoryErr != nil {
				if after.Status != models.EmbyWebhookRetry || after.Attempts != before.Attempts+1 || len(f.remote.writes) != 1 || rows["11"].Outcome != models.EmbyDeletionFailed || rows["20"].Outcome != models.EmbyDeletionFailed || rows["11"].Attempts != 1 || rows["20"].Attempts != 1 {
					t.Fatalf("未经原账号核实就吞掉 factory 错误：status=%s rows=%+v writes=%v", after.Status, rows, f.remote.writes)
				}
				return
			}
			if after.Status != models.EmbyWebhookUnresolved || rows["20"].Outcome != models.EmbyDeletionUnresolved || rows["20"].Reason != wantReason || rows["20"].Attempts != 1 || rows["11"].Outcome != models.EmbyDeletionDeleted || rows["11"].Attempts != 2 || len(f.remote.writes) != 2 || !slices.Equal(f.remote.writes[1], []string{"11"}) {
				t.Fatalf("账号冲突阻断了健康待执行目标：status=%s rows=%+v writes=%v/%v", after.Status, rows, f.remote.writes, cloud.writes)
			}
		})
	}
}
