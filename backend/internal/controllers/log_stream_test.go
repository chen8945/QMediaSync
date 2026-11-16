package controllers

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	router.GET("/logs/stream", LogStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/logs/stream?path=app.log")
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

	if file, err := os.OpenFile(fullLogPath, os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		t.Fatalf("打开测试日志失败: %v", err)
	} else {
		if _, err := file.WriteString("2026/07/18 10:00:01.000000 [INFO] appended\n"); err != nil {
			_ = file.Close()
			t.Fatalf("追加测试日志失败: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("关闭测试日志失败: %v", err)
		}
	}

	frame := readSSEFrame(t, reader)
	if !strings.Contains(frame, "event:log_append") || !strings.Contains(frame, `"message":"appended"`) {
		t.Fatalf("未收到追加日志 SSE 事件，frame = %q", frame)
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
	content := "2026/11/15 10:00:00.000000 [INFO] visible-entry\n"
	if err := os.WriteFile(filepath.Join(logDir, "app.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/logs/old", GetOldLogs)
	router.GET("/logs/download", DownloadLogFile)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/logs/old?path=app.log&direction=backward&limit=10", nil))
	var snapshot httpapi.APIResponse[OldLogsResponse]
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || len(snapshot.Data.Entries) != 1 || !strings.Contains(snapshot.Data.Entries[0].Message, "visible-entry") {
		t.Fatalf("log snapshot: HTTP=%d body=%s", w.Code, w.Body.String())
	}
	rangeRequest := httptest.NewRequest(http.MethodGet, "/logs/download?path=app.log", nil)
	rangeRequest.Header.Set("Range", "bytes=0-9")
	w = httptest.NewRecorder()
	router.ServeHTTP(w, rangeRequest)
	if w.Code != http.StatusPartialContent || w.Body.String() != content[:10] || w.Header().Get("Content-Range") == "" || w.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("native range download: HTTP=%d headers=%v body=%q", w.Code, w.Header(), w.Body.String())
	}
}

func TestLogRoutesRejectUnsafePathsBeforeNativeOutput(t *testing.T) {
	router := gin.New()
	router.GET("/logs/old", GetOldLogs)
	router.GET("/logs/stream", LogStream)
	router.GET("/logs/download", DownloadLogFile)
	for _, route := range []string{"/logs/old", "/logs/stream", "/logs/download"} {
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
