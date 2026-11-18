package models

import (
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func TestSyncDurationsCloseFailedAndCancelledStages(t *testing.T) {
	for _, status := range []SyncStatus{SyncStatusCompleted, SyncStatusFailed, SyncStatusCancelled} {
		t.Run(SyncStatusText[status], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setupSyncLedgerTestDB(t)
				task := CreateSync(0, "/remote", "root", "/local")
				if task == nil {
					t.Fatal("创建任务失败")
				}
				task.Logger = helpers.AppLogger
				if err := task.UpdateSubStatus(t.Context(), SyncSubStatusProcessNetFileList); err != nil {
					t.Fatal(err)
				}
				time.Sleep(500 * time.Microsecond)
				if err := task.SaveGeneration(t.Context(), status, "", false); err != nil {
					t.Fatal(err)
				}
				assertSyncDuration(t, task.NetFileDurationMs, 0)
				assertSyncDuration(t, task.GenerationDurationMs, 0)
				assertSyncDuration(t, task.TotalDurationMs, 0)
				if task.LocalFileDurationMs != nil {
					t.Fatal("未进入本地阶段，耗时应未记录")
				}
			})
		})
	}
}

func TestSyncDurationsDoNotInventHistoricalPrecision(t *testing.T) {
	setupSyncLedgerTestDB(t)
	task := &Sync{BaseModel: BaseModel{CreatedAt: 100}, Logger: helpers.AppLogger}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := task.SaveGeneration(t.Context(), SyncStatusFailed, "", false); err != nil {
		t.Fatal(err)
	}
	if task.GenerationDurationMs != nil || task.TotalDurationMs != nil || task.NetFileDurationMs != nil || task.LocalFileDurationMs != nil {
		t.Fatal("缺少本轮计时起点时不能推算毫秒精度")
	}
}

func assertSyncDuration(t *testing.T, got *int64, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("耗时未记录，期望 %d ms", want)
	}
	if *got != want {
		t.Fatalf("耗时 = %d ms，期望 %d ms", *got, want)
	}
}
