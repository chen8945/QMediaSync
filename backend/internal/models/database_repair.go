package models

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

func readMigrationRecord(conn *gorm.DB) (Migrator, error) {
	if conn == nil {
		return Migrator{}, errors.New("数据库连接为空")
	}
	if !conn.Migrator().HasTable(&Migrator{}) {
		return Migrator{}, errors.New("数据库版本表 migrator 缺失，无法确定迁移起点")
	}
	var records []Migrator
	if err := conn.Limit(2).Find(&records).Error; err != nil {
		return Migrator{}, fmt.Errorf("读取数据库版本记录失败：%w", err)
	}
	if len(records) != 1 {
		return Migrator{}, errors.New("数据库版本表 migrator 必须包含且仅包含一条版本记录")
	}
	if records[0].VersionCode < 1 || records[0].VersionCode > MaxVersionCode {
		return Migrator{}, fmt.Errorf("数据库版本 %d 不在支持范围 1–%d 内", records[0].VersionCode, MaxVersionCode)
	}
	return records[0], nil
}

// RepairDatabase 在可信版本元数据存在时补齐结构、派生索引和主键序列。
// 结构修复仍保留历史行为：回填传输去重键，并取消重复的活跃传输任务。
func RepairDatabase(conn *gorm.DB) error {
	if _, err := readMigrationRecord(conn); err != nil {
		return err
	}
	if err := batchCreateTable(conn, false); err != nil {
		return fmt.Errorf("补齐数据库结构失败：%w", err)
	}
	if conn.Dialector.Name() != "postgres" {
		return nil
	}
	for _, model := range AllTables {
		if err := conn.Transaction(func(tx *gorm.DB) error {
			return RepairSequencesTx(tx, []any{model})
		}); err != nil {
			return err
		}
	}
	return nil
}
