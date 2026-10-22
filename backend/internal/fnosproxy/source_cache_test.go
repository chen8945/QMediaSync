package fnosproxy

import (
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestSourceCacheFixedExpiryCapacityAndCopies(t *testing.T) {
	cache := newSourceCache()
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	generation := cache.begin("config-roots")
	cache.put("source", sourceRecord{url: "stable"}, generation)
	now = now.Add(23 * time.Hour)
	record, ok := cache.get("source", generation)
	if !ok {
		t.Fatal("24 小时内未命中")
	}
	record.url = "mutated"
	if current, ok := cache.get("source", generation); !ok || current.url != "stable" {
		t.Fatal("调用方修改了缓存值")
	}
	now = now.Add(time.Hour)
	if _, ok := cache.get("source", generation); ok {
		t.Fatal("命中延长了固定 24 小时有效期")
	}
	for index := range sourceCacheCapacity {
		now = now.Add(time.Second)
		cache.put(strconv.Itoa(index), sourceRecord{url: "entry"}, generation)
	}
	if _, ok := cache.get("0", generation); !ok {
		t.Fatal("容量内条目丢失")
	}
	now = now.Add(time.Second)
	cache.put("new", sourceRecord{url: "entry"}, generation)
	if len(cache.entries) != sourceCacheCapacity {
		t.Fatalf("缓存容量：%d", len(cache.entries))
	}
	if _, ok := cache.get("1", generation); ok {
		t.Fatal("没有淘汰最近最少使用条目")
	}
	if _, ok := cache.get("0", generation); !ok {
		t.Fatal("最近使用条目被淘汰")
	}
}

func TestSourceCacheScopeAndStopRejectOldInflight(t *testing.T) {
	cache := newSourceCache()
	old := cache.begin("old")
	cache.put("source", sourceRecord{url: "old"}, old)
	current := cache.begin("new")
	cache.put("source", sourceRecord{url: "stale"}, old)
	if _, ok := cache.get("source", current); ok {
		t.Fatal("旧操作写回新配置缓存")
	}
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			for range 100 {
				cache.put("source", sourceRecord{url: "new"}, current)
				_, _ = cache.get("source", current)
			}
		})
	}
	cache.clear()
	workers.Wait()
	cache.put("source", sourceRecord{url: "after-stop"}, cache.begin("new"))
	if len(cache.entries) != 0 {
		t.Fatal("停止后在途操作重新填入缓存")
	}
}
