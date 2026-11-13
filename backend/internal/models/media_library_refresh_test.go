package models

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestMediaLibraryRefreshScopes(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	testMediaLibraryRefreshScopes(t, db.Db)
}

func TestMediaLibraryRefreshSubmissionSkipsUnavailableConfig(t *testing.T) {
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		for _, state := range []string{"unconfigured", "disabled", "refresh-disabled", "missing-url", "missing-key"} {
			t.Run(string(kind)+"/"+state, func(t *testing.T) {
				setupEmbyRefreshTestDB(t)
				if kind == MediaServerJellyfin {
					if err := db.Db.Create(&JellyfinConfig{Enabled: true, EmbyUrl: "http://unused.invalid", EmbyApiKey: "fixture", EnableRefreshLibrary: 1}).Error; err != nil {
						t.Fatal(err)
					}
				}
				query := db.Db.Table(mediaLibraryConfigTable(kind)).Where("id > 0")
				var err error
				switch state {
				case "unconfigured":
					err = query.Delete(&EmbyConfig{}).Error
				case "disabled":
					err = query.Update("enabled", false).Error
				case "refresh-disabled":
					err = query.Update("enable_refresh_library", 0).Error
				case "missing-url":
					err = query.Update(string(kind)+"_url", "").Error
				case "missing-key":
					err = query.Update(string(kind)+"_api_key", "").Error
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := RequestMediaLibraryRefreshTargets(10, []EmbyRefreshTarget{{MediaServerType: kind, ServerID: "original-server", TargetType: EmbyRefreshTargetTypeLibrary, FallbackLibraryId: "library"}}); err != nil {
					t.Fatalf("unavailable refresh config should be skipped: %v", err)
				}
				var count int64
				if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("disabled/unconfigured endpoint received refresh: count=%d err=%v", count, err)
				}
			})
		}
	}
}

func TestMediaLibraryRefreshOfflineIdentityRequiresMatchingConnection(t *testing.T) {
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		t.Run(string(kind), func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			if err := db.Db.AutoMigrate(&JellyfinConfig{}, &EmbyIndexState{}); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "offline", http.StatusServiceUnavailable) }))
			t.Cleanup(server.Close)
			if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("enabled", false).Error; err != nil {
				t.Fatal(err)
			}
			var row any = &EmbyConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "key", EnableRefreshLibrary: 1}
			if kind == MediaServerJellyfin {
				row = &JellyfinConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "key", EnableRefreshLibrary: 1}
			} else {
				if err := db.Db.Where("id > 0").Delete(&EmbyConfig{}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := db.Db.Create(row).Error; err != nil {
				t.Fatal(err)
			}
			config, err := ReadMediaLibraryConfigSnapshot(kind)
			if err != nil {
				t.Fatal(err)
			}
			state := EmbyIndexState{MediaServerType: kind, ServerID: "original-server", ServerConfigKey: EmbyServerConfigIdentity(config)}
			if err := db.Db.Create(&state).Error; err != nil {
				t.Fatal(err)
			}
			targets, err := ResolveMediaLibraryRefreshTargets(nil)
			if err != nil || len(targets) != 1 || targets[0].MediaServerType != kind || targets[0].ServerID != "original-server" {
				t.Fatalf("lost known offline identity: %+v err=%v", targets, err)
			}
			if err := db.Db.Model(&state).Update("server_config_key", "previous-connection").Error; err != nil {
				t.Fatal(err)
			}
			targets, err = ResolveMediaLibraryRefreshTargets(nil)
			if err == nil || len(targets) != 0 {
				t.Fatalf("adopted old connection identity: %+v err=%v", targets, err)
			}
		})
	}
}

func testMediaLibraryRefreshScopes(t *testing.T, conn *gorm.DB) {
	t.Helper()
	if err := conn.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}, &EmbyIndexState{}, &EmbyMediaItem{}, &EmbyMediaSyncFile{}, &EmbyLibrarySyncPath{}, &EmbyLibraryRefreshTask{}); err != nil {
		t.Fatal(err)
	}
	now := nowUnix()
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		if err := conn.Create(&EmbyLibrarySyncPath{MediaServerType: kind, SyncPathId: 10, LibraryId: "shared-library", LibraryName: string(kind)}).Error; err != nil {
			t.Fatal(err)
		}
		if err := conn.Create(&EmbyMediaItem{MediaServerType: kind, ServerId: "same-server-id", ItemId: "shared-item", Type: "Movie", LibraryId: "shared-library"}).Error; err != nil {
			t.Fatal(err)
		}
		target := EmbyRefreshTarget{MediaServerType: kind, ServerID: "same-server-id", TargetType: EmbyRefreshTargetTypeItem, ItemID: "shared-item"}
		if err := upsertEmbyItemRefreshTaskWithDB(conn, target, 10, now); err != nil {
			t.Fatal(err)
		}
		if err := upsertEmbyLibraryRefreshTaskWithDB(conn, "shared-library", string(kind), 10, now, target); err != nil {
			t.Fatal(err)
		}
		var regrouped bool
		if err := reconcilePendingEmbyRefreshTasksWithDB(conn, now, &regrouped, target); err != nil {
			t.Fatal(err)
		}
	}
	old := EmbyRefreshTarget{MediaServerType: MediaServerEmby, ServerID: "old-instance", TargetType: EmbyRefreshTargetTypeItem, ItemID: "shared-item"}
	if err := upsertEmbyItemRefreshTaskWithDB(conn, old, 10, now); err != nil {
		t.Fatal(err)
	}
	var tasks []EmbyLibraryRefreshTask
	if err := conn.Order("id").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 5 {
		t.Fatalf("tasks=%d, want distinct kind and instance identities", len(tasks))
	}
	for _, task := range tasks {
		if task.ServerID == "old-instance" {
			if task.Status != EmbyLibraryRefreshStatusPending {
				t.Fatal("new instance absorbed the original pending task")
			}
			continue
		}
		if task.TargetType == EmbyLibraryRefreshTargetTypeLibrary && task.LibraryName != string(task.MediaServerType) {
			t.Fatalf("library name crossed endpoints: %+v", task)
		}
		if task.TargetType == EmbyLibraryRefreshTargetTypeItem && task.Status != EmbyLibraryRefreshStatusCancelled {
			t.Fatal("same endpoint library did not absorb its item")
		}
	}
}

func TestMediaLibraryRefreshExecutesOtherEndpointWhileOneIsSlow(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	const libraryID = "11111111111111111111111111111111"
	entered, release, jellyfinDone := make(chan struct{}), make(chan struct{}), make(chan struct{}, 2)
	var calls atomic.Int32
	embyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/System/Info/Public") {
			fmt.Fprint(w, `{"Id":"original-server","ProductName":"Emby Server"}`)
			return
		}
		if r.Method == http.MethodPost {
			calls.Add(1)
			close(entered)
			<-release
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(embyServer.Close)
	jellyfinServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/System/Info/Public") {
			fmt.Fprint(w, `{"Id":"original-server","ProductName":"Jellyfin Server"}`)
			return
		}
		if r.Method == http.MethodPost {
			jellyfinDone <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(jellyfinServer.Close)
	if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("emby_url", embyServer.URL).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&JellyfinConfig{Enabled: true, EmbyUrl: jellyfinServer.URL, EmbyApiKey: "test-key", EnableRefreshLibrary: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		task := newPendingEmbyLibraryRefreshTask(libraryID, string(kind), []uint{10}, nowUnix()-20, EmbyRefreshTarget{MediaServerType: kind, ServerID: "original-server"})
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatal(err)
		}
	}
	InitEmbyLibraryRefreshCoordinator()
	t.Cleanup(StopEmbyLibraryRefreshCoordinator)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("slow endpoint did not start")
	}
	select {
	case <-jellyfinDone:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("slow Emby endpoint blocked Jellyfin")
	}
	next := newPendingEmbyLibraryRefreshTask("22222222222222222222222222222222", "new Jellyfin event", []uint{10}, nowUnix()-20, EmbyRefreshTarget{MediaServerType: MediaServerJellyfin, ServerID: "original-server"})
	if err := db.Db.Create(next).Error; err != nil {
		close(release)
		t.Fatal(err)
	}
	TriggerEmbyLibraryRefreshCheck()
	select {
	case <-jellyfinDone:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("new Jellyfin event waited for slow Emby")
	}
	close(release)
	StopEmbyLibraryRefreshCoordinator()
	if calls.Load() != 1 {
		t.Fatal("same endpoint executed concurrently")
	}
}

func TestMediaLibraryRefreshRetainsDisabledAndRejectsChangedIdentity(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	task := newPendingEmbyLibraryRefreshTask("lib", "library", []uint{10}, nowUnix()-20, EmbyRefreshTarget{MediaServerType: MediaServerEmby, ServerID: "old-instance"})
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	CheckPendingEmbyLibraryRefreshTasks()
	assertNoScheduledEmbyRefreshCheck(t)
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != EmbyLibraryRefreshStatusPending || task.LastCheckedAt != 0 {
		t.Fatal("disabled endpoint consumed pending work")
	}
	if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	CheckPendingEmbyLibraryRefreshTasks()
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != EmbyLibraryRefreshStatusCancelled || !strings.Contains(task.Error, "服务器已变更") {
		t.Fatalf("changed instance accepted: %+v", task)
	}
	legacy := newPendingEmbyLibraryRefreshTask("legacy", "legacy", []uint{10}, nowUnix()-20)
	if err := db.Db.Create(legacy).Error; err != nil {
		t.Fatal(err)
	}
	CheckPendingEmbyLibraryRefreshTasks()
	if err := db.Db.First(legacy, legacy.ID).Error; err != nil {
		t.Fatal(err)
	}
	if legacy.Status != EmbyLibraryRefreshStatusCancelled || legacy.ServerID != "" {
		t.Fatal("unproven legacy identity was rebound")
	}
}

func TestMediaLibraryRefreshRetriesWithCurrentConnectionForSameServer(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/System/Info/Public") {
			fmt.Fprint(w, `{"Id":"refresh-test","ProductName":"Emby Server"}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Query().Get("api_key") != "rotated-key" {
			http.Error(w, "wrong current credentials", http.StatusUnauthorized)
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	task := newPendingEmbyLibraryRefreshTask("lib", "library", []uint{10}, nowUnix()-20, EmbyRefreshTarget{MediaServerType: MediaServerEmby, ServerID: "refresh-test"})
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Updates(map[string]any{"emby_url": server.URL, "emby_api_key": "rotated-key"}).Error; err != nil {
		t.Fatal(err)
	}
	CheckPendingEmbyLibraryRefreshTasks()
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || task.Status != EmbyLibraryRefreshStatusCompleted || task.ServerID != "refresh-test" {
		t.Fatalf("same server current connection rejected: calls=%d task=%+v", calls.Load(), task)
	}
}

func TestMediaLibraryRefreshDisableDuringBatchRetainsRemainingWork(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/System/Info/Public") {
			fmt.Fprint(w, `{"Id":"refresh-test","ProductName":"Emby Server"}`)
			return
		}
		if r.Method == http.MethodPost {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("emby_url", server.URL).Error; err != nil {
		t.Fatal(err)
	}
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{MediaServerType: MediaServerEmby, ServerID: "refresh-test", ItemID: "101"}, 10, nowUnix()-20)
	task.SetItemIds([]string{"101", "102"})
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- refreshEmbyLibraryTask(task) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("batch did not start")
	}
	if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Updates(map[string]any{"enabled": false, "path_mappings_json": "{broken"}).Error; err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || task.Status != EmbyLibraryRefreshStatusPending || task.Error != "" {
		t.Fatalf("disabled batch consumed work: calls=%d task=%+v", calls.Load(), task)
	}
}

func TestMediaLibraryRefreshRejectsStaleIndexedTargets(t *testing.T) {
	for _, targetKind := range []string{"item", "sibling", "library"} {
		t.Run(targetKind, func(t *testing.T) {
			setupEmbyRefreshTestDB(t)
			var refreshes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					refreshes.Add(1)
				}
				fmt.Fprint(w, `{"Id":"server-b","ProductName":"Emby Server"}`)
			}))
			t.Cleanup(server.Close)
			if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("emby_url", server.URL).Error; err != nil {
				t.Fatal(err)
			}
			sf := createEmbyRefreshSyncFile(t, 10, "/remote/movie", "movie.mkv", "new-item")
			if err := db.Db.Create(&EmbyIndexState{MediaServerType: MediaServerEmby, ServerID: "server-a"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Create(&EmbyLibrarySyncPath{MediaServerType: MediaServerEmby, SyncPathId: 10, LibraryId: "old-library"}).Error; err != nil {
				t.Fatal(err)
			}
			if targetKind != "library" {
				relationFile := sf
				if targetKind == "sibling" {
					relationFile = createEmbyRefreshSyncFile(t, 10, sf.Path, "old.mkv", "old-sibling")
				}
				item := EmbyMediaItem{MediaServerType: MediaServerEmby, ServerId: "server-a", ItemId: "old-item", Type: "Episode", SeasonId: "old-season", LibraryId: "old-library"}
				if err := db.Db.Create(&item).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Create(&EmbyMediaSyncFile{MediaServerType: MediaServerEmby, SyncPathId: 10, SyncFileId: relationFile.ID, EmbyItemId: item.ItemId}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				sf.LocalFilePath = ""
			}
			targets, err := ResolveMediaLibraryRefreshTargets(sf)
			if err == nil || len(targets) != 0 {
				t.Fatalf("stale index was rebound: targets=%+v err=%v", targets, err)
			}
			target, err := ResolveMediaLibraryRefreshTarget(MediaServerEmby, sf)
			if err != nil {
				t.Fatal(err)
			}
			if targetKind != "library" && target.ServerID != "server-a" {
				t.Fatalf("original item identity lost: %+v", target)
			}
			submitErr := RequestMediaLibraryRefreshTargets(10, []EmbyRefreshTarget{target})
			if targetKind == "library" && submitErr == nil {
				t.Fatal("unproven library target was accepted")
			}
			if targetKind != "library" && submitErr != nil {
				t.Fatalf("original target was not retained: %v", submitErr)
			}
			var count int64
			if err := db.Db.Model(&EmbyLibraryRefreshTask{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if targetKind == "library" {
				if count != 0 {
					t.Fatalf("created %d tasks for unproven library", count)
				}
				return
			}
			var task EmbyLibraryRefreshTask
			if count != 1 || db.Db.First(&task).Error != nil || task.ServerID != "server-a" {
				t.Fatalf("lost original target: count=%d task=%+v", count, task)
			}
			if err := db.Db.Model(&task).Update("refresh_after_at", nowUnix()-1).Error; err != nil {
				t.Fatal(err)
			}
			CheckPendingEmbyLibraryRefreshTasks()
			if err := db.Db.First(&task, task.ID).Error; err != nil {
				t.Fatal(err)
			}
			if task.Status != EmbyLibraryRefreshStatusCancelled || task.ServerID != "server-a" || refreshes.Load() != 0 {
				t.Fatalf("stale target reached replacement server: %+v refreshes=%d", task, refreshes.Load())
			}
		})
	}
}

func TestMediaLibraryRefreshPreservesOriginalFallbackAfterIndexSwitch(t *testing.T) {
	setupEmbyRefreshTestDB(t)
	if err := db.Db.Create(&EmbyIndexState{MediaServerType: MediaServerEmby, ServerID: "server-b"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&EmbyLibrarySyncPath{MediaServerType: MediaServerEmby, SyncPathId: 10, LibraryId: "new-library"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&EmbyMediaItem{MediaServerType: MediaServerEmby, ServerId: "server-b", ItemId: "shared-item", LibraryId: "new-library"}).Error; err != nil {
		t.Fatal(err)
	}
	task := newPendingEmbyItemRefreshTask(EmbyRefreshTarget{MediaServerType: MediaServerEmby, ServerID: "server-a", ItemID: "shared-item", FallbackLibraryId: "old-library", FallbackLibraryName: "old library"}, 10, nowUnix())
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	if err := reconcilePendingEmbyRefreshTasks(nowUnix()); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(task, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.ServerID != "server-a" || task.FallbackLibraryId != "old-library" || task.FallbackLibraryName != "old library" {
		t.Fatalf("original fallback was replaced: %+v", task)
	}
}
