package backup

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"qmediasync/internal/models"
)

func TestBackupInitialProgressExcludesNonExportedTables(t *testing.T) {
	setupBackupTest(t)
	models.AllTables = []any{&backupTestItem{}, &models.UserSession{}, &models.EmbyObservedEvidenceIndex{}, &models.EmbyItemMembership{}}
	if err := beginTask("backup"); err != nil {
		t.Fatal(err)
	}
	result := GetRunningResult()
	if result.Total != 1 || result.Count != 0 {
		t.Fatalf("initial progress includes excluded tables: %+v", result)
	}
	finishTask(nil)
}

func TestRestoreRestartRequiresCurrentTerminalReceipt(t *testing.T) {
	for _, scenario := range []string{"completed", "failed", "wrong_receipt", "empty_receipt", "expired_receipt", "backup", "running", "idle", "preflight_failure", "unsupported"} {
		t.Run(scenario, func(t *testing.T) {
			setupBackupTest(t)
			restoreReceipt = "current-restore-receipt"
			runningResult = BackupOrRestoreResult{Type: "restore", Status: models.BackupStatusCompleted, RestartRequired: true}
			receipt, supported := restoreReceipt, true
			var wantErr error
			switch scenario {
			case "failed":
				runningResult.Status, runningResult.ErrorMsg = models.BackupStatusFailed, "恢复失败"
			case "wrong_receipt":
				receipt, wantErr = "previous-restore-receipt", ErrRestoreReceipt
			case "empty_receipt":
				receipt, wantErr = "", ErrRestoreReceipt
			case "expired_receipt":
				restoreReceipt, wantErr = "", ErrRestoreReceipt
			case "backup":
				runningResult.Type, wantErr = "backup", ErrRestartNotReady
			case "running":
				runningResult.IsRunning, wantErr = true, ErrRestartNotReady
			case "idle":
				runningResult.Status, wantErr = "idle", ErrRestartNotReady
			case "preflight_failure":
				runningResult.Status = models.BackupStatusFailed
				runningResult.RestartRequired, wantErr = false, ErrRestartNotReady
			case "unsupported":
				supported, wantErr = false, ErrRestartUnsupported
			}
			before := runningResult
			calls := 0
			err := restartWithReceipt(receipt, supported, func() error { calls++; return nil })
			if !errors.Is(err, wantErr) {
				t.Fatalf("error=%v, want=%v", err, wantErr)
			}
			if wantErr == nil {
				before.RestartRequested = true
				if calls != 1 {
					t.Fatalf("restart calls=%d", calls)
				}
			} else if calls != 0 {
				t.Fatalf("rejected request invoked restart %d times", calls)
			}
			if runningResult != before {
				t.Fatalf("restart changed restore result: %+v, want %+v", runningResult, before)
			}
		})
	}
}

func TestRestoreRestartRetriesPreparationAndDeduplicatesConcurrentRequests(t *testing.T) {
	setupBackupTest(t)
	restoreReceipt = "current-restore-receipt"
	runningResult = BackupOrRestoreResult{Type: "restore", Status: models.BackupStatusFailed, ErrorMsg: "恢复失败", RestartRequired: true}
	before := *GetRunningResult()
	preparationError := errors.New("restart process could not start")
	if err := restartWithReceipt(restoreReceipt, true, func() error { return preparationError }); !errors.Is(err, preparationError) {
		t.Fatalf("preparation error=%v", err)
	}
	result, ok := RestoreResultWithReceipt(restoreReceipt)
	if !ok || *result != before {
		t.Fatalf("preparation failure consumed receipt or changed result: %+v", result)
	}

	var calls atomic.Int32
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			if err := restartWithReceipt(restoreReceipt, true, func() error { calls.Add(1); return nil }); err != nil {
				t.Errorf("restart request failed: %v", err)
			}
		})
	}
	workers.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent requests triggered %d restarts", calls.Load())
	}
	before.RestartRequested = true
	result, ok = RestoreResultWithReceipt(restoreReceipt)
	if !ok || *result != before || *GetRunningResult() != before {
		t.Fatalf("status reads changed restore result: %+v", result)
	}
	if err := restartWithReceipt("wrong-receipt", true, func() error { t.Error("repeated invalid receipt invoked restart"); return nil }); !errors.Is(err, ErrRestoreReceipt) {
		t.Fatalf("already accepted restart bypassed receipt check: %v", err)
	}
}
