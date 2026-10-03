package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestStrmResultsQuery(t *testing.T) {
	router, key, syncPath := setupStrmWebhookControllerTest(t)
	router.GET("/api/strm/tasks", JWTAuthMiddleware(), ListStrmResults)
	parent := models.StrmGenerationTask{SyncPathId: syncPath.ID, TaskType: "directory_scan", Status: "completed", TotalItems: 2, AcceptedItems: 1, SkippedItems: 1}
	if err := db.Db.Create(&parent).Error; err != nil {
		t.Fatal(err)
	}
	child := models.StrmGenerationTask{ParentTaskId: parent.ID, SyncPathId: syncPath.ID, UploadTaskId: 123, TaskType: "file", Status: "skipped", SkipReason: "名称被排除", PickCode: "secret", RequestHash: "private"}
	if err := db.Db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&parent).Update("total_items", nil).Error; err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		query string
		auth  bool
		code  int
		count int
	}{
		{"", false, 401, 0}, {"?page_size=101", true, 400, 0}, {"?page=-1", true, 400, 0}, {"?id=abc", true, 400, 0},
		{"", true, 200, 1}, {"?parent_task_id=1", true, 200, 1}, {"?upload_task_id=123", true, 200, 1}, {"?id=2&sync_path_id=999", true, 200, 0}, {"?page=2&page_size=1", true, 200, 0},
		{"?id=2&parent_task_id=999", true, 200, 0}, {"?id=2&parent_task_id=1", true, 200, 1}, {"?sync_path_id=999", true, 200, 0},
	} {
		t.Run(tt.query, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/strm/tasks"+tt.query, nil)
			if tt.auth {
				req.Header.Set("X-API-Key", key)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tt.code {
				t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
			}
			if tt.code != 200 {
				return
			}
			var response struct {
				Data struct {
					Items []map[string]any `json:"items"`
					Total int              `json:"total"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Data.Items) != tt.count {
				t.Fatalf("body=%s", w.Body.String())
			}
			for _, item := range response.Data.Items {
				if item["id"] == float64(parent.ID) && item["total_items"] != nil {
					t.Fatal("historical NULL count became zero")
				}
				if _, ok := item["pick_code"]; ok {
					t.Fatal("exposed pick code")
				}
				if _, ok := item["request_hash"]; ok {
					t.Fatal("exposed request hash")
				}
				if item["status"] == "skipped" && item["skip_reason"] != "名称被排除" {
					t.Fatal(item)
				}
			}
		})
	}
}

func TestStrmResultsCookie(t *testing.T) {
	router, _, session, _ := setupAuthSecurityTest(t)
	if err := db.Db.AutoMigrate(&models.StrmGenerationTask{}); err != nil {
		t.Fatal(err)
	}
	router.GET("/api/strm/tasks", ListStrmResults)
	req := httptest.NewRequest(http.MethodGet, "/api/strm/tasks", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: buildSessionCookieTokenForTest(t, session)})
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}
