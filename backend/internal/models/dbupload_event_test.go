package models

import (
	"encoding/json"
	"testing"

	"qmediasync/internal/realtime"
)

func TestPublishUploadQueueChangedIncludesSourceDeletedAt(t *testing.T) {
	events, unsubscribe := realtime.GlobalEventHub.Subscribe(1)
	defer unsubscribe()

	publishUploadQueueChanged(&DbUploadTask{
		BaseModel:           BaseModel{ID: 7},
		Source:              UploadSourceDirectoryMonitor,
		SourceCleanupStatus: UploadSourceCleanupStatusCompleted,
		SourceDeletedAt:     1_700_000_000,
	}, "source_cleanup_changed")

	event := <-events
	payload, ok := event.Data.(realtime.QueueChangedPayload)
	if !ok {
		t.Fatalf("事件 payload 类型 = %T，期望 QueueChangedPayload", event.Data)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("序列化队列更新 payload 失败：%v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatalf("解析队列更新 payload 失败：%v", err)
	}
	if got, ok := fields["source_deleted_at"].(float64); !ok || got != 1_700_000_000 {
		t.Fatalf("source_deleted_at = %#v，期望 1700000000", fields["source_deleted_at"])
	}
}
