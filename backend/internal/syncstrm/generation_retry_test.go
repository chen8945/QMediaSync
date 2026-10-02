package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func retryFile(t *testing.T, sp *models.SyncPath, name string) *models.StrmGenerationTask {
	t.Helper()
	task, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile,
		SyncPathId: sp.ID, AccountId: sp.AccountId, FileId: name, PickCode: name,
		ParentId: "root", Path: "/remote", FileName: name + ".mkv", FileSize: 1, Mtime: 1, Sha1: "sha",
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func retryParent(t *testing.T, child *models.StrmGenerationTask) {
	t.Helper()
	parent := &models.StrmGenerationTask{TaskType: models.StrmGenerationTaskTypeBatchFiles,
		SyncPathId: child.SyncPathId, AccountId: child.AccountId,
		Status: models.StrmGenerationStatusCompleted, TotalItems: 1, AcceptedItems: 1, ChangedItems: 1, RefreshEmby: true,
		RefreshTargetsStr: `[{"target_type":"item","item_id":"item"}]`,
	}
	if err := db.Db.Create(parent).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(child).Updates(map[string]any{"parent_task_id": parent.ID, "status": models.StrmGenerationStatusFinalizing}).Error; err != nil {
		t.Fatal(err)
	}
}

func retryDue(t *testing.T, task *models.StrmGenerationTask) {
	t.Helper()
	if err := db.Db.Model(task).Update("last_retry_time", time.Now().Unix()-5).Error; err != nil {
		t.Fatal(err)
	}
}

func TestGenerationFinalizingScopeFailureRetry(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	task := retryFile(t, sp, "scope-retry")
	retryParent(t, task)
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(sp.LocalPath, "scope-retry.strm")
	if err := os.WriteFile(generated, []byte("generated"), 0600); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(t.TempDir(), "broken-target")
	if err := os.Symlink(broken, broken); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(sp).Update("local_path", broken).Error; err != nil {
		t.Fatal(err)
	}
	writes, attempts, fail := 0, 0, false
	service := retryService(t, sp, account, &writes, &attempts, &fail)
	n, scopeErr := ProcessPendingStrmGenerationTasks(t.Context(), service, 5)
	if n != 1 || scopeErr == nil {
		t.Fatalf("scope failure: n=%d err=%v", n, scopeErr)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != models.StrmGenerationStatusFinalizing || task.RetryCount != 1 || task.LastRetryTime == 0 || task.LastError != scopeErr.Error() {
		t.Fatalf("scope failure not saved for retry: %+v", task)
	}
	var parent models.StrmGenerationTask
	if err := db.Db.First(&parent, task.ParentTaskId).Error; err != nil {
		t.Fatal(err)
	}
	if parent.AcceptedItems != 1 || parent.FailedItems != 0 || parent.ChangedItems != 1 || parent.RefreshSubmitted || writes != 0 || attempts != 0 {
		t.Fatalf("scope failure changed generation result: parent=%+v writes=%d refresh=%d", parent, writes, attempts)
	}
	if err := db.Db.Model(sp).Update("local_path", filepath.Dir(generated)).Error; err != nil {
		t.Fatal(err)
	}
	retryDue(t, task)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 1 || err != nil {
		t.Fatalf("repaired scope: n=%d err=%v", n, err)
	}
	for _, row := range []*models.StrmGenerationTask{task, &parent} {
		if err := db.Db.First(row, row.ID).Error; err != nil {
			t.Fatal(err)
		}
	}
	if task.Status != models.StrmGenerationStatusCompleted || task.RetryCount != 1 || task.LastError != "" ||
		parent.AcceptedItems != 1 || parent.FailedItems != 0 || parent.ChangedItems != 1 || !parent.RefreshSubmitted || writes != 0 || attempts != 1 {
		t.Fatalf("scope retry repeated generation: task=%+v parent=%+v writes=%d refresh=%d", task, parent, writes, attempts)
	}
	if data, err := os.ReadFile(generated); err != nil || string(data) != "generated" {
		t.Fatalf("generated file changed: data=%q err=%v", data, err)
	}
}

func retryService(t *testing.T, sp *models.SyncPath, account *models.Account, writes *int, attempts *int, fail *bool) *StrmGenerationService {
	service := newTestGenerationService(t, sp, account)
	service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 0 }
	service.processStrmFile = func(*SyncStrm, *SyncFileCache) error { *writes++; return nil }
	service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error {
		*attempts++
		if *fail {
			return errors.New("refresh unavailable")
		}
		return nil
	}
	return service
}

func TestGenerationRetryFairness(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	testGenerationRetryFairness(t, account, sp)
}

func testGenerationRetryFairness(t *testing.T, account *models.Account, sp *models.SyncPath) {
	first := retryFile(t, sp, "first")
	retryParent(t, first)
	upload := &models.DbUploadTask{}
	if err := db.Db.Create(upload).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(first).Update("upload_task_id", upload.ID).Error; err != nil {
		t.Fatal(err)
	}
	cleanups := 0
	oldCleanup := cleanupSourceAfterStrmSuccess
	cleanupSourceAfterStrmSuccess = func(uint) error { cleanups++; return nil }
	t.Cleanup(func() { cleanupSourceAfterStrmSuccess = oldCleanup })

	second := retryFile(t, sp, "independent")
	writes, attempts, fail := 0, 0, true
	service := retryService(t, sp, account, &writes, &attempts, &fail)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 1 || err == nil {
		t.Fatalf("first n=%d err=%v", n, err)
	}
	// 重建 service 保留数据库：未到期的失败任务不能挡住同目录独立文件。
	service = retryService(t, sp, account, &writes, &attempts, &fail)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 1 || err != nil {
		t.Fatalf("independent n=%d err=%v", n, err)
	}
	if err := db.Db.First(second, second.ID).Error; err != nil {
		t.Fatal(err)
	}
	if second.Status != models.StrmGenerationStatusCompleted || writes != 1 || attempts != 1 {
		t.Fatalf("writes=%d attempts=%d second=%s", writes, attempts, second.Status)
	}
	// 不断新增任务也不能挤掉已到期重试。
	for i := 0; i < 7; i++ {
		retryFile(t, sp, fmt.Sprintf("new-%d", i))
	}
	retryDue(t, first)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); n != 1 || err == nil || attempts != 2 {
		t.Fatalf("due n=%d err=%v attempts=%d", n, err, attempts)
	}
	fail = false
	retryDue(t, first)
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); n != 1 || err != nil {
		t.Fatalf("recover n=%d err=%v", n, err)
	}
	if err := db.Db.First(first, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	var parent models.StrmGenerationTask
	if err := db.Db.First(&parent, first.ParentTaskId).Error; err != nil {
		t.Fatal(err)
	}
	if cleanups != 1 || writes != 1 || parent.AcceptedItems != 1 || !parent.RefreshSubmitted || first.LastError != "" || first.RetryCount != 2 {
		t.Fatalf("writes=%d parent=%+v first=%+v", writes, parent, first)
	}
}

func TestGenerationRetryDependencyPaging(t *testing.T) {
	for _, kind := range []string{"parent", "upload", "local target", "mapped local target", "old local target", "remote path", "remote identity"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			first := retryFile(t, sp, "first")
			retryParent(t, first)
			if kind == "upload" {
				if err := db.Db.Create(&models.DbUploadTask{BaseModel: models.BaseModel{ID: 42}}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Db.Model(first).Updates(map[string]any{"retry_count": 1, "last_retry_time": time.Now().Unix()}).Error; err != nil {
				t.Fatal(err)
			}
			other := *sp
			other.ID = 0
			other.BaseCid = "other"
			if kind == "mapped local target" {
				other.LocalPath = filepath.Join(sp.LocalPath, "remote")
				other.RemotePath = "/"
			}
			if kind == "remote identity" {
				other.LocalPath = t.TempDir()
			}
			if err := db.Db.Create(&other).Error; err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 7; i++ {
				blocked := retryFile(t, &other, fmt.Sprintf("blocked-%d", i))
				changes := map[string]any{}
				switch kind {
				case "parent":
					changes["parent_task_id"] = first.ParentTaskId
				case "upload":
					if err := db.Db.Model(first).Update("upload_task_id", 42).Error; err != nil {
						t.Fatal(err)
					}
					changes["upload_task_id"] = 42
				case "mapped local target":
					changes["file_name"] = "first.mkv"
					changes["path"] = "/"
				case "local target", "remote path":
					changes["file_name"] = "first.mkv"
				case "remote identity":
					changes["file_id"] = first.FileId
					changes["path"] = "/remote/moved"
				case "old local target":
					cache := &SyncFileCache{Path: first.Path, FileName: first.FileName, IsVideo: true, SourceType: sp.SourceType}
					old := &models.SyncFile{SyncPathId: other.ID, AccountId: sp.AccountId, SourceType: sp.SourceType,
						FileId: blocked.FileId, PickCode: blocked.PickCode, Path: "/previous", FileName: "old.mkv", LocalFilePath: cache.GetLocalFilePath(sp.LocalPath, sp.RemotePath)}
					if err := db.Db.Create(old).Error; err != nil {
						t.Fatal(err)
					}
				}
				if len(changes) > 0 {
					if err := db.Db.Model(blocked).Updates(changes).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			independentPath := sp
			retryFile(t, independentPath, "independent")
			writes, attempts, fail := 0, 0, true
			service := retryService(t, independentPath, account, &writes, &attempts, &fail)
			if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 2); n != 1 || err != nil {
				t.Fatalf("n=%d err=%v", n, err)
			}
			if writes != 1 || attempts != 0 {
				t.Fatalf("writes=%d attempts=%d", writes, attempts)
			}
		})
	}
}

func TestGenerationRetryMoreFailuresThanBatch(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	for i := 0; i < 7; i++ {
		task := retryFile(t, sp, fmt.Sprintf("failed-%d", i))
		retryParent(t, task)
	}
	retryFile(t, sp, "independent")
	writes, attempts, fail := 0, 0, true
	service := retryService(t, sp, account, &writes, &attempts, &fail)
	for i := 0; i < 7; i++ {
		if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 2); n != 1 || err == nil {
			t.Fatalf("round %d n=%d err=%v", i, n, err)
		}
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 2); n != 1 || err != nil {
		t.Fatalf("independent n=%d err=%v", n, err)
	}
	if writes != 1 || attempts != 7 {
		t.Fatalf("writes=%d attempts=%d", writes, attempts)
	}
}

func TestGenerationRetryStorageFailureAndCancellation(t *testing.T) {
	for _, kind := range []string{"completion", "retry save", "cancel"} {
		t.Run(kind, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			first := retryFile(t, sp, "first")
			retryParent(t, first)
			retryFile(t, sp, "later")
			writes, attempts, fail := 0, 0, kind != "completion"
			service := retryService(t, sp, account, &writes, &attempts, &fail)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if kind == "cancel" {
				service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error { cancel(); return ctx.Err() }
			}
			if err := db.Db.Callback().Update().Before("gorm:update").Register("retry_fault", func(tx *gorm.DB) {
				task, ok := tx.Statement.Model.(*models.StrmGenerationTask)
				if !ok {
					return
				}
				if kind == "completion" && task.ID == first.ID && task.Status == models.StrmGenerationStatusCompleted {
					tx.AddError(errors.New("completion save failed"))
				}
				if values, ok := tx.Statement.Dest.(map[string]any); ok && kind == "retry save" && values["last_retry_time"] != nil {
					tx.AddError(errors.New("retry save failed"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			if n, err := ProcessPendingStrmGenerationTasks(ctx, service, 5); n != 1 || err == nil {
				t.Fatalf("n=%d err=%v", n, err)
			}
			if err := db.Db.First(first, first.ID).Error; err != nil {
				t.Fatal(err)
			}
			want := 0
			if kind == "completion" {
				want = 1
			}
			if writes != 0 || first.RetryCount != want || first.Status != models.StrmGenerationStatusFinalizing {
				t.Fatalf("writes=%d first=%+v", writes, first)
			}
		})
	}
}

func TestGenerationRetryRechecksTargetAfterClaim(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	first := retryFile(t, sp, "first")
	retryParent(t, first)
	if err := db.Db.Model(first).Updates(map[string]any{"retry_count": 1, "last_retry_time": time.Now().Unix()}).Error; err != nil {
		t.Fatal(err)
	}
	otherAccount := *account
	otherAccount.ID = 0
	otherAccount.UserId = "other"
	otherAccount.Name = "other"
	if err := db.Db.Create(&otherAccount).Error; err != nil {
		t.Fatal(err)
	}
	other := *sp
	other.ID = 0
	other.AccountId = otherAccount.ID
	other.BaseCid = "other"
	other.LocalPath = t.TempDir()
	if err := db.Db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	moved := retryFile(t, &other, "first")
	independent := retryFile(t, sp, "independent")
	changed := false
	// 领取与申请范围之间的屏障：模拟用户在此时将另一个目录改到相同目标。
	if err := db.Db.Callback().Update().After("gorm:update").Register("move_after_claim", func(tx *gorm.DB) {
		task, ok := tx.Statement.Model.(*models.StrmGenerationTask)
		if !ok || task.ID != moved.ID || changed {
			return
		}
		values, ok := tx.Statement.Dest.(map[string]any)
		if !ok || values["status"] != models.StrmGenerationStatusRunning {
			return
		}
		changed = true
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&models.SyncPath{}).Where("id = ?", other.ID).Update("local_path", sp.LocalPath).Error; err != nil {
			tx.AddError(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	writes := 0
	service := NewStrmGenerationService()
	service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) { return true, nil }
	service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 0 }
	service.processStrmFile = func(*SyncStrm, *SyncFileCache) error { writes++; return nil }
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 1 || err != nil {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if err := db.Db.First(moved, moved.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(independent, independent.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !changed || writes != 1 || moved.Status != models.StrmGenerationStatusPending || independent.Status != models.StrmGenerationStatusCompleted {
		t.Fatalf("changed=%v writes=%d moved=%s independent=%s", changed, writes, moved.Status, independent.Status)
	}
	// 下一轮筛选看到新目录位置，不再反复领取冲突项。
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 0 || err != nil {
		t.Fatalf("next n=%d err=%v", n, err)
	}
}

func TestGenerationRetryConfirmedDetailWaits(t *testing.T) {
	for _, fault := range []string{"", "return pending", "read scopes"} {
		t.Run(fault, func(t *testing.T) {
			_, sp := setupStrmGenerationServiceTestDB(t)
			first := retryFile(t, sp, "first")
			retryParent(t, first)
			if err := db.Db.Model(first).Updates(map[string]any{"retry_count": 1, "last_retry_time": time.Now().Unix()}).Error; err != nil {
				t.Fatal(err)
			}
			moved := retryFile(t, sp, "moved")
			independent := retryFile(t, sp, "independent")
			claimed := false
			if err := db.Db.Callback().Update().Before("gorm:update").Register("detail_after_claim", func(tx *gorm.DB) {
				task, ok := tx.Statement.Model.(*models.StrmGenerationTask)
				if !ok {
					return
				}
				values, ok := tx.Statement.Dest.(map[string]any)
				if !ok {
					return
				}
				if task.ID == moved.ID && values["status"] == models.StrmGenerationStatusRunning {
					task.FileSize = 0
					claimed = true
				}
				if fault == "return pending" && values["status"] == models.StrmGenerationStatusPending {
					tx.AddError(errors.New("pending save failed"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			// 只在复查旧文件范围时失败，不影响已完成的领取筛选。
			if err := db.Db.Callback().Query().Before("gorm:query").Register("retry_scope_read", func(tx *gorm.DB) {
				if fault == "read scopes" && claimed && tx.Statement.Table == "sync_files" && slices.Contains(tx.Statement.Selects, "file_name") {
					tx.AddError(errors.New("scope read failed"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			service := NewStrmGenerationService()
			writes, details := 0, 0
			service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
				details++
				return &SyncFileCache{
					FileId: moved.FileId, PickCode: moved.PickCode, ParentId: "root", FileName: "first.mkv", Path: "/remote", FileSize: 1, MTime: 1, Sha1: "sha", SourceType: sp.SourceType,
				}, nil
			}
			service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) { return true, nil }
			service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 0 }
			service.processStrmFile = func(*SyncStrm, *SyncFileCache) error { writes++; return nil }
			n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5)
			if fault == "" {
				if n != 1 || err != nil || writes != 1 {
					t.Fatalf("n=%d err=%v writes=%d", n, err, writes)
				}
			} else if err == nil || writes != 0 {
				t.Fatalf("n=%d err=%v writes=%d", n, err, writes)
			}
			if err := db.Db.First(moved, moved.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.First(independent, independent.ID).Error; err != nil {
				t.Fatal(err)
			}
			want := models.StrmGenerationStatusPending
			if fault == "return pending" {
				want = models.StrmGenerationStatusRunning
			}
			if moved.Status != want || moved.RetryCount != 0 {
				t.Fatalf("moved=%+v", moved)
			}
			if fault == "" {
				if moved.FileName != "first.mkv" || details != 1 {
					t.Fatalf("moved=%+v details=%d", moved, details)
				}
				if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 5); n != 0 || err != nil {
					t.Fatalf("next n=%d err=%v", n, err)
				}
				if details != 1 {
					t.Fatalf("details=%d: confirmed dependency must be filtered next time", details)
				}
			}
		})
	}
}
