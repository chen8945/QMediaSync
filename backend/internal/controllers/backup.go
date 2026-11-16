package controllers

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type backupRecordResponse struct {
	models.BackupRecord
	FileStatus string `json:"file_status"`
}

type backupFilesResponse struct {
	Directory string              `json:"directory"`
	Files     []backup.BackupFile `json:"files"`
}

func CreateBackup(c *gin.Context) {
	var req requests.BackupCreateRequest
	if err := c.ShouldBind(&req); err != nil {
		req = requests.BackupCreateRequest{}
	}
	if err := req.Validate(); err != nil {
		req.Reason = "手动备份"
	}

	if err := backup.StartBackup(models.BackupTypeManual, req.Reason); err != nil {
		backupTaskFailure(c, err)
		return
	}

	httpapi.Success(c, http.StatusAccepted, nil, "数据备份任务已开始")
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
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_LIST_FAILED", Message: "获取备份列表失败，请稍后重试", Cause: err})
		return
	}

	items := make([]backupRecordResponse, 0, len(records))
	for _, record := range records {
		items = append(items, backupRecordResponse{
			BackupRecord: record,
			FileStatus:   backup.BackupFileAvailability(record.FilePath),
		})
	}
	httpapi.Success(c, http.StatusOK, map[string]any{
		"list":      items,
		"total":     total,
		"page":      req.Page,
		"page_size": req.PageSize,
	}, "获取备份列表成功")
}

// GetBackupFiles 列出本机备份目录中的 ZIP 文件元数据，不读取归档内容。
func GetBackupFiles(c *gin.Context) {
	files, err := backup.ListBackupFiles()
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_FILE_LIST_FAILED", Message: "获取备份文件列表失败，请检查备份目录是否可访问", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, backupFilesResponse{Directory: backup.BackupDirectory(), Files: files}, "获取备份文件列表成功")
}

func GetBackupRecord(c *gin.Context) {
	req, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "无效的备份记录 ID", Data: nil})
		return
	}

	var record models.BackupRecord
	if err := db.Db.First(&record, req.ID).Error; err != nil {
		backupRecordFailure(c, err)
		return
	}

	httpapi.Success(c, http.StatusOK, backupRecordResponse{
		BackupRecord: record,
		FileStatus:   backup.BackupFileAvailability(record.FilePath),
	}, "获取备份记录成功")
}

func DeleteBackup(c *gin.Context) {
	req, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "无效的备份记录 ID", Data: nil})
		return
	}

	service := models.GetBackupService()
	if err := service.DeleteBackup(req.ID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			backupRecordFailure(c, err)
		} else {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_DELETE_FAILED", Message: "删除备份失败，请检查备份文件和数据库是否可访问", Cause: err})
		}
		return
	}

	httpapi.Success(c, http.StatusOK, nil, "备份记录已删除")
}

func DownloadBackup(c *gin.Context) {
	req, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "无效的备份记录 ID", Data: nil})
		return
	}

	var record models.BackupRecord
	if err := db.Db.First(&record, req.ID).Error; err != nil {
		backupRecordFailure(c, err)
		return
	}

	if record.FilePath == "" {
		backupFileFailure(c, backup.ErrBackupFileUnavailable)
		return
	}

	file, err := backup.OpenBackupFile(record.FilePath)
	if err != nil {
		backupFileFailure(c, err)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		backupFileFailure(c, err)
		return
	}

	fileName := filepath.Base(record.FilePath)
	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fileName}))
	c.Header("Content-Type", "application/octet-stream")
	http.ServeContent(c.Writer, c.Request, fileName, info.ModTime(), file)
}

func GetBackupConfig(c *gin.Context) {
	service := models.GetBackupService()
	config := service.GetBackupConfig()

	httpapi.Success(c, http.StatusOK, *config, "获取备份配置成功")
}

func UpdateBackupConfig(c *gin.Context) {
	var req requests.BackupConfigUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	service := models.GetBackupService()
	config := *service.GetBackupConfig()

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

	if err := service.UpdateBackupConfig(&config); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_CONFIG_SAVE_FAILED", Message: "更新备份配置失败，请稍后重试", Cause: err})
		return
	}

	synccron.InitCron()

	httpapi.Success(c, http.StatusOK, nil, "备份配置已更新")
}

func GetBackupStatus(c *gin.Context) {
	result := backup.GetRunningResult()
	httpapi.Success(c, http.StatusOK, *result, "获取备份状态成功")
}

func RestoreFromBackup(c *gin.Context) {
	var req requests.BackupRestoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	var filePath string
	if req.RecordID > 0 {
		var record models.BackupRecord
		if err := db.Db.First(&record, req.RecordID).Error; err != nil {
			backupRecordFailure(c, err)
			return
		}
		name, err := filepath.Rel(backup.BackupDirectory(), record.FilePath)
		if record.FilePath == "" || err != nil {
			backupFileFailure(c, backup.ErrBackupFileUnavailable)
			return
		}
		_, err = backup.ResolveBackupFile(name)
		if errors.Is(err, backup.ErrBackupFileNameInvalid) {
			err = backup.ErrBackupFileUnavailable
		}
		if err != nil {
			backupFileFailure(c, err)
			return
		}
		filePath = record.FilePath
	} else {
		var err error
		filePath, err = backup.ResolveBackupFile(req.FileName)
		if err != nil {
			backupFileFailure(c, err)
			return
		}
	}

	receipt, err := backup.StartRestoreWithReceipt(filePath, false)
	if err != nil {
		backupTaskFailure(c, err)
		return
	}

	httpapi.Success(c, http.StatusAccepted, map[string]string{"restore_receipt": receipt}, "数据恢复任务已开始")
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
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请上传备份文件", Data: nil})
		return
	}
	defer file.Close()
	if header.Size > maxArchiveSize {
		backupUploadLimitResponse(c)
		return
	}

	ext := strings.ToLower(filepath.Ext(header.Filename))
	if ext != ".zip" {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "仅支持 .zip 格式的备份文件", Data: nil})
		return
	}

	if backup.IsRunning() {
		backupTaskFailure(c, backup.ErrTaskRunning)
		return
	}

	tempDir := filepath.Join(helpers.ConfigDir, "backups", "temp")
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups", "temp"); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_UPLOAD_FAILED", Message: "保存上传文件失败", Cause: err})
		return
	}
	tempPath := filepath.Join(tempDir, fmt.Sprintf("upload_%d%s", time.Now().UnixNano(), ext))

	dst, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_UPLOAD_FAILED", Message: "保存上传文件失败", Cause: err})
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
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_UPLOAD_FAILED", Message: "保存上传文件失败", Cause: errors.Join(err, closeErr)})
		return
	}

	receipt, err := backup.StartUploadedRestoreWithReceipt(tempPath)
	if err != nil {
		os.Remove(tempPath)
		backupTaskFailure(c, err)
		return
	}

	httpapi.Success(c, http.StatusAccepted, map[string]string{"restore_receipt": receipt}, "数据恢复任务已开始")
}

func backupTaskFailure(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	failure := httpapi.Error{Code: "BACKUP_START_FAILED", Message: "启动备份或恢复任务失败，请稍后重试", Cause: err}
	switch {
	case errors.Is(err, backup.ErrTaskRunning):
		status = http.StatusConflict
		failure.Code, failure.Message = "BACKUP_TASK_RUNNING", "备份或恢复任务正在运行，请稍后再试"
	case errors.Is(err, db.ErrMaintenance):
		status = http.StatusServiceUnavailable
		failure.Code, failure.Message = "DATABASE_MAINTENANCE", "数据库正在维护，请完成恢复后的重启"
		c.Header("Retry-After", "30")
	}
	httpapi.Failure(c, status, failure)
}

func backupRecordFailure(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "BACKUP_RECORD_NOT_FOUND", Message: "备份记录不存在", Cause: err})
		return
	}
	httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "BACKUP_RECORD_READ_FAILED", Message: "读取备份记录失败，请稍后重试", Cause: err})
}

func backupFileFailure(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	failure := httpapi.Error{Code: "BACKUP_FILE_READ_FAILED", Message: "读取备份文件失败，请检查文件是否可访问", Cause: err}
	switch {
	case errors.Is(err, os.ErrNotExist):
		status = http.StatusNotFound
		failure.Code, failure.Message = "BACKUP_FILE_NOT_FOUND", "备份文件不存在，请刷新文件列表后重试"
	case errors.Is(err, backup.ErrBackupFileNameInvalid):
		status = http.StatusBadRequest
		failure.Code, failure.Message = "INVALID_REQUEST", "仅支持备份目录中的 ZIP 文件名"
		failure.FieldErrors = []httpapi.FieldError{{Field: "file_name", Message: failure.Message}}
	case errors.Is(err, backup.ErrBackupFileUnavailable):
		status = http.StatusBadRequest
		failure.Code, failure.Message = "BACKUP_FILE_UNAVAILABLE", "备份文件不可用，请检查文件是否存在且可访问"
	}
	httpapi.Failure(c, status, failure)
}

func backupUploadLimitResponse(c *gin.Context) {
	httpapi.Failure(c, http.StatusRequestEntityTooLarge, httpapi.Error{Code: "BACKUP_ARCHIVE_LIMIT", Message: "备份文件或上传请求超过大小限制", Data: nil})
}
