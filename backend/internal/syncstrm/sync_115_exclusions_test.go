package syncstrm

import (
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func Test115CachedAndPreloadedDirectoriesHonorRegexOnlyExclusions(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	directories := []struct {
		id       string
		name     string
		path     string
		excluded bool
	}{
		{id: "series", name: "Series", path: "Media"},
		{id: "plain-name", name: "EXTRAS", path: "Media"},
		{id: "plain-ancestor", name: "Season 1", path: "Media/Extras"},
		{id: "regex", name: ".hidden", path: "Media", excluded: true},
		{id: "regex-ancestor", name: "Season 2", path: "Media/.hidden", excluded: true},
		{id: "partial", name: "MyExtras", path: "Media"},
		{id: "case-sensitive", name: "Sample", path: "Media"},
		{id: "playback", name: "多端播放", path: "/", excluded: true},
		{id: "playback-child", name: "child", path: "多端播放", excluded: true},
		{id: "playback-nested-name", name: "多端播放", path: "Media"},
	}
	var preloaded []pathQueueItem
	for _, directory := range directories {
		row := &models.SyncFile{
			SyncPathId: syncPath.ID,
			AccountId:  account.ID,
			FileId:     directory.id,
			FileType:   v115open.TypeDir,
			FileName:   directory.name,
			Path:       directory.path,
			SourceType: models.SourceType115,
		}
		if err := db.Db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
		preloaded = append(preloaded, pathQueueItem{
			PathId: directory.id,
			Path:   filepath.ToSlash(filepath.Join(directory.path, directory.name)),
		})
	}
	for _, mode := range []string{"读取已有目录", "预取目录"} {
		t.Run(mode, func(t *testing.T) {
			syncer := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{
				ExcludeNameRegexes: []string{"^\\.hidden$", "^sample$"},
			}, false, 0, false, false)
			if syncer == nil {
				t.Fatal("创建同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.sync115 = &Sync115{}
			if mode == "读取已有目录" {
				if count, err := syncer.GetExistsPath(); err != nil || count != int64(len(directories)-2) {
					t.Fatalf("加载目录数 = %d，错误 = %v，期望同步根内的目录计数", count, err)
				}
			} else {
				syncer.SyncDriver = &fakeDirectoryScanDriver{
					dirsByID: map[string][]pathQueueItem{"root": preloaded},
					detailsByID: map[string]*SyncFileCache{
						"first": {Paths: []v115open.FileDetailPath{
							{FileId: "root", Name: "Media"},
							{FileId: "series", Name: "Series"},
						}},
					},
				}
				if err := syncer.Preload115Dirs("first"); err != nil {
					t.Fatal(err)
				}
			}
			for _, directory := range directories {
				fact, exists := syncer.sync115.paths[directory.id]
				cached, _ := syncer.memSyncCache.GetByFileId(directory.id)
				if cached != nil {
					t.Fatalf("路径确认完成前不应插入目录：%+v", cached)
				}
				if mode == "读取已有目录" {
					if exists {
						t.Fatal("旧账本被当成本轮事实")
					}
				} else {
					// 只有真实直属子目录才能从当前预取列表确认。
					want := directory.path == "Media"
					if exists != want || exists && syncer.IsExcludePath(fact.path) != directory.excluded {
						t.Errorf("目录 %s：已确认=%v，事实=%+v", directory.id, exists, fact)
					}
				}
			}
		})
	}
}

func TestPlaybackDirectoryIsExcludedFromLocalSyncComparison(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	for _, source := range []models.SourceType{models.SourceType115, models.SourceTypeBaiduPan} {
		t.Run(string(source), func(t *testing.T) {
			account.SourceType = source
			target := t.TempDir()
			paths := []string{
				"多端播放/child/movie.strm", "多端播放/child/movie.nfo",
				"Media/多端播放/movie.strm", "Media/多端播放/movie.nfo",
			}
			for _, name := range paths {
				path := filepath.Join(target, name)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			syncer := newSyncStrm(account, syncPath.ID, "/", "0", target, SyncStrmConfig{
				MetaExt: []string{".nfo"}, EnableDownloadMeta: 1,
				NetNotFoundFileAction: models.SyncTreeItemMetaActionDelete,
			}, false, 0, false, false)
			if syncer == nil {
				t.Fatal("创建同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			if err := syncer.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			for i, name := range paths {
				data, err := os.ReadFile(filepath.Join(target, name))
				if source == models.SourceType115 && i < 2 {
					if err != nil || string(data) != "original" {
						t.Errorf("临时目录本地镜像不应被处理：%s，err=%v", name, err)
					}
				} else if !os.IsNotExist(err) {
					t.Errorf("普通路径应沿用远端缺失时的删除行为：%s，err=%v", name, err)
				}
			}
		})
	}
}

func Test115PathCompletionExcludesPlaybackDirectory(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	for _, tt := range []struct {
		name      string
		parents   []v115open.FileDetailPath
		directory string
		wantPath  string
	}{
		{name: "临时目录", directory: "多端播放"},
		{name: "临时目录子树", parents: []v115open.FileDetailPath{{FileId: "playback", Name: "多端播放"}}, directory: "child"},
		{name: "同名非根目录", parents: []v115open.FileDetailPath{{FileId: "media", Name: "Media"}}, directory: "多端播放", wantPath: "Media/多端播放"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			syncer := newSyncStrm(account, syncPath.ID, "/", "0", t.TempDir(), SyncStrmConfig{}, false, 0, false, false)
			if syncer == nil {
				t.Fatal("创建同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.sync115 = &Sync115{}
			if err := syncer.memSyncCache.Insert(&SyncFileCache{
				FileId: "movie", ParentId: "directory", FileName: "movie.mkv",
				FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true,
			}); err != nil {
				t.Fatal(err)
			}
			syncer.SyncDriver = &fakeDirectoryScanDriver{detailsByID: map[string]*SyncFileCache{
				"directory": {
					FileId: "directory", FileName: tt.directory, FileType: v115open.TypeDir,
					Paths: append([]v115open.FileDetailPath{{FileId: "0"}}, tt.parents...),
				},
			}}
			if err := syncer.Start115PathDispathcer(); err != nil {
				t.Fatal(err)
			}
			file, _ := syncer.memSyncCache.GetByFileId("movie")
			if tt.wantPath == "" {
				if file != nil || syncer.memSyncCache.Count() != 0 {
					t.Fatalf("临时目录的文件或目录仍留在同步缓存：%+v", file)
				}
			} else if file == nil || file.Path != tt.wantPath {
				t.Fatalf("普通目录文件未完成路径补齐：%+v，期望 %s", file, tt.wantPath)
			}
		})
	}
}

func Test115PathCompletionHonorsAncestorsAboveSelectedRoot(t *testing.T) {
	account, syncPath := setupStrmExclusionTestDB(t)
	for _, tt := range []struct {
		ancestor string
		exact    []string
		excluded bool
	}{
		{ancestor: "EXTRAS", exact: []string{"extras"}, excluded: true},
		{ancestor: ".hidden", excluded: true},
		{ancestor: "MyExtras"},
	} {
		t.Run(tt.ancestor, func(t *testing.T) {
			source := filepath.ToSlash(filepath.Join("Library", tt.ancestor, "Media"))
			syncer := newSyncStrm(account, syncPath.ID, source, "selected", t.TempDir(), SyncStrmConfig{
				ExcludeNames:       tt.exact,
				ExcludeNameRegexes: []string{"^\\.hidden$"},
			}, false, 0, false, false)
			if syncer == nil {
				t.Fatal("创建同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.sync115 = &Sync115{}
			syncer.memSyncCache.Insert(&SyncFileCache{
				FileId: "movie", ParentId: "season", FileName: "Movie.mkv",
				FileType: v115open.TypeFile, SourceType: models.SourceType115, IsVideo: true,
			})
			syncer.SyncDriver = &fakeDirectoryScanDriver{detailsByID: map[string]*SyncFileCache{
				"season": {
					FileId: "season", FileName: "Season 1", FileType: v115open.TypeDir,
					Paths: []v115open.FileDetailPath{
						{FileId: "library", Name: "Library"},
						{FileId: "ancestor", Name: tt.ancestor},
						{FileId: "selected", Name: "Media"},
					},
				},
			}}
			if err := syncer.Start115PathDispathcer(); err != nil {
				t.Fatal(err)
			}
			file, _ := syncer.memSyncCache.GetByFileId("movie")
			if tt.excluded {
				if file != nil {
					t.Fatalf("祖先目录被排除后仍保留文件：%+v", file)
				}
			} else if file == nil || file.Path != source+"/Season 1" {
				t.Fatalf("合法目录未完成路径补全：%+v", file)
			}
		})
	}
}
