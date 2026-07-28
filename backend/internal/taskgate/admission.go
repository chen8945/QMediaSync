// Package taskgate 维护会影响业务任务执行的进程内准入屏障。
//
// 它与备份协调器的维护状态不同：在备份或恢复等待既有任务静止时，
// 维护 HTTP 中间件尚未开启，但任何新的任务入口都必须立即被拒绝。
// 各任务子系统在启动 worker、重启队列或接收新工作前检查此包，避免
// 由某个具体 API 或队列实现单独承担这条跨模块约束。
package taskgate

import (
	"errors"
	"sync"
	"sync/atomic"
)

// ErrTaskAdmissionBlocked 表示备份或恢复正在等待既有任务静止，当前不能接受新任务。
var ErrTaskAdmissionBlocked = errors.New("备份或恢复正在等待任务静止，暂不接受新任务")

var (
	taskAdmissionBlocked atomic.Bool
	taskAdmissionMu      sync.RWMutex
)

// BlockNewTasks 关闭任务准入，并等待已经通过 Admit 的任务完成运行状态登记。
// 调用方随后停止子系统并等待既有工作结束。
func BlockNewTasks() {
	taskAdmissionBlocked.Store(true)
	taskAdmissionMu.Lock()
	taskAdmissionMu.Unlock()
}

// AllowNewTasks 重新开放任务准入。只能在备份或恢复已经到达终态后调用。
func AllowNewTasks() {
	taskAdmissionMu.Lock()
	taskAdmissionBlocked.Store(false)
	taskAdmissionMu.Unlock()
}

// IsBlocked 返回当前是否拒绝启动或入队新的业务任务。
func IsBlocked() bool {
	return taskAdmissionBlocked.Load()
}

// Admit 原子地确认任务可以开始。调用方必须在任务的运行状态对等待方可见后调用返回的 release。
// release 可重复调用，方便在错误路径中通过 defer 释放。
func Admit() (release func(), err error) {
	if taskAdmissionBlocked.Load() {
		return nil, ErrTaskAdmissionBlocked
	}
	taskAdmissionMu.RLock()
	if taskAdmissionBlocked.Load() {
		taskAdmissionMu.RUnlock()
		return nil, ErrTaskAdmissionBlocked
	}

	var once sync.Once
	return func() {
		once.Do(taskAdmissionMu.RUnlock)
	}, nil
}
