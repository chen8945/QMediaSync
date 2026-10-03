package syncstrm

import (
	"context"
	"errors"
	"slices"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

type generationConfigKey struct {
	source     models.SourceType
	accountID  uint
	syncPathID uint
}

type generationConfigs map[generationConfigKey]SyncStrmConfig

// prepareGenerationConfigs 在第一项开始前准备所有目录，避免后半批读到刚修改的设置。
func prepareGenerationConfigs(ctx context.Context, tasks []*models.StrmGenerationTask) (generationConfigs, error) {
	ids := make([]uint, 0, len(tasks))
	for _, task := range tasks {
		if task.Status != models.StrmGenerationStatusFinalizing && !slices.Contains(ids, task.SyncPathId) {
			ids = append(ids, task.SyncPathId)
		}
	}
	configs := make(generationConfigs, len(ids))
	if len(ids) == 0 {
		return configs, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	models.LoadSettings()
	defaults, _ := models.SettingsGlobal.StrmSnapshot()
	var paths []models.SyncPath
	if err := db.Db.WithContext(ctx).Where("id IN ?", ids).Find(&paths).Error; err != nil {
		return nil, err
	}
	for i := range paths {
		path := &paths[i]
		path.ParseVideoAndMetaExt()
		configs[generationConfigKey{path.SourceType, path.AccountId, path.ID}] = configFromSyncPathDefaults(path, defaults)
	}
	return configs, nil
}

func (service *StrmGenerationService) buildGenerationSyncer(path *models.SyncPath, account *models.Account, configs generationConfigs) (*SyncStrm, error) {
	if configs == nil {
		return service.buildSyncer(path, account, nil)
	}
	config, ok := configs[generationConfigKey{path.SourceType, path.AccountId, path.ID}]
	if !ok {
		return nil, errors.New("同步目录或账号已变化，请重试 STRM 任务")
	}
	return service.buildSyncer(path, account, &config)
}

func cloneGenerationConfig(config SyncStrmConfig) SyncStrmConfig {
	config.VideoExt = slices.Clone(config.VideoExt)
	config.MetaExt = slices.Clone(config.MetaExt)
	config.ExcludeNames = slices.Clone(config.ExcludeNames)
	config.ExcludeNameRegexes = slices.Clone(config.ExcludeNameRegexes)
	return config
}
