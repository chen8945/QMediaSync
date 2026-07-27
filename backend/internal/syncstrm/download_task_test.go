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
					Source:         models.DownloadSourceStrm,
					AccountId:      1,
					SyncPathId:     10,
					SourceType:     models.SourceType115,
					RemoteFileId:   fmt.Sprintf("file-%d", index),
					RemotePickCode: fmt.Sprintf("pick-%d", index),
					Status:         models.DownloadStatusPending,
				})
			}
			if err := db.Db.CreateInBatches(tasks, 100).Error; err != nil {
				t.Fatalf("创建待下载任务失败: %v", err)
			}

			syncer := &SyncStrm{
				Account:    &models.Account{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115},
				SyncPathId: 10,
				Sync:       &models.Sync{Logger: helpers.AppLogger},
			}
			existing := syncer.pendingDownloadFileIDs()
			if len(existing) != count || !existing["pick-0"] || !existing[fmt.Sprintf("pick-%d", count-1)] {
				t.Fatalf("去重集合 = %d 条，期望 %d 条并包含首尾任务", len(existing), count)
			}
		})
	}
}

func TestPendingDownloadFileIDsUsesSourceSpecificLocator(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&models.DbDownloadTask{}); err != nil {
		t.Fatalf("迁移下载任务表失败: %v", err)
	}
	if err := db.Db.Create(&models.DbDownloadTask{
		Source:       models.DownloadSourceStrm,
		AccountId:    1,
		SyncPathId:   10,
		SourceType:   models.SourceTypeBaiduPan,
		RemoteFileId: "baidu-fs-id",
		Status:       models.DownloadStatusPending,
	}).Error; err != nil {
		t.Fatalf("创建百度待下载任务失败: %v", err)
	}

	syncer := &SyncStrm{
		Account:    &models.Account{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceTypeBaiduPan},
		SyncPathId: 10,
		Sync:       &models.Sync{Logger: helpers.AppLogger},
	}
	existing := syncer.pendingDownloadFileIDs()
	if len(existing) != 1 || !existing["baidu-fs-id"] {
		t.Fatalf("百度下载去重集合 = %#v，期望使用 fs_id", existing)
	}
}

func TestPendingDownloadFileIDsIsolatesAccountAndSyncPath(t *testing.T) {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&models.DbDownloadTask{}); err != nil {
		t.Fatalf("迁移下载任务表失败: %v", err)
	}

	tasks := []models.DbDownloadTask{
		{
			Source:         models.DownloadSourceStrm,
			AccountId:      1,
			SyncPathId:     10,
			SourceType:     models.SourceType115,
			RemotePickCode: "current-scope",
			Status:         models.DownloadStatusPending,
		},
		{
			Source:         models.DownloadSourceStrm,
			AccountId:      2,
			SyncPathId:     10,
			SourceType:     models.SourceType115,
			RemotePickCode: "other-account",
			Status:         models.DownloadStatusPending,
		},
		{
			Source:         models.DownloadSourceStrm,
			AccountId:      1,
			SyncPathId:     11,
			SourceType:     models.SourceType115,
			RemotePickCode: "other-sync-path",
			Status:         models.DownloadStatusPending,
		},
	}
	if err := db.Db.Create(&tasks).Error; err != nil {
		t.Fatalf("创建待下载任务失败: %v", err)
	}

	syncer := &SyncStrm{
		Account:    &models.Account{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115},
		SyncPathId: 10,
		Sync:       &models.Sync{Logger: helpers.AppLogger},
	}
	existing := syncer.pendingDownloadFileIDs()
	if len(existing) != 1 || !existing["current-scope"] || existing["other-account"] || existing["other-sync-path"] {
		t.Fatalf("去重集合 = %#v，期望只包含当前账号和同步目录任务", existing)
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
	if err := db.Db.Where("remote_pick_code = ?", "pick-meta").First(&task).Error; err != nil {
		t.Fatalf("查询下载任务失败: %v", err)
	}
	if task.SyncPathId != 10 {
		t.Fatalf("下载任务 sync_path_id = %d，期望 10", task.SyncPathId)
	}
}
