package app

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/controllers"
	"qmediasync/internal/db"
	"qmediasync/internal/emby302"
	"qmediasync/internal/emby302/config"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"golang.org/x/net/websocket"
	"gorm.io/gorm"
)

func TestRestoreDoesNotWaitForEmbyWebSocket(t *testing.T) {
	const childEnv = "QMS_TEST_RESTORE_EMBY_WEBSOCKET"
	if os.Getenv(childEnv) != "1" {
		// 恢复永久关闭维护门禁，使用独立进程。
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRestoreDoesNotWaitForEmbyWebSocket$", "-test.timeout=20s")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("WebSocket restore integration failed: %v\n%s", err, output)
		}
		return
	}
	setupFnosStartupTest(t)
	helpers.ConfigDir = t.TempDir()
	databaseFile := filepath.Join(t.TempDir(), "restore.db")
	database := db.InitSqlite3(databaseFile)
	db.Db = database
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	models.AllTables = []any{&fnosRestoreItem{}}
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
	config.C = &config.Config{Emby: &config.Emby{DownloadStrategy: config.DlStrategyOrigin, Strm: &config.Strm{}},
		Cache: &config.Cache{}, VideoPreview: &config.VideoPreview{}, Openlist: &config.Openlist{}}
	if err := config.C.Cache.Init(); err != nil {
		t.Fatal(err)
	}
	origin := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		defer conn.Close()
		for {
			var message string
			if err := websocket.Message.Receive(conn, &message); err != nil {
				return
			}
			if err := websocket.Message.Send(conn, message); err != nil {
				return
			}
		}
	}))
	defer origin.Close()
	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	proxy := emby302.NewService(emby302.Options{
		HTTPAddress: socket.Addr().String(), Listen: func(string, string) (net.Listener, error) { return socket, nil },
		RuntimeAcquire: backup.AcquireRuntimeRequest,
		Guard: func(string, string, http.RoundTripper) gin.HandlerFunc {
			return func(c *gin.Context) {
				if err := database.WithContext(c.Request.Context()).Exec("SELECT 1").Error; err != nil {
					c.AbortWithStatus(http.StatusServiceUnavailable)
				}
			}
		},
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := proxy.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	if err := proxy.Apply(t.Context(), emby302.Configuration{Enabled: true, URL: origin.URL, APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	base := "http://" + socket.Addr().String()
	wsConfig, err := websocket.NewConfig("ws"+strings.TrimPrefix(base, "http")+"/embywebsocket", base)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := wsConfig.DialContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	echo := func() {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := websocket.Message.Send(conn, "ping"); err != nil {
			t.Fatal(err)
		}
		var message string
		if err := websocket.Message.Receive(conn, &message); err != nil || message != "ping" {
			t.Fatalf("WS echo = %q, %v", message, err)
		}
	}
	echo()
	restored := make(chan error, 1)
	go func() { restored <- backup.Restore(archive) }()
	select {
	case err := <-restored:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		_ = conn.Close()
		<-restored
		t.Fatal("restore waited for an upgraded WebSocket that no longer accesses the database")
	}
	echo()
	if newConnection, err := wsConfig.DialContext(t.Context()); err == nil {
		_ = newConnection.Close()
		t.Fatal("restore maintenance admitted a new WebSocket")
	}
	observer, err := gorm.Open(sqlite.Open(databaseFile), &gorm.Config{})
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
		t.Fatalf("restore did not commit data: %+v %v", item, err)
	}
}

func TestRestoreWaitsForCommittedSettingsAfterBrowserCancellation(t *testing.T) {
	const childEnv = "QMS_TEST_RESTORE_COMMITTED_SETTINGS"
	kind := os.Getenv(childEnv)
	if kind == "" {
		for _, kind := range []string{"threads", "emby"} {
			t.Run(kind, func(t *testing.T) {
				// 恢复永久关闭维护门禁，每个设置组使用独立进程。
				command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRestoreWaitsForCommittedSettingsAfterBrowserCancellation$", "-test.timeout=20s")
				command.Env = append(os.Environ(), childEnv+"="+kind)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("settings restore integration failed: %v\n%s", err, output)
				}
			})
		}
		return
	}
	setupFnosStartupTest(t)
	helpers.ConfigDir = t.TempDir()
	helpers.GlobalConfig.Db = helpers.ConfigDb{Engine: helpers.DbEngineSqlite, SqliteFile: "restore.db"}
	if err := (&App{}).StartDatabase(); err != nil {
		t.Fatal(err)
	}
	database := db.Db
	pool, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	models.AllTables = []any{&fnosRestoreItem{}}
	if err := database.AutoMigrate(&fnosRestoreItem{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&fnosRestoreItem{ID: 1, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	models.LoadSettings()
	if err := models.StartSettingsRuntime(); err != nil {
		t.Fatal(err)
	}
	models.StartMediaLibrarySettingsRuntime()
	defer models.StopSettingsRuntime(context.Background())
	defer models.StopMediaLibrarySettingsRuntime(context.Background())

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

	applying, release := make(chan struct{}), make(chan struct{})
	finishApply := sync.OnceFunc(func() { close(release) })
	applyResult := make(chan error, 1)
	apply := func(ctx context.Context) error {
		close(applying)
		<-release
		err := database.WithContext(ctx).Exec("UPDATE fnos_restore_items SET name = 'applied' WHERE id = 1").Error
		applyResult <- err
		return err
	}
	models.SetThreadSettingsApplyHook(func(models.SettingThreadAndRapidWait) error { return apply(context.Background()) })
	defer models.SetThreadSettingsApplyHook(nil)
	controller := controllers.NewMediaLibraryConfigController(func(ctx context.Context, _ *models.EmbyConfig) error { return apply(ctx) })
	router := gin.New()
	router.Use(controllers.RestoreMaintenanceMiddleware())
	router.POST("/settings/threads", controllers.UpdateThreads)
	router.POST("/settings/emby", controller.UpdateEmby)
	router.GET("/probe", func(c *gin.Context) { c.Status(http.StatusOK) })
	body := []byte(`{"emby_url":"http://offline.invalid","emby_api_key":"key","enabled":true}`)
	if kind == "threads" {
		settings := models.GetThreadSettingsSnapshot()
		settings.DownloadThreads++
		body, err = json.Marshal(settings)
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/settings/"+kind, bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	saved, restored := make(chan *httptest.ResponseRecorder, 1), make(chan error, 1)
	var operations sync.WaitGroup
	defer func() { finishApply(); operations.Wait() }()
	operations.Go(func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		saved <- response
	})
	select {
	case <-applying:
	case <-time.After(3 * time.Second):
		t.Fatal("save did not reach its committed application")
	}
	cancel()
	operations.Go(func() { restored <- backup.Restore(archive) })
	deadline := time.After(3 * time.Second)
	for {
		probe := httptest.NewRecorder()
		router.ServeHTTP(probe, httptest.NewRequest(http.MethodGet, "/probe", nil))
		if probe.Code == http.StatusServiceUnavailable {
			break
		}
		select {
		case <-deadline:
			t.Fatal("restore did not close HTTP admission")
		case <-time.After(time.Millisecond):
		}
	}
	if db.IsMaintenance(database) {
		t.Fatal("SQL closed before committed settings finished applying")
	}
	select {
	case <-saved:
		t.Fatal("browser cancellation released an accepted save before application")
	case <-restored:
		t.Fatal("restore finished before committed settings application")
	default:
	}
	finishApply()
	if err := <-applyResult; err != nil {
		t.Fatalf("accepted local application lost its SQL/cancellation permit: %v", err)
	}
	response := <-saved
	var result httpapi.APIResponse[json.RawMessage]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Status != http.StatusOK {
		t.Fatalf("committed save failed: %s (%v)", response.Body, err)
	}
	if err := <-restored; err != nil {
		t.Fatal(err)
	}
	observer, err := gorm.Open(sqlite.Open(filepath.Join(helpers.ConfigDir, "restore.db")), &gorm.Config{})
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
		t.Fatalf("restore did not follow completed application: %+v %v", item, err)
	}
}
