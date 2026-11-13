package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	embypreload "qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func setupEmbyConfigControllerTest(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	previousLogger := helpers.AppLogger
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	setupControllerTestDB(t, &models.EmbyConfig{}, &models.JellyfinConfig{})

	r := gin.New()
	r.PUT("/emby/config", UpdateEmbyConfig)
	r.PUT("/jellyfin/config", UpdateJellyfinConfig)
	r.GET("/jellyfin/config", GetJellyfinConfig)
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
			response := requestMediaLibraryConfig(t, r, models.MediaServerEmby, body)
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
	response := requestMediaLibraryConfig(t, r, models.MediaServerEmby, `{"emby_url":"http://emby.local","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1,"path_mappings":[{"emby_root":"/external/","local_root":"/mounted/","sync_path_id":9}]}`)
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
			response := requestMediaLibraryConfig(t, r, models.MediaServerEmby, `{"emby_url":"http://changed.local","path_mappings":`+test.mappings+`}`)
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
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:reject_mapping_write", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_config" {
			tx.AddError(errors.New("injected mapping write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	response := requestMediaLibraryConfig(t, r, models.MediaServerEmby, `{"emby_url":"http://emby.local","path_mappings":[]}`)
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
	// 同步状态更新不拥有路径配置，不能由空类型化数组触发清空。
	if err := db.Db.Model(&models.EmbyConfig{}).Where("id = ?", config.ID).Updates(map[string]any{"is_running": true, "last_saved_cursor_at": 99}).Error; err != nil {
		t.Fatal(err)
	}
	response = requestMediaLibraryConfig(t, r, models.MediaServerEmby, `{"emby_url":"http://emby.local","sync_enabled":1,"sync_cron":"0 * * * *","sync_all_libraries":1}`)
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
	response := requestMediaLibraryConfig(t, r, models.MediaServerEmby, `{"emby_url":"http://changed.local","path_mappings":[{"emby_root":"/emby","local_root":"/mounted","sync_path_id":99}]}`)
	if response.Code != BadRequest || !strings.Contains(response.Message, "读取路径映射") {
		t.Fatalf("lookup failure hidden: %+v", response)
	}
	if err := db.Db.Model(&config).Update("path_mappings_json", "{corrupt").Error; err != nil {
		t.Fatal(err)
	}
	response = requestMediaLibraryConfig(t, r, models.MediaServerEmby, `{"emby_url":"http://changed.local","path_mappings":[]}`)
	if response.Code != BadRequest || !strings.Contains(response.Message, "读取媒体库路径映射失败") {
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

func requestMediaLibraryConfig(t *testing.T, router *gin.Engine, kind models.MediaServerType, body string) APIResponse[json.RawMessage] {
	t.Helper()
	request := httptest.NewRequest(http.MethodPut, "/"+string(kind)+"/config", strings.NewReader(body))
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
	if err := db.Db.Create(&models.EmbyConfig{Enabled: true, EmbyUrl: server.URL, EmbyApiKey: "private-emby-key"}).Error; err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/emby/libraries", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "查询媒体库失败") || strings.Contains(w.Body.String(), "private-emby-key") {
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

func TestMediaLibraryConfigSaveIsolatedAndExplicitZeros(t *testing.T) {
	router := setupEmbyConfigControllerTest(t)
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		body := fmt.Sprintf(`{"%s_url":"http://offline.invalid/%s","%s_api_key":"%s-key","enabled":false,"enable_media_notification":0,"enable_auth":0,"sync_enabled":0,"sync_all_libraries":0,"enable_daily_first_full_sync":0}`, kind, kind, kind, kind)
		response := requestMediaLibraryConfig(t, router, kind, body)
		if response.Code != Success {
			t.Fatalf("%s save failed: %+v", kind, response)
		}
		config, err := models.ReadMediaLibraryConfigSnapshot(kind)
		if err != nil {
			t.Fatal(err)
		}
		if config.MediaServerType != kind || config.ID != 1 || config.Enabled || config.EnableMediaNotification != 0 || config.EnableAuth != 0 || config.SyncEnabled != 0 || config.SyncAllLibraries != 0 || config.EnableDailyFirstFullSync != 0 {
			t.Fatalf("%s explicit zero or ownership changed: %+v", kind, config)
		}
		if config.CreatedAt <= 0 || config.UpdatedAt <= 0 || config.SyncMode != models.EmbySyncModeIdle || config.SelectedLibraries != "[]" {
			t.Fatalf("%s new configuration lost defaults: %+v", kind, config)
		}
	}
	for _, body := range []string{
		`{"jellyfin_url":"http://offline.invalid/jellyfin","jellyfin_api_key":"jellyfin-key","enabled":true,"enable_media_notification":1}`,
		`{"jellyfin_url":"http://offline.invalid/jellyfin","jellyfin_api_key":"jellyfin-key"}`,
		`{"jellyfin_url":"http://offline.invalid/jellyfin","jellyfin_api_key":"jellyfin-key","enabled":null,"enable_media_notification":null}`,
	} {
		if response := requestMediaLibraryConfig(t, router, models.MediaServerJellyfin, body); response.Code != Success {
			t.Fatalf("offline save failed: %+v", response)
		}
		config, err := models.ReadMediaLibraryConfigSnapshot(models.MediaServerJellyfin)
		if err != nil || !config.Enabled || config.EnableMediaNotification != 1 {
			t.Fatalf("omitted enabled/notification lost saved values: %+v %v", config, err)
		}
	}
	if response := requestMediaLibraryConfig(t, router, models.MediaServerJellyfin, `{"jellyfin_url":"http://offline.invalid/jellyfin","enabled":false,"enable_media_notification":0}`); response.Code != Success {
		t.Fatal(response)
	}
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		config, err := models.ReadMediaLibraryConfigSnapshot(kind)
		if err != nil || config.Enabled || config.EnableMediaNotification != 0 || config.EmbyUrl != "http://offline.invalid/"+string(kind) {
			t.Fatalf("%s explicit disable changed other configuration: %+v %v", kind, config, err)
		}
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/jellyfin/config", nil))
	if !strings.Contains(response.Body.String(), `"jellyfin_url"`) || strings.Contains(response.Body.String(), `"emby_url"`) {
		t.Fatalf("Jellyfin response fields: %s", response.Body)
	}
}

func TestMediaLibraryConfigSavedWarningCanRetrySameValues(t *testing.T) {
	router := setupEmbyConfigControllerTest(t)
	if err := db.Db.Create(&models.JellyfinConfig{EmbyUrl: "http://offline.invalid", EmbyApiKey: "old-key"}).Error; err != nil {
		t.Fatal(err)
	}
	saved := false
	if err := db.Db.Callback().Update().After("gorm:after_update").Register("test:mark_config_saved", func(tx *gorm.DB) {
		if tx.Statement.Table == "jellyfin_config" {
			saved = true
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Update().Remove("test:mark_config_saved") })
	if err := db.Db.Callback().Query().Before("gorm:query").Register("test:fail_config_apply", func(tx *gorm.DB) {
		if saved && tx.Statement.Table == "jellyfin_config" {
			tx.AddError(errors.New("injected post-commit reload failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove("test:fail_config_apply") })
	body := `{"jellyfin_url":"http://offline.invalid","jellyfin_api_key":"new-key","enabled":true,"sync_enabled":1}`
	response := requestMediaLibraryConfig(t, router, models.MediaServerJellyfin, body)
	var result struct {
		Warnings        []string `json:"warnings"`
		RestartRequired bool     `json:"restart_required"`
	}
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != Success || len(result.Warnings) != 1 || result.RestartRequired {
		t.Fatalf("committed save reported failure or lost application warning: %+v %+v", response, result)
	}
	if err := db.Db.Callback().Query().Remove("test:fail_config_apply"); err != nil {
		t.Fatal(err)
	}
	config, err := models.ReadMediaLibraryConfigSnapshot(models.MediaServerJellyfin)
	if err != nil || !config.Enabled || config.EmbyApiKey != "new-key" {
		t.Fatalf("application failure rolled back saved values: %+v %v", config, err)
	}
	response = requestMediaLibraryConfig(t, router, models.MediaServerJellyfin, body)
	if err := json.Unmarshal(response.Data, &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != Success || len(result.Warnings) != 0 || result.RestartRequired {
		t.Fatalf("same-value application retry failed: %+v %+v", response, result)
	}
}

func TestMediaLibraryLibrariesCanConfigureDisabledService(t *testing.T) {
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		t.Run(string(kind), func(t *testing.T) {
			router := setupEmbyConfigControllerTest(t)
			if err := db.Db.AutoMigrate(&models.EmbyLibrary{}, &models.EmbyLibrarySyncPath{}); err != nil {
				t.Fatal(err)
			}
			id := "101"
			if kind == models.MediaServerJellyfin {
				id = "11111111111111111111111111111111"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/System/Ping" {
					if kind == models.MediaServerJellyfin {
						fmt.Fprint(w, `"Jellyfin Server"`)
					} else {
						fmt.Fprint(w, "Emby Server")
					}
					return
				}
				path := "/emby/Library/MediaFolders"
				if kind == models.MediaServerJellyfin {
					path = "/Library/MediaFolders"
				}
				if r.URL.Path != path {
					t.Errorf("unexpected configuration lookup: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				fmt.Fprintf(w, `{"Items":[{"Id":%q,"Name":"Movies"}]}`, id)
			}))
			defer server.Close()
			var config any = &models.EmbyConfig{EmbyUrl: server.URL, EmbyApiKey: "key"}
			if kind == models.MediaServerJellyfin {
				config = &models.JellyfinConfig{EmbyUrl: server.URL, EmbyApiKey: "key"}
			}
			if err := db.Db.Create(config).Error; err != nil {
				t.Fatal(err)
			}
			endpoint := "/" + string(kind) + "/libraries"
			if kind == models.MediaServerJellyfin {
				router.GET(endpoint, GetJellyfinLibraries)
			} else {
				router.GET(endpoint, GetEmbyLibraries)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, endpoint, nil))
			var result APIResponse[[]models.EmbyLibrary]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Code != Success || len(result.Data) != 1 || result.Data[0].LibraryId != id {
				t.Fatalf("disabled service cannot configure libraries: %s", response.Body)
			}
			stored, err := models.ReadMediaLibraryConfigSnapshot(kind)
			if err != nil || stored.Enabled {
				t.Fatalf("lookup enabled the service: %+v %v", stored, err)
			}
		})
	}
}

func TestMediaLibrarySyncStatusDoesNotHideCountFailure(t *testing.T) {
	router := setupEmbyConfigControllerTest(t)
	if err := db.Db.Create(&models.EmbyConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	router.GET("/emby/status", GetEmbySyncStatus)
	for _, available := range []bool{false, true} {
		if available {
			if err := db.Db.AutoMigrate(&models.EmbyMediaItem{}); err != nil {
				t.Fatal(err)
			}
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/emby/status", nil))
		var result APIResponse[json.RawMessage]
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if !available && (result.Code != BadRequest || !strings.Contains(result.Message, "读取媒体库条目数量失败")) || available && result.Code != Success {
			t.Fatalf("count availability=%v: %s", available, response.Body)
		}
	}
}

func TestMediaLibraryDisableCancelsOnlyCommittedEmbyPreloads(t *testing.T) {
	for _, test := range []struct {
		name string
		kind models.MediaServerType
		fail bool
	}{
		{"Emby disabled", models.MediaServerEmby, false},
		{"Jellyfin disabled", models.MediaServerJellyfin, false},
		{"failed Emby save", models.MediaServerEmby, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := setupEmbyConfigControllerTest(t)
			if err := db.Db.Create(&models.EmbyConfig{Enabled: true, EmbyUrl: "http://offline.invalid", EmbyApiKey: "key"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Create(&models.JellyfinConfig{Enabled: true, EmbyUrl: "http://offline.invalid", EmbyApiKey: "key"}).Error; err != nil {
				t.Fatal(err)
			}
			if test.fail {
				if err := db.Db.Callback().Update().Before("gorm:update").Register("test:reject_preload_disable", func(tx *gorm.DB) {
					if tx.Statement.Table == "emby_config" {
						tx.AddError(errors.New("injected config failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			previous := playback.DefaultPreloader
			preloader := playback.NewPreloader()
			playback.DefaultPreloader = preloader
			defer func() { preloader.Stop(); playback.DefaultPreloader = previous }()
			preloader.Start(t.Context())
			preloader.Apply(true, 1)
			stopPreloading := embypreload.ConfigurePreloading(embypreload.PreloadOptions{Instance: "emby302", Coordinator: preloader})
			defer stopPreloading()
			started, completed := make(chan string, 2), make(chan string, 2)
			release := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			for index, instance := range []string{"emby302", "fnos:independent"} {
				if !preloader.Submit(playback.PreloadCandidate{Key: instance, Scope: instance, Instance: instance, Kind: playback.PreloadNext,
					Account: playback.PreloadAccount{Provider: "115", AccountID: uint(index + 1), UserID: instance},
					Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						started <- instance
						select {
						case <-release:
						case <-ctx.Done():
						}
						state := "valid"
						if ctx.Err() != nil || !task.Valid() {
							state = "canceled"
						}
						completed <- instance + ":" + state
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}}) {
					t.Fatal("preload was not admitted")
				}
			}
			for range 2 {
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("preloads did not start")
				}
			}
			body := fmt.Sprintf(`{"%s_url":"http://offline.invalid","%s_api_key":"key","enabled":false}`, test.kind, test.kind)
			response := requestMediaLibraryConfig(t, router, test.kind, body)
			if (response.Code == Success) == test.fail {
				t.Fatalf("unexpected save: %+v", response)
			}
			close(release)
			for range 2 {
				select {
				case result := <-completed:
					want := "valid"
					if strings.HasPrefix(result, "emby302:") && test.kind == models.MediaServerEmby && !test.fail {
						want = "canceled"
					}
					if !strings.HasSuffix(result, ":"+want) {
						t.Fatalf("preload after save: %s, want %s", result, want)
					}
				case <-time.After(time.Second):
					t.Fatal("preloads did not finish")
				}
			}
		})
	}
}

func TestMediaLibraryConfigSavesKeepCommitOrderWithinEachService(t *testing.T) {
	router := setupEmbyConfigControllerTest(t)
	if err := db.Db.Create(&models.EmbyConfig{Enabled: true, EmbyUrl: "http://offline.invalid", EmbyApiKey: "key"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&models.JellyfinConfig{}).Error; err != nil {
		t.Fatal(err)
	}
	firstCommitted, releaseFirst := make(chan struct{}), make(chan struct{})
	var blockFirst atomic.Bool
	var releaseOnce sync.Once
	var saves sync.WaitGroup
	release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
	defer func() { release(); saves.Wait() }()
	if err := db.Db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("test:pause_committed_emby_save", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_config" && tx.Error == nil && blockFirst.CompareAndSwap(false, true) {
			close(firstCommitted)
			<-releaseFirst
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Update().Remove("test:pause_committed_emby_save") })
	submit := func(kind models.MediaServerType, enabled bool, done chan<- *httptest.ResponseRecorder) {
		defer saves.Done()
		body := fmt.Sprintf(`{"%s_url":"http://offline.invalid","%s_api_key":"key","enabled":%t}`, kind, kind, enabled)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/"+string(kind)+"/config", strings.NewReader(body)))
		done <- response
	}
	waitResponse := func(done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
		t.Helper()
		select {
		case response := <-done:
			return response
		case <-time.After(time.Second):
			release()
			t.Fatal("configuration save did not finish")
			return nil
		}
	}
	firstDone, secondDone, jellyfinDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
	saves.Add(1)
	go submit(models.MediaServerEmby, false, firstDone)
	select {
	case <-firstCommitted:
	case <-time.After(time.Second):
		t.Fatal("first save did not reach its commit boundary")
	}
	saves.Add(2)
	go submit(models.MediaServerEmby, true, secondDone)
	go submit(models.MediaServerJellyfin, true, jellyfinDone)
	responses := []*httptest.ResponseRecorder{waitResponse(jellyfinDone)}
	var overtaking *httptest.ResponseRecorder
	select {
	case overtaking = <-secondDone:
	case <-time.After(100 * time.Millisecond):
	}
	release()
	responses = append(responses, waitResponse(firstDone))
	if overtaking == nil {
		responses = append(responses, waitResponse(secondDone))
	} else {
		responses = append(responses, overtaking)
	}
	for _, response := range responses {
		var result APIResponse[json.RawMessage]
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Code != Success {
			t.Fatalf("configuration save failed: %s (%v)", response.Body, err)
		}
	}
	if overtaking != nil {
		t.Fatal("a later Emby save overtook the first committed save and its runtime publication")
	}
	config, err := models.ReadMediaLibraryConfigSnapshot(models.MediaServerEmby)
	if err != nil || !config.Enabled {
		t.Fatalf("last committed Emby value was lost: %+v %v", config, err)
	}
}
