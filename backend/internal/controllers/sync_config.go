package controllers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/syncconfig"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
	validator "github.com/go-playground/validator/v10"
)

type saveSyncPathAggregateRequest struct {
	SyncPath        requests.SyncPathRequest         `json:"sync_path"`
	DirectoryUpload *directoryUploadRulesSaveRequest `json:"directory_upload"`
}

var newSyncPathConfigService = syncconfig.NewDefaultService

// CreateSyncPathAggregate 原子创建同步目录和目录监控上传规则。
// @Summary 原子创建同步目录
// @Description 原子创建同步目录基础配置和目录监控上传规则
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param Idempotency-Key header string false "创建幂等键"
// @Param request body saveSyncPathAggregateRequest true "同步目录聚合配置"
// @Success 200 {object} object
// @Failure 400 {object} object
// @Failure 409 {object} object
// @Failure 500 {object} object
// @Router /sync/paths [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CreateSyncPathAggregate(c *gin.Context) {
	saveSyncPathAggregate(c, 0)
}

// UpdateSyncPathAggregate 原子更新同步目录和目录监控上传规则。
// @Summary 原子更新同步目录
// @Description 原子更新同步目录基础配置和目录监控上传规则
// @Tags 同步管理
// @Accept json
// @Produce json
// @Param id path integer true "同步目录 ID"
// @Param request body saveSyncPathAggregateRequest true "同步目录聚合配置"
// @Success 200 {object} object
// @Failure 400 {object} object
// @Failure 404 {object} object
// @Failure 500 {object} object
// @Router /sync/paths/{id} [put]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateSyncPathAggregate(c *gin.Context) {
	syncPathID, ok := parseSyncPathAggregateIDParam(c, "id")
	if !ok {
		return
	}
	saveSyncPathAggregate(c, syncPathID)
}

func parseSyncPathAggregateIDParam(c *gin.Context, name string) (uint, bool) {
	raw := strings.TrimSpace(c.Param(name))
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		writeSyncPathSaveError(
			c,
			http.StatusBadRequest,
			syncconfig.ErrorCodeInvalidRequest,
			"请求参数错误",
			[]syncconfig.FieldError{{Field: name, Message: "格式错误"}},
		)
		return 0, false
	}
	return uint(value), true
}

func saveSyncPathAggregate(c *gin.Context, syncPathID uint) {
	var req saveSyncPathAggregateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeSyncPathSaveError(c, http.StatusBadRequest, syncconfig.ErrorCodeInvalidRequest, "请求格式错误", syncPathRequestFieldErrors(err))
		return
	}
	if err := req.SyncPath.Validate(); err != nil {
		writeSyncPathSaveError(c, http.StatusBadRequest, syncconfig.ErrorCodeInvalidRequest, err.Error(), syncPathRequestFieldErrors(err))
		return
	}
	directoryInput, err := buildDirectoryUploadCommandInput(req.DirectoryUpload)
	if err != nil {
		writeSyncPathSaveError(c, http.StatusBadRequest, syncconfig.ErrorCodeInvalidRequest, err.Error(), nil)
		return
	}
	result, err := newSyncPathConfigService().Save(c.Request.Context(), syncconfig.SaveSyncPathCommand{
		ID:             syncPathID,
		IdempotencyKey: strings.TrimSpace(c.GetHeader("Idempotency-Key")),
		SyncPath: syncconfig.SyncPathInput{
			SourceType:   req.SyncPath.SourceType,
			AccountID:    req.SyncPath.AccountID,
			BaseCid:      strings.TrimSpace(req.SyncPath.BaseCid),
			LocalPath:    strings.TrimSpace(req.SyncPath.LocalPath),
			RemotePath:   req.SyncPath.NormalizedRemotePath(),
			EnableCron:   req.SyncPath.EnableCron,
			CustomConfig: req.SyncPath.CustomConfig,
			Setting:      req.SyncPath.StrmSettingModel(),
		},
		DirectoryUpload: directoryInput,
	})
	if err != nil {
		if saveErr, ok := errors.AsType[*syncconfig.SaveError](err); ok {
			status := http.StatusBadRequest
			switch saveErr.Code {
			case syncconfig.ErrorCodeSyncPathNotFound:
				status = http.StatusNotFound
			case syncconfig.ErrorCodeIdempotencyConflict:
				status = http.StatusConflict
			case syncconfig.ErrorCodeDatabaseSave:
				status = http.StatusInternalServerError
			}
			writeSyncPathSaveError(c, status, saveErr.Code, saveErr.Message, saveErr.FieldErrors)
			return
		}
		writeSyncPathSaveError(c, http.StatusInternalServerError, syncconfig.ErrorCodeDatabaseSave, "保存同步目录失败", nil)
		return
	}
	message := "保存同步目录成功"
	if syncPathID == 0 {
		message = "添加同步目录成功"
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: message, Data: result})
}

// syncPathRequestFieldErrors 将请求绑定和基础配置校验错误映射到表单字段。
func syncPathRequestFieldErrors(err error) []syncconfig.FieldError {
	if requestErr, ok := errors.AsType[validation.Error](err); ok {
		return []syncconfig.FieldError{{Field: requestErr.Field, Message: requestErr.Message}}
	}
	bindingErrs, ok := errors.AsType[validator.ValidationErrors](err)
	if !ok {
		return nil
	}
	fieldNames := map[string]string{
		"SourceType": "source_type",
		"AccountID":  "account_id",
		"BaseCid":    "base_cid",
		"LocalPath":  "local_path",
		"RemotePath": "remote_path",
	}
	fieldErrors := make([]syncconfig.FieldError, 0, len(bindingErrs))
	for _, bindingErr := range bindingErrs {
		field, ok := fieldNames[bindingErr.Field()]
		if !ok {
			continue
		}
		message := "格式错误"
		if bindingErr.Tag() == "required" {
			message = "不能为空"
		}
		fieldErrors = append(fieldErrors, syncconfig.FieldError{Field: field, Message: message})
	}
	return fieldErrors
}

func buildDirectoryUploadCommandInput(req *directoryUploadRulesSaveRequest) (*syncconfig.DirectoryUploadInput, error) {
	if req == nil {
		return nil, nil
	}
	if req.Enabled == nil || req.Rules == nil {
		return nil, errors.New("目录监控上传配置格式错误")
	}
	result := &syncconfig.DirectoryUploadInput{Enabled: *req.Enabled, Rules: make([]syncconfig.DirectoryUploadRuleInput, 0, len(*req.Rules))}
	for _, item := range *req.Rules {
		enabled := true
		if item.Enabled != nil {
			enabled = *item.Enabled
		}
		recursive := true
		if item.Recursive != nil {
			recursive = *item.Recursive
		}
		startupScanEnabled := true
		if item.StartupScanEnabled != nil {
			startupScanEnabled = *item.StartupScanEnabled
		}
		watchMode := item.WatchMode
		if watchMode == "" {
			watchMode = models.DirectoryUploadWatchModeAuto
		}
		processedTTL := item.ProcessedCacheTTLSeconds
		if processedTTL <= 0 {
			processedTTL = 600
		}
		overwriteMode := item.OverwriteMode
		if overwriteMode == "" {
			overwriteMode = models.DirectoryUploadOverwriteSkipSame
		}
		deleteSource := false
		if item.DeleteSourceAfterSuccess != nil {
			deleteSource = *item.DeleteSourceAfterSuccess
		}
		uploadMetadata := false
		if item.UploadMetadata != nil {
			uploadMetadata = *item.UploadMetadata
		}
		result.Rules = append(result.Rules, syncconfig.DirectoryUploadRuleInput{
			ClientID:                 item.ClientID,
			ID:                       item.ID,
			Enabled:                  enabled,
			MonitorPath:              item.MonitorPath,
			RemoteRootPath:           item.RemoteRootPath,
			RemoteRootID:             item.RemoteRootID,
			Recursive:                recursive,
			UploadMetadata:           uploadMetadata,
			WatchMode:                watchMode,
			StartupScanEnabled:       startupScanEnabled,
			ProcessedCacheTTLSeconds: processedTTL,
			DeleteSourceAfterSuccess: deleteSource,
			IgnorePatterns:           item.IgnorePatterns,
			OverwriteMode:            overwriteMode,
		})
	}
	return result, nil
}

func writeSyncPathSaveError(c *gin.Context, status int, errorCode string, message string, fieldErrors []syncconfig.FieldError) {
	if fieldErrors == nil {
		fieldErrors = []syncconfig.FieldError{}
	}
	c.JSON(status, APIResponse[any]{
		Code:    BadRequest,
		Message: message,
		Data: gin.H{
			"error_code":   errorCode,
			"field_errors": fieldErrors,
		},
	})
}
