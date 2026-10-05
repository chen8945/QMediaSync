package emby

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestEmbyCleanupAncestorsBoundedReuseAndPhysicalRoots(t *testing.T) {
	previousDB, previousLogger := db.Db, helpers.AppLogger
	db.Db = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger })
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		fmt.Fprint(w, `[{"Id":"200","Type":"Series","Path":"/media/Show","IsFolder":true},{"Id":"201","Type":"Season","Path":"/media/Show/Specials","IndexNumber":0,"IsFolder":true}]`)
	}))
	defer server.Close()
	client := embyclientrestgo.NewClient(server.URL, "test")
	collector := embyCleanupEvidenceCollector{}
	snapshots := []models.EmbyItemSnapshot{
		{Item: models.EmbyMediaItem{ItemId: "101", Type: "Episode", ParentId: "201", SeriesId: "200", SeasonId: "201", Path: "/media/Show/Specials/one.strm"}},
		{Item: models.EmbyMediaItem{ItemId: "102", Type: "Episode", ParentId: "201", SeriesId: "200", SeasonId: "201", Path: "/media/Show/Specials/two.strm"}},
	}
	if err := collector.enrich(t.Context(), client, snapshots); err != nil || reads != 1 {
		t.Fatalf("同物理季中的集没有复用祖先读取：reads=%d err=%v", reads, err)
	}
	for _, observation := range collector.ancestors {
		if len(observation.Directories) != 2 || observation.Directories[1].SeasonNumber == nil || *observation.Directories[1].SeasonNumber != 0 {
			t.Fatalf("Specials 季号的明确零值丢失：%#v", observation)
		}
	}
	snapshots[0].Item.Path = "/second-root/Show/Specials/one.strm"
	if err := collector.enrich(t.Context(), client, snapshots[:1]); err != nil || reads != 2 {
		t.Fatalf("不同物理根错误复用祖先：reads=%d err=%v", reads, err)
	}
	for key, observation := range collector.ancestors {
		observation.At = time.Now().Add(-31 * time.Second)
		collector.ancestors[key] = observation
	}
	if err := collector.enrich(t.Context(), client, snapshots[:1]); err != nil || reads != 3 {
		t.Fatalf("过期观察没有重新读取：reads=%d err=%v", reads, err)
	}
}

func TestEmbyCleanupAncestorFailureDoesNotRepeatPerEpisode(t *testing.T) {
	previousDB, previousLogger := db.Db, helpers.AppLogger
	db.Db = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger })
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	collector := embyCleanupEvidenceCollector{}
	client := embyclientrestgo.NewClient(server.URL, "test")
	var snapshots []models.EmbyItemSnapshot
	for i := 0; i < 20; i++ {
		snapshots = append(snapshots, models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: fmt.Sprint(i + 100), Type: "Episode", ParentId: "201", SeriesId: "200", SeasonId: "201", Path: fmt.Sprintf("/media/Show/Specials/%d.strm", i)}})
	}
	if err := collector.enrich(t.Context(), client, snapshots); err != nil || reads != 1 {
		t.Fatalf("失败祖先读取按集重复：reads=%d err=%v", reads, err)
	}
	for _, snapshot := range snapshots {
		if len(snapshot.DirectoryScopes) != 0 {
			t.Fatal("失败查询不能成为空目录授权")
		}
	}
}

func TestEmbyMovieAncestorEvidenceBindsPerVersionAndReusesFolder(t *testing.T) {
	previousDB, previousLogger := db.Db, helpers.AppLogger
	db.Db = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger })
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		fmt.Fprint(w, `[{"Id":"3","Type":"CollectionFolder","IsFolder":true},{"Id":"7","Type":"Folder","Path":"/media/Movies/Work","IsFolder":true}]`)
	}))
	defer server.Close()
	client := embyclientrestgo.NewClient(server.URL, "test")
	collector := embyCleanupEvidenceCollector{}
	snapshots := []models.EmbyItemSnapshot{
		{Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", ParentId: "7", Path: "/media/Movies/Work/one.strm"}},
		{Item: models.EmbyMediaItem{ItemId: "102", Type: "Movie", ParentId: "7", Path: "/media/Movies/Work/two.strm"}},
	}
	if err := collector.enrich(t.Context(), client, snapshots); err != nil || reads != 1 {
		t.Fatalf("同父目录电影版本没有复用祖先读取：reads=%d err=%v", reads, err)
	}
	var observed []models.EmbyMediaDirectory
	for _, observation := range collector.ancestors {
		observed = observation.Directories
	}
	if len(collector.ancestors) != 1 || len(observed) != 1 || observed[0].MediaType != "Movie" || observed[0].LocalPath != "/media/Movies/Work" || observed[0].ItemID != "" {
		t.Fatalf("观察缓存应只保存目录角色而不含条目身份：%#v", collector.ancestors)
	}
	first := embySnapshotDirectories(snapshots[0].Item, observed)
	second := embySnapshotDirectories(snapshots[1].Item, observed)
	if len(first) != 1 || first[0].ItemID != "101" || len(second) != 1 || second[0].ItemID != "102" {
		t.Fatalf("每个版本必须绑定自身 ItemID：%#v %#v", first, second)
	}
	if observed[0].ItemID != "" {
		t.Fatal("按条目绑定不应污染观察缓存")
	}
}

func TestEmbyMovieAncestorEvidenceFiltering(t *testing.T) {
	cases := map[string]string{
		"valid":         `[{"Id":"7","Type":"Folder","Path":"/media/Movies/Work","IsFolder":true}]`,
		"non_folder":    `[{"Id":"7","Type":"Folder","Path":"/media/Movies/Work"}]`,
		"wrong_id":      `[{"Id":"8","Type":"Folder","Path":"/media/Movies/Work","IsFolder":true}]`,
		"wrong_path":    `[{"Id":"7","Type":"Folder","Path":"/media/Movies/Other","IsFolder":true}]`,
		"relative_path": `[{"Id":"7","Type":"Folder","Path":"Movies/Work","IsFolder":true}]`,
		"library_only":  `[{"Id":"3","Type":"CollectionFolder","IsFolder":true}]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			previousDB, previousLogger := db.Db, helpers.AppLogger
			db.Db = nil
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
			defer server.Close()
			collector := embyCleanupEvidenceCollector{}
			snapshots := []models.EmbyItemSnapshot{{Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", ParentId: "7", Path: "/media/Movies/Work/one.strm"}}}
			if err := collector.enrich(t.Context(), embyclientrestgo.NewClient(server.URL, "test"), snapshots); err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, observation := range collector.ancestors {
				found += len(observation.Directories)
			}
			if want := map[string]int{"valid": 1}[name]; found != want {
				t.Fatalf("过滤结果 %d，期望 %d", found, want)
			}
		})
	}
}

func TestEmbyMovieAncestorFailureDoesNotRepeatPerFolder(t *testing.T) {
	previousDB, previousLogger := db.Db, helpers.AppLogger
	db.Db = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger })
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads++
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	collector := embyCleanupEvidenceCollector{}
	client := embyclientrestgo.NewClient(server.URL, "test")
	snapshots := []models.EmbyItemSnapshot{
		{Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", ParentId: "7", Path: "/media/Movies/Work/one.strm"}},
		{Item: models.EmbyMediaItem{ItemId: "102", Type: "Movie", ParentId: "7", Path: "/media/Movies/Work/two.strm"}},
		{Item: models.EmbyMediaItem{ItemId: "103", Type: "Movie", ParentId: "8", Path: "/media/Movies/Other/three.strm"}},
	}
	if err := collector.enrich(t.Context(), client, snapshots); err != nil || reads != 2 {
		t.Fatalf("失败祖先读取按同父目录重复：reads=%d err=%v", reads, err)
	}
	for _, observation := range collector.ancestors {
		if !observation.Failed || len(observation.Directories) != 0 {
			t.Fatalf("失败观察不能成为空目录授权：%#v", observation)
		}
	}
}

func TestEmbySnapshotDirectoriesPreservesTVRoles(t *testing.T) {
	observed := []models.EmbyMediaDirectory{
		{ItemID: "201", MediaType: "Series", LocalPath: "/media/Show"},
		{ItemID: "202", MediaType: "Season", LocalPath: "/media/Show/Season 1"},
	}
	item := models.EmbyMediaItem{ItemId: "9001", Type: "Episode", SeriesId: "201", SeasonId: "202"}
	directories := embySnapshotDirectories(item, observed)
	if len(directories) != 2 || directories[0].ItemID != "201" || directories[1].ItemID != "202" ||
		directories[0].MediaType != "Series" || directories[1].MediaType != "Season" {
		t.Fatalf("TV 目录角色必须原样透传：%#v", directories)
	}
	if observed[0].ItemID != "201" || observed[1].ItemID != "202" {
		t.Fatal("按条目绑定不得修改观察缓存")
	}
}
