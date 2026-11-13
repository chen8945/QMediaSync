package m3u8

import (
	"context"
	"net/http"
	"sync"
	"time"

	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/util/urls"
)

const (
	MaxPlaylistNum     = 10
	PreChanSize        = 1000
	maintainDuration   = 10 * time.Minute
	stopUpdateDuration = maintainDuration + time.Minute
	removeDuration     = time.Hour
)

type playlistKey struct{ path, template string }
type playlistEntry struct {
	info     *Info
	lastRead time.Time
}
type playlistLoad struct {
	done chan struct{}
	info *Info
}

// Store 持有独立 OpenList 资源、单资源刷新和后台寿命。
type Store struct {
	mu           sync.Mutex
	closed       bool
	client       *http.Client
	ctx          context.Context
	cancel       context.CancelFunc
	entries      map[playlistKey]playlistEntry
	loading      map[playlistKey]*playlistLoad
	workers      sync.WaitGroup
	refreshSlots chan struct{}
	done         chan struct{}
	loadInfo     func(context.Context, playlistKey) (*Info, error)
}

// NewStore 同步构造所有查询入口；资源刷新不持共享锁。
func NewStore(client *http.Client) *Store {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Store{client: client, ctx: ctx, cancel: cancel, entries: make(map[playlistKey]playlistEntry), loading: make(map[playlistKey]*playlistLoad), done: make(chan struct{}), refreshSlots: make(chan struct{}, MaxPlaylistNum)}
	s.loadInfo = func(ctx context.Context, key playlistKey) (*Info, error) {
		info := &Info{OpenlistPath: key.path, TemplateId: key.template}
		if err := info.UpdateContent(ctx, client); err != nil {
			return nil, err
		}
		return info, nil
	}
	s.workers.Go(func() {
		ticker := time.NewTicker(maintainDuration)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshActive()
			}
		}
	})
	go func() { s.workers.Wait(); close(s.done) }()
	return s
}

// Stop 撤销刷新并等待实际网络工作完成。
func (s *Store) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	clear(s.entries)
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

func (s *Store) startLoadLocked(key playlistKey) *playlistLoad {
	if s.closed || key.path == "" || key.template == "" {
		return nil
	}
	if load := s.loading[key]; load != nil {
		return load
	}
	if len(s.loading) >= PreChanSize {
		return nil
	}
	load := &playlistLoad{done: make(chan struct{})}
	s.loading[key] = load
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		var info *Info
		var err error
		select {
		case s.refreshSlots <- struct{}{}:
			if err = s.ctx.Err(); err == nil {
				info, err = s.loadInfo(s.ctx, key)
			}
			<-s.refreshSlots
		case <-s.ctx.Done():
			err = s.ctx.Err()
		}
		s.mu.Lock()
		if !s.closed && err == nil {
			lastRead := s.entries[key].lastRead
			if lastRead.IsZero() {
				lastRead = time.Now()
			}
			s.entries[key] = playlistEntry{info: info, lastRead: lastRead}
			load.info = info
			s.evictLocked()
		} else if err != nil {
			delete(s.entries, key)
		}
		delete(s.loading, key)
		close(load.done)
		s.mu.Unlock()
		if err != nil && s.ctx.Err() == nil {
			logs.Error("播放列表更新失败, OpenListPath: %s, TemplateId: %s, 错误: %v", key.path, key.template, err)
		}
	}()
	return load
}

func (s *Store) queryInfo(ctx context.Context, path, template string) *Info {
	if ctx.Err() != nil {
		return nil
	}
	key := playlistKey{path, template}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	entry, exists := s.entries[key]
	if exists && time.Now().UnixMilli() < entry.info.LastUpdate+maintainDuration.Milliseconds() {
		entry.lastRead = time.Now()
		s.entries[key] = entry
		s.mu.Unlock()
		return entry.info
	}
	load := s.startLoadLocked(key)
	s.mu.Unlock()
	if load == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return nil
	case <-load.done:
		if ctx.Err() != nil {
			return nil
		}
		s.mu.Lock()
		if entry, ok := s.entries[key]; ok {
			entry.lastRead = time.Now()
			s.entries[key] = entry
		}
		info := load.info
		if s.closed {
			info = nil
		}
		s.mu.Unlock()
		return info
	}
}

func (s *Store) evictLocked() {
	for len(s.entries) > MaxPlaylistNum {
		var oldest playlistKey
		var oldestRead time.Time
		for key, entry := range s.entries {
			if oldestRead.IsZero() || entry.lastRead.Before(oldestRead) {
				oldest, oldestRead = key, entry.lastRead
			}
		}
		delete(s.entries, oldest)
	}
}

func (s *Store) refreshActive() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, entry := range s.entries {
		if now.UnixMilli() > entry.info.LastUpdate+removeDuration.Milliseconds() {
			delete(s.entries, key)
			continue
		}
		if now.Sub(entry.lastRead) < stopUpdateDuration {
			s.startLoadLocked(key)
		}
	}
}

// PushPlaylistAsync 只登记当前资源，不等待其它资源的刷新。
func (s *Store) PushPlaylistAsync(info Info) {
	s.mu.Lock()
	s.startLoadLocked(playlistKey{info.OpenlistPath, info.TemplateId})
	s.mu.Unlock()
}

// GetPlaylist 渲染本次请求的地址和 Token，资源快照不保留 Emby 授权。
func (s *Store) GetPlaylist(ctx context.Context, path, template string, proxy, main bool, prefix, apiKey string) (string, bool) {
	info := s.queryInfo(ctx, path, template)
	if info == nil {
		return "", false
	}
	if proxy {
		return info.ProxyContent(main, prefix, apiKey), true
	}
	return info.Content(), true
}

// GetTsLink 读取当次资源快照。
func (s *Store) GetTsLink(ctx context.Context, path, template string, idx int) (string, bool) {
	info := s.queryInfo(ctx, path, template)
	if info == nil {
		return "", false
	}
	return info.GetTsLink(idx)
}

// GetSubtitleLink 读取当次资源快照。
func (s *Store) GetSubtitleLink(ctx context.Context, path, template, name string) (string, bool) {
	info := s.queryInfo(ctx, path, template)
	if info == nil {
		return "", false
	}
	for _, subtitle := range info.Subtitles {
		if urls.ResolveResourceName(subtitle.Url) == name {
			return subtitle.Url, true
		}
	}
	return "", false
}
