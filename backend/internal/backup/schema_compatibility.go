package backup

import (
	"slices"

	"gorm.io/gorm"

	"qmediasync/internal/models"
)

func supportsLogicalSchemaVersion(version int) bool {
	return version == models.MaxVersionCode || (version == 67 && models.MaxVersionCode == 68)
}

// logicalTablesForSchemaVersion 只回退明确新增的表和列，其他清单、行及文件校验保持原样。
func logicalTablesForSchemaVersion(tables []logicalTable, version int) []logicalTable {
	if version != 67 || models.MaxVersionCode != 68 {
		return tables
	}
	tables = slices.DeleteFunc(tables, func(table logicalTable) bool {
		return table.Name == "sync_root_evidences"
	})
	for i := range tables {
		if tables[i].Name == "emby_config" {
			tables[i].Columns = slices.DeleteFunc(tables[i].Columns, func(column logicalColumn) bool {
				return column.Name == "path_mappings_json"
			})
		}
	}
	return tables
}

// adaptEmbySyncRestorePlan 按已验证的来源版本补齐 67 → 68 的空资料。
// 不推算来源身份、读取时间或目录权限，也不保留目标库与旧包无关的同步根资料。
func adaptEmbySyncRestorePlan(database *gorm.DB, plan *restorePlan) error {
	hasRootEvidence := false
	for i := range plan.tables {
		table := &plan.tables[i]
		switch table.schema.Table {
		case "sync_root_evidences":
			table.readRows, table.rowCount = nil, 0
			hasRootEvidence = true
		case "emby_config", "migrator":
			readRows, name := table.readRows, table.schema.Table
			table.readRows = func(consume func(map[string]any) error) error {
				return readRows(func(row map[string]any) error {
					if name == "emby_config" {
						row["path_mappings_json"] = "[]"
					} else {
						row["version_code"] = int64(68)
					}
					return consume(row)
				})
			}
		}
	}
	if !hasRootEvidence {
		for _, model := range models.AllTables {
			switch model.(type) {
			case models.SyncRootEvidence, *models.SyncRootEvidence:
				entry, err := newRestoreTable(database, model)
				if err != nil {
					return err
				}
				plan.tables = append(plan.tables, entry)
			}
		}
	}
	return nil
}
