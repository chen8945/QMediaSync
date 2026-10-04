package controllers

import (
	"net/http"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetEmbyConfig 获取 Emby 配置。
// @Summary 获取 Emby 配置
// @Description 获取 Emby 媒体服务器的配置信息
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /emby/config [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetEmbyConfig(c *gin.Context) {
	config, err := models.ReadEmbyConfigSnapshot()
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    Success,
			Message: "获取 Emby 配置成功",
			Data:    gin.H{"exists": false},
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 Emby 配置失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "获取 Emby 配置成功",
		Data:    gin.H{"exists": true, "config": config},
	})
}

// UpdateEmbyConfig 更新 Emby 配置。
// @Summary 更新 Emby 配置
// @Description 更新 Emby 媒体服务器的配置信息
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Param emby_url body string false "Emby 服务器地址"
// @Param emby_api_key body string false "Emby API Key"
// @Param enable_delete_netdisk body integer false "是否启用网盘删除"
// @Param enable_refresh_library body integer false "是否启用库刷新"
// @Param enable_media_notification body integer false "是否启用媒体通知"
// @Param enable_extract_media_info body integer false "是否启用提取媒体信息"
// @Param enable_auth body integer false "是否启用 Webhook 鉴权"
// @Param sync_enabled body integer false "是否启用同步"
// @Param sync_cron body string false "同步 Cron 表达式"
// @Param enable_daily_first_full_sync body integer false "是否启用每日首次全量同步"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /emby/config [put]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateEmbyConfig(c *gin.Context) {
	var req requests.UpdateEmbyConfigRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error()})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}

	config, err := models.ReadEmbyConfigSnapshot()
	if err != nil && err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询 Emby 配置失败：" + err.Error()})
		return
	}
	isNew := err == gorm.ErrRecordNotFound
	oldSyncEnabled := 0
	oldSyncCron := req.SyncCron
	if !isNew {
		oldSyncEnabled = config.SyncEnabled
		oldSyncCron = config.SyncCron
	}
	if isNew {
		config = &models.EmbyConfig{}
	}
	if req.SyncCron == "" {
		req.SyncCron = "0 * * * *"
	}
	oldIdentity := models.EmbyConfigIdentity(config)
	config.EmbyUrl = req.EmbyURL
	config.EmbyApiKey = req.EmbyAPIKey
	config.EnableDeleteNetdisk = req.EnableDeleteNetdisk
	config.EnableRefreshLibrary = req.EnableRefreshLibrary
	config.EnableMediaNotification = req.EnableMediaNotification
	config.EnableExtractMediaInfo = req.EnableExtractMediaInfo
	config.EnableAuth = req.EnableAuth
	config.SyncEnabled = req.SyncEnabled
	config.SyncCron = req.SyncCron
	config.SelectedLibraries = req.SelectedLibraries
	config.SyncAllLibraries = req.SyncAllLibraries
	if req.EnableDailyFirstFullSync != nil {
		config.EnableDailyFirstFullSync = *req.EnableDailyFirstFullSync
	} else if isNew {
		config.EnableDailyFirstFullSync = 1
	}
	config.EnablePlaybackOverview = req.EnablePlaybackOverview
	config.EnablePlaybackProgress = req.EnablePlaybackProgress
	// if req.DeleteNetdiskLibrary != nil {
	// 	config.DeleteNetdiskLibrary = strings.Join(req.DeleteNetdiskLibrary, ",")
	// }
	if config.SyncEnabled == 0 {
		config.EnableDeleteNetdisk = 0
		config.EnableRefreshLibrary = 0
		// config.DeleteNetdiskLibrary = ""
	}

	// 设置保存只拥有配置字段，不能回写读取期间变化的同步游标和运行状态。
	var saveErr error
	if isNew {
		saveErr = db.Db.Create(config).Error
	} else {
		fields := []string{
			"emby_url", "emby_api_key", "enable_delete_netdisk", "enable_refresh_library",
			"enable_media_notification", "enable_extract_media_info", "enable_auth", "sync_enabled", "sync_cron",
			"selected_libraries", "sync_all_libraries", "enable_daily_first_full_sync", "enable_playback_overview", "enable_playback_progress",
		}
		if oldIdentity != models.EmbyConfigIdentity(config) {
			config.LastSavedCursorAt, config.LastFullSyncAt, config.LastIncrementalSyncAt, config.LastSyncTime = 0, 0, 0, 0
			config.LastSuccessSyncMode = ""
			fields = append(fields, "last_saved_cursor_at", "last_full_sync_at", "last_incremental_sync_at", "last_sync_time", "last_success_sync_mode")
		}
		saveErr = db.Db.Model(config).Select(fields).Updates(config).Error
	}
	if err := saveErr; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存 Emby 配置失败：" + err.Error()})
		return
	}
	if _, err := models.GetEmbyConfigFromDB(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "刷新 Emby 配置缓存失败：" + err.Error()})
		return
	}

	if oldSyncEnabled != config.SyncEnabled || oldSyncCron != config.SyncCron {
		// 同步状态改变，需要重新加载 Cron
		synccron.InitCron()
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "Emby 配置已更新"})
}
