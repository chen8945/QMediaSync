package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
)

func baiduDirectoryFailureRow(s *SyncStrm, remote, id, local string, directory bool) models.SyncFile {
	file := models.SyncFile{SyncPathId: s.SyncPathId, AccountId: s.Account.ID, SourceType: models.SourceTypeBaiduPan,
		FileId: remote, PickCode: id, ParentId: filepath.Dir(remote), Path: filepath.Dir(remote), FileName: filepath.Base(remote),
		FileType: v115open.TypeFile, IsVideo: !directory, LocalFilePath: local}
	if directory {
		file.FileType = v115open.TypeDir
	}
	return file
}

func TestMovedUnreadableDirectoryKeepsOldSubtree(t *testing.T) {
	s, _ := newSavedBaiduTestSyncer(t, "/media")
	s.FullSync = true
	models.SettingsGlobal.OpenlistRetry = 0
	// 通配字符不能把其他兄弟目录纳入失败范围。
	oldRemote := "/media/old_%!"
	oldDir := filepath.Join(s.TargetPath, "media", "previous-directory-target")
	rows := []models.SyncFile{baiduDirectoryFailureRow(s, oldRemote, "10", oldDir, true)}
	protected := []string{filepath.Join(oldDir, "movie.strm"), filepath.Join(s.TargetPath, "media", "new", "keep.strm")}
	rows = append(rows, baiduDirectoryFailureRow(s, oldRemote+"/movie.mkv", "11", protected[0], false),
		baiduDirectoryFailureRow(s, "/media/new/keep.mkv", "12", protected[1], false))
	protected = append(protected, filepath.Join(oldDir, "untracked", "keep.strm"))
	// 超过一页的后代仍使用历史本地目标；这些目标不能仅靠当前远端路径重算。
	for i := range 260 {
		local := filepath.Join(s.TargetPath, "media", "previous-target", fmt.Sprintf("%03d.strm", i))
		protected = append(protected, local)
		rows = append(rows, baiduDirectoryFailureRow(s, fmt.Sprintf("%s/deep/%03d.mkv", oldRemote, i), fmt.Sprint(100+i), local, false))
	}
	good := filepath.Join(s.TargetPath, "media", "oldXanything!", "stale.strm")
	rows = append(rows, baiduDirectoryFailureRow(s, "/media/oldXanything!/stale.mkv", "999", good, false))
	for _, local := range append(append([]string(nil), protected...), good) {
		writeScopeFile(t, local)
	}
	if err := db.Db.CreateInBatches(&rows, 32).Error; err != nil {
		t.Fatal(err)
	}
	s.SyncDriver = &failureScanDriver{list: func(_ context.Context, remote, _ string) ([]*SyncFileCache, error) {
		switch remote {
		case "/media":
			return []*SyncFileCache{{SourceType: models.SourceTypeBaiduPan, FileId: "/media/new", PickCode: "10",
				ParentId: "/media", Path: "/media", FileName: "new", FileType: v115open.TypeDir}}, nil
		case "/media/new":
			return nil, errors.New("temporary list error")
		default:
			return nil, fmt.Errorf("unexpected directory %s", remote)
		}
	}}
	s.StartOther()
	if len(s.PathErrChan) != 0 {
		t.Fatal(<-s.PathErrChan)
	}
	if err := s.prepareCleanupProtection(); err != nil {
		t.Fatal(err)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	for _, local := range protected {
		requireScopeFile(t, local, true)
	}
	requireScopeFile(t, good, false)
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var remaining []models.SyncFile
	if err := db.Db.Order("id ASC").Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != len(rows)-1 {
		t.Fatalf("remaining ledger=%d want=%d", len(remaining), len(rows)-1)
	}
	for i, row := range remaining {
		if row.ID != rows[i].ID || row.FileId != rows[i].FileId || row.LocalFilePath != rows[i].LocalFilePath {
			t.Fatalf("old protected ledger overwritten: %+v", row)
		}
	}
	status, err := s.scanOutcome()
	result := s.scanResultSnapshot()
	if status != models.SyncStatusIncomplete || err == nil || result.CleanupStatus != "partial" || len(result.Failures) != 2 {
		t.Fatalf("status=%d error=%v result=%+v", status, err, result)
	}
}

func TestBaiduUnreadableDirectoryUncertainIdentityProtectsRoot(t *testing.T) {
	for _, mode := range []string{"no_identity", "no_ledger", "duplicate_identity", "wrong_account", "wrong_sync_path", "malformed_ledger"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := newSavedBaiduTestSyncer(t, "/media")
			models.SettingsGlobal.OpenlistRetry = 0
			oldDir := filepath.Join(s.TargetPath, "media", "old")
			old := baiduDirectoryFailureRow(s, "/media/old", "10", oldDir, true)
			identity := "10"
			switch mode {
			case "no_identity":
				identity = ""
			case "wrong_account":
				old.AccountId++
			case "wrong_sync_path":
				old.SyncPathId++
			case "malformed_ledger":
				old.FileId = "/outside/root"
			}
			if mode != "no_ledger" {
				if err := db.Db.Create(&old).Error; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "duplicate_identity" {
				other := baiduDirectoryFailureRow(s, "/media/another-old", "10", filepath.Join(s.TargetPath, "media", "another-old"), true)
				if err := db.Db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			}
			locals := []string{filepath.Join(oldDir, "movie.strm"), filepath.Join(s.TargetPath, "media", "sibling", "stale.strm")}
			for _, local := range locals {
				writeScopeFile(t, local)
			}
			s.SyncDriver = &failureScanDriver{list: func(_ context.Context, remote, _ string) ([]*SyncFileCache, error) {
				if remote == "/media" {
					return []*SyncFileCache{{SourceType: models.SourceTypeBaiduPan, FileId: "/media/new", PickCode: identity,
						ParentId: "/media", Path: "/media", FileName: "new", FileType: v115open.TypeDir}}, nil
				}
				return nil, errors.New("unreadable")
			}}
			s.StartOther()
			if len(s.PathErrChan) != 0 {
				t.Fatal(<-s.PathErrChan)
			}
			if err := s.prepareCleanupProtection(); err != nil {
				t.Fatal(err)
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			for _, local := range locals {
				requireScopeFile(t, local, true)
			}
			if result := s.scanResultSnapshot(); result.CleanupStatus != "skipped" {
				t.Fatalf("uncertain old location allowed cleanup: %+v", result)
			}
		})
	}
}

func TestBaiduUnreadableDirectoryLedgerLookupFailureIsFatal(t *testing.T) {
	s, _ := newSavedBaiduTestSyncer(t, "/media")
	want := errors.New("ledger unavailable")
	if err := db.Db.Callback().Query().Before("gorm:query").Register("failed_directory_ledger", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			tx.AddError(want)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := s.recordOtherDirectoryFailure(s.Context, pathQueueItem{Path: "/media/new", PickCode: "10"}, errors.New("unreadable"))
	if !errors.Is(err, want) || !isFatalSyncError(err) {
		t.Fatalf("necessary lookup failure lost: %v", err)
	}
}
