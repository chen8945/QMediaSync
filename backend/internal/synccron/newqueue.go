package synccron

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/scrape"
	"qmediasync/internal/syncstrm"
	"qmediasync/internal/taskgate"
)

type SyncTaskType string

const (
	SyncTaskTypeStrm   SyncTaskType = "strm_sync"
	SyncTaskTypeScrape SyncTaskType = "scrape_organize"
)

func (t SyncTaskType) DisplayName() string {
	switch t {
	case SyncTaskTypeStrm:
		return "STRM 同步"
	case SyncTaskTypeScrape:
		return "刮削整理"
	default:
		return string(t)
	}
}

func logInfo(format string, args ...interface{}) {
	if helpers.AppLogger != nil {
		helpers.AppLogger.Infof(format, args...)
	}
}

func logError(format string, args ...interface{}) {
	if helpers.AppLogger != nil {
		helpers.AppLogger.Errorf(format, args...)
	}
}

func tryBroadcastStrmTaskQueued(task *NewSyncTask) {
	if task == nil || task.TaskType != SyncTaskTypeStrm || task.ID == 0 {
		return
	}
	realtime.TryBroadcastEvent(realtime.EventStrmSyncTaskQueued, map[string]any{
		"sync_path_id": task.ID,
		"is_running":   TaskStatusWaiting,
		"task_type":    string(task.TaskType),
	})
}

var strmTaskQueuedBroadcaster = tryBroadcastStrmTaskQueued

const (
	QueueStatusRunning = "running"
	QueueStatusPaused  = "paused"
	QueueStatusStopped = "stopped"
)

const (
	TaskStatusNone    = 0
	TaskStatusWaiting = 1
	TaskStatusRunning = 2
)

type NewSyncTask struct {
	ID           uint
	TaskType     SyncTaskType
	SourcePath   string
	SourcePathId string
	TargetPath   string
	IsFile       bool
	SourceType   models.SourceType
	AccountId    uint
}

func (t *NewSyncTask) Key() string {
	if t.ID > 0 {
		return fmt.Sprintf("%d-%s", t.ID, string(t.TaskType))
	}
	return fmt.Sprintf("%s-%s", t.SourcePathId, string(t.TaskType))
}

type NewSyncQueuePerType struct {
	sourceType       models.SourceType
	taskChan         chan *NewSyncTask
	waitingQueue     map[string]*NewSyncTask
	currentTask      *NewSyncTask
	status           string
	mutex            sync.RWMutex
	processorStartMu sync.Mutex
	ctx              context.Context
	cancelFunc       context.CancelFunc
	runningFlag      int32
	scrapeInstance   *scrape.Scrape
	strmSync         *syncstrm.SyncStrm
}

func NewQueuePerType(sourceType models.SourceType) *NewSyncQueuePerType {
	ctx, cancel := context.WithCancel(context.Background())
	status := QueueStatusRunning
	if taskgate.IsBlocked() {
		// 新 source type 也必须继承当前准入状态，不能因为此前没有队列
		// 而绕过 PauseAll 创建一个默认运行的 worker。
		status = QueueStatusPaused
	}
	return &NewSyncQueuePerType{
		sourceType:   sourceType,
		taskChan:     make(chan *NewSyncTask, 50),
		waitingQueue: make(map[string]*NewSyncTask),
		status:       status,
		ctx:          ctx,
		cancelFunc:   cancel,
	}
}

func (q *NewSyncQueuePerType) isTaskExists(task *NewSyncTask) bool {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	key := task.Key()
	if _, exists := q.waitingQueue[key]; exists {
		return true
	}

	if q.currentTask != nil && q.currentTask.Key() == key {
		return true
	}

	return false
}

func (q *NewSyncQueuePerType) AddTask(task *NewSyncTask) error {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return err
	}
	defer releaseAdmission()
	q.processorStartMu.Lock()
	defer q.processorStartMu.Unlock()

	q.mutex.Lock()

	if q.isTaskExistsUnsafe(task) {
		q.mutex.Unlock()
		return fmt.Errorf("任务已存在：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
	}

	if len(q.waitingQueue) >= cap(q.taskChan) {
		q.mutex.Unlock()
		return fmt.Errorf("任务队列已满：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
	}

	q.waitingQueue[task.Key()] = task
	shouldBroadcastQueued := task.TaskType == SyncTaskTypeStrm && task.ID > 0
	shouldStartProcessor := false

	if q.status == QueueStatusRunning {
		if len(q.taskChan) >= cap(q.taskChan) {
			delete(q.waitingQueue, task.Key())
			q.mutex.Unlock()
			return fmt.Errorf("任务队列已满：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
		}
		if shouldBroadcastQueued {
			strmTaskQueuedBroadcaster(task)
		}
		select {
		case q.taskChan <- task:
			if helpers.AppLogger != nil {
				logInfo("任务已加入队列：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
			}
			shouldStartProcessor = true
		default:
			delete(q.waitingQueue, task.Key())
			q.mutex.Unlock()
			return fmt.Errorf("任务队列已满：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
		}
	} else {
		if helpers.AppLogger != nil {
			logInfo("任务已加入暂停队列：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
		}
	}

	q.mutex.Unlock()

	if shouldBroadcastQueued && !shouldStartProcessor {
		strmTaskQueuedBroadcaster(task)
	}

	if shouldStartProcessor {
		q.mutex.Lock()
		q.startProcessorIfNotRunningUnsafe()
		q.mutex.Unlock()
	}

	return nil
}

func (q *NewSyncQueuePerType) isTaskExistsUnsafe(task *NewSyncTask) bool {
	key := task.Key()
	if _, exists := q.waitingQueue[key]; exists {
		return true
	}

	if q.currentTask != nil && q.currentTask.Key() == key {
		return true
	}

	return false
}

func (q *NewSyncQueuePerType) startProcessorIfNotRunningUnsafe() {
	// 打印任务运行状态
	logInfo("队列运行状态：%d", atomic.LoadInt32(&q.runningFlag))
	if atomic.CompareAndSwapInt32(&q.runningFlag, 0, 1) {
		go q.process()
	}
}

func (q *NewSyncQueuePerType) StartProcessor() {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return
	}
	defer releaseAdmission()
	q.processorStartMu.Lock()
	defer q.processorStartMu.Unlock()

	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.startProcessorIfNotRunningUnsafe()
}

func (q *NewSyncQueuePerType) process() {
	logInfo("队列处理协程已启动：SourceType=%s", q.sourceType)
	defer atomic.StoreInt32(&q.runningFlag, 0)

	for {
		select {
		case <-q.ctx.Done():
			logInfo("队列处理协程已停止：SourceType=%s", q.sourceType)
			return

		case task, ok := <-q.taskChan:
			if !ok {
				logInfo("任务通道已关闭：SourceType=%s", q.sourceType)
				return
			}

			q.mutex.Lock()

			if _, exists := q.waitingQueue[task.Key()]; !exists {
				logInfo("任务已被取消，跳过处理：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
				q.mutex.Unlock()
				continue
			}
			if q.status != QueueStatusRunning {
				q.mutex.Unlock()
				continue
			}

			q.currentTask = task
			delete(q.waitingQueue, task.Key())
			q.mutex.Unlock()

			logInfo("开始处理任务：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)

			q.executeTask(task)

			q.mutex.Lock()
			q.currentTask = nil
			q.mutex.Unlock()

			logInfo("任务处理完成：类型=%s，ID=%d", task.TaskType.DisplayName(), task.ID)
		}
	}
}

func (q *NewSyncQueuePerType) executeTask(task *NewSyncTask) {
	defer func() {
		if r := recover(); r != nil {
			stack := make([]byte, 4096)
			length := runtime.Stack(stack, false)
			stackStr := string(stack[:length])
			logError("任务执行异常：类型=%s，ID=%d，错误=%v\n堆栈信息：\n%s", task.TaskType.DisplayName(), task.ID, r, stackStr)
		}
	}()

	switch task.TaskType {
	case SyncTaskTypeStrm:
		q.executeStrmSync(task)
	case SyncTaskTypeScrape:
		q.executeScrape(task)
	}
}

func (q *NewSyncQueuePerType) executeStrmSync(task *NewSyncTask) {
	if task.ID == 0 {
		// 手动同步
		account, err := models.GetAccountById(task.AccountId)
		if err != nil {
			logError("获取账号失败，ID=%d，错误=%v", task.AccountId, err)
			return
		}
		q.strmSync = syncstrm.NewSyncStrmByPath(account, task.SourcePath, task.SourcePathId, task.TargetPath, task.IsFile)
		if q.strmSync == nil {
			logError("创建同步任务失败")
			return
		}
	} else {
		syncPath := models.GetSyncPathById(task.ID)
		if syncPath == nil {
			logError("获取同步目录失败，ID=%d", task.ID)
			return
		}

		if syncPath.SourceType != q.sourceType {
			logError("同步目录类型不匹配：预期=%s，实际=%s", q.sourceType, syncPath.SourceType)
			return
		}

		logInfo("开始执行 STRM 同步任务：ID=%d", task.ID)
		q.strmSync = syncstrm.NewSyncStrmFromSyncPath(syncPath)
		if q.strmSync == nil {
			logError("创建同步任务失败")
			return
		}
	}

	// 触发 STRM 同步任务开始事件
	startPayload := map[string]any{
		"task_id": task.ID,
	}
	if q.strmSync != nil && q.strmSync.Sync != nil {
		startPayload["sync_id"] = q.strmSync.Sync.ID
		startPayload["sync_path_id"] = q.strmSync.Sync.SyncPathId
		startPayload["log_path"] = models.SyncLogRelativePath(q.strmSync.Sync.ID)
	}
	realtime.BroadcastEvent(realtime.EventStrmSyncTaskStart, startPayload)

	defer func() {
		q.strmSync = nil
	}()
	if startErr := q.strmSync.Start(); startErr == nil {
		logInfo("STRM 同步任务执行成功：ID=%d", task.ID)
		// 触发 STRM 同步任务完成事件
		completePayload := map[string]any{
			"task_id": task.ID,
			"success": true,
		}
		if q.strmSync != nil && q.strmSync.Sync != nil {
			completePayload["sync_id"] = q.strmSync.Sync.ID
			completePayload["sync_path_id"] = q.strmSync.Sync.SyncPathId
			completePayload["log_path"] = models.SyncLogRelativePath(q.strmSync.Sync.ID)
		}
		realtime.BroadcastEvent(realtime.EventStrmSyncTaskComplete, completePayload)
	} else {
		logError("STRM 同步任务执行失败：ID=%d，错误=%v", task.ID, startErr)
		// 触发 STRM 同步任务完成事件（失败）
		completePayload := map[string]any{
			"task_id": task.ID,
			"success": false,
			"error":   startErr.Error(),
		}
		if q.strmSync != nil && q.strmSync.Sync != nil {
			completePayload["sync_id"] = q.strmSync.Sync.ID
			completePayload["sync_path_id"] = q.strmSync.Sync.SyncPathId
			completePayload["log_path"] = models.SyncLogRelativePath(q.strmSync.Sync.ID)
		}
		realtime.BroadcastEvent(realtime.EventStrmSyncTaskComplete, completePayload)
	}
}

func (q *NewSyncQueuePerType) executeScrape(task *NewSyncTask) {
	scrapePath := models.GetScrapePathByID(task.ID)
	if scrapePath == nil {
		logError("获取刮削目录失败，ID=%d", task.ID)
		return
	}

	if scrapePath.SourceType != q.sourceType {
		logError("刮削目录类型不匹配：预期=%s，实际=%s", q.sourceType, scrapePath.SourceType)
		return
	}

	logInfo("开始执行刮削任务：ID=%d", task.ID)

	// 触发刮削任务开始事件
	realtime.BroadcastEvent(realtime.EventScraperTaskStart, map[string]any{
		"task_id":   task.ID,
		"path_name": scrapePath.SourcePath,
	})

	q.scrapeInstance = scrape.NewScrape(scrapePath)
	if q.scrapeInstance == nil {
		logError("创建刮削任务失败")
		return
	}
	defer func() {
		q.scrapeInstance = nil
	}()

	if success := q.scrapeInstance.Start(); success {
		logInfo("刮削任务执行成功：ID=%d", task.ID)
		// 触发刮削任务完成事件
		realtime.BroadcastEvent(realtime.EventScraperTaskComplete, map[string]any{
			"task_id":   task.ID,
			"path_name": scrapePath.SourcePath,
			"success":   true,
		})
	} else {
		logError("刮削任务执行失败：ID=%d", task.ID)
		// 触发刮削任务完成事件（失败）
		realtime.BroadcastEvent(realtime.EventScraperTaskComplete, map[string]any{
			"task_id":   task.ID,
			"path_name": scrapePath.SourcePath,
			"success":   false,
		})
	}
}

func (q *NewSyncQueuePerType) CancelTask(id uint, taskType SyncTaskType) error {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	key := fmt.Sprintf("%d-%s", id, string(taskType))

	if _, exists := q.waitingQueue[key]; exists {
		delete(q.waitingQueue, key)
		logInfo("任务已从等待队列移除：类型=%s，ID=%d", taskType.DisplayName(), id)
		return nil
	}

	if q.currentTask != nil && q.currentTask.Key() == key {
		if taskType == SyncTaskTypeStrm && q.strmSync != nil {
			q.strmSync.Stop()
			q.strmSync = nil
			logInfo("STRM 同步任务已取消：ID=%d", id)
		} else if taskType == SyncTaskTypeScrape && q.scrapeInstance != nil {
			q.scrapeInstance.Stop()
			logInfo("刮削任务已取消：ID=%d", id)
		}
		q.currentTask = nil
		return nil
	}

	return fmt.Errorf("任务未找到：类型=%s，ID=%d", taskType.DisplayName(), id)
}

func (q *NewSyncQueuePerType) CheckTaskStatus(id uint, taskType SyncTaskType) int {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	key := fmt.Sprintf("%d-%s", id, string(taskType))

	if _, exists := q.waitingQueue[key]; exists {
		return TaskStatusWaiting
	}

	if q.currentTask != nil && q.currentTask.Key() == key {
		return TaskStatusRunning
	}

	return TaskStatusNone
}

func (q *NewSyncQueuePerType) Pause() {
	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.status == QueueStatusPaused {
		return
	}

	logInfo("暂停队列：SourceType=%s", q.sourceType)
	q.status = QueueStatusPaused
}

func (q *NewSyncQueuePerType) Resume() {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return
	}
	defer releaseAdmission()
	q.processorStartMu.Lock()
	defer q.processorStartMu.Unlock()

	q.mutex.Lock()
	defer q.mutex.Unlock()

	if q.status != QueueStatusPaused {
		return
	}

	logInfo("恢复队列：SourceType=%s", q.sourceType)
	q.status = QueueStatusRunning

	taskCount := 0
	for _, task := range q.waitingQueue {
		select {
		case q.taskChan <- task:
			taskCount++
		default:
			goto done
		}
	}
done:
	if taskCount > 0 {
		logInfo("已将 %d 个任务重新加入队列：SourceType=%s", taskCount, q.sourceType)
		q.startProcessorIfNotRunningUnsafe()
	}
}

func (q *NewSyncQueuePerType) GetStatus() map[string]interface{} {
	q.mutex.RLock()
	defer q.mutex.RUnlock()

	currentTaskID := uint(0)
	currentTaskType := ""
	if q.currentTask != nil {
		currentTaskID = q.currentTask.ID
		currentTaskType = string(q.currentTask.TaskType)
	}

	return map[string]interface{}{
		"source_type":       q.sourceType,
		"status":            q.status,
		"waiting_count":     len(q.waitingQueue),
		"current_task_id":   currentTaskID,
		"current_task_type": currentTaskType,
		"is_running":        atomic.LoadInt32(&q.runningFlag) == 1,
	}
}

func (q *NewSyncQueuePerType) Stop() {
	q.cancelFunc()
	close(q.taskChan)
	q.mutex.Lock()
	q.status = QueueStatusStopped
	q.waitingQueue = make(map[string]*NewSyncTask)
	q.currentTask = nil
	q.mutex.Unlock()
}

type NewSyncQueueManager struct {
	queues map[models.SourceType]*NewSyncQueuePerType
	mutex  sync.RWMutex
}

var GlobalNewSyncQueueManager *NewSyncQueueManager

func InitNewSyncQueueManager() *NewSyncQueueManager {
	if GlobalNewSyncQueueManager != nil {
		return GlobalNewSyncQueueManager
	}

	GlobalNewSyncQueueManager = &NewSyncQueueManager{
		queues: make(map[models.SourceType]*NewSyncQueuePerType),
	}
	models.IsStrmSyncTaskActiveFunc = func(syncPathId uint) bool {
		status := CheckNewTaskStatus(syncPathId, SyncTaskTypeStrm)
		return status == TaskStatusWaiting || status == TaskStatusRunning
	}
	return GlobalNewSyncQueueManager
}

func (m *NewSyncQueueManager) getQueue(sourceType models.SourceType) *NewSyncQueuePerType {
	m.mutex.RLock()
	queue, exists := m.queues[sourceType]
	m.mutex.RUnlock()

	if exists {
		return queue
	}

	m.mutex.Lock()
	defer m.mutex.Unlock()

	if queue, exists := m.queues[sourceType]; exists {
		return queue
	}

	queue = NewQueuePerType(sourceType)
	m.queues[sourceType] = queue
	logInfo("创建新队列：SourceType=%s", sourceType)

	return queue
}

func (m *NewSyncQueueManager) AddSyncTask(task *NewSyncTask) error {
	if taskgate.IsBlocked() {
		return taskgate.ErrTaskAdmissionBlocked
	}
	// var sourceType models.SourceType

	// switch task.TaskType {
	// case SyncTaskTypeStrm:
	// 	syncPath := models.GetSyncPathById(task.ID)
	// 	if syncPath == nil {
	// 		return fmt.Errorf("获取同步目录失败：ID=%d", task.ID)
	// 	}
	// 	sourceType = syncPath.SourceType

	// case SyncTaskTypeScrape:
	// 	scrapePath := models.GetScrapePathByID(task.ID)
	// 	if scrapePath == nil {
	// 		return fmt.Errorf("获取刮削目录失败：ID=%d", task.ID)
	// 	}
	// 	sourceType = scrapePath.SourceType

	// default:
	// 	return fmt.Errorf("未知的任务类型：%s", task.TaskType)
	// }

	queue := m.getQueue(task.SourceType)
	// task := &NewSyncTask{ID: id, TaskType: taskType}

	if err := queue.AddTask(task); err != nil {
		return err
	}

	return nil
}

func (m *NewSyncQueueManager) CancelTask(id uint, taskType SyncTaskType) error {
	var sourceType models.SourceType

	switch taskType {
	case SyncTaskTypeStrm:
		syncPath := models.GetSyncPathById(id)
		if syncPath == nil {
			return fmt.Errorf("获取同步目录失败：ID=%d", id)
		}
		sourceType = syncPath.SourceType

	case SyncTaskTypeScrape:
		scrapePath := models.GetScrapePathByID(id)
		if scrapePath == nil {
			return fmt.Errorf("获取刮削目录失败：ID=%d", id)
		}
		sourceType = scrapePath.SourceType

	default:
		return fmt.Errorf("未知的任务类型：%s", taskType.DisplayName())
	}

	queue := m.getQueue(sourceType)
	return queue.CancelTask(id, taskType)
}

func (m *NewSyncQueueManager) CheckTaskStatus(id uint, taskType SyncTaskType) int {
	var sourceType models.SourceType

	switch taskType {
	case SyncTaskTypeStrm:
		syncPath := models.GetSyncPathById(id)
		if syncPath == nil {
			return TaskStatusNone
		}
		sourceType = syncPath.SourceType

	case SyncTaskTypeScrape:
		scrapePath := models.GetScrapePathByID(id)
		if scrapePath == nil {
			return TaskStatusNone
		}
		sourceType = scrapePath.SourceType

	default:
		return TaskStatusNone
	}

	queue := m.getQueue(sourceType)
	return queue.CheckTaskStatus(id, taskType)
}

func (m *NewSyncQueueManager) PauseAll() {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	logInfo("暂停所有任务队列")
	for _, queue := range m.queues {
		queue.Pause()
	}
}

func (m *NewSyncQueueManager) ResumeAll() {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	logInfo("恢复所有任务队列")
	for _, queue := range m.queues {
		queue.Resume()
	}
}

func (m *NewSyncQueueManager) GetAllStatus() map[models.SourceType]map[string]interface{} {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	status := make(map[models.SourceType]map[string]interface{})
	for sourceType, queue := range m.queues {
		status[sourceType] = queue.GetStatus()
	}

	return status
}

func (m *NewSyncQueueManager) GetQueueStatus(sourceType models.SourceType) map[string]interface{} {
	queue := m.getQueue(sourceType)
	return queue.GetStatus()
}

func AddNewSyncTask(task *NewSyncTask) error {
	if taskgate.IsBlocked() {
		return taskgate.ErrTaskAdmissionBlocked
	}
	if GlobalNewSyncQueueManager == nil {
		InitNewSyncQueueManager()
	}
	return GlobalNewSyncQueueManager.AddSyncTask(task)
}

func CancelNewSyncTask(id uint, taskType SyncTaskType) error {
	if GlobalNewSyncQueueManager == nil {
		return fmt.Errorf("队列管理器未初始化")
	}
	return GlobalNewSyncQueueManager.CancelTask(id, taskType)
}

func CheckNewTaskStatus(id uint, taskType SyncTaskType) int {
	if GlobalNewSyncQueueManager == nil {
		return TaskStatusNone
	}
	return GlobalNewSyncQueueManager.CheckTaskStatus(id, taskType)
}

func PauseAllNewSyncQueues() {
	if GlobalNewSyncQueueManager == nil {
		return
	}
	GlobalNewSyncQueueManager.PauseAll()
}

func ResumeAllNewSyncQueues() {
	if GlobalNewSyncQueueManager == nil {
		return
	}
	GlobalNewSyncQueueManager.ResumeAll()
}

func GetAllNewQueueStatus() map[models.SourceType]map[string]interface{} {
	if GlobalNewSyncQueueManager == nil {
		return nil
	}
	return GlobalNewSyncQueueManager.GetAllStatus()
}
