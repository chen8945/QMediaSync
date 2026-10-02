package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestGenerationOrphanedSyncPath(t *testing.T) {
	for _, kind := range []string{"pending", "finalizing_due", "finalizing_backoff", "skipped_finalizing", "more_than_page"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			testGenerationOrphanedSyncPath(t, account, sp, kind)
		})
	}
}

func testGenerationOrphanedSyncPath(t *testing.T, account *models.Account, sp *models.SyncPath, kind string) {
	t.Helper()
	if err := db.Db.AutoMigrate(&models.DirectoryUploadRule{}, &models.DirectoryUploadProcessedFile{}, &models.EmbyMediaSyncFile{}, &models.EmbyLibrarySyncPath{}); err != nil {
		t.Fatal(err)
	}
	count, accepted, skipped := 1, 1, 0
	if kind == "pending" {
		accepted = 0
	}
	if kind == "skipped_finalizing" {
		accepted, skipped = 0, 1
	}
	if kind == "more_than_page" {
		count = 258
	}
	parent := &models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeBatchFiles, SyncPathId: sp.ID,
		Status: models.StrmGenerationStatusWaitingChildren, TotalItems: count, AcceptedItems: accepted, SkippedItems: skipped,
		ChangedItems: accepted, NewMetaItems: accepted, RefreshEmby: true}
	if count == accepted+skipped {
		parent.Status = models.StrmGenerationStatusCompleted
	}
	if err := db.Db.Create(parent).Error; err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "source.mkv")
	generated := filepath.Join(sp.LocalPath, "existing.strm")
	for _, path := range []string{source, generated} {
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	upload := &models.DbUploadTask{Status: models.UploadStatusCompleted, Source: models.UploadSourceDirectoryMonitor,
		LocalFullPath: source, SourceCleanupStatus: models.UploadSourceCleanupStatusPending}
	if err := db.Db.Create(upload).Error; err != nil {
		t.Fatal(err)
	}
	for i := range count {
		task := retryFile(t, sp, fmt.Sprintf("orphan-%d", i))
		updates := map[string]any{"parent_task_id": parent.ID, "upload_task_id": upload.ID}
		if i == 0 && kind != "pending" {
			updates["status"] = models.StrmGenerationStatusFinalizing
			updates["retry_count"] = 1
			updates["last_retry_time"] = time.Now().Unix() + 3600
			if kind == "finalizing_due" {
				updates["last_retry_time"] = time.Now().Unix() - 10
			}
			if skipped > 0 {
				updates["skip_reason"], updates["skipped_items"] = "excluded", 1
			}
		}
		if err := db.Db.Model(task).Updates(updates).Error; err != nil {
			t.Fatal(err)
		}
	}
	historical := retryFile(t, sp, "historical")
	if err := db.Db.Model(historical).Update("status", models.StrmGenerationStatusCompleted).Error; err != nil {
		t.Fatal(err)
	}
	other := *sp
	other.ID, other.BaseCid, other.LocalPath = 0, "healthy-root", t.TempDir()
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	healthy := retryFile(t, &other, "healthy")
	if err := models.DeleteSyncPathByID(t.Context(), sp.ID); err != nil {
		t.Fatal(err)
	}
	writes, attempts, fail, cleanups := 0, 0, false, 0
	oldCleanup := cleanupSourceAfterStrmSuccess
	cleanupSourceAfterStrmSuccess = func(uint) error { cleanups++; return nil }
	t.Cleanup(func() { cleanupSourceAfterStrmSuccess = oldCleanup })
	service := retryService(t, &other, account, &writes, &attempts, &fail)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 2); n != 1 || err != nil {
		t.Fatalf("healthy task: n=%d err=%v", n, err)
	}
	// 重建 service 不复用内存退休信息，父计数和终态仍须保持。
	service = retryService(t, &other, account, &writes, &attempts, &fail)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 2); n != 0 || err != nil {
		t.Fatalf("restart: n=%d err=%v", n, err)
	}
	for _, task := range []*models.StrmGenerationTask{parent, healthy, historical} {
		if err := db.Db.First(task, task.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	var children []models.StrmGenerationTask
	if err := db.Db.Where("parent_task_id = ?", parent.ID).Find(&children).Error; err != nil {
		t.Fatal(err)
	}
	if len(children) != count {
		t.Fatalf("children=%d want=%d", len(children), count)
	}
	for _, child := range children {
		if child.Status != models.StrmGenerationStatusFailed || !strings.Contains(child.LastError, "同步目录已删除") {
			t.Fatalf("orphan did not retire: %+v", child)
		}
	}
	if parent.Status != models.StrmGenerationStatusFailed || parent.AcceptedItems != accepted || parent.SkippedItems != skipped ||
		parent.FailedItems != count-accepted-skipped || parent.TotalItems != count || parent.ChangedItems != accepted || parent.NewMetaItems != accepted ||
		parent.RefreshSubmitted || !strings.Contains(parent.LastError, "同步目录已删除") {
		t.Fatalf("parent result changed: %+v", parent)
	}
	if healthy.Status != models.StrmGenerationStatusCompleted || historical.Status != models.StrmGenerationStatusCompleted || writes != 1 || attempts != 0 || cleanups != 0 {
		t.Fatalf("healthy=%s historical=%s writes=%d refresh=%d cleanup=%d", healthy.Status, historical.Status, writes, attempts, cleanups)
	}
	for _, path := range []string{source, generated} {
		if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
			t.Fatalf("protected file %s changed: %q err=%v", path, data, err)
		}
	}
}

func TestGenerationMissingUpload(t *testing.T) {
	for _, kind := range []string{"pending_video", "pending_metadata", "finalizing_due", "finalizing_backoff"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			testGenerationMissingUpload(t, account, sp, kind)
		})
	}
}

func testGenerationMissingUpload(t *testing.T, account *models.Account, sp *models.SyncPath, kind string) {
	t.Helper()
	blocker := retryFile(t, sp, "blocker")
	retryParent(t, blocker)
	if err := db.Db.Model(blocker).Updates(map[string]any{"retry_count": 1, "last_retry_time": time.Now().Unix() + 3600}).Error; err != nil {
		t.Fatal(err)
	}
	task := retryFile(t, sp, "missing-upload")
	if strings.HasPrefix(kind, "finalizing") {
		retryParent(t, task)
		when := time.Now().Unix() - 10
		if kind == "finalizing_backoff" {
			when = time.Now().Unix() + 3600
		}
		if err := db.Db.Model(task).Updates(map[string]any{"retry_count": 1, "last_retry_time": when}).Error; err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(t.TempDir(), "source.nfo")
	target := filepath.Join(sp.LocalPath, sp.RemotePath, "missing-upload.nfo")
	for _, path := range []string{source, target} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("protected metadata"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	upload := &models.DbUploadTask{Status: models.UploadStatusCompleted, Source: models.UploadSourceDirectoryMonitor, LocalFullPath: source}
	if err := db.Db.Create(upload).Error; err != nil {
		t.Fatal(err)
	}
	updates := map[string]any{"upload_task_id": upload.ID}
	if kind == "pending_metadata" {
		updates["file_name"] = "missing-upload.nfo"
		updates["source"] = models.StrmGenerationSourceUploadCompleted
	}
	if err := db.Db.Model(task).Updates(updates).Error; err != nil {
		t.Fatal(err)
	}
	if err := models.ClearUploadSuccessAndFailed(); err != nil {
		t.Fatal(err)
	}
	healthy := retryFile(t, sp, "healthy")
	writes, attempts, fail := 0, 0, false
	service := retryService(t, sp, account, &writes, &attempts, &fail)
	n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5)
	wantCount := 2
	if kind == "finalizing_backoff" {
		wantCount = 1
	}
	if n != wantCount || err != nil {
		t.Fatalf("processing n=%d err=%v", n, err)
	}
	for _, row := range []*models.StrmGenerationTask{task, healthy} {
		if err := db.Db.First(row, row.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	wantStatus := models.StrmGenerationStatusCompleted
	if kind == "pending_metadata" {
		wantStatus = models.StrmGenerationStatusFailed
		if !strings.Contains(task.LastError, "读取上传任务失败") {
			t.Fatalf("missing metadata source error=%q", task.LastError)
		}
	}
	if kind == "finalizing_backoff" {
		if task.Status != models.StrmGenerationStatusFinalizing {
			t.Fatalf("backoff task changed: %+v", task)
		}
		retryDue(t, task)
		if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 1 || err != nil {
			t.Fatalf("due missing-upload retry: n=%d err=%v", n, err)
		}
		if err := db.Db.First(task, task.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	wantWrites := 1
	if kind == "pending_video" {
		wantWrites++
	}
	if task.Status != wantStatus || healthy.Status != models.StrmGenerationStatusCompleted || writes != wantWrites || task.UploadTaskId != upload.ID {
		t.Fatalf("task=%+v healthy=%s writes=%d", task, healthy.Status, writes)
	}
	if strings.HasPrefix(kind, "finalizing") {
		var parent models.StrmGenerationTask
		if err := db.Db.First(&parent, task.ParentTaskId).Error; err != nil {
			t.Fatal(err)
		}
		if parent.AcceptedItems != 1 || parent.FailedItems != 0 || attempts != 1 || !parent.RefreshSubmitted {
			t.Fatalf("finalizing repeated progress: parent=%+v refresh=%d", parent, attempts)
		}
	}
	for _, path := range []string{source, target} {
		if data, err := os.ReadFile(path); err != nil || string(data) != "protected metadata" {
			t.Fatalf("protected metadata changed: %q err=%v", data, err)
		}
	}
}

func TestGenerationOrphanRetirementRollback(t *testing.T) {
	for _, kind := range []string{"pending", "finalizing"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			testGenerationOrphanRetirementRollback(t, account, sp, kind)
		})
	}
}

func testGenerationOrphanRetirementRollback(t *testing.T, account *models.Account, sp *models.SyncPath, kind string) {
	t.Helper()
	child := retryFile(t, sp, "orphan")
	parent := &models.StrmGenerationTask{SyncPathId: sp.ID, TaskType: models.StrmGenerationTaskTypeBatchFiles, Status: models.StrmGenerationStatusWaitingChildren, TotalItems: 1}
	status := models.StrmGenerationStatusPending
	if kind == "finalizing" {
		status = models.StrmGenerationStatusFinalizing
		parent.Status, parent.AcceptedItems = models.StrmGenerationStatusCompleted, 1
	}
	if err := db.Db.Create(parent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(child).Updates(map[string]any{"parent_task_id": parent.ID, "status": status, "retry_count": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Delete(sp).Error; err != nil {
		t.Fatal(err)
	}
	fault := errors.New("parent retirement write failed")
	retireUpdates := 0
	if err := db.Db.Callback().Update().Before("gorm:update").Register("orphan_parent_fault", func(tx *gorm.DB) {
		if values, ok := tx.Statement.Dest.(map[string]any); ok && values["status"] == models.StrmGenerationStatusFailed {
			retireUpdates++
			if retireUpdates == 2 {
				tx.AddError(fault)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	writes, attempts, fail := 0, 0, false
	service := retryService(t, sp, account, &writes, &attempts, &fail)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 0 || !errors.Is(err, fault) {
		t.Fatalf("retirement failure n=%d err=%v", n, err)
	}
	wantParent := parent.Status
	for _, row := range []*models.StrmGenerationTask{parent, child} {
		if err := db.Db.First(row, row.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if child.Status != status || parent.Status != wantParent || parent.FailedItems != 0 || writes != 0 {
		t.Fatalf("partial retirement: child=%+v parent=%+v writes=%d", child, parent, writes)
	}
	if err := db.Db.Callback().Update().Remove("orphan_parent_fault"); err != nil {
		t.Fatal(err)
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 0 || err != nil {
		t.Fatalf("retirement recovery n=%d err=%v", n, err)
	}
	for _, row := range []*models.StrmGenerationTask{parent, child} {
		if err := db.Db.First(row, row.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	wantAccepted := 0
	if kind == "finalizing" {
		wantAccepted = 1
	}
	if child.Status != models.StrmGenerationStatusFailed || parent.Status != models.StrmGenerationStatusFailed || parent.AcceptedItems != wantAccepted || parent.FailedItems != 1-wantAccepted {
		t.Fatalf("recovery changed counters: child=%+v parent=%+v", child, parent)
	}
}

func TestGenerationOrphanDatabaseErrors(t *testing.T) {
	for _, kind := range []string{"sync_path_table", "upload_table", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			blocker := retryFile(t, sp, "blocker")
			retryParent(t, blocker)
			if err := db.Db.Model(blocker).Updates(map[string]any{"retry_count": 1, "last_retry_time": time.Now().Unix() + 3600}).Error; err != nil {
				t.Fatal(err)
			}
			task := retryFile(t, sp, "healthy")
			if err := db.Db.Model(task).Update("upload_task_id", 123).Error; err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch kind {
			case "sync_path_table":
				if err := db.Db.Migrator().DropTable(&models.SyncPath{}); err != nil {
					t.Fatal(err)
				}
			case "upload_table":
				if err := db.Db.Migrator().DropTable(&models.DbUploadTask{}); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				cancel()
			}
			writes, attempts, fail := 0, 0, false
			if n, err := ProcessPendingStrmGenerationTasks(ctx, retryService(t, sp, account, &writes, &attempts, &fail), 5); n != 0 || err == nil {
				t.Fatalf("database error swallowed: n=%d err=%v", n, err)
			}
			if err := db.Db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if task.Status != models.StrmGenerationStatusPending || writes != 0 || attempts != 0 {
				t.Fatalf("database error misclassified: task=%+v writes=%d refresh=%d", task, writes, attempts)
			}
		})
	}
}

func TestGenerationOrphanDeletionDuringExecution(t *testing.T) {
	for _, kind := range []string{"after_claim", "finalizing_retry", "dependency_recheck"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			if err := db.Db.AutoMigrate(&models.DirectoryUploadRule{}, &models.DirectoryUploadProcessedFile{}, &models.EmbyMediaSyncFile{}); err != nil {
				t.Fatal(err)
			}
			task := retryFile(t, sp, "first")
			parent := &models.StrmGenerationTask{SyncPathId: sp.ID, TaskType: models.StrmGenerationTaskTypeBatchFiles,
				Status: models.StrmGenerationStatusWaitingChildren, TotalItems: 1}
			if err := db.Db.Create(parent).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(task).Update("parent_task_id", parent.ID).Error; err != nil {
				t.Fatal(err)
			}
			if kind == "finalizing_retry" || kind == "dependency_recheck" {
				if err := db.Db.Model(task).Updates(map[string]any{"status": models.StrmGenerationStatusFinalizing, "retry_count": 1}).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Model(parent).Updates(map[string]any{"status": models.StrmGenerationStatusCompleted, "accepted_items": 1}).Error; err != nil {
					t.Fatal(err)
				}
			}
			other := *sp
			other.ID, other.BaseCid, other.LocalPath, other.RemotePath = 0, "independent-root", t.TempDir(), "/independent"
			if err := db.Db.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			healthy := retryFile(t, &other, "healthy")
			if err := db.Db.Model(healthy).Update("path", other.RemotePath).Error; err != nil {
				t.Fatal(err)
			}
			triggered := false
			deletePath := func(tx *gorm.DB) {
				triggered = true
				if err := models.DeleteSyncPathByID(t.Context(), sp.ID); err != nil {
					tx.AddError(err)
				}
			}
			if kind == "finalizing_retry" {
				if err := db.Db.Callback().Query().After("gorm:query").Register("delete_before_retry", func(tx *gorm.DB) {
					if !triggered && tx.Statement.Table == "sync_paths" {
						deletePath(tx)
					}
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				triggerTask := task.ID
				triggerStatus := models.StrmGenerationStatusRunning
				if kind == "dependency_recheck" {
					triggerTask = healthy.ID
					if err := db.Db.Model(task).Update("last_retry_time", time.Now().Unix()+3600).Error; err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("delete_after_claim", func(tx *gorm.DB) {
					if triggered || tx.Statement.Table != "strm_generation_tasks" {
						return
					}
					values, ok := tx.Statement.Dest.(map[string]any)
					if !ok || values["status"] != triggerStatus {
						return
					}
					if triggerStatus == models.StrmGenerationStatusRunning {
						row, ok := tx.Statement.Model.(*models.StrmGenerationTask)
						if !ok || row.ID != triggerTask {
							return
						}
					}
					deletePath(tx)
				}); err != nil {
					t.Fatal(err)
				}
			}
			writes, attempts, fail := 0, 0, false
			service := retryService(t, sp, account, &writes, &attempts, &fail)
			if kind == "dependency_recheck" {
				service = retryService(t, &other, account, &writes, &attempts, &fail)
			}
			if _, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); err != nil {
				t.Fatal(err)
			}
			if !triggered {
				t.Fatal("deletion race did not execute")
			}
			for _, row := range []*models.StrmGenerationTask{task, parent, healthy} {
				if err := db.Db.First(row, row.ID).Error; err != nil {
					t.Fatal(err)
				}
			}
			accepted, failed := 1, 0
			if kind == "after_claim" {
				accepted, failed = 0, 1
			}
			if task.Status != models.StrmGenerationStatusFailed || parent.Status != models.StrmGenerationStatusFailed || parent.AcceptedItems != accepted || parent.FailedItems != failed {
				t.Fatalf("race left stale state: task=%+v parent=%+v", task, parent)
			}
			if healthy.Status != models.StrmGenerationStatusPending {
				t.Fatalf("stale batch continued after retirement: %+v", healthy)
			}
			service = retryService(t, &other, account, &writes, &attempts, &fail)
			if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 1 || err != nil {
				t.Fatalf("healthy next batch: n=%d err=%v", n, err)
			}
		})
	}
}

func TestGenerationOrphanDeletionBetweenGenerationAndFinalization(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		account, sp := setupStrmGenerationServiceTestDB(t)
		sqlDB, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		defer sqlDB.Close()
		sqlDB.SetMaxOpenConns(1)
		if err := db.Db.AutoMigrate(&models.DirectoryUploadRule{}, &models.DirectoryUploadProcessedFile{}, &models.EmbyMediaSyncFile{}); err != nil {
			t.Fatal(err)
		}
		task := retryFile(t, sp, "generated")
		parent := &models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeBatchFiles, SyncPathId: sp.ID,
			Status: models.StrmGenerationStatusWaitingChildren, TotalItems: 1, RefreshEmby: true}
		if err := db.Db.Create(parent).Error; err != nil {
			t.Fatal(err)
		}
		source := filepath.Join(t.TempDir(), "source.mkv")
		if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
			t.Fatal(err)
		}
		upload := &models.DbUploadTask{Status: models.UploadStatusCompleted, Source: models.UploadSourceDirectoryMonitor, LocalFullPath: source}
		if err := db.Db.Create(upload).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Db.Model(task).Updates(map[string]any{"parent_task_id": parent.ID, "upload_task_id": upload.ID, "refresh_emby": true}).Error; err != nil {
			t.Fatal(err)
		}
		generatedPath := filepath.Join(sp.LocalPath, sp.RemotePath, "generated.strm")
		generated, proceed := make(chan struct{}), make(chan struct{})
		writes, attempts, fail, cleanups := 0, 0, false, 0
		service := retryService(t, sp, account, &writes, &attempts, &fail)
		service.processStrmFile = func(syncer *SyncStrm, file *SyncFileCache) error {
			if err := os.MkdirAll(filepath.Dir(generatedPath), 0700); err != nil {
				return err
			}
			if err := os.WriteFile(file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath), []byte("generated"), 0600); err != nil {
				return err
			}
			close(generated)
			<-proceed
			return nil
		}
		service.resolveRefreshTarget = func(*models.SyncFile) (models.EmbyRefreshTarget, error) {
			return models.EmbyRefreshTarget{TargetType: models.EmbyRefreshTargetTypeItem, ItemID: "item"}, nil
		}
		oldCleanup := cleanupSourceAfterStrmSuccess
		cleanupSourceAfterStrmSuccess = func(uint) error { cleanups++; return nil }
		defer func() { cleanupSourceAfterStrmSuccess = oldCleanup }()
		done := make(chan error, 1)
		go func() {
			_, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5)
			done <- err
		}()
		select {
		case <-generated:
		case err := <-done:
			t.Fatalf("generation ended before write: %v", err)
		}
		deleted := make(chan error, 1)
		go func() { deleted <- models.DeleteSyncPathByID(t.Context(), sp.ID) }()
		synctest.Wait()
		select {
		case err := <-deleted:
			t.Fatalf("deleted during generation: %v", err)
		default:
		}
		// 删除已排在当前生成之后，收尾重新获权时必须看到目录已删除。
		close(proceed)
		if err := <-deleted; err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		for _, row := range []*models.StrmGenerationTask{task, parent} {
			if err := db.Db.First(row, row.ID).Error; err != nil {
				t.Fatal(err)
			}
		}
		if task.Status != models.StrmGenerationStatusFailed || parent.Status != models.StrmGenerationStatusFailed || parent.AcceptedItems != 1 || parent.FailedItems != 0 || parent.RefreshSubmitted || attempts != 0 || cleanups != 0 {
			t.Fatalf("unsafe finalization: task=%+v parent=%+v refresh=%d cleanup=%d", task, parent, attempts, cleanups)
		}
		for path, want := range map[string]string{source: "source", generatedPath: "generated"} {
			if data, err := os.ReadFile(path); err != nil || string(data) != want {
				t.Fatalf("file not preserved: path=%s data=%q err=%v", path, data, err)
			}
		}
	})
}
