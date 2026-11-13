package models

import (
	"fmt"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

// MediaServerType 是固定媒体入口的业务归属，不由远端产品探测推断。
type MediaServerType string

const (
	MediaServerEmby     MediaServerType = "emby"
	MediaServerJellyfin MediaServerType = "jellyfin"
)

// DisplayName 返回面向用户的产品名，不改变配置和接口中的机器值。
func (kind MediaServerType) DisplayName() string {
	switch kind {
	case MediaServerEmby:
		return "Emby"
	case MediaServerJellyfin:
		return "Jellyfin"
	default:
		return "媒体服务器"
	}
}

func (kind MediaServerType) Validate() error {
	if kind != MediaServerEmby && kind != MediaServerJellyfin {
		return fmt.Errorf("无效媒体服务类型：%q", kind)
	}
	return nil
}

func mediaLibraryConfigTable(kind MediaServerType) string {
	if kind == MediaServerJellyfin {
		return "jellyfin_config"
	}
	return "emby_config"
}

func readMediaLibraryConfigTx(tx *gorm.DB, kind MediaServerType) (*EmbyConfig, error) {
	if err := kind.Validate(); err != nil {
		return nil, err
	}
	var config EmbyConfig
	if kind == MediaServerJellyfin {
		var stored JellyfinConfig
		if err := tx.First(&stored).Error; err != nil {
			return nil, err
		}
		config = EmbyConfig(stored)
	} else if err := tx.First(&config).Error; err != nil {
		return nil, err
	}
	config.MediaServerType = kind
	normalizeEmbySyncMode(&config)
	return &config, nil
}

// ReadMediaLibraryConfigSnapshot 读取本端独立配置，包括独立解码的嵌套映射。
func ReadMediaLibraryConfigSnapshot(kind MediaServerType) (*EmbyConfig, error) {
	return readMediaLibraryConfigTx(db.Db, kind)
}

func UpdateMediaLibraryConfig(kind MediaServerType, updates map[string]any) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	return db.Db.Table(mediaLibraryConfigTable(kind)).Where("id > 0").Updates(updates).Error
}

func StartMediaLibrarySyncRun(kind MediaServerType, mode string, startedAt int64) (bool, error) {
	if err := kind.Validate(); err != nil {
		return false, err
	}
	if startedAt <= 0 {
		startedAt = helpers.NowUnix()
	}
	if mode == "" {
		mode = EmbySyncModeFull
	}
	result := db.Db.Table(mediaLibraryConfigTable(kind)).Where("id > 0 AND is_running = ? AND enabled = ? AND sync_enabled = ?", false, true, 1).
		Updates(map[string]any{"is_running": true, "sync_mode": mode, "started_at": startedAt, "last_error": ""})
	return result.RowsAffected > 0, result.Error
}

func FinishMediaLibrarySyncRun(kind MediaServerType, mode string, processedCount, finishedAt int64, runErr error) error {
	if finishedAt <= 0 {
		finishedAt = helpers.NowUnix()
	}
	if mode == "" {
		mode = EmbySyncModeFull
	}
	updates := map[string]any{"is_running": false, "sync_mode": EmbySyncModeIdle, "started_at": 0, "last_processed_count": processedCount, "last_error": ""}
	if runErr != nil {
		updates["last_error"] = runErr.Error()
	} else {
		updates["last_sync_time"], updates["last_success_sync_mode"] = finishedAt, mode
		switch mode {
		case EmbySyncModeFull:
			updates["last_full_sync_at"] = finishedAt
		case EmbySyncModeIncremental:
			updates["last_incremental_sync_at"] = finishedAt
		}
	}
	return UpdateMediaLibraryConfig(kind, updates)
}

func FinishMediaLibraryIncrementalSyncRun(kind MediaServerType, processedCount, finishedAt, cursorAt int64, runErr error) error {
	if err := FinishMediaLibrarySyncRun(kind, EmbySyncModeIncremental, processedCount, finishedAt, runErr); err != nil {
		return err
	}
	if runErr != nil || cursorAt <= 0 {
		return nil
	}
	return UpdateMediaLibraryConfig(kind, map[string]any{"last_saved_cursor_at": cursorAt})
}

func IsMediaLibrarySyncRunningInDB(kind MediaServerType) bool {
	if db.Db == nil {
		return false
	}
	config, err := ReadMediaLibraryConfigSnapshot(kind)
	return err == nil && config.IsRunning
}

func ResetStaleMediaLibrarySyncRunOnStartup() error {
	if db.Db == nil {
		return nil
	}
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		if err := db.Db.Table(mediaLibraryConfigTable(kind)).Where("is_running = ?", true).
			Updates(map[string]any{"is_running": false, "sync_mode": EmbySyncModeIdle, "started_at": 0, "last_error": "程序启动时清理上次未完成的媒体库同步运行状态"}).Error; err != nil {
			return err
		}
	}
	return nil
}
