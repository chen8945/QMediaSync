package models

import (
	"fmt"

	"gorm.io/gorm"
)

// MigrateEmbySyncEvidenceSchema 增加正常同步的目录资料与明确路径映射，不改写历史删除身份。
// 调用方在同一事务内推进版本；根链等待正常 STRM 响应，不在迁移中查询网盘。
func MigrateEmbySyncEvidenceSchema(tx *gorm.DB) error {
	if err := tx.AutoMigrate(&SyncRootEvidence{}); err != nil {
		return fmt.Errorf("创建同步根目录资料表失败：%w", err)
	}
	if tx.Migrator().HasTable(&EmbyConfig{}) {
		if !tx.Migrator().HasColumn(&EmbyConfig{}, "PathMappingsJSON") {
			if err := tx.Migrator().AddColumn(&EmbyConfig{}, "PathMappingsJSON"); err != nil {
				return fmt.Errorf("添加 Emby 路径映射字段失败：%w", err)
			}
		}
		if err := tx.Model(&EmbyConfig{}).
			Where("path_mappings_json IS NULL OR path_mappings_json = ?", "").
			UpdateColumn("path_mappings_json", "[]").Error; err != nil {
			return err
		}
	}
	if err := EnsureSyncFileLookupIndexes(tx); err != nil {
		return fmt.Errorf("创建同步文件查询索引失败：%w", err)
	}
	if err := EnsureEmbySyncFileLookupIndexes(tx); err != nil {
		return fmt.Errorf("创建 Emby 同步文件查询索引失败：%w", err)
	}
	if err := EnsureEmbyMediaLookupIndexes(tx); err != nil {
		return fmt.Errorf("创建 Emby 成员查询索引失败：%w", err)
	}
	return nil
}
