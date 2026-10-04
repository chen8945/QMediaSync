package models

import (
	"context"
	"path/filepath"
	"strings"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"
	"qmediasync/internal/openlist"
	"qmediasync/internal/syncscope"
	"qmediasync/internal/v115open"

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

// 联动删除网盘的电影
func DeleteNetdiskMovieByEmbyItemId(itemId string) error {
	return DeleteNetdiskMovieByEmbyItemIdContext(context.Background(), itemId)
}

// DeleteNetdiskMovieByEmbyItemIdContext 等待相关文件处理结束后执行 Emby 联动删除。
func DeleteNetdiskMovieByEmbyItemIdContext(ctx context.Context, itemId string) error {
	// ponytail: 现有查询会跨账号读取同名目录，先等待全部同步；限定查询账号后可缩小范围。
	release, err := syncscope.Acquire(ctx, syncscope.Scope{Global: true})
	if err != nil {
		return err
	}
	defer release()
	return deleteNetdiskMovieByEmbyItemId(itemId)
}

func deleteNetdiskMovieByEmbyItemId(itemId string) error {
	itemIdUint := uint(helpers.StringToInt(itemId))
	embyItem := &EmbyMediaSyncFile{}
	if err := db.Db.Where("emby_item_id = ?", itemIdUint).First(embyItem).Error; err != nil {
		helpers.AppLogger.Errorf("Emby Item %s 没有关联的网盘文件", itemId)
		return err
	}
	syncFile := SyncFile{}
	if err := db.Db.Where("id = ?", embyItem.SyncFileId).Find(&syncFile).Error; err != nil {
		helpers.AppLogger.Errorf("查询 Emby Item %s 关联的网盘文件 %d 失败：%v", itemId, embyItem.SyncFileId, err)
		return err
	}
	// 查找 syncFile.Path 下是否只有一个视频文件
	files := []SyncFile{}
	if err := db.Db.Where("parent_id = ?", syncFile.ParentId).Find(&files).Error; err != nil {
		helpers.AppLogger.Errorf("查询网盘路径 %s 下的文件失败：%v", syncFile.Path, err)
		return err
	}
	helpers.AppLogger.Infof("准备删除 Emby Item %s 关联的网盘文件 %s", itemId, syncFile.Path+"/"+syncFile.FileName)
	// 检查是否只有一个视频文件
	videoFileCount := 0
	// 顺便遍历出视频文件对应的元数据文件，以视频文件 basename 开头的元数据文件
	ext := filepath.Ext(syncFile.FileName)
	baseName := strings.TrimSuffix(syncFile.FileName, ext)
	metaFiles := []SyncFile{}
	for _, f := range files {
		if f.IsVideo {
			videoFileCount++
		}
		if f.IsMeta && strings.HasPrefix(f.FileName, baseName) {
			// 记录文件
			metaFiles = append(metaFiles, f)
		}
	}
	// 调用网盘接口删除文件
	account, err := GetAccountById(syncFile.AccountId)
	if err != nil {
		helpers.AppLogger.Errorf("获取网盘账号 %d 失败：%v", syncFile.AccountId, err)
		return err
	}
	success := false
	delErr := error(nil)
	switch syncFile.SourceType {
	case SourceType115:
		// 执行 115 网盘删除逻辑
		client := account.Get115Client()
		if videoFileCount == 1 {
			// 删除目录
			success, delErr = delete115Folders(client, syncFile.Path, syncFile.ParentId, syncFile.SyncPathId, itemId)
		} else {
			// 删除视频文件+元数据
			success, delErr = delete115Files(client, syncFile, metaFiles)
		}
	case SourceTypeOpenList:
		// 执行 OpenList 网盘删除逻辑
		client := account.GetOpenListClient()
		if videoFileCount == 1 {
			// 删除目录
			success, delErr = deleteOpenListFolders(client, syncFile.Path)
		} else {
			// 删除视频文件+元数据
			success, delErr = deleteOpenListFiles(client, syncFile, metaFiles)
		}
	case SourceTypeBaiduPan:
		// 执行百度网盘删除逻辑
		client := account.GetBaiDuPanClient()
		if videoFileCount == 1 {
			// 删除目录
			success, delErr = deleteBaiduPanFolders(client, syncFile.Path)
		} else {
			// 删除视频文件+元数据
			success, delErr = deleteBaiduPanFiles(client, syncFile, metaFiles)
		}
	}
	if delErr != nil {
		helpers.AppLogger.Errorf("删除 Emby Item %s 关联的网盘视频文件+元数据失败：%v", itemId, delErr)
		return delErr
	}
	if success {
		helpers.AppLogger.Infof("删除 Emby Item %s 关联的网盘视频文件+元数据成功：%v", itemId, success)
		if err := db.Db.Where("emby_item_id = ?", itemIdUint).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			helpers.AppLogger.Errorf("删除 Emby Item %s 关联的 EmbyMediaSyncFile 记录失败：%v", itemId, err)
			return err
		}
		if err := db.Db.Where("item_id = ?", itemId).Delete(&EmbyMediaItem{}).Error; err != nil {
			helpers.AppLogger.Errorf("删除 Emby Item %s 关联的 EmbyMediaItem 记录失败：%v", itemId, err)
			return err
		}
	}
	return nil
}

// 联动删除网盘的集
func DeleteNetdiskEpisodeByEmbyItemId(itemId string) error {
	return DeleteNetdiskEpisodeByEmbyItemIdContext(context.Background(), itemId)
}

// DeleteNetdiskEpisodeByEmbyItemIdContext 等待相关文件处理结束后执行 Emby 联动删除。
func DeleteNetdiskEpisodeByEmbyItemIdContext(ctx context.Context, itemId string) error {
	// ponytail: 现有查询会跨账号读取同名目录，先等待全部同步；限定查询账号后可缩小范围。
	release, err := syncscope.Acquire(ctx, syncscope.Scope{Global: true})
	if err != nil {
		return err
	}
	defer release()
	return deleteNetdiskEpisodeByEmbyItemId(itemId)
}

func deleteNetdiskEpisodeByEmbyItemId(itemId string) error {
	itemIdUint := uint(helpers.StringToInt(itemId))
	embyItem := &EmbyMediaSyncFile{}
	if err := db.Db.Where("emby_item_id = ?", itemIdUint).First(embyItem).Error; err != nil {
		helpers.AppLogger.Errorf("Emby Item %s 没有关联的网盘文件", itemId)
		return err
	}
	syncFile := SyncFile{}
	if err := db.Db.Where("id = ?", embyItem.SyncFileId).Find(&syncFile).Error; err != nil {
		helpers.AppLogger.Errorf("查询 Emby Item %s 关联的网盘文件 %d 失败：%v", itemId, embyItem.SyncFileId, err)
		return err
	}
	files := []SyncFile{}
	if err := db.Db.Where("path = ?", syncFile.Path).Find(&files).Error; err != nil {
		helpers.AppLogger.Errorf("查询网盘路径 %s 下的文件失败：%v", syncFile.Path, err)
		return err
	}
	helpers.AppLogger.Infof("准备删除 Emby Item %s 关联的网盘文件 %s", itemId, syncFile.Path+"/"+syncFile.FileName)
	// 顺便遍历出视频文件对应的元数据文件，以视频文件 basename 开头的元数据文件
	ext := filepath.Ext(syncFile.FileName)
	baseName := strings.TrimSuffix(syncFile.FileName, ext)
	filesToDelete := make([]SyncFile, 0)
	for _, f := range files {
		if f.IsMeta && strings.HasPrefix(f.FileName, baseName) {
			// 记录文件
			filesToDelete = append(filesToDelete, f)
		}
	}
	// 调用网盘接口删除文件
	account, err := GetAccountById(syncFile.AccountId)
	if err != nil {
		helpers.AppLogger.Errorf("获取网盘账号 %d 失败：%v", syncFile.AccountId, err)
		return err
	}
	success := false
	delErr := error(nil)
	switch syncFile.SourceType {
	case SourceType115:
		// 执行 115 网盘删除逻辑
		client := account.Get115Client()
		success, delErr = delete115Files(client, syncFile, filesToDelete)
	case SourceTypeOpenList:
		// 执行 OpenList 网盘删除逻辑
		client := account.GetOpenListClient()
		success, delErr = deleteOpenListFiles(client, syncFile, filesToDelete)
	case SourceTypeBaiduPan:
		// 执行百度网盘删除逻辑
		client := account.GetBaiDuPanClient()
		success, delErr = deleteBaiduPanFiles(client, syncFile, filesToDelete)
	}
	if delErr != nil {
		helpers.AppLogger.Errorf("删除 Emby Item %s 关联的网盘集视频文件+元数据失败：%v", itemId, delErr)
		return delErr
	}
	helpers.AppLogger.Infof("删除 Emby Item %s 关联的网盘集视频文件+元数据成功：%v", itemId, success)
	// 删除 EmbyMediaSyncFile 数据
	// 删除 EmbyMediaItem 数据
	if success {
		if err := db.Db.Where("emby_item_id = ?", itemIdUint).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			helpers.AppLogger.Errorf("删除 Emby Item %s 关联的 EmbyMediaSyncFile 记录失败：%v", itemId, err)
			return err
		}
		if err := db.Db.Where("item_id = ?", itemId).Delete(&EmbyMediaItem{}).Error; err != nil {
			helpers.AppLogger.Errorf("删除 Emby Item %s 关联的 EmbyMediaItem 记录失败：%v", itemId, err)
			return err
		}
	}
	return nil
}

// 联动删除网盘的季
func DeleteNetdiskSeasonByItemId(itemId string) error {
	return DeleteNetdiskSeasonByItemIdContext(context.Background(), itemId)
}

// DeleteNetdiskSeasonByItemIdContext 等待相关文件处理结束后执行 Emby 联动删除。
func DeleteNetdiskSeasonByItemIdContext(ctx context.Context, itemId string) error {
	// ponytail: 现有查询会跨账号读取同名目录，先等待全部同步；限定查询账号后可缩小范围。
	release, err := syncscope.Acquire(ctx, syncscope.Scope{Global: true})
	if err != nil {
		return err
	}
	defer release()
	return deleteNetdiskSeasonByItemId(itemId)
}

func deleteNetdiskSeasonByItemId(itemId string) error {
	// 根据 itemId 先查找所有 EmbyMediaItem 记录
	var embyItems []EmbyMediaItem
	if err := db.Db.Where("season_id = ?", itemId).Find(&embyItems).Error; err != nil {
		helpers.AppLogger.Errorf("查询 SeasonId %s 关联的 EmbyMediaItem 记录失败：%v", itemId, err)
		return err
	}
	// 拿到所有关联的 SyncFileId
	syncFileIds := []uint{}
	for _, embyItem := range embyItems {
		var embyMediaSyncFiles []EmbyMediaSyncFile
		if err := db.Db.Where("emby_item_id = ?", embyItem.ID).Find(&embyMediaSyncFiles).Error; err != nil {
			helpers.AppLogger.Errorf("查询 Emby Item %s 关联的 EmbyMediaSyncFile 记录失败：%v", embyItem.ItemId, err)
			continue
		}
		for _, rel := range embyMediaSyncFiles {
			syncFileIds = append(syncFileIds, rel.SyncFileId)
		}
	}
	// 取第一个 SyncFileId 对应的 SyncFile.Path 作为季目录来处理
	if len(syncFileIds) == 0 {
		helpers.AppLogger.Infof("SeasonId %s 没有关联的网盘文件", itemId)
		return nil
	}
	syncFile := SyncFile{}
	if err := db.Db.Where("id = ?", syncFileIds[0]).Find(&syncFile).Error; err != nil {
		helpers.AppLogger.Errorf("查询 SeasonId %s 关联的网盘文件 %d 失败：%v", itemId, syncFileIds[0], err)
		return err
	}
	seasonPath := syncFile.Path
	// 检查季目录是否为单独的目录
	seasonNumber := helpers.ExtractSeasonsFromSeasonPath(filepath.Base(seasonPath))
	if seasonNumber >= 0 {
		// 是单独的季目录，删除整个目录
		// 调用 115 接口删除文件
		account, err := GetAccountById(syncFile.AccountId)
		if err != nil {
			helpers.AppLogger.Errorf("获取网盘账号 %d 失败：%v", syncFile.AccountId, err)
			return err
		}
		var delErr error
		switch syncFile.SourceType {
		case SourceType115:
			client := account.Get115Client()
			_, delErr = delete115Folders(client, seasonPath, syncFile.ParentId, syncFile.SyncPathId, itemId)
		case SourceTypeOpenList:
			client := account.GetOpenListClient()
			_, delErr = deleteOpenListFolders(client, seasonPath)
		case SourceTypeBaiduPan:
			client := account.GetBaiDuPanClient()
			_, delErr = deleteBaiduPanFolders(client, seasonPath)
		}
		if delErr != nil {
			helpers.AppLogger.Errorf("删除 Emby Item %s 关联的网盘电视剧季目录 %s 失败：%v", itemId, seasonPath, delErr)
			return delErr
		}
		helpers.AppLogger.Infof("删除 Emby Item %s 关联的网盘电视剧 季目录 %s 成功", itemId, seasonPath)
	} else {
		// 不是单独的季目录，仅删除季下所有集对应的视频文件和元数据（NFO、封面）
		for _, embyItem := range embyItems {
			if err := deleteNetdiskEpisodeByEmbyItemId(embyItem.ItemId); err != nil {
				continue
			}
		}
		helpers.AppLogger.Infof("删除 Emby Item %s 关联的网盘电视剧 季下的所有集成功", itemId)
	}
	// 删除 EmbyMediaItem 数据
	if err := db.Db.Where("season_id = ?", itemId).Delete(&EmbyMediaItem{}).Error; err != nil {
		helpers.AppLogger.Errorf("删除 SeasonId %s 关联的 EmbyMediaItem 记录失败：%v", itemId, err)
		return err
	}
	// 删除 EmbyMediaSyncFile 数据
	for _, syncFileId := range syncFileIds {
		if err := db.Db.Where("sync_file_id = ?", syncFileId).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			helpers.AppLogger.Errorf("删除 SeasonId %s 关联的 EmbyMediaSyncFile 记录失败：%v", itemId, err)
			return err
		}
	}
	return nil
}

// 联动删除网盘的剧
func DeleteNetdiskTvshowByItemId(itemId string) error {
	return DeleteNetdiskTvshowByItemIdContext(context.Background(), itemId)
}

// DeleteNetdiskTvshowByItemIdContext 等待相关文件处理结束后执行 Emby 联动删除。
func DeleteNetdiskTvshowByItemIdContext(ctx context.Context, itemId string) error {
	// ponytail: 现有查询会跨账号读取同名目录，先等待全部同步；限定查询账号后可缩小范围。
	release, err := syncscope.Acquire(ctx, syncscope.Scope{Global: true})
	if err != nil {
		return err
	}
	defer release()
	return deleteNetdiskTvshowByItemId(itemId)
}

func deleteNetdiskTvshowByItemId(itemId string) error {
	// 根据 itemId 先查找所有 EmbyMediaItem 记录
	var embyItems []EmbyMediaItem
	if err := db.Db.Where("series_id = ?", itemId).Find(&embyItems).Error; err != nil {
		helpers.AppLogger.Errorf("查询 SeriesId %s 关联的 EmbyMediaItem 记录失败：%v", itemId, err)
		return err
	}
	// 拿到所有关联的 SyncFileId
	syncFileIds := []uint{}
	for _, embyItem := range embyItems {
		var embyMediaSyncFiles []EmbyMediaSyncFile
		if err := db.Db.Where("emby_item_id = ?", embyItem.ItemId).Find(&embyMediaSyncFiles).Error; err != nil {
			helpers.AppLogger.Errorf("查询 Emby Item %s 关联的 EmbyMediaSyncFile 记录失败：%v", embyItem.ItemId, err)
			continue
		}
		for _, rel := range embyMediaSyncFiles {
			syncFileIds = append(syncFileIds, rel.SyncFileId)
		}
	}
	// 取第一个 SyncFileId 对应的 SyncFile.Path 作为剧目录来处理
	if len(syncFileIds) == 0 {
		helpers.AppLogger.Infof("SeriesId %s 没有关联的网盘文件", itemId)
		return nil
	}
	syncFile := SyncFile{}
	if err := db.Db.Where("id = ?", syncFileIds[0]).Find(&syncFile).Error; err != nil {
		helpers.AppLogger.Errorf("查询 SeriesId %s 关联的网盘文件 %d 失败：%v", itemId, syncFileIds[0], err)
		return err
	}
	// 检查目录是否为季目录
	seasonNumber := helpers.ExtractSeasonsFromSeasonPath(filepath.Base(syncFile.Path))
	tvshowPath := ""
	tvshowPathId := ""
	if seasonNumber >= 0 {
		// 是季目录，取父目录作为剧目录来删除
		tvshowPath = filepath.ToSlash(filepath.Dir(syncFile.Path))
		tvshowSyncFile := SyncFile{}
		if err := db.Db.Where("path = ?", tvshowPath).Find(&tvshowSyncFile).Error; err != nil {
			helpers.AppLogger.Errorf("查询剧目录 %s 关联的网盘文件 ID 失败：%v", tvshowPath, err)
			return err
		}
		tvshowPathId = tvshowSyncFile.ParentId
	} else {
		// 不是季目录，直接使用当前目录
		tvshowPath = syncFile.Path
		tvshowPathId = syncFile.ParentId
	}
	// 调用 115 接口删除文件
	account, err := GetAccountById(syncFile.AccountId)
	if err != nil {
		helpers.AppLogger.Errorf("获取网盘账号 %d 失败：%v", syncFile.AccountId, err)
		return err
	}
	var delErr error
	switch syncFile.SourceType {
	case SourceType115:
		client := account.Get115Client()
		_, delErr = delete115Folders(client, tvshowPath, tvshowPathId, syncFile.SyncPathId, itemId)
	case SourceTypeOpenList:
		client := account.GetOpenListClient()
		_, delErr = deleteOpenListFolders(client, tvshowPath)
	case SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		_, delErr = deleteBaiduPanFolders(client, tvshowPath)
	}
	if delErr != nil {
		helpers.AppLogger.Errorf("删除 Emby Item %s 关联的网盘电视剧目录 %s => %s 失败：%v", itemId, tvshowPathId, tvshowPath, delErr)
		return delErr
	}
	helpers.AppLogger.Infof("删除 Emby Item %s 关联的网盘电视剧目录 %s => %s 成功", itemId, tvshowPathId, tvshowPath)
	// 删除 EmbyMediaItem 数据
	if err := db.Db.Where("series_id = ?", itemId).Delete(&EmbyMediaItem{}).Error; err != nil {
		helpers.AppLogger.Errorf("删除 SeriesId %s 关联的 EmbyMediaItem 记录失败：%v", itemId, err)
		return err
	}
	// 删除 EmbyMediaSyncFile 数据
	for _, syncFileId := range syncFileIds {
		if err := db.Db.Where("sync_file_id = ?", syncFileId).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			helpers.AppLogger.Errorf("删除 SeriesId %s 关联的 EmbyMediaSyncFile 记录失败：%v", itemId, err)
			return err
		}
	}
	return nil
}

// 删除 115 文件（视频 + 元数据），增加详细调试日志
func delete115Files(client *v115open.OpenClient, syncFile SyncFile, metaFiles []SyncFile) (bool, error) {
	// 记录主视频文件信息
	helpers.AppLogger.Infof("[DEBUG-DELETE] 准备删除主视频文件 - 路径：%s，文件名：%s，File ID：%s，Parent ID：%s",
		syncFile.Path, syncFile.FileName, syncFile.FileId, syncFile.ParentId)

	// 记录元数据文件信息
	for i, mf := range metaFiles {
		helpers.AppLogger.Infof("[DEBUG-DELETE] 准备删除元数据文件 [%d] - 路径：%s，文件名：%s，File ID：%s",
			i+1, mf.Path, mf.FileName, mf.FileId)
	}

	// 收集所有要删除的 File ID
	fileIdsToDelete := []string{syncFile.FileId}
	for _, mf := range metaFiles {
		fileIdsToDelete = append(fileIdsToDelete, mf.FileId)
	}

	// 记录总删除文件数和 File ID 列表
	helpers.AppLogger.Infof("[DEBUG-DELETE] 总共准备删除 %d 个文件，File ID 列表：%v",
		len(fileIdsToDelete), fileIdsToDelete)

	// 调用 115 删除 API
	success, delErr := client.Del(context.Background(), fileIdsToDelete, syncFile.ParentId)

	// 记录删除结果
	helpers.AppLogger.Infof("[DEBUG-DELETE] 115 网盘文件删除%s - 成功：%v，File ID 列表：%v",
		map[bool]string{true: "成功", false: "失败"}[success], success, fileIdsToDelete)

	if delErr != nil {
		helpers.AppLogger.Errorf("[DEBUG-DELETE] 115 网盘文件删除失败 - 错误：%v", delErr)
	}

	return success, delErr
}

// 删除 115 文件夹，增加详细调试日志
func delete115Folders(client *v115open.OpenClient, delPath string, delPathId string, syncPathId uint, itemId string) (bool, error) {
	// 记录基本信息
	helpers.AppLogger.Infof("[DEBUG-DELETE] 准备删除目录 - Emby Item ID：%s，删除路径：%s，SyncPath ID：%d",
		itemId, delPath, syncPathId)

	// 删除整个目录
	if delPath == "" || delPath == "." || delPath == "/" {
		// 到了根目录，不能删除
		helpers.AppLogger.Errorf("[DEBUG-DELETE] 删除网盘目录失败 - 已到达根目录 %s", delPath)
		return false, nil
	}

	pathParent := filepath.ToSlash(filepath.Dir(delPath))
	pathParentId := ""
	pathParentStr := ""

	if pathParent == "" || pathParent == "." || pathParent == "/" {
		// 到了根目录，取 SyncPath.SourcePathId
		helpers.AppLogger.Infof("[DEBUG-DELETE] 已到达根目录，使用 SyncPath 的 BaseCid - SyncPath ID：%d", syncPathId)

		syncPath := GetSyncPathById(syncPathId)
		if syncPath == nil {
			helpers.AppLogger.Errorf("[DEBUG-DELETE] 查询 SyncPath %d 失败", syncPathId)
			return false, nil
		}

		pathParentId = syncPath.BaseCid
		pathParentStr = syncPath.RemotePath

		helpers.AppLogger.Infof("[DEBUG-DELETE] 使用 SyncPath 信息 - Parent ID：%s，Parent Path：%s",
			pathParentId, pathParentStr)
	} else {
		// 查询 pathParent 的 file_id
		helpers.AppLogger.Infof("[DEBUG-DELETE] 查询父目录 - 父路径：%s", pathParent)

		parentPath := SyncFile{}
		if err := db.Db.Where("path = ?", pathParent).First(&parentPath).Error; err != nil {
			helpers.AppLogger.Errorf("[DEBUG-DELETE] 查询电影文件夹的父路径 %s 失败：%v", pathParent, err)
			return false, nil
		}

		pathParentId = parentPath.FileId
		pathParentStr = parentPath.Path

		helpers.AppLogger.Infof("[DEBUG-DELETE] 父目录查询成功 - Parent ID：%s，Parent Path：%s",
			pathParentId, pathParentStr)
	}

	// 调用 115 删除 API
	success, delErr := client.Del(context.Background(), []string{delPathId}, pathParentId)

	// 记录删除结果
	if delErr != nil {
		helpers.AppLogger.Errorf("[DEBUG-DELETE] 115 网盘目录删除失败 - File ID：%s，Parent ID：%s，错误：%v",
			delPathId, pathParentId, delErr)
		return success, delErr
	}

	helpers.AppLogger.Infof("[DEBUG-DELETE] 115 网盘目录删除成功 - File ID：%s，Parent ID：%s，Parent Path：%s，Emby Item ID：%s",
		delPathId, pathParentId, pathParentStr, itemId)

	helpers.AppLogger.Infof("删除 Emby Item %s 关联的网盘电影目录 %s=>%s 成功", itemId, delPathId, delPath)

	return success, delErr
}

func deleteOpenListFiles(client *openlist.Client, syncFile SyncFile, metaFiles []SyncFile) (bool, error) {
	fileNameToDelete := []string{syncFile.FileName}
	for _, mf := range metaFiles {
		fileNameToDelete = append(fileNameToDelete, mf.FileName)
	}
	err := client.Del(syncFile.Path, fileNameToDelete)
	if err != nil {
		return false, err
	}
	return true, nil
}

func deleteOpenListFolders(client *openlist.Client, path string) (bool, error) {
	pathParent := filepath.Dir(path)
	if path == "" || path == "." || path == "/" {
		// 到了根目录，不能删除
		helpers.AppLogger.Errorf("删除网盘目录失败：已到达根目录 %s", path)
		return false, nil
	}
	folerName := filepath.Base(path)
	err := client.Del(pathParent, []string{folerName})
	if err != nil {
		return false, err
	}
	return true, nil
}

func deleteBaiduPanFolders(client *baidupan.Client, path string) (bool, error) {
	if path == "" || path == "." || path == "/" {
		// 到了根目录，不能删除
		helpers.AppLogger.Errorf("删除网盘目录失败：已到达根目录 %s", path)
		return false, nil
	}
	err := client.Del(context.Background(), []string{path})
	if err != nil {
		return false, err
	}
	return true, nil
}

func deleteBaiduPanFiles(client *baidupan.Client, syncFile SyncFile, metaFiles []SyncFile) (bool, error) {
	fileNameToDelete := []string{syncFile.FileName}
	for _, mf := range metaFiles {
		fileNameToDelete = append(fileNameToDelete, filepath.ToSlash(filepath.Join(mf.Path, mf.FileName)))
	}
	err := client.Del(context.Background(), fileNameToDelete)
	if err != nil {
		return false, err
	}
	return true, nil
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
	if err := tx.Exec("DELETE FROM emby_media_sync_files WHERE emby_item_id IN (SELECT id FROM emby_media_items)").Error; err != nil {
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
	if err := tx.Exec("DELETE FROM emby_media_sync_files WHERE emby_item_id IN (SELECT id FROM emby_media_items WHERE library_id IN ?)", unselectedLibIds).Error; err != nil {
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
