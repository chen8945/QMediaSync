package controllers

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	v115open "qmediasync/internal/v115/openapi"

	"github.com/gin-gonic/gin"
)

func setupQueueStatsControllerTest(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		v115open.GetGlobalExecutor().Stop()
	})

	setupControllerTestDB(t, &models.RequestStat{})

	r := gin.New()
	r.GET("/115/queue/stats", GetQueueStats)
	return r
}

func TestGetQueueStats重启后从数据库返回请求统计(t *testing.T) {
	r := setupQueueStatsControllerTest(t)
	now := time.Now().Unix()
	stats := []models.RequestStat{
		{RequestTime: now - 1, Duration: 100, IsThrottled: false},
		{RequestTime: now - 30, Duration: 200, IsThrottled: true},
	}
	if err := db.Db.Create(&stats).Error; err != nil {
		t.Fatalf("创建请求统计失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/115/queue/stats?time_window=3600", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d，期望 200，body=%s", w.Code, w.Body.String())
	}

	var resp APIResponse[map[string]any]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != Success {
		t.Fatalf("Code = %d，期望 %d，message=%s", resp.Code, Success, resp.Message)
	}
	if got := int64(resp.Data["total_requests"].(float64)); got != 2 {
		t.Fatalf("total_requests = %d，期望 2", got)
	}
	if got := int64(resp.Data["qpm_count"].(float64)); got != 2 {
		t.Fatalf("qpm_count = %d，期望 2", got)
	}
	if got := int64(resp.Data["throttled_count"].(float64)); got != 1 {
		t.Fatalf("throttled_count = %d，期望 1", got)
	}
}

func TestGetQueueStats限流中返回总等待时长(t *testing.T) {
	r := setupQueueStatsControllerTest(t)
	v115open.GetGlobalExecutor().SetThrottledForTesting(true)
	t.Cleanup(func() {
		v115open.GetGlobalExecutor().SetThrottledForTesting(false)
	})

	req := httptest.NewRequest(http.MethodGet, "/115/queue/stats?time_window=3600", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d，期望 200，body=%s", w.Code, w.Body.String())
	}

	var resp APIResponse[map[string]any]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Code != Success {
		t.Fatalf("Code = %d，期望 %d，message=%s", resp.Code, Success, resp.Message)
	}
	if got := resp.Data["is_throttled"].(bool); !got {
		t.Fatal("is_throttled = false，期望 true")
	}
	if got := resp.Data["throttle_wait_time"].(string); got != "1m0s" {
		t.Fatalf("throttle_wait_time = %q，期望 1m0s", got)
	}
	if got := resp.Data["throttled_remaining_time"].(string); got == "" {
		t.Fatal("throttled_remaining_time 为空，期望返回剩余限流时间")
	}
}

func setupUploadQueueControllerTest(t *testing.T) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}

	setupControllerTestDB(t, &models.DbUploadTask{}, &models.UploadSession{})

	r := gin.New()
	r.GET("/upload/queue", UploadList)
	return r
}

func TestUploadListIncludesResumeProgressFields(t *testing.T) {
	r := setupUploadQueueControllerTest(t)
	task := &models.DbUploadTask{
		Source:        models.UploadSourceDirectoryMonitor,
		SourceType:    models.SourceType115,
		Status:        models.UploadStatusUploading,
		LocalFullPath: "/watch/movie.mkv",
		FileName:      "movie.mkv",
		FileSize:      100,
		UploadedBytes: 40,
		UploadResult:  models.UploadResultUnknown,
		ResumeState:   models.UploadResumeStateResumedSession,
	}
	if err := db.Db.Create(task).Error; err != nil {
		t.Fatalf("创建上传任务失败: %v", err)
	}
	session := &models.UploadSession{
		UploadTaskId:  task.ID,
		Status:        models.UploadSessionStatusMultipart,
		ResumeState:   models.UploadResumeStateResumedSession,
		UploadId:      "upload-1",
		PartSize:      20,
		TotalParts:    5,
		UploadedBytes: 40,
		UploadedParts: 2,
	}
	if err := session.Save(); err != nil {
		t.Fatalf("保存上传会话失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/upload/queue?status=-1&page=1&page_size=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP status = %d，期望 200，body=%s", w.Code, w.Body.String())
	}

	var resp APIResponse[map[string]any]
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	list := resp.Data["list"].([]any)
	if len(list) != 1 {
		t.Fatalf("上传队列数量 = %d，期望 1", len(list))
	}
	item := list[0].(map[string]any)
	if item["upload_phase"] != "multipart_uploading" {
		t.Fatalf("upload_phase = %v，期望 multipart_uploading", item["upload_phase"])
	}
	if item["total_parts"] != float64(5) || item["uploaded_parts"] != float64(2) {
		t.Fatalf("分片进度 = %v/%v，期望 2/5", item["uploaded_parts"], item["total_parts"])
	}
	if item["progress_percent"] != float64(40) {
		t.Fatalf("progress_percent = %v，期望 40", item["progress_percent"])
	}
}
