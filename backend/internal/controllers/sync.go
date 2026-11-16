package controllers

import (
	"context"
	"errors"
	"net/http"
	pathpkg "path"
	"strings"

	"qmediasync/internal/directoryupload"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
	v115open "qmediasync/internal/v115/openapi"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// StartSync 启动同步
func StartSync(c *gin.Context) {
	// 启动同步
	synccron.StartSyncCron()
	httpapi.Success(c, http.StatusAccepted, gin.H{"status": "accepted"}, "同步任务已添加到队列")
}

// GetSyncRecords 获取同步记录列表
func GetSyncRecords(c *gin.Context) {
	type syncRecordsRequest struct {
		Page     int `form:"page" json:"page" binding:"omitempty,min=1"`           // 页码，默认 1
		PageSize int `form:"page_size" json:"page_size" binding:"omitempty,min=1"` // 每页数量，默认 50
	}

	var req syncRecordsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	page := req.Page
	pageSize := req.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}

	// 获取同步记录
	records, total, err := models.GetSyncRecords(page, pageSize)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取同步记录失败", Cause: err, Data: nil})
		return
	}

	httpapi.Success(c, http.StatusOK, map[string]any{
		"records": records,
		"total":   total,
	}, "获取同步记录成功")
}

// GetSyncTask 获取同步任务详情
func GetSyncTask(c *gin.Context) {
	type syncTaskRequest struct {
		SyncID uint `form:"sync_id" json:"sync_id" binding:"required"` // 同步任务 ID
	}
	var req syncTaskRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	sync, err := models.GetSyncByID(req.SyncID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "SYNC_TASK_NOT_FOUND", Message: "未找到对应的同步任务", Cause: err})
		return
	}
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取同步任务失败", Cause: err, Data: nil})
		return
	}

	if sync == nil {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "SYNC_TASK_NOT_FOUND", Message: "未找到对应的同步任务", Cause: nil, Data: nil})
		return
	}

	httpapi.Success(c, http.StatusOK, sync, "获取同步任务详情成功")
}

// GetSyncPathList 获取同步路径列表
func GetSyncPathList(c *gin.Context) {
	type syncPathListRequest struct {
		Page       int               `form:"page" json:"page" binding:"omitempty,min=1"`           // 页码，默认 1
		PageSize   int               `form:"page_size" json:"page_size" binding:"omitempty,min=1"` // 每页数量，默认 20
		SourceType models.SourceType `form:"source_type" json:"source_type" binding:"omitempty"`   // 来源类型
	}
	var req syncPathListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	page := req.Page
	pageSize := req.PageSize
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 20
	}

	syncPaths, total, err := models.GetSyncPathListWithContext(c.Request.Context(), page, pageSize, false, req.SourceType)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取同步路径列表失败", Cause: err})
		return
	}

	for _, sp := range syncPaths {
		status := synccron.CheckNewTaskStatus(sp.ID, synccron.SyncTaskTypeStrm)
		sp.IsRunning = int(status)
	}

	httpapi.Success(c, http.StatusOK, map[string]any{
		"list":      syncPaths,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}, "获取同步路径列表成功")
}

// DeleteSyncPath 删除同步路径
func DeleteSyncPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 删除同步路径
	if err := models.DeleteSyncPathByID(c.Request.Context(), req.ID); err != nil {
		status, code := http.StatusInternalServerError, "INTERNAL_ERROR"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status, code = http.StatusNotFound, "SYNC_PATH_NOT_FOUND"
		}
		httpapi.Failure(c, status, httpapi.Error{Code: code, Message: "删除同步路径失败", Cause: err})
		return
	}
	synccron.InitSyncCron()
	synccron.InitCron()
	directoryupload.ReloadDirectoryUploadService()
	httpapi.Success(c, http.StatusOK, nil, "删除同步路径成功")
}

// GetSyncPathById 根据 ID 获取同步路径详情。
func GetSyncPathById(c *gin.Context) {
	// 从路径参数获取 ID
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	syncPath, ok := loadSyncPathForRequest(c, idReq.ID)
	if !ok {
		return
	}

	httpapi.Success(c, http.StatusOK, syncPath, "获取同步路径详情成功")
}

// DelSyncRecords 批量删除同步记录
func DelSyncRecords(c *gin.Context) {
	var req requests.IDListRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	result := deleteSyncRecordsByIDs(req.NormalizedIDs())
	if len(result.DeletedIDs) > 0 {
		synccron.InitSyncCron()
		synccron.InitCron()
	}
	if len(result.Failures) > 0 {
		message := "部分同步记录删除失败"
		if len(result.DeletedIDs) == 0 {
			message = "删除同步记录失败"
		}
		httpapi.Failure(c, result.status, httpapi.Error{Code: "SYNC_RECORD_DELETE_FAILED", Message: message, Cause: result.cause, Data: result})
		return
	}
	httpapi.Success(c, http.StatusOK, result, "删除同步记录成功")
}

type deleteSyncRecordFailure struct {
	ID     uint   `json:"id"`
	Reason string `json:"reason"`
}

type deleteSyncRecordsResult struct {
	DeletedIDs []uint                    `json:"deleted_ids"`
	Failures   []deleteSyncRecordFailure `json:"failures"`
	status     int
	cause      error
}

func deleteSyncRecordsByIDs(ids []uint) deleteSyncRecordsResult {
	result := deleteSyncRecordsResult{
		DeletedIDs: make([]uint, 0, len(ids)),
		Failures:   make([]deleteSyncRecordFailure, 0),
	}
	for _, id := range ids {
		if err := models.DeleteSyncRecordById(id); err != nil {
			status, reason := http.StatusInternalServerError, "删除同步记录失败"
			if errors.Is(err, models.ErrSyncRecordNotFound) {
				status, reason = http.StatusNotFound, "同步记录不存在"
			} else if errors.Is(err, models.ErrSyncRecordNotDeletable) {
				status, reason = http.StatusConflict, "同步记录未完成，不能删除"
			}
			result.status = max(result.status, status)
			result.cause = errors.Join(result.cause, err)
			result.Failures = append(result.Failures, deleteSyncRecordFailure{ID: id, Reason: reason})
			continue
		}
		result.DeletedIDs = append(result.DeletedIDs, id)
	}
	return result
}

// StartSyncByPath 启动指定路径的同步任务
func StartSyncByPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	syncPath, ok := loadSyncPathForRequest(c, req.ID)
	if !ok {
		return
	}
	// syncPath.SetIsFullSync(false)
	// 添加同步任务到队列
	taskObj := &synccron.NewSyncTask{
		ID:           syncPath.ID,
		SourcePath:   "",
		SourcePathId: "",
		TargetPath:   "",
		AccountId:    syncPath.AccountId,
		SourceType:   syncPath.SourceType,
		IsFile:       false,
		TaskType:     synccron.SyncTaskTypeStrm,
	}
	if err := synccron.AddNewSyncTask(taskObj); err != nil {
		writeTaskEnqueueError(c, err, "添加同步任务失败")
		return
	}

	pathStatus := synccron.CheckNewTaskStatus(syncPath.ID, synccron.SyncTaskTypeStrm)
	httpapi.Success(c, http.StatusAccepted, map[string]any{
		"id":         syncPath.ID,
		"is_running": pathStatus,
	}, "同步任务已添加到队列")
}

// StopSyncByPath 停止指定路径的同步任务
func StopSyncByPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	syncPath, ok := loadSyncPathForRequest(c, req.ID)
	if !ok {
		return
	}
	// syncPath.SetIsFullSync(false)
	synccron.CancelNewSyncTask(syncPath.ID, synccron.SyncTaskTypeStrm)

	httpapi.Success(c, http.StatusOK, nil, "同步任务已停止")
}

// ToggleSyncByPath 切换同步路径的定时任务
func ToggleSyncByPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 切换 enable 参数
	syncPath, ok := loadSyncPathForRequest(c, req.ID)
	if !ok {
		return
	}
	if err := syncPath.ToggleCron(); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "更新定时同步开关失败", Cause: err, Data: nil})
		return
	}
	synccron.InitCron()
	// 重启自定义定时任务
	if syncPath.Cron != "" {
		synccron.InitSyncCron()
	}
	if syncPath.EnableCron {
		httpapi.Success(c, http.StatusOK, nil, "定时同步已开启")
	} else {
		httpapi.Success(c, http.StatusOK, nil, "定时同步已关闭")
	}

}

// FullStart115Sync 启动 115 全量同步
func FullStart115Sync(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	syncPath, ok := loadSyncPathForRequest(c, req.ID)
	if !ok {
		return
	}
	if err := syncPath.SetIsFullSync(true); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "设置全量同步失败", Cause: err, Data: nil})
		return
	}
	// 添加同步任务到队列
	taskObj := &synccron.NewSyncTask{
		ID:           syncPath.ID,
		SourcePath:   "",
		SourcePathId: "",
		TargetPath:   "",
		AccountId:    syncPath.AccountId,
		SourceType:   syncPath.SourceType,
		IsFile:       false,
		TaskType:     synccron.SyncTaskTypeStrm,
	}
	if err := synccron.AddNewSyncTask(taskObj); err != nil {
		writeTaskEnqueueError(c, err, "添加同步任务失败")
		return
	}

	httpapi.Success(c, http.StatusAccepted, gin.H{"status": "accepted"}, "同步任务已添加到队列")
}

func SaveRelScrapePath(c *gin.Context) {
	var req requests.SaveRelScrapePathRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	syncPath, ok := loadSyncPathForRequest(c, req.SyncPathID)
	if !ok {
		return
	}
	// 保存关联的刮削路径
	if err := syncPath.SaveScrapePaths(req.ScrapePathIDs); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存关联的刮削路径失败", Cause: err, Data: nil})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "关联刮削路径已保存")
}

func GetRelScrapePath(c *gin.Context) {
	idStr := c.Param("id")
	idReq, err := requests.ParsePositiveIDRequest(idStr)
	if err != nil {
		message := "ID 参数格式错误"
		if strings.TrimSpace(idStr) == "" {
			message = "ID 参数不能为空"
		}
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: message, Cause: err, Data: nil})
		return
	}
	syncPath, ok := loadSyncPathForRequest(c, idReq.ID)
	if !ok {
		return
	}
	// 获取关联的刮削路径
	scrapePathIds := syncPath.GetScrapePathIds()
	httpapi.Success(c, http.StatusOK, scrapePathIds, "获取关联刮削路径成功")
}

// 从网盘文件管理器手动触发同步
func ManualSync(c *gin.Context) {
	var req requests.ManualSyncRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "读取同步账号失败"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status, code, message = http.StatusNotFound, "ACCOUNT_NOT_FOUND", "账号不存在"
		}
		httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
		return
	}
	if req.Path == "" {
		// 使用文件 ID 查询详情
		switch account.SourceType {
		case models.SourceType115:
			client := account.Get115Client()
			// 115 网盘文件详情接口
			fileDetail, err := client.GetFsDetailByCid(context.Background(), req.PathID)
			if err != nil {
				writeCloudUpstreamError(c, err, "获取文件详情失败", account.Token, account.RefreshToken, account.Password)
				return
			}
			req.Path = resolve115ManualSyncPath(fileDetail)
			req.IsFile = fileDetail.FileCategory == v115open.TypeFile
		case models.SourceTypeBaiduPan:
			client := account.GetBaiDuPanClient()
			// 百度网盘文件详情接口
			fileDetail, err := client.FileExists(context.Background(), req.PathID)
			if err != nil {
				writeCloudUpstreamError(c, err, "获取文件详情失败", account.Token, account.RefreshToken, account.Password)
				return
			}
			// 目标不存在时 FileExists 返回空详情而不是错误
			if fileDetail == nil {
				httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "RESOURCE_NOT_FOUND", Message: "文件不存在", Cause: nil, Data: nil})
				return
			}
			req.Path = fileDetail.Path
			req.IsFile = fileDetail.IsDir == 0
		case models.SourceTypeOpenList:
			client := account.GetOpenListClient()
			// OpenList 以完整路径作为文件 ID，详情只用于确认存在和区分目录
			remotePath := normalizeOpenListPath(req.PathID)
			fileDetail, err := client.FileDetail(remotePath)
			if err != nil {
				writeCloudUpstreamError(c, err, "获取文件详情失败", account.Token, account.RefreshToken, account.Password)
				return
			}
			if fileDetail == nil || strings.TrimSpace(fileDetail.Name) == "" {
				httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "RESOURCE_NOT_FOUND", Message: "文件不存在", Cause: nil, Data: nil})
				return
			}
			req.Path = remotePath
			// 归一后的路径同时作为入口 ID，避免单文件同步和队列去重用到未归一的原始值
			req.PathID = remotePath
			req.IsFile = !fileDetail.IsDir
		default:
			httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "不支持的文件类型", Cause: nil, Data: nil})
			return
		}
		// 远端路径为空会让同步入口退化成网盘根目录，必须在入队前拦截
		if req.Path == "" {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "REMOTE_PATH_UNAVAILABLE", Message: "解析远端路径失败", Cause: errors.New("文件详情未返回有效路径")})
			return
		}
	}
	// 手动触发同步
	taskObj := &synccron.NewSyncTask{
		ID:           0,
		TaskType:     synccron.SyncTaskTypeStrm,
		SourcePath:   req.Path,
		SourcePathId: req.PathID,
		TargetPath:   req.TargetPath,
		IsFile:       req.IsFile,
		SourceType:   account.SourceType,
		AccountId:    req.AccountID,
	}
	if err := synccron.AddNewSyncTask(taskObj); err != nil {
		writeTaskEnqueueError(c, err, "添加同步任务失败")
		return
	}
	httpapi.Success(c, http.StatusAccepted, gin.H{"status": "accepted"}, "同步任务已添加到队列")
}

// resolve115ManualSyncPath 把 115 文件详情拼成完整远端路径。
// 115 `/open/folder/get_info` 的 paths 只包含父目录链，漏掉自身名称会让同步入口退化成父目录，
// 进而对父目录下的所有文件生成 STRM。
// 名称按 115 返回的原样拼接：远端可能存在首尾空格的名称，修饰后会导致路径反查不到。
func resolve115ManualSyncPath(detail *v115open.FileDetail) string {
	if detail == nil || strings.TrimSpace(detail.FileName) == "" {
		return ""
	}
	return pathpkg.Join(detail.Path, detail.FileName)
}

// loadSyncPathForRequest 将资源缺失与内部读取错误分别投影到 HTTP。
func loadSyncPathForRequest(c *gin.Context, id uint) (*models.SyncPath, bool) {
	syncPath, err := models.GetSyncPathByID(c.Request.Context(), id)
	if err != nil {
		status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "读取同步目录失败"
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status, code, message = http.StatusNotFound, "SYNC_PATH_NOT_FOUND", "同步路径不存在"
		}
		httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
		return nil, false
	}
	return syncPath, true
}

func writeTaskEnqueueError(c *gin.Context, err error, message string) {
	status, code := http.StatusInternalServerError, "INTERNAL_ERROR"
	if errors.Is(err, synccron.ErrTaskAlreadyExists) {
		status, code, message = http.StatusConflict, "TASK_ALREADY_EXISTS", "任务已在排队或正在运行，请等待当前任务完成"
	}
	httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
}
