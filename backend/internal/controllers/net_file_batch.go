package controllers

import (
	"fmt"
	pathpkg "path"
	"strings"

	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/v115open"
)

type netFileCacheStatus string

const (
	netFileCacheHit        netFileCacheStatus = "hit"
	netFileCacheMiss       netFileCacheStatus = "miss"
	netFileCachePartialHit netFileCacheStatus = "partial_hit"
	netFileCacheRefresh    netFileCacheStatus = "refresh"
)

type netFileSourceCapability struct {
	BatchSize  int
	TotalExact bool
}

type netFileBatchRange struct {
	Start int
	Size  int
}

type netFileCacheMeta struct {
	Status     netFileCacheStatus `json:"status"`
	BatchStart int                `json:"batch_start"`
	BatchSize  int                `json:"batch_size"`
	CachedAt   int64              `json:"cached_at"`
	ExpiresAt  int64              `json:"expires_at"`
}

type netFileListResponse struct {
	List       []*FileItem      `json:"list"`
	Total      int64            `json:"total"`
	TotalExact bool             `json:"total_exact"`
	HasMore    bool             `json:"has_more"`
	Page       int              `json:"page"`
	PageSize   int              `json:"page_size"`
	SortBy     string           `json:"sort_by"`
	SortOrder  string           `json:"sort_order"`
	Cache      netFileCacheMeta `json:"cache"`
}

func (b netFileBatch) fileItems() []*FileItem {
	if b.Raw115 == nil {
		return b.Items
	}
	items := make([]*FileItem, 0, len(b.Raw115.Data))
	for _, item := range b.Raw115.Data {
		items = append(items, &FileItem{
			Id: item.FileId, IsDirectory: item.FileCategory == v115open.TypeDir,
			Name: item.FileName, Size: item.FileSize, ModifiedAt: item.ModifiedAt(),
		})
	}
	return items
}

func getNetFileSourceCapability(sourceType models.SourceType, sortBy string, sortOrder string) (netFileSourceCapability, error) {
	if err := requests.ValidateBrowseSort(sourceType, "files", sortBy, sortOrder, nil); err != nil {
		return netFileSourceCapability{}, err
	}
	switch sourceType {
	case models.SourceType115:
		return netFileSourceCapability{BatchSize: 1000, TotalExact: true}, nil
	case models.SourceTypeBaiduPan:
		return netFileSourceCapability{BatchSize: 1000, TotalExact: false}, nil
	case models.SourceTypeOpenList:
		return netFileSourceCapability{BatchSize: 500, TotalExact: true}, nil
	default:
		return netFileSourceCapability{}, fmt.Errorf("未知的网盘类型")
	}
}

func map115Sort(sortBy string, sortOrder string) (string, string, error) {
	if sortBy == "" {
		sortBy = "name"
	}
	order, err := requests.BrowseSortParameter(models.SourceType115, sortBy)
	if err != nil || sortBy == "default" {
		return order, "", err
	}
	asc := "1"
	if sortOrder == "desc" {
		asc = "0"
	}
	return order, asc, nil
}

func mapBaiduSort(sortBy string, sortOrder string) (string, int32, error) {
	if sortBy == "" {
		sortBy = "name"
	}
	order, err := requests.BrowseSortParameter(models.SourceTypeBaiduPan, sortBy)
	var desc int32
	if sortOrder == "desc" {
		desc = 1
	}
	return order, desc, err
}

func browse115Options(sortBy, sortOrder string, foldersFirst *bool) (v115open.FileListOptions, error) {
	order, asc, err := map115Sort(sortBy, sortOrder)
	if err != nil {
		return v115open.FileListOptions{}, err
	}
	customOrder := 1
	if sortBy == "default" {
		customOrder = 0
	} else if foldersFirst != nil && !*foldersFirst {
		customOrder = 2
	}
	return v115open.FileListOptions{Order: order, Asc: asc, CustomOrder: &customOrder}, nil
}

func computeNetFileBatchRanges(page int, pageSize int, batchSize int) []netFileBatchRange {
	if page < 1 || pageSize < 1 || batchSize < 1 {
		return nil
	}
	uiStart := (page - 1) * pageSize
	uiEnd := uiStart + pageSize
	firstBatchStart := (uiStart / batchSize) * batchSize
	ranges := make([]netFileBatchRange, 0, 2)
	for start := firstBatchStart; start < uiEnd; start += batchSize {
		ranges = append(ranges, netFileBatchRange{Start: start, Size: batchSize})
	}
	return ranges
}

func normalizeNetFileSort(sourceType models.SourceType, sortBy string) string {
	if sortBy != "" {
		return sortBy
	}
	options, err := requests.BrowseSortOptionsFor(sourceType, "files")
	if err != nil {
		return sortBy
	}
	return options.Default.SortBy
}

func normalizeNetFileCachePath(sourceType models.SourceType, value string) string {
	switch sourceType {
	case models.SourceType115:
		value = strings.TrimSpace(value)
		if value == "" {
			return "0"
		}
		return value
	case models.SourceTypeBaiduPan, models.SourceTypeOpenList:
		value = normalizeOpenListPath(value)
		if value == "" {
			return "/"
		}
		return value
	default:
		return strings.TrimSpace(value)
	}
}

func normalizeOpenListPath(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	if value != "/" {
		value = strings.TrimRight(value, "/")
	}
	return value
}

func joinOpenListPath(parentPath string, name string) string {
	parentPath = normalizeOpenListPath(parentPath)
	if parentPath == "" {
		parentPath = "/"
	}
	return pathpkg.Join(parentPath, name)
}

func buildOpenListRemoveTarget(parentID string, fileID string) (string, []string, error) {
	return splitOpenListFileIDs(parentID, []string{fileID})
}

// splitOpenListFileIDs 把同一父目录下的完整路径文件 ID 拆分为父目录和文件名列表。
func splitOpenListFileIDs(parentID string, fileIDs []string) (string, []string, error) {
	parentID = normalizeOpenListPath(parentID)
	dir := parentID
	names := make([]string, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		fileID = normalizeOpenListPath(fileID)
		name := pathpkg.Base(fileID)
		if name == "" || name == "." || name == ".." || name == "/" {
			return "", nil, fmt.Errorf("OpenList 操作目标名称无效")
		}
		if dir == "" || dir == "." {
			dir = pathpkg.Dir(fileID)
		}
		if pathpkg.Dir(fileID) != dir {
			return "", nil, fmt.Errorf("OpenList 操作的文件必须位于同一父目录，且与 parent_id 一致")
		}
		names = append(names, name)
	}
	if dir == "." || dir == "" {
		dir = "/"
	}
	return dir, names, nil
}

func invalidateNetFileCacheForPath(sourceType models.SourceType, accountID uint, parentID string) {
	if accountID == 0 {
		return
	}
	netFileCache.InvalidatePath(string(sourceType), accountID, normalizeNetFileCachePath(sourceType, parentID))
}

func invalidateNetFileCacheForDeletedPath(sourceType models.SourceType, accountID uint, parentID string, fileID string) {
	if accountID == 0 {
		return
	}
	if sourceType == models.SourceTypeBaiduPan || sourceType == models.SourceTypeOpenList {
		parentID = pathpkg.Dir(normalizeNetFileCachePath(sourceType, fileID))
	}
	sourceTypeText := string(sourceType)
	netFileCache.InvalidatePath(sourceTypeText, accountID, normalizeNetFileCachePath(sourceType, parentID))
	netFileCache.InvalidatePathTree(sourceTypeText, accountID, normalizeNetFileCachePath(sourceType, fileID))
}

// invalidateNetFileCacheForChangedPaths 清理路径型文件的父目录和子树；115 使用稳定数字 ID。
func invalidateNetFileCacheForChangedPaths(sourceType models.SourceType, accountID uint, paths ...string) {
	if sourceType != models.SourceTypeBaiduPan && sourceType != models.SourceTypeOpenList {
		return
	}
	for _, value := range paths {
		invalidateNetFileCacheForDeletedPath(sourceType, accountID, "", value)
	}
}

func buildBaiduSyntheticTotal(batchStart int, itemCount int, batchSize int) (int64, bool) {
	if itemCount >= batchSize {
		return int64(batchStart + itemCount + 1), true
	}
	return int64(batchStart + itemCount), false
}

type netFileListResponseOptions struct {
	List       []*FileItem
	Total      int64
	TotalExact bool
	HasMore    bool
	Page       int
	PageSize   int
	SortBy     string
	SortOrder  string
	Cache      netFileCacheMeta
}

func sliceNetFileItems(items []*FileItem, baseStart int, page int, pageSize int) []*FileItem {
	if page < 1 || pageSize < 1 {
		return []*FileItem{}
	}
	start := (page-1)*pageSize - baseStart
	if start < 0 {
		start = 0
	}
	if start >= len(items) {
		return []*FileItem{}
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end]
}

func buildNetFileListResponse(options netFileListResponseOptions) netFileListResponse {
	if options.Page < 1 {
		options.Page = 1
	}
	if options.PageSize < 1 {
		options.PageSize = len(options.List)
	}
	loadedTotal := int64((options.Page-1)*options.PageSize + len(options.List))
	total := options.Total
	if total < loadedTotal {
		total = loadedTotal
	}
	return netFileListResponse{
		List:       options.List,
		Total:      total,
		TotalExact: options.TotalExact,
		HasMore:    options.HasMore,
		Page:       options.Page,
		PageSize:   options.PageSize,
		SortBy:     options.SortBy,
		SortOrder:  options.SortOrder,
		Cache:      options.Cache,
	}
}
