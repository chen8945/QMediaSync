package backup

import (
	"fmt"
	"slices"
	"strconv"

	"gorm.io/gorm"

	"qmediasync/internal/models"
)

var syncDurationColumns = []string{"net_file_duration_ms", "local_file_duration_ms", "generation_duration_ms", "total_duration_ms"}

func legacyEmbyIdentityColumn() logicalColumn {
	return logicalColumn{Name: "emby_item_id", Type: "unsigned_integer", Bits: 64}
}

// 旧关联只能使用同一备份的媒体身份解释，不能借目标库记录猜测来源。
func adaptEmbyIdentityRestorePlan(plan *restorePlan) error {
	var links, media *restoreTable
	for i := range plan.tables {
		table := &plan.tables[i]
		switch table.schema.Table {
		case "emby_media_sync_files":
			links = table
		case "emby_media_items":
			media = table
		}
	}
	if links != nil && links.rowCount > 0 {
		if media == nil {
			return fmt.Errorf("%w：恢复旧媒体关联时，请同时提供同一备份中的媒体条目表", ErrArchiveInvalid)
		}
		mapping := make(map[int64]string)
		conflicts := make(map[int64]bool)
		if media.readRows != nil {
			if err := media.readRows(func(row map[string]any) error {
				id, ok := row["item_id_int"].(int64)
				if !ok {
					return nil
				}
				itemID, ok := row["item_id"].(string)
				if !ok {
					return nil
				}
				if previous, exists := mapping[id]; exists && previous != itemID {
					conflicts[id] = true
				}
				mapping[id] = itemID
				return nil
			}); err != nil {
				return err
			}
		}
		readRows := links.readRows
		links.readRows = func(consume func(map[string]any) error) error {
			return readRows(func(row map[string]any) error {
				if id, ok := row["emby_item_id"].(int64); ok {
					if conflicts[id] {
						return fmt.Errorf("%w：旧媒体编号 %d 对应多个条目，无法恢复关联", ErrArchiveInvalid, id)
					}
					itemID, exists := mapping[id]
					if !exists {
						itemID = strconv.FormatInt(id, 10)
					}
					row["emby_item_id"] = itemID
				}
				return consume(row)
			})
		}
		if err := links.readRows(func(map[string]any) error { return nil }); err != nil {
			return err
		}
	}
	for i := range plan.tables {
		table := &plan.tables[i]
		if table.schema.Table == "migrator" && table.readRows != nil {
			readRows := table.readRows
			table.readRows = func(consume func(map[string]any) error) error {
				return readRows(func(row map[string]any) error {
					row["version_code"] = int64(models.MaxVersionCode)
					return consume(row)
				})
			}
		}
	}
	return nil
}

func supportsLogicalSchemaVersion(version int) bool {
	return version >= 67 && version <= models.MaxVersionCode
}

// logicalTablesForSchemaVersion 只回退明确新增的表和列，其他清单、行及文件校验保持原样。
func logicalTablesForSchemaVersion(tables []logicalTable, version int) []logicalTable {
	if version < 72 {
		for i := range tables {
			if tables[i].Name == "syncs" {
				tables[i].Columns = slices.DeleteFunc(tables[i].Columns, func(column logicalColumn) bool {
					return slices.Contains(syncDurationColumns, column.Name)
				})
			}
		}
	}
	if version < 71 {
		tables = slices.DeleteFunc(tables, func(table logicalTable) bool { return table.Name == "jellyfin_config" })
		for i := range tables {
			tables[i].Columns = slices.DeleteFunc(tables[i].Columns, func(column logicalColumn) bool {
				return slices.Contains(mediaLibraryAddedColumns(tables[i].Name), column.Name)
			})
		}
		for i := range tables {
			if tables[i].Name == "emby_media_sync_files" {
				for j := range tables[i].Columns {
					if tables[i].Columns[j].Name == "emby_item_id" {
						tables[i].Columns[j] = legacyEmbyIdentityColumn()
					}
				}
			}
		}
	}
	if version < 70 {
		for i := range tables {
			if tables[i].Name == "settings" {
				tables[i].Columns = slices.DeleteFunc(tables[i].Columns, func(column logicalColumn) bool {
					return column.Name == "strm_preload_enabled"
				})
			}
		}
	}
	if version < 69 {
		tables = slices.DeleteFunc(tables, func(table logicalTable) bool {
			return table.Name == "fnos_proxy_config"
		})
	}
	if version != 67 {
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

func logicalExclusionsForSchemaVersion(excluded []logicalExclusion, version int) []logicalExclusion {
	if version < 69 {
		return slices.DeleteFunc(excluded, func(item logicalExclusion) bool {
			return item.Name == "fnos_proxy_config"
		})
	}
	return excluded
}

// adaptSyncDurationRestorePlan 保留旧记录的未知精度，不从秒时间戳补造毫秒耗时。
func adaptSyncDurationRestorePlan(plan *restorePlan) {
	for i := range plan.tables {
		table := &plan.tables[i]
		readRows, name := table.readRows, table.schema.Table
		if name != "syncs" && name != "migrator" {
			continue
		}
		table.readRows = func(consume func(map[string]any) error) error {
			return readRows(func(row map[string]any) error {
				if name == "migrator" {
					row["version_code"] = int64(models.MaxVersionCode)
				} else {
					for _, field := range syncDurationColumns {
						row[field] = nil
					}
				}
				return consume(row)
			})
		}
	}
}

// adaptPreloadRestorePlan 只为已验证的旧版本补关闭值，不继承目标机的预加载开关。
func adaptPreloadRestorePlan(plan *restorePlan) {
	for i := range plan.tables {
		table := &plan.tables[i]
		if table.schema.Table != "settings" && table.schema.Table != "migrator" {
			continue
		}
		readRows, name := table.readRows, table.schema.Table
		table.readRows = func(consume func(map[string]any) error) error {
			return readRows(func(row map[string]any) error {
				if name == "settings" {
					row["strm_preload_enabled"] = int64(0)
				} else {
					row["version_code"] = int64(70)
				}
				return consume(row)
			})
		}
	}
}

// adaptFnosProxyRestorePlan 补空 69 新增的配置表，不保留目标机旧的启用配置。
func adaptFnosProxyRestorePlan(database *gorm.DB, plan *restorePlan) error {
	hasConfig := false
	for i := range plan.tables {
		table := &plan.tables[i]
		switch table.schema.Table {
		case "fnos_proxy_config":
			table.readRows, table.rowCount = nil, 0
			hasConfig = true
		case "migrator":
			readRows := table.readRows
			table.readRows = func(consume func(map[string]any) error) error {
				return readRows(func(row map[string]any) error {
					row["version_code"] = int64(69)
					return consume(row)
				})
			}
		}
	}
	if !hasConfig {
		for _, model := range models.AllTables {
			switch model.(type) {
			case models.FnosProxyConfig, *models.FnosProxyConfig:
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

// mediaLibraryAddedColumns 是 70→71 的新增列，旧清单只能退回这些已知差异。
func mediaLibraryAddedColumns(table string) []string {
	switch table {
	case "emby_config":
		return []string{"enabled"}
	case "db_download_tasks":
		return []string{"media_server_id"}
	case "emby_library_refresh_tasks":
		return []string{"media_server_type", "server_id"}
	case "emby_media_items", "emby_media_sync_files", "emby_libraries", "emby_library_sync_paths", "emby_index_states", "emby_item_states", "emby_item_evidences", "emby_item_memberships", "emby_observed_evidence_indices", "emby_webhook_records":
		return []string{"media_server_type"}
	}
	return nil
}

// adaptMediaLibraryRestorePlan 只为受支持旧包补默认归属，不改冻结 JSON 或摘要。
func adaptMediaLibraryRestorePlan(database *gorm.DB, plan *restorePlan) error {
	hasEmbyConfig := false
	for i := range plan.tables {
		table := &plan.tables[i]
		if table.schema.Table == "emby_config" {
			hasEmbyConfig = true
		}
		readRows, name := table.readRows, table.schema.Table
		if readRows == nil {
			continue
		}
		table.readRows = func(consume func(map[string]any) error) error {
			return readRows(func(row map[string]any) error {
				if name == "emby_config" {
					row["enabled"], row["enable_media_notification"] = true, int64(1)
				}
				if slices.Contains(mediaLibraryAddedColumns(name), "media_server_type") {
					row["media_server_type"] = string(models.MediaServerEmby)
				}
				return consume(row)
			})
		}
	}
	// 部分旧恢复只有实际恢复 Emby 配置时才初始化新增端，避免修改无关目标配置。
	for _, model := range models.AllTables {
		if !hasEmbyConfig || models.GetTableName(model) != "jellyfin_config" {
			continue
		}
		entry, err := newRestoreTable(database, model)
		if err != nil {
			return err
		}
		entry.rowCount = 1
		entry.readRows = func(consume func(map[string]any) error) error {
			return consume(map[string]any{"id": int64(1), "enabled": false, "sync_enabled": int64(0), "enable_media_notification": int64(0), "sync_cron": "0 * * * *", "enable_auth": int64(1), "enable_daily_first_full_sync": int64(1), "sync_mode": models.EmbySyncModeIdle, "sync_all_libraries": int64(1), "selected_libraries": "[]", "path_mappings_json": "[]"})
		}
		plan.tables = append(plan.tables, entry)
		break
	}
	return nil
}
