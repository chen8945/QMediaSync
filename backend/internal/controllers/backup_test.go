package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/backup"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
)

func setupBackupControllerFiles(t *testing.T) string {
	t.Helper()
	previousDir, previousLogger := helpers.ConfigDir, helpers.AppLogger
	helpers.ConfigDir = t.TempDir()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.ConfigDir, helpers.AppLogger = previousDir, previousLogger })
	directory := filepath.Join(helpers.ConfigDir, "backups")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestBackupRecordAvailabilityDoesNotChangeHistory(t *testing.T) {
	database := setupControllerTestDB(t, &models.BackupRecord{}, &models.BackupConfig{})
	directory := setupBackupControllerFiles(t)
	previousService := models.GlobalBackupService
	models.GlobalBackupService = nil
	t.Cleanup(func() { models.GlobalBackupService = previousService })
	existing := filepath.Join(directory, "existing.zip")
	if err := os.WriteFile(existing, []byte("not yet validated"), 0600); err != nil {
		t.Fatal(err)
	}
	records := []models.BackupRecord{
		{Status: models.BackupStatusCompleted, FilePath: filepath.Join(directory, "missing.zip")},
		{Status: models.BackupStatusFailed, FilePath: existing},
		{Status: models.BackupStatusRunning},
	}
	if err := database.Create(&records).Error; err != nil {
		t.Fatal(err)
	}
	wanted := map[uint]string{records[0].ID: "missing", records[1].ID: "available", records[2].ID: "unavailable"}
	router := gin.New()
	router.GET("/list", GetBackupList)
	router.GET("/records/:id", GetBackupRecord)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/list", nil))
	var body httpapi.APIResponse[struct {
		List  []backupRecordResponse `json:"list"`
		Total int64                  `json:"total"`
	}]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != http.StatusOK || body.Data.Total != 3 || len(body.Data.List) != 3 {
		t.Fatalf("unexpected history response: %s", response.Body)
	}
	for _, item := range body.Data.List {
		if item.FileStatus != wanted[item.ID] {
			t.Errorf("record %d file status = %q, want %q", item.ID, item.FileStatus, wanted[item.ID])
		}
	}
	for _, original := range records {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/records/%d", original.ID), nil))
		var detail httpapi.APIResponse[backupRecordResponse]
		if err := json.Unmarshal(response.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Status != http.StatusOK || detail.Data.FileStatus != wanted[original.ID] || detail.Data.Status != original.Status {
			t.Fatalf("unexpected detail response: %s", response.Body)
		}
		var stored models.BackupRecord
		if err := database.First(&stored, original.ID).Error; err != nil || stored.Status != original.Status || stored.CompletedAt != original.CompletedAt {
			t.Fatalf("history reads changed record: %+v, %v", stored, err)
		}
	}
}

func TestBackupRecordDistinguishesMissingRecordFromDatabaseFailure(t *testing.T) {
	database := setupControllerTestDB(t, &models.BackupRecord{})
	setupBackupControllerFiles(t)
	router := gin.New()
	router.GET("/records/:id", GetBackupRecord)
	for _, test := range []struct {
		name, code string
		status     int
		queryError error
	}{
		{name: "missing record", status: http.StatusNotFound, code: "BACKUP_RECORD_NOT_FOUND"},
		{name: "database fault", status: http.StatusInternalServerError, code: "BACKUP_RECORD_READ_FAILED", queryError: errors.New("database secret at /private/backup.db")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.queryError != nil {
				if err := database.Callback().Query().Before("gorm:query").Register("test:backup-query-failure", func(tx *gorm.DB) { tx.AddError(test.queryError) }); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { database.Callback().Query().Remove("test:backup-query-failure") })
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/records/9999", nil))
			var body httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != test.status || body.Status != response.Code || body.ErrorCode != test.code || body.Instance == "" {
				t.Fatalf("record error classification failed: %s, %v", response.Body, err)
			}
			if strings.Contains(response.Body.String(), "/private/backup.db") || strings.Contains(response.Body.String(), "database secret") {
				t.Fatalf("database details leaked: %s", response.Body)
			}
		})
	}
}

func TestBackupFilesReturnsOnlyRootZIPMetadata(t *testing.T) {
	directory := setupBackupControllerFiles(t)
	for name, content := range map[string]string{
		"备份.ZIP": "not a valid archive", "second.zip": "metadata only", "ignored.txt": "text",
	} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, child := range []string{"temp", "directory.zip"} {
		if err := os.Mkdir(filepath.Join(directory, child), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, child, "hidden.zip"), []byte("hidden"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(directory, "second.zip"), filepath.Join(directory, "link.zip")); err != nil {
		t.Logf("symlink assertion unavailable: %v", err)
	}
	router := gin.New()
	router.GET("/files", GetBackupFiles)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/files", nil))
	var body httpapi.APIResponse[backupFilesResponse]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != http.StatusOK || body.Data.Directory != directory || len(body.Data.Files) != 2 {
		t.Fatalf("unexpected files response: %s", response.Body)
	}
	for _, file := range body.Data.Files {
		if file.FileName != "备份.ZIP" && file.FileName != "second.zip" {
			t.Fatalf("unexpected file: %+v", file)
		}
		info, err := os.Stat(filepath.Join(directory, file.FileName))
		if err != nil || file.FileSize != info.Size() || file.ModifiedAt != info.ModTime().Unix() {
			t.Fatalf("unexpected metadata: %+v, %v", file, err)
		}
	}
}

func TestBackupFilesEmptyAndUnreadableDirectory(t *testing.T) {
	directory := setupBackupControllerFiles(t)
	router := gin.New()
	router.GET("/files", GetBackupFiles)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/files", nil))
	var body httpapi.APIResponse[backupFilesResponse]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Status != http.StatusOK || body.Data.Files == nil {
		t.Fatalf("empty listing must be an empty JSON array: %s, %v", response.Body, err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(directory, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/files", nil))
	var failure httpapi.APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != http.StatusInternalServerError || failure.Status != response.Code || failure.ErrorCode != "BACKUP_FILE_LIST_FAILED" || failure.Instance == "" {
		t.Fatalf("directory failure must not masquerade as an empty list: %s, %v", response.Body, err)
	}
}

func TestDownloadBackupChecksFileAvailabilityAndPreservesRanges(t *testing.T) {
	database := setupControllerTestDB(t, &models.BackupRecord{})
	directory := setupBackupControllerFiles(t)
	filePath := filepath.Join(directory, "本地备份.zip")
	if err := os.WriteFile(filePath, []byte("0123456789"), 0600); err != nil {
		t.Fatal(err)
	}
	record := models.BackupRecord{Status: models.BackupStatusFailed, FilePath: filePath}
	if err := database.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/download/:id", DownloadBackup)
	request := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/download/%d", record.ID), nil)
	request.Header.Set("Range", "bytes=2-4")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusPartialContent || response.Body.String() != "234" || response.Header().Get("Content-Disposition") == "" {
		t.Fatalf("available file range download failed: HTTP %d %s", response.Code, response.Body)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.zip")
	if err := os.WriteFile(outsidePath, []byte("outside data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filePath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/download/%d", record.ID), nil))
	var body httpapi.APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Status != http.StatusBadRequest || strings.Contains(response.Body.String(), "outside data") {
		t.Fatalf("symlink must not be downloaded: %s, %v", response.Body, err)
	}
}

func TestBackupStatusExposesSafeRestoreFailure(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "private-backup.zip")
	if err := backup.Restore(missingPath); err == nil {
		t.Fatal("缺失备份文件必须失败")
	}
	router := gin.New()
	router.GET("/api/backup/status", GetBackupStatus)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/backup/status", nil))
	var body httpapi.APIResponse[backup.BackupOrRestoreResult]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Status != http.StatusOK || body.Data.Status != "failed" || body.Data.Type != "restore" || body.Data.IsRunning {
		t.Fatalf("查询应成功返回任务失败终态：HTTP=%d body=%s", response.Code, response.Body)
	}
	if body.Data.ErrorMsg == "" || strings.Contains(response.Body.String(), missingPath) {
		t.Fatalf("状态必须提供安全错误信息：%s", response.Body)
	}
}

func TestRestoreBackupRejectsInvalidOrUnavailableSource(t *testing.T) {
	database := setupControllerTestDB(t, &models.BackupRecord{})
	directory := setupBackupControllerFiles(t)
	record := models.BackupRecord{Status: models.BackupStatusCompleted, FilePath: filepath.Join(directory, "missing.zip")}
	if err := database.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "directory.zip"), 0700); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/restore", RestoreFromBackup)
	for _, scenario := range []struct {
		name, body string
		status     int
	}{
		{"empty", `{}`, http.StatusBadRequest},
		{"both sources", `{"record_id":1,"file_name":"backup.zip"}`, http.StatusBadRequest},
		{"invalid ID", `{"record_id":-1}`, http.StatusBadRequest},
		{"traversal", `{"file_name":"../backup.zip"}`, http.StatusBadRequest},
		{"nested", `{"file_name":"temp/backup.zip"}`, http.StatusBadRequest},
		{"missing filename", `{"file_name":"missing.zip"}`, http.StatusNotFound},
		{"directory", `{"file_name":"directory.zip"}`, http.StatusBadRequest},
		{"missing record", `{"record_id":9999}`, http.StatusNotFound},
		{"missing record file", fmt.Sprintf(`{"record_id":%d}`, record.ID), http.StatusNotFound},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/restore", strings.NewReader(scenario.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			var body httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != scenario.status || body.Status != response.Code || body.ErrorCode == "" || body.Instance == "" || backup.IsRunning() {
				t.Fatalf("invalid source started a restore: %s, %v", response.Body, err)
			}
		})
	}
}

func TestRestoreBackupAcceptsFileAndExistingRecordWithoutTrustingStatus(t *testing.T) {
	database := setupControllerTestDB(t, &models.BackupRecord{})
	directory := setupBackupControllerFiles(t)
	filePath := filepath.Join(directory, "selected.ZIP")
	if err := os.WriteFile(filePath, []byte("invalid archive for async preflight"), 0600); err != nil {
		t.Fatal(err)
	}
	record := models.BackupRecord{Status: models.BackupStatusFailed, FilePath: filePath}
	if err := database.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/restore", RestoreFromBackup)
	for _, scenario := range []struct{ name, body string }{
		{"local file", `{"file_name":"selected.ZIP"}`},
		{"existing record", fmt.Sprintf(`{"record_id":%d}`, record.ID)},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/restore", strings.NewReader(scenario.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(response, request)
			var body httpapi.APIResponse[map[string]string]
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusAccepted || body.Status != response.Code || body.Data["restore_receipt"] == "" {
				t.Fatalf("available source did not start validation: %s, %v", response.Body, err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for backup.IsRunning() {
				if time.Now().After(deadline) {
					t.Fatal("restore preflight did not finish")
				}
				time.Sleep(time.Millisecond)
			}
			result := backup.GetRunningResult()
			if result.Status != models.BackupStatusFailed || result.RestartRequired {
				t.Fatalf("invalid archive should fail before maintenance: %+v", result)
			}
			if _, err := os.Stat(filePath); err != nil {
				t.Fatalf("selected archive was removed: %v", err)
			}
		})
	}
}
