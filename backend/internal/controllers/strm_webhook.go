package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	pathpkg "path"
	"strings"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type strmWebhookFileDetailResolver func(context.Context, *models.Account, string) (*v115open.FileDetail, error)
type strmWebhookFileDetailByIDResolver func(context.Context, *models.Account, string) (*v115open.FileDetail, error)

var resolveStrmWebhookFileDetail strmWebhookFileDetailResolver = defaultStrmWebhookFileDetailResolver
var resolveStrmWebhookFileDetailByID strmWebhookFileDetailByIDResolver = defaultStrmWebhookFileDetailByIDResolver

const (
	strmWebhookActionFile          = "file"
	strmWebhookActionBatchFiles    = "batch_files"
	strmWebhookActionDirectoryScan = "directory_scan"

	strmWebhookBatchTransactionMaxAttempts = 4
	strmWebhookBatchTransactionRetryDelay  = 10 * time.Millisecond
)

type strmWebhookRequest struct {
	SyncPathID   uint   `json:"sync_path_id"`
	Action       string `json:"action"`
	LocalPath    string `json:"local_path"`
	DownloadMeta bool   `json:"download_meta"`
	RefreshEmby  bool   `json:"refresh_emby"`

	strmWebhookFileItem

	Items         []strmWebhookFileItem `json:"items"`
	DirectoryID   string                `json:"directory_id"`
	DirectoryPath string                `json:"directory_path"`
}

type strmWebhookFileItem struct {
	FileID    string `json:"file_id"`
	PickCode  string `json:"pick_code"`
	ParentID  string `json:"parent_id"`
	Path      string `json:"path"`
	FileName  string `json:"file_name"`
	FileSize  int64  `json:"file_size"`
	Sha1      string `json:"sha1"`
	Mtime     int64  `json:"mtime"`
	LocalPath string `json:"local_path"`

	ItemDownloadMeta *bool `json:"download_meta"`
	ItemRefreshEmby  *bool `json:"refresh_emby"`
}

type strmWebhookResponse struct {
	RequestID     string                  `json:"request_id"`
	TaskIDs       []uint                  `json:"task_ids"`
	AcceptedCount int                     `json:"accepted_count"`
	FailedCount   int                     `json:"failed_count"`
	Results       []strmWebhookItemResult `json:"results"`
}

type strmWebhookItemResult struct {
	Index    int    `json:"index"`
	Accepted bool   `json:"accepted"`
	TaskID   uint   `json:"task_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

type strmWebhookOptions struct {
	DownloadMeta bool
	RefreshEmby  bool
}

type strmWebhookPreparedFile struct {
	index int
	item  strmWebhookFileItem
}

// StrmWebhook 接收外部请求创建 STRM 生成任务。
func StrmWebhook(c *gin.Context) {
	apiKey, err := authenticateStrmWebhookAPIKey(c)
	if err != nil {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "API Key 无效", Data: nil})
		return
	}
	_ = apiKey.UpdateLastUsedAt()

	var req strmWebhookRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("请求参数错误：%v", err), Data: nil})
		return
	}
	resp, err := handleStrmWebhookRequest(c.Request.Context(), req)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[strmWebhookResponse]{Code: Success, Message: "STRM 生成任务已接收", Data: resp})
}

func authenticateStrmWebhookAPIKey(c *gin.Context) (*models.ApiKey, error) {
	raw := c.GetHeader(apiKeyHeaderName)
	if raw == "" {
		raw = c.Query("api_key")
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("API Key 为空")
	}
	return models.ValidateAPIKey(raw)
}

func handleStrmWebhookRequest(ctx context.Context, req strmWebhookRequest) (strmWebhookResponse, error) {
	if strings.TrimSpace(req.LocalPath) != "" {
		return strmWebhookResponse{}, errors.New("不允许通过 local_path 指定本地写入路径")
	}
	action := inferStrmWebhookAction(req)
	if action == strmWebhookActionBatchFiles {
		if len(req.Items) == 0 {
			return strmWebhookResponse{}, errors.New("items 不能为空")
		}
		if err := validateStrmWebhookItemOptions(req.Items); err != nil {
			return strmWebhookResponse{}, err
		}
	}
	syncPath, err := resolveStrmWebhookSyncPath(req, action)
	if err != nil {
		return strmWebhookResponse{}, err
	}
	options := strmWebhookOptions{
		DownloadMeta: req.DownloadMeta,
		RefreshEmby:  req.RefreshEmby,
	}
	resp := strmWebhookResponse{RequestID: "strm_" + helpers.RandStr(16)}
	switch action {
	case strmWebhookActionFile:
		result := enqueueStrmWebhookFile(ctx, syncPath, 0, options, 0, req.strmWebhookFileItem)
		if !result.Accepted {
			return strmWebhookResponse{}, errors.New(result.Error)
		}
		resp.Results = append(resp.Results, result)
	case strmWebhookActionBatchFiles:
		preparedFiles, results := prepareStrmWebhookBatchFiles(ctx, syncPath, req.Items)
		if len(preparedFiles) > 0 {
			if err := enqueueStrmWebhookBatch(syncPath, options, preparedFiles, results); err != nil {
				return strmWebhookResponse{}, err
			}
		}
		resp.Results = append(resp.Results, results...)
	case strmWebhookActionDirectoryScan:
		result := enqueueStrmWebhookDirectory(ctx, syncPath, req)
		if !result.Accepted {
			return strmWebhookResponse{}, errors.New(result.Error)
		}
		resp.Results = append(resp.Results, result)
	default:
		return strmWebhookResponse{}, fmt.Errorf("不支持的 action：%s", action)
	}
	for _, result := range resp.Results {
		if result.Accepted {
			resp.AcceptedCount++
			resp.TaskIDs = append(resp.TaskIDs, result.TaskID)
		} else {
			resp.FailedCount++
		}
	}
	if helpers.AppLogger != nil {
		helpers.AppLogger.Infof(
			"[STRM Webhook] 接收到 STRM 任务：request_id=%s action=%s sync_path_id=%d download_meta=%t refresh_emby=%t accepted=%d failed=%d task_ids=%v",
			resp.RequestID,
			action,
			syncPath.ID,
			options.DownloadMeta,
			options.RefreshEmby,
			resp.AcceptedCount,
			resp.FailedCount,
			resp.TaskIDs,
		)
	}
	return resp, nil
}

func validateStrmWebhookItemOptions(items []strmWebhookFileItem) error {
	for _, item := range items {
		if item.ItemDownloadMeta != nil || item.ItemRefreshEmby != nil {
			return errors.New("items[] 不允许设置 download_meta 或 refresh_emby，请使用请求顶层字段统一控制")
		}
	}
	return nil
}

func loadStrmWebhookSyncPath(syncPathID uint) (*models.SyncPath, error) {
	if syncPathID == 0 {
		return nil, errors.New("sync_path_id 不能为空")
	}
	syncPath := models.GetSyncPathById(syncPathID)
	if syncPath == nil {
		return nil, fmt.Errorf("同步目录不存在：%d", syncPathID)
	}
	if syncPath.SourceType != models.SourceType115 {
		return nil, fmt.Errorf("sync_path_id 必须指向 115 同步目录：%d", syncPathID)
	}
	return syncPath, nil
}

func resolveStrmWebhookSyncPath(req strmWebhookRequest, action string) (*models.SyncPath, error) {
	if req.SyncPathID != 0 {
		return loadStrmWebhookSyncPath(req.SyncPathID)
	}
	switch action {
	case strmWebhookActionFile:
		item := normalizeStrmWebhookFileItem(req.strmWebhookFileItem)
		if item.Path == "" || item.FileName == "" {
			return nil, errors.New("sync_path_id 为空时 file 请求必须提供 path + file_name")
		}
		return matchStrmWebhookSyncPathByRemotePath(item.Path)
	case strmWebhookActionBatchFiles:
		return resolveStrmWebhookBatchSyncPath(req.Items)
	case strmWebhookActionDirectoryScan:
		directoryPath := normalizeRemotePath(req.DirectoryPath)
		if directoryPath == "" {
			return nil, errors.New("sync_path_id 为空时 directory_scan 请求必须提供 directory_path")
		}
		return matchStrmWebhookSyncPathByRemotePath(directoryPath)
	default:
		return nil, fmt.Errorf("不支持的 action：%s", action)
	}
}

func resolveStrmWebhookBatchSyncPath(items []strmWebhookFileItem) (*models.SyncPath, error) {
	var matched *models.SyncPath
	for index, item := range items {
		item = normalizeStrmWebhookFileItem(item)
		if item.Path == "" || item.FileName == "" {
			return nil, fmt.Errorf("sync_path_id 为空时 items[%d] 必须提供 path + file_name", index)
		}
		syncPath, err := matchStrmWebhookSyncPathByRemotePath(item.Path)
		if err != nil {
			return nil, err
		}
		if matched == nil {
			matched = syncPath
			continue
		}
		if matched.ID != syncPath.ID {
			return nil, errors.New("batch_files 自动匹配到多个同步目录，请拆分请求或显式提供 sync_path_id")
		}
	}
	if matched == nil {
		return nil, errors.New("items 不能为空")
	}
	return matched, nil
}

func matchStrmWebhookSyncPathByRemotePath(remotePath string) (*models.SyncPath, error) {
	remotePath = normalizeRemotePath(remotePath)
	if remotePath == "" {
		return nil, errors.New("远端路径为空，无法自动匹配同步目录")
	}
	var syncPaths []models.SyncPath
	if err := db.Db.Where("source_type = ?", models.SourceType115).Find(&syncPaths).Error; err != nil {
		return nil, fmt.Errorf("查询同步目录失败：%w", err)
	}

	var matched *models.SyncPath
	matchedBaseLen := -1
	ambiguous := false
	for index := range syncPaths {
		syncPath := &syncPaths[index]
		basePath := normalizeRemotePath(syncPath.RemotePath)
		if !remotePathWithin(remotePath, basePath) {
			continue
		}
		baseLen := len(basePath)
		if baseLen > matchedBaseLen {
			matched = syncPath
			matchedBaseLen = baseLen
			ambiguous = false
			continue
		}
		if baseLen == matchedBaseLen {
			ambiguous = true
		}
	}
	if matched == nil {
		return nil, fmt.Errorf("远端路径 %s 未匹配到 115 同步目录，请显式提供 sync_path_id", remotePath)
	}
	if ambiguous {
		return nil, fmt.Errorf("远端路径 %s 匹配到多个同步目录，请显式提供 sync_path_id", remotePath)
	}
	matched.ParseVideoAndMetaExt()
	return matched, nil
}

func inferStrmWebhookAction(req strmWebhookRequest) string {
	action := strings.TrimSpace(req.Action)
	if action != "" {
		return action
	}
	if len(req.Items) > 0 {
		return strmWebhookActionBatchFiles
	}
	if req.DirectoryID != "" || req.DirectoryPath != "" {
		return strmWebhookActionDirectoryScan
	}
	return strmWebhookActionFile
}

func enqueueStrmWebhookFile(ctx context.Context, syncPath *models.SyncPath, parentTaskID uint, options strmWebhookOptions, index int, item strmWebhookFileItem) strmWebhookItemResult {
	prepared, result := prepareStrmWebhookFileItem(ctx, syncPath, index, item)
	if !result.Accepted {
		return result
	}
	return createStrmWebhookFileTask(syncPath, parentTaskID, options, index, prepared.item)
}

func prepareStrmWebhookBatchFiles(ctx context.Context, syncPath *models.SyncPath, items []strmWebhookFileItem) ([]strmWebhookPreparedFile, []strmWebhookItemResult) {
	preparedFiles := make([]strmWebhookPreparedFile, 0, len(items))
	results := make([]strmWebhookItemResult, len(items))
	for index, item := range items {
		prepared, result := prepareStrmWebhookFileItem(ctx, syncPath, index, item)
		results[index] = result
		if result.Accepted {
			preparedFiles = append(preparedFiles, prepared)
		}
	}
	return preparedFiles, results
}

func prepareStrmWebhookFileItem(ctx context.Context, syncPath *models.SyncPath, index int, item strmWebhookFileItem) (strmWebhookPreparedFile, strmWebhookItemResult) {
	if strings.TrimSpace(item.LocalPath) != "" {
		return strmWebhookPreparedFile{}, strmWebhookItemResult{Index: index, Accepted: false, Error: "不允许通过 local_path 指定本地写入路径"}
	}
	item = normalizeStrmWebhookFileItem(item)
	if err := validateStrmWebhookFileItem(syncPath, item); err != nil {
		return strmWebhookPreparedFile{}, strmWebhookItemResult{Index: index, Accepted: false, Error: err.Error()}
	}
	if err := resolveStrmWebhookFileItem(ctx, syncPath, &item); err != nil {
		return strmWebhookPreparedFile{}, strmWebhookItemResult{Index: index, Accepted: false, Error: err.Error()}
	}
	return strmWebhookPreparedFile{index: index, item: item}, strmWebhookItemResult{Index: index, Accepted: true}
}

func createStrmWebhookFileTask(syncPath *models.SyncPath, parentTaskID uint, options strmWebhookOptions, index int, item strmWebhookFileItem) strmWebhookItemResult {
	task, err := models.EnqueueStrmGenerationTaskWithLegacyHashes(
		newStrmWebhookFileTask(
			syncPath,
			parentTaskID,
			options,
			item,
			strmWebhookFileRequestHash(syncPath.ID, parentTaskID, options, item),
		),
		legacyStrmWebhookFileRequestHash(syncPath.ID, parentTaskID, options, item),
	)
	if err != nil {
		return strmWebhookItemResult{Index: index, Accepted: false, Error: err.Error()}
	}
	return strmWebhookItemResult{Index: index, Accepted: true, TaskID: task.ID}
}

func newStrmWebhookFileTask(syncPath *models.SyncPath, parentTaskID uint, options strmWebhookOptions, item strmWebhookFileItem, requestHash string) *models.StrmGenerationTask {
	return &models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceWebhook,
		TaskType:     models.StrmGenerationTaskTypeFile,
		ParentTaskId: parentTaskID,
		SyncPathId:   syncPath.ID,
		AccountId:    syncPath.AccountId,
		DownloadMeta: options.DownloadMeta,
		RefreshEmby:  options.RefreshEmby,
		FileId:       item.FileID,
		ParentId:     item.ParentID,
		PickCode:     item.PickCode,
		Path:         item.Path,
		FileName:     item.FileName,
		FileSize:     item.FileSize,
		Sha1:         item.Sha1,
		Mtime:        item.Mtime,
		Status:       models.StrmGenerationStatusPending,
		RequestHash:  requestHash,
	}
}

func enqueueStrmWebhookBatch(syncPath *models.SyncPath, options strmWebhookOptions, preparedFiles []strmWebhookPreparedFile, results []strmWebhookItemResult) error {
	var err error
	for attempt := 0; attempt < strmWebhookBatchTransactionMaxAttempts; attempt++ {
		err = db.Db.Transaction(func(tx *gorm.DB) error {
			parent, err := enqueueStrmWebhookBatchParentWithDB(tx, syncPath, options, preparedFiles)
			if err != nil {
				return err
			}
			return fillStrmWebhookBatchResultsWithDB(tx, syncPath, parent.ID, options, preparedFiles, results)
		})
		if err == nil {
			return nil
		}
		if !isRetryableSQLiteLockError(err) || attempt == strmWebhookBatchTransactionMaxAttempts-1 {
			return err
		}
		time.Sleep(time.Duration(attempt+1) * strmWebhookBatchTransactionRetryDelay)
	}
	return err
}

func isRetryableSQLiteLockError(err error) bool {
	if err == nil {
		return false
	}
	type sqliteError interface {
		Code() int
	}
	var sqliteErr sqliteError
	if !errors.As(err, &sqliteErr) {
		return false
	}
	switch sqliteErr.Code() & 0xff {
	case 5, 6: // SQLITE_BUSY、SQLITE_LOCKED
		return true
	default:
		return false
	}
}

func enqueueStrmWebhookBatchParentWithDB(tx *gorm.DB, syncPath *models.SyncPath, options strmWebhookOptions, preparedFiles []strmWebhookPreparedFile) (*models.StrmGenerationTask, error) {
	if len(preparedFiles) == 0 {
		return nil, errors.New("批量请求没有合法文件项")
	}
	// 父任务不由 worker 执行，waiting_children 表示子任务尚未全部进入终态。
	return models.EnqueueStrmGenerationTaskWithDBAndLegacyHashes(tx, &models.StrmGenerationTask{
		Source:       models.StrmGenerationSourceWebhook,
		TaskType:     models.StrmGenerationTaskTypeBatchFiles,
		SyncPathId:   syncPath.ID,
		AccountId:    syncPath.AccountId,
		DownloadMeta: options.DownloadMeta,
		RefreshEmby:  options.RefreshEmby,
		TotalItems:   len(preparedFiles),
		Status:       models.StrmGenerationStatusWaitingChildren,
		RequestHash:  strmWebhookBatchRequestHash(syncPath.ID, options, preparedFiles),
	}, legacyStrmWebhookBatchRequestHash(syncPath.ID, options, preparedFiles))
}

func fillStrmWebhookBatchResultsWithDB(tx *gorm.DB, syncPath *models.SyncPath, parentID uint, options strmWebhookOptions, preparedFiles []strmWebhookPreparedFile, results []strmWebhookItemResult) error {
	children, err := getStrmWebhookBatchChildrenWithDB(tx, parentID)
	if err != nil {
		return err
	}
	childrenByRequestHash := make(map[string][]models.StrmGenerationTask, len(children))
	for _, child := range children {
		if child.RequestHash == "" {
			continue
		}
		childrenByRequestHash[child.RequestHash] = append(childrenByRequestHash[child.RequestHash], child)
	}
	for _, prepared := range preparedFiles {
		requestHash := strmWebhookBatchChildRequestHash(syncPath.ID, parentID, options, prepared.index, prepared.item)
		child, ok := takeStrmWebhookBatchChildByRequestHash(childrenByRequestHash, requestHash)
		if !ok {
			for _, legacyHash := range legacyStrmWebhookBatchChildRequestHashes(syncPath.ID, parentID, options, prepared.index, prepared.item) {
				child, ok = takeStrmWebhookBatchChildByRequestHash(childrenByRequestHash, legacyHash)
				if ok {
					break
				}
			}
		}
		if ok {
			results[prepared.index] = strmWebhookItemResult{Index: prepared.index, Accepted: true, TaskID: child.ID}
			continue
		}
		result, err := ensureStrmWebhookBatchChildTaskWithDB(tx, syncPath, parentID, options, prepared.index, prepared.item)
		if err != nil {
			return err
		}
		results[prepared.index] = result
	}
	return nil
}

func takeStrmWebhookBatchChildByRequestHash(children map[string][]models.StrmGenerationTask, requestHash string) (models.StrmGenerationTask, bool) {
	matches := children[requestHash]
	if len(matches) == 0 {
		return models.StrmGenerationTask{}, false
	}
	child := matches[0]
	if len(matches) == 1 {
		delete(children, requestHash)
	} else {
		children[requestHash] = matches[1:]
	}
	return child, true
}

func ensureStrmWebhookBatchChildTaskWithDB(tx *gorm.DB, syncPath *models.SyncPath, parentID uint, options strmWebhookOptions, index int, item strmWebhookFileItem) (strmWebhookItemResult, error) {
	requestHash := strmWebhookBatchChildRequestHash(syncPath.ID, parentID, options, index, item)
	task := newStrmWebhookFileTask(syncPath, parentID, options, item, requestHash)
	created, err := models.EnqueueStrmGenerationTaskWithDB(tx, task)
	if err != nil {
		return strmWebhookItemResult{}, fmt.Errorf("创建批量 STRM 子任务失败: %w", err)
	}
	return strmWebhookItemResult{Index: index, Accepted: true, TaskID: created.ID}, nil
}

func getStrmWebhookBatchChildrenWithDB(tx *gorm.DB, parentID uint) ([]models.StrmGenerationTask, error) {
	if parentID == 0 {
		return nil, nil
	}
	var children []models.StrmGenerationTask
	if err := tx.
		Where("parent_task_id = ?", parentID).
		Order("id ASC").
		Find(&children).Error; err != nil {
		return nil, err
	}
	return children, nil
}

func defaultStrmWebhookFileDetailResolver(ctx context.Context, account *models.Account, fullPath string) (*v115open.FileDetail, error) {
	if account == nil {
		return nil, errors.New("账号为空")
	}
	client := account.Get115Client()
	if client == nil {
		return nil, errors.New("115 客户端为空")
	}
	return client.GetFsDetailByPath(ctx, fullPath)
}

func defaultStrmWebhookFileDetailByIDResolver(ctx context.Context, account *models.Account, fileID string) (*v115open.FileDetail, error) {
	if account == nil {
		return nil, errors.New("账号为空")
	}
	client := account.Get115Client()
	if client == nil {
		return nil, errors.New("115 客户端为空")
	}
	return client.GetFsDetailByCid(ctx, fileID)
}

func resolveStrmWebhookFileItem(ctx context.Context, syncPath *models.SyncPath, item *strmWebhookFileItem) error {
	if item == nil {
		return nil
	}
	account, err := models.GetAccountById(syncPath.AccountId)
	if err != nil {
		return fmt.Errorf("查询同步账号失败: %w", err)
	}
	var detail *v115open.FileDetail
	var requestedFullPath string
	if item.FileID != "" {
		detail, err = resolveStrmWebhookFileDetailByID(ctx, account, item.FileID)
		if err != nil {
			return fmt.Errorf("解析远端文件详情失败: %w", err)
		}
	} else {
		requestedFullPath = pathpkg.Join(item.Path, item.FileName)
		detail, err = resolveStrmWebhookFileDetail(ctx, account, requestedFullPath)
		if err != nil {
			return fmt.Errorf("解析远端文件详情失败: %w", err)
		}
	}
	if detail == nil || strings.TrimSpace(detail.FileId) == "" {
		return errors.New("解析远端文件详情失败：返回空文件 ID")
	}
	applyStrmWebhookFileDetail(item, detail)
	if requestedFullPath != "" {
		if item.FileName == "" {
			item.FileName = pathpkg.Base(requestedFullPath)
		}
		if item.Path == "" {
			item.Path = normalizeRemotePath(pathpkg.Dir(requestedFullPath))
		}
	}
	if item.FileName == "" {
		return errors.New("解析远端文件详情失败：缺少文件名")
	}
	if item.Path == "" {
		return errors.New("解析远端文件详情失败：缺少远端路径")
	}
	if !remotePathWithin(item.Path, syncPath.RemotePath) {
		return fmt.Errorf("远端路径 %s 不在同步远端目录 %s 下", item.Path, normalizeRemotePath(syncPath.RemotePath))
	}
	return nil
}

func applyStrmWebhookFileDetail(item *strmWebhookFileItem, detail *v115open.FileDetail) {
	item.FileID = strings.TrimSpace(detail.FileId)
	item.PickCode = strings.TrimSpace(detail.PickCode)
	item.FileName = strings.TrimSpace(detail.FileName)
	item.Path = normalizeRemotePath(detail.Path)
	item.ParentID = strmWebhookDetailParentID(detail)
	if detail.FileSizeByte > 0 {
		item.FileSize = detail.FileSizeByte
	} else {
		item.FileSize = helpers.StringToInt64(detail.FileSize)
	}
	item.Sha1 = strings.TrimSpace(detail.Sha1)
	item.Mtime = detail.ModifiedAt()
}

func strmWebhookDetailParentID(detail *v115open.FileDetail) string {
	if detail == nil || len(detail.Paths) == 0 {
		return ""
	}
	return strings.TrimSpace(detail.Paths[len(detail.Paths)-1].FileId)
}

func normalizeStrmWebhookFileItem(item strmWebhookFileItem) strmWebhookFileItem {
	item.FileID = strings.TrimSpace(item.FileID)
	item.PickCode = strings.TrimSpace(item.PickCode)
	item.ParentID = strings.TrimSpace(item.ParentID)
	item.Path = normalizeRemotePath(item.Path)
	item.FileName = strings.TrimSpace(item.FileName)
	item.Sha1 = strings.TrimSpace(item.Sha1)
	item.LocalPath = strings.TrimSpace(item.LocalPath)
	return item
}

func validateStrmWebhookFileItem(syncPath *models.SyncPath, item strmWebhookFileItem) error {
	hasPathName := item.Path != "" && item.FileName != ""
	if item.FileID == "" && !hasPathName {
		if item.PickCode != "" {
			return errors.New("仅提供 pick_code 无法生成 STRM，请提供 file_id 或 path + file_name")
		}
		return errors.New("file_id 或 path + file_name 至少需要提供一组")
	}
	if item.Path != "" && !remotePathWithin(item.Path, syncPath.RemotePath) {
		return fmt.Errorf("远端路径 %s 不在同步远端目录 %s 下", item.Path, normalizeRemotePath(syncPath.RemotePath))
	}
	return nil
}

func enqueueStrmWebhookDirectory(ctx context.Context, syncPath *models.SyncPath, req strmWebhookRequest) strmWebhookItemResult {
	directoryID := strings.TrimSpace(req.DirectoryID)
	directoryPath := normalizeRemotePath(req.DirectoryPath)
	if directoryID == "" && directoryPath == "" {
		return strmWebhookItemResult{Index: 0, Accepted: false, Error: "directory_id 或 directory_path 至少需要提供一个"}
	}
	if directoryPath != "" && !remotePathWithin(directoryPath, syncPath.RemotePath) {
		return strmWebhookItemResult{Index: 0, Accepted: false, Error: fmt.Sprintf("远端目录 %s 不在同步远端目录 %s 下", directoryPath, normalizeRemotePath(syncPath.RemotePath))}
	}
	if err := validateStrmWebhookDirectoryDetail(ctx, syncPath, directoryID, directoryPath); err != nil {
		return strmWebhookItemResult{Index: 0, Accepted: false, Error: err.Error()}
	}
	options := strmWebhookOptions{DownloadMeta: req.DownloadMeta, RefreshEmby: req.RefreshEmby}
	task, err := models.EnqueueStrmGenerationTaskWithLegacyHashes(&models.StrmGenerationTask{
		Source:        models.StrmGenerationSourceWebhook,
		TaskType:      models.StrmGenerationTaskTypeDirectoryScan,
		SyncPathId:    syncPath.ID,
		AccountId:     syncPath.AccountId,
		DownloadMeta:  options.DownloadMeta,
		RefreshEmby:   options.RefreshEmby,
		DirectoryId:   directoryID,
		DirectoryPath: directoryPath,
		RequestHash:   strmWebhookDirectoryRequestHash(syncPath.ID, options, directoryID, directoryPath),
	}, legacyStrmWebhookDirectoryRequestHash(syncPath.ID, options, directoryID, directoryPath))
	if err != nil {
		return strmWebhookItemResult{Index: 0, Accepted: false, Error: err.Error()}
	}
	return strmWebhookItemResult{Index: 0, Accepted: true, TaskID: task.ID}
}

func validateStrmWebhookDirectoryDetail(ctx context.Context, syncPath *models.SyncPath, directoryID string, directoryPath string) error {
	if directoryID == "" || directoryPath == "" {
		return nil
	}
	account, err := models.GetAccountById(syncPath.AccountId)
	if err != nil {
		return fmt.Errorf("查询同步账号失败: %w", err)
	}
	detail, err := resolveStrmWebhookFileDetailByID(ctx, account, directoryID)
	if err != nil {
		return fmt.Errorf("解析远端目录详情失败: %w", err)
	}
	if detail == nil {
		return errors.New("解析远端目录详情失败：目录不存在")
	}
	detailID := strings.TrimSpace(detail.FileId)
	if detailID == "" {
		return errors.New("解析远端目录详情失败：返回空目录 ID")
	}
	if detailID != directoryID {
		return fmt.Errorf("directory_id %s 解析为远端目录 %s，不一致", directoryID, detailID)
	}
	if detail.FileCategory != v115open.TypeDir {
		return fmt.Errorf("directory_id %s 指向的远端对象不是目录", directoryID)
	}
	detailPath := strmWebhookDirectoryDetailRemotePath(detail)
	if detailPath == "" {
		return errors.New("解析远端目录详情失败：缺少远端路径")
	}
	if detailPath != directoryPath {
		return fmt.Errorf("directory_id %s 对应远端目录 %s，与 directory_path %s 不一致", directoryID, detailPath, directoryPath)
	}
	if !remotePathWithin(detailPath, syncPath.RemotePath) {
		return fmt.Errorf("远端目录 %s 不在同步远端目录 %s 下", detailPath, normalizeRemotePath(syncPath.RemotePath))
	}
	return nil
}

func strmWebhookDirectoryDetailRemotePath(detail *v115open.FileDetail) string {
	if detail == nil {
		return ""
	}
	parentPath := normalizeRemotePath(detail.Path)
	directoryName := strings.TrimSpace(detail.FileName)
	if directoryName == "" {
		return parentPath
	}
	if parentPath == "" || parentPath == "/" {
		return normalizeRemotePath(pathpkg.Join("/", directoryName))
	}
	return normalizeRemotePath(pathpkg.Join(parentPath, directoryName))
}

func strmWebhookFileRequestHash(syncPathID uint, parentTaskID uint, options strmWebhookOptions, item strmWebhookFileItem) string {
	return models.BuildStrmRequestHash(
		"webhook:file",
		fmt.Sprint(syncPathID),
		fmt.Sprint(parentTaskID),
		fmt.Sprint(options.DownloadMeta),
		fmt.Sprint(options.RefreshEmby),
		item.FileID,
		item.PickCode,
		item.Path,
		item.FileName,
	)
}

func legacyStrmWebhookFileRequestHash(syncPathID uint, parentTaskID uint, options strmWebhookOptions, item strmWebhookFileItem) string {
	return fmt.Sprintf(
		"webhook:file:%d:%d:%t:%t:%s:%s:%s:%s",
		syncPathID,
		parentTaskID,
		options.DownloadMeta,
		options.RefreshEmby,
		item.FileID,
		item.PickCode,
		item.Path,
		item.FileName,
	)
}

func strmWebhookBatchRequestHash(syncPathID uint, options strmWebhookOptions, preparedFiles []strmWebhookPreparedFile) string {
	fields := []string{
		fmt.Sprint(syncPathID),
		fmt.Sprint(options.DownloadMeta),
		fmt.Sprint(options.RefreshEmby),
	}
	for _, prepared := range preparedFiles {
		item := prepared.item
		fields = append(
			fields,
			fmt.Sprint(prepared.index),
			item.FileID,
			item.PickCode,
			item.Path,
			item.FileName,
		)
	}
	return models.BuildStrmRequestHash("webhook:batch", fields...)
}

func legacyStrmWebhookBatchRequestHash(syncPathID uint, options strmWebhookOptions, preparedFiles []strmWebhookPreparedFile) string {
	var builder strings.Builder
	for _, prepared := range preparedFiles {
		item := prepared.item
		builder.WriteString(fmt.Sprintf(
			"%d:%s:%s:%s:%s\n",
			prepared.index,
			item.FileID,
			item.PickCode,
			item.Path,
			item.FileName,
		))
	}
	return fmt.Sprintf(
		"webhook:batch:%d:%t:%t:%s",
		syncPathID,
		options.DownloadMeta,
		options.RefreshEmby,
		helpers.MD5Hash(builder.String()),
	)
}

func strmWebhookBatchChildRequestHash(syncPathID uint, parentTaskID uint, options strmWebhookOptions, index int, item strmWebhookFileItem) string {
	return models.BuildStrmRequestHash(
		"webhook:batch_file",
		fmt.Sprint(syncPathID),
		fmt.Sprint(parentTaskID),
		fmt.Sprint(index),
		fmt.Sprint(options.DownloadMeta),
		fmt.Sprint(options.RefreshEmby),
		item.FileID,
		item.PickCode,
		item.Path,
		item.FileName,
	)
}

func legacyStrmWebhookBatchChildRequestHashes(syncPathID uint, parentTaskID uint, options strmWebhookOptions, index int, item strmWebhookFileItem) []string {
	return []string{
		legacyStrmWebhookBatchChildRequestHash(syncPathID, parentTaskID, options, index, item),
		legacyStrmWebhookFileRequestHash(syncPathID, parentTaskID, options, item),
	}
}

func legacyStrmWebhookBatchChildRequestHash(syncPathID uint, parentTaskID uint, options strmWebhookOptions, index int, item strmWebhookFileItem) string {
	return fmt.Sprintf(
		"webhook:batch_file:%d:%d:%d:%t:%t:%s:%s:%s:%s",
		syncPathID,
		parentTaskID,
		index,
		options.DownloadMeta,
		options.RefreshEmby,
		item.FileID,
		item.PickCode,
		item.Path,
		item.FileName,
	)
}

func strmWebhookDirectoryRequestHash(syncPathID uint, options strmWebhookOptions, directoryID string, directoryPath string) string {
	return models.BuildStrmRequestHash(
		"webhook:directory",
		fmt.Sprint(syncPathID),
		fmt.Sprint(options.DownloadMeta),
		fmt.Sprint(options.RefreshEmby),
		directoryID,
		directoryPath,
	)
}

func legacyStrmWebhookDirectoryRequestHash(syncPathID uint, options strmWebhookOptions, directoryID string, directoryPath string) string {
	return fmt.Sprintf(
		"webhook:directory:%d:%t:%t:%s:%s",
		syncPathID,
		options.DownloadMeta,
		options.RefreshEmby,
		directoryID,
		directoryPath,
	)
}

func normalizeRemotePath(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return pathpkg.Clean(value)
}

func remotePathWithin(remotePath string, basePath string) bool {
	remotePath = normalizeRemotePath(remotePath)
	basePath = normalizeRemotePath(basePath)
	if remotePath == "" || basePath == "" {
		return false
	}
	if basePath == "/" {
		return strings.HasPrefix(remotePath, "/")
	}
	return remotePath == basePath || strings.HasPrefix(remotePath, basePath+"/")
}
