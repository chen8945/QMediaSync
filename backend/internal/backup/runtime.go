package backup

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"qmediasync/internal/helpers"
)

// ScheduledBackupIdleWait 是定时备份取得执行权后等待既有任务静止的上限。
// 它只约束等待，不是实际导出、预检、恢复提交或自动回滚的执行时限。
const ScheduledBackupIdleWait = 30 * time.Minute

var (
	coordinatorOnce   sync.Once
	globalCoordinator *OperationCoordinator
	coordinatorError  error

	globalTaskBarrier = newTaskBarrier()
)

// StateDir 返回备份运行状态目录。
// 它位于业务数据库和可恢复白名单之外，因此恢复覆盖配置与数据后仍能读出准确终态。
func StateDir() string {
	return filepath.Join(helpers.ConfigDir, "state", "backup")
}

// ArtifactDir 返回工件目录，是备份列表和目录清点的唯一扫描根。
func ArtifactDir() string {
	return filepath.Join(helpers.ConfigDir, "backups")
}

// UploadStagingDir 返回上传恢复专用暂存目录。
// 只有该目录会被启动清理和定时备份清空，config/tmp 的其余子目录属于其他功能。
func UploadStagingDir() string {
	return filepath.Join(helpers.ConfigDir, "tmp", "backup-restore")
}

// VerificationStagingDir 返回预检解密内层归档使用的暂存目录。
// 它与上传暂存分开：这里的中间文件不是恢复候选，不得进入恢复选择或定时清理的候选集合。
func VerificationStagingDir() string {
	return filepath.Join(helpers.ConfigDir, "tmp", "backup-verify")
}

// RollbackDir 返回预恢复快照根目录。
// 它位于状态目录内、白名单之外，因此恢复覆盖配置与数据后，新进程仍能读到快照并完成自动回滚。
func RollbackDir() string {
	return filepath.Join(StateDir(), "rollback")
}

// RestoreWorkDir 返回恢复提交期间的工作目录，并确保它存在。
// 日志与配置在这里预备和校验后才整体切换；它同样位于白名单之外，不会被恢复覆盖。
func RestoreWorkDir() string {
	directory := filepath.Join(StateDir(), "work")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		helpers.AppLogger.Warnf("创建恢复工作目录失败：%v", err)
	}
	return directory
}

// Coordinator 返回单实例操作协调器；状态目录不可用时返回错误而不是降级为无互斥运行。
func Coordinator() (*OperationCoordinator, error) {
	coordinatorOnce.Do(func() {
		globalCoordinator, coordinatorError = NewOperationCoordinator(StateDir())
		if coordinatorError != nil {
			helpers.AppLogger.Errorf("初始化备份操作协调器失败：%v", coordinatorError)
		}
	})
	if coordinatorError != nil {
		return nil, coordinatorError
	}
	return globalCoordinator, nil
}

// InMaintenance 供认证前的维护中间件判断是否应对业务 API 统一返回 HTTP 503。
// 协调器不可用时不进入维护，避免状态目录故障把整个服务锁死；
// 自动回滚失败的仅诊断模式则始终维持屏障，只保留状态查询。
func InMaintenance() bool {
	if DiagnosticsOnly() {
		return true
	}
	coordinator, err := Coordinator()
	if err != nil {
		return false
	}
	return coordinator.InMaintenance()
}

// ActiveOperation 返回仍未终态的操作；没有进行中的操作时返回 nil。
func ActiveOperation() *OperationView {
	coordinator, err := Coordinator()
	if err != nil {
		return nil
	}
	return coordinator.Active()
}

// AuthorizeOperation 以 operation ID 与一次性令牌读取最近一次操作状态。
// 即使自动回滚失败而进程只提供状态诊断，这里仍然要求两者同时有效。
func AuthorizeOperation(operationID string, token string) (OperationView, error) {
	coordinator, err := Coordinator()
	if err != nil {
		return OperationView{}, err
	}
	return coordinator.Authorize(operationID, token)
}

// LatestTerminalOperation 返回脱敏的最近一次终态，供备份列表在服务恢复后收敛展示。
func LatestTerminalOperation() *TerminalOperation {
	coordinator, err := Coordinator()
	if err != nil {
		return nil
	}
	return coordinator.LatestTerminal()
}

// RunningTasks 返回仍在执行的运行任务摘要，供人工请求冲突时返回 HTTP 409。
func RunningTasks() []RunningTask {
	return globalTaskBarrier.RunningTasks()
}

// LogOperationDiagnostic 输出脱敏诊断：只包含 operation ID、阶段和稳定错误码的预定义说明。
// 禁止拼接底层 error、密码、令牌、连接凭据、数据库标识或路径。
func LogOperationDiagnostic(operationID string, phase OperationPhase, code OperationErrorCode) {
	if helpers.AppLogger == nil {
		return
	}
	if !isOperationID(operationID) {
		operationID = "invalid"
	}
	if !isKnownOperationPhase(phase) {
		phase = "unknown"
	}
	code = normalizedOperationErrorCode(code)
	helpers.AppLogger.Errorf(
		"备份操作诊断：operation_id=%s phase=%s error_code=%s description=%s",
		operationID,
		phase,
		code,
		code.SafeDescription(),
	)
}

// operationErrorCodeFor 把内部错误归类为稳定错误码，避免底层错误文本进入状态或日志。
func operationErrorCodeFor(err error, fallback OperationErrorCode) OperationErrorCode {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrArtifactPasswordOrCorrupt):
		return OperationErrorCodePasswordOrCorrupt
	case errors.Is(err, ErrInvalidArtifact):
		return OperationErrorCodeArtifactInvalid
	case errors.Is(err, ErrRestoreTargetIncompatible):
		return OperationErrorCodeIncompatibleTarget
	case errors.Is(err, ErrRestoreTargetUnavailable):
		return OperationErrorCodeDatabaseUnavailable
	case errors.Is(err, ErrRestoreInsufficientSpace):
		return OperationErrorCodeInsufficientSpace
	case errors.Is(err, ErrRestoreSnapshotFailed):
		return OperationErrorCodeSnapshotFailed
	case errors.Is(err, os.ErrNotExist):
		return OperationErrorCodeArtifactInvalid
	case errors.Is(err, syscall.ENOSPC):
		return OperationErrorCodeInsufficientSpace
	default:
		return fallback
	}
}
