package main

import (
	"archive/zip"
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/emby302/web/webport"
	"qmediasync/internal/backup"
	"qmediasync/internal/controllers"
	"qmediasync/internal/db"
	"qmediasync/internal/fnosproxy"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type fnosRestoreItem struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type fnosPlaybackTransport func(*http.Request) (*http.Response, error)

func (transport fnosPlaybackTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return transport(r)
}

func TestFnosPlaybackRestoreAdmission(t *testing.T) {
	// 恢复门禁按契约不再开放，独立进程避免影响其他应用测试。
	const childEnv = "QMS_TEST_FNOS_RESTORE_ADMISSION"
	if os.Getenv(childEnv) != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestFnosPlaybackRestoreAdmission$", "-test.timeout=20s")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("飞牛播放恢复门禁回归失败：%v\n%s", err, output)
		}
		return
	}
	setupFnosStartupTest(t)
	databaseFile := filepath.Join(t.TempDir(), "restore.db")
	database := db.InitSqlite3(databaseFile)
	db.Db = database
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	models.AllTables = []any{&fnosRestoreItem{}}
	helpers.ConfigDir = t.TempDir()
	if err := database.AutoMigrate(&fnosRestoreItem{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&fnosRestoreItem{ID: 1, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "restore.zip")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("fnosRestoreItem.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(entry, "{\"ID\":1,\"Name\":\"restored\"}\n"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.Use(controllers.RestoreMaintenanceMiddleware())
	router.GET("/proxy-115", controllers.Proxy115)
	router.GET("/115/newurl", func(c *gin.Context) { c.String(http.StatusOK, "source") })
	handler := fnosPlaybackHandler(router)
	releaseVideo := make(chan struct{})
	finishVideo := sync.OnceFunc(func() { close(releaseVideo) })
	defer finishVideo()
	started := make(chan struct{}, 2)
	previousTransport := http.DefaultTransport
	http.DefaultTransport = fnosPlaybackTransport(func(r *http.Request) (*http.Response, error) {
		var body io.ReadCloser = io.NopCloser(strings.NewReader("head body must not be read"))
		if r.Method == http.MethodGet {
			reader, writer := io.Pipe()
			body = reader
			go func() { <-releaseVideo; _ = writer.Close() }()
			started <- struct{}{}
		}
		return &http.Response{StatusCode: http.StatusPartialContent, Header: http.Header{"Content-Range": {"bytes 0-9/10"}}, Body: body, Request: r}, nil
	})
	defer func() { http.DefaultTransport = previousTransport }()
	endpoints := []string{
		"/proxy-115?url=https%3A%2F%2Fcdn.115cdn.net%2Fmovie",
		"/proxy-115?baidupan=1&url=https%3A%2F%2Fd.pcs.baidu.com%2Fmovie",
	}
	var videos sync.WaitGroup
	for _, endpoint := range endpoints {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodHead, endpoint, nil))
		if response.Code != http.StatusPartialContent || response.Body.Len() != 0 || response.Header().Get("Content-Range") == "" {
			t.Fatalf("HEAD 语义变化：%d %s", response.Code, response.Body)
		}
		videos.Go(func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, endpoint, nil))
		})
	}
	defer videos.Wait()
	defer finishVideo()
	for range endpoints {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("视频未进入共享代理")
		}
	}
	restored := make(chan error, 1)
	go func() { restored <- backup.Restore(archive) }()
	select {
	case err := <-restored:
		if err != nil {
			t.Fatalf("视频转发期间恢复失败：%v", err)
		}
	case <-time.After(3 * time.Second):
		finishVideo()
		<-restored
		t.Fatal("恢复仍在等待未结束的视频流")
	}
	select {
	case <-releaseVideo:
		t.Fatal("测试未保持视频流打开")
	default:
	}
	observer, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	observerPool, err := observer.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer observerPool.Close()
	var item fnosRestoreItem
	if err := observer.First(&item, 1).Error; err != nil || item.Name != "restored" {
		t.Fatalf("恢复未写入真实数据：%+v %v", item, err)
	}
	for _, endpoint := range append(endpoints, "/115/newurl") {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, endpoint, nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("维护后仍放行 %s %s：%d", method, endpoint, response.Code)
			}
		}
	}
}

func setupFnosStartupTest(t *testing.T) {
	t.Helper()
	previousDB, previousConfig, previousLogger, previousRoot := db.Db, helpers.GlobalConfig, helpers.AppLogger, helpers.RootDir
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fnos.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	db.Db = conn
	if err := conn.AutoMigrate(&models.FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	helpers.GlobalConfig.HttpHost, helpers.GlobalConfig.HttpsHost = "127.0.0.1:12333", "127.0.0.1:12332"
	helpers.RootDir = t.TempDir()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		sqlDB, err := conn.DB()
		if err != nil {
			t.Error(err)
		} else if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
		db.Db, helpers.GlobalConfig, helpers.AppLogger, helpers.RootDir = previousDB, previousConfig, previousLogger, previousRoot
	})
}

func TestFnosProxyDisabledStartupAndPendingSave(t *testing.T) {
	setupFnosStartupTest(t)
	router := gin.New()
	router.Use(controllers.RestoreMaintenanceMiddleware())
	router.GET("/original", func(c *gin.Context) { c.String(http.StatusOK, "original") })
	app := &App{}
	app.startFnosProxy(router)
	t.Cleanup(func() {
		if err := app.fnosProxy.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	saved, err := models.ReadFnosProxyConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	status := app.fnosProxy.Status(saved)
	if status.Running || status.RestartRequired || status.LastError != "" || status.ListenPort != 0 || status.HTTPSPort != 0 {
		t.Fatalf("disabled startup status: %+v", status)
	}
	var count int64
	if err := db.Db.Model(&models.FnosProxyConfig{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("disabled startup created a row: count=%d err=%v", count, err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/setting/fnos-proxy", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("management API is not protected: %d %s", response.Code, response.Body)
	}
	saved.Enabled, saved.FnosURL = true, "http://127.0.0.1:8005"
	if err := models.SaveFnosProxyConfig(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	status = app.fnosProxy.Status(saved)
	if status.Running || !status.RestartRequired || status.ListenPort != 0 {
		t.Fatalf("saving unexpectedly enabled a listener: %+v", status)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/original", nil))
	if response.Code != http.StatusOK || response.Body.String() != "original" {
		t.Fatalf("original route changed: %d %s", response.Code, response.Body)
	}
}

func TestFnosProxyStartupFailureKeepsOriginalServer(t *testing.T) {
	setupFnosStartupTest(t)
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(fnosproxy.DefaultHTTPPort))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	config := models.FnosProxyConfig{Enabled: true, FnosURL: "http://127.0.0.1:8005"}
	if err := models.SaveFnosProxyConfig(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/original", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	app := &App{}
	app.startFnosProxy(router)
	defer app.fnosProxy.Stop(context.Background())
	status := app.fnosProxy.Status(config)
	if status.Running || status.LastError == "" || status.ListenPort != 0 || status.HTTPSPort != 0 {
		t.Fatalf("HTTP失败状态：%+v", status)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/original", nil))
	if response.Code != http.StatusNoContent {
		t.Fatal("飞牛启动失败影响了原主站路由")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.fnosProxy.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	status = app.fnosProxy.Status(config)
	if !status.Running || status.ListenPort != fnosproxy.DefaultHTTPPort || status.RestartRequired || status.LastError != "" {
		t.Fatalf("释放占用后无需重启即可重试：%+v", status)
	}
}

func TestFnosProxyStartupReservesMainAndEmbyPorts(t *testing.T) {
	for _, test := range []struct {
		name string
		port int
	}{
		{"main-http", fnosproxy.DefaultHTTPPort},
		{"main-http", fnosproxy.DefaultHTTPSPort},
		{"main-https", fnosproxy.DefaultHTTPPort},
		{"main-https", fnosproxy.DefaultHTTPSPort},
		{"emby-http", fnosproxy.DefaultHTTPPort},
		{"emby-https", fnosproxy.DefaultHTTPSPort},
	} {
		t.Run(test.name+strconv.Itoa(test.port), func(t *testing.T) {
			setupFnosStartupTest(t)
			previousRelease := helpers.IsRelease
			helpers.IsRelease = true
			t.Cleanup(func() { helpers.IsRelease = previousRelease })
			previousHTTP, previousHTTPS := webport.HTTP, webport.HTTPS
			t.Cleanup(func() { webport.HTTP, webport.HTTPS = previousHTTP, previousHTTPS })
			address := net.JoinHostPort("127.0.0.1", strconv.Itoa(test.port))
			switch test.name {
			case "main-http":
				helpers.GlobalConfig.HttpHost = address
			case "main-https":
				helpers.GlobalConfig.HttpsHost = address
			case "emby-http":
				webport.HTTP = strconv.Itoa(test.port)
			case "emby-https":
				webport.HTTPS = strconv.Itoa(test.port)
			}
			config := models.FnosProxyConfig{Enabled: true, FnosURL: "http://127.0.0.1:8005"}
			if err := models.SaveFnosProxyConfig(t.Context(), config); err != nil {
				t.Fatal(err)
			}
			app := &App{}
			app.startFnosProxy(gin.New())
			t.Cleanup(func() { _ = app.fnosProxy.Stop(context.Background()) })
			status := app.fnosProxy.Status(config)
			if status.Running || !status.RestartRequired || status.LastError == "" {
				t.Fatalf("启动时接受了保留端口冲突：%+v", status)
			}
			if err := app.fnosProxy.Apply(t.Context(), config); err == nil {
				t.Fatal("重试应用绕过了保留端口检查")
			}
			listener, err := net.Listen("tcp", address)
			if err != nil {
				t.Fatalf("飞牛抢占了主站或 Emby 的保留端口：%v", err)
			}
			_ = listener.Close()
		})
	}
}

func TestFnosProxyTrustedRootsAndAccountScopedCandidates(t *testing.T) {
	setupFnosStartupTest(t)
	if err := db.Db.AutoMigrate(&models.Settings{}, &models.SyncPath{}, &models.SyncFile{}, &models.Account{}, &models.ScrapePath{}); err != nil {
		t.Fatal(err)
	}
	settings := models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: "http://private-qms:12333"}}
	if err := db.Db.Create(&settings).Error; err != nil {
		t.Fatal(err)
	}
	previous := models.SettingsGlobal
	models.SettingsGlobal = &settings
	t.Cleanup(func() { models.SettingsGlobal = previous })
	root := t.TempDir()
	owner := models.Account{SourceType: models.SourceType115, UserId: "owner"}
	other := models.Account{SourceType: models.SourceType115, UserId: "other"}
	for _, account := range []*models.Account{&owner, &other} {
		if err := db.Db.Create(account).Error; err != nil {
			t.Fatal(err)
		}
		path := models.SyncPath{LocalPath: root, RemotePath: "Movies", SourceType: models.SourceType115, AccountId: account.ID}
		path.BaseCid = account.UserId
		if err := db.Db.Create(&path).Error; err != nil {
			t.Fatal(err)
		}
		file := models.SyncFile{AccountId: account.ID, SyncPathId: path.ID, SourceType: models.SourceType115,
			PickCode: "same-code", FileId: account.UserId, IsVideo: true,
			LocalFilePath: filepath.Join(path.GetFullLocalPath(), account.UserId+".strm")}
		if err := db.Db.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
	}
	roots, err := fnosOutputRoots(t.Context())
	if err != nil || len(roots) != 2 || roots[0] != filepath.Join(root, "Movies") {
		t.Fatalf("output roots lost the source directory: %v %v", roots, err)
	}
	origins, err := fnosKnownOrigins(t.Context())
	if err != nil || len(origins) != 1 || origins[0] != settings.StrmBaseUrl {
		t.Fatalf("current QMS origin: %v %v", origins, err)
	}
	source := fnosproxy.Source{URL: settings.StrmBaseUrl + "/115/newurl?userid=owner&pickcode=same-code"}
	candidates, err := fnosIdentityCandidates(t.Context(), source)
	if err != nil || len(candidates) != 1 || candidates[0].Path != filepath.Join(root, "Movies", "owner.strm") {
		t.Fatalf("same pickcode mixed accounts: %+v %v", candidates, err)
	}
	source.URL = "http://other-qms:12333/115/newurl?userid=owner&pickcode=same-code"
	candidates, err = fnosIdentityCandidates(t.Context(), source)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("unrecognized origin supplied candidates: %+v %v", candidates, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := fnosOutputRoots(ctx); err == nil {
		t.Fatal("canceled request still read output roots")
	}
}
