package controllers

import (
	"net/http"

	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartEmbySync 手动触发 Emby 条目同步。
// @Summary 启动 Emby 条目同步
// @Description 手动触发同步 Emby 条目到本地任务
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /emby/sync/start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StartEmbySync(c *gin.Context) { startMediaLibrarySync(c, models.MediaServerEmby) }

// StartJellyfinSync 手动启动 Jellyfin 条目同步。
func StartJellyfinSync(c *gin.Context) { startMediaLibrarySync(c, models.MediaServerJellyfin) }
func startMediaLibrarySync(c *gin.Context, kind models.MediaServerType) {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if err != nil || !config.Enabled || config.SyncEnabled != 1 || config.EmbyUrl == "" || config.EmbyApiKey == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先配置并启用媒体库同步"})
		return
	}
	// 检查是否已有任务在运行
	if emby.IsMediaLibrarySyncRunning(kind) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "已有媒体库条目同步任务正在运行，请稍后再试"})
		return
	}

	go func() {
		if _, err := emby.PerformMediaLibrarySync(kind); err != nil {
			helpers.AppLogger.Warnf("%s 条目同步失败：%v", kind.DisplayName(), err)
		}
	}()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "媒体库条目同步任务已启动"})
}

// GetEmbySyncStatus 同步状态
// @Summary 获取 Emby 条目同步状态
// @Description 获取 Emby 条目同步到本地的当前状态和信息
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /emby/sync/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetEmbySyncStatus(c *gin.Context) { getMediaLibrarySyncStatus(c, models.MediaServerEmby) }

// GetJellyfinSyncStatus 获取 Jellyfin 的独立同步状态。
func GetJellyfinSyncStatus(c *gin.Context) { getMediaLibrarySyncStatus(c, models.MediaServerJellyfin) }
func getMediaLibrarySyncStatus(c *gin.Context, kind models.MediaServerType) {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "尚未配置媒体库", Data: gin.H{"exists": false}})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取配置失败：" + err.Error()})
		return
	}
	helpers.AppLogger.Debugf("获取 %s 同步状态，最后同步时间：%s", kind.DisplayName(), helpers.FormatUnixLogTime(config.LastSyncTime))
	total, err := models.GetEmbyMediaItemsCount(kind)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "读取媒体库条目数量失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "获取同步状态成功",
		Data: gin.H{
			"last_sync_time":           config.LastSyncTime,
			"last_full_sync_at":        config.LastFullSyncAt,
			"last_incremental_sync_at": config.LastIncrementalSyncAt,
			"last_saved_cursor_at":     config.LastSavedCursorAt,
			"last_processed_count":     config.LastProcessedCount,
			"last_success_sync_mode":   config.LastSuccessSyncMode,
			"last_error":               config.LastError,
			"sync_mode":                config.SyncMode,
			"started_at":               config.StartedAt,
			"sync_cron":                config.SyncCron,
			"total_items":              total,
			"sync_enabled":             config.SyncEnabled,
			"is_running":               config.IsRunning || emby.IsMediaLibrarySyncRunning(kind),
		},
	})
}

// GetEmbyLibraries 获取所有可用的 Emby 媒体库。
// @Summary 获取 Emby 媒体库列表
// @Description 获取所有可用的 Emby 媒体库列表
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /emby/libraries [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetEmbyLibraries(c *gin.Context) { getMediaLibraryLibraries(c, models.MediaServerEmby) }

// GetJellyfinLibraries 查询 Jellyfin 媒体库供配置选择。
func GetJellyfinLibraries(c *gin.Context) { getMediaLibraryLibraries(c, models.MediaServerJellyfin) }
func getMediaLibraryLibraries(c *gin.Context, kind models.MediaServerType) {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "尚未配置媒体库"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取媒体库配置失败：" + err.Error()})
		return
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先保存媒体库连接"})
		return
	}

	// 查询本端媒体库，并更新带归属的本地记录。
	client := config.Client()
	libs, err := client.GetAllMediaLibrariesContext(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询媒体库失败：" + helpers.RedactSensitiveLog(err.Error(), config.EmbyApiKey)})
		return
	}
	if err := models.UpsertEmbyLibraries(kind, libs); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "写入媒体库表失败：" + err.Error()})
		return
	}

	// 只清理本端远端已经删除的媒体库记录。
	activeLibraryIds := make([]string, 0, len(libs))
	for _, lib := range libs {
		activeLibraryIds = append(activeLibraryIds, lib.ID)
	}
	if err := models.CleanupDeletedEmbyLibraries(kind, activeLibraryIds); err != nil {
		helpers.AppLogger.Warnf("清理已删除媒体库记录失败：%v", err)
	}

	libraries, err := models.GetAllEmbyLibraries(kind)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取媒体库列表失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取媒体库列表成功", Data: libraries})
}
