package realtime

import (
	"sync"
	"time"
)

// 事件类型常量。
const (
	EventScraperTaskStart     = "scraper_task_start"
	EventScraperTaskComplete  = "scraper_task_complete"
	EventScraperItemComplete  = "scraper_item_complete"
	EventStrmSyncTaskStart    = "strm_sync_task_start"
	EventStrmSyncTaskQueued   = "strm_sync_task_queued"
	EventStrmSyncTaskComplete = "strm_sync_task_complete"
	EventSyncTaskCreated      = "sync_task_created"
	EventSyncTaskUpdated      = "sync_task_updated"
	EventSyncTaskDeleted      = "sync_task_deleted"

	EventUploadQueueStatusChanged   = "upload_queue_status_changed"
	EventDownloadQueueStatusChanged = "download_queue_status_changed"
	EventUploadQueueChanged         = "upload_queue_changed"
	EventDownloadQueueChanged       = "download_queue_changed"
)

// RealtimeEvent 是全局业务事件载荷。
type RealtimeEvent struct {
	EventType string    `json:"event_type"`
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data"`
}

// QueueStatusPayload 是队列运行状态变更事件数据。
type QueueStatusPayload struct {
	Running bool `json:"running"`
}

// QueueChangedPayload 是队列列表变更事件数据。
type QueueChangedPayload struct {
	TaskID              uint    `json:"task_id,omitempty"`
	Status              int     `json:"status,omitempty"`
	Source              string  `json:"source,omitempty"`
	Reason              string  `json:"reason,omitempty"`
	UploadedBytes       int64   `json:"uploaded_bytes,omitempty"`
	FileSize            int64   `json:"file_size,omitempty"`
	ProgressPercent     float64 `json:"progress_percent,omitempty"`
	UploadSpeedBytes    int64   `json:"upload_speed_bytes,omitempty"`
	UploadPhase         string  `json:"upload_phase,omitempty"`
	UploadResult        string  `json:"upload_result,omitempty"`
	ResumeState         string  `json:"resume_state,omitempty"`
	RapidWaitUntil      int64   `json:"rapid_wait_until,omitempty"`
	TotalParts          int     `json:"total_parts,omitempty"`
	UploadedParts       int     `json:"uploaded_parts,omitempty"`
	SourceCleanupStatus string  `json:"source_cleanup_status,omitempty"`
	SourceCleanupError  *string `json:"source_cleanup_error,omitempty"`
	SourceDeletedAt     int64   `json:"source_deleted_at,omitempty"`
}

// EventHub 按有界 channel 向全局事件订阅者分发事件。
type EventHub struct {
	mu          sync.Mutex
	subscribers map[chan RealtimeEvent]struct{}
}

// GlobalEventHub 是应用的全局事件中心。
var GlobalEventHub = NewEventHub()

// NewEventHub 创建事件中心。
func NewEventHub() *EventHub {
	return &EventHub{subscribers: make(map[chan RealtimeEvent]struct{})}
}

// Subscribe 订阅全局事件，并返回幂等注销函数。
func (h *EventHub) Subscribe(buffer int) (<-chan RealtimeEvent, func()) {
	if buffer <= 0 {
		buffer = 1
	}
	ch := make(chan RealtimeEvent, buffer)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if _, ok := h.subscribers[ch]; ok {
				delete(h.subscribers, ch)
				close(ch)
			}
			h.mu.Unlock()
		})
	}
}

// Publish 向所有订阅者发布事件；慢订阅者会直接被关闭。
func (h *EventHub) Publish(event RealtimeEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.subscribers {
		select {
		case ch <- event:
		default:
			delete(h.subscribers, ch)
			close(ch)
		}
	}
}

// ClientCount 返回当前订阅数。
func (h *EventHub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subscribers)
}

// BroadcastEvent 广播全局事件。
func BroadcastEvent(eventType string, data any) {
	if GlobalEventHub == nil {
		return
	}
	GlobalEventHub.Publish(RealtimeEvent{EventType: eventType, Timestamp: time.Now(), Data: data})
}

// TryBroadcastEvent 尝试广播全局事件。SSE Hub 本身不会阻塞发布方。
func TryBroadcastEvent(eventType string, data any) bool {
	if GlobalEventHub == nil {
		return false
	}
	BroadcastEvent(eventType, data)
	return true
}

// BroadcastQueueStatusChanged 广播队列运行状态变更。
func BroadcastQueueStatusChanged(eventType string, running bool) {
	BroadcastEvent(eventType, QueueStatusPayload{Running: running})
}

// BroadcastQueueChanged 广播队列列表变更。
func BroadcastQueueChanged(eventType string, payload QueueChangedPayload) {
	BroadcastEvent(eventType, payload)
}
