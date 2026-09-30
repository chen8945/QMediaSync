package models

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	embyclientrestgo "qmediasync/internal/embyclient-rest-go"
	"qmediasync/internal/helpers"

	"gorm.io/gorm"
)

// EmbyRefreshTargetType 是 Emby 刷新目标类型。
type EmbyRefreshTargetType string

const (
	EmbyRefreshTargetTypeLibrary EmbyRefreshTargetType = EmbyLibraryRefreshTargetTypeLibrary
	EmbyRefreshTargetTypeItem    EmbyRefreshTargetType = EmbyLibraryRefreshTargetTypeItem
)

const embyRemoteLibraryResolutionCacheTTL = 30 * time.Second

type embyRemoteLibraryResolutionCacheEntry struct {
	resolution EmbyLibraryResolution
	expiresAt  time.Time
}

var embyRemoteLibraryResolutionCache = struct {
	sync.Mutex
	entries  map[string]embyRemoteLibraryResolutionCacheEntry
	inFlight map[string]chan struct{}
}{
	entries:  make(map[string]embyRemoteLibraryResolutionCacheEntry),
	inFlight: make(map[string]chan struct{}),
}

// EmbyLibraryResolutionSource 表示媒体库归属证据来源。
type EmbyLibraryResolutionSource string

const (
	EmbyLibraryResolutionSourceItem           EmbyLibraryResolutionSource = "item"
	EmbyLibraryResolutionSourceSiblingEpisode EmbyLibraryResolutionSource = "sibling_episode"
	EmbyLibraryResolutionSourceAncestors      EmbyLibraryResolutionSource = "ancestors"
	EmbyLibraryResolutionSourceSyncPath       EmbyLibraryResolutionSource = "sync_path"
	EmbyLibraryResolutionSourceFallbackHint   EmbyLibraryResolutionSource = "fallback_hint"
)

// EmbyLibraryResolution 描述 Emby item 的有效媒体库归属。
type EmbyLibraryResolution struct {
	LibraryID   string
	LibraryName string
	Source      EmbyLibraryResolutionSource
	Resolved    bool
	Candidates  []string
}

// EmbyRefreshTarget 描述一次 STRM 变更应刷新的 Emby 目标。
type EmbyRefreshTarget struct {
	TargetType          EmbyRefreshTargetType
	ItemID              string
	ItemName            string
	ItemType            string
	Recursive           bool
	SyncPathID          uint
	FallbackLibraryId   string
	FallbackLibraryName string
}

// ResolveEmbyRefreshTarget 解析 SyncFile 对应的 Emby 刷新目标。
func ResolveEmbyRefreshTarget(syncFile *SyncFile) (EmbyRefreshTarget, error) {
	if syncFile == nil {
		return EmbyRefreshTarget{TargetType: EmbyRefreshTargetTypeLibrary}, nil
	}
	withFallback := func(target EmbyRefreshTarget) EmbyRefreshTarget {
		target.SyncPathID = syncFile.SyncPathId
		resolution, err := resolveEmbyTargetLibraryWithDB(db.Db, target, []uint{syncFile.SyncPathId})
		if err == nil && resolution.Resolved {
			target.FallbackLibraryId = resolution.LibraryID
			target.FallbackLibraryName = resolution.LibraryName
		}
		return target
	}

	if item, err := findEmbyItemBySyncFile(syncFile); err != nil {
		return EmbyRefreshTarget{}, err
	} else if item != nil {
		return withFallback(targetFromEmbyMediaItem(item)), nil
	}

	if target, ok, err := resolveSiblingSeasonOrSeries(syncFile); err != nil {
		return EmbyRefreshTarget{}, err
	} else if ok {
		return resolveEmbyRefreshTargetWithSiblingFallback(target, syncFile.SyncPathId), nil
	}

	if item, err := findEmbyItemByPath(syncFile); err != nil {
		helpers.AppLogger.Warnf("按路径兜底查询 Emby 条目失败，将回退媒体库刷新：%v", err)
	} else if item != nil {
		target := targetFromEmbyPathItem(item)
		localResolution, localErr := resolveEmbyTargetLibraryWithDB(db.Db, target, []uint{syncFile.SyncPathId})
		if localErr == nil && localResolution.Resolved &&
			(localResolution.Source == EmbyLibraryResolutionSourceItem || localResolution.Source == EmbyLibraryResolutionSourceSiblingEpisode) {
			target.FallbackLibraryId = localResolution.LibraryID
			target.FallbackLibraryName = localResolution.LibraryName
			return target, nil
		}
		if resolution := resolveEmbyTargetLibraryRemote(target); resolution.Resolved {
			target.FallbackLibraryId = resolution.LibraryID
			target.FallbackLibraryName = resolution.LibraryName
			return target, nil
		}
		if localErr == nil && localResolution.Resolved {
			target.FallbackLibraryId = localResolution.LibraryID
			target.FallbackLibraryName = localResolution.LibraryName
		}
		return target, nil
	}

	return withFallback(EmbyRefreshTarget{TargetType: EmbyRefreshTargetTypeLibrary}), nil
}

func findEmbyItemBySyncFile(syncFile *SyncFile) (*EmbyMediaItem, error) {
	if syncFile == nil {
		return nil, nil
	}
	query := db.Db.Where("sync_file_id = ?", syncFile.ID)
	if syncFile.PickCode != "" {
		query = db.Db.Where("sync_file_id = ? OR pick_code = ?", syncFile.ID, syncFile.PickCode)
	}
	var relation EmbyMediaSyncFile
	err := query.Order("id ASC").First(&relation).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return findEmbyMediaItemByRelation(relation, syncFile)
}

func findEmbyMediaItemByRelation(relation EmbyMediaSyncFile, syncFile *SyncFile) (*EmbyMediaItem, error) {
	var item EmbyMediaItem
	itemID := fmt.Sprintf("%d", relation.EmbyItemId)
	err := db.Db.Where("item_id_int = ? OR item_id = ?", int64(relation.EmbyItemId), itemID).
		Order("id ASC").
		First(&item).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func resolveEmbyRefreshTargetWithSiblingFallback(target EmbyRefreshTarget, syncPathID uint) EmbyRefreshTarget {
	resolution, err := resolveEmbyTargetLibraryWithDB(db.Db, target, []uint{syncPathID})
	if err == nil && resolution.Resolved {
		target.FallbackLibraryId = resolution.LibraryID
		target.FallbackLibraryName = resolution.LibraryName
		return target
	}
	if len(resolution.Candidates) > 1 {
		if remoteResolution := resolveEmbyTargetLibraryRemote(target); remoteResolution.Resolved {
			target.FallbackLibraryId = remoteResolution.LibraryID
			target.FallbackLibraryName = remoteResolution.LibraryName
		}
	}
	return target
}

func resolveSiblingSeasonOrSeries(syncFile *SyncFile) (EmbyRefreshTarget, bool, error) {
	if syncFile == nil || syncFile.Path == "" || syncFile.SyncPathId == 0 {
		return EmbyRefreshTarget{}, false, nil
	}
	var siblings []SyncFile
	query := db.Db.Where("sync_path_id = ? AND path = ?", syncFile.SyncPathId, syncFile.Path)
	if syncFile.ID > 0 {
		query = query.Where("id <> ?", syncFile.ID)
	}
	if err := query.Order("id DESC").Limit(50).Find(&siblings).Error; err != nil {
		return EmbyRefreshTarget{}, false, err
	}
	var seasonTargetWithoutLibrary EmbyRefreshTarget
	var seriesTargetWithoutLibrary EmbyRefreshTarget
	for _, sibling := range siblings {
		item, err := findEmbyItemBySyncFile(&sibling)
		if err != nil {
			return EmbyRefreshTarget{}, false, err
		}
		if item == nil || item.Type != "Episode" {
			continue
		}
		if item.SeasonId != "" {
			target := EmbyRefreshTarget{
				TargetType: EmbyRefreshTargetTypeItem,
				ItemID:     item.SeasonId,
				ItemName:   item.SeasonName,
				ItemType:   "Season",
				Recursive:  true,
			}
			if item.LibraryId != "" {
				return target, true, nil
			}
			if seasonTargetWithoutLibrary.ItemID == "" {
				seasonTargetWithoutLibrary = target
			}
			continue
		}
		if item.SeriesId != "" {
			target := EmbyRefreshTarget{
				TargetType: EmbyRefreshTargetTypeItem,
				ItemID:     item.SeriesId,
				ItemName:   item.SeriesName,
				ItemType:   "Series",
				Recursive:  true,
			}
			if item.LibraryId != "" {
				return target, true, nil
			}
			if seriesTargetWithoutLibrary.ItemID == "" {
				seriesTargetWithoutLibrary = target
			}
		}
	}
	if seasonTargetWithoutLibrary.ItemID != "" {
		return seasonTargetWithoutLibrary, true, nil
	}
	if seriesTargetWithoutLibrary.ItemID != "" {
		return seriesTargetWithoutLibrary, true, nil
	}
	return EmbyRefreshTarget{}, false, nil
}

func findEmbyItemByPath(syncFile *SyncFile) (*embyclientrestgo.BaseItemDtoV2, error) {
	if syncFile == nil || syncFile.LocalFilePath == "" || GlobalEmbyConfig == nil || GlobalEmbyConfig.EmbyUrl == "" || GlobalEmbyConfig.EmbyApiKey == "" {
		return nil, nil
	}
	client := embyclientrestgo.NewClient(GlobalEmbyConfig.EmbyUrl, GlobalEmbyConfig.EmbyApiKey)
	return client.FindItemByPath(syncFile.LocalFilePath)
}

func targetFromEmbyMediaItem(item *EmbyMediaItem) EmbyRefreshTarget {
	if item == nil {
		return EmbyRefreshTarget{TargetType: EmbyRefreshTargetTypeLibrary}
	}
	itemID := item.ItemId
	if itemID == "" && item.ItemIdInt > 0 {
		itemID = fmt.Sprintf("%d", item.ItemIdInt)
	}
	target := EmbyRefreshTarget{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     itemID,
		ItemName:   item.Name,
		ItemType:   item.Type,
		Recursive:  isRecursiveEmbyRefreshType(item.Type),
	}
	if item.LibraryId != "" {
		target.FallbackLibraryId = item.LibraryId
	}
	return target
}

func targetFromEmbyPathItem(item *embyclientrestgo.BaseItemDtoV2) EmbyRefreshTarget {
	if item == nil {
		return EmbyRefreshTarget{TargetType: EmbyRefreshTargetTypeLibrary}
	}
	return EmbyRefreshTarget{
		TargetType: EmbyRefreshTargetTypeItem,
		ItemID:     item.Id,
		ItemName:   item.Name,
		ItemType:   item.Type,
		Recursive:  isRecursiveEmbyRefreshType(item.Type),
	}
}

func isRecursiveEmbyRefreshType(itemType string) bool {
	switch itemType {
	case "Season", "Series", "Folder":
		return true
	default:
		return false
	}
}

func firstEmbyLibraryForSyncPath(syncPathID uint) (string, string) {
	return firstEmbyLibraryForSyncPathWithDB(db.Db, syncPathID)
}

func firstEmbyLibraryForSyncPathWithDB(tx *gorm.DB, syncPathID uint) (string, string) {
	if tx == nil || syncPathID == 0 {
		return "", ""
	}
	var relations []EmbyLibrarySyncPath
	if err := tx.Where("sync_path_id = ?", syncPathID).Find(&relations).Error; err != nil {
		helpers.AppLogger.Errorf("查询同步目录 %d 关联 Emby 媒体库失败：%v", syncPathID, err)
		return "", ""
	}
	sort.Slice(relations, func(i, j int) bool {
		return relations[i].LibraryId < relations[j].LibraryId
	})
	for _, relation := range relations {
		if relation.LibraryId != "" {
			return relation.LibraryId, relation.LibraryName
		}
	}
	return "", ""
}

func resolveEmbyTargetLibraryWithDB(tx *gorm.DB, target EmbyRefreshTarget, syncPathIDs []uint) (EmbyLibraryResolution, error) {
	if tx == nil {
		return EmbyLibraryResolution{}, nil
	}
	candidates, names, err := embyLibrariesForSyncPathsWithDB(tx, syncPathIDs)
	if err != nil {
		return EmbyLibraryResolution{}, err
	}
	resolved := EmbyLibraryResolution{Candidates: candidates}
	if target.TargetType != EmbyRefreshTargetTypeItem || target.ItemID == "" {
		return resolved, nil
	}

	if libraryIDs, err := embyItemLibraryIDsWithDB(tx, target.ItemID); err != nil {
		return resolved, err
	} else if len(libraryIDs) == 1 {
		return resolvedEmbyLibrary(libraryIDs[0], embyLibraryNameWithDB(tx, libraryIDs[0], names[libraryIDs[0]]), EmbyLibraryResolutionSourceItem, candidates), nil
	} else if len(libraryIDs) > 1 {
		resolved.Candidates = mergeStringIds(candidates, libraryIDs)
		return resolved, nil
	}

	if libraryIDs, err := siblingEpisodeLibraryIDsWithDB(tx, target); err != nil {
		return resolved, err
	} else if len(libraryIDs) == 1 {
		return resolvedEmbyLibrary(libraryIDs[0], embyLibraryNameWithDB(tx, libraryIDs[0], names[libraryIDs[0]]), EmbyLibraryResolutionSourceSiblingEpisode, candidates), nil
	} else if len(libraryIDs) > 1 {
		resolved.Candidates = mergeStringIds(candidates, libraryIDs)
		return resolved, nil
	}

	if len(candidates) == 1 {
		return resolvedEmbyLibrary(candidates[0], embyLibraryNameWithDB(tx, candidates[0], names[candidates[0]]), EmbyLibraryResolutionSourceSyncPath, candidates), nil
	}
	return resolved, nil
}

// embyLibraryNameWithDB 从当前关联、全局关联或媒体库表补全媒体库名称。
func embyLibraryNameWithDB(tx *gorm.DB, libraryID string, knownName string) string {
	if knownName != "" || tx == nil || libraryID == "" {
		return knownName
	}

	var relation EmbyLibrarySyncPath
	if err := tx.Where("library_id = ? AND library_name <> ''", libraryID).Order("id ASC").First(&relation).Error; err == nil {
		return relation.LibraryName
	}

	var library EmbyLibrary
	if err := tx.Where("library_id = ? AND name <> ''", libraryID).Order("id ASC").First(&library).Error; err == nil {
		return library.Name
	}
	return ""
}

func embyItemLibraryIDsWithDB(tx *gorm.DB, itemID string) ([]string, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return []string{}, nil
	}
	query := tx.Model(&EmbyMediaItem{}).Where("item_id = ?", itemID)
	if itemIDInt := helpers.StringToInt64(itemID); itemIDInt > 0 {
		query = tx.Model(&EmbyMediaItem{}).Where("item_id = ? OR item_id_int = ?", itemID, itemIDInt)
	}
	var libraryIDs []string
	if err := query.Where("library_id <> ''").Distinct("library_id").Pluck("library_id", &libraryIDs).Error; err != nil {
		return nil, err
	}
	return mergeStringIds(libraryIDs, nil), nil
}

func siblingEpisodeLibraryIDsWithDB(tx *gorm.DB, target EmbyRefreshTarget) ([]string, error) {
	var column string
	switch target.ItemType {
	case "Season":
		column = "season_id"
	case "Series":
		column = "series_id"
	case "":
		var libraryIDs []string
		if err := tx.Model(&EmbyMediaItem{}).
			Where("type = ? AND (season_id = ? OR series_id = ?) AND library_id <> ''", "Episode", target.ItemID, target.ItemID).
			Distinct("library_id").
			Pluck("library_id", &libraryIDs).Error; err != nil {
			return nil, err
		}
		return mergeStringIds(libraryIDs, nil), nil
	default:
		return []string{}, nil
	}
	var libraryIDs []string
	if err := tx.Model(&EmbyMediaItem{}).
		Where("type = ? AND "+column+" = ? AND library_id <> ''", "Episode", target.ItemID).
		Distinct("library_id").
		Pluck("library_id", &libraryIDs).Error; err != nil {
		return nil, err
	}
	return mergeStringIds(libraryIDs, nil), nil
}

func embyLibrariesForSyncPathsWithDB(tx *gorm.DB, syncPathIDs []uint) ([]string, map[string]string, error) {
	syncPathIDs = mergeSyncPathIds(syncPathIDs, nil)
	if len(syncPathIDs) == 0 {
		return []string{}, map[string]string{}, nil
	}
	var relations []EmbyLibrarySyncPath
	if err := tx.Where("sync_path_id IN ?", syncPathIDs).Find(&relations).Error; err != nil {
		return nil, nil, err
	}
	names := make(map[string]string, len(relations))
	libraryIDs := make([]string, 0, len(relations))
	for _, relation := range relations {
		if relation.LibraryId == "" {
			continue
		}
		libraryIDs = append(libraryIDs, relation.LibraryId)
		if names[relation.LibraryId] == "" {
			names[relation.LibraryId] = relation.LibraryName
		}
	}
	return mergeStringIds(libraryIDs, nil), names, nil
}

func resolvedEmbyLibrary(libraryID string, libraryName string, source EmbyLibraryResolutionSource, candidates []string) EmbyLibraryResolution {
	return EmbyLibraryResolution{
		LibraryID:   libraryID,
		LibraryName: libraryName,
		Source:      source,
		Resolved:    libraryID != "",
		Candidates:  mergeStringIds(candidates, nil),
	}
}

func resolveEmbyTargetLibraryRemote(target EmbyRefreshTarget) EmbyLibraryResolution {
	if target.ItemID == "" || GlobalEmbyConfig == nil || GlobalEmbyConfig.EmbyUrl == "" || GlobalEmbyConfig.EmbyApiKey == "" {
		return EmbyLibraryResolution{}
	}
	cacheKey := GlobalEmbyConfig.EmbyUrl + "\x00" + GlobalEmbyConfig.EmbyApiKey + "\x00" + target.ItemID
	if resolution, ok := getCachedEmbyRemoteLibraryResolution(cacheKey); ok {
		return resolution
	}

	embyRemoteLibraryResolutionCache.Lock()
	if entry, ok := embyRemoteLibraryResolutionCache.entries[cacheKey]; ok && time.Now().Before(entry.expiresAt) {
		embyRemoteLibraryResolutionCache.Unlock()
		return entry.resolution
	}
	if done, ok := embyRemoteLibraryResolutionCache.inFlight[cacheKey]; ok {
		embyRemoteLibraryResolutionCache.Unlock()
		<-done
		if resolution, ok := getCachedEmbyRemoteLibraryResolution(cacheKey); ok {
			return resolution
		}
		return EmbyLibraryResolution{}
	}
	done := make(chan struct{})
	embyRemoteLibraryResolutionCache.inFlight[cacheKey] = done
	embyRemoteLibraryResolutionCache.Unlock()

	resolution := resolveEmbyTargetLibraryRemoteUncached(target)
	embyRemoteLibraryResolutionCache.Lock()
	delete(embyRemoteLibraryResolutionCache.inFlight, cacheKey)
	if resolution.Resolved || len(resolution.Candidates) > 0 {
		embyRemoteLibraryResolutionCache.entries[cacheKey] = embyRemoteLibraryResolutionCacheEntry{
			resolution: resolution,
			expiresAt:  time.Now().Add(embyRemoteLibraryResolutionCacheTTL),
		}
	}
	close(done)
	embyRemoteLibraryResolutionCache.Unlock()
	return resolution
}

func getCachedEmbyRemoteLibraryResolution(cacheKey string) (EmbyLibraryResolution, bool) {
	embyRemoteLibraryResolutionCache.Lock()
	defer embyRemoteLibraryResolutionCache.Unlock()
	entry, ok := embyRemoteLibraryResolutionCache.entries[cacheKey]
	if !ok || !time.Now().Before(entry.expiresAt) {
		if ok {
			delete(embyRemoteLibraryResolutionCache.entries, cacheKey)
		}
		return EmbyLibraryResolution{}, false
	}
	return entry.resolution, true
}

func resolveEmbyTargetLibraryRemoteUncached(target EmbyRefreshTarget) EmbyLibraryResolution {
	client := embyclientrestgo.NewClient(GlobalEmbyConfig.EmbyUrl, GlobalEmbyConfig.EmbyApiKey)
	libraries, err := client.GetItemLibraryId(target.ItemID)
	if err != nil {
		helpers.AppLogger.Warnf("远端解析 Emby 条目 %s 所属媒体库失败：%v", target.ItemID, err)
		return EmbyLibraryResolution{}
	}
	ids := make([]string, 0, len(libraries))
	names := make(map[string]string, len(libraries))
	for _, library := range libraries {
		libraryID := library.ID
		if libraryID == "" {
			libraryID = library.ItemId
		}
		if libraryID == "" {
			continue
		}
		ids = append(ids, libraryID)
		names[libraryID] = library.Name
	}
	ids = mergeStringIds(ids, nil)
	if len(ids) != 1 {
		return EmbyLibraryResolution{Candidates: ids}
	}
	return resolvedEmbyLibrary(ids[0], names[ids[0]], EmbyLibraryResolutionSourceAncestors, ids)
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}
