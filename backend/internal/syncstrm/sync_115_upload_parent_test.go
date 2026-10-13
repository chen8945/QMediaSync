package syncstrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
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
	dirs   func(context.Context, string) ([]pathQueueItem, error)
	create func(context.Context, string) (string, string, error)
}

func (d *uploadParent115Driver) CreateDirRecursively(ctx context.Context, local string) (string, string, error) {
	if d.create == nil {
		return "", "", errors.New("unexpected directory creation")
	}
	return d.create(ctx, local)
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
	if d.detail == nil {
		return nil, errors.New("unconfigured directory detail")
	}
	return d.detail(ctx, id)
}

func (d *uploadParent115Driver) GetDirsByPathId(ctx context.Context, id string) ([]pathQueueItem, error) {
	if d.dirs != nil {
		return d.dirs(ctx, id)
	}
	if items, ok := d.dirsByID[id]; ok {
		return items, nil
	}
	return nil, errors.New("directory list unavailable")
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

type emptyParentList115Driver struct {
	*open115Driver
}

func (d *emptyParentList115Driver) DetailByFileId(context.Context, string) (*SyncFileCache, error) {
	return nil, v115open.NewOpenAPIError(430004, "directory deleted")
}

func Test115EmptyParentListingMustMatchRequestedLocation(t *testing.T) {
	for _, mode := range []string{"unchanged", "renamed", "moved_inside", "moved_outside"} {
		t.Run(mode, func(t *testing.T) {
			s, parent := new115UploadParentSync(t)
			child := parent
			child.ID, child.FileId, child.FileName, child.ParentId = 0, "child-dir", "Child", parent.FileId
			child.Path = "Media/Empty"
			child.LocalFilePath = filepath.Join(parent.LocalFilePath, "Child")
			if err := db.Db.Create(&child).Error; err != nil {
				t.Fatal(err)
			}
			locals := []string{filepath.Join(child.LocalFilePath, "00-before.strm"), filepath.Join(child.LocalFilePath, "movie.nfo")}
			for _, local := range locals {
				writeScopeFile(t, local)
			}
			s.sync115 = &Sync115{}
			if err := s.publish115DirectoryList(s.Context, "Media", "root", []pathQueueItem{{PathId: parent.FileId, Path: "Media/Empty"}}); err != nil {
				t.Fatal(err)
			}
			driver := NewOpen115Driver(nil)
			driver.SetSyncStrm(s)
			s.SyncDriver = &emptyParentList115Driver{open115Driver: driver}
			original := list115FilesPage
			t.Cleanup(func() { list115FilesPage = original })
			calls := 0
			list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, id string, _, _, _ bool, offset, _ int) (*v115open.FileListResp, error) {
				calls++
				if id != parent.FileId || offset != 0 {
					t.Fatalf("意外目录请求：id=%s offset=%d", id, offset)
				}
				parts := []v115open.FileParentPath{{FileId: "0"}, {FileId: "root", Name: "Media"}}
				name, parentPath := "Empty", "Media"
				switch mode {
				case "renamed":
					name = "Renamed"
				case "moved_inside":
					parts = append(parts, v115open.FileParentPath{FileId: "other", Name: "Other"})
					parentPath = "Media/Other"
				case "moved_outside":
					parts = []v115open.FileParentPath{{FileId: "0"}, {FileId: "outside", Name: "Outside"}}
					parentPath = "Outside"
				}
				parts = append(parts, v115open.FileParentPath{FileId: json.Number(id), Name: name})
				return &v115open.FileListResp{Path: parts, PathStr: parentPath + "/" + name}, nil
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			protected := mode != "unchanged"
			for _, local := range locals {
				requireScopeFile(t, local, protected)
			}
			if calls != 1 || s.NewUpload != 0 || s.sync115.completeDirs["/Media/Empty"] == protected {
				t.Fatalf("目录事实错误：calls=%d uploads=%d complete=%v", calls, s.NewUpload, s.sync115.completeDirs)
			}
			if status, _ := s.scanOutcome(); (status == models.SyncStatusIncomplete) != protected {
				t.Fatalf("扫描结果错误：%+v", s.scanResultSnapshot())
			}
			if err := s.handleTempTableDiff(); err != nil {
				t.Fatal(err)
			}
			var remaining int64
			if err := db.Db.Model(&models.SyncFile{}).Where("id = ?", child.ID).Count(&remaining).Error; err != nil {
				t.Fatal(err)
			}
			if (remaining == 1) != protected {
				t.Fatalf("旧目录账本保护=%v，剩余=%d", protected, remaining)
			}
		})
	}
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
			if lookups != 0 || details != 1 || s.NewUpload != 2 {
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
			pathLookup := mode == "lookup_failure" || mode == "empty_id" || mode == "auth" || mode == "cancel"
			if pathLookup {
				if err := db.Db.Model(&old).Update("file_id", "").Error; err != nil {
					t.Fatal(err)
				}
			}
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
			wantLookups := 0
			if pathLookup {
				wantLookups = 1
			}
			if lookups != wantLookups || details > 1 || s.NewUpload != 0 {
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

func Test115UploadParentFallbackFailureScope(t *testing.T) {
	tests := []struct {
		name    string
		listErr error
		fatal   bool
	}{
		{
			name:    "authorization_expired",
			listErr: v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "expired"),
			fatal:   true,
		},
		{
			name:    "http_unauthorized",
			listErr: &v115open.OpenAPIError{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
			fatal:   true,
		},
		{
			name:    "temporary_list_failure",
			listErr: errors.New("directory list unavailable"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			files := []string{
				filepath.Join(old.LocalFilePath, "00-before.strm"),
				filepath.Join(old.LocalFilePath, "movie.nfo"),
				filepath.Join(old.LocalFilePath, "child", "keep.strm"),
			}
			for _, file := range files {
				writeScopeFile(t, file)
			}
			sibling := filepath.Join(s.TargetPath, "Media", "ZSibling", "stale.strm")
			writeScopeFile(t, sibling)
			details, lists := 0, 0
			s.SyncDriver = &uploadParent115Driver{
				detail: func(_ context.Context, id string) (*SyncFileCache, error) {
					details++
					if id != old.FileId {
						t.Fatalf("unexpected directory detail: %s", id)
					}
					return nil, fmt.Errorf("directory detail: %w", v115open.NewOpenAPIError(430004, "deleted"))
				},
				dirs: func(_ context.Context, id string) ([]pathQueueItem, error) {
					lists++
					if id != "root" {
						t.Fatalf("unexpected directory list: %s", id)
					}
					return nil, fmt.Errorf("parent directory list: %w", tt.listErr)
				},
			}
			err := s.compareLocalFilesWithTempTable()
			if tt.fatal {
				if !isFatalSyncError(err) || !errors.Is(err, tt.listErr) {
					t.Errorf("parent listing authentication failure did not stop comparison: %v", err)
				}
			} else if err != nil {
				t.Errorf("ordinary parent listing failure stopped comparison: %v", err)
			}
			if details != 1 || lists != 1 || s.NewUpload != 0 {
				t.Errorf("details=%d lists=%d uploads=%d", details, lists, s.NewUpload)
			}
			for _, file := range files {
				requireScopeFile(t, file, true)
			}
			requireScopeFile(t, sibling, tt.fatal)
			if status, _ := s.scanOutcome(); status != models.SyncStatusIncomplete {
				t.Fatalf("status=%d", status)
			}
			if !tt.fatal {
				if err := s.handleTempTableDiff(); err != nil {
					t.Fatal(err)
				}
				if err := db.Db.First(&models.SyncFile{}, old.ID).Error; err != nil {
					t.Fatalf("old directory ledger removed: %v", err)
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
			s.SyncDriver = &uploadParent115Driver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{dirsByID: map[string][]pathQueueItem{"root": nil}}, lookup: func(context.Context, string) (string, error) {
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
				dirs: func(_ context.Context, id string) ([]pathQueueItem, error) {
					if mode == "moved" {
						return []pathQueueItem{{PathId: old.FileId, Path: "Media/Moved"}}, nil
					}
					if mode == "deleted" {
						return nil, nil
					}
					return nil, errors.New("directory list unavailable")
				},
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
			protected := mode == "detail_failure" || mode == "db_failure"
			for _, local := range locals {
				requireScopeFile(t, local, protected)
			}
			wantDetails := 0
			if mode == "moved" || mode == "deleted" || mode == "detail_failure" {
				wantDetails = 1
			}
			wantLookups := 1
			if wantDetails != 0 || mode == "db_failure" {
				wantLookups = 0
			}
			if lookups != wantLookups || details != wantDetails || s.NewUpload != 0 {
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
			s.TmpSyncPath = true
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
	s.TmpSyncPath = true
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

func Test115PreloadedAbsenceCleansOldUploadPathWithoutLookup(t *testing.T) {
	s, old := new115UploadParentSync(t)
	s.Config.DelEmptyLocalDir = true
	for _, name := range []string{"00-before.strm", "movie.nfo", "another.nfo", "Season 1/episode.nfo"} {
		writeScopeFile(t, filepath.Join(old.LocalFilePath, name))
	}
	calls := 0
	s.SyncDriver = &uploadParent115Driver{
		fakeDirectoryScanDriver: fakeDirectoryScanDriver{dirsByID: map[string][]pathQueueItem{"root": {{PathId: "sibling", Path: "Media/Sibling"}}}},
		lookup: func(context.Context, string) (string, error) {
			calls++
			return "", errors.New("115 returned data=[]")
		},
		detail: func(_ context.Context, id string) (*SyncFileCache, error) {
			if id != "first" {
				calls++
			}
			return &SyncFileCache{Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}}}, nil
		},
	}
	s.sync115 = &Sync115{}
	if err := s.Preload115Dirs("first"); err != nil {
		t.Fatal(err)
	}
	if err := s.apply115Paths(); err != nil {
		t.Fatal(err)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	requireScopeFile(t, old.LocalFilePath, false)
	if calls != 0 || s.NewUpload != 0 {
		t.Fatalf("unexpected confirmation calls=%d uploads=%d", calls, s.NewUpload)
	}
	if status, err := s.scanOutcome(); status != models.SyncStatusCompleted || err != nil {
		t.Fatalf("status=%d error=%v", status, err)
	}
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&models.SyncFile{}, old.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("old directory ledger retained: %v", err)
	}
}

func Test115UploadDirectoryIdentityAndPathFallback(t *testing.T) {
	for _, mode := range []string{"unchanged", "renamed", "moved_outside", "deleted", "replacement", "empty_absent", "empty_present", "lookup_failure_recovered", "id_failure_recovered", "list_failure", "list_path_conflict", "list_auth", "id_auth", "path_auth"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			noID := mode == "empty_absent" || mode == "empty_present" || mode == "lookup_failure_recovered" || mode == "list_failure" || mode == "list_auth" || mode == "path_auth"
			if noID {
				if err := db.Db.Delete(&old).Error; err != nil {
					t.Fatal(err)
				}
			}
			locals := []string{filepath.Join(old.LocalFilePath, "00.strm"), filepath.Join(old.LocalFilePath, "a.nfo"), filepath.Join(old.LocalFilePath, "b.nfo")}
			for _, local := range locals {
				writeScopeFile(t, local)
			}
			lookups, details, lists := 0, 0, 0
			s.SyncDriver = &uploadParent115DetailPathDriver{
				uploadParent115Driver: uploadParent115Driver{
					detail: func(_ context.Context, id string) (*SyncFileCache, error) {
						details++
						if id != old.FileId {
							t.Fatalf("unexpected identity %s", id)
						}
						switch mode {
						case "deleted", "replacement", "list_path_conflict":
							return nil, v115open.NewOpenAPIError(430004, "deleted")
						case "id_failure_recovered":
							return nil, errors.New("detail unavailable")
						case "id_auth":
							return nil, v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "expired")
						}
						f := uploadParent115Detail(id)
						if mode == "renamed" {
							f.FileName = "Renamed"
						}
						if mode == "moved_outside" {
							f.Paths = []v115open.FileDetailPath{{FileId: "outside", Name: "Anime Movies"}}
						}
						return f, nil
					},
					dirs: func(_ context.Context, id string) ([]pathQueueItem, error) {
						lists++
						if id != "root" {
							t.Fatalf("unexpected list %s", id)
						}
						switch mode {
						case "renamed":
							return []pathQueueItem{{PathId: old.FileId, Path: "Media/Renamed"}}, nil
						case "replacement", "empty_present":
							return []pathQueueItem{{PathId: "new-dir", Path: "Media/Empty"}}, nil
						case "list_path_conflict":
							return []pathQueueItem{{PathId: "new-dir", Path: "Media/Empty"}, {PathId: "other-dir", Path: "Media/Empty"}}, nil
						case "list_failure":
							return nil, errors.New("directory page unavailable")
						case "list_auth":
							return nil, v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "expired")
						default:
							return nil, nil
						}
					},
				},
				byPath: func(_ context.Context, remote string) (*SyncFileCache, error) {
					lookups++
					if remote != "Media/Empty" {
						t.Fatalf("unexpected path %s", remote)
					}
					if mode == "lookup_failure_recovered" {
						return nil, errors.New("path unavailable")
					}
					if mode == "path_auth" {
						return nil, v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "expired")
					}
					return nil, fmt.Errorf("path detail: %w", v115open.ErrEmptyPathDetail)
				},
			}
			err := s.compareLocalFilesWithTempTable()
			fatal := mode == "id_auth" || mode == "path_auth" || mode == "list_auth"
			protected := fatal || mode == "list_failure" || mode == "list_path_conflict"
			exists := mode == "unchanged" || mode == "replacement" || mode == "empty_present"
			if (err != nil) != fatal || fatal && !isFatalSyncError(err) {
				t.Fatalf("error=%v fatal=%v", err, fatal)
			}
			requireScopeFile(t, locals[0], protected)
			for _, local := range locals[1:] {
				requireScopeFile(t, local, exists || protected)
			}
			wantLookups, wantDetails, wantLists := 0, 1, 1
			if noID {
				wantLookups, wantDetails = 1, 0
			}
			if mode == "unchanged" || mode == "id_auth" || mode == "path_auth" {
				wantLists = 0
			}
			if lookups != wantLookups || details != wantDetails || lists != wantLists {
				t.Fatalf("lookups/details/lists=%d/%d/%d want=%d/%d/%d", lookups, details, lists, wantLookups, wantDetails, wantLists)
			}
			wantUploads := int64(0)
			if exists {
				wantUploads = 2
			}
			if s.NewUpload != wantUploads {
				t.Fatalf("uploads=%d want=%d", s.NewUpload, wantUploads)
			}
			var uploads []models.DbUploadTask
			if err := db.Db.Find(&uploads).Error; err != nil {
				t.Fatal(err)
			}
			for _, upload := range uploads {
				wantID := "new-dir"
				if mode == "unchanged" {
					wantID = old.FileId
				}
				if upload.RemotePathId != wantID {
					t.Fatalf("wrong upload parent: %+v", upload)
				}
			}
			status, _ := s.scanOutcome()
			if (status == models.SyncStatusIncomplete) != protected {
				t.Fatalf("status=%d protected=%v result=%+v", status, protected, s.scanResultSnapshot())
			}
			if !fatal {
				if err := s.handleTempTableDiff(); err != nil {
					t.Fatal(err)
				}
				var count int64
				if err := db.Db.Model(&models.SyncFile{}).Where("id = ?", old.ID).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if !noID && (count > 0) != (protected || mode == "unchanged") {
					t.Fatalf("old ledger count=%d mode=%s", count, mode)
				}
			}
		})
	}
}

func Test115UploadDirectoryTVCompletenessScope(t *testing.T) {
	for _, mode := range []string{"show_absent", "season_absent", "show_only", "season_present"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := new115UploadParentSync(t)
			s.sync115 = &Sync115{}
			if err := s.publish115DirectoryList(s.Context, "Media", "root", []pathQueueItem{{PathId: "tv", Path: "Media/TV Shows"}}); err != nil {
				t.Fatal(err)
			}
			var shows []pathQueueItem
			if mode != "show_absent" {
				shows = []pathQueueItem{{PathId: "show", Path: "Media/TV Shows/Show"}}
			}
			if err := s.publish115DirectoryList(s.Context, "Media/TV Shows", "tv", shows); err != nil {
				t.Fatal(err)
			}
			if mode == "season_absent" || mode == "season_present" {
				var seasons []pathQueueItem
				if mode == "season_present" {
					seasons = []pathQueueItem{{PathId: "season", Path: "Media/TV Shows/Show/Season 1"}}
				}
				if err := s.publish115DirectoryList(s.Context, "Media/TV Shows/Show", "show", seasons); err != nil {
					t.Fatal(err)
				}
			}
			local := filepath.Join(s.TargetPath, "Media/TV Shows/Show/Season 1/episode.nfo")
			writeScopeFile(t, local)
			lookups, lists := 0, 0
			s.SyncDriver = &uploadParent115DetailPathDriver{
				uploadParent115Driver: uploadParent115Driver{dirs: func(_ context.Context, id string) ([]pathQueueItem, error) {
					lists++
					if id != "show" {
						t.Fatalf("scanned unrelated branch %s", id)
					}
					return nil, errors.New("season list unavailable")
				}},
				byPath: func(context.Context, string) (*SyncFileCache, error) {
					lookups++
					return nil, v115open.ErrEmptyPathDetail
				},
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			requireScopeFile(t, local, mode == "show_only" || mode == "season_present")
			wantCalls := 0
			if mode == "show_only" {
				wantCalls = 1
			}
			if lookups != wantCalls || lists != wantCalls {
				t.Fatalf("lookups=%d lists=%d want=%d", lookups, lists, wantCalls)
			}
			if mode == "season_present" {
				if s.NewUpload != 1 {
					t.Fatalf("uploads=%d", s.NewUpload)
				}
				_, absent, err := s.known115UploadDirectory("Media/TV Shows/Show/Season 1/extras")
				if absent || err != nil {
					t.Fatalf("season existence proved extras absence: %v %v", absent, err)
				}
			}
		})
	}
}

func Test115AccessoryDirectoryRequiresExistingHost(t *testing.T) {
	for _, mode := range []string{"host_present", "host_absent", "host_unknown", "accessory_absent", "nested", "root_named_extras"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			s.sync115 = &Sync115{}
			host, hostID := "Media/Empty", old.FileId
			if mode == "root_named_extras" {
				s.SourcePath, s.Sync.RemotePath = "extras", "extras"
				host, hostID = "extras", "root"
			} else if mode != "host_unknown" {
				var dirs []pathQueueItem
				if mode != "host_absent" {
					dirs = []pathQueueItem{{PathId: hostID, Path: host}}
				}
				if err := s.publish115DirectoryList(s.Context, "Media", "root", dirs); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "accessory_absent" {
				if err := s.publish115DirectoryList(s.Context, host, hostID, nil); err != nil {
					t.Fatal(err)
				}
			}
			suffix := "extras"
			if mode == "nested" {
				suffix = "extras/subs"
			}
			if mode == "root_named_extras" {
				suffix = "subs"
			}
			localDir := filepath.Join(s.TargetPath, host, suffix)
			strm := filepath.Join(s.TargetPath, host, "00-before.strm")
			writeScopeFile(t, strm)
			for _, name := range []string{"a.nfo", "b.nfo"} {
				writeScopeFile(t, filepath.Join(localDir, name))
			}
			lookups, details, lists, creates := 0, 0, 0, 0
			s.SyncDriver = &uploadParent115DetailPathDriver{
				uploadParent115Driver: uploadParent115Driver{
					detail: func(context.Context, string) (*SyncFileCache, error) {
						details++
						return nil, errors.New("detail unavailable")
					},
					dirs: func(_ context.Context, id string) ([]pathQueueItem, error) {
						lists++
						if mode == "host_unknown" {
							return nil, errors.New("host list unavailable")
						}
						if id != hostID {
							t.Fatalf("unexpected branch list %s", id)
						}
						return nil, nil
					},
					create: func(ctx context.Context, local string) (string, string, error) {
						creates++
						if local != localDir {
							t.Fatalf("wrong create target %s", local)
						}
						parent, parentID := host, hostID
						for _, part := range strings.Split(suffix, "/") {
							remote := parent + "/" + part
							id := "created-" + part
							if err := s.record115CreatedDirectory(ctx, remote, id, parentID); err != nil {
								return "", "", err
							}
							if err := s.cache115UploadDirectory(id); err != nil {
								return "", "", err
							}
							parent, parentID = remote, id
						}
						return parentID, parent, nil
					},
				},
				byPath: func(context.Context, string) (*SyncFileCache, error) {
					lookups++
					return nil, v115open.ErrEmptyPathDetail
				},
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			protected := mode == "host_unknown"
			created := mode != "host_absent" && !protected
			requireScopeFile(t, strm, protected)
			for _, name := range []string{"a.nfo", "b.nfo"} {
				requireScopeFile(t, filepath.Join(localDir, name), mode != "host_absent")
			}
			wantCreates, wantUploads, wantLookups, wantLists := 0, int64(0), 0, 0
			if created {
				wantCreates, wantUploads = 1, 2
			}
			if mode == "host_present" || mode == "nested" || mode == "root_named_extras" {
				wantLookups, wantLists = 1, 1
			}
			wantDetails := 0
			if protected {
				wantDetails, wantLists = 1, 1
			}
			if creates != wantCreates || s.NewUpload != wantUploads || lookups != wantLookups || lists != wantLists || details != wantDetails {
				t.Fatalf("creates/uploads/path/list/id=%d/%d/%d/%d/%d want=%d/%d/%d/%d/%d", creates, s.NewUpload, lookups, lists, details, wantCreates, wantUploads, wantLookups, wantLists, wantDetails)
			}
			if created {
				if err := s.confirm115UploadDirectory(host + "/" + suffix); err != nil {
					t.Fatal(err)
				}
				if lookups != wantLookups || lists != wantLists {
					t.Fatal("created directory queried again")
				}
			}
			if status, _ := s.scanOutcome(); (status == models.SyncStatusIncomplete) != protected {
				t.Fatalf("status=%d", status)
			}
		})
	}
}

func Test115UploadShortCircuitsNeedNoDirectoryRequests(t *testing.T) {
	for _, mode := range []string{"keep", "delete", "metadata_disabled", "excluded", "excluded_attachment_dir", "excluded_attachment_file", "only_strm", "pending_upload"} {
		t.Run(mode, func(t *testing.T) {
			s, old := new115UploadParentSync(t)
			local := filepath.Join(old.LocalFilePath, "movie.nfo")
			attachmentExcluded := strings.HasPrefix(mode, "excluded_attachment_")
			if attachmentExcluded {
				local = filepath.Join(old.LocalFilePath, "extras", "movie.nfo")
			}
			if mode == "only_strm" {
				local = filepath.Join(old.LocalFilePath, "movie.strm")
			}
			writeScopeFile(t, local)
			switch mode {
			case "keep":
				s.Config.NetNotFoundFileAction = models.SyncTreeItemMetaActionKeep
			case "delete":
				s.Config.NetNotFoundFileAction = models.SyncTreeItemMetaActionDelete
			case "metadata_disabled":
				s.Config.EnableDownloadMeta = 0
			case "excluded":
				s.Config.ExcludeNames = []string{"empty"}
			case "excluded_attachment_dir":
				s.Config.ExcludeNames = []string{"extras"}
			case "excluded_attachment_file":
				s.Config.ExcludeNames = []string{"movie.nfo"}
			case "pending_upload":
				file := &models.SyncFile{AccountId: s.Account.ID, SyncPathId: s.SyncPathId, SourceType: models.SourceType115, FileType: v115open.TypeFile, ParentId: old.FileId, Path: "Media/Empty", FileName: "movie.nfo", LocalFilePath: local}
				if err := models.AddUploadTaskFromSyncFile(file); err != nil {
					t.Fatal(err)
				}
			}
			calls, creates := 0, 0
			s.SyncDriver = &uploadParent115Driver{
				lookup: func(context.Context, string) (string, error) {
					calls++
					return "", errors.New("unexpected path lookup")
				},
				detail: func(context.Context, string) (*SyncFileCache, error) {
					calls++
					return nil, errors.New("unexpected detail")
				},
				dirs: func(context.Context, string) ([]pathQueueItem, error) {
					calls++
					return nil, errors.New("unexpected listing")
				},
				create: func(context.Context, string) (string, string, error) {
					creates++
					return "", "", errors.New("original attachment creation failed")
				},
			}
			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			if calls != 0 || s.NewUpload != 0 {
				t.Fatalf("calls=%d uploads=%d", calls, s.NewUpload)
			}
			if creates != 0 {
				t.Fatalf("跳过项不应创建目录，次数=%d", creates)
			}
			requireScopeFile(t, local, mode != "delete" && mode != "only_strm")
		})
	}
}
