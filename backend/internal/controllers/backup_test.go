package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/backup"
)

func TestBackupStatusExposesSafeRestoreFailure(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "private-backup.zip")
	if err := backup.Restore(missingPath); err == nil {
		t.Fatal("缺失备份文件必须失败")
	}
	router := gin.New()
	router.GET("/api/backup/status", GetBackupStatus)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/backup/status", nil))
	var body APIResponse[backup.BackupOrRestoreResult]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Code != Success || body.Data.Status != "failed" || body.Data.Type != "restore" || body.Data.IsRunning {
		t.Fatalf("查询应成功返回任务失败终态：HTTP=%d body=%s", response.Code, response.Body)
	}
	if body.Data.ErrorMsg == "" || strings.Contains(response.Body.String(), missingPath) {
		t.Fatalf("状态必须提供安全错误信息：%s", response.Body)
	}
}
