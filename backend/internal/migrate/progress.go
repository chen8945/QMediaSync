package migrate

import (
	"sync"
	"time"
)

// Progress 是迁移服务自身的进度状态。
// 迁移与常规备份是两套独立协议，因此它不复用常规备份的操作协调器，
// 也不会把迁移进度写进常规备份的最近一次操作状态。
type Progress struct {
	Type      string    `json:"type"`
	Desc      string    `json:"desc"`
	Total     int       `json:"total"`
	Count     int       `json:"count"`
	ErrorMsg  string    `json:"error_msg"`
	IsRunning bool      `json:"is_running"`
	StartTime time.Time `json:"start_time"`
	Elapsed   float64   `json:"elapsed"`
}

var (
	progressMutex   sync.RWMutex
	currentProgress Progress
)

// startProgress 原子地开始一次迁移备份或导入，并重置计时。
// 已有迁移在运行时返回 false，避免并发操作覆盖同一个迁移包。
func startProgress(kind string, desc string, total int) bool {
	progressMutex.Lock()
	defer progressMutex.Unlock()
	if currentProgress.IsRunning {
		return false
	}
	currentProgress = Progress{
		Type:      kind,
		Desc:      desc,
		Total:     total,
		IsRunning: true,
		StartTime: time.Now(),
	}
	return true
}

// updateProgress 更新进度描述与已完成数量。
func updateProgress(desc string, count int, errorMsg string) {
	progressMutex.Lock()
	defer progressMutex.Unlock()
	currentProgress.Desc = desc
	currentProgress.Count = count
	currentProgress.ErrorMsg = errorMsg
	currentProgress.Elapsed = time.Since(currentProgress.StartTime).Seconds()
}

// finishProgress 结束当前迁移操作。
func finishProgress(desc string, errorMsg string) {
	progressMutex.Lock()
	defer progressMutex.Unlock()
	currentProgress.Desc = desc
	currentProgress.ErrorMsg = errorMsg
	currentProgress.IsRunning = false
	currentProgress.Elapsed = time.Since(currentProgress.StartTime).Seconds()
}

// CurrentProgress 返回迁移进度快照。
func CurrentProgress() Progress {
	progressMutex.RLock()
	defer progressMutex.RUnlock()
	snapshot := currentProgress
	if snapshot.IsRunning {
		snapshot.Elapsed = time.Since(snapshot.StartTime).Seconds()
	}
	return snapshot
}
