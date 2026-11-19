package logstream

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Message 是 tailer 发给订阅者的消息。
type Message struct {
	Type   string `json:"type"`
	Entry  Entry  `json:"entry"`
	Cursor int64  `json:"cursor,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Manager 管理按文件路径共享的 tailer。
type Manager struct {
	mu      sync.Mutex
	tailers map[string]*Tailer
}

// GlobalManager 是通用日志和同步任务详情共享的 tailer manager。
var GlobalManager = NewManager()

// NewManager 创建共享 tailer manager。
func NewManager() *Manager {
	return &Manager{tailers: make(map[string]*Tailer)}
}

// Subscribe 订阅指定日志文件的新行。
func (m *Manager) Subscribe(ctx context.Context, path string, startCursor int64, buffer int, expectedFileID string) (<-chan Message, func(), error) {
	canonical, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	file, info, fileID, err := OpenFile(canonical, expectedFileID)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	if startCursor < 0 || startCursor > info.Size() {
		return nil, nil, ErrCursorInvalid
	}
	if buffer <= 0 {
		buffer = 128
	}

	m.mu.Lock()
	tailer := m.tailers[canonical]
	if tailer != nil {
		select {
		case <-tailer.done:
			tailer = nil
		default:
		}
	}
	if tailer == nil {
		tailer = newTailer(canonical, startCursor, func() {
			m.mu.Lock()
			if m.tailers[canonical] == tailer {
				delete(m.tailers, canonical)
			}
			m.mu.Unlock()
		})
		tailer.fileID = fileID
		m.tailers[canonical] = tailer
		go tailer.run()
	}
	ch, unsubscribe := tailer.subscribe(ctx, startCursor, buffer, fileID)
	m.mu.Unlock()
	tailer.stop()
	return ch, unsubscribe, nil
}

// TailerCount 返回当前 tailer 数量，用于测试和诊断。
func (m *Manager) TailerCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.tailers)
}

type subscriber struct {
	ch     chan Message
	cursor int64
	fileID string
	mu     sync.Mutex
	closed bool
}

// Tailer 负责单个文件路径的读取和广播。
type Tailer struct {
	path     string
	done     chan struct{}
	stopOnce sync.Once
	onStop   func()

	mu       sync.Mutex
	subs     map[*subscriber]struct{}
	cursor   int64
	leftover []byte
	fileID   string
}

func newTailer(path string, startCursor int64, onStop func()) *Tailer {
	return &Tailer{
		path:   path,
		done:   make(chan struct{}),
		onStop: onStop,
		subs:   make(map[*subscriber]struct{}),
		cursor: startCursor,
	}
}

func (t *Tailer) subscribe(ctx context.Context, startCursor int64, buffer int, fileID string) (<-chan Message, func()) {
	sub := &subscriber{ch: make(chan Message, buffer), cursor: startCursor, fileID: fileID}
	t.sendCatchUp(sub, startCursor)

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			t.unsubscribe(sub)
		})
	}

	go func() {
		<-ctx.Done()
		unsubscribe()
	}()
	return sub.ch, unsubscribe
}

func (t *Tailer) sendCatchUp(sub *subscriber, startCursor int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		sub.resync("tailer_stopped")
		return
	default:
	}
	if sub.fileID != "" && sub.fileID != t.fileID {
		sub.resync("log_file_rotated")
		return
	}
	// 补读与注册串行，避免实时消息越过补读中的较旧记录。
	if startCursor < t.cursor-int64(len(t.leftover)) {
		entries, nextCursor, _, err := ReadEntriesFromCursor(t.path, startCursor, 500, t.fileID)
		if err != nil {
			reason := "log_read_failed"
			if errors.Is(err, ErrCursorInvalid) {
				reason = "log_file_rotated"
			}
			sub.resync(reason)
			return
		}
		for _, entry := range entries {
			if !sub.send(Message{Type: "log_append", Entry: entry, Cursor: entry.Cursor}) {
				sub.resync("subscriber_queue_full")
				return
			}
		}
		if nextCursor < t.cursor-int64(len(t.leftover)) {
			sub.resync("catch_up_limit_reached")
			return
		}
	}
	t.subs[sub] = struct{}{}
}

func (t *Tailer) unsubscribe(sub *subscriber) {
	t.mu.Lock()
	if _, ok := t.subs[sub]; ok {
		delete(t.subs, sub)
		sub.close()
	}
	empty := len(t.subs) == 0
	t.mu.Unlock()
	if empty {
		t.stop()
	}
}

func (t *Tailer) stop() {
	t.mu.Lock()
	if len(t.subs) != 0 {
		t.mu.Unlock()
		return
	}
	stopped := false
	t.stopOnce.Do(func() {
		close(t.done)
		stopped = true
	})
	t.mu.Unlock()
	if stopped && t.onStop != nil {
		t.onStop()
	}
}

func (t *Tailer) run() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-t.done:
			return
		case <-ticker.C:
			t.readAvailable()
		}
	}
}

func (t *Tailer) readAvailable() {
	file, stat, fileID, err := OpenFile(t.path, "")
	if err != nil {
		if os.IsNotExist(err) {
			t.broadcast(Message{Type: "resync_required", Reason: "log_file_missing"})
			return
		}
		t.broadcast(Message{Type: "error", Reason: err.Error()})
		return
	}
	defer file.Close()

	t.mu.Lock()
	cursor := t.cursor
	rotated := t.fileID != "" && t.fileID != fileID
	t.fileID = fileID
	truncated := stat.Size() < cursor || rotated
	if truncated {
		t.cursor = 0
		t.leftover = nil
		reason := "log_file_truncated"
		if rotated {
			reason = "log_file_rotated"
		}
		for sub := range t.subs {
			sub.resync(reason)
			delete(t.subs, sub)
		}
		t.mu.Unlock()
		t.stop()
		return
	}
	t.mu.Unlock()
	if _, err := file.Seek(cursor, io.SeekStart); err != nil {
		t.broadcast(Message{Type: "error", Reason: err.Error()})
		return
	}

	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			t.mu.Lock()
			t.cursor += int64(len(line))
			cursor = t.cursor
			if line[len(line)-1] == '\n' {
				content := append(t.leftover, line[:len(line)-1]...)
				t.leftover = nil
				t.mu.Unlock()
				entry := ParseLine(string(content))
				entry.Cursor = cursor
				t.broadcast(Message{Type: "log_append", Entry: entry, Cursor: cursor})
			} else {
				t.leftover = append(t.leftover, line...)
				t.mu.Unlock()
			}
		}
		if err == io.EOF {
			return
		}
		if err != nil {
			t.broadcast(Message{Type: "error", Reason: err.Error()})
			return
		}
	}
}

func (t *Tailer) broadcast(msg Message) {
	t.mu.Lock()
	for sub := range t.subs {
		if !sub.send(msg) {
			sub.resync("subscriber_queue_full")
			delete(t.subs, sub)
			sub.close()
		}
	}
	empty := len(t.subs) == 0
	t.mu.Unlock()
	if empty {
		t.stop()
	}
}

func (s *subscriber) send(msg Message) (sent bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if msg.Type == "log_append" && msg.Cursor > 0 && msg.Cursor <= s.cursor {
		return true
	}
	defer func() {
		if recover() != nil {
			s.closed = true
			sent = false
		}
	}()
	select {
	case s.ch <- msg:
		if msg.Cursor > 0 {
			s.cursor = msg.Cursor
		}
		return true
	default:
		return false
	}
}

func (s *subscriber) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	defer func() {
		_ = recover()
	}()
	close(s.ch)
}

// resync 丢弃已失效的排队增量，为恢复提示留出位置后关闭慢订阅。
func (s *subscriber) resync(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	draining := true
	for draining {
		select {
		case <-s.ch:
		default:
			draining = false
		}
	}
	select {
	case s.ch <- Message{Type: "resync_required", Reason: reason}:
	default:
	}
	s.closed = true
	close(s.ch)
}
