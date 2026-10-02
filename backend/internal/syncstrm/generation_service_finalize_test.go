package syncstrm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"gorm.io/gorm"
)

func standaloneFinalizationFixture(t *testing.T, account *models.Account, sp *models.SyncPath) (*models.StrmGenerationTask, *StrmGenerationService, string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "movie.mkv")
	if err := os.WriteFile(source, []byte("keep upload source"), 0o600); err != nil {
		t.Fatal(err)
	}
	upload := &models.DbUploadTask{Source: models.UploadSourceDirectoryMonitor, LocalFullPath: source}
	if err := db.Db.Create(upload).Error; err != nil {
		t.Fatal(err)
	}
	task, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source: models.StrmGenerationSourceUploadCompleted, TaskType: models.StrmGenerationTaskTypeFile,
		SyncPathId: sp.ID, AccountId: account.ID, UploadTaskId: upload.ID,
		FileId: "finalize-file", PickCode: "finalize-pick", ParentId: "root",
		Path: "/remote", FileName: "movie.mkv", FileSize: 1024, Mtime: 1, Sha1: "sha",
		RequestHash: "standalone-finalization",
	})
	if err != nil {
		t.Fatal(err)
	}
	service := newTestGenerationService(t, sp, account)
	build := service.buildSyncer
	service.buildSyncer = func(path *models.SyncPath, a *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
		syncer, err := build(path, a, config)
		if err == nil {
			syncer.Sync = &models.Sync{Logger: helpers.AppLogger}
			syncer.SyncDriver = &fakeDirectoryScanDriver{strmContent: "http://qms.local/finalize-file"}
		}
		return syncer, err
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
	oldCleanup := cleanupSourceAfterStrmSuccess
	cleanupSourceAfterStrmSuccess = func(uint) error { return os.Remove(source) }
	t.Cleanup(func() { cleanupSourceAfterStrmSuccess = oldCleanup })
	return task, service, source
}

func TestStandaloneFinalizationFailure(t *testing.T) {
	for _, stage := range []string{"scope read", "completion save", "failure save"} {
		t.Run(stage, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			testStandaloneFinalizationFailure(t, account, sp, stage)
		})
	}
}

func testStandaloneFinalizationFailure(t *testing.T, account *models.Account, sp *models.SyncPath, stage string) {
	t.Helper()
	task, service, source := standaloneFinalizationFixture(t, account, sp)
	generated := false
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { generated = true; return nil }
	originalErr := errors.New("temporary finalization failure")
	saveErr := errors.New("cannot save finalization failure")
	faults := 0
	if err := db.Db.Callback().Query().Before("gorm:query").Register("finalize:read-failure", func(tx *gorm.DB) {
		if generated && faults == 0 && stage != "completion save" && tx.Statement.Table == "sync_paths" {
			faults++
			tx.AddError(originalErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Update().Before("gorm:update").Register("finalize:save-failure", func(tx *gorm.DB) {
		if tx.Statement.Table != "strm_generation_tasks" {
			return
		}
		row, ok := tx.Statement.Model.(*models.StrmGenerationTask)
		if stage == "completion save" && faults == 0 && ok && row.Status == models.StrmGenerationStatusCompleted {
			faults++
			tx.AddError(originalErr)
		}
		if values, ok := tx.Statement.Dest.(map[string]any); ok && stage == "failure save" && values["status"] == models.StrmGenerationStatusFailed {
			tx.AddError(saveErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5)
	if n != 1 || !errors.Is(err, originalErr) || faults != 1 || !generated {
		t.Fatalf("finalization: n=%d err=%v faults=%d generated=%v", n, err, faults, generated)
	}
	var ledger models.SyncFile
	if err := db.Db.Where("file_id = ?", task.FileId).First(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(ledger.LocalFilePath); err != nil || string(data) != "http://qms.local/finalize-file" {
		t.Fatalf("generated file lost: %q %v", data, err)
	}
	if data, err := os.ReadFile(source); err != nil || string(data) != "keep upload source" {
		t.Fatalf("source removed: %q %v", data, err)
	}
	var saved models.StrmGenerationTask
	if err := db.Db.First(&saved, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stage == "failure save" {
		if !errors.Is(err, saveErr) || saved.Status != models.StrmGenerationStatusRunning || saved.LastError != "" || saved.RetryCount != 0 {
			t.Fatalf("failed write falsely persisted: task=%+v err=%v", saved, err)
		}
		return
	}
	if saved.Status != models.StrmGenerationStatusFailed || saved.LastError != originalErr.Error() || saved.RetryCount != 1 || saved.LastRetryTime == 0 {
		t.Fatalf("failure not recorded: %+v", saved)
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 0 || err != nil {
		t.Fatalf("failed task auto-retried: n=%d err=%v", n, err)
	}
	duplicate := *task
	duplicate.BaseModel = models.BaseModel{}
	fresh, err := models.EnqueueStrmGenerationTask(&duplicate)
	if err != nil || fresh.ID == saved.ID || fresh.Status != models.StrmGenerationStatusPending {
		t.Fatalf("explicit resubmission blocked: task=%+v err=%v", fresh, err)
	}
}

func TestStandaloneFinalizationSuccessKeepsUpdates(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	task, service, source := standaloneFinalizationFixture(t, account, sp)
	updates := 0
	if err := db.Db.Callback().Update().Before("gorm:update").Register("finalize:count-updates", func(tx *gorm.DB) {
		if tx.Statement.Table == "strm_generation_tasks" {
			updates++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 1 || err != nil {
		t.Fatalf("success: n=%d err=%v", n, err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.StrmGenerationStatusCompleted || updates != 2 {
		t.Fatalf("successful task changed protocol: status=%s updates=%d", task.Status, updates)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("successful cleanup missing: %v", err)
	}
}

func TestStandaloneFinalizationCancellation(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	task, service, source := standaloneFinalizationFixture(t, account, sp)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { cancel(); return nil }
	if n, err := ProcessPendingStrmGenerationTasks(ctx, service, 5); n != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: n=%d err=%v", n, err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.StrmGenerationStatusRunning || task.LastError != "" || task.RetryCount != 0 {
		t.Fatalf("cancellation changed recovery: %+v", task)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("cancelled source removed: %v", err)
	}
}

func TestStandaloneFinalizationFailurePreservesChangedState(t *testing.T) {
	for _, status := range []models.StrmGenerationStatus{
		models.StrmGenerationStatusCompleted, models.StrmGenerationStatusSkipped,
		models.StrmGenerationStatusFailed, models.StrmGenerationStatusFinalizing, "deleted",
	} {
		t.Run(string(status), func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			testStandaloneFinalizationChangedState(t, account, sp, status)
		})
	}
}

func testStandaloneFinalizationChangedState(t *testing.T, account *models.Account, sp *models.SyncPath, status models.StrmGenerationStatus) {
	t.Helper()
	task, service, source := standaloneFinalizationFixture(t, account, sp)
	generated := false
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { generated = true; return nil }
	originalErr := errors.New("scope unavailable after task state changed")
	if err := db.Db.Callback().Query().Before("gorm:query").Register("finalize:changed-state", func(tx *gorm.DB) {
		if !generated || tx.Statement.Table != "sync_paths" {
			return
		}
		generated = false
		write := tx.Session(&gorm.Session{NewDB: true})
		if status == "deleted" {
			tx.AddError(write.Delete(&models.StrmGenerationTask{}, task.ID).Error)
		} else {
			tx.AddError(write.Model(&models.StrmGenerationTask{}).Where("id = ?", task.ID).
				Updates(map[string]any{"status": status, "last_error": "preserve newer state", "retry_count": 7}).Error)
		}
		tx.AddError(originalErr)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); !errors.Is(err, originalErr) {
		t.Fatalf("missing original error: %v", err)
	}
	var saved models.StrmGenerationTask
	err := db.Db.First(&saved, task.ID).Error
	if status == "deleted" {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			t.Fatalf("deleted task recreated: %+v %v", saved, err)
		}
	} else if err != nil || saved.Status != status || saved.LastError != "preserve newer state" || saved.RetryCount != 7 {
		t.Fatalf("newer state overwritten: %+v %v", saved, err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source removed: %v", err)
	}
}
