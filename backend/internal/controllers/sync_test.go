package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
)

func TestTaskStartRejectsDuplicate(t *testing.T) {
	for _, test := range []struct {
		name, path, body string
		handler          gin.HandlerFunc
		taskType         synccron.SyncTaskType
	}{
		{name: "saved sync", path: "/api/sync/path/start", body: `{"id":7}`, handler: StartSyncByPath, taskType: synccron.SyncTaskTypeStrm},
		{name: "full sync", path: "/api/sync/path/full-start", body: `{"id":7}`, handler: FullStart115Sync, taskType: synccron.SyncTaskTypeStrm},
		{name: "manual sync", path: "/api/sync/manual", body: `{"account_id":3,"path_id":"remote-7","path":"/movies/movie.mkv","target_path":"/strm","is_file":true}`, handler: ManualSync, taskType: synccron.SyncTaskTypeStrm},
		{name: "scrape scan", path: "/api/scrape/pathes/start", body: `{"id":7}`, handler: ScanScrapePath, taskType: synccron.SyncTaskTypeScrape},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := setupControllerTestDB(t, &models.Account{}, &models.SyncPath{}, &models.ScrapePath{})
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := sqlDB.Close(); err != nil {
					t.Error(err)
				}
			})
			for _, row := range []any{
				&models.Account{BaseModel: models.BaseModel{ID: 3}, SourceType: models.SourceType115},
				&models.SyncPath{BaseModel: models.BaseModel{ID: 7}, AccountId: 3, SourceType: models.SourceType115},
				&models.ScrapePath{BaseModel: models.BaseModel{ID: 7}, AccountId: 3, SourceType: models.SourceType115, VideoExt: "[]"},
			} {
				if err := conn.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			manager := setupPausedTaskQueue(t, models.SourceType115)
			router := gin.New()
			router.POST(test.path, test.handler)
			send := func() *httptest.ResponseRecorder {
				request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
				request.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				return response
			}
			first := send()
			var accepted httpapi.APIResponse[map[string]any]
			if err := json.Unmarshal(first.Body.Bytes(), &accepted); err != nil {
				t.Fatal(err)
			}
			if first.Code != http.StatusAccepted || accepted.Status != first.Code || accepted.ErrorCode != "" {
				t.Fatalf("first submission status=%d response=%s", first.Code, first.Body)
			}
			switch test.name {
			case "saved sync":
				if accepted.Data["id"] != float64(7) || accepted.Data["is_running"] != float64(synccron.TaskStatusWaiting) {
					t.Fatalf("missing saved task identity: %s", first.Body)
				}
			case "scrape scan":
				if accepted.Data["task_id"] != float64(7) || accepted.Data["status"] != "submitted" {
					t.Fatalf("missing scrape task identity: %s", first.Body)
				}
			default:
				if accepted.Data["status"] != "accepted" {
					t.Fatalf("missing admission result: %s", first.Body)
				}
			}
			duplicate := send()
			var conflict httpapi.APIResponse[any]
			if err := json.Unmarshal(duplicate.Body.Bytes(), &conflict); err != nil {
				t.Fatal(err)
			}
			if duplicate.Code != http.StatusConflict || conflict.Status != duplicate.Code || conflict.ErrorCode != "TASK_ALREADY_EXISTS" || conflict.Data != nil {
				t.Errorf("duplicate submission status=%d response=%s", duplicate.Code, duplicate.Body)
			}
			if !strings.Contains(conflict.Message, "排队") || !strings.Contains(conflict.Message, "运行") || conflict.Instance == "" {
				t.Errorf("missing safe duplicate-task explanation: %s", duplicate.Body)
			}
			status := manager.GetQueueStatus(models.SourceType115)
			if status["waiting_count"] != 1 || status["status"] != synccron.QueueStatusPaused || status["is_running"] != false || status["current_task_id"] != uint(0) {
				t.Fatalf("duplicate changed queued work: %+v", status)
			}
			if test.name != "manual sync" && manager.CheckTaskStatus(7, test.taskType) != synccron.TaskStatusWaiting {
				t.Fatal("original task no longer waiting")
			}
			if test.name == "full sync" {
				var path models.SyncPath
				if err := conn.First(&path, 7).Error; err != nil {
					t.Fatal(err)
				}
				if !path.IsFullSync {
					t.Fatal("full-sync flag was cleared after admission conflict")
				}
			}
		})
	}
}

func TestTaskStartQueueFullRemainsInternalError(t *testing.T) {
	conn := setupControllerTestDB(t, &models.SyncPath{})
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := conn.Create(&models.SyncPath{BaseModel: models.BaseModel{ID: 99}, SourceType: models.SourceType115}).Error; err != nil {
		t.Fatal(err)
	}
	manager := setupPausedTaskQueue(t, models.SourceType115)
	for id := uint(1); id <= 50; id++ {
		if err := synccron.AddNewSyncTask(&synccron.NewSyncTask{ID: id, TaskType: synccron.SyncTaskTypeStrm, SourceType: models.SourceType115}); err != nil {
			t.Fatal(err)
		}
	}
	router := gin.New()
	router.POST("/api/sync/path/start", StartSyncByPath)
	request := httptest.NewRequest(http.MethodPost, "/api/sync/path/start", strings.NewReader(`{"id":99}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var failure httpapi.APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || failure.Status != response.Code || failure.ErrorCode != "INTERNAL_ERROR" || failure.Data != nil {
		t.Fatalf("queue full status=%d response=%s", response.Code, response.Body)
	}
	if failure.Message == "" || strings.Contains(failure.Message, "ID=") || strings.Contains(failure.Message, "类型=") {
		t.Fatalf("unsafe queue error: %s", response.Body)
	}
	if count := manager.GetQueueStatus(models.SourceType115)["waiting_count"]; count != 50 {
		t.Fatalf("queue full changed accepted work: count=%v", count)
	}
	if manager.CheckTaskStatus(99, synccron.SyncTaskTypeStrm) != synccron.TaskStatusNone {
		t.Fatal("queue full admitted rejected task")
	}
}

func TestWriteTaskEnqueueError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
		http int
	}{
		{name: "wrapped duplicate", err: fmt.Errorf("internal-private-detail: %w", synccron.ErrTaskAlreadyExists), code: "TASK_ALREADY_EXISTS", http: http.StatusConflict},
		{name: "unknown containing duplicate text", err: errors.New("任务已存在：internal-private-detail"), code: "INTERNAL_ERROR", http: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/enqueue", func(c *gin.Context) {
				writeTaskEnqueueError(c, test.err, "test enqueue failure")
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/enqueue", nil))
			var failure httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.http || failure.Status != response.Code || failure.ErrorCode != test.code || failure.Data != nil || failure.Instance == "" {
				t.Fatalf("enqueue error status=%d response=%s", response.Code, response.Body)
			}
			if failure.Message == "" || strings.Contains(failure.Message, "internal-private-detail") {
				t.Fatalf("unsafe enqueue error message: %s", response.Body)
			}
			if test.http == http.StatusInternalServerError && failure.Message != "test enqueue failure" {
				t.Fatalf("fallback message lost: %s", response.Body)
			}
		})
	}
}

func setupPausedTaskQueue(t *testing.T, sourceType models.SourceType) *synccron.NewSyncQueueManager {
	t.Helper()
	oldManager := synccron.GlobalNewSyncQueueManager
	oldPause, oldResume, oldActive := models.PauseSyncQueuesFunc, models.ResumeSyncQueuesFunc, models.IsStrmSyncTaskActiveFunc
	synccron.GlobalNewSyncQueueManager = nil
	manager := synccron.InitNewSyncQueueManager()
	manager.GetQueueStatus(sourceType)
	manager.PauseAll()
	t.Cleanup(func() {
		synccron.GlobalNewSyncQueueManager = oldManager
		models.PauseSyncQueuesFunc, models.ResumeSyncQueuesFunc, models.IsStrmSyncTaskActiveFunc = oldPause, oldResume, oldActive
	})
	return manager
}
