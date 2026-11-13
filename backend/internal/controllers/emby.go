package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

type EmbyEvent = requests.EmbyWebhookRequest

// Webhook 持久接收 Emby 条目事件；成功只表示保存，后台核验后执行。
// @Summary Emby Webhook
// @Description 持久接收条目同步与删除事件；成功仅表示已保存，后台核验后执行
// @Tags Emby 管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 400 {object} object
// @Failure 401 {object} object
// @Failure 413 {object} object
// @Failure 503 {object} object
// @Router /emby/webhook [post]
func Webhook(ctx *gin.Context) { mediaLibraryWebhook(ctx, models.MediaServerEmby) }

// JellyfinWebhook 接收官方插件标准 JSON，缺失删除路径不补造历史证据。
func JellyfinWebhook(ctx *gin.Context) { mediaLibraryWebhook(ctx, models.MediaServerJellyfin) }

func mediaLibraryWebhook(ctx *gin.Context, kind models.MediaServerType) {
	receivedAt := time.Now()
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if err != nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"message": "Webhook 配置读取失败"})
		return
	}
	if config.EnableAuth == 1 {
		apiKey := apiKeyFromRequest(ctx)
		if apiKey == "" {
			ctx.JSON(http.StatusUnauthorized, gin.H{"message": "缺少 API Key"})
			return
		}
		if _, err := models.ValidateAPIKey(apiKey); err != nil {
			ctx.JSON(http.StatusUnauthorized, gin.H{"message": "API Key 无效"})
			return
		}
	}
	if ctx.Request.Body == nil {
		ctx.Request.Body = http.NoBody
	}
	body, err := io.ReadAll(http.MaxBytesReader(ctx.Writer, ctx.Request.Body, requests.EmbyWebhookMaxBytes))
	if err != nil {
		status := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			status = http.StatusRequestEntityTooLarge
		}
		ctx.JSON(status, gin.H{"message": "Webhook 请求体读取失败"})
		return
	}
	var event EmbyEvent
	var envelope models.EmbyWebhookEnvelope
	var playback models.EmbyPlaybackWebhook
	managed := false
	if kind == models.MediaServerJellyfin {
		request, parseErr := requests.ParseJellyfinWebhook(body)
		err = parseErr
		event, envelope, playback, managed = request.NotificationEvent(), request.ToEnvelope(), request.Playback(), request.Managed()
	} else {
		event, err = requests.ParseEmbyWebhook(body)
		envelope, managed = event.ToEnvelope(), event.Managed()
		if err == nil && strings.HasPrefix(event.Event, "playback.") {
			err = json.Unmarshal(body, &playback)
		}
	}
	if err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
		return
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		if managed {
			ctx.JSON(http.StatusServiceUnavailable, gin.H{"message": "Webhook 配置不完整"})
		} else {
			ctx.JSON(http.StatusOK, gin.H{"message": "webhook"})
		}
		return
	}
	if !config.Enabled {
		ctx.JSON(http.StatusOK, gin.H{"message": "webhook"})
		return
	}
	if managed {
		record, err := emby.ReceiveWebhook(ctx.Request.Context(), envelope)
		if err != nil {
			helpers.AppLogger.Warnf("媒体通知保存失败，本次通知不会被处理：%v", err)
			ctx.JSON(http.StatusServiceUnavailable, gin.H{"message": "Webhook 保存失败"})
			return
		}
		event = notificationEventWithHistory(event, record)
		// 同步 Emby 条目到本地与两端联动删除均由持久 worker 核验执行。
	}
	if event.Event != "" && (config.EnableMediaNotification == 1 || event.Event == "library.new" && config.EnableExtractMediaInfo == 1) {
		queueMediaWebhook(func(parent context.Context) {
			processMediaWebhook(parent, config, event, envelope, playback, receivedAt)
		})
	}
	ctx.JSON(http.StatusOK, gin.H{"message": "webhook"})
}

func processMediaWebhook(parent context.Context, config *models.EmbyConfig, event EmbyEvent, envelope models.EmbyWebhookEnvelope, playback models.EmbyPlaybackWebhook, receivedAt time.Time) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	n := newMediaNotifier(config)
	if envelope.Blocked {
		return
	}
	serverID := ""
	if event.Server.ID != "" {
		var err error
		serverID, err = n.client.GetServerID(ctx)
		if err != nil || serverID != event.Server.ID {
			return
		}
	} else if _, err := n.client.CheckConnection(ctx); err != nil {
		return
	}
	current, err := models.ReadMediaLibraryConfigSnapshot(config.MediaServerType)
	if err != nil || !current.Enabled || models.EmbyServerConfigIdentity(current) != models.EmbyServerConfigIdentity(config) {
		return
	}
	fallback := parseEmbyNotificationTime(event.Date, receivedAt)
	switch event.Event {
	case "library.new":
		if ctx.Err() == nil && current.EnableExtractMediaInfo == 1 && serverID != "" && (event.Item.Type == "Movie" || event.Item.Type == "Episode") {
			if err := models.AddDownloadTaskFromMediaLibrary(config, serverID, event.Item.ID, event.Item.Name); err != nil && !errors.Is(err, models.ErrActiveDownloadTaskExists) {
				helpers.AppLogger.Errorf("触发媒体信息提取失败：%v", err)
			}
		}
		if config.EnableMediaNotification == 1 {
			if event.Item.Type == "Episode" {
				if event.Item.SeriesId == "" {
					if detail := n.detail(ctx, event.Item.ID); detail != nil {
						event.Item.SeriesId, event.Item.ParentIndexNumber, event.Item.IndexNumber = detail.SeriesId, detail.ParentIndexNumber, detail.IndexNumber
					}
				}
				if event.Item.SeriesId != "" {
					n.addEpisode(event.Item.SeriesId, event.Item.ParentIndexNumber, event.Item.IndexNumber, event.Item.ID, event.Item.SeriesName, fallback, false)
				}
			} else if event.Item.Type == "Movie" {
				n.sendNewMovie(ctx, event.Item.ID, fallback)
			}
		}

	case "library.deleted":
		if config.EnableMediaNotification != 1 {
			return
		}
		if event.Item.Type == "Episode" && event.Item.SeriesId != "" {
			n.addEpisode(event.Item.SeriesId, event.Item.ParentIndexNumber, event.Item.IndexNumber, event.Item.ID, event.Item.SeriesName, fallback, true)
		} else if event.Item.Type == "Movie" {
			n.sendDeletedMovie(ctx, event.Item.Name)
		}
	case "playback.start", "playback.pause", "playback.stop":
		n.handlePlayback(ctx, playback)
	}
}
