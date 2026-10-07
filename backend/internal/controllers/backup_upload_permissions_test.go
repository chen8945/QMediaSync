package controllers

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/backup"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestUploadBackupRejectsSymlinkDirectory(t *testing.T) {
	previousDir := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = previousDir })
	if err := os.Mkdir(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(helpers.ConfigDir, "backups", "temp")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("uploaded data")); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/upload", UploadAndRestore)
	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var result APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != BadRequest || result.Message != "保存上传文件失败" {
		t.Fatalf("unsafe upload directory accepted: %s", response.Body)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("upload wrote outside the managed directory: %v", err)
	}
}

func TestUploadBackupCreatesPrivateArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits")
	}
	database := setupControllerTestDB(t, &models.User{}, &models.ApiKey{}, &models.Migrator{})
	previousDir, previousTables, previousLogger := helpers.ConfigDir, models.AllTables, helpers.AppLogger
	helpers.ConfigDir = t.TempDir()
	models.AllTables = []any{models.User{}, models.ApiKey{}, models.Migrator{}}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		helpers.ConfigDir, models.AllTables, helpers.AppLogger = previousDir, previousTables, previousLogger
	})
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	barrier, err := pool.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var uploaded string
	t.Cleanup(func() {
		_ = barrier.Close()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, statErr := os.Stat(uploaded)
			if !backup.IsRunning() && (uploaded == "" || os.IsNotExist(statErr)) {
				break
			}
			if time.Now().After(deadline) {
				t.Error("uploaded restore did not finish cleanup")
				break
			}
			time.Sleep(time.Millisecond)
		}
		if uploaded != "" {
			result := backup.GetRunningResult()
			if result.Status != models.BackupStatusFailed || result.RestartRequired {
				t.Errorf("test archive must finish preflight without maintenance: %+v", result)
			}
		}
	})
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, content := range map[string]string{
		"User.json":     "{\"id\":1,\"username\":\"backup-admin\",\"password\":\"hash\"}\n",
		"Migrator.json": fmt.Sprintf("{\"id\":1,\"version_code\":%d}\n", models.MaxVersionCode),
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/upload", func(c *gin.Context) { uploadAndRestore(c, int64(archive.Len())) })
	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var result APIResponse[map[string]string]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != Success || result.Data["restore_receipt"] == "" {
		t.Fatalf("upload was not accepted: %s", response.Body)
	}
	// 唯一业务连接被测试持有，异步恢复会在旧包关联检查等待，上传文件尚不能被清理。
	files, err := filepath.Glob(filepath.Join(helpers.ConfigDir, "backups", "temp", "*.zip"))
	if err != nil || len(files) != 1 {
		t.Fatalf("uploaded archive missing: %v", err)
	}
	uploaded = files[0]
	if info, err := os.Stat(uploaded); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("uploaded archive mode must be 0600: %v", err)
	}
	if content, err := os.ReadFile(uploaded); err != nil || !bytes.Equal(content, archive.Bytes()) {
		t.Fatalf("uploaded archive changed: %v", err)
	}
	// 释放后，缺少 ApiKey 关联表的旧包在预检阶段失败，不会进入永久维护。
}
