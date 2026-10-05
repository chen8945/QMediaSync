package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"
)

var pauseTasks = stopAllTasks
var resumeTasks = startAllTasks
var zipDir = helpers.ZipDir
var stopWebhookWorker = emby.StopWebhookWorker
var startWebhookWorker = emby.StartWebhookWorker

// 备份之前先停止所有同步任务、上传下载任务、定时任务
func stopAllTasks() error {
	// Webhook 收件和后台执行独立于同步 busy 标记；必须等待退出后才能导出或替换表。
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := stopWebhookWorker(ctx); err != nil {
		return fmt.Errorf("停止 Emby Webhook 后台处理失败：%w", err)
	}
	synccron.PauseAllNewSyncQueues()
	if synccron.SyncCron != nil {
		synccron.SyncCron.Stop()
	}
	if synccron.GlobalCron != nil {
		synccron.GlobalCron.Stop()
	}
	if synccron.ScrapeCron != nil {
		synccron.ScrapeCron.Stop()
	}
	if models.GlobalDownloadQueue != nil {
		models.GlobalDownloadQueue.Stop()
	}
	if models.GlobalUploadQueue != nil {
		models.GlobalUploadQueue.Stop()
	}
	emby.SetEmbySyncRunning(true)
	return nil
}

func startAllTasks() error {
	synccron.ResumeAllNewSyncQueues()
	synccron.InitCron()
	synccron.InitSyncCron()
	synccron.InitScrapeCron()
	if models.GlobalDownloadQueue != nil {
		models.GlobalDownloadQueue.Start()
	}
	if models.GlobalUploadQueue != nil {
		models.GlobalUploadQueue.Start()
	}
	emby.SetEmbySyncRunning(false)
	if err := startWebhookWorker(); err != nil {
		return fmt.Errorf("恢复 Emby Webhook 后台处理失败：%w", err)
	}
	return nil
}

// 每个表一个文件
// 每个文件的文件名格式为：模型名.json
// 文件中每一行都是一个 JSON 格式的字符串，代表一条数据
// 首先生成一个备份记录
// 然后将运行中状态设为 1

// 遍历每一个模型，生成 JSON 格式的备份文件
func Backup(backupType string, reason string) error {
	if err := beginTask("backup"); err != nil {
		return err
	}
	return runTask(func() error { return backup(backupType, reason) })
}

func backup(backupType, reason string) (err error) {
	totalTable := len(models.AllTables)
	count := 0
	backupDir := filepath.Join(helpers.ConfigDir, "backups")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return fmt.Errorf("创建备份目录失败：%w", err)
	}
	SetRunningResult("backup", fmt.Sprintf("开始 %s 备份", backupType), totalTable, count, "")
	models.GetBackupService().CleanupOldBackups()
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
			err = fmt.Errorf("备份任务异常：%v", recovered)
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
	if err := pauseTasks(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resumeTasks()) }()
	SetRunningResult("backup", "已停止所有同步任务、上传下载任务、定时任务", totalTable, count, "")

	backupRecordDir := filepath.Join(backupDir, fmt.Sprintf("%d", record.ID))
	if err := os.MkdirAll(backupRecordDir, 0755); err != nil {
		return fmt.Errorf("创建备份目录失败：%w", err)
	}
	defer os.RemoveAll(backupRecordDir)
	for _, table := range models.AllTables {
		if err := backupToJsonFile(backupRecordDir, helpers.GetStructName(table), totalTable, &count, table); err != nil {
			return err
		}
	}

	fileName := fmt.Sprintf("backup_%s_%s.zip", backupType, time.Now().Format("20060102_150405"))
	filePath := filepath.Join(backupDir, fileName)
	if err := zipDir(backupRecordDir, filePath); err != nil {
		// 残缺归档尚未写入记录路径，历史清理和删除都找不到它，只能在此删除。
		os.Remove(filePath)
		return fmt.Errorf("打包备份目录失败：%w", err)
	}
	stat, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("获取备份文件状态失败：%w", err)
	}
	record.FilePath = filePath
	record.FileSize = stat.Size()
	record.TableCount = totalTable
	helpers.AppLogger.Infof("备份文件已生成：共 %d 张表，耗时 %.1f 秒，文件大小 %.2f MB", totalTable, time.Since(startTime).Seconds(), float64(stat.Size())/1024/1024)
	return nil
}

// 备份账号信息
func backupToJsonFile(backupDir string, modelName string, totalTable int, count *int, model any) (err error) {
	// 打开一个文件用来写入
	backupFilePath := filepath.Join(backupDir, modelName+".json")
	backupFile, err := os.OpenFile(backupFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		helpers.AppLogger.Errorf("创建 %s 备份文件失败：%v", modelName, err)
		return err
	}
	defer func() { err = errors.Join(err, backupFile.Close()) }()
	encoder := json.NewEncoder(backupFile)
	// 从数据库中分页查询所有数据，每页 100 条
	pageSize := 100
	page := 0
	totalCount := 0
	typ := reflect.TypeOf(model)
	sliceType := reflect.SliceOf(typ)
	for {
		records := reflect.New(sliceType).Interface()
		if err := db.Db.Model(model).Offset(page * pageSize).Limit(pageSize).Order("id").Find(records).Error; err != nil {
			helpers.AppLogger.Errorf("查询 %s 失败：%v", modelName, err)
			return err
		}
		recordsValue := reflect.ValueOf(records).Elem()
		if recordsValue.Len() == 0 {
			break
		}

		for i := 0; i < recordsValue.Len(); i++ {
			record := recordsValue.Index(i).Interface()
			if task, ok := record.(models.DbDownloadTask); ok {
				record = downloadBackup(task)
			}
			if err := encoder.Encode(record); err != nil {
				return fmt.Errorf("写入 %s 备份文件失败：%w", modelName, err)
			}
			totalCount++
			if totalCount%10 == 0 {
				SetRunningResult("backup", fmt.Sprintf("已备份 %s %d 条", modelName, totalCount), totalTable, *count, "")
			}
		}
		page++
	}
	*count++
	SetRunningResult("backup", fmt.Sprintf("已备份 %s %d 条", modelName, totalCount), totalTable, *count, "")
	helpers.AppLogger.Infof("表 [%s] 备份完成，共 %d 条数据", modelName, totalCount)
	return nil
}
