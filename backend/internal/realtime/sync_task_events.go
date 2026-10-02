package realtime

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

const syncTaskReplayLimit = 64

// SyncLedgerStatus 表示生成结束后的账本更新结果；NULL 保留历史未知语义。
type SyncLedgerStatus string

const (
	SyncLedgerNotRequired SyncLedgerStatus = "not_required"
	SyncLedgerPending     SyncLedgerStatus = "pending"
	SyncLedgerRunning     SyncLedgerStatus = "running"
	SyncLedgerCompleted   SyncLedgerStatus = "completed"
	SyncLedgerFailed      SyncLedgerStatus = "failed"
	SyncLedgerInterrupted SyncLedgerStatus = "interrupted"
)

// IsActive 判断账本是否仍可能产生后台结果。
func (status SyncLedgerStatus) IsActive() bool {
	return status == SyncLedgerPending || status == SyncLedgerRunning
}

// SyncScanFailure 描述可定位的文件失败或扫描不完整目录。
type SyncScanFailure struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	FileID string `json:"file_id,omitempty"`
	Reason string `json:"reason"`
}

// SyncScanResult 保存本次实际处理数量及清理依据；历史记录没有该结果。
type SyncScanResult struct {
	SucceededFiles int               `json:"succeeded_files"`
	FailedFiles    int               `json:"failed_files"`
	SkippedFiles   int               `json:"skipped_files"`
	Failures       []SyncScanFailure `json:"failures"`
	CleanupStatus  string            `json:"cleanup_status"`
	CleanupReason  string            `json:"cleanup_reason"`
}

// SyncTaskEventPayload 是同步任务结构化事件数据。
type SyncTaskEventPayload struct {
	LedgerStatus      *SyncLedgerStatus `json:"ledger_status"`
	LedgerFinishedAt  *int64            `json:"ledger_finished_at"`
	LedgerError       string            `json:"ledger_error"`
	ScanResult        *SyncScanResult   `json:"scan_result"`
	SyncID            uint              `json:"sync_id"`
	SyncPathID        uint              `json:"sync_path_id"`
	Status            int               `json:"status"`
	SubStatus         int               `json:"sub_status"`
	Total             int               `json:"total"`
	NewStrm           int               `json:"new_strm"`
	NewMeta           int               `json:"new_meta"`
	NewUpload         int               `json:"new_upload"`
	FinishAt          int64             `json:"finish_at"`
	NetFileStartAt    int64             `json:"net_file_start_at"`
	NetFileFinishAt   int64             `json:"net_file_finish_at"`
	LocalFileStartAt  int64             `json:"local_file_start_at"`
	LocalFileFinishAt int64             `json:"local_file_finish_at"`
	LogPath           string            `json:"log_path"`
	Sequence          uint64            `json:"sequence"`
	EventTime         int64             `json:"event_time"`
	CreatedAt         int64             `json:"created_at,omitempty"`
	UpdatedAt         int64             `json:"updated_at,omitempty"`
	LocalPath         string            `json:"local_path,omitempty"`
	RemotePath        string            `json:"remote_path,omitempty"`
	FailReason        string            `json:"fail_reason,omitempty"`
	Deleted           bool              `json:"deleted,omitempty"`
	ResyncReason      string            `json:"resync_reason,omitempty"`
}

// IsTerminal 等待生成和后台均结束；历史未记录后台字段仍按原终态结束订阅。
func (p SyncTaskEventPayload) IsTerminal() bool {
	return p.Deleted || (p.Status >= 2 && p.Status <= 6 && (p.LedgerStatus == nil || !p.LedgerStatus.IsActive()))
}

// TaskStreamEvent 是同步任务详情流使用的内部事件。
type TaskStreamEvent struct {
	EventType string
	Payload   SyncTaskEventPayload
	Terminal  bool
}

// SyncTaskHub 按同步任务分发状态事件并缓存运行中任务的最近 patch。
type SyncTaskHub struct {
	mu          sync.Mutex
	epoch       string
	subscribers map[uint]map[chan TaskStreamEvent]struct{}
	caches      map[uint][]TaskStreamEvent
	sequences   map[uint]uint64
}

// GlobalSyncTaskHub 是同步任务详情 stream 使用的事件 hub。
var GlobalSyncTaskHub = NewSyncTaskHub()

// NewSyncTaskHub 创建同步任务事件 hub。
func NewSyncTaskHub() *SyncTaskHub {
	return &SyncTaskHub{
		epoch:       newStreamEpoch(),
		subscribers: make(map[uint]map[chan TaskStreamEvent]struct{}),
		caches:      make(map[uint][]TaskStreamEvent),
		sequences:   make(map[uint]uint64),
	}
}

func newStreamEpoch() string {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err == nil {
		return hex.EncodeToString(bytes)
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

// Epoch 返回当前进程的同步任务 stream epoch。
func (h *SyncTaskHub) Epoch() string {
	return h.epoch
}

// EventID 返回指定 sequence 的 SSE Last-Event-ID。
func (h *SyncTaskHub) EventID(sequence uint64) string {
	if sequence == 0 {
		return ""
	}
	return h.epoch + ":" + strconv.FormatUint(sequence, 10)
}

// SubscribeFrom 原子地注册订阅并根据 Last-Event-ID 返回回放或 snapshot 水位线。
func (h *SyncTaskHub) SubscribeFrom(syncID uint, lastEventID string, buffer int) (<-chan TaskStreamEvent, []TaskStreamEvent, uint64, bool, func()) {
	if buffer <= 0 {
		buffer = 1
	}
	ch := make(chan TaskStreamEvent, buffer)

	h.mu.Lock()
	if h.subscribers[syncID] == nil {
		h.subscribers[syncID] = make(map[chan TaskStreamEvent]struct{})
	}
	h.subscribers[syncID][ch] = struct{}{}

	replay, replayed := h.replayLocked(syncID, lastEventID)
	snapshotSequence := uint64(0)
	if !replayed {
		snapshotSequence = h.sequences[syncID]
	}
	h.mu.Unlock()

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			h.mu.Lock()
			if subscribers, ok := h.subscribers[syncID]; ok {
				if _, exists := subscribers[ch]; exists {
					delete(subscribers, ch)
					close(ch)
				}
				if len(subscribers) == 0 {
					delete(h.subscribers, syncID)
				}
			}
			h.mu.Unlock()
		})
	}

	return ch, replay, snapshotSequence, replayed, unsubscribe
}

func (h *SyncTaskHub) replayLocked(syncID uint, lastEventID string) ([]TaskStreamEvent, bool) {
	sequence, ok := h.parseEventID(lastEventID)
	if !ok {
		return nil, false
	}

	cache := h.caches[syncID]
	if len(cache) == 0 {
		return nil, false
	}
	first := cache[0].Payload.Sequence
	last := cache[len(cache)-1].Payload.Sequence
	if sequence < first-1 || sequence > last {
		return nil, false
	}

	replay := make([]TaskStreamEvent, 0, len(cache))
	next := sequence + 1
	for _, event := range cache {
		if event.Payload.Sequence <= sequence {
			continue
		}
		if event.Payload.Sequence != next {
			return nil, false
		}
		replay = append(replay, event)
		next++
	}
	return replay, true
}

func (h *SyncTaskHub) parseEventID(value string) (uint64, bool) {
	if strings.Count(value, ":") != 1 {
		return 0, false
	}
	epoch, sequenceText, ok := strings.Cut(value, ":")
	if !ok || epoch != h.epoch || sequenceText == "" {
		return 0, false
	}
	sequence, err := strconv.ParseUint(sequenceText, 10, 64)
	if err != nil || sequence == 0 {
		return 0, false
	}
	return sequence, true
}

// PublishSyncTaskEvent 原子地分配 sequence 并发布同步任务事件。
func (h *SyncTaskHub) PublishSyncTaskEvent(eventType string, payload SyncTaskEventPayload) SyncTaskEventPayload {
	if payload.SyncID == 0 {
		return payload
	}

	h.mu.Lock()
	payload.Sequence = h.sequences[payload.SyncID] + 1
	h.publishLocked(TaskStreamEvent{
		EventType: eventType,
		Payload:   payload,
		Terminal:  payload.IsTerminal(),
	})
	h.mu.Unlock()

	return payload
}

// Publish 发布任务事件；慢订阅者会被关闭，终态事件会清理缓存与 sequence。
func (h *SyncTaskHub) Publish(event TaskStreamEvent) {
	if event.Payload.SyncID == 0 {
		return
	}

	h.mu.Lock()
	h.publishLocked(event)
	h.mu.Unlock()
}

func (h *SyncTaskHub) publishLocked(event TaskStreamEvent) {
	if event.Payload.Sequence > h.sequences[event.Payload.SyncID] {
		h.sequences[event.Payload.SyncID] = event.Payload.Sequence
	}
	for ch := range h.subscribers[event.Payload.SyncID] {
		select {
		case ch <- event:
		default:
			delete(h.subscribers[event.Payload.SyncID], ch)
			close(ch)
		}
	}
	if len(h.subscribers[event.Payload.SyncID]) == 0 {
		delete(h.subscribers, event.Payload.SyncID)
	}

	if event.Terminal {
		delete(h.caches, event.Payload.SyncID)
		delete(h.sequences, event.Payload.SyncID)
		return
	}

	cache := append(h.caches[event.Payload.SyncID], event)
	if len(cache) > syncTaskReplayLimit {
		cache = cache[len(cache)-syncTaskReplayLimit:]
	}
	h.caches[event.Payload.SyncID] = cache
}

// BroadcastSyncTaskEvent 发布同步任务结构化事件，同时兼容全局事件消费者。
func BroadcastSyncTaskEvent(eventType string, payload SyncTaskEventPayload) SyncTaskEventPayload {
	if payload.SyncID == 0 {
		return payload
	}
	if payload.EventTime == 0 {
		payload.EventTime = time.Now().Unix()
	}
	if GlobalSyncTaskHub != nil {
		payload = GlobalSyncTaskHub.PublishSyncTaskEvent(eventType, payload)
	}

	BroadcastEvent(eventType, payload)
	return payload
}
