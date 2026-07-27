package syncstrm

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupStrmGenerationServiceTestDB(t *testing.T) (*models.Account, *models.SyncPath) {
	t.Helper()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDB
	if err := db.Db.AutoMigrate(
		&models.Account{},
		&models.SyncPath{},
		&models.Sync{},
		&models.SyncFile{},
		&models.DbDownloadTask{},
		&models.DbUploadTask{},
		&models.StrmGenerationTask{},
		&models.Settings{},
		&models.EmbyConfig{},
		&models.EmbyLibrary{},
		&models.EmbyLibrarySyncPath{},
		&models.EmbyLibraryRefreshTask{},
	); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}
	models.SettingsGlobal = &models.Settings{
		SettingThreads: models.SettingThreads{FileDetailThreads: 2},
		SettingStrm: models.SettingStrm{
			VideoExtArr: []string{".mkv", ".mp4"},
			MetaExtArr:  []string{".nfo"},
			AddPath:     3,
		},
	}
	if err := db.Db.Create(models.SettingsGlobal).Error; err != nil {
		t.Fatalf("创建测试设置失败: %v", err)
	}
	account := &models.Account{
		SourceType: models.SourceType115,
		Name:       "115",
		UserId:     "user-1",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	syncPath := &models.SyncPath{
		AccountId:    account.ID,
		SourceType:   models.SourceType115,
		LocalPath:    t.TempDir(),
		RemotePath:   "/remote",
		BaseCid:      "root",
		CustomConfig: true,
		SettingStrm: models.SettingStrm{
			StrmBaseUrl: "http://qms.local",
			VideoExtArr: []string{".mkv", ".mp4"},
			MetaExtArr:  []string{".nfo"},
			AddPath:     3,
		},
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}
	return account, syncPath
}

func newTestGenerationService(t *testing.T, syncPath *models.SyncPath, account *models.Account) *StrmGenerationService {
	t.Helper()
	service := NewStrmGenerationService()
	service.buildSyncer = func(_ *models.SyncPath, _ *models.Account) (*SyncStrm, error) {
		return &SyncStrm{
			Account:      account,
			SyncPathId:   syncPath.ID,
			SourcePath:   syncPath.RemotePath,
			SourcePathId: syncPath.BaseCid,
			TargetPath:   syncPath.LocalPath,
			Config: SyncStrmConfig{
				StrmBaseUrl:     syncPath.StrmBaseUrl,
				StrmUrlNeedPath: syncPath.AddPath,
				VideoExt:        syncPath.VideoExtArr,
				MetaExt:         syncPath.MetaExtArr,
			},
			SyncDriver: &fakeDirectoryScanDriver{},
		}, nil
	}
	service.acquireRefreshSubmission = func(context.Context) (func(), error) {
		return func() {}, nil
	}
	return service
}

func TestStrmGenerationServiceGenerateUpsertsSyncFileAndProcessesStrm(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	var processed *SyncFileCache
	var refreshSyncFile *models.SyncFile
	service.processStrmFile = func(_ *SyncStrm, file *SyncFileCache) error {
		processed = file
		return nil
	}
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.requestEmbyRefreshBySyncFile = func(syncFile *models.SyncFile) error {
		refreshSyncFile = syncFile
		return nil
	}

	result, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-1",
			ParentId:   "parent-1",
			PickCode:   "pick-1",
			Path:       "/remote",
			FileName:   "movie.mkv",
			FileSize:   1024,
			Sha1:       "sha1",
			Mtime:      123456,
		},
	})
	if err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	if processed == nil || processed.FileId != "file-1" || !processed.IsVideo {
		t.Fatalf("处理的文件 = %+v，期望 file-1 视频文件", processed)
	}
	if !result.Changed {
		t.Fatal("result.Changed = false，期望 true")
	}
	if refreshSyncFile == nil || refreshSyncFile.SyncPathId != syncPath.ID || refreshSyncFile.FileId != "file-1" {
		t.Fatalf("刷新 SyncFile = %+v，期望同步目录 %d 的 file-1", refreshSyncFile, syncPath.ID)
	}

	var syncFile models.SyncFile
	if err := db.Db.Where("sync_path_id = ? AND file_id = ?", syncPath.ID, "file-1").First(&syncFile).Error; err != nil {
		t.Fatalf("读取 SyncFile 失败: %v", err)
	}
	if syncFile.PickCode != "pick-1" || syncFile.LocalFilePath == "" {
		t.Fatalf("SyncFile = %+v，期望写入远端定位和本地 STRM 路径", syncFile)
	}
}

func TestStrmGenerationServiceGenerateCompletesDetailByFileID(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.detailByFileID = func(_ context.Context, _ *SyncStrm, fileID string) (*SyncFileCache, error) {
		if fileID != "file-2" {
			t.Fatalf("补详情 file_id = %s，期望 file-2", fileID)
		}
		return &SyncFileCache{
			FileId:     "file-2",
			ParentId:   "parent-2",
			FileType:   v115open.TypeFile,
			FileName:   "episode.mkv",
			Path:       "/remote/show",
			FileSize:   2048,
			MTime:      234567,
			PickCode:   "pick-2",
			Sha1:       "sha1-2",
			SourceType: models.SourceType115,
		}, nil
	}
	var processed *SyncFileCache
	service.processStrmFile = func(_ *SyncStrm, file *SyncFileCache) error {
		processed = file
		return nil
	}
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	if _, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{SyncPathId: syncPath.ID, AccountId: account.ID, FileId: "file-2"},
	}); err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	if processed == nil || processed.PickCode != "pick-2" || processed.Path != "/remote/show" {
		t.Fatalf("补齐后的文件 = %+v，期望包含 pick_code 和路径", processed)
	}
}

func TestStrmGenerationServiceGenerateUsesFullPathLocatorForPathBasedSources(t *testing.T) {
	tests := []struct {
		name               string
		sourceType         models.SourceType
		remoteFileID       string
		wantSyncFileID     string
		wantOpenListObject string
	}{
		{
			name:           "百度使用完整路径查询并持久化路径型 SyncFile ID",
			sourceType:     models.SourceTypeBaiduPan,
			remoteFileID:   "baidu-fs-id",
			wantSyncFileID: "/remote/show/movie.mkv",
		},
		{
			name:               "OpenList 使用完整路径查询并持久化补全身份",
			sourceType:         models.SourceTypeOpenList,
			remoteFileID:       "openlist-object-id",
			wantSyncFileID:     "/remote/show/movie.mkv",
			wantOpenListObject: "openlist-object-id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, syncPath := setupStrmGenerationServiceTestDB(t)
			account.SourceType = tt.sourceType
			syncPath.SourceType = tt.sourceType
			if err := db.Db.Save(account).Error; err != nil {
				t.Fatalf("更新测试账号来源失败: %v", err)
			}
			if err := db.Db.Save(syncPath).Error; err != nil {
				t.Fatalf("更新测试同步目录来源失败: %v", err)
			}

			service := newTestGenerationService(t, syncPath, account)
			service.detailByFileID = func(_ context.Context, _ *SyncStrm, locator string) (*SyncFileCache, error) {
				if locator != "/remote/show/movie.mkv" {
					t.Fatalf("路径型来源详情定位 = %q，期望完整路径", locator)
				}
				return &SyncFileCache{
					FileId:           tt.remoteFileID,
					ParentId:         "/remote/show",
					FileType:         v115open.TypeFile,
					FileName:         "movie.mkv",
					Path:             "/remote/show",
					FileSize:         2048,
					MTime:            234567,
					PickCode:         "baidu-fs-id",
					OpenlistObjectId: tt.wantOpenListObject,
					OpenlistSHA1:     "openlist-sha1",
					OpenlistMD5:      "openlist-md5",
					SourceType:       tt.sourceType,
				}, nil
			}
			service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
			service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
			service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

			if _, err := service.Generate(context.Background(), StrmGenerationInput{
				Task: &models.StrmGenerationTask{
					Source:     models.StrmGenerationSourceUploadCompleted,
					TaskType:   models.StrmGenerationTaskTypeFile,
					SyncPathId: syncPath.ID,
					AccountId:  account.ID,
					FileId:     tt.remoteFileID,
					ParentId:   "/remote/show",
					Path:       "/remote/show",
					FileName:   "movie.mkv",
				},
			}); err != nil {
				t.Fatalf("生成 STRM 失败: %v", err)
			}

			var syncFile models.SyncFile
			if err := db.Db.Where("sync_path_id = ? AND file_name = ?", syncPath.ID, "movie.mkv").First(&syncFile).Error; err != nil {
				t.Fatalf("读取生成的 SyncFile 失败: %v", err)
			}
			if syncFile.FileId != tt.wantSyncFileID {
				t.Fatalf("SyncFile.file_id = %q，期望 %q", syncFile.FileId, tt.wantSyncFileID)
			}
			if tt.sourceType == models.SourceTypeBaiduPan && syncFile.PickCode != "baidu-fs-id" {
				t.Fatalf("百度 SyncFile.pick_code = %q，期望 fs_id baidu-fs-id", syncFile.PickCode)
			}
			if tt.sourceType == models.SourceTypeOpenList &&
				(syncFile.OpenlistObjectId != tt.wantOpenListObject ||
					syncFile.OpenlistSHA1 != "openlist-sha1" ||
					syncFile.OpenlistMD5 != "openlist-md5") {
				t.Fatalf("OpenList SyncFile 身份补全丢失: %+v", syncFile)
			}
		})
	}
}

func TestStrmGenerationServiceGenerateSkipsDetailForCompleteBaiduUpload(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	account.SourceType = models.SourceTypeBaiduPan
	syncPath.SourceType = models.SourceTypeBaiduPan
	if err := db.Db.Save(account).Error; err != nil {
		t.Fatalf("更新测试账号来源失败: %v", err)
	}
	if err := db.Db.Save(syncPath).Error; err != nil {
		t.Fatalf("更新测试同步目录来源失败: %v", err)
	}

	service := newTestGenerationService(t, syncPath, account)
	service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
		t.Fatal("百度上传元数据齐全时不应查询完整路径详情")
		return nil, nil
	}
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	if _, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "baidu-new-fs-id",
			ParentId:   "/remote/show",
			PickCode:   "baidu-new-fs-id",
			Path:       "/remote/show",
			FileName:   "movie.mkv",
			FileSize:   2048,
			Mtime:      234567,
		},
	}); err != nil {
		t.Fatalf("百度上传元数据齐全时生成 STRM 失败: %v", err)
	}

	var syncFile models.SyncFile
	if err := db.Db.Where("sync_path_id = ? AND file_name = ?", syncPath.ID, "movie.mkv").First(&syncFile).Error; err != nil {
		t.Fatalf("读取百度 SyncFile 失败: %v", err)
	}
	if syncFile.FileId != "/remote/show/movie.mkv" || syncFile.PickCode != "baidu-new-fs-id" {
		t.Fatalf("百度完整上传保存的 SyncFile = %+v，期望路径型 file_id 和 fs_id pick_code", syncFile)
	}
}

func TestStrmGenerationServiceGenerateUsesFullPathWhenBaiduMtimeIsMissing(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	account.SourceType = models.SourceTypeBaiduPan
	syncPath.SourceType = models.SourceTypeBaiduPan
	if err := db.Db.Save(account).Error; err != nil {
		t.Fatalf("更新测试账号来源失败: %v", err)
	}
	if err := db.Db.Save(syncPath).Error; err != nil {
		t.Fatalf("更新测试同步目录来源失败: %v", err)
	}

	service := newTestGenerationService(t, syncPath, account)
	detailCalled := false
	service.detailByFileID = func(_ context.Context, _ *SyncStrm, locator string) (*SyncFileCache, error) {
		detailCalled = true
		if locator != "/remote/show/movie.mkv" {
			t.Fatalf("百度缺少 mtime 时详情定位 = %q，期望完整路径", locator)
		}
		return &SyncFileCache{
			FileId:     "baidu-new-fs-id",
			ParentId:   "/remote/show",
			FileType:   v115open.TypeFile,
			FileName:   "movie.mkv",
			Path:       "/remote/show",
			FileSize:   2048,
			MTime:      234567,
			PickCode:   "baidu-new-fs-id",
			SourceType: models.SourceTypeBaiduPan,
		}, nil
	}
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	if _, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "baidu-new-fs-id",
			ParentId:   "/remote/show",
			PickCode:   "baidu-new-fs-id",
			Path:       "/remote/show",
			FileName:   "movie.mkv",
			FileSize:   2048,
		},
	}); err != nil {
		t.Fatalf("百度缺少 mtime 时生成 STRM 失败: %v", err)
	}
	if !detailCalled {
		t.Fatal("百度缺少 mtime 时应按完整路径补详情")
	}
}

func TestStrmGenerationServiceGenerateUsesFullPathWhenOpenListObjectIDIsAbsent(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	account.SourceType = models.SourceTypeOpenList
	syncPath.SourceType = models.SourceTypeOpenList
	if err := db.Db.Save(account).Error; err != nil {
		t.Fatalf("更新测试账号来源失败: %v", err)
	}
	if err := db.Db.Save(syncPath).Error; err != nil {
		t.Fatalf("更新测试同步目录来源失败: %v", err)
	}

	service := newTestGenerationService(t, syncPath, account)
	detailCalled := false
	service.detailByFileID = func(_ context.Context, _ *SyncStrm, locator string) (*SyncFileCache, error) {
		detailCalled = true
		if locator != "/remote/show/movie.mkv" {
			t.Fatalf("OpenList 无对象 ID 时详情定位 = %q，期望完整路径", locator)
		}
		return &SyncFileCache{
			FileId:     "/remote/show/movie.mkv",
			ParentId:   "/remote/show",
			FileType:   v115open.TypeFile,
			FileName:   "movie.mkv",
			Path:       "/remote/show",
			FileSize:   2048,
			MTime:      234567,
			PickCode:   "/remote/show/movie.mkv",
			SourceType: models.SourceTypeOpenList,
		}, nil
	}
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	if _, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			ParentId:   "/remote/show",
			Path:       "/remote/show",
			FileName:   "movie.mkv",
		},
	}); err != nil {
		t.Fatalf("OpenList 无对象 ID 时生成 STRM 失败: %v", err)
	}
	if !detailCalled {
		t.Fatal("OpenList 无对象 ID 时仍应使用完整路径补齐远端详情")
	}
}

func TestRemoteDetailLocatorRejectsStableParentIDForPathSources(t *testing.T) {
	for _, sourceType := range []models.SourceType{models.SourceTypeBaiduPan, models.SourceTypeOpenList} {
		t.Run(string(sourceType), func(t *testing.T) {
			_, err := remoteDetailLocator(&SyncFileCache{
				SourceType: sourceType,
				ParentId:   "stable-parent-id",
				FileName:   "movie.mkv",
			})
			if err == nil {
				t.Fatal("路径型来源缺少父路径时不应将稳定父目录 ID 当作详情查询路径")
			}
		})
	}
}

func TestStrmGenerationServiceGenerateCompletesRemoteDetailWhenMtimeMissing(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	var detailCalled bool
	service.detailByFileID = func(_ context.Context, _ *SyncStrm, fileID string) (*SyncFileCache, error) {
		detailCalled = true
		if fileID != "file-mtime" {
			t.Fatalf("补详情 file_id = %s，期望 file-mtime", fileID)
		}
		return &SyncFileCache{
			FileId:     "file-mtime",
			ParentId:   "parent-mtime",
			FileType:   v115open.TypeFile,
			FileName:   "movie.mkv",
			Path:       "/remote",
			FileSize:   4096,
			MTime:      345678,
			PickCode:   "pick-mtime",
			Sha1:       "sha1-mtime",
			SourceType: models.SourceType115,
		}, nil
	}
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	if _, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-mtime",
			ParentId:   "parent-mtime",
			PickCode:   "pick-mtime",
			Path:       "/remote",
			FileName:   "movie.mkv",
			FileSize:   4096,
			Sha1:       "sha1-mtime",
		},
	}); err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	if !detailCalled {
		t.Fatal("远端时间缺失时应按 file_id 补齐远端详情")
	}

	var syncFile models.SyncFile
	if err := db.Db.Where("sync_path_id = ? AND file_id = ?", syncPath.ID, "file-mtime").First(&syncFile).Error; err != nil {
		t.Fatalf("读取 SyncFile 失败: %v", err)
	}
	if syncFile.MTime != 345678 {
		t.Fatalf("SyncFile.MTime=%d，期望补齐远端时间 345678", syncFile.MTime)
	}
}

func TestStrmGenerationServiceGenerateSkipsRefreshWhenStrmUnchanged(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	var refreshCalled bool
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error {
		refreshCalled = true
		return nil
	}

	result, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-3",
			ParentId:   "parent-3",
			PickCode:   "pick-3",
			Path:       "/remote",
			FileName:   "movie.mkv",
		},
	})
	if err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	if result.Changed {
		t.Fatal("result.Changed = true，期望 false")
	}
	if refreshCalled {
		t.Fatal("STRM 无变化时不应提交 Emby 刷新")
	}
}

func TestStrmGenerationServiceDownloadsMatchedMetadata(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	syncPath.MetaExtArr = []string{".nfo", ".jpg"}
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
	service.buildSyncer = func(_ *models.SyncPath, _ *models.Account) (*SyncStrm, error) {
		return &SyncStrm{
			Account:      account,
			SyncPathId:   syncPath.ID,
			SourcePath:   syncPath.RemotePath,
			SourcePathId: syncPath.BaseCid,
			TargetPath:   syncPath.LocalPath,
			Config: SyncStrmConfig{
				StrmBaseUrl:     syncPath.StrmBaseUrl,
				StrmUrlNeedPath: syncPath.AddPath,
				VideoExt:        syncPath.VideoExtArr,
				MetaExt:         syncPath.MetaExtArr,
			},
			SyncDriver: &fakeDirectoryScanDriver{
				filesByID: map[string][]*SyncFileCache{
					"parent-1": {
						{
							FileId:     "nfo-1",
							ParentId:   "parent-1",
							FileName:   "movie.nfo",
							Path:       "/remote",
							PickCode:   "pick-nfo",
							SourceType: models.SourceType115,
							FileType:   v115open.TypeFile,
						},
						{
							FileId:     "thumb-1",
							ParentId:   "parent-1",
							FileName:   "movie-thumb.jpg",
							Path:       "/remote",
							PickCode:   "pick-thumb",
							SourceType: models.SourceType115,
							FileType:   v115open.TypeFile,
						},
						{
							FileId:     "poster-1",
							ParentId:   "parent-1",
							FileName:   "poster.jpg",
							Path:       "/remote",
							PickCode:   "pick-poster",
							SourceType: models.SourceType115,
							FileType:   v115open.TypeFile,
						},
					},
				},
			},
		}, nil
	}

	_, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:       models.StrmGenerationSourceWebhook,
			TaskType:     models.StrmGenerationTaskTypeFile,
			SyncPathId:   syncPath.ID,
			AccountId:    account.ID,
			FileId:       "file-1",
			ParentId:     "parent-1",
			PickCode:     "pick-video",
			Path:         "/remote",
			FileName:     "movie.mkv",
			DownloadMeta: true,
		},
	})
	if err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	var downloads []models.DbDownloadTask
	if err := db.Db.Order("file_name ASC").Find(&downloads).Error; err != nil {
		t.Fatalf("读取下载任务失败: %v", err)
	}
	gotNames := []string{}
	for _, task := range downloads {
		gotNames = append(gotNames, task.FileName)
	}
	wantNames := []string{"movie-thumb.jpg", "movie.nfo"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("下载任务文件名 = %v，期望 %v", gotNames, wantNames)
	}
}

func TestStrmGenerationServiceIgnoresDownloadMetaForNonWebhookTask(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	syncPath.MetaExtArr = []string{".nfo"}
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
	service.buildSyncer = func(_ *models.SyncPath, _ *models.Account) (*SyncStrm, error) {
		return &SyncStrm{
			Account:      account,
			SyncPathId:   syncPath.ID,
			SourcePath:   syncPath.RemotePath,
			SourcePathId: syncPath.BaseCid,
			TargetPath:   syncPath.LocalPath,
			Config: SyncStrmConfig{
				StrmBaseUrl:     syncPath.StrmBaseUrl,
				StrmUrlNeedPath: syncPath.AddPath,
				VideoExt:        syncPath.VideoExtArr,
				MetaExt:         syncPath.MetaExtArr,
			},
			SyncDriver: &fakeDirectoryScanDriver{
				filesByID: map[string][]*SyncFileCache{
					"parent-guard": {
						{
							FileId:     "nfo-guard",
							ParentId:   "parent-guard",
							FileName:   "movie.nfo",
							Path:       "/remote",
							PickCode:   "pick-nfo-guard",
							SourceType: models.SourceType115,
							FileType:   v115open.TypeFile,
						},
					},
				},
			},
		}, nil
	}

	result, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:       models.StrmGenerationSourceUploadCompleted,
			TaskType:     models.StrmGenerationTaskTypeFile,
			SyncPathId:   syncPath.ID,
			AccountId:    account.ID,
			FileId:       "file-guard",
			ParentId:     "parent-guard",
			PickCode:     "pick-video-guard",
			Path:         "/remote",
			FileName:     "movie.mkv",
			DownloadMeta: true,
		},
	})
	if err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	if result.NewMeta != 0 {
		t.Fatalf("非 Webhook 任务新增元数据数 = %d，期望 0", result.NewMeta)
	}

	var count int64
	if err := db.Db.Model(&models.DbDownloadTask{}).Count(&count).Error; err != nil {
		t.Fatalf("统计下载任务失败: %v", err)
	}
	if count != 0 {
		t.Fatalf("非 Webhook 任务创建下载任务数 = %d，期望 0", count)
	}
}

func TestStrmGenerationServiceWebhookRefreshRequiresEnabledChangeOrNewMetadata(t *testing.T) {
	tests := []struct {
		name        string
		refreshEmby bool
		compare     int
	}{
		{name: "刷新开关关闭时 STRM 变更也不刷新", refreshEmby: false, compare: 0},
		{name: "刷新开关开启但 STRM 未变且无新增元数据时不刷新", refreshEmby: true, compare: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, syncPath := setupStrmGenerationServiceTestDB(t)
			service := newTestGenerationService(t, syncPath, account)
			service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return tt.compare }
			service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
			service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error {
				t.Fatal("Webhook file 不应走旧逐文件 Emby 刷新入口")
				return nil
			}
			service.resolveRefreshTarget = func(*models.SyncFile) (models.EmbyRefreshTarget, error) {
				t.Fatal("未满足刷新条件时不应解析 Emby 刷新目标")
				return models.EmbyRefreshTarget{}, nil
			}
			service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error {
				t.Fatal("未满足刷新条件时不应提交 Emby 刷新目标")
				return nil
			}

			if _, err := service.Generate(context.Background(), StrmGenerationInput{
				Task: &models.StrmGenerationTask{
					Source:      models.StrmGenerationSourceWebhook,
					TaskType:    models.StrmGenerationTaskTypeFile,
					SyncPathId:  syncPath.ID,
					AccountId:   account.ID,
					FileId:      "file-gate",
					ParentId:    "parent-gate",
					PickCode:    "pick-gate",
					Path:        "/remote",
					FileName:    "movie.mkv",
					RefreshEmby: tt.refreshEmby,
				},
			}); err != nil {
				t.Fatalf("生成 STRM 失败: %v", err)
			}
		})
	}
}

func TestStrmGenerationServiceDefaultBuilderDoesNotCreateSyncRecord(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := NewStrmGenerationService()
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error {
		t.Fatal("STRM 无变化时不应写入文件")
		return nil
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error {
		t.Fatal("STRM 无变化时不应提交 Emby 刷新")
		return nil
	}

	if _, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-no-sync",
			ParentId:   "parent-no-sync",
			PickCode:   "pick-no-sync",
			Path:       "/remote",
			FileName:   "movie.mkv",
			FileSize:   1024,
			Sha1:       "sha1-no-sync",
			Mtime:      123456,
		},
	}); err != nil {
		t.Fatalf("生成 STRM 后处理失败: %v", err)
	}

	var syncCount int64
	if err := db.Db.Model(&models.Sync{}).Count(&syncCount).Error; err != nil {
		t.Fatalf("统计同步记录失败: %v", err)
	}
	if syncCount != 0 {
		t.Fatalf("同步记录数量 = %d，期望目录监控上传后处理不创建同步记录", syncCount)
	}
}

func TestStrmGenerationServiceDefaultWriterComparesStrmOnce(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	var logBuf bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logBuf, "", 0)}

	existingStrmPath := filepath.Join(syncPath.LocalPath, "remote", "movie.strm")
	if err := os.MkdirAll(filepath.Dir(existingStrmPath), 0o755); err != nil {
		t.Fatalf("创建已有 STRM 目录失败: %v", err)
	}
	existingContent := "http://qms.local/115/url/video.mkv?pickcode=old-pick&userid=user-1"
	if err := os.WriteFile(existingStrmPath, []byte(existingContent), 0o644); err != nil {
		t.Fatalf("创建已有 STRM 失败: %v", err)
	}

	service := NewStrmGenerationService()
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	result, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-compare-once",
			ParentId:   "parent-compare-once",
			PickCode:   "new-pick",
			Path:       "/remote",
			FileName:   "movie.mkv",
			FileSize:   1024,
			Sha1:       "sha1",
			Mtime:      123456,
		},
	})
	if err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	if !result.Changed {
		t.Fatal("result.Changed = false，期望 true")
	}

	logOutput := logBuf.String()
	if count := strings.Count(logOutput, "STRM 内容 PickCode 与本地不一致"); count != 1 {
		t.Fatalf("PickCode 差异日志数量 = %d，期望 1，实际日志：%s", count, logOutput)
	}
}

func TestStrmGenerationServiceCleansOldStrmAfterRemotePathChanges(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	oldStrmPath := filepath.Join(syncPath.LocalPath, "old", "movie.strm")
	if err := os.MkdirAll(filepath.Dir(oldStrmPath), 0o755); err != nil {
		t.Fatalf("创建旧 STRM 目录失败: %v", err)
	}
	if err := os.WriteFile(oldStrmPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("创建旧 STRM 失败: %v", err)
	}
	if err := db.Db.Create(&models.SyncFile{
		AccountId:     account.ID,
		SyncPathId:    syncPath.ID,
		SourceType:    models.SourceType115,
		FileId:        "file-move",
		ParentId:      "old-parent",
		FileName:      "movie.mkv",
		Path:          "/old",
		PickCode:      "pick-move",
		LocalFilePath: filepath.ToSlash(oldStrmPath),
		IsVideo:       true,
	}).Error; err != nil {
		t.Fatalf("创建旧 SyncFile 失败: %v", err)
	}

	newStrmPath := filepath.Join(syncPath.LocalPath, "remote", "movie.strm")
	service := newTestGenerationService(t, syncPath, account)
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error {
		if err := os.MkdirAll(filepath.Dir(newStrmPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(newStrmPath, []byte("new"), 0o644)
	}
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	result, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-move",
			ParentId:   "new-parent",
			PickCode:   "pick-move",
			Path:       "/remote",
			FileName:   "movie.mkv",
		},
	})
	if err != nil {
		t.Fatalf("生成 STRM 失败: %v", err)
	}
	if !result.Changed {
		t.Fatal("result.Changed = false，期望 true")
	}
	if _, err := os.Stat(oldStrmPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("旧 STRM 状态错误: %v，期望已删除", err)
	}
	if _, err := os.Stat(newStrmPath); err != nil {
		t.Fatalf("新 STRM 应保留: %v", err)
	}
}

func TestStrmGenerationServiceKeepsNewStrmWhenOldPathCleanupFails(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	oldDir := filepath.Join(syncPath.LocalPath, "old.strm")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("创建旧路径目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("创建旧路径占用文件失败: %v", err)
	}
	if err := db.Db.Create(&models.SyncFile{
		AccountId:     account.ID,
		SyncPathId:    syncPath.ID,
		SourceType:    models.SourceType115,
		FileId:        "file-4",
		ParentId:      "old-parent",
		FileName:      "old.mkv",
		Path:          "/old",
		PickCode:      "pick-4",
		LocalFilePath: oldDir,
		IsVideo:       true,
	}).Error; err != nil {
		t.Fatalf("创建旧 SyncFile 失败: %v", err)
	}

	newStrmPath := filepath.Join(syncPath.LocalPath, "remote", "new.strm")
	service := newTestGenerationService(t, syncPath, account)
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error {
		if err := os.MkdirAll(filepath.Dir(newStrmPath), 0o755); err != nil {
			return err
		}
		return os.WriteFile(newStrmPath, []byte("new"), 0o644)
	}
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	_, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-4",
			ParentId:   "new-parent",
			PickCode:   "pick-4",
			Path:       "/remote",
			FileName:   "new.mkv",
		},
	})
	if err != nil {
		t.Fatalf("旧路径清理失败不应阻断 STRM 生成: %v", err)
	}
	if _, statErr := os.Stat(newStrmPath); statErr != nil {
		t.Fatalf("新 STRM 不应因旧路径清理失败被删除: %v", statErr)
	}
	if _, statErr := os.Stat(oldDir); statErr != nil {
		t.Fatalf("旧路径清理失败后旧路径应保持原状: %v", statErr)
	}
	var got models.SyncFile
	if err := db.Db.Where("file_id = ? AND pick_code = ?", "file-4", "pick-4").First(&got).Error; err != nil {
		t.Fatalf("读取更新后的 SyncFile 失败: %v", err)
	}
	if filepath.Clean(got.LocalFilePath) != filepath.Clean(newStrmPath) {
		t.Fatalf("SyncFile.LocalFilePath = %s，期望保存为新 STRM 路径 %s", got.LocalFilePath, newStrmPath)
	}
}

func TestProcessPendingStrmGenerationTasksMarksCompletedAndFailed(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, file *SyncFileCache) int {
		if file.FileId == "file-fail" {
			return 0
		}
		return 1
	}
	service.processStrmFile = func(_ *SyncStrm, file *SyncFileCache) error {
		if file.FileId == "file-fail" {
			return errors.New("写入失败")
		}
		return nil
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	completed, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:     models.StrmGenerationSourceUploadCompleted,
		TaskType:   models.StrmGenerationTaskTypeFile,
		SyncPathId: syncPath.ID,
		AccountId:  account.ID,
		FileId:     "file-ok",
		ParentId:   "parent-ok",
		PickCode:   "pick-ok",
		Path:       "/remote",
		FileName:   "ok.mkv",
	})
	if err != nil {
		t.Fatalf("创建成功任务失败: %v", err)
	}
	failed, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:     models.StrmGenerationSourceUploadCompleted,
		TaskType:   models.StrmGenerationTaskTypeFile,
		SyncPathId: syncPath.ID,
		AccountId:  account.ID,
		FileId:     "file-fail",
		ParentId:   "parent-fail",
		PickCode:   "pick-fail",
		Path:       "/remote",
		FileName:   "fail.mkv",
	})
	if err != nil {
		t.Fatalf("创建失败任务失败: %v", err)
	}

	processed, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 10)
	if err != nil {
		t.Fatalf("处理待生成 STRM 任务失败: %v", err)
	}
	if processed != 2 {
		t.Fatalf("处理任务数 = %d，期望 2", processed)
	}

	var completedTask models.StrmGenerationTask
	if err := db.Db.First(&completedTask, completed.ID).Error; err != nil {
		t.Fatalf("读取完成任务失败: %v", err)
	}
	if completedTask.Status != models.StrmGenerationStatusCompleted || completedTask.LastError != "" {
		t.Fatalf("完成任务状态 = %+v，期望 completed 且无错误", completedTask)
	}
	var failedTask models.StrmGenerationTask
	if err := db.Db.First(&failedTask, failed.ID).Error; err != nil {
		t.Fatalf("读取失败任务失败: %v", err)
	}
	if failedTask.Status != models.StrmGenerationStatusFailed || failedTask.RetryCount != 1 || failedTask.LastError == "" {
		t.Fatalf("失败任务状态 = %+v，期望 failed/retry_count=1/last_error", failedTask)
	}
}

func TestStrmGenerationWorkerStopReturnsAndAllowsRestart(t *testing.T) {
	setupStrmGenerationServiceTestDB(t)
	t.Cleanup(func() {
		StopStrmGenerationWorker()
		strmGenerationWorkerState.mu.Lock()
		strmGenerationWorkerState.stopping = false
		strmGenerationWorkerState.mu.Unlock()
	})

	InitStrmGenerationWorker()
	assertStopStrmGenerationWorkerReturns(t)

	InitStrmGenerationWorker()
	assertStopStrmGenerationWorkerReturns(t)
}

func TestWait115RateLimitRetryReturnsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err := wait115RateLimitRetry(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("限流等待取消 err = %v，期望 context.Canceled", err)
	}
	if time.Since(started) > 100*time.Millisecond {
		t.Fatal("限流等待未及时响应取消")
	}
}

func TestAcquireStrmGenerationRefreshSubmissionRejectsWhenStopping(t *testing.T) {
	strmGenerationWorkerState.mu.Lock()
	previousStopping := strmGenerationWorkerState.stopping
	strmGenerationWorkerState.stopping = true
	strmGenerationWorkerState.mu.Unlock()
	t.Cleanup(func() {
		strmGenerationWorkerState.mu.Lock()
		strmGenerationWorkerState.stopping = previousStopping
		strmGenerationWorkerState.mu.Unlock()
	})

	_, err := acquireStrmGenerationRefreshSubmission(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("停止期间获取刷新提交许可 err = %v，期望 context.Canceled", err)
	}
}

func TestStopStrmGenerationWorkerCancelsBeforeWaitingForRefreshSubmission(t *testing.T) {
	StopStrmGenerationWorker()
	strmGenerationWorkerState.mu.Lock()
	strmGenerationWorkerState.stopping = false
	cancelled := make(chan struct{})
	var cancelOnce sync.Once
	strmGenerationWorkerState.cancel = func() { cancelOnce.Do(func() { close(cancelled) }) }
	strmGenerationWorkerState.mu.Unlock()
	t.Cleanup(func() {
		StopStrmGenerationWorker()
		strmGenerationWorkerState.mu.Lock()
		strmGenerationWorkerState.cancel = nil
		strmGenerationWorkerState.stopping = false
		strmGenerationWorkerState.mu.Unlock()
	})

	release, err := acquireStrmGenerationRefreshSubmission(context.Background())
	if err != nil {
		t.Fatalf("获取刷新提交许可失败: %v", err)
	}
	done := make(chan struct{})
	go func() {
		StopStrmGenerationWorker()
		close(done)
	}()

	select {
	case <-cancelled:
	case <-time.After(100 * time.Millisecond):
		release()
		<-done
		t.Fatal("StopStrmGenerationWorker 应先发出 cancel，不应等待刷新提交许可释放")
	}

	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("StopStrmGenerationWorker 释放刷新提交许可后仍未返回")
	}
}

func assertStopStrmGenerationWorkerReturns(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		StopStrmGenerationWorker()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("停止 STRM 生成 worker 超时")
	}
}

func TestProcessPendingStrmGenerationTasksKeepsTaskRetryableWhenContextCancelled(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	task, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:     models.StrmGenerationSourceUploadCompleted,
		TaskType:   models.StrmGenerationTaskTypeFile,
		SyncPathId: syncPath.ID,
		AccountId:  account.ID,
		FileId:     "cancelled-file",
	})
	if err != nil {
		t.Fatalf("创建 STRM 任务失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := newTestGenerationService(t, syncPath, account)
	service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
		cancel()
		return nil, context.Canceled
	}

	processed, err := ProcessPendingStrmGenerationTasks(ctx, service, 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("处理取消任务 err = %v，期望 context.Canceled", err)
	}
	if processed != 1 {
		t.Fatalf("处理任务数 = %d，期望 1", processed)
	}

	var got models.StrmGenerationTask
	if err := db.Db.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读取 STRM 任务失败: %v", err)
	}
	if got.Status != models.StrmGenerationStatusRunning || got.RetryCount != 0 || got.LastError != "" {
		t.Fatalf("取消后的 STRM 任务 = %+v，期望保持 running 且未记录失败", got)
	}
}

func TestCompleteStrmGenerationTaskAfterSideEffectsSkipsCompletionWhenContextCancelled(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	task, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:     models.StrmGenerationSourceUploadCompleted,
		TaskType:   models.StrmGenerationTaskTypeFile,
		SyncPathId: syncPath.ID,
		AccountId:  account.ID,
		FileId:     "finalizing-file",
		Status:     models.StrmGenerationStatusFinalizing,
	})
	if err != nil {
		t.Fatalf("创建收尾 STRM 任务失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = completeStrmGenerationTaskAfterSideEffects(ctx, newTestGenerationService(t, syncPath, account), task)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("收尾取消任务 err = %v，期望 context.Canceled", err)
	}
	var got models.StrmGenerationTask
	if err := db.Db.First(&got, task.ID).Error; err != nil {
		t.Fatalf("读取收尾 STRM 任务失败: %v", err)
	}
	if got.Status != models.StrmGenerationStatusFinalizing {
		t.Fatalf("取消后的收尾任务 status = %s，期望 finalizing", got.Status)
	}
}

func TestProcessPendingStrmGenerationTasksDelaysParentRefreshUntilAllChildrenDone(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:      models.StrmGenerationSourceWebhook,
		TaskType:    models.StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  syncPath.ID,
		AccountId:   account.ID,
		RefreshEmby: true,
		TotalItems:  2,
		Status:      models.StrmGenerationStatusCompleted,
	})
	if err != nil {
		t.Fatalf("创建父任务失败: %v", err)
	}
	for _, fileID := range []string{"file-1", "file-2"} {
		if _, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
			Source:       models.StrmGenerationSourceWebhook,
			TaskType:     models.StrmGenerationTaskTypeFile,
			ParentTaskId: parent.ID,
			SyncPathId:   syncPath.ID,
			AccountId:    account.ID,
			FileId:       fileID,
			ParentId:     "parent-1",
			PickCode:     "pick-" + fileID,
			Path:         "/remote",
			FileName:     fileID + ".mkv",
			RefreshEmby:  true,
		}); err != nil {
			t.Fatalf("创建子任务失败: %v", err)
		}
	}

	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error {
		t.Fatal("Webhook 父任务路径不应逐文件提交 Emby 刷新")
		return nil
	}
	service.resolveRefreshTarget = func(_ *models.SyncFile) (models.EmbyRefreshTarget, error) {
		return models.EmbyRefreshTarget{
			TargetType:        models.EmbyRefreshTargetTypeItem,
			ItemID:            "season-1",
			ItemName:          "第一季",
			ItemType:          "Season",
			Recursive:         true,
			FallbackLibraryId: "lib-tv",
		}, nil
	}
	var submitted int
	service.requestEmbyRefreshTargets = func(_ uint, targets []models.EmbyRefreshTarget) error {
		submitted++
		if len(targets) != 1 || targets[0].ItemID != "season-1" {
			t.Fatalf("提交目标 = %+v，期望单个 season-1", targets)
		}
		return nil
	}

	processed, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 1)
	if err != nil {
		t.Fatalf("处理第一批失败: %v", err)
	}
	if processed != 1 || submitted != 0 {
		t.Fatalf("第一批 processed=%d submitted=%d，期望处理一个且不提交", processed, submitted)
	}
	processed, err = ProcessPendingStrmGenerationTasks(context.Background(), service, 10)
	if err != nil {
		t.Fatalf("处理第二批失败: %v", err)
	}
	if processed != 1 || submitted != 1 {
		t.Fatalf("第二批 processed=%d submitted=%d，期望最后一个子任务完成后提交一次", processed, submitted)
	}
}

func TestProcessPendingStrmGenerationTasksKeepsChildRetryableWhenParentRefreshFails(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:      models.StrmGenerationSourceWebhook,
		TaskType:    models.StrmGenerationTaskTypeBatchFiles,
		SyncPathId:  syncPath.ID,
		AccountId:   account.ID,
		RefreshEmby: true,
		TotalItems:  1,
		Status:      models.StrmGenerationStatusWaitingChildren,
		RequestHash: "webhook:batch:refresh-fails",
	})
	if err != nil {
		t.Fatalf("创建父任务失败: %v", err)
	}
	child, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceWebhook,
		TaskType:     models.StrmGenerationTaskTypeFile,
		ParentTaskId: parent.ID,
		SyncPathId:   syncPath.ID,
		AccountId:    account.ID,
		FileId:       "file-refresh-fails",
		ParentId:     "parent-refresh-fails",
		PickCode:     "pick-refresh-fails",
		Path:         "/remote",
		FileName:     "movie.mkv",
		RefreshEmby:  true,
		RequestHash:  "webhook:batch:file-refresh-fails",
	})
	if err != nil {
		t.Fatalf("创建子任务失败: %v", err)
	}

	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.resolveRefreshTarget = func(_ *models.SyncFile) (models.EmbyRefreshTarget, error) {
		return models.EmbyRefreshTarget{
			TargetType:        models.EmbyRefreshTargetTypeItem,
			ItemID:            "season-refresh-fails",
			FallbackLibraryId: "lib-tv",
		}, nil
	}
	refreshAttempts := 0
	service.requestEmbyRefreshTargets = func(uint, []models.EmbyRefreshTarget) error {
		refreshAttempts++
		if refreshAttempts == 1 {
			return errors.New("emby refresh failed")
		}
		return nil
	}

	processed, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 1)
	if err == nil || !strings.Contains(err.Error(), "emby refresh failed") {
		t.Fatalf("父刷新失败应向上返回错误，processed=%d err=%v", processed, err)
	}

	var gotChild models.StrmGenerationTask
	if err := db.Db.First(&gotChild, child.ID).Error; err != nil {
		t.Fatalf("读取子任务失败: %v", err)
	}
	if gotChild.Status != models.StrmGenerationStatusFinalizing {
		t.Fatalf("父刷新失败时子任务 status = %s，期望 finalizing: %+v", gotChild.Status, gotChild)
	}
	var gotParent models.StrmGenerationTask
	if err := db.Db.First(&gotParent, parent.ID).Error; err != nil {
		t.Fatalf("读取父任务失败: %v", err)
	}
	if gotParent.Status != models.StrmGenerationStatusCompleted ||
		gotParent.AcceptedItems != 1 ||
		gotParent.ChangedItems != 1 ||
		gotParent.RefreshSubmitted {
		t.Fatalf("父刷新失败后父任务 = %+v，期望 completed 且进度只累计一次、未标记已刷新", gotParent)
	}

	processed, err = ProcessPendingStrmGenerationTasks(context.Background(), service, 1)
	if err != nil {
		t.Fatalf("重试父刷新失败任务失败: %v", err)
	}
	if processed != 1 {
		t.Fatalf("重试 processed=%d，期望处理 finalizing 子任务 1 个", processed)
	}
	if err := db.Db.First(&gotChild, child.ID).Error; err != nil {
		t.Fatalf("重试后读取子任务失败: %v", err)
	}
	if gotChild.Status != models.StrmGenerationStatusCompleted {
		t.Fatalf("重试父刷新成功后子任务 status = %s，期望 completed", gotChild.Status)
	}
	if err := db.Db.First(&gotParent, parent.ID).Error; err != nil {
		t.Fatalf("重试后读取父任务失败: %v", err)
	}
	if gotParent.AcceptedItems != 1 || gotParent.ChangedItems != 1 || !gotParent.RefreshSubmitted {
		t.Fatalf("重试后父任务 = %+v，期望未重复累计且已标记刷新", gotParent)
	}
}

func TestProcessPendingStrmGenerationTasksExpandsDirectoryScan(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.buildSyncer = func(_ *models.SyncPath, _ *models.Account) (*SyncStrm, error) {
		return &SyncStrm{
			Account:      account,
			SyncPathId:   syncPath.ID,
			SourcePath:   syncPath.RemotePath,
			SourcePathId: syncPath.BaseCid,
			TargetPath:   syncPath.LocalPath,
			Config: SyncStrmConfig{
				StrmBaseUrl:     syncPath.StrmBaseUrl,
				StrmUrlNeedPath: syncPath.AddPath,
				VideoExt:        syncPath.VideoExtArr,
				MetaExt:         syncPath.MetaExtArr,
			},
			SyncDriver: &fakeDirectoryScanDriver{
				filesByID: map[string][]*SyncFileCache{
					"dir-root": {
						{
							FileId:     "file-1",
							ParentId:   "dir-root",
							FileType:   v115open.TypeFile,
							FileName:   "movie.mkv",
							Path:       "/remote/show",
							FileSize:   1024,
							MTime:      101,
							PickCode:   "pick-1",
							Sha1:       "sha1-1",
							SourceType: models.SourceType115,
						},
						{
							FileId:     "meta-1",
							ParentId:   "dir-root",
							FileType:   v115open.TypeFile,
							FileName:   "poster.jpg",
							Path:       "/remote/show",
							FileSize:   20,
							MTime:      102,
							SourceType: models.SourceType115,
						},
						{
							FileId:     "dir-season",
							ParentId:   "dir-root",
							FileType:   v115open.TypeDir,
							FileName:   "Season 01",
							Path:       "/remote/show",
							SourceType: models.SourceType115,
						},
					},
					"dir-season": {
						{
							FileId:     "file-2",
							ParentId:   "dir-season",
							FileType:   v115open.TypeFile,
							FileName:   "episode.mp4",
							Path:       "/remote/show/Season 01",
							FileSize:   2048,
							MTime:      201,
							PickCode:   "pick-2",
							Sha1:       "sha1-2",
							SourceType: models.SourceType115,
						},
					},
				},
			},
		}, nil
	}
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:        models.StrmGenerationSourceWebhook,
		TaskType:      models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    syncPath.ID,
		AccountId:     account.ID,
		DownloadMeta:  true,
		RefreshEmby:   true,
		DirectoryPath: "/remote/show",
		RequestHash:   "webhook:directory:test",
	})
	if err != nil {
		t.Fatalf("创建目录扫描任务失败: %v", err)
	}

	processed, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 10)
	if err != nil {
		t.Fatalf("处理目录扫描任务失败: %v", err)
	}
	if processed != 1 {
		t.Fatalf("处理任务数 = %d，期望 1", processed)
	}

	var gotParent models.StrmGenerationTask
	if err := db.Db.First(&gotParent, parent.ID).Error; err != nil {
		t.Fatalf("读取目录扫描父任务失败: %v", err)
	}
	if gotParent.Status != models.StrmGenerationStatusWaitingChildren || gotParent.TotalItems != 2 || gotParent.AcceptedItems != 0 || gotParent.FailedItems != 0 {
		t.Fatalf("目录扫描父任务 = %+v，期望 waiting_children 且 total_items=2", gotParent)
	}

	var children []models.StrmGenerationTask
	if err := db.Db.Where("parent_task_id = ?", parent.ID).Order("file_id ASC").Find(&children).Error; err != nil {
		t.Fatalf("读取目录扫描子任务失败: %v", err)
	}
	if len(children) != 2 {
		t.Fatalf("子任务数量 = %d，期望 2: %+v", len(children), children)
	}
	if children[0].FileId != "file-1" || children[0].Path != "/remote/show" || children[0].FileName != "movie.mkv" {
		t.Fatalf("第一个子任务 = %+v，期望 movie.mkv", children[0])
	}
	if children[1].FileId != "file-2" || children[1].Path != "/remote/show/Season 01" || children[1].FileName != "episode.mp4" {
		t.Fatalf("第二个子任务 = %+v，期望 episode.mp4", children[1])
	}
	for _, child := range children {
		if child.DownloadMeta != gotParent.DownloadMeta || child.RefreshEmby != gotParent.RefreshEmby {
			t.Fatalf("目录扫描子任务未继承父任务开关: child=%+v parent=%+v", child, gotParent)
		}
	}
}

func TestProcessPendingStrmGenerationTasksExpandsDirectoryScanByDirectoryID(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.buildSyncer = func(_ *models.SyncPath, _ *models.Account) (*SyncStrm, error) {
		return &SyncStrm{
			Account:      account,
			SyncPathId:   syncPath.ID,
			SourcePath:   syncPath.RemotePath,
			SourcePathId: syncPath.BaseCid,
			TargetPath:   syncPath.LocalPath,
			Config: SyncStrmConfig{
				StrmBaseUrl:     syncPath.StrmBaseUrl,
				StrmUrlNeedPath: syncPath.AddPath,
				VideoExt:        syncPath.VideoExtArr,
				MetaExt:         syncPath.MetaExtArr,
			},
			SyncDriver: &fakeDirectoryScanDriver{
				filesByID: map[string][]*SyncFileCache{
					"dir-root": {
						{
							FileId:     "file-1",
							ParentId:   "dir-root",
							FileType:   v115open.TypeFile,
							FileName:   "movie.mkv",
							Path:       "/remote/show",
							FileSize:   1024,
							MTime:      101,
							PickCode:   "pick-1",
							Sha1:       "sha1-1",
							SourceType: models.SourceType115,
						},
					},
				},
				detailsByID: map[string]*SyncFileCache{
					"dir-root": {
						FileId:     "dir-root",
						ParentId:   "root",
						FileType:   v115open.TypeDir,
						FileName:   "show",
						Path:       "/remote",
						SourceType: models.SourceType115,
					},
				},
			},
		}, nil
	}
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:      models.StrmGenerationSourceWebhook,
		TaskType:    models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:  syncPath.ID,
		AccountId:   account.ID,
		DirectoryId: "dir-root",
		RequestHash: "webhook:directory:id-only",
	})
	if err != nil {
		t.Fatalf("创建目录扫描任务失败: %v", err)
	}

	processed, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 10)
	if err != nil {
		t.Fatalf("处理目录扫描任务失败: %v", err)
	}
	if processed != 1 {
		t.Fatalf("处理任务数 = %d，期望 1", processed)
	}

	var gotParent models.StrmGenerationTask
	if err := db.Db.First(&gotParent, parent.ID).Error; err != nil {
		t.Fatalf("读取目录扫描父任务失败: %v", err)
	}
	if gotParent.Status != models.StrmGenerationStatusWaitingChildren || gotParent.TotalItems != 1 {
		t.Fatalf("目录扫描父任务 = %+v，期望 waiting_children 且 total_items=1", gotParent)
	}

	var child models.StrmGenerationTask
	if err := db.Db.Where("parent_task_id = ?", parent.ID).First(&child).Error; err != nil {
		t.Fatalf("读取目录扫描子任务失败: %v", err)
	}
	if child.FileId != "file-1" || child.Path != "/remote/show" || child.FileName != "movie.mkv" {
		t.Fatalf("子任务 = %+v，期望 movie.mkv", child)
	}
}

func TestProcessPendingStrmGenerationTasksDirectoryScanParentFailsWhenChildFails(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.buildSyncer = func(_ *models.SyncPath, _ *models.Account) (*SyncStrm, error) {
		return &SyncStrm{
			Account:      account,
			SyncPathId:   syncPath.ID,
			SourcePath:   syncPath.RemotePath,
			SourcePathId: syncPath.BaseCid,
			TargetPath:   syncPath.LocalPath,
			Config: SyncStrmConfig{
				StrmBaseUrl:     syncPath.StrmBaseUrl,
				StrmUrlNeedPath: syncPath.AddPath,
				VideoExt:        syncPath.VideoExtArr,
				MetaExt:         syncPath.MetaExtArr,
			},
			SyncDriver: &fakeDirectoryScanDriver{
				filesByID: map[string][]*SyncFileCache{
					"dir-root": {
						{
							FileId:     "file-fail",
							ParentId:   "dir-root",
							FileType:   v115open.TypeFile,
							FileName:   "fail.mkv",
							Path:       "/remote/show",
							FileSize:   1024,
							MTime:      101,
							PickCode:   "pick-fail",
							Sha1:       "sha1-fail",
							SourceType: models.SourceType115,
						},
					},
				},
			},
		}, nil
	}
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:        models.StrmGenerationSourceWebhook,
		TaskType:      models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    syncPath.ID,
		AccountId:     account.ID,
		DirectoryPath: "/remote/show",
		RequestHash:   "webhook:directory:child-fails",
	})
	if err != nil {
		t.Fatalf("创建目录扫描任务失败: %v", err)
	}

	processed, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 1)
	if err != nil {
		t.Fatalf("展开目录扫描任务失败: %v", err)
	}
	if processed != 1 {
		t.Fatalf("展开处理数 = %d，期望 1", processed)
	}

	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 0 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error {
		return errors.New("写入失败")
	}
	processed, err = ProcessPendingStrmGenerationTasks(context.Background(), service, 10)
	if err != nil {
		t.Fatalf("处理目录扫描子任务失败: %v", err)
	}
	if processed != 1 {
		t.Fatalf("子任务处理数 = %d，期望 1", processed)
	}

	var gotParent models.StrmGenerationTask
	if err := db.Db.First(&gotParent, parent.ID).Error; err != nil {
		t.Fatalf("读取目录扫描父任务失败: %v", err)
	}
	if gotParent.Status != models.StrmGenerationStatusFailed ||
		gotParent.TotalItems != 1 ||
		gotParent.AcceptedItems != 0 ||
		gotParent.FailedItems != 1 {
		t.Fatalf("目录扫描父任务 = %+v，期望 failed 且 failed_items=1", gotParent)
	}
}

func TestProcessPendingStrmGenerationTasksDirectoryScanRetryReusesWaitingParent(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.buildSyncer = func(_ *models.SyncPath, _ *models.Account) (*SyncStrm, error) {
		return &SyncStrm{
			Account:      account,
			SyncPathId:   syncPath.ID,
			SourcePath:   syncPath.RemotePath,
			SourcePathId: syncPath.BaseCid,
			TargetPath:   syncPath.LocalPath,
			Config: SyncStrmConfig{
				StrmBaseUrl:     syncPath.StrmBaseUrl,
				StrmUrlNeedPath: syncPath.AddPath,
				VideoExt:        syncPath.VideoExtArr,
				MetaExt:         syncPath.MetaExtArr,
			},
			SyncDriver: &fakeDirectoryScanDriver{
				filesByID: map[string][]*SyncFileCache{
					"dir-root": {
						{
							FileId:     "file-1",
							ParentId:   "dir-root",
							FileType:   v115open.TypeFile,
							FileName:   "movie.mkv",
							Path:       "/remote/show",
							FileSize:   1024,
							MTime:      101,
							PickCode:   "pick-1",
							Sha1:       "sha1-1",
							SourceType: models.SourceType115,
						},
					},
				},
			},
		}, nil
	}
	const requestHash = "webhook:directory:retry-reuse"
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:        models.StrmGenerationSourceWebhook,
		TaskType:      models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    syncPath.ID,
		AccountId:     account.ID,
		DirectoryPath: "/remote/show",
		RequestHash:   requestHash,
	})
	if err != nil {
		t.Fatalf("创建目录扫描任务失败: %v", err)
	}
	if _, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 1); err != nil {
		t.Fatalf("展开目录扫描任务失败: %v", err)
	}

	reused, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:        models.StrmGenerationSourceWebhook,
		TaskType:      models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    syncPath.ID,
		AccountId:     account.ID,
		DirectoryPath: "/remote/show",
		RequestHash:   requestHash,
	})
	if err != nil {
		t.Fatalf("重复目录扫描任务入队失败: %v", err)
	}
	if reused.ID != parent.ID {
		t.Fatalf("重复目录扫描任务 ID = %d，期望复用父任务 %d", reused.ID, parent.ID)
	}

	var total int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).
		Where("task_type = ?", models.StrmGenerationTaskTypeDirectoryScan).
		Count(&total).Error; err != nil {
		t.Fatalf("统计目录扫描父任务失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("目录扫描父任务数量 = %d，期望 1", total)
	}
}

func TestBuildDirectoryScanChildTaskUsesShortRequestHashAndPreservesRemoteFields(t *testing.T) {
	longPath := "/remote/" + strings.Repeat("very-long-path-segment/", 20)
	longFileName := strings.Repeat("movie-", 60) + ".mkv"
	parent := &models.StrmGenerationTask{
		BaseModel:    models.BaseModel{ID: 42},
		Source:       models.StrmGenerationSourceWebhook,
		SyncPathId:   10,
		AccountId:    2,
		DownloadMeta: true,
		RefreshEmby:  true,
	}
	syncPath := &models.SyncPath{
		BaseModel: models.BaseModel{ID: 10},
		AccountId: 2,
	}
	file := &SyncFileCache{
		FileId:   "file-long",
		ParentId: "parent-long",
		PickCode: "pick-long",
		Path:     longPath,
		FileName: longFileName,
		FileSize: 1024,
		Sha1:     "sha1-long",
		MTime:    123,
	}

	task := buildDirectoryScanChildTask(parent, syncPath, file)

	if len(task.RequestHash) > 255 || !strings.HasPrefix(task.RequestHash, "directory_scan:file:v2:") {
		t.Fatalf("目录扫描子任务 request_hash = %s，期望短 v2 哈希", task.RequestHash)
	}
	if strings.Contains(task.RequestHash, longPath) || strings.Contains(task.RequestHash, longFileName) {
		t.Fatalf("目录扫描子任务 request_hash 不应包含明文长路径或文件名: %s", task.RequestHash)
	}
	if task.Path != longPath || task.FileName != longFileName {
		t.Fatalf("目录扫描子任务 path=%q file_name=%q，期望保留原始路径和文件名", task.Path, task.FileName)
	}
}

func TestProcessPendingStrmGenerationTasksCallsSourceCleanupAfterCompletedUploadTask(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	var cleanupUploadTaskIDs []uint
	oldCleanup := cleanupSourceAfterStrmSuccess
	cleanupSourceAfterStrmSuccess = func(uploadTaskID uint) error {
		cleanupUploadTaskIDs = append(cleanupUploadTaskIDs, uploadTaskID)
		return nil
	}
	t.Cleanup(func() {
		cleanupSourceAfterStrmSuccess = oldCleanup
	})

	if _, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceUploadCompleted,
		TaskType:     models.StrmGenerationTaskTypeFile,
		UploadTaskId: 42,
		SyncPathId:   syncPath.ID,
		AccountId:    account.ID,
		FileId:       "file-cleanup",
		ParentId:     "parent-cleanup",
		PickCode:     "pick-cleanup",
		Path:         "/remote",
		FileName:     "cleanup.mkv",
	}); err != nil {
		t.Fatalf("创建清理任务失败: %v", err)
	}
	if _, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 10); err != nil {
		t.Fatalf("处理 STRM 任务失败: %v", err)
	}
	if len(cleanupUploadTaskIDs) != 1 || cleanupUploadTaskIDs[0] != 42 {
		t.Fatalf("cleanup ids=%v，期望只清理上传任务 42", cleanupUploadTaskIDs)
	}
}

func TestProcessPendingStrmGenerationTasksCleansOnlyStrmTaskUploadID(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	if err := db.Db.AutoMigrate(&models.DirectoryUploadProcessedFile{}); err != nil {
		t.Fatalf("迁移目录监控处理记录表失败: %v", err)
	}
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int { return 1 }
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error { return nil }
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	var cleanupUploadTaskIDs []uint
	oldCleanup := cleanupSourceAfterStrmSuccess
	cleanupSourceAfterStrmSuccess = func(uploadTaskID uint) error {
		cleanupUploadTaskIDs = append(cleanupUploadTaskIDs, uploadTaskID)
		return nil
	}
	t.Cleanup(func() {
		cleanupSourceAfterStrmSuccess = oldCleanup
	})

	_, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceUploadCompleted,
		TaskType:     models.StrmGenerationTaskTypeFile,
		UploadTaskId: 42,
		SyncPathId:   syncPath.ID,
		AccountId:    account.ID,
		FileId:       "file-reused-cleanup",
		ParentId:     "parent-reused-cleanup",
		PickCode:     "pick-reused-cleanup",
		Path:         "/remote",
		FileName:     "reused-cleanup.mkv",
	})
	if err != nil {
		t.Fatalf("创建复用 STRM 任务失败: %v", err)
	}
	records := []*models.DirectoryUploadProcessedFile{
		{SourceKey: "source-cleanup-42", UploadTaskId: 42},
		{SourceKey: "source-cleanup-43", UploadTaskId: 43},
	}
	if err := db.Db.Create(&records).Error; err != nil {
		t.Fatalf("创建目录监控处理账本记录失败: %v", err)
	}

	if _, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 10); err != nil {
		t.Fatalf("处理 STRM 任务失败: %v", err)
	}

	got := make(map[uint]int, len(cleanupUploadTaskIDs))
	for _, uploadTaskID := range cleanupUploadTaskIDs {
		got[uploadTaskID]++
	}
	if got[42] != 1 || len(got) != 1 {
		t.Fatalf("cleanup ids=%v，期望只按 STRM 任务自身 upload_task_id 清理任务 42", cleanupUploadTaskIDs)
	}
}

func TestProcessPendingStrmGenerationTasksCopiesDirectoryUploadMetadataBeforeCleanup(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, _ *SyncFileCache) int {
		t.Fatal("元数据后处理不应比较 STRM 内容")
		return 1
	}
	service.processStrmFile = func(_ *SyncStrm, _ *SyncFileCache) error {
		t.Fatal("元数据后处理不应写入 STRM 文件")
		return nil
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	sourceDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "show", "Season 01", "episode.nfo")
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o777); err != nil {
		t.Fatalf("创建源文件目录失败: %v", err)
	}
	content := []byte("metadata")
	if err := os.WriteFile(sourcePath, content, 0o666); err != nil {
		t.Fatalf("写入源文件失败: %v", err)
	}

	uploadTask := &models.DbUploadTask{
		Source:              models.UploadSourceDirectoryMonitor,
		AccountId:           account.ID,
		SyncPathId:          syncPath.ID,
		SourceType:          models.SourceType115,
		LocalFullPath:       sourcePath,
		RelativePath:        "show/Season 01/episode.nfo",
		RemoteFullPath:      "/remote/show/Season 01/episode.nfo",
		RemotePathId:        "parent-meta",
		FileName:            "episode.nfo",
		Status:              models.UploadStatusCompleted,
		FileSize:            int64(len(content)),
		UploadResult:        models.UploadResultMultipartUploaded,
		RemoteFileId:        "file-meta",
		RemotePickCode:      "pick-meta",
		SourceCleanupStatus: models.UploadSourceCleanupStatusPending,
	}
	if err := db.Db.Create(uploadTask).Error; err != nil {
		t.Fatalf("创建目录监控上传任务失败: %v", err)
	}

	targetFile := (&SyncFileCache{
		FileId:     "file-meta",
		ParentId:   "parent-meta",
		FileType:   v115open.TypeFile,
		FileName:   "episode.nfo",
		Path:       "/remote/show/Season 01",
		PickCode:   "pick-meta",
		SourceType: models.SourceType115,
		IsMeta:     true,
	}).GetLocalFilePath(syncPath.LocalPath, syncPath.RemotePath)

	cleanupCalled := false
	oldCleanup := cleanupSourceAfterStrmSuccess
	cleanupSourceAfterStrmSuccess = func(uploadTaskID uint) error {
		cleanupCalled = true
		if uploadTaskID != uploadTask.ID {
			t.Fatalf("清理 upload_task_id = %d，期望 %d", uploadTaskID, uploadTask.ID)
		}
		got, err := os.ReadFile(targetFile)
		if err != nil {
			t.Fatalf("源文件清理前应已复制元数据到 STRM 路径: %v", err)
		}
		if string(got) != string(content) {
			t.Fatalf("复制后的元数据内容 = %q，期望 %q", got, content)
		}
		if _, err := os.Stat(sourcePath); err != nil {
			t.Fatalf("源文件清理前源文件应仍存在: %v", err)
		}
		return nil
	}
	t.Cleanup(func() {
		cleanupSourceAfterStrmSuccess = oldCleanup
	})

	if _, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceUploadCompleted,
		TaskType:     models.StrmGenerationTaskTypeFile,
		UploadTaskId: uploadTask.ID,
		SyncPathId:   syncPath.ID,
		AccountId:    account.ID,
		FileId:       "file-meta",
		ParentId:     "parent-meta",
		PickCode:     "pick-meta",
		Path:         "/remote/show/Season 01",
		FileName:     "episode.nfo",
		FileSize:     int64(len(content)),
	}); err != nil {
		t.Fatalf("创建元数据 STRM 后处理任务失败: %v", err)
	}
	if _, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 10); err != nil {
		t.Fatalf("处理 STRM 任务失败: %v", err)
	}
	if !cleanupCalled {
		t.Fatal("目录监控元数据任务完成后应触发源文件清理")
	}

	var syncFile models.SyncFile
	if err := db.Db.Where("sync_path_id = ? AND file_id = ?", syncPath.ID, "file-meta").First(&syncFile).Error; err != nil {
		t.Fatalf("读取元数据 SyncFile 失败: %v", err)
	}
	if !syncFile.IsMeta || syncFile.IsVideo || syncFile.LocalFilePath != targetFile {
		t.Fatalf("元数据 SyncFile = %+v，期望记录 STRM 路径 %s", syncFile, targetFile)
	}
}

func TestProcessPendingStrmGenerationTasksSkipsCleanupWhenFailedOrNoUploadTask(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(_ *SyncStrm, file *SyncFileCache) int {
		if file.FileId == "file-fail-cleanup" {
			return 0
		}
		return 1
	}
	service.processStrmFile = func(_ *SyncStrm, file *SyncFileCache) error {
		if file.FileId == "file-fail-cleanup" {
			return errors.New("写入失败")
		}
		return nil
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }

	cleanupCalled := false
	oldCleanup := cleanupSourceAfterStrmSuccess
	cleanupSourceAfterStrmSuccess = func(uploadTaskID uint) error {
		cleanupCalled = true
		return nil
	}
	t.Cleanup(func() {
		cleanupSourceAfterStrmSuccess = oldCleanup
	})

	if _, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:     models.StrmGenerationSourceUploadCompleted,
		TaskType:   models.StrmGenerationTaskTypeFile,
		SyncPathId: syncPath.ID,
		AccountId:  account.ID,
		FileId:     "file-no-upload",
		ParentId:   "parent-no-upload",
		PickCode:   "pick-no-upload",
		Path:       "/remote",
		FileName:   "no-upload.mkv",
	}); err != nil {
		t.Fatalf("创建无上传 ID 任务失败: %v", err)
	}
	if _, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceUploadCompleted,
		TaskType:     models.StrmGenerationTaskTypeFile,
		UploadTaskId: 43,
		SyncPathId:   syncPath.ID,
		AccountId:    account.ID,
		FileId:       "file-fail-cleanup",
		ParentId:     "parent-fail-cleanup",
		PickCode:     "pick-fail-cleanup",
		Path:         "/remote",
		FileName:     "fail-cleanup.mkv",
	}); err != nil {
		t.Fatalf("创建失败任务失败: %v", err)
	}
	if _, err := ProcessPendingStrmGenerationTasks(context.Background(), service, 10); err != nil {
		t.Fatalf("处理 STRM 任务失败: %v", err)
	}
	if cleanupCalled {
		t.Fatal("STRM 失败或无 upload_task_id 时不应触发源文件清理")
	}
}

func TestStrmRemotePathWithinRoot(t *testing.T) {
	tests := []struct {
		name       string
		remotePath string
		basePath   string
		want       bool
	}{
		{name: "根目录包含子路径", remotePath: "/remote/show", basePath: "/", want: true},
		{name: "同级前缀不误判", remotePath: "/remote2/show", basePath: "/remote", want: false},
		{name: "同步目录包含自身", remotePath: "/remote", basePath: "/remote", want: true},
		{name: "同步目录包含子路径", remotePath: "/remote/show", basePath: "/remote", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := strmRemotePathWithin(tt.remotePath, tt.basePath); got != tt.want {
				t.Fatalf("strmRemotePathWithin(%q, %q) = %v，期望 %v", tt.remotePath, tt.basePath, got, tt.want)
			}
		})
	}
}

type fakeDirectoryScanDriver struct {
	filesByID   map[string][]*SyncFileCache
	detailsByID map[string]*SyncFileCache
}

func (driver *fakeDirectoryScanDriver) GetNetFileFiles(_ context.Context, _ string, parentPathID string) ([]*SyncFileCache, error) {
	return driver.filesByID[parentPathID], nil
}

func (driver *fakeDirectoryScanDriver) GetPathIdByPath(_ context.Context, path string) (string, error) {
	switch path {
	case "/remote/show":
		return "dir-root", nil
	case "/remote/show/Season 01":
		return "dir-season", nil
	default:
		return "", nil
	}
}

func (driver *fakeDirectoryScanDriver) SetSyncStrm(*SyncStrm) {}

func (driver *fakeDirectoryScanDriver) MakeStrmContent(*SyncFileCache) string { return "" }

func (driver *fakeDirectoryScanDriver) CreateDirRecursively(context.Context, string) (string, string, error) {
	return "", "", nil
}

func (driver *fakeDirectoryScanDriver) GetTotalFileCount(context.Context) (int64, string, error) {
	return 0, "", nil
}

func (driver *fakeDirectoryScanDriver) GetDirsByPathId(context.Context, string) ([]pathQueueItem, error) {
	return nil, nil
}

func (driver *fakeDirectoryScanDriver) GetFilesByPathId(context.Context, string, int, int) ([]v115open.File, error) {
	return nil, nil
}

func (driver *fakeDirectoryScanDriver) GetFilesByPathMtime(context.Context, string, int, int, int64) (*baidupan.FileListAllResponse, error) {
	return nil, nil
}

func (driver *fakeDirectoryScanDriver) DetailByFileId(_ context.Context, fileID string) (*SyncFileCache, error) {
	return driver.detailsByID[fileID], nil
}

func (driver *fakeDirectoryScanDriver) DeleteFile(context.Context, string, []string) error {
	return nil
}
