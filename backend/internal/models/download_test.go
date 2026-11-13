package models

import (
	"errors"
	"sync/atomic"
	"testing"

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
	dq := NewDq(1)
	dq.running = true
	dq.moveTasksToChannel()
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
		dq.moveTasksToChannel()
	}
	queued := make(map[uint]bool)
	for len(dq.tasks) > 0 {
		task := <-dq.tasks
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
		dq.moveTasksToChannel()
	}
	if len(dq.tasks) != 0 || retryQueries.Load() != 3 {
		t.Fatalf("exhausted tasks or successful idle retry were repeated: queued=%d queries=%d", len(dq.tasks), retryQueries.Load())
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
