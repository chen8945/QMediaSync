package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"qmediasync/internal/db"
	"qmediasync/internal/emby"
	embypreload "qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/web"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

var embyConfigSaveMu, jellyfinConfigSaveMu sync.Mutex

// GetEmbyConfig 获取 Emby 配置。
// @Summary 获取 Emby 配置
// @Description 获取 Emby 媒体服务器的配置信息
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/emby-config [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetEmbyConfig(c *gin.Context) { getMediaLibraryConfig(c, models.MediaServerEmby) }

// GetJellyfinConfig 获取独立 Jellyfin 配置。
func GetJellyfinConfig(c *gin.Context) { getMediaLibraryConfig(c, models.MediaServerJellyfin) }

func getMediaLibraryConfig(c *gin.Context, kind models.MediaServerType) {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if err == gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    Success,
			Message: "获取媒体库配置成功",
			Data:    gin.H{"exists": false},
		})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取媒体库配置失败：" + err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "获取媒体库配置成功",
		Data:    gin.H{"exists": true, "config": mediaLibraryConfigResponse(kind, config)},
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
// @Router /setting/emby-config [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateEmbyConfig(c *gin.Context) { updateMediaLibraryConfig(c, models.MediaServerEmby) }

// UpdateJellyfinConfig 保存独立 Jellyfin 配置并报告应用结果。
func UpdateJellyfinConfig(c *gin.Context) { updateMediaLibraryConfig(c, models.MediaServerJellyfin) }

func mediaLibraryConfigResponse(kind models.MediaServerType, config *models.EmbyConfig) any {
	if kind == models.MediaServerJellyfin {
		value := models.JellyfinConfig(*config)
		return &value
	}
	return config
}

func updateMediaLibraryConfig(c *gin.Context, kind models.MediaServerType) {
	var payload struct {
		requests.UpdateEmbyConfigRequest
		JellyfinURL    string `json:"jellyfin_url"`
		JellyfinAPIKey string `json:"jellyfin_api_key"`
	}
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error()})
		return
	}
	req := payload.UpdateEmbyConfigRequest
	if kind == models.MediaServerJellyfin {
		if err := validation.HTTPURL("jellyfin_url", payload.JellyfinURL, true); err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
			return
		}
		req.EmbyURL, req.EmbyAPIKey = payload.JellyfinURL, payload.JellyfinAPIKey
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	var pathMappings []models.EmbyPathMapping
	if req.PathMappings != nil {
		var err error
		pathMappings, err = req.NormalizedPathMappings()
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: validation.New("path_mappings", err.Error()).Error()})
			return
		}
		if err := validateEmbyPathMappingSyncPaths(pathMappings); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error()})
			return
		}
	}

	// 本端提交与运行状态发布使用相同顺序；两端保存仍各自独立。
	saveMu := &embyConfigSaveMu
	if kind == models.MediaServerJellyfin {
		saveMu = &jellyfinConfigSaveMu
	}
	saveMu.Lock()
	defer saveMu.Unlock()
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if err != nil && err != gorm.ErrRecordNotFound {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询媒体库配置失败：" + err.Error()})
		return
	}
	isNew := err == gorm.ErrRecordNotFound
	if isNew {
		config = &models.EmbyConfig{MediaServerType: kind}
	}
	if req.Enabled != nil {
		config.Enabled = *req.Enabled
	}
	if req.SyncCron == "" {
		req.SyncCron = "0 * * * *"
	}
	oldIdentity := models.EmbyConfigIdentity(config)
	if req.PathMappings != nil {
		encoded, err := json.Marshal(pathMappings)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "编码媒体库路径映射失败"})
			return
		}
		config.PathMappings, config.PathMappingsJSON = pathMappings, string(encoded)
	}
	config.EmbyUrl = req.EmbyURL
	config.EmbyApiKey = req.EmbyAPIKey
	config.EnableDeleteNetdisk = req.EnableDeleteNetdisk
	config.EnableRefreshLibrary = req.EnableRefreshLibrary
	if req.EnableMediaNotification != nil {
		config.EnableMediaNotification = *req.EnableMediaNotification
	}
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

	// 设置保存只拥有配置字段，不能回写读取期间变化的同步游标和运行状态。
	fields := map[string]any{
		"enabled":                      config.Enabled,
		"emby_url":                     config.EmbyUrl,
		"emby_api_key":                 config.EmbyApiKey,
		"enable_delete_netdisk":        config.EnableDeleteNetdisk,
		"enable_refresh_library":       config.EnableRefreshLibrary,
		"enable_media_notification":    config.EnableMediaNotification,
		"enable_extract_media_info":    config.EnableExtractMediaInfo,
		"enable_auth":                  config.EnableAuth,
		"sync_enabled":                 config.SyncEnabled,
		"sync_cron":                    config.SyncCron,
		"selected_libraries":           config.SelectedLibraries,
		"sync_all_libraries":           config.SyncAllLibraries,
		"enable_daily_first_full_sync": config.EnableDailyFirstFullSync,
		"enable_playback_overview":     config.EnablePlaybackOverview,
		"enable_playback_progress":     config.EnablePlaybackProgress,
	}
	if req.PathMappings != nil {
		fields["path_mappings_json"] = config.PathMappingsJSON
	}
	if oldIdentity != models.EmbyConfigIdentity(config) {
		for _, field := range []string{"last_saved_cursor_at", "last_full_sync_at", "last_incremental_sync_at", "last_sync_time"} {
			fields[field] = int64(0)
		}
		fields["last_success_sync_mode"] = ""
	}
	var model any = &models.EmbyConfig{}
	if kind == models.MediaServerJellyfin {
		model = &models.JellyfinConfig{}
		fields["jellyfin_url"], fields["jellyfin_api_key"] = fields["emby_url"], fields["emby_api_key"]
		delete(fields, "emby_url")
		delete(fields, "emby_api_key")
	}
	var saveErr error
	if isNew {
		// 显式零值不能被结构体创建时的 GORM 默认值改成开启。
		now := helpers.NowUnix()
		fields["created_at"], fields["updated_at"] = now, now
		if config.SelectedLibraries == "" {
			fields["selected_libraries"] = "[]"
		}
		saveErr = db.Db.Model(model).Create(fields).Error
	} else {
		saveErr = db.Db.Model(model).Where("id = ?", config.ID).Updates(fields).Error
	}
	if saveErr != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存配置失败：" + saveErr.Error()})
		return
	}
	if kind == models.MediaServerEmby {
		embypreload.PublishPreloadingEnabled(config.Enabled)
	}
	warnings := []string{}
	if err := synccron.UpdateMediaLibraryCron(kind); err != nil {
		warnings = append(warnings, "配置已保存，定时同步尚未生效："+err.Error())
	}
	if err := emby.WakeMediaLibraryWebhookWorker(kind); err != nil {
		warnings = append(warnings, "配置已保存，通知后台处理尚未唤醒："+err.Error())
	}
	models.TriggerEmbyLibraryRefreshCheck()
	models.TriggerMediaLibraryDownloadCheck()
	restartRequired := false
	if kind == models.MediaServerEmby {
		restartRequired = web.EmbyRestartRequired(config.Enabled, config.EmbyUrl, config.EmbyApiKey)
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "配置已保存", Data: gin.H{"warnings": warnings, "restart_required": restartRequired}})
}

func validateEmbyPathMappingSyncPaths(mappings []models.EmbyPathMapping) error {
	ids := make([]uint, 0, len(mappings))
	for _, mapping := range mappings {
		if mapping.SyncPathID != 0 {
			ids = append(ids, mapping.SyncPathID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var syncPaths []models.SyncPath
	if err := db.Db.Where("id IN ?", ids).Find(&syncPaths).Error; err != nil {
		return fmt.Errorf("读取路径映射的同步目录失败：%w", err)
	}
	byID := make(map[uint]models.SyncPath, len(syncPaths))
	for _, syncPath := range syncPaths {
		byID[syncPath.ID] = syncPath
	}
	accountIDs := make([]uint, 0, len(syncPaths))
	for i, mapping := range mappings {
		if mapping.SyncPathID == 0 {
			continue
		}
		syncPath, exists := byID[mapping.SyncPathID]
		if !exists {
			return validation.New("path_mappings", fmt.Sprintf("第 %d 行选择的同步目录不存在", i+1))
		}
		switch syncPath.SourceType {
		case models.SourceType115, models.SourceTypeBaiduPan, models.SourceTypeOpenList:
		default:
			return validation.New("path_mappings", fmt.Sprintf("第 %d 行同步目录的来源不支持联动删除", i+1))
		}
		accountIDs = append(accountIDs, syncPath.AccountId)
	}
	var accounts []models.Account
	if err := db.Db.Select("id, source_type, user_id, base_url, username, created_at").
		Where("id IN ?", accountIDs).Find(&accounts).Error; err != nil {
		return fmt.Errorf("读取路径映射的网盘账号失败：%w", err)
	}
	accountsByID := make(map[uint]models.Account, len(accounts))
	for _, account := range accounts {
		accountsByID[account.ID] = account
	}
	for i, mapping := range mappings {
		if mapping.SyncPathID == 0 {
			continue
		}
		syncPath := byID[mapping.SyncPathID]
		account, exists := accountsByID[syncPath.AccountId]
		if !exists {
			return validation.New("path_mappings", fmt.Sprintf("第 %d 行同步目录的网盘账号不存在", i+1))
		}
		if account.SourceType != syncPath.SourceType {
			return validation.New("path_mappings", fmt.Sprintf("第 %d 行同步目录与网盘账号的来源不一致", i+1))
		}
		if models.EmbyAccountIdentity(account) == "" {
			return validation.New("path_mappings", fmt.Sprintf("第 %d 行同步目录的网盘账号身份不完整，请先更新账号授权", i+1))
		}
	}
	return nil
}
