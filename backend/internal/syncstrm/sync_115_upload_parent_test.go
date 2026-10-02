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

type uploadParent115Driver struct {
	fakeDirectoryScanDriver
	total  int64
	lookup func(context.Context, string) (string, error)
	detail func(context.Context, string) (*SyncFileCache, error)
}

func (d *uploadParent115Driver) GetTotalFileCount(context.Context) (int64, string, error) {
	return d.total, "other-file", nil
}

func (d *uploadParent115Driver) GetFilesByPathId(context.Context, string, int, int) ([]v115open.File, error) {
	return []v115open.File{{FileId: "other-file", Pid: "root", FileName: "unrelated.txt", FileCategory: v115open.TypeFile}}, nil
}

func (d *uploadParent115Driver) GetPathIdByPath(ctx context.Context, remote string) (string, error) {
	return d.lookup(ctx, remote)
}

func (d *uploadParent115Driver) DetailByFileId(ctx context.Context, id string) (*SyncFileCache, error) {
	return d.detail(ctx, id)
}

func new115UploadParentSync(t *testing.T) (*SyncStrm, models.SyncFile) {
	t.Helper()
	account, sp := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, sp.ID, "Media", "root", t.TempDir(), SyncStrmConfig{
		VideoExt: []string{".mkv"}, MetaExt: []string{".nfo"}, EnableDownloadMeta: 1,
		NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload,
	}, false, 100, false, false)
	t.Cleanup(s.Cancel)
	dir := models.SyncFile{SyncPathId: sp.ID, AccountId: account.ID, SourceType: models.SourceType115,
		FileId: "empty-dir", FileName: "Empty", ParentId: "root", Path: "Media", FileType: v115open.TypeDir,
		LocalFilePath: filepath.Join(s.TargetPath, "Media", "Empty"), MTime: 1}
	if err := db.Db.Create(&dir).Error; err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func uploadParent115Detail(id string) *SyncFileCache {
	return &SyncFileCache{SourceType: models.SourceType115, FileId: id, FileType: v115open.TypeDir,
		FileName: "Empty", Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}}}
}

func Test115EmptyExistingDirectoryPreservesUploadMetadata(t *testing.T) {
	for _, total := range []int64{0, 1} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			for _, name := range []string{"movie.nfo", "another.nfo"} {
				writeScopeFile(t, filepath.Join(old.LocalFilePath, name))
			}
			lookups, details := 0, 0
			s.SyncDriver = &uploadParent115Driver{total: total,
				lookup: func(_ context.Context, remote string) (string, error) {
					lookups++
					if remote != "Media/Empty" {
						t.Errorf("unexpected lookup %q", remote)
					}
					return "empty-dir", nil
				},
				detail: func(_ context.Context, id string) (*SyncFileCache, error) {
					details++
					return uploadParent115Detail(id), nil
				},
			}
			s.Start115Sync()
			if len(s.PathErrChan) != 0 {
				t.Fatal(<-s.PathErrChan)
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			if lookups != 1 || details != 1 || s.NewUpload != 2 {
				t.Fatalf("lookup=%d detail=%d uploaded=%d", lookups, details, s.NewUpload)
			}
			var uploads []models.DbUploadTask
			if err := db.Db.Find(&uploads).Error; err != nil {
				t.Fatal(err)
			}
			if len(uploads) != 2 {
				t.Fatalf("uploads=%+v", uploads)
			}
			for _, task := range uploads {
				requireScopeFile(t, task.LocalFullPath, true)
				if task.RemotePathId != "empty-dir" || filepath.Dir(task.RemoteFullPath) != "Media/Empty" {
					t.Fatalf("wrong remote parent: %+v", task)
				}
			}
			if status, err := s.scanOutcome(); status != models.SyncStatusCompleted || err != nil {
				t.Fatalf("status=%d error=%v", status, err)
			}
			if err := s.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			var remaining models.SyncFile
			if err := db.Db.First(&remaining, old.ID).Error; err != nil || remaining.FileId != old.FileId {
				t.Fatalf("confirmed empty directory lost: %+v %v", remaining, err)
			}
		})
	}
}

func Test115UploadParentFailureProtectsBeforeAnyLocalDeletion(t *testing.T) {
	for _, mode := range []string{"lookup_failure", "empty_id", "detail_failure", "wrong_id", "wrong_path", "wrong_type", "auth", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			files := []string{
				filepath.Join(old.LocalFilePath, "00-before.strm"),
				filepath.Join(old.LocalFilePath, "movie.nfo"),
				filepath.Join(old.LocalFilePath, "another.nfo"),
				filepath.Join(old.LocalFilePath, "child", "keep.strm"),
			}
			for _, file := range files {
				writeScopeFile(t, file)
			}
			good := filepath.Join(s.TargetPath, "Media", "ZGood", "stale.strm")
			writeScopeFile(t, good)
			lookups, details := 0, 0
			s.SyncDriver = &uploadParent115Driver{
				lookup: func(context.Context, string) (string, error) {
					lookups++
					switch mode {
					case "lookup_failure":
						return "", errors.New("lookup unavailable")
					case "empty_id":
						return "", nil
					case "auth":
						return "", v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "expired")
					case "cancel":
						s.Cancel()
						return "", context.Canceled
					}
					return old.FileId, nil
				},
				detail: func(_ context.Context, id string) (*SyncFileCache, error) {
					details++
					file := uploadParent115Detail(id)
					switch mode {
					case "detail_failure":
						return nil, context.DeadlineExceeded
					case "wrong_id":
						file.FileId = "another-id"
					case "wrong_path":
						file.FileName = "Moved"
					case "wrong_type":
						file.FileType = v115open.TypeFile
					}
					return file, nil
				},
			}
			s.Start115Sync()
			err := s.compareLocalFilesWithTempTable()
			fatal := mode == "auth" || mode == "cancel"
			if (err != nil) != fatal || fatal && !isFatalSyncError(err) {
				t.Fatalf("fatal=%v error=%v", fatal, err)
			}
			for _, file := range files {
				requireScopeFile(t, file, true)
			}
			requireScopeFile(t, good, fatal)
			if lookups != 1 || details > 1 || s.NewUpload != 0 {
				t.Fatalf("lookup=%d detail=%d uploaded=%d", lookups, details, s.NewUpload)
			}
			if cached, _ := s.memSyncCache.GetByFileId(old.FileId); cached != nil {
				t.Fatalf("unverified old directory entered cache: %+v", cached)
			}
			if status, _ := s.scanOutcome(); status != models.SyncStatusIncomplete {
				t.Fatalf("status=%d", status)
			}
			if !fatal {
				if err := s.handleTempTableDiff(); err != nil {
					t.Fatal(err)
				}
				if err := db.Db.First(&models.SyncFile{}, old.ID).Error; err != nil {
					t.Fatalf("old ledger removed: %v", err)
				}
			}
		})
	}
}

func Test115UploadParentConfirmedAbsenceAndCurrentRelations(t *testing.T) {
	for _, mode := range []string{"absent", "known", "conflict", "no_upload_candidate"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			metadata := filepath.Join(old.LocalFilePath, "movie.nfo")
			writeScopeFile(t, metadata)
			if mode == "no_upload_candidate" {
				s.Config.NetNotFoundFileAction = models.SyncTreeItemMetaActionKeep
			}
			calls := 0
			s.SyncDriver = &uploadParent115Driver{lookup: func(context.Context, string) (string, error) {
				calls++
				return "", v115open.NewOpenAPIError(430004, "absent")
			}, detail: func(context.Context, string) (*SyncFileCache, error) {
				if mode == "absent" {
					return nil, v115open.NewOpenAPIError(430004, "directory deleted")
				}
				t.Fatal("known directory must not query details")
				return nil, nil
			}}
			s.Start115Sync()
			if mode == "known" || mode == "conflict" {
				if err := s.publish115Paths(s.Context, map[string]verified115Path{old.FileId: {path: "Media/Empty", parentID: "root", within: true}}); err != nil {
					t.Fatal(err)
				}
				if mode == "conflict" {
					if err := s.publish115Paths(s.Context, map[string]verified115Path{"other-id": {path: "Media/Empty", parentID: "root", within: true}}); err == nil {
						t.Fatal("two identities claimed the same directory")
					}
				}
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, metadata, mode != "absent")
			wantCalls, wantUploads := 0, int64(0)
			if mode == "absent" {
				wantCalls = 1
			}
			if mode == "known" {
				wantUploads = 1
			}
			if calls != wantCalls || s.NewUpload != wantUploads {
				t.Fatalf("calls=%d uploads=%d", calls, s.NewUpload)
			}
		})
	}
}

func Test115AbsentUploadPathChecksPreviousDirectoryIdentity(t *testing.T) {
	for _, mode := range []string{"moved", "deleted", "detail_failure", "ambiguous_ledger", "db_failure", "no_ledger", "other_account", "other_sync_path"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			locals := []string{filepath.Join(old.LocalFilePath, "00-before.strm"), filepath.Join(old.LocalFilePath, "movie.nfo"), filepath.Join(old.LocalFilePath, "another.nfo")}
			for _, local := range locals {
				writeScopeFile(t, local)
			}
			switch mode {
			case "no_ledger":
				if err := db.Db.Delete(&old).Error; err != nil {
					t.Fatal(err)
				}
			case "other_account", "other_sync_path":
				column := "account_id"
				if mode == "other_sync_path" {
					column = "sync_path_id"
				}
				if err := db.Db.Model(&old).Update(column, 999).Error; err != nil {
					t.Fatal(err)
				}
			case "ambiguous_ledger":
				other := old
				other.ID = 0
				other.FileId = "another-dir"
				if err := db.Db.Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			}
			lookups, details := 0, 0
			s.SyncDriver = &uploadParent115Driver{
				lookup: func(context.Context, string) (string, error) {
					lookups++
					return "", v115open.NewOpenAPIError(430004, "path absent")
				},
				detail: func(_ context.Context, id string) (*SyncFileCache, error) {
					details++
					if id != old.FileId {
						t.Errorf("queried unrelated identity %q", id)
					}
					if mode == "deleted" {
						return nil, v115open.NewOpenAPIError(430004, "directory deleted")
					}
					if mode == "detail_failure" {
						return nil, errors.New("detail unavailable")
					}
					file := uploadParent115Detail(id)
					file.FileName = "Moved"
					return file, nil
				},
			}
			s.Start115Sync()
			if mode == "db_failure" {
				if err := db.Db.Callback().Query().Before("gorm:query").Register("upload_parent_lookup_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						tx.AddError(errors.New("ledger unavailable"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			err := s.compareLocalFilesWithTempTable()
			if (err != nil) != (mode == "db_failure") || mode == "db_failure" && !isFatalSyncError(err) {
				t.Fatalf("unexpected comparison error: %v", err)
			}
			protected := mode == "moved" || mode == "detail_failure" || mode == "ambiguous_ledger" || mode == "db_failure"
			for _, local := range locals {
				requireScopeFile(t, local, protected)
			}
			wantDetails := 0
			if mode == "moved" || mode == "deleted" || mode == "detail_failure" {
				wantDetails = 1
			}
			if lookups != 1 || details != wantDetails || s.NewUpload != 0 {
				t.Fatalf("lookups=%d details=%d uploads=%d", lookups, details, s.NewUpload)
			}
			if mode == "moved" || mode == "detail_failure" || mode == "deleted" {
				if err := s.handleTempTableDiff(); err != nil {
					t.Fatal(err)
				}
				var count int64
				if err := db.Db.Model(&models.SyncFile{}).Where("id = ?", old.ID).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if (count == 1) != protected {
					t.Fatalf("protected=%v remaining old rows=%d", protected, count)
				}
			}
		})
	}
}

type uploadParent115DetailPathDriver struct {
	uploadParent115Driver
	byPath func(context.Context, string) (*SyncFileCache, error)
}

func (d *uploadParent115DetailPathDriver) DetailByPath(ctx context.Context, path string) (*SyncFileCache, error) {
	return d.byPath(ctx, path)
}

func Test115UploadParentReusesCompletePathDetail(t *testing.T) {
	for _, mode := range []string{"complete", "incomplete", "wrong_path", "wrong_type", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			writeScopeFile(t, filepath.Join(old.LocalFilePath, "movie.nfo"))
			lookups, details := 0, 0
			s.SyncDriver = &uploadParent115DetailPathDriver{
				uploadParent115Driver: uploadParent115Driver{detail: func(_ context.Context, id string) (*SyncFileCache, error) {
					details++
					return uploadParent115Detail(id), nil
				}},
				byPath: func(context.Context, string) (*SyncFileCache, error) {
					lookups++
					f := uploadParent115Detail(old.FileId)
					switch mode {
					case "incomplete":
						f.Paths = nil
					case "wrong_path":
						f.FileName = "Moved"
					case "wrong_type":
						f.FileType = v115open.TypeFile
					case "cancel":
						s.Cancel()
					}
					return f, nil
				}}
			s.Start115Sync()
			err := s.compareLocalFilesWithTempTable()
			if (err != nil) != (mode == "cancel") {
				t.Fatalf("err=%v", err)
			}
			wantDetails := 0
			if mode == "incomplete" {
				wantDetails = 1
			}
			wantUploads := int64(0)
			if mode == "complete" || mode == "incomplete" {
				wantUploads = 1
			}
			if lookups != 1 || details != wantDetails || s.NewUpload != wantUploads {
				t.Fatalf("lookups=%d details=%d uploads=%d", lookups, details, s.NewUpload)
			}
			requireScopeFile(t, filepath.Join(old.LocalFilePath, "movie.nfo"), true)
		})
	}
}

func Test115PathDetailConflictProtectsEntireRoot(t *testing.T) {
	s, old := new115UploadParentSync(t)
	s.sync115 = &Sync115{}
	if err := s.publish115Paths(s.Context, map[string]verified115Path{"root": {path: "DifferentRoot", root: true, within: true}}); err != nil {
		t.Fatal(err)
	}
	s.SyncDriver = &uploadParent115DetailPathDriver{byPath: func(context.Context, string) (*SyncFileCache, error) { return uploadParent115Detail(old.FileId), nil }}
	if err := s.confirm115UploadDirectory("Media/Empty"); err == nil {
		t.Fatal("expected ancestor conflict")
	}
	if !s.cleanupProtected(filepath.Join(s.TargetPath, "Media", "Sibling", "old.strm"), false) {
		t.Fatal("conflict did not protect independent root subtree")
	}
}

func Test115LateHistoricalMoveProtectsRootWithoutRescan(t *testing.T) {
	for _, moved := range []bool{false, true} {
		t.Run(fmt.Sprint(moved), func(t *testing.T) {
			s, _ := new115UploadParentSync(t)
			s.sync115 = &Sync115{historicalPaths: map[string]verified115Path{"ancestor": {path: "Media/Old", parentID: "root", within: true}, "child": {path: "Media/Old/Child", parentID: "ancestor", within: true}}}
			if err := s.memSyncCache.Insert(&SyncFileCache{FileId: "video", ParentId: "child", FileName: "movie.mkv", FileType: v115open.TypeFile, IsVideo: true}); err != nil {
				t.Fatal(err)
			}
			if err := s.apply115Paths(); err != nil {
				t.Fatal(err)
			}
			calls := 0
			parentID, parentName := "other", "Unrelated"
			if moved {
				parentID, parentName = "ancestor", "Moved"
			}
			s.SyncDriver = &uploadParent115DetailPathDriver{byPath: func(context.Context, string) (*SyncFileCache, error) {
				calls++
				return &SyncFileCache{FileId: "empty", FileName: "Empty", FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: parentID, Name: parentName}}}, nil
			}}
			err := s.confirm115UploadDirectory("Media/" + parentName + "/Empty")
			if (err != nil) != moved || calls != 1 {
				t.Fatalf("moved=%v calls=%d err=%v", moved, calls, err)
			}
			if protected := s.cleanupProtected(filepath.Join(s.TargetPath, "Media", "Sibling", "keep.strm"), false); protected != moved {
				t.Fatalf("protected=%v moved=%v", protected, moved)
			}
			if moved {
				if _, ok := s.sync115.historicalPaths["child"]; ok {
					t.Fatal("late move retained stale child history")
				}
			}
		})
	}
}
