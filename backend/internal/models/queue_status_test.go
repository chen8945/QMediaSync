package models

import (
	"errors"
	"io"
	"log"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupQueueStatusTestDB(t *testing.T) {
	t.Helper()
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&DbDownloadTask{}, &DbUploadTask{}, &UploadSession{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	t.Cleanup(func() {
		GlobalDownloadQueue = nil
		GlobalUploadQueue = nil
	})
}

func TestDownloadQueueStatusSnapshot(t *testing.T) {
	setupQueueStatusTestDB(t)
	GlobalDownloadQueue = NewDq(1)
	GlobalDownloadQueue.running = true
	db.Db.Create(&DbDownloadTask{Status: DownloadStatusPending})
	db.Db.Create(&DbDownloadTask{Status: DownloadStatusDownloading})
	db.Db.Create(&DbDownloadTask{Status: DownloadStatusCompleted})

	snapshot := GetDownloadQueueStatusSnapshot()

	if !snapshot.Running {
		t.Fatal("下载队列快照应显示运行中")
	}
	if snapshot.Pending != 1 || snapshot.Processing != 1 || snapshot.Completed != 1 {
		t.Fatalf("下载队列状态统计 = %+v，期望 pending=1 processing=1 completed=1", snapshot)
	}
	if snapshot.Total != 3 {
		t.Fatalf("下载队列总数 = %d，期望 3", snapshot.Total)
	}
}

func TestUploadQueueStatusSnapshot(t *testing.T) {
	setupQueueStatusTestDB(t)
	GlobalUploadQueue = NewUq(1)
	GlobalUploadQueue.running = false
	db.Db.Create(&DbUploadTask{Status: UploadStatusPending})
	db.Db.Create(&DbUploadTask{Status: UploadStatusRemoteCompletedFinalizing})
	db.Db.Create(&DbUploadTask{Status: UploadStatusFailed})
	db.Db.Create(&DbUploadTask{Status: UploadStatusCancelled})

	snapshot := GetUploadQueueStatusSnapshot()

	if snapshot.Running {
		t.Fatal("上传队列快照应显示已停止")
	}
	if snapshot.Pending != 1 || snapshot.Processing != 1 || snapshot.Failed != 1 || snapshot.Cancelled != 1 {
		t.Fatalf("上传队列状态统计 = %+v，期望 pending=1 processing=1 failed=1 cancelled=1", snapshot)
	}
	if snapshot.Total != 4 {
		t.Fatalf("上传队列总数 = %d，期望 4", snapshot.Total)
	}
}

func TestUpdateUploadingToPendingPreservesUploadSession(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		Status:        UploadStatusUploading,
		Source:        UploadSourceDirectoryMonitor,
		SourceType:    SourceType115,
		LocalFullPath: "/watch/movie.mkv",
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	session := &UploadSession{
		UploadTaskId:  task.ID,
		Status:        UploadSessionStatusMultipart,
		ResumeState:   UploadResumeStateResumedSession,
		UploadId:      "upload-1",
		UploadedBytes: 1024,
	}
	if err := session.Save(); err != nil {
		t.Fatalf("保存上传会话失败: %v", err)
	}

	if err := UpdateUploadingToPending(); err != nil {
		t.Fatalf("恢复上传中任务失败: %v", err)
	}

	var gotTask DbUploadTask
	if err := db.Db.First(&gotTask, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if gotTask.Status != UploadStatusPending {
		t.Fatalf("任务状态 = %s，期望 pending", gotTask.Status.String())
	}
	gotSession, err := GetUploadSessionByUploadTaskId(task.ID)
	if err != nil {
		t.Fatalf("读取上传会话失败: %v", err)
	}
	if gotSession.UploadId != "upload-1" || gotSession.UploadedBytes != 1024 {
		t.Fatalf("上传会话 = %+v，期望保留 upload_id 和进度", gotSession)
	}
}

func TestUpdateUploadingToPendingResetsInterruptedFinalizeToPendingFinalize(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		Status: UploadStatusRemoteCompletedFinalizing,
		Source: UploadSourceDirectoryMonitor,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建收尾中上传任务失败: %v", err)
	}

	if err := UpdateUploadingToPending(); err != nil {
		t.Fatalf("恢复中断上传任务失败: %v", err)
	}

	var gotTask DbUploadTask
	if err := db.Db.First(&gotTask, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if gotTask.Status != UploadStatusRemoteCompletedPendingFinalize {
		t.Fatalf("收尾中任务重置后 status = %s，期望 remote_completed_pending_finalize", gotTask.Status.String())
	}
}

func TestRemoteCompletedFinalizeClaimAllowsSingleOwner(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		Status: UploadStatusRemoteCompletedPendingFinalize,
		Source: UploadSourceDirectoryMonitor,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建待收尾上传任务失败: %v", err)
	}

	claimed, err := task.claimRemoteCompletedFinalize()
	if err != nil {
		t.Fatalf("首次抢占待收尾任务失败: %v", err)
	}
	if !claimed {
		t.Fatal("首次抢占待收尾任务应成功")
	}
	duplicate := &DbUploadTask{ID: task.ID}
	claimedAgain, err := duplicate.claimRemoteCompletedFinalize()
	if err != nil {
		t.Fatalf("重复抢占待收尾任务失败: %v", err)
	}
	if claimedAgain {
		t.Fatal("重复抢占同一待收尾任务不应成功")
	}

	var gotTask DbUploadTask
	if err := db.Db.First(&gotTask, task.ID).Error; err != nil {
		t.Fatalf("读取上传任务失败: %v", err)
	}
	if gotTask.Status != UploadStatusRemoteCompletedFinalizing {
		t.Fatalf("抢占后任务状态 = %s，期望 remote_completed_finalizing", gotTask.Status.String())
	}
}

func TestRemoteCompletedFinalizeClaimClearsPreviousError(t *testing.T) {
	setupQueueStatusTestDB(t)

	task := &DbUploadTask{
		Status: UploadStatusRemoteCompletedPendingFinalize,
		Error:  "temporary finalize error",
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建待收尾上传任务失败: %v", err)
	}

	claimed, err := task.claimRemoteCompletedFinalize()
	if err != nil {
		t.Fatalf("抢占待收尾任务失败: %v", err)
	}
	if !claimed {
		t.Fatal("待收尾任务应成功被抢占")
	}
	if task.Error != "" {
		t.Fatalf("抢占后内存错误 = %q，期望为空", task.Error)
	}

	var gotTask DbUploadTask
	if err := db.Db.First(&gotTask, task.ID).Error; err != nil {
		t.Fatalf("读取收尾任务失败: %v", err)
	}
	if gotTask.Error != "" {
		t.Fatalf("抢占后数据库错误 = %q，期望为空", gotTask.Error)
	}
}

func TestUploadProgressThrottleClearedWhenTaskLeavesActiveState(t *testing.T) {
	setupQueueStatusTestDB(t)
	resetUploadProgressThrottleForTest(t)

	tests := []struct {
		name       string
		transition func(task *DbUploadTask)
	}{
		{
			name: "完成",
			transition: func(task *DbUploadTask) {
				task.Complete()
			},
		},
		{
			name: "失败",
			transition: func(task *DbUploadTask) {
				task.Fail(errors.New("upload failed"))
			},
		},
		{
			name: "取消",
			transition: func(task *DbUploadTask) {
				task.Cancel()
			},
		},
		{
			name: "带错误取消",
			transition: func(task *DbUploadTask) {
				task.cancelWithError(errors.New("fingerprint mismatch"))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := &DbUploadTask{Status: UploadStatusUploading}
			if err := db.Db.Create(task).Error; err != nil {
				t.Fatalf("创建上传任务失败: %v", err)
			}
			if shouldThrottleUploadProgressBroadcast(task.ID) {
				t.Fatal("首次进度广播不应被节流")
			}

			tt.transition(task)

			uploadQueueProgressBroadcast.Lock()
			_, exists := uploadQueueProgressBroadcast.lastAt[task.ID]
			uploadQueueProgressBroadcast.Unlock()
			if exists {
				t.Fatalf("任务进入终态后仍保留进度节流记录: task_id=%d", task.ID)
			}
		})
	}
}

func TestUploadProgressThrottlePrunesExpiredEntries(t *testing.T) {
	resetUploadProgressThrottleForTest(t)

	uploadQueueProgressBroadcast.Lock()
	uploadQueueProgressBroadcast.lastAt[101] = time.Now().Add(-31 * time.Minute)
	uploadQueueProgressBroadcast.lastAt[102] = time.Now().Add(-29 * time.Minute)
	uploadQueueProgressBroadcast.Unlock()

	if shouldThrottleUploadProgressBroadcast(103) {
		t.Fatal("新任务首次进度广播不应被节流")
	}

	uploadQueueProgressBroadcast.Lock()
	_, expiredExists := uploadQueueProgressBroadcast.lastAt[101]
	_, freshExists := uploadQueueProgressBroadcast.lastAt[102]
	_, newExists := uploadQueueProgressBroadcast.lastAt[103]
	uploadQueueProgressBroadcast.Unlock()

	if expiredExists {
		t.Fatal("超过 TTL 的进度节流记录应被清理")
	}
	if !freshExists || !newExists {
		t.Fatalf("未过期记录和新记录应保留: fresh=%v new=%v", freshExists, newExists)
	}
}

func resetUploadProgressThrottleForTest(t *testing.T) {
	t.Helper()
	uploadQueueProgressBroadcast.Lock()
	originalLastAt := uploadQueueProgressBroadcast.lastAt
	originalLastCleanup := uploadQueueProgressBroadcast.lastCleanup
	uploadQueueProgressBroadcast.lastAt = make(map[uint]time.Time)
	uploadQueueProgressBroadcast.lastCleanup = time.Time{}
	uploadQueueProgressBroadcast.Unlock()
	t.Cleanup(func() {
		uploadQueueProgressBroadcast.Lock()
		uploadQueueProgressBroadcast.lastAt = originalLastAt
		uploadQueueProgressBroadcast.lastCleanup = originalLastCleanup
		uploadQueueProgressBroadcast.Unlock()
	})
}
