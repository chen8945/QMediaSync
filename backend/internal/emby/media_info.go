package emby

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
	"qmediasync/internal/models"
)

// ErrMediaLibraryUnavailable 表示本端未配置、未启用或已有提取任务。
var ErrMediaLibraryUnavailable = errors.New("media library unavailable")

var embyMediaInfoRunning atomic.Int32
var jellyfinMediaInfoRunning atomic.Int32

// StartParseEmbyMediaInfo 保留原 Emby 提取入口。
func StartParseEmbyMediaInfo() bool {
	return StartParseMediaLibraryInfo(models.MediaServerEmby) == nil
}

// StartParseMediaLibraryInfo 分别收集两端需要提取的条目，同端同时只允许一轮扫描。
func StartParseMediaLibraryInfo(kind models.MediaServerType) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w：%w", ErrMediaLibraryUnavailable, err)
	}
	if err != nil {
		return fmt.Errorf("读取 %s 媒体库配置失败：%w", kind, err)
	}
	if !config.Enabled || config.EmbyUrl == "" || config.EmbyApiKey == "" {
		return ErrMediaLibraryUnavailable
	}
	running := &embyMediaInfoRunning
	if kind == models.MediaServerJellyfin {
		running = &jellyfinMediaInfoRunning
	}
	if !running.CompareAndSwap(0, 1) {
		return ErrMediaLibraryUnavailable
	}
	product := "Emby"
	if kind == models.MediaServerJellyfin {
		product = "Jellyfin"
	}
	go func() {
		defer running.Store(0)
		count, err := collectMediaLibraryInfo(context.Background(), config)
		if err != nil {
			helpers.AppLogger.Errorf("%s 媒体信息提取扫描失败：%v", product, err)
			return
		}
		helpers.AppLogger.Infof("%s 媒体信息提取已加入下载队列，共 %d 项", product, count)
	}()
	return nil
}

func collectMediaLibraryInfo(ctx context.Context, config *models.EmbyConfig) (int, error) {
	client := config.Client()
	serverID, err := client.GetServerID(ctx)
	if err != nil {
		return 0, err
	}
	users, err := client.GetUsersWithAllLibrariesAccessContext(ctx)
	if err != nil {
		return 0, err
	}
	userID := ""
	for _, user := range users {
		if user.Policy.EnableMediaPlayback {
			userID = user.ID
			break
		}
	}
	if userID == "" {
		return 0, errors.New("没有可访问全部媒体库且允许播放的提取用户")
	}
	libraries, err := client.GetAllMediaLibrariesContext(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, library := range libraries {
		err := client.FetchMediaItemsByLibraryID(ctx, embyclientrestgo.EmbyItemsQuery{
			LibraryID: library.ID, UserID: userID, Fields: "MediaStreams",
		}, func(item embyclientrestgo.BaseItemDtoV2) error {
			if !needsMediaLibraryInfo(item) {
				return nil
			}
			err := models.AddDownloadTaskFromMediaLibrary(config, serverID, item.Id, item.Name)
			if errors.Is(err, models.ErrActiveDownloadTaskExists) {
				return nil
			}
			if err == nil {
				count++
			}
			return err
		})
		if err != nil {
			return count, err
		}
	}
	return count, nil
}

func needsMediaLibraryInfo(item embyclientrestgo.BaseItemDtoV2) bool {
	streamType := "Video"
	if strings.EqualFold(item.Type, "Audio") || strings.EqualFold(item.Type, "AudioBook") {
		streamType = "Audio"
	}
	for _, stream := range item.MediaStreams {
		if strings.EqualFold(stream.Type, streamType) && strings.TrimSpace(stream.Codec) != "" {
			return false
		}
	}
	return true
}
