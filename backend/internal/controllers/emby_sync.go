package controllers

import (
	"context"
	"errors"
	"net"
	"net/http"

	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartEmbySync 手动触发 Emby 条目同步。
func StartEmbySync(c *gin.Context) { startMediaLibrarySync(c, models.MediaServerEmby) }

// StartJellyfinSync 手动启动 Jellyfin 条目同步。
func StartJellyfinSync(c *gin.Context) { startMediaLibrarySync(c, models.MediaServerJellyfin) }
func startMediaLibrarySync(c *gin.Context, kind models.MediaServerType) {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "MEDIA_LIBRARY_NOT_CONFIGURED", Message: "尚未配置媒体库", Cause: err})
		return
	}
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取媒体库配置失败", Cause: err})
		return
	}
	if !config.Enabled || config.SyncEnabled != 1 || config.EmbyUrl == "" || config.EmbyApiKey == "" {
		httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "MEDIA_LIBRARY_SYNC_UNAVAILABLE", Message: "请先配置并启用媒体库同步"})
		return
	}
	// 检查是否已有任务在运行
	if emby.IsMediaLibrarySyncRunning(kind) {
		httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "MEDIA_LIBRARY_SYNC_RUNNING", Message: "已有媒体库条目同步任务正在运行，请稍后再试"})
		return
	}

	go func() {
		if _, err := emby.PerformMediaLibrarySync(kind); err != nil {
			helpers.AppLogger.Warnf("%s 条目同步失败：%v", kind.DisplayName(), err)
		}
	}()
	httpapi.Success(c, http.StatusAccepted, gin.H{"status": "submitted", "media_server_type": kind}, "媒体库条目同步任务已受理")
}

// GetEmbySyncStatus 同步状态
func GetEmbySyncStatus(c *gin.Context) { getMediaLibrarySyncStatus(c, models.MediaServerEmby) }

// GetJellyfinSyncStatus 获取 Jellyfin 的独立同步状态。
func GetJellyfinSyncStatus(c *gin.Context) { getMediaLibrarySyncStatus(c, models.MediaServerJellyfin) }
func getMediaLibrarySyncStatus(c *gin.Context, kind models.MediaServerType) {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Success(c, http.StatusOK, gin.H{"exists": false}, "尚未配置媒体库")
		return
	}
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取媒体库配置失败", Cause: err})
		return
	}
	helpers.AppLogger.Debugf("获取 %s 同步状态，最后同步时间：%s", kind.DisplayName(), helpers.FormatUnixLogTime(config.LastSyncTime))
	total, err := models.GetEmbyMediaItemsCount(kind)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取媒体库条目数量失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{
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
	}, "获取同步状态成功")
}

// GetEmbyLibraries 获取所有可用的 Emby 媒体库。
func GetEmbyLibraries(c *gin.Context) { getMediaLibraryLibraries(c, models.MediaServerEmby) }

// GetJellyfinLibraries 查询 Jellyfin 媒体库供配置选择。
func GetJellyfinLibraries(c *gin.Context) { getMediaLibraryLibraries(c, models.MediaServerJellyfin) }
func getMediaLibraryLibraries(c *gin.Context, kind models.MediaServerType) {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "MEDIA_LIBRARY_NOT_CONFIGURED", Message: "尚未配置媒体库", Cause: err})
		return
	}
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取媒体库配置失败", Cause: err})
		return
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" {
		httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "MEDIA_LIBRARY_CONNECTION_UNAVAILABLE", Message: "请先保存媒体库连接"})
		return
	}

	// 查询本端媒体库，并更新带归属的本地记录。
	client := config.Client()
	libs, err := client.GetAllMediaLibrariesContext(c.Request.Context())
	if err != nil {
		status, failure := mediaLibraryQueryError(err)
		failure.Secrets = []string{config.EmbyApiKey}
		httpapi.Failure(c, status, failure)
		return
	}
	if err := models.UpsertEmbyLibraries(kind, libs); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存媒体库列表失败", Cause: err})
		return
	}

	// 只清理本端远端已经删除的媒体库记录。
	activeLibraryIds := make([]string, 0, len(libs))
	for _, lib := range libs {
		activeLibraryIds = append(activeLibraryIds, lib.ID)
	}
	var warnings []string
	if err := models.CleanupDeletedEmbyLibraries(kind, activeLibraryIds); err != nil {
		warnings = append(warnings, "媒体库列表已更新，清理已删除媒体库记录失败")
		helpers.AppLogger.Warnf("清理已删除媒体库记录失败：%v", err)
	}

	libraries, err := models.GetAllEmbyLibraries(kind)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取媒体库列表失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, libraries, "获取媒体库列表成功", warnings...)
}

func mediaLibraryQueryError(err error) (int, httpapi.Error) {
	status, code := http.StatusBadGateway, "UPSTREAM_ERROR"
	if timeout, ok := errors.AsType[net.Error](err); errors.Is(err, context.DeadlineExceeded) || ok && timeout.Timeout() {
		status, code = http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT"
	}
	return status, httpapi.Error{Code: code, Message: "查询媒体库失败", Cause: err}
}
