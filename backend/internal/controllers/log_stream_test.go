package controllers

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/logstream"
	"qmediasync/internal/realtime"

	"github.com/gin-gonic/gin"
)

func TestLogStreamEmitsAppendedLogEntry(t *testing.T) {
	oldLifecycle := realtime.GlobalLifecycle
	oldLogManager := logstream.GlobalManager
	oldConfigDir := helpers.ConfigDir
	realtime.GlobalLifecycle = realtime.NewLifecycle()
	logstream.GlobalManager = logstream.NewManager()
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		realtime.GlobalLifecycle = oldLifecycle
		logstream.GlobalManager = oldLogManager
		helpers.ConfigDir = oldConfigDir
	})

	logsDir := filepath.Join(helpers.ConfigDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("创建测试日志目录失败: %v", err)
	}
	fullLogPath := filepath.Join(logsDir, "app.log")
	if err := os.WriteFile(fullLogPath, []byte("2026/07/18 10:00:00.000000 [INFO] existing\n"), 0o644); err != nil {
		t.Fatalf("写入初始日志失败: %v", err)
	}

	router := gin.New()
	router.GET("/logs/old", GetOldLogs)
	router.GET("/logs/stream", LogStream)
	snapshotResponse := httptest.NewRecorder()
	router.ServeHTTP(snapshotResponse, httptest.NewRequest(http.MethodGet, "/logs/old?path=app.log&pos=-1", nil))
	var snapshot httpapi.APIResponse[OldLogsResponse]
	if err := json.Unmarshal(snapshotResponse.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshotResponse.Code != http.StatusOK || snapshot.Data.FileID == "" {
		t.Fatal(snapshotResponse.Body.String())
	}
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/logs/stream?path=app.log&cursor=" + strconv.FormatInt(snapshot.Data.Cursor, 10) + "&file_id=" + url.QueryEscape(snapshot.Data.FileID))
	if err != nil {
		t.Fatalf("建立日志 SSE 请求失败: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("日志 SSE status = %d，期望 %d", response.StatusCode, http.StatusOK)
	}

	reader := bufio.NewReader(response.Body)
	connected := readSSEFrame(t, reader)
	if !strings.Contains(connected, ": connected") {
		t.Fatalf("首帧应为 connected 注释，frame = %q", connected)
	}

	message := "appended " + strings.Repeat("中", 360000) + " tail"
	if file, err := os.OpenFile(fullLogPath, os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		t.Fatalf("打开测试日志失败: %v", err)
	} else {
		if _, err := file.WriteString("2026/07/18 10:00:01.000000 [INFO] " + message + "\n"); err != nil {
			_ = file.Close()
			t.Fatalf("追加测试日志失败: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("关闭测试日志失败: %v", err)
		}
	}

	frame := readSSEFrame(t, reader)
	if !strings.Contains(frame, "event:log_batch") {
		t.Fatalf("未收到追加日志 SSE 事件，frame length = %d", len(frame))
	}
	var batch struct {
		Entries []logstream.Entry `json:"entries"`
		FileID  string            `json:"file_id"`
	}
	for _, line := range strings.Split(frame, "\n") {
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			if err := json.Unmarshal([]byte(data), &batch); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if batch.FileID != snapshot.Data.FileID || len(batch.Entries) != 1 || batch.Entries[0].Message != message {
		t.Fatalf("追加日志被截断: message length = %d，期望 %d", len(batch.Entries), len(message))
	}
}

func TestLogStreamEmitsResyncAfterLogTruncation(t *testing.T) {
	oldLifecycle := realtime.GlobalLifecycle
	oldLogManager := logstream.GlobalManager
	oldConfigDir := helpers.ConfigDir
	realtime.GlobalLifecycle = realtime.NewLifecycle()
	logstream.GlobalManager = logstream.NewManager()
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		realtime.GlobalLifecycle = oldLifecycle
		logstream.GlobalManager = oldLogManager
		helpers.ConfigDir = oldConfigDir
	})

	logsDir := filepath.Join(helpers.ConfigDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		t.Fatalf("创建测试日志目录失败: %v", err)
	}
	fullLogPath := filepath.Join(logsDir, "app.log")
	if err := os.WriteFile(fullLogPath, []byte("2026/07/18 10:00:00.000000 [INFO] existing\n"), 0o644); err != nil {
		t.Fatalf("写入初始日志失败: %v", err)
	}

	router := gin.New()
	router.GET("/logs/stream", LogStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/logs/stream?path=app.log")
	if err != nil {
		t.Fatalf("建立日志 SSE 请求失败: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	_ = readSSEFrame(t, reader)

	if err := os.WriteFile(fullLogPath, nil, 0o644); err != nil {
		t.Fatalf("截断测试日志失败: %v", err)
	}
	frame := readSSEFrame(t, reader)
	if !strings.Contains(frame, "event:resync_required") || !strings.Contains(frame, "log_file_truncated") {
		t.Fatalf("截断日志后未收到 resync_required，frame = %q", frame)
	}
}

func readSSEFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()

	type result struct {
		frame string
		err   error
	}
	resultCh := make(chan result, 1)
	go func() {
		var frame strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				resultCh <- result{err: err}
				return
			}
			frame.WriteString(line)
			if line == "\n" {
				resultCh <- result{frame: frame.String()}
				return
			}
		}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("读取 SSE 帧失败: %v", result.err)
		}
		return result.frame
	case <-time.After(3 * time.Second):
		t.Fatal("读取 SSE 帧超时")
		return ""
	}
}

func TestLogSnapshotAndNativeRangeDownload(t *testing.T) {
	oldConfigDir := helpers.ConfigDir
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() { helpers.ConfigDir = oldConfigDir })
	logDir := filepath.Join(helpers.ConfigDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	message := "visible-entry <script>alert('日志')</script> " + strings.Repeat("中", 360000) + " tail"
	content := "2026/11/15 10:00:00.000000 [INFO] " + message + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "app.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/logs/old", GetOldLogs)
	router.GET("/logs/download", DownloadLogFile)
	router.GET("/logs/view", ViewLogFile)
	for _, direction := range []string{"forward", "backward"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs/old?path=app.log&pos=-1&direction="+direction+"&limit=10", nil))
		var snapshot httpapi.APIResponse[OldLogsResponse]
		if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(snapshot.Data.Entries) != 1 || snapshot.Data.Entries[0].Message != message {
			t.Fatalf("log snapshot %s: HTTP=%d entries=%d", direction, w.Code, len(snapshot.Data.Entries))
		}
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs/view?path=app.log", nil))
	if w.Code != http.StatusOK || w.Body.String() != content || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Header().Get("Content-Disposition") != "inline" || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("native text view: HTTP=%d headers=%v bytes=%d", w.Code, w.Header(), w.Body.Len())
	}
	for _, route := range []struct{ path, contentType string }{
		{"/logs/download", "application/octet-stream"},
		{"/logs/view", "text/plain; charset=utf-8"},
	} {
		rangeRequest := httptest.NewRequest(http.MethodGet, route.path+"?path=app.log", nil)
		rangeRequest.Header.Set("Range", "bytes=0-9")
		w = httptest.NewRecorder()
		router.ServeHTTP(w, rangeRequest)
		if w.Code != http.StatusPartialContent || w.Body.String() != content[:10] || w.Header().Get("Content-Range") == "" || w.Header().Get("Content-Type") != route.contentType {
			t.Fatalf("native range %s: HTTP=%d headers=%v body=%q", route.path, w.Code, w.Header(), w.Body.String())
		}
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs/view?path=missing.log", nil))
	var failure httpapi.APIResponse[any]
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusNotFound || failure.ErrorCode != "LOG_FILE_NOT_FOUND" {
		t.Fatalf("missing text view: HTTP=%d body=%s", w.Code, w.Body.String())
	}
}

func TestLogRoutesRejectUnsafePathsBeforeNativeOutput(t *testing.T) {
	router := gin.New()
	router.GET("/logs/old", GetOldLogs)
	router.GET("/logs/stream", LogStream)
	router.GET("/logs/download", DownloadLogFile)
	router.GET("/logs/view", ViewLogFile)
	for _, route := range []string{"/logs/old", "/logs/stream", "/logs/download", "/logs/view"} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, route+"?path=../private.log", nil))
		var failure httpapi.APIResponse[any]
		if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusBadRequest || failure.ErrorCode != "INVALID_REQUEST" || len(failure.FieldErrors) != 1 || failure.FieldErrors[0].Field != "path" {
			t.Fatalf("unsafe log path response for %s: HTTP=%d body=%s", route, w.Code, w.Body.String())
		}
	}
}
