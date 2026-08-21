package controllers

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func TestUpdateThreadsApplies115RateConfigAfterSaving(t *testing.T) {
	gin.SetMode(gin.TestMode)

	oldSettings := models.SettingsGlobal
	oldDownloadQueue := models.GlobalDownloadQueue
	oldSetGlobalExecutorConfig := setGlobalExecutorConfig
	oldAppLogger := helpers.AppLogger
	oldDownloadQueueRunning := oldDownloadQueue != nil && oldDownloadQueue.IsRunning()
	if oldDownloadQueueRunning {
		oldDownloadQueue.Stop()
	}
	models.GlobalDownloadQueue = nil

	setupControllerTestDB(t, &models.Settings{})
	t.Cleanup(func() {
		if models.GlobalDownloadQueue != nil && models.GlobalDownloadQueue != oldDownloadQueue {
			models.GlobalDownloadQueue.Stop()
		}
		models.GlobalDownloadQueue = oldDownloadQueue
		if oldDownloadQueueRunning {
			oldDownloadQueue.Start()
		}
		models.SettingsGlobal = oldSettings
		setGlobalExecutorConfig = oldSetGlobalExecutorConfig
		helpers.AppLogger = oldAppLogger
	})

	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	settings := &models.Settings{
		SettingThreads: models.SettingThreads{
			DownloadThreads:    1,
			FileDetailThreads:  2,
			OpenlistQPS:        2,
			OpenlistRetry:      1,
			OpenlistRetryDelay: 30,
			FileListPageSize:   1150,
		},
	}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatalf("创建测试 settings 失败：%v", err)
	}
	models.SettingsGlobal = settings

	var (
		setCalls       int
		gotQPS         int
		gotQPM         int
		gotQPH         int
		savedQPS       int
		setCallbackErr error
	)
	setGlobalExecutorConfig = func(qps, qpm, qph int) {
		setCalls++
		gotQPS, gotQPM, gotQPH = qps, qpm, qph
		var saved models.Settings
		setCallbackErr = db.Db.Take(&saved, settings.ID).Error
		if setCallbackErr == nil {
			savedQPS = saved.FileDetailThreads
		}
	}

	r := gin.New()
	r.POST("/setting/threads", UpdateThreads)
	req := httptest.NewRequest(http.MethodPost, "/setting/threads", strings.NewReader(`{
		"download_threads": 1,
		"file_detail_threads": 4,
		"openlist_qps": 2,
		"openlist_retry": 1,
		"openlist_retry_delay": 30,
		"file_list_page_size": 1150
	}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP = %d，期望 200，body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"code":200`) {
		t.Fatalf("更新线程配置失败：%s", w.Body.String())
	}
	if setCallbackErr != nil {
		t.Fatalf("运行时配置回调读取 settings 失败：%v", setCallbackErr)
	}
	if setCalls != 1 {
		t.Fatalf("运行时配置回调次数 = %d，期望 1", setCalls)
	}
	if gotQPS != 4 || gotQPM != 240 || gotQPH != 14400 {
		t.Fatalf("115 运行时配置 = %d/%d/%d，期望 4/240/14400", gotQPS, gotQPM, gotQPH)
	}
	if savedQPS != 4 {
		t.Fatalf("运行时配置回调执行时数据库 QPS = %d，期望已保存为 4", savedQPS)
	}
}
