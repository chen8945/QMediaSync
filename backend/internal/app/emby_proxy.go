package app

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/backup"
	"qmediasync/internal/emby302"
	"qmediasync/internal/emby302/config"
	embypreload "qmediasync/internal/emby302/service/emby"
	emby302https "qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/web/webport"
	"qmediasync/internal/helpers"
	embyclientrestgo "qmediasync/internal/medialibrary/client"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
)

func (app *App) startEmby302() error {
	options, err := app.emby302Options()
	if err != nil {
		return err
	}
	app.embyProxy = emby302.NewService(options)
	stored, err := models.ReadMediaLibraryConfigSnapshot(models.MediaServerEmby)
	if err == gorm.ErrRecordNotFound {
		stored = &models.EmbyConfig{}
	} else if err != nil {
		return err
	}
	return app.applyEmby302(context.Background(), stored)
}

func (app *App) emby302Options() (emby302.Options, error) {
	data, err := fs.ReadFile(build.Resources, "assets/emby302.yaml")
	if err != nil {
		return emby302.Options{}, fmt.Errorf("读取 Emby 302 内嵌配置失败：%w", err)
	}
	if err := config.ReadFromFile(data); err != nil {
		return emby302.Options{}, err
	}
	config.C.Emby.ImagesOriginal = helpers.GlobalConfig.Emby302.ImagesOriginal
	config.C.Emby.EpisodesUnplayPrior = false
	config.C.VideoPreview.Enable = true
	config.C.VideoPreview.Containers = []string{"strm"}
	config.BasePath = helpers.ConfigDir
	certFile, keyFile := filepath.Join(helpers.ConfigDir, "server.crt"), filepath.Join(helpers.ConfigDir, "server.key")
	if helpers.PathExists(certFile) && helpers.PathExists(keyFile) {
		config.C.Ssl.Enable = true
		config.C.Ssl.SinglePort = false
		config.C.Ssl.Crt, config.C.Ssl.Key = "server.crt", "server.key"
	}
	options := emby302.Options{
		CertificateFile: certFile, KeyFile: keyFile,
		ClientOptions: emby302https.ClientOptions{InsecureSkipVerify: helpers.GlobalConfig.Emby302.InsecureSkipVerify},
		Guard:         newEmby302Guard, RuntimeAcquire: backup.AcquireRuntimeRequest,
		Preload: embypreload.PreloadOptions{
			Instance: "emby302", TrustedOrigin: app.isTrustedPreloadOrigin,
			Resolve: app.newEmbyPlaybackPreload, Coordinator: playback.DefaultPreloader,
			Account: app.playbackPreloadAccount,
		},
	}
	if !config.C.Ssl.Enable || !config.C.Ssl.SinglePort {
		options.HTTPAddress = "0.0.0.0:" + webport.HTTP
	}
	if config.C.Ssl.Enable {
		options.HTTPSAddress = "0.0.0.0:" + webport.HTTPS
	}
	if helpers.GlobalConfig.Emby302.InsecureSkipVerify {
		helpers.AppLogger.RequiredWarnf("Emby 302 已开启 insecure_skip_verify，出站 HTTPS 请求将跳过证书校验，存在中间人攻击风险，仅建议在受控内网自签名证书场景临时使用")
	}
	return options, nil
}

func (app *App) applyEmby302(ctx context.Context, stored *models.EmbyConfig) error {
	if app.embyProxy == nil {
		return fmt.Errorf("Emby 302 模块未初始化")
	}
	return app.embyProxy.Apply(ctx, emby302.Configuration{Enabled: stored.Enabled, URL: stored.EmbyUrl, APIKey: stored.EmbyApiKey})
}

func newEmby302Guard(serverURL, apiKey string, transport http.RoundTripper) gin.HandlerFunc {
	client := embyclientrestgo.NewClientForProduct(serverURL, apiKey, embyclientrestgo.ProductEmby, transport)
	return func(c *gin.Context) {
		if _, err := client.CheckConnection(c.Request.Context()); err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"message": err.Error()})
		}
	}
}
