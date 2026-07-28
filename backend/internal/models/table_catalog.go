package models

import (
	"reflect"

	"gorm.io/gorm/schema"
)

// SchemaVersion 是应用数据表结构的兼容版本。
// 备份工件记录它以拒绝来自更高版本的恢复；较低版本的工件导入后由既有迁移链升级。
// 新增、删除表或做出不向后兼容的字段变更时必须递增，并同步更新数据库 schema 文档。
const SchemaVersion = 1

// TableCatalogUsage 描述表在不同数据交换流程中的用途。
type TableCatalogUsage struct {
	RegularBackupRestore    bool
	SQLitePostgresMigration bool
}

// TableCatalogEntry 是由 AllTables 派生出的稳定数据表目录项。
// ID 使用物理表名，作为工件和迁移文件的稳定标识。
type TableCatalogEntry struct {
	ID           string
	Model        any
	PhysicalName string
	ImportOrder  int
	Usage        TableCatalogUsage
}

var masterTableCatalog = buildMasterTableCatalog(AllTables)

// MasterTableCatalog 返回全部应用表的目录副本。
func MasterTableCatalog() []TableCatalogEntry {
	return append([]TableCatalogEntry(nil), masterTableCatalog...)
}

// RegularBackupRestoreTableCatalog 返回常规备份和恢复使用的表目录。
// BackupRecord 是当前实例上的工件索引，不能随工件恢复。
func RegularBackupRestoreTableCatalog() []TableCatalogEntry {
	return selectTableCatalog(func(entry TableCatalogEntry) bool {
		return entry.Usage.RegularBackupRestore
	})
}

// SQLitePostgresMigrationTableCatalog 返回 SQLite→PostgreSQL 迁移使用的全部应用表目录。
func SQLitePostgresMigrationTableCatalog() []TableCatalogEntry {
	return selectTableCatalog(func(entry TableCatalogEntry) bool {
		return entry.Usage.SQLitePostgresMigration
	})
}

func buildMasterTableCatalog(tables []any) []TableCatalogEntry {
	entries := make([]TableCatalogEntry, 0, len(tables))
	backupRecordType := reflect.TypeOf(BackupRecord{})
	for order, model := range tables {
		physicalName := catalogPhysicalTableName(model)
		entries = append(entries, TableCatalogEntry{
			ID:           physicalName,
			Model:        model,
			PhysicalName: physicalName,
			ImportOrder:  order,
			Usage: TableCatalogUsage{
				RegularBackupRestore:    reflect.TypeOf(model) != backupRecordType,
				SQLitePostgresMigration: true,
			},
		})
	}
	return entries
}

func selectTableCatalog(include func(TableCatalogEntry) bool) []TableCatalogEntry {
	entries := make([]TableCatalogEntry, 0, len(masterTableCatalog))
	for _, entry := range masterTableCatalog {
		if include(entry) {
			entries = append(entries, entry)
		}
	}
	return entries
}

func catalogPhysicalTableName(model any) string {
	if named, ok := model.(interface{ TableName() string }); ok {
		return named.TableName()
	}
	modelType := reflect.TypeOf(model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	} else if named, ok := reflect.New(modelType).Interface().(interface{ TableName() string }); ok {
		return named.TableName()
	}
	return schema.NamingStrategy{}.TableName(modelType.Name())
}
