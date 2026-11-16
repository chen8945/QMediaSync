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
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
)

func TestRequiredRequestFieldsUseDomainValidation(t *testing.T) {
	previousLogger := helpers.AppLogger
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	user, _ := setupAPIKeyControllerTest(t)
	if err := db.Db.AutoMigrate(&models.SyncPath{}); err != nil {
		t.Fatal(err)
	}
	apiKey, _, err := models.CreateAPIKey(user.ID, "existing")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(func(c *gin.Context) { SetCurrentUser(c, user, authMethodSession) })
	endpoints := []struct {
		name, method, path string
		handler            gin.HandlerFunc
		values             map[string]any
		fields             []string
		nested             bool
	}{
		{"log", http.MethodPost, "/setting/log", UpdateLogSetting, map[string]any{"level": "info"}, []string{"level"}, false},
		{"cron-next", http.MethodGet, "/setting/cron", GetCronNextTime, map[string]any{"cron": "0 * * * *"}, []string{"cron"}, false},
		{"cron-validate", http.MethodPost, "/cron/validate", ValidateCron, map[string]any{"cron_expression": "0 * * * *"}, []string{"cron_expression"}, false},
		{"strm", http.MethodPost, "/setting/strm-config", UpdateStrmConfig, map[string]any{"strm_base_url": "http://qms.test", "cron": "0 * * * *", "add_path": 3}, []string{"strm_base_url", "cron"}, false},
		{"queue-limit", http.MethodPost, "/115/queue/rate-limit", SetQueueRateLimit, map[string]any{"qps": 4, "qpm": 120, "qph": 1000}, []string{"qps", "qpm", "qph"}, false},
		{"stats-clean", http.MethodPost, "/115/stats/clean", CleanOldRequestStats, map[string]any{"days": 7}, []string{"days"}, false},
		{"api-key-create", http.MethodPost, "/api-keys", CreateAPIKey, map[string]any{"name": "new"}, []string{"name"}, false},
		{"api-key-status", http.MethodPut, "/api-keys/:id/status", UpdateAPIKeyStatus, map[string]any{"is_active": true}, []string{"is_active"}, false},
		{"sync-create", http.MethodPost, "/sync/paths", CreateSyncPathAggregate, map[string]any{"source_type": "local", "base_cid": "root", "local_path": "/local", "remote_path": "/remote"}, []string{"source_type", "base_cid", "local_path", "remote_path"}, true},
		{"tmdb", http.MethodGet, "/scrape/tmdb-search", TmdbSearch, map[string]any{"type": "movie", "name": "fixture"}, []string{"type"}, false},
	}
	for _, endpoint := range endpoints {
		router.Handle(endpoint.method, endpoint.path, endpoint.handler)
	}
	assertField := func(t *testing.T, response *httptest.ResponseRecorder, field string) {
		t.Helper()
		assertScrapeFailure(t, response, http.StatusBadRequest, "INVALID_REQUEST")
		var result httpapi.APIResponse[any]
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.FieldErrors) != 1 || result.FieldErrors[0].Field != field || result.FieldErrors[0].Message == "" {
			t.Fatalf("missing domain field error for %s: %s", field, response.Body)
		}
	}
	for _, endpoint := range endpoints {
		t.Run(endpoint.name, func(t *testing.T) {
			for _, field := range endpoint.fields {
				variants := []string{"missing", "null"}
				if endpoint.method == http.MethodGet {
					variants = []string{"missing", "empty"}
				} else if _, numeric := endpoint.values[field].(int); numeric {
					variants = append(variants, "zero")
				}
				for _, variant := range variants {
					t.Run(field+"/"+variant, func(t *testing.T) {
						values := make(map[string]any, len(endpoint.values))
						for key, value := range endpoint.values {
							values[key] = value
						}
						delete(values, field)
						switch variant {
						case "null":
							values[field] = nil
						case "empty":
							values[field] = ""
						case "zero":
							values[field] = 0
						}
						path := strings.Replace(endpoint.path, ":id", fmt.Sprint(apiKey.ID), 1)
						var body []byte
						if endpoint.method == http.MethodGet {
							query := url.Values{}
							for key, value := range values {
								query.Set(key, fmt.Sprint(value))
							}
							path += "?" + query.Encode()
						} else {
							if endpoint.nested {
								values = map[string]any{"sync_path": values}
							}
							var err error
							body, err = json.Marshal(values)
							if err != nil {
								t.Fatal(err)
							}
						}
						request := httptest.NewRequest(endpoint.method, path, bytes.NewReader(body))
						if endpoint.method != http.MethodGet {
							request.Header.Set("Content-Type", "application/json")
						}
						response := httptest.NewRecorder()
						router.ServeHTTP(response, request)
						assertField(t, response, field)
					})
				}
			}
		})
	}
	t.Run("sync-empty-aggregate", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/sync/paths", strings.NewReader(`{"sync_path":{}}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertField(t, response, "source_type")
	})
	for _, test := range []struct{ name, method, path, body string }{
		{"wrong-string-type", http.MethodPost, "/setting/log", `{"level":123}`},
		{"wrong-number-type", http.MethodPost, "/115/queue/rate-limit", `{"qps":"private-value","qpm":120,"qph":1000}`},
		{"wrong-bool-type", http.MethodPut, fmt.Sprintf("/api-keys/%d/status", apiKey.ID), `{"is_active":"private-value"}`},
		{"malformed-json", http.MethodPost, "/api-keys", `{"name":`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertScrapeFailure(t, response, http.StatusBadRequest, "INVALID_REQUEST")
			var result httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if len(result.FieldErrors) != 0 || strings.Contains(response.Body.String(), "private-value") {
				t.Fatalf("binding error exposed request contents or invented field errors: %s", response.Body)
			}
		})
	}
	var keyCount, syncCount int64
	if err := db.Db.Model(&models.ApiKey{}).Count(&keyCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&models.SyncPath{}).Count(&syncCount).Error; err != nil {
		t.Fatal(err)
	}
	if keyCount != 1 || syncCount != 0 {
		t.Fatalf("invalid requests changed stored data: keys=%d sync_paths=%d", keyCount, syncCount)
	}
	stored, err := models.GetAPIKeyByIDAndUserID(apiKey.ID, user.ID)
	if err != nil || !stored.IsActive {
		t.Fatalf("invalid requests changed API Key status: stored=%+v err=%v", stored, err)
	}
	t.Run("explicit-false-disables-api-key", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api-keys/%d/status", apiKey.ID), strings.NewReader(`{"is_active":false}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		stored, err := models.GetAPIKeyByIDAndUserID(apiKey.ID, user.ID)
		if response.Code != http.StatusOK || err != nil || stored.IsActive {
			t.Fatalf("explicit false was not accepted: %s, stored=%+v err=%v", response.Body, stored, err)
		}
	})
}

func TestStrmConfigMultiPlaybackSave(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings, originalLogger := models.SettingsGlobal, helpers.AppLogger
	t.Cleanup(func() {
		models.SettingsGlobal, helpers.AppLogger = originalSettings, originalLogger
	})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	for _, tt := range []struct {
		name                  string
		previous, next, proxy int
		failWrite             bool
	}{
		{name: "启用并读回", next: 1},
		{name: "代理开启保留多端播放", next: 1, proxy: 1},
		{name: "关闭并读回", previous: 1},
		{name: "启用失败保留全部设置", next: 1, proxy: 1, failWrite: true},
		{name: "关闭失败保留全部设置", previous: 1, failWrite: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupControllerTestDB(t, &models.Settings{})
			models.SettingsGlobal = &models.Settings{
				MultiPlaybackEnabled: tt.previous,
				Cron:                 "0 * * * *", StrmBaseUrl: "http://old.local",
			}
			if err := db.Db.Create(models.SettingsGlobal).Error; err != nil {
				t.Fatal(err)
			}
			previous := *models.SettingsGlobal
			if tt.failWrite {
				if err := db.Db.Exec(`CREATE TRIGGER fail_strm_update BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'test write failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			router := gin.New()
			router.POST("/setting/strm-config", UpdateStrmConfig)
			router.GET("/setting/strm-config", GetStrmConfig)
			body, err := json.Marshal(map[string]any{
				"strm_base_url": "http://new.local", "cron": previous.Cron, "add_path": 3,
				"local_proxy": tt.proxy, "multi_playback_enabled": tt.next,
			})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/setting/strm-config", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			var stored models.Settings
			if err := db.Db.First(&stored).Error; err != nil {
				t.Fatal(err)
			}
			if tt.failWrite {
				if response.Code != http.StatusInternalServerError || result.Status != http.StatusInternalServerError {
					t.Fatalf("保存失败应返回业务错误：HTTP %d，%s", response.Code, response.Body)
				}
				if !reflect.DeepEqual(*models.SettingsGlobal, previous) || !reflect.DeepEqual(stored, previous) {
					t.Fatal("保存失败不能修改运行时或数据库中的任意 STRM 设置")
				}
				return
			}
			if response.Code != http.StatusOK || result.Status != http.StatusOK {
				t.Fatalf("保存失败：HTTP %d，%s", response.Code, response.Body)
			}
			for _, settings := range []*models.Settings{models.SettingsGlobal, &stored} {
				if settings.MultiPlaybackEnabled != tt.next || settings.LocalProxy != tt.proxy || settings.StrmBaseUrl != "http://new.local" {
					t.Fatal("多端播放、本地代理和原有 STRM 字段应一起保存")
				}
			}
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setting/strm-config", nil))
			var loaded httpapi.APIResponse[models.Settings]
			if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil {
				t.Fatal(err)
			}
			if loaded.Status != http.StatusOK || loaded.Data.MultiPlaybackEnabled != tt.next || loaded.Data.LocalProxy != tt.proxy {
				t.Fatalf("设置读回与保存值不一致：%s", response.Body)
			}
		})
	}
}

func TestStrmConfigRegexSaveRoundTripAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings, originalLogger := models.SettingsGlobal, helpers.AppLogger
	t.Cleanup(func() {
		models.SettingsGlobal, helpers.AppLogger = originalSettings, originalLogger
	})
	setupControllerTestDB(t, &models.Settings{})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.SettingsGlobal = &models.Settings{Cron: "0 * * * *"}
	if err := db.Db.Create(models.SettingsGlobal).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/setting/strm-config", UpdateStrmConfig)
	router.GET("/setting/strm-config", GetStrmConfig)
	patterns := []string{`(?i)^Sample\.[^.]+$`, `  \D{1,3};[A-Z]+,  `, " "}
	save := func(t *testing.T, values []string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"strm_base_url":          "http://qms.local",
			"cron":                   "0 * * * *",
			"video_ext_arr":          []string{".mkv"},
			"meta_ext_arr":           []string{".nfo"},
			"exclude_name_arr":       []string{"SaMpLe"},
			"exclude_name_regex_arr": values,
			"add_path":               3,
		})
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/setting/strm-config", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	checkReloaded := func(t *testing.T, want []string) {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setting/strm-config", nil))
		var result httpapi.APIResponse[models.SettingStrm]
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Status != http.StatusOK || !reflect.DeepEqual(result.Data.ExcludeNameRegexArr, want) {
			t.Fatalf("设置读回未保留原文：%s，期望 %q", response.Body, want)
		}
		if !reflect.DeepEqual(result.Data.ExcludeNameArr, []string{"sample"}) {
			t.Fatalf("原名称列表大小写行为改变：%q", result.Data.ExcludeNameArr)
		}
	}
	if response := save(t, patterns); response.Code != http.StatusOK {
		t.Fatalf("保存合法规则失败：%s", response.Body)
	}
	checkReloaded(t, patterns)
	for _, pattern := range []string{"[", "(?=sample)", `\p{UnknownClass}`, ""} {
		t.Run("拒绝_"+pattern, func(t *testing.T) {
			response := save(t, []string{"sample", pattern})
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "exclude_name_regex_arr[1]") {
				t.Fatalf("非法规则未返回下标错误：HTTP %d，%s", response.Code, response.Body)
			}
			checkReloaded(t, patterns)
		})
	}
	if response := save(t, []string{}); response.Code != http.StatusOK {
		t.Fatalf("清空正则失败：%s", response.Body)
	}
	checkReloaded(t, []string{})
}

func TestStrmConfigEmptyExtensionsUseConfiguredDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings, originalLogger, originalStrm := models.SettingsGlobal, helpers.AppLogger, helpers.GlobalConfig.Strm
	t.Cleanup(func() {
		models.SettingsGlobal, helpers.AppLogger, helpers.GlobalConfig.Strm = originalSettings, originalLogger, originalStrm
	})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.GlobalConfig.Strm.VideoExt = []string{".configured-video"}
	helpers.GlobalConfig.Strm.MetaExt = []string{".configured-meta"}

	for _, tt := range []struct {
		name                string
		video, meta         []string
		wantVideo, wantMeta []string
		omitExtensions      bool
		failWrite           bool
	}{
		{name: "清空视频扩展名", video: []string{}, meta: []string{".nfo"}, wantVideo: helpers.GlobalConfig.Strm.VideoExt, wantMeta: []string{".nfo"}},
		{name: "清空元数据扩展名", video: []string{".mkv"}, meta: []string{}, wantVideo: []string{".mkv"}, wantMeta: helpers.GlobalConfig.Strm.MetaExt},
		{name: "清空两类扩展名", video: []string{}, meta: []string{}, wantVideo: helpers.GlobalConfig.Strm.VideoExt, wantMeta: helpers.GlobalConfig.Strm.MetaExt},
		{name: "null 扩展名使用默认值", wantVideo: helpers.GlobalConfig.Strm.VideoExt, wantMeta: helpers.GlobalConfig.Strm.MetaExt},
		{name: "省略扩展名使用默认值", omitExtensions: true, wantVideo: helpers.GlobalConfig.Strm.VideoExt, wantMeta: helpers.GlobalConfig.Strm.MetaExt},
		{name: "保存失败保留旧设置", video: []string{}, meta: []string{}, failWrite: true},
		{name: "null 扩展名保存失败保留旧设置", failWrite: true},
		{name: "省略扩展名保存失败保留旧设置", omitExtensions: true, failWrite: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupControllerTestDB(t, &models.Settings{})
			initial := (models.SettingStrm{
				StrmBaseUrl:      "http://old.local",
				Cron:             "0 * * * *",
				VideoExt:         `[".old-video"]`,
				MetaExt:          `[".old-meta"]`,
				ExcludeName:      `["sample"]`,
				ExcludeNameRegex: `["(?i)sample"]`,
			}).DecodeArr(true)
			if initial == nil {
				t.Fatal("解码初始 STRM 设置失败")
			}
			models.SettingsGlobal = &models.Settings{SettingStrm: *initial}
			if err := db.Db.Create(models.SettingsGlobal).Error; err != nil {
				t.Fatal(err)
			}
			previous := *models.SettingsGlobal
			if tt.failWrite {
				if err := db.Db.Exec(`CREATE TRIGGER fail_strm_update BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'test write failure'); END`).Error; err != nil {
					t.Fatal(err)
				}
			}
			router := gin.New()
			router.POST("/setting/strm-config", UpdateStrmConfig)
			router.GET("/setting/strm-config", GetStrmConfig)
			payload := map[string]any{
				"strm_base_url":          "http://qms.local",
				"cron":                   "0 * * * *",
				"video_ext_arr":          tt.video,
				"meta_ext_arr":           tt.meta,
				"exclude_name_arr":       []string{},
				"exclude_name_regex_arr": []string{},
				"add_path":               3,
			}
			if tt.omitExtensions {
				delete(payload, "video_ext_arr")
				delete(payload, "meta_ext_arr")
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "/setting/strm-config", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var savedResult httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &savedResult); err != nil {
				t.Fatal(err)
			}
			var stored models.Settings
			if err := db.Db.Take(&stored, previous.ID).Error; err != nil {
				t.Fatal(err)
			}
			if tt.failWrite {
				if response.Code != http.StatusInternalServerError || savedResult.Status != http.StatusInternalServerError {
					t.Fatalf("保存失败应返回业务错误：HTTP %d，%s", response.Code, response.Body)
				}
				if !reflect.DeepEqual(*models.SettingsGlobal, previous) {
					t.Fatal("保存失败修改了运行时 STRM 设置")
				}
				stored.SettingStrm = *stored.SettingStrm.DecodeArr(true)
				if !reflect.DeepEqual(stored, previous) {
					t.Fatal("保存失败修改了数据库 STRM 设置")
				}
				return
			}
			if response.Code != http.StatusOK || savedResult.Status != http.StatusOK {
				t.Fatalf("清空扩展名保存失败：HTTP %d，%s", response.Code, response.Body)
			}
			if (len(tt.video) == 0 && stored.VideoExt != "[]") || (len(tt.meta) == 0 && stored.MetaExt != "[]") {
				t.Fatalf("清空扩展名应持久化为空数组：video=%q，meta=%q", stored.VideoExt, stored.MetaExt)
			}
			if stored.ExcludeName != "[]" || stored.ExcludeNameRegex != "[]" {
				t.Fatal("排除列表未持久化为空数组")
			}
			checkEffective := func(got models.SettingStrm) {
				t.Helper()
				if !reflect.DeepEqual(got.VideoExtArr, tt.wantVideo) || !reflect.DeepEqual(got.MetaExtArr, tt.wantMeta) {
					t.Fatalf("有效扩展名 = %q / %q，期望 %q / %q", got.VideoExtArr, got.MetaExtArr, tt.wantVideo, tt.wantMeta)
				}
				if !reflect.DeepEqual(got.ExcludeNameArr, []string{}) || !reflect.DeepEqual(got.ExcludeNameRegexArr, []string{}) {
					t.Fatalf("清空后的排除列表应保持为空：%q / %q", got.ExcludeNameArr, got.ExcludeNameRegexArr)
				}
			}
			checkEffective(models.SettingsGlobal.SettingStrm)
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setting/strm-config", nil))
			var loaded httpapi.APIResponse[models.SettingStrm]
			if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || loaded.Status != http.StatusOK {
				t.Fatalf("读取设置失败：HTTP %d，%s", response.Code, response.Body)
			}
			checkEffective(loaded.Data)
		})
	}
}

func TestUpdateThreadsApplies115RateConfigAfterSaving(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldSettings := models.SettingsGlobal
	oldDownloadQueue := models.GlobalDownloadQueue
	oldAppLogger := helpers.AppLogger
	oldDownloadQueueRunning := oldDownloadQueue != nil && oldDownloadQueue.IsRunning()
	if oldDownloadQueueRunning {
		oldDownloadQueue.Stop()
	}
	models.GlobalDownloadQueue = nil

	setupControllerTestDB(t, &models.Settings{})
	t.Cleanup(func() {
		if models.GlobalDownloadQueue != nil && models.GlobalDownloadQueue != oldDownloadQueue {
			models.GlobalDownloadQueue.Stop()
		}
		models.GlobalDownloadQueue = oldDownloadQueue
		if oldDownloadQueueRunning {
			oldDownloadQueue.Start()
		}
		models.SettingsGlobal = oldSettings
		models.SetThreadSettingsApplyHook(nil)
		helpers.AppLogger = oldAppLogger
	})

	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	settings := &models.Settings{
		DownloadThreads:    1,
		FileDetailThreads:  2,
		OpenlistQPS:        2,
		OpenlistRetry:      1,
		OpenlistRetryDelay: 30,
		FileListPageSize:   1150,
	}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatalf("创建测试 settings 失败：%v", err)
	}
	models.SettingsGlobal = settings
	models.LoadSettings()
	if err := models.StartSettingsRuntime(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = models.StopSettingsRuntime(context.Background()) })

	var (
		setCalls       int
		gotQPS         int
		gotQPM         int
		gotQPH         int
		savedQPS       int
		setCallbackErr error
	)
	models.SetThreadSettingsApplyHook(func(config models.SettingThreadAndRapidWait) error {
		qps, qpm, qph := config.FileDetailThreads, config.FileDetailThreads*60, config.FileDetailThreads*3600
		setCalls++
		gotQPS, gotQPM, gotQPH = qps, qpm, qph
		var saved models.Settings
		setCallbackErr = db.Db.Take(&saved, settings.ID).Error
		if setCallbackErr == nil {
			savedQPS = saved.FileDetailThreads
		}
		return nil
	})

	r := gin.New()
	r.POST("/setting/threads", UpdateThreads)
	req := httptest.NewRequest(http.MethodPost, "/setting/threads", strings.NewReader(`{
		"download_threads": 1,
		"file_detail_threads": 4,
		"openlist_qps": 2,
		"openlist_retry": 1,
		"openlist_retry_delay": 30,
		"file_list_page_size": 1150
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d，期望 200，body=%s", w.Code, w.Body.String())
	}
	if setCallbackErr != nil {
		t.Fatalf("运行时配置回调读取 settings 失败：%v", setCallbackErr)
	}
	if setCalls != 1 {
		t.Fatalf("运行时配置回调次数 = %d，期望 1", setCalls)
	}
	if gotQPS != 4 || gotQPM != 240 || gotQPH != 14400 {
		t.Fatalf("115 运行时配置 = %d/%d/%d，期望 4/240/14400", gotQPS, gotQPM, gotQPH)
	}
	if savedQPS != 4 {
		t.Fatalf("运行时配置回调执行时数据库 QPS = %d，期望已保存为 4", savedQPS)
	}
}

func TestUpdateThreadsUploadConcurrency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldSettings, oldUploadQueue, oldDownloadQueue := models.SettingsGlobal, models.GlobalUploadQueue, models.GlobalDownloadQueue
	oldLogger := helpers.AppLogger
	oldDownloadRunning := oldDownloadQueue != nil && oldDownloadQueue.IsRunning()
	if oldDownloadRunning {
		oldDownloadQueue.Stop()
	}
	models.GlobalDownloadQueue = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		models.SettingsGlobal, models.GlobalUploadQueue, models.GlobalDownloadQueue = oldSettings, oldUploadQueue, oldDownloadQueue
		helpers.AppLogger = oldLogger
		models.SetThreadSettingsApplyHook(nil)
		if oldDownloadRunning {
			oldDownloadQueue.Start()
		}
	})

	for _, tt := range []struct {
		name       string
		field      string
		value      any
		omit       bool
		failWrite  bool
		wantHTTP   int
		wantUpload int
	}{
		{name: "允许最小值", value: 1, wantHTTP: http.StatusOK, wantUpload: 1},
		{name: "允许最大值", value: 10, wantHTTP: http.StatusOK, wantUpload: 10},
		{name: "旧客户端省略字段保留配置", omit: true, wantHTTP: http.StatusOK, wantUpload: 7},
		{name: "空值沿用可选字段兼容行为", value: nil, wantHTTP: http.StatusOK, wantUpload: 7},
		{name: "拒绝零", value: 0, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "拒绝负数", value: -1, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "拒绝大于上限", value: 11, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "拒绝小数", value: 1.5, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "拒绝字符串", value: "3", wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "拒绝布尔值", value: true, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "写入失败保留配置", value: 3, failWrite: true, wantHTTP: http.StatusInternalServerError, wantUpload: 7},
		{name: "下载线程空值定位字段", field: "download_threads", value: nil, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "文件详情线程空值定位字段", field: "file_detail_threads", value: nil, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "OpenList QPS 空值定位字段", field: "openlist_qps", value: nil, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "OpenList 重试次数空值定位字段", field: "openlist_retry", value: nil, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "OpenList 重试间隔空值定位字段", field: "openlist_retry_delay", value: nil, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "文件列表分页大小空值定位字段", field: "file_list_page_size", value: nil, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "下载线程缺失定位字段", field: "download_threads", omit: true, wantHTTP: http.StatusBadRequest, wantUpload: 7},
		{name: "下载线程零值定位字段", field: "download_threads", value: 0, wantHTTP: http.StatusBadRequest, wantUpload: 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupControllerTestDB(t, &models.Settings{}, &models.DbDownloadTask{})
			settings := &models.Settings{
				DownloadThreads: 1, UploadThreads: 7, FileDetailThreads: 3,
				OpenlistQPS: 2, OpenlistRetry: 1, OpenlistRetryDelay: 30, FileListPageSize: 1150}
			if err := db.Db.Create(settings).Error; err != nil {
				t.Fatal(err)
			}
			models.SettingsGlobal = settings
			models.LoadSettings()
			if err := models.StartSettingsRuntime(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = models.StopSettingsRuntime(context.Background()) })
			queue := models.NewUq(7)
			models.GlobalUploadQueue = queue
			t.Cleanup(func() {
				if models.GlobalDownloadQueue != nil {
					models.GlobalDownloadQueue.Stop()
					models.GlobalDownloadQueue = nil
				}
			})
			previous := settings.ThreadAndRapidWait()
			if tt.failWrite {
				if err := db.Db.Exec("CREATE TRIGGER fail_threads_update BEFORE UPDATE ON settings BEGIN SELECT RAISE(ABORT, 'test write failure'); END").Error; err != nil {
					t.Fatal(err)
				}
			}
			rateCalls := 0
			models.SetThreadSettingsApplyHook(func(models.SettingThreadAndRapidWait) error { rateCalls++; return nil })
			payload := map[string]any{
				"download_threads": 1, "file_detail_threads": 4,
				"openlist_qps": 2, "openlist_retry": 1, "openlist_retry_delay": 30,
				"file_list_page_size": 1150,
			}
			field := tt.field
			if field == "" {
				field = "upload_threads"
			}
			if tt.omit {
				delete(payload, field)
			} else {
				payload[field] = tt.value
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.POST("/setting/threads", UpdateThreads)
			router.GET("/setting/threads", GetThreads)
			request := httptest.NewRequest(http.MethodPost, "/setting/threads", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			wantCode := tt.wantHTTP
			if response.Code != tt.wantHTTP || result.Status != wantCode {
				t.Fatalf("保存响应 = HTTP %d，%s", response.Code, response.Body)
			}
			if wantCode != http.StatusOK && (models.GetThreadSettingsSnapshot() != previous || rateCalls != 0) {
				t.Fatal("拒绝请求或保存失败后不应修改内存配置或运行时速率")
			}
			if models.GlobalUploadQueue != queue || queue.IsRunning() || queue.GetConcurrency() != tt.wantUpload {
				t.Fatalf("应保留原暂停队列并设置并发为 %d；实际并发 %d、运行中 %t", tt.wantUpload, queue.GetConcurrency(), queue.IsRunning())
			}
			var saved models.Settings
			if err := db.Db.Take(&saved, settings.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.UploadThreads != tt.wantUpload || models.GetThreadSettingsSnapshot().UploadThreads != tt.wantUpload {
				t.Fatalf("数据库/内存上传并发 = %d/%d，期望 %d", saved.UploadThreads, models.GetThreadSettingsSnapshot().UploadThreads, tt.wantUpload)
			}
			if wantCode != http.StatusOK && saved.ThreadAndRapidWait() != previous {
				t.Fatal("拒绝请求或保存失败后不应修改已保存配置")
			}
			if tt.field != "" {
				if result.ErrorCode != "INVALID_REQUEST" || result.Message == "" || result.Instance == "" || result.Data != nil ||
					len(result.FieldErrors) != 1 || result.FieldErrors[0].Field != tt.field || result.FieldErrors[0].Message == "" {
					t.Fatalf("字段拒绝应保留安全说明和 JSON 字段定位：%s", response.Body)
				}
				if strings.Contains(response.Body.String(), "UpdateThreadsRequest") || strings.Contains(response.Body.String(), "required") {
					t.Fatalf("字段拒绝不应暴露绑定器细节：%s", response.Body)
				}
			}
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setting/threads", nil))
			var loaded httpapi.APIResponse[models.SettingThreadAndRapidWait]
			if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || loaded.Status != http.StatusOK || loaded.Data.UploadThreads != tt.wantUpload {
				t.Fatalf("上传并发读取结果不一致：%s", response.Body)
			}
		})
	}
}

func TestParseMediaLibraryDistinguishesReadFailureFromUnavailable(t *testing.T) {
	for _, handler := range []struct {
		name  string
		parse gin.HandlerFunc
	}{
		{name: "Emby", parse: ParseEmby},
		{name: "Jellyfin", parse: ParseJellyfin},
	} {
		for _, failRead := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/readFailure=%v", handler.name, failRead), func(t *testing.T) {
				conn := setupControllerTestDB(t, &models.EmbyConfig{}, &models.JellyfinConfig{})
				var logs bytes.Buffer
				previous := helpers.AppLogger
				helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
				t.Cleanup(func() { helpers.AppLogger = previous })
				if failRead {
					if err := conn.Callback().Query().Before("gorm:query").Register("fixture_media_read_failure", func(tx *gorm.DB) {
						tx.AddError(errors.New("private media configuration read failure"))
					}); err != nil {
						t.Fatal(err)
					}
				}
				router := gin.New()
				router.POST("/parse", handler.parse)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/parse", nil))
				status, code := http.StatusConflict, "MEDIA_LIBRARY_UNAVAILABLE"
				if failRead {
					status, code = http.StatusInternalServerError, "INTERNAL_ERROR"
				}
				assertScrapeFailure(t, response, status, code)
				if failRead {
					var result httpapi.APIResponse[any]
					if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(response.Body.String(), "private media configuration") || !strings.Contains(logs.String(), "private media configuration read failure") || !strings.Contains(logs.String(), result.Instance) {
						t.Fatalf("read cause must be private and correlated: response=%s logs=%s", response.Body, &logs)
					}
				}
			})
		}
	}
}
