package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/controllers"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func readHTTPResponse[T any](t *testing.T, response *httptest.ResponseRecorder) httpapi.APIResponse[T] {
	t.Helper()
	var result httpapi.APIResponse[T]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("invalid API response: %s (%v)", response.Body, err)
	}
	if result.Status != response.Code {
		t.Fatalf("body status %d differs from HTTP %d: %s", result.Status, response.Code, response.Body)
	}
	return result
}

func testMainRouter(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	oldRoot := helpers.RootDir
	helpers.RootDir = t.TempDir()
	t.Cleanup(func() { helpers.RootDir = oldRoot })
	staticDir := filepath.Join(helpers.RootDir, "web_statics")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(controllers.Cors())
	setRouter(router, controllers.NewMediaLibraryConfigController(nil))
	return router
}

func TestMainRouterFrameworkResponsesRespectProtocol(t *testing.T) {
	router := testMainRouter(t)
	for _, tc := range []struct {
		method, path, code, allow string
		status                    int
		json                      bool
	}{
		{http.MethodGet, "/api/missing", "RESOURCE_NOT_FOUND", "", 404, true},
		{http.MethodGet, "/api/login", "METHOD_NOT_ALLOWED", "POST", 405, true},
		{http.MethodPost, "/api/sync/tasks/123/stream", "", "", 404, false},
		{http.MethodHead, "/api/login", "", "", 404, false},
		{http.MethodPost, "/", "", "", 404, false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status || response.Header().Get("Allow") != tc.allow {
				t.Fatalf("HTTP=%d headers=%v body=%s", response.Code, response.Header(), response.Body)
			}
			if tc.json {
				body := readHTTPResponse[any](t, response)
				if body.ErrorCode != tc.code || body.Instance == "" || body.Data != nil {
					t.Fatalf("framework response lost failure contract: %s", response.Body)
				}
			} else if strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("native route was wrapped in JSON: %s", response.Body)
			}
		})
	}
}

func TestMainRouterOptionsStopsBeforeAuthMaintenanceAndFramework(t *testing.T) {
	oldConfig := helpers.GlobalConfig
	helpers.GlobalConfig = helpers.Config{}
	t.Cleanup(func() { helpers.GlobalConfig = oldConfig })
	router := testMainRouter(t)
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
	for _, maintenance := range []bool{false, true} {
		if maintenance {
			if _, err := db.BeginMaintenance(context.Background(), database); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range []string{"/api/session", "/api/user/info", "/api/login", "/api/missing", "/api/backup/restart"} {
			request := httptest.NewRequest(http.MethodOptions, path, nil)
			request.Header.Set("Origin", "http://example.com")
			request.Header.Set("Access-Control-Request-Method", http.MethodPost)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Access-Control-Allow-Origin") != "http://example.com" || response.Header().Get("Access-Control-Allow-Credentials") != "true" {
				t.Fatalf("maintenance=%t path=%s response=%d %v %s", maintenance, path, response.Code, response.Header(), response.Body)
			}
		}
	}
}

func TestJSONPanicBeforeAndAfterResponse(t *testing.T) {
	oldLogger, oldLevel := helpers.AppLogger, helpers.ConfiguredLogLevel()
	var logs bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
	helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
	t.Cleanup(func() {
		helpers.AppLogger = oldLogger
		helpers.SetGlobalLogLevel(oldLevel)
	})
	router := gin.New()
	configureHTTPResponses(router)
	router.GET("/api/panic", func(c *gin.Context) {
		triggerJSONTestPanic(errors.New("handler calculation failed: password=private-panic-secret"))
	})
	router.GET("/api/written", func(c *gin.Context) {
		httpapi.Success(c, http.StatusOK, false, "")
		triggerJSONTestPanic("post-response cleanup failed: password=private-panic-secret")
	})
	for _, tc := range []struct {
		name, path, detail string
		status             int
	}{
		{"error before response", "/api/panic", "handler calculation failed", http.StatusInternalServerError},
		{"string after response", "/api/written", "post-response cleanup failed", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs.Reset()
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
			body := readHTTPResponse[any](t, response)
			if body.Status != tc.status {
				t.Fatalf("panic changed response status: %s", response.Body)
			}
			if tc.status == http.StatusOK {
				if body.Data != false || body.ErrorCode != "" || body.Instance != "" {
					t.Fatalf("panic changed an already written response: %s", response.Body)
				}
			} else if body.ErrorCode != "INTERNAL_ERROR" || body.Instance == "" || !strings.Contains(logs.String(), body.Instance) {
				t.Fatalf("panic response lost its correlated failure: %s logs=%s", response.Body, &logs)
			}
			for _, private := range []string{"private-panic-secret", tc.detail, "goroutine", "triggerJSONTestPanic"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatalf("panic details reached client: %s", response.Body)
				}
			}
			if strings.Contains(logs.String(), "private-panic-secret") || strings.Count(logs.String(), "[ERROR]") != 1 || strings.Count(logs.String(), "\n") != 1 {
				t.Fatalf("panic diagnostics leaked credentials or duplicated output: %s", &logs)
			}
			for _, diagnostic := range []string{tc.detail, `\ngoroutine `, "triggerJSONTestPanic", "http_response_test.go:", "/request-errors/"} {
				if !strings.Contains(logs.String(), diagnostic) {
					t.Fatalf("panic diagnostics lost %q: %s", diagnostic, &logs)
				}
			}
		})
	}
}

func triggerJSONTestPanic(value any) { panic(value) }

func TestHTTPPanicCausePreservesErrorChain(t *testing.T) {
	original := &os.PathError{Op: "open", Path: "/media/movie.mkv", Err: os.ErrPermission}
	cause := httpPanicCause(original, []byte("test stack"))
	if !errors.Is(cause, os.ErrPermission) {
		t.Fatalf("panic lost wrapped permission cause: %v", cause)
	}
	if got, ok := errors.AsType[*os.PathError](cause); !ok || got != original || !strings.Contains(cause.Error(), "test stack") {
		t.Fatalf("panic lost original typed cause or stack: %v", cause)
	}
}

func TestInitialConfigFailureUsesSafeStartupDiagnostics(t *testing.T) {
	oldLogger, oldLevel := helpers.AppLogger, helpers.ConfiguredLogLevel()
	helpers.AppLogger = nil
	helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
	var logs bytes.Buffer
	oldOutput := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() {
		helpers.AppLogger = oldLogger
		helpers.SetGlobalLogLevel(oldLevel)
		log.SetOutput(oldOutput)
	})
	router := newConfigRouter(os.DirFS("../.."), func() (bool, error) { t.Fatal("invalid request prepared restart"); return false, nil }, func() { t.Fatal("invalid request exited") })
	request := httptest.NewRequest(http.MethodPost, "/api/config/save", strings.NewReader(`{"engine":123,"password":"private-startup-secret"}`))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	body := readHTTPResponse[any](t, response)
	if body.Status != http.StatusBadRequest || body.ErrorCode != "INVALID_REQUEST" || body.Instance == "" || !strings.Contains(logs.String(), body.Instance) || strings.Contains(logs.String()+response.Body.String(), "private-startup-secret") {
		t.Fatalf("startup diagnostics mismatch: %s logs=%s", response.Body, &logs)
	}
	if response.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatal("configuration failure is cacheable")
	}
}
