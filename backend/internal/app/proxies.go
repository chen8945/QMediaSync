package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"strconv"

	"qmediasync/internal/backup"
	"qmediasync/internal/controllers"
	"qmediasync/internal/db"
	"qmediasync/internal/emby302/web/webport"
	"qmediasync/internal/fnosproxy"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"

	"github.com/gin-gonic/gin"
)

func fnosPlaybackHandler(mainHandler http.Handler) http.Handler {
	videoProxy := gin.New()
	videoProxy.Match([]string{http.MethodGet, http.MethodHead}, "/proxy-115", func(c *gin.Context) {
		release, admitted := backup.AcquireRuntimeRequest()
		if !admitted {
			c.String(http.StatusServiceUnavailable, "QMediaSync 正在恢复数据库，请重启后再试")
			return
		}
		// 视频转发不访问数据库，重新检查准入后立即释放，避免恢复等待整个视频流。
		release()
		controllers.Proxy115(c)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxy-115" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
			videoProxy.ServeHTTP(w, r)
			return
		}
		mainHandler.ServeHTTP(w, r)
	})
}

func (app *App) startFnosProxy(mainHandler *gin.Engine) {
	httpsHost := helpers.GlobalConfig.HttpsHost
	if !helpers.IsRelease {
		httpsHost = "localhost:12332"
	}
	var reservedPorts []int
	for _, address := range []string{helpers.GlobalConfig.HttpHost, httpsHost} {
		if _, port, err := net.SplitHostPort(address); err == nil {
			if value, err := strconv.Atoi(port); err == nil && value > 0 {
				reservedPorts = append(reservedPorts, value)
			}
		}
	}
	for _, port := range []string{webport.HTTP, webport.HTTPS} {
		if value, err := strconv.Atoi(port); err == nil && value > 0 {
			reservedPorts = append(reservedPorts, value)
		}
	}
	app.fnosProxy = fnosproxy.NewService(fnosproxy.Options{
		MainHandler: fnosPlaybackHandler(mainHandler), ReservedPorts: reservedPorts,
		HTTPHost: helpers.GlobalConfig.HttpHost, HTTPSHost: httpsHost,
		CertificateFile: filepath.Join(helpers.RootDir, "config", "server.crt"),
		KeyFile:         filepath.Join(helpers.RootDir, "config", "server.key"),
		OutputRoots:     fnosOutputRoots, KnownOrigins: fnosKnownOrigins,
		OpenListOrigins: fnosOpenListOrigins, IdentityCandidates: fnosIdentityCandidates,
		Preloader: playback.DefaultPreloader, NewPreload: newPlaybackPreload,
	})
	controller := controllers.NewFnosProxyController(app.fnosProxy.Status, app.fnosProxy.Apply, reservedPorts...)
	api := mainHandler.Group("/api", controllers.JWTAuthMiddleware())
	api.GET("/setting/fnos-proxy", controller.GetConfig)
	api.POST("/setting/fnos-proxy", controller.SaveConfig)
	config, err := models.ReadFnosProxyConfig(context.Background())
	if err != nil {
		helpers.AppLogger.Errorf("读取飞牛影视反代启动配置失败：%v", err)
		return
	}
	if err := app.fnosProxy.Start(config); err != nil {
		helpers.AppLogger.Warnf("飞牛影视反代未能完全启动：%v", err)
	}
}

func fnosOutputRoots(ctx context.Context) ([]string, error) {
	var paths []models.SyncPath
	if err := db.Db.WithContext(ctx).Select("local_path", "remote_path", "source_type").Find(&paths).Error; err != nil {
		return nil, err
	}
	roots := make([]string, 0, len(paths))
	for _, path := range paths {
		if path.LocalPath != "" {
			roots = append(roots, path.GetFullLocalPath())
		}
	}
	var scrapePaths []models.ScrapePath
	if err := db.Db.WithContext(ctx).Select("dest_path").Where("source_type = ?", models.SourceTypeLocal).Find(&scrapePaths).Error; err != nil {
		return nil, err
	}
	for i := range scrapePaths {
		if scrapePaths[i].DestPath != "" {
			roots = append(roots, scrapePaths[i].DestPath)
		}
	}
	return roots, nil
}

func fnosKnownOrigins(ctx context.Context) ([]string, error) {
	var paths []models.SyncPath
	if err := db.Db.WithContext(ctx).Select("strm_base_url").Find(&paths).Error; err != nil {
		return nil, err
	}
	var origins []string
	if models.SettingsGlobal != nil {
		settings, _ := models.SettingsGlobal.StrmSnapshot()
		origins = append(origins, settings.StrmBaseUrl)
	}
	for _, path := range paths {
		if path.StrmBaseUrl != "" {
			origins = append(origins, path.StrmBaseUrl)
		}
	}
	return origins, nil
}

func fnosOpenListOrigins(ctx context.Context) ([]string, error) {
	var accounts []models.Account
	if err := db.Db.WithContext(ctx).Select("base_url").Where("source_type = ?", models.SourceTypeOpenList).Find(&accounts).Error; err != nil {
		return nil, err
	}
	origins := make([]string, 0, len(accounts))
	for _, account := range accounts {
		origins = append(origins, account.BaseUrl)
	}
	return origins, nil
}

func fnosIdentityCandidates(ctx context.Context, source fnosproxy.Source) ([]fnosproxy.Candidate, error) {
	identity, err := models.ResolveEmbySnapshotSourceIdentity(ctx, models.EmbySnapshotSource{Path: source.URL})
	if err != nil || identity.AccountID == 0 || identity.ObjectID == "" {
		return nil, err
	}
	query := db.Db.WithContext(ctx).Where("source_type = ? AND account_id = ? AND is_video = ?", identity.SourceType, identity.AccountID, true)
	if identity.SourceType == models.SourceTypeOpenList {
		query = query.Where("file_id = ?", identity.ObjectID)
	} else if identity.SourceType == models.SourceType115 || identity.SourceType == models.SourceTypeBaiduPan {
		query = query.Where("pick_code = ?", identity.ObjectID)
	} else {
		return nil, nil
	}
	var files []models.SyncFile
	if err := query.Limit(257).Find(&files).Error; err != nil {
		return nil, err
	}
	if len(files) > 256 {
		return nil, errors.New("飞牛影视 STRM 候选过多，请填写路径映射")
	}
	candidates := make([]fnosproxy.Candidate, 0, len(files))
	for _, file := range files {
		if file.SyncPathId == 0 || file.LocalFilePath == "" {
			continue
		}
		var path models.SyncPath
		if err := db.Db.WithContext(ctx).First(&path, file.SyncPathId).Error; err != nil {
			return nil, err
		}
		if path.SourceType != identity.SourceType || path.AccountId != identity.AccountID {
			continue
		}
		candidates = append(candidates, fnosproxy.Candidate{Root: path.GetFullLocalPath(), Path: file.LocalFilePath, URL: source.URL})
	}
	return candidates, nil
}
