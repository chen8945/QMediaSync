package directoryupload

import (
	"context"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCleanupUsesLatestFileDependency(t *testing.T) {
	for _, status := range []models.StrmGenerationStatus{"skipped", "failed", "cancelled", "pending", "running", "finalizing", "waiting_children"} {
		t.Run(string(status), func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			root := t.TempDir()
			_, rule := createDirectoryUploadRuleForTest(t, root)
			rule.DeleteSourceAfterSuccess = true
			if err := db.Db.Save(rule).Error; err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "movie.mkv")
			writeFileWithMtime(t, path, []byte("movie"), time.Now())
			upload := createCleanupUploadTask(t, rule, path, models.UploadResultMultipartUploaded)
			createCleanupStrmTask(t, upload.ID, models.StrmGenerationStatusCompleted)
			createCleanupStrmTask(t, upload.ID, status)
			if err := CleanupSourceAfterStrmSuccess(upload.ID); err != nil {
				t.Fatal(err)
			}
			assertPathExists(t, path)
			if cleaned, err := CleanupCompletedStrmDependencies(1); err != nil || cleaned != 0 {
				t.Fatalf("cleaned=%d err=%v", cleaned, err)
			}
			assertPathExists(t, path)

			// 新成功能替代旧终态，但不能越过仍在处理的任务。
			createCleanupStrmTask(t, upload.ID, models.StrmGenerationStatusCompleted)
			cleaned, err := CleanupCompletedStrmDependencies(1)
			if err != nil {
				t.Fatal(err)
			}
			if status == "skipped" || status == "failed" || status == "cancelled" {
				if cleaned != 1 {
					t.Fatalf("cleaned=%d", cleaned)
				}
				assertPathMissing(t, path)
			} else {
				if cleaned != 0 {
					t.Fatalf("cleaned=%d", cleaned)
				}
				assertPathExists(t, path)
			}
		})
	}
}

func TestCleanupParentCompletionDoesNotAuthorizeSourceDeletion(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	root := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, root)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "movie.mkv")
	writeFileWithMtime(t, path, []byte("movie"), time.Now())
	upload := createCleanupUploadTask(t, rule, path, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, upload.ID, "skipped")
	if err := db.Db.Create(&models.StrmGenerationTask{UploadTaskId: upload.ID, TaskType: models.StrmGenerationTaskTypeDirectoryScan, Status: models.StrmGenerationStatusCompleted}).Error; err != nil {
		t.Fatal(err)
	}
	if err := CleanupSourceAfterStrmSuccess(upload.ID); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := CleanupCompletedStrmDependencies(1); err != nil || cleaned != 0 {
		t.Fatalf("cleaned=%d err=%v", cleaned, err)
	}
	assertPathExists(t, path)
}

func TestCleanupSkippedSourceSurvivesReloadStartupAndInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupDirectoryUploadServiceTestDB(t)
		root := t.TempDir()
		_, rule := createDirectoryUploadRuleForTest(t, root)
		rule.DeleteSourceAfterSuccess = true
		if err := db.Db.Save(rule).Error; err != nil {
			t.Fatal(err)
		}
		skippedPath := filepath.Join(root, "skipped.mkv")
		writeFileWithMtime(t, skippedPath, []byte("skipped"), time.Now())
		skippedUpload := createCleanupUploadTask(t, rule, skippedPath, models.UploadResultMultipartUploaded)
		createCleanupStrmTask(t, skippedUpload.ID, models.StrmGenerationStatusCompleted)
		createCleanupStrmTask(t, skippedUpload.ID, "skipped")
		makeSuccess := func(name string) string {
			path := filepath.Join(root, name)
			writeFileWithMtime(t, path, []byte(name), time.Now())
			upload := createCleanupUploadTask(t, rule, path, models.UploadResultMultipartUploaded)
			createCleanupStrmTask(t, upload.ID, models.StrmGenerationStatusCompleted)
			return path
		}
		startupPath := makeSuccess("startup.mkv")
		dsn := db.Db.Dialector.(*sqlite.Dialector).DSN
		sqlDB, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		if err := sqlDB.Close(); err != nil {
			t.Fatal(err)
		}
		db.Db, err = gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		reopened, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		service := NewService(ServiceOptions{ProcessedCleanupInterval: 5 * time.Second})
		ctx, cancel := context.WithCancel(context.Background())
		service.startProcessedCleanup(ctx)
		defer func() { cancel(); <-service.cleanupDone }()
		assertPathMissing(t, startupPath)
		assertPathExists(t, skippedPath)
		intervalPath := makeSuccess("interval.mkv")
		time.Sleep(6 * time.Second)
		synctest.Wait()
		assertPathMissing(t, intervalPath)
		assertPathExists(t, skippedPath)
		var restored models.DbUploadTask
		if err := db.Db.First(&restored, skippedUpload.ID).Error; err != nil {
			t.Fatal(err)
		}
		if restored.SourceDeletedAt != 0 || restored.SourceCleanupStatus != models.UploadSourceCleanupStatusPending {
			t.Fatalf("skipped upload=%+v", restored)
		}
	})
}
