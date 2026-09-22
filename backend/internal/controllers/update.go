package controllers

import (
	"context"
	"encoding/json"
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

func cleanupUpdatePackageOnDownloadError(updateFilename string) {
	if updateFilename == "" {
		return
	}
	if err := os.Remove(updateFilename); err != nil && !os.IsNotExist(err) {
		helpers.AppLogger.Errorf("删除下载失败的临时更新包失败：%v", err)
	}
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

	var downloadURL string
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
		downloadURL, _, _, err = ghUpdater.GetReleaseDownloadURL(version)
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
			downloadURL = proxyUrl
		}
		if connType == github.ConnectionTypeProxy {
			httpProxy = models.SettingsGlobal.HttpProxy
		}
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
		if helpers.PathExists(updateFilePath) {
			os.MkdirAll(updateFilePath, 0777)
		}
		// 获取 URL 中的文件名
		filename := filepath.Base(downloadURL)
		updateFilename := filepath.Join(updateFilePath, filename)
		if helpers.PathExists(updateFilename) {
			os.Remove(updateFilename)
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
			cleanupUpdatePackageOnDownloadError(updateFilename)
			updateCurrentUpdateInfo(func(info *updateInfo) {
				if info.Status != string(updateStatusCancelled) {
					info.Status = string(updateStatusFailed)
					info.ErrorMessage = err.Error()
				}
			})
			return
		}
		// 检查上下文是否被取消
		select {
		case <-ctx.Done():
			// 上下文被取消，删除下载的文件
			os.Remove(updateFilename)
			helpers.AppLogger.Infof("更新已取消，删除下载的文件：%s", updateFilename)
			return
		default:
			// 上下文未被取消，继续执行安装
		}
		// 修改为安装中
		updateCurrentUpdateInfo(func(info *updateInfo) {
			info.Status = string(updateStatusInstall)
			info.Progress = 100
			info.Downloaded = info.TotalSize
		})
		// Windows 平台解压到 helpers.ConfigDir/update 目录下。
		if runtime.GOOS == "windows" {
			updateDestpath := filepath.Join(helpers.ConfigDir, "update")
			if helpers.PathExists(updateDestpath) {
				os.RemoveAll(updateDestpath)
			}
			os.MkdirAll(updateDestpath, 0777)
			// 复制文件到 update 目录
			goos := runtime.GOOS
			goarch := runtime.GOARCH
			if goarch == "amd64" {
				goarch = "x86_64"
			}
			srcPath := filepath.Join(updateFilePath, "qmediasync_"+goos+"_"+goarch)
			// 解压后将文件从 qmediasync_GOOS_GOARCH 目录复制到 update 目录。
			helpers.ExtractZip(updateFilename, srcPath)
			err = helpers.MoveDir(srcPath, updateDestpath)
			if err != nil {
				helpers.AppLogger.Errorf("移动文件失败：%v", err)
			} else {
				// 文件已复制到更新目录
				helpers.AppLogger.Infof("文件已复制到更新目录：%s", updateDestpath)
			}
			// 删除解压目录
			rerr := os.RemoveAll(srcPath)
			if rerr != nil {
				helpers.AppLogger.Errorf("删除解压目录失败：%v", rerr)

			} else {
				// 解压目录已删除
				helpers.AppLogger.Infof("解压目录已删除：%s", srcPath)
			}
			// 删除压缩包
			err = os.Remove(updateFilename)
			if err != nil {
				helpers.AppLogger.Errorf("删除压缩包失败：%v", err)
			} else {
				// 压缩包已删除
				helpers.AppLogger.Infof("压缩包已删除：%s", updateFilename)
			}
			// 启动更新脚本
			if helpers.IsRelease {
				updateCurrentUpdateInfo(func(info *updateInfo) {
					if info.Status != string(updateStatusCancelled) && info.Status != string(updateStatusFailed) {
						info.Status = string(updateStatusCompleted)
						info.Progress = 100
					}
				})
				// 启动更新脚本
				triggerUpdate()
			} else {
				// 模拟更新结束
				updateCurrentUpdateInfo(func(info *updateInfo) {
					if info.Status != string(updateStatusCancelled) && info.Status != string(updateStatusFailed) {
						info.Status = string(updateStatusCompleted)
						info.Progress = 100
					}
				})
			}
			return
		}
		if helpers.IsRunningInDocker() {
			folerName := filepath.Base(downloadURL)
			folerName = strings.ReplaceAll(folerName, ".tar.gz", "")
			// 重新打包，将压缩包中的文件从 qmediasync_GOOS_GOARCH 目录打包到压缩包根目录。
			// 解压后将文件从 qmediasync_GOOS_GOARCH 目录复制到 update 目录。
			helpers.ExtractTarGz(updateFilename, updateFilePath)
			// 复制文件到 update 目录
			srcPath := filepath.Join(helpers.ConfigDir, "tmp", folerName)
			// 给 srcPath/scripts 下的所有脚本增加执行权限。
			scriptsPath := filepath.Join(srcPath, "scripts")
			if helpers.PathExists(scriptsPath) {
				// 遍历 scripts 目录下的所有文件。
				files, rerr := os.ReadDir(scriptsPath)
				if rerr != nil {
					helpers.AppLogger.Errorf("读取目录失败：%v", rerr)
				} else {
					for _, file := range files {
						if file.IsDir() {
							continue
						}
						// 给文件增加执行权限
						err = os.Chmod(filepath.Join(scriptsPath, file.Name()), 0777)
						if err != nil {
							helpers.AppLogger.Errorf("增加执行权限失败：%v", err)
						} else {
							helpers.AppLogger.Infof("文件已增加执行权限：%s", filepath.Join(scriptsPath, file.Name()))
						}
					}
				}
			}
			exeFile := filepath.Join(srcPath, "QMediaSync")
			if helpers.PathExists(exeFile) {
				os.Chmod(exeFile, 0777)
			}

			destFile := filepath.Join(helpers.ConfigDir, "tmp", "qms.update.tar.gz")
			if helpers.PathExists(destFile) {
				os.Remove(destFile)
			}
			// 将 srcPath 内的文件打包到 destFile。
			// helpers.CreateTarGz(srcPath, destFile)
			err = exec.Command("tar", "-czvf", destFile, "-C", srcPath, ".").Run()
			if err != nil {
				helpers.AppLogger.Errorf("打包文件失败：%v", err)
			} else {
				// 压缩包已创建
				helpers.AppLogger.Infof("压缩包已创建：%s", destFile)
			}
			// 删除更新目录
			updatePath := filepath.Join(helpers.RootDir, "update")
			if helpers.PathExists(updatePath) {
				os.RemoveAll(updatePath)
				helpers.AppLogger.Infof("已删除旧更新目录：%s", updatePath)
			}
			destGzFile := filepath.Join(helpers.RootDir, "qms.update.tar.gz")
			if helpers.PathExists(destGzFile) {
				os.Remove(destGzFile)
			}
			// 将文件移动到 rootDir。
			err = helpers.CopyFile(destFile, destGzFile)
			if err != nil {
				helpers.AppLogger.Errorf("移动文件失败：%v", err)
			} else {
				// 文件已移动到更新目录
				helpers.AppLogger.Infof("文件已移动到更新目录：%s", destGzFile)
			}
			// 删除解压目录
			err = os.RemoveAll(srcPath)
			if err != nil {
				helpers.AppLogger.Errorf("删除解压目录失败：%v", err)
			} else {
				// 解压目录已删除
				helpers.AppLogger.Infof("解压目录已删除：%s", srcPath)
			}
			// 删除下载文件
			err = os.Remove(updateFilename)
			if err != nil {
				helpers.AppLogger.Errorf("删除压缩包失败：%v", err)
			} else {
				// 压缩包已删除
				helpers.AppLogger.Infof("压缩包已删除：%s", updateFilename)
			}
			err = os.Remove(destFile)
			if err != nil {
				helpers.AppLogger.Errorf("删除压缩包失败：%v", err)
			} else {
				// 压缩包已删除
				helpers.AppLogger.Infof("压缩包已删除：%s", destFile)
			}
			// 等待更新器重启应用
		}
		updateCurrentUpdateInfo(func(info *updateInfo) {
			if info.Status != string(updateStatusCancelled) && info.Status != string(updateStatusFailed) {
				info.Status = string(updateStatusCompleted)
				info.Progress = 100
			}
		})
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
// @Description 取消正在进行的更新任务
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

func triggerUpdate() {
	exePath, err := os.Executable()
	if err != nil {
		helpers.AppLogger.Errorf("获取程序路径失败：%v", err)
		return
	}
	updateDir := filepath.Join(helpers.ConfigDir, "update")
	if !helpers.PathExists(updateDir) {
		helpers.AppLogger.Errorf("更新目录不存在：%s", updateDir)
		return
	}

	if !helpers.StartNewProcess(exePath, updateDir) {
		helpers.AppLogger.Errorf("启动更新进程失败：%v", err)
		return
	}

	helpers.StopApp()
}
