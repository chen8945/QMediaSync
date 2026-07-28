package backup

import (
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"

	"qmediasync/internal/helpers"
)

// diagnosticsOnly 表示自动回滚失败，本次进程只提供状态诊断能力。
// 它让维护屏障保持启用：所有业务 API 返回 HTTP 503，仅带 operation ID 与令牌的状态查询可用。
var diagnosticsOnly atomic.Bool

// StartupConvergence 是启动期状态收敛的结果。
type StartupConvergence struct {
	// DiagnosticsOnly 为真时不得启动正常业务运行时。
	DiagnosticsOnly bool
	// RolledBack 表示本次启动执行过自动回滚。
	RolledBack bool
	// ConfigRestored 表示回滚改写了白名单配置，调用方必须重新加载配置后再连接数据库。
	ConfigRestored bool
	ErrorCode      OperationErrorCode
}

// DiagnosticsOnly 供启动流程与中间件判断当前进程是否只允许状态诊断。
func DiagnosticsOnly() bool {
	return diagnosticsOnly.Load()
}

// ConvergeStartupState 在连接数据库之前收敛上一次进程留下的操作状态。
// 恢复中断在预恢复快照之后时，这里执行幂等自动回滚；回滚失败则只允许状态诊断。
// 它必须在数据库连接建立前调用：回滚会替换目标数据库文件与配置。
func ConvergeStartupState() StartupConvergence {
	coordinator, err := Coordinator()
	if err != nil {
		// 状态目录不可用时不阻断启动，也没有可信的中断记录可收敛。
		helpers.AppLogger.Errorf("读取备份操作状态失败，跳过启动期状态收敛")
		return StartupConvergence{}
	}

	convergence := StartupConvergence{}
	active := coordinator.Active()
	switch {
	case active == nil:
		// 终态已经可靠读出，上一次操作的预恢复快照可以删除。
		RemoveOtherSnapshots("")
	case active.Kind == OperationKindBackup:
		// 备份中断不改动业务数据，直接收敛为失败即可。
		helpers.AppLogger.Warnf("检测到上次备份未完成，已标记为失败")
		finishOperation(coordinator, active.OperationID, OperationStateFailed, OperationErrorCodeBackupFailed)
		RemoveOtherSnapshots("")
	default:
		convergence = convergeInterruptedRestore(coordinator, *active)
	}

	cleanStartupTemporaries()
	return convergence
}

// convergeInterruptedRestore 处理中断的恢复：按阶段日志判断是否需要回滚，并写入终态。
func convergeInterruptedRestore(coordinator *OperationCoordinator, active OperationView) StartupConvergence {
	phases, err := coordinator.Phases(active.OperationID)
	if err != nil {
		helpers.AppLogger.Errorf("读取恢复阶段日志失败：%v", err)
	}
	if !hasPhase(phases, OperationPhaseSnapshotReady) {
		// 快照尚未就绪，数据库与文件树都没有被改动。
		helpers.AppLogger.Warnf("检测到上次恢复在写入前中断，已标记为失败")
		finishRestoreOperation(coordinator, active, OperationErrorCodeRestoreFailed, RollbackStateNotStarted)
		RemoveOtherSnapshots("")
		return StartupConvergence{ErrorCode: OperationErrorCodeRestoreFailed}
	}

	helpers.AppLogger.RequiredWarnf("检测到上次恢复在写入过程中中断，开始自动回滚")
	snapshot, err := LoadRestoreSnapshot(active.OperationID)
	if err != nil || snapshot == nil {
		// 没有可用快照就无法保证数据安全，绝不能启动业务运行时。
		LogOperationDiagnostic(active.OperationID, OperationPhaseSnapshotReady, OperationErrorCodeSnapshotFailed)
		finishRestoreOperation(coordinator, active, OperationErrorCodeRestoreFailed, RollbackStateFailed)
		enterDiagnosticsOnly()
		return StartupConvergence{DiagnosticsOnly: true, ErrorCode: OperationErrorCodeRestoreFailed}
	}

	if err := snapshot.Rollback(); err != nil {
		LogOperationDiagnostic(active.OperationID, OperationPhaseSnapshotReady, OperationErrorCodeSnapshotFailed)
		helpers.AppLogger.Errorf("自动回滚失败：%v", err)
		finishRestoreOperation(coordinator, active, OperationErrorCodeRestoreFailed, RollbackStateFailed)
		enterDiagnosticsOnly()
		return StartupConvergence{DiagnosticsOnly: true, ErrorCode: OperationErrorCodeRestoreFailed}
	}

	helpers.AppLogger.RequiredWarnf("上次恢复失败，已自动回滚到恢复前状态")
	finishRestoreOperation(coordinator, active, OperationErrorCodeRestoreFailed, RollbackStateSucceeded)
	// 终态已经写入并可靠读出，快照可以删除。
	if err := snapshot.Remove(); err != nil {
		helpers.AppLogger.Warnf("删除预恢复快照失败：%v", err)
	}
	RemoveOtherSnapshots("")
	return StartupConvergence{
		RolledBack:     true,
		ConfigRestored: true,
		ErrorCode:      OperationErrorCodeRestoreFailed,
	}
}

// finishRestoreOperation 写入恢复的终态与回滚结果。
// 状态机要求先离开运行态，因此这里按需先推进到 rolling_back。
func finishRestoreOperation(
	coordinator *OperationCoordinator,
	active OperationView,
	code OperationErrorCode,
	rollbackState RollbackState,
) {
	if active.State == OperationStateRunning {
		if err := coordinator.Transition(active.OperationID, OperationTransition{State: OperationStateRollingBack}); err != nil {
			helpers.AppLogger.Errorf("推进回滚状态失败：%v", err)
		}
	}
	if err := coordinator.Transition(active.OperationID, OperationTransition{
		State:         OperationStateFailed,
		ErrorCode:     code,
		RollbackState: rollbackState,
	}); err != nil {
		helpers.AppLogger.Errorf("写入恢复终态失败：%v", err)
	}
	if err := coordinator.RecordPhase(active.OperationID, OperationPhaseTerminal); err != nil {
		helpers.AppLogger.Warnf("记录恢复终态阶段失败：%v", err)
	}
}

// enterDiagnosticsOnly 让本次进程只提供状态诊断：业务 API 统一维护屏障拒绝。
// 状态查询仍然要求 operation ID 与请求头令牌，不提供匿名诊断结果。
func enterDiagnosticsOnly() {
	diagnosticsOnly.Store(true)
	helpers.AppLogger.RequiredWarnf("自动回滚失败，本次启动只提供备份状态诊断，不会启动业务服务")
}

// cleanStartupTemporaries 清理上传暂存与恢复中间目录的遗留物。
// 只清理这些专用目录：config/tmp 的其余子目录属于刮削、更新等其他功能。
func cleanStartupTemporaries() {
	ClearUploadStaging()
	for _, directory := range []string{VerificationStagingDir(), RestoreWorkDir()} {
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
				helpers.AppLogger.Warnf("清理恢复中间文件失败：%v", err)
			}
		}
	}
}

func hasPhase(phases []OperationPhaseEntry, phase OperationPhase) bool {
	return slices.ContainsFunc(phases, func(entry OperationPhaseEntry) bool {
		return entry.Phase == phase
	})
}
