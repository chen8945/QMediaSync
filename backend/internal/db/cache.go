package db

import (
	"errors"

	"qmediasync/internal/helpers"

	"github.com/coocood/freecache"
)

type CacheGlobal struct {
	CacheInstance *freecache.Cache
	CacheSize     int
}

var Cache CacheGlobal
var DefaultExpire = 300 // 默认 5 分钟过期

// ErrCacheUnavailable 表示内存缓存尚未初始化。
var ErrCacheUnavailable = errors.New("内存缓存未初始化")

// 初始化缓存
func InitCache() {
	cacheSize := helpers.GlobalConfig.CacheSize
	Cache = CacheGlobal{
		CacheInstance: freecache.NewCache(cacheSize),
		CacheSize:     cacheSize,
	}
	helpers.AppLogger.Info("成功初始化内存缓存组件")
}

// Set 写入内存缓存；expire 设置为 -1 时使用默认期限。
func (c *CacheGlobal) Set(key string, value []byte, expire int) error {
	if c == nil || c.CacheInstance == nil {
		return ErrCacheUnavailable
	}
	keyHash := helpers.MD5Hash(key)
	keyBytes := []byte(keyHash)
	if expire == -1 {
		expire = DefaultExpire
	}
	return c.CacheInstance.Set(keyBytes, value, expire)
}

func (c *CacheGlobal) Get(key string) []byte {
	keyHash := helpers.MD5Hash(key)
	keyBytes := []byte(keyHash)
	value, err := c.CacheInstance.Get(keyBytes)
	if err != nil {
		return nil
	}
	return value
}

func (c *CacheGlobal) Delete(key string) {
	keyHash := helpers.MD5Hash(key)
	keyBytes := []byte(keyHash)
	c.CacheInstance.Del(keyBytes)
}
