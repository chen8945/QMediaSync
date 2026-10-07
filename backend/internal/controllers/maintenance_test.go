package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/backup"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func TestRestoreReceiptReadsStatusWithoutDatabaseAuthentication(t *testing.T) {
	oldDB := db.Db
	db.Db = nil
	t.Cleanup(func() { db.Db = oldDB })
	receipt, err := backup.StartRestoreWithReceipt(filepath.Join(t.TempDir(), "missing.zip"), false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for backup.IsRunning() {
		select {
		case <-deadline:
			t.Fatal("restore did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	router := gin.New()
	router.Use(RestoreMaintenanceMiddleware())
	authCalls := 0
	router.GET("/api/backup/status", func(c *gin.Context) { authCalls++; c.AbortWithStatus(http.StatusUnauthorized) })
	router.POST("/api/write", func(c *gin.Context) { authCalls++; c.AbortWithStatus(http.StatusUnauthorized) })
	request := httptest.NewRequest(http.MethodGet, "/api/backup/status", nil)
	request.Header.Set("X-Restore-Receipt", receipt)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var body APIResponse[backup.BackupOrRestoreResult]
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Data.Status != "failed" || authCalls != 0 {
		t.Fatalf("status=%d body=%s auth=%d", response.Code, response.Body, authCalls)
	}
	if strings.Contains(response.Body.String(), receipt) || response.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatal("receipt response leaked its credential or was cacheable")
	}
	for _, path := range []string{"/api/backup/status", "/api/write"} {
		method := http.MethodGet
		if path == "/api/write" {
			method = http.MethodPost
		}
		request := httptest.NewRequest(method, path, nil)
		request.Header.Set("X-Restore-Receipt", receipt+"invalid")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("receipt bypassed unrelated permission: %d", response.Code)
		}
		if path == "/api/backup/status" && !strings.Contains(response.Body.String(), errorCodeRestoreReceiptInvalid) {
			t.Fatalf("invalid receipt needs a distinct application error: %s", response.Body)
		}
	}
	if authCalls != 1 {
		t.Fatalf("receipt granted business permission or invalid receipt queried auth: %d", authCalls)
	}
	for _, scenario := range []struct {
		name, method, receipt, origin string
		status                        int
		errorCode                     string
	}{
		{"missing receipt", http.MethodPost, "", "http://example.com", http.StatusUnauthorized, errorCodeRestoreReceiptInvalid},
		{"wrong receipt", http.MethodPost, receipt + "invalid", "http://example.com", http.StatusUnauthorized, errorCodeRestoreReceiptInvalid},
		{"preflight failed", http.MethodPost, receipt, "http://example.com", http.StatusConflict, "RESTORE_RESTART_NOT_READY"},
		{"cross origin", http.MethodPost, receipt, "https://untrusted.invalid", http.StatusForbidden, ErrorCodeRequestOriginInvalid},
		{"missing origin", http.MethodPost, receipt, "", http.StatusForbidden, ErrorCodeRequestOriginInvalid},
		{"get cannot restart", http.MethodGet, receipt, "http://example.com", http.StatusMethodNotAllowed, ""},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			before := *backup.GetRunningResult()
			request := httptest.NewRequest(scenario.method, "/api/backup/restart", nil)
			request.Header.Set("X-Restore-Receipt", scenario.receipt)
			request.Header.Set("Origin", scenario.origin)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != scenario.status || (scenario.errorCode != "" && !strings.Contains(response.Body.String(), scenario.errorCode)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if *backup.GetRunningResult() != before || authCalls != 1 {
				t.Fatal("restart rejection changed restore result or reached database authentication")
			}
		})
	}
}

func TestRestoreRestartResponsesAndTrustedOrigins(t *testing.T) {
	oldConfig := helpers.GlobalConfig
	helpers.GlobalConfig.TrustedOrigins = []string{"https://trusted.example"}
	t.Cleanup(func() { helpers.GlobalConfig = oldConfig })
	for _, scenario := range []struct {
		name, origin string
		err          error
		status       int
		errorCode    string
	}{
		{"same origin", "http://example.com", nil, http.StatusOK, ""},
		{"trusted origin", "https://trusted.example", nil, http.StatusOK, ""},
		{"untrusted origin", "https://untrusted.example", nil, http.StatusForbidden, ErrorCodeRequestOriginInvalid},
		{"not ready", "http://example.com", backup.ErrRestartNotReady, http.StatusConflict, "RESTORE_RESTART_NOT_READY"},
		{"unsupported", "http://example.com", backup.ErrRestartUnsupported, http.StatusServiceUnavailable, "RESTORE_RESTART_UNSUPPORTED"},
		{"preparation failure", "http://example.com", errors.New("private-path-and-secret"), http.StatusServiceUnavailable, "RESTORE_RESTART_PREPARATION_FAILED"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			before := *backup.GetRunningResult()
			calls := 0
			router := gin.New()
			router.POST("/api/backup/restart", func(c *gin.Context) {
				restartAfterRestore(c, func(receipt string) error {
					calls++
					if receipt != "current-receipt" {
						t.Fatalf("receipt not passed to authorization: %q", receipt)
					}
					return scenario.err
				})
			})
			request := httptest.NewRequest(http.MethodPost, "/api/backup/restart", nil)
			request.Header.Set("Origin", scenario.origin)
			request.Header.Set("X-Restore-Receipt", "current-receipt")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != scenario.status || strings.Contains(response.Body.String(), "private-path-and-secret") || strings.Contains(response.Body.String(), "current-receipt") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if response.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatal("restart response is cacheable")
			}
			if (scenario.status == http.StatusForbidden && calls != 0) || (scenario.status != http.StatusForbidden && calls != 1) {
				t.Fatalf("unexpected restart calls: %d", calls)
			}
			if *backup.GetRunningResult() != before {
				t.Fatal("restart response changed restore result")
			}
			var body APIResponse[map[string]any]
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.ErrorCode != scenario.errorCode {
				t.Fatalf("error_code=%q, want=%q", body.ErrorCode, scenario.errorCode)
			}
			if scenario.status == http.StatusOK {
				for _, field := range []string{"restart_requested", "restart_supported", "status"} {
					if _, ok := body.Data[field]; !ok {
						t.Fatalf("restart response missing %s", field)
					}
				}
			}
		})
	}
}

func TestRestoreMaintenanceRejectsBusinessRequestsBeforeAuth(t *testing.T) {
	pool, err := db.OpenMaintenanceSQL(sqlite.DriverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	database, err := gorm.Open(sqlite.Dialector{Conn: pool}, &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	oldDB := db.Db
	db.Db = database
	t.Cleanup(func() { db.Db = oldDB })
	if _, err := db.BeginMaintenance(context.Background(), database); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(RestoreMaintenanceMiddleware())
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.POST("/api/write", func(c *gin.Context) { t.Error("maintenance request reached authentication/business handler") })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/write", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "DATABASE_MAINTENANCE") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("static maintenance page unavailable: %d", response.Code)
	}
}
