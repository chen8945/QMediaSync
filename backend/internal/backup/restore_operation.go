package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// ErrRestoreOverwriteNotConfirmed 表示确认阶段缺少「配置和全部数据都会被覆盖」的显式确认。
var ErrRestoreOverwriteNotConfirmed = errors.New("未确认完整覆盖恢复")

// RestoreConfirmRequest 是恢复第二阶段的受理参数。
// Password 由恢复页面在确认时重新提交，服务端不得持久化明文、派生密钥或预检 ID 明文。
type RestoreConfirmRequest struct {
	Kind             PreflightSourceKind
	RecordID         uint
	ArtifactPath     string
	PreflightID      string
	Password         []byte
	ConfirmOverwrite bool
}

// restoreJob 是协调器接管后、不再依赖 HTTP 请求上下文的恢复工作单元。
// 移交成功后，浏览器关闭、断网或反向代理超时都不会取消它。
type restoreJob struct {
	kind             PreflightSourceKind
	artifactPath     string
	target           RestoreTarget
	targetConfigYAML []byte
	verified         VerifiedArtifact
}

var (
	restoreExitMutex sync.Mutex
	restoreExitHook  func()
)

// SetOrderlyExitHook 注入进程的有序退出实现。
// 应用不写重启标记，也不请求或配置 Docker、systemd、飞牛等平台重启；
// 是否以及何时拉起新进程完全由部署平台或操作者决定。
func SetOrderlyExitHook(hook func()) {
	restoreExitMutex.Lock()
	defer restoreExitMutex.Unlock()
	restoreExitHook = hook
}

// ConfirmRestore 受理恢复的第二阶段。
// 它重新验证预检 ID、来源身份、工件散列、密码和全部预检条件，
// 全部一致才消耗该 ID、原子创建 operation，并把后台工作单元移交给协调器。
func ConfirmRestore(request RestoreConfirmRequest) (OperationGrant, error) {
	defer clear(request.Password)
	if !request.ConfirmOverwrite {
		return OperationGrant{}, ErrRestoreOverwriteNotConfirmed
	}
	if len(globalTaskBarrier.RunningTasks()) > 0 {
		return OperationGrant{}, ErrTasksRunning
	}
	coordinator, err := Coordinator()
	if err != nil {
		return OperationGrant{}, err
	}
	if coordinator.Active() != nil {
		return OperationGrant{}, ErrOperationInProgress
	}

	// 确认阶段重新执行完整验证：密码、清单、散列、白名单与三方密钥指纹都必须再次通过。
	verified, artifactSHA256, err := verifyRestoreArtifact(request.ArtifactPath, request.Password)
	if err != nil {
		return OperationGrant{}, err
	}
	released := false
	defer func() {
		if !released {
			if cleanupErr := verified.Cleanup(); cleanupErr != nil {
				helpers.AppLogger.Warnf("清理恢复暂存文件失败：%v", cleanupErr)
			}
		}
	}()

	target, targetConfigYAML, err := resolveConfirmedRestoreTarget(verified)
	if err != nil {
		return OperationGrant{}, err
	}
	if err := checkRestoreTargetReady(target, verified.Header.Encryption.PlaintextSize); err != nil {
		return OperationGrant{}, err
	}

	source := PreflightSource{
		Kind:           request.Kind,
		RecordID:       request.RecordID,
		ArtifactPath:   request.ArtifactPath,
		ArtifactSHA256: artifactSHA256,
	}
	if _, err := ConsumePreflight(request.PreflightID, source); err != nil {
		return OperationGrant{}, err
	}

	grant, err := coordinator.Begin(OperationKindRestore, true)
	if err != nil {
		return OperationGrant{}, err
	}
	released = true
	go runRestoreOperation(coordinator, grant.OperationID, restoreJob{
		kind:             request.Kind,
		artifactPath:     request.ArtifactPath,
		target:           target,
		targetConfigYAML: targetConfigYAML,
		verified:         verified,
	})
	return grant, nil
}

// resolveConfirmedRestoreTarget 返回恢复目标及其配置副本。
// 配置副本随预恢复快照保存，使新进程在配置被覆盖后仍能解析出同一个目标并完成自动回滚。
func resolveConfirmedRestoreTarget(verified VerifiedArtifact) (RestoreTarget, []byte, error) {
	reader, err := openInnerArchive(verified.InnerArchivePath, verified.Manifest)
	if err != nil {
		return RestoreTarget{}, nil, err
	}
	defer reader.Close()

	configYAML, err := reader.artifactConfigYAML()
	if err != nil {
		return RestoreTarget{}, nil, err
	}
	target, err := resolveRestoreTarget(configYAML)
	if err != nil {
		return RestoreTarget{}, nil, err
	}
	if err := verifyRestoreCompatibility(verified.Header, target, models.SchemaVersion); err != nil {
		return RestoreTarget{}, nil, err
	}
	return target, configYAML, nil
}

// runRestoreOperation 执行一次实际恢复。
// 快照就绪前的失败保持当前进程运行；快照就绪后的任何失败都自动回滚，
// 并在终态与阶段日志持久化后有序退出。
func runRestoreOperation(coordinator *OperationCoordinator, operationID string, job restoreJob) {
	var snapshot *RestoreSnapshot
	defer func() {
		if cleanupErr := job.verified.Cleanup(); cleanupErr != nil {
			helpers.AppLogger.Warnf("清理恢复暂存文件失败：%v", cleanupErr)
		}
	}()
	defer func() {
		if recovered := recover(); recovered != nil {
			recoverRestoreOperationPanic(coordinator, operationID, snapshot)
		}
	}()

	var err error
	snapshot, err = prepareRestore(coordinator, operationID, job)
	if err != nil {
		// 快照尚未就绪：目标数据与文件树都没有被改动，解除维护后继续提供服务。
		code := operationErrorCodeFor(err, OperationErrorCodeRestoreFailed)
		LogOperationDiagnostic(operationID, OperationPhaseValidated, code)
		helpers.AppLogger.Errorf("恢复预备失败：%v", err)
		globalTaskBarrier.Resume()
		finishOperation(coordinator, operationID, OperationStateFailed, code)
		return
	}

	applyErr := applyRestoreArtifact(coordinator, operationID, job.target, job.verified, func(completed int, total int, message string) {
		if progressErr := coordinator.UpdateProgress(operationID, OperationProgress{
			Message:   message,
			Completed: completed,
			Total:     total,
		}); progressErr != nil {
			helpers.AppLogger.Warnf("更新恢复进度失败：%v", progressErr)
		}
	})
	if applyErr != nil {
		rollbackAfterFailedRestore(coordinator, operationID, snapshot, applyErr)
		return
	}

	if job.kind == PreflightSourceUpload {
		// 成功恢复后立即删除上传源文件，它不再是可选恢复候选。
		if removeErr := os.Remove(job.artifactPath); removeErr != nil && !os.IsNotExist(removeErr) {
			helpers.AppLogger.Warnf("删除已恢复的上传工件失败：%v", removeErr)
		}
	}
	finishOperation(coordinator, operationID, OperationStateCompleted, "")
	if err := coordinator.RecordPhase(operationID, OperationPhaseTerminal); err != nil {
		helpers.AppLogger.Errorf("记录恢复终态阶段失败：%v", err)
	}
	helpers.AppLogger.Infof("恢复完成，进程即将有序退出，请由部署平台或操作者重新启动")
	requestOrderlyExit()
}

// recoverRestoreOperationPanic 保证快照就绪后的异常与普通提交失败遵循同一回滚路径。
// 仅记录稳定错误码，避免 panic 值把底层连接信息或路径写入日志。
func recoverRestoreOperationPanic(coordinator *OperationCoordinator, operationID string, snapshot *RestoreSnapshot) {
	helpers.AppLogger.Errorf("恢复操作异常终止")
	if snapshot == nil {
		globalTaskBarrier.Resume()
		finishOperation(coordinator, operationID, OperationStateFailed, OperationErrorCodeInternal)
		return
	}
	rollbackAfterFailedRestore(coordinator, operationID, snapshot, errors.New("恢复操作异常终止"))
}

// prepareRestore 完成任务静止、维护屏障与预恢复快照，返回可回滚的快照。
func prepareRestore(coordinator *OperationCoordinator, operationID string, job restoreJob) (*RestoreSnapshot, error) {
	if err := coordinator.Transition(operationID, OperationTransition{State: OperationStateWaitingForTasks}); err != nil {
		return nil, err
	}
	globalTaskBarrier.Block()
	// 人工恢复在受理时已经以 HTTP 409 拒绝了运行中的任务，这里只等待受理与阻断之间新进入的任务；
	// 它没有总执行时限，绝不能在安全切换中被强制中断。
	if err := globalTaskBarrier.WaitIdle(context.Background(), func(running []RunningTask) {
		if progressErr := coordinator.UpdateProgress(operationID, OperationProgress{
			Message: fmt.Sprintf("等待 %d 个运行中的任务结束", len(running)),
		}); progressErr != nil {
			helpers.AppLogger.Warnf("更新等待进度失败：%v", progressErr)
		}
	}); err != nil {
		return nil, err
	}

	if err := coordinator.Transition(operationID, OperationTransition{State: OperationStateValidating}); err != nil {
		return nil, err
	}
	if err := coordinator.SetMaintenance(operationID, true); err != nil {
		return nil, err
	}
	if err := coordinator.Transition(operationID, OperationTransition{State: OperationStateRunning}); err != nil {
		return nil, err
	}
	if err := coordinator.RecordPhase(operationID, OperationPhaseValidated); err != nil {
		return nil, err
	}

	snapshot, err := CreateRestoreSnapshot(operationID, job.target, job.targetConfigYAML)
	if err != nil {
		return nil, err
	}
	if err := coordinator.RecordPhase(operationID, OperationPhaseSnapshotReady); err != nil {
		return nil, err
	}
	// 快照就绪后才清理历史快照，保证任何时刻磁盘上都存在一份可回滚副本。
	RemoveOtherSnapshots(operationID)
	return snapshot, nil
}

// rollbackAfterFailedRestore 在快照就绪后的失败上执行自动回滚，并按结果写入终态。
// 恢复失败与回滚失败必须分别呈现，不能被统一降级为普通失败。
func rollbackAfterFailedRestore(
	coordinator *OperationCoordinator,
	operationID string,
	snapshot *RestoreSnapshot,
	cause error,
) {
	code := operationErrorCodeFor(cause, OperationErrorCodeRestoreFailed)
	LogOperationDiagnostic(operationID, OperationPhaseSnapshotReady, code)
	helpers.AppLogger.Errorf("恢复失败，开始自动回滚：%v", cause)
	if err := coordinator.Transition(operationID, OperationTransition{State: OperationStateRollingBack}); err != nil {
		helpers.AppLogger.Errorf("推进回滚状态失败：%v", err)
	}

	rollbackState := RollbackStateSucceeded
	if err := snapshot.Rollback(); err != nil {
		rollbackState = RollbackStateFailed
		LogOperationDiagnostic(operationID, OperationPhaseSnapshotReady, OperationErrorCodeSnapshotFailed)
		helpers.AppLogger.Errorf("自动回滚失败：%v", err)
	} else {
		helpers.AppLogger.Warnf("恢复失败，已自动回滚到恢复前状态")
	}

	if err := coordinator.Transition(operationID, OperationTransition{
		State:         OperationStateFailed,
		ErrorCode:     code,
		RollbackState: rollbackState,
	}); err != nil {
		helpers.AppLogger.Errorf("写入恢复终态失败：%v", err)
	}
	if err := coordinator.RecordPhase(operationID, OperationPhaseTerminal); err != nil {
		helpers.AppLogger.Errorf("记录恢复终态阶段失败：%v", err)
	}
	requestOrderlyExit()
}

// requestOrderlyExit 在终态与阶段日志持久化后关闭运行时并退出当前进程。
func requestOrderlyExit() {
	restoreExitMutex.Lock()
	hook := restoreExitHook
	restoreExitMutex.Unlock()
	if hook != nil {
		hook()
		return
	}
	// 没有注入退出实现时也必须退出：恢复后的进程持有已被替换的数据库和配置，不能继续提供服务。
	helpers.CloseLogger()
	os.Exit(0)
}
