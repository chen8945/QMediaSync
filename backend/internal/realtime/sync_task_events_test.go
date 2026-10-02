package realtime

import (
	"testing"
	"time"
)

func TestSyncTaskHubReplaysOnlyConsecutivePatchesInCurrentEpoch(t *testing.T) {
	hub := NewSyncTaskHub()
	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Payload: SyncTaskEventPayload{SyncID: 9, Sequence: 1}})
	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Payload: SyncTaskEventPayload{SyncID: 9, Sequence: 2}})

	_, replay, _, replayed, unsubscribe := hub.SubscribeFrom(9, hub.EventID(1), 1)
	defer unsubscribe()

	if !replayed {
		t.Fatal("同 epoch 的连续 patch 应命中回放")
	}
	if len(replay) != 1 || replay[0].Payload.Sequence != 2 {
		t.Fatalf("replay = %#v，期望只回放 sequence=2", replay)
	}
}

func TestSyncTaskHubFallsBackToSnapshotForInvalidOrMissingReplay(t *testing.T) {
	hub := NewSyncTaskHub()
	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Payload: SyncTaskEventPayload{SyncID: 9, Sequence: 3}})

	cases := []struct {
		name        string
		lastEventID string
	}{
		{name: "空 ID", lastEventID: ""},
		{name: "负数", lastEventID: hub.Epoch() + ":-1"},
		{name: "多余分隔符", lastEventID: hub.Epoch() + ":1:2"},
		{name: "其他 epoch", lastEventID: "other:3"},
		{name: "缓存缺口", lastEventID: hub.EventID(1)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, replay, snapshotSequence, replayed, unsubscribe := hub.SubscribeFrom(9, tc.lastEventID, 1)
			defer unsubscribe()
			if replayed || len(replay) != 0 {
				t.Fatalf("last event ID %q 不应命中回放", tc.lastEventID)
			}
			if snapshotSequence != 3 {
				t.Fatalf("snapshot waterline = %d，期望 3", snapshotSequence)
			}
		})
	}
}

func TestSyncTaskHubFiltersMatchingTaskAndClosesSlowSubscriber(t *testing.T) {
	hub := NewSyncTaskHub()
	events, _, _, _, unsubscribe := hub.SubscribeFrom(9, "", 1)
	defer unsubscribe()

	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Payload: SyncTaskEventPayload{SyncID: 8, Sequence: 1}})
	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Payload: SyncTaskEventPayload{SyncID: 9, Sequence: 1}})
	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Payload: SyncTaskEventPayload{SyncID: 9, Sequence: 2}})

	select {
	case event, ok := <-events:
		if !ok || event.Payload.SyncID != 9 {
			t.Fatalf("订阅收到错误事件: %#v", event)
		}
		if _, ok := <-events; ok {
			t.Fatal("慢订阅者应被关闭")
		}
	case <-time.After(time.Second):
		t.Fatal("慢订阅者阻塞了发布")
	}
}

func TestSyncTaskHubClearsReplayCacheAfterTerminalEvent(t *testing.T) {
	hub := NewSyncTaskHub()
	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Payload: SyncTaskEventPayload{SyncID: 9, Sequence: 1}})
	hub.Publish(TaskStreamEvent{EventType: EventSyncTaskUpdated, Terminal: true, Payload: SyncTaskEventPayload{SyncID: 9, Sequence: 2}})

	_, replay, snapshotSequence, replayed, unsubscribe := hub.SubscribeFrom(9, hub.EventID(1), 1)
	defer unsubscribe()
	if replayed || len(replay) != 0 {
		t.Fatal("终态事件后不应回放 patch")
	}
	if snapshotSequence != 0 {
		t.Fatalf("终态事件后 snapshot waterline = %d，期望 0", snapshotSequence)
	}
}

func TestSyncTaskHubPublishesSequenceAndTerminalCleanupAtomically(t *testing.T) {
	hub := NewSyncTaskHub()

	first := hub.PublishSyncTaskEvent(EventSyncTaskUpdated, SyncTaskEventPayload{
		SyncID: 9,
		Status: 1,
	})
	terminal := hub.PublishSyncTaskEvent(EventSyncTaskUpdated, SyncTaskEventPayload{
		SyncID: 9,
		Status: 2,
	})

	if first.Sequence != 1 || terminal.Sequence != 2 {
		t.Fatalf("sequence = (%d, %d)，期望 (1, 2)", first.Sequence, terminal.Sequence)
	}

	_, replay, snapshotSequence, replayed, unsubscribe := hub.SubscribeFrom(9, hub.EventID(first.Sequence), 1)
	defer unsubscribe()
	if replayed || len(replay) != 0 {
		t.Fatal("终态事件后不应恢复已清理的 patch")
	}
	if snapshotSequence != 0 {
		t.Fatalf("终态事件后 snapshot waterline = %d，期望 0", snapshotSequence)
	}
}

func TestSyncTaskHubKeepsGenerationReplayUntilLedgerEnds(t *testing.T) {
	hub := NewSyncTaskHub()
	pending, running, completed := SyncLedgerPending, SyncLedgerRunning, SyncLedgerCompleted
	first := hub.PublishSyncTaskEvent(EventSyncTaskUpdated, SyncTaskEventPayload{SyncID: 9, Status: 1})
	generation := hub.PublishSyncTaskEvent(EventSyncTaskUpdated, SyncTaskEventPayload{SyncID: 9, Status: 2, LedgerStatus: &pending, FinishAt: 120})
	background := hub.PublishSyncTaskEvent(EventSyncTaskUpdated, SyncTaskEventPayload{SyncID: 9, Status: 2, LedgerStatus: &running, FinishAt: 120})
	_, replay, _, replayed, unsubscribe := hub.SubscribeFrom(9, hub.EventID(first.Sequence), 8)
	defer unsubscribe()
	if !replayed || len(replay) != 2 || replay[0].Terminal || replay[1].Terminal || generation.Sequence != 2 || background.Sequence != 3 {
		t.Fatalf("生成结束过早清理回放: %+v", replay)
	}
	exit := int64(128)
	terminal := hub.PublishSyncTaskEvent(EventSyncTaskUpdated, SyncTaskEventPayload{SyncID: 9, Status: 2, LedgerStatus: &completed, LedgerFinishedAt: &exit, FinishAt: 120})
	if terminal.Sequence != 4 || !terminal.IsTerminal() {
		t.Fatalf("后台终点序号或终态错误: %+v", terminal)
	}
	_, replay, waterline, replayed, unsubscribeFinal := hub.SubscribeFrom(9, hub.EventID(background.Sequence), 8)
	defer unsubscribeFinal()
	if replayed || len(replay) != 0 || waterline != 0 {
		t.Fatal("后台终态未清理回放")
	}
}
