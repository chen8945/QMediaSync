package controllers

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"qmediasync/internal/helpers"

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

func TestCancelUpdateRejectsTaskPastDownload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldInfo := currentUpdateInfo
	oldCancel := currentUpdateCancel
	t.Cleanup(func() {
		currentUpdateInfo = oldInfo
		currentUpdateCancel = oldCancel
	})

	finished := make(chan struct{})
	close(finished)
	for _, tc := range []struct {
		name    string
		status  updateStatus
		done    chan struct{}
		message string
	}{
		{name: "上一轮已结束", status: updateStatusFailed, done: finished, message: "未开始更新"},
		{name: "安装中", status: updateStatusInstall, done: make(chan struct{}), message: "正在安装更新，无法取消"},
		{name: "安装完成等待重启", status: updateStatusCompleted, done: make(chan struct{}), message: "正在安装更新，无法取消"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cancelCalled := false
			currentUpdateMu.Lock()
			currentUpdateInfo = &updateInfo{Version: "v0.16.0", Status: string(tc.status), done: tc.done}
			currentUpdateCancel = func() { cancelCalled = true }
			currentUpdateMu.Unlock()

			req := httptest.NewRequest(http.MethodPost, "/api/update/cancel", nil)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			CancelUpdate(c)

			if body := rec.Body.String(); !containsAll(body, []string{`"code":500`, tc.message}) {
				t.Fatalf("响应体 = %s，期望提示 %s", body, tc.message)
			}
			if info := getCurrentUpdateInfoSnapshot(); cancelCalled || info.Status != string(tc.status) {
				t.Fatalf("取消被拒绝后状态 = %s，取消函数调用 = %v", info.Status, cancelCalled)
			}
		})
	}
}

func TestBeginUpdateInstallExcludesCancel(t *testing.T) {
	oldInfo := currentUpdateInfo
	oldCancel := currentUpdateCancel
	t.Cleanup(func() {
		currentUpdateInfo = oldInfo
		currentUpdateCancel = oldCancel
	})

	setCurrentUpdateInfoForTest(&updateInfo{Status: string(updateStatusCancelled)})
	if beginUpdateInstall() {
		t.Fatal("已取消的任务不能进入安装")
	}
	if info := getCurrentUpdateInfoSnapshot(); info.Status != string(updateStatusCancelled) {
		t.Fatalf("已取消任务状态 = %s，期望保持 cancelled", info.Status)
	}

	setCurrentUpdateInfoForTest(&updateInfo{Status: string(updateStatusDownloading), TotalSize: 100, done: make(chan struct{})})
	if !beginUpdateInstall() {
		t.Fatal("下载完成且未取消的任务应进入安装")
	}
	if info := getCurrentUpdateInfoSnapshot(); info.Status != string(updateStatusInstall) || info.Downloaded != 100 {
		t.Fatalf("进入安装后状态 = %+v", info)
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

func TestSupportsOnlineUpdateRequiresInstallMechanism(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 发布版由独立更新进程安装")
	}
	oldFnOS := helpers.IsFnOS
	t.Cleanup(func() { helpers.IsFnOS = oldFnOS })
	helpers.IsFnOS = false

	t.Setenv("DOCKER", "")
	if supportsOnlineUpdate() {
		t.Fatal("不经容器入口脚本运行的进程没有安装机制，不能在线更新")
	}
	t.Setenv("DOCKER", "1")
	if !supportsOnlineUpdate() {
		t.Fatal("由容器入口脚本管理的进程应支持在线更新")
	}
	helpers.IsFnOS = true
	if supportsOnlineUpdate() {
		t.Fatal("飞牛由应用商店更新，不能在线更新")
	}
}

func TestUpdateToVersionRejectsUnsupportedRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldInfo := currentUpdateInfo
	oldCancel := currentUpdateCancel
	oldFnOS := helpers.IsFnOS
	t.Cleanup(func() {
		currentUpdateInfo = oldInfo
		currentUpdateCancel = oldCancel
		helpers.IsFnOS = oldFnOS
	})
	setCurrentUpdateInfoForTest(nil)
	// 飞牛在任何宿主平台上都不支持在线更新，使结果不依赖测试机是否运行在 Docker 中。
	helpers.IsFnOS = true

	req := httptest.NewRequest(http.MethodPost, "/api/update/to-version", strings.NewReader(`{"version":"v1.0.0","channel":"github"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = req

	UpdateToVersion(c)

	if body := rec.Body.String(); !containsAll(body, []string{`"code":500`, "当前运行方式不支持在线更新"}) {
		t.Fatalf("响应体 = %s，期望拒绝不支持的运行方式", body)
	}
	if info := getCurrentUpdateInfoSnapshot(); info != nil {
		t.Fatalf("不支持的运行方式不能创建更新任务：%+v", info)
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
