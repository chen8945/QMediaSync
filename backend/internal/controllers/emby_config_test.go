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
