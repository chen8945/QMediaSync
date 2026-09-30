package v115open

import (
	"context"
	"sync"
	"time"

	"qmediasync/internal/helpers"
)

// ThrottleManager 全局限流管理器，用于管理 API 访问频率限制
type ThrottleManager struct {
	sync.RWMutex
	// 是否处于限流状态
	isThrottled bool
	// 限流开始时间
	throttleStartTime time.Time
	// 限流通知通道
	throttleNotify chan struct{}
	// 限流暂停时长（硬编码 1 分钟）
	throttleDuration time.Duration
}

// NewThrottleManager 创建新的限流管理器
func NewThrottleManager() *ThrottleManager {
	return &ThrottleManager{
		isThrottled:      false,
		throttleNotify:   make(chan struct{}),
		throttleDuration: 1 * time.Minute,
	}
}

// IsThrottled 检查是否处于限流状态
func (tm *ThrottleManager) IsThrottled() bool {
	tm.RLock()
	defer tm.RUnlock()

	if !tm.isThrottled {
		return false
	}

	// 检查是否已经恢复
	if time.Since(tm.throttleStartTime) >= tm.throttleDuration {
		// 时间已过，应该恢复
		return false
	}

	return true
}

// MarkThrottled 标记为限流状态，并启动恢复计时器
func (tm *ThrottleManager) MarkThrottled(stats *RequestStats) {
	tm.Lock()
	defer tm.Unlock()

	if tm.isThrottled {
		// 已经在限流状态，不需要重复标记
		return
	}

	tm.isThrottled = true
	tm.throttleStartTime = time.Now()

	helpers.V115Log.Warnf("检测到限流，将在 %.0f 秒后恢复", tm.throttleDuration.Seconds())

	// 记录限流事件
	if stats != nil {
		stats.RecordThrottle(tm.throttleStartTime, tm.throttleDuration)
	}

	// 启动恢复计时器
	go tm.startRecoveryTimer()
}

// startRecoveryTimer 启动恢复计时器
func (tm *ThrottleManager) startRecoveryTimer() {
	time.Sleep(tm.throttleDuration)

	tm.Lock()
	defer tm.Unlock()

	tm.isThrottled = false
	helpers.V115Log.Infof("限流已恢复，继续处理请求")

	// 发送恢复通知
	select {
	case tm.throttleNotify <- struct{}{}:
	default:
		// 通道已满，不需要发送
	}
}

// WaitThrottleRecovery 等待限流恢复，如果当前不在限流状态则立即返回
func (tm *ThrottleManager) WaitThrottleRecovery(ctx context.Context) {
	for {
		if !tm.IsThrottled() {
			return
		}

		// 创建一个定时器来定期检查
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 继续检查
			if !tm.IsThrottled() {
				return
			}
		}
	}
}

// GetThrottleStatus 获取限流状态详情
func (tm *ThrottleManager) GetThrottleStatus() ThrottleStatus {
	tm.RLock()
	defer tm.RUnlock()

	status := ThrottleStatus{
		IsThrottled: tm.isThrottled,
	}

	if tm.isThrottled {
		elapsed := time.Since(tm.throttleStartTime)
		status.WaitTime = tm.throttleDuration
		status.ElapsedTime = elapsed
		status.RemainingTime = max(tm.throttleDuration-elapsed, 0)
	}

	return status
}

// ClearThrottled 清除限流状态（仅用于测试）
func (tm *ThrottleManager) ClearThrottled() {
	tm.Lock()
	defer tm.Unlock()
	tm.isThrottled = false
}

// ThrottleStatus 限流状态详情
type ThrottleStatus struct {
	IsThrottled   bool          // 是否处于限流状态
	WaitTime      time.Duration // 本次限流总等待时长
	ElapsedTime   time.Duration // 已经限流的时长
	RemainingTime time.Duration // 剩余限流时长
}
