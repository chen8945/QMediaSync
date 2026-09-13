package syncstrm

import (
	"context"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestStrmGenerationServiceRejectsPlaybackFiles(t *testing.T) {
	for _, tt := range []struct {
		name       string
		source     models.SourceType
		path       string
		needDetail bool
		wantError  bool
	}{
		{name: "直接指定副本", source: models.SourceType115, path: "/多端播放", wantError: true},
		{name: "补齐路径后发现副本", source: models.SourceType115, path: "多端播放/child", needDetail: true, wantError: true},
		{name: "同名非根目录", source: models.SourceType115, path: "/Media/多端播放"},
		{name: "百度同名目录", source: models.SourceTypeBaiduPan, path: "/多端播放"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account, syncPath := setupStrmExclusionTestDB(t)
			account.SourceType, syncPath.SourceType = tt.source, tt.source
			if err := db.Db.Save(account).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Save(syncPath).Error; err != nil {
				t.Fatal(err)
			}
			service := newTestGenerationService(t, syncPath, account)
			processed := false
			service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 0 }
			service.processStrmFile = func(*SyncStrm, *SyncFileCache) error {
				processed = true
				return nil
			}
			task := &models.StrmGenerationTask{
				Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile,
				SyncPathId: syncPath.ID, AccountId: account.ID, FileId: "file", ParentId: "parent",
				FileName: "movie.mkv", Path: tt.path, PickCode: "pick", Sha1: "sha1", FileSize: 1024, Mtime: 100,
			}
			if tt.needDetail {
				task.Path = ""
				service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
					return &SyncFileCache{Path: tt.path, SourceType: tt.source}, nil
				}
			}
			result, err := service.Generate(context.Background(), StrmGenerationInput{Task: task})
			if tt.wantError {
				if err == nil || !strings.Contains(err.Error(), "多端播放临时目录") || result != nil || processed {
					t.Fatalf("临时文件应被拒绝：result=%+v err=%v processed=%v", result, err, processed)
				}
			} else if err != nil || result == nil || !result.Changed || !processed {
				t.Fatalf("普通文件应继续生成：result=%+v err=%v processed=%v", result, err, processed)
			}
			var count int64
			if err := db.Db.Model(&models.SyncFile{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if (count == 0) != tt.wantError {
				t.Fatalf("SyncFile 记录数 = %d，期望拒绝=%v", count, tt.wantError)
			}
		})
	}
}

func TestStrmGenerationDirectoryScanExcludesPlaybackSubtree(t *testing.T) {
	for _, tt := range []struct {
		name   string
		source models.SourceType
		rootID string
		byID   bool
		want   int
	}{
		{name: "115 根目录扫描", source: models.SourceType115, rootID: "0", want: 1},
		{name: "直接选中临时目录", source: models.SourceType115, rootID: "playback"},
		{name: "仅有临时目录 ID", source: models.SourceType115, rootID: "playback", byID: true},
		{name: "其他来源同名目录", source: models.SourceTypeBaiduPan, rootID: "playback", want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account, syncPath := setupStrmExclusionTestDB(t)
			account.SourceType = tt.source
			syncPath.SourceType, syncPath.RemotePath, syncPath.BaseCid = tt.source, "/", "0"
			if err := db.Db.Save(account).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Save(syncPath).Error; err != nil {
				t.Fatal(err)
			}
			driver := &fakeDirectoryScanDriver{
				filesByID: map[string][]*SyncFileCache{
					"0": {
						{FileId: "playback", FileName: "多端播放", FileType: v115open.TypeDir},
						{FileId: "media", FileName: "Media", FileType: v115open.TypeDir},
					},
					"playback": {{FileId: "copy", FileName: "copy.mkv", FileType: v115open.TypeFile}},
					"media":    {{FileId: "nested", FileName: "多端播放", FileType: v115open.TypeDir}},
					"nested":   {{FileId: "movie", FileName: "movie.mkv", FileType: v115open.TypeFile}},
				},
				detailsByID: map[string]*SyncFileCache{
					"playback": {FileId: "playback", FileName: "多端播放", Path: "/", FileType: v115open.TypeDir, SourceType: tt.source},
				},
			}
			service := newTestGenerationService(t, syncPath, account)
			buildSyncer := service.buildSyncer
			service.buildSyncer = func(path *models.SyncPath, account *models.Account) (*SyncStrm, error) {
				syncer, err := buildSyncer(path, account)
				if err == nil {
					syncer.SyncDriver = driver
				}
				return syncer, err
			}
			path := "/"
			if tt.rootID == "playback" {
				path = "/多端播放"
			}
			if tt.byID {
				path = ""
			}
			task := &models.StrmGenerationTask{
				BaseModel: models.BaseModel{ID: 1}, Source: models.StrmGenerationSourceWebhook,
				TaskType: models.StrmGenerationTaskTypeDirectoryScan, AccountId: account.ID, SyncPathId: syncPath.ID,
				DirectoryId: tt.rootID, DirectoryPath: path,
			}
			count, err := service.ExpandDirectoryScan(context.Background(), task)
			if err != nil || count != tt.want {
				t.Fatalf("展开数量 = %d，期望 %d，err=%v", count, tt.want, err)
			}
			var children []models.StrmGenerationTask
			if err := db.Db.Where("parent_task_id = ?", task.ID).Find(&children).Error; err != nil {
				t.Fatal(err)
			}
			if len(children) != tt.want {
				t.Fatalf("子任务数量 = %d，期望 %d", len(children), tt.want)
			}
			if tt.source == models.SourceType115 && len(children) == 1 && children[0].Path != "/Media/多端播放" {
				t.Fatalf("只应展开非根目录同名目录中的文件：%+v", children[0])
			}
		})
	}
}
