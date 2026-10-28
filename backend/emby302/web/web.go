package web

import (
	"crypto/tls"
	"log"
	"net/http"

	"qmediasync/emby302/config"
	"qmediasync/emby302/service/emby"
	"qmediasync/emby302/util/logs"
	"qmediasync/emby302/web/cache"
	"qmediasync/emby302/web/webport"

	"github.com/gin-gonic/gin"
)

// Listen 监听指定端口
func Listen() error {
	initRulePatterns()

	errChanHTTP, errChanHTTPS := make(chan error, 1), make(chan error, 1)
	if !config.C.Ssl.Enable {
		go listenHTTP(errChanHTTP)
	} else if config.C.Ssl.SinglePort {
		go listenHTTPS(errChanHTTPS)
	} else {
		go listenHTTP(errChanHTTP)
		go listenHTTPS(errChanHTTPS)
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
func initRouter(r *gin.Engine) {
	r.Use(referrerPolicySetter())
	r.Use(emby.ApiKeyChecker())
	r.Use(emby.DownloadStrategyChecker())
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
func listenHTTP(errChan chan error) {
	r := gin.New()
	r.Use(gin.Recovery())
	// r.Use(CustomLogger(webport.HTTP))
	r.Use(func(c *gin.Context) {
		c.Set(webport.GinKey, webport.HTTP)
	})
	initRouter(r)
	logs.Info("在端口【%s】上启动 HTTP 服务", webport.HTTP)
	err := r.Run("0.0.0.0:" + webport.HTTP)
	errChan <- err
	close(errChan)
}

// listenHTTPS 在指定端口上监听 HTTPS 服务
//
// 出现错误时, 会写入 errChan 中
func listenHTTPS(errChan chan error) {
	r := gin.New()
	r.Use(gin.Recovery())
	// r.Use(CustomLogger(webport.HTTPS))
	r.Use(func(c *gin.Context) {
		c.Set(webport.GinKey, webport.HTTPS)
	})
	initRouter(r)
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
