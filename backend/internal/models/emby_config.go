package models

import (
	"encoding/json"
	"fmt"

	"gorm.io/gorm"
)

const (
	EmbySyncModeIdle           = "idle"
	EmbySyncModeFull           = "full"
	EmbySyncModeIncremental    = "incremental"
	EmbySyncModeWebhook        = "webhook"
	EmbySyncModeRefreshLibrary = "refresh_library"
)

// EmbyConfig 独立的 Emby 配置表
type EmbyConfig struct {
	BaseModel
	Enabled                  bool              `json:"enabled" gorm:"default:false"`
	MediaServerType          MediaServerType   `json:"-" gorm:"-"`
	EmbyUrl                  string            `json:"emby_url" gorm:"type:text"`
	EmbyApiKey               string            `json:"emby_api_key" gorm:"type:text"`
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

func (*EmbyConfig) TableName() string {
	return "emby_config"
}

// AfterFind 解码路径映射；损坏配置不能被当作空配置覆盖。
func (c *EmbyConfig) AfterFind(_ *gorm.DB) error {
	c.PathMappings = []EmbyPathMapping{}
	if c.PathMappingsJSON == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(c.PathMappingsJSON), &c.PathMappings); err != nil {
		return fmt.Errorf("读取媒体库路径映射失败：%w", err)
	}
	if c.PathMappings == nil {
		c.PathMappings = []EmbyPathMapping{}
	}
	return nil
}

// GetEmbyConfig 获取独立的 Emby 配置快照。
func GetEmbyConfig() (*EmbyConfig, error) { return ReadMediaLibraryConfigSnapshot(MediaServerEmby) }

func GetEmbyConfigFromDB() (*EmbyConfig, error) {
	return ReadMediaLibraryConfigSnapshot(MediaServerEmby)
}

func (c *EmbyConfig) Update(updates map[string]any) error {
	return UpdateMediaLibraryConfig(c.MediaServerType, updates)
}

// StartEmbySyncRun 标记 Emby 条目同步任务开始。
func StartEmbySyncRun(mode string, startedAt int64) (bool, error) {
	return StartMediaLibrarySyncRun(MediaServerEmby, mode, startedAt)
}

// FinishEmbySyncRun 标记 Emby 条目同步任务结束。
func FinishEmbySyncRun(mode string, processedCount, finishedAt int64, runErr error) error {
	return FinishMediaLibrarySyncRun(MediaServerEmby, mode, processedCount, finishedAt, runErr)
}

// FinishEmbyIncrementalSyncRun 标记 Emby 增量同步结束并推进游标。
func FinishEmbyIncrementalSyncRun(processedCount, finishedAt, cursorAt int64, runErr error) error {
	return FinishMediaLibraryIncrementalSyncRun(MediaServerEmby, processedCount, finishedAt, cursorAt, runErr)
}

// ResetStaleEmbySyncRunOnStartup 清理媒体库上次未结束的运行标记。
func ResetStaleEmbySyncRunOnStartup() error {
	return ResetStaleMediaLibrarySyncRunOnStartup()
}

// IsEmbySyncRunningInDB 查询 Emby 条目同步运行状态。
func IsEmbySyncRunningInDB() bool {
	return IsMediaLibrarySyncRunningInDB(MediaServerEmby)
}

func normalizeEmbySyncMode(config *EmbyConfig) {
	if config.SyncMode == "" {
		config.SyncMode = EmbySyncModeIdle
	}
}
