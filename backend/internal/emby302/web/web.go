package web

import (
	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/service/m3u8"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/emby302/web/webport"

	"github.com/gin-gonic/gin"
)

// NewRouter 的所有连接处理器绑定同一会话，监听由上层 Service 拥有。
func NewRouter(session *emby.Session, guard gin.HandlerFunc, playlists *m3u8.Store) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), func(c *gin.Context) {
		port := webport.HTTP
		if c.Request.TLS != nil {
			port = webport.HTTPS
		}
		c.Set(webport.GinKey, port)
	}, referrerPolicySetter())
	if guard != nil {
		r.Use(guard)
	}
	r.Use(session.ApiKeyChecker(), session.DownloadStrategyChecker(), emby.MediaPathDisplay(), session.PreloadObserver())
	if config.C.Cache.Enable {
		r.Use(cache.CacheableRouteMarker(), session.Cache.RequestCacher())
	}
	r.Any("/*vars", defaultHandler(rulePatterns(session, playlists)))
	return r
}
