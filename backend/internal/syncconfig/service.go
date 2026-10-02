package syncconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"qmediasync/internal/db"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"
	"qmediasync/internal/syncscope"

	"gorm.io/gorm"
)

const (
	ErrorCodeInvalidRequest       = "INVALID_REQUEST"
	ErrorCodeAccountSourceInvalid = "ACCOUNT_SOURCE_INVALID"
	ErrorCodeSyncPathNotFound     = "SYNC_PATH_NOT_FOUND"
	ErrorCodeRuleOwnership        = "DIRECTORY_UPLOAD_RULE_OWNERSHIP"
	ErrorCodeRuleBoundary         = "DIRECTORY_UPLOAD_RULE_BOUNDARY"
	ErrorCodeRuleConflict         = "DIRECTORY_UPLOAD_RULE_CONFLICT"
	ErrorCodeIdempotencyConflict  = "IDEMPOTENCY_CONFLICT"
	ErrorCodeDatabaseSave         = "DATABASE_SAVE_FAILED"
)

// FieldError 描述可定位到基础字段或规则 client_id 的校验错误。
type FieldError struct {
	ClientID string `json:"client_id,omitempty"`
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// SaveError 是同步目录聚合保存业务错误。
type SaveError struct {
	Code        string
	Message     string
	FieldErrors []FieldError
	Cause       error
}

func (err *SaveError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}

func (err *SaveError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Cause
}

// SyncPathInput 描述同步目录基础配置。
type SyncPathInput struct {
	SourceType   models.SourceType
	AccountID    uint
	BaseCid      string
	LocalPath    string
	RemotePath   string
	EnableCron   bool
	CustomConfig bool
	Setting      models.SettingStrm
}

// DirectoryUploadRuleInput 描述一条目录监控上传规则。
type DirectoryUploadRuleInput struct {
	ClientID                 string
	ID                       uint
	Enabled                  bool
	MonitorPath              string
	RemoteRootPath           string
	RemoteRootID             string
	Recursive                bool
	UploadMetadata           bool
	WatchMode                models.DirectoryUploadWatchMode
	StartupScanEnabled       bool
	ProcessedCacheTTLSeconds int
	DeleteSourceAfterSuccess bool
	IgnorePatterns           []string
	OverwriteMode            models.DirectoryUploadOverwriteMode
}

// DirectoryUploadInput 描述同步目录的目录上传最终配置。
type DirectoryUploadInput struct {
	Enabled bool
	Rules   []DirectoryUploadRuleInput
}

// SaveSyncPathCommand 保存同步目录聚合。
type SaveSyncPathCommand struct {
	ID              uint
	IdempotencyKey  string
	SyncPath        SyncPathInput
	DirectoryUpload *DirectoryUploadInput
}

// DirectoryUploadResult 是聚合保存后的目录上传配置。
type DirectoryUploadResult struct {
	Enabled bool                          `json:"enabled"`
	Rules   []*models.DirectoryUploadRule `json:"rules"`
}

// SaveSyncPathResult 是同步目录聚合保存结果。
type SaveSyncPathResult struct {
	SyncPath        *models.SyncPath      `json:"sync_path"`
	DirectoryUpload DirectoryUploadResult `json:"directory_upload"`
	Warnings        []string              `json:"warnings"`
}

// ServiceOptions 配置同步目录应用服务及事务后副作用。
type ServiceOptions struct {
	DB                             *gorm.DB
	CreateLocalDirectory           func(string) error
	ReloadSyncCron                 func()
	ReloadDirectoryUpload          func()
	ReloadSyncCronWithError        func() error
	ReloadDirectoryUploadWithError func() error
	RunTransaction                 func(context.Context, *gorm.DB, func(*gorm.DB) error) error
}

// Service 保存同步目录配置聚合。
type Service struct {
	db                    *gorm.DB
	createLocalDirectory  func(string) error
	reloadSyncCron        func() error
	reloadDirectoryUpload func() error
	runTransaction        func(context.Context, *gorm.DB, func(*gorm.DB) error) error
}

// NewService 创建同步目录配置应用服务。
func NewService(options ServiceOptions) *Service {
	handle := options.DB
	if handle == nil {
		handle = db.Db
	}
	createLocalDirectory := options.CreateLocalDirectory
	if createLocalDirectory == nil {
		createLocalDirectory = func(string) error { return nil }
	}
	reloadSyncCron := options.ReloadSyncCronWithError
	if reloadSyncCron == nil && options.ReloadSyncCron != nil {
		reloadSyncCron = func() error {
			options.ReloadSyncCron()
			return nil
		}
	}
	if reloadSyncCron == nil {
		reloadSyncCron = func() error { return nil }
	}
	reloadDirectoryUpload := options.ReloadDirectoryUploadWithError
	if reloadDirectoryUpload == nil && options.ReloadDirectoryUpload != nil {
		reloadDirectoryUpload = func() error {
			options.ReloadDirectoryUpload()
			return nil
		}
	}
	if reloadDirectoryUpload == nil {
		reloadDirectoryUpload = func() error { return nil }
	}
	runTransaction := options.RunTransaction
	if runTransaction == nil {
		runTransaction = func(ctx context.Context, handle *gorm.DB, fn func(*gorm.DB) error) error {
			return handle.WithContext(ctx).Transaction(fn)
		}
	}
	return &Service{
		db:                    handle,
		createLocalDirectory:  createLocalDirectory,
		reloadSyncCron:        reloadSyncCron,
		reloadDirectoryUpload: reloadDirectoryUpload,
		runTransaction:        runTransaction,
	}
}

// NewDefaultService 创建使用生产副作用的同步目录配置应用服务。
func NewDefaultService() *Service {
	return NewService(ServiceOptions{
		DB:                             db.Db,
		CreateLocalDirectory:           func(path string) error { return os.MkdirAll(path, 0o777) },
		ReloadSyncCronWithError:        synccron.InitSyncCronWithError,
		ReloadDirectoryUploadWithError: directoryupload.ReloadDirectoryUploadServiceWithError,
	})
}

// Save 原子保存 SyncPath 与目录监控上传规则，并在 commit 后执行运行态副作用。
func (service *Service) Save(ctx context.Context, command SaveSyncPathCommand) (*SaveSyncPathResult, error) {
	if service == nil || service.db == nil {
		return nil, newSaveError(ErrorCodeDatabaseSave, "数据库连接为空", nil)
	}
	if err := validateCommand(command); err != nil {
		return nil, err
	}
	directoryInput, err := normalizeDirectoryUploadInput(command.SyncPath.SourceType, command.DirectoryUpload)
	if err != nil {
		return nil, err
	}
	nextPath := &models.SyncPath{
		SourceType: command.SyncPath.SourceType, AccountId: command.SyncPath.AccountID,
		LocalPath: command.SyncPath.LocalPath, RemotePath: command.SyncPath.RemotePath,
	}
	var release func()
	positionChanged := command.ID == 0
	if command.ID == 0 {
		release, err = syncscope.Acquire(ctx, nextPath.Scope())
	} else {
		var current *models.SyncPath
		current, release, err = models.AcquireSyncPathConfigScope(ctx, service.db, command.ID, nextPath.Scope())
		if err == nil {
			positionChanged = current.SourceType != nextPath.SourceType || current.AccountId != nextPath.AccountId || current.LocalPath != nextPath.LocalPath || current.RemotePath != nextPath.RemotePath
		}
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, newSaveError(ErrorCodeSyncPathNotFound, "同步目录不存在", err)
		}
		return nil, newSaveError(ErrorCodeDatabaseSave, "保存同步目录失败", err)
	}
	defer release()
	idempotencyKey := strings.TrimSpace(command.IdempotencyKey)
	idempotencyKeyHash := hashIdempotencyKey(idempotencyKey)
	var result *SaveSyncPathResult
	replayed := false
	finishPosition := func() {}
	if positionChanged {
		finishPosition = models.BeginSyncPositionMutation()
	}
	defer finishPosition()
	err = service.runTransaction(ctx, service.db, func(tx *gorm.DB) error {
		if command.ID == 0 && idempotencyKey != "" {
			existing, found, err := findIdempotencyRecordWithDB(tx, idempotencyKeyHash)
			if err != nil {
				return err
			}
			if found {
				if existing.Status != "completed" || existing.SyncPathId == 0 {
					return newSaveError(ErrorCodeIdempotencyConflict, "相同幂等键的创建请求正在处理", nil)
				}
				result, err = loadAggregateWithDB(tx, existing.SyncPathId)
				replayed = err == nil
				return err
			}
			if err := tx.Create(&models.SyncPathIdempotencyRecord{KeyHash: idempotencyKeyHash, Status: "pending"}).Error; err != nil {
				return newSaveError(ErrorCodeIdempotencyConflict, "幂等键冲突", err)
			}
		}
		if replayed {
			return nil
		}
		writeInput := models.SyncPathWriteInput{
			SourceType:             command.SyncPath.SourceType,
			AccountID:              command.SyncPath.AccountID,
			BaseCid:                command.SyncPath.BaseCid,
			LocalPath:              command.SyncPath.LocalPath,
			RemotePath:             command.SyncPath.RemotePath,
			EnableCron:             command.SyncPath.EnableCron,
			DirectoryUploadEnabled: directoryInput.Enabled,
			CustomConfig:           command.SyncPath.CustomConfig,
			Setting:                command.SyncPath.Setting,
		}
		var syncPath *models.SyncPath
		var err error
		if command.ID != 0 {
			syncPath = &models.SyncPath{}
			if err = tx.First(syncPath, command.ID).Error; errors.Is(err, gorm.ErrRecordNotFound) {
				return newSaveError(ErrorCodeSyncPathNotFound, "同步目录不存在", err)
			}
			if err != nil {
				return err
			}
			if err := validateImmutableSyncPathFields(syncPath, command.SyncPath); err != nil {
				return err
			}
		}
		if err := validateAccountWithDB(tx, command.SyncPath); err != nil {
			return err
		}
		if command.ID == 0 {
			syncPath, err = models.CreateSyncPathWithDB(tx, writeInput)
		} else {
			err = models.UpdateSyncPathWithDB(tx, syncPath, writeInput)
		}
		if err != nil {
			return newSaveError(ErrorCodeDatabaseSave, "保存同步目录失败", err)
		}
		rules, err := buildRulesWithDB(tx, syncPath, directoryInput.Rules)
		if err != nil {
			return err
		}
		savedRules, err := models.SaveDirectoryUploadRulesForSyncPathWithDB(tx, syncPath, directoryInput.Enabled, rules)
		if err != nil {
			return classifyRuleError(err, directoryInput.Rules)
		}
		result = &SaveSyncPathResult{
			SyncPath:        syncPath,
			DirectoryUpload: DirectoryUploadResult{Enabled: directoryInput.Enabled, Rules: savedRules},
			Warnings:        []string{},
		}
		if command.ID == 0 && idempotencyKey != "" {
			if err := tx.Model(&models.SyncPathIdempotencyRecord{}).
				Where("key_hash = ?", idempotencyKeyHash).
				Updates(map[string]any{"sync_path_id": syncPath.ID, "status": "completed"}).Error; err != nil {
				return newSaveError(ErrorCodeDatabaseSave, "保存幂等结果失败", err)
			}
		}
		return nil
	})
	finishPosition()
	if err != nil {
		if saveErr, ok := errors.AsType[*SaveError](err); ok {
			return nil, saveErr
		}
		return nil, newSaveError(ErrorCodeDatabaseSave, "保存同步目录失败", err)
	}
	if result == nil || replayed {
		return result, nil
	}
	if err := service.createLocalDirectory(result.SyncPath.GetFullLocalPath()); err != nil {
		result.Warnings = append(result.Warnings, "同步目录已保存，但创建本地目录失败")
	}
	// 重载服务可能等待任务结束，先让其他文件处理继续。
	release()
	if err := service.reloadSyncCron(); err != nil {
		result.Warnings = append(result.Warnings, "同步目录已保存，但重载定时同步任务失败")
	}
	if err := service.reloadDirectoryUpload(); err != nil {
		result.Warnings = append(result.Warnings, "同步目录已保存，但重载目录监控上传服务失败")
	}
	return result, nil
}

func validateCommand(command SaveSyncPathCommand) error {
	if command.SyncPath.SourceType == "" || strings.TrimSpace(command.SyncPath.BaseCid) == "" ||
		strings.TrimSpace(command.SyncPath.LocalPath) == "" || strings.TrimSpace(command.SyncPath.RemotePath) == "" {
		return newSaveError(ErrorCodeInvalidRequest, "请求格式错误", nil)
	}
	return nil
}

func validateImmutableSyncPathFields(existing *models.SyncPath, input SyncPathInput) error {
	if existing.SourceType != input.SourceType {
		return &SaveError{
			Code:        ErrorCodeInvalidRequest,
			Message:     "同步来源不能修改",
			FieldErrors: []FieldError{{Field: "source_type", Message: "同步来源不能修改"}},
		}
	}
	if existing.AccountId != input.AccountID {
		return &SaveError{
			Code:        ErrorCodeInvalidRequest,
			Message:     "同步账号不能修改",
			FieldErrors: []FieldError{{Field: "account_id", Message: "同步账号不能修改"}},
		}
	}
	return nil
}

func hashIdempotencyKey(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func normalizeDirectoryUploadInput(sourceType models.SourceType, input *DirectoryUploadInput) (*DirectoryUploadInput, error) {
	if sourceType != models.SourceType115 {
		if input != nil && (input.Enabled || len(input.Rules) > 0) {
			return nil, newSaveError(ErrorCodeInvalidRequest, "非 115 来源不能配置目录监控上传", nil)
		}
		return &DirectoryUploadInput{Rules: []DirectoryUploadRuleInput{}}, nil
	}
	if input == nil {
		return &DirectoryUploadInput{Rules: []DirectoryUploadRuleInput{}}, nil
	}
	normalized := &DirectoryUploadInput{
		Enabled: input.Enabled,
		Rules:   make([]DirectoryUploadRuleInput, len(input.Rules)),
	}
	copy(normalized.Rules, input.Rules)
	return normalized, nil
}

func validateAccountWithDB(tx *gorm.DB, input SyncPathInput) error {
	if input.SourceType == models.SourceTypeLocal {
		return nil
	}
	var account models.Account
	if err := tx.First(&account, input.AccountID).Error; err != nil {
		return &SaveError{
			Code:        ErrorCodeAccountSourceInvalid,
			Message:     "账号不存在",
			FieldErrors: []FieldError{{Field: "account_id", Message: "账号不存在"}},
			Cause:       err,
		}
	}
	if account.SourceType != input.SourceType {
		return &SaveError{
			Code:        ErrorCodeAccountSourceInvalid,
			Message:     "账号类型与同步源类型不一致",
			FieldErrors: []FieldError{{Field: "account_id", Message: "账号类型与同步源类型不一致"}},
		}
	}
	return nil
}

func buildRulesWithDB(tx *gorm.DB, syncPath *models.SyncPath, inputs []DirectoryUploadRuleInput) ([]*models.DirectoryUploadRule, error) {
	existingRules, err := models.GetDirectoryUploadRulesWithDB(tx, syncPath.ID)
	if err != nil {
		return nil, newSaveError(ErrorCodeDatabaseSave, "读取目录监控上传规则失败", err)
	}
	existingByID := make(map[uint]*models.DirectoryUploadRule, len(existingRules))
	for _, rule := range existingRules {
		existingByID[rule.ID] = rule
	}
	rules := make([]*models.DirectoryUploadRule, 0, len(inputs))
	for _, input := range inputs {
		rule := &models.DirectoryUploadRule{}
		if input.ID > 0 {
			existing := existingByID[input.ID]
			if existing == nil {
				return nil, &SaveError{Code: ErrorCodeRuleOwnership, Message: "目录监控上传规则不属于当前同步目录", FieldErrors: []FieldError{{ClientID: input.ClientID, Field: "id", Message: "规则不属于当前同步目录"}}}
			}
			*rule = *existing
		}
		rule.SyncPathId = syncPath.ID
		rule.AccountId = syncPath.AccountId
		rule.Enabled = input.Enabled
		rule.MonitorPath = strings.TrimSpace(input.MonitorPath)
		rule.RemoteRootPath = strings.TrimSpace(input.RemoteRootPath)
		rule.RemoteRootId = strings.TrimSpace(input.RemoteRootID)
		rule.Recursive = input.Recursive
		rule.UploadMetadata = input.UploadMetadata
		rule.WatchMode = input.WatchMode
		if rule.WatchMode == "" {
			rule.WatchMode = models.DirectoryUploadWatchModeAuto
		}
		rule.StabilitySeconds = models.DirectoryUploadDefaultStabilitySeconds
		rule.StabilityCheckIntervalSeconds = models.DirectoryUploadDefaultStabilityCheckIntervalSeconds
		rule.StabilityRequiredCount = models.DirectoryUploadDefaultStabilityRequiredCount
		rule.RescanIntervalSeconds = models.DirectoryUploadDefaultRescanIntervalSeconds
		rule.StartupScanEnabled = input.StartupScanEnabled
		rule.ProcessedCacheTTLSeconds = input.ProcessedCacheTTLSeconds
		if rule.ProcessedCacheTTLSeconds <= 0 {
			rule.ProcessedCacheTTLSeconds = 600
		}
		rule.DeleteSourceAfterSuccess = input.DeleteSourceAfterSuccess
		rule.IgnorePatterns = input.IgnorePatterns
		rule.OverwriteMode = input.OverwriteMode
		if rule.OverwriteMode == "" {
			rule.OverwriteMode = models.DirectoryUploadOverwriteSkipSame
		}
		if err := rule.SetIgnorePatterns(input.IgnorePatterns); err != nil {
			return nil, newSaveError(ErrorCodeInvalidRequest, "目录监控上传规则校验失败", err)
		}
		if err := validateRuleEnums(rule); err != nil {
			return nil, &SaveError{Code: ErrorCodeInvalidRequest, Message: err.Error(), FieldErrors: []FieldError{{ClientID: input.ClientID, Field: ruleFieldForError(err), Message: err.Error()}}, Cause: err}
		}
		if err := rule.ValidateWithSyncPath(syncPath); err != nil {
			return nil, &SaveError{Code: ErrorCodeRuleBoundary, Message: err.Error(), FieldErrors: []FieldError{{ClientID: input.ClientID, Field: ruleFieldForError(err), Message: err.Error()}}, Cause: err}
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

func validateRuleEnums(rule *models.DirectoryUploadRule) error {
	switch rule.WatchMode {
	case models.DirectoryUploadWatchModeAuto, models.DirectoryUploadWatchModeFSNotify, models.DirectoryUploadWatchModePolling:
	default:
		return fmt.Errorf("不支持的监控模式：%s", rule.WatchMode)
	}
	switch rule.OverwriteMode {
	case models.DirectoryUploadOverwriteSkipSame, models.DirectoryUploadOverwriteFailConflict, models.DirectoryUploadOverwriteReplaceConflict:
	default:
		return fmt.Errorf("不支持的同名文件处理方式：%s", rule.OverwriteMode)
	}
	return nil
}

func ruleFieldForError(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "监控目录"):
		return "monitor_path"
	case strings.Contains(message, "根目录 ID"):
		return "remote_root_id"
	case strings.Contains(message, "远端"):
		return "remote_root_path"
	case strings.Contains(message, "监控模式"):
		return "watch_mode"
	case strings.Contains(message, "同名文件"):
		return "overwrite_mode"
	default:
		return "rules"
	}
}

func classifyRuleError(err error, inputs []DirectoryUploadRuleInput) error {
	message := err.Error()
	code := ErrorCodeRuleBoundary
	if strings.Contains(message, "重复") || strings.Contains(message, "重叠") || strings.Contains(message, "至少启用") {
		code = ErrorCodeRuleConflict
	}
	fieldErrors := make([]FieldError, 0)
	if code == ErrorCodeRuleConflict {
		for _, input := range inputs {
			if input.ClientID == "" {
				continue
			}
			if strings.Contains(message, "至少启用") || strings.Contains(message, strings.TrimSpace(input.MonitorPath)) {
				fieldErrors = append(fieldErrors, FieldError{
					ClientID: input.ClientID,
					Field:    "monitor_path",
					Message:  message,
				})
			}
		}
	}
	return &SaveError{Code: code, Message: message, FieldErrors: fieldErrors, Cause: err}
}

func findIdempotencyRecordWithDB(tx *gorm.DB, keyHash string) (*models.SyncPathIdempotencyRecord, bool, error) {
	var record models.SyncPathIdempotencyRecord
	err := tx.Where("key_hash = ?", strings.TrimSpace(keyHash)).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return &record, err == nil, err
}

func loadAggregateWithDB(tx *gorm.DB, syncPathID uint) (*SaveSyncPathResult, error) {
	var syncPath models.SyncPath
	if err := tx.First(&syncPath, syncPathID).Error; err != nil {
		return nil, err
	}
	rules, err := models.GetDirectoryUploadRulesWithDB(tx, syncPathID)
	if err != nil {
		return nil, err
	}
	return &SaveSyncPathResult{
		SyncPath:        &syncPath,
		DirectoryUpload: DirectoryUploadResult{Enabled: syncPath.DirectoryUploadEnabled, Rules: rules},
		Warnings:        []string{},
	}, nil
}

func newSaveError(code string, message string, cause error) *SaveError {
	return &SaveError{Code: code, Message: message, Cause: cause}
}

func (result SaveSyncPathResult) String() string {
	if result.SyncPath == nil {
		return ""
	}
	return fmt.Sprintf("sync_path:%d", result.SyncPath.ID)
}
