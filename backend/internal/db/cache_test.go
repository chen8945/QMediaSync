package db

import (
	"bytes"
	"errors"
	"testing"

	"github.com/coocood/freecache"
)

func TestCacheSetReturnsWriteErrors(t *testing.T) {
	cache := CacheGlobal{CacheInstance: freecache.NewCache(512 << 10)}
	if err := cache.Set("valid", []byte("value"), -1); err != nil || string(cache.Get("valid")) != "value" {
		t.Fatalf("成功写入未保留值：err=%v", err)
	}
	if err := cache.Set("too-large", bytes.Repeat([]byte("x"), 1024), 60); !errors.Is(err, freecache.ErrLargeEntry) {
		t.Fatalf("未返回底层写入错误：err=%v", err)
	}
	if cache.Get("too-large") != nil {
		t.Fatal("失败写入仍留下缓存值")
	}
	var unavailable CacheGlobal
	if err := unavailable.Set("key", []byte("value"), 60); !errors.Is(err, ErrCacheUnavailable) {
		t.Fatalf("未初始化缓存没有安全失败：err=%v", err)
	}
}
