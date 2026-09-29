package controllers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"qmediasync/internal/v115open"
)

type netFileBatchCacheKey struct {
	SourceType   string
	AccountID    uint
	Path         string
	SortBy       string
	SortOrder    string
	FoldersFirst bool
	Filter       string
	BatchStart   int
	BatchSize    int
}

type netFileBatchCacheViewKey struct {
	SourceType   string
	AccountID    uint
	Path         string
	SortBy       string
	SortOrder    string
	FoldersFirst bool
	Filter       string
}

type netFileBatchCachePathKey struct {
	SourceType string
	AccountID  uint
	Path       string
}

type netFileBatch struct {
	// 115 保留原始批次及祖先链，两个浏览入口共享；仅文件响应投影 FileItem。
	Raw115      *v115open.FileListResp
	Directories []DirResp
	Items       []*FileItem
	Total       int64
	TotalExact  bool
	HasMore     bool
	CachedAt    int64
	ExpiresAt   int64
}

type netFileBatchCache struct {
	flight   singleflight.Group
	mu       sync.Mutex
	maxItems int
	ttl      time.Duration
	items    map[netFileBatchCacheKey]netFileBatch
	views    map[netFileBatchCacheViewKey]uint64
	paths    map[netFileBatchCachePathKey]uint64
	trees    map[netFileBatchCachePathKey]uint64
	order    []netFileBatchCacheKey
}

var netFileCache = newNetFileBatchCache(200, 180*time.Second)

// 共享读取允许其他等待者继续使用；所有等待者离开后最多再运行一个有界批次。
const netFileFetchTimeout = 60 * time.Second

func (c *netFileBatchCache) getOrFetch(
	ctx context.Context,
	key netFileBatchCacheKey,
	refresh bool,
	fetch func(context.Context) (netFileBatch, error),
) (netFileBatch, bool, error) {
	if err := ctx.Err(); err != nil {
		return netFileBatch{}, false, err
	}
	generation := c.Generation(key)
	if !refresh {
		if batch, ok := c.Get(key, time.Now()); ok {
			return batch, true, nil
		}
	}
	load := func(fetchCtx context.Context) (netFileBatch, error) {
		batch, err := fetch(fetchCtx)
		if err != nil {
			return netFileBatch{}, err
		}
		if err := fetchCtx.Err(); err != nil {
			return netFileBatch{}, err
		}
		now := time.Now()
		batch.CachedAt, batch.ExpiresAt = now.Unix(), now.Add(c.ttl).Unix()
		c.setIfGeneration(key, batch, now, generation, refresh)
		return batch, nil
	}
	if refresh {
		batch, err := load(ctx)
		return batch, false, err
	}
	result := c.flight.DoChan(fmt.Sprintf("%s/%d", netFileSingleflightKey(key), generation), func() (any, error) {
		if batch, ok := c.Get(key, time.Now()); ok {
			return batch, nil
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), netFileFetchTimeout)
		defer cancel()
		return load(fetchCtx)
	})
	select {
	case <-ctx.Done():
		return netFileBatch{}, false, ctx.Err()
	case result := <-result:
		if err := ctx.Err(); err != nil {
			return netFileBatch{}, false, err
		}
		if result.Err != nil {
			return netFileBatch{}, false, result.Err
		}
		return result.Val.(netFileBatch), false, nil
	}
}

func newNetFileBatchCache(maxItems int, ttl time.Duration) *netFileBatchCache {
	if maxItems < 1 {
		maxItems = 1
	}
	return &netFileBatchCache{
		maxItems: maxItems,
		ttl:      ttl,
		items:    make(map[netFileBatchCacheKey]netFileBatch),
		views:    make(map[netFileBatchCacheViewKey]uint64),
		paths:    make(map[netFileBatchCachePathKey]uint64),
		trees:    make(map[netFileBatchCachePathKey]uint64),
		order:    make([]netFileBatchCacheKey, 0, maxItems),
	}
}

func (c *netFileBatchCache) Get(key netFileBatchCacheKey, now time.Time) (netFileBatch, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	batch, ok := c.items[key]
	if !ok {
		return netFileBatch{}, false
	}
	if batch.ExpiresAt > 0 && now.Unix() >= batch.ExpiresAt {
		c.deleteLocked(key)
		return netFileBatch{}, false
	}
	return batch, true
}

func (c *netFileBatchCache) Set(key netFileBatchCacheKey, batch netFileBatch, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.setLocked(key, batch, now)
}

func (c *netFileBatchCache) SetIfGeneration(key netFileBatchCacheKey, batch netFileBatch, now time.Time, generation uint64) bool {
	return c.setIfGeneration(key, batch, now, generation, true)
}

func (c *netFileBatchCache) setIfGeneration(key netFileBatchCacheKey, batch netFileBatch, now time.Time, generation uint64, overwrite bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.generationLocked(key) != generation {
		return false
	}
	// 普通请求可能在刷新期间开始，完成较晚时不能覆盖已刷新的同一批次。
	if current, exists := c.items[key]; !overwrite && exists && (current.ExpiresAt <= 0 || now.Unix() < current.ExpiresAt) {
		return false
	}
	c.setLocked(key, batch, now)
	return true
}

func (c *netFileBatchCache) Generation(key netFileBatchCacheKey) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.generationLocked(key)
}

func (c *netFileBatchCache) setLocked(key netFileBatchCacheKey, batch netFileBatch, now time.Time) {
	if _, exists := c.items[key]; !exists {
		c.order = append(c.order, key)
	}
	batch.CachedAt = now.Unix()
	batch.ExpiresAt = now.Add(c.ttl).Unix()
	c.items[key] = batch
	for len(c.items) > c.maxItems && len(c.order) > 0 {
		c.deleteLocked(c.order[0])
	}
}

func (c *netFileBatchCache) InvalidateView(sourceType string, accountID uint, path string, sortBy string, sortOrder string, filter string, foldersFirst bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	viewKey := netFileBatchCacheViewKey{
		SourceType:   sourceType,
		AccountID:    accountID,
		Path:         path,
		SortBy:       sortBy,
		SortOrder:    sortOrder,
		FoldersFirst: foldersFirst,
		Filter:       filter,
	}
	c.views[viewKey]++
	for key := range c.items {
		if key.SourceType == sourceType &&
			key.AccountID == accountID &&
			key.Path == path &&
			key.SortBy == sortBy &&
			key.SortOrder == sortOrder &&
			key.Filter == filter &&
			key.FoldersFirst == foldersFirst {
			c.deleteLocked(key)
		}
	}
}

func (c *netFileBatchCache) InvalidatePath(sourceType string, accountID uint, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.paths[netFileBatchCachePathKey{
		SourceType: sourceType,
		AccountID:  accountID,
		Path:       path,
	}]++
	for key := range c.items {
		if key.SourceType == sourceType && key.AccountID == accountID && key.Path == path {
			c.deleteLocked(key)
		}
	}
}

func (c *netFileBatchCache) InvalidatePathTree(sourceType string, accountID uint, path string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.trees[netFileBatchCachePathKey{
		SourceType: sourceType,
		AccountID:  accountID,
		Path:       path,
	}]++
	if sourceType == "115" {
		// 数字 ID 不携带祖先信息，阻止尚未返回祖先链的旧请求回填。
		c.paths[netFileBatchCachePathKey{SourceType: sourceType, AccountID: accountID}]++
	}
	for key, batch := range c.items {
		if key.SourceType == sourceType &&
			key.AccountID == accountID &&
			(netFileCachePathInTree(key.Path, path) || batch.has115Ancestor(path)) {
			c.deleteLocked(key)
		}
	}
}

func (b netFileBatch) has115Ancestor(id string) bool {
	if b.Raw115 != nil {
		for _, parent := range b.Raw115.Path {
			if parent.FileId.String() == id {
				return true
			}
		}
	}
	return false
}

func (c *netFileBatchCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.items)
}

func (c *netFileBatchCache) deleteLocked(key netFileBatchCacheKey) {
	delete(c.items, key)
	for i, item := range c.order {
		if item == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			return
		}
	}
}

func (c *netFileBatchCache) generationLocked(key netFileBatchCacheKey) uint64 {
	generation := c.views[key.viewKey()] + c.paths[key.pathKey()]
	if key.SourceType == "115" {
		generation += c.paths[netFileBatchCachePathKey{SourceType: key.SourceType, AccountID: key.AccountID}]
	}
	for treeKey, treeGeneration := range c.trees {
		if treeKey.SourceType == key.SourceType &&
			treeKey.AccountID == key.AccountID &&
			netFileCachePathInTree(key.Path, treeKey.Path) {
			generation += treeGeneration
		}
	}
	return generation
}

func (k netFileBatchCacheKey) viewKey() netFileBatchCacheViewKey {
	return netFileBatchCacheViewKey{
		SourceType:   k.SourceType,
		AccountID:    k.AccountID,
		Path:         k.Path,
		SortBy:       k.SortBy,
		SortOrder:    k.SortOrder,
		FoldersFirst: k.FoldersFirst,
		Filter:       k.Filter,
	}
}

func (k netFileBatchCacheKey) pathKey() netFileBatchCachePathKey {
	return netFileBatchCachePathKey{
		SourceType: k.SourceType,
		AccountID:  k.AccountID,
		Path:       k.Path,
	}
}

func netFileCachePathInTree(path string, root string) bool {
	if root == "/" {
		return path == "/" || strings.HasPrefix(path, "/")
	}
	return path == root || strings.HasPrefix(path, root+"/")
}
