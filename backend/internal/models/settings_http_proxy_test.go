package models

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/github"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
)

type settingsLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (buffer *settingsLogBuffer) Write(value []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.Write(value)
}
func (buffer *settingsLogBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.Buffer.String()
}

func setupHttpProxyTest(t *testing.T) (*Settings, *settingsLogBuffer) {
	t.Helper()
	return setupHttpProxyTestDatabase(t, ":memory:")
}

func setupHttpProxyTestDatabase(t *testing.T, dsn string) (*Settings, *settingsLogBuffer) {
	t.Helper()
	oldDB, oldSettings, oldSnapshot := db.Db, SettingsGlobal, committedSettings.Load()
	oldLogger, oldTMDBLogger := helpers.AppLogger, helpers.TMDBLog
	oldManager := notificationmanager.GlobalEnhancedNotificationManager
	oldDownload, oldUpload := GlobalDownloadQueue, GlobalUploadQueue
	oldThreadHook := threadSettingsApply.Load()
	var buf settingsLogBuffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&buf, "", 0)}
	helpers.TMDBLog = helpers.AppLogger
	testDB, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Db = testDB
	if err := testDB.AutoMigrate(&Settings{}, &ScrapeSettings{}); err != nil {
		t.Fatal(err)
	}
	settings := &Settings{HttpProxy: "http://old.proxy.local:8080", DownloadThreads: 1, UploadThreads: 1, FileDetailThreads: 2}
	if err := testDB.Create(settings).Error; err != nil {
		t.Fatal(err)
	}
	if err := testDB.Create(&ScrapeSettings{TmdbEnableProxy: true, TmdbApiKey: "api-key"}).Error; err != nil {
		t.Fatal(err)
	}
	notificationmanager.GlobalEnhancedNotificationManager = nil
	GlobalDownloadQueue, GlobalUploadQueue = nil, nil
	SettingsGlobal = nil
	LoadSettings()
	LoadScrapeSettings()
	github.GetManager()
	if err := StartSettingsRuntime(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := StopSettingsRuntime(context.Background()); err != nil {
			t.Error(err)
		}
		if current := notificationmanager.GlobalEnhancedNotificationManager; current != nil && current != oldManager {
			current.StopAll()
		}
		threadSettingsApply.Store(oldThreadHook)
		GlobalDownloadQueue, GlobalUploadQueue = oldDownload, oldUpload
		db.Db, SettingsGlobal = oldDB, oldSettings
		committedSettings.Store(oldSnapshot)
		helpers.AppLogger, helpers.TMDBLog = oldLogger, oldTMDBLogger
		notificationmanager.GlobalEnhancedNotificationManager = oldManager
		_ = sqlDB.Close()
	})
	return settings, &buf
}

func TestUpdateHttpProxyFailureKeepsCommittedSettings(t *testing.T) {
	settings, _ := setupHttpProxyTest(t)
	if err := db.Db.Migrator().DropTable(&Settings{}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveHTTPProxy(context.Background(), "socks5://user:secret@10.0.0.5:1080", nil); err == nil {
		t.Fatal("数据库失败应拒绝保存")
	}
	if GetHTTPProxy() != settings.HttpProxy {
		t.Fatal("保存失败改变了运行时代理")
	}
}

func TestUpdateHttpProxyAffectsNewRequestsAndPreservesCapturedClient(t *testing.T) {
	_, _ = setupHttpProxyTest(t)
	var firstCount, secondCount, directCount atomic.Int32
	server := func(counter *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		}))
	}
	first, second, target := server(&firstCount), server(&secondCount), server(&directCount)
	defer first.Close()
	defer second.Close()
	defer target.Close()
	ctx := context.Background()
	if _, err := SaveHTTPProxy(ctx, first.URL, nil); err != nil {
		t.Fatal(err)
	}
	config := GetScrapeSettingsSnapshot()
	config.TmdbUrl = target.URL
	if err := SaveTmdbSettings(ctx, config); err != nil {
		t.Fatal(err)
	}
	oldConfig := GetScrapeSettingsSnapshot()
	oldClient, err := oldConfig.GetTmdbClient()
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.Close()
	if _, err := SaveHTTPProxy(ctx, second.URL, nil); err != nil {
		t.Fatal(err)
	}
	newConfig := GetScrapeSettingsSnapshot()
	newClient, err := newConfig.GetTmdbClient()
	if err != nil {
		t.Fatal(err)
	}
	defer newClient.Close()
	if !oldClient.TestToken(ctx) || !newClient.TestToken(ctx) {
		t.Fatal("代理请求失败")
	}
	if firstCount.Load() != 1 || secondCount.Load() != 1 {
		t.Fatal("后续请求未按各自捕获的代理发送")
	}
	if _, err := SaveHTTPProxy(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	directConfig := GetScrapeSettingsSnapshot()
	directClient, err := directConfig.GetTmdbClient()
	if err != nil {
		t.Fatal(err)
	}
	defer directClient.Close()
	if !directClient.TestToken(ctx) || !newClient.TestToken(ctx) {
		t.Fatal("清空代理后请求失败")
	}
	if directCount.Load() != 1 || secondCount.Load() != 2 {
		t.Fatal("清空代理未作用于新请求，或改写了在途客户端")
	}
}

func TestInitNotificationManagerProxyLogRedactsCredentials(t *testing.T) {
	_, buf := setupHttpProxyTest(t)
	if err := db.Db.AutoMigrate(&notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.TelegramChannelConfig{}); err != nil {
		t.Fatal(err)
	}
	channel := notification.NotificationChannel{ChannelType: "telegram", ChannelName: "Telegram", IsEnabled: true}
	if err := db.Db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&notification.TelegramChannelConfig{ChannelID: channel.ID, BotToken: "bot-token", ChatID: "chat-id"}).Error; err != nil {
		t.Fatal(err)
	}
	InitNotificationManager()
	if _, err := SaveHTTPProxy(context.Background(), "socks5://user:secret@10.0.0.5:1080", nil); err != nil {
		t.Fatal(err)
	}
	logged := buf.String()
	if strings.Contains(logged, "secret") || strings.Contains(logged, "user:") {
		t.Fatalf("日志泄露凭据：%s", logged)
	}
	if !strings.Contains(logged, "xxxxx") {
		t.Fatalf("日志未使用脱敏地址：%s", logged)
	}
}
