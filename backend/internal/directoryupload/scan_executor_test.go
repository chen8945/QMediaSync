package directoryupload

import (
	"context"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/models"
	"qmediasync/internal/taskgate"
)

func TestScanExecutorDoesNotStartNewScanWhileTaskAdmissionBlocked(t *testing.T) {
	taskgate.BlockNewTasks()
	t.Cleanup(taskgate.AllowNewTasks)

	executor := newScanExecutorWithScanFunc(1, func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
		t.Fatal("任务准入关闭时不应执行目录扫描")
		return 0, nil
	})
	executor.Enqueue(context.Background(), scanRequest{
		rule: &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 99}},
		root: t.TempDir(),
	})

	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.running || len(executor.queue) != 0 || len(executor.inflight) != 0 {
		t.Fatalf("准入关闭时目录扫描被接受：running=%t queue=%d inflight=%d", executor.running, len(executor.queue), len(executor.inflight))
	}
}

func TestScanExecutorMergesSameRuleAndRoot(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 7}}
	root := filepath.Join(t.TempDir(), "Show")
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32

	executor := newScanExecutorWithScanFunc(1, func(ctx context.Context, _ *models.DirectoryUploadRule, _ string) (int, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		close(finished)
		return 0, nil
	})

	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: root})
	waitForSignal(t, started, "等待首次扫描启动")
	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: root})
	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: filepath.Join(root, ".")})

	if got := calls.Load(); got != 1 {
		t.Fatalf("scan calls=%d，期望同一 rule/root 只启动 1 次", got)
	}
	close(release)
	waitForSignal(t, finished, "等待扫描结束")
	if got := calls.Load(); got != 1 {
		t.Fatalf("scan calls=%d，期望重复提交不补跑", got)
	}
}

func TestScanExecutorSkipsCanceledContext(t *testing.T) {
	var calls atomic.Int32
	executor := newScanExecutorWithScanFunc(1, func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
		calls.Add(1)
		return 0, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	executor.Enqueue(ctx, scanRequest{
		rule: &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 8}},
		root: t.TempDir(),
	})

	if got := calls.Load(); got != 0 {
		t.Fatalf("scan calls=%d，期望 0", got)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.running || len(executor.queue) != 0 || len(executor.inflight) != 0 {
		t.Fatalf("ctx 已取消时不应留下执行器状态: running=%v queue=%d inflight=%d", executor.running, len(executor.queue), len(executor.inflight))
	}
}

func TestScanExecutorReplacesCanceledQueuedRequest(t *testing.T) {
	oldMaxProcs := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldMaxProcs)

	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 11}}
	baseDir := t.TempDir()
	rootA := filepath.Join(baseDir, "A")
	rootK := filepath.Join(baseDir, "K")
	started := make(chan string, 2)
	releaseA := make(chan struct{})
	finished := make(chan string, 2)

	executor := newScanExecutorWithScanFunc(1, func(ctx context.Context, _ *models.DirectoryUploadRule, root string) (int, error) {
		started <- root
		if root == rootA {
			select {
			case <-releaseA:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
		finished <- root
		return 0, nil
	})

	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: rootA})
	if got := waitForScanRoot(t, started); got != rootA {
		t.Fatalf("首次扫描 root=%s，期望 %s", got, rootA)
	}

	oldCtx, oldCancel := context.WithCancel(context.Background())
	executor.Enqueue(oldCtx, scanRequest{rule: rule, root: rootK})
	waitForQueuedScanToLeaveQueue(t, executor, rootK)

	executor.mu.Lock()
	newEnqueueReady := make(chan struct{})
	newEnqueueReturned := make(chan struct{})
	go func() {
		close(newEnqueueReady)
		executor.Enqueue(context.Background(), scanRequest{rule: rule, root: rootK})
		close(newEnqueueReturned)
	}()
	waitForSignal(t, newEnqueueReady, "等待新扫描提交开始")
	oldCancel()
	executor.mu.Unlock()
	waitForSignal(t, newEnqueueReturned, "等待新扫描提交返回")

	close(releaseA)
	if got := waitForScanRoot(t, finished); got != rootA {
		t.Fatalf("首次扫描结束 root=%s，期望 %s", got, rootA)
	}
	if got := waitForScanRoot(t, started); got != rootK {
		t.Fatalf("取消旧排队项后新扫描 root=%s，期望 %s", got, rootK)
	}
	if got := waitForScanRoot(t, finished); got != rootK {
		t.Fatalf("新扫描结束 root=%s，期望 %s", got, rootK)
	}
}

func TestScanExecutorReplacesCanceledRunningRequest(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 12}}
	root := filepath.Join(t.TempDir(), "K")
	started := make(chan string, 2)
	finished := make(chan string, 2)
	releaseOld := make(chan struct{})
	oldCtx, oldCancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	executor := newScanExecutorWithScanFunc(1, func(ctx context.Context, _ *models.DirectoryUploadRule, root string) (int, error) {
		call := calls.Add(1)
		started <- root
		if call == 1 {
			<-releaseOld
			return 0, ctx.Err()
		}
		finished <- root
		return 0, nil
	})

	executor.Enqueue(oldCtx, scanRequest{rule: rule, root: root})
	if got := waitForScanRoot(t, started); got != root {
		t.Fatalf("旧扫描 root=%s，期望 %s", got, root)
	}

	oldCancel()
	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: root})
	close(releaseOld)

	if got := waitForScanRoot(t, started); got != root {
		t.Fatalf("新扫描 root=%s，期望 %s", got, root)
	}
	if got := waitForScanRoot(t, finished); got != root {
		t.Fatalf("新扫描结束 root=%s，期望 %s", got, root)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("scan calls=%d，期望旧 running ctx 取消后新请求补跑", got)
	}
}

func TestScanExecutorUsesRequestScanFunc(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 13}}
	root := t.TempDir()
	started := make(chan string, 1)
	var defaultCalls atomic.Int32

	executor := newScanExecutorWithScanFunc(1, func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
		defaultCalls.Add(1)
		started <- "default"
		return 0, nil
	})
	executor.Enqueue(context.Background(), scanRequest{
		rule: rule,
		root: root,
		scan: func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
			started <- "request"
			return 0, nil
		},
	})

	if got := waitForScanRoot(t, started); got != "request" {
		t.Fatalf("scan func=%s，期望执行请求自带扫描函数", got)
	}
	if got := defaultCalls.Load(); got != 0 {
		t.Fatalf("default scan calls=%d，期望 request scan 存在时不调用默认扫描", got)
	}
}

func TestScanExecutorRequestScanFuncDoesNotReplaceDefaultForSameKey(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 15}}
	root := t.TempDir()
	started := make(chan string, 2)

	executor := newScanExecutorWithScanFunc(1, func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
		started <- "default"
		return 0, nil
	})
	executor.Enqueue(context.Background(), scanRequest{
		rule: rule,
		root: root,
		scan: func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
			started <- "request"
			return 0, nil
		},
	})
	if got := waitForScanRoot(t, started); got != "request" {
		t.Fatalf("首次 scan func=%s，期望 request", got)
	}
	waitForInflightToClear(t, executor, rule, root)

	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: root})
	if got := waitForScanRoot(t, started); got != "default" {
		t.Fatalf("同 key 后续默认扫描执行=%s，期望 default", got)
	}
}

func TestScanExecutorCanceledQueuedReplacementUsesNewRequestScanFunc(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 14}}
	baseDir := t.TempDir()
	rootA := filepath.Join(baseDir, "A")
	rootK := filepath.Join(baseDir, "K")
	started := make(chan string, 3)
	releaseA := make(chan struct{})

	executor := newScanExecutorWithScanFunc(1, func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
		started <- "default"
		return 0, nil
	})
	executor.Enqueue(context.Background(), scanRequest{
		rule: rule,
		root: rootA,
		scan: func(ctx context.Context, _ *models.DirectoryUploadRule, _ string) (int, error) {
			started <- "blocker"
			select {
			case <-releaseA:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
			return 0, nil
		},
	})
	if got := waitForScanRoot(t, started); got != "blocker" {
		t.Fatalf("首次扫描=%s，期望 blocker", got)
	}

	oldCtx, oldCancel := context.WithCancel(context.Background())
	executor.Enqueue(oldCtx, scanRequest{
		rule: rule,
		root: rootK,
		scan: func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
			started <- "old"
			return 0, nil
		},
	})
	oldCancel()
	executor.Enqueue(context.Background(), scanRequest{
		rule: rule,
		root: rootK,
		scan: func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
			started <- "new"
			return 0, nil
		},
	})
	close(releaseA)

	if got := waitForScanRoot(t, started); got != "new" {
		t.Fatalf("取消旧排队项后执行扫描=%s，期望使用新请求扫描函数", got)
	}
}

func TestScanExecutorCanceledRunningReplacementUsesNewRequestScanFunc(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 16}}
	root := filepath.Join(t.TempDir(), "K")
	started := make(chan string, 2)
	releaseOld := make(chan struct{})
	oldCtx, oldCancel := context.WithCancel(context.Background())

	executor := newScanExecutorWithScanFunc(1, func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
		started <- "default"
		return 0, nil
	})
	executor.Enqueue(oldCtx, scanRequest{
		rule: rule,
		root: root,
		scan: func(ctx context.Context, _ *models.DirectoryUploadRule, _ string) (int, error) {
			started <- "old"
			select {
			case <-releaseOld:
			case <-ctx.Done():
				return 0, ctx.Err()
			}
			return 0, nil
		},
	})
	if got := waitForScanRoot(t, started); got != "old" {
		t.Fatalf("旧 running 扫描=%s，期望 old", got)
	}

	oldCancel()
	executor.Enqueue(context.Background(), scanRequest{
		rule: rule,
		root: root,
		scan: func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
			started <- "new"
			return 0, nil
		},
	})
	close(releaseOld)

	if got := waitForScanRoot(t, started); got != "new" {
		t.Fatalf("取消旧 running 后执行扫描=%s，期望使用新请求扫描函数", got)
	}
}

func TestScanExecutorReleasesInflightAfterScan(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 9}}
	root := t.TempDir()
	started := make(chan int, 2)
	release := make(chan struct{})
	finished := make(chan int, 2)
	var calls atomic.Int32

	executor := newScanExecutorWithScanFunc(1, func(ctx context.Context, _ *models.DirectoryUploadRule, _ string) (int, error) {
		call := int(calls.Add(1))
		started <- call
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		finished <- call
		return 0, nil
	})

	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: root})
	if got := waitForScanCall(t, started); got != 1 {
		t.Fatalf("首次扫描序号=%d，期望 1", got)
	}
	release <- struct{}{}
	if got := waitForScanCall(t, finished); got != 1 {
		t.Fatalf("首次扫描结束序号=%d，期望 1", got)
	}

	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: root})
	if got := waitForScanCall(t, started); got != 2 {
		t.Fatalf("第二次扫描序号=%d，期望 inflight 释放后可再次提交", got)
	}
	release <- struct{}{}
}

func TestScanExecutorLimitsConcurrentScans(t *testing.T) {
	var active atomic.Int32
	var maxActive atomic.Int32
	started := make(chan string, 2)
	release := make(chan struct{})

	executor := newScanExecutorWithScanFunc(1, func(ctx context.Context, _ *models.DirectoryUploadRule, root string) (int, error) {
		current := active.Add(1)
		for {
			max := maxActive.Load()
			if current <= max || maxActive.CompareAndSwap(max, current) {
				break
			}
		}
		started <- root
		select {
		case <-release:
		case <-ctx.Done():
			active.Add(-1)
			return 0, ctx.Err()
		}
		active.Add(-1)
		return 0, nil
	})

	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 10}}
	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: filepath.Join(t.TempDir(), "A")})
	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: filepath.Join(t.TempDir(), "B")})

	_ = waitForScanRoot(t, started)
	release <- struct{}{}
	_ = waitForScanRoot(t, started)
	release <- struct{}{}

	if got := maxActive.Load(); got > 1 {
		t.Fatalf("max active scans=%d，期望不超过 1", got)
	}
}

func TestScanExecutorWaitBlocksUntilRunningScanFinishes(t *testing.T) {
	rule := &models.DirectoryUploadRule{BaseModel: models.BaseModel{ID: 17}}
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})

	executor := newScanExecutorWithScanFunc(1, func(ctx context.Context, _ *models.DirectoryUploadRule, _ string) (int, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		close(finished)
		return 0, nil
	})

	executor.Enqueue(context.Background(), scanRequest{rule: rule, root: t.TempDir()})
	waitForSignal(t, started, "等待扫描启动")

	waitReturned := make(chan struct{})
	go func() {
		executor.Wait()
		close(waitReturned)
	}()

	select {
	case <-waitReturned:
		t.Fatal("扫描仍在运行时 Wait 不应返回")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	waitForSignal(t, finished, "等待扫描结束")
	waitForSignal(t, waitReturned, "等待 Wait 返回")
}

func waitForInflightToClear(t *testing.T, executor *scanExecutor, rule *models.DirectoryUploadRule, root string) {
	t.Helper()
	key, _, ok := (scanRequest{rule: rule, root: root}).scanKey()
	if !ok {
		t.Fatal("测试 scan key 无效")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		executor.mu.Lock()
		_, exists := executor.inflight[key]
		executor.mu.Unlock()
		if !exists {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待 inflight %s 清理超时", key)
}

func waitForSignal(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func waitForScanCall(t *testing.T, ch <-chan int) int {
	t.Helper()
	select {
	case call := <-ch:
		return call
	case <-time.After(time.Second):
		t.Fatal("等待扫描调用超时")
		return 0
	}
}

func waitForScanRoot(t *testing.T, ch <-chan string) string {
	t.Helper()
	select {
	case root := <-ch:
		return root
	case <-time.After(time.Second):
		t.Fatal("等待扫描启动超时")
		return ""
	}
}

func waitForQueuedScanToLeaveQueue(t *testing.T, executor *scanExecutor, root string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		executor.mu.Lock()
		found := false
		for _, request := range executor.queue {
			if request.request.root == root {
				found = true
				break
			}
		}
		executor.mu.Unlock()
		if !found {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("等待扫描 %s 离开队列超时", root)
}
