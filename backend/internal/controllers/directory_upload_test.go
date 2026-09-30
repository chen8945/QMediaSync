package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func setupDirectoryUploadControllerTest(t *testing.T) (*gin.Engine, *models.SyncPath) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	setupControllerTestDB(
		t,
		&models.Account{},
		&models.SyncPath{},
		&models.DirectoryUploadRule{},
		&models.DirectoryUploadProcessedFile{},
	)
	models.SettingsGlobal = &models.Settings{
		VideoExtArr:  []string{".mkv", ".mp4"},
		MetaExtArr:   []string{".nfo"},
		MinVideoSize: 0,
	}
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
		VideoExtArr:            []string{".mkv", ".mp4"}, MinVideoSize: 0,
	}
	if err := db.Db.Create(syncPath).Error; err != nil {
		t.Fatalf("创建同步目录失败: %v", err)
	}

	router := gin.New()
	router.GET("/rules", ListDirectoryUploadRules)
	router.POST("/sync-paths/:sync_path_id/scan", ScanDirectoryUploadSyncPathRules)
	router.GET("/runtime-status", GetDirectoryUploadRuntimeStatuses)
	return router, syncPath
}

func TestListDirectoryUploadRulesReturnsIgnorePatterns(t *testing.T) {
	router, syncPath := setupDirectoryUploadControllerTest(t)
	rule := &models.DirectoryUploadRule{
		SyncPathId:                    syncPath.ID,
		AccountId:                     syncPath.AccountId,
		Enabled:                       true,
		MonitorPath:                   t.TempDir(),
		RemoteRootPath:                "/remote/uploads",
		RemoteRootId:                  "remote-root",
		Recursive:                     true,
		WatchMode:                     models.DirectoryUploadWatchModeAuto,
		StabilitySeconds:              models.DirectoryUploadDefaultStabilitySeconds,
		StabilityCheckIntervalSeconds: models.DirectoryUploadDefaultStabilityCheckIntervalSeconds,
		StabilityRequiredCount:        models.DirectoryUploadDefaultStabilityRequiredCount,
		RescanIntervalSeconds:         models.DirectoryUploadDefaultRescanIntervalSeconds,
		StartupScanEnabled:            true,
		ProcessedCacheTTLSeconds:      600,
		IgnorePatternsStr:             `["**/sample/**","*.tmp"]`,
		OverwriteMode:                 models.DirectoryUploadOverwriteSkipSame,
	}
	if err := db.Db.Create(rule).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/rules?sync_path_id=%d", syncPath.ID), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("查询规则响应状态异常: code=%d body=%s", w.Code, w.Body.String())
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			List []struct {
				IgnorePatterns []string `json:"ignore_patterns"`
			} `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析规则列表响应失败: %v, body=%s", err, w.Body.String())
	}
	if response.Code != int(Success) || len(response.Data.List) != 1 {
		t.Fatalf("规则列表响应 = %+v，期望返回 1 条成功数据", response)
	}
	want := []string{"**/sample/**", "*.tmp"}
	if !reflect.DeepEqual(response.Data.List[0].IgnorePatterns, want) {
		t.Fatalf("ignore_patterns=%v，期望 %v", response.Data.List[0].IgnorePatterns, want)
	}
}

func TestScanDirectoryUploadSyncPathRuleReturnsAcceptedCount(t *testing.T) {
	router, syncPath := setupDirectoryUploadControllerTest(t)
	monitorPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(monitorPath, "movie.mkv"), []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入测试视频失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(monitorPath, "ignore.tmp"), []byte("tmp"), 0o644); err != nil {
		t.Fatalf("写入测试临时文件失败: %v", err)
	}
	if err := db.Db.Create(&models.DirectoryUploadRule{
		SyncPathId:                    syncPath.ID,
		AccountId:                     syncPath.AccountId,
		Enabled:                       true,
		MonitorPath:                   monitorPath,
		RemoteRootPath:                "/remote",
		RemoteRootId:                  "remote-root",
		Recursive:                     true,
		WatchMode:                     models.DirectoryUploadWatchModePolling,
		StabilitySeconds:              0,
		StabilityCheckIntervalSeconds: 1,
		StabilityRequiredCount:        1,
		ProcessedCacheTTLSeconds:      600,
	}).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/sync-paths/%d/scan", syncPath.ID), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted":1`) {
		t.Fatalf("扫描响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestScanDirectoryUploadSyncPathRulesScansAllEnabledRules(t *testing.T) {
	router, syncPath := setupDirectoryUploadControllerTest(t)
	monitorOne := t.TempDir()
	monitorTwo := t.TempDir()
	monitorDisabled := t.TempDir()
	if err := os.WriteFile(filepath.Join(monitorOne, "movie-one.mkv"), []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入第一个测试视频失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(monitorTwo, "movie-two.mkv"), []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入第二个测试视频失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(monitorDisabled, "disabled.mkv"), []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入停用规则测试视频失败: %v", err)
	}
	rules := []*models.DirectoryUploadRule{
		{
			SyncPathId:               syncPath.ID,
			AccountId:                syncPath.AccountId,
			Enabled:                  true,
			MonitorPath:              monitorOne,
			RemoteRootPath:           "/remote/one",
			RemoteRootId:             "remote-one",
			Recursive:                true,
			WatchMode:                models.DirectoryUploadWatchModePolling,
			ProcessedCacheTTLSeconds: 600,
		},
		{
			SyncPathId:               syncPath.ID,
			AccountId:                syncPath.AccountId,
			Enabled:                  true,
			MonitorPath:              monitorTwo,
			RemoteRootPath:           "/remote/two",
			RemoteRootId:             "remote-two",
			Recursive:                true,
			WatchMode:                models.DirectoryUploadWatchModePolling,
			ProcessedCacheTTLSeconds: 600,
		},
		{
			SyncPathId:               syncPath.ID,
			AccountId:                syncPath.AccountId,
			Enabled:                  false,
			MonitorPath:              monitorDisabled,
			RemoteRootPath:           "/remote/disabled",
			RemoteRootId:             "remote-disabled",
			Recursive:                true,
			WatchMode:                models.DirectoryUploadWatchModePolling,
			ProcessedCacheTTLSeconds: 600,
		},
	}
	if err := db.Db.Create(&rules).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}
	if err := db.Db.Model(rules[2]).Update("enabled", false).Error; err != nil {
		t.Fatalf("停用第三条目录上传规则失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/sync-paths/%d/scan", syncPath.ID), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("批量扫描状态异常: code=%d body=%s", w.Code, w.Body.String())
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			Accepted int `json:"accepted"`
			Items    []struct {
				RuleID   uint `json:"rule_id"`
				Accepted int  `json:"accepted"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析批量扫描响应失败: %v, body=%s", err, w.Body.String())
	}
	if response.Code != int(Success) || response.Data.Accepted != 2 || len(response.Data.Items) != 2 {
		t.Fatalf("批量扫描响应 = %+v，期望只扫描 2 条启用规则并加入 2 个候选文件", response)
	}
}

func TestScanDirectoryUploadSyncPathRulesUsesRequestContext(t *testing.T) {
	router, syncPath := setupDirectoryUploadControllerTest(t)
	monitorPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(monitorPath, "movie.mkv"), []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入测试视频失败: %v", err)
	}
	if err := db.Db.Create(&models.DirectoryUploadRule{
		SyncPathId:               syncPath.ID,
		AccountId:                syncPath.AccountId,
		Enabled:                  true,
		MonitorPath:              monitorPath,
		RemoteRootPath:           "/remote",
		RemoteRootId:             "remote-root",
		Recursive:                true,
		WatchMode:                models.DirectoryUploadWatchModePolling,
		ProcessedCacheTTLSeconds: 600,
	}).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/sync-paths/%d/scan", syncPath.ID), nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("取消请求扫描 HTTP 状态异常: code=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Accepted int `json:"accepted"`
		} `json:"data"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析取消请求扫描响应失败: %v, body=%s", err, w.Body.String())
	}
	if response.Code != int(BadRequest) || response.Data.Accepted != 0 || !strings.Contains(response.Message, context.Canceled.Error()) {
		t.Fatalf("取消请求扫描响应 = %+v，期望使用请求 context 并返回 context canceled", response)
	}
}

func TestDirectoryUploadRuntimeStatusReturnsItems(t *testing.T) {
	router, syncPath := setupDirectoryUploadControllerTest(t)
	directoryupload.StopDirectoryUploadService()
	t.Cleanup(directoryupload.StopDirectoryUploadService)

	monitorPath := t.TempDir()
	rule := &models.DirectoryUploadRule{
		SyncPathId:                    syncPath.ID,
		AccountId:                     syncPath.AccountId,
		Enabled:                       true,
		MonitorPath:                   monitorPath,
		RemoteRootPath:                "/remote",
		RemoteRootId:                  "remote-root",
		Recursive:                     true,
		WatchMode:                     models.DirectoryUploadWatchModePolling,
		StartupScanEnabled:            false,
		StabilitySeconds:              0,
		StabilityCheckIntervalSeconds: 1,
		StabilityRequiredCount:        1,
		ProcessedCacheTTLSeconds:      600,
	}
	if err := db.Db.Create(rule).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}
	directoryupload.InitDirectoryUploadService()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/runtime-status", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("运行状态响应状态异常: code=%d body=%s", w.Code, w.Body.String())
	}

	var response struct {
		Code int `json:"code"`
		Data struct {
			Items []struct {
				RuleID         uint   `json:"rule_id"`
				ConfiguredMode string `json:"configured_mode"`
				ActualMode     string `json:"actual_mode"`
				PendingCount   int    `json:"pending_count"`
			} `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析运行状态响应失败: %v, body=%s", err, w.Body.String())
	}
	if response.Code != int(Success) || len(response.Data.Items) != 1 {
		t.Fatalf("运行状态响应 = %+v，期望返回 1 条成功数据", response)
	}
	item := response.Data.Items[0]
	if item.RuleID != rule.ID ||
		item.ConfiguredMode != string(models.DirectoryUploadWatchModePolling) ||
		item.ActualMode != "polling" ||
		item.PendingCount != 0 {
		t.Fatalf("运行状态 item=%+v，期望 polling 规则状态", item)
	}
}

func TestScanDirectoryUploadSyncPathRulesRejectsDisabledRules(t *testing.T) {
	router, syncPath := setupDirectoryUploadControllerTest(t)
	monitorPath := t.TempDir()
	if err := os.WriteFile(filepath.Join(monitorPath, "movie.mkv"), []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入测试视频失败: %v", err)
	}
	rule := &models.DirectoryUploadRule{
		SyncPathId:               syncPath.ID,
		AccountId:                syncPath.AccountId,
		Enabled:                  true,
		MonitorPath:              monitorPath,
		RemoteRootPath:           "/remote",
		RemoteRootId:             "remote-root",
		Recursive:                true,
		WatchMode:                models.DirectoryUploadWatchModePolling,
		ProcessedCacheTTLSeconds: 600,
	}
	if err := db.Db.Create(rule).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}
	rule.Enabled = false
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("停用目录上传规则失败: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/sync-paths/%d/scan", syncPath.ID), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"code":500`) ||
		!strings.Contains(w.Body.String(), "未启用") {
		t.Fatalf("停用规则扫描响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestDirectoryUploadControllerRejectsInvalidID(t *testing.T) {
	router, _ := setupDirectoryUploadControllerTest(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync-paths/abc/scan", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("非法 ID 状态码 = %d，期望 %d", w.Code, http.StatusBadRequest)
	}
}

func TestScanDirectoryUploadSyncPathRulesWithMissingSyncPath(t *testing.T) {
	router, _ := setupDirectoryUploadControllerTest(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sync-paths/999/scan", nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("缺失同步目录状态码 = %d，期望 %d", w.Code, http.StatusNotFound)
	}
}

func TestDirectoryUploadRuleScanIgnoresFutureMtime(t *testing.T) {
	router, syncPath := setupDirectoryUploadControllerTest(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "future.mkv")
	if err := os.WriteFile(filePath, []byte("movie"), 0o644); err != nil {
		t.Fatalf("写入测试视频失败: %v", err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(filePath, future, future); err != nil {
		t.Fatalf("设置测试视频 mtime 失败: %v", err)
	}
	if err := db.Db.Create(&models.DirectoryUploadRule{
		SyncPathId:                    syncPath.ID,
		AccountId:                     syncPath.AccountId,
		Enabled:                       true,
		MonitorPath:                   monitorPath,
		RemoteRootPath:                "/remote",
		RemoteRootId:                  "remote-root",
		Recursive:                     true,
		WatchMode:                     models.DirectoryUploadWatchModePolling,
		StabilitySeconds:              0,
		StabilityCheckIntervalSeconds: 1,
		StabilityRequiredCount:        1,
		ProcessedCacheTTLSeconds:      600,
	}).Error; err != nil {
		t.Fatalf("创建目录上传规则失败: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/sync-paths/%d/scan", syncPath.ID), nil)
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted":1`) {
		t.Fatalf("扫描响应异常: code=%d body=%s", w.Code, w.Body.String())
	}
}
