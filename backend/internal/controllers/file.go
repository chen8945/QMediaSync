package controllers

import (
	"net/http"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"

	"github.com/gin-gonic/gin"
)

// UploadList 获取上传队列列表
func UploadList(ctx *gin.Context) {
	var req requests.QueueListRequest
	if err := ctx.ShouldBind(&req); err != nil {
		httpapi.Failure(ctx, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(ctx, http.StatusBadRequest, httpapi.InvalidRequest(err))
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
	uploadList, total, err := models.GetUploadTaskList(ctx.Request.Context(), models.UploadStatus(req.Status), req.Page, req.PageSize)
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取上传队列失败", Cause: err})
		return
	}
	uploading, err := models.GetUploadingCount(ctx.Request.Context())
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取上传队列失败", Cause: err})
		return
	}
	httpapi.Success(ctx, http.StatusOK, uploadQueueResp{
		Total:       int(total),
		Uploading:   int(uploading),
		QueueStatus: models.GetUploadQueueStatusSnapshot(),
		List:        uploadList,
	}, "获取上传队列成功")
}

// ClearPendingUploadTasks 清除上传队列中未开始的任务
func ClearPendingUploadTasks(ctx *gin.Context) {
	// 调用全局上传队列的 ClearPendingTasks 方法
	err := models.ClearPendingUploadTasks()
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "清除待上传任务失败", Cause: err})
		return
	}

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, nil, "待上传任务已清除")
}

// ClearUploadSuccessAndFailedTasks 清除上传队列中成功和失败的任务
func ClearUploadSuccessAndFailedTasks(ctx *gin.Context) {
	err := models.ClearUploadSuccessAndFailed()
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "删除已完成和失败的上传任务失败", Cause: err})
		return
	}

	httpapi.Success(ctx, http.StatusOK, nil, "已完成和失败的上传任务已删除")
}

// RetryFailedUploadTasks 重试所有失败的上传任务
func RetryFailedUploadTasks(ctx *gin.Context) {
	err := models.RetryFailedUploadTasks(models.DefaultQueueRetryMax)
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "重试失败的上传任务失败", Cause: err})
		return
	}

	httpapi.Success(ctx, http.StatusOK, nil, "失败的上传任务已重新加入队列")
}

// RetryFailedDownloadTasks 重试所有失败的下载任务
func RetryFailedDownloadTasks(ctx *gin.Context) {
	err := models.RetryFailedDownloadTasks(models.DefaultQueueRetryMax)
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "重试失败的下载任务失败", Cause: err})
		return
	}

	httpapi.Success(ctx, http.StatusOK, nil, "失败的下载任务已重新加入队列")
}

// StartUploadQueue 启动上传队列
func StartUploadQueue(ctx *gin.Context) {
	// 调用全局上传队列的 Start 方法
	models.GlobalUploadQueue.Start()

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, nil, "上传队列已启动")
}

// StopUploadQueue 停止上传队列
func StopUploadQueue(ctx *gin.Context) {
	// 调用全局上传队列的 Stop 方法
	models.GlobalUploadQueue.Stop()

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, nil, "上传队列已停止")
}

// UploadQueueStatus 查询上传队列状态
func UploadQueueStatus(ctx *gin.Context) {
	status := models.GetUploadQueueStatusSnapshot()

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, status, "获取上传队列状态成功")
}

// DownloadList 获取下载队列列表
func DownloadList(ctx *gin.Context) {
	type downloadQueueResp struct {
		Total       int64                      `json:"total"`
		Downloading int64                      `json:"downloading"`
		QueueStatus models.QueueStatusSnapshot `json:"queue_status"`
		List        []*models.DbDownloadTask   `json:"list"`
	}
	var req requests.QueueListRequest
	if err := ctx.ShouldBind(&req); err != nil {
		httpapi.Failure(ctx, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(ctx, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 从请求中获取文件列表
	// 从 model/download.go 中查询下载队列列表
	downloadList, total, err := models.GetDownloadTaskList(ctx.Request.Context(), models.DownloadStatus(req.Status), req.Page, req.PageSize)
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取下载队列失败", Cause: err})
		return
	}
	downloading, err := models.GetDownloadingCount(ctx.Request.Context())
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取下载队列失败", Cause: err})
		return
	}
	httpapi.Success(ctx, http.StatusOK, downloadQueueResp{
		Total:       total,
		Downloading: downloading,
		QueueStatus: models.GetDownloadQueueStatusSnapshot(),
		List:        downloadList,
	}, "获取下载队列成功")
}

// ClearPendingDownloadTasks 清除下载队列中未开始的任务
func ClearPendingDownloadTasks(ctx *gin.Context) {
	// 调用全局下载队列的 ClearPendingTasks 方法
	err := models.ClearDownloadPendingTasks()
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "清除待下载任务失败", Cause: err})
		return
	}

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, nil, "待下载任务已清除")
}

// StartDownloadQueue 启动下载队列
func StartDownloadQueue(ctx *gin.Context) {
	// 调用全局下载队列的 Start 方法
	models.GlobalDownloadQueue.Start()

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, nil, "下载队列已启动")
}

// StopDownloadQueue 停止下载队列
func StopDownloadQueue(ctx *gin.Context) {
	// 调用全局下载队列的 Stop 方法
	models.GlobalDownloadQueue.Stop()

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, nil, "下载队列已停止")
}

// DownloadQueueStatus 查询下载队列状态
func DownloadQueueStatus(ctx *gin.Context) {
	status := models.GetDownloadQueueStatusSnapshot()

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, status, "获取下载队列状态成功")
}

// ClearDownloadSuccessAndFailedTasks 清除下载队列中成功和失败的任务
func ClearDownloadSuccessAndFailedTasks(ctx *gin.Context) {
	// 调用全局下载队列的 DeleteSuccessAndFailed 方法
	err := models.ClearDownloadSuccessAndFailed()
	if err != nil {
		httpapi.Failure(ctx, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "删除已完成和失败的下载任务失败", Cause: err})
		return
	}

	// 返回结果
	httpapi.Success(ctx, http.StatusOK, nil, "已完成和失败的下载任务已删除")
}
