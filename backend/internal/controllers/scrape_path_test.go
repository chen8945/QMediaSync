package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
)

func TestScrapePathQueriesDistinguishMissingAndStorageFailure(t *testing.T) {
	for _, test := range []struct {
		name, method, path, body string
		storageFailure, corrupt  bool
		wantStatus               int
	}{
		{name: "missing detail", method: http.MethodGet, path: "/paths/999", wantStatus: http.StatusNotFound},
		{name: "missing edit", method: http.MethodPost, path: "/paths", body: `{"id":999}`, wantStatus: http.StatusNotFound},
		{name: "missing delete", method: http.MethodDelete, path: "/paths/999", wantStatus: http.StatusNotFound},
		{name: "missing scan", method: http.MethodPost, path: "/paths/start", body: `{"id":999}`, wantStatus: http.StatusNotFound},
		{name: "missing toggle", method: http.MethodPost, path: "/paths/toggle", body: `{"id":999}`, wantStatus: http.StatusNotFound},
		{name: "detail database failure", method: http.MethodGet, path: "/paths/999", storageFailure: true, wantStatus: http.StatusInternalServerError},
		{name: "list database failure", method: http.MethodGet, path: "/paths", storageFailure: true, wantStatus: http.StatusInternalServerError},
		{name: "detail decode failure", method: http.MethodGet, path: "/paths/999", corrupt: true, wantStatus: http.StatusInternalServerError},
		{name: "list decode failure", method: http.MethodGet, path: "/paths", corrupt: true, wantStatus: http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := setupControllerTestDB(t, &models.ScrapePath{})
			if test.corrupt {
				if err := conn.Create(&models.ScrapePath{BaseModel: models.BaseModel{ID: 999}, VideoExt: "{invalid"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if test.storageFailure {
				if err := conn.Migrator().DropTable(&models.ScrapePath{}); err != nil {
					t.Fatal(err)
				}
			}
			router := gin.New()
			router.GET("/paths", GetScrapePathes)
			router.GET("/paths/:id", GetScrapePath)
			router.POST("/paths", SaveScrapePath)
			router.DELETE("/paths/:id", DeleteScrapePath)
			router.POST("/paths/start", ScanScrapePath)
			router.POST("/paths/toggle", ToggleScrapePathCron)
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			var result httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.wantStatus || result.Status != response.Code || result.Data != nil {
				t.Fatalf("status=%d response=%s", response.Code, response.Body)
			}
			wantCode := "SCRAPE_PATH_NOT_FOUND"
			if test.storageFailure || test.corrupt {
				wantCode = "INTERNAL_ERROR"
			}
			if result.ErrorCode != wantCode || strings.Contains(result.Message, "no such table") {
				t.Fatalf("failure classification=%+v", result)
			}
		})
	}
}
