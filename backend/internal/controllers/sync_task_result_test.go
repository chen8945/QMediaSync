package controllers

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"

	"github.com/gin-gonic/gin"
)

func TestSyncTaskResultAndFailurePagination(t *testing.T) {
	db := setupControllerTestDB(t, &models.Sync{})
	failures := make([]realtime.SyncScanFailure, 201)
	for i := range failures {
		failures[i] = realtime.SyncScanFailure{Kind: "directory", Path: fmt.Sprintf("/dir/%d", i), Reason: "read failed"}
	}
	task := &models.Sync{Status: models.SyncStatusIncomplete, ScanResult: &realtime.SyncScanResult{FailedFiles: 0, Failures: failures, CleanupStatus: "partial"}}
	if err := db.Create(task).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/tasks/:id/result", GetSyncTaskResult)
	router.GET("/tasks/:id/failures", GetSyncTaskFailures)
	get := func(suffix string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", fmt.Sprintf("/tasks/%d/%s", task.ID, suffix), nil))
		return response
	}
	result := get("result")
	if result.Code != 200 || !strings.Contains(result.Body.String(), `"cleanup_status":"partial"`) || strings.Contains(result.Body.String(), "failures") {
		t.Fatal(result.Body.String())
	}
	page := get("failures?offset=200&limit=100")
	var body httpapi.APIResponse[struct {
		Entries  []realtime.SyncScanFailure `json:"entries"`
		Total    int                        `json:"total"`
		Recorded bool                       `json:"recorded"`
	}]
	if err := json.Unmarshal(page.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if page.Code != 200 || body.Data.Total != 201 || !body.Data.Recorded || len(body.Data.Entries) != 1 || body.Data.Entries[0].Path != "/dir/200" {
		t.Fatal(page.Body.String())
	}
	for _, query := range []string{"offset=-1", "limit=201", "limit=0"} {
		if response := get("failures?" + query); response.Code != 400 {
			t.Fatalf("%s: status=%d", query, response.Code)
		}
	}
}
