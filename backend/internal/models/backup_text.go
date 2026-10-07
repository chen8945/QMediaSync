package models

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MigrateBackupTextColumns 将没有业务长度上限的外部文本改为 text，保留原值和其他约束。
// 调用方负责在同一事务内推进 schema 版本；SQLite 的现有文本列本就不限制长度。
func MigrateBackupTextColumns(tx *gorm.DB) error {
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	changes := []struct {
		model   interface{ TableName() string }
		columns []string
	}{
		{&Account{}, []string{"token", "refresh_token", "username", "password", "base_url", "token_failed_reason"}},
		{&EmbyConfig{}, []string{"emby_url", "emby_api_key", "sync_cron"}},
		{&RequestStat{}, []string{"url"}},
		{&EmbyLibraryRefreshTask{}, []string{"library_name", "fallback_library_name"}},
	}
	for _, change := range changes {
		if !tx.Migrator().HasTable(change.model) {
			continue
		}
		for _, column := range change.columns {
			if !tx.Migrator().HasColumn(change.model, column) {
				continue
			}
			// 只修改类型，避免 GORM AlterColumn 同时调整已有列的默认值和可空约束。
			if err := tx.Exec("ALTER TABLE ? ALTER COLUMN ? TYPE text",
				clause.Table{Name: change.model.TableName()}, clause.Column{Name: column}).Error; err != nil {
				return fmt.Errorf("扩展 %s.%s 文本列失败：%w", change.model.TableName(), column, err)
			}
		}
	}
	return nil
}
