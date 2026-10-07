package backup

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

var (
	ErrTaskRunning        = errors.New("备份或恢复任务正在运行")
	ErrRestoreReceipt     = errors.New("恢复凭据无效")
	ErrRestartNotReady    = errors.New("当前恢复任务尚不能重启")
	ErrRestartUnsupported = errors.New("当前运行环境不支持自动重启")
)

type BackupOrRestoreResult struct {
	Type             string    `json:"type"`
	Status           string    `json:"status"` // idle/running/completed/failed；停止不等于成功。
	Desc             string    `json:"desc"`
	Total            int       `json:"total"`
	Count            int       `json:"count"`
	ErrorMsg         string    `json:"error_msg"` // 仅包含允许公开的失败说明。
	ErrorCode        string    `json:"error_code"`
	RestoreOutcome   string    `json:"restore_outcome"` // 仅终态恢复：not_started/rolled_back/committed/uncertain。
	RestartRequired  bool      `json:"restart_required"`
	RestartSupported bool      `json:"restart_supported"`
	RestartRequested bool      `json:"restart_requested"`
	IsRunning        bool      `json:"is_running"`
	StartTime        time.Time `json:"start_time"`
	Elapsed          float64   `json:"elapsed"`
}

var restoreReceipt string
var restoreOutcome string

var progressMu sync.RWMutex
var runningResult = BackupOrRestoreResult{Status: "idle"}

// GetRunningResult 返回独立快照，HTTP 序列化不持有或修改任务共享状态。
func GetRunningResult() *BackupOrRestoreResult {
	progressMu.RLock()
	defer progressMu.RUnlock()
	result := runningResult
	result.RestartSupported = helpers.SupportsAppRestart()
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
	if db.IsMaintenance(db.Db) {
		return db.ErrMaintenance
	}
	progressMu.Lock()
	defer progressMu.Unlock()
	if runningResult.RestartRequired {
		return db.ErrMaintenance
	}
	if runningResult.IsRunning {
		return ErrTaskRunning
	}
	restoreReceipt = ""
	restoreOutcome = ""
	total := len(models.AllTables)
	if taskType == "backup" {
		// 解析失败交由任务主体记录失败；初始进度不计入明确排除的表。
		total = 0
		if db.Db != nil {
			if tables, _, err := logicalTables(db.Db); err == nil {
				total = len(tables)
			}
		}
	}
	runningResult = BackupOrRestoreResult{
		Type: taskType, Status: models.BackupStatusRunning,
		IsRunning: true, StartTime: time.Now(), Total: total,
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
		return "恢复任务失败，请查看服务日志。"
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
	runningResult.ErrorCode = ""
	runningResult.Desc = "备份任务完成"
	if runningResult.Type == "restore" {
		runningResult.RestoreOutcome = restoreOutcome
		if runningResult.RestoreOutcome == "" {
			runningResult.RestoreOutcome = "not_started"
		}
		runningResult.Desc = "恢复任务完成"
		if runningResult.RestartRequired {
			runningResult.Desc = "恢复任务完成，请重启应用后重新登录"
		}
	}
	if err != nil {
		runningResult.Status = models.BackupStatusFailed
		runningResult.ErrorCode = FailureCode(runningResult.Type, err)
		runningResult.ErrorMsg = failureMessage(runningResult.Type, err)
		runningResult.Desc = runningResult.ErrorMsg
		if runningResult.RestartRequired {
			runningResult.Desc += "服务已暂停，请重启应用后继续。"
		}
	}
}

func runTask(operation func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errors.New("备份或恢复任务异常")
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
	_, err := StartRestoreWithReceipt(filePath, removeAfterRestore)
	return err
}

// StartRestoreWithReceipt 返回本轮进度和结束后重启的随机凭据，不恢复登录权限。
func StartRestoreWithReceipt(filePath string, removeAfterRestore bool) (string, error) {
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	receipt := base64.RawURLEncoding.EncodeToString(secret[:])
	if err := beginTask("restore"); err != nil {
		return "", err
	}
	progressMu.Lock()
	restoreReceipt = receipt
	progressMu.Unlock()
	go func() {
		if removeAfterRestore {
			defer os.Remove(filePath)
		}
		_ = runTask(func() error { return restore(filePath) })
	}()
	return receipt, nil
}

// RestoreResultWithReceipt 验证恢复凭据，只读取当前进程内存，不恢复任何登录权限。
func RestoreResultWithReceipt(receipt string) (*BackupOrRestoreResult, bool) {
	progressMu.RLock()
	defer progressMu.RUnlock()
	if !validRestoreReceipt(receipt) {
		return nil, false
	}
	result := runningResult
	result.RestartSupported = helpers.SupportsAppRestart()
	if result.IsRunning {
		result.Elapsed = time.Since(result.StartTime).Seconds()
	}
	return &result, true
}

// validRestoreReceipt 的调用方必须持有 progressMu。
func validRestoreReceipt(receipt string) bool {
	return receipt != "" && restoreReceipt != "" &&
		subtle.ConstantTimeCompare([]byte(receipt), []byte(restoreReceipt)) == 1
}

// RestartWithReceipt 仅允许本轮恢复进入维护并结束后请求一次重启。
func RestartWithReceipt(receipt string) error {
	return restartWithReceipt(receipt, helpers.SupportsAppRestart(), helpers.RestartApp)
}

func restartWithReceipt(receipt string, supported bool, restart func() error) error {
	progressMu.Lock()
	defer progressMu.Unlock()
	if !validRestoreReceipt(receipt) {
		return ErrRestoreReceipt
	}
	if runningResult.Type != "restore" || runningResult.IsRunning || !runningResult.RestartRequired ||
		(runningResult.Status != models.BackupStatusCompleted && runningResult.Status != models.BackupStatusFailed) {
		return ErrRestartNotReady
	}
	if runningResult.RestartRequested {
		return nil
	}
	if !supported {
		return ErrRestartUnsupported
	}
	// 准备失败不消费凭据；串行准备避免重复请求启动多个重启进程。
	if err := restart(); err != nil {
		return err
	}
	runningResult.RestartRequested = true
	return nil
}
