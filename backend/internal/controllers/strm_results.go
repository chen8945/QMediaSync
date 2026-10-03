package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

// ListStrmResults 返回持久化的 STRM 后处理结果；默认只列出顶层任务。
func ListStrmResults(c *gin.Context) {
	var req requests.StrmResultsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "查询参数无效"})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error()})
		return
	}
	query := db.Db.WithContext(c.Request.Context()).Model(&models.StrmGenerationTask{})
	if req.ID > 0 {
		query = query.Where("id = ?", req.ID)
	}
	if req.ParentTaskID > 0 {
		query = query.Where("parent_task_id = ?", req.ParentTaskID)
	} else if req.ID == 0 && req.UploadTaskID == 0 {
		query = query.Where("parent_task_id = 0 OR parent_task_id IS NULL")
	}
	if req.UploadTaskID > 0 {
		query = query.Where("upload_task_id = ?", req.UploadTaskID)
	}
	if req.SyncPathID > 0 {
		query = query.Where("sync_path_id = ?", req.SyncPathID)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "读取 STRM 结果失败"})
		return
	}
	// 只返回展示所需字段，避免暴露远端定位凭据和内部幂等信息。
	items := make([]strmResult, 0)
	if err := query.Select("id, parent_task_id, upload_task_id, sync_path_id, source, task_type, file_name, directory_path, status, skip_reason, total_items, accepted_items, failed_items, skipped_items, updated_at").Order("id DESC").Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).Scan(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "读取 STRM 结果失败"})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Data: gin.H{"items": items, "total": total}})
}

type strmResult struct {
	ID            uint   `json:"id"`
	ParentTaskID  uint   `json:"parent_task_id"`
	UploadTaskID  uint   `json:"upload_task_id"`
	SyncPathID    uint   `json:"sync_path_id"`
	Source        string `json:"source"`
	TaskType      string `json:"task_type"`
	FileName      string `json:"file_name"`
	DirectoryPath string `json:"directory_path"`
	Status        string `json:"status"`
	SkipReason    string `json:"skip_reason"`
	TotalItems    *int   `json:"total_items"`
	AcceptedItems *int   `json:"accepted_items"`
	FailedItems   *int   `json:"failed_items"`
	SkippedItems  *int   `json:"skipped_items"`
	UpdatedAt     int64  `json:"updated_at"`
}
