package syncstrm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
)

func TestLocalCleanupRemovesNestedEmptyDirectoriesInOnePass(t *testing.T) {
	for _, mode := range []string{"empty", "unknown_file", "excluded_file", "active_upload", "disabled", "missing_cleanup_disabled", "failed_directory"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			s.Config.DelEmptyLocalDir = mode != "disabled"
			s.sync115 = &Sync115{absentDirs: map[string]bool{"/Media/Empty": true}}
			season := filepath.Join(old.LocalFilePath, "Season 1")
			strm := filepath.Join(season, "episode.strm")
			writeScopeFile(t, strm)
			retained := ""
			switch mode {
			case "unknown_file":
				retained = filepath.Join(season, "keep.txt")
			case "excluded_file":
				retained = filepath.Join(season, "keep.nfo")
				s.Config.ExcludeNames = []string{"keep.nfo"}
			case "active_upload":
				retained = filepath.Join(season, "keep.nfo")
				task := models.DbUploadTask{Source: models.UploadSourceStrm, LocalFullPath: retained, Status: models.UploadStatusPending}
				if err := db.Db.Create(&task).Error; err != nil {
					t.Fatal(err)
				}
			case "missing_cleanup_disabled":
				s.skipMissingCleanup("增量不按缺项删除")
			case "failed_directory":
				s.recordScanFailure("Media/Empty/Season 1", errors.New("目录读取失败"))
			}
			if retained != "" {
				writeScopeFile(t, retained)
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, strm, mode == "missing_cleanup_disabled" || mode == "failed_directory")
			if retained != "" {
				requireScopeFile(t, retained, true)
			}
			requireScopeFile(t, season, mode != "empty")
			requireScopeFile(t, old.LocalFilePath, mode != "empty")
		})
	}
}

func TestLocalCleanupPreservesInitiallyEmptyDirectoryLedger(t *testing.T) {
	for _, mode := range []string{"pending_metadata", "empty_child", "removed_direct_file"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			s.Config.DelEmptyLocalDir = true
			s.sync115 = &Sync115{}
			if err := s.publish115DirectoryList(s.Context, "Media", "root", []pathQueueItem{{PathId: old.FileId, Path: "Media/Empty"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.cache115UploadDirectory(old.FileId); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(old.LocalFilePath, 0755); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "pending_metadata":
				if err := db.Db.AutoMigrate(&models.EmbyMediaSyncFile{}); err != nil {
					t.Fatal(err)
				}
				meta := &SyncFileCache{SourceType: models.SourceType115, FileId: "metadata", ParentId: old.FileId,
					FileType: v115open.TypeFile, FileName: "movie.nfo", Path: "Media/Empty", IsMeta: true,
					LocalFilePath: filepath.Join(old.LocalFilePath, "movie.nfo"), PickCode: "meta-pick", FileSize: 8}
				if err := s.memSyncCache.Insert(meta); err != nil {
					t.Fatal(err)
				}
				s.AddDownloadTaskTemp(meta)
				if err := s.AddDownloadTaskFromMemCache(); err != nil {
					t.Fatal(err)
				}
			case "empty_child":
				if err := os.MkdirAll(filepath.Join(old.LocalFilePath, "Child"), 0755); err != nil {
					t.Fatal(err)
				}
			case "removed_direct_file":
				writeScopeFile(t, filepath.Join(old.LocalFilePath, "stale.strm"))
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, old.LocalFilePath, false)
			retained := mode != "removed_direct_file"
			cached, _ := s.memSyncCache.GetByFileId(old.FileId)
			if (cached != nil) != retained {
				t.Errorf("目录缓存驱逐范围改变：mode=%s cached=%v", mode, cached)
			}
			if err := s.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			var rows int64
			if err := db.Db.Model(&models.SyncFile{}).Where("id = ?", old.ID).Count(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if (rows == 1) != retained {
				t.Errorf("目录账本清理范围改变：mode=%s rows=%d", mode, rows)
			}
			if mode == "pending_metadata" {
				var task models.DbDownloadTask
				if err := db.Db.Where("remote_file_id = ?", "metadata").First(&task).Error; err != nil {
					t.Fatal(err)
				}
				if task.Status != models.DownloadStatusPending {
					t.Fatalf("待下载元数据状态改变：%+v", task)
				}
			}
		})
	}
}

func TestLocalCleanupUploadQueryFailureStopsBeforeDeletion(t *testing.T) {
	for _, mode := range []string{"unknown_parent", "known_absent_parent", "cached_parent"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			s.Config.DelEmptyLocalDir = true
			empty := filepath.Join(s.GetLocalBaseDir(), "AEmpty")
			if err := os.MkdirAll(empty, 0755); err != nil {
				t.Fatal(err)
			}
			meta := filepath.Join(old.LocalFilePath, "a.nfo")
			strm := filepath.Join(old.LocalFilePath, "z.strm")
			later := filepath.Join(s.GetLocalBaseDir(), "ZLater", "stale.strm")
			for _, local := range []string{meta, strm, later} {
				writeScopeFile(t, local)
			}
			if mode == "known_absent_parent" {
				s.sync115 = &Sync115{absentDirs: map[string]bool{"/Media/Empty": true}}
			} else if mode == "cached_parent" {
				if err := s.memSyncCache.Insert(ledgerTestCache(old, false)); err != nil {
					t.Fatal(err)
				}
			}
			s.SyncDriver = &uploadParent115Driver{
				detail: func(context.Context, string) (*SyncFileCache, error) {
					return nil, errors.New("数据库检查失败后不应补查目录")
				},
			}
			queryErr := errors.New("upload tasks unavailable")
			if err := db.Db.Callback().Query().Before("gorm:query").Register("fail_upload_check", func(tx *gorm.DB) {
				if tx.Statement.Table == "db_upload_tasks" {
					tx.AddError(queryErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err := s.compareLocalFilesWithTempTable()
			if !isFatalSyncError(err) || !errors.Is(err, queryErr) {
				t.Fatalf("上传任务查询错误未停止清理：%v", err)
			}
			for _, local := range []string{meta, strm, later, empty} {
				requireScopeFile(t, local, true)
			}
			if status, err := s.scanOutcome(); status == models.SyncStatusCompleted || err == nil {
				t.Fatalf("查询失败被标记为完成：status=%v err=%v", status, err)
			}
		})
	}
}

func TestLocalCleanupFinalizationPreservesFailedDirectories(t *testing.T) {
	for _, mode := range []string{"read_failure", "remove_failure", "late_failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			s.Config.DelEmptyLocalDir = true
			child := filepath.Join(old.LocalFilePath, "Child")
			sibling := filepath.Join(old.LocalFilePath, "Sibling")
			for _, dir := range []string{child, sibling} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			dirs := []string{old.LocalFilePath, child, sibling}
			switch mode {
			case "read_failure":
				if err := os.Remove(child); err != nil {
					t.Fatal(err)
				}
				writeScopeFile(t, child)
			case "remove_failure":
				// 保留末尾 /.，使目录读取成功、删除返回真实文件系统错误。
				dirs[1] = child + string(filepath.Separator) + "."
			case "late_failure":
				s.recordScanFailure(s.remotePathForLocal(child), errors.New("收尾前发现目录读取失败"))
			case "cancel":
				s.Cancel()
			}
			err := s.removeEmptyLocalDirs(dirs, nil)
			if (mode == "cancel" && !errors.Is(err, context.Canceled)) || (mode != "cancel" && err != nil) {
				t.Fatalf("目录收尾错误不符：%v", err)
			}
			requireScopeFile(t, child, true)
			requireScopeFile(t, old.LocalFilePath, true)
			requireScopeFile(t, sibling, mode == "cancel")
			if mode != "cancel" {
				result := s.scanResultSnapshot()
				if len(result.Failures) != 1 || result.Failures[0].Kind != "directory" || result.Failures[0].Path != s.remotePathForLocal(child) {
					t.Fatalf("目录错误未记录到正确范围：%+v", result)
				}
				if !s.cleanupProtected(old.LocalFilePath, true) {
					t.Fatal("收尾错误未保护祖先目录")
				}
			}
		})
	}
}

func Test115UploadTaskRecheckedAfterDirectoryConfirmation(t *testing.T) {
	s, old := new115UploadParentSync(t)
	local := filepath.Join(old.LocalFilePath, "movie.nfo")
	writeScopeFile(t, local)
	s.SyncDriver = &uploadParent115Driver{
		detail: func(_ context.Context, id string) (*SyncFileCache, error) {
			task := models.DbUploadTask{Source: models.UploadSourceStrm, LocalFullPath: local, Status: models.UploadStatusPending}
			if err := db.Db.Create(&task).Error; err != nil {
				t.Fatal(err)
			}
			return uploadParent115Detail(id), nil
		},
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	requireScopeFile(t, local, true)
	var uploads int64
	if err := db.Db.Model(&models.DbUploadTask{}).Count(&uploads).Error; err != nil {
		t.Fatal(err)
	}
	if s.NewUpload != 0 || uploads != 1 {
		t.Fatalf("未重新检查确认目录期间创建的上传任务：new=%d total=%d", s.NewUpload, uploads)
	}
}

func Test115KnownAbsentParentChecksEachUploadTaskOnce(t *testing.T) {
	s, old := new115UploadParentSync(t)
	s.sync115 = &Sync115{absentDirs: map[string]bool{"/Media/Empty": true}}
	for _, name := range []string{"a.nfo", "b.nfo"} {
		writeScopeFile(t, filepath.Join(old.LocalFilePath, name))
	}
	checks := 0
	if err := db.Db.Callback().Query().Before("gorm:query").Register("count_upload_checks", func(tx *gorm.DB) {
		if tx.Statement.Table == "db_upload_tasks" {
			checks++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	if checks != 2 {
		t.Fatalf("已有目录缺失结论仍重复查询上传任务：checks=%d，期望 2", checks)
	}
	for _, name := range []string{"a.nfo", "b.nfo"} {
		requireScopeFile(t, filepath.Join(old.LocalFilePath, name), false)
	}
}
