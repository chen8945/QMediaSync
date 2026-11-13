package emby

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestJellyfinSyncPreparationFailureReleasesOwnRuntime(t *testing.T) {
	for _, mode := range []string{"full", "incremental", "single"} {
		for _, fault := range []string{"selection", "connection", "disabled", "identity"} {
			t.Run(mode+"/"+fault, func(t *testing.T) {
				conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
				if err != nil {
					t.Fatal(err)
				}
				sqlDB, _ := conn.DB()
				t.Cleanup(func() { sqlDB.Close() })
				testJellyfinSyncPreparationFailure(t, conn, mode, fault)
			})
		}
	}
}

func testJellyfinSyncPreparationFailure(t *testing.T, conn *gorm.DB, mode, fault string) {
	t.Helper()
	previousDB, previousLogger := db.Db, helpers.AppLogger
	db.Db, helpers.AppLogger = conn, &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { db.Db, helpers.AppLogger = previousDB, previousLogger; jellyfinSyncRunning.Store(0) })
	setupSnapshotTestTables(t)
	if err := conn.Create(&models.EmbyConfig{IsRunning: true, SyncMode: models.EmbySyncModeFull, StartedAt: 123, LastError: "keep Emby state"}).Error; err != nil {
		t.Fatal(err)
	}
	var broken atomic.Bool
	broken.Store(true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			if broken.Load() {
				if fault == "identity" {
					http.Error(w, "offline", http.StatusServiceUnavailable)
					return
				}
				column, value := "selected_libraries", any(`["changed"]`)
				if fault == "connection" {
					column, value = "jellyfin_url", "http://127.0.0.1:1/changed"
				}
				if fault == "disabled" {
					column, value = "enabled", false
				}
				if err := conn.Table("jellyfin_config").Where("id > 0").Update(column, value).Error; err != nil {
					t.Error(err)
				}
			}
			fmt.Fprint(w, `{"Id":"jellyfin-test","ProductName":"Jellyfin Server"}`)
		case "/Users":
			fmt.Fprint(w, `[{"Id":"user","Policy":{"EnableAllFolders":true}}]`)
		case "/Library/MediaFolders":
			fmt.Fprintf(w, `{"Items":[{"Id":%q,"Name":"fixture"}]}`, jellyfinLibrary)
		case "/Library/VirtualFolders":
			fmt.Fprint(w, `[]`)
		case "/Items":
			fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	config := models.JellyfinConfig{MediaServerType: models.MediaServerJellyfin, Enabled: true, SyncEnabled: 1, EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncAllLibraries: 0, SelectedLibraries: `["` + jellyfinLibrary + `"]`, LastSavedCursorAt: 100, LastFullSyncAt: 50}
	if err := conn.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&config).Update("sync_all_libraries", 0).Error; err != nil {
		t.Fatal(err)
	}
	run := func() error {
		switch mode {
		case "full":
			_, err := PerformMediaLibrarySync(models.MediaServerJellyfin)
			return err
		case "incremental":
			_, err := PerformMediaLibraryIncrementalSync(models.MediaServerJellyfin)
			return err
		default:
			_, err := SyncMediaItemByIDContext(t.Context(), models.MediaServerJellyfin, jellyfinVersionA)
			return err
		}
	}
	if err := run(); err == nil {
		t.Fatal("preparation fault unexpectedly succeeded")
	}
	var emby models.EmbyConfig
	var jellyfin models.JellyfinConfig
	if err := conn.First(&emby).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.First(&jellyfin).Error; err != nil {
		t.Fatal(err)
	}
	if !emby.IsRunning || emby.SyncMode != models.EmbySyncModeFull || emby.StartedAt != 123 || emby.LastError != "keep Emby state" || jellyfin.IsRunning || jellyfin.SyncMode != models.EmbySyncModeIdle || jellyfin.LastError == "" || jellyfin.LastSavedCursorAt != 100 || jellyfin.LastFullSyncAt != 50 {
		t.Fatalf("wrong runtime finalized: Emby=%+v Jellyfin=%+v", emby, jellyfin)
	}
	broken.Store(false)
	if err := conn.Table("jellyfin_config").Where("id = ?", config.ID).Updates(map[string]any{"selected_libraries": `["` + jellyfinLibrary + `"]`, "jellyfin_url": server.URL, "enabled": true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := run(); err != nil {
		t.Fatalf("sync did not recover: %v", err)
	}
	if err := conn.First(&jellyfin).Error; err != nil {
		t.Fatal(err)
	}
	if jellyfin.IsRunning || jellyfin.LastError != "" {
		t.Fatalf("subsequent sync remained stuck: %+v", jellyfin)
	}
}
