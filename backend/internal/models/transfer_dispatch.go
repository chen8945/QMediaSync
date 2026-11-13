package models

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// transferDispatch 仅共用传输的内存名额与生命周期，候选和持久化仍由各队列负责。
type transferDispatch[T any] struct {
	mutex         sync.RWMutex
	numWorkers    int
	active        int
	reserved      map[uint]struct{}
	running       bool
	wantRunning   bool
	suspended     int
	closed        bool
	generation    context.Context
	cancel        context.CancelFunc
	execution     context.Context
	cancelExecute context.CancelFunc
	wake          chan struct{}
	workers       sync.WaitGroup
	schedulers    sync.WaitGroup
	shutdownDone  chan struct{}
	interval      time.Duration
	id            func(T) uint
	candidates    func(context.Context, int, []uint) ([]T, error)
	prepare       func(context.Context) error
	run           func(context.Context, T)
	changed       func(bool)
}

func newTransferDispatch[T any](limit int, interval time.Duration, id func(T) uint,
	candidates func(context.Context, int, []uint) ([]T, error), run func(context.Context, T), changed func(bool),
) *transferDispatch[T] {
	ctx, cancel := context.WithCancel(context.Background())
	return &transferDispatch[T]{
		numWorkers: limit, reserved: make(map[uint]struct{}), execution: ctx, cancelExecute: cancel,
		wake: make(chan struct{}, 1), interval: interval, id: id, candidates: candidates, run: run, changed: changed,
	}
}

func (dispatch *transferDispatch[T]) signal() {
	select {
	case dispatch.wake <- struct{}{}:
	default:
	}
}

// reconcileLocked 只交换准入代次；取消和状态通知由调用方在锁外完成。
func (dispatch *transferDispatch[T]) reconcileLocked() (context.CancelFunc, bool) {
	running := dispatch.wantRunning && dispatch.suspended == 0 && !dispatch.closed
	if running == dispatch.running {
		return nil, false
	}
	dispatch.running = running
	if !running {
		cancel := dispatch.cancel
		dispatch.generation, dispatch.cancel = nil, nil
		return cancel, true
	}
	ctx, cancel := context.WithCancel(dispatch.execution)
	dispatch.generation, dispatch.cancel = ctx, cancel
	dispatch.schedulers.Add(1)
	go dispatch.schedule(ctx)
	return nil, true
}

func (dispatch *transferDispatch[T]) notify(cancel context.CancelFunc, changed bool) {
	if cancel != nil {
		cancel()
	}
	if changed && dispatch.changed != nil {
		dispatch.changed(dispatch.IsRunning())
	}
}

func (dispatch *transferDispatch[T]) Start() {
	dispatch.mutex.Lock()
	dispatch.wantRunning = true
	cancel, changed := dispatch.reconcileLocked()
	dispatch.mutex.Unlock()
	dispatch.notify(cancel, changed)
}

// Stop 只暂停新准入，不等待慢查询，不取消已经接受的传输。
func (dispatch *transferDispatch[T]) Stop() {
	dispatch.mutex.Lock()
	dispatch.wantRunning = false
	cancel, changed := dispatch.reconcileLocked()
	dispatch.mutex.Unlock()
	dispatch.notify(cancel, changed)
}

// Suspend 暂时暂停，最后一份释放只恢复用户仍希望运行的队列。
func (dispatch *transferDispatch[T]) Suspend() func() {
	dispatch.mutex.Lock()
	dispatch.suspended++
	cancel, changed := dispatch.reconcileLocked()
	dispatch.mutex.Unlock()
	dispatch.notify(cancel, changed)
	return sync.OnceFunc(func() {
		dispatch.mutex.Lock()
		dispatch.suspended--
		cancel, changed := dispatch.reconcileLocked()
		dispatch.mutex.Unlock()
		dispatch.notify(cancel, changed)
	})
}

func (dispatch *transferDispatch[T]) UpdateConcurrency(limit int) {
	dispatch.mutex.Lock()
	dispatch.numWorkers = limit
	dispatch.mutex.Unlock()
	dispatch.signal()
}

func (dispatch *transferDispatch[T]) GetConcurrency() int {
	dispatch.mutex.RLock()
	defer dispatch.mutex.RUnlock()
	return dispatch.numWorkers
}

func (dispatch *transferDispatch[T]) IsRunning() bool {
	dispatch.mutex.RLock()
	defer dispatch.mutex.RUnlock()
	return dispatch.running
}

func (dispatch *transferDispatch[T]) schedule(ctx context.Context) {
	defer dispatch.schedulers.Done()
	ticker := time.NewTicker(dispatch.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-dispatch.wake:
		}
		dispatch.dispatch(ctx)
	}
}

func (dispatch *transferDispatch[T]) dispatch(ctx context.Context) {
	dispatch.mutex.RLock()
	if !dispatch.running || dispatch.generation != ctx {
		dispatch.mutex.RUnlock()
		return
	}
	limit := dispatch.numWorkers - len(dispatch.reserved)
	excluded := make([]uint, 0, len(dispatch.reserved))
	for id := range dispatch.reserved {
		excluded = append(excluded, id)
	}
	dispatch.mutex.RUnlock()
	if limit <= 0 {
		return
	}
	tasks, err := dispatch.candidates(ctx, limit, excluded)
	if err != nil {
		return // 各领域查询记录错误；下一次轮询可重试。
	}
	for _, task := range tasks {
		id := dispatch.id(task)
		dispatch.mutex.Lock()
		_, reserved := dispatch.reserved[id]
		if !dispatch.running || dispatch.generation != ctx || len(dispatch.reserved) >= dispatch.numWorkers {
			dispatch.mutex.Unlock()
			return
		}
		if reserved {
			dispatch.mutex.Unlock()
			continue
		}
		dispatch.reserved[id] = struct{}{}
		dispatch.workers.Add(1)
		dispatch.mutex.Unlock()
		go dispatch.execute(ctx, task, id)
	}
}

func (dispatch *transferDispatch[T]) execute(ctx context.Context, task T, id uint) {
	accepted := false
	defer func() {
		dispatch.mutex.Lock()
		delete(dispatch.reserved, id)
		if accepted {
			dispatch.active--
		}
		dispatch.mutex.Unlock()
		dispatch.workers.Done()
		dispatch.signal()
	}()
	if dispatch.prepare != nil {
		if err := dispatch.prepare(ctx); err != nil {
			return
		}
	}
	dispatch.mutex.Lock()
	// 预留或限速等待不等于在途；这里按最新上限接受执行。
	if !dispatch.running || dispatch.generation != ctx || ctx.Err() != nil || dispatch.active >= dispatch.numWorkers {
		dispatch.mutex.Unlock()
		return
	}
	dispatch.active++
	accepted = true
	dispatch.mutex.Unlock()
	dispatch.run(dispatch.execution, task)
}

// Shutdown 终止准入、取消真实执行，并等待查询、准备及完整收尾；超时不会伪报退出。
func (dispatch *transferDispatch[T]) Shutdown(ctx context.Context) error {
	dispatch.mutex.Lock()
	first := !dispatch.closed
	dispatch.closed = true
	cancel, changed := dispatch.reconcileLocked()
	if first {
		dispatch.shutdownDone = make(chan struct{})
	}
	done := dispatch.shutdownDone
	dispatch.mutex.Unlock()
	dispatch.notify(cancel, changed)
	if first {
		dispatch.cancelExecute()
		go func() {
			dispatch.schedulers.Wait()
			dispatch.workers.Wait()
			close(done)
		}()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("传输队列尚未退出：%w", ctx.Err())
	}
}
