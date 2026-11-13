package emby

import (
	"context"
	"net/http"
	"sync"

	"qmediasync/internal/emby302/web/cache"
)

// SessionOptions 只保存一条已提交连接需要的依赖。
type SessionOptions struct {
	Origin       string
	Client       *http.Client
	Cache        *cache.Scope
	Assets       *CustomAssets
	Preload      PreloadOptions
	RunAuxiliary func(func(context.Context)) bool
}

// Session 在一次请求及其辅助工作期间固定连接、授权和响应缓存。
type Session struct {
	Origin       string
	Client       *http.Client
	Cache        *cache.Scope
	RunAuxiliary func(func(context.Context)) bool
	assets       *CustomAssets
	preload      *embyPreloader
	validApiKeys sync.Map
}

// NewSession 构造独立上游身份，不探测远端。
func NewSession(options SessionOptions) *Session {
	return &Session{Origin: options.Origin, Client: options.Client, Cache: options.Cache,
		RunAuxiliary: options.RunAuxiliary, assets: options.Assets, preload: newPreloader(options.Preload, options.Origin, options.Client)}
}

// Deactivate 在发布锁内撤销后台准入，实际取消和清理在锁外执行。
func (s *Session) Deactivate() { s.preload.stopped.Store(true) }

// Retire 不取消已进入的业务请求。
func (s *Session) Retire() { s.preload.stop() }

// Close 由模块在请求引用归零后清理本会话响应。
func (s *Session) Close() { s.Retire(); s.Cache.Close() }
