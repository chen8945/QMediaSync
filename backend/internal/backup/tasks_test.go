package backup

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/taskgate"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestTaskBarrierBlocksAndWaitsForActualIdle 保护「停止请求不是静止证明」这条边界：
// 阻塞只关闭新任务入口，等待必须以子系统自身的运行计数为准。
func TestTaskBarrierBlocksAndWaitsForActualIdle(t *testing.T) {
	taskgate.AllowNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)
	blocked := 0
	resumed := 0
	// inFlight 会被后台 goroutine 改写，同时被 WaitIdle 在测试 goroutine 上读取，
	// 必须用原子变量，否则 -race 会判定为数据竞争。
	var inFlight atomic.Int64
	inFlight.Store(2)
	barrier := &taskBarrier{subsystems: []taskSubsystem{{
		kind:     "download",
		name:     "下载队列",
		block:    func() { blocked++ },
		resume:   func() { resumed++ },
		inFlight: func() int { return int(inFlight.Load()) },
	}}}

	running := barrier.RunningTasks()
	if len(running) != 1 || running[0].Kind != "download" || running[0].Running != 2 {
		t.Fatalf("RunningTasks() = %+v, want one running download subsystem", running)
	}

	barrier.Block()
	if !taskgate.IsBlocked() {
		t.Fatal("任务屏障必须在停止各子系统前关闭共享任务准入")
	}
	if blocked != 1 {
		t.Fatalf("Block() called subsystem block %d times, want 1", blocked)
	}
	if len(barrier.RunningTasks()) != 1 {
		t.Fatal("阻塞后仍在执行的任务必须继续计入等待")
	}

	go func() {
		time.Sleep(taskIdlePollInterval / 2)
		inFlight.Store(0)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*taskIdlePollInterval)
	defer cancel()
	if err := barrier.WaitIdle(ctx, nil); err != nil {
		t.Fatalf("WaitIdle() error = %v", err)
	}

	barrier.Resume()
	if taskgate.IsBlocked() {
		t.Fatal("任务屏障恢复后必须重新开放共享任务准入")
	}
	if resumed != 1 {
		t.Fatalf("Resume() called subsystem resume %d times, want 1", resumed)
	}
	barrier.Resume()
	if resumed != 1 {
		t.Fatal("重复 Resume 不得再次恢复子系统")
	}
}

// TestTaskBarrierWaitIdleRespectsDeadline 覆盖定时备份等待上限：超时以调用方决定终态，不是无限等待。
func TestTaskBarrierWaitIdleRespectsDeadline(t *testing.T) {
	barrier := &taskBarrier{subsystems: []taskSubsystem{{
		kind:     "sync",
		name:     "同步任务",
		block:    func() {},
		resume:   func() {},
		inFlight: func() int { return 1 },
	}}}

	observed := 0
	ctx, cancel := context.WithTimeout(context.Background(), taskIdlePollInterval/2)
	defer cancel()
	err := barrier.WaitIdle(ctx, func(running []RunningTask) { observed += len(running) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitIdle() error = %v, want deadline exceeded", err)
	}
	if observed == 0 {
		t.Fatal("等待期间必须向调用方报告运行中的任务")
	}
}

// TestTaskBarrierSkipsSynchronouslyStoppedSubsystems 覆盖同步停止的子系统：
// 它们在阻塞时已经等待过既有任务退出，不能再因积压计数把备份无限期挡住。
func TestTaskBarrierSkipsSynchronouslyStoppedSubsystems(t *testing.T) {
	barrier := &taskBarrier{subsystems: []taskSubsystem{{
		kind:       "strm",
		name:       "STRM 生成",
		blockWaits: true,
		block:      func() {},
		resume:     func() {},
		inFlight:   func() int { return 3 },
	}}}

	if len(barrier.RunningTasks()) != 1 {
		t.Fatal("阻塞前必须如实报告运行中的任务")
	}
	barrier.Block()
	if got := barrier.RunningTasks(); len(got) != 0 {
		t.Fatalf("RunningTasks() = %+v, want empty after synchronous stop", got)
	}
}

func TestSyncQueueExecutingIgnoresIdleWorker(t *testing.T) {
	for _, test := range []struct {
		name   string
		status map[string]interface{}
		want   bool
	}{
		{
			name: "空闲 worker",
			status: map[string]interface{}{
				"is_running":        true,
				"current_task_type": "",
			},
		},
		{
			name: "正在执行同步",
			status: map[string]interface{}{
				"is_running":        true,
				"current_task_type": "strm",
			},
			want: true,
		},
		{
			name: "状态字段损坏",
			status: map[string]interface{}{
				"is_running":        true,
				"current_task_type": 1,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := syncQueueExecuting(test.status); got != test.want {
				t.Fatalf("syncQueueExecuting() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestRunningEmbySyncCountIncludesMediaLibraryRefresh(t *testing.T) {
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	originalDB := db.Db
	db.Db = testDB
	t.Cleanup(func() { db.Db = originalDB })
	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.EmbyLibraryRefreshTask{}); err != nil {
		t.Fatalf("迁移 Emby 测试表失败: %v", err)
	}

	if err := db.Db.Create(&models.EmbyLibraryRefreshTask{
		TaskKey: "library:refreshing",
		Status:  models.EmbyLibraryRefreshStatusRefreshing,
	}).Error; err != nil {
		t.Fatalf("创建运行中的 Emby 媒体库刷新任务失败: %v", err)
	}

	if got := runningEmbySyncCount(); got != 1 {
		t.Fatalf("runningEmbySyncCount() = %d，期望 1", got)
	}
}
