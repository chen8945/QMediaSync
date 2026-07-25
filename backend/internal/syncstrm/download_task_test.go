package syncstrm

import (
	"fmt"
	"io"
	"log"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestPendingDownloadFileIDsIncludesFinalPage(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	for _, count := range []int{1, 1000, 1001} {
		t.Run(fmt.Sprintf("%d 条任务", count), func(t *testing.T) {
			testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatalf("打开测试数据库失败: %v", err)
			}
			db.Db = testDb
			if err := db.Db.AutoMigrate(&models.DbDownloadTask{}); err != nil {
				t.Fatalf("迁移下载任务表失败: %v", err)
			}

			tasks := make([]models.DbDownloadTask, 0, count)
			for index := 0; index < count; index++ {
				tasks = append(tasks, models.DbDownloadTask{
					SourceType:   models.SourceType115,
					RemoteFileId: fmt.Sprintf("pick-%d", index),
					Status:       models.DownloadStatusPending,
				})
			}
			if err := db.Db.CreateInBatches(tasks, 100).Error; err != nil {
				t.Fatalf("创建待下载任务失败: %v", err)
			}

			syncer := &SyncStrm{
				Account: &models.Account{SourceType: models.SourceType115},
				Sync:    &models.Sync{Logger: helpers.AppLogger},
			}
			existing := syncer.pendingDownloadFileIDs()
			if len(existing) != count || !existing["pick-0"] || !existing[fmt.Sprintf("pick-%d", count-1)] {
				t.Fatalf("去重集合 = %d 条，期望 %d 条并包含首尾任务", len(existing), count)
			}
		})
	}
}

func TestAddMetaDownloadTaskCountsNewMeta(t *testing.T) {
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(
		&models.DbDownloadTask{},
		&models.EmbyMediaSyncFile{},
		&models.EmbyLibrarySyncPath{},
	); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}

	s := &SyncStrm{}
	file := &models.SyncFile{
		SyncPathId:    10,
		SourceType:    models.SourceType115,
		PickCode:      "pick-meta",
		FileName:      "movie.nfo",
		LocalFilePath: "/media/movie/movie.nfo",
		SyncPath:      &models.SyncPath{},
	}

	if err := s.addMetaDownloadTask(file); err != nil {
		t.Fatalf("添加下载任务失败: %v", err)
	}
	if got := atomic.LoadInt64(&s.NewMeta); got != 1 {
		t.Fatalf("NewMeta = %d，期望 1", got)
	}

	var task models.DbDownloadTask
	if err := db.Db.Where("remote_file_id = ?", "pick-meta").First(&task).Error; err != nil {
		t.Fatalf("查询下载任务失败: %v", err)
	}
	if task.SyncPathId != 10 {
		t.Fatalf("下载任务 sync_path_id = %d，期望 10", task.SyncPathId)
	}
}
