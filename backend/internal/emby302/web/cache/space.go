package cache

import "time"

const (
	HeaderKeySpace    = "Space"
	HeaderKeySpaceKey = "Space-Key"
)

// GetSpaceCache 获取当前连接配置的空间缓存。
func (scope *Scope) GetSpaceCache(space, key string) (RespCache, bool) {
	s := scope.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if scope.closed || s.closed {
		return nil, false
	}
	rc := scope.spaces[space][key]
	if rc == nil || rc.expired <= time.Now().UnixMilli() {
		return nil, false
	}
	return rc, true
}

func (scope *Scope) putSpaceCacheLocked(space, key string, rc *respCache) {
	if space == "" || key == "" {
		return
	}
	values := scope.spaces[space]
	if values == nil {
		values = make(map[string]*respCache)
		scope.spaces[space] = values
	}
	values[key] = rc
}

func (scope *Scope) delSpaceCacheLocked(space, key string, expected *respCache) {
	values := scope.spaces[space]
	if values[key] == expected {
		delete(values, key)
	}
	if len(values) == 0 {
		delete(scope.spaces, space)
	}
}
