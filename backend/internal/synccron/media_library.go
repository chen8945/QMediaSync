package synccron

import (
	"errors"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func selectScheduledEmbySyncMode(config *models.EmbyConfig, now time.Time) string {
	if config == nil || config.EnableDailyFirstFullSync != 1 {
		return models.EmbySyncModeIncremental
	}
	if config.LastFullSyncAt <= 0 {
		return models.EmbySyncModeFull
	}

	loc := now.Location()
	lastFullSyncAt := time.Unix(config.LastFullSyncAt, 0).In(loc)
	now = now.In(loc)
	nowYear, nowMonth, nowDay := now.Date()
	lastYear, lastMonth, lastDay := lastFullSyncAt.Date()
	if nowYear == lastYear && nowMonth == lastMonth && nowDay == lastDay {
		return models.EmbySyncModeIncremental
	}
	return models.EmbySyncModeFull
}

var performScheduledMediaLibrarySync = func(kind models.MediaServerType, mode string) (int, error) {
	if mode == models.EmbySyncModeFull {
		return emby.PerformMediaLibrarySync(kind)
	}
	return emby.PerformMediaLibraryIncrementalSync(kind)
}

var mediaLibraryCronEntries = make(map[models.MediaServerType]cron.EntryID)

// UpdateMediaLibraryCron 只替换所属端的 Cron，重复保存也能重试应用。
func UpdateMediaLibraryCron(kind models.MediaServerType) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	globalCronMu.Lock()
	defer globalCronMu.Unlock()
	if db.IsMaintenance(db.Db) {
		return errors.New("数据库维护期间无法应用定时同步")
	}
	return updateMediaLibraryCronLocked(kind)
}

func updateMediaLibraryCronLocked(kind models.MediaServerType) error {
	config, err := models.ReadMediaLibraryConfigSnapshot(kind)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if GlobalCron != nil {
			GlobalCron.Remove(mediaLibraryCronEntries[kind])
		}
		delete(mediaLibraryCronEntries, kind)
		return nil
	}
	if err != nil {
		return err
	}
	enabled := config.Enabled && config.SyncEnabled == 1 && config.EmbyUrl != "" && config.EmbyApiKey != ""
	if enabled {
		if _, err := cron.ParseStandard(config.SyncCron); err != nil {
			return err
		}
	}
	if GlobalCron == nil {
		return nil
	}
	if entry, ok := mediaLibraryCronEntries[kind]; ok {
		GlobalCron.Remove(entry)
		delete(mediaLibraryCronEntries, kind)
	}
	if !enabled {
		return nil
	}
	entry, err := GlobalCron.AddFunc(config.SyncCron, func() {
		config, err := models.ReadMediaLibraryConfigSnapshot(kind)
		if err != nil || db.IsMaintenance(db.Db) || !config.Enabled || config.SyncEnabled != 1 || config.EmbyUrl == "" || config.EmbyApiKey == "" {
			return
		}
		_, err = performScheduledMediaLibrarySync(kind, selectScheduledEmbySyncMode(config, time.Now()))
		if err != nil {
			helpers.AppLogger.Warnf("%s 定时同步失败：%v", kind.DisplayName(), err)
		}
	})
	if err == nil {
		mediaLibraryCronEntries[kind] = entry
	}
	return err
}
