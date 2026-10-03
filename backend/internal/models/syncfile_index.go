package models

import "gorm.io/gorm"

const (
	syncFileSiblingPathIndexName = "idx_sync_files_sibling_path"
	syncFileIdentityIndexName    = "idx_sync_files_identity"
)

// EnsureSyncFileLookupIndexes 为同步文件和同目录查询补齐索引，不修改已有记录。
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
		return dbConn.Exec("CREATE INDEX IF NOT EXISTS " + syncFileIdentityIndexName + " ON sync_files (sync_path_id, file_id)").Error
	}
	return nil
}
