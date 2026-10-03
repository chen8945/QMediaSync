package models

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"qmediasync/internal/db"
)

func createEmbyRefreshSyncFile(t *testing.T, syncPathID uint, path, fileName, pickCode string) *SyncFile {
	t.Helper()
	syncFile := &SyncFile{
		SyncPathId:    syncPathID,
		AccountId:     1,
		SourceType:    SourceType115,
		FileId:        "file-" + pickCode,
		ParentId:      "parent-" + pickCode,
		PickCode:      pickCode,
		Path:          path,
		FileName:      fileName,
		LocalFilePath: "/strm" + path + "/" + fileName[:len(fileName)-4] + ".strm",
		IsVideo:       true,
	}
	if err := db.Db.Create(syncFile).Error; err != nil {
		t.Fatalf("创建 SyncFile 失败: %v", err)
	}
	return syncFile
}

func createEmbyRefreshItem(t *testing.T, itemID string, itemType string, seasonID string, seriesID string) {
	t.Helper()
	if err := db.Db.Create(&EmbyMediaItem{
		ItemId:     itemID,
		ItemIdInt:  helpersStringToInt64ForTest(itemID),
		Name:       "item-" + itemID,
		Type:       itemType,
		SeasonId:   seasonID,
		SeriesId:   seriesID,
		LibraryId:  "lib-tv",
		Path:       "/strm/remote/item-" + itemID + ".strm",
		PickCode:   "pick-" + itemID,
		LastSeenAt: nowUnix(),
	}).Error; err != nil {
		t.Fatalf("创建 EmbyMediaItem 失败: %v", err)
	}
}

func helpersStringToInt64ForTest(value string) int64 {
	var id int64
	for _, r := range value {
		if r < '0' || r > '9' {
			continue
		}
		id = id*10 + int64(r-'0')
	}
	return id
}

func TestResolveEmbyRefreshTargetUsesExistingMovieItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/movie", "movie.mkv", "movie")
	createEmbyRefreshItem(t, "101", "Movie", "", "")
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 101, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.TargetType != EmbyRefreshTargetTypeItem || target.ItemID != "101" || target.Recursive {
		t.Fatalf("刷新目标 = %+v，期望 Movie item 101 非递归刷新", target)
	}
}

func TestResolveEmbyRefreshTargetUsesExistingEpisodeItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode.mkv", "episode")
	createEmbyRefreshItem(t, "201", "Episode", "301", "401")
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 201, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.TargetType != EmbyRefreshTargetTypeItem || target.ItemID != "201" || target.Recursive {
		t.Fatalf("刷新目标 = %+v，期望 Episode item 201 非递归刷新", target)
	}
}

func TestResolveEmbyRefreshTargetFillsLibraryNameFromGlobalSyncPathRelation(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-current", LibraryName: "当前目录媒体库", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 20})
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/show", "episode.mkv", "global-library-name")
	createEmbyRefreshItem(t, "202", "Episode", "", "")
	if err := db.Db.Model(&EmbyMediaItem{}).Where("item_id = ?", "202").Update("library_id", "lib-tv").Error; err != nil {
		t.Fatalf("更新 item 媒体库失败: %v", err)
	}
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 202, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.FallbackLibraryId != "lib-tv" || target.FallbackLibraryName != "剧集" {
		t.Fatalf("fallback library = %s/%s，期望 lib-tv/剧集", target.FallbackLibraryId, target.FallbackLibraryName)
	}
}

func TestResolveEmbyTargetLibraryRemoteCachesAndCoalescesSameItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	var ancestorsRequests atomic.Int32
	var virtualFoldersRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items/cache-item/Ancestors":
			ancestorsRequests.Add(1)
			fmt.Fprint(w, `[{"Id":"tv-folder","Path":"/media/tv"}]`)
		case "/emby/Library/VirtualFolders":
			virtualFoldersRequests.Add(1)
			fmt.Fprint(w, `[{"Id":"lib-tv","Name":"剧集","Locations":["/media/tv"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	const callers = 10
	start := make(chan struct{})
	results := make(chan EmbyLibraryResolution, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			<-start
			results <- resolveEmbyTargetLibraryRemote(EmbyRefreshTarget{ItemID: "cache-item"})
		})
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if !result.Resolved || result.LibraryID != "lib-tv" || result.LibraryName != "剧集" {
			t.Fatalf("远端解析结果 = %+v，期望 lib-tv/剧集", result)
		}
	}
	if ancestorsRequests.Load() != 1 || virtualFoldersRequests.Load() != 1 {
		t.Fatalf("同 item 并发解析请求次数 ancestors=%d virtual_folders=%d，期望均为 1", ancestorsRequests.Load(), virtualFoldersRequests.Load())
	}

	resolution := resolveEmbyTargetLibraryRemote(EmbyRefreshTarget{ItemID: "cache-item"})
	if !resolution.Resolved || ancestorsRequests.Load() != 1 || virtualFoldersRequests.Load() != 1 {
		t.Fatalf("TTL 缓存未命中，结果=%+v ancestors=%d virtual_folders=%d", resolution, ancestorsRequests.Load(), virtualFoldersRequests.Load())
	}
}

func TestResolveEmbyRefreshTargetUsesSeasonThenSeriesForNewEpisode(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	existing := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode01.mkv", "ep1")
	createEmbyRefreshItem(t, "201", "Episode", "301", "401")
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 201, SyncFileId: existing.ID, PickCode: existing.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}
	newEpisode := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode02.mkv", "ep2")

	target, err := ResolveEmbyRefreshTarget(newEpisode)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.ItemID != "301" || target.ItemType != "Season" || !target.Recursive {
		t.Fatalf("刷新目标 = %+v，期望优先刷新 Season 301", target)
	}

	if err := db.Db.Model(&EmbyMediaItem{}).Where("item_id = ?", "201").Update("season_id", "").Error; err != nil {
		t.Fatalf("清空 season_id 失败: %v", err)
	}
	target, err = ResolveEmbyRefreshTarget(newEpisode)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.ItemID != "401" || target.ItemType != "Series" || !target.Recursive {
		t.Fatalf("刷新目标 = %+v，期望回退刷新 Series 401", target)
	}
}

func TestResolveEmbyRefreshTargetSiblingPathIndexKeepsCandidateBounds(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	if err := EnsureSyncFileLookupIndexes(db.Db); err != nil {
		t.Fatal(err)
	}
	const directory = "/remote/show/Season 01"
	siblings := make([]*SyncFile, 52)
	for i := range siblings {
		siblings[i] = createEmbyRefreshSyncFile(t, 10, directory, fmt.Sprintf("episode%d.mkv", i), fmt.Sprintf("ep%d", i))
	}
	self := createEmbyRefreshSyncFile(t, 10, directory, "self.mkv", "self")
	otherSyncPath := createEmbyRefreshSyncFile(t, 20, directory, "other.mkv", "other")
	otherDirectory := createEmbyRefreshSyncFile(t, 10, "/remote/other", "other.mkv", "other-dir")
	bindEpisode := func(file *SyncFile, itemID int64) {
		t.Helper()
		createEmbyRefreshItem(t, fmt.Sprint(itemID), "Episode", fmt.Sprint(itemID+100), "series")
		if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: file.SyncPathId, SyncFileId: file.ID, EmbyItemId: uint(itemID), PickCode: file.PickCode}).Error; err != nil {
			t.Fatal(err)
		}
	}
	bindEpisode(self, 1000)
	bindEpisode(otherSyncPath, 2000)
	bindEpisode(otherDirectory, 3000)
	bindEpisode(siblings[1], 4000) // 最新 50 条之外。
	if target, found, err := resolveSiblingSeasonOrSeries(self); err != nil || found {
		t.Fatalf("不能使用自己、其他目录或第 51 条记录：%+v %v", target, err)
	}
	bindEpisode(siblings[2], 5000)
	if target, found, err := resolveSiblingSeasonOrSeries(self); err != nil || !found || target.ItemID != "5100" {
		t.Fatalf("应包含第 50 条记录：%+v %v", target, err)
	}
	bindEpisode(siblings[51], 6000)
	if target, found, err := resolveSiblingSeasonOrSeries(self); err != nil || !found || target.ItemID != "6100" {
		t.Fatalf("应优先使用最新记录：%+v %v", target, err)
	}
	if target, err := ResolveEmbyRefreshTarget(self); err != nil || target.ItemID != "1000" {
		t.Fatalf("自身 item 仍应优先于同目录 Season：%+v %v", target, err)
	}
	if target, found, err := resolveSiblingSeasonOrSeries(&SyncFile{SyncPathId: 10, Path: "/remote/missing"}); err != nil || found {
		t.Fatalf("不存在的目录不应返回候选：%+v %v", target, err)
	}
	if db.Db.Dialector.Name() == "sqlite" {
		var columns []struct{ Name string }
		if err := db.Db.Raw("PRAGMA index_info(" + syncFileSiblingPathIndexName + ")").Scan(&columns).Error; err != nil ||
			len(columns) != 2 || columns[0].Name != "sync_path_id" || columns[1].Name != "path" {
			t.Fatalf("目录索引列不正确：%+v %v", columns, err)
		}
		for _, path := range []string{directory, "/remote/missing"} {
			var plan []struct{ Detail string }
			if err := db.Db.Raw("EXPLAIN QUERY PLAN SELECT * FROM sync_files WHERE sync_path_id = ? AND path = ? AND id <> ? ORDER BY id DESC LIMIT 50", 10, path, self.ID).Scan(&plan).Error; err != nil {
				t.Fatal(err)
			}
			if len(plan) != 1 || !strings.Contains(plan[0].Detail, syncFileSiblingPathIndexName) {
				t.Fatalf("目录查询应直接使用路径索引，无需另行排序：%+v", plan)
			}
			t.Logf("path=%s plan=%s", path, plan[0].Detail)
		}
	}
}

func TestResolveEmbyRefreshTargetUsesSiblingLibraryEvidenceFromSameEpisode(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	withLibrary := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode01.mkv", "same-sibling-lib")
	if err := db.Db.Create(&EmbyMediaItem{
		ItemId:     "201",
		ItemIdInt:  201,
		Name:       "第 1 集",
		Type:       "Episode",
		SeasonId:   "301",
		SeasonName: "第一季",
		SeriesId:   "401",
		SeriesName: "剧集",
		LibraryId:  "lib-tv",
		LastSeenAt: nowUnix(),
	}).Error; err != nil {
		t.Fatalf("创建带媒体库 sibling item 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 201, SyncFileId: withLibrary.ID, PickCode: withLibrary.PickCode}).Error; err != nil {
		t.Fatalf("创建带媒体库 sibling 关联失败: %v", err)
	}
	withoutLibrary := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode02.mkv", "same-sibling-empty")
	if err := db.Db.Create(&EmbyMediaItem{
		ItemId:     "202",
		ItemIdInt:  202,
		Name:       "第 2 集",
		Type:       "Episode",
		SeasonId:   "301",
		SeasonName: "未确认第一季",
		SeriesId:   "401",
		SeriesName: "未确认剧集",
		LastSeenAt: nowUnix(),
	}).Error; err != nil {
		t.Fatalf("创建无媒体库 sibling item 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 202, SyncFileId: withoutLibrary.ID, PickCode: withoutLibrary.PickCode}).Error; err != nil {
		t.Fatalf("创建无媒体库 sibling 关联失败: %v", err)
	}
	newEpisode := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode03.mkv", "same-sibling-new")

	target, err := ResolveEmbyRefreshTarget(newEpisode)
	if err != nil {
		t.Fatalf("解析 sibling 刷新目标失败: %v", err)
	}
	if target.ItemID != "301" || target.ItemName != "第一季" || target.FallbackLibraryId != "lib-tv" {
		t.Fatalf("刷新目标 = %+v，期望目标信息和媒体库都来自同一个带库 sibling", target)
	}
}

func TestResolveEmbyRefreshTargetFallsBackToPathLookupWhenLocalRelationIsStale(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/movie", "movie.mkv", "stale")
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 999, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建陈旧关联失败: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emby/Items" || r.URL.Query().Get("Path") != syncFile.LocalFilePath {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"Items":[{"Id":"501","Name":"path movie","Type":"Movie","Path":"`+syncFile.LocalFilePath+`"}],"TotalRecordCount":1}`)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.ItemID != "501" || target.ItemType != "Movie" || target.Recursive {
		t.Fatalf("刷新目标 = %+v，期望路径兜底 Movie 501", target)
	}
}

func TestResolveEmbyRefreshTargetPathLookupUsesAncestorsInMultipleLibrarySyncPath(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/show", "episode.mkv", "path-ancestors")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/emby/Items" && r.URL.Query().Get("Path") == syncFile.LocalFilePath:
			fmt.Fprint(w, `{"Items":[{"Id":"501","Name":"path episode","Type":"Episode","Path":"`+syncFile.LocalFilePath+`"}],"TotalRecordCount":1}`)
		case r.URL.Path == "/emby/Items/501/Ancestors":
			fmt.Fprint(w, `[{"Id":"tv-folder","Path":"/media/tv"}]`)
		case r.URL.Path == "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-tv","Name":"剧集","Locations":["/media/tv"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.ItemID != "501" || target.FallbackLibraryId != "lib-tv" || target.FallbackLibraryName != "剧集" {
		t.Fatalf("刷新目标 = %+v，期望 Ancestors 解析到 lib-tv/剧集", target)
	}
}

func TestResolveEmbyRefreshTargetPathLookupPrefersAncestorsOverUniqueSyncPathLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/show", "episode.mkv", "path-unique-ancestors")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/emby/Items" && r.URL.Query().Get("Path") == syncFile.LocalFilePath:
			fmt.Fprint(w, `{"Items":[{"Id":"501","Name":"path episode","Type":"Episode","Path":"`+syncFile.LocalFilePath+`"}],"TotalRecordCount":1}`)
		case r.URL.Path == "/emby/Items/501/Ancestors":
			fmt.Fprint(w, `[{"Id":"tv-folder","Path":"/media/tv"}]`)
		case r.URL.Path == "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-tv","Name":"剧集","Locations":["/media/tv"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.FallbackLibraryId != "lib-tv" || target.FallbackLibraryName != "剧集" {
		t.Fatalf("刷新目标 = %+v，期望 Ancestors 优先解析到 lib-tv/剧集", target)
	}
}

func TestResolveEmbyRefreshTargetResolvesSiblingSeasonLibraryConflictFromAncestors(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-a", LibraryName: "剧集 A", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-b", LibraryName: "剧集 B", SyncPathId: 10})
	for index, libraryID := range []string{"lib-a", "lib-b"} {
		sibling := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", fmt.Sprintf("episode0%d.mkv", index+1), fmt.Sprintf("conflict-%d", index+1))
		itemID := fmt.Sprintf("20%d", index+1)
		if err := db.Db.Create(&EmbyMediaItem{
			ItemId:     itemID,
			ItemIdInt:  helpersStringToInt64ForTest(itemID),
			Name:       "episode-" + itemID,
			Type:       "Episode",
			SeasonId:   "301",
			SeasonName: "第一季",
			SeriesId:   "401",
			SeriesName: "剧集",
			LibraryId:  libraryID,
			LastSeenAt: nowUnix(),
		}).Error; err != nil {
			t.Fatalf("创建 sibling item 失败: %v", err)
		}
		if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: uint(helpersStringToInt64ForTest(itemID)), SyncFileId: sibling.ID, PickCode: sibling.PickCode}).Error; err != nil {
			t.Fatalf("创建 sibling 关联失败: %v", err)
		}
	}
	newEpisode := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode03.mkv", "conflict-new")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items/301/Ancestors":
			fmt.Fprint(w, `[{"Id":"tv-folder","Path":"/media/tv"}]`)
		case "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-b","Name":"剧集 B","Locations":["/media/tv"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	target, err := ResolveEmbyRefreshTarget(newEpisode)
	if err != nil {
		t.Fatalf("解析 sibling 刷新目标失败: %v", err)
	}
	if target.ItemID != "301" || target.ItemType != "Season" {
		t.Fatalf("刷新目标 = %+v，期望保留 Season 301 item 目标", target)
	}
	if target.FallbackLibraryId != "lib-b" || target.FallbackLibraryName != "剧集 B" {
		t.Fatalf("冲突 sibling fallback = %s/%s，期望通过 Ancestors 解析为 lib-b/剧集 B", target.FallbackLibraryId, target.FallbackLibraryName)
	}
}

func TestResolveEmbyRefreshTargetPathLookupPrefersSiblingLibraryOverAncestors(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-a", LibraryName: "剧集 A", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-b", LibraryName: "剧集 B", SyncPathId: 10})
	if err := db.Db.Create(&EmbyMediaItem{ItemId: "501", ItemIdInt: 501, Type: "Episode", SeasonId: "301", LibraryId: "lib-a", LastSeenAt: nowUnix()}).Error; err != nil {
		t.Fatalf("创建本地 sibling item 失败: %v", err)
	}
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/show", "episode.mkv", "path-sibling")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/emby/Items" && r.URL.Query().Get("Path") == syncFile.LocalFilePath:
			fmt.Fprint(w, `{"Items":[{"Id":"301","Name":"path season","Type":"Season","Path":"`+syncFile.LocalFilePath+`"}],"TotalRecordCount":1}`)
		case r.URL.Path == "/emby/Items/301/Ancestors":
			fmt.Fprint(w, `[{"Id":"tv-folder","Path":"/media/tv"}]`)
		case r.URL.Path == "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-b","Name":"剧集 B","Locations":["/media/tv"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.FallbackLibraryId != "lib-a" {
		t.Fatalf("fallback library = %s，期望优先使用本地 sibling 库 lib-a", target.FallbackLibraryId)
	}
}

func TestResolveEmbyRefreshTargetUsesCachedItemWithoutNetworkValidation(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/movie", "movie.mkv", "stale-id")
	createEmbyRefreshItem(t, "101", "Movie", "", "")
	if err := db.Db.Model(&EmbyMediaItem{}).Where("item_id = ?", "101").Update("last_seen_at", 0).Error; err != nil {
		t.Fatalf("标记本地 Emby item 缓存未确认失败: %v", err)
	}
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 101, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		http.NotFound(w, r)
	}))
	defer server.Close()
	GlobalEmbyConfig.EmbyUrl = server.URL

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.ItemID != "101" || target.ItemType != "Movie" || target.Recursive {
		t.Fatalf("刷新目标 = %+v，期望使用本地缓存 Movie 101", target)
	}
	if requestCount != 0 {
		t.Fatalf("本地快速解析发起 Emby 请求 %d 次，期望 0 次", requestCount)
	}
}

func TestResolveEmbyTargetLibraryDoesNotPromoteFallbackHintInMultipleLibraries(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})

	resolution, err := resolveEmbyTargetLibraryWithDB(db.Db, EmbyRefreshTarget{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "unknown-item",
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影",
	}, []uint{10})
	if err != nil {
		t.Fatalf("解析媒体库失败: %v", err)
	}
	if resolution.Resolved || resolution.LibraryID != "" || resolution.Source == EmbyLibraryResolutionSourceFallbackHint {
		t.Fatalf("fallback hint 被升级为确定证据: %+v，期望多库 unresolved", resolution)
	}
	if !reflect.DeepEqual(resolution.Candidates, []string{"lib-movie", "lib-tv"}) {
		t.Fatalf("候选媒体库 = %v，期望保留 lib-movie/lib-tv", resolution.Candidates)
	}
}

func TestRequestEmbyRefreshBySyncFileFallsBackToLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/movie", "movie.mkv", "fallback")
	syncFile.LocalFilePath = ""

	if err := RequestEmbyRefreshBySyncFile(syncFile); err != nil {
		t.Fatalf("提交刷新失败: %v", err)
	}
	var task EmbyLibraryRefreshTask
	if err := db.Db.Where("library_id = ?", "lib-movie").First(&task).Error; err != nil {
		t.Fatalf("读取媒体库刷新任务失败: %v", err)
	}
	if task.TargetType != "" && task.TargetType != string(EmbyRefreshTargetTypeLibrary) {
		t.Fatalf("target_type = %s，期望空或 library 兼容值", task.TargetType)
	}
}

func TestRequestEmbyRefreshBySyncFileDedupesSameItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/movie", "movie.mkv", "dedupe")
	createEmbyRefreshItem(t, "101", "Movie", "", "")
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 101, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}

	if err := RequestEmbyRefreshBySyncFile(syncFile); err != nil {
		t.Fatalf("首次提交刷新失败: %v", err)
	}
	if err := RequestEmbyRefreshBySyncFile(syncFile); err != nil {
		t.Fatalf("重复提交刷新失败: %v", err)
	}

	var tasks []EmbyLibraryRefreshTask
	if err := db.Db.Find(&tasks).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TargetType != string(EmbyRefreshTargetTypeItem) {
		t.Fatalf("刷新任务 = %+v，期望单个 item 任务", tasks)
	}
	itemIDs := tasks[0].GetItemIds()
	if len(itemIDs) != 1 || itemIDs[0] != "101" || tasks[0].TaskKey != embyItemRefreshTaskKey("101") || tasks[0].LibraryId != "lib-tv" {
		t.Fatalf("item_ids=%v task_key=%s library_id=%s，期望 item 101 使用独立去抖键并保留真实媒体库", itemIDs, tasks[0].TaskKey, tasks[0].LibraryId)
	}
}

func TestRequestEmbyRefreshBySyncFileStoresFallbackLibraryForItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode.mkv", "episode-fallback")
	createEmbyRefreshItem(t, "301", "Episode", "401", "501")
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 301, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}

	if err := RequestEmbyRefreshBySyncFile(syncFile); err != nil {
		t.Fatalf("提交刷新失败: %v", err)
	}

	var task EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("301")).First(&task).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if task.FallbackLibraryId != "lib-tv" || task.FallbackLibraryName != "剧集" {
		t.Fatalf("fallback library = %s/%s，期望 lib-tv/剧集", task.FallbackLibraryId, task.FallbackLibraryName)
	}
}

func TestResolveEmbyRefreshTargetUsesItemLibraryInsteadOfFirstSyncPathLibrary(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	syncFile := createEmbyRefreshSyncFile(t, 10, "/remote/show/Season 01", "episode.mkv", "episode-real-library")
	createEmbyRefreshItem(t, "301", "Episode", "401", "501")
	if err := db.Db.Create(&EmbyMediaSyncFile{SyncPathId: 10, EmbyItemId: 301, SyncFileId: syncFile.ID, PickCode: syncFile.PickCode}).Error; err != nil {
		t.Fatalf("创建关联失败: %v", err)
	}

	target, err := ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		t.Fatalf("解析刷新目标失败: %v", err)
	}
	if target.FallbackLibraryId != "lib-tv" || target.FallbackLibraryName != "剧集" {
		t.Fatalf("fallback library = %s/%s，期望使用 item 真实媒体库 lib-tv/剧集", target.FallbackLibraryId, target.FallbackLibraryName)
	}
}

func TestRequestEmbyRefreshTargetsRepairsIncorrectFallbackFromIndexedItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})
	createEmbyRefreshItem(t, "301", "Episode", "401", "501")

	err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{{
		TargetType:          EmbyRefreshTargetTypeItem,
		ItemID:              "301",
		ItemName:            "第 1 集",
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影",
	}})
	if err != nil {
		t.Fatalf("提交 item 刷新失败: %v", err)
	}

	var task EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("301")).First(&task).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if task.FallbackLibraryId != "lib-tv" || task.FallbackLibraryName != "剧集" {
		t.Fatalf("修复后 fallback library = %s/%s，期望 lib-tv/剧集", task.FallbackLibraryId, task.FallbackLibraryName)
	}
}

func TestRequestEmbyRefreshTargetsLeavesItemUnresolvedForMultipleLibraries(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})

	if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     "unknown-item",
		ItemName:   "未知条目",
	}}); err != nil {
		t.Fatalf("提交 item 刷新失败: %v", err)
	}

	var task EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("unknown-item")).First(&task).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if task.FallbackLibraryId != "" || task.FallbackLibraryName != "" {
		t.Fatalf("多库无证据时 fallback library = %s/%s，期望 unresolved", task.FallbackLibraryId, task.FallbackLibraryName)
	}
}

func TestRequestEmbyRefreshTargetsExpandsLibraryFallbackAcrossAllSyncPathLibraries(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})

	if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{{TargetType: EmbyRefreshTargetTypeLibrary}}); err != nil {
		t.Fatalf("提交媒体库刷新失败: %v", err)
	}

	var tasks []EmbyLibraryRefreshTask
	if err := db.Db.Order("library_id ASC").Find(&tasks).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if len(tasks) != 2 || tasks[0].LibraryId != "lib-movie" || tasks[1].LibraryId != "lib-tv" {
		t.Fatalf("媒体库刷新任务 = %+v，期望展开 lib-movie 和 lib-tv", tasks)
	}
}

func TestRequestEmbyRefreshTargetsExpandsAllLibrariesWhenLibraryTargetHasFallbackHint(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})

	if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{{
		TargetType:          EmbyRefreshTargetTypeLibrary,
		FallbackLibraryId:   "lib-movie",
		FallbackLibraryName: "电影",
	}}); err != nil {
		t.Fatalf("提交媒体库刷新失败: %v", err)
	}

	var tasks []EmbyLibraryRefreshTask
	if err := db.Db.Order("library_id ASC").Find(&tasks).Error; err != nil {
		t.Fatalf("查询媒体库刷新任务失败: %v", err)
	}
	if len(tasks) != 2 || tasks[0].LibraryId != "lib-movie" || tasks[1].LibraryId != "lib-tv" {
		t.Fatalf("媒体库刷新任务 = %+v，期望展开 lib-movie 和 lib-tv", tasks)
	}
}

func TestRequestEmbyRefreshTargetsFillsFallbackLibraryForItem(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-tv", LibraryName: "剧集", SyncPathId: 10})

	if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{
		{
			TargetType: EmbyRefreshTargetTypeItem,
			ItemID:     "301",
			ItemName:   "第一季",
			ItemType:   "Season",
			Recursive:  true,
		},
	}); err != nil {
		t.Fatalf("批量提交刷新失败: %v", err)
	}

	var task EmbyLibraryRefreshTask
	if err := db.Db.Where("task_key = ?", embyItemRefreshTaskKey("301")).First(&task).Error; err != nil {
		t.Fatalf("查询 item 刷新任务失败: %v", err)
	}
	if task.FallbackLibraryId != "lib-tv" || task.FallbackLibraryName != "剧集" {
		t.Fatalf("fallback library = %s/%s，期望 lib-tv/剧集", task.FallbackLibraryId, task.FallbackLibraryName)
	}
}

func TestRequestEmbyRefreshTargetsDedupesItemsAndLibraries(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})

	targets := []EmbyRefreshTarget{
		{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              "101",
			ItemName:            "电影 1",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影",
		},
		{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              "101",
			ItemName:            "电影 1",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影",
		},
		{
			TargetType:          EmbyRefreshTargetTypeLibrary,
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影",
		},
	}

	if err := RequestEmbyRefreshTargets(10, targets); err != nil {
		t.Fatalf("批量提交刷新失败: %v", err)
	}
	var tasks []EmbyLibraryRefreshTask
	if err := db.Db.Find(&tasks).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TargetType != EmbyLibraryRefreshTargetTypeLibrary || tasks[0].LibraryId != "lib-movie" {
		t.Fatalf("刷新任务 = %+v，期望只提交 lib-movie 媒体库刷新", tasks)
	}
}

func TestRequestEmbyRefreshTargetsGenericLibraryFallbackCoversSameLibraryItems(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	db.Db.Create(&EmbyLibrarySyncPath{LibraryId: "lib-movie", LibraryName: "电影", SyncPathId: 10})

	if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{
		{TargetType: EmbyRefreshTargetTypeLibrary},
		{
			TargetType:          EmbyRefreshTargetTypeItem,
			ItemID:              "101",
			ItemName:            "电影 1",
			FallbackLibraryId:   "lib-movie",
			FallbackLibraryName: "电影",
		},
	}); err != nil {
		t.Fatalf("批量提交刷新失败: %v", err)
	}

	var tasks []EmbyLibraryRefreshTask
	if err := db.Db.Find(&tasks).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if len(tasks) != 1 || tasks[0].TargetType != EmbyLibraryRefreshTargetTypeLibrary || tasks[0].LibraryId != "lib-movie" {
		t.Fatalf("刷新任务 = %+v，期望 generic library fallback 覆盖 item", tasks)
	}
}

func TestRequestEmbyRefreshTargetsKeepsItemsAcrossDifferentLibraries(t *testing.T) {
	setupEmbyRefreshTestDB(t)

	targets := []EmbyRefreshTarget{
		{
			TargetType:        EmbyRefreshTargetTypeItem,
			ItemID:            "101",
			ItemName:          "电影",
			FallbackLibraryId: "lib-movie",
		},
		{
			TargetType:        EmbyRefreshTargetTypeItem,
			ItemID:            "301",
			ItemName:          "第一季",
			ItemType:          "Season",
			Recursive:         true,
			FallbackLibraryId: "lib-tv",
		},
	}
	if err := RequestEmbyRefreshTargets(10, targets); err != nil {
		t.Fatalf("批量提交刷新失败: %v", err)
	}
	var tasks []EmbyLibraryRefreshTask
	if err := db.Db.Order("library_id ASC").Find(&tasks).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("刷新任务数量 = %d，期望 2", len(tasks))
	}
}

func TestRequestEmbyRefreshTargetsSetsDebounceTime(t *testing.T) {
	setupEmbyRefreshTestDB(t)

	if err := RequestEmbyRefreshTargets(10, []EmbyRefreshTarget{
		{
			TargetType:        EmbyRefreshTargetTypeItem,
			ItemID:            "101",
			ItemName:          "电影",
			FallbackLibraryId: "lib-movie",
		},
	}); err != nil {
		t.Fatalf("批量提交刷新失败: %v", err)
	}
	var task EmbyLibraryRefreshTask
	if err := db.Db.First(&task).Error; err != nil {
		t.Fatalf("查询刷新任务失败: %v", err)
	}
	want := task.LastEventAt + DefaultEmbyRefreshDebounceSeconds
	if task.RefreshAfterAt != want {
		t.Fatalf("refresh_after_at = %d，期望 %d", task.RefreshAfterAt, want)
	}
}

func TestEmbyRefreshTargetWaitsForSameSyncPathDownloads(t *testing.T) {
	setupEmbyRefreshTestDB(t)

	now := nowUnix()
	task := newPendingEmbyLibraryRefreshTask("lib-tv", "剧集", []uint{10}, now-DefaultEmbyRefreshDebounceSeconds-1)
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建刷新任务失败: %v", err)
	}
	for _, status := range []DownloadStatus{DownloadStatusPending, DownloadStatusDownloading} {
		db.Db.Exec("DELETE FROM db_download_tasks")
		if err := db.Db.Create(&DbDownloadTask{SyncPathId: 10, Status: status}).Error; err != nil {
			t.Fatalf("创建下载任务失败: %v", err)
		}
		ready, reason, err := IsEmbyLibraryRefreshTaskReady(task, now)
		if err != nil {
			t.Fatalf("检查刷新任务失败: %v", err)
		}
		if ready || reason != "download_running" {
			t.Fatalf("下载状态 %s 时 ready=%v reason=%s，期望等待同 sync_path_id 下载完成", status, ready, reason)
		}
	}
}
