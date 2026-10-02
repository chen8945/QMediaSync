package models

import (
	"context"
	"errors"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/realtime"

	"gorm.io/gorm"
)

func setupSyncLedgerTestDB(t *testing.T) {
	t.Helper()
	setupSyncDeleteTestDB(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	oldHub := realtime.GlobalSyncTaskHub
	realtime.GlobalSyncTaskHub = realtime.NewSyncTaskHub()
	t.Cleanup(func() { realtime.GlobalSyncTaskHub = oldHub })
}

func TestSyncLedgerPreservesGenerationAndPublishesPersistedSnapshot(t *testing.T) {
	setupSyncLedgerTestDB(t)
	path := &SyncPath{BaseCid: "ledger", IsFullSync: true}
	if err := db.Db.Create(path).Error; err != nil {
		t.Fatal(err)
	}
	pending := realtime.SyncLedgerPending
	task := &Sync{SyncPathId: path.ID, Status: SyncStatusInProgress, IsFullSync: true, Total: 7, NewStrm: 3, Logger: helpers.AppLogger, LedgerStatus: &pending}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	stale := *task
	events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(task.ID, "", 8)
	defer unsubscribe()
	if err := task.SaveGeneration(t.Context(), SyncStatusCompleted, "", true); err != nil {
		t.Fatal(err)
	}
	generated := <-events
	if generated.Terminal || generated.Payload.FinishAt != task.FinishAt {
		t.Fatalf("生成里程碑不应结束后台流: %+v", generated)
	}
	var storedPath SyncPath
	if err := db.Db.First(&storedPath, path.ID).Error; err != nil || storedPath.LastSyncAt != task.FinishAt || storedPath.IsFullSync {
		t.Fatalf("扫描水位没有随生成结果提交: %+v %v", storedPath, err)
	}
	if err := stale.UpdateLedger(t.Context(), realtime.SyncLedgerRunning, nil, ""); err != nil {
		t.Fatal(err)
	}
	running := <-events
	if running.Terminal || running.Payload.Status != int(SyncStatusCompleted) || running.Payload.FinishAt != task.FinishAt || running.Payload.NewStrm != 3 {
		t.Fatalf("后台事件使用了旧生成快照: %+v", running)
	}
	finishedAt := task.FinishAt + 8
	if err := stale.UpdateLedger(t.Context(), realtime.SyncLedgerFailed, &finishedAt, "access_token=secret-value"); err != nil {
		t.Fatal(err)
	}
	finished := <-events
	if !finished.Terminal || finished.Payload.Sequence != running.Payload.Sequence+1 || finished.Payload.LedgerFinishedAt == nil || *finished.Payload.LedgerFinishedAt != finishedAt {
		t.Fatalf("后台终态事件错误: %+v", finished)
	}
	var stored Sync
	if err := db.Db.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != task.Status || stored.FinishAt != task.FinishAt || stored.NewStrm != 3 || stored.Total != 7 || strings.Contains(stored.LedgerError, "secret-value") {
		t.Fatalf("后台改写生成结果或未脱敏: %+v", stored)
	}
	if err := stale.UpdateLedger(t.Context(), realtime.SyncLedgerRunning, nil, ""); err == nil {
		t.Fatal("晚到 running 不能覆盖后台终态")
	}
	if err := task.SaveGeneration(t.Context(), SyncStatusFailed, "late failure", false); err == nil {
		t.Fatal("生成终点不能再次覆盖")
	}
	select {
	case event := <-events:
		t.Fatalf("失败写入不应广播: %+v", event)
	default:
	}
}

func TestSyncLedgerRequiredWritesFailWithoutPublishing(t *testing.T) {
	setupSyncLedgerTestDB(t)
	pending := realtime.SyncLedgerPending
	task := &Sync{Status: SyncStatusInProgress, SyncPathId: 42, Logger: helpers.AppLogger, LedgerStatus: &pending}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(task.ID, "", 8)
	defer unsubscribe()
	if err := task.SaveGeneration(t.Context(), SyncStatusCompleted, "", true); err == nil {
		t.Fatal("缺失水位行应回滚生成写入")
	}
	var stored Sync
	if err := db.Db.First(&stored, task.ID).Error; err != nil || stored.Status != SyncStatusInProgress || stored.FinishAt != 0 {
		t.Fatalf("生成事务没有回滚: %+v %v", stored, err)
	}
	if err := task.SaveGeneration(t.Context(), SyncStatusCompleted, "", false); err != nil {
		t.Fatal(err)
	}
	<-events
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := task.UpdateLedger(ctx, realtime.SyncLedgerRunning, nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消未传播: %v", err)
	}
	const callback = "test:reject-ledger-write"
	injected := errors.New("ledger write unavailable")
	if err := db.Db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) { tx.AddError(injected) }); err != nil {
		t.Fatal(err)
	}
	if err := task.UpdateLedger(t.Context(), realtime.SyncLedgerRunning, nil, ""); !errors.Is(err, injected) {
		t.Fatalf("后台必要写入失败未传播: %v", err)
	}
	db.Db.Callback().Update().Remove(callback)
	if err := db.Db.Delete(&Sync{}, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	finishedAt := int64(100)
	if err := task.UpdateLedger(t.Context(), realtime.SyncLedgerCompleted, &finishedAt, ""); err == nil {
		t.Fatal("已删除任务不能重新写回")
	}
	if err := db.Db.First(&stored, task.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("后台重建了已删行: %v", err)
	}
	select {
	case event := <-events:
		t.Fatalf("失败写入广播了事件: %+v", event)
	default:
	}
}

func TestSyncLedgerRecoveryKeepsUnknownTimesAndHistoricalRows(t *testing.T) {
	setupSyncLedgerTestDB(t)
	for _, status := range []realtime.SyncLedgerStatus{"", realtime.SyncLedgerNotRequired, realtime.SyncLedgerPending, realtime.SyncLedgerRunning, realtime.SyncLedgerCompleted, realtime.SyncLedgerFailed, realtime.SyncLedgerInterrupted} {
		var ledgerStatus *realtime.SyncLedgerStatus
		if status != "" {
			ledgerStatus = &status
		}
		task := &Sync{Status: SyncStatusCompleted, FinishAt: 100, LedgerStatus: ledgerStatus}
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := InterruptRunningSyncLedgers(t.Context()); err != nil {
		t.Fatal(err)
	}
	var records []Sync
	if err := db.Db.Order("id").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	for i, record := range records {
		if record.Status != SyncStatusCompleted || record.FinishAt != 100 || record.LedgerFinishedAt != nil {
			t.Fatalf("恢复伪造时间或修改生成结果: %+v", record)
		}
		if i == 0 && record.LedgerStatus != nil {
			t.Fatalf("历史未知被改写: %+v", record)
		}
		if (i == 2 || i == 3) && (record.LedgerStatus == nil || *record.LedgerStatus != realtime.SyncLedgerInterrupted || record.LedgerError == "") {
			t.Fatalf("旧在途未显示中断: %+v", record)
		}
	}
}

func TestSyncLedgerNoWorkAndObservedInterruption(t *testing.T) {
	setupSyncLedgerTestDB(t)
	task := &Sync{Status: SyncStatusInProgress, Logger: helpers.AppLogger}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := task.SaveGeneration(t.Context(), SyncStatusCancelled, "cancelled", false); err != nil {
		t.Fatal(err)
	}
	if task.LedgerStatus == nil || *task.LedgerStatus != realtime.SyncLedgerNotRequired || !task.SyncTaskEventPayload().IsTerminal() {
		t.Fatalf("无需后台不能结束: %+v", task)
	}
	pending := realtime.SyncLedgerPending
	interrupted := &Sync{Status: SyncStatusCompleted, FinishAt: 120, LedgerStatus: &pending}
	if err := db.Db.Create(interrupted).Error; err != nil {
		t.Fatal(err)
	}
	exit := int64(128)
	if err := interrupted.UpdateLedger(t.Context(), realtime.SyncLedgerInterrupted, &exit, "service shutdown"); err != nil {
		t.Fatal(err)
	}
	if interrupted.LedgerFinishedAt == nil || *interrupted.LedgerFinishedAt != 128 || !interrupted.SyncTaskEventPayload().IsTerminal() {
		t.Fatalf("已观察到的退出时间未保留: %+v", interrupted)
	}
}

func TestSyncLedgerDeletedRecordErrorRequiresConfirmedAbsence(t *testing.T) {
	for _, mode := range []string{"deleted", "conflict", "query_failure", "write_failure", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			setupSyncLedgerTestDB(t)
			status := realtime.SyncLedgerCompleted
			task := &Sync{Status: SyncStatusCompleted, LedgerStatus: &status}
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatal(err)
			}
			if mode != "conflict" {
				if err := db.Db.Delete(&Sync{}, task.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			cause := errors.New("database unavailable")
			const callback = "test:ledger-absence-error"
			switch mode {
			case "query_failure":
				if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) { tx.AddError(cause) }); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
			case "write_failure":
				if err := db.Db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) { tx.AddError(cause) }); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove(callback) })
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "cancelled" {
				cancel()
				cause = context.Canceled
			}
			err := task.UpdateLedger(ctx, realtime.SyncLedgerRunning, nil, "")
			if err == nil || errors.Is(err, ErrSyncLedgerRecordDeleted) != (mode == "deleted") {
				t.Fatalf("incorrect deleted record classification: %v", err)
			}
			if mode != "deleted" && mode != "conflict" && !errors.Is(err, cause) {
				t.Fatalf("lost original failure: %v", err)
			}
		})
	}
}
