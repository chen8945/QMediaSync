package fnosproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"sync"
	"time"
)

const (
	sourceCacheCapacity = 256
	sourceCacheTTL      = 24 * time.Hour
)

type sourceRecord struct {
	source     Source
	root       string
	relative   string
	url        string
	fileInfo   os.FileInfo
	rootInfo   os.FileInfo
	expiresAt  time.Time
	lastUsedAt time.Time
}

type sourceCache struct {
	mu         sync.Mutex
	entries    map[string]sourceRecord
	scope      string
	generation uint64
	stopped    bool
	now        func() time.Time
}

func newSourceCache() sourceCache {
	return sourceCache{entries: make(map[string]sourceRecord), now: time.Now}
}

func (cache *sourceCache) begin(scope string) uint64 {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.scope != scope {
		clear(cache.entries)
		cache.scope = scope
		cache.generation++
	}
	return cache.generation
}

func (cache *sourceCache) get(key string, generation uint64) (sourceRecord, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.stopped || cache.generation != generation {
		return sourceRecord{}, false
	}
	record, exists := cache.entries[key]
	if !exists {
		return sourceRecord{}, false
	}
	if !cache.now().Before(record.expiresAt) {
		delete(cache.entries, key)
		return sourceRecord{}, false
	}
	record.lastUsedAt = cache.now()
	cache.entries[key] = record
	return record, true
}

func (cache *sourceCache) put(key string, record sourceRecord, generation uint64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.stopped || cache.generation != generation {
		return
	}
	now := cache.now()
	for key, previous := range cache.entries {
		if !now.Before(previous.expiresAt) {
			delete(cache.entries, key)
		}
	}
	if _, exists := cache.entries[key]; !exists && len(cache.entries) >= sourceCacheCapacity {
		var oldestKey string
		var oldest time.Time
		for candidate, value := range cache.entries {
			if oldestKey == "" || value.lastUsedAt.Before(oldest) {
				oldestKey, oldest = candidate, value.lastUsedAt
			}
		}
		delete(cache.entries, oldestKey)
	}
	record.expiresAt, record.lastUsedAt = now.Add(sourceCacheTTL), now
	cache.entries[key] = record
}

func (cache *sourceCache) remove(key string, generation uint64) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.generation == generation {
		delete(cache.entries, key)
	}
}

func (cache *sourceCache) clear() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	clear(cache.entries)
	cache.generation++
	cache.stopped = true
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
