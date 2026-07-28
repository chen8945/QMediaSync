package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/models"
	"qmediasync/internal/taskgate"

	"github.com/gin-gonic/gin"
)

func TestQueueStartHandlersRejectTaskAdmissionBarrier(t *testing.T) {
	previousUploadQueue := models.GlobalUploadQueue
	previousDownloadQueue := models.GlobalDownloadQueue
	models.GlobalUploadQueue = models.NewUq(1)
	models.GlobalDownloadQueue = models.NewDq(1)
	taskgate.BlockNewTasks()
	t.Cleanup(func() {
		models.GlobalUploadQueue = previousUploadQueue
		models.GlobalDownloadQueue = previousDownloadQueue
		taskgate.AllowNewTasks()
	})

	for _, testCase := range []struct {
		name    string
		handler gin.HandlerFunc
		running func() bool
	}{
		{name: "upload", handler: StartUploadQueue, running: models.GlobalUploadQueue.IsRunning},
		{name: "download", handler: StartDownloadQueue, running: models.GlobalDownloadQueue.IsRunning},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			testCase.handler(ctx)

			if response.Code != http.StatusConflict {
				t.Fatalf("HTTP status = %d，期望 %d", response.Code, http.StatusConflict)
			}
			if testCase.running() {
				t.Fatal("任务准入关闭时 HTTP 队列重启入口不能启动 worker")
			}
		})
	}
}
