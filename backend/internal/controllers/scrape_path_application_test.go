package controllers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
)

func TestScrapePathSavedCronControlsRealQueueAndPreservesManualScan(t *testing.T) {
	<-synccron.StopScrapeCron().Done()
	conn := setupControllerTestDB(t, &models.ScrapePath{}, &models.ScrapePathCategory{}, &models.ScrapeMediaFile{}, &models.Media{}, &models.MediaSeason{}, &models.MediaEpisode{})
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	oldManager, oldLogger := synccron.GlobalNewSyncQueueManager, helpers.AppLogger
	oldPause, oldResume, oldActive := models.PauseSyncQueuesFunc, models.ResumeSyncQueuesFunc, models.IsStrmSyncTaskActiveFunc
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	synccron.GlobalNewSyncQueueManager = nil
	manager := synccron.InitNewSyncQueueManager()
	manager.GetQueueStatus(models.SourceTypeLocal)
	manager.PauseAll()
	t.Cleanup(func() {
		<-synccron.StopScrapeCron().Done()
		synccron.GlobalNewSyncQueueManager, helpers.AppLogger = oldManager, oldLogger
		models.PauseSyncQueuesFunc, models.ResumeSyncQueuesFunc, models.IsStrmSyncTaskActiveFunc = oldPause, oldResume, oldActive
	})
	router := gin.New()
	router.POST("/paths", SaveScrapePath)
	router.POST("/paths/toggle", ToggleScrapePathCron)
	router.POST("/paths/start", ScanScrapePath)
	router.DELETE("/paths/:id", DeleteScrapePath)
	send := func(method, path string, payload any) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		var result struct {
			Code     APIResponseCode `json:"code"`
			Warnings []string        `json:"warnings"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || result.Code != Success || len(result.Warnings) != 0 {
			t.Fatalf("write failed: %d %s", response.Code, response.Body.String())
		}
	}
	payload := map[string]any{
		"source_type": models.SourceTypeLocal, "media_type": models.MediaTypeMovie,
		"scrape_type": models.ScrapeTypeOnly, "rename_type": models.RenameTypeSame,
		"source_path": t.TempDir(), "video_ext_list": []string{".mp4"}, "max_threads": 1,
		"enable_cron": true, "cron_expression": "@every 1s",
	}
	send(http.MethodPost, "/paths", payload)
	var saved models.ScrapePath
	if err := conn.Where("source_path = ?", payload["source_path"]).First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	payload["id"] = saved.ID
	idPayload := map[string]any{"id": saved.ID}
	waitForTask := func() {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if manager.CheckTaskStatus(saved.ID, synccron.SyncTaskTypeScrape) == synccron.TaskStatusWaiting {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("saved enabled cron did not admit a task to the real queue")
	}
	clearTask := func() {
		t.Helper()
		if err := manager.CancelTask(saved.ID, synccron.SyncTaskTypeScrape); err != nil {
			t.Fatal(err)
		}
	}
	assertNoFutureTask := func() {
		t.Helper()
		deadline := time.Now().Add(1200 * time.Millisecond)
		for time.Now().Before(deadline) {
			if count := manager.GetQueueStatus(models.SourceTypeLocal)["waiting_count"].(int); count != 0 {
				t.Fatalf("disabled or deleted cron admitted %d tasks", count)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForTask()
	payload["enable_cron"] = false
	send(http.MethodPost, "/paths", payload)
	clearTask()
	assertNoFutureTask()
	send(http.MethodPost, "/paths/start", idPayload)
	if manager.CheckTaskStatus(saved.ID, synccron.SyncTaskTypeScrape) != synccron.TaskStatusWaiting {
		t.Fatal("manual scan was blocked by disabled cron")
	}
	clearTask()
	payload["enable_cron"] = true
	send(http.MethodPost, "/paths", payload)
	waitForTask()
	send(http.MethodPost, "/paths/toggle", idPayload)
	clearTask()
	assertNoFutureTask()
	send(http.MethodPost, "/paths/toggle", idPayload)
	waitForTask()
	clearTask()
	send(http.MethodDelete, fmt.Sprintf("/paths/%d", saved.ID), nil)
	assertNoFutureTask()
}
