package emby

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func setupMediaInfoTestDB(t *testing.T) {
	t.Helper()
	oldDB, oldLogger := db.Db, helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := conn.AutoMigrate(&models.EmbyConfig{}, &models.JellyfinConfig{}, &models.DbDownloadTask{}); err != nil {
		t.Fatal(err)
	}
	db.Db = conn
	t.Cleanup(func() { db.Db, helpers.AppLogger = oldDB, oldLogger; sqlDB.Close() })
}

func TestStartParseEmbyMediaInfoRunsOneAtATime(t *testing.T) {
	setupMediaInfoTestDB(t)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	}))
	config := models.EmbyConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "key"}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	jellyfin := models.JellyfinConfig(models.EmbyConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "key"})
	if err := db.Db.Create(&jellyfin).Error; err != nil {
		t.Fatal(err)
	}
	waitIdle := func() bool {
		deadline := time.Now().Add(5 * time.Second)
		for embyMediaInfoRunning.Load() == 1 || jellyfinMediaInfoRunning.Load() == 1 {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(time.Millisecond)
		}
		return true
	}
	t.Cleanup(func() { releaseServer(); waitIdle(); server.Close() })
	if !StartParseEmbyMediaInfo() {
		t.Fatal("first Emby scan must start even with automatic extraction off")
	}
	if StartParseEmbyMediaInfo() {
		t.Fatal("same endpoint cannot start overlapping scan")
	}
	if !StartParseMediaLibraryInfo(models.MediaServerJellyfin) {
		t.Fatal("slow Emby scan must not block Jellyfin scan")
	}
	if StartParseMediaLibraryInfo(models.MediaServerJellyfin) {
		t.Fatal("same Jellyfin endpoint cannot overlap")
	}
	releaseServer()
	if !waitIdle() {
		t.Fatal("running markers not released")
	}
	if err := models.UpdateMediaLibraryConfig(models.MediaServerEmby, map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	if StartParseEmbyMediaInfo() {
		t.Fatal("disabled endpoint must not start manual scan")
	}
}

func TestCollectMediaLibraryInfoUsesMainStreamAndPreservesManualScope(t *testing.T) {
	setupMediaInfoTestDB(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Info/Public":
			fmt.Fprint(w, `{"Id":"original","ProductName":"Emby Server"}`)
		case "/emby/Users":
			fmt.Fprint(w, `[{"Id":"reader","Policy":{"EnableAllFolders":true,"EnableMediaPlayback":false}},{"Id":"player","Policy":{"EnableAllFolders":true,"EnableMediaPlayback":true}}]`)
		case "/emby/Library/MediaFolders":
			fmt.Fprint(w, `{"Items":[{"Id":"first","Name":"first"},{"Id":"second","Name":"second"}]}`)
		case "/emby/Users/player/Items":
			if r.URL.Query().Get("Fields") != "MediaStreams" {
				t.Error("scan must request main stream fields")
			}
			id := "101"
			if r.URL.Query().Get("ParentId") == "second" {
				id = "201"
			}
			fmt.Fprintf(w, `{"Items":[{"Id":%q,"Name":"missing","Type":"Movie"},{"Id":"102","Name":"silent video","Type":"Movie","MediaStreams":[{"Type":"Video","Codec":"h264"}]},{"Id":"103","Name":"subtitles only","Type":"Movie","MediaStreams":[{"Type":"Subtitle","Codec":"srt"}]}],"TotalRecordCount":3}`, id)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	config := &models.EmbyConfig{MediaServerType: models.MediaServerEmby, Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "key", SyncAllLibraries: 0, SelectedLibraries: `["first"]`}
	if err := db.Db.Create(config).Error; err != nil {
		t.Fatal(err)
	}
	count, err := collectMediaLibraryInfo(context.Background(), config)
	if err != nil || count != 3 {
		t.Fatalf("count=%d error=%v", count, err)
	}
	var tasks []models.DbDownloadTask
	if err := db.Db.Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	foundSecond := false
	for _, task := range tasks {
		if task.EmbyItemId == "102" {
			t.Fatal("silent video with main stream must not be extracted again")
		}
		if task.EmbyItemId == "201" {
			foundSecond = true
		}
		if task.MediaServerID != "original" || task.RemoteDownloadUrl != "" {
			t.Fatalf("identity=%+v", task)
		}
	}
	if !foundSecond {
		t.Fatal("manual extraction must retain all-libraries scope")
	}
}
