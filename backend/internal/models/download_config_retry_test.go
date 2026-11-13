package models

import (
	"errors"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestDownloadQueueRetriesAfterConfigReadError(t *testing.T) {
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		t.Run(string(kind), func(t *testing.T) {
			setupMediaDownloadTestDB(t)
			for _, product := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
				saveMediaDownloadConfig(t, product, "http://media.invalid", "key")
			}
			task := DbDownloadTask{Source: DownloadSource(kind + "_media"), SourceType: SourceType(kind + "_media"), MediaServerID: "original", EmbyItemId: mediaDownloadTestItem, Status: DownloadStatusFailed, Error: "earlier error"}
			if err := createDownloadTaskWithDB(db.Db, &task); err != nil {
				t.Fatal(err)
			}
			var configReads atomic.Int32
			const callback = "test:retry_config_read_error"
			if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == mediaLibraryConfigTable(kind) && configReads.Add(1) == 2 {
					tx.AddError(errors.New("temporary configuration query failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
			dq := NewDq(1)
			dq.running = true
			dq.moveTasksToChannel()
			var stored DbDownloadTask
			if err := db.Db.First(&stored, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.Status != DownloadStatusFailed || stored.RetryCount != 0 || stored.Error != task.Error {
				t.Fatalf("failed configuration query changed task: %+v", stored)
			}
			for range 4 {
				dq.moveTasksToChannel()
			}
			if len(dq.tasks) != 1 {
				t.Fatalf("database recovered but extraction was not automatically retried: queued=%d", len(dq.tasks))
			}
			queued := <-dq.tasks
			if queued.ID != task.ID || queued.Status != DownloadStatusDownloading || queued.RetryCount != DefaultQueueRetryMax || queued.Error != "" {
				t.Fatalf("invalid recovered extraction: %+v", queued)
			}
		})
	}
}

func TestDownloadRetryPropagatesConfigReadError(t *testing.T) {
	setupMediaDownloadTestDB(t)
	wantErr := errors.New("temporary configuration query failure")
	const callback = "test:retry_config_error_result"
	if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "jellyfin_config" {
			tx.AddError(wantErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
	if err := RetryFailedDownloadTasks(DefaultQueueRetryMax); !errors.Is(err, wantErr) {
		t.Fatalf("configuration query error was swallowed: %v", err)
	}
}

func TestDownloadRetryKeepsUnconfiguredAndDisabledSourcesPaused(t *testing.T) {
	for _, state := range []string{"missing", "disabled", "incomplete"} {
		t.Run(state, func(t *testing.T) {
			setupMediaDownloadTestDB(t)
			if state != "missing" {
				for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
					config := saveMediaDownloadConfig(t, kind, "http://media.invalid", "key")
					updates := map[string]any{"enabled": false}
					if state == "incomplete" {
						updates = map[string]any{string(kind) + "_api_key": ""}
					}
					if err := db.Db.Table(mediaLibraryConfigTable(kind)).Where("id = ?", config.ID).Updates(updates).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
				task := DbDownloadTask{Source: DownloadSource(kind + "_media"), SourceType: SourceType(kind + "_media"), MediaServerID: "original", EmbyItemId: mediaDownloadTestItem, Status: DownloadStatusFailed}
				if err := createDownloadTaskWithDB(db.Db, &task); err != nil {
					t.Fatal(err)
				}
			}
			ordinary := DbDownloadTask{Source: DownloadSourceLocalFile, SourceType: SourceTypeLocal, LocalSourcePath: "/synthetic/source", LocalFullPath: "/synthetic/target", Status: DownloadStatusFailed}
			if err := createDownloadTaskWithDB(db.Db, &ordinary); err != nil {
				t.Fatal(err)
			}
			if err := RetryFailedDownloadTasks(DefaultQueueRetryMax); err != nil {
				t.Fatalf("unconfigured or paused product blocked ordinary downloads: %v", err)
			}
			if pending := GetPendingDownloadTasks(10); len(pending) != 1 || pending[0].ID != ordinary.ID {
				t.Fatalf("unexpected runnable tasks: %+v", pending)
			}
			var paused []DbDownloadTask
			if err := db.Db.Where("source IN ?", []DownloadSource{DownloadSourceEmbyMedia, DownloadSourceJellyfinMedia}).Find(&paused).Error; err != nil {
				t.Fatal(err)
			}
			for _, task := range paused {
				if task.Status != DownloadStatusFailed || task.RetryCount != 0 {
					t.Fatalf("paused extraction consumed retry: %+v", task)
				}
			}
		})
	}
}
