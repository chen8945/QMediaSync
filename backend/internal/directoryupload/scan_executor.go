package directoryupload

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/taskgate"
)

const defaultScanConcurrency = 2

type scanRequest struct {
	rule *models.DirectoryUploadRule
	root string
	scan scanFunc
}

type scanFunc func(context.Context, *models.DirectoryUploadRule, string) (int, error)

type scanEntry struct {
	key     string
	request scanRequest
	ctx     context.Context
	running bool
}

type scanExecutor struct {
	sem      chan struct{}
	wg       sync.WaitGroup
	mu       sync.Mutex
	inflight map[string]*scanEntry
	queue    []*scanEntry
	running  bool
	scan     scanFunc
}

func newScanExecutor(scan scanFunc) *scanExecutor {
	return newScanExecutorWithScanFunc(defaultScanConcurrency, scan)
}

func newScanExecutorWithScanFunc(concurrency int, scan scanFunc) *scanExecutor {
	if concurrency <= 0 {
		concurrency = defaultScanConcurrency
	}
	if scan == nil {
		scan = func(context.Context, *models.DirectoryUploadRule, string) (int, error) {
			return 0, nil
		}
	}
	return &scanExecutor{
		sem:      make(chan struct{}, concurrency),
		inflight: make(map[string]*scanEntry),
		scan:     scan,
	}
}

func (executor *scanExecutor) Enqueue(ctx context.Context, request scanRequest) {
	if executor == nil || ctx == nil || ctx.Err() != nil || taskgate.IsBlocked() {
		return
	}
	key, root, ok := request.scanKey()
	if !ok {
		return
	}

	executor.mu.Lock()
	entry := &scanEntry{
		key:     key,
		request: scanRequest{rule: request.rule, root: root, scan: request.scan},
		ctx:     ctx,
	}
	if existing, exists := executor.inflight[key]; exists {
		if existing.ctx.Err() == nil {
			executor.mu.Unlock()
			return
		}
		executor.inflight[key] = entry
	} else {
		executor.inflight[key] = entry
	}
	executor.queue = append(executor.queue, entry)
	shouldStart := !executor.running
	if shouldStart {
		executor.running = true
		executor.wg.Add(1)
	}
	executor.mu.Unlock()
	if shouldStart {
		go executor.run()
	}
}

func (executor *scanExecutor) run() {
	defer executor.wg.Done()
	for {
		request, ok := executor.next()
		if !ok {
			return
		}
		executor.start(request)
	}
}

func (executor *scanExecutor) next() (*scanEntry, bool) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if len(executor.queue) == 0 {
		executor.running = false
		return nil, false
	}
	request := executor.queue[0]
	executor.queue = executor.queue[1:]
	return request, true
}

func (executor *scanExecutor) start(entry *scanEntry) {
	if entry == nil {
		return
	}
	select {
	case executor.sem <- struct{}{}:
	case <-entry.ctx.Done():
		executor.releaseInflight(entry)
		return
	}
	if err := entry.ctx.Err(); err != nil {
		executor.releaseSemaphore()
		executor.releaseInflight(entry)
		return
	}
	if !executor.markRunning(entry) {
		executor.releaseSemaphore()
		return
	}

	executor.wg.Add(1)
	go func() {
		defer executor.wg.Done()
		defer executor.releaseSemaphore()
		defer executor.releaseInflight(entry)
		scan := entry.request.scan
		if scan == nil {
			scan = executor.scan
		}
		if _, err := scan(entry.ctx, entry.request.rule, entry.request.root); err != nil &&
			entry.ctx.Err() == nil &&
			!errors.Is(err, context.Canceled) {
			helpers.AppLogger.Warnf("[目录上传] 规则 %d 扫描目录 %s 失败：%v", entry.request.rule.ID, entry.request.root, err)
		}
	}()
}

func (executor *scanExecutor) Wait() {
	if executor == nil {
		return
	}
	executor.wg.Wait()
}

func (executor *scanExecutor) markRunning(entry *scanEntry) bool {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.inflight[entry.key] != entry {
		return false
	}
	if err := entry.ctx.Err(); err != nil {
		delete(executor.inflight, entry.key)
		return false
	}
	entry.running = true
	return true
}

func (executor *scanExecutor) releaseSemaphore() {
	<-executor.sem
}

func (executor *scanExecutor) releaseInflight(entry *scanEntry) {
	if entry == nil {
		return
	}
	executor.mu.Lock()
	if executor.inflight[entry.key] == entry {
		delete(executor.inflight, entry.key)
	}
	executor.mu.Unlock()
}

func (request scanRequest) scanKey() (string, string, bool) {
	if request.rule == nil || request.root == "" {
		return "", "", false
	}
	root := filepath.Clean(request.root)
	return fmt.Sprintf("%d:%s", request.rule.ID, root), root, true
}
