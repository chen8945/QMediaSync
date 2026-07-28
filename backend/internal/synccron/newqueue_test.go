package synccron

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/models"
	"qmediasync/internal/taskgate"
)

func TestTaskAdmissionGateBlocksSyncEnqueueAndNewSourceQueue(t *testing.T) {
	taskgate.BlockNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	queue := NewQueuePerType(models.SourceType115)
	if queue.status != QueueStatusPaused {
		t.Fatalf("准入关闭时新来源队列状态 = %s，期望 %s", queue.status, QueueStatusPaused)
	}

	err := queue.AddTask(&NewSyncTask{ID: 1, TaskType: SyncTaskTypeStrm})
	if !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("AddTask() error = %v，期望任务准入被拒绝", err)
	}
}

func TestTaskAdmissionGateBlocksCronInitialization(t *testing.T) {
	oldSyncCron, oldScrapeCron := SyncCron, ScrapeCron
	SyncCron, ScrapeCron = nil, nil
	t.Cleanup(func() {
		SyncCron, ScrapeCron = oldSyncCron, oldScrapeCron
	})
	taskgate.BlockNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	if err := InitSyncCronWithError(); !errors.Is(err, taskgate.ErrTaskAdmissionBlocked) {
		t.Fatalf("InitSyncCronWithError() error = %v，期望任务准入被拒绝", err)
	}
	InitScrapeCron()
	if SyncCron != nil || ScrapeCron != nil {
		t.Fatal("任务准入关闭时不能创建 Cron 调度器")
	}
}

func TestNewSyncQueuePerType(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)
	if queue == nil {
		t.Fatal("Failed to create queue")
	}
	if queue.sourceType != models.SourceType115 {
		t.Errorf("Expected source type %s, got %s", models.SourceType115, queue.sourceType)
	}
	if queue.status != QueueStatusRunning {
		t.Errorf("Expected status %s, got %s", QueueStatusRunning, queue.status)
	}
}

func TestAddTask(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)

	task := &NewSyncTask{
		ID:       1,
		TaskType: SyncTaskTypeStrm,
	}

	err := queue.AddTask(task)
	if err != nil {
		t.Fatalf("Failed to add task: %v", err)
	}

	if !queue.isTaskExists(task) {
		t.Error("Task should exist after adding")
	}

	duplicateTask := &NewSyncTask{
		ID:       1,
		TaskType: SyncTaskTypeStrm,
	}

	err = queue.AddTask(duplicateTask)
	if err == nil {
		t.Error("Should return error when adding duplicate task")
	}
}

func TestTaskStatus(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)

	status := queue.CheckTaskStatus(1, SyncTaskTypeStrm)
	if status != TaskStatusNone {
		t.Errorf("Expected status %d, got %d", TaskStatusNone, status)
	}

	task := &NewSyncTask{
		ID:       1,
		TaskType: SyncTaskTypeStrm,
	}

	err := queue.AddTask(task)
	if err != nil {
		t.Fatalf("Failed to add task: %v", err)
	}

	status = queue.CheckTaskStatus(1, SyncTaskTypeStrm)
	if status != TaskStatusWaiting {
		t.Errorf("Expected status %d, got %d", TaskStatusWaiting, status)
	}
}

func TestAddStrmTaskReportsWaitingStatus(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)
	task := &NewSyncTask{ID: 7, TaskType: SyncTaskTypeStrm}

	if err := queue.AddTask(task); err != nil {
		t.Fatalf("添加 STRM 任务失败: %v", err)
	}

	status := queue.CheckTaskStatus(7, SyncTaskTypeStrm)
	if status != TaskStatusWaiting {
		t.Fatalf("队列中的 STRM 任务状态 = %d，期望 %d", status, TaskStatusWaiting)
	}
}

func TestAddTaskBroadcastsQueuedBeforeStartingProcessor(t *testing.T) {
	data, err := os.ReadFile("newqueue.go")
	if err != nil {
		t.Fatalf("读取队列实现失败: %v", err)
	}
	source := string(data)
	addTaskStart := strings.Index(source, "func (q *NewSyncQueuePerType) AddTask(task *NewSyncTask) error")
	if addTaskStart < 0 {
		t.Fatal("未找到 AddTask 实现")
	}
	nextFunc := strings.Index(source[addTaskStart:], "\nfunc (q *NewSyncQueuePerType) isTaskExistsUnsafe")
	if nextFunc < 0 {
		t.Fatal("未找到 AddTask 结束位置")
	}
	addTaskBody := source[addTaskStart : addTaskStart+nextFunc]
	queuedIndex := strings.Index(addTaskBody, "strmTaskQueuedBroadcaster(task)")
	handoffIndex := strings.Index(addTaskBody, "q.taskChan <- task")
	startIndex := strings.Index(addTaskBody, "q.startProcessorIfNotRunningUnsafe()")
	if queuedIndex < 0 {
		t.Fatal("AddTask 应广播 STRM 等待状态")
	}
	if handoffIndex < 0 {
		t.Fatal("AddTask 应将任务交给 taskChan")
	}
	if startIndex < 0 {
		t.Fatal("AddTask 应启动队列处理器")
	}
	if queuedIndex > handoffIndex {
		t.Fatal("STRM 等待状态广播必须早于 taskChan 交付，避免已有空闲处理器先发运行中事件")
	}
	if queuedIndex > startIndex {
		t.Fatal("STRM 等待状态广播必须早于处理器启动，避免等待事件覆盖运行中状态")
	}
}

func TestStrmQueuedBroadcasterUsesNonBlockingBroadcast(t *testing.T) {
	data, err := os.ReadFile("newqueue.go")
	if err != nil {
		t.Fatalf("读取队列实现失败: %v", err)
	}
	source := string(data)
	broadcastStart := strings.Index(source, "func tryBroadcastStrmTaskQueued(task *NewSyncTask)")
	if broadcastStart < 0 {
		t.Fatal("未找到 STRM queued 广播实现")
	}
	nextConst := strings.Index(source[broadcastStart:], "\nvar strmTaskQueuedBroadcaster")
	if nextConst < 0 {
		t.Fatal("未找到 STRM queued 广播实现结束位置")
	}
	body := source[broadcastStart : broadcastStart+nextConst]
	if !strings.Contains(body, "realtime.TryBroadcastEvent") {
		t.Fatal("STRM queued 广播必须使用非阻塞 TryBroadcastEvent")
	}
	if strings.Contains(body, "realtime.BroadcastEvent") {
		t.Fatal("STRM queued 广播不能使用阻塞 BroadcastEvent")
	}
}

func TestCancelTask(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)

	task := &NewSyncTask{
		ID:       1,
		TaskType: SyncTaskTypeStrm,
	}

	err := queue.AddTask(task)
	if err != nil {
		t.Fatalf("Failed to add task: %v", err)
	}

	status := queue.CheckTaskStatus(1, SyncTaskTypeStrm)
	if status != TaskStatusWaiting {
		t.Errorf("Expected status %d, got %d", TaskStatusWaiting, status)
	}

	err = queue.CancelTask(1, SyncTaskTypeStrm)
	if err != nil {
		t.Fatalf("Failed to cancel task: %v", err)
	}

	status = queue.CheckTaskStatus(1, SyncTaskTypeStrm)
	if status != TaskStatusNone {
		t.Errorf("Expected status %d after cancellation, got %d", TaskStatusNone, status)
	}
}

func TestPauseResume(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)

	task := &NewSyncTask{
		ID:       1,
		TaskType: SyncTaskTypeStrm,
	}

	err := queue.AddTask(task)
	if err != nil {
		t.Fatalf("Failed to add task: %v", err)
	}

	queue.Pause()
	statusInfo := queue.GetStatus()
	if statusInfo["status"] != QueueStatusPaused {
		t.Errorf("Expected status %s, got %s", QueueStatusPaused, statusInfo["status"])
	}

	task2 := &NewSyncTask{
		ID:       2,
		TaskType: SyncTaskTypeStrm,
	}

	err = queue.AddTask(task2)
	if err != nil {
		t.Fatalf("Failed to add task during pause: %v", err)
	}

	queue.Resume()
	statusInfo = queue.GetStatus()
	if statusInfo["status"] != QueueStatusRunning {
		t.Errorf("Expected status %s, got %s", QueueStatusRunning, statusInfo["status"])
	}

	time.Sleep(100 * time.Millisecond)
}

func TestPauseKeepsBufferedTaskWaiting(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)
	queue.Pause()
	task := &NewSyncTask{ID: 1, TaskType: SyncTaskTypeStrm}
	queue.waitingQueue[task.Key()] = task
	queue.taskChan <- task

	go queue.process()
	t.Cleanup(queue.Stop)

	deadline := time.Now().Add(time.Second)
	for {
		if len(queue.taskChan) == 0 && queue.CheckTaskStatus(task.ID, task.TaskType) == TaskStatusWaiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("暂停后已缓冲的任务不应进入运行状态")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestNewSyncQueueManager(t *testing.T) {
	manager := InitNewSyncQueueManager()
	if manager == nil {
		t.Fatal("Failed to create manager")
	}

	task := &NewSyncTask{
		ID:       1,
		TaskType: SyncTaskTypeStrm,
	}

	queue := manager.getQueue(models.SourceType115)
	err := queue.AddTask(task)
	if err != nil {
		t.Fatalf("Failed to add task: %v", err)
	}

	status := queue.CheckTaskStatus(1, SyncTaskTypeStrm)
	if status != TaskStatusWaiting {
		t.Errorf("Expected status %d, got %d", TaskStatusWaiting, status)
	}
}

func TestPauseResumeAll(t *testing.T) {
	manager := InitNewSyncQueueManager()

	task1 := &NewSyncTask{
		ID:       1,
		TaskType: SyncTaskTypeStrm,
	}
	task2 := &NewSyncTask{
		ID:       2,
		TaskType: SyncTaskTypeStrm,
	}

	queue115 := manager.getQueue(models.SourceType115)
	queueLocal := manager.getQueue(models.SourceTypeLocal)

	queue115.AddTask(task1)
	queueLocal.AddTask(task2)

	manager.PauseAll()

	status115 := queue115.GetStatus()
	statusLocal := queueLocal.GetStatus()

	if status115["status"] != QueueStatusPaused {
		t.Errorf("Expected 115 queue status %s, got %s", QueueStatusPaused, status115["status"])
	}
	if statusLocal["status"] != QueueStatusPaused {
		t.Errorf("Expected Local queue status %s, got %s", QueueStatusPaused, statusLocal["status"])
	}

	manager.ResumeAll()

	status115 = queue115.GetStatus()
	statusLocal = queueLocal.GetStatus()

	if status115["status"] != QueueStatusRunning {
		t.Errorf("Expected 115 queue status %s, got %s", QueueStatusRunning, status115["status"])
	}
	if statusLocal["status"] != QueueStatusRunning {
		t.Errorf("Expected Local queue status %s, got %s", QueueStatusRunning, statusLocal["status"])
	}

	time.Sleep(100 * time.Millisecond)
}

func TestSyncTaskTypeFmtStringUsesMachineValue(t *testing.T) {
	tests := []struct {
		name     string
		taskType SyncTaskType
		want     string
	}{
		{name: "STRM 同步", taskType: SyncTaskTypeStrm, want: "strm_sync"},
		{name: "刮削整理", taskType: SyncTaskTypeScrape, want: "scrape_organize"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fmt.Sprintf("%s", tt.taskType); got != tt.want {
				t.Fatalf("fmt 字符串值 = %s，期望 %s", got, tt.want)
			}
		})
	}
}

func TestSyncTaskTypeValuesAndDisplayNames(t *testing.T) {
	tests := []struct {
		name        string
		taskType    SyncTaskType
		wantValue   string
		wantDisplay string
	}{
		{name: "STRM 同步", taskType: SyncTaskTypeStrm, wantValue: "strm_sync", wantDisplay: "STRM 同步"},
		{name: "刮削整理", taskType: SyncTaskTypeScrape, wantValue: "scrape_organize", wantDisplay: "刮削整理"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.taskType) != tt.wantValue {
				t.Fatalf("任务类型存储值 = %s，期望 %s", tt.taskType, tt.wantValue)
			}
			if tt.taskType.DisplayName() != tt.wantDisplay {
				t.Fatalf("任务类型展示名 = %s，期望 %s", tt.taskType.DisplayName(), tt.wantDisplay)
			}
		})
	}
}

func TestNewSyncTaskKeyUsesMachineTaskTypeValue(t *testing.T) {
	tests := []struct {
		name string
		task NewSyncTask
		want string
	}{
		{
			name: "ID 任务使用机器值",
			task: NewSyncTask{ID: 7, TaskType: SyncTaskTypeStrm},
			want: "7-strm_sync",
		},
		{
			name: "路径任务使用机器值",
			task: NewSyncTask{SourcePathId: "/movie", TaskType: SyncTaskTypeScrape},
			want: "/movie-scrape_organize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.Key(); got != tt.want {
				t.Fatalf("任务 key = %s，期望 %s", got, tt.want)
			}
		})
	}
}

func TestQueueStatusReturnsMachineTaskTypeValue(t *testing.T) {
	queue := NewQueuePerType(models.SourceType115)
	queue.currentTask = &NewSyncTask{ID: 8, TaskType: SyncTaskTypeStrm}

	status := queue.GetStatus()
	if status["current_task_type"] != "strm_sync" {
		t.Fatalf("current_task_type = %v，期望 strm_sync", status["current_task_type"])
	}
}
