package models

import "gorm.io/gorm"

type JellyfinConfig struct {
	BaseModel
	Enabled                  bool              `json:"enabled" gorm:"default:false"`
	MediaServerType          MediaServerType   `json:"-" gorm:"-"`
	EmbyUrl                  string            `json:"jellyfin_url" gorm:"column:jellyfin_url;type:text"`
	EmbyApiKey               string            `json:"jellyfin_api_key" gorm:"column:jellyfin_api_key;type:text"`
	EnableDeleteNetdisk      int               `json:"enable_delete_netdisk" gorm:"default:0"`
	EnableRefreshLibrary     int               `json:"enable_refresh_library" gorm:"default:0"`
	EnableMediaNotification  int               `json:"enable_media_notification" gorm:"default:0"`
	EnableExtractMediaInfo   int               `json:"enable_extract_media_info" gorm:"default:0"`
	EnableAuth               int               `json:"enable_auth" gorm:"default:1"`
	SyncEnabled              int               `json:"sync_enabled" gorm:"default:1"`
	SyncCron                 string            `json:"sync_cron" gorm:"type:text;default:'0 * * * *'"`
	LastSyncTime             int64             `json:"last_sync_time" gorm:"default:0"`
	LastFullSyncAt           int64             `json:"last_full_sync_at" gorm:"index;default:0"`
	LastIncrementalSyncAt    int64             `json:"last_incremental_sync_at" gorm:"index;default:0"`
	LastSavedCursorAt        int64             `json:"last_saved_cursor_at" gorm:"index;default:0"`
	LastProcessedCount       int64             `json:"last_processed_count" gorm:"default:0"`
	LastSuccessSyncMode      string            `json:"last_success_sync_mode" gorm:"size:32;default:''"` // 最近一次成功同步模式
	LastError                string            `json:"last_error" gorm:"type:text"`
	IsRunning                bool              `json:"is_running" gorm:"default:false"`
	SyncMode                 string            `json:"sync_mode" gorm:"size:32;index;default:'idle'"`
	StartedAt                int64             `json:"started_at" gorm:"index;default:0"`
	SelectedLibraries        string            `json:"selected_libraries" gorm:"type:text;default:'[]'"` // 选中的媒体库 ID 列表（JSON 格式）
	SyncAllLibraries         int               `json:"sync_all_libraries" gorm:"default:1"`              // 是否同步所有媒体库（1=全部，0=部分）
	EnableDailyFirstFullSync int               `json:"enable_daily_first_full_sync" gorm:"default:1"`    // 每日首次定时同步是否执行全量同步
	EnablePlaybackOverview   int               `json:"enable_playback_overview" gorm:"default:0"`        // 播放通知是否显示剧情简介
	EnablePlaybackProgress   int               `json:"enable_playback_progress" gorm:"default:0"`        // 播放通知是否显示播放进度
	PathMappings             []EmbyPathMapping `json:"path_mappings" gorm:"-"`
	PathMappingsJSON         string            `json:"-" gorm:"column:path_mappings_json;type:text;default:'[]'"`
	// DeleteNetdiskLibrary    string `json:"delete_netdisk_library" gorm:"type:varchar(200);default:''"` // 允许联动删除的媒体库 ID，用英文逗号分隔，空表示允许全部
}

func (*JellyfinConfig) TableName() string { return "jellyfin_config" }
func (c *JellyfinConfig) AfterFind(tx *gorm.DB) error {
	config := EmbyConfig(*c)
	if err := config.AfterFind(tx); err != nil {
		return err
	}
	*c = JellyfinConfig(config)
	return nil
}
