package models

import (
	"context"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/syncscope"

	"gorm.io/gorm"
)

// EmbyMediaItem 同步下来的 Emby 媒体项
type EmbyMediaItem struct {
	BaseModel
	ItemId            string `json:"item_id" gorm:"uniqueIndex:idx_emby_item_id"`
	ItemIdInt         int64  `json:"item_id_int" gorm:"index:idx_emby_item_id_int"`
	ServerId          string `json:"server_id" gorm:"index:idx_emby_server_id"`
	Name              string `json:"name"`
	Type              string `json:"type" gorm:"index:idx_emby_type"`
	ParentId          string `json:"parent_id" gorm:"index:idx_emby_parent_id"`
	SeriesId          string `json:"series_id" gorm:"index:idx_emby_series_id"`
	SeriesName        string `json:"series_name"`
	SeasonId          string `json:"season_id" gorm:"index:idx_emby_season_id"`
	SeasonName        string `json:"season_name"`
	LibraryId         string `json:"library_id" gorm:"index:idx_emby_library_id"`
	Path              string `json:"path"`
	PickCode          string `json:"pick_code" gorm:"index:idx_emby_pick_code"`
	MediaSourcePath   string `json:"media_source_path"`
	IndexNumber       int    `json:"index_number"`
	ParentIndexNumber int    `json:"parent_index_number"`
	ProductionYear    int    `json:"production_year"`
	PremiereDate      string `json:"premiere_date"`
	DateCreated       string `json:"date_created"`
	DateCreatedTime   int64  `json:"date_created_time" gorm:"index:idx_emby_date_created_time"`
	DateModified      string `json:"date_modified"`
	DateModifiedTime  int64  `json:"date_modified_time"`
	IsFolder          bool   `json:"is_folder"`
	LastSeenSyncRun   string `json:"last_seen_sync_run" gorm:"index;type:varchar(64)"`
	LastSeenAt        int64  `json:"last_seen_at" gorm:"index"`
	PartCount         int    `json:"part_count"`
	PartOfItemID      string `json:"part_of_item_id" gorm:"index"`
	VersionOfItemID   string `json:"version_of_item_id" gorm:"index"`
	SnapshotID        uint   `json:"snapshot_id" gorm:"index"`
	Generation        int64  `json:"generation"`
}

func (*EmbyMediaItem) TableName() string {
	return "emby_media_items"
}

// EmbyMediaSyncFile 关联表（多对多）
type EmbyMediaSyncFile struct {
	BaseModel
	SyncPathId uint   `json:"sync_path_id" gorm:"index:idx_emby_sync_path_id"`
	EmbyItemId uint   `json:"emby_item_id" gorm:"index:idx_emby_media_item_id"`
	SyncFileId uint   `json:"sync_file_id" gorm:"index:idx_emby_sync_file_id"`
	PickCode   string `json:"pick_code" gorm:"index:idx_emby_sf_pick_code"`
	SnapshotID uint   `json:"snapshot_id" gorm:"index"`
	SourceID   string `json:"source_id"`
}

func (*EmbyMediaSyncFile) TableName() string {
	return "emby_media_sync_files"
}

// EmbyLibrarySyncPath 媒体库与 SyncPath 关联（多对多允许重复库对应多个路径）
type EmbyLibrarySyncPath struct {
	BaseModel
	LibraryId   string `json:"library_id" gorm:"uniqueIndex:idx_lib_sync_path,priority:1"`
	SyncPathId  uint   `json:"sync_path_id" gorm:"uniqueIndex:idx_lib_sync_path,priority:2"`
	LibraryName string `json:"library_name"`
}

func (*EmbyLibrarySyncPath) TableName() string {
	return "emby_library_sync_paths"
}

// EmbyLibrary 媒体库基础表（LibraryId 改为 string 以兼容 Emby 返回的字符串 ID）
type EmbyLibrary struct {
	BaseModel
	Name       string `json:"name"`
	LibraryId  string `json:"library_id"`
	SyncPathId uint   `json:"sync_path_id"` // 媒体库对应的同步目录 ID，如果为 0 则表示没有关联同步目录
}

func (*EmbyLibrary) TableName() string {
	return "emby_libraries"
}

// UpsertEmbyLibraries 更新或创建媒体库记录
func UpsertEmbyLibraries(libs []embyclientrestgo.EmbyLibrary) error {
	for _, lib := range libs {
		existing := &EmbyLibrary{}
		err := db.Db.Where("library_id = ?", lib.ID).First(existing).Error
		switch {
		case err == nil:
			if existing.Name != lib.Name {
				existing.Name = lib.Name
				if uerr := db.Db.Save(existing).Error; uerr != nil {
					return uerr
				}
			}
		case err == gorm.ErrRecordNotFound:
			rec := &EmbyLibrary{Name: lib.Name, LibraryId: lib.ID}
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
func CleanupDeletedEmbyLibraries(activeLibraryIds []string) error {
	if len(activeLibraryIds) == 0 {
		return nil
	}

	// 级联清理关联的同步路径记录
	if err := db.Db.Where("library_id NOT IN ?", activeLibraryIds).Delete(&EmbyLibrarySyncPath{}).Error; err != nil {
		return err
	}

	// 清理已删除的媒体库记录
	return db.Db.Where("library_id NOT IN ?", activeLibraryIds).Delete(&EmbyLibrary{}).Error
}

// CreateOrUpdateEmbyMediaItem upsert by ItemId
func CreateOrUpdateEmbyMediaItem(item *EmbyMediaItem) error {
	return upsertEmbyMediaItem(db.Db, item)
}

func GetEmbyMediaItemsCount() (int64, error) {
	var total int64
	return total, db.Db.Model(&EmbyMediaItem{}).Count(&total).Error
}

func CleanupOrphanedEmbyMediaItems(validItemIds []string) error {
	if len(validItemIds) == 0 {
		return db.Db.Where("1 = 1").Delete(&EmbyMediaItem{}).Error
	}

	// 当 validItemIds 很多时，分批处理以避免 SQL 语句过长
	// 每批处理 1000 个 ID，这是一个安全的数量
	const batchSize = 1000

	if len(validItemIds) <= batchSize {
		// 数量不多，直接使用 IN 操作符
		return db.Db.Where("item_id NOT IN ?", validItemIds).Delete(&EmbyMediaItem{}).Error
	}

	// 数量很多，使用分批删除逻辑
	// 先获取所有 item_id，然后分批删除不在 validItemIds 中的记录
	validItemSet := make(map[string]bool)
	for _, itemId := range validItemIds {
		validItemSet[itemId] = true
	}

	// 获取数据库中所有 item_id，然后找出需要删除的
	var allItems []string
	if err := db.Db.Model(&EmbyMediaItem{}).Pluck("item_id", &allItems).Error; err != nil {
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
		if err := db.Db.Where("item_id IN ?", batch).Delete(&EmbyMediaItem{}).Error; err != nil {
			return err
		}
	}

	return nil
}

// CleanupStaleEmbyMediaItemsByLibrarySyncRun 按全量同步批次清理指定媒体库内未出现的旧条目。
func CleanupStaleEmbyMediaItemsByLibrarySyncRun(libraryID string, syncRunID string) error {
	if libraryID == "" || syncRunID == "" {
		return nil
	}

	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var staleItemIDs []int64
	if err := tx.Model(&EmbyMediaItem{}).
		Where("library_id = ? AND (last_seen_sync_run IS NULL OR last_seen_sync_run != ?)", libraryID, syncRunID).
		Pluck("item_id_int", &staleItemIDs).Error; err != nil {
		tx.Rollback()
		return err
	}
	if len(staleItemIDs) == 0 {
		tx.Rollback()
		return nil
	}

	if err := tx.Where("emby_item_id IN ?", staleItemIDs).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("library_id = ? AND (last_seen_sync_run IS NULL OR last_seen_sync_run != ?)", libraryID, syncRunID).
		Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// DeleteLocalEmbyItemByID 删除本地单个 Emby 条目索引及关联。
func DeleteLocalEmbyItemByID(itemID string) error {
	if itemID == "" {
		return nil
	}
	itemIDInt := helpers.StringToInt64(itemID)
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()
	if err := tx.Where("emby_item_id = ?", itemIDInt).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Where("item_id = ?", itemID).Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// DeleteLocalEmbyItemsBySeasonID 删除本地指定季下的 Emby 条目索引及关联。
func DeleteLocalEmbyItemsBySeasonID(seasonID string) error {
	return deleteLocalEmbyItemsByField("season_id", seasonID)
}

// DeleteLocalEmbyItemsBySeriesID 删除本地指定剧下的 Emby 条目索引及关联。
func DeleteLocalEmbyItemsBySeriesID(seriesID string) error {
	return deleteLocalEmbyItemsByField("series_id", seriesID)
}

func deleteLocalEmbyItemsByField(field string, value string) error {
	if value == "" {
		return nil
	}
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	var itemIDs []int64
	if err := tx.Model(&EmbyMediaItem{}).Where(field+" = ?", value).Pluck("item_id_int", &itemIDs).Error; err != nil {
		tx.Rollback()
		return err
	}
	if len(itemIDs) > 0 {
		if err := tx.Where("emby_item_id IN ?", itemIDs).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	if err := tx.Where(field+" = ?", value).Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

// CreateEmbyMediaSyncFile 创建关联（存在则跳过）
func CreateEmbyMediaSyncFile(embyItemId string, syncFileId uint, pickCode string, syncPathId uint) error {
	var count int64
	embyItemIdInt := helpers.StringToInt(embyItemId)
	if err := db.Db.Model(&EmbyMediaSyncFile{}).
		Where("emby_item_id = ? AND sync_file_id = ?", uint(embyItemIdInt), syncFileId).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	relation := &EmbyMediaSyncFile{EmbyItemId: uint(embyItemIdInt), SyncFileId: syncFileId, PickCode: pickCode, SyncPathId: syncPathId}
	return db.Db.Save(relation).Error
}

// CreateOrUpdateEmbyLibrarySyncPath 创建或更新关联（存在则跳过）
func CreateOrUpdateEmbyLibrarySyncPath(libraryId string, syncPathId uint, libraryName string) error {
	var count int64
	if err := db.Db.Model(&EmbyLibrarySyncPath{}).
		Where("library_id = ? AND sync_path_id = ?", libraryId, syncPathId).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	relation := &EmbyLibrarySyncPath{LibraryId: libraryId, SyncPathId: syncPathId, LibraryName: libraryName}
	return db.Db.Save(relation).Error
}

// DeleteEmbyLibrarySyncPathsBySyncPathID 按同步路径删除关联
func DeleteEmbyLibrarySyncPathsBySyncPathID(syncPathId uint) error {
	return db.Db.Where("sync_path_id = ?", syncPathId).Delete(&EmbyLibrarySyncPath{}).Error
}

// DeleteEmbyMediaSyncFilesBySyncFileID 按 SyncFile 删除关联
func DeleteEmbyMediaSyncFilesBySyncFileID(syncFileId uint) error {
	return db.Db.Where("sync_file_id = ?", syncFileId).Delete(&EmbyMediaSyncFile{}).Error
}

// DeleteEmbyMediaSyncFilesByPickCode 按 PickCode 删除关联
func DeleteEmbyMediaSyncFilesByPickCode(pickCode string) error {
	if pickCode == "" {
		return nil
	}
	return db.Db.Where("pick_code = ?", pickCode).Delete(&EmbyMediaSyncFile{}).Error
}

// UpdateLastSyncTime 更新最后同步时间戳
func UpdateLastSyncTime() error {
	config := &EmbyConfig{}
	if err := db.Db.First(config).Error; err != nil {
		return err
	}
	if err := db.Db.Model(config).Update("last_sync_time", helpers.NowUnix()).Error; err != nil {
		return err
	}
	_, err := GetEmbyConfigFromDB()
	return err
}

// 使用 SyncPath 查询关联的 Emby LibraryId -> LibraryName 列表
func GetEmbyLibraryIdsBySyncPathId(syncPathId uint) map[string]string {
	var relations []EmbyLibrarySyncPath
	if err := db.Db.Where("sync_path_id = ?", syncPathId).Find(&relations).Error; err != nil {
		return nil
	}
	var libraryIds map[string]string = make(map[string]string)
	for _, rel := range relations {
		libraryIds[rel.LibraryId] = rel.LibraryName
	}
	return libraryIds
}

// 通过 SyncPathId 刷新 Emby 媒体库
func RefreshEmbyLibraryBySyncPathId(syncPathId uint) error {
	if GlobalEmbyConfig == nil || GlobalEmbyConfig.EmbyUrl == "" || GlobalEmbyConfig.EmbyApiKey == "" || GlobalEmbyConfig.EnableRefreshLibrary == 0 {
		helpers.AppLogger.Infof("Emby 未配置或未启用刷新媒体库，跳过刷新")
		return nil
	}
	// 创建一个新的 Emby 客户端
	client := embyclientrestgo.NewClient(GlobalEmbyConfig.EmbyUrl, GlobalEmbyConfig.EmbyApiKey)
	libraryIds := GetEmbyLibraryIdsBySyncPathId(syncPathId)
	for libId, libName := range libraryIds {
		if err := client.RefreshLibrary(libId, libName); err != nil {
			return err
		}
	}
	return nil
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

func GetLastItemDateCreatedTimeByLibraryID(libraryID string) int64 {
	var lastItem EmbyMediaItem
	if err := db.Db.Where("library_id = ?", libraryID).Order("item_id_int DESC").First(&lastItem).Error; err != nil {
		helpers.AppLogger.Errorf("查询媒体库 %s 最后一个项目失败：%v", libraryID, err)
	}
	helpers.AppLogger.Infof("查询媒体库 %s 最后一个项目成功：%d => %d", libraryID, lastItem.ItemIdInt, lastItem.DateCreatedTime)
	return lastItem.DateCreatedTime
}

// GetAllEmbyLibraries 获取所有 Emby 媒体库
func GetAllEmbyLibraries() ([]EmbyLibrary, error) {
	var libraries []EmbyLibrary
	err := db.Db.Find(&libraries).Error
	return libraries, err
}

// CleanupAllEmbyLibraryData 清理所有 Emby 媒体库数据
func CleanupAllEmbyLibraryData() error {
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 清理 emby_library_sync_paths
	if err := tx.Exec("DELETE FROM emby_library_sync_paths").Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_sync_files
	if err := tx.Exec("DELETE FROM emby_media_sync_files WHERE emby_item_id IN (SELECT item_id_int FROM emby_media_items)").Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_items
	if err := tx.Exec("DELETE FROM emby_media_items").Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_libraries
	if err := tx.Exec("DELETE FROM emby_libraries").Error; err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}

// CleanupUnselectedEmbyLibraryData 清理未选中的媒体库数据
func CleanupUnselectedEmbyLibraryData(selectedLibIds []string) error {
	tx := db.Db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// 获取未选中的媒体库 ID 列表
	var unselectedLibIds []string
	if err := tx.Model(&EmbyLibrary{}).Where("library_id NOT IN ?", selectedLibIds).Pluck("library_id", &unselectedLibIds).Error; err != nil {
		tx.Rollback()
		return err
	}

	if len(unselectedLibIds) == 0 {
		tx.Rollback()
		return nil
	}

	// 清理 emby_library_sync_paths
	if err := tx.Where("library_id IN ?", unselectedLibIds).Delete(&EmbyLibrarySyncPath{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_sync_files
	if err := tx.Exec("DELETE FROM emby_media_sync_files WHERE emby_item_id IN (SELECT item_id_int FROM emby_media_items WHERE library_id IN ?)", unselectedLibIds).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_media_items
	if err := tx.Where("library_id IN ?", unselectedLibIds).Delete(&EmbyMediaItem{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	// 清理 emby_libraries
	if err := tx.Where("library_id IN ?", unselectedLibIds).Delete(&EmbyLibrary{}).Error; err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit().Error
}
