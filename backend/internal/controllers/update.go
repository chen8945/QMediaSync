package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/github"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/updater"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
)

type version struct {
	Version     string `json:"version"`
	PublishedAt int64  `json:"published_at"`
	Date        string `json:"date"`
	Note        string `json:"note"`
	Url         string `json:"url"`
	Current     bool   `json:"current"`
	Latest      bool   `json:"latest"`
}

type updateStatus string

const (
	updateStatusDownloading updateStatus = "downloading" // 正在下载
	updateStatusInstall     updateStatus = "install"     // 安装中
	updateStatusCompleted   updateStatus = "completed"   // 已完成
	updateStatusFailed      updateStatus = "failed"      // 失败
	updateStatusCancelled   updateStatus = "cancelled"   // 已取消
)

type updateInfo struct {
	Version      string          `json:"version"`                 // 要更新的版本
	DownloadURL  string          `json:"downloadURL"`             // 下载链接
	Progress     int             `json:"progress"`                // 下载进度
	TotalSize    int64           `json:"total_size"`              // 总大小
	Downloaded   int64           `json:"downloaded"`              // 已下载大小
	Checksum     string          `json:"checksum"`                // 校验和
	Status       string          `json:"status"`                  // 状态
	ErrorMessage string          `json:"error_message,omitempty"` // 错误信息
	ctx          context.Context `json:"-"`                       // 上下文
	done         chan struct{}   `json:"-"`                       // 更新任务退出信号
}

var (
	currentUpdateInfo   *updateInfo
	currentUpdateCancel context.CancelFunc
	currentUpdateMu     sync.RWMutex
)

func getCurrentUpdateInfoSnapshot() *updateInfo {
	currentUpdateMu.RLock()
	defer currentUpdateMu.RUnlock()
	if currentUpdateInfo == nil {
		return nil
	}
	info := *currentUpdateInfo
	info.ctx = nil
	return &info
}

func setCurrentUpdateInfoForTest(info *updateInfo) {
	currentUpdateMu.Lock()
	defer currentUpdateMu.Unlock()
	currentUpdateInfo = info
	currentUpdateCancel = nil
}

func updateCurrentUpdateInfo(mutator func(*updateInfo)) {
	currentUpdateMu.Lock()
	defer currentUpdateMu.Unlock()
	if currentUpdateInfo == nil {
		return
	}
	mutator(currentUpdateInfo)
}

func isCurrentUpdateRunning() bool {
	currentUpdateMu.RLock()
	defer currentUpdateMu.RUnlock()
	return isUpdateTaskRunning(currentUpdateInfo)
}

func isUpdateTaskRunning(info *updateInfo) bool {
	if info == nil {
		return false
	}
	if info.Status == string(updateStatusInstall) {
		return true
	}
	if info.done == nil {
		return !isUpdateTerminalStatus(info.Status)
	}
	select {
	case <-info.done:
		return false
	default:
		return true
	}
}

func isUpdateTerminalStatus(status string) bool {
	return status == string(updateStatusCompleted) ||
		status == string(updateStatusFailed) ||
		status == string(updateStatusCancelled)
}

// beginUpdateInstall 在任务未被取消时切换到安装阶段；与 CancelUpdate 共用锁，二者只有一个生效。
func beginUpdateInstall() bool {
	currentUpdateMu.Lock()
	defer currentUpdateMu.Unlock()
	if currentUpdateInfo == nil || currentUpdateInfo.Status == string(updateStatusCancelled) {
		return false
	}
	currentUpdateInfo.Status = string(updateStatusInstall)
	currentUpdateInfo.Progress = 100
	currentUpdateInfo.Downloaded = currentUpdateInfo.TotalSize
	return true
}

func cleanupUpdatePackageOnDownloadError(updateFilename string) {
	if updateFilename == "" {
		return
	}
	if err := os.Remove(updateFilename); err != nil && !os.IsNotExist(err) {
		helpers.AppLogger.Errorf("删除下载失败的临时更新包失败：%v", err)
	}
}

// supportsOnlineUpdate 判断下载后能否替换程序文件；其他运行方式只能手动更新，飞牛由应用商店更新。
// Docker 只认入口脚本导出的 DOCKER=1：绕过入口脚本运行的容器没有更新监视器和重启循环。
func supportsOnlineUpdate() bool {
	if helpers.IsFnOS {
		return false
	}
	return runtime.GOOS == "windows" || os.Getenv("DOCKER") == "1" || isSystemdOnlineUpdate()
}

// isSystemdOnlineUpdate 判断是否由允许在线更新的 systemd 服务管理：linux-init.sh 创建的服务写入
// QMS_SYSTEMD_UPDATE=1 并使用 Restart=always，systemd 为服务进程设置 INVOCATION_ID。
func isSystemdOnlineUpdate() bool {
	return runtime.GOOS == "linux" && os.Getenv("DOCKER") != "1" &&
		os.Getenv("QMS_SYSTEMD_UPDATE") == "1" && os.Getenv("INVOCATION_ID") != ""
}

// runsInstalledBinary 判断运行中的程序就是 rootDir 下的 QMediaSync；os.Executable 在 Linux 上返回解析符号链接后的路径。
func runsInstalledBinary(rootDir string) bool {
	exe, err := os.Executable()
	return err == nil && exe == filepath.Join(rootDir, "QMediaSync")
}

// isDirWritable 通过创建临时文件判断目录可写，能同时反映权限和只读挂载。
func isDirWritable(dir string) bool {
	file, err := os.CreateTemp(dir, ".qms-write-check-*")
	if err != nil {
		return false
	}
	name := file.Name()
	file.Close()
	os.Remove(name)
	return true
}

// verifyUpdatePackage 用 release 附带的 checksums.txt 校验下载包的 sha256。
// 旧版本 release 没有 checksums.txt：systemd 路径拒绝更新，Docker/Windows 仅告警以免无法升级到旧 release。
func verifyUpdatePackage(ctx context.Context, httpProxy, checksumURL, packagePath string, requireChecksum bool) error {
	if checksumURL == "" {
		if requireChecksum {
			return fmt.Errorf("该版本未发布 %s，无法校验更新包", updater.ChecksumsAssetName)
		}
		helpers.AppLogger.Warnf("该版本未发布 %s，跳过更新包校验", updater.ChecksumsAssetName)
		return nil
	}
	checksumFile := packagePath + ".checksums"
	defer os.Remove(checksumFile)
	if err := helpers.DownloadFileWithProgress(ctx, httpProxy, checksumURL, checksumFile, v115open.DEFAULTUA, nil); err != nil {
		return fmt.Errorf("下载校验文件失败：%w", err)
	}
	content, err := os.ReadFile(checksumFile)
	if err != nil {
		return err
	}
	name := filepath.Base(packagePath)
	var expected string
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if expected == "" {
		return fmt.Errorf("校验文件中未找到 %s", name)
	}
	f, err := os.Open(packagePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if actual := hex.EncodeToString(h.Sum(nil)); actual != expected {
		return fmt.Errorf("更新包 sha256 不匹配，期望 %s，实际 %s", expected, actual)
	}
	return nil
}

// runSystemdUpdate 安装已下载的更新包；成功后标记完成并退出进程，由 systemd 按 Restart=always 启动新版本。
func runSystemdUpdate(archivePath string) {
	err := installSystemdUpdate(archivePath, helpers.RootDir)
	cleanupUpdatePath(archivePath)
	if err != nil {
		helpers.AppLogger.Errorf("安装更新失败：%v", err)
		updateCurrentUpdateInfo(func(info *updateInfo) {
			info.Status = string(updateStatusFailed)
			info.ErrorMessage = err.Error()
		})
		return
	}
	updateCurrentUpdateInfo(func(info *updateInfo) {
		info.Status = string(updateStatusCompleted)
		info.Progress = 100
	})
	helpers.AppLogger.Infof("更新文件已替换，即将退出并由 systemd 启动新版本")
	// 留出进度轮询读到完成状态的时间。
	time.Sleep(3 * time.Second)
	// 自行退出没有 systemd 停止超时兜底；关闭流程卡住时强制退出，Restart=always 同样会拉起新版本。
	time.AfterFunc(60*time.Second, func() { os.Exit(1) })
	helpers.StopApp()
}

func cleanupUpdatePath(path string) {
	if err := os.RemoveAll(path); err != nil {
		helpers.AppLogger.Warnf("清理更新临时文件失败：%v", err)
	}
}

func validateReleaseFiles(srcDir, binaryName string) error {
	if info, err := os.Stat(filepath.Join(srcDir, binaryName)); err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("更新包缺少 %s", binaryName)
	}
	if info, err := os.Stat(filepath.Join(srcDir, "web_statics")); err != nil || !info.IsDir() {
		return errors.New("更新包缺少 web_statics")
	}
	return nil
}

// installWindowsUpdate 准备供独立更新进程替换的文件；每次先清理旧内容，失败不保留残缺目录。
func installWindowsUpdate(archivePath string) (err error) {
	stagingDir := filepath.Join(helpers.ConfigDir, "update")
	if err := os.RemoveAll(stagingDir); err != nil {
		return fmt.Errorf("清理更新目录失败：%w", err)
	}
	defer func() {
		if err != nil {
			cleanupUpdatePath(stagingDir)
		}
	}()
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return fmt.Errorf("创建更新目录失败：%w", err)
	}
	if err := helpers.ExtractZip(archivePath, stagingDir); err != nil {
		return fmt.Errorf("解压更新包失败：%w", err)
	}
	if err := validateReleaseFiles(stagingDir, "QMediaSync.exe"); err != nil {
		return err
	}
	cleanupUpdatePath(archivePath)
	return nil
}

// installDockerUpdate 在安装目录同一文件系统内写完更新包，再原子改名通知更新监视器。
func installDockerUpdate(archivePath string) error {
	stagingDir, err := os.MkdirTemp(filepath.Dir(archivePath), "qms-extract-*")
	if err != nil {
		return fmt.Errorf("创建解压目录失败：%w", err)
	}
	defer cleanupUpdatePath(stagingDir)
	if err := helpers.ExtractTarGz(archivePath, stagingDir); err != nil {
		return fmt.Errorf("解压更新包失败：%w", err)
	}
	srcDir := stagingDir
	if entries, err := os.ReadDir(stagingDir); err == nil && len(entries) == 1 && entries[0].IsDir() {
		srcDir = filepath.Join(stagingDir, entries[0].Name())
	}
	if err := validateReleaseFiles(srcDir, "QMediaSync"); err != nil {
		return err
	}
	for _, name := range []string{"QMediaSync", "scripts/docker-entrypoint.sh", "scripts/watch_update.sh"} {
		if err := os.Chmod(filepath.Join(srcDir, name), 0755); err != nil {
			return fmt.Errorf("设置 %s 执行权限失败：%w", name, err)
		}
	}
	file, err := os.CreateTemp(helpers.RootDir, ".qms-update-*")
	if err != nil {
		return fmt.Errorf("创建更新包失败：%w", err)
	}
	defer cleanupUpdatePath(file.Name())
	cmd := exec.Command("tar", "-czf", "-", "-C", srcDir, "QMediaSync", "web_statics", "scripts")
	cmd.Stdout = file
	if err := errors.Join(cmd.Run(), file.Close()); err != nil {
		return fmt.Errorf("打包更新文件失败：%w", err)
	}
	if err := os.Rename(file.Name(), filepath.Join(helpers.RootDir, "qms.update.tar.gz")); err != nil {
		return fmt.Errorf("交付更新包失败：%w", err)
	}
	cleanupUpdatePath(archivePath)
	return nil
}

// installSystemdUpdate 把更新包解压到安装目录下的 update/ 再替换程序文件；解压目录与目标在同一文件系统，
// 可以直接改名。无论成败都会删除解压目录。
func installSystemdUpdate(archivePath, rootDir string) error {
	stagingDir := filepath.Join(rootDir, "update")
	if err := os.RemoveAll(stagingDir); err != nil {
		return fmt.Errorf("清理更新目录失败：%w", err)
	}
	defer cleanupUpdatePath(stagingDir)
	if err := helpers.ExtractTarGz(archivePath, stagingDir); err != nil {
		return fmt.Errorf("解压更新包失败：%w", err)
	}
	// 发布包内有一层 QMediaSync_linux_<arch>/ 目录。
	srcDir := stagingDir
	if entries, err := os.ReadDir(stagingDir); err == nil && len(entries) == 1 && entries[0].IsDir() {
		srcDir = filepath.Join(stagingDir, entries[0].Name())
	}
	return InstallReleaseFiles(srcDir, rootDir, "QMediaSync")
}

// renameInstallPath 供测试注入替换中途的失败。
var renameInstallPath = os.Rename

// InstallReleaseFiles 替换程序和 web_statics/，旧文件移到 rootDir/old/；任一步失败都恢复已处理的条目。
// binaryName 由 Windows 或 systemd 更新入口指定；scripts/ 仅供 Docker 使用。
func InstallReleaseFiles(srcDir, rootDir, binaryName string) (err error) {
	if err := validateReleaseFiles(srcDir, binaryName); err != nil {
		return err
	}
	binary := filepath.Join(srcDir, binaryName)
	if err := os.Chmod(binary, 0755); err != nil {
		return fmt.Errorf("设置 %s 执行权限失败：%w", binaryName, err)
	}
	backupDir := filepath.Join(rootDir, "old")
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("清理旧版本备份失败：%w", err)
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("创建旧版本备份目录失败：%w", err)
	}
	// moved 中的条目原文件已移入备份目录（或原本不存在），回滚时目标位置只可能是新文件或空。
	var moved []string
	defer func() {
		if err == nil {
			return
		}
		for i := len(moved) - 1; i >= 0; i-- {
			dst := filepath.Join(rootDir, moved[i])
			backup := filepath.Join(backupDir, moved[i])
			if removeErr := os.RemoveAll(dst); removeErr != nil {
				err = errors.Join(err, fmt.Errorf("回滚时删除新版本 %s 失败：%w", moved[i], removeErr))
				continue
			}
			if _, statErr := os.Lstat(backup); statErr != nil {
				continue
			}
			if restoreErr := os.Rename(backup, dst); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("回滚时恢复 %s 失败：%w", moved[i], restoreErr))
			}
		}
	}()
	for _, name := range []string{binaryName, "web_statics"} {
		dst := filepath.Join(rootDir, name)
		if err := renameInstallPath(dst, filepath.Join(backupDir, name)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("备份 %s 失败：%w", name, err)
		}
		moved = append(moved, name)
		if err := renameInstallPath(filepath.Join(srcDir, name), dst); err != nil {
			return fmt.Errorf("替换 %s 失败：%w", name, err)
		}
	}
	return nil
}

// GetLastRelease 获取最新版本列表
// @Summary 获取最新版本
// @Description 获取 GitHub 上最新的 5 个稳定版本
// @Tags 更新管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /update/last [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetLastRelease(c *gin.Context) {
	force := c.Query("force")
	channel := c.Query("channel")
	if channel == "" {
		channel = "github"
	}
	passCache := false
	if force == "1" {
		passCache = true
	}
	releases := listReleases(passCache, channel)
	if releases == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取最新版本失败", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取最新版本成功", Data: releases})
}

// UpdateToVersion 更新到指定版本
// @Summary 更新到指定版本
// @Description 下载并安装指定版本的更新包
// @Tags 更新管理
// @Accept json
// @Produce json
// @Param version body string true "版本号"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /update/to-version [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateToVersion(c *gin.Context) {
	// 不支持的运行方式下载后无法安装，会误报更新完成。
	if !supportsOnlineUpdate() {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "当前运行方式不支持在线更新，请手动下载安装", Data: nil})
		return
	}
	// systemd 安装在安装目录内替换文件，服务用户无写权限时下载后必然失败。
	if isSystemdOnlineUpdate() && !isDirWritable(helpers.RootDir) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "安装目录不可写，无法在线更新，请手动下载安装", Data: nil})
		return
	}
	// 只替换安装目录下的 QMediaSync；程序改名或不在安装目录运行时替换不会生效，同样会误报完成。
	if isSystemdOnlineUpdate() && !runsInstalledBinary(helpers.RootDir) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "当前运行方式不支持在线更新，请手动下载安装", Data: nil})
		return
	}
	if isCurrentUpdateRunning() {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "正在更新中", Data: nil})
		return
	}
	var req requests.UpdateVersionRequest
	if perr := c.ShouldBindJSON(&req); perr != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	version := req.Version

	var downloadURL, checksumURL string
	var err error
	var connType github.ConnectionType
	var httpProxy string

	// Gitee 渠道暂未启用：本仓库尚未在 Gitee 发布。保留实现，待建立 Gitee 镜像仓库后取消注释并恢复 if/else 即可。
	// if channel == "gitee" {
	// 	giteeUpdater := updater.NewGiteeUpdater("chen8945", "QMediaSync", helpers.Version)
	// 	downloadURL, _, _, err = giteeUpdater.GetReleaseDownloadURL(version)
	// 	if err != nil {
	// 		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "版本不存在", Data: nil})
	// 		return
	// 	}
	// } else {
	{
		ghUpdater := updater.NewGitHubUpdater("chen8945", "QMediaSync", helpers.Version)
		downloadURL, checksumURL, _, err = ghUpdater.GetReleaseDownloadURL(version)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "版本不存在", Data: nil})
			return
		}
		var proxyUrl string
		connType, proxyUrl = helpers.TestGithub(downloadURL, models.SettingsGlobal.HttpProxy)
		if connType == github.ConnectionTypeFailed {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "无法连接 GitHub，且未设置代理，无法升级", Data: nil})
			return
		}
		if connType == github.ConnectionTypeGitHubProxy {
			if checksumURL != "" {
				checksumURL = strings.Replace(proxyUrl, downloadURL, checksumURL, 1)
			}
			downloadURL = proxyUrl
		}
		if connType == github.ConnectionTypeProxy {
			httpProxy = models.SettingsGlobal.HttpProxy
		}
	}
	// systemd 替换文件前必须校验更新包；未发布校验文件的旧版本在下载前拒绝。
	if isSystemdOnlineUpdate() && checksumURL == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "该版本未发布校验文件，无法在线更新，请手动下载安装", Data: nil})
		return
	}
	currentUpdateMu.Lock()
	if isUpdateTaskRunning(currentUpdateInfo) {
		currentUpdateMu.Unlock()
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "正在更新中", Data: nil})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	info := &updateInfo{
		Version:     version,
		DownloadURL: downloadURL,
		Progress:    0,
		TotalSize:   0,
		Downloaded:  0,
		Status:      string(updateStatusDownloading),
		ctx:         ctx,
		done:        make(chan struct{}),
	}
	currentUpdateInfo = info
	currentUpdateCancel = cancel
	currentUpdateMu.Unlock()
	// 启动一个更新协程，然后返回
	go func() {
		defer close(info.done)
		updateFilePath := filepath.Join(helpers.ConfigDir, "tmp")
		// 获取 URL 中的文件名
		filename := filepath.Base(downloadURL)
		updateFilename := filepath.Join(updateFilePath, filename)
		failUpdate := func(err error) {
			cleanupUpdatePackageOnDownloadError(updateFilename)
			updateCurrentUpdateInfo(func(info *updateInfo) {
				if info.Status != string(updateStatusCancelled) {
					info.Status = string(updateStatusFailed)
					info.ErrorMessage = err.Error()
				}
			})
		}
		if err := os.MkdirAll(updateFilePath, 0777); err != nil {
			helpers.AppLogger.Errorf("创建更新临时目录失败：%v", err)
			failUpdate(err)
			return
		}
		if err := os.Remove(updateFilename); err != nil && !os.IsNotExist(err) {
			failUpdate(fmt.Errorf("清理旧更新包失败：%w", err))
			return
		}
		// 下载文件
		err := helpers.DownloadFileWithProgress(ctx, httpProxy, downloadURL, updateFilename, v115open.DEFAULTUA, func(progress int64, total int64) {
			updateCurrentUpdateInfo(func(info *updateInfo) {
				if total > 0 {
					info.Progress = int(float64(progress) / float64(total) * 100)
				}
				info.TotalSize = total
				info.Downloaded = progress
			})
		})
		if err != nil {
			helpers.AppLogger.Errorf("下载文件失败：%v", err)
			failUpdate(err)
			return
		}
		if err := verifyUpdatePackage(ctx, httpProxy, checksumURL, updateFilename, isSystemdOnlineUpdate()); err != nil {
			helpers.AppLogger.Errorf("更新包校验失败：%v", err)
			failUpdate(err)
			return
		}
		// 取消先于安装时删除下载的文件；进入安装后取消接口不再接受请求。
		if !beginUpdateInstall() {
			os.Remove(updateFilename)
			helpers.AppLogger.Infof("更新已取消，删除下载的文件：%s", updateFilename)
			return
		}
		if isSystemdOnlineUpdate() {
			runSystemdUpdate(updateFilename)
			return
		}
		if runtime.GOOS == "windows" {
			err = installWindowsUpdate(updateFilename)
			if err == nil {
				err = triggerUpdate()
			}
		} else {
			err = installDockerUpdate(updateFilename)
		}
		if err != nil {
			helpers.AppLogger.Errorf("安装更新失败：%v", err)
			failUpdate(err)
		}
		// Windows/Docker 交付后仍处于安装中；前端在进程重启后核对实际版本。
	}()
	// 返回更新信息
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新开始", Data: getCurrentUpdateInfoSnapshot()})
}

// UpdateProgress 获取更新进度
// @Summary 获取更新进度
// @Description 查询当前更新任务的下载和安装进度
// @Tags 更新管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /update/progress [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateProgress(c *gin.Context) {
	info := getCurrentUpdateInfoSnapshot()
	if info == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未开始更新", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新进度", Data: info})
}

// CancelUpdate 取消更新
// @Summary 取消更新
// @Description 取消仍在下载的更新任务；进入安装后不能取消
// @Tags 更新管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /update/cancel [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CancelUpdate(c *gin.Context) {
	currentUpdateMu.Lock()
	cancel := currentUpdateCancel
	// 上一轮已结束时新任务可能尚未换入，不能把旧终态改写为已取消。
	if !isUpdateTaskRunning(currentUpdateInfo) {
		currentUpdateMu.Unlock()
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未开始更新", Data: nil})
		return
	}
	// 下载完成后开始替换文件并重启，取消已无法阻止。
	if status := currentUpdateInfo.Status; status != string(updateStatusDownloading) && status != string(updateStatusCancelled) {
		currentUpdateMu.Unlock()
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "正在安装更新，无法取消", Data: nil})
		return
	}
	currentUpdateInfo.Status = string(updateStatusCancelled)
	currentUpdateInfo.ErrorMessage = "用户取消更新"
	currentUpdateCancel = nil
	currentUpdateMu.Unlock()

	if cancel != nil {
		cancel()
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新已取消", Data: getCurrentUpdateInfoSnapshot()})
}

// listReleases 列出最新版本
func listReleases(passCache bool, channel string) []version {
	cacheKey := "latest_releases"
	if channel == "gitee" {
		cacheKey = "latest_releases_gitee"
	}
	// 直接读取缓存
	if !passCache {
		cached := db.Cache.Get(cacheKey)
		if cached != nil {
			helpers.AppLogger.Infof("使用缓存的最新版本列表（channel：%s）", channel)
			var versionList []version
			err := json.Unmarshal(cached, &versionList)
			if err == nil {
				fillCachedPublishedAt(versionList)
				return versionList
			} else {
				helpers.AppLogger.Infof("解析缓存的最新版本列表失败：%v", err)
			}
		}
	}

	var releases []updater.ReleaseInfo
	var err error

	// Gitee 渠道暂未启用：本仓库尚未在 Gitee 发布。保留实现，待建立 Gitee 镜像仓库后取消注释并恢复 if/else 即可。
	// if channel == "gitee" {
	// 	giteeUpdater := updater.NewGiteeUpdater("chen8945", "QMediaSync", helpers.Version)
	// 	giteeUpdater.IncludePreRelease = false
	// 	releases, err = giteeUpdater.GetLatestStableReleases(5)
	// 	if err != nil {
	// 		helpers.AppLogger.Errorf("查找 Gitee 最新版本失败：%v", err)
	// 		return nil
	// 	}
	// } else {
	{
		ghUpdater := updater.NewGitHubUpdater("chen8945", "QMediaSync", helpers.Version)
		ghUpdater.IncludePreRelease = false
		releases, err = ghUpdater.GetLatestStableReleases(5)
		if err != nil {
			helpers.AppLogger.Errorf("查找 GitHub 最新版本失败：%v", err)
			return nil
		}
	}

	if len(releases) == 0 {
		helpers.AppLogger.Infof("未找到最新版本")
		return nil
	}

	helpers.AppLogger.Infof("找到 %s/%s 的 %d 个最新版本（channel：%s）", "chen8945", "QMediaSync", len(releases), channel)
	versionList := make([]version, 0)
	for i, release := range releases {
		versionList = append(versionList, version{
			Version:     release.Version,
			PublishedAt: release.PublishedAt.Unix(),
			Date:        release.PublishedAt.Format("2006-01-02 15:04:05"),
			Note:        release.ReleaseNotes,
			Url:         release.PageURL,
			Current:     release.Version == helpers.Version,
			Latest:      i == 0,
		})
	}
	// 缓存 1 小时
	versionListStr, _ := json.Marshal(versionList)
	if versionListStr != nil {
		db.Cache.Set(cacheKey, versionListStr, 3600)
	}
	return versionList
}

func fillCachedPublishedAt(versionList []version) {
	for i := range versionList {
		if versionList[i].PublishedAt != 0 {
			continue
		}
		if versionList[i].Date == "" {
			continue
		}
		publishedAt, err := time.Parse("2006-01-02 15:04:05", versionList[i].Date)
		if err != nil {
			continue
		}
		versionList[i].PublishedAt = publishedAt.Unix()
	}
}

var startUpdateProcess = helpers.StartNewProcess

func triggerUpdate() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取程序路径失败：%w", err)
	}
	updateDir := filepath.Join(helpers.ConfigDir, "update")
	if !helpers.PathExists(updateDir) {
		return fmt.Errorf("更新目录不存在：%s", updateDir)
	}

	if !startUpdateProcess(exePath, updateDir) {
		return errors.New("启动更新进程失败")
	}

	helpers.StopApp()
	return nil
}
