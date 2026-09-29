package controllers

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/v115open"
)

func TestNetFileBatchCacheHitAndExpire(t *testing.T) {
	cache := newNetFileBatchCache(2, time.Second)
	key := netFileBatchCacheKey{
		SourceType: "115",
		AccountID:  1,
		Path:       "0",
		SortBy:     "name",
		SortOrder:  "asc",
		BatchStart: 0,
		BatchSize:  1000,
	}
	cache.Set(key, netFileBatch{Items: []*FileItem{{Id: "1", Name: "a"}}}, time.Now())
	if batch, ok := cache.Get(key, time.Now()); !ok || len(batch.Items) != 1 {
		t.Fatalf("cache hit = (%+v,%v), want one item hit", batch, ok)
	}
	if _, ok := cache.Get(key, time.Now().Add(2*time.Second)); ok {
		t.Fatal("expired batch still hit")
	}
}

func TestNetFileBatchCacheInvalidateView(t *testing.T) {
	cache := newNetFileBatchCache(10, time.Minute)
	now := time.Now()
	base := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "0", BatchStart: 0, BatchSize: 1000}
	nameAsc := base
	nameAsc.SortBy = "name"
	nameAsc.SortOrder = "asc"
	timeDesc := base
	timeDesc.SortBy = "time"
	timeDesc.SortOrder = "desc"
	cache.Set(nameAsc, netFileBatch{}, now)
	cache.Set(timeDesc, netFileBatch{}, now)
	cache.InvalidateView("115", 1, "0", "name", "asc", "", false)
	if _, ok := cache.Get(nameAsc, now); ok {
		t.Fatal("name asc view still exists")
	}
	if _, ok := cache.Get(timeDesc, now); !ok {
		t.Fatal("time desc view should remain")
	}
}

func TestNetFileBatchCacheInvalidatePath(t *testing.T) {
	cache := newNetFileBatchCache(10, time.Minute)
	now := time.Now()
	nameAsc := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "0", SortBy: "name", SortOrder: "asc", BatchStart: 0, BatchSize: 1000}
	timeDesc := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "0", SortBy: "time", SortOrder: "desc", BatchStart: 0, BatchSize: 1000}
	nameGeneration := cache.Generation(nameAsc)
	timeGeneration := cache.Generation(timeDesc)
	cache.Set(nameAsc, netFileBatch{}, now)
	cache.Set(timeDesc, netFileBatch{}, now)
	cache.InvalidatePath("115", 1, "0")
	if cache.Len() != 0 {
		t.Fatalf("Len = %d, want 0", cache.Len())
	}
	if cache.Generation(nameAsc) <= nameGeneration {
		t.Fatal("name asc view generation 未随路径失效推进")
	}
	if cache.Generation(timeDesc) <= timeGeneration {
		t.Fatal("time desc view generation 未随路径失效推进")
	}
	if ok := cache.SetIfGeneration(nameAsc, netFileBatch{Items: []*FileItem{{Id: "old"}}}, now, nameGeneration); ok {
		t.Fatal("路径失效前开始的旧请求不应重新写入缓存")
	}
}

func TestNetFileBatchCacheInvalidatePathTree(t *testing.T) {
	cache := newNetFileBatchCache(10, time.Minute)
	now := time.Now()
	parent := netFileBatchCacheKey{SourceType: "openlist", AccountID: 1, Path: "/", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}
	target := netFileBatchCacheKey{SourceType: "openlist", AccountID: 1, Path: "/Movies", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}
	child := netFileBatchCacheKey{SourceType: "openlist", AccountID: 1, Path: "/Movies/Season 1", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}
	sibling := netFileBatchCacheKey{SourceType: "openlist", AccountID: 1, Path: "/Movies2", SortBy: "default", SortOrder: "asc", Filter: "none", BatchStart: 0, BatchSize: 500}
	targetGeneration := cache.Generation(target)
	childGeneration := cache.Generation(child)

	cache.Set(parent, netFileBatch{}, now)
	cache.Set(target, netFileBatch{}, now)
	cache.Set(child, netFileBatch{}, now)
	cache.Set(sibling, netFileBatch{}, now)

	cache.InvalidatePathTree("openlist", 1, "/Movies")
	if _, ok := cache.Get(target, now); ok {
		t.Fatal("被删目录缓存仍存在")
	}
	if _, ok := cache.Get(child, now); ok {
		t.Fatal("被删目录子路径缓存仍存在")
	}
	if _, ok := cache.Get(parent, now); !ok {
		t.Fatal("父目录缓存不应由路径树失效清理")
	}
	if _, ok := cache.Get(sibling, now); !ok {
		t.Fatal("相似前缀兄弟目录缓存不应被清理")
	}
	if cache.Generation(target) <= targetGeneration {
		t.Fatal("被删目录 generation 未推进")
	}
	if cache.Generation(child) <= childGeneration {
		t.Fatal("子路径 generation 未推进")
	}
}

func TestNetFileBatchCacheInvalidateViewAdvancesGeneration(t *testing.T) {
	cache := newNetFileBatchCache(10, time.Minute)
	key := netFileBatchCacheKey{
		SourceType: "openlist",
		AccountID:  1,
		Path:       "/",
		SortBy:     "default",
		SortOrder:  "asc",
		Filter:     "none",
		BatchStart: 0,
		BatchSize:  500,
	}

	before := cache.Generation(key)
	cache.InvalidateView(key.SourceType, key.AccountID, key.Path, key.SortBy, key.SortOrder, key.Filter, key.FoldersFirst)
	after := cache.Generation(key)
	if after <= before {
		t.Fatalf("generation 未推进：before=%d after=%d", before, after)
	}
}

func TestNetFileBatchCacheSetIfGenerationPreventsStaleWriteAfterRefresh(t *testing.T) {
	cache := newNetFileBatchCache(10, time.Minute)
	now := time.Now()
	key := netFileBatchCacheKey{
		SourceType: "openlist",
		AccountID:  1,
		Path:       "/",
		SortBy:     "default",
		SortOrder:  "asc",
		Filter:     "none",
		BatchStart: 0,
		BatchSize:  500,
	}

	ordinaryGeneration := cache.Generation(key)
	cache.InvalidateView(key.SourceType, key.AccountID, key.Path, key.SortBy, key.SortOrder, key.Filter, key.FoldersFirst)
	refreshGeneration := cache.Generation(key)

	if ok := cache.SetIfGeneration(key, netFileBatch{Items: []*FileItem{{Id: "old", Name: "old"}}}, now, ordinaryGeneration); ok {
		t.Fatal("刷新后旧 generation 的普通请求不应写入缓存")
	}
	if ok := cache.SetIfGeneration(key, netFileBatch{Items: []*FileItem{{Id: "new", Name: "new"}}}, now, refreshGeneration); !ok {
		t.Fatal("当前 generation 的刷新请求应写入缓存")
	}

	batch, ok := cache.Get(key, now)
	if !ok {
		t.Fatal("刷新结果未写入缓存")
	}
	if len(batch.Items) != 1 || batch.Items[0].Id != "new" {
		t.Fatalf("缓存结果 = %+v，期望保留刷新结果", batch.Items)
	}
}

func TestNetFileCacheSeparatesFolderGroupingAndFollowMode(t *testing.T) {
	cache := newNetFileBatchCache(10, time.Minute)
	now := time.Now()
	first := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "0", SortBy: "name", SortOrder: "asc", FoldersFirst: true}
	mixed := first
	mixed.FoldersFirst = false
	follow := mixed
	follow.SortBy = "default"
	for i, key := range []netFileBatchCacheKey{first, mixed, follow} {
		cache.Set(key, netFileBatch{Total: int64(i + 1)}, now)
	}
	for i, key := range []netFileBatchCacheKey{first, mixed, follow} {
		batch, ok := cache.Get(key, now)
		if !ok || batch.Total != int64(i+1) {
			t.Fatalf("缓存相互覆盖：%+v", key)
		}
	}
	if netFileSingleflightKey(first) == netFileSingleflightKey(mixed) || netFileSingleflightKey(mixed) == netFileSingleflightKey(follow) {
		t.Fatal("不同排序不能合并请求")
	}
	generation := cache.Generation(first)
	cache.InvalidateView(first.SourceType, first.AccountID, first.Path, first.SortBy, first.SortOrder, first.Filter, true)
	if cache.SetIfGeneration(first, netFileBatch{}, now, generation) {
		t.Fatal("刷新后旧置顶请求不能回写")
	}
	if _, ok := cache.Get(mixed, now); !ok {
		t.Fatal("刷新置顶视图不应覆盖混排")
	}
	cache.InvalidatePath(first.SourceType, first.AccountID, first.Path)
	if cache.Len() != 0 {
		t.Fatal("目录变更必须失效所有排序视图")
	}
}

func TestNetFileBatchCacheAbsoluteTTL180Seconds(t *testing.T) {
	if netFileCache.ttl != 180*time.Second {
		t.Fatalf("浏览缓存 TTL = %s，期望 180s", netFileCache.ttl)
	}
	cache := newNetFileBatchCache(2, netFileCache.ttl)
	key := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "0"}
	now := time.Unix(1000, 0)
	cache.Set(key, netFileBatch{}, now)
	if batch, ok := cache.Get(key, now.Add(179*time.Second)); !ok || batch.ExpiresAt != 1180 {
		t.Fatalf("180 秒内应命中且不续期：%+v %t", batch, ok)
	}
	if _, ok := cache.Get(key, now.Add(180*time.Second)); ok {
		t.Fatal("命中不应延长绝对过期时间")
	}
}

func TestNetFileSharedFetchCancellationDoesNotCancelOtherWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newNetFileBatchCache(2, 180*time.Second)
		key := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "0"}
		firstCtx, cancelFirst := context.WithCancel(t.Context())
		defer cancelFirst()
		release := make(chan struct{})
		calls := 0
		var fetchCtx context.Context
		fetch := func(ctx context.Context) (netFileBatch, error) {
			calls++
			fetchCtx = ctx
			select {
			case <-ctx.Done():
				return netFileBatch{}, ctx.Err()
			case <-release:
				return netFileBatch{Total: 7}, nil
			}
		}
		first := make(chan error, 1)
		go func() { _, _, err := cache.getOrFetch(firstCtx, key, false, fetch); first <- err }()
		synctest.Wait()
		second := make(chan error, 1)
		go func() {
			batch, _, err := cache.getOrFetch(t.Context(), key, false, fetch)
			if err == nil && batch.Total != 7 {
				err = fmt.Errorf("共享结果错误：%+v", batch)
			}
			second <- err
		}()
		synctest.Wait()
		cancelFirst()
		synctest.Wait()
		if err := <-first; !errors.Is(err, context.Canceled) {
			t.Fatalf("取消等待者应立即退出：%v", err)
		}
		if fetchCtx.Err() != nil {
			t.Fatal("首个等待者取消了共享上游请求")
		}
		close(release)
		if err := <-second; err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("相同批次请求数 = %d，期望 1", calls)
		}
		batch, hit, err := cache.getOrFetch(t.Context(), key, false, fetch)
		if err != nil || !hit || batch.ExpiresAt-batch.CachedAt != 180 || calls != 1 {
			t.Fatalf("共享结果应写入 180 秒缓存：%+v %t %v 请求数=%d", batch, hit, err, calls)
		}
	})
}

func TestNetFileSharedFetchHasBoundedLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newNetFileBatchCache(1, time.Minute)
		key := netFileBatchCacheKey{SourceType: "openlist", AccountID: 1, Path: "/"}
		started := time.Now()
		_, _, err := cache.getOrFetch(t.Context(), key, false, func(ctx context.Context) (netFileBatch, error) {
			<-ctx.Done()
			return netFileBatch{}, ctx.Err()
		})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) != netFileFetchTimeout || cache.Len() != 0 {
			t.Fatalf("共享请求超时必须退出且不缓存：%v，耗时 %s", err, time.Since(started))
		}
	})
}

func TestNetFileSharedFetchDoesNotJoinPreInvalidationRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newNetFileBatchCache(2, time.Minute)
		key := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "0"}
		release := make(chan struct{})
		oldDone := make(chan error, 1)
		go func() {
			_, _, err := cache.getOrFetch(t.Context(), key, false, func(context.Context) (netFileBatch, error) {
				<-release
				return netFileBatch{Total: 1}, nil
			})
			oldDone <- err
		}()
		synctest.Wait()
		cache.InvalidatePath("115", 1, "0")
		batch, _, err := cache.getOrFetch(t.Context(), key, false, func(context.Context) (netFileBatch, error) {
			return netFileBatch{Total: 2}, nil
		})
		if err != nil || batch.Total != 2 {
			t.Fatalf("失效后的请求不能加入旧 flight：%+v %v", batch, err)
		}
		close(release)
		if err := <-oldDone; err != nil {
			t.Fatal(err)
		}
		if got, ok := cache.Get(key, time.Now()); !ok || got.Total != 2 {
			t.Fatalf("旧请求不能覆盖失效后的缓存：%+v %t", got, ok)
		}
	})
}

func TestNetFileBatchCacheInvalidates115AncestorMetadata(t *testing.T) {
	cache := newNetFileBatchCache(10, time.Minute)
	now := time.Now()
	child := netFileBatchCacheKey{SourceType: "115", AccountID: 1, Path: "30"}
	sibling, foreign := child, child
	sibling.Path, foreign.AccountID = "40", 2
	raw := &v115open.FileListResp{Path: []v115open.FileParentPath{{FileId: "0"}, {FileId: "10"}, {FileId: "30"}}, PathStr: "old/child"}
	cache.Set(child, netFileBatch{Raw115: raw}, now)
	cache.Set(sibling, netFileBatch{}, now)
	cache.Set(foreign, netFileBatch{Raw115: raw}, now)
	inflight := child
	inflight.Path = "50"
	generation := cache.Generation(inflight)
	cache.InvalidatePathTree("115", 1, "10")
	if _, ok := cache.Get(child, now); ok {
		t.Fatal("重命名或移动祖先后不能保留旧完整路径")
	}
	for _, key := range []netFileBatchCacheKey{sibling, foreign} {
		if _, ok := cache.Get(key, now); !ok {
			t.Fatalf("不相关缓存被清理：%+v", key)
		}
	}
	if cache.SetIfGeneration(inflight, netFileBatch{Raw115: raw}, now, generation) {
		t.Fatal("尚未返回祖先链的旧请求不能回填")
	}
}

func TestNetFileSharedFetchCannotOverwriteConcurrentRefresh(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cache := newNetFileBatchCache(2, time.Minute)
		key := netFileBatchCacheKey{SourceType: "openlist", AccountID: 1, Path: "/parent"}
		cache.InvalidatePath(key.SourceType, key.AccountID, key.Path)
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			_, _, err := cache.getOrFetch(t.Context(), key, false, func(context.Context) (netFileBatch, error) {
				<-release
				return netFileBatch{Total: 1}, nil
			})
			done <- err
		}()
		synctest.Wait()
		if _, _, err := cache.getOrFetch(t.Context(), key, true, func(context.Context) (netFileBatch, error) {
			return netFileBatch{Total: 2}, nil
		}); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if batch, ok := cache.Get(key, time.Now()); !ok || batch.Total != 2 {
			t.Fatalf("同代次的普通请求不能覆盖刷新批次：%+v %t", batch, ok)
		}
	})
}
