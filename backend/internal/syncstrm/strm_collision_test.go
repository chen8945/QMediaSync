package syncstrm

import (
	"context"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestSelectLatest115StrmOwners(t *testing.T) {
	tests := []struct {
		name      string
		files     []*SyncFileCache
		target    string
		wantOwner string
	}{
		{
			name: "上传时间较新的文件成为 owner",
			files: []*SyncFileCache{
				new115CollisionTestFile("file-old", "episode.mkv", "/media/episode.strm", 100),
				new115CollisionTestFile("file-new", "episode.mp4", "/media/episode.strm", 200),
			},
			target:    "/media/episode.strm",
			wantOwner: "file-new",
		},
		{
			name: "上传时间相同时 FileID 较大的文件成为 owner",
			files: []*SyncFileCache{
				new115CollisionTestFile("100", "episode.mkv", "/media/episode.strm", 200),
				new115CollisionTestFile("200", "episode.mp4", "/media/episode.strm", 200),
			},
			target:    "/media/episode.strm",
			wantOwner: "200",
		},
		{
			name: "不同目标路径分别选择 owner",
			files: []*SyncFileCache{
				new115CollisionTestFile("movie", "movie.mkv", "/media/movie.strm", 100),
				new115CollisionTestFile("episode", "episode.mp4", "/media/episode.strm", 200),
			},
			target:    "/media/movie.strm",
			wantOwner: "movie",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owners := selectLatest115StrmOwners(tt.files)
			owner := owners[tt.target]
			if owner == nil || owner.FileId != tt.wantOwner {
				t.Fatalf("owner = %+v，期望 FileID=%s", owner, tt.wantOwner)
			}
		})
	}
}

func TestProcess115CollectedFilesWritesOnlyLatestCollisionOwner(t *testing.T) {
	targetPath := t.TempDir()
	syncer := &SyncStrm{
		Account:       &models.Account{SourceType: models.SourceType115, UserId: "user-115"},
		Sync:          &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
		Context:       context.Background(),
		SourcePath:    "Media",
		TargetPath:    targetPath,
		TmpSyncPath:   true,
		PathWorkerMax: 2,
		Config: SyncStrmConfig{
			StrmBaseUrl:     "http://qmediasync:12333",
			StrmUrlNeedPath: 2,
		},
		memSyncCache: NewMemorySyncCache(1),
	}
	driver := NewOpen115Driver(nil)
	driver.SetSyncStrm(syncer)
	syncer.SyncDriver = driver

	oldFile := &SyncFileCache{
		FileId:     "file-old",
		ParentId:   "parent-1",
		FileType:   v115open.TypeFile,
		FileName:   "episode.mkv",
		Path:       "Media/Season 1",
		MTime:      100,
		PickCode:   "pick-old",
		SourceType: models.SourceType115,
		IsVideo:    true,
	}
	newFile := &SyncFileCache{
		FileId:     "file-new",
		ParentId:   "parent-1",
		FileType:   v115open.TypeFile,
		FileName:   "episode.mp4",
		Path:       "Media/Season 1",
		MTime:      200,
		PickCode:   "pick-new",
		SourceType: models.SourceType115,
		IsVideo:    true,
	}
	for _, file := range []*SyncFileCache{oldFile, newFile} {
		file.GetLocalFilePath(targetPath, "Media")
		if err := syncer.memSyncCache.Insert(file); err != nil {
			t.Fatalf("插入同步缓存失败: %v", err)
		}
	}

	if err := syncer.process115CollectedFiles(); err != nil {
		t.Fatalf("处理 115 文件失败: %v", err)
	}
	if syncer.NewStrm != 1 {
		t.Fatalf("NewStrm = %d，期望只生成 1 个 STRM", syncer.NewStrm)
	}

	strmPath := newFile.GetLocalFilePath(targetPath, "Media")
	content, err := os.ReadFile(strmPath)
	if err != nil {
		t.Fatalf("读取 STRM 失败: %v", err)
	}
	parsed, err := url.Parse(string(content))
	if err != nil {
		t.Fatalf("解析 STRM 内容失败: %v", err)
	}
	if parsed.Path != "/115/url/video.mp4" || parsed.Query().Get("pickcode") != "pick-new" {
		t.Fatalf("STRM 内容 = %s，期望指向最新的 mp4 文件", content)
	}

	if err := syncer.process115CollectedFiles(); err != nil {
		t.Fatalf("第二次处理 115 文件失败: %v", err)
	}
	if syncer.NewStrm != 1 {
		t.Fatalf("第二次处理后 NewStrm = %d，期望不重复生成", syncer.NewStrm)
	}
}

func TestStrmGenerationServiceSkipsNonOwnerWithoutComparingOrWriting(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache) (bool, error) {
		return false, nil
	}
	service.compareStrm = func(*SyncStrm, *SyncFileCache) int {
		t.Fatal("non-owner 不应比较 STRM")
		return 0
	}
	service.processStrmFile = func(*SyncStrm, *SyncFileCache) error {
		t.Fatal("non-owner 不应写入 STRM")
		return nil
	}

	result, err := service.Generate(context.Background(), StrmGenerationInput{
		Task: &models.StrmGenerationTask{
			Source:     models.StrmGenerationSourceUploadCompleted,
			TaskType:   models.StrmGenerationTaskTypeFile,
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     "file-old",
			ParentId:   "parent-1",
			PickCode:   "pick-old",
			Path:       "/remote",
			FileName:   "episode.mkv",
			FileSize:   1024,
			Sha1:       "sha1-old",
			Mtime:      100,
		},
	})
	if err != nil {
		t.Fatalf("生成 non-owner STRM 任务失败: %v", err)
	}
	if result.Changed {
		t.Fatal("non-owner result.Changed = true，期望 false")
	}
}

func TestResolveLatest115StrmOwner(t *testing.T) {
	tests := []struct {
		name              string
		existingCollision bool
		currentMTime      int64
		wantOwner         bool
		wantListCalls     int
	}{
		{
			name:          "数据库没有冲突时不请求远端目录",
			currentMTime:  100,
			wantOwner:     true,
			wantListCalls: 0,
		},
		{
			name:              "发现冲突时远端较旧文件不是 owner",
			existingCollision: true,
			currentMTime:      100,
			wantOwner:         false,
			wantListCalls:     1,
		},
		{
			name:              "发现冲突时远端最新文件成为 owner",
			existingCollision: true,
			currentMTime:      200,
			wantOwner:         true,
			wantListCalls:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, syncPath := setupStrmGenerationServiceTestDB(t)
			currentFileID := "file-old"
			currentName := "episode.mkv"
			currentPickCode := "pick-old"
			if tt.currentMTime == 200 {
				currentFileID = "file-new"
				currentName = "episode.mp4"
				currentPickCode = "pick-new"
			}
			current := &SyncFileCache{
				FileId:     currentFileID,
				ParentId:   "parent-1",
				FileType:   v115open.TypeFile,
				FileName:   currentName,
				Path:       "/remote",
				MTime:      tt.currentMTime,
				PickCode:   currentPickCode,
				SourceType: models.SourceType115,
				IsVideo:    true,
			}
			current.GetLocalFilePath(syncPath.LocalPath, syncPath.RemotePath)
			if tt.existingCollision {
				existing := &models.SyncFile{
					SyncPathId:    syncPath.ID,
					AccountId:     account.ID,
					SourceType:    models.SourceType115,
					FileId:        "file-existing",
					FileName:      "episode.avi",
					LocalFilePath: current.LocalFilePath,
					IsVideo:       true,
				}
				if err := db.Db.Create(existing).Error; err != nil {
					t.Fatalf("创建冲突 SyncFile 失败: %v", err)
				}
			}

			driver := &collisionTestDriver{
				files: []*SyncFileCache{
					{
						FileId:     "file-old",
						ParentId:   "parent-1",
						FileType:   v115open.TypeFile,
						FileName:   "episode.mkv",
						Path:       "/remote",
						MTime:      100,
						PickCode:   "pick-old",
						SourceType: models.SourceType115,
					},
					{
						FileId:     "file-new",
						ParentId:   "parent-1",
						FileType:   v115open.TypeFile,
						FileName:   "episode.mp4",
						Path:       "/remote",
						MTime:      200,
						PickCode:   "pick-new",
						SourceType: models.SourceType115,
					},
				},
			}
			syncer := &SyncStrm{
				SyncPathId: syncPath.ID,
				SourcePath: syncPath.RemotePath,
				TargetPath: syncPath.LocalPath,
				Account:    account,
				Sync:       &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
				SyncDriver: driver,
				Config: SyncStrmConfig{
					VideoExt: []string{".mkv", ".mp4", ".avi"},
				},
			}

			gotOwner, err := resolveLatest115StrmOwner(context.Background(), syncer, current)
			if err != nil {
				t.Fatalf("解析 STRM owner 失败: %v", err)
			}
			if gotOwner != tt.wantOwner {
				t.Fatalf("owner = %v，期望 %v", gotOwner, tt.wantOwner)
			}
			if driver.listCalls != tt.wantListCalls {
				t.Fatalf("远端目录请求次数 = %d，期望 %d", driver.listCalls, tt.wantListCalls)
			}
		})
	}
}

func TestLockStrmTargetSerializesSamePath(t *testing.T) {
	events := make(chan string, 3)
	releaseFirst := make(chan struct{})
	firstDone := make(chan struct{})
	go func() {
		unlock := lockStrmTarget("/media/episode.strm")
		defer unlock()
		defer close(firstDone)
		events <- "first-enter"
		<-releaseFirst
		events <- "first-exit"
	}()

	if got := <-events; got != "first-enter" {
		t.Fatalf("第一个事件 = %s，期望 first-enter", got)
	}
	secondDone := make(chan struct{})
	go func() {
		unlock := lockStrmTarget("/media/episode.strm")
		defer unlock()
		defer close(secondDone)
		events <- "second-enter"
	}()

	select {
	case got := <-events:
		t.Fatalf("第一个任务持锁时收到事件 %s，第二个任务不应进入", got)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseFirst)
	if got := <-events; got != "first-exit" {
		t.Fatalf("释放锁后的第一个事件 = %s，期望 first-exit", got)
	}
	if got := <-events; got != "second-enter" {
		t.Fatalf("释放锁后的第二个事件 = %s，期望 second-enter", got)
	}
	<-firstDone
	<-secondDone
}

func TestExpandDirectoryScanChildrenEnqueuesOnlyLatestCollisionOwner(t *testing.T) {
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	driver := &collisionTestDriver{
		files: []*SyncFileCache{
			{
				FileId:     "file-old",
				ParentId:   "parent-1",
				FileType:   v115open.TypeFile,
				FileName:   "episode.mkv",
				Path:       "/remote",
				MTime:      100,
				PickCode:   "pick-old",
				SourceType: models.SourceType115,
			},
			{
				FileId:     "file-new",
				ParentId:   "parent-1",
				FileType:   v115open.TypeFile,
				FileName:   "episode.mp4",
				Path:       "/remote",
				MTime:      200,
				PickCode:   "pick-new",
				SourceType: models.SourceType115,
			},
		},
	}
	syncer := &SyncStrm{
		SyncPathId: syncPath.ID,
		SourcePath: syncPath.RemotePath,
		TargetPath: syncPath.LocalPath,
		Account:    account,
		Sync:       &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
		SyncDriver: driver,
		Config: SyncStrmConfig{
			VideoExt: []string{".mkv", ".mp4"},
		},
	}
	service := NewStrmGenerationService()
	parent := &models.StrmGenerationTask{
		ID:         9001,
		Source:     models.StrmGenerationSourceWebhook,
		TaskType:   models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId: syncPath.ID,
		AccountId:  account.ID,
	}

	total, err := service.expandDirectoryScanChildren(context.Background(), parent, syncer, syncPath, "/remote", "parent-1")
	if err != nil {
		t.Fatalf("展开目录扫描失败: %v", err)
	}
	if total != 1 {
		t.Fatalf("子任务数量 = %d，期望只创建最新 owner 的 1 个任务", total)
	}
	var tasks []models.StrmGenerationTask
	if err := db.Db.Where("parent_task_id = ?", parent.ID).Find(&tasks).Error; err != nil {
		t.Fatalf("查询目录扫描子任务失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].FileId != "file-new" {
		t.Fatalf("目录扫描子任务 = %+v，期望只包含 file-new", tasks)
	}
}

func new115CollisionTestFile(fileID, fileName, localPath string, mtime int64) *SyncFileCache {
	return &SyncFileCache{
		FileId:        fileID,
		FileType:      v115open.TypeFile,
		FileName:      fileName,
		LocalFilePath: filepath.ToSlash(localPath),
		MTime:         mtime,
		SourceType:    models.SourceType115,
		IsVideo:       true,
	}
}

type collisionTestDriver struct {
	files     []*SyncFileCache
	listCalls int
}

func (driver *collisionTestDriver) GetNetFileFiles(context.Context, string, string) ([]*SyncFileCache, error) {
	driver.listCalls++
	return driver.files, nil
}

func (*collisionTestDriver) GetPathIdByPath(context.Context, string) (string, error) {
	return "", nil
}

func (*collisionTestDriver) SetSyncStrm(*SyncStrm) {}

func (*collisionTestDriver) MakeStrmContent(*SyncFileCache) string { return "" }

func (*collisionTestDriver) CreateDirRecursively(context.Context, string) (string, string, error) {
	return "", "", nil
}

func (*collisionTestDriver) GetTotalFileCount(context.Context) (int64, string, error) {
	return 0, "", nil
}

func (*collisionTestDriver) GetDirsByPathId(context.Context, string) ([]pathQueueItem, error) {
	return nil, nil
}

func (*collisionTestDriver) GetFilesByPathId(context.Context, string, int, int) ([]v115open.File, error) {
	return nil, nil
}

func (*collisionTestDriver) GetFilesByPathMtime(context.Context, string, int, int, int64) (*baidupan.FileListAllResponse, error) {
	return nil, nil
}

func (*collisionTestDriver) DetailByFileId(context.Context, string) (*SyncFileCache, error) {
	return nil, nil
}

func (*collisionTestDriver) DeleteFile(context.Context, string, []string) error {
	return nil
}
