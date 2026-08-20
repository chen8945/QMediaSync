package controllers

import (
	"context"
	"fmt"
	"net/http"
	pathpkg "path"
	"strings"

	"qmediasync/internal/directoryupload"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
)

// StartSync 启动同步
// @Summary 启动同步任务
// @Description 启动全局同步任务并添加到队列
// @Tags 同步管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StartSync(c *gin.Context) {
	// 启动同步
	synccron.StartSyncCron()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "同步任务已添加到队列", Data: nil})
}

// GetSyncRecords 获取同步记录列表
// @Summary 获取同步记录
// @Description 分页获取同步任务记录列表
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param page query integer false "页码"
// @Param page_size query integer false "每页数量"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/records [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetSyncRecords(c *gin.Context) {
	type syncRecordsRequest struct {
		Page     int `form:"page" json:"page" binding:"omitempty,min=1"`           // 页码，默认 1
		PageSize int `form:"page_size" json:"page_size" binding:"omitempty,min=1"` // 每页数量，默认 50
	}

	var req syncRecordsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: 400, Message: "请求参数错误", Data: nil})
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: 500, Message: "获取同步记录失败", Data: nil})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取同步记录成功", Data: map[string]interface{}{
		"records": records,
		"total":   total,
	}})
}

// GetSyncTask 获取同步任务详情
// @Summary 获取同步任务详情
// @Description 根据 ID 获取指定同步任务的详细信息
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param sync_id query integer true "同步任务 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/task [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetSyncTask(c *gin.Context) {
	type syncTaskRequest struct {
		SyncID uint `form:"sync_id" json:"sync_id" binding:"required"` // 同步任务 ID
	}
	var req syncTaskRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: 400, Message: "请求参数错误", Data: nil})
		return
	}

	sync, err := models.GetSyncByID(req.SyncID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: 500, Message: "获取同步任务失败", Data: nil})
		return
	}

	if sync == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: 404, Message: "未找到对应的同步任务", Data: nil})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取同步任务详情成功", Data: sync})
}

// GetSyncPathList 获取同步路径列表
// @Summary 获取同步路径列表
// @Description 分页获取所有配置的同步路径
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param page query integer false "页码"
// @Param page_size query integer false "每页数量"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/path-list [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetSyncPathList(c *gin.Context) {
	type syncPathListRequest struct {
		Page       int               `form:"page" json:"page" binding:"omitempty,min=1"`           // 页码，默认 1
		PageSize   int               `form:"page_size" json:"page_size" binding:"omitempty,min=1"` // 每页数量，默认 20
		SourceType models.SourceType `form:"source_type" json:"source_type" binding:"omitempty"`   // 来源类型
	}
	var req syncPathListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
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

	syncPaths, total := models.GetSyncPathList(page, pageSize, false, req.SourceType)

	for _, sp := range syncPaths {
		status := synccron.CheckNewTaskStatus(sp.ID, synccron.SyncTaskTypeStrm)
		sp.IsRunning = int(status)
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取同步路径列表成功", Data: map[string]any{
		"list":      syncPaths,
		"total":     total,
		"page":      page,
		"page_size": pageSize,
	}})
}

// DeleteSyncPath 删除同步路径
// @Summary 删除同步路径
// @Description 根据 ID 删除指定的同步路径
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param id body integer true "同步路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/path-delete [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func DeleteSyncPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 删除同步路径
	success := models.DeleteSyncPathById(req.ID)
	if !success {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除同步路径失败", Data: nil})
		return
	}
	synccron.InitSyncCron()
	synccron.InitCron()
	directoryupload.ReloadDirectoryUploadService()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "删除同步路径成功", Data: nil})
}

// GetSyncPathById 根据 ID 获取同步路径详情。
// @Summary 获取同步路径详情
// @Description 根据 ID 获取指定同步路径的详细配置
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param id body integer true "同步路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/path/:id [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetSyncPathById(c *gin.Context) {
	// 从路径参数获取 ID
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "ID 参数格式错误", Data: nil})
		return
	}

	syncPath := models.GetSyncPathById(idReq.ID)
	if syncPath == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "同步路径不存在", Data: nil})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取同步路径详情成功", Data: syncPath})
}

// DelSyncRecords 批量删除同步记录
// @Summary 删除同步记录
// @Description 批量删除已完成或失败的同步记录
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param ids body []integer true "同步记录 ID 列表"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/delete-records [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func DelSyncRecords(c *gin.Context) {
	var req requests.IDListRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
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
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: message,
			Data:    result,
		})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "删除同步记录成功", Data: result})
}

type deleteSyncRecordFailure struct {
	ID     uint   `json:"id"`
	Reason string `json:"reason"`
}

type deleteSyncRecordsResult struct {
	DeletedIDs []uint                    `json:"deleted_ids"`
	Failures   []deleteSyncRecordFailure `json:"failures"`
}

func deleteSyncRecordsByIDs(ids []uint) deleteSyncRecordsResult {
	result := deleteSyncRecordsResult{
		DeletedIDs: make([]uint, 0, len(ids)),
		Failures:   make([]deleteSyncRecordFailure, 0),
	}
	for _, id := range ids {
		if err := models.DeleteSyncRecordById(id); err != nil {
			result.Failures = append(result.Failures, deleteSyncRecordFailure{
				ID:     id,
				Reason: err.Error(),
			})
			continue
		}
		result.DeletedIDs = append(result.DeletedIDs, id)
	}
	return result
}

// StartSyncByPath 启动指定路径的同步任务
// @Summary 启动同步路径
// @Description 启动指定同步目录的同步任务
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param id body integer true "同步路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/path/start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StartSyncByPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	syncPath := models.GetSyncPathById(req.ID)
	if syncPath == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "同步路径不存在", Data: nil})
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "添加同步任务失败：" + err.Error(), Data: nil})
		return
	}

	pathStatus := synccron.CheckNewTaskStatus(syncPath.ID, synccron.SyncTaskTypeStrm)
	c.JSON(http.StatusOK, APIResponse[map[string]any]{
		Code:    Success,
		Message: "同步任务已添加到队列",
		Data: map[string]any{
			"id":         syncPath.ID,
			"is_running": pathStatus,
		},
	})
}

// StopSyncByPath 停止指定路径的同步任务
// @Summary 停止同步路径
// @Description 停止指定同步目录的同步任务
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param id body integer true "同步路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/path/stop [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StopSyncByPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	syncPath := models.GetSyncPathById(req.ID)
	if syncPath == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "同步路径不存在", Data: nil})
		return
	}
	// syncPath.SetIsFullSync(false)
	synccron.CancelNewSyncTask(syncPath.ID, synccron.SyncTaskTypeStrm)

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "同步任务已停止", Data: nil})
}

// ToggleSyncByPath 切换同步路径的定时任务
// @Summary 切换定时同步
// @Description 开启或关闭同步目录的定时同步任务
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param id body integer true "同步路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/path/toggle-cron [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ToggleSyncByPath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 切换 enable 参数
	syncPath := models.GetSyncPathById(req.ID)
	if syncPath == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "同步路径不存在", Data: nil})
		return
	}
	syncPath.ToggleCron()
	synccron.InitCron()
	// 重启自定义定时任务
	if syncPath.Cron != "" {
		synccron.InitSyncCron()
	}
	if syncPath.EnableCron {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "定时同步已开启", Data: nil})
	} else {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "定时同步已关闭", Data: nil})
	}

}

// FullStart115Sync 启动 115 全量同步
// @Summary 启动 115 全量同步
// @Description 删除本地缓存数据并触发 115 的全量同步
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param id body integer true "同步路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /sync/path/full-start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func FullStart115Sync(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	syncPath := models.GetSyncPathById(req.ID)
	if syncPath == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "同步路径不存在", Data: nil})
		return
	}
	// 删除所有的数据库记录，重新查询接口
	// if syncPath.SourceType == models.SourceType115 {
	// 	// 清空数据表
	// 	if err := models.DeleteAllFileBySyncPathId(syncPath.ID); err != nil {
	// 		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "清空同步目录的数据表失败：" + err.Error(), Data: nil})
	// 		return
	// 	}
	// }
	syncPath.SetIsFullSync(true)
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "添加同步任务失败：" + err.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "同步任务已添加到队列", Data: nil})
}

func SaveRelScrapePath(c *gin.Context) {
	var req requests.SaveRelScrapePathRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	syncPath := models.GetSyncPathById(req.SyncPathID)
	if syncPath == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "同步路径不存在", Data: nil})
		return
	}
	// 保存关联的刮削路径
	if err := syncPath.SaveScrapePaths(req.ScrapePathIDs); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存关联的刮削路径失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "关联刮削路径已保存", Data: nil})
}

func GetRelScrapePath(c *gin.Context) {
	idStr := c.Param("id")
	idReq, err := requests.ParsePositiveIDRequest(idStr)
	if err != nil {
		message := "ID 参数格式错误"
		if strings.TrimSpace(idStr) == "" {
			message = "ID 参数不能为空"
		}
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: message, Data: nil})
		return
	}
	syncPath := models.GetSyncPathById(idReq.ID)
	if syncPath == nil {
		c.JSON(http.StatusNotFound, APIResponse[any]{Code: BadRequest, Message: "同步路径不存在", Data: nil})
		return
	}
	// 获取关联的刮削路径
	scrapePathIds := syncPath.GetScrapePathIds()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取关联刮削路径成功", Data: scrapePathIds})
}

// 从网盘文件管理器手动触发同步
func ManualSync(c *gin.Context) {
	var req requests.ManualSyncRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("请求参数错误：%v", err), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号不存在", Data: nil})
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
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取文件详情失败：" + err.Error(), Data: nil})
				return
			}
			req.Path = resolve115ManualSyncPath(fileDetail)
			req.IsFile = fileDetail.FileCategory == v115open.TypeFile
		case models.SourceTypeBaiduPan:
			client := account.GetBaiDuPanClient()
			// 百度网盘文件详情接口
			fileDetail, err := client.FileExists(context.Background(), req.PathID)
			if err != nil {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取文件详情失败：" + err.Error(), Data: nil})
				return
			}
			// 目标不存在时 FileExists 返回空详情而不是错误
			if fileDetail == nil {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "文件不存在：" + req.PathID, Data: nil})
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
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取文件详情失败：" + err.Error(), Data: nil})
				return
			}
			if fileDetail == nil || strings.TrimSpace(fileDetail.Name) == "" {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "文件不存在：" + remotePath, Data: nil})
				return
			}
			req.Path = remotePath
			// 归一后的路径同时作为入口 ID，避免单文件同步和队列去重用到未归一的原始值
			req.PathID = remotePath
			req.IsFile = !fileDetail.IsDir
		default:
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的文件类型", Data: nil})
			return
		}
		// 远端路径为空会让同步入口退化成网盘根目录，必须在入队前拦截
		if req.Path == "" {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解析远端路径失败：详情未返回有效路径", Data: nil})
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "添加同步任务失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "同步任务已添加到队列", Data: nil})
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
