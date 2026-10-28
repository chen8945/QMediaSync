package playback

import (
	"strings"
	"sync"
	"time"
)

const (
	preloadUAMappingCapacity = 256
	preloadUAMappingIdle     = 72 * time.Hour
)

type preloadUAMapping struct {
	ua   string
	used time.Time
}

// PreloadUAMappings 只保存播放 UA 预测；调用方用完整控制 UA 和认证设备身份构造键。
// 零值可用，Lookup 仅供真实用户活动调用，后台作业不得续期。
type PreloadUAMappings struct {
	mu         sync.Mutex
	generation uint64
	entries    map[string]preloadUAMapping
	now        func() time.Time
}

func (m *PreloadUAMappings) timeNow() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// Lookup 返回当前代际的完整播放器 UA，并记录本次真实活动。
func (m *PreloadUAMappings) Lookup(key string, generation uint64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation < m.generation {
		return ""
	}
	if m.generation != generation {
		clear(m.entries)
		m.generation = generation
	}
	entry, ok := m.entries[key]
	if !ok {
		return ""
	}
	now := m.timeNow()
	if now.Sub(entry.used) >= preloadUAMappingIdle {
		delete(m.entries, key)
		return ""
	}
	entry.used = now
	m.entries[key] = entry
	return entry.ua
}

// Remember 保存真实播放成功确认的 UA；不归一化大小写、空格或版本号。
func (m *PreloadUAMappings) Remember(key, ua string, generation uint64) bool {
	if key == "" || ua == "" || len(ua) > 8192 || strings.ContainsAny(ua, "\r\n\x00") {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation < m.generation {
		return false
	}
	if m.entries == nil || m.generation != generation {
		m.entries = make(map[string]preloadUAMapping)
		m.generation = generation
	}
	now := m.timeNow()
	oldestKey, oldest := "", now
	for candidate, entry := range m.entries {
		if now.Sub(entry.used) >= preloadUAMappingIdle {
			delete(m.entries, candidate)
		} else if oldestKey == "" || entry.used.Before(oldest) {
			oldestKey, oldest = candidate, entry.used
		}
	}
	if _, exists := m.entries[key]; !exists && len(m.entries) >= preloadUAMappingCapacity {
		delete(m.entries, oldestKey)
	}
	previous, exists := m.entries[key]
	m.entries[key] = preloadUAMapping{ua: ua, used: now}
	return !exists || previous.ua != ua
}

// Clear 在反代实例停止时释放其预测，不影响正常播放缓存。
func (m *PreloadUAMappings) Clear() {
	m.mu.Lock()
	clear(m.entries)
	m.mu.Unlock()
}
