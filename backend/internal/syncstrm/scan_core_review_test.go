package syncstrm

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestLedgerUpdateChecksProtectedDestinationAndFreshPickCode(t *testing.T) {
	s := newScanResultTestSync(t)
	s.Account.SourceType = models.SourceType115
	s.SourcePath = "/media"
	old := []models.SyncFile{
		{SyncPathId: 7, FileId: "moving", Path: "/media/safe", FileName: "movie.mkv", LocalFilePath: filepath.Join(s.TargetPath, "media/safe/movie.strm"), PickCode: "old-moving", IsVideo: true},
		{SyncPathId: 7, FileId: "replacement", Path: "/media/safe", FileName: "replacement.mkv", LocalFilePath: filepath.Join(s.TargetPath, "media/safe/replacement.strm"), PickCode: "old-id", IsVideo: true},
	}
	if err := db.Db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	for _, file := range []*SyncFileCache{
		{SourceType: models.SourceType115, FileType: v115open.TypeFile, FileId: "moving", Path: "/media/failed", FileName: "movie.mkv", PickCode: "new-moving", IsVideo: true},
		{SourceType: models.SourceType115, FileType: v115open.TypeFile, FileId: "replacement", Path: "/media/safe", FileName: "replacement.mkv", PickCode: "new-id", IsVideo: true},
	} {
		file.GetLocalFilePath(s.TargetPath, s.SourcePath)
		if err := s.memSyncCache.Insert(file); err != nil {
			t.Fatal(err)
		}
	}
	s.recordScanFailure("/media/failed", errors.New("directory read failed"))
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var rows []models.SyncFile
	if err := db.Db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows[0].Path != old[0].Path || rows[0].LocalFilePath != old[0].LocalFilePath || rows[0].PickCode != old[0].PickCode {
		t.Errorf("protected destination overwrote prior ledger: %+v", rows[0])
	}
	if rows[1].PickCode != "new-id" {
		t.Errorf("fresh identity not committed: %+v", rows[1])
	}
}

func TestFailedMoveProtectsLegacyLedgerWithoutLocalPath(t *testing.T) {
	s := newScanResultTestSync(t)
	s.Account.SourceType = models.SourceType115
	s.SourcePath = "/media"
	old := models.SyncFile{SyncPathId: 7, FileId: "moving", Path: "/media/old", FileName: "movie.mkv", IsVideo: true}
	if err := db.Db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	oldPath := s.MakeFullLocalPath(&old)
	writeScopeFile(t, oldPath)
	s.recordFileFailure(&SyncFileCache{SourceType: models.SourceType115, FileType: v115open.TypeFile, FileId: "moving", Path: "/media/new", FileName: "movie.mkv", IsVideo: true}, errors.New("write failed"))
	if err := s.prepareCleanupProtection(); err != nil {
		t.Fatal(err)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	requireScopeFile(t, oldPath, true)
}

type cleanupAuthDriver struct {
	fakeDirectoryScanDriver
	calls int
}

func (d *cleanupAuthDriver) GetPathIdByPath(context.Context, string) (string, error) {
	return "", v115open.NewOpenAPIError(430004, "directory absent")
}

func (d *cleanupAuthDriver) CreateDirRecursively(context.Context, string) (string, string, error) {
	d.calls++
	return "", "", v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "auth failed")
}
func (d *cleanupAuthDriver) DeleteFile(context.Context, string, []string) error {
	d.calls++
	return v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "auth failed")
}

func TestLocalComparisonAuthenticationFailureStopsRemainingFiles(t *testing.T) {
	for _, mode := range []string{"create", "delete"} {
		t.Run(mode, func(t *testing.T) {
			account, path := setupStrmExclusionTestDB(t)
			s := newSyncStrm(account, path.ID, "/media", "root", t.TempDir(), SyncStrmConfig{MetaExt: []string{".nfo"}, EnableDownloadMeta: 1, CheckMetaMtime: 1, NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload}, false, 0, false, false)
			t.Cleanup(s.Cancel)
			driver := &cleanupAuthDriver{}
			s.SyncDriver = driver
			for _, name := range []string{"a.nfo", "b.nfo"} {
				local := filepath.Join(s.TargetPath, "media/extras", name)
				writeScopeFile(t, local)
				if mode == "delete" {
					file := &SyncFileCache{SourceType: models.SourceType115, FileType: v115open.TypeFile, FileId: name, ParentId: "extras", Path: "/media/extras", FileName: name, IsMeta: true, MTime: 1, FileSize: 1, LocalFilePath: local}
					if err := s.memSyncCache.Insert(file); err != nil {
						t.Fatal(err)
					}
				}
			}
			err := s.compareLocalFilesWithTempTable()
			if !isFatalSyncError(err) || driver.calls != 1 {
				t.Fatalf("error=%v calls=%d; shared auth must stop remaining files", err, driver.calls)
			}
			for _, name := range []string{"a.nfo", "b.nfo"} {
				requireScopeFile(t, filepath.Join(s.TargetPath, "media/extras", name), true)
			}
		})
	}
}

func TestScanRetryNewInstanceConvergesWithoutRewritingSuccessfulFiles(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	account.SourceType = models.SourceTypeBaiduPan
	if err := db.Db.Model(path).Updates(map[string]any{"last_sync_at": 11, "is_full_sync": true}).Error; err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	models.SettingsGlobal.OpenlistRetry = 0
	for run := 0; run < 2; run++ {
		s := newSyncStrm(account, path.ID, "/media", "root", target, SyncStrmConfig{VideoExt: []string{".mkv"}, StrmBaseUrl: "http://qms.test", StrmUrlNeedPath: 1}, true, 11, false, true)
		t.Cleanup(s.Cancel)
		urlDriver := NewBaiduPanDriver(nil)
		urlDriver.SetSyncStrm(s)
		s.SyncDriver = &failureScanDriver{list: func(context.Context, string, string) ([]*SyncFileCache, error) {
			var files []*SyncFileCache
			for _, name := range []string{"bad", "good"} {
				files = append(files, &SyncFileCache{SourceType: models.SourceTypeBaiduPan, FileType: v115open.TypeFile, FileId: "/media/" + name + ".mkv", PickCode: name, Path: "/media", ParentId: "/media", FileName: name + ".mkv", MTime: 100})
			}
			return files, nil
		}, content: func(file *SyncFileCache) string {
			if run == 0 && file.PickCode == "bad" {
				return ""
			}
			return urlDriver.MakeStrmContent(file)
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
		if err := s.completeSync(); err != nil {
			t.Fatal(err)
		}
		if err := s.handleTempTableDiff(); err != nil {
			t.Fatal(err)
		}
		var saved models.SyncPath
		if err := db.Db.First(&saved, path.ID).Error; err != nil {
			t.Fatal(err)
		}
		if s.NewStrm != 1 {
			t.Fatalf("run %d generated=%d; retry should write only formerly failed file", run, s.NewStrm)
		}
		if run == 0 {
			if s.Sync.Status != models.SyncStatusPartial || saved.LastSyncAt != 11 || !saved.IsFullSync {
				t.Fatalf("failed run advanced root: result=%+v path=%+v", s.Sync, saved)
			}
		} else {
			if s.Sync.Status != models.SyncStatusCompleted || saved.LastSyncAt <= 11 || saved.IsFullSync {
				t.Fatalf("retry did not converge: result=%+v path=%+v", s.Sync, saved)
			}
		}
		good, err := os.Stat(filepath.Join(target, "media/good.strm"))
		if err != nil {
			t.Fatal(err)
		}
		if !good.ModTime().Equal(time.Unix(100, 0)) {
			t.Fatalf("unexpected successful file time: %v", good.ModTime())
		}
	}
}

func TestBaiduPositiveMoveDoesNotReturnFromOldLedgerNextIncrement(t *testing.T) {
	for _, mode := range []string{"insert", "update", "insert_failure"} {
		t.Run(mode, func(t *testing.T) {
			failInsert := mode == "insert_failure"
			first := true
			s := newBaiduIncrementalTestSyncer(t, func(*http.Request, int) any {
				if first {
					return baidupan.FileListAllResponse{List: []*baidupan.FileListAllItem{{FsId: 1, Path: "/media/new.mkv", Size: 2048, ServerMtime: 200}}}
				}
				return baidupan.FileListAllResponse{}
			})
			old := models.SyncFile{SyncPathId: s.SyncPathId, SourceType: models.SourceTypeBaiduPan, FileId: "/media/old.mkv", PickCode: "1", Path: "/media", ParentId: "/media", FileName: "old.mkv", FileType: v115open.TypeFile, IsVideo: true}
			if err := db.Db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "update" {
				existing := old
				existing.ID, existing.FileId, existing.FileName = 0, "/media/new.mkv", "new.mkv"
				existing.PickCode = "2"
				if err := db.Db.Create(&existing).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := s.StartBaiduPanSyncByMtime(100); err != nil {
				t.Fatal(err)
			}
			cause := errors.New("new ledger insert failed")
			if failInsert {
				if err := db.Db.Callback().Create().Before("gorm:create").Register("review:fail-new-ledger", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						tx.AddError(cause)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Create().Remove("review:fail-new-ledger") })
			}
			err := s.handleTempTableDiff()
			if failInsert {
				if !errors.Is(err, cause) {
					t.Fatalf("error=%v", err)
				}
				var retained models.SyncFile
				if err := db.Db.First(&retained, old.ID).Error; err != nil {
					t.Fatalf("old ledger deleted before new fact was durable: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			first = false
			next := newSyncStrm(s.Account, s.SyncPathId, s.SourcePath, s.SourcePathId, s.TargetPath, s.Config, false, 100, false, false)
			t.Cleanup(next.Cancel)
			driver := NewBaiduPanDriver(s.SyncDriver.(*BaiduPanDriver).client)
			driver.SetSyncStrm(next)
			next.SyncDriver = driver
			if err := next.StartBaiduPanSyncByMtime(100); err != nil {
				t.Fatal(err)
			}
			if _, err := next.memSyncCache.GetByFileId(old.FileId); err == nil {
				t.Fatal("old path resurrected after a positively confirmed move")
			}
			if file, err := next.memSyncCache.GetByFileId("/media/new.mkv"); err != nil || file.PickCode != "1" {
				t.Fatalf("new fact lost: %+v %v", file, err)
			}
		})
	}
}

func TestStartFileRecordsKnownOutcome(t *testing.T) {
	for _, mode := range []string{"success", "failure", "excluded", "masked"} {
		t.Run(mode, func(t *testing.T) {
			s := newFailureScanSyncer(t)
			s.TmpSyncPath, s.IsFile = true, true
			file := &SyncFileCache{SourceType: models.SourceType115, FileType: v115open.TypeFile, FileId: s.SourcePathId, Path: "/media", FileName: "movie.mkv", PickCode: "pick", IsVideo: true}
			content := "http://qms.test/video"
			if mode == "failure" {
				content = ""
			}
			if mode == "excluded" {
				s.Config.ExcludeNames = []string{file.FileName}
			}
			if mode == "masked" {
				file.Path = "/media/**"
			}
			s.SyncDriver = &fakeDirectoryScanDriver{detailsByID: map[string]*SyncFileCache{s.SourcePathId: file}, strmContent: content}
			err := s.StartFile()
			if (err != nil) != (mode == "failure") {
				t.Fatalf("error=%v", err)
			}
			result := s.scanResultSnapshot()
			var success, failed, skipped int
			switch mode {
			case "success":
				success = 1
			case "failure":
				failed = 1
			default:
				skipped = 1
			}
			if result.SucceededFiles != success || result.FailedFiles != failed || result.SkippedFiles != skipped {
				t.Fatalf("outcome=%+v", result)
			}
		})
	}
}

func TestMaskedMetadataRemainsSkippedAfterOuterSuccess(t *testing.T) {
	s := newFailureScanSyncer(t)
	s.Config.EnableDownloadMeta = 1
	file := &SyncFileCache{SourceType: models.SourceType115, FileType: v115open.TypeFile, FileId: "meta", Path: "/media/**", FileName: "movie.nfo", IsMeta: true}
	if err := s.processNetFile(file); err != nil {
		t.Fatal(err)
	}
	s.recordFileSuccess(file)
	result := s.scanResultSnapshot()
	if result.SucceededFiles != 0 || result.SkippedFiles != 1 || result.FailedFiles != 0 {
		t.Fatalf("outcome=%+v", result)
	}
}
