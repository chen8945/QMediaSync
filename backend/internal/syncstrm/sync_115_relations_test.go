package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func Test115VerifiedRelationsReuseAndUnknown(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, path.ID, "Media", "root", t.TempDir(), SyncStrmConfig{ExcludeNames: []string{"extras"}}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.sync115 = &Sync115{}
	var calls atomic.Int64
	s.SyncDriver = &incomplete115PathDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.test/movie"}, detail: func(_ context.Context, id string) (*SyncFileCache, error) {
		calls.Add(1)
		return &SyncFileCache{FileId: id, FileName: id, FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: "excluded", Name: "Extras"}, {FileId: "ancestor", Name: "Season"}}}, nil
	}}
	if err := s.process115Path(s.Context, "leaf"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"excluded", "ancestor", "leaf"} {
		if err := s.process115Path(s.Context, id); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("known chain requests=%d", calls.Load())
	}
	if err := s.process115Path(s.Context, "unknown"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("unknown directory requests=%d", calls.Load())
	}
	for i := range 8 {
		if err := s.memSyncCache.Insert(&SyncFileCache{FileId: fmt.Sprint(i), ParentId: "leaf", FileName: fmt.Sprintf("%d.mkv", i), FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Start115PathDispathcer(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || s.memSyncCache.Count() != 0 {
		t.Fatalf("requests=%d cache=%d", calls.Load(), s.memSyncCache.Count())
	}
	t.Logf("known chain: 4 directory resolutions / 1 request; unknown leaf: 1 extra request; 8 same-parent files: 0 extra requests")
}

func Test115FullSyncOldDirectoryLocationMustBeConfirmed(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	for _, names := range [][2]string{{"Extras", "Allowed"}, {"Allowed", "Extras"}} {
		t.Run(names[0]+"_to_"+names[1], func(t *testing.T) {
			if err := db.Db.Where("sync_path_id = ?", path.ID).Delete(&models.SyncFile{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Create(&models.SyncFile{SyncPathId: path.ID, AccountId: account.ID, FileId: "parent", FileName: names[0], Path: "Media", FileType: v115open.TypeDir}).Error; err != nil {
				t.Fatal(err)
			}
			s := newSyncStrm(account, path.ID, "Media", "root", t.TempDir(), SyncStrmConfig{ExcludeNames: []string{"extras"}, VideoExt: []string{".mkv"}}, true, 0, false, false)
			t.Cleanup(s.Cancel)
			s.sync115 = &Sync115{}
			if n, err := s.GetExistsPath(); err != nil || n != 0 {
				t.Fatalf("count=%d error=%v", n, err)
			}
			var calls atomic.Int64
			originalList := list115FilesPage
			t.Cleanup(func() { list115FilesPage = originalList })
			list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, _ string, _, _, _ bool, _, _ int) (*v115open.FileListResp, error) {
				return &v115open.FileListResp{RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: []v115open.File{{FileId: "file", Pid: "parent", FileName: "movie.mkv", FileCategory: v115open.TypeFile, PickCode: "pick", FileSize: 1024}}}, Count: 1}, nil
			}
			if err := s.Start115FileDispathcer(1); err != nil {
				t.Fatal(err)
			}
			list115FilesPage = originalList
			s.SyncDriver = &incomplete115PathDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.test/movie"}, detail: func(_ context.Context, id string) (*SyncFileCache, error) {
				calls.Add(1)
				return &SyncFileCache{FileId: id, FileName: names[1], FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}}}, nil
			}}
			if err := s.Start115PathDispathcer(); err != nil {
				t.Fatal(err)
			}
			f, _ := s.memSyncCache.GetByFileId("file")
			if calls.Load() != 1 || names[1] == "Allowed" && (f == nil || f.Path != "Media/Allowed") || names[1] == "Extras" && f != nil {
				t.Fatalf("requests=%d file=%+v", calls.Load(), f)
			}
			if err := s.process115CollectedFiles(); err != nil {
				t.Fatal(err)
			}
			if names[1] == "Allowed" {
				if data, err := os.ReadFile(filepath.Join(s.TargetPath, "Media/Allowed/movie.strm")); err != nil || string(data) != "http://qms.test/movie" {
					t.Fatalf("output=%q err=%v", data, err)
				}
			}

		})
	}
}

func Test115InFlightConflictPreservesUnresolvedFiles(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	for _, conflict := range []bool{false, true} {
		t.Run(fmt.Sprint(conflict), func(t *testing.T) {
			s := newSyncStrm(account, path.ID, "Media", "root", t.TempDir(), SyncStrmConfig{ExcludeNames: []string{"extras"}}, false, 0, false, false)
			t.Cleanup(s.Cancel)
			s.sync115 = &Sync115{}
			s.PathWorkerMax = 2
			arrived := make(chan string, 2)
			release := make(chan struct{})
			s.SyncDriver = &incomplete115PathDriver{detail: func(ctx context.Context, id string) (*SyncFileCache, error) {
				arrived <- id
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				name := "Extras"
				if conflict && id == "b" {
					name = "Allowed"
				}
				return &SyncFileCache{FileId: id, FileName: id, FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: "shared", Name: name}}}, nil
			}}
			for _, id := range []string{"a", "b"} {
				if err := s.memSyncCache.Insert(&SyncFileCache{FileId: "file-" + id, ParentId: id, FileName: id + ".mkv", FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true}); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() { done <- s.Start115PathDispathcer() }()
			<-arrived
			<-arrived
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if conflict {
				for _, id := range []string{"a", "b"} {
					f, _ := s.memSyncCache.GetByFileId("file-" + id)
					if f == nil || f.Path != "" {
						t.Fatalf("conflicting file lost or resolved: %+v", f)
					}
				}
				if !s.sync115.pathConflict || len(s.scanResultSnapshot().Failures) == 0 {
					t.Fatal("conflict not protected")
				}
				if !s.cleanupProtected(filepath.Join(s.TargetPath, "Media/old.strm"), false) {
					t.Fatal("conflict did not protect old file")
				}
				if err := s.process115CollectedFiles(); err != nil {
					t.Fatal(err)
				}
				if s.NewStrm != 0 {
					t.Fatal("conflicting paths generated output")
				}

			} else if s.memSyncCache.Count() != 0 {
				t.Fatal("equivalent excluded responses did not exclude files")
			}
		})
	}
}

func Test115FailedAndCanceledDetailsDoNotTeachRelations(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, path.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.sync115 = &Sync115{}
	for _, mode := range []string{"failure", "cancel", "invalid", "success"} {
		ctx, cancel := context.WithCancel(s.Context)
		s.SyncDriver = &incomplete115PathDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.test/movie"}, detail: func(_ context.Context, id string) (*SyncFileCache, error) {
			if mode == "failure" {
				return nil, errors.New("unavailable")
			}
			if mode == "cancel" {
				cancel()
			}
			name := "Child"
			if mode == "invalid" {
				name = ".."
			}
			return &SyncFileCache{FileId: id, FileName: name, FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}}}, nil
		}}
		err := s.process115Path(ctx, "child")
		cancel()
		if mode == "success" {
			if err != nil || len(s.sync115.paths) != 2 {
				t.Fatalf("recovery: %v", err)
			}
		} else if err == nil || len(s.sync115.paths) != 0 {
			t.Fatalf("%s learned failed detail: %v", mode, err)
		}
	}
}

func Test115PreloadedRootConflictAndRuleChange(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	for _, excluded := range []bool{true, false} {
		config := SyncStrmConfig{}
		if excluded {
			config.ExcludeNames = []string{"extras"}
		}
		s := newSyncStrm(account, path.ID, "Media", "root", t.TempDir(), config, false, 0, false, false)
		t.Cleanup(s.Cancel)
		s.sync115 = &Sync115{}
		s.SyncDriver = &fakeDirectoryScanDriver{dirsByID: map[string][]pathQueueItem{"root": {{PathId: "a", Path: "Media/Extras"}}}, detailsByID: map[string]*SyncFileCache{"first": {Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: "a", Name: "Extras"}}}}}
		if err := s.Preload115Dirs("first"); err != nil {
			t.Fatal(err)
		}
		if err := s.memSyncCache.Insert(&SyncFileCache{FileId: "file", ParentId: "a", FileName: "movie.mkv", FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true}); err != nil {
			t.Fatal(err)
		}
		if err := s.Start115PathDispathcer(); err != nil {
			t.Fatal(err)
		}
		f, _ := s.memSyncCache.GetByFileId("file")
		if (f == nil) != excluded {
			t.Fatalf("new round rules ignored: %+v", f)
		}
	}
	s := newSyncStrm(account, path.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.sync115 = &Sync115{}
	s.SyncDriver = &fakeDirectoryScanDriver{dirsByID: map[string][]pathQueueItem{"root": {{PathId: "a", Path: "Media/A"}}}, detailsByID: map[string]*SyncFileCache{"first": {Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: "a", Name: "A"}}}}}
	if err := s.Preload115Dirs("first"); err != nil {
		t.Fatal(err)
	}
	s.SyncDriver = &incomplete115PathDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.test/movie"}, detail: func(_ context.Context, id string) (*SyncFileCache, error) {
		return &SyncFileCache{FileId: id, FileName: "B", FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Renamed"}}}, nil
	}}
	if err := s.process115Path(s.Context, "b"); err == nil || !s.sync115.pathConflict {
		t.Fatal("preloaded root rename not detected")
	}
}

func Test115RelationRequestSamples(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	for _, siblings := range []bool{false, true} {
		for _, reuse := range []bool{false, true} {
			t.Run(fmt.Sprintf("siblings=%v/reuse=%v", siblings, reuse), func(t *testing.T) {
				s := newSyncStrm(account, path.ID, "Media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
				t.Cleanup(s.Cancel)
				s.sync115 = &Sync115{}
				var calls int
				s.SyncDriver = &incomplete115PathDriver{detail: func(_ context.Context, id string) (*SyncFileCache, error) {
					calls++
					var n int
					fmt.Sscanf(id, "dir-%d", &n)
					parents := []v115open.FileDetailPath{{FileId: "root", Name: "Media"}}
					if !siblings {
						for i := range n {
							parents = append(parents, v115open.FileDetailPath{FileId: fmt.Sprintf("dir-%d", i), Name: fmt.Sprintf("D%d", i)})
						}
					}
					return &SyncFileCache{FileId: id, FileName: fmt.Sprintf("D%d", n), FileType: v115open.TypeDir, Paths: parents}, nil
				}}
				start := time.Now()
				for i := 31; i >= 0; i-- {
					// 对照只关闭请求前命中，保留同样的完整链校验。
					if !reuse {
						delete(s.sync115.paths, fmt.Sprintf("dir-%d", i))
					}
					if err := s.process115Path(s.Context, fmt.Sprintf("dir-%d", i)); err != nil {
						t.Fatal(err)
					}
				}
				elapsed := time.Since(start)
				want := 32
				if reuse && !siblings {
					want = 1
				}
				if calls != want {
					t.Fatalf("requests=%d want=%d", calls, want)
				}
				pathBytes := 0
				for _, fact := range s.sync115.paths {
					pathBytes += len(fact.path)
				}
				t.Logf("directories=32 requests=%d elapsed=%s retained_facts=%d retained_path_bytes=%d (local fixture; not network throughput)", calls, elapsed, len(s.sync115.paths), pathBytes)
			})
		}
	}
}

func Test115PreloadRootRelativePaths(t *testing.T) {
	account, path := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, path.ID, "/", "0", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
	t.Cleanup(s.Cancel)
	s.sync115 = &Sync115{}
	s.SyncDriver = &fakeDirectoryScanDriver{dirsByID: map[string][]pathQueueItem{"0": {{PathId: "a", Path: "Media", Mtime: 123}, {PathId: "playback", Path: "多端播放"}}}, detailsByID: map[string]*SyncFileCache{"first": {Paths: []v115open.FileDetailPath{{FileId: "0"}, {FileId: "a", Name: "Media"}}}}}
	if err := s.Preload115Dirs("first"); err != nil {
		t.Fatal(err)
	}
	if err := s.apply115Paths(); err != nil {
		t.Fatal(err)
	}
	item, _ := s.memSyncCache.GetByFileId("a")
	if item == nil || item.MTime != 123 {
		t.Fatalf("root child missing or mtime changed: %+v", item)
	}
	if item, _ := s.memSyncCache.GetByFileId("playback"); item != nil {
		t.Fatal("playback child retained")
	}
	if len(s.scanResultSnapshot().Failures) != 0 {
		t.Fatal("valid root children reported incomplete")
	}
}

func Test115HistoricalDirectoriesReuseAcrossRounds(t *testing.T) {
	account, sp := setupStrmExclusionTestDB(t)
	rows := []models.SyncFile{
		{SyncPathId: sp.ID, AccountId: account.ID, SourceType: models.SourceType115, FileId: "parent", ParentId: "root", FileName: "Extras", Path: "Media", FileType: v115open.TypeDir},
		{SyncPathId: sp.ID, AccountId: account.ID, SourceType: models.SourceType115, FileId: "child", ParentId: "parent", FileName: "Season", Path: "Media/Extras", FileType: v115open.TypeDir},
	}
	if err := db.Db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, excluded := range []bool{false, true, false} {
		s := newSyncStrm(account, sp.ID, "Media", "root", t.TempDir(), SyncStrmConfig{VideoExt: []string{".mkv"}}, false, 0, false, false)
		t.Cleanup(s.Cancel)
		if excluded {
			s.Config.ExcludeNames = []string{"extras"}
		}
		if _, err := s.GetExistsPath(); err != nil {
			t.Fatal(err)
		}
		calls := 0
		s.SyncDriver = &incomplete115PathDriver{detail: func(context.Context, string) (*SyncFileCache, error) {
			calls++
			return nil, errors.New("unexpected request")
		}}
		for i := range 4 {
			if err := s.memSyncCache.Insert(&SyncFileCache{FileId: fmt.Sprint(i), ParentId: "child", FileName: fmt.Sprintf("%d.mkv", i), FileType: v115open.TypeFile, IsVideo: true}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Start115PathDispathcer(); err != nil {
			t.Fatal(err)
		}
		file, _ := s.memSyncCache.GetByFileId("0")
		if calls != 0 || (file == nil) != excluded || file != nil && file.Path != "Media/Extras/Season" {
			t.Fatalf("excluded=%v calls=%d file=%+v", excluded, calls, file)
		}
		if len(s.sync115.paths) != 0 {
			t.Fatal("history became current evidence")
		}
	}
}

func Test115CurrentMoveInvalidatesHistoricalDescendants(t *testing.T) {
	s, _ := new115UploadParentSync(t)
	s.sync115 = &Sync115{historicalPaths: map[string]verified115Path{
		"parent": {path: "Media/Old", parentID: "root", within: true},
		"child":  {path: "Media/Old/Season", parentID: "parent", within: true},
	}}
	if err := s.publish115Paths(s.Context, map[string]verified115Path{"parent": {path: "Media/New", parentID: "root", within: true}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.SyncDriver = &incomplete115PathDriver{detail: func(_ context.Context, id string) (*SyncFileCache, error) {
		calls++
		return &SyncFileCache{FileId: id, FileName: "Season", FileType: v115open.TypeDir, Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: "parent", Name: "New"}}}, nil
	}}
	if err := s.memSyncCache.Insert(&SyncFileCache{FileId: "video", ParentId: "child", FileName: "movie.mkv", FileType: v115open.TypeFile, IsVideo: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start115PathDispathcer(); err != nil {
		t.Fatal(err)
	}
	file, _ := s.memSyncCache.GetByFileId("video")
	if calls != 1 || file == nil || file.Path != "Media/New/Season" || s.sync115.pathConflict {
		t.Fatalf("calls=%d file=%+v conflict=%v", calls, file, s.sync115.pathConflict)
	}
}

func Test115HistoricalAncestorsSurviveLedgerRound(t *testing.T) {
	s, old := new115UploadParentSync(t)
	parent := old
	parent.ID = 0
	parent.FileId = "ancestor"
	parent.FileName = "Parent"
	parent.ParentId = "root"
	parent.Path = "Media"
	child := old
	child.ID = 0
	child.FileId = "child"
	child.FileName = "Child"
	child.ParentId = parent.FileId
	child.Path = "Media/Parent"
	if err := db.Db.Create(&[]models.SyncFile{parent, child}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetExistsPath(); err != nil {
		t.Fatal(err)
	}
	if err := s.memSyncCache.Insert(&SyncFileCache{FileId: "video", ParentId: child.FileId, FileName: "movie.mkv", FileType: v115open.TypeFile, IsVideo: true, SourceType: models.SourceType115}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start115PathDispathcer(); err != nil {
		t.Fatal(err)
	}
	if item, _ := s.memSyncCache.GetByFileId(parent.FileId); item == nil {
		t.Fatal("referenced ancestor not retained")
	}
	if item, _ := s.memSyncCache.GetByFileId(old.FileId); item != nil {
		t.Fatal("unrelated empty directory trusted")
	}
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	s.sync115 = &Sync115{}
	if _, err := s.GetExistsPath(); err != nil {
		t.Fatal(err)
	}
	if err := s.publish115Paths(s.Context, map[string]verified115Path{parent.FileId: {path: "Media/Moved", parentID: "root", within: true}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.sync115.historicalPaths[child.FileId]; ok {
		t.Fatal("persisted ancestor move did not invalidate child")
	}
}

func Test115InvalidHistoricalParentInvalidatesDescendants(t *testing.T) {
	s, old := new115UploadParentSync(t)
	rows := []models.SyncFile{
		{SyncPathId: s.SyncPathId, AccountId: s.Account.ID, SourceType: models.SourceType115, FileId: "bad", FileName: "..", Path: "Media", FileType: v115open.TypeDir},
		{SyncPathId: s.SyncPathId, AccountId: s.Account.ID, SourceType: models.SourceType115, FileId: "child", ParentId: "bad", FileName: "Child", Path: "Media/Bad", FileType: v115open.TypeDir},
		{SyncPathId: s.SyncPathId, AccountId: s.Account.ID, SourceType: models.SourceType115, FileId: "leaf", ParentId: "child", FileName: "Leaf", Path: "Media/Bad/Child", FileType: v115open.TypeDir},
	}
	if err := db.Db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := s.GetExistsPath(); err != nil || n != 1 {
		t.Fatalf("count=%d err=%v", n, err)
	}
	if _, ok := s.sync115.historicalPaths[old.FileId]; !ok {
		t.Fatal("unrelated valid history lost")
	}
}
