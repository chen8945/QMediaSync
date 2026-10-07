package controllers

import (
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
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"qmediasync/internal/backup"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"
)

func TestUpdateBackupConfigRemovesDisabledSchedule(t *testing.T) {
	database := setupControllerTestDB(t, &models.BackupConfig{})
	previousDir, previousLogger := helpers.ConfigDir, helpers.AppLogger
	previousService, previousCron := models.GlobalBackupService, synccron.GlobalCron
	previousSettings, previousEmby := models.SettingsGlobal, models.GlobalEmbyConfig
	helpers.ConfigDir = t.TempDir()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.GlobalBackupService, synccron.GlobalCron = nil, nil
	models.SettingsGlobal = &models.Settings{Cron: "0 * * * *"}
	models.GlobalEmbyConfig = &models.EmbyConfig{}
	t.Cleanup(func() {
		if synccron.GlobalCron != nil {
			<-synccron.GlobalCron.Stop().Done()
		}
		models.GlobalBackupService, synccron.GlobalCron = previousService, previousCron
		models.SettingsGlobal, models.GlobalEmbyConfig = previousSettings, previousEmby
		helpers.ConfigDir, helpers.AppLogger = previousDir, previousLogger
	})

	// 这个分钟和小时组合不与 InitCron 的固定任务重合。
	const expression = "17 5 * * *"
	hasBackupSchedule := func() bool {
		for _, entry := range synccron.GlobalCron.Entries() {
			if schedule, ok := entry.Schedule.(*cron.SpecSchedule); ok && schedule.Minute == 1<<17 && schedule.Hour == 1<<5 {
				return true
			}
		}
		return false
	}
	router := gin.New()
	router.PUT("/config", UpdateBackupConfig)
	for _, enabled := range []int{1, 0, 1, 0} {
		request := httptest.NewRequest(http.MethodPut, "/config", strings.NewReader(fmt.Sprintf(
			`{"backup_enabled":%d,"backup_cron":%q,"backup_max_count":10,"backup_compress":1}`, enabled, expression)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		var result APIResponse[any]
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Code != Success {
			t.Fatalf("config update failed: %s, %v", response.Body, err)
		}
		var stored models.BackupConfig
		if err := database.First(&stored).Error; err != nil || stored.BackupEnabled != enabled {
			t.Fatalf("configuration not saved: %+v, %v", stored, err)
		}
		if got := hasBackupSchedule(); got != (enabled == 1) {
			t.Fatalf("backup_enabled=%d, schedule remains=%v", enabled, got)
		}
	}
	var reloads sync.WaitGroup
	for range 8 {
		reloads.Go(synccron.InitCron)
	}
	reloads.Wait()
	if hasBackupSchedule() {
		t.Fatal("concurrent reloads recreated the disabled backup schedule")
	}
}

func TestInitCronDoesNotReplaceDuringMaintenance(t *testing.T) {
	pool, err := db.OpenMaintenanceSQL(sqlite.DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	database, err := gorm.Open(sqlite.Dialector{Conn: pool}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	previousDB, previousCron := db.Db, synccron.GlobalCron
	db.Db, synccron.GlobalCron = database, cron.New()
	t.Cleanup(func() { db.Db, synccron.GlobalCron = previousDB, previousCron })
	if _, err := db.BeginMaintenance(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	before := synccron.GlobalCron
	synccron.InitCron()
	if synccron.GlobalCron != before || len(before.Entries()) != 0 {
		t.Fatal("maintenance must not rebuild a scheduler")
	}
}

func backupMultipartRequest(t *testing.T, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", "backup.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	return request
}

func TestUploadBackupLimitsBeforeAndDuringRead(t *testing.T) {
	const maxArchiveSize = int64(64)
	for _, test := range []struct {
		name          string
		payloadSize   int
		contentLength int64
	}{
		{"advertised overflow", 1, backup.MaxArchiveSize + 1<<20 + 1},
		{"chunked request overflow", 1<<20 + 65, -1},
		{"incorrect content length", 1<<20 + 65, 1},
		{"file exceeds limit within multipart allowance", 65, -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			previousDir := helpers.ConfigDir
			helpers.ConfigDir = t.TempDir()
			t.Cleanup(func() { helpers.ConfigDir = previousDir })
			request := backupMultipartRequest(t, bytes.Repeat([]byte("x"), test.payloadSize))
			request.ContentLength = test.contentLength
			router := gin.New()
			router.POST("/upload", func(c *gin.Context) { uploadAndRestore(c, maxArchiveSize) })
			if test.name == "advertised overflow" {
				// 同时钉住公开入口必须在 multipart 解析前安装总量限制。
				router = gin.New()
				router.POST("/upload", UploadAndRestore)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertBackupUploadLimit(t, response)
			entries, err := os.ReadDir(helpers.ConfigDir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("rejected request created an upload: %v", err)
			}
		})
	}
}

func TestUploadBackupCopyLimitCleansMultipartAndDestination(t *testing.T) {
	previousDir := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = previousDir })
	parserTemp := t.TempDir()
	t.Setenv("TMPDIR", parserTemp)
	request := backupMultipartRequest(t, bytes.Repeat([]byte("x"), 128))
	if err := request.ParseMultipartForm(1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = request.MultipartForm.RemoveAll() })
	entries, err := os.ReadDir(parserTemp)
	if err != nil || len(entries) != 1 {
		t.Fatalf("multipart spill file not created: %v", err)
	}
	// 即使上游已有的 multipart 元数据大小错误，复制过程也不能超过限额。
	request.MultipartForm.File["file"][0].Size = 1
	router := gin.New()
	router.POST("/upload", func(c *gin.Context) { uploadAndRestore(c, 64) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertBackupUploadLimit(t, response)
	for _, directory := range []string{parserTemp, filepath.Join(helpers.ConfigDir, "backups", "temp")} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("rejected upload left temporary files in %s: %v", directory, err)
		}
	}
}

func assertBackupUploadLimit(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var result APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusRequestEntityTooLarge || result.Code != BadRequest || result.ErrorCode != "BACKUP_ARCHIVE_LIMIT" {
		t.Fatalf("unexpected oversized upload response: HTTP %d: %s", response.Code, response.Body)
	}
}
