package syncstrm

import (
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
				if count := syncer.GetExistsPath(); count != 5 {
					t.Fatalf("加载目录数 = %d，期望 5", count)
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
				_, exists := syncer.sync115.existsPathes.Load(directory.id)
				_, excluded := syncer.sync115.excludePathId.Load(directory.id)
				cached, _ := syncer.memSyncCache.GetByFileId(directory.id)
				if exists == directory.excluded || excluded != directory.excluded || (cached == nil) != directory.excluded {
					t.Errorf("目录 %s：已加载=%v，已排除=%v，缓存=%+v，期望排除=%v",
						directory.id, exists, excluded, cached, directory.excluded)
				}
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
					FileId: "season", FileName: "Season 1",
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
