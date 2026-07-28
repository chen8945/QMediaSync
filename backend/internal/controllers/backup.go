package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"qmediasync/internal/backup"
	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
)

// BackupOperationAccepted 是备份或恢复受理响应。
// Token 是只出现一次的明文状态令牌，前端只可保存在操作页内存中。
type BackupOperationAccepted struct {
	OperationID string `json:"operation_id"`
	Token       string `json:"token"`
}

// BackupListResponse 是既有备份列表的扩展响应。
// LatestOperation 仅在存在终态时返回，绝不泄露状态令牌、操作标识、进度、原始错误或恢复目标。
type BackupListResponse struct {
	List            []models.BackupRecord     `json:"list"`
	Total           int64                     `json:"total"`
	Page            int                       `json:"page"`
	PageSize        int                       `json:"page_size"`
	InventoryStatus string                    `json:"inventory_status"`
	LatestOperation *backup.TerminalOperation `json:"latest_operation,omitempty"`
}

// CreateBackup 受理手动备份。
// 协调器接管后台工作单元后以 HTTP 202 返回 operation ID 与一次性令牌，表示已受理而非已完成。
func CreateBackup(c *gin.Context) {
	var req requests.BackupCreateRequest
	if err := c.ShouldBind(&req); err != nil {
		req = requests.BackupCreateRequest{}
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: err.Error(),
			Data:    nil,
		})
		return
	}

	grant, err := backup.StartManualBackup(backup.ManualBackupRequest{
		Reason:             req.Reason,
		Password:           []byte(req.Password),
		ConfirmUnencrypted: req.ConfirmUnencrypted,
	})
	switch {
	case errors.Is(err, backup.ErrUnencryptedNotConfirmed):
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "创建未加密备份前请确认风险：备份可能包含 TLS 私钥",
			Data:    nil,
		})
		return
	case errors.Is(err, backup.ErrTasksRunning):
		respondBackupConflict(c, "有任务正在运行，请等待其结束后再备份")
		return
	case errors.Is(err, backup.ErrOperationInProgress):
		respondBackupConflict(c, "已有备份或恢复正在进行，请稍后再试")
		return
	case err != nil:
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份任务受理失败",
			Data:    nil,
		})
		return
	}

	respondAcceptedBackupOperation(c, grant, "备份任务已受理")
}

// respondBackupConflict 以 HTTP 409 返回运行任务摘要，既不进入维护也不等待。
func respondBackupConflict(c *gin.Context, message string) {
	// 协调器冲突不依赖业务运行时；只有实际内部任务冲突才读取任务摘要。
	// 这保证维护前的操作冲突可在数据库尚未初始化时稳定返回 409。
	tasks := []backup.RunningTask{}
	if backup.ActiveOperation() == nil {
		tasks = backup.RunningTasks()
	}
	c.JSON(http.StatusConflict, APIResponse[[]backup.RunningTask]{
		Code:    BadRequest,
		Message: message,
		Data:    tasks,
	})
}

func GetBackupList(c *gin.Context) {
	var req requests.BackupListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		req.Type = c.DefaultQuery("type", "all")
	}
	req.Normalize()
	// 目录清点只能在后台运行；列表请求立即返回当前索引和本轮状态。
	backup.TriggerInventoryScan()

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

	c.JSON(http.StatusOK, APIResponse[BackupListResponse]{
		Code:    Success,
		Message: "获取备份列表成功",
		Data: BackupListResponse{
			List:            records,
			Total:           total,
			Page:            req.Page,
			PageSize:        req.PageSize,
			InventoryStatus: backup.CurrentInventoryStatus(),
			LatestOperation: backup.LatestTerminalOperation(),
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

	c.JSON(http.StatusOK, APIResponse[models.BackupConfigReadDTO]{
		Code:    Success,
		Message: "获取备份配置成功",
		Data:    config.ToReadDTO(),
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
	if config.RequiresUnencryptedConfirmation(req.BackupEnabled, req.ScheduledBackupPassword) && !req.ConfirmUnencrypted {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "启用无密码定时备份前请确认风险",
			Data:    nil,
		})
		return
	}

	if req.BackupCron != "" {
		config.BackupCron = req.BackupCron
	}
	if req.BackupRetention > 0 {
		config.BackupRetention = req.BackupRetention
	}
	if req.BackupMaxCount >= 0 {
		config.BackupMaxCount = req.BackupMaxCount
	}
	if req.ScheduledBackupPassword != nil {
		if err := config.SetScheduledBackupPassword(*req.ScheduledBackupPassword); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{
				Code:    BadRequest,
				Message: "更新定时备份密码失败",
				Data:    nil,
			})
			return
		}
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

	// InitCron 会先停止旧调度器再按最新配置重建；禁用自动备份也会立即移除旧任务。
	synccron.InitCron()

	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "备份配置已更新",
		Data:    nil,
	})
}

// BackupOperationTokenHeader 是状态查询携带一次性令牌的请求头。
// 令牌只能走请求头，不得进入 URL、Cookie 或持久化浏览器存储。
const BackupOperationTokenHeader = "X-Backup-Operation-Token"

// GetBackupStatus 以 operation ID 和请求头令牌读取最近一次操作状态。
// 它位于认证之前，只读取受限状态目录中的外部状态，不触碰业务数据库。
func GetBackupStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")

	view, err := backup.AuthorizeOperation(c.Query("operation_id"), c.GetHeader(BackupOperationTokenHeader))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "备份操作不存在或状态令牌无效",
			Data:    nil,
		})
		return
	}

	c.JSON(http.StatusOK, APIResponse[backup.OperationView]{
		Code:    Success,
		Message: "获取备份状态成功",
		Data:    view,
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

	if err := req.Validate(); err != nil || req.RecordID == 0 {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "恢复请求参数不正确",
			Data:    nil,
		})
		return
	}
	if backup.ActiveOperation() != nil {
		respondBackupConflict(c, "已有备份或恢复正在进行，请稍后再试")
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

	switch req.Phase {
	case requests.BackupRestorePhasePreflight:
		result, err := backup.PreflightRestore(backup.RestorePreflightRequest{
			Kind:         backup.PreflightSourceRecord,
			RecordID:     record.ID,
			ArtifactPath: record.FilePath,
			Password:     []byte(req.Password),
		})
		if err != nil {
			respondRestoreError(c, err)
			return
		}
		c.JSON(http.StatusOK, APIResponse[backup.RestorePreflightResult]{
			Code:    Success,
			Message: "恢复预检通过",
			Data:    result,
		})
	case requests.BackupRestorePhaseConfirm:
		grant, err := backup.ConfirmRestore(backup.RestoreConfirmRequest{
			Kind:             backup.PreflightSourceRecord,
			RecordID:         record.ID,
			ArtifactPath:     record.FilePath,
			PreflightID:      req.PreflightID,
			Password:         []byte(req.Password),
			ConfirmOverwrite: req.ConfirmOverwrite,
		})
		if err != nil {
			respondRestoreError(c, err)
			return
		}
		respondAcceptedBackupOperation(c, grant, "恢复任务已受理")
	}
}

func UploadAndRestore(c *gin.Context) {
	var req requests.BackupRestoreRequest
	if err := c.ShouldBind(&req); err != nil || req.RecordID != 0 || req.Validate() != nil {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    BadRequest,
			Message: "恢复请求参数不正确",
			Data:    nil,
		})
		return
	}

	if backup.ActiveOperation() != nil {
		respondBackupConflict(c, "已有备份或恢复正在进行，请稍后再试")
		return
	}

	switch req.Phase {
	case requests.BackupRestorePhasePreflight:
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请上传备份文件"})
			return
		}
		defer file.Close()
		if strings.ToLower(filepath.Ext(header.Filename)) != ".zip" {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "仅支持 .zip 格式的备份文件"})
			return
		}
		artifactPath, _, err := backup.StageUploadArtifact(file)
		if err != nil {
			respondRestoreError(c, err)
			return
		}
		result, err := backup.PreflightRestore(backup.RestorePreflightRequest{
			Kind:         backup.PreflightSourceUpload,
			ArtifactPath: artifactPath,
			Password:     []byte(req.Password),
		})
		if err != nil {
			respondRestoreError(c, err)
			return
		}
		backup.TriggerInventoryScan()
		c.JSON(http.StatusOK, APIResponse[backup.RestorePreflightResult]{
			Code:    Success,
			Message: "恢复预检通过",
			Data:    result,
		})
	case requests.BackupRestorePhaseConfirm:
		source, err := backup.ResolvePreflightSource(req.PreflightID, backup.PreflightSourceUpload)
		if err != nil {
			respondRestoreError(c, err)
			return
		}
		grant, err := backup.ConfirmRestore(backup.RestoreConfirmRequest{
			Kind:             source.Kind,
			RecordID:         source.RecordID,
			ArtifactPath:     source.ArtifactPath,
			PreflightID:      req.PreflightID,
			Password:         []byte(req.Password),
			ConfirmOverwrite: req.ConfirmOverwrite,
		})
		if err != nil {
			respondRestoreError(c, err)
			return
		}
		respondAcceptedBackupOperation(c, grant, "恢复任务已受理")
	}
}

func respondAcceptedBackupOperation(c *gin.Context, grant backup.OperationGrant, message string) {
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusAccepted, APIResponse[BackupOperationAccepted]{
		Code:    Success,
		Message: message,
		Data:    BackupOperationAccepted{OperationID: grant.OperationID, Token: grant.Token},
	})
}

// respondRestoreError 保持既有恢复 URL 的业务拒绝为 HTTP 200；只有运行冲突使用 HTTP 409。
// 不把工件校验、密码、路径或数据库连接的技术细节返回给浏览器。
func respondRestoreError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, backup.ErrTasksRunning):
		respondBackupConflict(c, "有任务正在运行，请等待其结束后再恢复")
	case errors.Is(err, backup.ErrOperationInProgress):
		respondBackupConflict(c, "已有备份或恢复正在进行，请稍后再试")
	case errors.Is(err, backup.ErrPreflightInvalid):
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "恢复预检无效或已过期"})
	case errors.Is(err, backup.ErrRestoreOverwriteNotConfirmed):
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请确认配置和全部数据都会被覆盖"})
	default:
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "密码错误或工件损坏"})
	}
}
