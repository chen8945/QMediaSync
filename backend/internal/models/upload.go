package models

import (
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/realtime"
)

// UQ 上传队列。
type UQ struct {
	tasks          chan *DbUploadTask
	numWorkers     int
	active         int
	reserved       map[uint]struct{} // 缓冲和执行中的任务，直到整个 Upload 返回才释放
	mutex          sync.RWMutex
	workers        sync.WaitGroup
	running        bool
	retryTriggered bool
	stop           chan struct{}
	wake           chan struct{}
	schedulerDone  chan struct{}
}

// 全局上传队列实例
var GlobalUploadQueue *UQ

// InitUploadQueueBySync 初始化上传队列
func InitUQ() bool {
	if GlobalUploadQueue != nil {
		// 有正在运行的队列
		helpers.AppLogger.Warnf("上传队列已存在，无法重复初始化")
		return false
	}
	GlobalUploadQueue = NewUq(SettingsGlobal.UploadThreads)
	GlobalUploadQueue.Start()
	helpers.AppLogger.Infof("上传队列初始化完成，工作线程数 %d", GlobalUploadQueue.GetConcurrency())
	return true
}

func NewUq(maxConcurrency int) *UQ {
	if maxConcurrency < DefaultUploadThreads || maxConcurrency > MaxUploadThreads {
		maxConcurrency = DefaultUploadThreads
	}
	return &UQ{
		tasks:      make(chan *DbUploadTask, maxConcurrency),
		numWorkers: maxConcurrency,
		reserved:   make(map[uint]struct{}),
	}
}

// Start 启动上传队列
func (uq *UQ) Start() {
	uq.mutex.Lock()
	if uq.running {
		uq.mutex.Unlock()
		return
	}
	uq.running = true
	uq.stop = make(chan struct{})
	uq.wake = make(chan struct{}, 1)
	uq.schedulerDone = make(chan struct{})
	go uq.taskScheduler(uq.stop, uq.wake, uq.schedulerDone)
	realtime.BroadcastQueueStatusChanged(realtime.EventUploadQueueStatusChanged, true)
	uq.mutex.Unlock()
}

// startWorkersLocked 仅在有空闲名额时派发任务；缩小并发和暂停均不影响在途任务。
func (uq *UQ) startWorkersLocked() {
	for uq.running && uq.active < uq.numWorkers && len(uq.tasks) > 0 {
		task := <-uq.tasks
		uq.active++
		uq.workers.Add(1)
		go uq.worker(task)
	}
}

func (uq *UQ) worker(task *DbUploadTask) {
	defer func() {
		uq.mutex.Lock()
		uq.active--
		delete(uq.reserved, task.ID)
		uq.startWorkersLocked()
		uq.mutex.Unlock()
		uq.workers.Done()
	}()
	task.Upload()
}

func (uq *UQ) moveTasksToChannel(stop <-chan struct{}) {
	uq.mutex.Lock()
	if !uq.running || uq.stop != stop {
		uq.mutex.Unlock()
		return
	}
	uq.startWorkersLocked()
	availableSpace := cap(uq.tasks) - len(uq.tasks)
	if availableSpace == 0 {
		uq.mutex.Unlock()
		return
	}
	excludedIDs := make([]uint, 0, len(uq.reserved))
	for id := range uq.reserved {
		excludedIDs = append(excludedIDs, id)
	}
	uq.mutex.Unlock()

	var total int64
	if err := db.Db.Model(&DbUploadTask{}).
		Where("status IN ?", []UploadStatus{UploadStatusPending, UploadStatusRemoteCompletedPendingFinalize}).
		Count(&total).Error; err != nil {
		helpers.AppLogger.Errorf("查询待上传任务数量失败：%v", err)
		return
	}
	if total == 0 {
		if GetUploadingCount() > 0 {
			return
		}
		uq.mutex.Lock()
		if !uq.running || uq.stop != stop || uq.active > 0 || len(uq.tasks) > 0 || uq.retryTriggered {
			uq.mutex.Unlock()
			return
		}
		uq.retryTriggered = true
		uq.mutex.Unlock()
		if err := RetryFailedUploadTasks(DefaultQueueRetryMax); err != nil {
			helpers.AppLogger.Errorf("上传队列自动重试失败任务失败：%v", err)
			uq.mutex.Lock()
			if uq.stop == stop {
				uq.retryTriggered = false
			}
			uq.mutex.Unlock()
		}
		return
	}

	tasks := GetPendingUploadTasks(availableSpace, excludedIDs...)
	uq.mutex.Lock()
	defer uq.mutex.Unlock()
	// 查询期间可能已暂停或恢复，旧调度协程不能向新一轮队列派发任务。
	if !uq.running || uq.stop != stop {
		return
	}
	uq.retryTriggered = false
	for _, task := range tasks {
		if len(uq.tasks) == cap(uq.tasks) {
			break
		}
		if _, reserved := uq.reserved[task.ID]; reserved {
			continue
		}
		uq.reserved[task.ID] = struct{}{}
		uq.tasks <- task
	}
	uq.startWorkersLocked()
}

func (uq *UQ) taskScheduler(stop <-chan struct{}, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		case <-wake:
		}
		uq.moveTasksToChannel(stop)
	}
}

// UpdateConcurrency 更新并发上限；在途上传自然完成，暂停状态保持不变。
func (uq *UQ) UpdateConcurrency(newConcurrency int) {
	if newConcurrency < DefaultUploadThreads || newConcurrency > MaxUploadThreads {
		return
	}
	uq.mutex.Lock()
	defer uq.mutex.Unlock()
	if uq.numWorkers == newConcurrency {
		return
	}
	uq.numWorkers = newConcurrency
	tasks := make(chan *DbUploadTask, newConcurrency)
	for len(uq.tasks) > 0 {
		task := <-uq.tasks
		if len(tasks) < cap(tasks) {
			tasks <- task
		} else {
			delete(uq.reserved, task.ID)
		}
	}
	uq.tasks = tasks
	select {
	case uq.wake <- struct{}{}:
	default:
	}
}

// GetConcurrency 返回配置的上传并发上限。
func (uq *UQ) GetConcurrency() int {
	uq.mutex.RLock()
	defer uq.mutex.RUnlock()
	return uq.numWorkers
}

// Stop 暂停派发并等待调度协程退出，在途上传继续完成。
func (uq *UQ) Stop() {
	uq.mutex.Lock()
	if !uq.running {
		uq.mutex.Unlock()
		helpers.AppLogger.Warnf("上传队列未在运行中")
		return
	}
	uq.running = false
	close(uq.stop)
	done := uq.schedulerDone
	for len(uq.tasks) > 0 {
		task := <-uq.tasks
		delete(uq.reserved, task.ID)
	}
	realtime.BroadcastQueueStatusChanged(realtime.EventUploadQueueStatusChanged, false)
	uq.mutex.Unlock()
	<-done
	helpers.AppLogger.Info("上传队列已停止")
}

// Restart 重启上传队列
func (uq *UQ) Restart() {
	uq.Stop()
	uq.Start()
	helpers.AppLogger.Info("上传队列已重启")
}

func (uq *UQ) IsRunning() bool {
	uq.mutex.RLock()
	defer uq.mutex.RUnlock()
	return uq.running
}
