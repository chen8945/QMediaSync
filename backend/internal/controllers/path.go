package controllers

import (
	"context"
	"fmt"
	"net/http"
	"os"
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
	Id   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// GetPathList 获取目录列表
// @Summary 获取目录列表
// @Description 按同步源类型获取本地、OpenList 或 115 的目录列表
// @Tags 路径管理
// @Accept json
// @Produce json
// @Param parent_id query string false "父目录 ID，仅 115 使用"
// @Param parent_path query string false "父目录路径，本地或 OpenList 使用"
// @Param source_type query integer true "同步源类型，0-本地 1-115 2-OpenList"
// @Param account_id query integer false "账号 ID，115 或 OpenList 必填"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /path/list [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetPathList(c *gin.Context) {
	var req requests.PathListRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	var pathes []DirResp
	var err error
	switch req.SourceType {
	case models.SourceTypeLocal:
		pathes, err = GetLocalPath(req.ParentID)
	case models.SourceTypeOpenList:
		pathes, err = GetOpenListPath(req.ParentID, req.AccountID)
	case models.SourceType115:
		pathes, err = Get115PathList(req.ParentID, req.AccountID)
	case models.SourceTypeBaiduPan:
		pathes, err = GetBaiduPanPathList(req.ParentID, req.AccountID)
	default:
		// 报错
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "未知的同步源类型", Data: nil})
		return
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取目录列表失败：" + err.Error(), Data: nil})
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
					paths := strings.Split(accessiblePaths, ":")
					for _, path := range paths {
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
			pathes = append(pathes, DirResp{
				Id:   fullPath,
				Name: entry.Name(),
				Path: fullPath,
			})
		}
	}

	return pathes, nil
}

func GetOpenListPath(parentPath string, accountId uint) ([]DirResp, error) {
	account, err := models.GetAccountById(accountId)
	if err != nil {
		return nil, err
	}
	// 去掉 parentPath 末尾的 /
	parentPath = strings.TrimSuffix(parentPath, "/")
	parentPath = strings.TrimSuffix(parentPath, "\\")

	helpers.AppLogger.Debugf("开始获取 OpenList 目录列表，父目录路径：%s", parentPath)
	client := account.GetOpenListClient()
	resp, err := client.FileList(context.Background(), parentPath, 1, 100)
	if err != nil {
		return nil, err
	}
	// 只返回文件夹列表
	folders := make([]DirResp, 0)
	for _, item := range resp.Content {
		if item.IsDir {
			folders = append(folders, DirResp{
				Id:   parentPath + "/" + item.Name,
				Name: item.Name,
				Path: parentPath + "/" + item.Name,
			})
		}
	}
	return folders, nil
}

func Get115PathList(parentId string, accountId uint) ([]DirResp, error) {
	// 获取 115 目录列表
	account, err := models.GetAccountById(accountId)
	if err != nil {
		return nil, err
	}
	client := account.Get115Client()
	helpers.AppLogger.Debugf("开始获取 115 目录列表，父目录 ID：%s", parentId)
	ctx := context.Background()
	resp, err := client.GetFsList(ctx, parentId, true, true, true, 0, 200)
	if err != nil {
		helpers.AppLogger.Warnf("获取 115 目录列表失败：父目录=%s，错误=%v", parentId, err)
		return nil, err
	}
	helpers.AppLogger.Debugf("成功获取 115 目录列表，父目录 ID：%s，文件数量：%d", parentId, len(resp.Data))
	folders := make([]DirResp, 0)
	// 构建路径
	for _, item := range resp.Data {
		parentPath := resp.PathStr
		if parentPath == "" {
			parentPath = ""
		}
		helpers.AppLogger.Debugf("遍历 %s 的 115 目录列表，路径：%s", parentPath, item.FileName)
		if item.FileCategory == v115open.TypeDir {
			folders = append(folders, DirResp{
				Id:   item.FileId,
				Name: item.FileName,
				Path: filepath.ToSlash(filepath.Join(parentPath, item.FileName)),
			})
		}
	}
	return folders, nil
}

func GetBaiduPanPathList(parentId string, accountId uint) ([]DirResp, error) {
	// 获取百度网盘目录列表
	account, err := models.GetAccountById(accountId)
	if err != nil {
		return nil, err
	}
	client := account.GetBaiDuPanClient()
	ctx := context.Background()
	fileList, fileErr := client.GetFileList(ctx, parentId, 1, 1, 0, 1000)
	if fileErr != nil {
		helpers.AppLogger.Warnf("获取百度网盘目录列表失败：父目录=%s，错误=%v", parentId, fileErr)
		return nil, fileErr
	}
	// helpers.AppLogger.Infof("成功获取百度网盘文件列表，父目录 ID：%s，文件数量：%d", parentId, len(resp.Data))
	items := make([]DirResp, 0)
	// 构建路径
	for _, item := range fileList {
		// 去掉 item.Path 开头的 /
		item.Path = strings.TrimPrefix(item.Path, "/")
		items = append(items, DirResp{
			Id:   item.Path,
			Name: filepath.Base(item.Path),
			Path: item.Path,
		})
	}
	return items, nil
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取账号信息失败：" + err.Error(), Data: nil})
		return
	}
	resp, err := getNetFileListPage(c.Request.Context(), netFileListQuery{
		Account:   account,
		ParentID:  req.ParentID,
		Page:      req.Page,
		PageSize:  req.PageSize,
		Refresh:   req.Refresh,
		SortBy:    req.SortBy,
		SortOrder: req.SortOrder,
	})
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取目录列表失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[netFileListResponse]{
		Code:    Success,
		Message: "",
		Data:    resp,
	})
}

type netFileListQuery struct {
	Account   *models.Account
	ParentID  string
	Page      int
	PageSize  int
	Refresh   bool
	SortBy    string
	SortOrder string
}

func getNetFileListPage(ctx context.Context, query netFileListQuery) (netFileListResponse, error) {
	if query.Account == nil {
		return netFileListResponse{}, fmt.Errorf("账号不能为空")
	}
	sortBy := normalizeNetFileSort(query.Account.SourceType, query.SortBy)
	sortOrder := query.SortOrder
	if sortOrder == "" {
		sortOrder = "asc"
	}
	capability, err := getNetFileSourceCapability(query.Account.SourceType, sortBy, sortOrder)
	if err != nil {
		return netFileListResponse{}, err
	}
	cachePath := normalizeNetFileCachePath(query.Account.SourceType, query.ParentID)
	const filter = "none"
	if query.Refresh {
		netFileCache.InvalidateView(string(query.Account.SourceType), query.Account.ID, cachePath, sortBy, sortOrder, filter)
	}

	ranges := computeNetFileBatchRanges(query.Page, query.PageSize, capability.BatchSize)
	items := make([]*FileItem, 0, query.PageSize)
	total := int64(0)
	hasMore := false
	status := netFileCacheHit
	var firstBatch netFileBatch
	hitCount := 0
	missCount := 0
	now := time.Now()

	for _, batchRange := range ranges {
		key := netFileBatchCacheKey{
			SourceType: string(query.Account.SourceType),
			AccountID:  query.Account.ID,
			Path:       cachePath,
			SortBy:     sortBy,
			SortOrder:  sortOrder,
			Filter:     filter,
			BatchStart: batchRange.Start,
			BatchSize:  batchRange.Size,
		}
		batch, ok := netFileCache.Get(key, now)
		if ok {
			hitCount++
		} else {
			missCount++
			if query.Refresh {
				generation := netFileCache.Generation(key)
				fetched, fetchErr := fetchNetFileBatch(ctx, query.Account, query.ParentID, batchRange.Start, batchRange.Size, sortBy, sortOrder, true)
				if fetchErr != nil {
					return netFileListResponse{}, fetchErr
				}
				netFileCache.SetIfGeneration(key, fetched, time.Now(), generation)
				batch = fetched
			} else {
				result, err, _ := netFileSingleflight.Do(netFileSingleflightKey(key), func() (any, error) {
					generation := netFileCache.Generation(key)
					fetched, fetchErr := fetchNetFileBatch(ctx, query.Account, query.ParentID, batchRange.Start, batchRange.Size, sortBy, sortOrder, false)
					if fetchErr != nil {
						return netFileBatch{}, fetchErr
					}
					netFileCache.SetIfGeneration(key, fetched, time.Now(), generation)
					return fetched, nil
				})
				if err != nil {
					return netFileListResponse{}, err
				}
				var typeOK bool
				batch, typeOK = result.(netFileBatch)
				if !typeOK {
					return netFileListResponse{}, fmt.Errorf("网盘文件缓存结果类型错误")
				}
			}
		}
		if firstBatch.CachedAt == 0 {
			firstBatch = batch
		}
		items = append(items, batch.Items...)
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
	return fmt.Sprintf("%s/%d/%s/%s/%s/%s/%d/%d", key.SourceType, key.AccountID, key.Path, key.SortBy, key.SortOrder, key.Filter, key.BatchStart, key.BatchSize)
}

func fetchNetFileBatch(ctx context.Context, account *models.Account, parentID string, start int, size int, sortBy string, sortOrder string, refresh bool) (netFileBatch, error) {
	switch account.SourceType {
	case models.SourceType115:
		return fetch115NetFileBatch(ctx, account, parentID, start, size, sortBy, sortOrder)
	case models.SourceTypeBaiduPan:
		return fetchBaiduNetFileBatch(ctx, account, parentID, start, size, sortBy, sortOrder)
	case models.SourceTypeOpenList:
		return fetchOpenListNetFileBatch(ctx, account, parentID, start, size, refresh)
	default:
		return netFileBatch{}, fmt.Errorf("未知的网盘类型")
	}
}

func fetch115NetFileBatch(ctx context.Context, account *models.Account, parentID string, start int, size int, sortBy string, sortOrder string) (netFileBatch, error) {
	if parentID == "" {
		parentID = "0"
	}
	order, asc, err := map115Sort(sortBy, sortOrder)
	if err != nil {
		return netFileBatch{}, err
	}
	client := account.Get115Client()
	resp, err := client.GetFsListWithOptions(ctx, parentID, true, false, true, start, size, v115open.FileListOptions{Order: order, Asc: asc})
	if err != nil {
		helpers.AppLogger.Warnf("获取 115 文件列表失败：父目录=%s，错误=%v", parentID, err)
		return netFileBatch{}, err
	}
	items := make([]*FileItem, 0, len(resp.Data))
	for _, item := range resp.Data {
		items = append(items, &FileItem{
			Id:          item.FileId,
			IsDirectory: item.FileCategory == v115open.TypeDir,
			Name:        item.FileName,
			Size:        item.FileSize,
			ModifiedAt:  item.ModifiedAt(),
		})
	}
	return netFileBatch{
		Items:      items,
		Total:      int64(resp.Count),
		TotalExact: true,
		HasMore:    int64(start+len(resp.Data)) < int64(resp.Count),
	}, nil
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
		if len(items) >= size {
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
	return newDir, nil
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
