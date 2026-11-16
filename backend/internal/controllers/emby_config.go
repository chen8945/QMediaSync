package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
	"qmediasync/internal/validation"
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
func GetEmbyConfig(c *gin.Context) { getMediaLibraryConfig(c, models.MediaServerEmby) }

// GetJellyfinConfig 获取独立 Jellyfin 配置。
func GetJellyfinConfig(c *gin.Context) { getMediaLibraryConfig(c, models.MediaServerJellyfin) }

func getMediaLibraryConfig(c *gin.Context, kind models.MediaServerType) {
	c.Header("Cache-Control", "no-store, private")
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Success(c, http.StatusOK, gin.H{"exists": false}, "获取媒体库配置成功")
		return
	}
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取媒体库配置失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{"exists": true, "config": mediaLibraryConfigResponse(kind, config)}, "获取媒体库配置成功")
}

// UpdateEmbyConfig 更新 Emby 配置。
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
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	req := payload.UpdateEmbyConfigRequest
	if kind == models.MediaServerJellyfin {
		if err := validation.HTTPURL("jellyfin_url", payload.JellyfinURL, true); err != nil {
			httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
			return
		}
		req.EmbyURL, req.EmbyAPIKey = payload.JellyfinURL, payload.JellyfinAPIKey
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	var pathMappings []models.EmbyPathMapping
	if req.PathMappings != nil {
		var err error
		pathMappings, err = req.NormalizedPathMappings()
		if err != nil {
			httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(validation.New("path_mappings", err.Error())))
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
		if _, ok := errors.AsType[validation.Error](err); ok {
			httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		} else {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存媒体库配置失败", Cause: err, Secrets: []string{req.EmbyAPIKey}})
		}
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{"restart_required": false}, "配置已保存", warnings...)
}

func (controller *MediaLibraryConfigController) applyCommitted(ctx context.Context, config *models.EmbyConfig) []string {
	warnings := []string{}
	if config.MediaServerType == models.MediaServerEmby && controller.apply != nil {
		if err := controller.apply(ctx, config); err != nil {
			if helpers.AppLogger != nil {
				helpers.AppLogger.Warnf("应用 Emby 302 配置失败：%s", helpers.RedactSensitiveLog(err.Error(), config.EmbyApiKey))
			}
			warnings = append(warnings, "配置已保存，Emby 302 尚未应用，请重新保存重试")
		}
	}
	if err := synccron.UpdateMediaLibraryCron(config.MediaServerType); err != nil {
		if helpers.AppLogger != nil {
			helpers.AppLogger.Warnf("应用媒体库定时同步失败：%s", helpers.RedactSensitiveLog(err.Error(), config.EmbyApiKey))
		}
		warnings = append(warnings, "配置已保存，定时同步尚未生效，请重新保存重试")
	}
	if err := emby.WakeMediaLibraryWebhookWorker(config.MediaServerType); err != nil {
		if helpers.AppLogger != nil {
			helpers.AppLogger.Warnf("唤醒媒体库通知后台处理失败：%s", helpers.RedactSensitiveLog(err.Error(), config.EmbyApiKey))
		}
		warnings = append(warnings, "配置已保存，通知后台处理尚未唤醒，请重新保存重试")
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
