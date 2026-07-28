package models

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"gorm.io/gorm"
)

const (
	BackupStatusPending   = "pending"
	BackupStatusRunning   = "running"
	BackupStatusCompleted = "completed"
	BackupStatusFailed    = "failed"
	BackupStatusCancelled = "cancelled"
	BackupStatusTimeout   = "timeout"

	BackupTypeManual          = "manual"
	BackupTypeAuto            = "auto"
	BackupTypeLegacy          = "legacy"
	BackupTypeImported        = "imported"
	BackupTypeTemporaryUpload = "temporary_upload"

	BackupFormatV1     = "v1"
	BackupFormatLegacy = "legacy"

	BackupVerificationVerified        = "verified"
	BackupVerificationPendingPassword = "pending_password"
	BackupVerificationInvalid         = "invalid"

	DefaultBackupRetention = 7
	DefaultBackupMaxCount  = 10
)

// AutoCleanupBackupTypes 是参与保留天数与数量自动清理的备份来源。
// 目录导入只可手动删除，上传暂存由定时备份整体清空，均不在此列。
var AutoCleanupBackupTypes = []string{BackupTypeManual, BackupTypeAuto, BackupTypeLegacy}

var GlobalBackupService *BackupService

// BackupService 只负责备份配置与工件索引；实际备份和恢复的互斥、状态与阶段日志
// 由 internal/backup 的操作协调器持有，避免布尔运行标记被当作成功依据。
type BackupService struct {
	config *BackupConfig
}

// BackupConfig 备份配置
type BackupConfig struct {
	BaseModel
	BackupEnabled                     int    `json:"backup_enabled" gorm:"default:0"`                      // 是否启用自动备份，0 表示禁用，1 表示启用
	BackupCron                        string `json:"backup_cron"`                                          // 备份 Cron 表达式
	BackupRetention                   int    `json:"backup_retention" gorm:"default:7"`                    // 备份保留天数
	BackupMaxCount                    int    `json:"backup_max_count" gorm:"default:10"`                   // 最多保留的备份数量
	ScheduledBackupPasswordCiphertext string `json:"-" gorm:"column:scheduled_backup_password_ciphertext"` // 仅供定时备份使用的本机加密密码
}

func (*BackupConfig) TableName() string {
	return "backup_config"
}

// BackupConfigReadDTO 是不包含本机密文的备份配置读取模型。
type BackupConfigReadDTO struct {
	ID                      uint   `json:"id"`
	CreatedAt               int64  `json:"created_at"`
	UpdatedAt               int64  `json:"updated_at"`
	BackupEnabled           int    `json:"backup_enabled"`
	BackupCron              string `json:"backup_cron"`
	BackupRetention         int    `json:"backup_retention"`
	BackupMaxCount          int    `json:"backup_max_count"`
	BackupEncryptionEnabled bool   `json:"backup_encryption_enabled"`
}

// ToReadDTO 构造不携带定时备份密码密文的读取模型。
func (config BackupConfig) ToReadDTO() BackupConfigReadDTO {
	return BackupConfigReadDTO{
		ID:                      config.ID,
		CreatedAt:               config.CreatedAt,
		UpdatedAt:               config.UpdatedAt,
		BackupEnabled:           config.BackupEnabled,
		BackupCron:              config.BackupCron,
		BackupRetention:         config.BackupRetention,
		BackupMaxCount:          config.BackupMaxCount,
		BackupEncryptionEnabled: config.ScheduledBackupPasswordCiphertext != "",
	}
}

// SetScheduledBackupPassword 用本机密钥保存定时备份密码；空密码会清除密文。
func (config *BackupConfig) SetScheduledBackupPassword(password string) error {
	if password == "" {
		config.ScheduledBackupPasswordCiphertext = ""
		return nil
	}
	ciphertext, err := helpers.EncryptLocalSecret(password)
	if err != nil {
		return fmt.Errorf("加密定时备份密码失败：%w", err)
	}
	config.ScheduledBackupPasswordCiphertext = ciphertext
	return nil
}

// ScheduledBackupPassword 解密仅供 Cron 触发的定时备份使用的密码。
func (config BackupConfig) ScheduledBackupPassword() (string, error) {
	if config.ScheduledBackupPasswordCiphertext == "" {
		return "", nil
	}
	password, err := helpers.DecryptLocalSecret(config.ScheduledBackupPasswordCiphertext)
	if err != nil {
		return "", fmt.Errorf("解密定时备份密码失败：%w", err)
	}
	return password, nil
}

// RequiresUnencryptedConfirmation 判断保存启用的定时备份且没有密码时是否需要显式确认。
func (config BackupConfig) RequiresUnencryptedConfirmation(backupEnabled int, password *string) bool {
	if backupEnabled != 1 {
		return false
	}
	if password != nil {
		return *password == ""
	}
	return config.ScheduledBackupPasswordCiphertext == ""
}

// BackupRecord 备份记录（历史记录）
type BackupRecord struct {
	BaseModel
	TaskID                uint    `json:"task_id"`                      // 关联的任务 ID
	Status                string  `json:"status"`                       // 任务状态：completed/cancelled/timeout/failed
	FilePath              string  `json:"file_path"`                    // 备份文件路径
	FileSize              int64   `json:"file_size"`                    // 备份文件大小（字节）
	DatabaseSize          int64   `json:"database_size"`                // 数据库大小（字节）
	TableCount            int     `json:"table_count"`                  // 表数量
	BackupDuration        int64   `json:"backup_duration"`              // 备份耗时（秒）
	BackupType            string  `json:"backup_type"`                  // 备份来源：manual/auto/legacy/imported/temporary_upload
	Format                string  `json:"format" gorm:"default:legacy"` // 工件格式：v1/legacy
	VerificationState     string  `json:"verification_state"`           // 非敏感验证状态
	VerificationErrorCode string  `json:"verification_error_code"`      // 稳定安全验证错误码
	InventoryPath         string  `json:"inventory_path"`               // 目录清点使用的规范路径
	InventoryFileSize     int64   `json:"inventory_file_size"`          // 目录清点使用的文件大小
	InventoryModifiedAt   int64   `json:"inventory_modified_at"`        // 目录清点使用的文件修改时间
	CreatedReason         string  `json:"created_reason"`               // 创建原因
	FailureReason         string  `json:"failure_reason"`               // 失败原因
	CompressionRatio      float64 `json:"compression_ratio"`            // 压缩比例
	IsCompressed          int     `json:"is_compressed"`                // 是否已压缩，0 表示否，1 表示是
	CompletedAt           int64   `json:"completed_at"`                 // 完成时间戳
}

func (*BackupRecord) TableName() string {
	return "backup_record"
}

func InitBackupService() *BackupService {
	if GlobalBackupService != nil {
		return GlobalBackupService
	}

	config := GetOrCreateBackupConfig()
	backupDir := filepath.Join(helpers.ConfigDir, "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		helpers.AppLogger.Errorf("创建备份目录失败：%v", err)
	}

	GlobalBackupService = &BackupService{config: config}

	helpers.AppLogger.Infof("备份服务已初始化，备份目录：%s", backupDir)
	return GlobalBackupService
}

func GetBackupService() *BackupService {
	if GlobalBackupService == nil {
		return InitBackupService()
	}
	return GlobalBackupService
}

func GetOrCreateBackupConfig() *BackupConfig {
	var config BackupConfig
	if err := db.Db.First(&config).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			config = BackupConfig{
				BackupEnabled:   0,
				BackupCron:      "0 3 * * *",
				BackupRetention: DefaultBackupRetention,
				BackupMaxCount:  DefaultBackupMaxCount,
			}
			if err := db.Db.Save(&config).Error; err != nil {
				helpers.AppLogger.Errorf("创建默认备份配置失败：%v", err)
				return &config
			}
			helpers.AppLogger.Info("已创建默认备份配置")
		} else {
			helpers.AppLogger.Errorf("获取备份配置失败：%v", err)
			return &BackupConfig{
				BackupRetention: DefaultBackupRetention,
				BackupMaxCount:  DefaultBackupMaxCount,
			}
		}
	}
	return &config
}

// CleanupOldBackups 按保留天数和数量清理应用生成的工件。
// 只有 manual、auto 和升级迁移标记的 legacy 记录参与计数与删除：
// imported 无法区分是本应用生成还是用户手工拷入，temporary_upload 由定时备份整体清空，
// 两者都不得被自动删除。
func (s *BackupService) CleanupOldBackups() {
	config := s.config

	var records []BackupRecord
	db.Db.Where("status = ?", BackupStatusCompleted).
		Where("backup_type IN ?", AutoCleanupBackupTypes).
		Order("created_at DESC").
		Find(&records)

	now := time.Now().Unix()
	retentionSeconds := int64(config.BackupRetention * 24 * 60 * 60)

	for i, record := range records {
		shouldDelete := false
		reason := ""

		if config.BackupMaxCount > 0 && i >= config.BackupMaxCount {
			shouldDelete = true
			reason = "超过最大备份数量"
		}

		if config.BackupRetention > 0 && (now-record.CreatedAt) > retentionSeconds {
			shouldDelete = true
			reason = "超过保留天数"
		}

		if shouldDelete {
			if err := s.DeleteBackup(record.ID); err != nil {
				helpers.AppLogger.Warnf("清理旧备份失败，ID=%d：%v，原因：%s", record.ID, err, reason)
			} else {
				helpers.AppLogger.Infof("已清理旧备份，ID=%d，原因：%s", record.ID, reason)
			}
		}
	}
}

func (s *BackupService) DeleteBackup(recordID uint) error {
	var record BackupRecord
	if err := db.Db.First(&record, recordID).Error; err != nil {
		return fmt.Errorf("备份记录不存在")
	}

	if record.FilePath != "" {
		if _, err := os.Stat(record.FilePath); err == nil {
			if err := os.Remove(record.FilePath); err != nil {
				helpers.AppLogger.Warnf("删除备份文件失败：%v", err)
			}
		}
	}

	if err := db.Db.Delete(&record).Error; err != nil {
		return fmt.Errorf("删除备份记录失败：%v", err)
	}

	return nil
}

func (s *BackupService) GetBackupRecords(page, pageSize int, backupType string) ([]BackupRecord, int64, error) {
	var records []BackupRecord
	var total int64

	query := db.Db.Model(&BackupRecord{})
	if backupType != "" && backupType != "all" {
		query = query.Where("backup_type = ?", backupType)
	}

	query.Count(&total)

	offset := (page - 1) * pageSize
	if err := query.Order("created_at DESC").
		Offset(offset).Limit(pageSize).
		Find(&records).Error; err != nil {
		return nil, 0, err
	}

	return records, total, nil
}

func (s *BackupService) GetBackupConfig() *BackupConfig {
	return s.config
}

func (s *BackupService) UpdateBackupConfig(config *BackupConfig) error {
	if err := db.Db.Save(config).Error; err != nil {
		return err
	}
	s.config = config
	return nil
}

// InventoryRecords 返回全部工件索引记录，供目录清点比对缓存身份。
func InventoryRecords() ([]BackupRecord, error) {
	var records []BackupRecord
	if err := db.Db.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("读取备份记录失败：%w", err)
	}
	return records, nil
}

// UpsertInventoryRecord 按规范路径写入或更新目录清点结果。
// 记录 ID 为零表示新发现的工件；否则只更新清点相关字段，不改动创建信息。
func UpsertInventoryRecord(record *BackupRecord) error {
	if record.InventoryPath == "" {
		return fmt.Errorf("写入备份记录失败：缺少规范路径")
	}
	if record.ID == 0 {
		if err := db.Db.Create(record).Error; err != nil {
			return fmt.Errorf("创建备份记录失败：%w", err)
		}
		return nil
	}
	updates := map[string]any{
		"status":                  record.Status,
		"file_path":               record.FilePath,
		"file_size":               record.FileSize,
		"backup_type":             record.BackupType,
		"format":                  record.Format,
		"verification_state":      record.VerificationState,
		"verification_error_code": record.VerificationErrorCode,
		"inventory_path":          record.InventoryPath,
		"inventory_file_size":     record.InventoryFileSize,
		"inventory_modified_at":   record.InventoryModifiedAt,
	}
	if err := db.Db.Model(&BackupRecord{}).Where("id = ?", record.ID).Updates(updates).Error; err != nil {
		return fmt.Errorf("更新备份记录失败：%w", err)
	}
	return nil
}

// DeleteInventoryRecord 只删除索引记录本身。
// 目录清点用它清理文件已不存在的记录，因此不能连带删除工件文件。
func DeleteInventoryRecord(recordID uint) error {
	if err := db.Db.Delete(&BackupRecord{}, recordID).Error; err != nil {
		return fmt.Errorf("删除备份记录失败：%w", err)
	}
	return nil
}
