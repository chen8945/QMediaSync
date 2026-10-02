package syncstrm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
	"qmediasync/internal/v115open"
)

func generationScopeTask(account *models.Account, syncPath *models.SyncPath) *models.StrmGenerationTask {
	return &models.StrmGenerationTask{
		Source: models.StrmGenerationSourceUploadCompleted, TaskType: models.StrmGenerationTaskTypeFile,
		AccountId: account.ID, SyncPathId: syncPath.ID,
		FileId: "scope-file", ParentId: "root", Path: syncPath.RemotePath, FileName: "movie.mkv",
		PickCode: "scope-pick", Sha1: "scope-sha1", FileSize: 1024, Mtime: 100,
	}
}

func TestStrmGenerationScopeValidatesRemotePaths(t *testing.T) {
	tests := []struct {
		name       string
		sourceType models.SourceType
		remotePath string
		filePath   string
		wantReject bool
	}{
		{name: "百度空根目录", sourceType: models.SourceTypeBaiduPan, remotePath: "", filePath: "/"},
		{name: "百度斜杠根目录", sourceType: models.SourceTypeBaiduPan, remotePath: "/", filePath: "/"},
		{name: "百度点号根目录", sourceType: models.SourceTypeBaiduPan, remotePath: ".", filePath: "/"},
		{name: "前导空格", remotePath: " media", filePath: "/ media"},
		{name: "尾随空格", remotePath: "media ", filePath: "/media "},
		{name: "仅空格目录", remotePath: " ", filePath: "/ "},
		{name: "前导空格不同", remotePath: " media", filePath: "/media", wantReject: true},
		{name: "尾随空格不同", remotePath: "media ", filePath: "/media", wantReject: true},
		{name: "兄弟目录前缀", remotePath: "media ", filePath: "/media other", wantReject: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, syncPath := setupStrmExclusionTestDB(t)
			if tt.sourceType != "" {
				account.SourceType = tt.sourceType
				syncPath.SourceType = tt.sourceType
				if err := db.Db.Save(account).Error; err != nil {
					t.Fatal(err)
				}
			}
			syncPath.RemotePath = tt.remotePath
			if err := db.Db.Save(syncPath).Error; err != nil {
				t.Fatal(err)
			}
			service := newTestGenerationService(t, syncPath, account)
			build := service.buildSyncer
			const content = "http://qms.local/movie.mkv"
			service.buildSyncer = func(sp *models.SyncPath, account *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
				syncer, err := build(sp, account, config)
				syncer.Sync = &models.Sync{Logger: helpers.AppLogger}
				syncer.SyncDriver = &fakeDirectoryScanDriver{strmContent: content}
				return syncer, err
			}
			service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
			task := generationScopeTask(account, syncPath)
			task.Path = tt.filePath
			_, err := service.Generate(t.Context(), StrmGenerationInput{Task: task})
			if tt.wantReject {
				if err == nil || !strings.Contains(err.Error(), "不在同步远端目录") {
					t.Fatalf("应拒绝其他目录中的文件，得到 %v", err)
				}
				var count int64
				if err := db.Db.Model(&models.SyncFile{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("越界任务不应保存文件：count=%d err=%v", count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			localPath := filepath.Join(syncPath.GetFullLocalPath(), "movie.strm")
			if data, err := os.ReadFile(localPath); err != nil || string(data) != content {
				t.Fatalf("合法目录未生成 STRM：content=%q err=%v", data, err)
			}
			var saved models.SyncFile
			if err := db.Db.First(&saved).Error; err != nil || saved.Path != tt.filePath || saved.LocalFilePath != localPath {
				t.Fatalf("账本应保留实际路径：%+v err=%v", saved, err)
			}
		})
	}
}

func TestStrmGenerationScopeDirectoryScanPreservesSpaces(t *testing.T) {
	for _, name := range []string{" media", "media "} {
		t.Run(name, func(t *testing.T) {
			account, syncPath := setupStrmExclusionTestDB(t)
			syncPath.RemotePath = name
			if err := db.Db.Save(syncPath).Error; err != nil {
				t.Fatal(err)
			}
			service := newTestGenerationService(t, syncPath, account)
			build := service.buildSyncer
			service.buildSyncer = func(sp *models.SyncPath, account *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
				syncer, err := build(sp, account, config)
				syncer.SyncDriver = &fakeDirectoryScanDriver{filesByID: map[string][]*SyncFileCache{
					"root": {{FileId: "child", FileName: "movie.mkv", FileType: v115open.TypeFile,
						PickCode: "pick", Sha1: "sha1", FileSize: 1024, MTime: 100}},
				}}
				return syncer, err
			}
			parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
				Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeDirectoryScan,
				SyncPathId: syncPath.ID, AccountId: account.ID, DirectoryPath: "/" + name, DirectoryId: "root",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); err != nil {
				t.Fatal(err)
			}
			var child models.StrmGenerationTask
			if err := db.Db.Where("parent_task_id = ?", parent.ID).First(&child).Error; err != nil || child.Path != "/"+name {
				t.Fatalf("目录扫描子任务应保留路径中的空格：%+v err=%v", child, err)
			}
		})
	}
}

func TestStrmGenerationScopeWaitsForOverlappingPaths(t *testing.T) {
	for _, name := range []string{"远端父目录", "其他账号同一本地目录", "旧记录本地目录", "独立目录"} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account, syncPath := setupStrmExclusionTestDB(t)
				service := newTestGenerationService(t, syncPath, account)
				service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 1 }
				readOwner := false
				service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) {
					readOwner = true
					return true, nil
				}
				busy := syncscope.Scope{SourceType: string(account.SourceType), AccountID: account.ID, RemotePath: "/"}
				switch name {
				case "其他账号同一本地目录":
					busy = syncscope.Scope{SourceType: "115", AccountID: account.ID + 1, LocalPath: syncPath.GetFullLocalPath()}
				case "旧记录本地目录":
					oldRoot := t.TempDir()
					old := &models.SyncFile{
						SyncPathId: syncPath.ID, AccountId: account.ID, SourceType: account.SourceType,
						FileId: "scope-file", PickCode: "scope-pick", FileName: "movie.mkv", Path: "/old",
						LocalFilePath: filepath.Join(oldRoot, "movie.strm"), IsVideo: true,
					}
					if err := db.Db.Create(old).Error; err != nil {
						t.Fatal(err)
					}
					busy = syncscope.Scope{LocalPath: oldRoot}
				case "独立目录":
					busy = syncscope.Scope{SourceType: "115", AccountID: account.ID, RemotePath: "/independent", LocalPath: t.TempDir()}
				}
				release, err := syncscope.Acquire(t.Context(), busy)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				done := make(chan error, 1)
				go func() {
					_, err := service.Generate(t.Context(), StrmGenerationInput{Task: generationScopeTask(account, syncPath)})
					done <- err
				}()
				synctest.Wait()
				if readOwner != (name == "独立目录") {
					t.Fatalf("范围占用期间读取 owner = %v", readOwner)
				}
				release()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if !readOwner {
					t.Fatal("释放后没有继续生成")
				}
			})
		})
	}
}

func TestStrmGenerationScopeCancellationKeepsTaskRetryable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		account, syncPath := setupStrmExclusionTestDB(t)
		service := newTestGenerationService(t, syncPath, account)
		service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 1 }
		task, err := models.EnqueueStrmGenerationTask(generationScopeTask(account, syncPath))
		if err != nil {
			t.Fatal(err)
		}
		release, err := syncscope.Acquire(t.Context(), syncPath.Scope())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := ProcessPendingStrmGenerationTasks(ctx, service, 1)
			done <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("等待取消返回 %v", err)
		}
		var saved models.StrmGenerationTask
		if err := db.Db.First(&saved, task.ID).Error; err != nil {
			t.Fatal(err)
		}
		if saved.Status != models.StrmGenerationStatusRunning || saved.LastError != "" {
			t.Fatalf("取消等待不应被记录为生成失败：%+v", saved)
		}
		var count int64
		if err := db.Db.Model(&models.SyncFile{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("取消后仍保存文件：count=%d err=%v", count, err)
		}
		release()
		if err := models.ResetRunningStrmGenerationTasks(); err != nil {
			t.Fatal(err)
		}
		if _, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 1); err != nil {
			t.Fatal(err)
		}
		if err := db.Db.First(&saved, task.ID).Error; err != nil || saved.Status != models.StrmGenerationStatusCompleted {
			t.Fatalf("恢复后任务未完成：status=%s err=%v", saved.Status, err)
		}
	})
}

func TestStrmGenerationScopeRechecksAccountAfterWaiting(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		account, syncPath := setupStrmExclusionTestDB(t)
		service := newTestGenerationService(t, syncPath, account)
		built := false
		service.buildSyncer = func(*models.SyncPath, *models.Account, *SyncStrmConfig) (*SyncStrm, error) {
			built = true
			return nil, errors.New("不应使用旧账号创建同步器")
		}
		release, err := syncscope.Acquire(t.Context(), syncPath.Scope())
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		done := make(chan error, 1)
		go func() {
			_, err := service.Generate(t.Context(), StrmGenerationInput{Task: generationScopeTask(account, syncPath)})
			done <- err
		}()
		synctest.Wait()
		if err := db.Db.Model(&models.SyncPath{}).Where("id = ?", syncPath.ID).Update("account_id", account.ID+1).Error; err != nil {
			t.Fatal(err)
		}
		release()
		if err := <-done; err == nil || !strings.Contains(err.Error(), "任务账号") || built {
			t.Fatalf("等待后没有拒绝旧账号：built=%v err=%v", built, err)
		}
	})
}

func TestStrmGenerationScopeRejectsChangedRemoteFacts(t *testing.T) {
	for _, name := range []string{"完整任务越界", "补齐详情后越界", "详情来源改变", "OpenList 实际父路径越界"} {
		t.Run(name, func(t *testing.T) {
			account, syncPath := setupStrmExclusionTestDB(t)
			if name == "OpenList 实际父路径越界" {
				account.SourceType, syncPath.SourceType = models.SourceTypeOpenList, models.SourceTypeOpenList
				if err := db.Db.Save(account).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Save(syncPath).Error; err != nil {
					t.Fatal(err)
				}
			}
			service := newTestGenerationService(t, syncPath, account)
			ownerCalled := false
			service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) {
				ownerCalled = true
				return true, nil
			}
			task := generationScopeTask(account, syncPath)
			switch name {
			case "完整任务越界":
				task.Path = "/remote-other"
			case "补齐详情后越界", "详情来源改变":
				task.Mtime = 0
				service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
					if name == "详情来源改变" {
						return &SyncFileCache{SourceType: models.SourceTypeBaiduPan}, nil
					}
					return &SyncFileCache{Path: "/remote-other"}, nil
				}
			case "OpenList 实际父路径越界":
				task.ParentId = "/remote-other"
			}
			if _, err := service.Generate(t.Context(), StrmGenerationInput{Task: task}); err == nil || ownerCalled {
				t.Fatalf("实际文件越界时不应读取 owner：owner=%v err=%v", ownerCalled, err)
			}
			var count int64
			if err := db.Db.Model(&models.SyncFile{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("不应保存越界文件：count=%d err=%v", count, err)
			}
		})
	}
}

func TestStrmGenerationScopeDirectoryParentReleasesBeforeChildren(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	service := newTestGenerationService(t, syncPath, account)
	build := service.buildSyncer
	service.buildSyncer = func(sp *models.SyncPath, account *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
		syncer, err := build(sp, account, config)
		syncer.SyncDriver = &fakeDirectoryScanDriver{filesByID: map[string][]*SyncFileCache{
			"root": {{FileId: "child", FileName: "movie.mkv", FileType: v115open.TypeFile,
				PickCode: "pick", Sha1: "sha1", FileSize: 1024, MTime: 100}},
		}}
		return syncer, err
	}
	service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 1 }
	parent, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
		Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId: syncPath.ID, AccountId: account.ID, DirectoryPath: syncPath.RemotePath, DirectoryId: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := ProcessPendingStrmGenerationTasks(ctx, service, 1); err != nil {
		t.Fatal(err)
	}
	var saved models.StrmGenerationTask
	if err := db.Db.First(&saved, parent.ID).Error; err != nil || saved.Status != models.StrmGenerationStatusWaitingChildren {
		t.Fatalf("父任务未等待子任务：%+v err=%v", saved, err)
	}
	release, err := syncscope.Acquire(ctx, syncPath.Scope())
	if err != nil {
		t.Fatalf("父任务等待子任务时仍占用范围：%v", err)
	}
	release()
	if _, err := ProcessPendingStrmGenerationTasks(ctx, service, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&saved, parent.ID).Error; err != nil || saved.Status != models.StrmGenerationStatusCompleted || saved.AcceptedItems != 1 {
		t.Fatalf("子任务结束后父任务未完成：%+v err=%v", saved, err)
	}
}

func TestStrmGenerationScopeUsesResolvedIdentityOldPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		account, syncPath := setupStrmExclusionTestDB(t)
		oldRoot := t.TempDir()
		old := &models.SyncFile{
			SyncPathId: syncPath.ID, AccountId: account.ID, SourceType: account.SourceType,
			FileId: "resolved-file", PickCode: "resolved-pick", FileName: "movie.mkv", Path: "/old",
			LocalFilePath: filepath.Join(oldRoot, "movie.strm"), IsVideo: true,
		}
		if err := db.Db.Create(old).Error; err != nil {
			t.Fatal(err)
		}
		service := newTestGenerationService(t, syncPath, account)
		service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 1 }
		detailCalls, ownerCalls := 0, 0
		remoteName, remoteMtime := "movie.mkv", int64(100)
		service.detailByFileID = func(context.Context, *SyncStrm, string) (*SyncFileCache, error) {
			detailCalls++
			return &SyncFileCache{FileId: "resolved-file", PickCode: "resolved-pick", FileName: remoteName, MTime: remoteMtime}, nil
		}
		service.resolveStrmOwner = func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error) {
			ownerCalls++
			return true, nil
		}
		release, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: oldRoot})
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		task := generationScopeTask(account, syncPath)
		task.Mtime = 0
		done := make(chan error, 1)
		go func() {
			_, err := service.Generate(t.Context(), StrmGenerationInput{Task: task})
			done <- err
		}()
		synctest.Wait()
		if detailCalls != 1 || ownerCalls != 0 {
			t.Fatalf("应先补详情再等待实际旧位置：detail=%d owner=%d", detailCalls, ownerCalls)
		}
		remoteName, remoteMtime = "renamed.mkv", 200
		release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if detailCalls != 2 || ownerCalls != 1 {
			t.Fatalf("重新申请后应刷新远端详情：detail=%d owner=%d", detailCalls, ownerCalls)
		}
		var saved models.SyncFile
		if err := db.Db.First(&saved, old.ID).Error; err != nil || saved.Path != syncPath.RemotePath || saved.FileName != remoteName || saved.MTime != remoteMtime {
			t.Fatalf("旧记录未被正确更新：%+v err=%v", saved, err)
		}
	})
}

func TestStrmGenerationScopeRejectsSymlinkOutsideRoot(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	root := syncPath.GetFullLocalPath()
	outside := t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(outside, "movie.strm")
	if err := os.WriteFile(oldFile, []byte("保留原文件"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := newTestGenerationService(t, syncPath, account)
	service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 0 }
	service.processStrmFile = func(syncer *SyncStrm, file *SyncFileCache) error {
		return os.WriteFile(file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath), []byte("错误覆盖"), 0o600)
	}
	service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
	task := generationScopeTask(account, syncPath)
	task.Path += "/linked"
	if _, err := service.Generate(t.Context(), StrmGenerationInput{Task: task}); err == nil {
		t.Fatal("子目录链接到未保护位置时应拒绝生成")
	}
	if data, err := os.ReadFile(oldFile); err != nil || string(data) != "保留原文件" {
		t.Fatalf("范围外旧文件被修改：%q %v", data, err)
	}
	var count int64
	if err := db.Db.Model(&models.SyncFile{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("越界任务不应保存文件记录：count=%d err=%v", count, err)
	}
}

func TestStrmGenerationScopeHeldUntilMetadataEnqueued(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		account, syncPath := setupStrmExclusionTestDB(t)
		service := newTestGenerationService(t, syncPath, account)
		build := service.buildSyncer
		service.buildSyncer = func(sp *models.SyncPath, account *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
			syncer, err := build(sp, account, config)
			syncer.SyncDriver = &fakeDirectoryScanDriver{filesByID: map[string][]*SyncFileCache{
				"root": {{FileId: "nfo", FileName: "movie.nfo", FileType: v115open.TypeFile, PickCode: "nfo-pick"}},
			}}
			return syncer, err
		}
		service.compareStrm = func(*SyncStrm, *SyncFileCache) int { return 1 }
		entered, proceed := make(chan struct{}), make(chan struct{})
		if err := db.Db.Callback().Create().Before("gorm:create").Register("generation:hold-download", func(tx *gorm.DB) {
			if _, ok := tx.Statement.Dest.(*models.DbDownloadTask); ok {
				close(entered)
				<-proceed
			}
		}); err != nil {
			t.Fatal(err)
		}
		defer db.Db.Callback().Create().Remove("generation:hold-download")
		task := generationScopeTask(account, syncPath)
		task.Source, task.DownloadMeta = models.StrmGenerationSourceWebhook, true
		done := make(chan error, 1)
		go func() {
			_, err := service.Generate(t.Context(), StrmGenerationInput{Task: task})
			done <- err
		}()
		<-entered
		acquired := false
		nextDone := make(chan error, 1)
		go func() {
			release, err := syncscope.Acquire(t.Context(), syncPath.Scope())
			if err == nil {
				acquired = true
				release()
			}
			nextDone <- err
		}()
		synctest.Wait()
		if acquired {
			t.Fatal("元数据尚未入队，就让下一次任务读取文件记录")
		}
		close(proceed)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := <-nextDone; err != nil {
			t.Fatal(err)
		}
		var count int64
		if err := db.Db.Model(&models.DbDownloadTask{}).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("元数据未入队：count=%d err=%v", count, err)
		}
	})
}
