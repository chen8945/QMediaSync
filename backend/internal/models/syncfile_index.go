package models

import "gorm.io/gorm"

const (
	syncFileSiblingPathIndexName       = "idx_sync_files_sibling_path"
	syncFileIdentityIndexName          = "idx_sync_files_identity"
	syncFileNameIndexName              = "idx_sync_files_file_name"
	syncFileSidecarLookupIndexName     = "idx_sync_files_sidecar_lookup"
	syncFileDirectoryNameIndexName     = "idx_sync_files_directory_name"
	syncFileDirectoryPickCodeIndexName = "idx_sync_files_directory_pick_code"
)

// EnsureSyncFileLookupIndexes 为同步文件、同目录和完整名称查询补齐索引，不修改已有记录。
func EnsureSyncFileLookupIndexes(dbConn *gorm.DB) error {
	if !dbConn.Migrator().HasTable(&SyncFile{}) {
		return nil
	}
	query := "CREATE INDEX IF NOT EXISTS " + syncFileSiblingPathIndexName + " ON sync_files (sync_path_id, path)"
	if dbConn.Dialector.Name() == "postgres" {
		// HASH 支持路径相等查询，不受 B-tree 单项长度限制。
		query = "CREATE INDEX IF NOT EXISTS " + syncFileSiblingPathIndexName + " ON sync_files USING HASH (path)"
	}
	if err := dbConn.Exec(query).Error; err != nil {
		return err
	}
	if dbConn.Dialector.Name() == "sqlite" {
		if err := dbConn.Exec("CREATE INDEX IF NOT EXISTS " + syncFileIdentityIndexName + " ON sync_files (sync_path_id, file_id)").Error; err != nil {
			return err
		}
	}
	query = "CREATE INDEX IF NOT EXISTS " + syncFileNameIndexName + " ON sync_files (file_name)"
	if dbConn.Dialector.Name() == "postgres" {
		// 名称只参与相等查询；完整长名称同样不能受 B-tree 单项长度限制。
		query = "CREATE INDEX IF NOT EXISTS " + syncFileNameIndexName + " ON sync_files USING HASH (file_name)"
	}
	return dbConn.Exec(query).Error
}

// EnsureEmbySyncFileLookupIndexes 补齐 SQLite 的 Emby 旁车及目录定位索引。
func EnsureEmbySyncFileLookupIndexes(dbConn *gorm.DB) error {
	if dbConn.Dialector.Name() != "sqlite" || !dbConn.Migrator().HasTable(&SyncFile{}) {
		return nil
	}
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS " + syncFileSidecarLookupIndexName + " ON sync_files (source_type, account_id, path, is_meta, is_video)",
		"CREATE INDEX IF NOT EXISTS " + syncFileDirectoryNameIndexName + " ON sync_files (sync_path_id, path, file_name)",
		"CREATE INDEX IF NOT EXISTS " + syncFileDirectoryPickCodeIndexName + " ON sync_files (sync_path_id, pick_code)",
	} {
		if err := dbConn.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
