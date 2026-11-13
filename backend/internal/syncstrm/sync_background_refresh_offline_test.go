package syncstrm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestFullSyncDownstreamRetainsOfflineRefresh(t *testing.T) {
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		for _, targetType := range []models.EmbyRefreshTargetType{models.EmbyRefreshTargetTypeItem, models.EmbyRefreshTargetTypeLibrary} {
			for _, scenario := range []string{"recover", "replacement", "blank", "stale-index"} {
				t.Run(string(kind)+"/"+string(targetType)+"/"+scenario, func(t *testing.T) {
					oldDB, oldActive := db.Db, models.IsStrmSyncTaskActiveFunc
					t.Cleanup(func() { db.Db, models.IsStrmSyncTaskActiveFunc = oldDB, oldActive })
					_, sp := setupStrmGenerationServiceTestDB(t)
					sqlDB, err := db.Db.DB()
					if err != nil {
						t.Fatal(err)
					}
					sqlDB.SetMaxOpenConns(1)
					t.Cleanup(func() { sqlDB.Close() })
					if err := db.Db.AutoMigrate(&models.JellyfinConfig{}, &models.EmbyIndexState{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}); err != nil {
						t.Fatal(err)
					}
					models.IsStrmSyncTaskActiveFunc = func(uint) bool { return false }
					var offline atomic.Bool
					offline.Store(true)
					var refreshes atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if offline.Load() {
							http.Error(w, "offline", http.StatusServiceUnavailable)
							return
						}
						if strings.HasSuffix(r.URL.Path, "/System/Info/Public") {
							id := "original-server"
							if scenario == "replacement" {
								id = "replacement-server"
							}
							fmt.Fprintf(w, `{"Id":%q,"ProductName":%q}`, id, kind.DisplayName()+" Server")
							return
						}
						if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/Refresh") {
							refreshes.Add(1)
							w.WriteHeader(http.StatusNoContent)
							return
						}
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
					}))
					t.Cleanup(server.Close)
					config := models.EmbyConfig{Enabled: true, MediaServerType: kind, EmbyUrl: server.URL, EmbyApiKey: "key", EnableRefreshLibrary: 1}
					if kind == models.MediaServerJellyfin {
						c := models.JellyfinConfig(config)
						if err := db.Db.Create(&c).Error; err != nil {
							t.Fatal(err)
						}
					} else {
						if err := db.Db.Create(&config).Error; err != nil {
							t.Fatal(err)
						}
					}
					t.Cleanup(func() {
						if err := models.UpdateMediaLibraryConfig(kind, map[string]any{"enabled": false}); err != nil {
							t.Error(err)
						}
						models.ScheduleNextEmbyLibraryRefreshCheck()
					})
					const itemID = "11111111111111111111111111111111"
					const libraryID = "22222222222222222222222222222222"
					stateID := "original-server"
					if scenario == "stale-index" {
						stateID = "replacement-server"
					}
					if err := db.Db.Create(&models.EmbyIndexState{MediaServerType: kind, ServerID: stateID}).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Db.Create(&models.EmbyLibrarySyncPath{MediaServerType: kind, SyncPathId: sp.ID, LibraryId: libraryID}).Error; err != nil {
						t.Fatal(err)
					}
					if err := db.Db.Create(&models.EmbyMediaItem{MediaServerType: kind, ServerId: stateID, ItemId: itemID, Type: "Movie", LibraryId: libraryID}).Error; err != nil {
						t.Fatal(err)
					}
					target := models.EmbyRefreshTarget{MediaServerType: kind, ServerID: "original-server", TargetType: targetType, ItemID: itemID, FallbackLibraryId: libraryID}
					if scenario == "blank" {
						target.ServerID = ""
					}
					record := models.Sync{SyncPathId: sp.ID, NewMeta: 1, Logger: helpers.AppLogger}
					submitGenerationDownstream(context.Background(), &record, []models.EmbyRefreshTarget{target})
					var tasks []models.EmbyLibraryRefreshTask
					if err := db.Db.Find(&tasks).Error; err != nil {
						t.Fatal(err)
					}
					if scenario == "blank" || scenario == "stale-index" {
						if len(tasks) != 0 {
							t.Fatalf("unproven target queued: %+v", tasks)
						}
						return
					}
					if len(tasks) != 1 || tasks[0].ServerID != "original-server" || tasks[0].Status != models.EmbyLibraryRefreshStatusPending {
						t.Fatalf("full sync lost offline refresh: %+v", tasks)
					}
					task := tasks[0]
					if err := db.Db.Model(&task).Update("refresh_after_at", time.Now().Unix()-1).Error; err != nil {
						t.Fatal(err)
					}
					models.CheckPendingEmbyLibraryRefreshTasks()
					if err := db.Db.First(&task, task.ID).Error; err != nil {
						t.Fatal(err)
					}
					if task.Status != models.EmbyLibraryRefreshStatusPending || refreshes.Load() != 0 {
						t.Fatalf("offline execution consumed task: %+v refreshes=%d", task, refreshes.Load())
					}
					offline.Store(false)
					models.CheckPendingEmbyLibraryRefreshTasks()
					if err := db.Db.First(&task, task.ID).Error; err != nil {
						t.Fatal(err)
					}
					wantStatus := models.EmbyLibraryRefreshStatusCompleted
					wantRefreshes := int32(1)
					if scenario == "replacement" {
						wantStatus = models.EmbyLibraryRefreshStatusCancelled
						wantRefreshes = 0
					}
					if task.Status != wantStatus || task.ServerID != "original-server" || refreshes.Load() != wantRefreshes {
						t.Fatalf("recovery: task=%+v refreshes=%d", task, refreshes.Load())
					}
				})
			}
		}
	}
}
