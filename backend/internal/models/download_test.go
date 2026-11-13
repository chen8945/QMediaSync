package models

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"

	"gorm.io/gorm"
)

func TestDownloadQueueRetriesAfterDatabaseError(t *testing.T) {
	setupMediaDownloadTestDB(t)
	testDownloadQueueRetriesAfterDatabaseError(t)
}

func testDownloadQueueRetriesAfterDatabaseError(t *testing.T) {
	t.Helper()
	if err := db.Db.AutoMigrate(&DbDownloadTask{}, &EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		saveMediaDownloadConfig(t, kind, "http://media.invalid", "key")
	}
	tasks := []*DbDownloadTask{
		{Source: DownloadSourceEmbyMedia, SourceType: SourceTypeEmbyMedia, MediaServerID: "original", EmbyItemId: mediaDownloadTestItem},
		{Source: DownloadSourceJellyfinMedia, SourceType: SourceTypeJellyfinMedia, MediaServerID: "original", EmbyItemId: mediaDownloadTestItem},
		{Source: DownloadSourceLocalFile, SourceType: SourceTypeLocal, LocalSourcePath: "/synthetic/source", LocalFullPath: "/synthetic/target"},
		{Source: DownloadSourceLocalFile, SourceType: SourceTypeLocal, LocalSourcePath: "/synthetic/exhausted", LocalFullPath: "/synthetic/exhausted-target", RetryCount: DefaultQueueRetryMax},
	}
	for _, task := range tasks {
		task.Status, task.Error = DownloadStatusFailed, "earlier error"
		if err := createDownloadTaskWithDB(db.Db, task); err != nil {
			t.Fatal(err)
		}
	}
	var retryQueries atomic.Int32
	const callback = "test:download_retry_database_error"
	if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table != "db_download_tasks" {
			return
		}
		if _, ok := tx.Statement.Dest.(*[]DbDownloadTask); ok && retryQueries.Add(1) == 1 {
			tx.AddError(errors.New("temporary database connection failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
	dq := newDownloadPollTestQueue(t)
	pollDownloads(t, dq)
	for _, task := range tasks {
		var stored DbDownloadTask
		if err := db.Db.First(&stored, task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != DownloadStatusFailed || stored.RetryCount != task.RetryCount || stored.Error != task.Error {
			t.Fatalf("failed retry query changed task %d: %+v", task.ID, stored)
		}
	}
	for range 4 {
		pollDownloads(t, dq)
	}
	queued := make(map[uint]bool)
	for _, task := range GetPendingDownloadTasks(10) {
		if !dq.claim(t.Context(), task) {
			t.Fatalf("pending task %d was not claimed", task.ID)
		}
		if queued[task.ID] || task.RetryCount != DefaultQueueRetryMax || task.Status != DownloadStatusDownloading || task.Error != "" {
			t.Fatalf("invalid recovered download task: %+v", task)
		}
		queued[task.ID] = true
	}
	for i, task := range tasks {
		if queued[task.ID] != (i < 3) {
			t.Fatalf("recovery queued task %d (%s) = %t, want %t", task.ID, task.Source, queued[task.ID], i < 3)
		}
	}
	if err := db.Db.Model(&DbDownloadTask{}).Where("status = ?", DownloadStatusDownloading).Update("status", DownloadStatusFailed).Error; err != nil {
		t.Fatal(err)
	}
	for range 3 {
		pollDownloads(t, dq)
	}
	if len(GetPendingDownloadTasks(10)) != 0 || retryQueries.Load() != 3 {
		t.Fatalf("exhausted tasks or successful idle retry were repeated: queries=%d", retryQueries.Load())
	}
	for _, task := range tasks {
		var stored DbDownloadTask
		if err := db.Db.First(&stored, task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Status != DownloadStatusFailed || stored.RetryCount != DefaultQueueRetryMax {
			t.Fatalf("retry limit was not preserved: %+v", stored)
		}
	}
}

// 固定慢轮询只隔离自动重试的SQL行为；正常准入由派发测试验证。
func newDownloadPollTestQueue(t *testing.T) *DQ {
	t.Helper()
	queue := NewDq(1)
	queue.interval = time.Hour
	queue.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return queue
}

func pollDownloads(t *testing.T, queue *DQ) []*DbDownloadTask {
	t.Helper()
	queue.mutex.RLock()
	ctx := queue.generation
	queue.mutex.RUnlock()
	tasks, _ := queue.pendingTasks(ctx, 10, nil)
	return tasks
}

func TestDownloadQueuePauseKeepsIOAndShutdownCancelsIt(t *testing.T) {
	setupMediaDownloadTestDB(t)
	requested, cancelled := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(requested)
		<-r.Context().Done()
		close(cancelled)
	}))
	t.Cleanup(server.Close)
	task := &DbDownloadTask{Source: DownloadSourceStrm, SourceType: SourceTypeOpenList, Status: DownloadStatusPending,
		LocalFullPath: filepath.Join(t.TempDir(), "movie.nfo"), RemoteDownloadUrl: server.URL, Size: -1}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	queue := NewDq(1)
	query := queue.candidates
	queue.candidates = func(ctx context.Context, limit int, excluded []uint) ([]*DbDownloadTask, error) {
		tasks, err := query(ctx, limit, excluded)
		for _, task := range tasks {
			task.Account = &Account{SourceType: SourceTypeOpenList}
		}
		return tasks, err
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	queue.Start()
	select {
	case <-requested:
	case <-time.After(3 * time.Second):
		t.Fatal("下载没有进入真实HTTP请求")
	}
	queue.Stop()
	select {
	case <-cancelled:
		t.Fatal("网页暂停不应取消在途IO")
	default:
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := queue.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-ctx.Done():
		t.Fatal("最终关闭未取消HTTP请求")
	}
	if _, err := os.Stat(task.LocalFullPath); !os.IsNotExist(err) {
		t.Fatalf("取消不能发布不完整元数据：%v", err)
	}
	var stored DbDownloadTask
	if err := db.Db.First(&stored, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != DownloadStatusPending || stored.StartTime != 0 || stored.EndTime != 0 || stored.Error != "" || stored.RetryCount != 0 {
		t.Fatalf("取消的下载应保留完整待办状态：%+v", stored)
	}
}
