package emby

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestPerformEmbyFullSyncAdvancesIncrementalCursorWithOverlap(t *testing.T) {
	previousDB, previousLogger, previousConfig := db.Db, helpers.AppLogger, models.GlobalEmbyConfig
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	db.Db = conn
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.GlobalEmbyConfig = nil
	SetEmbySyncRunning(false)
	t.Cleanup(func() {
		SetEmbySyncRunning(false)
		db.Db, helpers.AppLogger, models.GlobalEmbyConfig = previousDB, previousLogger, previousConfig
		_ = sqlDB.Close()
	})
	setupSnapshotTestTables(t)
	var scanStarted atomic.Int64
	queries := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/emby/System/Info/Public":
			fmt.Fprint(w, `{"Id":"server-a"}`)
		case "/emby/Users":
			fmt.Fprint(w, `[{"Id":"u","Policy":{"EnableAllFolders":true}}]`)
		case "/emby/Library/MediaFolders":
			fmt.Fprint(w, `{"Items":[{"Id":"lib","Name":"library"}]}`)
		case "/emby/Items":
			cursor := r.URL.Query().Get("MinDateLastSaved")
			if cursor == "" {
				var running models.EmbyConfig
				if err := conn.First(&running).Error; err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				scanStarted.Store(running.StartedAt)
			}
			queries <- cursor
			fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	config := models.EmbyConfig{
		EmbyUrl: server.URL, EmbyApiKey: "fixture", SyncEnabled: 1,
		LastSavedCursorAt: 100, LastIncrementalSyncAt: 50,
	}
	if err := conn.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	seedCursor := func(t *testing.T) {
		t.Helper()
		current, err := models.ReadEmbyConfigSnapshot()
		if err != nil {
			t.Fatal(err)
		}
		// 历史水位属于当前选库身份，不能混入切换配置时的合法重置。
		if _, err := models.BeginEmbyIndexRead("server-a", current); err != nil {
			t.Fatal(err)
		}
		if err := conn.Model(current).Updates(map[string]any{
			"last_saved_cursor_at":     100,
			"last_incremental_sync_at": 50,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var fresh models.EmbyConfig
	for _, test := range []struct {
		name      string
		selection string
		queries   int
	}{
		{name: "empty-selection", selection: `[]`},
		{name: "selected-library-missing", selection: `["missing"]`},
		{name: "some-selected-libraries-missing", selection: `["lib","missing"]`, queries: 1},
	} {
		if !t.Run(test.name, func(t *testing.T) {
			if err := conn.Model(&config).Updates(map[string]any{
				"sync_all_libraries": 0,
				"selected_libraries": test.selection,
			}).Error; err != nil {
				t.Fatal(err)
			}
			seedCursor(t)
			if count, err := PerformEmbySync(); err != nil || count != 0 {
				t.Fatalf("incomplete selection full sync: count=%d err=%v", count, err)
			}
			if err := conn.First(&fresh).Error; err != nil {
				t.Fatal(err)
			}
			if len(queries) != test.queries || fresh.LastSavedCursorAt != 100 || fresh.LastIncrementalSyncAt != 50 {
				t.Fatalf("未完整扫描选中媒体库却推进游标或改变扫描范围：queries=%d config=%+v", len(queries), fresh)
			}
			if test.queries > 0 && <-queries != "" {
				t.Fatal("全量扫描意外添加增量过滤")
			}
		}) {
			return
		}
	}
	if err := conn.Model(&config).Update("sync_all_libraries", 1).Error; err != nil {
		t.Fatal(err)
	}
	seedCursor(t)
	if count, err := PerformEmbySync(); err != nil || count != 0 {
		t.Fatalf("full sync: count=%d err=%v", count, err)
	}
	if cursor := <-queries; cursor != "" {
		t.Fatalf("全量扫描意外添加增量过滤：%q", cursor)
	}
	if err := conn.First(&fresh).Error; err != nil {
		t.Fatal(err)
	}
	if scanStarted.Load() == 0 || fresh.LastSavedCursorAt != scanStarted.Load() || fresh.LastFullSyncAt == 0 || fresh.LastIncrementalSyncAt != 50 {
		t.Fatalf("全量未保存扫描起点或误写增量完成时间：started=%d config=%+v", scanStarted.Load(), fresh)
	}
	if count, err := PerformEmbyIncrementalSync(); err != nil || count != 0 {
		t.Fatalf("incremental sync: count=%d err=%v", count, err)
	}
	want := time.Unix(scanStarted.Load()-600, 0).UTC().Format(time.RFC3339)
	if cursor := <-queries; cursor != want {
		t.Fatalf("后续增量未保留十分钟重叠：cursor=%q want=%q", cursor, want)
	}
}

func TestStartParseEmbyMediaInfoRunsOneAtATime(t *testing.T) {
	oldLogger, oldConfig := helpers.AppLogger, models.GlobalEmbyConfig
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	// 首轮扫描阻塞在读取媒体库，模拟提取仍在后台运行。
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusInternalServerError)
	}))
	models.GlobalEmbyConfig = &models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "key"}
	waitIdle := func() bool {
		deadline := time.Now().Add(5 * time.Second)
		for embyMediaInfoRunning.Load() == 1 {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(time.Millisecond)
		}
		return true
	}
	t.Cleanup(func() {
		releaseServer()
		waitIdle()
		server.Close()
		helpers.AppLogger, models.GlobalEmbyConfig = oldLogger, oldConfig
	})

	if !StartParseEmbyMediaInfo() {
		t.Fatal("首次提取应启动")
	}
	if StartParseEmbyMediaInfo() {
		t.Fatal("提取运行期间不能再启动一轮")
	}
	releaseServer()
	if !waitIdle() {
		t.Fatal("后台提取结束后没有释放运行标记")
	}
}
