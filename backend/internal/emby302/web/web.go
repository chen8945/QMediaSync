package web

import (
	"crypto/tls"
	"log"
	"net/http"
	"strings"
	"sync/atomic"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/emby302/web/webport"

	"github.com/gin-gonic/gin"
)

type listenerConfig struct {
	host   string
	apiKey string
}

var activeListener atomic.Pointer[listenerConfig]

// EmbyRestartRequired 比较已保存配置和本次进程实际采用的代理连接。
func EmbyRestartRequired(enabled bool, serverURL, apiKey string) bool {
	active := activeListener.Load()
	wanted := enabled && strings.TrimSpace(serverURL) != "" && strings.TrimSpace(apiKey) != ""
	if active == nil {
		return wanted
	}
	return !wanted || active.host != serverURL || active.apiKey != apiKey
}

// Listen 监听指定端口；远端检查只在业务请求到达时执行。
func Listen(guard gin.HandlerFunc, apiKey string) error {
	initRulePatterns()
	activeListener.Store(&listenerConfig{host: config.C.Emby.Host, apiKey: apiKey})

	errChanHTTP, errChanHTTPS := make(chan error, 1), make(chan error, 1)
	if !config.C.Ssl.Enable {
		go listenHTTP(errChanHTTP, guard)
	} else if config.C.Ssl.SinglePort {
		go listenHTTPS(errChanHTTPS, guard)
	} else {
		go listenHTTP(errChanHTTP, guard)
		go listenHTTPS(errChanHTTPS, guard)
	}

	select {
	case err := <-errChanHTTP:
		log.Fatal("HTTP 服务异常: ", err)
	case err := <-errChanHTTPS:
		log.Fatal("HTTPS 服务异常: ", err)
	}
	return nil
}

// initRouter 初始化路由引擎
func initRouter(r *gin.Engine, guard gin.HandlerFunc) {
	r.Use(referrerPolicySetter())
	if guard != nil {
		r.Use(guard)
	}
	r.Use(emby.ApiKeyChecker())
	r.Use(emby.DownloadStrategyChecker())
	r.Use(emby.MediaPathDisplay())
	r.Use(emby.PreloadObserver())
	if config.C.Cache.Enable {
		r.Use(cache.CacheableRouteMarker())
		r.Use(cache.RequestCacher())
	}
	initRoutes(r)
}

// listenHTTP 在指定端口上监听 HTTP 服务
//
// 出现错误时, 会写入 errChan 中
func listenHTTP(errChan chan error, guard gin.HandlerFunc) {
	r := gin.New()
	r.Use(gin.Recovery())
	// r.Use(CustomLogger(webport.HTTP))
	r.Use(func(c *gin.Context) {
		c.Set(webport.GinKey, webport.HTTP)
	})
	initRouter(r, guard)
	logs.Info("在端口【%s】上启动 HTTP 服务", webport.HTTP)
	err := r.Run("0.0.0.0:" + webport.HTTP)
	errChan <- err
	close(errChan)
}

// listenHTTPS 在指定端口上监听 HTTPS 服务
//
// 出现错误时, 会写入 errChan 中
func listenHTTPS(errChan chan error, guard gin.HandlerFunc) {
	r := gin.New()
	r.Use(gin.Recovery())
	// r.Use(CustomLogger(webport.HTTPS))
	r.Use(func(c *gin.Context) {
		c.Set(webport.GinKey, webport.HTTPS)
	})
	initRouter(r, guard)
	logs.Info("在端口【%s】上启动 HTTPS 服务", webport.HTTPS)
	ssl := config.C.Ssl

	srv := &http.Server{
		Addr:    "0.0.0.0:" + webport.HTTPS,
		Handler: r,
	}
	// 禁用 HTTP/2
	srv.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}

	err := srv.ListenAndServeTLS(ssl.CrtPath(), ssl.KeyPath())
	errChan <- err
	close(errChan)
}
