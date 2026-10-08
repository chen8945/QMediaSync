package backup

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// Backup 使用一致性快照生成按持久化列编码的 JSON Lines 和版本清单。
func Backup(backupType string, reason string) error {
	if err := beginTask("backup"); err != nil {
		return err
	}
	return runTask(func() error { return backup(backupType, reason) })
}

func backup(backupType, reason string) (err error) {
	count := 0
	backupDir := filepath.Join(helpers.ConfigDir, "backups")
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups"); err != nil {
		return fmt.Errorf("创建备份目录失败：%w", err)
	}

	record := &models.BackupRecord{
		Status: models.BackupStatusRunning, BackupType: backupType, CreatedReason: reason,
	}
	if err := db.Db.Save(record).Error; err != nil {
		return fmt.Errorf("创建备份记录失败：%w", err)
	}
	startTime := time.Now()
	// 每个退出分支（包括 panic）均落下历史终态，完成记录写入失败也不能报告成功。
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errors.New("备份任务异常")
		}
		record.Status = models.BackupStatusCompleted
		record.CompletedAt = time.Now().Unix()
		record.BackupDuration = int64(time.Since(startTime).Seconds())
		if err != nil {
			record.Status = models.BackupStatusFailed
			record.FailureReason = taskFailureMessage("backup")
		}
		if saveErr := db.Db.Save(record).Error; saveErr != nil {
			err = errors.Join(err, fmt.Errorf("保存备份终态失败：%w", saveErr))
			// 尽可能将先前的 running 记录标为失败；数据库持续故障时仍由内存快照报告 failed。
			if updateErr := db.Db.Model(record).Updates(map[string]any{
				"status": models.BackupStatusFailed, "failure_reason": taskFailureMessage("backup"),
			}).Error; updateErr != nil {
				err = errors.Join(err, fmt.Errorf("保存备份失败状态失败：%w", updateErr))
			}
		}
	}()
	helpers.AppLogger.Infof("开始 %s 备份，备份记录 ID：%d", backupType, record.ID)
	SetRunningResult("backup", "正在清理超过保留策略的旧备份", 0, 0, "")
	if err := models.GetBackupService().CleanupOldBackupsBeforeBackup(); err != nil {
		return fmt.Errorf("清理旧备份失败，未开始导出：%w", err)
	}
	tables, _, err := logicalTables(db.Db)
	if err != nil {
		return err
	}
	totalTable := len(tables)
	SetRunningResult("backup", "正在读取数据库一致性快照", totalTable, count, "")

	backupRecordDir, err := os.MkdirTemp(backupDir, "backup-export-")
	if err != nil {
		return fmt.Errorf("创建备份目录失败：%w", err)
	}
	defer os.RemoveAll(backupRecordDir)
	if err := writeLogicalBackup(db.Db, backupRecordDir, func(table logicalTable) {
		count++
		SetRunningResult("backup", fmt.Sprintf("已备份 %s %d 条", table.ModelName, table.RowCount), totalTable, count, "")
		helpers.AppLogger.Infof("表 [%s] 备份完成，共 %d 条数据", table.Name, table.RowCount)
	}); err != nil {
		return err
	}

	fileName := fmt.Sprintf("backup_%s_%s.zip", backupType, time.Now().Format("20060102_150405"))
	filePath := filepath.Join(backupDir, fileName)
	tempPath := filepath.Join(backupDir, ".backup-publish-"+rand.Text()+".part")
	removeTemp := true
	defer func() {
		if removeTemp {
			os.Remove(tempPath)
		}
	}()
	if err := zipDir(backupRecordDir, tempPath); err != nil {
		// O_EXCL 失败时，不能删除其他写入方的文件。
		removeTemp = !errors.Is(err, os.ErrExist)
		return fmt.Errorf("打包备份目录失败：%w", err)
	}
	stat, err := os.Stat(tempPath)
	if err != nil {
		return fmt.Errorf("获取备份文件状态失败：%w", err)
	}
	if stat.Size() > MaxArchiveSize {
		return fmt.Errorf("备份压缩文件超过大小限制：%w", ErrArchiveLimit)
	}
	if err := helpers.SyncAndPublishFileNoReplace(tempPath, filePath); err != nil {
		return fmt.Errorf("发布备份文件失败：%w", err)
	}
	record.FilePath = filePath
	record.FileSize = stat.Size()
	record.TableCount = totalTable
	helpers.AppLogger.Infof("备份文件已生成：共 %d 张表，耗时 %.1f 秒，文件大小 %.2f MB", totalTable, time.Since(startTime).Seconds(), float64(stat.Size())/1024/1024)
	return nil
}
