package models

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"gorm.io/gorm"
)

// MediaLibrarySettingsChange 只包含网页拥有的配置和需要保留的省略字段。
type MediaLibrarySettingsChange struct {
	Values                   EmbyConfig
	Enabled                  *bool
	EnableMediaNotification  *int
	EnableDailyFirstFullSync *int
	PathMappings             *[]EmbyPathMapping
}

type mediaLibrarySettingsCommand struct {
	ctx      context.Context
	change   MediaLibrarySettingsChange
	validate func() error
	apply    func(context.Context, *EmbyConfig) []string
	result   chan mediaLibrarySettingsResult
}

type mediaLibrarySettingsResult struct {
	warnings []string
	err      error
}

type mediaLibrarySettingsWriter struct {
	commands chan mediaLibrarySettingsCommand
	done     chan struct{}
}

var mediaLibrarySettingsRuntime = struct {
	sync.Mutex
	writers  map[MediaServerType]*mediaLibrarySettingsWriter
	stopping bool
}{}

// StartMediaLibrarySettingsRuntime 在管理入口开放前启动两端独立的有序写入。
func StartMediaLibrarySettingsRuntime() {
	mediaLibrarySettingsRuntime.Lock()
	defer mediaLibrarySettingsRuntime.Unlock()
	if mediaLibrarySettingsRuntime.writers != nil {
		return
	}
	mediaLibrarySettingsRuntime.writers = make(map[MediaServerType]*mediaLibrarySettingsWriter, 2)
	mediaLibrarySettingsRuntime.stopping = false
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		writer := &mediaLibrarySettingsWriter{commands: make(chan mediaLibrarySettingsCommand, 16), done: make(chan struct{})}
		mediaLibrarySettingsRuntime.writers[kind] = writer
		go func() {
			defer close(writer.done)
			for command := range writer.commands {
				warnings, err := commitMediaLibrarySettings(kind, command)
				command.result <- mediaLibrarySettingsResult{warnings, err}
			}
		}()
	}
}

// StopMediaLibrarySettingsRuntime 停止接收，锁外等待已接受保存与本地应用结束。
func StopMediaLibrarySettingsRuntime(ctx context.Context) error {
	mediaLibrarySettingsRuntime.Lock()
	writers := mediaLibrarySettingsRuntime.writers
	if !mediaLibrarySettingsRuntime.stopping {
		mediaLibrarySettingsRuntime.stopping = true
		for _, writer := range writers {
			close(writer.commands)
		}
	}
	mediaLibrarySettingsRuntime.Unlock()
	for _, writer := range writers {
		select {
		case <-writer.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	mediaLibrarySettingsRuntime.Lock()
	mediaLibrarySettingsRuntime.writers = nil
	mediaLibrarySettingsRuntime.Unlock()
	return nil
}

// SaveMediaLibrarySettings 在本端序列中合并最新值、提交并完成应用；接受后不提前释放请求许可。
func SaveMediaLibrarySettings(ctx context.Context, kind MediaServerType, change MediaLibrarySettingsChange,
	validate func() error, apply func(context.Context, *EmbyConfig) []string,
) ([]string, error) {
	if err := kind.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	command := mediaLibrarySettingsCommand{ctx: ctx, change: change, validate: validate, apply: apply, result: make(chan mediaLibrarySettingsResult, 1)}
	mediaLibrarySettingsRuntime.Lock()
	writer := mediaLibrarySettingsRuntime.writers[kind]
	if writer == nil || mediaLibrarySettingsRuntime.stopping {
		mediaLibrarySettingsRuntime.Unlock()
		return nil, fmt.Errorf("媒体库设置保存已停止")
	}
	select {
	case writer.commands <- command:
		mediaLibrarySettingsRuntime.Unlock()
	default:
		mediaLibrarySettingsRuntime.Unlock()
		return nil, fmt.Errorf("媒体库设置保存繁忙，请重试")
	}
	result := <-command.result
	return result.warnings, result.err
}

func commitMediaLibrarySettings(kind MediaServerType, command mediaLibrarySettingsCommand) ([]string, error) {
	if err := command.ctx.Err(); err != nil {
		return nil, err
	}
	ctx := command.ctx
	if command.validate != nil {
		if err := command.validate(); err != nil {
			return nil, err
		}
	}
	config, err := readMediaLibraryConfigTx(db.Db.WithContext(ctx), kind)
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("查询媒体库配置失败：%w", err)
	}
	isNew := err == gorm.ErrRecordNotFound
	if isNew {
		config = &EmbyConfig{MediaServerType: kind}
	}
	oldIdentity := EmbyConfigIdentity(config)
	change := command.change
	values := change.Values
	values.ID, values.MediaServerType = config.ID, kind
	values.Enabled = config.Enabled
	if change.Enabled != nil {
		values.Enabled = *change.Enabled
	}
	values.EnableMediaNotification = config.EnableMediaNotification
	if change.EnableMediaNotification != nil {
		values.EnableMediaNotification = *change.EnableMediaNotification
	}
	values.EnableDailyFirstFullSync = config.EnableDailyFirstFullSync
	if change.EnableDailyFirstFullSync != nil {
		values.EnableDailyFirstFullSync = *change.EnableDailyFirstFullSync
	} else if isNew {
		values.EnableDailyFirstFullSync = 1
	}
	values.PathMappings, values.PathMappingsJSON = config.PathMappings, config.PathMappingsJSON
	if change.PathMappings != nil {
		encoded, err := json.Marshal(*change.PathMappings)
		if err != nil {
			return nil, fmt.Errorf("编码媒体库路径映射失败：%w", err)
		}
		values.PathMappings, values.PathMappingsJSON = *change.PathMappings, string(encoded)
	}
	if values.SyncCron == "" {
		values.SyncCron = "0 * * * *"
	}
	fields := map[string]any{
		"enabled": values.Enabled, "emby_url": values.EmbyUrl, "emby_api_key": values.EmbyApiKey,
		"enable_delete_netdisk": values.EnableDeleteNetdisk, "enable_refresh_library": values.EnableRefreshLibrary,
		"enable_media_notification": values.EnableMediaNotification, "enable_extract_media_info": values.EnableExtractMediaInfo,
		"enable_auth": values.EnableAuth, "sync_enabled": values.SyncEnabled, "sync_cron": values.SyncCron,
		"selected_libraries": values.SelectedLibraries, "sync_all_libraries": values.SyncAllLibraries,
		"enable_daily_first_full_sync": values.EnableDailyFirstFullSync,
		"enable_playback_overview":     values.EnablePlaybackOverview, "enable_playback_progress": values.EnablePlaybackProgress,
	}
	if change.PathMappings != nil {
		fields["path_mappings_json"] = values.PathMappingsJSON
	}
	if oldIdentity != EmbyConfigIdentity(&values) {
		for _, field := range []string{"last_saved_cursor_at", "last_full_sync_at", "last_incremental_sync_at", "last_sync_time"} {
			fields[field] = int64(0)
		}
		fields["last_success_sync_mode"] = ""
	}
	var model any = &EmbyConfig{}
	if kind == MediaServerJellyfin {
		model = &JellyfinConfig{}
		fields["jellyfin_url"], fields["jellyfin_api_key"] = fields["emby_url"], fields["emby_api_key"]
		delete(fields, "emby_url")
		delete(fields, "emby_api_key")
	}
	if isNew {
		now := helpers.NowUnix()
		fields["created_at"], fields["updated_at"] = now, now
		if values.SelectedLibraries == "" {
			fields["selected_libraries"] = "[]"
		}
		err = db.Db.WithContext(ctx).Model(model).Create(fields).Error
	} else {
		err = db.Db.WithContext(ctx).Model(model).Where("id = ?", values.ID).Updates(fields).Error
	}
	if err != nil {
		return nil, fmt.Errorf("保存配置失败：%w", err)
	}
	if command.apply == nil {
		return []string{}, nil
	}
	applyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return command.apply(applyCtx, &values), nil
}

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
