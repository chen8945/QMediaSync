package controllers

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func TestStrmConfigRegexSaveRoundTripAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalSettings, originalLogger := models.SettingsGlobal, helpers.AppLogger
	t.Cleanup(func() {
		models.SettingsGlobal, helpers.AppLogger = originalSettings, originalLogger
	})
	setupControllerTestDB(t, &models.Settings{})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.SettingsGlobal = &models.Settings{SettingStrm: models.SettingStrm{Cron: "0 * * * *"}}
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
		var result APIResponse[models.SettingStrm]
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Code != Success || !reflect.DeepEqual(result.Data.ExcludeNameRegexArr, want) {
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
			var savedResult APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &savedResult); err != nil {
				t.Fatal(err)
			}
			var stored models.Settings
			if err := db.Db.Take(&stored, previous.ID).Error; err != nil {
				t.Fatal(err)
			}
			if tt.failWrite {
				if response.Code != http.StatusOK || savedResult.Code != BadRequest {
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
			if response.Code != http.StatusOK || savedResult.Code != Success {
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
			var loaded APIResponse[models.SettingStrm]
			if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || loaded.Code != Success {
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
	oldSetGlobalExecutorConfig := setGlobalExecutorConfig
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
		setGlobalExecutorConfig = oldSetGlobalExecutorConfig
		helpers.AppLogger = oldAppLogger
	})

	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	settings := &models.Settings{
		SettingThreads: models.SettingThreads{
			DownloadThreads:    1,
			FileDetailThreads:  2,
			OpenlistQPS:        2,
			OpenlistRetry:      1,
			OpenlistRetryDelay: 30,
			FileListPageSize:   1150,
		},
	}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatalf("创建测试 settings 失败：%v", err)
	}
	models.SettingsGlobal = settings

	var (
		setCalls       int
		gotQPS         int
		gotQPM         int
		gotQPH         int
		savedQPS       int
		setCallbackErr error
	)
	setGlobalExecutorConfig = func(qps, qpm, qph int) {
		setCalls++
		gotQPS, gotQPM, gotQPH = qps, qpm, qph
		var saved models.Settings
		setCallbackErr = db.Db.Take(&saved, settings.ID).Error
		if setCallbackErr == nil {
			savedQPS = saved.FileDetailThreads
		}
	}

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
	if !strings.Contains(w.Body.String(), `"code":200`) {
		t.Fatalf("更新线程配置失败：%s", w.Body.String())
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
	oldLogger, oldSetRateConfig := helpers.AppLogger, setGlobalExecutorConfig
	oldDownloadRunning := oldDownloadQueue != nil && oldDownloadQueue.IsRunning()
	if oldDownloadRunning {
		oldDownloadQueue.Stop()
	}
	models.GlobalDownloadQueue = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		models.SettingsGlobal, models.GlobalUploadQueue, models.GlobalDownloadQueue = oldSettings, oldUploadQueue, oldDownloadQueue
		helpers.AppLogger, setGlobalExecutorConfig = oldLogger, oldSetRateConfig
		if oldDownloadRunning {
			oldDownloadQueue.Start()
		}
	})

	for _, tt := range []struct {
		name       string
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
		{name: "写入失败保留配置", value: 3, failWrite: true, wantHTTP: http.StatusOK, wantUpload: 7},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setupControllerTestDB(t, &models.Settings{}, &models.DbDownloadTask{})
			settings := &models.Settings{SettingThreads: models.SettingThreads{
				DownloadThreads: 1, UploadThreads: 7, FileDetailThreads: 3,
				OpenlistQPS: 2, OpenlistRetry: 1, OpenlistRetryDelay: 30, FileListPageSize: 1150,
			}}
			if err := db.Db.Create(settings).Error; err != nil {
				t.Fatal(err)
			}
			models.SettingsGlobal = settings
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
			setGlobalExecutorConfig = func(_, _, _ int) { rateCalls++ }
			payload := map[string]any{
				"download_threads": 1, "file_detail_threads": 4,
				"openlist_qps": 2, "openlist_retry": 1, "openlist_retry_delay": 30,
				"file_list_page_size": 1150,
			}
			if !tt.omit {
				payload["upload_threads"] = tt.value
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
			var result APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			wantCode := Success
			if tt.wantHTTP != http.StatusOK || tt.failWrite {
				wantCode = BadRequest
				if settings.ThreadAndRapidWait() != previous || rateCalls != 0 {
					t.Fatal("拒绝请求或保存失败后不应修改内存配置或运行时速率")
				}
			}
			if response.Code != tt.wantHTTP || result.Code != wantCode {
				t.Fatalf("保存响应 = HTTP %d，%s", response.Code, response.Body)
			}
			if models.GlobalUploadQueue != queue || queue.IsRunning() || queue.GetConcurrency() != tt.wantUpload {
				t.Fatalf("应保留原暂停队列并设置并发为 %d；实际并发 %d、运行中 %t", tt.wantUpload, queue.GetConcurrency(), queue.IsRunning())
			}
			var saved models.Settings
			if err := db.Db.Take(&saved, settings.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.UploadThreads != tt.wantUpload || settings.UploadThreads != tt.wantUpload {
				t.Fatalf("数据库/内存上传并发 = %d/%d，期望 %d", saved.UploadThreads, settings.UploadThreads, tt.wantUpload)
			}
			if wantCode != Success && saved.ThreadAndRapidWait() != previous {
				t.Fatal("拒绝请求或保存失败后不应修改已保存配置")
			}
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/setting/threads", nil))
			var loaded APIResponse[models.SettingThreadAndRapidWait]
			if err := json.Unmarshal(response.Body.Bytes(), &loaded); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || loaded.Code != Success || loaded.Data.UploadThreads != tt.wantUpload {
				t.Fatalf("上传并发读取结果不一致：%s", response.Body)
			}
		})
	}
}
