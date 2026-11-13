package models

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
	syncscope "qmediasync/internal/strm/scope"

	"gorm.io/gorm"
)

// EmbyMediaItem 同步下来的 Emby 媒体项
type EmbyMediaItem struct {
	BaseModel
	MediaServerType   MediaServerType `json:"-" gorm:"not null;default:emby;uniqueIndex:idx_emby_item_id,priority:0"`
	ItemId            string          `json:"item_id" gorm:"uniqueIndex:idx_emby_item_id"`
	ItemIdInt         int64           `json:"item_id_int" gorm:"index:idx_emby_item_id_int"`
	ServerId          string          `json:"server_id" gorm:"index:idx_emby_server_id"`
	Name              string          `json:"name"`
	Type              string          `json:"type" gorm:"index:idx_emby_type"`
	ParentId          string          `json:"parent_id" gorm:"index:idx_emby_parent_id"`
	SeriesId          string          `json:"series_id" gorm:"index:idx_emby_series_id"`
	SeriesName        string          `json:"series_name"`
	SeasonId          string          `json:"season_id" gorm:"index:idx_emby_season_id"`
	SeasonName        string          `json:"season_name"`
	LibraryId         string          `json:"library_id" gorm:"index:idx_emby_library_id"`
	Path              string          `json:"path"`
	PickCode          string          `json:"pick_code" gorm:"index:idx_emby_pick_code"`
	MediaSourcePath   string          `json:"media_source_path"`
	IndexNumber       int             `json:"index_number"`
	ParentIndexNumber int             `json:"parent_index_number"`
	ProductionYear    int             `json:"production_year"`
	PremiereDate      string          `json:"premiere_date"`
	DateCreated       string          `json:"date_created"`
	DateCreatedTime   int64           `json:"date_created_time" gorm:"index:idx_emby_date_created_time"`
	DateModified      string          `json:"date_modified"`
	DateModifiedTime  int64           `json:"date_modified_time"`
	IsFolder          bool            `json:"is_folder"`
	LastSeenSyncRun   string          `json:"last_seen_sync_run" gorm:"index;type:varchar(64)"`
	LastSeenAt        int64           `json:"last_seen_at" gorm:"index"`
	PartCount         int             `json:"part_count"`
	PartOfItemID      string          `json:"part_of_item_id" gorm:"index"`
	VersionOfItemID   string          `json:"version_of_item_id" gorm:"index"`
	SnapshotID        uint            `json:"snapshot_id" gorm:"index"`
	Generation        int64           `json:"generation"`
}

func (*EmbyMediaItem) TableName() string {
	return "emby_media_items"
}

// EmbyMediaSyncFile 关联表（多对多）
type EmbyMediaSyncFile struct {
	BaseModel
	MediaServerType MediaServerType `json:"-" gorm:"not null;default:emby;index"`
	SyncPathId      uint            `json:"sync_path_id" gorm:"index:idx_emby_sync_path_id"`
	EmbyItemId      string          `json:"emby_item_id" gorm:"index:idx_emby_media_item_id"`
	SyncFileId      uint            `json:"sync_file_id" gorm:"index:idx_emby_sync_file_id"`
	PickCode        string          `json:"pick_code" gorm:"index:idx_emby_sf_pick_code"`
	SnapshotID      uint            `json:"snapshot_id" gorm:"index"`
	SourceID        string          `json:"source_id"`
}

func (*EmbyMediaSyncFile) TableName() string {
	return "emby_media_sync_files"
}

// UnmarshalJSON 兼容持久删除任务中冻结的旧整数 ID，保留精度且不改写原始 JSON。
func (link *EmbyMediaSyncFile) UnmarshalJSON(data []byte) error {
	type plain EmbyMediaSyncFile
	decoded := struct {
		*plain
		EmbyItemID json.RawMessage `json:"emby_item_id"`
	}{plain: (*plain)(link)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	if len(decoded.EmbyItemID) == 0 {
		return nil
	}
	if err := json.Unmarshal(decoded.EmbyItemID, &link.EmbyItemId); err == nil {
		return nil
	}
	var legacyID uint64
	if err := json.Unmarshal(decoded.EmbyItemID, &legacyID); err != nil {
		return err
	}
	link.EmbyItemId = strconv.FormatUint(legacyID, 10)
	return nil
}

// EmbyLibrarySyncPath 媒体库与 SyncPath 关联（多对多允许重复库对应多个路径）
type EmbyLibrarySyncPath struct {
	BaseModel
	MediaServerType MediaServerType `json:"-" gorm:"not null;default:emby;uniqueIndex:idx_lib_sync_path,priority:0"`
	LibraryId       string          `json:"library_id" gorm:"uniqueIndex:idx_lib_sync_path,priority:1"`
	SyncPathId      uint            `json:"sync_path_id" gorm:"uniqueIndex:idx_lib_sync_path,priority:2"`
	LibraryName     string          `json:"library_name"`
}

func (*EmbyLibrarySyncPath) TableName() string {
	return "emby_library_sync_paths"
}

// EmbyLibrary 媒体库基础表（LibraryId 改为 string 以兼容 Emby 返回的字符串 ID）
type EmbyLibrary struct {
	BaseModel
	MediaServerType MediaServerType `json:"-" gorm:"not null;default:emby;index"`
	Name            string          `json:"name"`
	LibraryId       string          `json:"library_id"`
	SyncPathId      uint            `json:"sync_path_id"` // 媒体库对应的同步目录 ID，如果为 0 则表示没有关联同步目录
}

func (*EmbyLibrary) TableName() string {
	return "emby_libraries"
}

// UpsertEmbyLibraries 更新或创建媒体库记录
func UpsertEmbyLibraries(kind MediaServerType, libs []embyclientrestgo.EmbyLibrary) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	for _, lib := range libs {
		existing := &EmbyLibrary{}
		err := db.Db.Where("media_server_type = ?", kind).Where("library_id = ?", lib.ID).First(existing).Error
		switch {
		case err == nil:
			if existing.Name != lib.Name {
				existing.Name = lib.Name
				if uerr := db.Db.Save(existing).Error; uerr != nil {
					return uerr
				}
			}
		case err == gorm.ErrRecordNotFound:
			rec := &EmbyLibrary{MediaServerType: kind, Name: lib.Name, LibraryId: lib.ID}
			if cerr := db.Db.Save(rec).Error; cerr != nil {
				return cerr
			}
		default:
			return err
		}
	}
	return nil
}

// CleanupDeletedEmbyLibraries 清理已不在 Emby 中存在的媒体库记录
func CleanupDeletedEmbyLibraries(kind MediaServerType, activeLibraryIds []string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if len(activeLibraryIds) == 0 {
		return nil
	}

	// 级联清理关联的同步路径记录
	if err := db.Db.Where("media_server_type = ?", kind).Where("library_id NOT IN ?", activeLibraryIds).Delete(&EmbyLibrarySyncPath{}).Error; err != nil {
		return err
	}

	// 清理已删除的媒体库记录
	return db.Db.Where("media_server_type = ?", kind).Where("library_id NOT IN ?", activeLibraryIds).Delete(&EmbyLibrary{}).Error
}

// CreateOrUpdateEmbyMediaItem upsert by ItemId
func CreateOrUpdateEmbyMediaItem(kind MediaServerType, item *EmbyMediaItem) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if item == nil {
		return fmt.Errorf("媒体条目不能为空")
	}
	item.MediaServerType = kind
	return upsertEmbyMediaItem(db.Db, item)
}

func GetEmbyMediaItemsCount(kind MediaServerType) (int64, error) {
	if err := kind.Validate(); err != nil {
		return 0, err
	}
	var total int64
	err := db.Db.Where("media_server_type = ?", kind).Model(&EmbyMediaItem{}).Count(&total).Error
	return total, err
}

func CleanupOrphanedEmbyMediaItems(kind MediaServerType, validItemIds []string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if len(validItemIds) == 0 {
		return db.Db.Where("media_server_type = ?", kind).Where("1 = 1").Delete(&EmbyMediaItem{}).Error
	}

	// 当 validItemIds 很多时，分批处理以避免 SQL 语句过长
	// 每批处理 1000 个 ID，这是一个安全的数量
	const batchSize = 1000

	if len(validItemIds) <= batchSize {
		// 数量不多，直接使用 IN 操作符
		return db.Db.Where("media_server_type = ?", kind).Where("item_id NOT IN ?", validItemIds).Delete(&EmbyMediaItem{}).Error
	}

	// 数量很多，使用分批删除逻辑
	// 先获取所有 item_id，然后分批删除不在 validItemIds 中的记录
	validItemSet := make(map[string]bool)
	for _, itemId := range validItemIds {
		validItemSet[itemId] = true
	}

	// 获取数据库中所有 item_id，然后找出需要删除的
	var allItems []string
	if err := db.Db.Where("media_server_type = ?", kind).Model(&EmbyMediaItem{}).Pluck("item_id", &allItems).Error; err != nil {
		return err
	}

	// 找出需要删除的 item_id
	var itemsToDelete []string
	for _, itemId := range allItems {
		if !validItemSet[itemId] {
			itemsToDelete = append(itemsToDelete, itemId)
		}
	}

	if len(itemsToDelete) == 0 {
		return nil
	}

	// 分批删除
	for i := 0; i < len(itemsToDelete); i += batchSize {
		end := min(i+batchSize, len(itemsToDelete))

		batch := itemsToDelete[i:end]
		if err := db.Db.Where("media_server_type = ?", kind).Where("item_id IN ?", batch).Delete(&EmbyMediaItem{}).Error; err != nil {
			return err
		}
	}

	return nil
}

// CleanupStaleEmbyMediaItemsByLibrarySyncRun 按全量同步批次清理指定媒体库内未出现的旧条目。
func CleanupStaleEmbyMediaItemsByLibrarySyncRun(kind MediaServerType, libraryID string, syncRunID string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if libraryID == "" || syncRunID == "" {
		return nil
	}

	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var staleItemIDs []string
	if err := tx.Where("media_server_type = ?", kind).Model(&EmbyMediaItem{}).
		Where("library_id = ? AND (last_seen_sync_run IS NULL OR last_seen_sync_run != ?)", libraryID, syncRunID).
		Pluck("item_id", &staleItemIDs).Error; err != nil {
		tx.Rollback()
		return err
	}
	if len(staleItemIDs) == 0 {
		tx.Rollback()
		return nil
	}

	if err := tx.Where("media_server_type = ?", kind).Where("emby_item_id IN ?", staleItemIDs).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("media_server_type = ?", kind).Where("library_id = ? AND (last_seen_sync_run IS NULL OR last_seen_sync_run != ?)", libraryID, syncRunID).
		Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// DeleteLocalEmbyItemByID 删除本地单个 Emby 条目索引及关联。
func DeleteLocalEmbyItemByID(kind MediaServerType, itemID string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if itemID == "" {
		return nil
	}
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()
	if err := tx.Where("media_server_type = ?", kind).Where("emby_item_id = ?", itemID).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("media_server_type = ?", kind).Where("item_id = ?", itemID).Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// DeleteLocalEmbyItemsBySeasonID 删除本地指定季下的 Emby 条目索引及关联。
func DeleteLocalEmbyItemsBySeasonID(kind MediaServerType, seasonID string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	return deleteLocalEmbyItemsByField(kind, "season_id", seasonID)
}

// DeleteLocalEmbyItemsBySeriesID 删除本地指定剧下的 Emby 条目索引及关联。
func DeleteLocalEmbyItemsBySeriesID(kind MediaServerType, seriesID string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	return deleteLocalEmbyItemsByField(kind, "series_id", seriesID)
}

func deleteLocalEmbyItemsByField(kind MediaServerType, field string, value string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if value == "" {
		return nil
	}
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var itemIDs []string
	if err := tx.Where("media_server_type = ?", kind).Model(&EmbyMediaItem{}).Where(field+" = ?", value).Pluck("item_id", &itemIDs).Error; err != nil {
		tx.Rollback()
		return err
	}
	if len(itemIDs) > 0 {
		if err := tx.Where("media_server_type = ?", kind).Where("emby_item_id IN ?", itemIDs).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Where("media_server_type = ?", kind).Where(field+" = ?", value).Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// CreateEmbyMediaSyncFile 创建关联（存在则跳过）
func CreateEmbyMediaSyncFile(kind MediaServerType, embyItemId string, syncFileId uint, pickCode string, syncPathId uint) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	var count int64
	if err := db.Db.Where("media_server_type = ?", kind).Model(&EmbyMediaSyncFile{}).
		Where("emby_item_id = ? AND sync_file_id = ?", embyItemId, syncFileId).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	relation := &EmbyMediaSyncFile{MediaServerType: kind, EmbyItemId: embyItemId, SyncFileId: syncFileId, PickCode: pickCode, SyncPathId: syncPathId}
	return db.Db.Save(relation).Error
}

// CreateOrUpdateEmbyLibrarySyncPath 创建或更新关联（存在则跳过）
func CreateOrUpdateEmbyLibrarySyncPath(kind MediaServerType, libraryId string, syncPathId uint, libraryName string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	var count int64
	if err := db.Db.Where("media_server_type = ?", kind).Model(&EmbyLibrarySyncPath{}).
		Where("library_id = ? AND sync_path_id = ?", libraryId, syncPathId).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	relation := &EmbyLibrarySyncPath{MediaServerType: kind, LibraryId: libraryId, SyncPathId: syncPathId, LibraryName: libraryName}
	return db.Db.Save(relation).Error
}

// DeleteEmbyLibrarySyncPathsBySyncPathID 按同步路径删除关联
func DeleteEmbyLibrarySyncPathsBySyncPathID(kind MediaServerType, syncPathId uint) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	return db.Db.Where("media_server_type = ?", kind).Where("sync_path_id = ?", syncPathId).Delete(&EmbyLibrarySyncPath{}).Error
}

// DeleteEmbyMediaSyncFilesBySyncFileID 按 SyncFile 删除关联
func DeleteEmbyMediaSyncFilesBySyncFileID(kind MediaServerType, syncFileId uint) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	return db.Db.Where("media_server_type = ?", kind).Where("sync_file_id = ?", syncFileId).Delete(&EmbyMediaSyncFile{}).Error
}

// DeleteEmbyMediaSyncFilesByPickCode 按 PickCode 删除关联
func DeleteEmbyMediaSyncFilesByPickCode(kind MediaServerType, pickCode string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	if pickCode == "" {
		return nil
	}
	return db.Db.Where("media_server_type = ?", kind).Where("pick_code = ?", pickCode).Delete(&EmbyMediaSyncFile{}).Error
}

// UpdateLastSyncTime 更新最后同步时间戳
func UpdateLastSyncTime(kind MediaServerType) error {
	return UpdateMediaLibraryConfig(kind, map[string]any{"last_sync_time": helpers.NowUnix()})
}

// 使用 SyncPath 查询关联的 Emby LibraryId -> LibraryName 列表
func GetEmbyLibraryIdsBySyncPathId(kind MediaServerType, syncPathId uint) (map[string]string, error) {
	if err := kind.Validate(); err != nil {
		return nil, err
	}
	var relations []EmbyLibrarySyncPath
	if err := db.Db.Where("media_server_type = ?", kind).Where("sync_path_id = ?", syncPathId).Find(&relations).Error; err != nil {
		return nil, err
	}
	var libraryIds map[string]string = make(map[string]string)
	for _, rel := range relations {
		libraryIds[rel.LibraryId] = rel.LibraryName
	}
	return libraryIds, nil
}

// DeleteNetdiskMovieByEmbyItemId 保留旧调用签名；删除需由已核验的冻结计划执行。
func DeleteNetdiskMovieByEmbyItemId(itemId string) error {
	return DeleteNetdiskMovieByEmbyItemIdContext(context.Background(), itemId)
}

func DeleteNetdiskMovieByEmbyItemIdContext(ctx context.Context, itemId string) error {
	return waitForVerifiedEmbyDeletion(ctx)
}

func DeleteNetdiskEpisodeByEmbyItemId(itemId string) error {
	return DeleteNetdiskEpisodeByEmbyItemIdContext(context.Background(), itemId)
}

func DeleteNetdiskEpisodeByEmbyItemIdContext(ctx context.Context, itemId string) error {
	return waitForVerifiedEmbyDeletion(ctx)
}

func DeleteNetdiskVideoByEmbyItemIdContext(ctx context.Context, itemId string) error {
	return waitForVerifiedEmbyDeletion(ctx)
}

func DeleteNetdiskSeasonByItemId(itemId string) error {
	return DeleteNetdiskSeasonByItemIdContext(context.Background(), itemId)
}

func DeleteNetdiskSeasonByItemIdContext(ctx context.Context, itemId string) error {
	return waitForVerifiedEmbyDeletion(ctx)
}

func DeleteNetdiskTvshowByItemId(itemId string) error {
	return DeleteNetdiskTvshowByItemIdContext(context.Background(), itemId)
}

func DeleteNetdiskTvshowByItemIdContext(ctx context.Context, itemId string) error {
	return waitForVerifiedEmbyDeletion(ctx)
}

// 仅有 item ID 的旧入口不能证明原 STRM 已删除。持久事件 worker 使用
// Capture/Build/Execute/FinalizeEmbyDeletionPlan，并提供独立存活核验。
func waitForVerifiedEmbyDeletion(ctx context.Context) error {
	release, err := syncscope.Acquire(ctx, syncscope.Scope{Global: true})
	if err != nil {
		return err
	}
	defer release()
	return ErrEmbyDeleteUnverified
}

func GetLastItemDateCreatedTimeByLibraryID(kind MediaServerType, libraryID string) (int64, error) {
	if err := kind.Validate(); err != nil {
		return 0, err
	}
	var lastItem EmbyMediaItem
	if err := db.Db.Where("media_server_type = ?", kind).Where("library_id = ?", libraryID).Order("item_id_int DESC").First(&lastItem).Error; err != nil {
		return 0, err
	}
	return lastItem.DateCreatedTime, nil
}

// GetAllEmbyLibraries 获取所有 Emby 媒体库
func GetAllEmbyLibraries(kind MediaServerType) ([]EmbyLibrary, error) {
	if err := kind.Validate(); err != nil {
		return nil, err
	}
	var libraries []EmbyLibrary
	err := db.Db.Where("media_server_type = ?", kind).Find(&libraries).Error
	return libraries, err
}

// CleanupAllEmbyLibraryData 清理所有 Emby 媒体库数据
func CleanupAllEmbyLibraryData(kind MediaServerType) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 清理 emby_library_sync_paths
	if err := tx.Exec("DELETE FROM emby_library_sync_paths WHERE media_server_type = ?", kind).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_sync_files
	if err := tx.Exec("DELETE FROM emby_media_sync_files WHERE media_server_type = ? AND emby_item_id IN (SELECT item_id FROM emby_media_items WHERE media_server_type = ?)", kind, kind).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_items
	if err := tx.Exec("DELETE FROM emby_media_items WHERE media_server_type = ?", kind).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_libraries
	if err := tx.Exec("DELETE FROM emby_libraries WHERE media_server_type = ?", kind).Error; err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// CleanupUnselectedEmbyLibraryData 清理未选中的媒体库数据
func CleanupUnselectedEmbyLibraryData(kind MediaServerType, selectedLibIds []string) error {
	if err := kind.Validate(); err != nil {
		return err
	}
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 获取未选中的媒体库 ID 列表
	var unselectedLibIds []string
	if err := tx.Where("media_server_type = ?", kind).Model(&EmbyLibrary{}).Where("library_id NOT IN ?", selectedLibIds).Pluck("library_id", &unselectedLibIds).Error; err != nil {
		tx.Rollback()
		return err
	}

	if len(unselectedLibIds) == 0 {
		tx.Rollback()
		return nil
	}

	// 清理 emby_library_sync_paths
	if err := tx.Where("media_server_type = ?", kind).Where("library_id IN ?", unselectedLibIds).Delete(&EmbyLibrarySyncPath{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_sync_files
	if err := tx.Exec("DELETE FROM emby_media_sync_files WHERE media_server_type = ? AND emby_item_id IN (SELECT item_id FROM emby_media_items WHERE media_server_type = ? AND library_id IN ?)", kind, kind, unselectedLibIds).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_items
	if err := tx.Where("media_server_type = ?", kind).Where("library_id IN ?", unselectedLibIds).Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_libraries
	if err := tx.Where("media_server_type = ?", kind).Where("library_id IN ?", unselectedLibIds).Delete(&EmbyLibrary{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}
