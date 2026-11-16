package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func setupSyncDeleteControllerTestDB(t *testing.T) {
	t.Helper()

	originalConfigDir := helpers.ConfigDir
	originalGlobalConfig := helpers.GlobalConfig
	originalLogger := helpers.AppLogger
	t.Cleanup(func() {
		helpers.ConfigDir = originalConfigDir
		helpers.GlobalConfig = originalGlobalConfig
		helpers.AppLogger = originalLogger
	})

	setupControllerTestDB(t, &models.Sync{})
	helpers.ConfigDir = t.TempDir()
	helpers.GlobalConfig = *helpers.MakeDefaultConfig()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
}

func TestDeleteSyncRecordsByIDs汇总删除结果(t *testing.T) {
	setupSyncDeleteControllerTestDB(t)
	completed := &models.Sync{SyncPathId: 1, Status: models.SyncStatusCompleted, LocalPath: "/local", RemotePath: "/remote"}
	running := &models.Sync{SyncPathId: 1, Status: models.SyncStatusInProgress, LocalPath: "/local", RemotePath: "/remote"}
	if err := db.Db.Create(completed).Error; err != nil {
		t.Fatalf("创建已完成同步记录失败: %v", err)
	}
	if err := db.Db.Create(running).Error; err != nil {
		t.Fatalf("创建进行中同步记录失败: %v", err)
	}

	result := deleteSyncRecordsByIDs([]uint{completed.ID, running.ID, 404})

	if len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != completed.ID {
		t.Fatalf("DeletedIDs = %+v，期望只包含 %d", result.DeletedIDs, completed.ID)
	}
	if len(result.Failures) != 2 {
		t.Fatalf("Failures = %+v，期望 2 条失败", result.Failures)
	}
	if result.Failures[0].ID != running.ID || result.Failures[0].Reason == "" {
		t.Fatalf("第一条失败 = %+v，期望进行中记录失败且有原因", result.Failures[0])
	}
	if result.Failures[1].ID != 404 || result.Failures[1].Reason == "" {
		t.Fatalf("第二条失败 = %+v，期望缺失记录失败且有原因", result.Failures[1])
	}
}

func TestDeleteSyncRecordsHTTPPreservesPartialResults(t *testing.T) {
	setupSyncDeleteControllerTestDB(t)
	if err := db.Db.AutoMigrate(&models.SyncPath{}); err != nil {
		t.Fatal(err)
	}
	oldSettings, oldGlobalCron, oldSyncCron := models.SettingsGlobal, synccron.GlobalCron, synccron.SyncCron
	models.SettingsGlobal = &models.Settings{Cron: "0 0 * * *"}
	synccron.GlobalCron, synccron.SyncCron = nil, nil
	t.Cleanup(func() {
		if synccron.GlobalCron != nil && synccron.GlobalCron != oldGlobalCron {
			synccron.GlobalCron.Stop()
		}
		if synccron.SyncCron != nil && synccron.SyncCron != oldSyncCron {
			synccron.SyncCron.Stop()
		}
		models.SettingsGlobal, synccron.GlobalCron, synccron.SyncCron = oldSettings, oldGlobalCron, oldSyncCron
	})
	completed := &models.Sync{Status: models.SyncStatusCompleted}
	running := &models.Sync{Status: models.SyncStatusInProgress}
	for _, task := range []*models.Sync{completed, running} {
		if err := db.Db.Create(task).Error; err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	router.POST("/sync/delete-records", DelSyncRecords)
	body, err := json.Marshal(map[string]any{"ids": []uint{completed.ID, running.ID, 1000}})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/sync/delete-records", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var response httpapi.APIResponse[deleteSyncRecordsResult]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusConflict || response.Status != http.StatusConflict || response.ErrorCode != "SYNC_RECORD_DELETE_FAILED" || len(response.Data.DeletedIDs) != 1 || response.Data.DeletedIDs[0] != completed.ID || len(response.Data.Failures) != 2 {
		t.Fatalf("partial deletion lost: HTTP=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := models.GetSyncByID(completed.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("completed record was not deleted")
	}
	if _, err := models.GetSyncByID(running.ID); err != nil {
		t.Fatalf("running record was deleted: %v", err)
	}
}
