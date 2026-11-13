package models

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
)

func TestMediaLibraryRefreshStopRetainsRemainingBatch(t *testing.T) {
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		for _, phase := range []string{"identity_failure", "refresh_success"} {
			t.Run(string(kind)+"/"+phase, func(t *testing.T) {
				oldDB, oldActive := db.Db, IsStrmSyncTaskActiveFunc
				t.Cleanup(func() { db.Db, IsStrmSyncTaskActiveFunc = oldDB, oldActive })
				setupEmbyRefreshTestDB(t)
				entered, release := make(chan struct{}), make(chan struct{})
				var releaseOnce sync.Once
				var offline atomic.Bool
				offline.Store(phase == "identity_failure")
				var identityCalls, refreshCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/System/Info/Public") {
						if identityCalls.Add(1) == 1 && phase == "identity_failure" {
							close(entered)
							<-release
						}
						if offline.Load() {
							http.Error(w, "offline", http.StatusServiceUnavailable)
							return
						}
						fmt.Fprintf(w, `{"Id":"original-server","ProductName":%q}`, kind.DisplayName()+" Server")
						return
					}
					if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Refresh") {
						if refreshCalls.Add(1) == 1 && phase == "refresh_success" {
							close(entered)
							<-release
						}
						w.WriteHeader(http.StatusNoContent)
						return
					}
					http.NotFound(w, r)
				}))
				t.Cleanup(server.Close)
				t.Cleanup(func() {
					releaseOnce.Do(func() { close(release) })
					StopEmbyLibraryRefreshCoordinator()
				})
				if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
				if kind == MediaServerJellyfin {
					if err := db.Db.Create(&JellyfinConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "key", EnableRefreshLibrary: 1}).Error; err != nil {
						t.Fatal(err)
					}
				} else if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Updates(map[string]any{"enabled": true, "emby_url": server.URL}).Error; err != nil {
					t.Fatal(err)
				}
				tasks := make([]*EmbyLibraryRefreshTask, 0, 2)
				for _, id := range []string{"11111111111111111111111111111111", "22222222222222222222222222222222"} {
					task := newPendingEmbyLibraryRefreshTask(id, id, []uint{10}, nowUnix()-20, EmbyRefreshTarget{MediaServerType: kind, ServerID: "original-server"})
					if err := db.Db.Create(task).Error; err != nil {
						t.Fatal(err)
					}
					tasks = append(tasks, task)
				}
				if err := db.Db.Model(tasks[0]).UpdateColumn("updated_at", nowUnix()-2).Error; err != nil {
					t.Fatal(err)
				}
				InitEmbyLibraryRefreshCoordinator()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("first refresh did not enter remote request")
				}
				embyRefreshCoordinatorState.Lock()
				stop := embyRefreshCoordinatorState.stop
				embyRefreshCoordinatorState.Unlock()
				stopped := make(chan struct{})
				go func() {
					StopEmbyLibraryRefreshCoordinator()
					close(stopped)
				}()
				select {
				case <-stop:
				case <-time.After(5 * time.Second):
					t.Fatal("stop signal was not closed")
				}
				releaseOnce.Do(func() { close(release) })
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
					t.Fatal("coordinator did not stop after in-flight request finished")
				}
				if (phase == "identity_failure" && identityCalls.Load() != 1) || (phase == "refresh_success" && refreshCalls.Load() != 1) {
					t.Fatalf("stop started subsequent batch requests: identity=%d refresh=%d", identityCalls.Load(), refreshCalls.Load())
				}
				if err := db.Db.First(tasks[1], tasks[1].ID).Error; err != nil {
					t.Fatal(err)
				}
				if tasks[1].Status != EmbyLibraryRefreshStatusPending || tasks[1].LastCheckedAt != 0 {
					t.Fatalf("stop consumed unstarted task: %+v", tasks[1])
				}
				offline.Store(false)
				InitEmbyLibraryRefreshCoordinator()
				TriggerEmbyLibraryRefreshCheck()
				deadline := time.After(5 * time.Second)
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for tasks[1].Status != EmbyLibraryRefreshStatusCompleted {
					select {
					case <-deadline:
						t.Fatal("restart did not execute remaining refresh")
					case <-ticker.C:
						if err := db.Db.First(tasks[1], tasks[1].ID).Error; err != nil {
							t.Fatal(err)
						}
					}
				}
				StopEmbyLibraryRefreshCoordinator()
				if refreshCalls.Load() != 2 {
					t.Fatalf("restart lost or repeated refresh: calls=%d", refreshCalls.Load())
				}
			})
		}
	}
}
