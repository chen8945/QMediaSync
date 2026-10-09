package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestUpdateEmbyConfigPathMappingsCompatibilityAndClear(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	r.GET("/emby/config", GetEmbyConfig)
	config := models.EmbyConfig{EmbyUrl: "http://emby.local", EmbyApiKey: "key", SyncEnabled: 1,
		SyncCron: "0 * * * *", PathMappingsJSON: `[{"emby_root":"/emby","local_root":"/mnt/strm"}]`}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, field string
		count       int
	}{
		{"omitted", "", 1},
		{"null", `,"path_mappings":null`, 1},
		{"empty clears", `,"path_mappings":[]`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"emby_url":"http://emby.local","emby_api_key":"key","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1` + test.field + `}`
			response := requestEmbyConfig(t, r, body)
			if response.Code != Success {
				t.Fatalf("save failed: %+v", response)
			}
			stored, err := models.ReadEmbyConfigSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if len(stored.PathMappings) != test.count {
				t.Fatalf("mappings = %+v", stored.PathMappings)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/emby/config", nil))
			if !strings.Contains(w.Body.String(), `"path_mappings":[`) || strings.Contains(w.Body.String(), "path_mappings_json") {
				t.Fatalf("GET contract: %s", w.Body.String())
			}
		})
	}
}

func TestUpdateEmbyConfigPathMappingsValidationAndPersistence(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	if err := db.Db.AutoMigrate(&models.SyncPath{}, &models.Account{}); err != nil {
		t.Fatal(err)
	}
	accounts := []models.Account{
		{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115, UserId: "mapping-owner"},
		{BaseModel: models.BaseModel{ID: 2}, SourceType: models.SourceTypeBaiduPan, UserId: "other-provider"},
		{BaseModel: models.BaseModel{ID: 3}, SourceType: models.SourceType115},
	}
	if err := db.Db.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	syncPath := models.SyncPath{BaseModel: models.BaseModel{ID: 9}, BaseCid: "9", SourceType: models.SourceType115,
		LocalPath: "/qms", RemotePath: "/Movies", AccountId: 1}
	if err := db.Db.Create(&syncPath).Error; err != nil {
		t.Fatal(err)
	}
	config := models.EmbyConfig{EmbyUrl: "http://emby.local", SyncEnabled: 1, SyncCron: "0 * * * *", LastFullSyncAt: 20}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	response := requestEmbyConfig(t, r, `{"emby_url":"http://emby.local","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1,"path_mappings":[{"emby_root":"/external/","local_root":"/mounted/","sync_path_id":9}]}`)
	if response.Code != Success {
		t.Fatalf("save failed: %+v", response)
	}
	stored, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.PathMappings) != 1 || stored.PathMappings[0].EmbyRoot != "/external" || stored.PathMappings[0].LocalRoot != "/mounted" || stored.PathMappings[0].SyncPathID != 9 {
		t.Fatalf("mapped config = %+v", stored.PathMappings)
	}
	if stored.LastFullSyncAt != 0 {
		t.Fatalf("mapping changed without resetting watermark: %d", stored.LastFullSyncAt)
	}
	if models.EmbyServerConfigIdentity(&config) != models.EmbyServerConfigIdentity(stored) {
		t.Fatal("mapping changed connection identity")
	}
	for _, test := range []struct {
		name, mappings string
		source         models.SourceType
		accountID      uint
	}{
		{"unknown root", `[{"emby_root":"/other","local_root":"/mounted","sync_path_id":99}]`, models.SourceType115, 1},
		{"unsupported root", `[{"emby_root":"/other","local_root":"/mounted","sync_path_id":9}]`, models.SourceTypeLocal, 1},
		{"overlap", `[{"emby_root":"/other","local_root":"/mounted"},{"emby_root":"/other/sub","local_root":"/mounted"}]`, models.SourceType115, 1},
		{"missing account", `[{"emby_root":"/other","local_root":"/mounted","sync_path_id":9}]`, models.SourceType115, 99},
		{"wrong account provider", `[{"emby_root":"/other","local_root":"/mounted","sync_path_id":9}]`, models.SourceType115, 2},
		{"missing account identity", `[{"emby_root":"/other","local_root":"/mounted","sync_path_id":9}]`, models.SourceType115, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := db.Db.Model(&syncPath).Updates(map[string]any{"source_type": test.source, "account_id": test.accountID}).Error; err != nil {
				t.Fatal(err)
			}
			response := requestEmbyConfig(t, r, `{"emby_url":"http://changed.local","path_mappings":`+test.mappings+`}`)
			if response.Code != BadRequest {
				t.Fatalf("accepted invalid mapping: %+v", response)
			}
			fresh, err := models.ReadEmbyConfigSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			if fresh.EmbyUrl != stored.EmbyUrl || fresh.PathMappingsJSON != stored.PathMappingsJSON {
				t.Fatal("rejected save changed config")
			}
			if models.GlobalEmbyConfig.EmbyUrl != stored.EmbyUrl || models.GlobalEmbyConfig.PathMappingsJSON != stored.PathMappingsJSON {
				t.Fatal("rejected save changed runtime config")
			}
		})
	}
}

func TestUpdateEmbyConfigMappingWriteFailureAndConcurrentState(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	config := models.EmbyConfig{EmbyUrl: "http://emby.local", SyncEnabled: 1, SyncCron: "0 * * * *",
		PathMappingsJSON: `[{"emby_root":"/emby","local_root":"/mnt/old"}]`}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := models.GetEmbyConfigFromDB(); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:reject_mapping_write", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_config" {
			tx.AddError(errors.New("injected mapping write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	response := requestEmbyConfig(t, r, `{"emby_url":"http://emby.local","path_mappings":[]}`)
	if response.Code != BadRequest {
		t.Fatalf("failed write returned success: %+v", response)
	}
	if err := db.Db.Callback().Update().Remove("test:reject_mapping_write"); err != nil {
		t.Fatal(err)
	}
	stored, err := models.ReadEmbyConfigSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.PathMappingsJSON != config.PathMappingsJSON {
		t.Fatal("failed write lost mappings")
	}
	if models.GlobalEmbyConfig.PathMappingsJSON != config.PathMappingsJSON {
		t.Fatal("failed write changed runtime mappings")
	}
	// 同步状态更新不拥有路径配置，不能由空类型化数组触发清空。
	if err := db.Db.Model(&models.EmbyConfig{}).Where("id = ?", config.ID).Updates(map[string]any{"is_running": true, "last_saved_cursor_at": 99}).Error; err != nil {
		t.Fatal(err)
	}
	response = requestEmbyConfig(t, r, `{"emby_url":"http://emby.local","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1}`)
	if response.Code != Success {
		t.Fatalf("save failed: %+v", response)
	}
	stored, err = models.ReadEmbyConfigSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stored.PathMappingsJSON != config.PathMappingsJSON || !stored.IsRunning || stored.LastSavedCursorAt != 99 {
		t.Fatalf("lost independent state: %+v", stored)
	}
}

func TestUpdateEmbyConfigMappingLookupAndDecodeFailure(t *testing.T) {
	r := setupEmbyConfigControllerTest(t)
	config := models.EmbyConfig{EmbyUrl: "http://emby.local", PathMappingsJSON: `[]`}
	if err := db.Db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	response := requestEmbyConfig(t, r, `{"emby_url":"http://changed.local","path_mappings":[{"emby_root":"/emby","local_root":"/mounted","sync_path_id":99}]}`)
	if response.Code != BadRequest || !strings.Contains(response.Message, "读取路径映射") {
		t.Fatalf("lookup failure hidden: %+v", response)
	}
	if err := db.Db.Model(&config).Update("path_mappings_json", "{corrupt").Error; err != nil {
		t.Fatal(err)
	}
	response = requestEmbyConfig(t, r, `{"emby_url":"http://changed.local","path_mappings":[]}`)
	if response.Code != BadRequest || !strings.Contains(response.Message, "读取 Emby 路径映射失败") {
		t.Fatalf("decode failure hidden: %+v", response)
	}
	var persistedURL string
	if err := db.Db.Model(&models.EmbyConfig{}).Select("emby_url").Row().Scan(&persistedURL); err != nil {
		t.Fatal(err)
	}
	if persistedURL != config.EmbyUrl {
		t.Fatal("failed read overwrote configuration")
	}
}

func requestEmbyConfig(t *testing.T, router *gin.Engine, body string) APIResponse[json.RawMessage] {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/emby/config", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, request)
	var response APIResponse[json.RawMessage]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response
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
