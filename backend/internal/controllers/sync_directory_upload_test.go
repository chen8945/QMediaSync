package controllers

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/syncconfig"

	"github.com/gin-gonic/gin"
)

func TestSyncPathAggregateRegexSaveRoundTripAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	originalFactory, originalLogger := newSyncPathConfigService, helpers.AppLogger
	t.Cleanup(func() {
		newSyncPathConfigService, helpers.AppLogger = originalFactory, originalLogger
	})
	testDB := setupControllerTestDB(t, &models.Account{}, &models.SyncPath{},
		&models.DirectoryUploadRule{}, &models.SyncPathIdempotencyRecord{})
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	newSyncPathConfigService = func() *syncconfig.Service {
		return syncconfig.NewService(syncconfig.ServiceOptions{
			DB: testDB, CreateLocalDirectory: func(string) error { return nil },
			ReloadSyncCron: func() {}, ReloadDirectoryUpload: func() {},
		})
	}
	router := gin.New()
	router.POST("/sync/paths", CreateSyncPathAggregate)
	router.PUT("/sync/paths/:id", UpdateSyncPathAggregate)
	router.GET("/sync/path/:id", GetSyncPathById)
	patterns := []string{`(?i)^Sample\.[^.]+$`, `  \D{1,3};[A-Z]+,  `, " "}
	setting := map[string]any{
		"exclude_name_arr": []string{"SaMpLe"}, "exclude_name_regex_arr": patterns, "add_path": 3,
	}
	payload := map[string]any{"sync_path": map[string]any{
		"source_type": "local", "base_cid": "/media", "remote_path": "/media",
		"local_path": t.TempDir(), "custom_config": true, "setting": setting,
	}}
	save := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", "strm-regex-roundtrip")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := save(http.MethodPost, "/sync/paths"); response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"code":200`) {
		t.Fatalf("创建含正则的同步目录失败：%s", response.Body)
	}
	checkReloaded := func(want []string) {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/sync/path/1", nil))
		var result APIResponse[models.SyncPath]
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Code != Success || !reflect.DeepEqual(result.Data.ExcludeNameRegexArr, want) {
			t.Fatalf("同步目录读回未保留原文：%s，期望 %q", response.Body, want)
		}
		if !reflect.DeepEqual(result.Data.ExcludeNameArr, []string{"sample"}) {
			t.Fatalf("原名称列表大小写行为改变：%q", result.Data.ExcludeNameArr)
		}
	}
	checkReloaded(patterns)
	setting["exclude_name_regex_arr"] = []string{`\p{UnknownClass}`}
	response := save(http.MethodPut, "/sync/paths/1")
	if response.Code != http.StatusBadRequest ||
		!strings.Contains(response.Body.String(), `"field":"exclude_name_regex_arr[0]"`) {
		t.Fatalf("后端校验未返回可定位字段：HTTP %d，%s", response.Code, response.Body)
	}
	checkReloaded(patterns)
	setting["exclude_name_regex_arr"] = []string{}
	if response := save(http.MethodPut, "/sync/paths/1"); response.Code != http.StatusOK {
		t.Fatalf("清空目录正则失败：%s", response.Body)
	}
	checkReloaded([]string{})
}

func TestSyncPathAggregateHandlersHaveSwaggerAnnotations(t *testing.T) {
	content, err := os.ReadFile("sync_config.go")
	if err != nil {
		t.Fatalf("读取同步目录聚合控制器失败: %v", err)
	}
	source := string(content)
	for _, expected := range []string{
		"// @Router /sync/paths [post]",
		"// @Router /sync/paths/{id} [put]",
		"// @Param Idempotency-Key header string false",
	} {
		if !strings.Contains(source, expected) {
			t.Fatalf("sync_config.go 缺少 Swagger 注解 %q", expected)
		}
	}
}

func TestUpdateSyncPathAggregateReloadsDirectoryUploadServiceWhenMasterSwitchChanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	setupControllerTestDB(
		t,
		&models.Account{},
		&models.SyncPath{},
		&models.DirectoryUploadRule{},
		&models.DirectoryUploadProcessedFile{},
	)
	models.SettingsGlobal = &models.Settings{
		SettingStrm: models.SettingStrm{VideoExtArr: []string{".mkv"}, MinVideoSize: 0},
	}
	t.Cleanup(directoryupload.StopDirectoryUploadService)

	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	syncPath := &models.SyncPath{
		SourceType:             models.SourceType115,
		AccountId:              account.ID,
		BaseCid:                "root",
		LocalPath:              filepath.Join(t.TempDir(), "strm"),
		RemotePath:             "/remote",
		DirectoryUploadEnabled: true,
		SettingStrm:            models.SettingStrm{VideoExtArr: []string{".mkv"}, MinVideoSize: 0},
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}
	rule := &models.DirectoryUploadRule{
		SyncPathId:               syncPath.ID,
		AccountId:                account.ID,
		Enabled:                  true,
		MonitorPath:              t.TempDir(),
		RemoteRootPath:           "/remote/uploads",
		RemoteRootId:             "remote-root",
		Recursive:                true,
		WatchMode:                models.DirectoryUploadWatchModePolling,
		StartupScanEnabled:       false,
		ProcessedCacheTTLSeconds: 600,
	}
	if err := db.Db.Create(rule).Error; err != nil {
		t.Fatalf("创建目录监控规则失败: %v", err)
	}

	directoryupload.InitDirectoryUploadService()
	if got := len(directoryupload.GetDirectoryUploadRuntimeStatuses()); got != 1 {
		t.Fatalf("关闭前运行规则数量 = %d，期望 1", got)
	}

	oldFactory := newSyncPathConfigService
	newSyncPathConfigService = func() *syncconfig.Service {
		return syncconfig.NewService(syncconfig.ServiceOptions{
			DB:                    db.Db,
			CreateLocalDirectory:  func(string) error { return nil },
			ReloadSyncCron:        func() {},
			ReloadDirectoryUpload: directoryupload.ReloadDirectoryUploadService,
		})
	}
	t.Cleanup(func() { newSyncPathConfigService = oldFactory })

	router := gin.New()
	router.PUT("/sync/paths/:id", UpdateSyncPathAggregate)
	body, _ := json.Marshal(map[string]any{
		"sync_path": map[string]any{
			"source_type":   string(models.SourceType115),
			"account_id":    account.ID,
			"base_cid":      syncPath.BaseCid,
			"local_path":    syncPath.LocalPath,
			"remote_path":   syncPath.RemotePath,
			"enable_cron":   false,
			"custom_config": false,
		},
		"directory_upload": map[string]any{
			"enabled": false,
			"rules": []map[string]any{{
				"id":                          rule.ID,
				"client_id":                   "rule-1",
				"enabled":                     true,
				"monitor_path":                rule.MonitorPath,
				"remote_root_path":            rule.RemoteRootPath,
				"remote_root_id":              rule.RemoteRootId,
				"recursive":                   rule.Recursive,
				"watch_mode":                  rule.WatchMode,
				"startup_scan_enabled":        rule.StartupScanEnabled,
				"processed_cache_ttl_seconds": rule.ProcessedCacheTTLSeconds,
			}},
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/sync/paths/1", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"code":200`)) {
		t.Fatalf("更新同步目录响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	if got := len(directoryupload.GetDirectoryUploadRuntimeStatuses()); got != 0 {
		t.Fatalf("关闭总开关后运行规则数量 = %d，期望服务重载后为 0", got)
	}
}

func TestUpdateSyncPathAggregateRejectsInvalidIDWithStructuredError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/sync/paths/:id", UpdateSyncPathAggregate)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/sync/paths/abc", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"error_code":"INVALID_REQUEST"`)) ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"field":"id"`)) {
		t.Fatalf("非法 ID 结构化错误响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateSyncPathAggregateRollsBackWhenRuleValidationFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDB := setupControllerTestDB(
		t,
		&models.Account{},
		&models.SyncPath{},
		&models.DirectoryUploadRule{},
		&models.DirectoryUploadProcessedFile{},
		&models.DbUploadTask{},
		&models.SyncPathIdempotencyRecord{},
	)
	account := &models.Account{SourceType: models.SourceType115, Name: "115"}
	if err := testDB.Create(account).Error; err != nil {
		t.Fatalf("创建账号失败: %v", err)
	}
	oldFactory := newSyncPathConfigService
	newSyncPathConfigService = func() *syncconfig.Service {
		return syncconfig.NewService(syncconfig.ServiceOptions{DB: testDB})
	}
	t.Cleanup(func() { newSyncPathConfigService = oldFactory })

	router := gin.New()
	router.POST("/sync/paths", CreateSyncPathAggregate)
	body, _ := json.Marshal(map[string]any{
		"sync_path": map[string]any{
			"source_type": "115",
			"account_id":  account.ID,
			"base_cid":    "root",
			"local_path":  filepath.Join(t.TempDir(), "strm"),
			"remote_path": "/remote",
		},
		"directory_upload": map[string]any{
			"enabled": true,
			"rules": []map[string]any{{
				"client_id":        "rule-2",
				"enabled":          true,
				"monitor_path":     t.TempDir(),
				"remote_root_path": "/outside",
				"remote_root_id":   "outside",
			}},
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "controller-rollback")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"error_code":"DIRECTORY_UPLOAD_RULE_BOUNDARY"`)) ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"client_id":"rule-2"`)) ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"field":"remote_root_path"`)) {
		t.Fatalf("规则失败响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var total int64
	if err := testDB.Model(&models.SyncPath{}).Count(&total).Error; err != nil {
		t.Fatalf("统计同步目录失败: %v", err)
	}
	if total != 0 {
		t.Fatalf("规则失败后同步目录数量 = %d，期望事务回滚为 0", total)
	}
}

func TestCreateSyncPathAggregateReturnsFieldErrorForMissingBaseField(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/sync/paths", CreateSyncPathAggregate)
	body, _ := json.Marshal(map[string]any{
		"sync_path": map[string]any{
			"source_type": "local",
			"base_cid":    "root",
			"remote_path": "/remote",
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"field":"local_path"`)) ||
		!bytes.Contains(w.Body.Bytes(), []byte(`"message":"不能为空"`)) {
		t.Fatalf("基础字段校验响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestCreateSyncPathAggregateReturnsFieldErrorForUnknownAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	testDB := setupControllerTestDB(t, &models.Account{}, &models.SyncPath{}, &models.SyncPathIdempotencyRecord{})
	oldFactory := newSyncPathConfigService
	newSyncPathConfigService = func() *syncconfig.Service {
		return syncconfig.NewService(syncconfig.ServiceOptions{DB: testDB})
	}
	t.Cleanup(func() { newSyncPathConfigService = oldFactory })
	router := gin.New()
	router.POST("/sync/paths", CreateSyncPathAggregate)
	body, _ := json.Marshal(map[string]any{
		"sync_path": map[string]any{
			"source_type": "115",
			"account_id":  999,
			"base_cid":    "root",
			"local_path":  filepath.Join(t.TempDir(), "strm"),
			"remote_path": "/remote",
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync/paths", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || !bytes.Contains(w.Body.Bytes(), []byte(`"field":"account_id"`)) {
		t.Fatalf("账号字段校验响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
}
