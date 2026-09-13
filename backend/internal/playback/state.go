// Package playback 编排 115 临时副本取链与缓存槽位。
package playback

import (
	"context"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	ModeDirect = "direct"
	ModeProxy  = "proxy"
	// URLCacheTTL 与现有 115 直链缓存保持相同期限。
	URLCacheTTL = 50 * time.Minute
)

// SourceKey 隔离本地账号、实际 115 用户及原始文件。
type SourceKey struct {
	AccountID uint
	UserID    string
	PickCode  string
}

// Slot 使用现有播放模式及有效 UA，不引入播放会话身份。
type Slot struct {
	Mode string
	UA   string
}

type accountKey struct {
	id  uint
	uid string
}

type copyDirectory struct {
	lock chan struct{}
	id   string // 由 Manager.mu 保护，lock 仅合并根目录初始化。
}

type operationKey struct {
	account accountKey
	name    string
}

// Manager 保存进程内槽位和临时目录，不持久化播放状态。
type Manager struct {
	mu      sync.Mutex
	slots   map[SourceKey]map[Slot]time.Time
	pending map[SourceKey]map[Slot]int
	dirs    map[accountKey]*copyDirectory
	// nil 表示正在取链、等待删除或清理中；非 nil 是待重试的不可变目录身份。
	operations map[operationKey]*operationDirectory
	nextSweep  time.Time
	retries    []time.Duration
	stopped    bool
	workers    sync.WaitGroup
	// shutdown 只传播进程退出，不承载请求身份或播放器取消。
	shutdown context.Context
	stop     context.CancelFunc
}

// DefaultManager 供播放控制器与定时维护共享槽位及操作目录状态。
var DefaultManager = NewManager()

// NewManager 创建播放编排器。
func NewManager() *Manager {
	shutdown, stop := context.WithCancel(context.Background())
	return &Manager{
		slots:      make(map[SourceKey]map[Slot]time.Time),
		pending:    make(map[SourceKey]map[Slot]int),
		dirs:       make(map[accountKey]*copyDirectory),
		operations: make(map[operationKey]*operationDirectory),
		retries:    []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second},
		shutdown:   shutdown,
		stop:       stop,
	}
}

// Begin 原子预留当前槽位，并判断其他有效或正在取链的槽位；网络调用在锁外执行。
func (m *Manager) Begin(source SourceKey, slot Slot, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweep(now)
	other := m.hasOther(source, slot, now)
	if !m.stopped {
		if m.pending[source] == nil {
			m.pending[source] = make(map[Slot]int)
		}
		m.pending[source][slot]++
	}
	return other
}

// Finish 撤销一次预留，成功时同时发布缓存期限；零期限只撤销预留。
func (m *Manager) Finish(source SourceKey, slot Slot, expiresAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slots := m.pending[source]; slots != nil {
		if slots[slot] > 1 {
			slots[slot]--
		} else {
			delete(slots, slot)
		}
		if len(slots) == 0 {
			delete(m.pending, source)
		}
	}
	m.record(source, slot, expiresAt)
}

// HasOther 查看其他有效或正在取链的槽位；实际 miss 应使用 Begin 原子判定和预留。
func (m *Manager) HasOther(source SourceKey, current Slot, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweep(now)
	return m.hasOther(source, current, now)
}

func (m *Manager) hasOther(source SourceKey, current Slot, now time.Time) bool {
	for slot, expiresAt := range m.slots[source] {
		if slot != current && expiresAt.After(now) {
			return true
		}
	}
	for slot, count := range m.pending[source] {
		if slot != current && count > 0 {
			return true
		}
	}
	return false
}

// Record 在 URL 写入缓存后登记期限；缓存命中和副本删除均不续期。
func (m *Manager) Record(source SourceKey, slot Slot, expiresAt time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record(source, slot, expiresAt)
}

func (m *Manager) record(source SourceKey, slot Slot, expiresAt time.Time) {
	now := time.Now()
	m.sweep(now)
	if !expiresAt.After(now) {
		return
	}
	if m.slots[source] == nil {
		m.slots[source] = make(map[Slot]time.Time)
	}
	m.slots[source][slot] = expiresAt
}

// URLExpiresAt 使用签名 t 的到期前五分钟和现有缓存上限中的较早期限。
// 缺少或无法解析 t 时保留原期限；已进入安全窗口时返回非未来时间，由调用方拒绝缓存。
func URLExpiresAt(rawURL string, now time.Time) time.Time {
	expiresAt := now.Add(URLCacheTTL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return expiresAt
	}
	expires, err := strconv.ParseInt(u.Query().Get("t"), 10, 64)
	if err != nil {
		return expiresAt
	}
	if expires < 0 {
		return time.Time{}
	}
	// 先比较 Unix 秒，避免畸形的大整数经 time.Unix 转换溢出后变成过去。
	if expires < expiresAt.Unix()+int64(5*time.Minute/time.Second) {
		return time.Unix(expires-int64(5*time.Minute/time.Second), 0)
	}
	return expiresAt
}

func (m *Manager) sweep(now time.Time) {
	if now.Before(m.nextSweep) {
		return
	}
	// ponytail: 按访问惰性扫描，每分钟最多一次；槽位规模成为瓶颈时再更换到期索引。
	for source, slots := range m.slots {
		for slot, expiresAt := range slots {
			if !expiresAt.After(now) {
				delete(slots, slot)
			}
		}
		if len(slots) == 0 {
			delete(m.slots, source)
		}
	}
	m.nextSweep = now.Add(time.Minute)
}

func (m *Manager) directory(source SourceKey) *copyDirectory {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := accountKey{source.AccountID, source.UserID}
	if m.dirs[key] == nil {
		m.dirs[key] = &copyDirectory{lock: make(chan struct{}, 1)}
	}
	return m.dirs[key]
}

func (m *Manager) claimOperation(source SourceKey, name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := operationKey{accountKey{source.AccountID, source.UserID}, name}
	if _, exists := m.operations[key]; exists || m.stopped {
		return false
	}
	m.operations[key] = nil
	return true
}

func (m *Manager) releaseOperation(source SourceKey, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.operations, operationKey{accountKey{source.AccountID, source.UserID}, name})
}

func (m *Manager) finishCleanup(source SourceKey, dir operationDirectory, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := operationKey{accountKey{source.AccountID, source.UserID}, dir.name}
	if err == nil || m.stopped || !dir.created {
		delete(m.operations, key)
		return
	}
	m.operations[key] = &dir
}

func (m *Manager) pendingCleanups(source SourceKey) map[string]*operationDirectory {
	m.mu.Lock()
	defer m.mu.Unlock()
	account := accountKey{source.AccountID, source.UserID}
	pending := make(map[string]*operationDirectory)
	for key, dir := range m.operations {
		if key.account == account && dir != nil {
			pending[key.name] = dir
		}
	}
	return pending
}

func (m *Manager) claimCleanup(source SourceKey, dir *operationDirectory) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := operationKey{accountKey{source.AccountID, source.UserID}, dir.name}
	// 比较同一份待重试记录，避免并发维护再次领取另一轮刚归还的失败记录。
	if m.stopped || m.operations[key] != dir {
		return false
	}
	m.operations[key] = nil
	return true
}

func (m *Manager) startWork() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped {
		return false
	}
	m.workers.Add(1)
	return true
}

// Shutdown 阻止新工作，取消副本和后台清理，并等待已登记的调用退出。
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	m.stop()
	clear(m.operations)
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		m.workers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
