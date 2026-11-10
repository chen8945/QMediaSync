package models

import (
	"database/sql"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// 历史迁移必须使用当时的整数列，不能在身份转换前由 AutoMigrate 改成文本。
type legacyEmbyMediaSyncFile struct {
	BaseModel
	SyncPathId uint   `json:"sync_path_id" gorm:"index:idx_emby_sync_path_id"`
	EmbyItemId uint   `json:"emby_item_id" gorm:"index:idx_emby_media_item_id"`
	SyncFileId uint   `json:"sync_file_id" gorm:"index:idx_emby_sync_file_id"`
	PickCode   string `json:"pick_code" gorm:"index:idx_emby_sf_pick_code"`
	SnapshotID uint   `json:"snapshot_id" gorm:"index"`
	SourceID   string `json:"source_id"`
}

func (*legacyEmbyMediaSyncFile) TableName() string { return "emby_media_sync_files" }

func migrateEmbyIdentity(tx *gorm.DB) error {
	var identityColumnType string
	if tx.Dialector.Name() == "postgres" {
		// 系统目录查询不复用历史增列前 SELECT * 的缓存返回类型。
		if err := tx.Raw(`SELECT format_type(atttypid, atttypmod) FROM pg_attribute
			WHERE attrelid = to_regclass('emby_media_sync_files') AND attname = 'emby_item_id'
			AND NOT attisdropped`).Scan(&identityColumnType).Error; err != nil {
			return err
		}
	} else {
		columns, err := tx.Migrator().ColumnTypes(&legacyEmbyMediaSyncFile{})
		if err != nil {
			return err
		}
		for _, column := range columns {
			if column.Name() == "emby_item_id" {
				identityColumnType = column.DatabaseTypeName()
			}
		}
	}
	if !strings.Contains(strings.ToLower(identityColumnType), "int") {
		return fmt.Errorf("旧媒体关联列不是整数类型，请使用升级前备份恢复后重试")
	}
	var conflicts []int64
	if err := tx.Raw(`SELECT m.item_id_int FROM emby_media_items m
		WHERE EXISTS (SELECT 1 FROM emby_media_sync_files l WHERE l.emby_item_id = m.item_id_int)
		GROUP BY m.item_id_int HAVING COUNT(DISTINCT m.item_id) > 1`).Scan(&conflicts).Error; err != nil {
		return err
	}
	if len(conflicts) != 0 {
		return fmt.Errorf("媒体关联存在重复的旧条目编号，无法确定原条目：%v", conflicts)
	}
	var countBefore int64
	if err := tx.Model(&legacyEmbyMediaSyncFile{}).Count(&countBefore).Error; err != nil {
		return err
	}
	switch tx.Dialector.Name() {
	case "postgres":
		if err := tx.Exec(`ALTER TABLE emby_media_sync_files ALTER COLUMN emby_item_id TYPE text USING emby_item_id::text`).Error; err != nil {
			return err
		}
	case "sqlite":
		var indexes []struct{ Name, SQL string }
		if err := tx.Raw(`SELECT name, sql FROM sqlite_master WHERE type = 'index'
			AND tbl_name = 'emby_media_sync_files' AND sql IS NOT NULL`).Scan(&indexes).Error; err != nil {
			return err
		}
		var sequence sql.NullInt64
		if err := tx.Raw(`SELECT seq FROM sqlite_sequence WHERE name = 'emby_media_sync_files'`).Scan(&sequence).Error; err != nil {
			return err
		}
		if err := tx.Migrator().AlterColumn(&EmbyMediaSyncFile{}, "EmbyItemId"); err != nil {
			return err
		}
		for _, index := range indexes {
			if !tx.Migrator().HasIndex(&EmbyMediaSyncFile{}, index.Name) {
				if err := tx.Exec(index.SQL).Error; err != nil {
					return err
				}
			}
		}
		if sequence.Valid {
			if err := tx.Exec(`UPDATE sqlite_sequence SET seq = MAX(seq, ?) WHERE name = 'emby_media_sync_files'`, sequence.Int64).Error; err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("不支持的数据库类型：%s", tx.Dialector.Name())
	}
	// 旧列只含精确整数或 NULL；以数字索引查找真实远端 ID，孤儿保留十进制值。
	if err := tx.Exec(`UPDATE emby_media_sync_files SET emby_item_id = COALESCE(
		(SELECT MIN(m.item_id) FROM emby_media_items m
		WHERE m.item_id_int = CAST(emby_media_sync_files.emby_item_id AS BIGINT)), emby_item_id)
		WHERE emby_item_id IS NOT NULL`).Error; err != nil {
		return err
	}
	// PostgreSQL 原地改列已保留索引与约束，避免再次查询旧的缓存返回类型。
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.AutoMigrate(&EmbyMediaSyncFile{}); err != nil {
			return err
		}
	}
	var countAfter int64
	if err := tx.Model(&EmbyMediaSyncFile{}).Count(&countAfter).Error; err != nil {
		return err
	}
	if countAfter != countBefore {
		return fmt.Errorf("媒体关联迁移前后行数不一致")
	}
	return nil
}
