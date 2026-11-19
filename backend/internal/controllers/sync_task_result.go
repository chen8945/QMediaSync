package controllers

import (
	"errors"
	"net/http"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetSyncTaskResult 独立读取扫描摘要，失败不影响任务基本信息。
func GetSyncTaskResult(c *gin.Context) {
	id, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	result, err := models.GetSyncScanSummary(c.Request.Context(), id.ID)
	if syncTaskResultError(c, err) {
		return
	}
	httpapi.Success(c, http.StatusOK, result, "")
}

// GetSyncTaskFailures 分页返回失败范围，包含文件和目录失败。
func GetSyncTaskFailures(c *gin.Context) {
	id, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	req := struct {
		Offset int `form:"offset" binding:"min=0"`
		Limit  int `form:"limit" binding:"min=1,max=200"`
	}{Limit: 100}
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	entries, total, recorded, err := models.GetSyncFailurePage(c.Request.Context(), id.ID, req.Offset, req.Limit)
	if syncTaskResultError(c, err) {
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{"entries": entries, "total": total, "recorded": recorded}, "")
}

func syncTaskResultError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "读取同步任务结果失败"
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code, message = http.StatusNotFound, "SYNC_TASK_NOT_FOUND", "未找到对应的同步任务"
	}
	httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
	return true
}
