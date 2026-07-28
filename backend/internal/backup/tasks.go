package backup

import (
	"context"
	"sync"
	"time"

	"qmediasync/internal/directoryupload"
	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"
	"qmediasync/internal/syncstrm"
	"qmediasync/internal/taskgate"

	"github.com/robfig/cron/v3"
)

// taskIdlePollInterval 是等待既有任务静止时的轮询间隔。
// 轮询只读取各子系统自身的运行计数，不写入业务数据。
const taskIdlePollInterval = 2 * time.Second

// RunningTask 是仍未静止的运行子系统摘要。
// 人工备份或恢复遇到运行中的任务时以 HTTP 409 返回它，不进入维护也不等待。
type RunningTask struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Running int    `json:"running"`
}

// taskSubsystem 描述一个必须在实际备份或恢复前静止的运行子系统。
// block 只负责阻止新任务进入；静止判定必须来自 inFlight，因为停止请求本身不是静止证明。
type taskSubsystem struct {
	kind string
	name string
	// blockWaits 表示 block 自身会同步等待既有任务退出，阻塞完成后不需要继续轮询。
	blockWaits bool
	block      func()
	resume     func()
	inFlight   func() int
}

// taskBarrier 阻止新任务入队并等待既有任务实际静止。
type taskBarrier struct {
	subsystems  []taskSubsystem
	mutex       sync.Mutex
	blocked     bool
	cronStopped []context.Context
}

func newTaskBarrier() *taskBarrier {
	barrier := &taskBarrier{}
	barrier.subsystems = []taskSubsystem{
		{
			kind:     "sync",
			name:     "同步任务",
			block:    synccron.PauseAllNewSyncQueues,
			resume:   synccron.ResumeAllNewSyncQueues,
			inFlight: runningSyncQueueCount,
		},
		{
			kind:       "download",
			name:       "下载队列",
			blockWaits: true,
			block:      stopDownloadQueue,
			resume:     startDownloadQueue,
			inFlight:   models.CountRunningDownloadTasks,
		},
		{
			kind:       "upload",
			name:       "上传队列",
			blockWaits: true,
			block:      stopUploadQueue,
			resume:     startUploadQueue,
			inFlight:   models.CountRunningUploadTasks,
		},
		{
			kind:       "directory_upload",
			name:       "目录上传",
			blockWaits: true,
			block:      directoryupload.StopDirectoryUploadService,
			resume:     directoryupload.InitDirectoryUploadService,
			inFlight:   pendingDirectoryUploadCount,
		},
		{
			kind:       "strm",
			name:       "STRM 生成",
			blockWaits: true,
			block:      syncstrm.StopStrmGenerationWorker,
			resume:     syncstrm.InitStrmGenerationWorker,
			inFlight:   models.CountRunningStrmGenerationTasks,
		},
		{
			kind:     "emby",
			name:     "Emby 刷新",
			block:    func() { emby.SetEmbySyncRunning(true) },
			resume:   func() { emby.SetEmbySyncRunning(false) },
			inFlight: runningEmbySyncCount,
		},
		{
			kind:     "cron",
			name:     "定时任务",
			block:    barrier.stopCronSchedulers,
			resume:   startCronSchedulers,
			inFlight: barrier.runningCronCount,
		},
	}
	return barrier
}

// RunningTasks 返回仍在执行的子系统摘要；空切片表示所有任务已经静止。
// 已同步停止并等待过的子系统在阻塞后不再计入，避免把待处理积压误判为运行中。
func (barrier *taskBarrier) RunningTasks() []RunningTask {
	barrier.mutex.Lock()
	blocked := barrier.blocked
	barrier.mutex.Unlock()

	running := make([]RunningTask, 0, len(barrier.subsystems))
	for _, subsystem := range barrier.subsystems {
		if blocked && subsystem.blockWaits {
			continue
		}
		if count := subsystem.inFlight(); count > 0 {
			running = append(running, RunningTask{Kind: subsystem.kind, Name: subsystem.name, Running: count})
		}
	}
	return running
}

// Block 阻止全部子系统接受新任务；已经在执行的任务不会被中断。
func (barrier *taskBarrier) Block() {
	barrier.mutex.Lock()
	defer barrier.mutex.Unlock()
	// 准入屏障必须先于各队列的 Stop/Pause 生效。否则 HTTP Restart、
	// 动态 source queue 或后台事件能在等待静止期间重新启动任务。
	taskgate.BlockNewTasks()
	barrier.cronStopped = nil
	for _, subsystem := range barrier.subsystems {
		subsystem.block()
	}
	barrier.blocked = true
}

// WaitIdle 等待全部子系统实际静止。
// ctx 取消时返回其错误，由调用方决定终态；实际备份和恢复不设总执行时限，
// 只有定时备份等待既有任务静止会带上截止时间。
func (barrier *taskBarrier) WaitIdle(ctx context.Context, observe func([]RunningTask)) error {
	ticker := time.NewTicker(taskIdlePollInterval)
	defer ticker.Stop()
	for {
		running := barrier.RunningTasks()
		if len(running) == 0 {
			return nil
		}
		if observe != nil {
			observe(running)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Resume 恢复全部子系统接受新任务。
func (barrier *taskBarrier) Resume() {
	barrier.mutex.Lock()
	defer barrier.mutex.Unlock()
	if !barrier.blocked {
		return
	}
	barrier.blocked = false
	barrier.cronStopped = nil
	// 终态已经解除维护，先恢复准入再重新启动各子系统；否则它们自身的
	// 准入检查会把恢复动作误判成新的业务任务。
	taskgate.AllowNewTasks()
	for _, subsystem := range barrier.subsystems {
		subsystem.resume()
	}
}

func (barrier *taskBarrier) stopCronSchedulers() {
	for _, scheduler := range []*cron.Cron{
		synccron.GlobalCron,
		synccron.SyncCron,
		synccron.ScrapeCron,
		synccron.TokenCron,
	} {
		if scheduler == nil {
			continue
		}
		barrier.cronStopped = append(barrier.cronStopped, scheduler.Stop())
	}
}

// runningCronCount 统计停止后仍在执行的定时任务；cron 的停止上下文完成即代表任务已退出。
// cronStopped 由 Block 与 Resume 在锁内改写，而 RunningTasks 可能被 HTTP 请求 goroutine
// 并发调用，因此这里必须自行加锁；Block 与 Resume 都不会回调 inFlight，不存在重入。
func (barrier *taskBarrier) runningCronCount() int {
	barrier.mutex.Lock()
	defer barrier.mutex.Unlock()

	running := 0
	for _, stopped := range barrier.cronStopped {
		select {
		case <-stopped.Done():
		default:
			running++
		}
	}
	return running
}

func startCronSchedulers() {
	synccron.InitCron()
	synccron.InitSyncCron()
	synccron.InitScrapeCron()
	synccron.InitTokenCron()
}

func stopDownloadQueue() {
	if models.GlobalDownloadQueue != nil {
		models.GlobalDownloadQueue.StopAndWait()
		if err := models.UpdateDownloadingToPending(); err != nil {
			helpers.AppLogger.Errorf("恢复未开始下载任务为待处理失败：%v", err)
		}
	}
}

func startDownloadQueue() {
	if models.GlobalDownloadQueue != nil {
		models.GlobalDownloadQueue.Start()
	}
}

func stopUploadQueue() {
	if models.GlobalUploadQueue != nil {
		models.GlobalUploadQueue.StopAndWait()
	}
}

func startUploadQueue() {
	if models.GlobalUploadQueue != nil {
		models.GlobalUploadQueue.Start()
	}
}

func runningSyncQueueCount() int {
	running := 0
	for _, status := range synccron.GetAllNewQueueStatus() {
		if syncQueueExecuting(status) {
			running++
		}
	}
	return running
}

// syncQueueExecuting 只把正在执行同步任务的队列计入等待。
// is_running 只是 processor goroutine 存活标记：空闲 worker 在 PauseAll 后仍会存活，
// 不能被误判为业务任务仍在运行。
func syncQueueExecuting(status map[string]interface{}) bool {
	taskType, ok := status["current_task_type"].(string)
	return ok && taskType != ""
}

// pendingDirectoryUploadCount 统计仍在目录上传管线中的待处理文件。
// 服务停止后不再返回运行状态，因此阻塞完成即代表该子系统已静止。
func pendingDirectoryUploadCount() int {
	pending := 0
	for _, status := range directoryupload.GetDirectoryUploadRuntimeStatuses() {
		pending += status.PendingCount
	}
	return pending
}

func runningEmbySyncCount() int {
	running := models.CountRunningEmbyLibraryRefreshTasks()
	if models.IsEmbySyncRunningInDB() {
		running++
	}
	return running
}
