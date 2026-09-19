package controllers

import (
	"net/http"

	"qmediasync/internal/emby"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
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
// @Router /emby/sync-start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StartEmbySync(c *gin.Context) {
	// 检查是否已有任务在运行
	if emby.IsEmbySyncRunning() {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "已有 Emby 条目同步任务正在运行，请稍后再试"})
		return
	}

	go func() {
		if _, err := emby.PerformEmbySync(); err != nil {
			helpers.AppLogger.Warnf("同步 Emby 条目到本地失败：%v", err)
		}
	}()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "Emby 条目同步任务已启动"})
}

// GetEmbySyncStatus 同步状态
// @Summary 获取 Emby 条目同步状态
// @Description 获取 Emby 条目同步到本地的当前状态和信息
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /emby/sync-status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetEmbySyncStatus(c *gin.Context) {
	config, err := models.GetEmbyConfigFromDB()
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "尚未配置 Emby", Data: gin.H{"exists": false}})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取配置失败：" + err.Error()})
		return
	}
	helpers.AppLogger.Infof("获取 Emby 同步状态，最后同步时间：%s", helpers.FormatUnixLogTime(config.LastSyncTime))
	total, _ := models.GetEmbyMediaItemsCount()
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
			"is_running":               config.IsRunning || emby.IsEmbySyncRunning(),
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
func GetEmbyLibraries(c *gin.Context) {
	config, err := models.GetEmbyConfig()
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "尚未配置 Emby"})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 Emby 配置失败：" + err.Error()})
		return
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "Emby URL 或 API Key 为空"})
		return
	}

	// 直接从 Emby 查询媒体库，并写入本地 emby_libraries 表
	client := embyclientrestgo.NewClient(config.EmbyUrl, config.EmbyApiKey)
	libs, err := client.GetAllMediaLibraries()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询 Emby 媒体库失败：" + helpers.RedactSensitiveLog(err.Error(), config.EmbyApiKey)})
		return
	}
	if err := models.UpsertEmbyLibraries(libs); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "写入媒体库表失败：" + err.Error()})
		return
	}

	// 清理已不在 Emby 中存在的媒体库记录
	activeLibraryIds := make([]string, 0, len(libs))
	for _, lib := range libs {
		activeLibraryIds = append(activeLibraryIds, lib.ID)
	}
	if err := models.CleanupDeletedEmbyLibraries(activeLibraryIds); err != nil {
		helpers.AppLogger.Warnf("清理已删除媒体库记录失败：%v", err)
	}

	libraries, err := models.GetAllEmbyLibraries()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取媒体库列表失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取媒体库列表成功", Data: libraries})
}
