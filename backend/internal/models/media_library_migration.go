package models

import (
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// migrateMediaLibrarySchema 仅由 70→71 在旧整数身份转换后调用。
func migrateMediaLibrarySchema(tx *gorm.DB) error {
	// 同名旧索引不会因 AutoMigrate 增加复合列，先删除后按当前模型重建。
	for _, entry := range []struct {
		model any
		names []string
	}{
		{&EmbyMediaItem{}, []string{"idx_emby_item_id", embyMediaPartLookupIndexName, embyMediaVersionLookupIndexName}},
		{&EmbyLibrarySyncPath{}, []string{"idx_lib_sync_path"}},
		{&EmbyItemState{}, []string{"idx_emby_item_state"}},
		{&EmbyItemEvidence{}, []string{"idx_emby_evidence_item"}},
		{&EmbyItemMembership{}, []string{"idx_emby_membership_item", "idx_emby_membership_part", "idx_emby_membership_season", "idx_emby_membership_series"}},
		{&EmbyObservedEvidenceIndex{}, []string{"idx_emby_observed_item", "idx_emby_observed_part", "idx_emby_observed_season", "idx_emby_observed_series"}},
		{&EmbyLibraryRefreshTask{}, []string{"idx_emby_library_refresh_tasks_task_key"}},
	} {
		if !tx.Migrator().HasTable(entry.model) {
			continue
		}
		for _, name := range entry.names {
			if tx.Migrator().HasIndex(entry.model, name) {
				if err := tx.Migrator().DropIndex(entry.model, name); err != nil {
					return err
				}
			}
		}
	}
	// 已有表只加本版本列和索引，保留原列的值、NULL、默认值与约束。
	for _, entry := range []struct {
		model   any
		columns []string
	}{
		{&EmbyConfig{}, []string{"Enabled"}},
		{&JellyfinConfig{}, nil},
		{&EmbyMediaItem{}, []string{"MediaServerType"}},
		{&EmbyMediaSyncFile{}, []string{"MediaServerType"}},
		{&EmbyLibrary{}, []string{"MediaServerType"}},
		{&EmbyLibrarySyncPath{}, []string{"MediaServerType"}},
		{&EmbyIndexState{}, []string{"MediaServerType"}},
		{&EmbyItemState{}, []string{"MediaServerType"}},
		{&EmbyItemEvidence{}, []string{"MediaServerType"}},
		{&EmbyItemMembership{}, []string{"MediaServerType"}},
		{&EmbyObservedEvidenceIndex{}, []string{"MediaServerType"}},
		{&EmbyWebhookRecord{}, []string{"MediaServerType"}},
		{&EmbyWebhookTarget{}, nil},
		{&EmbyLibraryRefreshTask{}, []string{"MediaServerType", "ServerID"}},
		{&StrmGenerationTask{}, nil},
		{&DbDownloadTask{}, []string{"MediaServerID"}},
	} {
		if !tx.Migrator().HasTable(entry.model) {
			if err := tx.AutoMigrate(entry.model); err != nil {
				return err
			}
			continue
		}
		for _, column := range entry.columns {
			if !tx.Migrator().HasColumn(entry.model, column) {
				if err := tx.Migrator().AddColumn(entry.model, column); err != nil {
					return err
				}
			}
		}
		statement := &gorm.Statement{DB: tx}
		if err := statement.Parse(entry.model); err != nil {
			return err
		}
		for _, index := range statement.Schema.ParseIndexes() {
			changed := slices.ContainsFunc(index.Fields, func(option schema.IndexOption) bool {
				return option.Field != nil && slices.Contains(entry.columns, option.Field.Name)
			})
			if !changed {
				continue
			}
			if !tx.Migrator().HasIndex(entry.model, index.Name) {
				if err := tx.Migrator().CreateIndex(entry.model, index.Name); err != nil {
					return err
				}
			}
		}
	}
	if err := tx.Model(&EmbyConfig{}).Where("id > 0").Updates(map[string]any{"enabled": true, "enable_media_notification": 1}).Error; err != nil {
		return err
	}
	var embyConfigCount int64
	if err := tx.Model(&EmbyConfig{}).Count(&embyConfigCount).Error; err != nil {
		return err
	}
	if embyConfigCount == 0 {
		if err := initEmbyConfig(tx); err != nil {
			return err
		}
	}
	if err := initJellyfinConfig(tx); err != nil {
		return err
	}
	return EnsureEmbyMediaLookupIndexes(tx)
}

func initJellyfinConfig(tx *gorm.DB) error {
	var count int64
	if err := tx.Model(&JellyfinConfig{}).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return nil
	}
	config := JellyfinConfig{SyncEnabled: 0, SyncCron: "0 * * * *", EnableAuth: 1, EnableDailyFirstFullSync: 1, SyncMode: EmbySyncModeIdle}
	// map 写入保留显式零值，避免 GORM 的旧字段默认值覆盖。
	return tx.Model(&JellyfinConfig{}).Create(map[string]any{"enabled": false, "sync_enabled": 0, "sync_cron": config.SyncCron, "enable_auth": 1, "enable_daily_first_full_sync": 1, "sync_mode": config.SyncMode, "sync_all_libraries": 1, "selected_libraries": "[]", "path_mappings_json": "[]"}).Error
}
