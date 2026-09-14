package cache

import (
	"strconv"
	"sync"
	"time"

	"qmediasync/emby302/config"
	"qmediasync/emby302/util/strs"
)

const (

	// MaxCacheSize 缓存最大大小（字节）
	//
	// 这里的大小指的是响应体大小, 实际占用大小可能略大一些
	MaxCacheSize int64 = 100 * 1024 * 1024

	// MaxCacheNum 最多缓存多少个请求信息
	MaxCacheNum = 8092

	// HeaderKeyExpired 缓存截止时间（Unix 毫秒），-1 表示不缓存
	HeaderKeyExpired = "Expired"
)

// currentCacheSize 当前内存中的缓存大小（字节）
var currentCacheSize int64 = 0

// DefaultExpired 默认的请求过期时间
//
// 可通过设置 "Expired" 响应头进行覆盖
var DefaultExpired = func() time.Duration { return config.C.Cache.ExpiredDuration() }

// cacheMap 存放缓存数据的 map
var cacheMap = sync.Map{}

// preCacheChan 预缓存通道
//
// 缓存数据先暂存在通道中, 再由专门的 goroutine 单线程处理
//
// preCacheChan 的淘汰规则是先入先淘汰, 不管缓存对象的过期时间
var preCacheChan = make(chan *respCache, MaxCacheNum)

// cacheHandleWaitGroup 允许等待预缓存通道处理完毕后再获取数据
var cacheHandleWaitGroup = sync.WaitGroup{}

func init() {
	go loopMaintainCache()
}

// loopMaintainCache 由单独的 goroutine 维护 cacheMap
func loopMaintainCache() {

	// cleanCache 清理缓存数据
	cleanCache := func() {
		validCnt := 0
		nowMillis := time.Now().UnixMilli()
		toDelete := make([]*respCache, 0, 1<<3)

		cacheMap.Range(func(key, value any) bool {
			rc := value.(*respCache)
			if nowMillis >= rc.expired || validCnt == MaxCacheNum || currentCacheSize > MaxCacheSize {
				toDelete = append(toDelete, rc)
			} else {
				validCnt++
			}
			return true
		})

		for _, rc := range toDelete {
			cacheMap.Delete(rc.cacheKey)
			rc.mu.RLock()
			currentCacheSize -= int64(len(rc.body))
			rc.mu.RUnlock()
			delSpaceCache(rc.header.space, rc.header.spaceKey, rc)
		}
	}

	// putrespCache 将缓存对象维护到 cacheMap 中
	//
	// 同时淘汰掉过期缓存
	putrespCache := func(rc *respCache) {
		rc.mu.RLock()
		bodySize := int64(len(rc.body))
		rc.mu.RUnlock()
		// 排队期间也可能过期，不能将旧响应重新发布到任何缓存入口。
		if rc.expired <= time.Now().UnixMilli() {
			return
		}
		if previous, loaded := cacheMap.Swap(rc.cacheKey, rc); loaded {
			old := previous.(*respCache)
			old.mu.RLock()
			currentCacheSize -= int64(len(old.body))
			old.mu.RUnlock()
			delSpaceCache(old.header.space, old.header.spaceKey, old)
		}
		currentCacheSize += bodySize
		space, spaceKey := rc.header.space, rc.header.spaceKey
		if strs.AllNotEmpty(space, spaceKey) {
			putSpaceCache(space, spaceKey, rc)
		}
	}

	timer := time.NewTicker(time.Second * 10)
	defer timer.Stop()
	for {
		select {
		case rc := <-preCacheChan:
			putrespCache(rc)
			cacheHandleWaitGroup.Done()
		case <-timer.C:
			cleanCache()
		}
	}
}

// getCache 根据 cacheKey 获取缓存
func getCache(cacheKey string) (*respCache, bool) {
	if c, ok := cacheMap.Load(cacheKey); ok {
		rc := c.(*respCache)
		if rc.expired > time.Now().UnixMilli() {
			return rc, true
		}
	}
	return nil, false
}

// putCache 设置缓存
func putCache(cacheKey string, code int, respBody []byte, respHeader respHeader) {
	if cacheKey == "" || respBody == nil {
		return
	}

	// 计算缓存过期时间
	nowMillis := time.Now().UnixMilli()
	var expiredMillis int64
	if respHeader.expired == "" {
		expiredMillis = DefaultExpired().Milliseconds() + nowMillis
	} else {
		var err error
		expiredMillis, err = strconv.ParseInt(respHeader.expired, 10, 64)
		if err != nil {
			return
		}
	}
	if expiredMillis <= nowMillis {
		return
	}

	rc := &respCache{
		code:     code,
		body:     respBody,
		cacheKey: cacheKey,
		expired:  expiredMillis,
		header:   respHeader,
	}

	// 依据先进先淘汰原则, 将最新缓存放入预缓存通道
	cacheHandleWaitGroup.Add(1)
	for {
		select {
		case preCacheChan <- rc:
			return
		default:
			select {
			case <-preCacheChan:
				cacheHandleWaitGroup.Done()
			default:
			}
		}
	}
}
