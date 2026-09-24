package controllers

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
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

	// 测试进程可能本身运行在 systemd 服务或容器中，每个场景都显式设置相关环境变量。
	for _, tc := range []struct {
		name                       string
		docker, marker, invocation string
		fnOS                       bool
		want, wantSystemd          bool
	}{
		{name: "无安装机制的进程"},
		{name: "只有标记但不在 systemd 服务中", marker: "1"},
		{name: "声明在线更新的 systemd 服务", marker: "1", invocation: "test", want: runtime.GOOS == "linux", wantSystemd: runtime.GOOS == "linux"},
		{name: "容器入口脚本", docker: "1", want: true},
		{name: "容器内带标记仍走容器更新", docker: "1", marker: "1", invocation: "test", want: true},
		{name: "飞牛", docker: "1", fnOS: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOCKER", tc.docker)
			t.Setenv("QMS_SYSTEMD_UPDATE", tc.marker)
			t.Setenv("INVOCATION_ID", tc.invocation)
			helpers.IsFnOS = tc.fnOS
			if got := supportsOnlineUpdate(); got != tc.want {
				t.Fatalf("supportsOnlineUpdate() = %v，期望 %v", got, tc.want)
			}
			if got := isSystemdOnlineUpdate(); got != tc.wantSystemd {
				t.Fatalf("isSystemdOnlineUpdate() = %v，期望 %v", got, tc.wantSystemd)
			}
		})
	}
}

func TestUpdateToVersionRejectsSystemdInstallBeforeDownload(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd 在线更新只在 Linux 上启用")
	}
	gin.SetMode(gin.TestMode)
	oldInfo, oldCancel := currentUpdateInfo, currentUpdateCancel
	oldFnOS, oldRootDir := helpers.IsFnOS, helpers.RootDir
	t.Cleanup(func() {
		currentUpdateInfo, currentUpdateCancel = oldInfo, oldCancel
		helpers.IsFnOS, helpers.RootDir = oldFnOS, oldRootDir
	})
	setCurrentUpdateInfoForTest(nil)
	helpers.IsFnOS = false
	t.Setenv("DOCKER", "")
	t.Setenv("QMS_SYSTEMD_UPDATE", "1")
	t.Setenv("INVOCATION_ID", "test")

	for _, tc := range []struct {
		name, rootDir, message string
	}{
		// 以 root 运行测试时权限位无法模拟不可写，改用不存在的目录让临时文件创建失败。
		{name: "安装目录不可写", rootDir: filepath.Join(t.TempDir(), "missing"), message: "安装目录不可写"},
		// 测试进程是 controllers.test，不是安装目录下的 QMediaSync。
		{name: "运行中的程序不在安装目录", rootDir: t.TempDir(), message: "当前运行方式不支持在线更新"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helpers.RootDir = tc.rootDir
			req := httptest.NewRequest(http.MethodPost, "/api/update/to-version", strings.NewReader(`{"version":"v1.0.0","channel":"github"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = req

			UpdateToVersion(c)

			if body := rec.Body.String(); !containsAll(body, []string{`"code":500`, tc.message}) {
				t.Fatalf("响应体 = %s，期望在下载前提示 %s", body, tc.message)
			}
			if info := getCurrentUpdateInfoSnapshot(); info != nil {
				t.Fatalf("下载前拒绝时不能创建更新任务：%+v", info)
			}
		})
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	return string(content)
}

func TestPrepareWindowsAndDockerUpdates(t *testing.T) {
	oldRoot, oldConfig := helpers.RootDir, helpers.ConfigDir
	t.Cleanup(func() { helpers.RootDir, helpers.ConfigDir = oldRoot, oldConfig })
	for _, platform := range []string{"windows", "docker"} {
		for _, scenario := range []string{"success", "corrupt", "incomplete", "write failure", "pack failure", "publish failure"} {
			t.Run(platform+"/"+scenario, func(t *testing.T) {
				helpers.RootDir, helpers.ConfigDir = t.TempDir(), t.TempDir()
				archive := filepath.Join(t.TempDir(), "qmediasync_windows_x86_64.zip")
				files := map[string]string{"QMediaSync.exe": "new-binary", "web_statics/index.html": "new-web"}
				if scenario == "incomplete" {
					delete(files, "web_statics/index.html")
				}
				if platform == "windows" {
					f, err := os.Create(archive)
					if err != nil {
						t.Fatal(err)
					}
					zw := zip.NewWriter(f)
					for name, content := range files {
						w, err := zw.Create(name)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := io.WriteString(w, content); err != nil {
							t.Fatal(err)
						}
					}
					if err := errors.Join(zw.Close(), f.Close()); err != nil {
						t.Fatal(err)
					}
				} else {
					linuxFiles := map[string]string{}
					for name, content := range files {
						linuxFiles["qmediasync_linux_x86_64/"+strings.TrimSuffix(name, ".exe")] = content
					}
					linuxFiles["qmediasync_linux_x86_64/scripts/docker-entrypoint.sh"] = "entrypoint"
					linuxFiles["qmediasync_linux_x86_64/scripts/watch_update.sh"] = "watcher"
					archive = writeReleaseArchive(t, linuxFiles)
				}
				if scenario == "corrupt" {
					writeTestFile(t, archive, "corrupt")
				}
				if scenario == "write failure" {
					if platform == "windows" {
						// config 指向普通文件，实际创建解压目录必须失败。
						helpers.ConfigDir = filepath.Join(t.TempDir(), "blocked")
						writeTestFile(t, helpers.ConfigDir, "blocked")
					} else {
						helpers.RootDir = filepath.Join(t.TempDir(), "blocked")
						writeTestFile(t, helpers.RootDir, "blocked")
					}
				}
				if scenario == "pack failure" {
					if platform == "windows" {
						t.Skip("Windows 不重新打包")
					}
					binDir := t.TempDir()
					writeTestFile(t, filepath.Join(binDir, "tar"), "#!/bin/sh\nprintf partial\nexit 1\n")
					if err := os.Chmod(filepath.Join(binDir, "tar"), 0755); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", binDir)
				}
				if scenario == "publish failure" {
					if platform == "windows" {
						t.Skip("Windows 直接准备解压目录")
					}
					if err := os.Mkdir(filepath.Join(helpers.RootDir, "qms.update.tar.gz"), 0755); err != nil {
						t.Fatal(err)
					}
				}
				// 旧临时文件和解压内容不能混入新包。
				if scenario != "write failure" {
					writeTestFile(t, filepath.Join(helpers.RootDir, "qms.update.tar.gz.tmp"), "stale-package")
				}
				if scenario == "success" {
					writeTestFile(t, filepath.Join(helpers.ConfigDir, "update", "stale.js"), "stale")
				}
				var err error
				if platform == "windows" {
					err = installWindowsUpdate(archive)
				} else {
					err = installDockerUpdate(archive)
				}
				if scenario != "success" {
					if err == nil {
						t.Fatal("准备或交付失败必须返回错误")
					}
					if info, statErr := os.Stat(filepath.Join(helpers.RootDir, "qms.update.tar.gz")); statErr == nil && !info.IsDir() {
						t.Fatal("失败不得发布更新包")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					src := filepath.Join(helpers.ConfigDir, "update")
					binary := "QMediaSync.exe"
					if platform == "docker" {
						binary, src = "QMediaSync", t.TempDir()
						if err := helpers.ExtractTarGz(filepath.Join(helpers.RootDir, "qms.update.tar.gz"), src); err != nil {
							t.Fatal(err)
						}
					}
					if got := readTestFile(t, filepath.Join(src, binary)); got != "new-binary" {
						t.Fatalf("binary = %q", got)
					}
					if got := readTestFile(t, filepath.Join(src, "web_statics", "index.html")); got != "new-web" {
						t.Fatalf("web = %q", got)
					}
					if _, err := os.Stat(filepath.Join(src, "stale.js")); !os.IsNotExist(err) {
						t.Fatal("不应复用残留解压内容")
					}
				}
				if paths, err := filepath.Glob(filepath.Join(helpers.RootDir, ".qms-update-*")); err != nil || len(paths) != 0 {
					t.Fatalf("交付临时文件未清理：%v, %v", paths, err)
				}
			})
		}
	}
}

// writeReleaseArchive 按发布包结构生成 tar.gz：条目名即归档内路径。
func writeReleaseArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "QMediaSync_linux_x86_64.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	for _, closer := range []io.Closer{tw, gz, file} {
		if err := closer.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return archivePath
}

func TestInstallSystemdUpdateReplacesReleaseFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "QMediaSync"), "old-binary")
	writeTestFile(t, filepath.Join(root, "web_statics", "index.html"), "old-web")
	writeTestFile(t, filepath.Join(root, "web_statics", "stale.js"), "stale")
	writeTestFile(t, filepath.Join(root, "scripts", "custom.sh"), "keep")
	writeTestFile(t, filepath.Join(root, "config", "config.yaml"), "keep")
	archive := writeReleaseArchive(t, map[string]string{
		"QMediaSync_linux_x86_64/QMediaSync":                   "new-binary",
		"QMediaSync_linux_x86_64/web_statics/index.html":       "new-web",
		"QMediaSync_linux_x86_64/scripts/docker-entrypoint.sh": "docker-only",
	})

	if err := installSystemdUpdate(archive, root); err != nil {
		t.Fatalf("安装更新失败：%v", err)
	}

	for path, want := range map[string]string{
		"QMediaSync":                 "new-binary",
		"web_statics/index.html":     "new-web",
		"old/QMediaSync":             "old-binary",
		"old/web_statics/index.html": "old-web",
		"scripts/custom.sh":          "keep",
		"config/config.yaml":         "keep",
	} {
		if got := readTestFile(t, filepath.Join(root, path)); got != want {
			t.Fatalf("%s = %q，期望 %q", path, got, want)
		}
	}
	if info, err := os.Stat(filepath.Join(root, "QMediaSync")); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("新程序必须可执行：%v %v", info, err)
	}
	for _, path := range []string{"web_statics/stale.js", "scripts/docker-entrypoint.sh", "update"} {
		if _, err := os.Stat(filepath.Join(root, path)); !os.IsNotExist(err) {
			t.Fatalf("%s 不应存在，stat err = %v", path, err)
		}
	}
}

func TestInstallReleaseFilesKeepsOldVersionOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withoutWeb bool
		failTarget string
		binaryName string
	}{
		{name: "更新包不完整", withoutWeb: true},
		{name: "替换中途失败", failTarget: "web_statics"},
		{name: "备份旧文件失败", failTarget: "old/web_statics"},
		{name: "换入新程序失败", failTarget: "QMediaSync"},
		{name: "Windows 替换中途失败", failTarget: "web_statics", binaryName: "QMediaSync.exe"},
		{name: "Windows 换入新程序失败", failTarget: "QMediaSync.exe", binaryName: "QMediaSync.exe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, src := t.TempDir(), t.TempDir()
			if tc.binaryName == "" {
				tc.binaryName = "QMediaSync"
			}
			writeTestFile(t, filepath.Join(root, tc.binaryName), "old-binary")
			writeTestFile(t, filepath.Join(root, "web_statics", "index.html"), "old-web")
			writeTestFile(t, filepath.Join(src, tc.binaryName), "new-binary")
			if !tc.withoutWeb {
				writeTestFile(t, filepath.Join(src, "web_statics", "index.html"), "new-web")
			}
			if tc.failTarget != "" {
				oldRename := renameInstallPath
				t.Cleanup(func() { renameInstallPath = oldRename })
				// 在 failTarget 对应的改名处注入失败，验证回滚能恢复已处理的条目。
				renameInstallPath = func(from, to string) error {
					if to == filepath.Join(root, tc.failTarget) {
						return errors.New("injected rename failure")
					}
					return os.Rename(from, to)
				}
			}

			if err := InstallReleaseFiles(src, root, tc.binaryName); err == nil {
				t.Fatal("安装失败时必须返回错误")
			}

			if got := readTestFile(t, filepath.Join(root, tc.binaryName)); got != "old-binary" {
				t.Fatalf("QMediaSync = %q，期望恢复旧版本", got)
			}
			if got := readTestFile(t, filepath.Join(root, "web_statics", "index.html")); got != "old-web" {
				t.Fatalf("web_statics/index.html = %q，期望恢复旧版本", got)
			}
		})
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

func TestVerifyUpdatePackageChecksSha256(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "qmediasync_linux_x86_64.tar.gz")
	if err := os.WriteFile(pkg, []byte("package"), 0644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("package"))
	checksums := hex.EncodeToString(sum[:]) + "  qmediasync_linux_x86_64.tar.gz\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, checksums)
	}))
	defer srv.Close()

	if err := verifyUpdatePackage(context.Background(), "", srv.URL, pkg, true); err != nil {
		t.Fatalf("expected checksum to match: %v", err)
	}
	checksums = strings.Repeat("0", 64) + "  qmediasync_linux_x86_64.tar.gz\n"
	if err := verifyUpdatePackage(context.Background(), "", srv.URL, pkg, true); err == nil {
		t.Fatal("expected mismatch error")
	}
	if err := verifyUpdatePackage(context.Background(), "", "", pkg, true); err == nil {
		t.Fatal("expected missing checksum to fail when required")
	}
	if err := verifyUpdatePackage(context.Background(), "", "", pkg, false); err != nil {
		t.Fatalf("expected missing checksum to be tolerated: %v", err)
	}
}

func TestHandedOffUpdateRemainsRunning(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if !isUpdateTaskRunning(&updateInfo{Status: string(updateStatusInstall), done: done}) {
		t.Fatal("更新包交付后安装仍在进行，不能接受另一轮更新")
	}
}

func TestTriggerUpdateReportsStartFailure(t *testing.T) {
	oldConfig, oldStart := helpers.ConfigDir, startUpdateProcess
	t.Cleanup(func() { helpers.ConfigDir, startUpdateProcess = oldConfig, oldStart })
	helpers.ConfigDir = t.TempDir()
	if err := os.Mkdir(filepath.Join(helpers.ConfigDir, "update"), 0755); err != nil {
		t.Fatal(err)
	}
	startUpdateProcess = func(string, string) bool { return false }
	if err := triggerUpdate(); err == nil {
		t.Fatal("更新进程启动失败必须传回调用方")
	}
}
