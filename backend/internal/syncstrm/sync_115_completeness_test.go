package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func Test115DispatcherRejectsIncompleteRawPagesBeforeCleanup(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	models.SettingsGlobal.FileListPageSize = 100
	original := list115FilesPage
	t.Cleanup(func() { list115FilesPage = original })
	for _, mode := range []string{"complete", "empty", "short", "repeated", "missing_id", "changed_count", "changed_first", "request_error", "nil_response", "auth"} {
		t.Run(mode, func(t *testing.T) {
			s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{VideoExt: []string{".mkv"}, StrmBaseUrl: "http://qms.test"}, false, 0, false, false)
			t.Cleanup(s.Cancel)
			s.PathWorkerMax = 1
			s.sync115 = &Sync115{firstFileID: "file-0"}
			s.sync115.paths = map[string]verified115Path{"root": {path: "Media", parentID: "0", within: true, root: true}}
			var calls atomic.Int64
			list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, _ string, _, _, _ bool, offset, limit int) (*v115open.FileListResp, error) {
				calls.Add(1)
				if mode == "auth" {
					return nil, v115open.NewOpenAPIError(v115open.ACCESS_AUTH_INVALID, "fixture: authorization required")
				}
				files := make115ScanPage(offset, limit, 250)
				count := 250
				if offset == 100 {
					switch mode {
					case "empty":
						files = nil
					case "short":
						files = files[:99]
					case "repeated":
						files = make115ScanPage(0, limit, 250)
					case "missing_id":
						files[0].FileId = ""
					case "changed_count":
						count = 251
					case "request_error":
						return nil, errors.New("page unavailable")
					case "nil_response":
						return nil, nil
					}
				}
				if mode == "changed_first" && offset == 0 {
					files[0].FileId = "replacement"
				}
				return &v115open.FileListResp{RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: files}, Count: count}, nil
			}
			old := filepath.Join(s.TargetPath, "Media", "old.strm")
			if err := os.MkdirAll(filepath.Dir(old), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(old, []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			err := s.Start115FileDispathcer(250)
			if mode == "auth" {
				if !isFatalSyncError(err) || calls.Load() != 1 {
					t.Fatalf("auth error=%v, requests=%d", err, calls.Load())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 3 {
				t.Fatalf("requests=%d, want all 3 pages", calls.Load())
			}
			if got := s.cleanupProtected(old, false); got != (mode != "complete") {
				t.Fatalf("cleanup protected=%v for %s", got, mode)
			}
			if err := s.apply115Paths(); err != nil {
				t.Fatal(err)
			}
			if err := s.process115CollectedFiles(); err != nil {
				t.Fatal(err)
			}
			if mode == "complete" {
				if len(s.sync115.rawFileIDs) != 250 || s.memSyncCache.Count() != 1 || s.NewStrm != 1 {
					t.Fatalf("raw=%d cache=%d generated=%d", len(s.sync115.rawFileIDs), s.memSyncCache.Count(), s.NewStrm)
				}
				if len(s.scanResultSnapshot().Failures) != 0 {
					t.Fatalf("complete pages reported failure: %+v", s.scanResultSnapshot())
				}
			} else {
				if s.NewStrm != 0 {
					t.Fatal("incomplete pages must not select an unproven video owner")
				}
				if err := s.compareLocalFilesWithTempTable(); err != nil {
					t.Fatal(err)
				}
				if content, err := os.ReadFile(old); err != nil || string(content) != "old" {
					t.Fatalf("protected file changed: %q, %v", content, err)
				}
				if result := s.scanResultSnapshot(); result.SucceededFiles != 0 || len(result.Failures) == 0 {
					t.Fatalf("unexpected result: %+v", result)
				}
			}
		})
	}
}

func make115ScanPage(offset, limit, total int) []v115open.File {
	files := make([]v115open.File, 0, min(limit, total-offset))
	for i := offset; i < min(offset+limit, total); i++ {
		name := fmt.Sprintf("file-%d.txt", i)
		if i == 0 {
			name = "movie.mkv"
		}
		files = append(files, v115open.File{FileId: fmt.Sprintf("file-%d", i), Pid: "root", FileName: name, FileCategory: v115open.TypeFile, Aid: "1", PickCode: fmt.Sprintf("pick-%d", i)})
	}
	return files
}

func Test115DriverCountAndDirectoryResponsesMustBeComplete(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	models.SettingsGlobal.FileListPageSize = 100
	original := list115FilesPage
	t.Cleanup(func() { list115FilesPage = original })
	s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	for _, mode := range []string{"zero", "nil", "missing_first", "negative", "zero_with_file", "missing_id"} {
		t.Run("count_"+mode, func(t *testing.T) {
			list115FilesPage = func(context.Context, *v115open.OpenClient, string, bool, bool, bool, int, int) (*v115open.FileListResp, error) {
				resp := &v115open.FileListResp{}
				switch mode {
				case "nil":
					return nil, nil
				case "missing_first":
					resp.Count = 1
				case "negative":
					resp.Count = -1
				case "zero_with_file":
					resp.Data = make115ScanPage(0, 1, 1)
				case "missing_id":
					resp.Count = 1
					resp.Data = []v115open.File{{}}
				}
				return resp, nil
			}
			total, _, err := s.SyncDriver.GetTotalFileCount(s.Context)
			if (err == nil) != (mode == "zero") || total != 0 {
				t.Fatalf("total=%d error=%v", total, err)
			}
		})
	}
	for _, listing := range []string{"files", "dirs"} {
		t.Run(listing+"_later_page_error", func(t *testing.T) {
			list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, _ string, _, _, _ bool, offset, limit int) (*v115open.FileListResp, error) {
				if offset > 0 {
					return nil, errors.New("later page failed")
				}
				files := make115ScanPage(offset, limit, 101)
				for i := range files {
					files[i].FileCategory = v115open.TypeDir
				}
				return &v115open.FileListResp{RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: files}, Count: 101, PathStr: "Media"}, nil
			}
			if listing == "files" {
				files, err := s.SyncDriver.GetNetFileFiles(s.Context, "Media", "root")
				if err == nil || len(files) != 0 {
					t.Fatalf("files=%d error=%v", len(files), err)
				}
			} else {
				dirs, err := s.SyncDriver.GetDirsByPathId(s.Context, "root")
				if err == nil || len(dirs) != 0 {
					t.Fatalf("dirs=%d error=%v", len(dirs), err)
				}
			}
		})
	}
}

type incomplete115PathDriver struct {
	fakeDirectoryScanDriver
	detail func(context.Context, string) (*SyncFileCache, error)
	dirs   func(context.Context, string) ([]pathQueueItem, error)
}

func (d *incomplete115PathDriver) DetailByFileId(ctx context.Context, id string) (*SyncFileCache, error) {
	return d.detail(ctx, id)
}

func (d *incomplete115PathDriver) GetDirsByPathId(ctx context.Context, id string) ([]pathQueueItem, error) {
	if d.dirs != nil {
		return d.dirs(ctx, id)
	}
	return d.fakeDirectoryScanDriver.GetDirsByPathId(ctx, id)
}

func Test115PreloadFailureProtectsOnlyFailedSubtreeAndContinuesSibling(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.sync115 = &Sync115{}
	s.SyncDriver = &incomplete115PathDriver{
		detail: func(context.Context, string) (*SyncFileCache, error) {
			return &SyncFileCache{Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: "a", Name: "A"}, {FileId: "b", Name: "B"}}}, nil
		},
		dirs: func(_ context.Context, id string) ([]pathQueueItem, error) {
			switch id {
			case "root":
				return []pathQueueItem{{PathId: "failed", Path: "Media/Failed"}, {PathId: "safe", Path: "Media/Safe"}}, nil
			case "failed":
				return nil, errors.New("failed subtree")
			case "safe":
				return []pathQueueItem{{PathId: "safe-child", Path: "Media/Safe/Child"}}, nil
			default:
				return nil, fmt.Errorf("unexpected directory %s", id)
			}
		},
	}
	if err := s.Preload115Dirs("first"); err != nil {
		t.Fatal(err)
	}
	if err := s.apply115Paths(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.memSyncCache.GetByFileId("safe-child"); err != nil {
		t.Fatal("successful sibling was cancelled", err)
	}
	if !s.cleanupProtected(filepath.Join(s.TargetPath, "Media", "Failed", "old.strm"), false) {
		t.Fatal("failed subtree is not protected")
	}
	if s.cleanupProtected(filepath.Join(s.TargetPath, "Media", "Safe", "old.strm"), false) {
		t.Fatal("independent sibling was unnecessarily protected")
	}
	if !s.cleanupProtected(filepath.Join(s.TargetPath, "Media"), true) {
		t.Fatal("ancestor directory can delete a protected descendant")
	}
}

func Test115UnknownPathProtectsConflictingOwnerAndContinuesIndependentFile(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.sync115 = &Sync115{}
	s.SyncDriver = &incomplete115PathDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.test/movie"}, detail: func(_ context.Context, id string) (*SyncFileCache, error) {
		if id == "unknown" {
			return nil, errors.New("directory unavailable")
		}
		return &SyncFileCache{FileId: id, FileName: "Good", FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}}}, nil
	}}
	for _, file := range []*SyncFileCache{
		{FileId: "unknown-video", ParentId: "unknown", FileName: "conflict.mp4", FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true},
		{FileId: "known-video", ParentId: "known", FileName: "conflict.mkv", FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true},
		{FileId: "independent", ParentId: "known", FileName: "independent.mkv", FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true},
	} {
		if err := s.memSyncCache.Insert(file); err != nil {
			t.Fatal(err)
		}
	}
	conflict := filepath.Join(s.TargetPath, "Media", "Good", "conflict.strm")
	if err := os.MkdirAll(filepath.Dir(conflict), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conflict, []byte("old-owner"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := s.Start115PathDispathcer(); err != nil {
		t.Fatal(err)
	}
	if err := s.process115CollectedFiles(); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(conflict); err != nil || string(content) != "old-owner" {
		t.Fatalf("unresolved owner overwritten: %q %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(conflict), "independent.strm")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.TargetPath, "conflict.strm")); !os.IsNotExist(err) {
		t.Fatalf("unknown path generated at root: %v", err)
	}
	if result := s.scanResultSnapshot(); result.SucceededFiles != 1 || result.SkippedFiles != 0 || len(result.Failures) == 0 {
		t.Fatalf("result=%+v", result)
	}
	if !s.cleanupProtected(filepath.Join(s.TargetPath, "Media", "other.strm"), false) {
		t.Fatal("unknown directory must protect the entire root")
	}
}

func Test115FileWriteFailureDoesNotCancelIndependentFiles(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.PathWorkerMax = 1
	s.SyncDriver = &fakeDirectoryScanDriver{strmContent: "http://qms.test/movie"}
	for _, name := range []string{"bad", "good"} {
		file := &SyncFileCache{FileId: name, ParentId: "root", Path: "Media", FileName: name + ".mkv", FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true}
		file.GetLocalFilePath(s.TargetPath, s.SourcePath)
		if err := s.memSyncCache.Insert(file); err != nil {
			t.Fatal(err)
		}
		if name == "bad" {
			if err := os.MkdirAll(file.LocalFilePath, 0755); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.process115CollectedFiles(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.TargetPath, "Media", "good.strm")); err != nil {
		t.Fatal(err)
	}
	if result := s.scanResultSnapshot(); result.FailedFiles != 1 || result.SucceededFiles != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func Test115DispatcherCancellationStopsAndWaitsForRequests(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	models.SettingsGlobal.FileListPageSize = 100
	original := list115FilesPage
	t.Cleanup(func() { list115FilesPage = original })
	s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.PathWorkerMax = 1
	started := make(chan struct{})
	var active atomic.Int64
	list115FilesPage = func(ctx context.Context, _ *v115open.OpenClient, _ string, _, _, _ bool, _, _ int) (*v115open.FileListResp, error) {
		active.Add(1)
		defer active.Add(-1)
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- s.Start115FileDispathcer(250) }()
	<-started
	s.Cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if active.Load() != 0 {
		t.Fatal("dispatcher returned before request exit")
	}
}

func Test115PathRejectsUntrustworthyIdentity(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	for _, mode := range []string{"nil", "identity", "file_type", "missing_type", "outside_root", "masked", "empty_name", "repeated_ancestor"} {
		t.Run(mode, func(t *testing.T) {
			s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
			t.Cleanup(s.Cancel)
			s.sync115 = &Sync115{}
			detail := &SyncFileCache{FileId: "parent", FileName: "Child", FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}}}
			switch mode {
			case "nil":
				detail = nil
			case "identity":
				detail.FileId = "other"
			case "file_type":
				detail.FileType = v115open.TypeFile
			case "missing_type":
				detail.FileType = ""
			case "outside_root":
				detail.Paths[0].FileId = "other"
			case "masked":
				detail.FileName = "**"
			case "empty_name":
				detail.FileName = ""
			case "repeated_ancestor":
				detail.Paths = append(detail.Paths, detail.Paths[0])
			}
			s.SyncDriver = &incomplete115PathDriver{detail: func(context.Context, string) (*SyncFileCache, error) { return detail, nil }}
			file := &SyncFileCache{FileId: "video", ParentId: "parent", FileName: "movie.mkv", FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true}
			if err := s.memSyncCache.Insert(file); err != nil {
				t.Fatal(err)
			}
			if err := s.Start115PathDispathcer(); err != nil {
				t.Fatal(err)
			}
			if file.Path != "" || len(s.scanResultSnapshot().Failures) == 0 {
				t.Fatalf("untrusted path accepted: %+v", file)
			}
		})
	}
}

func TestOpen115DriverMissingCredentialsIsFatal(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	driver := NewOpen115Driver(v115open.NewClient(9901, "fixture", "", ""))
	driver.SetSyncStrm(s)
	_, _, err := driver.GetTotalFileCount(s.Context)
	if !isFatalSyncError(err) {
		t.Fatalf("missing credentials must stop scanning: %v", err)
	}
	if isFatalSyncError(v115open.NewOpenAPIError(430004, "directory unavailable")) {
		t.Fatal("one missing directory must not cancel siblings")
	}
}
