package models

import (
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupStrmGenerationTaskTestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&StrmGenerationTask{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
}

func setupConcurrentStrmGenerationTaskTestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "strm-generation.db")), &gorm.Config{
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDb, err := testDb.DB()
	if err != nil {
		t.Fatalf("读取底层测试数据库失败: %v", err)
	}
	sqlDb.SetMaxOpenConns(16)
	db.Db = testDb
	if err := db.Db.AutoMigrate(&StrmGenerationTask{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
}

func TestBuildStrmRequestHashUsesShortStableDigest(t *testing.T) {
	longPath := "/remote/" + strings.Repeat("very-long-path-segment/", 20)
	longFileName := strings.Repeat("movie-", 60) + ".mkv"

	first := BuildStrmRequestHash("webhook:file", "10", longPath, longFileName)
	second := BuildStrmRequestHash("webhook:file", "10", longPath, longFileName)

	if first != second {
		t.Fatalf("相同输入生成的 request_hash 不稳定: %s != %s", first, second)
	}
	if len(first) > 255 {
		t.Fatalf("request_hash 长度 = %d，期望不超过 255: %s", len(first), first)
	}
	if !strings.HasPrefix(first, "webhook:file:v2:") {
		t.Fatalf("request_hash = %s，期望使用 v2 前缀", first)
	}
	if strings.Contains(first, longPath) || strings.Contains(first, longFileName) {
		t.Fatalf("request_hash 不应包含明文长路径或文件名: %s", first)
	}

	ambiguousA := BuildStrmRequestHash("webhook:file", "ab", "c")
	ambiguousB := BuildStrmRequestHash("webhook:file", "a", "bc")
	if ambiguousA == ambiguousB {
		t.Fatalf("长度前缀序列化应区分字段边界: %s", ambiguousA)
	}
}

func TestEnqueueStrmGenerationTaskDedupesRequestHash(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	first, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeFile,
		SyncPathId:  10,
		AccountId:   2,
		FileId:      "file-1",
		RequestHash: "sync:10:file:file-1",
	})
	if err != nil {
		t.Fatalf("首次入队失败: %v", err)
	}

	second, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeFile,
		SyncPathId:  10,
		AccountId:   2,
		FileId:      "file-1",
		RequestHash: "sync:10:file:file-1",
	})
	if err != nil {
		t.Fatalf("重复入队失败: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("重复 request_hash 应返回已有任务，got %d want %d", second.ID, first.ID)
	}

	var count int64
	if err := db.Db.Model(&StrmGenerationTask{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("任务数量 = %d，期望 1", count)
	}
}

func TestEnqueueStrmGenerationTaskConcurrentSameRequestHashReusesExisting(t *testing.T) {
	setupConcurrentStrmGenerationTaskTestDB(t)

	const workers = 8
	requestHash := "webhook:file:concurrent-same-request"
	ready := make(chan struct{}, workers)
	release := make(chan struct{})
	var releaseOnce sync.Once
	callbackName := "qms:test_block_strm_concurrent_create"
	if err := db.Db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		task, ok := tx.Statement.Dest.(*StrmGenerationTask)
		if !ok || task.RequestHash != requestHash {
			return
		}
		ready <- struct{}{}
		<-release
	}); err != nil {
		t.Fatalf("注册测试 callback 失败: %v", err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() {
			close(release)
		})
		_ = db.Db.Callback().Create().Remove(callbackName)
	})

	var wg sync.WaitGroup
	start := make(chan struct{})
	ids := make(chan uint, workers)
	errs := make(chan error, workers)
	for i := range workers {
		wg.Go(func() {
			<-start
			task, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
				Source:      StrmGenerationSourceWebhook,
				TaskType:    StrmGenerationTaskTypeFile,
				SyncPathId:  10,
				AccountId:   2,
				FileId:      "file-concurrent",
				PickCode:    "pick-concurrent",
				FileName:    "movie.mkv",
				RequestHash: requestHash,
			})
			if err != nil {
				errs <- err
				return
			}
			if task == nil {
				errs <- errors.New("入队返回空 STRM 任务")
				return
			}
			if task.RequestHash != requestHash {
				errs <- fmt.Errorf("worker %d 返回 request_hash = %s，期望 %s", i, task.RequestHash, requestHash)
				return
			}
			ids <- task.ID
		})
	}
	close(start)
	for i := range workers {
		select {
		case <-ready:
		case <-time.After(3 * time.Second):
			releaseOnce.Do(func() {
				close(release)
			})
			t.Fatalf("等待第 %d 个并发创建进入 callback 超时", i+1)
		}
	}
	releaseOnce.Do(func() {
		close(release)
	})
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("并发重复入队返回错误: %v", err)
		}
	}
	var firstID uint
	for id := range ids {
		if id == 0 {
			t.Fatal("并发重复入队返回空任务 ID")
		}
		if firstID == 0 {
			firstID = id
			continue
		}
		if id != firstID {
			t.Fatalf("并发重复入队返回不同任务 ID: got %d want %d", id, firstID)
		}
	}

	var count int64
	if err := db.Db.Model(&StrmGenerationTask{}).Where("request_hash = ?", requestHash).Count(&count).Error; err != nil {
		t.Fatalf("统计 request_hash 任务失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("request_hash=%s 的任务数量 = %d，期望 1", requestHash, count)
	}
}

func TestEnqueueStrmGenerationTaskTreatsWaitingChildrenAsActive(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	first, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  10,
		AccountId:   2,
		TotalItems:  2,
		Status:      StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:10:false:false:hash",
	})
	if err != nil {
		t.Fatalf("首次批量父任务入队失败: %v", err)
	}

	second, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  10,
		AccountId:   2,
		TotalItems:  2,
		Status:      StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:10:false:false:hash",
	})
	if err != nil {
		t.Fatalf("重复批量父任务入队失败: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("等待子任务完成的父任务应复用，got %d want %d", second.ID, first.ID)
	}
}

func TestEnqueueStrmGenerationTaskTreatsFinalizingAsActive(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	first, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeFile,
		SyncPathId:  10,
		AccountId:   2,
		FileId:      "file-finalizing",
		Status:      StrmGenerationStatusFinalizing,
		RequestHash: "sync:10:file:file-finalizing",
	})
	if err != nil {
		t.Fatalf("首次收尾中任务入队失败: %v", err)
	}

	second, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeFile,
		SyncPathId:  10,
		AccountId:   2,
		FileId:      "file-finalizing",
		RequestHash: "sync:10:file:file-finalizing",
	})
	if err != nil {
		t.Fatalf("重复收尾中任务入队失败: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("收尾中任务应复用，got %d want %d", second.ID, first.ID)
	}
}

func TestEnqueueStrmGenerationFileArchivesFailedRequestHash(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	first, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeFile,
		SyncPathId:  10,
		AccountId:   2,
		FileId:      "file-1",
		RequestHash: "sync:10:file:file-1",
	})
	if err != nil {
		t.Fatalf("首次单文件任务入队失败: %v", err)
	}
	if err := first.MarkFailed("生成 STRM 失败"); err != nil {
		t.Fatalf("标记单文件任务失败失败: %v", err)
	}

	second, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeFile,
		SyncPathId:  10,
		AccountId:   2,
		FileId:      "file-1",
		RequestHash: "sync:10:file:file-1",
	})
	if err != nil {
		t.Fatalf("失败后单文件任务重新入队失败: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("失败任务再次入队应创建新任务，仍返回 ID %d", second.ID)
	}
	if second.Status != StrmGenerationStatusPending {
		t.Fatalf("新任务状态 = %s，期望 pending", second.Status)
	}

	var archived StrmGenerationTask
	if err := db.Db.First(&archived, first.ID).Error; err != nil {
		t.Fatalf("读取归档任务失败: %v", err)
	}
	if archived.RequestHash == "sync:10:file:file-1" {
		t.Fatalf("旧失败任务 request_hash 未归档: %s", archived.RequestHash)
	}
}

func TestEnqueueStrmGenerationDirectoryScanDedupesOnlyActiveTasks(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	first, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:        StrmGenerationSourceWebhook,
		TaskType:      StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    10,
		AccountId:     2,
		DirectoryPath: "/remote/show",
		RequestHash:   "webhook:directory:10::/remote/show",
	})
	if err != nil {
		t.Fatalf("首次目录扫描入队失败: %v", err)
	}

	activeDuplicate, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:        StrmGenerationSourceWebhook,
		TaskType:      StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    10,
		AccountId:     2,
		DirectoryPath: "/remote/show",
		RequestHash:   "webhook:directory:10::/remote/show",
	})
	if err != nil {
		t.Fatalf("运行中目录扫描重复入队失败: %v", err)
	}
	if activeDuplicate.ID != first.ID {
		t.Fatalf("未完成目录扫描应去重，got %d want %d", activeDuplicate.ID, first.ID)
	}

	first.Status = StrmGenerationStatusCompleted
	if err := db.Db.Save(first).Error; err != nil {
		t.Fatalf("标记目录扫描完成失败: %v", err)
	}

	afterCompleted, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:        StrmGenerationSourceWebhook,
		TaskType:      StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    10,
		AccountId:     2,
		DirectoryPath: "/remote/show",
		RequestHash:   "webhook:directory:10::/remote/show",
	})
	if err != nil {
		t.Fatalf("完成后目录扫描重新入队失败: %v", err)
	}
	if afterCompleted.ID == first.ID {
		t.Fatalf("已完成目录扫描再次请求应创建新任务，仍返回 ID %d", afterCompleted.ID)
	}

	var count int64
	if err := db.Db.Model(&StrmGenerationTask{}).Count(&count).Error; err != nil {
		t.Fatalf("统计任务失败: %v", err)
	}
	if count != 2 {
		t.Fatalf("任务数量 = %d，期望 2", count)
	}
}

func TestStrmGenerationTaskWebhookOptionsDefaultFalse(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	task, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:     StrmGenerationSourceWebhook,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 1,
		FileId:     "file-1",
		PickCode:   "pick-1",
	})
	if err != nil {
		t.Fatalf("创建 STRM 任务失败: %v", err)
	}
	if task.DownloadMeta || task.RefreshEmby {
		t.Fatalf("Webhook 开关默认值 = download_meta:%v refresh_emby:%v，期望均为 false", task.DownloadMeta, task.RefreshEmby)
	}
}

func TestStrmGenerationTaskRefreshTargetsMergeAndLibraryOverride(t *testing.T) {
	task := &StrmGenerationTask{}
	task.MergeRefreshTargets([]EmbyRefreshTarget{
		{
			TargetType:        EmbyRefreshTargetTypeItem,
			ItemID:            "movie-1",
			ItemName:          "电影 1",
			FallbackLibraryId: "lib-movie",
		},
		{
			TargetType:          EmbyRefreshTargetTypeLibrary,
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影库",
		},
		{
			TargetType:        EmbyRefreshTargetTypeItem,
			ItemID:            "movie-2",
			ItemName:          "电影 2",
			FallbackLibraryId: "lib-movie",
		},
		{
			TargetType:        EmbyRefreshTargetTypeItem,
			ItemID:            "season-1",
			ItemName:          "第一季",
			ItemType:          "Season",
			Recursive:         true,
			FallbackLibraryId: "lib-tv",
		},
	})

	got := task.GetRefreshTargets()
	if len(got) != 2 {
		t.Fatalf("刷新目标数量 = %d，期望电影库 + season 两个目标: %+v", len(got), got)
	}
	if got[0].TargetType != EmbyRefreshTargetTypeLibrary || got[0].FallbackLibraryId != "lib-movie" {
		t.Fatalf("第一个目标 = %+v，期望 lib-movie 媒体库目标", got[0])
	}
	if got[1].TargetType != EmbyRefreshTargetTypeItem || got[1].ItemID != "season-1" {
		t.Fatalf("第二个目标 = %+v，期望 season-1 item 目标", got[1])
	}
}

func TestStrmGenerationParentReadyForRefresh(t *testing.T) {
	parent := &StrmGenerationTask{
		TotalItems:    3,
		AcceptedItems: 3,
		FailedItems:   0,
		ChangedItems:  1,
		RefreshEmby:   true,
		Status:        StrmGenerationStatusCompleted,
	}
	if !parent.IsReadyToSubmitRefresh() {
		t.Fatal("全部子任务成功完成且有变化时应可提交刷新")
	}
	parent.RefreshSubmitted = true
	if parent.IsReadyToSubmitRefresh() {
		t.Fatal("已提交刷新后不应再次提交")
	}

	parent.RefreshSubmitted = false
	parent.AcceptedItems = 2
	parent.FailedItems = 1
	if parent.IsReadyToSubmitRefresh() {
		t.Fatal("父任务存在失败子项时不应提交刷新")
	}

	parent.AcceptedItems = 3
	parent.FailedItems = 0
	parent.Status = StrmGenerationStatusFailed
	if parent.IsReadyToSubmitRefresh() {
		t.Fatal("父任务失败时不应提交刷新")
	}
	parent.Status = StrmGenerationStatusWaitingChildren
	if parent.IsReadyToSubmitRefresh() {
		t.Fatal("父任务仍在等待子任务时不应提交刷新")
	}
	parent.Status = StrmGenerationStatusFinalizing
	if parent.IsReadyToSubmitRefresh() {
		t.Fatal("父任务仍在收尾时不应提交刷新")
	}
}

func TestStrmGenerationTaskRetryAndRunningReset(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	task, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:       StrmGenerationSourceUploadCompleted,
		TaskType:     StrmGenerationTaskTypeFile,
		SyncPathId:   10,
		AccountId:    2,
		FileId:       "file-2",
		UploadTaskId: 99,
		RequestHash:  "sync:10:file:file-2",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}
	if task.UploadTaskId != 99 {
		t.Fatalf("upload_task_id = %d，期望 99", task.UploadTaskId)
	}

	if err := task.MarkFailed("生成 STRM 失败"); err != nil {
		t.Fatalf("标记失败失败: %v", err)
	}
	var failed StrmGenerationTask
	if err := db.Db.First(&failed, task.ID).Error; err != nil {
		t.Fatalf("读取失败任务失败: %v", err)
	}
	if failed.Status != StrmGenerationStatusFailed || failed.RetryCount != 1 || failed.LastError != "生成 STRM 失败" {
		t.Fatalf("失败任务 = %+v，期望 failed/retry_count=1/last_error", failed)
	}

	running := &StrmGenerationTask{
		Source:     StrmGenerationSourceWebhook,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 10,
		AccountId:  2,
		FileId:     "file-running",
		Status:     StrmGenerationStatusRunning,
	}
	if err := db.Db.Create(running).Error; err != nil {
		t.Fatalf("创建 running 任务失败: %v", err)
	}
	if err := ResetRunningStrmGenerationTasks(); err != nil {
		t.Fatalf("恢复 running 任务失败: %v", err)
	}
	var reset StrmGenerationTask
	if err := db.Db.First(&reset, running.ID).Error; err != nil {
		t.Fatalf("读取重置任务失败: %v", err)
	}
	if reset.Status != StrmGenerationStatusPending {
		t.Fatalf("running 重置后 status = %s，期望 pending", reset.Status)
	}
}

func TestStrmGenerationTaskRunningAndCompletedTransitions(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	task, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:     StrmGenerationSourceUploadCompleted,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 10,
		AccountId:  2,
		FileId:     "file-transition",
	})
	if err != nil {
		t.Fatalf("入队失败: %v", err)
	}

	if err := task.MarkRunning(); err != nil {
		t.Fatalf("标记 running 失败: %v", err)
	}
	var running StrmGenerationTask
	if err := db.Db.First(&running, task.ID).Error; err != nil {
		t.Fatalf("读取 running 任务失败: %v", err)
	}
	if running.Status != StrmGenerationStatusRunning || running.LastError != "" {
		t.Fatalf("running 任务 = %+v，期望 status=running 且清空 last_error", running)
	}

	if err := running.MarkCompleted(); err != nil {
		t.Fatalf("标记 completed 失败: %v", err)
	}
	var completed StrmGenerationTask
	if err := db.Db.First(&completed, task.ID).Error; err != nil {
		t.Fatalf("读取 completed 任务失败: %v", err)
	}
	if completed.Status != StrmGenerationStatusCompleted || completed.LastError != "" {
		t.Fatalf("completed 任务 = %+v，期望 completed 且无错误", completed)
	}
}

func TestStrmGenerationTaskMarkDirectoryScanExpanded(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	tests := []struct {
		name       string
		totalItems int
		wantStatus StrmGenerationStatus
	}{
		{
			name:       "有子任务时等待子任务汇总",
			totalItems: 2,
			wantStatus: StrmGenerationStatusWaitingChildren,
		},
		{
			name:       "空目录直接完成",
			totalItems: 0,
			wantStatus: StrmGenerationStatusCompleted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
				Source:      StrmGenerationSourceWebhook,
				TaskType:    StrmGenerationTaskTypeDirectoryScan,
				SyncPathId:  10,
				AccountId:   2,
				Status:      StrmGenerationStatusRunning,
				TotalItems:  99,
				RequestHash: "webhook:directory:expanded:" + tt.name,
			})
			if err != nil {
				t.Fatalf("创建目录扫描任务失败: %v", err)
			}

			if err := task.MarkDirectoryScanExpanded(tt.totalItems); err != nil {
				t.Fatalf("标记目录扫描展开完成失败: %v", err)
			}

			var got StrmGenerationTask
			if err := db.Db.First(&got, task.ID).Error; err != nil {
				t.Fatalf("读取目录扫描任务失败: %v", err)
			}
			if got.Status != tt.wantStatus ||
				got.TotalItems != tt.totalItems ||
				got.AcceptedItems != 0 ||
				got.FailedItems != 0 ||
				got.LastError != "" {
				t.Fatalf("目录扫描任务 = %+v，期望 status=%s total_items=%d 且清空统计", got, tt.wantStatus, tt.totalItems)
			}
		})
	}
}

func TestGetPendingStrmGenerationTasksFiltersAndOrders(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	first, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:     StrmGenerationSourceWebhook,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 10,
		AccountId:  2,
		FileId:     "file-pending-1",
	})
	if err != nil {
		t.Fatalf("创建第一个 pending 任务失败: %v", err)
	}
	if _, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:     StrmGenerationSourceWebhook,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 10,
		AccountId:  2,
		FileId:     "file-running",
		Status:     StrmGenerationStatusRunning,
	}); err != nil {
		t.Fatalf("创建 running 任务失败: %v", err)
	}
	if _, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:     StrmGenerationSourceWebhook,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 10,
		AccountId:  2,
		FileId:     "file-failed",
		Status:     StrmGenerationStatusFailed,
	}); err != nil {
		t.Fatalf("创建 failed 任务失败: %v", err)
	}
	finalizing := &StrmGenerationTask{
		Source:     StrmGenerationSourceWebhook,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 10,
		AccountId:  2,
		FileId:     "file-finalizing",
		Status:     StrmGenerationStatusFinalizing,
	}
	if err := db.Db.Create(finalizing).Error; err != nil {
		t.Fatalf("创建 finalizing 任务失败: %v", err)
	}
	second, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:     StrmGenerationSourceWebhook,
		TaskType:   StrmGenerationTaskTypeFile,
		SyncPathId: 10,
		AccountId:  2,
		FileId:     "file-pending-2",
	})
	if err != nil {
		t.Fatalf("创建第二个 pending 任务失败: %v", err)
	}

	tasks, err := GetPendingStrmGenerationTasks(1)
	if err != nil {
		t.Fatalf("查询 pending 任务失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != first.ID {
		t.Fatalf("limit=1 返回 = %+v，期望只返回第一个 pending", tasks)
	}

	tasks, err = GetPendingStrmGenerationTasks(10)
	if err != nil {
		t.Fatalf("查询 pending 任务失败: %v", err)
	}
	if len(tasks) != 3 || tasks[0].ID != first.ID || tasks[1].ID != finalizing.ID || tasks[2].ID != second.ID {
		t.Fatalf("待处理任务 = %+v，期望按 ID 返回 pending/finalizing/pending", tasks)
	}
}

func TestStrmGenerationDirectoryParentChildStats(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	parent, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:        StrmGenerationSourceWebhook,
		TaskType:      StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    10,
		AccountId:     2,
		DirectoryId:   "dir-1",
		DirectoryPath: "/remote/show",
		RequestHash:   "sync:10:dir:dir-1",
	})
	if err != nil {
		t.Fatalf("目录扫描父任务入队失败: %v", err)
	}

	child, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:       StrmGenerationSourceWebhook,
		TaskType:     StrmGenerationTaskTypeFile,
		ParentTaskId: parent.ID,
		SyncPathId:   10,
		AccountId:    2,
		FileId:       "file-child",
		RequestHash:  "sync:10:file:file-child",
	})
	if err != nil {
		t.Fatalf("目录扫描子任务入队失败: %v", err)
	}
	if child.ParentTaskId != parent.ID {
		t.Fatalf("parent_task_id = %d，期望 %d", child.ParentTaskId, parent.ID)
	}

	if err := IncrementStrmGenerationDirectoryStats(parent.ID, 1, 2); err != nil {
		t.Fatalf("累计目录扫描统计失败: %v", err)
	}
	var got StrmGenerationTask
	if err := db.Db.First(&got, parent.ID).Error; err != nil {
		t.Fatalf("读取目录扫描父任务失败: %v", err)
	}
	if got.AcceptedItems != 1 || got.FailedItems != 2 || got.TotalItems != 3 {
		t.Fatalf("目录统计 = accepted:%d failed:%d total:%d，期望 1/2/3", got.AcceptedItems, got.FailedItems, got.TotalItems)
	}
}

func TestStrmGenerationDirectoryStatsKeepsExpandedTotal(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	parent, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:        StrmGenerationSourceWebhook,
		TaskType:      StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    10,
		AccountId:     2,
		DirectoryId:   "dir-1",
		DirectoryPath: "/remote/show",
		TotalItems:    2,
		RequestHash:   "sync:10:dir:dir-1",
	})
	if err != nil {
		t.Fatalf("目录扫描父任务入队失败: %v", err)
	}

	if err := IncrementStrmGenerationDirectoryStats(parent.ID, 1, 0); err != nil {
		t.Fatalf("累计目录扫描统计失败: %v", err)
	}
	var got StrmGenerationTask
	if err := db.Db.First(&got, parent.ID).Error; err != nil {
		t.Fatalf("读取目录扫描父任务失败: %v", err)
	}
	if got.AcceptedItems != 1 || got.FailedItems != 0 || got.TotalItems != 2 {
		t.Fatalf("目录统计 = accepted:%d failed:%d total:%d，期望 1/0/2", got.AcceptedItems, got.FailedItems, got.TotalItems)
	}
}

func TestUpdateStrmGenerationParentProgressCompletesWaitingParent(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	parent, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  10,
		AccountId:   2,
		TotalItems:  2,
		Status:      StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:10:false:false:progress",
	})
	if err != nil {
		t.Fatalf("批量父任务入队失败: %v", err)
	}

	updated, err := UpdateStrmGenerationParentProgress(parent.ID, StrmGenerationParentProgress{Accepted: 1})
	if err != nil {
		t.Fatalf("首次累计父任务进度失败: %v", err)
	}
	if updated.Status != StrmGenerationStatusWaitingChildren {
		t.Fatalf("部分子任务完成后 status = %s，期望 waiting_children", updated.Status)
	}

	updated, err = UpdateStrmGenerationParentProgress(parent.ID, StrmGenerationParentProgress{Accepted: 1})
	if err != nil {
		t.Fatalf("第二次累计父任务进度失败: %v", err)
	}
	if updated.Status != StrmGenerationStatusCompleted {
		t.Fatalf("全部子任务完成后 status = %s，期望 completed", updated.Status)
	}
}

func TestUpdateStrmGenerationParentProgressUsesAtomicIncrements(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	parent, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  10,
		AccountId:   2,
		TotalItems:  3,
		Status:      StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:10:false:false:atomic",
	})
	if err != nil {
		t.Fatalf("批量父任务入队失败: %v", err)
	}

	var injected atomic.Int32
	callbackName := "test:inject_strm_parent_progress_increment"
	if err := db.Db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if injected.CompareAndSwap(0, 1) {
			if err := tx.Exec("UPDATE strm_generation_tasks SET accepted_items = accepted_items + 1 WHERE id = ?", parent.ID).Error; err != nil {
				t.Fatalf("注入并发父任务进度失败: %v", err)
			}
		}
	}); err != nil {
		t.Fatalf("注册更新回调失败: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Db.Callback().Update().Remove(callbackName)
	})

	updated, err := UpdateStrmGenerationParentProgress(parent.ID, StrmGenerationParentProgress{Accepted: 1})
	if err != nil {
		t.Fatalf("累计父任务进度失败: %v", err)
	}
	if updated.AcceptedItems != 2 {
		t.Fatalf("父任务 accepted_items = %d，期望保留外部增量后为 2", updated.AcceptedItems)
	}
}

func TestUpdateStrmGenerationParentProgressKeepsZeroTotalWaiting(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	parent, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  10,
		AccountId:   2,
		Status:      StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:10:false:false:zero-total",
	})
	if err != nil {
		t.Fatalf("批量父任务入队失败: %v", err)
	}

	updated, err := UpdateStrmGenerationParentProgress(parent.ID, StrmGenerationParentProgress{Accepted: 1})
	if err != nil {
		t.Fatalf("累计零总数父任务进度失败: %v", err)
	}
	if updated.Status != StrmGenerationStatusWaitingChildren {
		t.Fatalf("total_items=0 时 status = %s，期望保持 waiting_children", updated.Status)
	}
}

func TestUpdateStrmGenerationParentProgressFailsWaitingParent(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)

	parent, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  10,
		AccountId:   2,
		TotalItems:  2,
		Status:      StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:10:false:false:failed-child",
	})
	if err != nil {
		t.Fatalf("批量父任务入队失败: %v", err)
	}

	if _, err := UpdateStrmGenerationParentProgress(parent.ID, StrmGenerationParentProgress{Accepted: 1}); err != nil {
		t.Fatalf("累计成功子任务进度失败: %v", err)
	}
	updated, err := UpdateStrmGenerationParentProgress(parent.ID, StrmGenerationParentProgress{Failed: 1})
	if err != nil {
		t.Fatalf("累计失败子任务进度失败: %v", err)
	}
	if updated.Status != StrmGenerationStatusFailed {
		t.Fatalf("存在失败子任务时 status = %s，期望 failed", updated.Status)
	}
}

func TestMarkStrmGenerationChildFailedUpdatesChildAndParent(t *testing.T) {
	setupStrmGenerationTaskTestDB(t)
	parent, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:      StrmGenerationSourceWebhook,
		TaskType:    StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  10,
		AccountId:   2,
		TotalItems:  1,
		Status:      StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:atomic-failure",
	})
	if err != nil {
		t.Fatalf("创建父任务失败: %v", err)
	}
	child, err := EnqueueStrmGenerationTask(&StrmGenerationTask{
		Source:       StrmGenerationSourceWebhook,
		TaskType:     StrmGenerationTaskTypeFile,
		ParentTaskId: parent.ID,
		SyncPathId:   10,
		AccountId:    2,
		Status:       StrmGenerationStatusRunning,
		RequestHash:  "webhook:file:atomic-failure",
	})
	if err != nil {
		t.Fatalf("创建子任务失败: %v", err)
	}

	updated, err := MarkStrmGenerationChildFailed(child.ID, parent.ID, "生成失败")
	if err != nil {
		t.Fatalf("原子标记子任务失败: %v", err)
	}
	if updated.Status != StrmGenerationStatusFailed || updated.FailedItems != 1 {
		t.Fatalf("父任务 = %+v，期望 failed 且 failed_items=1", updated)
	}
	var gotChild StrmGenerationTask
	if err := db.Db.First(&gotChild, child.ID).Error; err != nil {
		t.Fatalf("读取子任务失败: %v", err)
	}
	if gotChild.Status != StrmGenerationStatusFailed || gotChild.RetryCount != 1 || gotChild.LastError != "生成失败" {
		t.Fatalf("子任务 = %+v，期望 failed、retry_count=1 且保存错误", gotChild)
	}
}
