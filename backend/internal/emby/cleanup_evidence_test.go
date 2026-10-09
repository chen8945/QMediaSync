package emby

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
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
	for _, observation := range collector.ancestors.observations {
		if len(observation.Directories) != 2 || observation.Directories[1].SeasonNumber == nil || *observation.Directories[1].SeasonNumber != 0 {
			t.Fatalf("Specials 季号的明确零值丢失：%#v", observation)
		}
	}
	snapshots[0].Item.Path = "/second-root/Show/Specials/one.strm"
	if err := collector.enrich(t.Context(), client, snapshots[:1]); err != nil || reads != 2 {
		t.Fatalf("不同物理根错误复用祖先：reads=%d err=%v", reads, err)
	}
	for key, observation := range collector.ancestors.observations {
		observation.At = time.Now().Add(-31 * time.Second)
		collector.ancestors.observations[key] = observation
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

func TestEmbyCleanupAncestorWithoutParentDoesNotShare(t *testing.T) {
	previousDB, previousLogger := db.Db, helpers.AppLogger
	db.Db = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger })
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		fmt.Fprint(w, `[{"Id":"200","Type":"Series","Path":"/media/Show","IsFolder":true},{"Id":"201","Type":"Season","Path":"/media/Show/Season 1","IsFolder":true}]`)
	}))
	defer server.Close()
	client := embyclientrestgo.NewClient(server.URL, "test")
	shared := &embyAncestorCache{}
	for _, id := range []string{"101", "102"} {
		collector := embyCleanupEvidenceCollector{ancestors: shared}
		snapshots := []models.EmbyItemSnapshot{{Item: models.EmbyMediaItem{
			ItemId: id, Type: "Episode", SeriesId: "200", SeasonId: "201", Path: "/media/Show/Season 1/" + id + ".strm",
		}}}
		if err := collector.enrich(t.Context(), client, snapshots); err != nil {
			t.Fatal(err)
		}
	}
	if reads.Load() != 2 {
		t.Fatalf("missing parent identity shared an ancestor read: %d", reads.Load())
	}
}

func TestEmbyCleanupAncestorCancellationReachesHTTPAndAllowsRetry(t *testing.T) {
	previousDB, previousLogger := db.Db, helpers.AppLogger
	db.Db = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger })
	var reads atomic.Int32
	started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reads.Add(1) == 1 {
			close(started)
			select {
			case <-r.Context().Done():
				close(stopped)
			case <-release:
			}
			return
		}
		fmt.Fprint(w, `[{"Id":"7","Type":"Folder","Path":"/media/Work","IsFolder":true}]`)
	}))
	defer func() {
		close(release)
		server.Close()
	}()
	client := embyclientrestgo.NewClient(server.URL, "test")
	collector := embyCleanupEvidenceCollector{}
	snapshots := []models.EmbyItemSnapshot{{Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", ParentId: "7", Path: "/media/Work/movie.strm"}}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- collector.enrich(ctx, client, snapshots) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("ancestor HTTP request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled ancestor request did not reach the caller: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled ancestor request did not return")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled ancestor HTTP request did not stop")
	}
	if err := collector.enrich(t.Context(), client, snapshots); err != nil || reads.Load() != 2 || len(snapshots[0].MediaDirectories) != 1 {
		t.Fatalf("cancelled ancestor read poisoned retry: reads=%d directories=%+v err=%v", reads.Load(), snapshots[0].MediaDirectories, err)
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
	shared := &embyAncestorCache{}
	collector := embyCleanupEvidenceCollector{ancestors: shared}
	other := embyCleanupEvidenceCollector{ancestors: shared}
	snapshots := []models.EmbyItemSnapshot{
		{Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", ParentId: "7", Path: "/media/Movies/Work/one.strm"}},
		{Item: models.EmbyMediaItem{ItemId: "102", Type: "Movie", ParentId: "7", Path: "/media/Movies/Work/two.strm"}},
	}
	if err := collector.enrich(t.Context(), client, snapshots[:1]); err != nil {
		t.Fatal(err)
	}
	if err := other.enrich(t.Context(), client, snapshots[1:]); err != nil || reads != 1 {
		t.Fatalf("同父目录电影版本没有复用祖先读取：reads=%d err=%v", reads, err)
	}
	var observed []models.EmbyMediaDirectory
	for _, observation := range shared.observations {
		observed = observation.Directories
	}
	if len(shared.observations) != 1 || len(observed) != 1 || observed[0].MediaType != "Movie" || observed[0].LocalPath != "/media/Movies/Work" || observed[0].ItemID != "" {
		t.Fatalf("观察缓存应只保存目录角色而不含条目身份：%#v", shared.observations)
	}
	first, second := snapshots[0].MediaDirectories, snapshots[1].MediaDirectories
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
			for _, observation := range collector.ancestors.observations {
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
	for _, observation := range collector.ancestors.observations {
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

func TestEmbyAncestorDirectoriesPreservesWindowsAndUNCRoles(t *testing.T) {
	for _, tc := range []struct {
		name, mediaType, itemPath, directory string
		want                                 int
	}{
		{"Windows movie", "Movie", `D:\Media\Film\movie.strm`, `d:\media\Film`, 1},
		{"UNC movie", "Movie", `\\server\share\Film\movie.strm`, `\\SERVER\share\Film`, 1},
		{"Windows season", "Season", `D:\Media\Show\Season 1\episode.strm`, `d:\media\Show\Season 1`, 1},
		{"UNC series", "Series", `\\server\share\Show\Season 1\episode.strm`, `\\SERVER\share\Show`, 1},
		{"Windows sibling", "Movie", `D:\Media\Film-other\movie.strm`, `D:\Media\Film`, 0},
		{"Windows parent traversal", "Movie", `D:\Media\Film\..\Other\movie.strm`, `D:\Media\Film`, 0},
		{"movie grandparent", "Movie", `D:\Media\Film\child\movie.strm`, `D:\Media\Film`, 0},
		{"native newline filename", "Movie", "/media/Film/movie\npart.strm", "/media/Film", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := models.EmbyMediaItem{ItemId: "101", Type: "Movie", ParentId: "7", Path: tc.itemPath}
			ancestorType := "Folder"
			if tc.mediaType != "Movie" {
				item.Type, item.SeriesId, item.SeasonId = "Episode", "7", "7"
				ancestorType = tc.mediaType
			}
			observed := embyAncestorDirectories(item, []embyclientrestgo.AncestorDto{{ID: "7", Type: ancestorType, Path: tc.directory, IsFolder: true}})
			directories := embySnapshotDirectories(item, observed)
			if len(directories) != tc.want {
				t.Fatalf("directories = %+v; want %d", directories, tc.want)
			}
			if tc.want != 0 && (directories[0].MediaType != tc.mediaType || directories[0].LocalPath != tc.directory) {
				t.Fatalf("original media role/path was lost: %+v", directories)
			}
		})
	}
}

func TestEmbyWebhookEarlyObservationUsesLocalDirectoryWithoutCloudHTTP(t *testing.T) {
	f := setupWebhookWorkerTest(t, false)
	if err := db.Db.AutoMigrate(&models.Media{}, &models.ScrapePath{}, &models.SyncRootEvidence{}); err != nil {
		t.Fatal(err)
	}
	var root models.SyncPath
	var account models.Account
	if err := db.Db.First(&root, f.file.SyncPathId).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&account, f.file.AccountId).Error; err != nil {
		t.Fatal(err)
	}
	account.BaseModel = models.BaseModel{ID: uint(90_000_000 + cleanupSourceAccountID.Add(1))}
	account.UserId = fmt.Sprintf("early-observation-%d", account.ID)
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	root.AccountId, f.file.AccountId = account.ID, account.ID
	if err := db.Db.Model(&root).Update("account_id", account.ID).Error; err != nil {
		t.Fatal(err)
	}
	f.file.Path, f.file.ParentId = "/movies/Work", "201"
	f.file.LocalFilePath = filepath.Join(root.GetFullLocalPath(), "Work", "movie.strm")
	if err := db.Db.Save(&f.file).Error; err != nil {
		t.Fatal(err)
	}
	directory := models.SyncFile{SourceType: root.SourceType, AccountId: account.ID, SyncPathId: root.ID, FileType: v115open.TypeDir, FileId: "201", ParentId: root.BaseCid, Path: "/movies", FileName: "Work"}
	if err := db.Db.Create(&directory).Error; err != nil {
		t.Fatal(err)
	}
	rootEvidence := models.SyncRootEvidence{SyncPathID: root.ID, SourceType: root.SourceType, AccountID: account.ID, AccountIdentity: models.EmbyAccountIdentity(account), RootFileID: root.BaseCid, RootPath: "/movies", ObservedAt: 123,
		Ancestors: []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: root.BaseCid, ParentID: "0", Path: "/movies"}}}
	if err := models.SaveSyncRootEvidenceTx(db.Db, rootEvidence, root.BaseCid); err != nil {
		t.Fatal(err)
	}
	var cloudReads atomic.Int64
	account.Get115Client().SetTransport(cleanupSourceHTTPTransport(func(w http.ResponseWriter, r *http.Request) {
		cloudReads.Add(1)
		t.Errorf("Webhook 观察不得查询网盘：%s %s", r.Method, r.URL.Path)
		http.Error(w, "unexpected cloud access", http.StatusBadRequest)
	}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"server-a"}`)
		case "/emby/Items":
			fmt.Fprintf(w, `{"Items":[{"Id":"101","ParentId":"201","Type":"Movie","Path":%q,"MediaSources":[{"Id":"source-101","ItemId":"101","Path":"http://qms/stream?pickcode=p1"}]}],"TotalRecordCount":1}`, f.file.LocalFilePath)
		case "/emby/Items/101/Ancestors":
			fmt.Fprintf(w, `[{"Id":"201","Type":"Folder","IsFolder":true,"Path":%q}]`, filepath.Dir(f.file.LocalFilePath))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	f.config.EmbyUrl = server.URL
	if err := db.Db.Model(&f.config).Update("emby_url", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := models.BeginEmbyIndexRead("server-a", &f.config); err != nil {
		t.Fatal(err)
	}
	record, err := models.SaveEmbyWebhook(t.Context(), f.envelope("library.new"))
	if err != nil {
		t.Fatal(err)
	}
	SetEmbySyncRunning(true)
	f.worker.process(t.Context(), claimWebhookTest(t))
	fresh := readWebhookTest(t, record.ID)
	if fresh.ObservationJSON == "" || fresh.Status != models.EmbyWebhookRetry || cloudReads.Load() != 0 {
		t.Fatalf("早期观察失败：%+v cloud=%d", fresh, cloudReads.Load())
	}
	var evidence models.EmbyItemEvidence
	if err := db.Db.First(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
	if err != nil || len(metadata.DirectoryScopes) != 1 || len(metadata.MediaDirectories) != 1 {
		t.Fatalf("早期观察目录证据缺失：%+v %v", metadata, err)
	}
}

func TestEmbyCleanupLocalReadFailureReachesCaller(t *testing.T) {
	f := setupWebhookWorkerTest(t, true)
	if err := db.Db.AutoMigrate(&models.Media{}, &models.ScrapePath{}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected local evidence read failure")
	injections := 0
	const callback = "test:emby-collector-local-failure"
	if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "media" {
			injections++
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
	snapshots := []models.EmbyItemSnapshot{workerSnapshot(f.file)}
	collector := embyCleanupEvidenceCollector{}
	err := collector.enrich(t.Context(), embyclientrestgo.NewClient(f.config.EmbyUrl, "fixture"), snapshots)
	if injections != 1 {
		t.Fatalf("本地目录资料查询注入次数=%d，期望 1", injections)
	}
	if !errors.Is(err, injected) {
		t.Fatalf("本地读取错误被吞：%v", err)
	}
	countWebhookTest(t, &models.EmbyItemEvidence{}, 1)
}
