package models

import (
	"context"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/realtime"

	"golang.org/x/time/rate"
)

// DQ 保持稳定实例，配置只更新后续下载的并发与启动速率。
type DQ struct {
	*transferDispatch[*DbDownloadTask]
	limiter        *rate.Limiter
	retryTriggered bool
}

const DefaultQueueRetryMax = 1

// String 返回状态的字符串表示
func (s DownloadStatus) String() string {
	switch s {
	case DownloadStatusPending:
		return "待下载"
	case DownloadStatusDownloading:
		return "下载中"
	case DownloadStatusCompleted:
		return "已完成"
	case DownloadStatusFailed:
		return "失败"
	case DownloadStatusCancelled:
		return "已取消"
	default:
		return "未知"
	}
}

// 全局下载队列实例。
var GlobalDownloadQueue *DQ

// InitDQ 只创建队列；启动恢复完成后由应用开放派发。
func InitDQ() bool {
	if GlobalDownloadQueue != nil {
		return true
	}
	GlobalDownloadQueue = NewDq(GetThreadSettingsSnapshot().DownloadThreads)
	return true
}

func NewDq(maxConcurrency int) *DQ {
	if maxConcurrency <= 0 {
		maxConcurrency = 1
	}
	dq := &DQ{limiter: rate.NewLimiter(rate.Limit(maxConcurrency), maxConcurrency)}
	dq.transferDispatch = newTransferDispatch(maxConcurrency, 100*time.Millisecond,
		func(task *DbDownloadTask) uint { return task.ID }, dq.pendingTasks, dq.download,
		func(running bool) {
			realtime.BroadcastQueueStatusChanged(realtime.EventDownloadQueueStatusChanged, running)
		},
	)
	dq.prepare = dq.limiter.Wait
	return dq
}

func (dq *DQ) pendingTasks(ctx context.Context, limit int, excluded []uint) ([]*DbDownloadTask, error) {
	query := runnableDownloadTaskQuery(db.Db.WithContext(ctx).Model(&DbDownloadTask{})).Where("status = ?", DownloadStatusPending)
	var total int64
	if err := query.Count(&total).Error; err != nil {
		helpers.AppLogger.Errorf("查询待下载任务失败：%v", err)
		return nil, err
	}
	if total == 0 {
		var downloading int64
		if err := runnableDownloadTaskQuery(db.Db.WithContext(ctx).Model(&DbDownloadTask{})).Where("status = ?", DownloadStatusDownloading).Count(&downloading).Error; err != nil {
			return nil, err
		}
		dq.mutex.Lock()
		retry := dq.running && dq.generation == ctx && downloading == 0 && dq.active == 0 && !dq.retryTriggered
		if retry {
			dq.retryTriggered = true
		}
		dq.mutex.Unlock()
		if retry {
			if err := retryFailedDownloadTasks(ctx, DefaultQueueRetryMax); err != nil {
				dq.mutex.Lock()
				dq.retryTriggered = false
				dq.mutex.Unlock()
				helpers.AppLogger.Errorf("下载队列自动重试失败任务失败：%v", err)
				return nil, err
			}
			TriggerEmbyLibraryRefreshCheck()
		}
		return nil, nil
	}
	dq.mutex.Lock()
	dq.retryTriggered = false
	dq.mutex.Unlock()
	var tasks []*DbDownloadTask
	query = runnableDownloadTaskQuery(db.Db.WithContext(ctx).Model(&DbDownloadTask{})).Where("status = ?", DownloadStatusPending)
	if len(excluded) > 0 {
		query = query.Where("id NOT IN ?", excluded)
	}
	err := query.Order("id ASC").Limit(limit).Find(&tasks).Error
	if err != nil {
		helpers.AppLogger.Errorf("读取待下载任务失败：%v", err)
	}
	return tasks, err
}

func (dq *DQ) claim(ctx context.Context, task *DbDownloadTask) bool {
	startedAt := time.Now().Unix()
	result := db.Db.WithContext(ctx).Model(&DbDownloadTask{}).Where("id = ? AND status = ?", task.ID, DownloadStatusPending).
		Updates(map[string]any{"status": DownloadStatusDownloading, "start_time": startedAt})
	if result.Error != nil {
		helpers.AppLogger.Errorf("下载任务领取失败：%v", result.Error)
		return false
	}
	if result.RowsAffected == 0 {
		return false
	}
	task.Status, task.StartTime = DownloadStatusDownloading, startedAt
	publishDownloadQueueChanged(task, "status_changed")
	return true
}

func (dq *DQ) download(ctx context.Context, task *DbDownloadTask) {
	if dq.claim(ctx, task) {
		task.DownloadContext(ctx)
	}
}

// UpdateConcurrency 更新后续执行的名额和QPS，保持暂停与在途任务。
func (dq *DQ) UpdateConcurrency(limit int) {
	if limit <= 0 {
		return
	}
	dq.limiter.SetLimit(rate.Limit(limit))
	dq.limiter.SetBurst(limit)
	dq.transferDispatch.UpdateConcurrency(limit)
}

// Restart 恢复派发，不重建或复位记录。
func (dq *DQ) Restart() { dq.Start() }

// TriggerMediaLibraryDownloadCheck 重新检查启用端的失败提取任务。
func TriggerMediaLibraryDownloadCheck() {
	if GlobalDownloadQueue == nil {
		return
	}
	GlobalDownloadQueue.mutex.Lock()
	GlobalDownloadQueue.retryTriggered = false
	GlobalDownloadQueue.mutex.Unlock()
	GlobalDownloadQueue.signal()
}

// RestartGlobalDownloadQueue 恢复全局下载派发。
func RestartGlobalDownloadQueue() {
	if GlobalDownloadQueue != nil {
		GlobalDownloadQueue.Start()
	}
}

// UpdateGlobalDownloadQueueConcurrency 更新下载上限。
func UpdateGlobalDownloadQueueConcurrency(limit int) {
	if GlobalDownloadQueue != nil {
		GlobalDownloadQueue.UpdateConcurrency(limit)
	}
}

// GetGlobalDownloadQueueConcurrency 返回下载上限。
func GetGlobalDownloadQueueConcurrency() int {
	if GlobalDownloadQueue != nil {
		return GlobalDownloadQueue.GetConcurrency()
	}
	return 0
}
