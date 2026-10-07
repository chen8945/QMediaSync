package controllers

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
)

func CreateBackup(c *gin.Context) {
	var req requests.BackupCreateRequest
	if err := c.ShouldBind(&req); err != nil {
		req = requests.BackupCreateRequest{}
	}
	if err := req.Validate(); err != nil {
		req.Reason = "手动备份"
	}

	if err := backup.StartBackup(models.BackupTypeManual, req.Reason); err != nil {
		c.JSON(http.StatusConflict, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份任务正在运行，请稍后再试",
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "数据备份任务已开始",
		Data:    nil,
	})
}

func GetBackupList(c *gin.Context) {
	var req requests.BackupListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		req.Type = c.DefaultQuery("type", "all")
	}
	req.Normalize()

	service := models.GetBackupService()
	records, total, err := service.GetBackupRecords(req.Page, req.PageSize, req.Type)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: fmt.Sprintf("获取备份列表失败：%v", err),
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, APIResponse[map[string]any]{
		Code:    Success,
		Message: "获取备份列表成功",
		Data: map[string]any{
			"list":      records,
			"total":     total,
			"page":      req.Page,
			"page_size": req.PageSize,
		},
	})
}

func GetBackupRecord(c *gin.Context) {
	req, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "无效的备份记录 ID",
			Data:    nil,
		})
		return
	}

	var record models.BackupRecord
	if err := db.Db.First(&record, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份记录不存在",
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, APIResponse[models.BackupRecord]{
		Code:    Success,
		Message: "获取备份记录成功",
		Data:    record,
	})
}

func DeleteBackup(c *gin.Context) {
	req, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "无效的备份记录 ID",
			Data:    nil,
		})
		return
	}

	service := models.GetBackupService()
	if err := service.DeleteBackup(req.ID); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: fmt.Sprintf("删除备份失败：%v", err),
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "备份记录已删除",
		Data:    nil,
	})
}

func DownloadBackup(c *gin.Context) {
	req, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "无效的备份记录 ID",
			Data:    nil,
		})
		return
	}

	var record models.BackupRecord
	if err := db.Db.First(&record, req.ID).Error; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份记录不存在",
			Data:    nil,
		})
		return
	}

	if record.FilePath == "" {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份文件路径为空",
			Data:    nil,
		})
		return
	}

	if _, err := os.Stat(record.FilePath); os.IsNotExist(err) {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: fmt.Sprintf("备份文件不存在：%s", record.FilePath),
			Data:    nil,
		})
		return
	}

	fileName := filepath.Base(record.FilePath)
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Content-Disposition", "attachment; filename="+fileName)
	c.Header("Content-Type", "application/octet-stream")
	c.File(record.FilePath)
}

func GetBackupConfig(c *gin.Context) {
	service := models.GetBackupService()
	config := service.GetBackupConfig()

	c.JSON(http.StatusOK, APIResponse[models.BackupConfig]{
		Code:    Success,
		Message: "获取备份配置成功",
		Data:    *config,
	})
}

func UpdateBackupConfig(c *gin.Context) {
	var req requests.BackupConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "请求参数不正确",
			Data:    nil,
		})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: err.Error(),
			Data:    nil,
		})
		return
	}

	service := models.GetBackupService()
	config := service.GetBackupConfig()

	if req.BackupCron != "" {
		config.BackupCron = req.BackupCron
	}
	if req.BackupRetention > 0 {
		config.BackupRetention = req.BackupRetention
	}
	if req.BackupMaxCount >= 0 {
		config.BackupMaxCount = req.BackupMaxCount
	}
	if req.BackupCompress >= 0 {
		config.BackupCompress = req.BackupCompress
	}
	config.BackupEnabled = req.BackupEnabled

	if err := service.UpdateBackupConfig(config); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: fmt.Sprintf("更新配置失败：%v", err),
			Data:    nil,
		})
		return
	}

	synccron.InitCron()

	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "备份配置已更新",
		Data:    nil,
	})
}

func GetBackupStatus(c *gin.Context) {
	result := backup.GetRunningResult()
	c.JSON(http.StatusOK, APIResponse[backup.BackupOrRestoreResult]{
		Code:    Success,
		Message: "获取备份状态成功",
		Data:    *result,
	})
}

func RestoreFromBackup(c *gin.Context) {
	var req requests.BackupRestoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "请求参数不正确",
			Data:    nil,
		})
		return
	}

	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "请指定要恢复的备份记录 ID",
			Data:    nil,
		})
		return
	}

	var record models.BackupRecord
	if err := db.Db.First(&record, req.RecordID).Error; err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份记录不存在",
			Data:    nil,
		})
		return
	}

	receipt, err := backup.StartRestoreWithReceipt(record.FilePath, false)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code: BadRequest, Message: "备份或恢复任务正在运行，请稍后再试", Data: nil,
		})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "数据恢复任务已开始",
		Data:    map[string]string{"restore_receipt": receipt},
	})
}

func UploadAndRestore(c *gin.Context) {
	uploadAndRestore(c, backup.MaxArchiveSize)
}

func uploadAndRestore(c *gin.Context, maxArchiveSize int64) {
	// multipart 的边界和字段最多额外占 1 MiB；实际文件另行限制。
	maxRequestSize := maxArchiveSize + 1<<20
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestSize)
	defer func() {
		if c.Request.MultipartForm != nil {
			_ = c.Request.MultipartForm.RemoveAll()
		}
	}()
	if c.Request.ContentLength > maxRequestSize {
		backupUploadLimitResponse(c)
		return
	}

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		if _, exceeded := errors.AsType[*http.MaxBytesError](err); exceeded {
			backupUploadLimitResponse(c)
			return
		}
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "请上传备份文件",
			Data:    nil,
		})
		return
	}
	defer file.Close()
	if header.Size > maxArchiveSize {
		backupUploadLimitResponse(c)
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".zip" {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "仅支持 .zip 格式的备份文件",
			Data:    nil,
		})
		return
	}

	if backup.IsRunning() {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份或恢复任务正在运行，请稍后再试",
			Data:    nil,
		})
		return
	}

	tempDir := filepath.Join(helpers.ConfigDir, "backups", "temp")
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups", "temp"); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存上传文件失败", Data: nil})
		return
	}
	tempPath := filepath.Join(tempDir, fmt.Sprintf("upload_%d%s", time.Now().UnixNano(), ext))

	dst, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "保存上传文件失败",
			Data:    nil,
		})
		return
	}

	written, err := io.Copy(dst, io.LimitReader(file, maxArchiveSize+1))
	closeErr := dst.Close()
	if written > maxArchiveSize {
		os.Remove(tempPath)
		backupUploadLimitResponse(c)
		return
	}
	if err != nil || closeErr != nil {
		os.Remove(tempPath)
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "保存上传文件失败",
			Data:    nil,
		})
		return
	}

	receipt, err := backup.StartRestoreWithReceipt(tempPath, true)
	if err != nil {
		os.Remove(tempPath)
		c.JSON(http.StatusOK, APIResponse[any]{
			Code: BadRequest, Message: "备份或恢复任务正在运行，请稍后再试", Data: nil,
		})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "数据恢复任务已开始",
		Data:    map[string]string{"restore_receipt": receipt},
	})
}

func backupUploadLimitResponse(c *gin.Context) {
	c.JSON(http.StatusRequestEntityTooLarge, APIResponse[any]{
		Code: BadRequest, Message: "备份文件或上传请求超过大小限制", ErrorCode: "BACKUP_ARCHIVE_LIMIT",
	})
}
