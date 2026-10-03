package syncstrm

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestGenerationMetadataSourceLinks(t *testing.T) {
	for _, mode := range []string{"file_link", "parent_link", "outside", "retarget_before_publish", "missing_rule", "target_link"} {
		t.Run(mode, func(t *testing.T) {
			account, syncPath := setupStrmGenerationServiceTestDB(t)
			if err := db.Db.AutoMigrate(&models.DirectoryUploadRule{}, &models.DirectoryUploadProcessedFile{}); err != nil {
				t.Fatal(err)
			}
			monitor := t.TempDir()
			realDir := filepath.Join(monitor, "original")
			if mode == "outside" {
				realDir = t.TempDir()
			}
			if err := os.MkdirAll(realDir, 0755); err != nil {
				t.Fatal(err)
			}
			realSource := filepath.Join(realDir, "episode.nfo")
			if err := os.WriteFile(realSource, []byte("new"), 0644); err != nil {
				t.Fatal(err)
			}
			mtime := time.Unix(100, 0)
			if err := os.Chtimes(realSource, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			source := filepath.Join(monitor, "episode.nfo")
			if mode == "parent_link" {
				parent := filepath.Join(monitor, "linked")
				if err := os.Symlink(realDir, parent); err != nil {
					t.Fatal(err)
				}
				source = filepath.Join(parent, "episode.nfo")
			} else if err := os.Symlink(realSource, source); err != nil {
				t.Fatal(err)
			}
			upload := &models.DbUploadTask{
				Source: models.UploadSourceDirectoryMonitor, AccountId: account.ID, SyncPathId: syncPath.ID,
				SourceType: models.SourceType115, LocalFullPath: source, FileName: "episode.nfo", FileSize: 3,
				LocalMtime: 100, SourceFingerprint: models.BuildDirectoryUploadSourceFingerprint(3, mtime.UnixNano()),
				Status: models.UploadStatusCompleted, UploadResult: models.UploadResultMultipartUploaded,
				RemoteFileId: "meta", RemotePickCode: "pick-meta", SourceCleanupStatus: models.UploadSourceCleanupStatusPending,
			}
			if err := db.Db.Create(upload).Error; err != nil {
				t.Fatal(err)
			}
			if mode != "missing_rule" {
				rule := &models.DirectoryUploadRule{SyncPathId: syncPath.ID, AccountId: account.ID, MonitorPath: monitor,
					UploadMetadata: true, DeleteSourceAfterSuccess: true}
				if err := db.Db.Create(rule).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Create(&models.DirectoryUploadProcessedFile{RuleId: rule.ID, UploadTaskId: upload.ID, SourceKey: "meta"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			cache := &SyncFileCache{FileId: "meta", ParentId: "root", FileName: "episode.nfo", Path: "/remote",
				FileType: v115open.TypeFile, SourceType: models.SourceType115, IsMeta: true}
			target := cache.GetLocalFilePath(syncPath.LocalPath, syncPath.RemotePath)
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			if mode == "target_link" {
				original := filepath.Join(filepath.Dir(target), "original.nfo")
				if err := os.WriteFile(original, []byte("old"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(original, target); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			task, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{
				Source: models.StrmGenerationSourceUploadCompleted, TaskType: models.StrmGenerationTaskTypeFile,
				UploadTaskId: upload.ID, SyncPathId: syncPath.ID, AccountId: account.ID,
				FileId: "meta", ParentId: "root", PickCode: "pick-meta", FileName: "episode.nfo",
				Path: "/remote", FileSize: 3, Mtime: 100, Sha1: "remote-sha1",
			})
			if err != nil {
				t.Fatal(err)
			}
			service := newTestGenerationService(t, syncPath, account)
			service.requestEmbyRefreshBySyncFile = func(*models.SyncFile) error { return nil }
			if mode == "retarget_before_publish" {
				outside := filepath.Join(t.TempDir(), "outside.nfo")
				if err := os.WriteFile(outside, []byte("new"), 0644); err != nil {
					t.Fatal(err)
				}
				queries := 0
				if err := db.Db.Callback().Query().Before("gorm:query").Register("retarget_metadata", func(tx *gorm.DB) {
					if tx.Statement.Table == "directory_upload_processed_files" {
						queries++
						if queries == 2 {
							if err := os.Remove(source); err != nil {
								t.Fatal(err)
							}
							if err := os.Symlink(outside, source); err != nil {
								t.Fatal(err)
							}
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Query().Remove("retarget_metadata") })
			}
			oldCleanup := cleanupSourceAfterStrmSuccess
			// 文件链接清理验证调用真实入口；父目录链接不改变既有删除语义。
			cleanupCalls := 0
			cleanupSourceAfterStrmSuccess = func(id uint) error {
				cleanupCalls++
				if mode == "file_link" {
					return directoryupload.CleanupSourceAfterStrmSuccess(id)
				}
				return nil
			}
			t.Cleanup(func() { cleanupSourceAfterStrmSuccess = oldCleanup })
			if _, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 10); err != nil {
				t.Fatal(err)
			}
			if err := db.Db.First(task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			success := mode == "file_link" || mode == "parent_link"
			if (task.Status == models.StrmGenerationStatusCompleted) != success || (cleanupCalls > 0) != success {
				t.Fatalf("状态=%s，错误=%s，清理次数=%d", task.Status, task.LastError, cleanupCalls)
			}
			want := "old"
			if success {
				want = "new"
			}
			if content, err := os.ReadFile(target); err != nil || string(content) != want {
				t.Fatalf("目标内容=%q，错误=%v", content, err)
			}
			if content, err := os.ReadFile(realSource); err != nil || string(content) != "new" {
				t.Fatalf("真实源文件被修改或清理：%q，%v", content, err)
			}
			if mode == "file_link" {
				if _, err := os.Lstat(source); !os.IsNotExist(err) {
					t.Fatalf("原链接未清理：%v", err)
				}
			}
			if err := db.Db.First(upload, upload.ID).Error; err != nil || upload.LocalFullPath != source {
				t.Fatalf("上传原路径被改写：%s，%v", upload.LocalFullPath, err)
			}
		})
	}
}
