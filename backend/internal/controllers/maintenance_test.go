package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/backup"
	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

// enterMaintenance 让协调器进入维护屏障，并返回退出维护的清理函数。
func enterMaintenance(t *testing.T) (backup.OperationGrant, func()) {
	t.Helper()
	if helpers.ConfigDir == "" {
		helpers.ConfigDir = t.TempDir()
	}
	coordinator, err := backup.Coordinator()
	if err != nil {
		t.Fatalf("Coordinator() error = %v", err)
	}
	grant, err := coordinator.Begin(backup.OperationKindBackup, true)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := coordinator.SetMaintenance(grant.OperationID, true); err != nil {
		t.Fatalf("SetMaintenance() error = %v", err)
	}
	return grant, func() {
		if err := coordinator.Transition(grant.OperationID, backup.OperationTransition{
			State: backup.OperationStateCancelled,
		}); err != nil {
			t.Fatalf("Transition(cancelled) error = %v", err)
		}
	}
}

func newMaintenanceRouter() *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(MaintenanceMiddleware())
	router.GET(BackupStatusPath, GetBackupStatus)
	for _, path := range []string{"/api/backup/list", "/api/backup/download/1", "/api/login", "/emby/webhook"} {
		router.GET(path, func(c *gin.Context) {
			c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "ok"})
		})
	}
	for _, path := range []string{"/api/backup/create", "/api/backup/restore", "/api/backup/upload-restore"} {
		router.POST(path, func(c *gin.Context) {
			c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "ok"})
		})
	}
	router.GET("/assets/index.js", func(c *gin.Context) { c.String(http.StatusOK, "asset") })
	return router
}

// TestMaintenanceMiddlewareBlocksBusinessApisAndKeepsStatusReadable 覆盖维护期契约：
// 业务 API（含备份列表与下载）、登录和 Webhook 一律 503，只有静态资源和状态查询可用。
func TestMaintenanceMiddlewareBlocksBusinessApisAndKeepsStatusReadable(t *testing.T) {
	grant, leaveMaintenance := enterMaintenance(t)
	defer leaveMaintenance()
	router := newMaintenanceRouter()

	for _, requestCase := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/backup/list"},
		{method: http.MethodGet, path: "/api/backup/download/1"},
		{method: http.MethodPost, path: "/api/backup/create"},
		{method: http.MethodPost, path: "/api/backup/restore"},
		{method: http.MethodPost, path: "/api/backup/upload-restore"},
		{method: http.MethodPost, path: BackupStatusPath},
		{method: http.MethodGet, path: "/api/login"},
		{method: http.MethodGet, path: "/emby/webhook"},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(requestCase.method, requestCase.path, nil))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s 维护期状态码 = %d, want 503", requestCase.method, requestCase.path, recorder.Code)
		}
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/assets/index.js", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("静态资源状态码 = %d, want 200", recorder.Code)
	}

	request := httptest.NewRequest(http.MethodGet, BackupStatusPath+"?operation_id="+grant.OperationID, nil)
	request.Header.Set(BackupOperationTokenHeader, grant.Token)
	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("状态查询状态码 = %d, want 200", recorder.Code)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("状态响应 Cache-Control = %q, want no-store", recorder.Header().Get("Cache-Control"))
	}

	var response APIResponse[backup.OperationView]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析状态响应失败：%v", err)
	}
	if response.Data.OperationID != grant.OperationID || response.Data.Kind != backup.OperationKindBackup {
		t.Fatalf("状态响应 = %+v, want 当前备份操作", response.Data)
	}
}

// TestBackupOperationEndpointsConflictBeforeMaintenance 验证协调器已取得执行权、
// 但维护屏障尚未通过全局中间件裁决时，手动备份与两种恢复入口都统一返回 HTTP 409。
func TestBackupOperationEndpointsConflictBeforeMaintenance(t *testing.T) {
	if helpers.ConfigDir == "" {
		helpers.ConfigDir = t.TempDir()
	}
	coordinator, err := backup.Coordinator()
	if err != nil {
		t.Fatalf("Coordinator() error = %v", err)
	}
	grant, err := coordinator.Begin(backup.OperationKindBackup, true)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	t.Cleanup(func() {
		if err := coordinator.Transition(grant.OperationID, backup.OperationTransition{State: backup.OperationStateCancelled}); err != nil {
			t.Fatalf("Transition(cancelled) error = %v", err)
		}
	})

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/api/backup/create", CreateBackup)
	router.POST("/api/backup/restore", RestoreFromBackup)
	router.POST("/api/backup/upload-restore", UploadAndRestore)

	for _, testCase := range []struct {
		name        string
		path        string
		contentType string
		body        string
	}{
		{name: "manual backup", path: "/api/backup/create", contentType: "application/json", body: `{"confirm_unencrypted":true}`},
		{name: "saved artifact preflight", path: "/api/backup/restore", contentType: "application/json", body: `{"phase":"preflight","record_id":1}`},
		{name: "upload preflight", path: "/api/backup/upload-restore", contentType: "application/x-www-form-urlencoded", body: "phase=preflight"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader(testCase.body))
			request.Header.Set("Content-Type", testCase.contentType)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusConflict {
				t.Fatalf("状态码 = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// TestBackupStatusRequiresOperationIDAndToken 覆盖仅诊断模式的边界：
// 缺少 operation ID 或令牌时绝不返回任何状态，也不泄露操作是否存在。
func TestBackupStatusRequiresOperationIDAndToken(t *testing.T) {
	grant, leaveMaintenance := enterMaintenance(t)
	defer leaveMaintenance()
	router := newMaintenanceRouter()

	cases := []struct {
		name        string
		operationID string
		token       string
	}{
		{name: "缺少令牌", operationID: grant.OperationID},
		{name: "令牌错误", operationID: grant.OperationID, token: "incorrect-token"},
		{name: "缺少操作标识", token: grant.Token},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, BackupStatusPath+"?operation_id="+testCase.operationID, nil)
			if testCase.token != "" {
				request.Header.Set(BackupOperationTokenHeader, testCase.token)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			var response APIResponse[any]
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("解析状态响应失败：%v", err)
			}
			if response.Code != BadRequest || response.Data != nil {
				t.Fatalf("响应 = %+v, want 拒绝且不含数据", response)
			}
		})
	}
}

// TestRollbackFailedDiagnosticStatusStillRequiresOperationIDAndToken 覆盖仅诊断模式：
// 自动回滚失败时也不能因为需要人工排障而放宽 operation ID 或请求头令牌校验。
func TestRollbackFailedDiagnosticStatusStillRequiresOperationIDAndToken(t *testing.T) {
	coordinator, err := backup.Coordinator()
	if err != nil {
		t.Fatalf("Coordinator() error = %v", err)
	}
	grant, err := coordinator.Begin(backup.OperationKindRestore, true)
	if err != nil {
		t.Fatalf("Begin(restore) error = %v", err)
	}
	if err := coordinator.Transition(grant.OperationID, backup.OperationTransition{
		State:         backup.OperationStateFailed,
		ErrorCode:     backup.OperationErrorCodeRestoreFailed,
		RollbackState: backup.RollbackStateFailed,
	}); err != nil {
		t.Fatalf("Transition(rollback failed) error = %v", err)
	}

	router := newMaintenanceRouter()
	for _, test := range []struct {
		name        string
		operationID string
		token       string
	}{
		{name: "缺少操作标识", token: grant.Token},
		{name: "缺少请求头令牌", operationID: grant.OperationID},
		{name: "请求头令牌错误", operationID: grant.OperationID, token: "incorrect-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, BackupStatusPath+"?operation_id="+test.operationID, nil)
			if test.token != "" {
				request.Header.Set(BackupOperationTokenHeader, test.token)
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			var response APIResponse[any]
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("解析状态响应失败：%v", err)
			}
			if response.Code != BadRequest || response.Data != nil {
				t.Fatalf("响应 = %+v, want 拒绝且不含诊断状态", response)
			}
		})
	}

	request := httptest.NewRequest(http.MethodGet, BackupStatusPath+"?operation_id="+grant.OperationID, nil)
	request.Header.Set(BackupOperationTokenHeader, grant.Token)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var response APIResponse[backup.OperationView]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("解析授权状态响应失败：%v", err)
	}
	if response.Code != Success || response.Data.RollbackState != backup.RollbackStateFailed {
		t.Fatalf("授权状态响应 = %+v, want rollback_state=failed", response)
	}
}
