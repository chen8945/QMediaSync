package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

const mediaDownloadTestItem = "11111111111111111111111111111111"
const mediaDownloadTestUser = "22222222222222222222222222222222"

func setupMediaDownloadTestDB(t *testing.T) {
	t.Helper()
	oldDB := db.Db
	setupQueueStatusTestDB(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close(); db.Db = oldDB })
	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatal(err)
	}
}

func saveMediaDownloadConfig(t *testing.T, kind MediaServerType, address, key string) *EmbyConfig {
	t.Helper()
	config := &EmbyConfig{Enabled: true, EmbyUrl: address, EmbyApiKey: key, MediaServerType: kind}
	if kind == MediaServerJellyfin {
		stored := JellyfinConfig(*config)
		if err := db.Db.Create(&stored).Error; err != nil {
			t.Fatal(err)
		}
		config.ID = stored.ID
	} else if err := db.Db.Create(config).Error; err != nil {
		t.Fatal(err)
	}
	return config
}

func mediaDownloadTestServer(t *testing.T, kind MediaServerType, serverID, key string, status int, body string, allowPlayback bool, playbackCalls *atomic.Int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prefix, product := "/emby", "Emby Server"
		if kind == MediaServerJellyfin {
			prefix, product = "", "Jellyfin Server"
		}
		if r.URL.Path == "/System/Info/Public" {
			fmt.Fprintf(w, `{"Id":%q,"ProductName":%q}`, serverID, product)
			return
		}
		if r.URL.Path == "/System/Ping" {
			fmt.Fprintf(w, "%q", product)
			return
		}
		if kind == MediaServerJellyfin {
			if !strings.Contains(r.Header.Get("Authorization"), `Token="`+key+`"`) || r.URL.Query().Has("api_key") {
				t.Errorf("Jellyfin authentication not native")
			}
		} else if r.URL.Query().Get("api_key") != key {
			t.Errorf("Emby authentication mismatch")
		}
		switch r.URL.Path {
		case prefix + "/Users":
			fmt.Fprintf(w, `[{"Id":"reader","Policy":{"EnableAllFolders":true,"EnableMediaPlayback":false}},{"Id":%q,"Policy":{"EnableAllFolders":true,"EnableMediaPlayback":%t}}]`, mediaDownloadTestUser, allowPlayback)
		case prefix + "/Users/" + mediaDownloadTestUser + "/Items/" + mediaDownloadTestItem, prefix + "/Items/" + mediaDownloadTestItem:
			fmt.Fprintf(w, `{"Id":%q,"Type":"Movie"}`, mediaDownloadTestItem)
		case prefix + "/Items/" + mediaDownloadTestItem + "/PlaybackInfo":
			playbackCalls.Add(1)
			var values map[string]any
			if err := json.NewDecoder(r.Body).Decode(&values); err != nil || values["UserId"] != mediaDownloadTestUser || len(values) != 1 {
				t.Errorf("incorrect playback user or unexpected playback options: %v", values)
			}
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func mediaDownloadPendingTask(t *testing.T, kind MediaServerType) DbDownloadTask {
	t.Helper()
	var task DbDownloadTask
	if err := db.Db.Where("source = ? AND status = ?", DownloadSource(kind+"_media"), DownloadStatusPending).First(&task).Error; err != nil {
		t.Fatal(err)
	}
	return task
}

func TestMediaDownloadDeduplicationKeepsProductAndServerIdentity(t *testing.T) {
	setupMediaDownloadTestDB(t)
	emby := saveMediaDownloadConfig(t, MediaServerEmby, "http://emby.invalid", "key")
	jellyfin := saveMediaDownloadConfig(t, MediaServerJellyfin, "http://jellyfin.invalid", "key")
	for _, config := range []*EmbyConfig{emby, jellyfin} {
		if err := AddDownloadTaskFromMediaLibrary(config, "server-1", mediaDownloadTestItem, "movie"); err != nil {
			t.Fatal(err)
		}
	}
	if err := AddDownloadTaskFromMediaLibrary(emby, "server-1", mediaDownloadTestItem, "movie"); !errors.Is(err, ErrActiveDownloadTaskExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if err := AddDownloadTaskFromMediaLibrary(emby, "server-2", mediaDownloadTestItem, "new server"); err != nil {
		t.Fatal(err)
	}
	var tasks []DbDownloadTask
	db.Db.Find(&tasks)
	if len(tasks) != 3 || emby.ID != jellyfin.ID {
		t.Fatalf("same ID must not share queue target: configs=%d/%d tasks=%+v", emby.ID, jellyfin.ID, tasks)
	}
	for _, task := range tasks {
		if task.RemoteDownloadUrl != "" || task.MediaServerID == "" || task.EmbyItemId != mediaDownloadTestItem {
			t.Fatalf("new task must store identity without a credential URL: %+v", task)
		}
	}
}

func TestMediaDownloadUsesCurrentConnectionOnlyForOriginalServer(t *testing.T) {
	for _, serverID := range []string{"original", "replacement"} {
		t.Run(serverID, func(t *testing.T) {
			setupMediaDownloadTestDB(t)
			config := saveMediaDownloadConfig(t, MediaServerJellyfin, "http://old.invalid", "old-key")
			if err := AddDownloadTaskFromMediaLibrary(config, "original", mediaDownloadTestItem, "movie"); err != nil {
				t.Fatal(err)
			}
			task := mediaDownloadPendingTask(t, MediaServerJellyfin)
			var calls atomic.Int32
			server := mediaDownloadTestServer(t, MediaServerJellyfin, serverID, "new-key", 200,
				`{"MediaSources":[{"MediaStreams":[{"Type":"Video","Codec":"h264"}]}]}`, true, &calls)
			if err := UpdateMediaLibraryConfig(MediaServerJellyfin, map[string]any{"jellyfin_url": server.URL, "jellyfin_api_key": "new-key"}); err != nil {
				t.Fatal(err)
			}
			if serverID == "original" {
				current, _ := ReadMediaLibraryConfigSnapshot(MediaServerJellyfin)
				if err := AddDownloadTaskFromMediaLibrary(current, "original", mediaDownloadTestItem, "movie"); !errors.Is(err, ErrActiveDownloadTaskExists) {
					t.Fatalf("rotated URL/key must retain deduplication: %v", err)
				}
			}
			task.Download()
			wantStatus, wantCalls := DownloadStatusFailed, int32(0)
			if serverID == "original" {
				wantStatus, wantCalls = DownloadStatusCompleted, 1
			}
			if task.Status != wantStatus || calls.Load() != wantCalls || task.MediaServerID != "original" {
				t.Fatalf("status=%v calls=%d original=%s error=%s", task.Status, calls.Load(), task.MediaServerID, task.Error)
			}
		})
	}
}

func TestMediaDownloadErrorsAndMissingPlaybackPermissionNeverComplete(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		allow  bool
	}{
		{"unauthorized", 401, "", true}, {"forbidden", 403, "", true}, {"missing", 404, "", true}, {"server", 500, "", true},
		{"bad JSON", 200, "<html>error</html>", true}, {"no stream", 200, `{"MediaSources":[]}`, true},
		{"playback disabled", 200, `{"MediaSources":[]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupMediaDownloadTestDB(t)
			var calls atomic.Int32
			server := mediaDownloadTestServer(t, MediaServerEmby, "original", "key", tc.status, tc.body, tc.allow, &calls)
			config := saveMediaDownloadConfig(t, MediaServerEmby, server.URL, "key")
			if err := AddDownloadTaskFromMediaLibrary(config, "original", mediaDownloadTestItem, "movie"); err != nil {
				t.Fatal(err)
			}
			task := mediaDownloadPendingTask(t, MediaServerEmby)
			task.Download()
			if task.Status != DownloadStatusFailed || task.Error == "" || !tc.allow && calls.Load() != 0 {
				t.Fatalf("status=%v calls=%d error=%s", task.Status, calls.Load(), task.Error)
			}
		})
	}
}

func TestMediaDownloadDisabledEndpointKeepsPendingAndRetryCount(t *testing.T) {
	setupMediaDownloadTestDB(t)
	var calls atomic.Int32
	server := mediaDownloadTestServer(t, MediaServerEmby, "original", "key", 200,
		`{"MediaSources":[{"MediaStreams":[{"Type":"Video","Codec":"h264"}]}]}`, true, &calls)
	emby := saveMediaDownloadConfig(t, MediaServerEmby, server.URL, "key")
	jellyfin := saveMediaDownloadConfig(t, MediaServerJellyfin, "http://jellyfin.invalid", "key")
	for _, config := range []*EmbyConfig{emby, jellyfin} {
		if err := AddDownloadTaskFromMediaLibrary(config, "original", mediaDownloadTestItem, "movie"); err != nil {
			t.Fatal(err)
		}
	}
	embyTask := mediaDownloadPendingTask(t, MediaServerEmby)
	if err := UpdateMediaLibraryConfig(MediaServerEmby, map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	tasks := GetPendingDownloadTasks(10)
	if len(tasks) != 1 || tasks[0].Source != DownloadSourceJellyfinMedia {
		t.Fatalf("disabled Emby should not hide Jellyfin pending task: %+v", tasks)
	}
	embyTask.Downloading()
	embyTask.Download()
	if embyTask.Status != DownloadStatusPending || embyTask.RetryCount != 0 {
		t.Fatalf("disabled in-channel task: %+v", embyTask)
	}
	embyTask.Fail(errors.New("earlier error"))
	if err := RetryFailedDownloadTasks(1); err != nil {
		t.Fatal(err)
	}
	var fresh DbDownloadTask
	db.Db.First(&fresh, embyTask.ID)
	if fresh.Status != DownloadStatusFailed || fresh.RetryCount != 0 {
		t.Fatalf("disabled failed task must retain retry budget: %+v", fresh)
	}
	if err := UpdateMediaLibraryConfig(MediaServerEmby, map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	if err := RetryFailedDownloadTasks(1); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&fresh, embyTask.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Status != DownloadStatusPending || fresh.RetryCount != 1 || calls.Load() != 0 {
		t.Fatalf("reenabled task must regain one retry without executing: %+v calls=%d", fresh, calls.Load())
	}
	if tasks = GetPendingDownloadTasks(10); len(tasks) != 2 {
		t.Fatalf("both endpoints should be runnable after enabling: %+v", tasks)
	}
}

func TestDownloadQueueRetriesWithoutRemoteAuthorization(t *testing.T) {
	setupMediaDownloadTestDB(t)
	var remoteCalls atomic.Int32
	requested, release := make(chan struct{}, 1), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteCalls.Add(1)
		select {
		case requested <- struct{}{}:
		default:
		}
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		saveMediaDownloadConfig(t, kind, server.URL, "key")
	}
	tasks := []*DbDownloadTask{
		{Source: DownloadSourceEmbyMedia, SourceType: SourceTypeEmbyMedia, MediaServerID: "original", EmbyItemId: mediaDownloadTestItem},
		{Source: DownloadSourceJellyfinMedia, SourceType: SourceTypeJellyfinMedia, MediaServerID: "original", EmbyItemId: mediaDownloadTestItem},
		{Source: DownloadSourceLocalFile, SourceType: SourceTypeLocal, LocalSourcePath: "/synthetic/source", LocalFullPath: "/synthetic/target"},
	}
	expected := make(map[uint]*DbDownloadTask)
	for _, task := range tasks {
		task.Status, task.Error = DownloadStatusFailed, "earlier error"
		if err := createDownloadTaskWithDB(db.Db, task); err != nil {
			t.Fatal(err)
		}
		expected[task.ID] = task
	}
	dq := NewDq(1)
	dq.running = true
	done := make(chan struct{})
	go func() { defer close(done); dq.taskScheduler() }()
	t.Cleanup(func() {
		unblock()
		dq.mutex.Lock()
		dq.running = false
		dq.mutex.Unlock()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("download scheduler did not stop")
		}
	})
	queued := make(map[uint]bool)
	for range tasks {
		select {
		case task := <-dq.tasks:
			want := expected[task.ID]
			if want == nil || queued[task.ID] || task.RetryCount != 1 || task.MediaServerID != want.MediaServerID || task.Source != want.Source || task.SourceType != want.SourceType {
				t.Fatalf("retry lost its identity or budget: %+v", task)
			}
			queued[task.ID] = true
		case <-requested:
			t.Fatal("retry scheduling made a remote request and blocked the shared scheduler")
		case <-time.After(3 * time.Second):
			t.Fatal("retry scheduling did not advance both endpoints and local copy")
		}
	}
	if remoteCalls.Load() != 0 {
		t.Fatalf("retry scheduling made %d remote requests", remoteCalls.Load())
	}
	for _, task := range tasks {
		if !queued[task.ID] {
			t.Fatalf("task was not queued: %+v", task)
		}
	}
}

func TestLegacyMediaDownloadCannotBorrowChangedCredentials(t *testing.T) {
	setupMediaDownloadTestDB(t)
	config := saveMediaDownloadConfig(t, MediaServerEmby, "http://emby.invalid/base", "new-key")
	task := &DbDownloadTask{Source: DownloadSourceEmbyMedia, SourceType: SourceTypeEmbyMedia, EmbyItemId: mediaDownloadTestItem,
		RemoteDownloadUrl: config.EmbyUrl + "/emby/Items/" + mediaDownloadTestItem + "/PlaybackInfo?api_key=old-key"}
	if err := validateLegacyMediaDownload(task, config); err == nil {
		t.Fatal("legacy task cannot borrow new credentials")
	}
	config.EmbyApiKey = "old-key"
	if err := validateLegacyMediaDownload(task, config); err != nil {
		t.Fatalf("unchanged legacy connection: %v", err)
	}
	if task.MediaServerID != "" {
		t.Fatal("legacy task must not fabricate historical identity")
	}
}

func TestMediaDownloadDisabledDuringAuthorizationKeepsPending(t *testing.T) {
	setupMediaDownloadTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			fmt.Fprint(w, `{"Id":"original","ProductName":"Emby Server"}`)
		case "/emby/Users":
			if err := UpdateMediaLibraryConfig(MediaServerEmby, map[string]any{"enabled": false}); err != nil {
				t.Error(err)
			}
			fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true,"EnableMediaPlayback":true}}]`, mediaDownloadTestUser)
		case "/emby/Users/" + mediaDownloadTestUser + "/Items/" + mediaDownloadTestItem, "/emby/Items/" + mediaDownloadTestItem:
			fmt.Fprintf(w, `{"Id":%q,"Type":"Movie"}`, mediaDownloadTestItem)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	config := saveMediaDownloadConfig(t, MediaServerEmby, server.URL, "key")
	if err := AddDownloadTaskFromMediaLibrary(config, "original", mediaDownloadTestItem, "movie"); err != nil {
		t.Fatal(err)
	}
	task := mediaDownloadPendingTask(t, MediaServerEmby)
	task.Download()
	var fresh DbDownloadTask
	if err := db.Db.First(&fresh, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if fresh.Status != DownloadStatusPending || fresh.RetryCount != 0 {
		t.Fatalf("disabled during remote permission lookup must retain retry budget: %+v", fresh)
	}
}

func TestDownloadQueueConcurrentSchedulersClaimEachEndpointOnce(t *testing.T) {
	setupMediaDownloadTestDB(t)
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		config := saveMediaDownloadConfig(t, kind, "http://media.invalid", "key")
		if err := AddDownloadTaskFromMediaLibrary(config, "shared-server", mediaDownloadTestItem, "movie"); err != nil {
			t.Fatal(err)
		}
	}
	fetched := make(chan struct{}, 2)
	release := make(chan struct{})
	if err := db.Db.Callback().Query().After("gorm:query").Register("review:both_pending_snapshots", func(tx *gorm.DB) {
		if tx.Statement.Table != "db_download_tasks" {
			return
		}
		if _, ok := tx.Statement.Dest.(*[]*DbDownloadTask); !ok {
			return
		}
		fetched <- struct{}{}
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("review:both_pending_snapshots")
	dq := NewDq(1)
	dq.running = true
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); dq.moveTasksToChannel() }()
	go func() { defer wg.Done(); dq.moveTasksToChannel() }()
	for range 2 {
		select {
		case <-fetched:
		case <-time.After(3 * time.Second):
			close(release)
			wg.Wait()
			t.Fatal("did not fetch both pending snapshots")
		}
	}
	close(release)
	wg.Wait()
	var ids []uint
	for len(dq.tasks) > 0 {
		ids = append(ids, (<-dq.tasks).ID)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("each endpoint must be claimed once by overlapping schedulers: %v", ids)
	}
}

func TestMediaDownloadDisableDuringDetailKeepsPendingWithDamagedMapping(t *testing.T) {
	setupMediaDownloadTestDB(t)
	var playbackCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			fmt.Fprint(w, `{"Id":"original","ProductName":"Emby Server"}`)
		case "/emby/Users":
			fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true,"EnableMediaPlayback":true}}]`, mediaDownloadTestUser)
		case "/emby/Users/" + mediaDownloadTestUser + "/Items/" + mediaDownloadTestItem, "/emby/Items/" + mediaDownloadTestItem:
			if err := UpdateMediaLibraryConfig(MediaServerEmby, map[string]any{"enabled": false, "path_mappings_json": "{broken"}); err != nil {
				t.Error(err)
			}
			fmt.Fprintf(w, `{"Id":%q,"Type":"Movie"}`, mediaDownloadTestItem)
		default:
			playbackCalls.Add(1)
			t.Errorf("unexpected request: %s", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	config := saveMediaDownloadConfig(t, MediaServerEmby, server.URL, "key")
	if err := AddDownloadTaskFromMediaLibrary(config, "original", mediaDownloadTestItem, "movie"); err != nil {
		t.Fatal(err)
	}
	task := mediaDownloadPendingTask(t, MediaServerEmby)
	task.DownloadMediaLibraryInfo()
	if err := db.Db.First(&task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != DownloadStatusPending || task.RetryCount != 0 || playbackCalls.Load() != 0 {
		t.Fatalf("disabled task lost its pending state: %+v, playback calls=%d", task, playbackCalls.Load())
	}
}
