package models

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestInitEmbyConfig默认开启Webhook鉴权(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatalf("迁移 EmbyConfig 失败: %v", err)
	}

	InitEmbyConfig()

	var config EmbyConfig
	if err := db.Db.First(&config).Error; err != nil {
		t.Fatalf("查询 EmbyConfig 失败: %v", err)
	}
	if config.EnableAuth != 1 {
		t.Fatalf("EnableAuth = %d, want 1", config.EnableAuth)
	}
}

func TestEmbyConfig状态字段迁移默认值(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := db.Db.Exec(`
		CREATE TABLE emby_config (
			id integer primary key autoincrement,
			created_at integer,
			updated_at integer,
			emby_url text,
			emby_api_key text,
			sync_enabled integer,
			sync_cron text,
			last_sync_time integer
		)
	`).Error; err != nil {
		t.Fatalf("创建旧 emby_config 表失败: %v", err)
	}
	if err := db.Db.Exec(`
		INSERT INTO emby_config (created_at, updated_at, emby_url, emby_api_key, sync_enabled, sync_cron, last_sync_time)
		VALUES (1, 1, 'http://emby.local', 'key', 1, '0 * * * *', 123)
	`).Error; err != nil {
		t.Fatalf("插入旧 EmbyConfig 失败: %v", err)
	}

	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatalf("迁移 EmbyConfig 失败: %v", err)
	}

	var config EmbyConfig
	if err := db.Db.First(&config).Error; err != nil {
		t.Fatalf("查询 EmbyConfig 失败: %v", err)
	}
	if config.IsRunning {
		t.Fatal("IsRunning = true, want false")
	}
	if config.SyncMode != "" && config.SyncMode != EmbySyncModeIdle {
		t.Fatalf("SyncMode = %q, want empty or idle", config.SyncMode)
	}
	if config.StartedAt != 0 || config.LastFullSyncAt != 0 || config.LastIncrementalSyncAt != 0 || config.LastSavedCursorAt != 0 {
		t.Fatalf("状态时间字段默认值异常: %+v", config)
	}
	if config.LastProcessedCount != 0 || config.LastError != "" {
		t.Fatalf("状态结果字段默认值异常: %+v", config)
	}
}

func TestEmbySyncRunStateLifecycle(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatalf("迁移 EmbyConfig 失败: %v", err)
	}
	config := &EmbyConfig{Enabled: true, SyncEnabled: 1, SyncCron: "0 * * * *"}
	if err := db.Db.Create(config).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	started, err := StartEmbySyncRun(EmbySyncModeFull, 100)
	if err != nil {
		t.Fatalf("StartEmbySyncRun() error = %v", err)
	}
	if !started {
		t.Fatal("StartEmbySyncRun() started = false, want true")
	}

	started, err = StartEmbySyncRun(EmbySyncModeIncremental, 101)
	if err != nil {
		t.Fatalf("StartEmbySyncRun() second error = %v", err)
	}
	if started {
		t.Fatal("StartEmbySyncRun() second started = true, want false")
	}

	fresh, err := GetEmbyConfigFromDB()
	if err != nil {
		t.Fatalf("GetEmbyConfigFromDB() error = %v", err)
	}
	if !fresh.IsRunning || fresh.SyncMode != EmbySyncModeFull || fresh.StartedAt != 100 {
		t.Fatalf("运行中状态异常: %+v", fresh)
	}

	if err := FinishEmbySyncRun(EmbySyncModeFull, 42, 200, nil); err != nil {
		t.Fatalf("FinishEmbySyncRun() success error = %v", err)
	}
	fresh, err = GetEmbyConfigFromDB()
	if err != nil {
		t.Fatalf("GetEmbyConfigFromDB() after success error = %v", err)
	}
	if fresh.IsRunning || fresh.SyncMode != EmbySyncModeIdle || fresh.StartedAt != 0 {
		t.Fatalf("完成后运行状态异常: %+v", fresh)
	}
	if fresh.LastSyncTime != 200 || fresh.LastFullSyncAt != 200 || fresh.LastProcessedCount != 42 || fresh.LastError != "" || fresh.LastSuccessSyncMode != EmbySyncModeFull {
		t.Fatalf("完成后结果状态异常: %+v", fresh)
	}
}

func TestEmbySyncRunFailureState(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatalf("迁移 EmbyConfig 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyConfig{Enabled: true, SyncEnabled: 1, SyncCron: "0 * * * *"}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}
	if started, err := StartEmbySyncRun(EmbySyncModeIncremental, 300); err != nil || !started {
		t.Fatalf("StartEmbySyncRun() = %v, %v, want true, nil", started, err)
	}

	if err := FinishEmbySyncRun(EmbySyncModeIncremental, 7, 400, errTestEmbySyncFailure{}); err != nil {
		t.Fatalf("FinishEmbySyncRun() failure error = %v", err)
	}

	fresh, err := GetEmbyConfigFromDB()
	if err != nil {
		t.Fatalf("GetEmbyConfigFromDB() error = %v", err)
	}
	if fresh.IsRunning || fresh.SyncMode != EmbySyncModeIdle || fresh.StartedAt != 0 {
		t.Fatalf("失败后运行状态异常: %+v", fresh)
	}
	if fresh.LastSyncTime != 0 || fresh.LastIncrementalSyncAt != 0 {
		t.Fatalf("失败后不应推进同步时间: %+v", fresh)
	}
	if fresh.LastError != "同步失败" || fresh.LastProcessedCount != 7 {
		t.Fatalf("失败后错误状态异常: %+v", fresh)
	}
}

func TestResetStaleEmbySyncRunOnStartup清理遗留运行标记且不推进游标(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatalf("迁移 EmbyConfig 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyConfig{
		SyncEnabled:           1,
		SyncCron:              "0 * * * *",
		LastSyncTime:          1000,
		LastFullSyncAt:        900,
		LastIncrementalSyncAt: 800,
		LastSavedCursorAt:     700,
		LastProcessedCount:    6,
		IsRunning:             true,
		SyncMode:              EmbySyncModeIncremental,
		StartedAt:             600,
	}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	if err := ResetStaleEmbySyncRunOnStartup(); err != nil {
		t.Fatalf("ResetStaleEmbySyncRunOnStartup() error = %v", err)
	}

	fresh, err := GetEmbyConfigFromDB()
	if err != nil {
		t.Fatalf("GetEmbyConfigFromDB() error = %v", err)
	}
	if fresh.IsRunning || fresh.SyncMode != EmbySyncModeIdle || fresh.StartedAt != 0 {
		t.Fatalf("启动清理后运行状态异常: %+v", fresh)
	}
	if fresh.LastSyncTime != 1000 || fresh.LastFullSyncAt != 900 || fresh.LastIncrementalSyncAt != 800 || fresh.LastSavedCursorAt != 700 {
		t.Fatalf("启动清理不应推进或清空同步时间和游标: %+v", fresh)
	}
	if fresh.LastProcessedCount != 6 {
		t.Fatalf("LastProcessedCount = %d, want 6", fresh.LastProcessedCount)
	}
	if fresh.LastError == "" {
		t.Fatal("启动清理应记录 last_error")
	}
}

func TestFinishEmbyIncrementalSyncRun推进游标(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatalf("迁移 EmbyConfig 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyConfig{Enabled: true, SyncEnabled: 1, SyncCron: "0 * * * *"}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}
	if started, err := StartEmbySyncRun(EmbySyncModeIncremental, 500); err != nil || !started {
		t.Fatalf("StartEmbySyncRun() = %v, %v, want true, nil", started, err)
	}

	if err := FinishEmbyIncrementalSyncRun(9, 600, 550, nil); err != nil {
		t.Fatalf("FinishEmbyIncrementalSyncRun() error = %v", err)
	}

	fresh, err := GetEmbyConfigFromDB()
	if err != nil {
		t.Fatalf("GetEmbyConfigFromDB() error = %v", err)
	}
	if fresh.LastSyncTime != 600 || fresh.LastIncrementalSyncAt != 600 || fresh.LastSuccessSyncMode != EmbySyncModeIncremental {
		t.Fatalf("增量同步时间异常: %+v", fresh)
	}
	if fresh.LastSavedCursorAt != 550 {
		t.Fatalf("LastSavedCursorAt = %d, want 550", fresh.LastSavedCursorAt)
	}
}

func TestFinishEmbyIncrementalSyncRun失败不推进游标(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb

	if err := db.Db.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatalf("迁移 EmbyConfig 失败: %v", err)
	}
	if err := db.Db.Create(&EmbyConfig{Enabled: true, SyncEnabled: 1, SyncCron: "0 * * * *", LastSavedCursorAt: 123}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}
	if started, err := StartEmbySyncRun(EmbySyncModeIncremental, 500); err != nil || !started {
		t.Fatalf("StartEmbySyncRun() = %v, %v, want true, nil", started, err)
	}

	if err := FinishEmbyIncrementalSyncRun(3, 600, 550, errTestEmbySyncFailure{}); err != nil {
		t.Fatalf("FinishEmbyIncrementalSyncRun() error = %v", err)
	}

	fresh, err := GetEmbyConfigFromDB()
	if err != nil {
		t.Fatalf("GetEmbyConfigFromDB() error = %v", err)
	}
	if fresh.LastSavedCursorAt != 123 {
		t.Fatalf("LastSavedCursorAt = %d, want 123", fresh.LastSavedCursorAt)
	}
	if fresh.LastIncrementalSyncAt != 0 || fresh.LastSyncTime != 0 {
		t.Fatalf("失败后不应推进同步时间: %+v", fresh)
	}
}

type errTestEmbySyncFailure struct{}

func (errTestEmbySyncFailure) Error() string {
	return "同步失败"
}

func TestMediaLibraryConfigClientRejectsInvalidOwner(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	for _, kind := range []MediaServerType{"", "other"} {
		config := &EmbyConfig{MediaServerType: kind, EmbyUrl: server.URL, EmbyApiKey: "key"}
		if _, err := config.Client().CheckConnection(t.Context()); err == nil {
			t.Fatalf("invalid owner %q silently used Emby", kind)
		}
		if _, err := config.Client().GetServerID(t.Context()); err == nil {
			t.Fatalf("invalid owner %q queried a server identity", kind)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid owner reached a remote server")
	}
}

func TestMediaLibrarySyncRunIsolatedAndGated(t *testing.T) {
	connection, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	previous := db.Db
	db.Db = connection
	t.Cleanup(func() { db.Db = previous })
	if err := connection.AutoMigrate(&EmbyConfig{}, &JellyfinConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := connection.Create(&EmbyConfig{Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := connection.Create(&JellyfinConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	if started, err := StartMediaLibrarySyncRun(MediaServerJellyfin, EmbySyncModeFull, 100); err != nil || started {
		t.Fatalf("disabled service started: %v %v", started, err)
	}
	if started, err := StartMediaLibrarySyncRun(MediaServerEmby, EmbySyncModeFull, 101); err != nil || !started {
		t.Fatalf("enabled Emby did not start: %v %v", started, err)
	}
	if err := connection.Model(&JellyfinConfig{}).Where("id > 0").Updates(map[string]any{"enabled": true, "sync_enabled": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if started, err := StartMediaLibrarySyncRun(MediaServerJellyfin, EmbySyncModeFull, 102); err != nil || started {
		t.Fatalf("disabled sync started: %v %v", started, err)
	}
	if err := connection.Model(&JellyfinConfig{}).Where("id > 0").Update("sync_enabled", 1).Error; err != nil {
		t.Fatal(err)
	}
	if started, err := StartMediaLibrarySyncRun(MediaServerJellyfin, EmbySyncModeIncremental, 103); err != nil || !started {
		t.Fatalf("Emby running blocked independent Jellyfin: %v %v", started, err)
	}
	if err := FinishMediaLibrarySyncRun(MediaServerEmby, EmbySyncModeFull, 7, 200, errTestEmbySyncFailure{}); err != nil {
		t.Fatal(err)
	}
	embyConfig, err := ReadMediaLibraryConfigSnapshot(MediaServerEmby)
	if err != nil {
		t.Fatal(err)
	}
	jellyfin, err := ReadMediaLibraryConfigSnapshot(MediaServerJellyfin)
	if err != nil {
		t.Fatal(err)
	}
	if embyConfig.IsRunning || embyConfig.LastProcessedCount != 7 || embyConfig.LastError == "" || !jellyfin.IsRunning || jellyfin.StartedAt != 103 || jellyfin.SyncMode != EmbySyncModeIncremental || jellyfin.LastError != "" {
		t.Fatalf("run state crossed owners: Emby=%+v Jellyfin=%+v", embyConfig, jellyfin)
	}
}
