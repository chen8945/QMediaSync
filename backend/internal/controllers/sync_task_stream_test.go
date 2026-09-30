package controllers

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/logstream"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"

	"github.com/gin-gonic/gin"
)

func TestBuildSyncTaskSnapshotMessageIncludesCursorAndVersion(t *testing.T) {
	task := &models.Sync{
		ID: 9, CreatedAt: 100, UpdatedAt: 120,
		SyncPathId: 5,
		Status:     models.SyncStatusInProgress,
		SubStatus:  models.SyncSubStatusProcessNetFileList,
	}
	msg := buildSyncTaskSnapshotMessage(task, []logstream.Entry{{Message: "hello"}}, 88, 3, "sync/sync_9.log")

	if msg.Type != syncTaskStreamSnapshot {
		t.Fatalf("type = %s，期望 %s", msg.Type, syncTaskStreamSnapshot)
	}
	if msg.Version != syncTaskStreamVersion {
		t.Fatalf("version = %d，期望 %d", msg.Version, syncTaskStreamVersion)
	}
	data, ok := msg.Data.(syncTaskSnapshot)
	if !ok {
		t.Fatalf("data 类型 = %T，期望 syncTaskSnapshot", msg.Data)
	}
	if data.LogCursor != 88 {
		t.Fatalf("log_cursor = %d，期望 88", data.LogCursor)
	}
	if data.Task.ID != 9 {
		t.Fatalf("task.id = %d，期望 9", data.Task.ID)
	}
}

func TestBuildSyncTaskSnapshotMessageUsesSelectedLogPath(t *testing.T) {
	task := &models.Sync{ID: 9}
	msg := buildSyncTaskSnapshotMessage(task, nil, 0, 0, "libs/sync_9.log")
	data, ok := msg.Data.(syncTaskSnapshot)
	if !ok {
		t.Fatalf("data 类型 = %T，期望 syncTaskSnapshot", msg.Data)
	}
	if data.LogPath != "libs/sync_9.log" {
		t.Fatalf("log_path = %s，期望 libs/sync_9.log", data.LogPath)
	}
}

func TestSyncTaskStreamSendsDeletedCompleteForMissingTask(t *testing.T) {
	setupControllerTestDB(t, &models.Sync{})
	setupSyncTaskStreamRuntime(t)
	router := gin.New()
	router.GET("/sync/tasks/:id/stream", SyncTaskStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/sync/tasks/404/stream")
	if err != nil {
		t.Fatalf("请求缺失同步任务 stream 失败: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("缺失任务 stream status = %d，期望 %d", response.StatusCode, http.StatusOK)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取缺失同步任务 stream 失败: %v", err)
	}
	if !strings.Contains(string(body), "event:complete") || !strings.Contains(string(body), `"deleted":true`) {
		t.Fatalf("缺失任务应发送 deleted complete，body = %q", body)
	}
}

func TestSyncTaskStreamReturnsHTTPErrorWhenTaskQueryFails(t *testing.T) {
	testDB := setupControllerTestDB(t, &models.Sync{})
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatalf("读取底层测试数据库失败: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("关闭测试数据库失败: %v", err)
	}

	setupSyncTaskStreamRuntime(t)
	router := gin.New()
	router.GET("/sync/tasks/:id/stream", SyncTaskStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/sync/tasks/1/stream")
	if err != nil {
		t.Fatalf("请求同步任务 stream 失败: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("查询同步任务失败时 status = %d，期望 %d，body = %q", response.StatusCode, http.StatusInternalServerError, body)
	}
}

func TestSyncTaskStreamTerminalSnapshotDoesNotRepeatComplete(t *testing.T) {
	testDB := setupControllerTestDB(t, &models.Sync{})
	setupSyncTaskStreamRuntime(t)

	task := &models.Sync{Status: models.SyncStatusCompleted}
	if err := testDB.Create(task).Error; err != nil {
		t.Fatalf("创建终态同步任务失败: %v", err)
	}

	router := gin.New()
	router.GET("/sync/tasks/:id/stream", SyncTaskStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/sync/tasks/" + strconv.FormatUint(uint64(task.ID), 10) + "/stream")
	if err != nil {
		t.Fatalf("请求终态同步任务 stream 失败: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取终态同步任务 stream 失败: %v", err)
	}
	if !strings.Contains(string(body), "event:snapshot") {
		t.Fatalf("终态任务应返回 snapshot，body = %q", body)
	}
	if strings.Contains(string(body), "event:complete") {
		t.Fatalf("终态 snapshot 后不应重复发送 complete，body = %q", body)
	}
}

func TestSyncTaskStreamReplaysPatchFromLastEventID(t *testing.T) {
	testDB := setupControllerTestDB(t, &models.Sync{})
	setupSyncTaskStreamRuntime(t)

	task := &models.Sync{Status: models.SyncStatusInProgress}
	if err := testDB.Create(task).Error; err != nil {
		t.Fatalf("创建运行中同步任务失败: %v", err)
	}
	first := realtime.GlobalSyncTaskHub.PublishSyncTaskEvent(realtime.EventSyncTaskUpdated, realtime.SyncTaskEventPayload{
		SyncID: task.ID,
		Status: int(models.SyncStatusInProgress),
	})
	second := realtime.GlobalSyncTaskHub.PublishSyncTaskEvent(realtime.EventSyncTaskUpdated, realtime.SyncTaskEventPayload{
		SyncID:    task.ID,
		Status:    int(models.SyncStatusInProgress),
		SubStatus: int(models.SyncSubStatusProcessNetFileList),
	})

	router := gin.New()
	router.GET("/sync/tasks/:id/stream", SyncTaskStream)
	server := httptest.NewServer(router)
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL+"/sync/tasks/"+strconv.FormatUint(uint64(task.ID), 10)+"/stream", nil)
	if err != nil {
		t.Fatalf("创建同步任务 stream 请求失败: %v", err)
	}
	request.Header.Set("Last-Event-ID", realtime.GlobalSyncTaskHub.EventID(first.Sequence))
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("请求同步任务 stream 失败: %v", err)
	}
	defer response.Body.Close()

	reader := bufio.NewReader(response.Body)
	connected := readSSEFrame(t, reader)
	if !strings.Contains(connected, ": connected") {
		t.Fatalf("首帧应为 connected 注释，frame = %q", connected)
	}
	patch := readSSEFrame(t, reader)
	if !strings.Contains(patch, "event:task_patch") || !strings.Contains(patch, `"sequence":2`) {
		t.Fatalf("未收到回放 task_patch，frame = %q", patch)
	}
	if !strings.Contains(patch, "id:"+realtime.GlobalSyncTaskHub.EventID(second.Sequence)) {
		t.Fatalf("回放 task_patch 缺少 SSE ID，frame = %q", patch)
	}
}

func TestReplaySyncTaskEventsStopsWhenRequestIsCancelled(t *testing.T) {
	streamCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	replay := []realtime.TaskStreamEvent{
		{Payload: realtime.SyncTaskEventPayload{SyncID: 9, Sequence: 2}},
		{Payload: realtime.SyncTaskEventPayload{SyncID: 9, Sequence: 3}},
	}
	writes := 0

	err := replaySyncTaskEvents(streamCtx, replay, func(realtime.TaskStreamEvent) error {
		writes++
		cancel()
		return nil
	})

	if !errors.Is(err, errSSEStreamStopped) {
		t.Fatalf("请求取消后的回放错误 = %v，期望 %v", err, errSSEStreamStopped)
	}
	if writes != 1 {
		t.Fatalf("请求取消后 replay patch 数量 = %d，期望 1", writes)
	}
}

func TestSyncTaskStreamSnapshotCarriesWaterlineEventID(t *testing.T) {
	testDB := setupControllerTestDB(t, &models.Sync{})
	setupSyncTaskStreamRuntime(t)

	task := &models.Sync{Status: models.SyncStatusInProgress}
	if err := testDB.Create(task).Error; err != nil {
		t.Fatalf("创建运行中同步任务失败: %v", err)
	}
	payload := realtime.GlobalSyncTaskHub.PublishSyncTaskEvent(realtime.EventSyncTaskUpdated, realtime.SyncTaskEventPayload{
		SyncID: task.ID,
		Status: int(models.SyncStatusInProgress),
	})

	router := gin.New()
	router.GET("/sync/tasks/:id/stream", SyncTaskStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/sync/tasks/" + strconv.FormatUint(uint64(task.ID), 10) + "/stream")
	if err != nil {
		t.Fatalf("请求同步任务 stream 失败: %v", err)
	}
	defer response.Body.Close()

	reader := bufio.NewReader(response.Body)
	_ = readSSEFrame(t, reader)
	snapshot := readSSEFrame(t, reader)
	if !strings.Contains(snapshot, "event:snapshot") {
		t.Fatalf("未收到 snapshot，frame = %q", snapshot)
	}
	if !strings.Contains(snapshot, "id:"+realtime.GlobalSyncTaskHub.EventID(payload.Sequence)) {
		t.Fatalf("snapshot 缺少水位线 SSE ID，frame = %q", snapshot)
	}
}

func TestSyncTaskStreamFlushesFinalLogsBeforeComplete(t *testing.T) {
	testDB := setupControllerTestDB(t, &models.Sync{})
	setupSyncTaskStreamRuntime(t)

	task := &models.Sync{Status: models.SyncStatusInProgress}
	if err := testDB.Create(task).Error; err != nil {
		t.Fatalf("创建运行中同步任务失败: %v", err)
	}
	fullLogPath := models.SyncLogFullPath(task.ID)
	if err := os.MkdirAll(filepath.Dir(fullLogPath), 0o755); err != nil {
		t.Fatalf("创建同步日志目录失败: %v", err)
	}
	if err := os.WriteFile(fullLogPath, []byte("2026/07/18 10:00:00.000000 [INFO] started\n"), 0o644); err != nil {
		t.Fatalf("写入初始同步日志失败: %v", err)
	}

	router := gin.New()
	router.GET("/sync/tasks/:id/stream", SyncTaskStream)
	server := httptest.NewServer(router)
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/sync/tasks/" + strconv.FormatUint(uint64(task.ID), 10) + "/stream")
	if err != nil {
		t.Fatalf("建立同步任务 SSE 请求失败: %v", err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	_ = readSSEFrame(t, reader)
	snapshot := readSSEFrame(t, reader)
	if !strings.Contains(snapshot, "event:snapshot") {
		t.Fatalf("运行中任务应先返回 snapshot，frame = %q", snapshot)
	}

	realtime.GlobalSyncTaskHub.PublishSyncTaskEvent(realtime.EventSyncTaskUpdated, realtime.SyncTaskEventPayload{
		SyncID: task.ID,
		Status: int(models.SyncStatusCompleted),
	})
	if file, err := os.OpenFile(fullLogPath, os.O_APPEND|os.O_WRONLY, 0o644); err != nil {
		t.Fatalf("打开同步日志失败: %v", err)
	} else {
		if _, err := file.WriteString("2026/07/18 10:00:01.000000 [INFO] final line\n"); err != nil {
			_ = file.Close()
			t.Fatalf("追加最终同步日志失败: %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("关闭同步日志失败: %v", err)
		}
	}

	logAppend := readSSEFrame(t, reader)
	if !strings.Contains(logAppend, "event:log_append") || !strings.Contains(logAppend, `"message":"final line"`) {
		t.Fatalf("终态 final flush 未发送新增日志，frame = %q", logAppend)
	}
	complete := readSSEFrame(t, reader)
	if !strings.Contains(complete, "event:complete") {
		t.Fatalf("终态 final flush 后未发送 complete，frame = %q", complete)
	}
}

func setupSyncTaskStreamRuntime(t *testing.T) {
	t.Helper()

	oldLifecycle := realtime.GlobalLifecycle
	oldTaskHub := realtime.GlobalSyncTaskHub
	oldLogManager := logstream.GlobalManager
	oldConfigDir := helpers.ConfigDir
	realtime.GlobalLifecycle = realtime.NewLifecycle()
	realtime.GlobalSyncTaskHub = realtime.NewSyncTaskHub()
	logstream.GlobalManager = logstream.NewManager()
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		realtime.GlobalLifecycle = oldLifecycle
		realtime.GlobalSyncTaskHub = oldTaskHub
		logstream.GlobalManager = oldLogManager
		helpers.ConfigDir = oldConfigDir
	})
}
