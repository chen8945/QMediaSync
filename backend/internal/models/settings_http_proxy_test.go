package models

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/tmdb"
)

// setupHttpProxyTest 准备内存数据库和一条 settings 记录，并在结束后还原被测试改写的全局状态。
func setupHttpProxyTest(t *testing.T) (*Settings, *bytes.Buffer) {
	t.Helper()

	oldDb := db.Db
	oldSettings := SettingsGlobal
	oldScrapeSettings := GlobalScrapeSettings
	oldProxy := helpers.HTTP_PROXY
	oldFanartKey := helpers.FANART_API_KEY
	oldLogger := helpers.AppLogger
	oldManager := notificationmanager.GlobalEnhancedNotificationManager
	oldTmdbClient := tmdb.GlobalTmdbClient
	t.Cleanup(func() {
		db.Db = oldDb
		SettingsGlobal = oldSettings
		GlobalScrapeSettings = oldScrapeSettings
		helpers.HTTP_PROXY = oldProxy
		helpers.FANART_API_KEY = oldFanartKey
		helpers.AppLogger = oldLogger
		notificationmanager.GlobalEnhancedNotificationManager = oldManager
		tmdb.GlobalTmdbClient = oldTmdbClient
	})

	var buf bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&buf, "", 0)}

	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDb
	if err := db.Db.AutoMigrate(&Settings{}); err != nil {
		t.Fatalf("迁移 Settings 失败: %v", err)
	}

	settings := &Settings{HttpProxy: "http://old.proxy.local:8080"}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatalf("创建 settings 记录失败: %v", err)
	}
	SettingsGlobal = settings

	// 代理开关打开，GetProxyUrl 才会把全局代理下发给 Fanart 和 TMDB
	GlobalScrapeSettings = &ScrapeSettings{TmdbEnableProxy: true}
	helpers.HTTP_PROXY = "http://old.proxy.local:8080"

	return settings, &buf
}

func TestUpdateHttpProxy写库失败不改内存全局值(t *testing.T) {
	settings, _ := setupHttpProxyTest(t)

	// 删表制造写库失败：控制器随后会提前返回，github.UpdateConfig 和 InitNotificationManager 都不会执行，
	// 此时内存若已被改写就会与数据库、GitHub 管理器、通知管理器脑裂。
	if err := db.Db.Migrator().DropTable(&Settings{}); err != nil {
		t.Fatalf("删除 settings 表失败: %v", err)
	}

	if settings.UpdateHttpProxy("socks5://user:secret@10.0.0.5:1080") {
		t.Fatal("写库失败时 UpdateHttpProxy 应返回 false")
	}
	if settings.HttpProxy != "http://old.proxy.local:8080" {
		t.Fatalf("写库失败后内存代理 = %q，应保持旧值", settings.HttpProxy)
	}
	if helpers.HTTP_PROXY != "http://old.proxy.local:8080" {
		t.Fatalf("写库失败后 Fanart 生效代理 = %q，应保持旧值", helpers.HTTP_PROXY)
	}
}

func TestUpdateHttpProxy写库成功后刷新下游客户端(t *testing.T) {
	settings, _ := setupHttpProxyTest(t)

	// 构造 TMDB 全局单例，验证保存代理后单例会被同步刷新
	tmdb.GlobalTmdbClient = nil
	client := tmdb.NewClient("api-key", "access-token", "https://api.tmdb.org", "zh-CN", "http://old.proxy.local:8080")

	const newProxy = "socks5://user:secret@10.0.0.5:1080"
	if !settings.UpdateHttpProxy(newProxy) {
		t.Fatal("写库成功时 UpdateHttpProxy 应返回 true")
	}

	if settings.HttpProxy != newProxy {
		t.Fatalf("内存代理 = %q，期望 %q", settings.HttpProxy, newProxy)
	}
	var saved Settings
	if err := db.Db.Take(&saved).Error; err != nil {
		t.Fatalf("读取 settings 失败: %v", err)
	}
	if saved.HttpProxy != newProxy {
		t.Fatalf("数据库代理 = %q，期望 %q", saved.HttpProxy, newProxy)
	}
	// Fanart 客户端每次构造都直读该变量
	if helpers.HTTP_PROXY != newProxy {
		t.Fatalf("Fanart 生效代理 = %q，期望 %q", helpers.HTTP_PROXY, newProxy)
	}
	// TMDB 是单例，不刷新就会继续拨旧代理
	if got := client.ProxyURL(); got != newProxy {
		t.Fatalf("TMDB 单例代理 = %q，期望 %q", got, newProxy)
	}
}

func TestUpdateHttpProxy清空代理后下游同步清空(t *testing.T) {
	settings, _ := setupHttpProxyTest(t)

	tmdb.GlobalTmdbClient = nil
	client := tmdb.NewClient("api-key", "access-token", "https://api.tmdb.org", "zh-CN", "http://old.proxy.local:8080")

	if !settings.UpdateHttpProxy("") {
		t.Fatal("清空代理时 UpdateHttpProxy 应返回 true")
	}

	if helpers.HTTP_PROXY != "" {
		t.Fatalf("清空代理后 Fanart 生效代理 = %q，应为空", helpers.HTTP_PROXY)
	}
	if got := client.ProxyURL(); got != "" {
		t.Fatalf("清空代理后 TMDB 单例代理 = %q，应为空", got)
	}
}

func TestInitNotificationManager代理日志脱敏(t *testing.T) {
	settings, buf := setupHttpProxyTest(t)

	// 只有存在启用的 Telegram 渠道，才会走到读取代理的回调；这正是每次保存代理都会触发的路径
	if err := db.Db.AutoMigrate(&notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.TelegramChannelConfig{}); err != nil {
		t.Fatalf("迁移通知表失败: %v", err)
	}
	channel := notification.NotificationChannel{ChannelType: "telegram", ChannelName: "Telegram", IsEnabled: true}
	if err := db.Db.Create(&channel).Error; err != nil {
		t.Fatalf("创建通知渠道失败: %v", err)
	}
	if err := db.Db.Create(&notification.TelegramChannelConfig{
		ChannelID: channel.ID,
		BotToken:  "bot-token",
		ChatID:    "chat-id",
	}).Error; err != nil {
		t.Fatalf("创建 Telegram 配置失败: %v", err)
	}

	if !settings.UpdateHttpProxy("socks5://user:secret@10.0.0.5:1080") {
		t.Fatal("UpdateHttpProxy 应返回 true")
	}

	logged := buf.String()
	if !strings.Contains(logged, "获取 HTTP 代理") {
		t.Fatalf("未触发读取代理的日志，实际：%s", logged)
	}
	if strings.Contains(logged, "secret") {
		t.Fatalf("保存代理的日志泄露了密码：%s", logged)
	}
	if strings.Contains(logged, "user:") {
		t.Fatalf("保存代理的日志泄露了用户名：%s", logged)
	}
	if !strings.Contains(logged, "xxxxx") {
		t.Fatalf("保存代理的日志应包含脱敏占位符，实际：%s", logged)
	}
}
