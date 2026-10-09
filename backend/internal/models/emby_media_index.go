package models

import "gorm.io/gorm"

const (
	embyMediaPartLookupIndexName    = "idx_emby_server_part_lookup"
	embyMediaVersionLookupIndexName = "idx_emby_server_version_lookup"
)

// EnsureEmbyMediaLookupIndexes 补齐 SQLite 分段及版本成员查询索引，不依赖同步文件表。
func EnsureEmbyMediaLookupIndexes(dbConn *gorm.DB) error {
	if dbConn.Dialector.Name() != "sqlite" || !dbConn.Migrator().HasTable(&EmbyMediaItem{}) {
		return nil
	}
	for _, statement := range []string{
		"CREATE INDEX IF NOT EXISTS " + embyMediaPartLookupIndexName + " ON emby_media_items (server_id, part_of_item_id)",
		"CREATE INDEX IF NOT EXISTS " + embyMediaVersionLookupIndexName + " ON emby_media_items (server_id, version_of_item_id)",
	} {
		if err := dbConn.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}
