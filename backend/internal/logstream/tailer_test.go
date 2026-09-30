package logstream

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerSharesTailerForSamePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}

	manager := NewManager()
	ctx := t.Context()

	first, firstClose, err := manager.Subscribe(ctx, path, 0, 8)
	if err != nil {
		t.Fatalf("订阅 first 失败：%v", err)
	}
	defer firstClose()
	second, secondClose, err := manager.Subscribe(ctx, path, 0, 8)
	if err != nil {
		t.Fatalf("订阅 second 失败：%v", err)
	}
	defer secondClose()

	if manager.TailerCount() != 1 {
		t.Fatalf("tailer count = %d，期望 1", manager.TailerCount())
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("2025/11/29 12:33:11.000001 [INFO] shared\n")
	_ = file.Close()

	assertLine := func(name string, ch <-chan Message) {
		t.Helper()
		select {
		case msg := <-ch:
			if msg.Entry.Message != "shared" {
				t.Fatalf("%s message = %s，期望 shared", name, msg.Entry.Message)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s 未收到日志", name)
		}
	}
	assertLine("first", first)
	assertLine("second", second)
}

func TestTailerResyncsAndClearsOversizedPartialLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxScannerBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}

	tailer := newTailer(path, 0, nil)
	sub := &subscriber{ch: make(chan Message, 2)}
	tailer.subs[sub] = struct{}{}

	tailer.readAvailable()

	select {
	case msg := <-sub.ch:
		if msg.Type != "resync_required" || msg.Reason != "partial_line_too_long" {
			t.Fatalf("msg = %+v，期望 partial_line_too_long resync_required", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到超长半行重同步消息")
	}
	if len(tailer.leftover) != 0 {
		t.Fatalf("leftover length = %d，期望 0", len(tailer.leftover))
	}
}

func TestTailerCatchUpSkipsClosedSubscriber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	content := "2025/11/29 12:33:09.000001 [INFO] old\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tailer := newTailer(path, int64(len(content)), nil)
	sub := &subscriber{ch: make(chan Message, 1)}
	close(sub.ch)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("sendCatchUp 向已关闭订阅通道发送时发生 panic：%v", r)
		}
	}()

	tailer.sendCatchUp(sub, 0)
}

func TestTailerStopsWhenBroadcastDropsLastSlowSubscriber(t *testing.T) {
	stopped := make(chan struct{})
	tailer := newTailer("missing.log", 0, func() {
		close(stopped)
	})
	sub := &subscriber{ch: make(chan Message)}
	tailer.subs[sub] = struct{}{}

	tailer.broadcast(Message{Type: "log_append"})

	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("最后一个慢订阅者被移除后 tailer 未停止")
	}
}
