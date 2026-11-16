package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func TestQueueListsDistinguishEmptyResultsFromDatabaseFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		handler gin.HandlerFunc
		model   any
	}{
		{"upload", UploadList, &models.DbUploadTask{}},
		{"download", DownloadList, &models.DbDownloadTask{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn := setupControllerTestDB(t, test.model)
			router := gin.New()
			router.GET("/queue", test.handler)
			perform := func() *httptest.ResponseRecorder {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/queue?status=-1", nil))
				return response
			}
			response := perform()
			var result httpapi.APIResponse[struct {
				Total int64             `json:"total"`
				List  []json.RawMessage `json:"list"`
			}]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || result.Data.Total != 0 || result.Data.List == nil || len(result.Data.List) != 0 {
				t.Fatalf("empty queue = %s", response.Body)
			}
			if err := conn.Callback().Query().Before("gorm:query").Register("fixture_queue_read_failure", func(tx *gorm.DB) {
				tx.AddError(errors.New("private queue database failure"))
			}); err != nil {
				t.Fatal(err)
			}
			assertScrapeFailure(t, perform(), http.StatusInternalServerError, "INTERNAL_ERROR")
		})
	}
}
