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

func setupSyncDeleteTestDB(t *testing.T) {
	t.Helper()

	originalDb := db.Db
	originalConfigDir := helpers.ConfigDir
	originalGlobalConfig := helpers.GlobalConfig
	originalLogger := helpers.AppLogger
	t.Cleanup(func() {
		db.Db = originalDb
		helpers.ConfigDir = originalConfigDir
		helpers.GlobalConfig = originalGlobalConfig
		helpers.AppLogger = originalLogger
	})

	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&Sync{}, &SyncPath{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	helpers.ConfigDir = t.TempDir()
	helpers.GlobalConfig = *helpers.MakeDefaultConfig()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
}

func TestDeleteSyncRecordById只允许删除已结束记录(t *testing.T) {
	tests := []struct {
		name       string
		status     SyncStatus
		wantDelete bool
		wantErr    error
	}{
		{name: "待处理记录不能删除", status: SyncStatusPending, wantErr: errSyncRecordNotDeletable},
		{name: "进行中记录不能删除", status: SyncStatusInProgress, wantErr: errSyncRecordNotDeletable},
		{name: "已完成记录可以删除", status: SyncStatusCompleted, wantDelete: true},
		{name: "失败记录可以删除", status: SyncStatusFailed, wantDelete: true},
		{name: "部分完成记录可以删除", status: SyncStatusPartial, wantDelete: true},
		{name: "不完整记录可以删除", status: SyncStatusIncomplete, wantDelete: true},
		{name: "取消记录可以删除", status: SyncStatusCancelled, wantDelete: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupSyncDeleteTestDB(t)
			sync := &Sync{SyncPathId: 1, Status: tt.status, LocalPath: "/local", RemotePath: "/remote"}
			if err := db.Db.Create(sync).Error; err != nil {
				t.Fatalf("创建同步记录失败: %v", err)
			}

			err := DeleteSyncRecordById(sync.ID)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("DeleteSyncRecordById() error = %v，期望匹配 %v", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("DeleteSyncRecordById() error = %v，期望 nil", err)
			}

			var count int64
			if err := db.Db.Model(&Sync{}).Where("id = ?", sync.ID).Count(&count).Error; err != nil {
				t.Fatalf("统计同步记录失败: %v", err)
			}
			if tt.wantDelete && count != 0 {
				t.Fatalf("同步记录未删除，count = %d", count)
			}
			if !tt.wantDelete && count != 1 {
				t.Fatalf("同步记录不应删除，count = %d", count)
			}
		})
	}
}

func TestDeleteSyncRecordById记录不存在时返回错误(t *testing.T) {
	setupSyncDeleteTestDB(t)

	err := DeleteSyncRecordById(404)
	if !errors.Is(err, errSyncRecordNotFound) {
		t.Fatalf("DeleteSyncRecordById() error = %v，期望匹配 %v", err, errSyncRecordNotFound)
	}
}

func TestDeleteTemporarySyncRecordById允许删除待处理记录(t *testing.T) {
	setupSyncDeleteTestDB(t)
	sync := &Sync{SyncPathId: 1, Status: SyncStatusPending, LocalPath: "/local", RemotePath: "/remote"}
	if err := db.Db.Create(sync).Error; err != nil {
		t.Fatalf("创建同步记录失败: %v", err)
	}

	if err := DeleteTemporarySyncRecordById(sync.ID); err != nil {
		t.Fatalf("DeleteTemporarySyncRecordById() error = %v，期望 nil", err)
	}

	var count int64
	if err := db.Db.Model(&Sync{}).Where("id = ?", sync.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计同步记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("临时同步记录未删除，count = %d", count)
	}
}

func TestClearExpiredSyncRecords清理过期待处理记录(t *testing.T) {
	setupSyncDeleteTestDB(t)
	expired := &Sync{
		CreatedAt:  time.Now().AddDate(0, 0, -8).Unix(),
		SyncPathId: 1,
		Status:     SyncStatusPending,
		LocalPath:  "/local",
		RemotePath: "/remote",
	}
	if err := db.Db.Create(expired).Error; err != nil {
		t.Fatalf("创建过期同步记录失败: %v", err)
	}

	ClearExpiredSyncRecords(7)

	var count int64
	if err := db.Db.Model(&Sync{}).Where("id = ?", expired.ID).Count(&count).Error; err != nil {
		t.Fatalf("统计同步记录失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("过期待处理同步记录未清理，count = %d", count)
	}
}

func TestGetSyncByID同步路径缺失时仍返回历史记录(t *testing.T) {
	setupSyncDeleteTestDB(t)
	sync := &Sync{SyncPathId: 99, Status: SyncStatusCompleted, LocalPath: "/local", RemotePath: "/remote"}
	if err := db.Db.Create(sync).Error; err != nil {
		t.Fatalf("创建同步记录失败: %v", err)
	}

	got, err := GetSyncByID(sync.ID)
	if err != nil {
		t.Fatalf("GetSyncByID() error = %v，期望 nil", err)
	}
	if got == nil {
		t.Fatal("GetSyncByID() 返回 nil")
	}
	if got.ID != sync.ID {
		t.Fatalf("GetSyncByID().ID = %d，期望 %d", got.ID, sync.ID)
	}
	if got.SyncPath != nil {
		t.Fatalf("同步路径缺失时 SyncPath = %+v，期望 nil", got.SyncPath)
	}
}
