package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
)

func testContext(writer http.ResponseWriter) *gin.Context {
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/test", nil)
	return c
}

func readResponse(t *testing.T, recorder *httptest.ResponseRecorder) APIResponse[json.RawMessage] {
	t.Helper()
	var response APIResponse[json.RawMessage]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("invalid response JSON: %v; body=%q", err, recorder.Body.String())
	}
	if response.Status != recorder.Code {
		t.Fatalf("body status %d differs from HTTP %d", response.Status, recorder.Code)
	}
	if recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("unexpected content type: %q", recorder.Header().Get("Content-Type"))
	}
	return response
}

func captureLogger(t *testing.T, level helpers.LogLevel) *bytes.Buffer {
	t.Helper()
	oldLogger := helpers.AppLogger
	oldLevel := helpers.ConfiguredLogLevel()
	t.Cleanup(func() {
		helpers.AppLogger = oldLogger
		helpers.SetGlobalLogLevel(oldLevel)
	})
	var output bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	helpers.SetGlobalLogLevel(level)
	return &output
}

func TestSuccessPreservesEmptyData(t *testing.T) {
	cases := []struct {
		name string
		data any
		want string
	}{
		{"null", nil, "null"},
		{"false", false, "false"},
		{"zero", 0, "0"},
		{"empty string", "", `""`},
		{"empty array", []string{}, "[]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			Success(testContext(recorder), http.StatusOK, tc.data, "")
			response := readResponse(t, recorder)
			if string(response.Data) != tc.want {
				t.Fatalf("data=%s, want %s", response.Data, tc.want)
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(recorder.Body.Bytes(), &fields)
			if len(fields) != 2 {
				t.Fatalf("success without feedback should contain status/data, got %s", recorder.Body.String())
			}
		})
	}
}

func TestSuccessPreservesFeedbackAndStatus(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusAccepted} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			recorder := httptest.NewRecorder()
			message, warning := "test feedback", "test application warning"
			Success(testContext(recorder), status, map[string]int{"id": 12}, message, warning)
			response := readResponse(t, recorder)
			if response.Message != message || len(response.Warnings) != 1 || response.Warnings[0] != warning {
				t.Fatalf("feedback not preserved: %+v", response)
			}
			if response.ErrorCode != "" || response.Instance != "" || len(response.FieldErrors) != 0 {
				t.Fatalf("success contains request error fields: %+v", response)
			}
		})
	}
}

func TestHEADPreservesStatusAndDiagnosticsWithoutBody(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			logs := captureLogger(t, helpers.LogLevelInfo)
			recorder := httptest.NewRecorder()
			c := testContext(recorder)
			c.Request.Method = http.MethodHead
			c.Header("Cache-Control", "no-store, private")
			if status == http.StatusOK {
				Success(c, status, false, "")
			} else {
				Failure(c, status, Error{Code: "DOWNLOAD_PROXY_FAILED", Message: "test rejection"})
			}
			if recorder.Code != status || recorder.Body.Len() != 0 || !c.Writer.Written() || recorder.Header().Get("Content-Type") != "application/json; charset=utf-8" || recorder.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatalf("HEAD response = %d %v %q", recorder.Code, recorder.Header(), recorder.Body.String())
			}
			if status == http.StatusBadGateway && (!strings.Contains(logs.String(), "method=HEAD") || !strings.Contains(logs.String(), "status=502") || !strings.Contains(logs.String(), "/request-errors/")) {
				t.Fatalf("HEAD failure lost correlation: %s", logs.String())
			}
			before := logs.String()
			headers := recorder.Header().Clone()
			Success(c, http.StatusCreated, nil, "late feedback")
			if recorder.Code != status || recorder.Body.Len() != 0 || !reflect.DeepEqual(recorder.Header(), headers) || logs.String() != before {
				t.Fatal("HEAD response was written twice")
			}
			logs.Reset()
			Failure(c, http.StatusInternalServerError, Error{Code: "INTERNAL_ERROR"})
			if recorder.Code != status || recorder.Body.Len() != 0 || !reflect.DeepEqual(recorder.Header(), headers) {
				t.Fatal("late failure changed the HEAD response")
			}
			if !strings.Contains(logs.String(), "[ERROR]") || !strings.Contains(logs.String(), "method=HEAD") || !strings.Contains(logs.String(), "/request-errors/") {
				t.Fatalf("late HEAD failure lost diagnostics: %s", logs.String())
			}
		})
	}
}

func TestFailurePreservesFieldAndPartialResults(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelInfo)
	recorder := httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusBadRequest, Error{
		Code:        "INVALID_REQUEST",
		Message:     "test rejection",
		Cause:       errors.New("internal cause only"),
		Data:        map[string]int{"accepted": 2},
		FieldErrors: []FieldError{{Field: "rules.path", Message: "test field feedback", ClientID: "rule-3"}},
	})
	response := readResponse(t, recorder)
	if response.ErrorCode != "INVALID_REQUEST" || response.Message != "test rejection" || string(response.Data) != `{"accepted":2}` {
		t.Fatalf("failure result not preserved: %+v", response)
	}
	if len(response.FieldErrors) != 1 || response.FieldErrors[0].ClientID != "rule-3" || response.FieldErrors[0].Field != "rules.path" {
		t.Fatalf("field location not preserved: %+v", response.FieldErrors)
	}
	if !regexp.MustCompile(`^/request-errors/[A-Za-z0-9]{24}$`).MatchString(response.Instance) {
		t.Fatalf("invalid instance URI: %q", response.Instance)
	}
	if strings.Contains(recorder.Body.String(), "internal cause only") || !strings.Contains(logs.String(), response.Instance) {
		t.Fatalf("cause must stay internal and instance must correlate: body=%s logs=%s", recorder.Body.String(), logs.String())
	}
}

func TestEncodingFailureReturnsSafeInternalError(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelInfo)
	recorder := httptest.NewRecorder()
	c := testContext(recorder)
	Success(c, http.StatusCreated, make(chan int), "test success feedback")
	response := readResponse(t, recorder)
	if response.Status != http.StatusInternalServerError || response.ErrorCode != "INTERNAL_ERROR" || string(response.Data) != "null" || response.Message == "" {
		t.Fatalf("encoding failure did not produce a safe error: %+v", response)
	}
	if !c.IsAborted() || !strings.Contains(logs.String(), response.Instance) || !strings.Contains(logs.String(), "[ERROR]") {
		t.Fatalf("encoding failure should abort and correlate: %s", logs.String())
	}
	if c.Errors.Last() == nil {
		t.Fatal("encoding failure lost its internal error")
	}
	if _, ok := errors.AsType[*json.UnsupportedTypeError](c.Errors.Last().Err); !ok {
		t.Fatalf("encoding failure lost its original error type: %v", c.Errors)
	}
	if strings.Contains(recorder.Body.String(), "chan int") || strings.Contains(recorder.Body.String(), "test success feedback") {
		t.Fatalf("encoding error leaked internal details or success feedback: %s", recorder.Body.String())
	}
}

func TestWrittenResponseIsNotReplaced(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelInfo)
	recorder := httptest.NewRecorder()
	c := testContext(recorder)
	c.Header("Content-Type", "text/event-stream")
	c.Status(http.StatusOK)
	_, _ = c.Writer.WriteString("event: complete\n\ndata: {}\n\n")
	before := recorder.Body.String()
	headers := recorder.Header().Clone()
	Failure(c, http.StatusInternalServerError, Error{
		Code: "INTERNAL_ERROR", Cause: errors.New("stream callback failed: private-late-secret"), Secrets: []string{"private-late-secret"},
	})
	loggedFailure := logs.String()
	Success(c, http.StatusCreated, make(chan int), "")
	if recorder.Code != http.StatusOK || recorder.Body.String() != before || !reflect.DeepEqual(recorder.Header(), headers) || logs.String() != loggedFailure {
		t.Fatalf("started native response was replaced or reinterpreted: body=%q logs=%q", recorder.Body.String(), logs.String())
	}
	if !strings.Contains(loggedFailure, "[ERROR]") || !strings.Contains(loggedFailure, "/request-errors/") || !strings.Contains(loggedFailure, "stream callback failed") || strings.Contains(loggedFailure, "private-late-secret") {
		t.Fatalf("late failure lost safe diagnostics: %s", loggedFailure)
	}
}

func TestFailureUsesExistingLogLevel(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelError)
	recorder := httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusBadRequest, Error{Code: "INVALID_REQUEST", Message: "test rejection"})
	response := readResponse(t, recorder)
	if response.Instance == "" || logs.Len() != 0 {
		t.Fatalf("expected rejection must keep its instance while respecting log filtering: %q", logs.String())
	}
	recorder = httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusInternalServerError, Error{Code: "INTERNAL_ERROR", Cause: errors.New("database query failed")})
	response = readResponse(t, recorder)
	if !strings.Contains(logs.String(), response.Instance) || !strings.Contains(logs.String(), "[ERROR]") {
		t.Fatalf("internal failure should remain visible at Error level: %q", logs.String())
	}
}

func TestExpectedMaintenanceUsesInfoLevel(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelError)
	recorder := httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusServiceUnavailable, Error{
		Code: "DATABASE_MAINTENANCE", Message: "test maintenance feedback", Cause: errors.New("maintenance active"),
	})
	response := readResponse(t, recorder)
	if response.Instance == "" || logs.Len() != 0 {
		t.Fatalf("expected maintenance should respect Info filtering: %s", logs.String())
	}
	helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
	recorder = httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusServiceUnavailable, Error{Code: "DATABASE_MAINTENANCE"})
	response = readResponse(t, recorder)
	if !strings.Contains(logs.String(), "[INFO]") || !strings.Contains(logs.String(), response.Instance) {
		t.Fatalf("default logging should keep maintenance correlation: %s", logs.String())
	}
}

func TestInvalidRequestProjectsKnownFieldsAndHidesUnknownBindingDetails(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelInfo)
	fieldCause := fmt.Errorf("validate: %w", validation.New("settings.directory", "test field feedback"))
	recorder := httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusBadRequest, InvalidRequest(fieldCause))
	response := readResponse(t, recorder)
	if response.ErrorCode != "INVALID_REQUEST" || len(response.FieldErrors) != 1 || response.FieldErrors[0].Field != "settings.directory" || response.FieldErrors[0].Message != "test field feedback" {
		t.Fatalf("wrapped known validation lost its field location: %+v", response)
	}
	unknownCause := errors.New("private binding detail")
	recorder = httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusBadRequest, InvalidRequest(unknownCause))
	response = readResponse(t, recorder)
	if response.Message == "" || strings.Contains(recorder.Body.String(), unknownCause.Error()) || len(response.FieldErrors) != 0 {
		t.Fatalf("unknown binding cause leaked or became an invented field error: %+v", response)
	}
	if !strings.Contains(logs.String(), unknownCause.Error()) {
		t.Fatalf("unknown binding cause should remain diagnosable: %s", logs.String())
	}
}

func TestStartupFailureUsesSafeStandardLog(t *testing.T) {
	oldAppLogger := helpers.AppLogger
	oldWriter, oldFlags, oldPrefix := log.Writer(), log.Flags(), log.Prefix()
	t.Cleanup(func() {
		helpers.AppLogger = oldAppLogger
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})
	helpers.AppLogger = nil
	var logs bytes.Buffer
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	const secret = "private-db-password"
	recorder := httptest.NewRecorder()
	Failure(testContext(recorder), http.StatusInternalServerError, Error{Code: "DATABASE_CONFIG_FAILED", Cause: fmt.Errorf("password=%s", secret)})
	response := readResponse(t, recorder)
	if response.Message == "" || !strings.Contains(logs.String(), response.Instance) || strings.Contains(logs.String(), secret) {
		t.Fatalf("startup failure lost safe correlation: body=%s logs=%s", recorder.Body.String(), logs.String())
	}
}

func TestKnownSecretsStayOutOfResponseAndDiagnostic(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelInfo)
	const secret = "private-unlabelled-key"
	original := errors.New("provider rejected " + secret)
	cause := fmt.Errorf("operation failed: %w", original)
	failure := Error{Code: "UPSTREAM_REJECTED", Message: "test safe message", Cause: cause, Secrets: []string{secret}}
	recorder := httptest.NewRecorder()
	c := testContext(recorder)
	Failure(c, http.StatusBadGateway, failure)
	response := readResponse(t, recorder)
	originalBody := recorder.Body.String()
	Failure(c, http.StatusInternalServerError, failure)
	if recorder.Code != http.StatusBadGateway || recorder.Body.String() != originalBody || strings.Count(logs.String(), response.Instance) != 2 {
		t.Fatalf("late failure changed the response or its diagnostic instance: body=%s logs=%s", recorder.Body.String(), logs.String())
	}
	if strings.Contains(logs.String(), secret) || strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("known unlabelled secret leaked: body=%s logs=%s", recorder.Body.String(), logs.String())
	}
	if !strings.Contains(logs.String(), response.Instance) || !strings.Contains(logs.String(), helpers.SensitiveLogMask) {
		t.Fatalf("safe correlation or redaction missing: %s", logs.String())
	}
}

func TestFailureDiagnosticsExcludeRequestAndUpstreamSecrets(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelInfo)
	const secret = "private-sentinel"
	engine := gin.New()
	engine.GET("/api/items/:id", func(c *gin.Context) {
		Failure(c, http.StatusBadGateway, Error{
			Code: "UPSTREAM_UNAVAILABLE", Message: "test safe message",
			Cause: fmt.Errorf("upstream fetch: %w", &url.Error{
				Op: "Get", URL: "https://user:" + secret + "@remote.test/" + secret + "?token=" + secret,
				Err: errors.New("deadline exceeded\nprivate diagnostic detail"),
			}),
		})
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/items/"+secret+"?api_key="+secret, nil)
	request.Header.Set("Cookie", "auth_token="+secret)
	engine.ServeHTTP(recorder, request)
	response := readResponse(t, recorder)
	if strings.Contains(logs.String(), secret) || strings.Contains(recorder.Body.String(), secret) {
		t.Fatalf("diagnostic leaked request or upstream secrets: body=%s logs=%s", recorder.Body.String(), logs.String())
	}
	if !strings.Contains(logs.String(), "/api/items/:id") || !strings.Contains(logs.String(), "https://remote.test") || !strings.Contains(logs.String(), response.Instance) {
		t.Fatalf("safe diagnostic context missing: %s", logs.String())
	}
	if strings.Count(logs.String(), "\n") != 1 {
		t.Fatalf("cause injected additional log lines: %q", logs.String())
	}
}

type failedResponseWriter struct {
	header http.Header
	status int
	writes int
	err    error
}

func (w *failedResponseWriter) Header() http.Header    { return w.header }
func (w *failedResponseWriter) WriteHeader(status int) { w.status = status }
func (w *failedResponseWriter) Write([]byte) (int, error) {
	w.writes++
	return 0, w.err
}

func TestNetworkWriteFailureDoesNotWriteAnotherResponse(t *testing.T) {
	logs := captureLogger(t, helpers.LogLevelError)
	writeErr := errors.New("connection closed")
	writer := &failedResponseWriter{header: make(http.Header), err: writeErr}
	c := testContext(writer)
	Success(c, http.StatusOK, false, "")
	if writer.writes != 1 || writer.status != http.StatusOK || !c.IsAborted() || !errors.Is(c.Errors.Last().Err, writeErr) {
		t.Fatalf("write error should be retained without a second response: writes=%d status=%d errors=%v", writer.writes, writer.status, c.Errors)
	}
	if !strings.Contains(logs.String(), "RESPONSE_WRITE_FAILED") || !strings.Contains(logs.String(), "status=200") || !strings.Contains(logs.String(), "[ERROR]") {
		t.Fatalf("write error diagnostic lost actual status or severity: %s", logs.String())
	}
}
