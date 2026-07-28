package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

// withStubbedTaskBarrier 用可控的任务闸门替换全局实例，避免测试触碰真实子系统与业务数据库。
func withStubbedTaskBarrier(t *testing.T, inFlight int) {
	t.Helper()
	original := globalTaskBarrier
	globalTaskBarrier = &taskBarrier{subsystems: []taskSubsystem{{
		kind:     "download",
		name:     "下载队列",
		block:    func() {},
		resume:   func() {},
		inFlight: func() int { return inFlight },
	}}}
	t.Cleanup(func() { globalTaskBarrier = original })
}

// TestStartManualBackupRequiresUnencryptedConfirmation 覆盖 D4 的受理边界：
// 不带密码的备份必须由本次请求显式确认，确认不能来自配置或上一次请求。
func TestStartManualBackupRequiresUnencryptedConfirmation(t *testing.T) {
	withStubbedTaskBarrier(t, 0)

	_, err := StartManualBackup(ManualBackupRequest{Reason: "手动备份"})
	if !errors.Is(err, ErrUnencryptedNotConfirmed) {
		t.Fatalf("StartManualBackup() error = %v, want ErrUnencryptedNotConfirmed", err)
	}
}

// TestStartManualBackupRejectsWhileTasksRunning 覆盖任务冲突：
// 有运行中的任务时直接拒绝，既不等待也不进入维护，更不能留下已受理的操作记录。
func TestStartManualBackupRejectsWhileTasksRunning(t *testing.T) {
	withStubbedTaskBarrier(t, 1)
	helpers.ConfigDir = t.TempDir()

	_, err := StartManualBackup(ManualBackupRequest{Reason: "手动备份", ConfirmUnencrypted: true})
	if !errors.Is(err, ErrTasksRunning) {
		t.Fatalf("StartManualBackup() error = %v, want ErrTasksRunning", err)
	}
	if _, err := os.Stat(filepath.Join(StateDir(), operationStateFileName)); !os.IsNotExist(err) {
		t.Fatalf("任务冲突不应写入操作状态：%v", err)
	}
}

// TestWaitForIdleTasksCancelsWhenIdleWaitElapses 覆盖定时备份的等待上限：
// 超过上限以 cancelled 结束并给出 tasks_not_idle，而不是无限期占用协调器。
func TestWaitForIdleTasksCancelsWhenIdleWaitElapses(t *testing.T) {
	withStubbedTaskBarrier(t, 2)
	coordinator, err := NewOperationCoordinator(t.TempDir())
	if err != nil {
		t.Fatalf("NewOperationCoordinator() error = %v", err)
	}
	grant, err := coordinator.Begin(OperationKindBackup, false)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if err := coordinator.Transition(grant.OperationID, OperationTransition{State: OperationStateWaitingForTasks}); err != nil {
		t.Fatalf("Transition(waiting_for_tasks) error = %v", err)
	}

	if waitForIdleTasks(coordinator, grant.OperationID, time.Millisecond) {
		t.Fatal("waitForIdleTasks() = true, want false on timeout")
	}

	terminal := coordinator.LatestTerminal()
	if terminal == nil {
		t.Fatal("等待超时后必须写入终态")
	}
	if terminal.State != OperationStateCancelled {
		t.Fatalf("terminal.State = %q, want %q", terminal.State, OperationStateCancelled)
	}
	if terminal.ErrorCode != OperationErrorCodeTasksNotIdle {
		t.Fatalf("terminal.ErrorCode = %q, want %q", terminal.ErrorCode, OperationErrorCodeTasksNotIdle)
	}
	if coordinator.Active() != nil {
		t.Fatal("终态后不应仍有进行中的操作")
	}
	if coordinator.InMaintenance() {
		t.Fatal("等待阶段被取消不得留下维护屏障")
	}
}
