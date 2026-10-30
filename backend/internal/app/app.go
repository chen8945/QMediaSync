package app

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"

	"qmediasync/internal/controllers"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/emby"
	"qmediasync/internal/fnosproxy"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	"qmediasync/internal/realtime"
	"qmediasync/internal/synccron"
	"qmediasync/internal/syncstrm"
	v115open "qmediasync/internal/v115/openapi"

	"github.com/gin-gonic/gin"
)

var QMSApp *App

type App struct {
	isRelease       bool
	httpServer      *http.Server
	httpsServer     *http.Server
	fnosProxy       *fnosproxy.Service
	version         string
	publishDate     string
	preloadCancel   context.CancelFunc
	preloadOrigins  atomic.Pointer[trustedPreloadOrigins]
	stopEmbyPreload func()
}

func (app *App) Start() {
	app.startPlaybackPreloading(context.Background())
	// 启动外网 302 服务
	app.startEmby302()
	if helpers.IsRelease {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(controllers.Cors())
	setRouter(r)
	app.startFnosProxy(r)
	app.StartHttpServer(r)
	app.StartHttpsServer(r)
	if runtime.GOOS == "windows" {
		// 监听 Ctrl+C 信号
		go func() {
			quit := make(chan os.Signal, 1)
			signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
			<-quit
			log.Println("收到 Ctrl+C 信号")
			helpers.StopApp()
		}()
		<-helpers.ExitChan
		log.Println("收到停止信号")
		app.Stop()
		close(helpers.ExitChan)
		log.Println("应用程序正常退出")
		return
	} else {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
		<-quit
		log.Println("收到停止信号")
		// 停止应用
		app.Stop()
		log.Println("应用程序正常退出")
	}
}

func (app *App) Stop() {
	// 先停止后台取链，再关闭代理、播放管理器及共享连接池。
	app.stopPlaybackPreloading()
	realtime.GlobalLifecycle.Shutdown()
	if app.fnosProxy != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := app.fnosProxy.Stop(ctx); err != nil {
			helpers.AppLogger.Warnf("停止飞牛影视反代失败：%v", err)
		}
		cancel()
	}
	app.shutdownHTTPServers()
	webhookCtx, cancelWebhook := context.WithTimeout(context.Background(), 10*time.Second)
	if err := emby.ShutdownWebhookWorker(webhookCtx); err != nil {
		helpers.AppLogger.Warnf("等待 Emby 通知后台处理结束失败：%v", err)
	}
	cancelWebhook()
	// 关闭同步任务执行队列
	synccron.PauseAllNewSyncQueues()
	// 已交接账本使用独立生命周期；在日志关闭前有界取消和保存已知退出结果。
	ledgerCtx, cancelLedger := context.WithTimeout(context.Background(), 10*time.Second)
	if err := syncstrm.StopSyncBackgroundService(ledgerCtx); err != nil {
		helpers.AppLogger.Warnf("等待 STRM 同步文件记录更新结束失败：%v", err)
	}
	cancelLedger()
	// 关闭上传下载队列
	models.GlobalDownloadQueue.Stop()
	models.GlobalUploadQueue.Stop()
	// 关闭目录监控上传服务
	directoryupload.StopDirectoryUploadService()
	// 关闭 STRM 生成 worker
	syncstrm.StopStrmGenerationWorker()
	// 关闭定时任务（包含备份定时任务）
	synccron.GlobalCron.Stop()
	// 播放清理退出后再释放共享连接池；未完成的目录由下次定时维护回收。
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 10*time.Second)
	if err := playback.DefaultManager.Shutdown(cleanupCtx); err != nil {
		helpers.AppLogger.Warnf("等待 115 多端播放清理退出失败：%v", err)
	}
	cancelCleanup()
	if err := v115open.ClosePlaybackClient(); err != nil {
		helpers.AppLogger.Warnf("关闭 115 播放客户端失败：%v", err)
	}
	// 停止统计写入 worker，并在关闭数据库前尽量刷完已入队记录。
	if requestStatWriter != nil {
		requestStatWriter.Close()
	}
	helpers.CloseLogger() // 关闭日志
}

// shutdownHTTPServers 停止接收新请求并等待现有 handler 退出。
func (app *App) shutdownHTTPServers() {
	if app.httpServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.httpServer.Shutdown(ctx); err != nil {
			log.Println("HTTP Server Shutdown:", err)
		}
	}
	if app.httpsServer != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := app.httpsServer.Shutdown(ctx); err != nil {
			log.Println("HTTPS Server Shutdown:", err)
		}
	}
}

func (app *App) StartHttpsServer(r *gin.Engine) {
	certFile := filepath.Join(helpers.RootDir, "config", "server.crt")
	keyFile := filepath.Join(helpers.RootDir, "config", "server.key")
	if !helpers.PathExists(certFile) || !helpers.PathExists(keyFile) {
		return
	}
	go func() {
		// 在 12332 端口上启动 HTTPS 服务
		sslHost := ""
		// 启动 Web Server
		if !helpers.IsRelease {
			sslHost = "localhost:12332"
		} else {
			sslHost = helpers.GlobalConfig.HttpsHost
		}
		app.httpsServer = &http.Server{
			Addr:    sslHost,
			Handler: r,
		}
		// 没有证书则回退到普通 HTTP
		weberr := app.httpsServer.ListenAndServeTLS(certFile, keyFile)
		if weberr != nil {
			fmt.Println("ListenAndServe error:", weberr)
		}
	}()
}

func (app *App) StartHttpServer(r *gin.Engine) {
	host := helpers.GlobalConfig.HttpHost
	// 同时在 12333 端口上启动 HTTP 服务
	app.httpServer = &http.Server{
		Addr:    host,
		Handler: r,
	}
	go func() {
		weberr := app.httpServer.ListenAndServe()
		if weberr != nil {
			fmt.Println("ListenAndServe error:", weberr)
		}
	}()
}

func newApp() {
	if QMSApp != nil {
		log.Println("App 已经初始化，不能再次初始化")
		return
	}
	// 初始化 App
	QMSApp = &App{
		isRelease:   helpers.IsRelease,
		version:     build.Version,
		publishDate: build.PublishDate,
	}
}
