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

func TestTailerPreservesLongPartialLineUntilCompleted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")
	longLine := "头部" + strings.Repeat("中", 360000) + "尾部"
	if err := os.WriteFile(path, []byte(longLine), 0o644); err != nil {
		t.Fatal(err)
	}

	tailer := newTailer(path, 0, nil)
	sub := &subscriber{ch: make(chan Message, 2)}
	tailer.subs[sub] = struct{}{}

	tailer.readAvailable()
	tailer.sendCatchUp(sub, 0)

	select {
	case msg := <-sub.ch:
		t.Fatalf("未完成半行不应投递或要求重同步: type=%s reason=%s", msg.Type, msg.Reason)
	default:
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\nnext\n"); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	tailer.readAvailable()
	for _, want := range []string{longLine, "next"} {
		select {
		case msg := <-sub.ch:
			if msg.Type != "log_append" || msg.Entry.Message != want {
				t.Fatalf("完整日志被截断或顺序错误: type=%s length=%d", msg.Type, len(msg.Entry.Message))
			}
		default:
			t.Fatal("补齐换行后未投递完整日志")
		}
	}
	tailer.readAvailable()
	select {
	case msg := <-sub.ch:
		t.Fatalf("重复投递: type=%s", msg.Type)
	default:
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
