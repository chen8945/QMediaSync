package controllers

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func setupEmbyConfigControllerTest(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.GlobalEmbyConfig = nil
	setupControllerTestDB(t, &models.EmbyConfig{})

	r := gin.New()
	r.PUT("/emby/config", UpdateEmbyConfig)
	return r
}

func TestGetEmbyLibraries连接失败保留原因且隐藏密钥(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	r.GET("/emby/libraries", GetEmbyLibraries)
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close()
	models.GlobalEmbyConfig = &models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "private-emby-key"}
	t.Cleanup(func() { models.GlobalEmbyConfig = nil })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/emby/libraries", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "查询 Emby 媒体库失败") || strings.Contains(w.Body.String(), "private-emby-key") {
		t.Fatalf("应返回不含密钥的连接失败：%s", w.Body.String())
	}
}

func TestUpdateEmbyConfig省略每日首次全量同步字段时保留现有配置(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	if err := db.Db.Create(&models.EmbyConfig{
		EmbyUrl:                  "http://emby.local",
		EmbyApiKey:               "api-key",
		SyncEnabled:              1,
		SyncCron:                 "0 * * * *",
		EnableDailyFirstFullSync: 1,
	}).Error; err != nil {
		t.Fatalf("创建 EmbyConfig 失败: %v", err)
	}

	body := bytes.NewBufferString(`{
		"emby_url": "http://emby.local",
		"emby_api_key": "api-key",
		"sync_enabled": 1,
		"sync_cron": "0 * * * *",
		"sync_all_libraries": 1
	}`)
	req := httptest.NewRequest(http.MethodPut, "/emby/config", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d, body=%s", w.Code, w.Body.String())
	}
	var config models.EmbyConfig
	if err := db.Db.First(&config).Error; err != nil {
		t.Fatalf("查询 EmbyConfig 失败: %v", err)
	}
	if config.EnableDailyFirstFullSync != 1 {
		t.Fatalf("EnableDailyFirstFullSync = %d, want 1", config.EnableDailyFirstFullSync)
	}
}

func TestUpdateEmbyConfigDoesNotOverwriteConcurrentSyncState(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	config := models.EmbyConfig{EmbyUrl: "http://emby.local", EmbyApiKey: "key", SyncEnabled: 1, SyncCron: "0 * * * *", LastSavedCursorAt: 10}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	models.GlobalEmbyConfig = &config
	triggered := false
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:advance_emby_cursor", func(tx *gorm.DB) {
		if triggered || tx.Statement.Table != "emby_config" {
			return
		}
		triggered = true
		if err := tx.Session(&gorm.Session{NewDB: true}).Model(&models.EmbyConfig{}).Where("id = ?", config.ID).Updates(map[string]any{"last_saved_cursor_at": 99, "is_running": true, "sync_mode": models.EmbySyncModeIncremental}).Error; err != nil {
			t.Error(err)
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Update().Remove("test:advance_emby_cursor")
	body := bytes.NewBufferString(`{"emby_url":"http://emby.local","emby_api_key":"key","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1}`)
	request := httptest.NewRequest(http.MethodPut, "/emby/config", body)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var fresh models.EmbyConfig
	db.Db.First(&fresh)
	if !triggered || fresh.LastSavedCursorAt != 99 || !fresh.IsRunning || fresh.SyncMode != models.EmbySyncModeIncremental {
		t.Fatalf("settings overwrote concurrent state: %+v", fresh)
	}
}

func TestUpdateEmbyConfigScopeChangeResetsSuccessfulWatermarks(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	config := models.EmbyConfig{EmbyUrl: "http://old-emby.local", EmbyApiKey: "key", SyncEnabled: 1, SyncCron: "0 * * * *", LastSavedCursorAt: 10, LastFullSyncAt: 20, LastSyncTime: 20}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/emby/config", bytes.NewBufferString(`{"emby_url":"http://new-emby.local","emby_api_key":"key","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	var fresh models.EmbyConfig
	db.Db.First(&fresh)
	if fresh.LastSavedCursorAt != 0 || fresh.LastFullSyncAt != 0 || fresh.LastSyncTime != 0 {
		t.Fatalf("new server retained old watermarks: %+v", fresh)
	}
}
