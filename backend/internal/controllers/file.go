package controllers

import (
	"net/http"

	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/taskgate"

	"github.com/gin-gonic/gin"
)

// UploadList 获取上传队列列表
// @Summary 获取上传队列
// @Description 按状态分页获取上传队列任务列表
// @Tags 队列管理
// @Accept json
// @Produce json
// @Param status query string false "任务状态"
// @Param page query integer false "页码，默认 1"
// @Param page_size query integer false "每页数量，默认 100"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /upload/queue [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func UploadList(ctx *gin.Context) {
	var req requests.QueueListRequest
	if err := ctx.ShouldBind(&req); err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 从请求中获取文件列表
	// 从 model/upload.go 中查询上传队列列表
	type uploadQueueResp struct {
		Total       int                        `json:"total"`
		Uploading   int                        `json:"uploading"`
		QueueStatus models.QueueStatusSnapshot `json:"queue_status"`
		List        []*models.DbUploadTask     `json:"list"`
	}
	// 从请求中获取文件列表
	// 从 model/upload.go 中查询上传队列列表
	uploadList, total := models.GetUploadTaskList(models.UploadStatus(req.Status), req.Page, req.PageSize)
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取上传队列成功", Data: uploadQueueResp{
		Total:       int(total),
		Uploading:   int(models.GetUploadingCount()),
		QueueStatus: models.GetUploadQueueStatusSnapshot(),
		List:        uploadList,
	}})
}

// ClearPendingUploadTasks 清除上传队列中未开始的任务
// @Summary 清空待上传任务
// @Description 清除上传队列中所有未开始的任务
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /upload/queue/clear-pending [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ClearPendingUploadTasks(ctx *gin.Context) {
	// 调用全局上传队列的 ClearPendingTasks 方法
	err := models.ClearPendingUploadTasks()
	if err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "清除待上传任务失败", Data: nil})
		return
	}

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "待上传任务已清除", Data: nil})
}

// ClearUploadSuccessAndFailedTasks 清除上传队列中成功和失败的任务
// @Summary 清空已完成/失败的上传任务
// @Description 删除上传队列中已成功和失败的任务
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /upload/queue/clear-success-failed [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ClearUploadSuccessAndFailedTasks(ctx *gin.Context) {
	err := models.ClearUploadSuccessAndFailed()
	if err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除已完成和失败的上传任务失败", Data: nil})
		return
	}

	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已完成和失败的上传任务已删除", Data: nil})
}

// RetryFailedUploadTasks 重试所有失败的上传任务
// @Summary 重试失败的上传任务
// @Description 将所有失败的上传任务状态改为等待中，会自动触发重试
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /upload/queue/retry-failed [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func RetryFailedUploadTasks(ctx *gin.Context) {
	err := models.RetryFailedUploadTasks(models.DefaultQueueRetryMax)
	if err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重试失败的上传任务失败", Data: nil})
		return
	}

	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "失败的上传任务已重新加入队列", Data: nil})
}

// RetryFailedDownloadTasks 重试所有失败的下载任务
// @Summary 重试失败的下载任务
// @Description 将未超过最大重试次数的失败下载任务状态改为等待中，会自动触发重试
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /download/queue/retry-failed [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func RetryFailedDownloadTasks(ctx *gin.Context) {
	err := models.RetryFailedDownloadTasks(models.DefaultQueueRetryMax)
	if err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "重试失败的下载任务失败", Data: nil})
		return
	}

	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "失败的下载任务已重新加入队列", Data: nil})
}

// StartUploadQueue 启动上传队列
// @Summary 启动上传队列
// @Description 启动或恢复上传队列执行
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /upload/queue/start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StartUploadQueue(ctx *gin.Context) {
	if taskgate.IsBlocked() {
		ctx.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: taskgate.ErrTaskAdmissionBlocked.Error()})
		return
	}
	// 调用全局上传队列的 Start 方法
	models.GlobalUploadQueue.Restart()

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "上传队列已启动", Data: nil})
}

// StopUploadQueue 停止上传队列
// @Summary 停止上传队列
// @Description 停止上传队列执行
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /upload/queue/stop [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StopUploadQueue(ctx *gin.Context) {
	// 调用全局上传队列的 Stop 方法
	models.GlobalUploadQueue.Stop()

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "上传队列已停止", Data: nil})
}

// UploadQueueStatus 查询上传队列状态
// @Summary 查询上传队列状态
// @Description 获取上传队列当前运行状态
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /upload/queue/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func UploadQueueStatus(ctx *gin.Context) {
	status := models.GetUploadQueueStatusSnapshot()

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取上传队列状态成功", Data: status})
}

// DownloadList 获取下载队列列表
// @Summary 获取下载队列
// @Description 按状态分页获取下载队列任务列表
// @Tags 队列管理
// @Accept json
// @Produce json
// @Param status query string false "任务状态"
// @Param page query integer false "页码，默认 1"
// @Param page_size query integer false "每页数量，默认 100"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /download/queue [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func DownloadList(ctx *gin.Context) {
	type downloadQueueResp struct {
		Total       int64                      `json:"total"`
		Downloading int64                      `json:"downloading"`
		QueueStatus models.QueueStatusSnapshot `json:"queue_status"`
		List        []*models.DbDownloadTask   `json:"list"`
	}
	var req requests.QueueListRequest
	if err := ctx.ShouldBind(&req); err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 从请求中获取文件列表
	// 从 model/download.go 中查询下载队列列表
	downloadList, total := models.GetDownloadTaskList(models.DownloadStatus(req.Status), req.Page, req.PageSize)
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取下载队列成功", Data: downloadQueueResp{
		Total:       total,
		Downloading: models.GetDownloadingCount(),
		QueueStatus: models.GetDownloadQueueStatusSnapshot(),
		List:        downloadList,
	}})
}

// ClearPendingDownloadTasks 清除下载队列中未开始的任务
// @Summary 清空待下载任务
// @Description 清除下载队列中所有未开始的任务
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /download/queue/clear-pending [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ClearPendingDownloadTasks(ctx *gin.Context) {
	// 调用全局下载队列的 ClearPendingTasks 方法
	err := models.ClearDownloadPendingTasks()
	if err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "清除待下载任务失败", Data: nil})
		return
	}

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "待下载任务已清除", Data: nil})
}

// StartDownloadQueue 启动下载队列
// @Summary 启动下载队列
// @Description 启动或恢复下载队列执行
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /download/queue/start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StartDownloadQueue(ctx *gin.Context) {
	if taskgate.IsBlocked() {
		ctx.JSON(http.StatusConflict, APIResponse[any]{Code: BadRequest, Message: taskgate.ErrTaskAdmissionBlocked.Error()})
		return
	}
	// 调用全局下载队列的 Start 方法
	models.GlobalDownloadQueue.Restart()

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "下载队列已启动", Data: nil})
}

// StopDownloadQueue 停止下载队列
// @Summary 停止下载队列
// @Description 停止下载队列执行
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /download/queue/stop [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func StopDownloadQueue(ctx *gin.Context) {
	// 调用全局下载队列的 Stop 方法
	models.GlobalDownloadQueue.Stop()

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "下载队列已停止", Data: nil})
}

// DownloadQueueStatus 查询下载队列状态
// @Summary 查询下载队列状态
// @Description 获取下载队列当前运行状态
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /download/queue/status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func DownloadQueueStatus(ctx *gin.Context) {
	status := models.GetDownloadQueueStatusSnapshot()

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取下载队列状态成功", Data: status})
}

// ClearDownloadSuccessAndFailedTasks 清除下载队列中成功和失败的任务
// @Summary 清空已完成/失败的下载任务
// @Description 删除下载队列中已成功和失败的任务
// @Tags 队列管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /download/queue/clear-success-failed [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ClearDownloadSuccessAndFailedTasks(ctx *gin.Context) {
	// 调用全局下载队列的 DeleteSuccessAndFailed 方法
	err := models.ClearDownloadSuccessAndFailed()
	if err != nil {
		ctx.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除已完成和失败的下载任务失败", Data: nil})
		return
	}

	// 返回结果
	ctx.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已完成和失败的下载任务已删除", Data: nil})
}
