package controllers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUpdateProgressReturnsSnapshotForTerminalStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldInfo := currentUpdateInfo
	oldCancel := currentUpdateCancel
	t.Cleanup(func() {
		currentUpdateInfo = oldInfo
		currentUpdateCancel = oldCancel
	})

	setCurrentUpdateInfoForTest(&updateInfo{
		Version:    "v0.16.0",
		Progress:   100,
		TotalSize:  100,
		Downloaded: 100,
		Status:     string(updateStatusCompleted),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/update/progress", nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	UpdateProgress(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !containsAll(body, []string{`"code":200`, `"status":"completed"`, `"progress":100`}) {
		t.Fatalf("响应体 = %s，期望包含 completed 终态快照", body)
	}
}

func TestUpdateProgressReturnsBadRequestWhenNoSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldInfo := currentUpdateInfo
	oldCancel := currentUpdateCancel
	t.Cleanup(func() {
		currentUpdateInfo = oldInfo
		currentUpdateCancel = oldCancel
	})

	setCurrentUpdateInfoForTest(nil)

	req := httptest.NewRequest(http.MethodGet, "/api/update/progress", nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	UpdateProgress(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); !containsAll(body, []string{`"code":500`, "未开始更新"}) {
		t.Fatalf("响应体 = %s，期望提示未开始更新", body)
	}
}

func TestCancelUpdateMarksSnapshotCancelled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldInfo := currentUpdateInfo
	oldCancel := currentUpdateCancel
	t.Cleanup(func() {
		currentUpdateInfo = oldInfo
		currentUpdateCancel = oldCancel
	})

	cancelCalled := false
	currentUpdateMu.Lock()
	currentUpdateInfo = &updateInfo{Version: "v0.16.0", Status: string(updateStatusDownloading)}
	currentUpdateCancel = func() { cancelCalled = true }
	currentUpdateMu.Unlock()

	req := httptest.NewRequest(http.MethodPost, "/api/update/cancel", nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	CancelUpdate(c)

	if !cancelCalled {
		t.Fatal("取消函数未被调用")
	}
	info := getCurrentUpdateInfoSnapshot()
	if info == nil || info.Status != string(updateStatusCancelled) {
		t.Fatalf("取消后状态 = %+v，期望 cancelled", info)
	}
}

func TestCancelUpdateRejectsFinishedTask(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldInfo := currentUpdateInfo
	oldCancel := currentUpdateCancel
	t.Cleanup(func() {
		currentUpdateInfo = oldInfo
		currentUpdateCancel = oldCancel
	})

	done := make(chan struct{})
	close(done)
	setCurrentUpdateInfoForTest(&updateInfo{Version: "v0.16.0", Status: string(updateStatusFailed), done: done})

	req := httptest.NewRequest(http.MethodPost, "/api/update/cancel", nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	CancelUpdate(c)

	if body := rec.Body.String(); !containsAll(body, []string{`"code":500`, "未开始更新"}) {
		t.Fatalf("响应体 = %s，期望拒绝取消已结束的任务", body)
	}
	if info := getCurrentUpdateInfoSnapshot(); info.Status != string(updateStatusFailed) {
		t.Fatalf("已结束任务状态 = %s，期望保持 failed", info.Status)
	}
}

func TestCancelledUpdateStillOccupiesUntilDone(t *testing.T) {
	done := make(chan struct{})
	info := &updateInfo{Status: string(updateStatusCancelled), done: done}

	if !isUpdateTaskRunning(info) {
		t.Fatal("已取消但 goroutine 未退出的更新任务应继续占用更新槽位")
	}

	close(done)
	if isUpdateTaskRunning(info) {
		t.Fatal("done 关闭后更新任务不应继续占用更新槽位")
	}
}

func TestCleanupUpdatePackageOnDownloadErrorRemovesPartialFile(t *testing.T) {
	updateFilename := filepath.Join(t.TempDir(), "QMediaSync.tar.gz")
	if err := os.WriteFile(updateFilename, []byte("partial"), 0o666); err != nil {
		t.Fatalf("写入临时更新包失败：%v", err)
	}

	cleanupUpdatePackageOnDownloadError(updateFilename)

	if _, err := os.Stat(updateFilename); !os.IsNotExist(err) {
		t.Fatalf("下载失败后临时更新包仍存在，stat err = %v", err)
	}
}

func containsAll(raw string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(raw, part) {
			return false
		}
	}
	return true
}
