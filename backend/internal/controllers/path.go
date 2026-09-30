package controllers

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
	"github.com/shirou/gopsutil/v4/disk"
)

type DirResp struct {
	Id           string `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	ModifiedTime *int64 `json:"modified_time,omitempty"`
}

// GetPathList 获取目录列表
// @Summary 获取目录列表
// @Description 获取当前层目录；远端顺序由来源接口决定，本地排序由调用方处理
// @Tags 路径管理
// @Accept json
// @Produce json
// @Param parent_id query string false "父目录 ID（115）或路径（其他来源）"
// @Param source_type query string true "来源类型：local、115、openlist、baidupan"
// @Param account_id query integer false "账号 ID，远程来源必填"
// @Param sort_by query string false "排序字段，以 sort-options 返回的目录能力为准"
// @Param sort_order query string false "asc 或 desc；跟随网盘时省略"
// @Param refresh query integer false "0 或 1，显式刷新当前父目录"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /path/list [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetPathList(c *gin.Context) {
	var req requests.PathListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	var pathes []DirResp
	var err error
	var account *models.Account
	if req.SourceType == models.SourceTypeLocal {
		pathes, err = GetLocalPath(req.ParentID)
	} else {
		account, err = models.GetAccountById(req.AccountID)
		if err == nil && account.SourceType != req.SourceType {
			err = fmt.Errorf("账号与来源类型不匹配")
		}
		if err == nil {
			switch req.SourceType {
			case models.SourceTypeOpenList:
				pathes, err = GetOpenListPath(c.Request.Context(), req.ParentID, account, req.Refresh == 1)
			case models.SourceType115:
				pathes, err = Get115PathList(c.Request.Context(), req, account)
			case models.SourceTypeBaiduPan:
				pathes, err = GetBaiduPanPathList(c.Request.Context(), req, account)
			}
		}
	}

	if err != nil {
		message := helpers.RedactSensitiveLog(err.Error())
		if account != nil {
			message = helpers.RedactSensitiveLog(err.Error(), account.Token, account.RefreshToken, account.Password)
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取目录列表失败：" + message, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取目录列表成功", Data: pathes})
}

// GetLocalPath 获取本地目录列表。
// parentPath 为空时，Windows 返回盘符列表，其他系统返回根目录 / 的子目录列表。
func GetLocalPath(parentPath string) ([]DirResp, error) {
	pathes := make([]DirResp, 0)
	// Windows
	if parentPath == "" {
		if runtime.GOOS == "windows" {
			// helpers.AppLogger.Infof("parentPath：%s", parentPath)
			if parentPath == "" {
				// 获取盘符列表，限制异常磁盘驱动导致的等待时间。
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				partitions, err := disk.PartitionsWithContext(ctx, false)
				// helpers.AppLogger.Infof("partitions：%+v", partitions)
				if err != nil {
					helpers.AppLogger.Errorf("获取盘符失败：%v", err)
					return nil, err
				}
				for _, partition := range partitions {
					// helpers.AppLogger.Debugf("盘符：%s", partition.Mountpoint)
					pathes = append(pathes, DirResp{
						Id:   partition.Mountpoint + "\\",
						Name: partition.Mountpoint,
						Path: partition.Mountpoint + "\\",
					})
				}
				return pathes, nil
			}
		} else {
			if helpers.IsFnOS {
				// 飞牛环境下使用环境变量获取有权限的目录。
				if helpers.AccessiblePathes == "" {
					helpers.AccessiblePathes = os.Getenv("TRIM_DATA_ACCESSIBLE_PATHS")
				}
				// if helpers.SharePathes == "" {
				helpers.SharePathes = os.Getenv("TRIM_DATA_SHARE_PATHS")
				// }
				helpers.AppLogger.Debugf("AccessiblePathes：%s", helpers.AccessiblePathes)
				helpers.AppLogger.Debugf("SharePathes：%s", helpers.SharePathes)
				if helpers.AccessiblePathes != "" || helpers.SharePathes != "" {
					accessiblePaths := helpers.AccessiblePathes
					sharePaths := helpers.SharePathes
					if sharePaths != "" {
						accessiblePaths += ":" + sharePaths
					}
					helpers.AppLogger.Debugf("合并后有权限访问的目录为：%s", accessiblePaths)
					// 用冒号分割
					paths := strings.SplitSeq(accessiblePaths, ":")
					for path := range paths {
						// 去掉首尾空格
						path = strings.TrimSpace(path)
						// 加入列表
						pathes = append(pathes, DirResp{
							Id:   path,
							Name: path,
							Path: path,
						})
					}
				}
				return pathes, nil
			} else {
				// 获取根目录 / 的子目录列表
				parentPath = "/"
			}
		}
	}
	// 获取子目录列表
	entries, err := os.ReadDir(parentPath)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			// 跳过隐藏目录
			if strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			fullPath := filepath.ToSlash(filepath.Join(parentPath, entry.Name()))
			directory := DirResp{Id: fullPath, Name: entry.Name(), Path: fullPath}
			if info, infoErr := entry.Info(); infoErr == nil {
				modified := info.ModTime().Unix()
				directory.ModifiedTime = &modified
			}
			pathes = append(pathes, directory)
		}
	}

	return pathes, nil
}

// GetBrowseSortOptions 返回当前来源和浏览场景的排序能力。
func GetBrowseSortOptions(c *gin.Context) {
	var req requests.BrowseSortOptionsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	options, err := requests.BrowseSortOptionsFor(req.SourceType, req.Scope)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[requests.BrowseSortOptions]{Code: Success, Data: options})
}

// GetOpenListPath 使用原生目录接口，保持上游顺序；显式刷新失败时不回退旧缓存。
func GetOpenListPath(
	ctx context.Context,
	parentPath string,
	account *models.Account,
	refresh bool,
) ([]DirResp, error) {
	parentPath = normalizeNetFileCachePath(models.SourceTypeOpenList, parentPath)
	key := netFileBatchCacheKey{
		SourceType: string(models.SourceTypeOpenList), AccountID: account.ID, Path: parentPath,
		SortBy: "default", SortOrder: "asc", Filter: "directories",
	}
	if refresh {
		netFileCache.InvalidatePath(key.SourceType, key.AccountID, key.Path)
	}
	batch, _, err := netFileCache.getOrFetch(ctx, key, refresh, func(fetchCtx context.Context) (netFileBatch, error) {
		client := account.GetOpenListClient()
		if refresh {
			if _, err := client.FileListWithRefresh(fetchCtx, parentPath, 1, 1, true); err != nil {
				return netFileBatch{}, err
			}
		}
		items, err := client.DirList(fetchCtx, parentPath, false)
		if err != nil {
			return netFileBatch{}, err
		}
		folders := make([]DirResp, 0, len(items))
		for _, item := range items {
			path := joinOpenListPath(parentPath, item.Name)
			folders = append(folders, DirResp{Id: path, Name: item.Name, Path: path})
		}
		return netFileBatch{Directories: folders}, nil
	})
	return batch.Directories, err
}

// Get115PathList 分页读取当前父目录，显式排序时采用 115 原生目录置顶。
func Get115PathList(ctx context.Context, req requests.PathListRequest, account *models.Account) ([]DirResp, error) {
	parentID := normalizeNetFileCachePath(models.SourceType115, req.ParentID)
	sortOrder := req.SortOrder
	if req.SortBy != "" && sortOrder == "" {
		sortOrder = "asc"
	}
	foldersFirst := req.SortBy != "" && req.SortBy != "default"
	if req.Refresh == 1 {
		invalidateNetFileCacheForPath(models.SourceType115, account.ID, parentID)
	}
	return read115DirectoryPages(ctx, foldersFirst, func(offset int) (*v115open.FileListResp, error) {
		key := netFileBatchCacheKey{
			SourceType: string(models.SourceType115), AccountID: account.ID, Path: parentID,
			SortBy: req.SortBy, SortOrder: sortOrder, FoldersFirst: foldersFirst,
			Filter: "none", BatchStart: offset, BatchSize: 1000,
		}
		batch, _, err := netFileCache.getOrFetch(ctx, key, req.Refresh == 1, func(fetchCtx context.Context) (netFileBatch, error) {
			return fetch115NetFileBatch(fetchCtx, account, parentID, offset, 1000, req.SortBy, sortOrder, nil)
		})
		return batch.Raw115, err
	})
}

func read115DirectoryPages(
	ctx context.Context,
	foldersFirst bool,
	fetch func(int) (*v115open.FileListResp, error),
) ([]DirResp, error) {
	folders := make([]DirResp, 0)
	seen := make(map[string]struct{})
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		resp, err := fetch(offset)
		if err != nil {
			return nil, err
		}
		if err := validate115BrowseBatch(resp, offset); err != nil {
			return nil, err
		}
		foundFile := false
		for _, item := range resp.Data {
			if _, exists := seen[item.FileId]; exists || item.FileId == "" {
				return nil, fmt.Errorf("115 目录分页出现重复或无效条目，请刷新重试")
			}
			seen[item.FileId] = struct{}{}
			if item.FileCategory == v115open.TypeFile {
				foundFile = true
				continue
			}
			folders = append(folders, DirResp{
				Id: item.FileId, Name: item.FileName,
				Path: pathpkg.Join(resp.PathStr, item.FileName),
			})
		}
		// count 已包含系统目录；offset 必须按原始条目数而非目录数推进。
		offset += len(resp.Data)
		if (foldersFirst && foundFile) || offset >= resp.Count {
			return folders, ctx.Err()
		}
	}
}

// GetBaiduPanPathList 使用普通 list 的 folder=1 和 start/limit 分页。
func GetBaiduPanPathList(ctx context.Context, req requests.PathListRequest, account *models.Account) ([]DirResp, error) {
	options := baidupan.FileListOptions{}
	if req.SortBy != "" {
		order, desc, err := mapBaiduSort(req.SortBy, req.SortOrder)
		if err != nil {
			return nil, err
		}
		options = baidupan.FileListOptions{Order: order, Desc: &desc}
	}
	parentID := normalizeNetFileCachePath(models.SourceTypeBaiduPan, req.ParentID)
	if req.Refresh == 1 {
		invalidateNetFileCacheForPath(models.SourceTypeBaiduPan, account.ID, parentID)
	}
	folders := make([]DirResp, 0)
	seen := make(map[string]struct{})
	const limit = 1000
	for start := 0; ; {
		key := netFileBatchCacheKey{
			SourceType: string(models.SourceTypeBaiduPan), AccountID: account.ID, Path: parentID,
			SortBy: req.SortBy, SortOrder: req.SortOrder, Filter: "directories", BatchStart: start, BatchSize: limit,
		}
		batch, _, err := netFileCache.getOrFetch(ctx, key, req.Refresh == 1, func(fetchCtx context.Context) (netFileBatch, error) {
			items, err := account.GetBaiDuPanClient().GetFileListWithOptions(fetchCtx, parentID, 1, 1, int32(start), limit, options)
			if err != nil {
				return netFileBatch{}, err
			}
			page := make([]DirResp, 0, len(items))
			pageSeen := make(map[string]struct{}, len(items))
			for _, item := range items {
				if item == nil || item.Path == "" {
					return netFileBatch{}, fmt.Errorf("百度网盘目录列表包含无效条目")
				}
				if _, exists := pageSeen[item.Path]; exists {
					return netFileBatch{}, fmt.Errorf("百度网盘目录分页出现重复条目，请刷新重试")
				}
				pageSeen[item.Path] = struct{}{}
				// folder=1 可只返回 path，不依赖 isdir 或时间字段。
				path := strings.TrimPrefix(item.Path, "/")
				page = append(page, DirResp{Id: path, Name: pathpkg.Base(path), Path: path})
			}
			return netFileBatch{Directories: page}, nil
		})
		if err != nil {
			return nil, err
		}
		for _, item := range batch.Directories {
			if _, exists := seen[item.Id]; exists {
				return nil, fmt.Errorf("百度网盘目录分页出现重复条目，请刷新重试")
			}
			seen[item.Id] = struct{}{}
			folders = append(folders, item)
		}
		if len(batch.Directories) < limit {
			return folders, ctx.Err()
		}
		if int64(start)+int64(len(batch.Directories)) > math.MaxInt32 {
			return nil, fmt.Errorf("百度网盘目录分页超出接口范围")
		}
		start += len(batch.Directories)
	}
}

type FileItem struct {
	Id          string `json:"id"`
	IsDirectory bool   `json:"is_directory"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ModifiedAt  int64  `json:"modified_time"`
}

type netFileListPage struct {
	list  []*FileItem
	total int64
}

// 返回目录和文件列表
func GetNetFileList(c *gin.Context) {
	var req requests.NetFileListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号信息失败：" + err.Error(), Data: nil})
		return
	}
	resp, err := getNetFileListPage(c.Request.Context(), netFileListQuery{
		Account:      account,
		ParentID:     req.ParentID,
		Page:         req.Page,
		PageSize:     req.PageSize,
		Refresh:      req.Refresh,
		SortBy:       req.SortBy,
		SortOrder:    req.SortOrder,
		FoldersFirst: req.FoldersFirst,
	})
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取目录列表失败：" + helpers.RedactSensitiveLog(err.Error(), account.Token, account.RefreshToken, account.Password), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[netFileListResponse]{
		Code:    Success,
		Message: "",
		Data:    resp,
	})
}

type netFileListQuery struct {
	FoldersFirst *bool
	Account      *models.Account
	ParentID     string
	Page         int
	PageSize     int
	Refresh      bool
	SortBy       string
	SortOrder    string
}

func getNetFileListPage(ctx context.Context, query netFileListQuery) (netFileListResponse, error) {
	if err := ctx.Err(); err != nil {
		return netFileListResponse{}, err
	}
	if query.Account == nil {
		return netFileListResponse{}, fmt.Errorf("账号不能为空")
	}
	sortBy := normalizeNetFileSort(query.Account.SourceType, query.SortBy)
	sortOrder := query.SortOrder
	if sortOrder == "" {
		sortOrder = "asc"
	}
	if err := requests.ValidateBrowseSort(query.Account.SourceType, "files", sortBy, sortOrder, query.FoldersFirst); err != nil {
		return netFileListResponse{}, err
	}
	foldersFirst := query.Account.SourceType == models.SourceType115 && sortBy != "default"
	if query.FoldersFirst != nil {
		foldersFirst = *query.FoldersFirst
	}
	capability, err := getNetFileSourceCapability(query.Account.SourceType, sortBy, sortOrder)
	if err != nil {
		return netFileListResponse{}, err
	}
	cachePath := normalizeNetFileCachePath(query.Account.SourceType, query.ParentID)
	const filter = "none"
	if query.Refresh {
		netFileCache.InvalidatePath(string(query.Account.SourceType), query.Account.ID, cachePath)
	}

	ranges := computeNetFileBatchRanges(query.Page, query.PageSize, capability.BatchSize)
	items := make([]*FileItem, 0, query.PageSize)
	total := int64(0)
	hasMore := false
	status := netFileCacheHit
	var firstBatch netFileBatch
	hitCount := 0
	missCount := 0

	for _, batchRange := range ranges {
		key := netFileBatchCacheKey{
			SourceType:   string(query.Account.SourceType),
			AccountID:    query.Account.ID,
			Path:         cachePath,
			SortBy:       sortBy,
			SortOrder:    sortOrder,
			FoldersFirst: foldersFirst,
			Filter:       filter,
			BatchStart:   batchRange.Start,
			BatchSize:    batchRange.Size,
		}
		batch, hit, err := netFileCache.getOrFetch(ctx, key, query.Refresh, func(fetchCtx context.Context) (netFileBatch, error) {
			return fetchNetFileBatch(fetchCtx, query.Account, cachePath, batchRange.Start, batchRange.Size, sortBy, sortOrder, query.Refresh, query.FoldersFirst)
		})
		if err != nil {
			return netFileListResponse{}, err
		}
		if hit {
			hitCount++
		} else {
			missCount++
		}
		if err := ctx.Err(); err != nil {
			return netFileListResponse{}, err
		}
		if firstBatch.CachedAt == 0 {
			firstBatch = batch
		}
		items = append(items, batch.fileItems()...)
		if batch.Total > total {
			total = batch.Total
		}
		hasMore = hasMore || batch.HasMore
	}
	if query.Refresh {
		status = netFileCacheRefresh
	} else if hitCount > 0 && missCount > 0 {
		status = netFileCachePartialHit
	} else if missCount > 0 {
		status = netFileCacheMiss
	}

	batchStart := 0
	if len(ranges) > 0 {
		batchStart = ranges[0].Start
	}
	pageItems := sliceNetFileItems(items, batchStart, query.Page, query.PageSize)
	return buildNetFileListResponse(netFileListResponseOptions{
		List:       pageItems,
		Total:      total,
		TotalExact: capability.TotalExact,
		HasMore:    hasMore,
		Page:       query.Page,
		PageSize:   query.PageSize,
		SortBy:     sortBy,
		SortOrder:  sortOrder,
		Cache: netFileCacheMeta{
			Status:     status,
			BatchStart: batchStart,
			BatchSize:  capability.BatchSize,
			CachedAt:   firstBatch.CachedAt,
			ExpiresAt:  firstBatch.ExpiresAt,
		},
	}), nil
}

func netFileSingleflightKey(key netFileBatchCacheKey) string {
	return fmt.Sprintf(
		"%s/%d/%s/%s/%s/%s/%t/%d/%d",
		key.SourceType,
		key.AccountID,
		key.Path,
		key.SortBy,
		key.SortOrder,
		key.Filter,
		key.FoldersFirst,
		key.BatchStart,
		key.BatchSize,
	)
}

func fetchNetFileBatch(
	ctx context.Context,
	account *models.Account,
	parentID string,
	start, size int,
	sortBy, sortOrder string,
	refresh bool,
	foldersFirst *bool,
) (netFileBatch, error) {
	switch account.SourceType {
	case models.SourceType115:
		return fetch115NetFileBatch(ctx, account, parentID, start, size, sortBy, sortOrder, foldersFirst)
	case models.SourceTypeBaiduPan:
		return fetchBaiduNetFileBatch(ctx, account, parentID, start, size, sortBy, sortOrder)
	case models.SourceTypeOpenList:
		return fetchOpenListNetFileBatch(ctx, account, parentID, start, size, refresh)
	default:
		return netFileBatch{}, fmt.Errorf("未知的网盘类型")
	}
}

func fetch115NetFileBatch(
	ctx context.Context,
	account *models.Account,
	parentID string,
	start, size int,
	sortBy, sortOrder string,
	foldersFirst *bool,
) (netFileBatch, error) {
	if parentID == "" {
		parentID = "0"
	}
	options := v115open.FileListOptions{}
	if sortBy != "" {
		var err error
		options, err = browse115Options(sortBy, sortOrder, foldersFirst)
		if err != nil {
			return netFileBatch{}, err
		}
	}
	// 两个入口均读取当前层全部类型，没有筛选条件，不需要 stdir。
	resp, err := account.Get115Client().GetFsListWithOptions(ctx, parentID, true, false, true, start, size, options)
	if err != nil {
		return netFileBatch{}, err
	}
	if err := validate115BrowseBatch(resp, start); err != nil {
		return netFileBatch{}, err
	}
	return netFileBatch{
		Raw115: resp, Total: int64(resp.Count), TotalExact: true,
		HasMore: start+len(resp.Data) < resp.Count,
	}, nil
}

func validate115BrowseBatch(resp *v115open.FileListResp, start int) error {
	if resp == nil || !resp.State || resp.Count < 0 {
		return fmt.Errorf("115 目录列表响应无效")
	}
	if len(resp.Data) == 0 && start < resp.Count {
		return fmt.Errorf("115 目录列表未读取完整，请刷新重试")
	}
	if len(resp.Data) > 0 && (start > resp.Count || len(resp.Data) > resp.Count-start) {
		return fmt.Errorf("115 目录列表条目数超出总数，请刷新重试")
	}
	seen := make(map[string]struct{}, len(resp.Data))
	for _, item := range resp.Data {
		if _, exists := seen[item.FileId]; exists || item.FileId == "" {
			return fmt.Errorf("115 目录分页出现重复或无效条目，请刷新重试")
		}
		seen[item.FileId] = struct{}{}
		if item.FileCategory != v115open.TypeDir && item.FileCategory != v115open.TypeFile {
			return fmt.Errorf("115 目录分页包含无效条目类型，请刷新重试")
		}
	}
	return nil
}

func fetchBaiduNetFileBatch(ctx context.Context, account *models.Account, parentID string, start int, size int, sortBy string, sortOrder string) (netFileBatch, error) {
	if parentID == "" {
		parentID = "/"
	}
	order, desc, err := mapBaiduSort(sortBy, sortOrder)
	if err != nil {
		return netFileBatch{}, err
	}
	client := account.GetBaiDuPanClient()
	fileList, err := client.GetFileListWithOptions(ctx, parentID, 0, 1, int32(start), int32(size), baidupan.FileListOptions{Order: order, Desc: &desc})
	if err != nil {
		helpers.AppLogger.Warnf("获取百度网盘文件列表失败：父目录=%s，错误=%v", parentID, err)
		return netFileBatch{}, err
	}
	items := make([]*FileItem, 0, len(fileList))
	for _, item := range fileList {
		name := item.ServerFilename
		if name == "" {
			name = filepath.Base(item.Path)
		}
		items = append(items, &FileItem{
			Id:          item.Path,
			IsDirectory: item.IsDir == 1,
			Name:        name,
			Size:        int64(item.Size),
			ModifiedAt:  int64(item.ServerMtime),
		})
	}
	total, hasMore := buildBaiduSyntheticTotal(start, len(fileList), size)
	return netFileBatch{
		Items:      items,
		Total:      total,
		TotalExact: false,
		HasMore:    hasMore,
	}, nil
}

func fetchOpenListNetFileBatch(ctx context.Context, account *models.Account, parentPath string, start int, size int, refresh bool) (netFileBatch, error) {
	parentPath = normalizeOpenListPath(parentPath)
	if parentPath == "" {
		parentPath = "/"
	}
	client := account.GetOpenListClient()
	const perPage = 100
	firstPage := start/perPage + 1
	maxPages := size / perPage
	if size%perPage != 0 {
		maxPages++
	}
	items := make([]*FileItem, 0, size)
	total := int64(0)
	for i := 0; i < maxPages; i++ {
		resp, err := client.FileListWithRefresh(ctx, parentPath, firstPage+i, perPage, refresh && i == 0)
		if err != nil {
			return netFileBatch{}, err
		}
		if resp.Total > 0 || total == 0 {
			total = resp.Total
		}
		if len(resp.Content) == 0 {
			break
		}
		for _, item := range resp.Content {
			modifiedAt := int64(0)
			if parsedAt, parseErr := time.Parse(time.RFC3339, item.Modified); parseErr == nil {
				modifiedAt = parsedAt.Unix()
			}
			items = append(items, &FileItem{
				Id:          joinOpenListPath(parentPath, item.Name),
				IsDirectory: item.IsDir,
				Name:        item.Name,
				Size:        item.Size,
				ModifiedAt:  modifiedAt,
			})
		}
		if len(items) >= size || int64(start+len(items)) >= total {
			break
		}
	}
	return netFileBatch{
		Items:      items,
		Total:      total,
		TotalExact: true,
		HasMore:    int64(start+len(items)) < total,
	}, nil
}

func getOpenlistDirs(parentPath string, account *models.Account, page, pageSize int) (netFileListPage, error) {
	// 去掉 parentPath 末尾的 /
	parentPath = strings.TrimSuffix(parentPath, "/")
	parentPath = strings.TrimSuffix(parentPath, "\\")
	helpers.AppLogger.Infof("开始获取 OpenList 目录列表，父目录路径：%s", parentPath)
	client := account.GetOpenListClient()
	resp, err := client.FileList(context.Background(), parentPath, page, pageSize)
	if err != nil {
		return netFileListPage{}, err
	}
	// 只返回文件夹列表
	items := make([]*FileItem, 0)
	for _, item := range resp.Content {
		t, err := time.Parse(time.RFC3339, item.Modified)
		var mtime int64
		if err != nil {
			mtime = 0 // 错误时使用默认值
		} else {
			mtime = t.Unix() // 转换为 Unix 时间戳（秒）
		}
		items = append(items, &FileItem{
			Id:          parentPath + "/" + item.Name,
			IsDirectory: item.IsDir,
			Name:        item.Name,
			Size:        item.Size,
			ModifiedAt:  mtime,
		})
	}
	return netFileListPage{list: items, total: resp.Total}, nil
}

func get115Dirs(parentId string, account *models.Account, page, pageSize int) (netFileListPage, error) {
	client := account.Get115Client()
	ctx := context.Background()
	if parentId == "" {
		parentId = "0"
	}
	resp, err := client.GetFsList(ctx, parentId, true, false, true, (page-1)*pageSize, pageSize)
	if err != nil {
		helpers.AppLogger.Warnf("获取 115 目录列表失败：父目录=%s，错误=%v", parentId, err)
		return netFileListPage{}, err
	}
	helpers.AppLogger.Infof("成功获取 115 文件列表，父目录 ID：%s，文件数量：%d", parentId, len(resp.Data))
	items := make([]*FileItem, 0)
	// 构建 Path
	for _, item := range resp.Data {
		items = append(items, &FileItem{
			Id:          item.FileId,
			IsDirectory: item.FileCategory == v115open.TypeDir,
			Name:        item.FileName,
			Size:        item.FileSize,
			ModifiedAt:  item.ModifiedAt(),
		})
	}
	return netFileListPage{list: items, total: int64(resp.Count)}, nil

}

func getBaiduPanDirs(parentId string, account *models.Account, page, pageSize int) (netFileListPage, error) {
	client := account.GetBaiDuPanClient()
	ctx := context.Background()
	fileList, fileErr := client.GetFileList(ctx, parentId, 0, 1, int32((page-1)*pageSize), int32(pageSize))
	if fileErr != nil {
		helpers.AppLogger.Warnf("获取百度网盘目录列表失败：父目录：%s，错误：%v", parentId, fileErr)
		return netFileListPage{}, fileErr
	}
	// helpers.AppLogger.Infof("成功获取百度网盘文件列表，父目录 ID：%s，文件数量：%d", parentId, len(resp.Data))
	items := make([]*FileItem, 0)
	// 构建路径
	for _, item := range fileList {
		items = append(items, &FileItem{
			Id:          item.Path,
			IsDirectory: item.IsDir == 1,
			Name:        filepath.Base(item.Path),
			Size:        int64(item.Size),
			ModifiedAt:  int64(item.ServerMtime),
		})
	}
	return netFileListPage{list: items}, nil
}

// 创建文件夹
func CreateDir(c *gin.Context) {
	var req requests.CreateDirRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	var err error
	var pathId string
	switch req.SourceType {
	case models.SourceTypeLocal:
		pathId, err = makeLocalPath(req.ParentID, req.Name)
	case models.SourceTypeOpenList:
		pathId, err = makeOpenListPath(req.ParentID, req.Name, req.AccountID)
	case models.SourceType115:
		pathId, err = make115PathList(req.ParentID, req.ParentPath, req.Name, req.AccountID)
	case models.SourceTypeBaiduPan:
		pathId, err = makeBaiduPanPathList(req.ParentID, req.Name, req.AccountID)
	default:
		// 报错
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未知的同步源类型", Data: nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "创建目录失败：" + err.Error(), Data: nil})
		return
	}
	if req.SourceType != models.SourceTypeLocal {
		invalidateNetFileCacheForPath(req.SourceType, req.AccountID, req.ParentID)
	}
	dirResp := DirResp{
		Id:   pathId,
		Name: req.Name,
		Path: filepath.ToSlash(filepath.Join(req.ParentPath, req.Name)),
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "创建目录成功", Data: dirResp})
}

// 创建本地目录
func makeLocalPath(parentId string, folderName string) (string, error) {
	// 检查父目录是否存在
	if !helpers.PathExists(parentId) || parentId == "" {
		return "", fmt.Errorf("父目录不存在：%s", parentId)
	}
	// 构建新目录路径
	newDir := filepath.Join(parentId, folderName)
	// 创建目录
	if err := os.Mkdir(newDir, 0755); err != nil {
		return "", fmt.Errorf("创建目录失败：%s，错误：%v", newDir, err)
	}
	return filepath.ToSlash(newDir), nil
}

// 创建 OpenList 目录
func makeOpenListPath(parentId string, folderName string, accountId uint) (string, error) {
	if parentId == "" {
		parentId = "/"
	}
	// 检查父目录是否存在
	account, err := models.GetAccountById(accountId)
	if err != nil {
		return "", fmt.Errorf("获取账号失败：%v", err)
	}
	client := account.GetOpenListClient()
	_, err = client.FileDetail(parentId)
	if err != nil {
		return "", fmt.Errorf("获取 OpenList 目录详情失败，目录可能不存在：%v", err)
	}
	newDir := filepath.ToSlash(filepath.Join(parentId, folderName))
	err = client.Mkdir(newDir)
	if err != nil {
		return "", fmt.Errorf("创建 OpenList 目录失败：%s，错误：%v", newDir, err)
	}
	return newDir, nil
}

// 创建 115 目录
func make115PathList(parentId, parentPath, folderName string, accountId uint) (string, error) {
	if parentId == "" {
		parentId = "0"
	}
	// 检查父目录是否存在
	account, err := models.GetAccountById(accountId)
	if err != nil {
		return "", fmt.Errorf("获取账号失败：%v", err)
	}
	client := account.Get115Client()
	if parentId != "0" {
		_, err = client.GetFsDetailByCid(context.Background(), parentId)
		if err != nil {
			return "", fmt.Errorf("获取 115 目录详情失败，目录可能不存在：%v", err)
		}
	}
	newDir := filepath.ToSlash(filepath.Join(parentPath, folderName))
	newPathId, err := client.MkDir(context.Background(), parentId, folderName)
	if err != nil {
		return "", fmt.Errorf("创建 115 目录失败：%s，错误：%v", newDir, err)
	}
	return newPathId, nil
}

func makeBaiduPanPathList(parentId string, folderName string, accountId uint) (string, error) {
	if parentId == "" {
		parentId = "/"
	}
	// 检查父目录是否存在
	account, err := models.GetAccountById(accountId)
	if err != nil {
		return "", fmt.Errorf("获取账号失败：%v", err)
	}
	client := account.GetBaiDuPanClient()
	exists, err := client.PathExists(context.Background(), parentId)
	if err != nil {
		return "", fmt.Errorf("获取百度网盘目录失败，目录可能不存在：%v", err)
	}
	if !exists {
		return "", fmt.Errorf("父目录不存在：%s", parentId)
	}
	// 创建新目录
	newDir := filepath.ToSlash(filepath.Join(parentId, folderName))
	err = client.Mkdir(context.Background(), newDir)
	if err != nil {
		return "", fmt.Errorf("创建百度网盘目录失败：%s，错误：%v", newDir, err)
	}
	return newDir, nil
}

// 更新飞牛有权限的目录
// 飞牛执行目录授权操作后，会触发该接口调用
func UpdateFNPath(c *gin.Context) {
	var req requests.FNPathRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 用冒号分隔路径
	paths := strings.Split(req.Path, ":")
	// 对每个路径进行清理
	sysPathes := []string{"/dev", "/usr", "/etc", "/var", "/bin", "/lib", "/proc", "/run", "/boot", "/sbin", "/sys", "/srv", "/lib64"}
	safePathes := make([]string, 0)
mainloop:
	for _, path := range paths {
		p := filepath.Clean(path)
		sp := ""
		for _, sysPath := range sysPathes {
			if strings.HasPrefix(p, sysPath) {
				continue mainloop
			}
			sp = p
		}
		if sp != "" {
			safePathes = append(safePathes, sp)
		}
	}
	helpers.AccessiblePathes = strings.Join(safePathes, ":")
	helpers.AppLogger.Infof("更新飞牛有权限的目录为：%s", req.Path)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新目录成功", Data: nil})
}

func DeleteDir(c *gin.Context) {
	var req requests.DeleteDirRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	invalidateParentID := req.ParentID
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		_, err = client.Del(context.Background(), []string{req.FileID}, req.ParentID)
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		err = client.Del(context.Background(), []string{req.FileID})
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		var names []string
		invalidateParentID, names, err = buildOpenListRemoveTarget(req.ParentID, req.FileID)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		err = client.Del(invalidateParentID, names)
	default:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的文件系统", Data: nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除目录失败：" + err.Error(), Data: nil})
		return
	}
	invalidateNetFileCacheForDeletedPath(account.SourceType, req.AccountID, invalidateParentID, req.FileID)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "删除目录成功", Data: nil})
}

func check115FileOperationResult(ok bool, err error) error {
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("115 未确认操作成功")
	}
	return nil
}

type fileTransferResult struct {
	Status  string   `json:"status"`
	TaskIDs []string `json:"task_ids"`
}

func sendFileTransferResult(c *gin.Context, action string, taskIDs []string) {
	result := fileTransferResult{Status: "completed", TaskIDs: []string{}}
	message := "批量" + action + "成功"
	if len(taskIDs) > 0 {
		result.Status = "submitted"
		result.TaskIDs = taskIDs
		message = "批量" + action + "已提交，请在 OpenList 查看任务进度和结果"
	}
	c.JSON(http.StatusOK, APIResponse[fileTransferResult]{Code: Success, Message: message, Data: result})
}

// DeleteFiles 批量删除网盘文件或目录。
func DeleteFiles(c *gin.Context) {
	var req requests.DeleteFilesRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	fileIDs := req.NormalizedFileIDs(account.SourceType)
	invalidateParentID := req.ParentID
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		err = check115FileOperationResult(client.Del(context.Background(), fileIDs, req.ParentID))
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		err = client.Del(context.Background(), fileIDs)
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		var names []string
		invalidateParentID, names, err = splitOpenListFileIDs(req.ParentID, fileIDs)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		err = client.Del(invalidateParentID, names)
	default:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的文件系统", Data: nil})
		return
	}
	// 上游可能已删除部分条目，即使返回错误也不能继续使用旧列表。
	for _, fileID := range fileIDs {
		invalidateNetFileCacheForDeletedPath(account.SourceType, req.AccountID, invalidateParentID, fileID)
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "批量删除失败：" + helpers.RedactSensitiveLog(err.Error(), account.Token, account.RefreshToken, account.Password), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "批量删除成功", Data: nil})
}

// buildBaiduTransferItems 构造百度网盘批量移动 / 复制条目，保持原文件名并补全路径分隔符。
func buildBaiduTransferItems(fileIDs []string, targetParentID string) []baidupan.MoveOrCopyItem {
	dest := "/" + strings.TrimPrefix(targetParentID, "/")
	items := make([]baidupan.MoveOrCopyItem, 0, len(fileIDs))
	for _, fileID := range fileIDs {
		source := "/" + strings.TrimPrefix(fileID, "/")
		items = append(items, baidupan.MoveOrCopyItem{
			Path:    source,
			Dest:    dest,
			NewName: pathpkg.Base(source),
		})
	}
	return items
}

// MoveFiles 批量移动网盘文件或目录。
func MoveFiles(c *gin.Context) {
	var req requests.MoveFilesRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	fileIDs := req.NormalizedFileIDs(account.SourceType)
	var taskIDs []string
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		err = check115FileOperationResult(client.Move(context.Background(), fileIDs, strings.TrimSpace(req.TargetParentID)))
		for _, fileID := range fileIDs {
			netFileCache.InvalidatePathTree(string(account.SourceType), req.AccountID, fileID)
		}
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		err = client.MoveBatch(context.Background(), buildBaiduTransferItems(fileIDs, req.TargetParentID))
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		var srcDir string
		var names []string
		srcDir, names, err = splitOpenListFileIDs(req.ParentID, fileIDs)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		taskIDs, err = client.MoveWithTasks(srcDir, normalizeOpenListPath(req.TargetParentID), names)
	default:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的文件系统", Data: nil})
		return
	}
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.ParentID)
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.TargetParentID)
	for _, fileID := range fileIDs {
		targetPath := joinOpenListPath(req.TargetParentID, pathpkg.Base(normalizeOpenListPath(fileID)))
		invalidateNetFileCacheForChangedPaths(account.SourceType, req.AccountID, fileID, targetPath)
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "批量移动失败：" + helpers.RedactSensitiveLog(err.Error(), account.Token, account.RefreshToken, account.Password), Data: nil})
		return
	}
	sendFileTransferResult(c, "移动", taskIDs)
}

// CopyFiles 批量复制网盘文件或目录。同名冲突不覆盖，由网盘服务端处理。
func CopyFiles(c *gin.Context) {
	var req requests.CopyFilesRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	fileIDs := req.NormalizedFileIDs(account.SourceType)
	var taskIDs []string
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		err = check115FileOperationResult(client.Copy(context.Background(), fileIDs, strings.TrimSpace(req.TargetParentID), false))
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		err = client.CopyBatch(context.Background(), buildBaiduTransferItems(fileIDs, req.TargetParentID))
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		var srcDir string
		var names []string
		srcDir, names, err = splitOpenListFileIDs(req.ParentID, fileIDs)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		taskIDs, err = client.CopyWithTasks(srcDir, normalizeOpenListPath(req.TargetParentID), names)
	default:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的文件系统", Data: nil})
		return
	}
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.TargetParentID)
	for _, fileID := range fileIDs {
		targetPath := joinOpenListPath(req.TargetParentID, pathpkg.Base(normalizeOpenListPath(fileID)))
		invalidateNetFileCacheForChangedPaths(account.SourceType, req.AccountID, targetPath)
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "批量复制失败：" + helpers.RedactSensitiveLog(err.Error(), account.Token, account.RefreshToken, account.Password), Data: nil})
		return
	}
	sendFileTransferResult(c, "复制", taskIDs)
}

// RenameFile 重命名网盘文件或目录。
func RenameFile(c *gin.Context) {
	var req requests.RenameFileRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号失败：" + err.Error(), Data: nil})
		return
	}
	fileID := req.FileID
	if account.SourceType == models.SourceType115 {
		fileID = strings.TrimSpace(fileID)
	}
	newName := strings.TrimSpace(req.NewName)
	switch account.SourceType {
	case models.SourceType115:
		client := account.Get115Client()
		err = check115FileOperationResult(client.ReName(context.Background(), fileID, newName))
		netFileCache.InvalidatePathTree(string(account.SourceType), req.AccountID, fileID)
	case models.SourceTypeBaiduPan:
		client := account.GetBaiDuPanClient()
		err = client.Rename(context.Background(), fileID, newName)
	case models.SourceTypeOpenList:
		client := account.GetOpenListClient()
		var srcDir string
		var names []string
		srcDir, names, err = splitOpenListFileIDs(req.ParentID, []string{fileID})
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		err = client.RenameNoReplace(joinOpenListPath(srcDir, names[0]), newName)
	default:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的文件系统", Data: nil})
		return
	}
	invalidateNetFileCacheForPath(account.SourceType, req.AccountID, req.ParentID)
	newPath := joinOpenListPath(pathpkg.Dir(normalizeOpenListPath(fileID)), newName)
	invalidateNetFileCacheForChangedPaths(account.SourceType, req.AccountID, fileID, newPath)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重命名失败：" + helpers.RedactSensitiveLog(err.Error(), account.Token, account.RefreshToken, account.Password), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "重命名成功", Data: nil})
}
