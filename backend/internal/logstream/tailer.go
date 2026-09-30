package logstream

import (
	"bufio"
	"context"
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
func (m *Manager) Subscribe(ctx context.Context, path string, startCursor int64, buffer int) (<-chan Message, func(), error) {
	canonical, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	if buffer <= 0 {
		buffer = 128
	}

	m.mu.Lock()
	tailer := m.tailers[canonical]
	if tailer == nil {
		tailer = newTailer(canonical, startCursor, func() {
			m.mu.Lock()
			delete(m.tailers, canonical)
			m.mu.Unlock()
		})
		m.tailers[canonical] = tailer
		go tailer.run()
	}
	m.mu.Unlock()

	ch, unsubscribe := tailer.subscribe(ctx, startCursor, buffer)
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

func (t *Tailer) subscribe(ctx context.Context, startCursor int64, buffer int) (<-chan Message, func()) {
	sub := &subscriber{ch: make(chan Message, buffer), cursor: startCursor}
	t.mu.Lock()
	t.subs[sub] = struct{}{}
	currentCursor := t.cursor
	t.mu.Unlock()

	if startCursor < currentCursor {
		go t.sendCatchUp(sub, startCursor)
	}

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
	entries, nextCursor, err := ReadEntriesFromCursor(t.path, startCursor, 500)
	if err != nil {
		sub.send(Message{Type: "resync_required", Reason: err.Error()})
		return
	}
	for _, entry := range entries {
		if !sub.send(Message{Type: "log_append", Entry: entry, Cursor: entry.Cursor}) {
			t.unsubscribe(sub)
			return
		}
	}
	t.mu.Lock()
	currentCursor := t.cursor
	t.mu.Unlock()
	if nextCursor < currentCursor {
		sub.send(Message{Type: "resync_required", Reason: "catch_up_limit_reached"})
	}
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
	t.stopOnce.Do(func() {
		close(t.done)
		if t.onStop != nil {
			t.onStop()
		}
	})
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
	file, err := os.Open(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			t.broadcast(Message{Type: "resync_required", Reason: "log_file_missing"})
			return
		}
		t.broadcast(Message{Type: "error", Reason: err.Error()})
		return
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		t.broadcast(Message{Type: "error", Reason: err.Error()})
		return
	}
	t.mu.Lock()
	cursor := t.cursor
	truncated := stat.Size() < cursor
	if truncated {
		t.cursor = 0
		t.leftover = nil
		cursor = 0
	}
	t.mu.Unlock()
	if truncated {
		t.broadcast(Message{Type: "resync_required", Reason: "log_file_truncated"})
	}
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
				if len(content) > maxScannerBytes {
					t.broadcast(Message{Type: "resync_required", Reason: "partial_line_too_long", Cursor: cursor})
					continue
				}
				entry := ParseLine(string(content))
				entry.Cursor = cursor
				t.broadcast(Message{Type: "log_append", Entry: entry, Cursor: cursor})
			} else {
				t.leftover = append(t.leftover, line...)
				partialLineTooLong := len(t.leftover) > maxScannerBytes
				if partialLineTooLong {
					t.leftover = nil
				}
				t.mu.Unlock()
				if partialLineTooLong {
					t.broadcast(Message{Type: "resync_required", Reason: "partial_line_too_long", Cursor: cursor})
					return
				}
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
			sub.send(Message{Type: "resync_required", Reason: "subscriber_queue_full"})
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
