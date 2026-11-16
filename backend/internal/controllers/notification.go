package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	notificationconfig "qmediasync/internal/notification/config"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/requests"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type notificationChannelResponse struct {
	ID          uint   `json:"id"`
	ChannelType string `json:"channel_type"`
	ChannelName string `json:"channel_name"`
	Description string `json:"description"`
	IsEnabled   bool   `json:"is_enabled"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func toNotificationChannelResponse(channel models.NotificationChannel) notificationChannelResponse {
	return notificationChannelResponse{
		ID:          channel.ID,
		ChannelType: channel.ChannelType,
		ChannelName: channel.ChannelName,
		Description: channel.Description,
		IsEnabled:   channel.IsEnabled,
		CreatedAt:   channel.CreatedAt.Unix(),
		UpdatedAt:   channel.UpdatedAt.Unix(),
	}
}

func toNotificationChannelResponses(channels []models.NotificationChannel) []notificationChannelResponse {
	responses := make([]notificationChannelResponse, 0, len(channels))
	for _, channel := range channels {
		responses = append(responses, toNotificationChannelResponse(channel))
	}
	return responses
}

// GetNotificationChannels 获取所有通知渠道
func GetNotificationChannels(c *gin.Context) {
	var channels []models.NotificationChannel
	if err := db.Db.WithContext(c.Request.Context()).Find(&channels).Error; err != nil {
		writeNotificationError(c, err, "获取通知渠道失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		toNotificationChannelResponses(channels),
		"获取成功",
	)
}

func parseNotificationChannelID(c *gin.Context) (uint, bool) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		writeNotificationError(c, err, "参数错误")
		return 0, false
	}
	return idReq.ID, true
}

// CreateTelegramChannel 创建 Telegram 渠道
func CreateTelegramChannel(c *gin.Context) {
	var r requests.CreateTelegramChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.CreateTelegram(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "创建渠道失败")
		return
	}
	c.Header("Location", fmt.Sprintf("/api/setting/notification/channels/telegram/%d", result.Channel.ID))
	httpapi.Success(
		c,
		http.StatusCreated,
		toNotificationChannelResponse(result.Channel),
		"创建成功",
		result.Warnings...,
	)
}

// CreateMeoWChannel 创建 MeoW 渠道
func CreateMeoWChannel(c *gin.Context) {
	var r requests.CreateMeoWChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.CreateMeoW(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "创建渠道失败")
		return
	}
	c.Header("Location", fmt.Sprintf("/api/setting/notification/channels/meow/%d", result.Channel.ID))
	httpapi.Success(
		c,
		http.StatusCreated,
		toNotificationChannelResponse(result.Channel),
		"创建成功",
		result.Warnings...,
	)
}

// CreateBarkChannel 创建 Bark 渠道
func CreateBarkChannel(c *gin.Context) {
	var r requests.CreateBarkChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.CreateBark(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "创建渠道失败")
		return
	}
	c.Header("Location", fmt.Sprintf("/api/setting/notification/channels/bark/%d", result.Channel.ID))
	httpapi.Success(
		c,
		http.StatusCreated,
		toNotificationChannelResponse(result.Channel),
		"创建成功",
		result.Warnings...,
	)
}

// CreateServerChanChannel 创建 Server酱渠道
func CreateServerChanChannel(c *gin.Context) {
	var r requests.CreateServerChanChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.CreateServerChan(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "创建渠道失败")
		return
	}
	c.Header("Location", fmt.Sprintf("/api/setting/notification/channels/serverchan/%d", result.Channel.ID))
	httpapi.Success(
		c,
		http.StatusCreated,
		toNotificationChannelResponse(result.Channel),
		"创建成功",
		result.Warnings...,
	)
}

// CreateCustomWebhookChannel 创建自定义 Webhook 渠道
func CreateCustomWebhookChannel(c *gin.Context) {
	var r requests.CustomWebhookChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	result, err := notificationconfig.CreateWebhook(c.Request.Context(), r.Input())
	if err != nil {
		writeNotificationError(c, err, "创建渠道失败")
		return
	}
	c.Header("Location", fmt.Sprintf("/api/setting/notification/channels/webhook/%d", result.Channel.ID))
	httpapi.Success(
		c,
		http.StatusCreated,
		toNotificationChannelResponse(result.Channel),
		"创建成功",
		result.Warnings...,
	)
}

// UpdateCustomWebhookChannel 更新自定义 Webhook 渠道配置
func UpdateCustomWebhookChannel(c *gin.Context) {
	var r requests.CustomWebhookChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	result, err := notificationconfig.UpdateWebhook(c.Request.Context(), r.Input())
	if err != nil {
		writeNotificationError(c, err, "更新渠道失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		toNotificationChannelResponse(result.Channel),
		"更新成功",
		result.Warnings...,
	)
}

// UpdateTelegramChannel 更新 Telegram 渠道配置
func UpdateTelegramChannel(c *gin.Context) {
	var r requests.UpdateTelegramChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.UpdateTelegram(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "更新渠道失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		toNotificationChannelResponse(result.Channel),
		"更新成功",
		result.Warnings...,
	)
}

// UpdateMeoWChannel 更新 MeoW 渠道配置
func UpdateMeoWChannel(c *gin.Context) {
	var r requests.UpdateMeoWChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.UpdateMeoW(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "更新渠道失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		toNotificationChannelResponse(result.Channel),
		"更新成功",
		result.Warnings...,
	)
}

// UpdateBarkChannel 更新 Bark 渠道配置
func UpdateBarkChannel(c *gin.Context) {
	var r requests.UpdateBarkChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.UpdateBark(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "更新渠道失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		toNotificationChannelResponse(result.Channel),
		"更新成功",
		result.Warnings...,
	)
}

// UpdateServerChanChannel 更新 Server酱渠道配置
func UpdateServerChanChannel(c *gin.Context) {
	var r requests.UpdateServerChanChannelRequest
	if !bindNotificationJSON(c, &r) {
		return
	}
	edit, cfg := r.Input()
	result, err := notificationconfig.UpdateServerChan(c.Request.Context(), edit, cfg)
	if err != nil {
		writeNotificationError(c, err, "更新渠道失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		toNotificationChannelResponse(result.Channel),
		"更新成功",
		result.Warnings...,
	)
}

// UpdateChannelStatus 启用/禁用渠道
func UpdateChannelStatus(c *gin.Context) {
	var r struct {
		ChannelID uint `json:"channel_id"`
		IsEnabled bool `json:"is_enabled"`
	}
	if !bindNotificationJSON(c, &r) {
		return
	}
	result, err := notificationconfig.SetEnabled(c.Request.Context(), r.ChannelID, r.IsEnabled)
	if err != nil {
		writeNotificationError(c, err, "更新失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		nil,
		"更新成功",
		result.Warnings...,
	)
}

// GetTelegramChannel 查询单个 Telegram 渠道配置
func GetTelegramChannel(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	channelID, ok := parseNotificationChannelID(c)
	if !ok {
		return
	}
	var channel models.NotificationChannel
	if err := db.Db.WithContext(c.Request.Context()).First(&channel, channelID).Error; err != nil {
		writeNotificationLookupError(c, err, false)
		return
	}
	if channel.ChannelType != "telegram" {
		writeNotificationError(c, notificationconfig.ErrChannelTypeMismatch, "渠道类型不匹配")
		return
	}
	var cfg models.TelegramChannelConfig
	if err := db.Db.WithContext(c.Request.Context()).Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		writeNotificationLookupError(c, err, true)
		return
	}
	// ProxyURL 由迁移从 settings.HttpProxy 复制而来，可能带凭据，与 GetHttpProxy 用同一个脱敏函数
	cfg.ProxyURL = validation.RedactProxyURL(cfg.ProxyURL)
	httpapi.Success(c, http.StatusOK, gin.H{
		"channel": toNotificationChannelResponse(channel),
		"config":  cfg,
	}, "获取成功")
}

// GetMeoWChannel 查询单个 MeoW 渠道配置
func GetMeoWChannel(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	channelID, ok := parseNotificationChannelID(c)
	if !ok {
		return
	}
	var channel models.NotificationChannel
	if err := db.Db.WithContext(c.Request.Context()).First(&channel, channelID).Error; err != nil {
		writeNotificationLookupError(c, err, false)
		return
	}
	if channel.ChannelType != "meow" {
		writeNotificationError(c, notificationconfig.ErrChannelTypeMismatch, "渠道类型不匹配")
		return
	}
	var cfg models.MeoWChannelConfig
	if err := db.Db.WithContext(c.Request.Context()).Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		writeNotificationLookupError(c, err, true)
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{
		"channel": toNotificationChannelResponse(channel),
		"config":  cfg,
	}, "获取成功")
}

// GetBarkChannel 查询单个 Bark 渠道配置
func GetBarkChannel(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	channelID, ok := parseNotificationChannelID(c)
	if !ok {
		return
	}
	var channel models.NotificationChannel
	if err := db.Db.WithContext(c.Request.Context()).First(&channel, channelID).Error; err != nil {
		writeNotificationLookupError(c, err, false)
		return
	}
	if channel.ChannelType != "bark" {
		writeNotificationError(c, notificationconfig.ErrChannelTypeMismatch, "渠道类型不匹配")
		return
	}
	var cfg models.BarkChannelConfig
	if err := db.Db.WithContext(c.Request.Context()).Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		writeNotificationLookupError(c, err, true)
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{
		"channel": toNotificationChannelResponse(channel),
		"config":  cfg,
	}, "获取成功")
}

// GetServerChanChannel 查询单个 Server酱渠道配置
func GetServerChanChannel(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	channelID, ok := parseNotificationChannelID(c)
	if !ok {
		return
	}
	var channel models.NotificationChannel
	if err := db.Db.WithContext(c.Request.Context()).First(&channel, channelID).Error; err != nil {
		writeNotificationLookupError(c, err, false)
		return
	}
	if channel.ChannelType != "serverchan" {
		writeNotificationError(c, notificationconfig.ErrChannelTypeMismatch, "渠道类型不匹配")
		return
	}
	var cfg models.ServerChanChannelConfig
	if err := db.Db.WithContext(c.Request.Context()).Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		writeNotificationLookupError(c, err, true)
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{
		"channel": toNotificationChannelResponse(channel),
		"config":  cfg,
	}, "获取成功")
}

// GetCustomWebhookChannel 查询单个 Webhook 渠道配置
func GetCustomWebhookChannel(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	channelID, ok := parseNotificationChannelID(c)
	if !ok {
		return
	}
	var channel models.NotificationChannel
	if err := db.Db.WithContext(c.Request.Context()).First(&channel, channelID).Error; err != nil {
		writeNotificationLookupError(c, err, false)
		return
	}
	if channel.ChannelType != "webhook" {
		writeNotificationError(c, notificationconfig.ErrChannelTypeMismatch, "渠道类型不匹配")
		return
	}
	var cfg models.CustomWebhookChannelConfig
	if err := db.Db.WithContext(c.Request.Context()).Where("channel_id = ?", channel.ID).First(&cfg).Error; err != nil {
		writeNotificationLookupError(c, err, true)
		return
	}
	// 解析 headers JSON 字符串为对象
	var headers map[string]string
	if cfg.Headers != "" {
		if err := json.Unmarshal([]byte(cfg.Headers), &headers); err != nil {
			headers = nil
		}
	}
	httpapi.Success(c, http.StatusOK, gin.H{
		"channel": toNotificationChannelResponse(channel),
		"config": gin.H{
			"id":              cfg.ID,
			"channel_id":      cfg.ChannelID,
			"endpoint":        cfg.Endpoint,
			"method":          cfg.Method,
			"template":        cfg.Template,
			"format":          cfg.Format,
			"query_param":     cfg.QueryParam,
			"auth_type":       cfg.AuthType,
			"auth_token":      cfg.AuthToken,
			"auth_user":       cfg.AuthUser,
			"auth_pass":       cfg.AuthPass,
			"auth_header_key": cfg.AuthHeaderKey,
			"auth_query_key":  cfg.AuthQueryKey,
			"headers":         headers,
			"created_at":      cfg.CreatedAt,
			"updated_at":      cfg.UpdatedAt,
		},
	}, "获取成功")
}

// DeleteChannel 删除渠道
func DeleteChannel(c *gin.Context) {
	channelID, ok := parseNotificationChannelID(c)
	if !ok {
		return
	}
	result, err := notificationconfig.Delete(c.Request.Context(), channelID)
	if err != nil {
		writeNotificationError(c, err, "删除失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		nil,
		"删除成功",
		result.Warnings...,
	)
}

// GetNotificationRules 获取通知规则
func GetNotificationRules(c *gin.Context) {
	query := db.Db.WithContext(c.Request.Context())
	if channelID := c.Query("channel_id"); channelID != "" {
		query = query.Where("channel_id = ?", channelID)
	}
	rules := make([]models.NotificationRule, 0)
	if err := query.Find(&rules).Error; err != nil {
		writeNotificationError(c, err, "获取规则失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		rules,
		"获取成功",
	)
}

// UpdateNotificationRule 更新通知规则
func UpdateNotificationRule(c *gin.Context) {
	var r struct {
		ChannelID uint   `json:"channel_id"`
		EventType string `json:"event_type"`
		IsEnabled bool   `json:"is_enabled"`
	}
	if !bindNotificationJSON(c, &r) {
		return
	}
	input := notification.NotificationRule{
		ChannelID: r.ChannelID,
		EventType: r.EventType,
		IsEnabled: r.IsEnabled,
	}
	result, err := notificationconfig.UpdateRule(c.Request.Context(), input)
	if err != nil {
		writeNotificationError(c, err, "更新规则失败")
		return
	}
	httpapi.Success(
		c,
		http.StatusOK,
		nil,
		"更新成功",
		result.Warnings...,
	)
}

// TestChannelConnection 测试渠道连接
func TestChannelConnection(c *gin.Context) {
	type req struct {
		ChannelID uint `json:"channel_id" binding:"required"`
	}

	var r req
	if err := c.ShouldBindJSON(&r); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	var channel models.NotificationChannel
	if err := db.Db.WithContext(c.Request.Context()).Where("id = ?", r.ChannelID).First(&channel).Error; err != nil {
		writeNotificationLookupError(c, err, false)
		return
	}

	// 构建测试通知
	testNotif := &notification.Notification{
		Type:      notification.SystemAlert,
		Title:     "通知渠道测试",
		Content:   "这是一条测试消息",
		Timestamp: time.Now(),
		Priority:  notification.NormalPriority,
	}

	// 创建处理器并发送测试消息
	var handler notificationmanager.ChannelHandler
	var secrets []string

	switch channel.ChannelType {
	case "telegram":
		var config models.TelegramChannelConfig
		err := db.Db.WithContext(c.Request.Context()).
			Where("channel_id = ?", r.ChannelID).
			First(&config).Error
		if err != nil {
			writeNotificationLookupError(c, err, true)
			return
		}
		handler = notificationmanager.NewTelegramChannelHandlerWithProxy(&config, models.GetHTTPProxy())
		secrets = []string{config.BotToken}

	case "meow":
		var config models.MeoWChannelConfig
		err := db.Db.WithContext(c.Request.Context()).
			Where("channel_id = ?", r.ChannelID).
			First(&config).Error
		if err != nil {
			writeNotificationLookupError(c, err, true)
			return
		}
		handler = notificationmanager.NewMeoWChannelHandler(&config)
		secrets = []string{config.Nickname}

	case "bark":
		var config models.BarkChannelConfig
		err := db.Db.WithContext(c.Request.Context()).
			Where("channel_id = ?", r.ChannelID).
			First(&config).Error
		if err != nil {
			writeNotificationLookupError(c, err, true)
			return
		}
		handler = notificationmanager.NewBarkChannelHandler(&config)
		secrets = []string{config.DeviceKey}

	case "serverchan":
		var config models.ServerChanChannelConfig
		err := db.Db.WithContext(c.Request.Context()).
			Where("channel_id = ?", r.ChannelID).
			First(&config).Error
		if err != nil {
			writeNotificationLookupError(c, err, true)
			return
		}
		handler = notificationmanager.NewServerChanChannelHandler(&config)
		secrets = []string{config.SCKEY}

	case "webhook":
		var config models.CustomWebhookChannelConfig
		err := db.Db.WithContext(c.Request.Context()).
			Where("channel_id = ?", r.ChannelID).
			First(&config).Error
		if err != nil {
			writeNotificationLookupError(c, err, true)
			return
		}
		handler = notificationmanager.NewCustomWebhookChannelHandler(&config)
		secrets = []string{config.AuthToken, config.AuthPass}

	default:
		writeNotificationError(c, notificationconfig.ErrChannelTypeMismatch, "未知的渠道类型")
		return
	}

	// 发送测试消息
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	if err := handler.Send(ctx, testNotif); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{
			Code:    "NOTIFICATION_TEST_FAILED",
			Message: "通知渠道测试失败，请检查配置或稍后重试",
			Cause:   err,
			Secrets: secrets,
		})
		return
	}

	httpapi.Success(
		c,
		http.StatusOK,
		nil,
		"测试成功",
	)
}

func bindNotificationJSON(c *gin.Context, request any) bool {
	if err := c.ShouldBindJSON(request); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return false
	}
	return true
}

func writeNotificationLookupError(c *gin.Context, err error, config bool) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if config {
			err = notificationconfig.ErrConfigNotFound
		} else {
			err = notificationconfig.ErrChannelNotFound
		}
	}
	writeNotificationError(c, err, "读取通知配置失败")
}

func writeNotificationError(c *gin.Context, err error, message string) {
	status := http.StatusInternalServerError
	failure := httpapi.Error{Code: "INTERNAL_ERROR", Message: message, Cause: err}
	if _, ok := errors.AsType[validation.Error](err); ok {
		status = http.StatusBadRequest
		failure = httpapi.InvalidRequest(err)
	} else if errors.Is(err, notificationconfig.ErrChannelNotFound) {
		status = http.StatusNotFound
		failure.Code = "NOTIFICATION_CHANNEL_NOT_FOUND"
		failure.Message = "渠道不存在"
	} else if errors.Is(err, notificationconfig.ErrConfigNotFound) {
		status = http.StatusNotFound
		failure.Code = "NOTIFICATION_CONFIG_NOT_FOUND"
		failure.Message = "配置不存在"
	} else if errors.Is(err, notificationconfig.ErrChannelTypeMismatch) {
		status = http.StatusBadRequest
		failure.Code = "NOTIFICATION_CHANNEL_TYPE_MISMATCH"
		failure.Message = "渠道类型不匹配"
	} else if errors.Is(err, gorm.ErrRecordNotFound) {
		status = http.StatusNotFound
		failure.Code = "RESOURCE_NOT_FOUND"
		failure.Message = "通知渠道或配置不存在"
	}
	httpapi.Failure(c, status, failure)
}
