package cache

import (
	"container/list"
	"context"
	"strconv"
	"sync"
	"time"
)

const (
	MaxCacheSize     int64 = 100 * 1024 * 1024
	MaxCacheNum            = 8092
	HeaderKeyExpired       = "Expired"
)

// Store 统一维护各连接配置的响应缓存和总容量。
type Store struct {
	mu            sync.Mutex
	closed        bool
	defaultExpiry time.Duration
	size          int64
	entries       list.List
	cancel        context.CancelFunc
	done          chan struct{}
}

// Scope 隔离一个连接配置的请求与空间缓存。
type Scope struct {
	store    *Store
	closed   bool
	requests map[string]*respCache
	spaces   map[string]map[string]*respCache
}

// NewStore 创建有明确退出拥有者的缓存。
func NewStore(defaultExpiry time.Duration) *Store {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{defaultExpiry: defaultExpiry, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				now := time.Now().UnixMilli()
				for element := s.entries.Front(); element != nil; {
					next := element.Next()
					rc := element.Value.(*respCache)
					if rc.expired <= now {
						s.removeLocked(rc)
					}
					element = next
				}
				s.mu.Unlock()
			}
		}
	}()
	return s
}

// NewScope 为新连接分配隔离空间。
func (s *Store) NewScope() *Scope {
	return &Scope{store: s, requests: make(map[string]*respCache), spaces: make(map[string]map[string]*respCache)}
}

// Stop 拒绝新缓存并等待维护后台退出。
func (s *Store) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for s.entries.Len() > 0 {
		s.removeLocked(s.entries.Front().Value.(*respCache))
	}
	s.mu.Unlock()
	s.cancel()
	select {
	case <-s.done:
		return nil
	default:
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 释放已退役连接的缓存，迟到响应不能重新登记。
func (scope *Scope) Close() {
	s := scope.store
	s.mu.Lock()
	scope.closed = true
	for _, rc := range scope.requests {
		s.removeLocked(rc)
	}
	clear(scope.spaces)
	s.mu.Unlock()
}

func (s *Store) removeLocked(rc *respCache) {
	if rc.element == nil {
		return
	}
	s.entries.Remove(rc.element)
	rc.element = nil
	s.size -= rc.size
	if rc.scope.requests[rc.cacheKey] == rc {
		delete(rc.scope.requests, rc.cacheKey)
	}
	rc.scope.delSpaceCacheLocked(rc.header.space, rc.header.spaceKey, rc)
}

func (s *Store) trimLocked() {
	for s.size > MaxCacheSize || s.entries.Len() > MaxCacheNum {
		s.removeLocked(s.entries.Front().Value.(*respCache))
	}
}

func (scope *Scope) getCache(key string) (*respCache, bool) {
	s := scope.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope.closed || s.closed {
		return nil, false
	}
	rc := scope.requests[key]
	if rc == nil || rc.expired <= time.Now().UnixMilli() {
		return nil, false
	}
	return rc, true
}

func (scope *Scope) putCache(key string, code int, body []byte, header respHeader) {
	if key == "" || body == nil || int64(len(body)) > MaxCacheSize {
		return
	}
	now := time.Now().UnixMilli()
	expired := now + scope.store.defaultExpiry.Milliseconds()
	if header.expired != "" {
		var err error
		expired, err = strconv.ParseInt(header.expired, 10, 64)
		if err != nil {
			return
		}
	}
	if expired <= now {
		return
	}
	rc := &respCache{code: code, body: body, cacheKey: key, expired: expired, header: header, scope: scope, size: int64(len(body))}
	s := scope.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope.closed || s.closed || expired <= time.Now().UnixMilli() {
		return
	}
	if previous := scope.requests[key]; previous != nil {
		s.removeLocked(previous)
	}
	scope.requests[key] = rc
	rc.element = s.entries.PushBack(rc)
	s.size += rc.size
	scope.putSpaceCacheLocked(header.space, header.spaceKey, rc)
	s.trimLocked()
}
