package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
)

func TestRepairDatabaseHidesInternalErrors(t *testing.T) {
	database := setupControllerTestDB(t, &models.Migrator{})
	if err := database.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
		t.Fatal(err)
	}
	internalError := "injected database failure at private_schema.migrator"
	if err := database.Callback().Query().Before("gorm:query").Register("test:repair_failure", func(tx *gorm.DB) {
		tx.AddError(errors.New(internalError))
	}); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	originalLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = originalLogger })
	router := gin.New()
	router.POST("/api/database/repair", RepairDB)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/database/repair", nil))
	var body httpapi.APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || body.Status != response.Code || body.ErrorCode != "INTERNAL_ERROR" || body.Instance == "" || body.Data != nil {
		t.Fatalf("repair failure changed response contract: HTTP=%d body=%s", response.Code, response.Body)
	}
	if body.Message == "" || strings.Contains(response.Body.String(), internalError) {
		t.Fatalf("repair exposed internal error: %s", response.Body)
	}
	if !strings.Contains(logs.String(), internalError) || !strings.Contains(logs.String(), body.Instance) {
		t.Fatalf("repair omitted diagnostic log: %s", &logs)
	}
}
