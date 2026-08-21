package models

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

const (
	requestStatQueueSize     = 2048
	requestStatBatchSize     = 32
	requestStatFlushInterval = 500 * time.Millisecond
)

type requestStatBatchSaver func([]*RequestStat) error

// RequestStatWriter 将 115 请求统计放入有界队列，由单个 worker 批量写入数据库。
// 统计数据用于监控展示，队列满时允许丢弃，不能阻塞 115 请求 worker。
type RequestStatWriter struct {
	queue         chan *RequestStat
	batchSize     int
	flushInterval time.Duration
	saveBatch     requestStatBatchSaver
	stop          chan struct{}
	closed        atomic.Bool
	dropped       atomic.Uint64
	closeOnce     sync.Once
	enqueueMu     sync.RWMutex
	workerWG      sync.WaitGroup
}

// NewRequestStatWriter 创建默认配置的请求统计写入器。
func NewRequestStatWriter() *RequestStatWriter {
	return newRequestStatWriter(
		requestStatQueueSize,
		requestStatBatchSize,
		requestStatFlushInterval,
		saveRequestStatBatch,
	)
}

func newRequestStatWriter(queueSize, batchSize int, flushInterval time.Duration, saveBatch requestStatBatchSaver) *RequestStatWriter {
	if queueSize <= 0 {
		queueSize = 1
	}
	if batchSize <= 0 {
		batchSize = 1
	}
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	if saveBatch == nil {
		saveBatch = saveRequestStatBatch
	}

	writer := &RequestStatWriter{
		queue:         make(chan *RequestStat, queueSize),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		saveBatch:     saveBatch,
		stop:          make(chan struct{}),
	}
	writer.workerWG.Add(1)
	go writer.run()
	return writer
}

// Enqueue 非阻塞地提交一条请求统计，方法签名与 v115open.RequestStatSaver 兼容。
func (w *RequestStatWriter) Enqueue(requestTime int64, url, method string, duration int64, isThrottled bool) {
	if w == nil {
		return
	}
	w.enqueueMu.RLock()
	defer w.enqueueMu.RUnlock()
	if w.closed.Load() {
		return
	}

	stat := &RequestStat{
		RequestTime: requestTime,
		URL:         url,
		Method:      method,
		Duration:    duration,
		IsThrottled: isThrottled,
		AccountID:   0, // 可以后续扩展传入账号 ID
	}
	select {
	case w.queue <- stat:
	default:
		w.dropped.Add(1)
	}
}

// DroppedCount 返回队列已丢弃的统计数量，供关闭时日志和运行时观测使用。
func (w *RequestStatWriter) DroppedCount() uint64 {
	if w == nil {
		return 0
	}
	return w.dropped.Load()
}

// Close 停止 worker，并在退出前尽量写完队列中已经接收的统计。
func (w *RequestStatWriter) Close() {
	if w == nil {
		return
	}
	w.closeOnce.Do(func() {
		w.enqueueMu.Lock()
		w.closed.Store(true)
		w.enqueueMu.Unlock()
		close(w.stop)
		w.workerWG.Wait()

		if dropped := w.DroppedCount(); dropped > 0 && helpers.V115Log != nil {
			helpers.V115Log.Warnf("115 请求统计队列已丢弃 %d 条记录", dropped)
		}
	})
}

func (w *RequestStatWriter) run() {
	defer w.workerWG.Done()

	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()

	batch := make([]*RequestStat, 0, w.batchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		pending := batch
		batch = make([]*RequestStat, 0, w.batchSize)
		if err := w.saveBatch(pending); err != nil && helpers.V115Log != nil {
			helpers.V115Log.Errorf("批量写入 115 请求统计失败（%d 条）：%v", len(pending), err)
		}
	}
	drain := func() {
		for {
			select {
			case stat := <-w.queue:
				batch = append(batch, stat)
				if len(batch) >= w.batchSize {
					flush()
				}
			default:
				flush()
				return
			}
		}
	}

	for {
		select {
		case stat := <-w.queue:
			batch = append(batch, stat)
			if len(batch) >= w.batchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-w.stop:
			drain()
			return
		}
	}
}

func saveRequestStatBatch(stats []*RequestStat) error {
	if db.Db == nil {
		return fmt.Errorf("数据库尚未初始化")
	}
	return db.Db.CreateInBatches(stats, len(stats)).Error
}
