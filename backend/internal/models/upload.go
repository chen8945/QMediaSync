package models

import (
	"context"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/realtime"
)

// UQ 的数据库状态机与上传业务独立于共用内存派发。
type UQ struct {
	*transferDispatch[*DbUploadTask]
	retryTriggered bool
}

var GlobalUploadQueue *UQ

// InitUQ 只创建稳定队列，恢复完成后由应用开放派发。
func InitUQ() bool {
	if GlobalUploadQueue != nil {
		return true
	}
	GlobalUploadQueue = NewUq(GetThreadSettingsSnapshot().UploadThreads)
	return true
}

func NewUq(limit int) *UQ {
	if limit < DefaultUploadThreads || limit > MaxUploadThreads {
		limit = DefaultUploadThreads
	}
	uq := &UQ{}
	uq.transferDispatch = newTransferDispatch(limit, time.Second,
		func(task *DbUploadTask) uint { return task.ID }, uq.pendingTasks,
		func(ctx context.Context, task *DbUploadTask) { task.UploadContext(ctx) },
		func(running bool) {
			realtime.BroadcastQueueStatusChanged(realtime.EventUploadQueueStatusChanged, running)
		},
	)
	return uq
}

func (uq *UQ) pendingTasks(ctx context.Context, limit int, excluded []uint) ([]*DbUploadTask, error) {
	var total int64
	statuses := []UploadStatus{UploadStatusPending, UploadStatusRemoteCompletedPendingFinalize}
	if err := db.Db.WithContext(ctx).Model(&DbUploadTask{}).Where("status IN ?", statuses).Count(&total).Error; err != nil {
		helpers.AppLogger.Errorf("查询待上传任务失败：%v", err)
		return nil, err
	}
	if total == 0 {
		var uploading int64
		if err := db.Db.WithContext(ctx).Model(&DbUploadTask{}).Where("status IN ?", []UploadStatus{UploadStatusUploading, UploadStatusRemoteCompletedFinalizing}).Count(&uploading).Error; err != nil {
			return nil, err
		}
		uq.mutex.Lock()
		retry := uq.running && uq.generation == ctx && uploading == 0 && uq.active == 0 && !uq.retryTriggered
		if retry {
			uq.retryTriggered = true
		}
		uq.mutex.Unlock()
		if retry {
			if err := retryFailedUploadTasks(ctx, DefaultQueueRetryMax); err != nil {
				uq.mutex.Lock()
				uq.retryTriggered = false
				uq.mutex.Unlock()
				helpers.AppLogger.Errorf("上传队列自动重试失败任务失败：%v", err)
				return nil, err
			}
		}
		return nil, nil
	}
	uq.mutex.Lock()
	uq.retryTriggered = false
	uq.mutex.Unlock()
	var tasks []*DbUploadTask
	query := db.Db.WithContext(ctx).Model(&DbUploadTask{}).Where("status IN ?", statuses)
	if len(excluded) > 0 {
		query = query.Where("id NOT IN ?", excluded)
	}
	if err := query.Order("id ASC").Limit(limit).Find(&tasks).Error; err != nil {
		helpers.AppLogger.Errorf("读取待上传任务失败：%v", err)
		return nil, err
	}
	hydrateUploadTaskQueueFields(tasks)
	return tasks, nil
}

// UpdateConcurrency 更新后续名额，不改变用户暂停。
func (uq *UQ) UpdateConcurrency(limit int) {
	if limit < DefaultUploadThreads || limit > MaxUploadThreads {
		return
	}
	uq.transferDispatch.UpdateConcurrency(limit)
}

// Restart 恢复派发，保留在途预留。
func (uq *UQ) Restart() { uq.Start() }
