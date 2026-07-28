package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// ErrTasksRunning 表示仍有运行中的内部任务。
// 人工备份和恢复据此以 HTTP 409 返回任务摘要，既不进入维护也不等待。
var ErrTasksRunning = errors.New("有运行中的任务")

// ErrUnencryptedNotConfirmed 表示创建未加密工件缺少本次请求的显式确认。
var ErrUnencryptedNotConfirmed = errors.New("未确认创建未加密备份")

// ManualBackupRequest 是一次手动备份的受理参数。
// Password 只来自本次请求，绝不读取或复用 backup_config 中的定时备份密码。
type ManualBackupRequest struct {
	Reason             string
	Password           []byte
	ConfirmUnencrypted bool
}

// backupJob 是协调器接管后在后台执行的备份工作单元。
type backupJob struct {
	backupType string
	reason     string
	password   []byte
	// idleWait 是等待既有任务静止的上限；仅定时备份设置它。
	// 它不约束实际导出，导出没有总执行时限。
	idleWait time.Duration
}

// StartManualBackup 受理一次手动备份：先确认没有运行中的任务，再由协调器接管后台工作单元。
// 返回的 grant 携带只出现一次的明文令牌，调用方必须以 HTTP 202 表示已受理而非已完成。
func StartManualBackup(request ManualBackupRequest) (OperationGrant, error) {
	if len(request.Password) == 0 && !request.ConfirmUnencrypted {
		return OperationGrant{}, ErrUnencryptedNotConfirmed
	}
	coordinator, err := Coordinator()
	if err != nil {
		return OperationGrant{}, err
	}
	// 已有实际操作时先返回协调器冲突，避免为本次请求读取业务数据库。
	// 这也让维护前的 HTTP 409 快路径不依赖业务运行时已初始化。
	if coordinator.Active() != nil {
		return OperationGrant{}, ErrOperationInProgress
	}
	// 运行中的任务先于协调器判定，冲突时不会留下任何已受理的操作记录。
	if len(globalTaskBarrier.RunningTasks()) > 0 {
		return OperationGrant{}, ErrTasksRunning
	}

	grant, err := coordinator.Begin(OperationKindBackup, true)
	if err != nil {
		return OperationGrant{}, err
	}
	job := backupJob{
		backupType: models.BackupTypeManual,
		reason:     request.Reason,
		password:   request.Password,
	}
	go runBackupOperation(coordinator, grant.OperationID, job)
	return grant, nil
}

// RunScheduledBackup 执行 Cron 触发的定时备份。
// 协调器被占用时跳过本次任务且不清理上传暂存；取得执行权后才清空上传暂存并失效其预检 ID。
func RunScheduledBackup(reason string) {
	coordinator, err := Coordinator()
	if err != nil {
		return
	}
	grant, err := coordinator.Begin(OperationKindBackup, false)
	if err != nil {
		if errors.Is(err, ErrOperationInProgress) {
			helpers.AppLogger.Warnf("已有备份或恢复正在进行，跳过本次定时备份")
			return
		}
		helpers.AppLogger.Errorf("定时备份未能取得执行权：%v", err)
		return
	}

	ClearUploadStaging()
	password, err := scheduledBackupPassword()
	if err != nil {
		helpers.AppLogger.Errorf("读取定时备份密码失败，本次任务终止")
		finishOperation(coordinator, grant.OperationID, OperationStateFailed, OperationErrorCodeInternal)
		return
	}
	runBackupOperation(coordinator, grant.OperationID, backupJob{
		backupType: models.BackupTypeAuto,
		reason:     reason,
		password:   password,
		idleWait:   ScheduledBackupIdleWait,
	})
}

// scheduledBackupPassword 只为 Cron 触发的备份解密本机密文；手动备份绝不使用它。
func scheduledBackupPassword() ([]byte, error) {
	config := models.GetBackupService().GetBackupConfig()
	password, err := config.ScheduledBackupPassword()
	if err != nil {
		return nil, err
	}
	if password == "" {
		return nil, nil
	}
	return []byte(password), nil
}

// runBackupOperation 按 D3 的状态机执行一次实际备份。
// 顺序固定为：阻止新任务 → 等待实际静止 → 进入维护屏障 → 一致读视图导出 → 终态。
func runBackupOperation(coordinator *OperationCoordinator, operationID string, job backupJob) {
	startedAt := time.Now()
	defer clear(job.password)
	defer func() {
		if recovered := recover(); recovered != nil {
			helpers.AppLogger.Errorf("备份操作异常终止：%v", recovered)
			finishOperation(coordinator, operationID, OperationStateFailed, OperationErrorCodeInternal)
		}
	}()

	if err := coordinator.Transition(operationID, OperationTransition{State: OperationStateWaitingForTasks}); err != nil {
		helpers.AppLogger.Errorf("推进备份状态失败：%v", err)
		finishOperation(coordinator, operationID, OperationStateFailed, OperationErrorCodeInternal)
		return
	}
	globalTaskBarrier.Block()
	defer globalTaskBarrier.Resume()

	if !waitForIdleTasks(coordinator, operationID, job.idleWait) {
		return
	}
	if err := coordinator.Transition(operationID, OperationTransition{State: OperationStateValidating}); err != nil {
		helpers.AppLogger.Errorf("推进备份状态失败：%v", err)
		finishOperation(coordinator, operationID, OperationStateFailed, OperationErrorCodeInternal)
		return
	}
	if err := coordinator.SetMaintenance(operationID, true); err != nil {
		helpers.AppLogger.Errorf("启用维护屏障失败：%v", err)
		finishOperation(coordinator, operationID, OperationStateFailed, OperationErrorCodeInternal)
		return
	}
	if err := coordinator.Transition(operationID, OperationTransition{State: OperationStateRunning}); err != nil {
		helpers.AppLogger.Errorf("推进备份状态失败：%v", err)
		finishOperation(coordinator, operationID, OperationStateFailed, OperationErrorCodeInternal)
		return
	}

	exported, err := exportArtifact(job.backupType, job.password, func(completed int, total int, message string) {
		if progressErr := coordinator.UpdateProgress(operationID, OperationProgress{
			Message:   message,
			Completed: completed,
			Total:     total,
		}); progressErr != nil {
			helpers.AppLogger.Warnf("更新备份进度失败：%v", progressErr)
		}
	})
	if err != nil {
		code := operationErrorCodeFor(err, OperationErrorCodeBackupFailed)
		LogOperationDiagnostic(operationID, OperationPhaseValidated, code)
		helpers.AppLogger.Errorf("备份失败：%v", err)
		finishOperation(coordinator, operationID, OperationStateFailed, code)
		return
	}

	if err := recordExportedArtifact(exported, job, startedAt); err != nil {
		helpers.AppLogger.Errorf("写入备份记录失败：%v", err)
		// 未入索引的工件不能留在备份目录，否则会被目录清点当作可恢复候选。
		if removeErr := os.Remove(exported.Path); removeErr != nil {
			helpers.AppLogger.Warnf("清理未入索引的工件失败：%v", removeErr)
		}
		finishOperation(coordinator, operationID, OperationStateFailed, OperationErrorCodeBackupFailed)
		return
	}
	models.GetBackupService().CleanupOldBackups()

	if err := coordinator.RecordPhase(operationID, OperationPhaseTerminal); err != nil {
		helpers.AppLogger.Warnf("记录备份阶段日志失败：%v", err)
	}
	finishOperation(coordinator, operationID, OperationStateCompleted, "")
	helpers.AppLogger.Infof(
		"备份完成：共 %d 张表 %d 条记录，耗时 %.1f 秒，文件大小 %.2f MB",
		exported.TableCount,
		exported.RecordCount,
		time.Since(startedAt).Seconds(),
		float64(exported.Size)/1024/1024,
	)
}

// waitForIdleTasks 等待既有任务实际静止；定时备份超过等待上限时以 cancelled 结束。
// 已清理的上传暂存不会因为取消而恢复。
func waitForIdleTasks(coordinator *OperationCoordinator, operationID string, idleWait time.Duration) bool {
	ctx := context.Background()
	if idleWait > 0 {
		timedCtx, cancel := context.WithTimeout(ctx, idleWait)
		defer cancel()
		ctx = timedCtx
	}
	err := globalTaskBarrier.WaitIdle(ctx, func(running []RunningTask) {
		if progressErr := coordinator.UpdateProgress(operationID, OperationProgress{
			Message: fmt.Sprintf("等待 %d 个运行中的任务结束", len(running)),
		}); progressErr != nil {
			helpers.AppLogger.Warnf("更新等待进度失败：%v", progressErr)
		}
	})
	if err == nil {
		return true
	}
	helpers.AppLogger.Warnf("等待任务静止超时，本次备份已取消")
	finishOperation(coordinator, operationID, OperationStateCancelled, OperationErrorCodeTasksNotIdle)
	return false
}

// finishOperation 写入终态；终态同时解除维护屏障。
func finishOperation(
	coordinator *OperationCoordinator,
	operationID string,
	state OperationState,
	code OperationErrorCode,
) {
	if err := coordinator.Transition(operationID, OperationTransition{State: state, ErrorCode: code}); err != nil {
		helpers.AppLogger.Errorf("写入备份操作终态失败：%v", err)
	}
}

// recordExportedArtifact 把已发布的工件写入本机工件索引。
// 索引身份使用规范路径、字节大小和修改时间，供目录清点复用已完成的验证结果。
func recordExportedArtifact(exported ExportedArtifact, job backupJob, startedAt time.Time) error {
	info, err := os.Stat(exported.Path)
	if err != nil {
		return fmt.Errorf("读取工件状态: %w", err)
	}
	record := &models.BackupRecord{
		Status:              models.BackupStatusCompleted,
		FilePath:            exported.Path,
		FileSize:            exported.Size,
		TableCount:          exported.TableCount,
		BackupDuration:      int64(time.Since(startedAt).Seconds()),
		BackupType:          job.backupType,
		Format:              models.BackupFormatV1,
		VerificationState:   models.BackupVerificationVerified,
		InventoryPath:       exported.Path,
		InventoryFileSize:   info.Size(),
		InventoryModifiedAt: info.ModTime().Unix(),
		CreatedReason:       job.reason,
		IsCompressed:        1,
		CompletedAt:         time.Now().Unix(),
	}
	if err := db.Db.Create(record).Error; err != nil {
		return fmt.Errorf("创建备份记录: %w", err)
	}
	return nil
}
