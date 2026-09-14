// 缓存空间功能会将特定请求的响应缓存分类整理,
// 便于后续其他请求复用响应。
package cache

import (
	"sync"
	"time"

	"qmediasync/emby302/util/strs"
)

const (

	// HeaderKeySpace 缓存空间 key
	HeaderKeySpace = "Space"

	// HeaderKeySpaceKey 缓存空间内部 key
	HeaderKeySpaceKey = "Space-Key"
)

// spaceMap 缓存空间
//
// 三层结构: map[string]map[string]*respCache
var spaceMap = sync.Map{}

// GetSpaceCache 获取缓存空间的缓存对象
func GetSpaceCache(space, spaceKey string) (RespCache, bool) {
	if strs.AnyEmpty(space, spaceKey) {
		return nil, false
	}
	s := getSpace(space)
	rc, ok := getSpaceCache(s, spaceKey)
	if !ok {
		return nil, false
	}
	return rc, true
}

// putSpaceCache 设置缓存到缓存空间中
func putSpaceCache(space, spaceKey string, cache *respCache) {
	if strs.AnyEmpty(space, spaceKey) {
		return
	}
	getSpace(space).Store(spaceKey, cache)
}

func delSpaceCache(space, spaceKey string, expected *respCache) {
	if strs.AnyEmpty(space, spaceKey) {
		return
	}
	getSpace(space).CompareAndDelete(spaceKey, expected)
}

// getSpace 获取缓存空间
//
// 不存在指定名称的空间时, 初始化一个新的空间
func getSpace(space string) *sync.Map {
	if strs.AnyEmpty(space) {
		return nil
	}
	s, _ := spaceMap.LoadOrStore(space, new(sync.Map))
	return s.(*sync.Map)
}

// getSpaceCache 获取缓存空间中的某个缓存
func getSpaceCache(space *sync.Map, spaceKey string) (*respCache, bool) {
	if space == nil || strs.AnyEmpty(spaceKey) {
		return nil, false
	}
	if cache, ok := space.Load(spaceKey); ok {
		rc := cache.(*respCache)
		if rc.expired > time.Now().UnixMilli() {
			return rc, true
		}
	}
	return nil, false
}
