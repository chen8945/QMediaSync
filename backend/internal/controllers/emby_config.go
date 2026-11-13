package controllers

import (
	"context"
	"fmt"
	"net/http"

	"qmediasync/internal/db"
	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type MediaLibraryConfigController struct {
	apply func(context.Context, *models.EmbyConfig) error
}

// NewMediaLibraryConfigController 注入已提交 Emby 配置的本地应用。
func NewMediaLibraryConfigController(apply func(context.Context, *models.EmbyConfig) error) *MediaLibraryConfigController {
	return &MediaLibraryConfigController{apply: apply}
}

func (controller *MediaLibraryConfigController) UpdateEmby(c *gin.Context) {
	controller.update(c, models.MediaServerEmby)
}

func (controller *MediaLibraryConfigController) UpdateJellyfin(c *gin.Context) {
	controller.update(c, models.MediaServerJellyfin)
}

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
func UpdateEmbyConfig(c *gin.Context) { (&MediaLibraryConfigController{}).UpdateEmby(c) }

// UpdateJellyfinConfig 保存独立 Jellyfin 配置并报告应用结果。
func UpdateJellyfinConfig(c *gin.Context) { (&MediaLibraryConfigController{}).UpdateJellyfin(c) }

func mediaLibraryConfigResponse(kind models.MediaServerType, config *models.EmbyConfig) any {
	if kind == models.MediaServerJellyfin {
		value := models.JellyfinConfig(*config)
		return &value
	}
	return config
}

func (controller *MediaLibraryConfigController) update(c *gin.Context, kind models.MediaServerType) {
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
	}

	change := models.MediaLibrarySettingsChange{
		Values: models.EmbyConfig{
			EmbyUrl: req.EmbyURL, EmbyApiKey: req.EmbyAPIKey,
			EnableDeleteNetdisk: req.EnableDeleteNetdisk, EnableRefreshLibrary: req.EnableRefreshLibrary,
			EnableExtractMediaInfo: req.EnableExtractMediaInfo, EnableAuth: req.EnableAuth,
			SyncEnabled: req.SyncEnabled, SyncCron: req.SyncCron,
			SelectedLibraries: req.SelectedLibraries, SyncAllLibraries: req.SyncAllLibraries,
			EnablePlaybackOverview: req.EnablePlaybackOverview, EnablePlaybackProgress: req.EnablePlaybackProgress,
		},
		Enabled: req.Enabled, EnableMediaNotification: req.EnableMediaNotification,
		EnableDailyFirstFullSync: req.EnableDailyFirstFullSync,
	}
	var validate func() error
	if req.PathMappings != nil {
		change.PathMappings = &pathMappings
		validate = func() error { return validateEmbyPathMappingSyncPaths(c.Request.Context(), pathMappings) }
	}
	warnings, err := models.SaveMediaLibrarySettings(c.Request.Context(), kind, change, validate, controller.applyCommitted)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "配置已保存", Data: gin.H{"warnings": warnings, "restart_required": false}})
}

func (controller *MediaLibraryConfigController) applyCommitted(ctx context.Context, config *models.EmbyConfig) []string {
	warnings := []string{}
	if config.MediaServerType == models.MediaServerEmby && controller.apply != nil {
		if err := controller.apply(ctx, config); err != nil {
			if helpers.AppLogger != nil {
				helpers.AppLogger.Warnf("应用 Emby 302 配置失败：%v", err)
			}
			warnings = append(warnings, "配置已保存，Emby 302 尚未应用，请重新保存重试")
		}
	}
	if err := synccron.UpdateMediaLibraryCron(config.MediaServerType); err != nil {
		warnings = append(warnings, "配置已保存，定时同步尚未生效："+err.Error())
	}
	if err := emby.WakeMediaLibraryWebhookWorker(config.MediaServerType); err != nil {
		warnings = append(warnings, "配置已保存，通知后台处理尚未唤醒："+err.Error())
	}
	models.TriggerEmbyLibraryRefreshCheck()
	models.TriggerMediaLibraryDownloadCheck()
	return warnings
}

func validateEmbyPathMappingSyncPaths(ctx context.Context, mappings []models.EmbyPathMapping) error {
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
	if err := db.Db.WithContext(ctx).Where("id IN ?", ids).Find(&syncPaths).Error; err != nil {
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
	if err := db.Db.WithContext(ctx).Select("id, source_type, user_id, base_url, username, created_at").
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
