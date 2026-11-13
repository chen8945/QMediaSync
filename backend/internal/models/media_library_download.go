package models

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
)

var errMediaLibraryDownloadDisabled = errors.New("媒体信息提取已关闭")

func (task *DbDownloadTask) isMediaLibraryDownload() bool {
	return task.Source == DownloadSourceEmbyMedia || task.Source == DownloadSourceJellyfinMedia ||
		task.SourceType == SourceTypeEmbyMedia || task.SourceType == SourceTypeJellyfinMedia
}

func mediaLibraryDownloadKind(task *DbDownloadTask) (MediaServerType, error) {
	switch {
	case task.Source == DownloadSourceEmbyMedia && task.SourceType == SourceTypeEmbyMedia:
		return MediaServerEmby, nil
	case task.Source == DownloadSourceJellyfinMedia && task.SourceType == SourceTypeJellyfinMedia:
		return MediaServerJellyfin, nil
	default:
		return "", errors.New("媒体提取任务来源不一致")
	}
}

func runnableDownloadTaskQuery(query *gorm.DB) *gorm.DB {
	var enabled []DownloadSource
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		config, err := ReadMediaLibraryConfigSnapshot(kind)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			query.AddError(err)
			return query
		}
		if config.Enabled && config.EmbyUrl != "" && config.EmbyApiKey != "" {
			enabled = append(enabled, DownloadSource(kind+"_media"))
		}
	}
	return query.Where("COALESCE(source, '') NOT IN ? OR source IN ?", []DownloadSource{DownloadSourceEmbyMedia, DownloadSourceJellyfinMedia}, enabled)
}

// AddDownloadTaskFromMediaLibrary 保存可证明的原服务器身份，执行时再核验本端当前连接。
func AddDownloadTaskFromMediaLibrary(config *EmbyConfig, serverID, itemID, itemName string) error {
	if config == nil {
		return errors.New("媒体库配置不存在")
	}
	if err := config.MediaServerType.Validate(); err != nil {
		return err
	}
	if !config.Enabled {
		return errMediaLibraryDownloadDisabled
	}
	if strings.TrimSpace(serverID) == "" || strings.TrimSpace(itemID) == "" {
		return errors.New("媒体提取任务缺少服务器或条目身份")
	}
	var current struct {
		Enabled bool
		URL     string
		APIKey  string
	}
	kind := string(config.MediaServerType)
	if err := db.Db.Table(mediaLibraryConfigTable(config.MediaServerType)).
		Select("enabled, " + kind + "_url AS url, " + kind + "_api_key AS api_key").
		Order("id ASC").Take(&current).Error; err != nil {
		return err
	}
	if !current.Enabled {
		return errMediaLibraryDownloadDisabled
	}
	if current.URL != config.EmbyUrl || current.APIKey != config.EmbyApiKey {
		return errors.New("媒体库连接在提取扫描期间发生变化")
	}
	task := &DbDownloadTask{
		Source: DownloadSource(config.MediaServerType + "_media"), SourceType: SourceType(config.MediaServerType + "_media"),
		MediaServerID: serverID, EmbyItemId: itemID, FileName: itemName, Status: DownloadStatusPending,
	}
	if existing := findActiveDownloadTaskByDeduplicationKeys(task); existing != nil {
		return activeDownloadTaskExistsError(existing)
	}
	if err := createDownloadTaskWithDB(db.Db, task); err != nil {
		return err
	}
	publishDownloadQueueChanged(task, "created")
	return nil
}

func authorizeMediaLibraryDownload(ctx context.Context, task *DbDownloadTask) (*EmbyConfig, *embyclientrestgo.Client, string, error) {
	kind, err := mediaLibraryDownloadKind(task)
	if err != nil {
		return nil, nil, "", err
	}
	config, err := ReadMediaLibraryConfigSnapshot(kind)
	if err != nil {
		return nil, nil, "", err
	}
	if !config.Enabled {
		return nil, nil, "", errMediaLibraryDownloadDisabled
	}
	if config.EmbyUrl == "" || config.EmbyApiKey == "" || task.EmbyItemId == "" {
		return nil, nil, "", errors.New("媒体提取任务或连接不完整")
	}
	client := config.Client()
	if task.MediaServerID != "" {
		serverID, err := client.GetServerID(ctx)
		if err != nil {
			return nil, nil, "", err
		}
		if serverID != task.MediaServerID {
			return nil, nil, "", errors.New("媒体服务器已变更，不能执行旧提取任务")
		}
	} else if err := validateLegacyMediaDownload(task, config); err != nil {
		return nil, nil, "", err
	}
	users, err := client.GetUsersWithAllLibrariesAccessContext(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	for _, user := range users {
		if !user.Policy.EnableMediaPlayback {
			continue
		}
		return config, client, user.ID, nil
	}
	return nil, nil, "", errors.New("没有可访问全部媒体库且允许播放的提取用户")
}

func validateLegacyMediaDownload(task *DbDownloadTask, config *EmbyConfig) error {
	if config.MediaServerType != MediaServerEmby {
		return errors.New("旧提取任务没有可证明的原服务器身份")
	}
	address, err := url.Parse(task.RemoteDownloadUrl)
	if err != nil {
		return errors.New("旧媒体提取地址无效")
	}
	base, err := url.Parse(strings.TrimRight(config.EmbyUrl, "/"))
	if err != nil || address.User != nil || address.Fragment != "" || address.Scheme != base.Scheme || address.Host != base.Host ||
		address.Path != base.Path+"/emby/Items/"+task.EmbyItemId+"/PlaybackInfo" || address.Query().Get("api_key") != config.EmbyApiKey {
		return errors.New("旧提取任务无法核对原连接，不能替换地址或凭据")
	}
	return nil
}

// DownloadMediaLibraryInfo 只有响应包含适用的主要媒体流时才完成任务。
func (task *DbDownloadTask) DownloadMediaLibraryInfo() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	config, client, userID, err := authorizeMediaLibraryDownload(ctx, task)
	if errors.Is(err, errMediaLibraryDownloadDisabled) {
		task.keepMediaLibraryDownloadPending()
		return
	}
	if err == nil {
		var item *embyclientrestgo.BaseItemDtoV2
		item, err = client.GetItemDetailByUserContext(ctx, task.EmbyItemId, userID)
		if err == nil {
			var current struct {
				Enabled bool
				URL     string
				APIKey  string
			}
			kind := string(config.MediaServerType)
			readErr := db.Db.Table(mediaLibraryConfigTable(config.MediaServerType)).
				Select("enabled, " + kind + "_url AS url, " + kind + "_api_key AS api_key").Order("id ASC").Take(&current).Error
			if readErr != nil {
				err = readErr
			} else if !current.Enabled {
				task.keepMediaLibraryDownloadPending()
				return
			} else if current.URL != config.EmbyUrl || current.APIKey != config.EmbyApiKey {
				err = errors.New("媒体库连接在提取期间发生变化")
			} else {
				task.Downloading()
				err = client.CheckPlaybackInfoContext(ctx, *item, userID)
			}
		}
	}
	if err != nil {
		task.Fail(err)
		return
	}
	task.Complete()
	product := "Emby"
	if config.MediaServerType == MediaServerJellyfin {
		product = "Jellyfin"
	}
	helpers.AppLogger.Infof("%s 媒体信息提取完成：%s", product, task.FileName)
}

func (task *DbDownloadTask) keepMediaLibraryDownloadPending() {
	// 关闭期间保留待办，重新启用后领取，不消耗失败重试。
	task.Status = DownloadStatusPending
	task.StartTime = 0
	if err := db.Db.Model(task).Updates(map[string]any{"status": DownloadStatusPending, "start_time": 0}).Error; err != nil {
		helpers.AppLogger.Errorf("保留媒体提取待办失败：%v", err)
		return
	}
	publishDownloadQueueChanged(task, "status_changed")
}
