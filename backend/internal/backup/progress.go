package backup

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

var ErrTaskRunning = errors.New("备份或恢复任务正在运行")

type BackupOrRestoreResult struct {
	Type      string    `json:"type"`
	Status    string    `json:"status"` // idle/running/completed/failed；停止不等于成功。
	Desc      string    `json:"desc"`
	Total     int       `json:"total"`
	Count     int       `json:"count"`
	ErrorMsg  string    `json:"error_msg"` // 仅包含允许公开的失败说明。
	IsRunning bool      `json:"is_running"`
	StartTime time.Time `json:"start_time"`
	Elapsed   float64   `json:"elapsed"`
}

var progressMu sync.RWMutex
var runningResult = BackupOrRestoreResult{Status: "idle"}

// GetRunningResult 返回独立快照，HTTP 序列化不持有或修改任务共享状态。
func GetRunningResult() *BackupOrRestoreResult {
	progressMu.RLock()
	defer progressMu.RUnlock()
	result := runningResult
	if result.IsRunning {
		result.Elapsed = time.Since(result.StartTime).Seconds()
	}
	return &result
}

func IsRunning() bool {
	progressMu.RLock()
	defer progressMu.RUnlock()
	return runningResult.IsRunning
}

func beginTask(taskType string) error {
	progressMu.Lock()
	defer progressMu.Unlock()
	if runningResult.IsRunning {
		return ErrTaskRunning
	}
	runningResult = BackupOrRestoreResult{
		Type: taskType, Status: models.BackupStatusRunning,
		IsRunning: true, StartTime: time.Now(), Total: len(models.AllTables),
	}
	return nil
}

func SetRunningResult(taskType, desc string, total, count int, errorMsg string) {
	progressMu.Lock()
	defer progressMu.Unlock()
	runningResult.Type = taskType
	runningResult.Desc = desc
	runningResult.Total = total
	runningResult.Count = count
	runningResult.ErrorMsg = errorMsg
	runningResult.Elapsed = time.Since(runningResult.StartTime).Seconds()
}

func taskFailureMessage(taskType string) string {
	if taskType == "restore" {
		return "恢复任务失败，部分数据可能已恢复，请查看服务日志并核验数据。"
	}
	return "备份任务失败，请查看服务日志。"
}

func finishTask(err error) {
	progressMu.Lock()
	defer progressMu.Unlock()
	runningResult.IsRunning = false
	runningResult.Elapsed = time.Since(runningResult.StartTime).Seconds()
	runningResult.Status = models.BackupStatusCompleted
	runningResult.ErrorMsg = ""
	runningResult.Desc = "备份任务完成"
	if runningResult.Type == "restore" {
		runningResult.Desc = "恢复任务完成"
	}
	if err != nil {
		runningResult.Status = models.BackupStatusFailed
		runningResult.ErrorMsg = taskFailureMessage(runningResult.Type)
		runningResult.Desc = runningResult.ErrorMsg
	}
}

func runTask(operation func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("备份或恢复任务异常：%v", recovered)
		}
		if err != nil && helpers.AppLogger != nil {
			helpers.AppLogger.Errorf("备份或恢复任务失败：%v", err)
		}
		finishTask(err)
	}()
	return operation()
}

// StartBackup 在返回接受请求前占用运行状态，避免并发启动或读到上一轮终态。
func StartBackup(backupType, reason string) error {
	if err := beginTask("backup"); err != nil {
		return err
	}
	go func() { _ = runTask(func() error { return backup(backupType, reason) }) }()
	return nil
}

// StartRestore 启动恢复；上传的临时文件在恢复结束后清理。
func StartRestore(filePath string, removeAfterRestore bool) error {
	if err := beginTask("restore"); err != nil {
		return err
	}
	go func() {
		if removeAfterRestore {
			defer os.Remove(filePath)
		}
		_ = runTask(func() error { return restore(filePath) })
	}()
	return nil
}
