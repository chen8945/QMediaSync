package models

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"gorm.io/gorm"
)

var (
	PauseSyncQueuesFunc  func()
	ResumeSyncQueuesFunc func()
)

const (
	BackupStatusPending     = "pending"
	BackupStatusRunning     = "running"
	BackupStatusCompleted   = "completed"
	BackupStatusFailed      = "failed"
	BackupStatusCancelled   = "cancelled"
	BackupStatusTimeout     = "timeout"
	BackupStatusUnconfirmed = "unconfirmed"

	BackupTypeManual = "manual"
	BackupTypeAuto   = "auto"

	DefaultBackupRetention = 7
	DefaultBackupMaxCount  = 10
	DefaultBackupTimeout   = 30 * time.Minute
	MaxBackupTimeout       = 2 * time.Hour
)

var (
	GlobalBackupService *BackupService
	backupMutex         sync.Mutex

	errBackupFileNotManaged = errors.New("备份文件不在当前备份目录内，拒绝删除")
)

type BackupService struct {
	db             *gorm.DB
	config         *BackupConfig
	backupDir      string
	isRunning      bool
	runningMutex   sync.RWMutex
	cancelFunc     context.CancelFunc
	timeout        time.Duration
	statusChangeMu sync.Mutex
}

// BackupConfig 备份配置
type BackupConfig struct {
	BaseModel
	BackupEnabled   int    `json:"backup_enabled" gorm:"default:0"`    // 是否启用自动备份，0 表示禁用，1 表示启用
	BackupCron      string `json:"backup_cron"`                        // 备份 Cron 表达式
	BackupPath      string `json:"backup_path"`                        // 备份存储路径
	BackupRetention int    `json:"backup_retention" gorm:"default:7"`  // 备份保留天数
	BackupMaxCount  int    `json:"backup_max_count" gorm:"default:10"` // 最多保留的备份数量
	BackupCompress  int    `json:"backup_compress" gorm:"default:1"`   // 是否压缩备份，0 表示不压缩，1 表示压缩
}

func (*BackupConfig) TableName() string {
	return "backup_config"
}

// BackupRecord 备份记录（历史记录）
type BackupRecord struct {
	BaseModel
	TaskID           uint    `json:"task_id"`           // 关联的任务 ID
	Status           string  `json:"status"`            // 任务状态；unconfirmed 表示上一进程未记录结果
	FilePath         string  `json:"file_path"`         // 备份文件路径
	FileSize         int64   `json:"file_size"`         // 备份文件大小（字节）
	DatabaseSize     int64   `json:"database_size"`     // 数据库大小（字节）
	TableCount       int     `json:"table_count"`       // 表数量
	BackupDuration   int64   `json:"backup_duration"`   // 备份耗时（秒）
	BackupType       string  `json:"backup_type"`       // 备份类型：manual/auto
	CreatedReason    string  `json:"created_reason"`    // 创建原因
	FailureReason    string  `json:"failure_reason"`    // 失败原因
	CompressionRatio float64 `json:"compression_ratio"` // 压缩比例
	IsCompressed     int     `json:"is_compressed"`     // 是否已压缩，0 表示否，1 表示是
	CompletedAt      int64   `json:"completed_at"`      // 完成时间戳
}

func (*BackupRecord) TableName() string {
	return "backup_record"
}

// ReconcileInterruptedBackups 仅在启动后台任务前收敛上一进程遗留状态，不猜测完成时间或执行结果。
func ReconcileInterruptedBackups() error {
	return db.Db.Model(&BackupRecord{}).
		Where("status IN ?", []string{BackupStatusPending, BackupStatusRunning}).
		Updates(map[string]any{
			"status":         BackupStatusUnconfirmed,
			"failure_reason": "上一进程已结束，备份结果未记录，请检查备份文件和服务日志。",
		}).Error
}

func InitBackupService() *BackupService {
	if GlobalBackupService != nil {
		return GlobalBackupService
	}

	config := GetOrCreateBackupConfig()
	backupDir := filepath.Join(helpers.ConfigDir, "backups")
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups"); err != nil {
		helpers.AppLogger.Errorf("创建备份目录失败：%v", err)
	}

	GlobalBackupService = &BackupService{
		db:        db.Db,
		config:    config,
		backupDir: backupDir,
		timeout:   DefaultBackupTimeout,
	}

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
				BackupCompress:  1,
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
				BackupCompress:  1,
			}
		}
	}
	return &config
}

func (s *BackupService) CleanupOldBackups() {
	if err := s.cleanupOldBackups(false); err != nil {
		helpers.AppLogger.Warnf("清理旧备份失败：%v", err)
	}
}

// CleanupOldBackupsBeforeBackup 在导出前按保留策略清理，为即将生成的备份预留一个数量名额。
func (s *BackupService) CleanupOldBackupsBeforeBackup() error {
	return s.cleanupOldBackups(true)
}

func (s *BackupService) cleanupOldBackups(reserveNewBackup bool) error {
	config := s.config

	var records []BackupRecord
	if err := s.db.Where("status = ?", BackupStatusCompleted).
		Order("created_at DESC, id DESC").
		Find(&records).Error; err != nil {
		return fmt.Errorf("查询旧备份记录失败：%w", err)
	}

	now := time.Now().Unix()
	retentionSeconds := int64(config.BackupRetention) * 24 * 60 * 60
	maxCount := config.BackupMaxCount
	if reserveNewBackup && maxCount > 0 {
		maxCount--
	}

	for i, record := range records {
		shouldDelete := false
		reason := ""

		if config.BackupMaxCount > 0 && i >= maxCount {
			shouldDelete = true
			reason = "超过最大备份数量"
		}

		if config.BackupRetention > 0 && (now-record.CreatedAt) > retentionSeconds {
			shouldDelete = true
			reason = "超过保留天数"
		}

		if shouldDelete {
			if err := s.DeleteBackup(record.ID); err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return fmt.Errorf("清理旧备份 ID=%d 失败（%s）：%w", record.ID, reason, err)
			}
			helpers.AppLogger.Infof("已清理旧备份，ID=%d，原因：%s", record.ID, reason)
		}
	}
	return nil
}

func (s *BackupService) DeleteBackup(recordID uint) error {
	var record BackupRecord
	if err := s.db.First(&record, recordID).Error; err != nil {
		return fmt.Errorf("查询备份记录失败：%w", err)
	}

	recordOnly := false
	if record.FilePath != "" {
		if err := s.deleteBackupFile(record.FilePath); err != nil {
			if !errors.Is(err, errBackupFileNotManaged) {
				return err
			}
			recordOnly = true
		}
	}

	if err := s.db.Delete(&record).Error; err != nil {
		return fmt.Errorf("删除备份记录失败：%w", err)
	}

	if recordOnly {
		helpers.AppLogger.Infof("备份文件不属于当前备份目录，仅删除历史记录，ID=%d", record.ID)
	}

	return nil
}

func (s *BackupService) deleteBackupFile(filePath string) error {
	name, err := filepath.Rel(s.backupDir, filePath)
	if err != nil || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return errBackupFileNotManaged
	}
	if !filepath.IsLocal(name) || strings.ContainsAny(name, "/\\:\x00") ||
		!strings.EqualFold(filepath.Ext(name), ".zip") {
		return fmt.Errorf("备份文件不在当前备份目录内，拒绝删除")
	}
	config, err := os.OpenRoot(helpers.ConfigDir)
	if err != nil {
		return err
	}
	defer config.Close()
	info, err := config.Lstat("backups")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("备份目录不是普通目录，拒绝删除")
	}
	root, err := config.OpenRoot("backups")
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return fmt.Errorf("备份目录已改变，拒绝删除")
	}
	file, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !file.Mode().IsRegular() {
		return fmt.Errorf("备份文件不是普通文件，拒绝删除")
	}
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除备份文件失败：%w", err)
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

// func (s *BackupService) UploadAndRestore(ctx context.Context, fileData []byte, fileName string) error {
// 	tempPath := filepath.Join(s.backupDir, "upload_"+fileName)
// 	if err := os.WriteFile(tempPath, fileData, 0644); err != nil {
// 		return fmt.Errorf("保存上传文件失败：%v", err)
// 	}

// 	defer func() {
// 		os.Remove(tempPath)
// 	}()

// 	return s.RestoreBackup(ctx, 0, tempPath)
// }
