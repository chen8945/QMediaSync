package notificationmanager

import (
	"strings"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
)

// useTestLogLevel 临时把日志级别调到 Debug，结束后还原。Send 的代理日志是 Debug 级。
func useTestLogLevel(t *testing.T) {
	t.Helper()
	oldLevel := helpers.ConfiguredLogLevel()
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	t.Cleanup(func() {
		helpers.SetGlobalLogLevel(oldLevel)
	})
}

// TestCreateChannelHandlerRedactsProxyLog 渠道重载会在每次保存代理时执行，日志不得泄露代理凭据。
func TestCreateChannelHandlerRedactsProxyLog(t *testing.T) {
	manager, testDb, buf := setupNotificationManagerTest(t)
	if err := testDb.AutoMigrate(&notification.TelegramChannelConfig{}); err != nil {
		t.Fatalf("迁移 Telegram 配置表失败: %v", err)
	}
	manager.getProxyURL = func() string { return "socks5://user:secret@10.0.0.5:1080" }

	channel := notification.NotificationChannel{ChannelType: "telegram", ChannelName: "Telegram", IsEnabled: true}
	if err := testDb.Create(&channel).Error; err != nil {
		t.Fatalf("创建通知渠道失败: %v", err)
	}
	if err := testDb.Create(&notification.TelegramChannelConfig{
		ChannelID: channel.ID,
		BotToken:  "bot-token",
		ChatID:    "chat-id",
	}).Error; err != nil {
		t.Fatalf("创建 Telegram 配置失败: %v", err)
	}

	if err := manager.LoadChannels(); err != nil {
		t.Fatalf("加载通知渠道失败: %v", err)
	}

	logged := buf.String()
	if !strings.Contains(logged, "为 Telegram 渠道使用代理") {
		t.Fatalf("未写入代理日志，实际：%s", logged)
	}
	if strings.Contains(logged, "secret") {
		t.Fatalf("渠道重载日志泄露了密码：%s", logged)
	}
	if strings.Contains(logged, "user:") {
		t.Fatalf("渠道重载日志泄露了用户名：%s", logged)
	}
	if !strings.Contains(logged, "xxxxx") {
		t.Fatalf("渠道重载日志应包含脱敏占位符，实际：%s", logged)
	}
	// handler 必须仍持有真实地址，脱敏只作用于日志
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	handler, ok := manager.handlers[channel.ID].handler.(*TelegramChannelHandler)
	if !ok {
		t.Fatal("应创建 Telegram handler")
	}
	if handler.proxyURL != "socks5://user:secret@10.0.0.5:1080" {
		t.Fatalf("handler 代理 = %q，脱敏不应影响实际拨号地址", handler.proxyURL)
	}
}

// TestTelegramSendRedactsProxyLog 发送日志是 Debug 级，恰好是运维调高日志级别排查投递问题时最可能分享的输出。
func TestTelegramSendRedactsProxyLog(t *testing.T) {
	_, _, buf := setupNotificationManagerTest(t)
	useTestLogLevel(t)

	handler := &TelegramChannelHandler{
		config:   &notification.TelegramChannelConfig{BotToken: "bot-token", ChatID: "123456"},
		proxyURL: "socks5://user:secret@127.0.0.1:1",
	}

	// 端口 1 上不会有代理监听，Send 会立即失败；这里只关心失败前写出的那条 Debug 日志
	_ = handler.Send(t.Context(), &notification.Notification{Type: notification.SyncFinished, Title: "同步完成"})

	logged := buf.String()
	if !strings.Contains(logged, "使用系统代理发送 Telegram 消息") {
		t.Fatalf("未写入发送代理日志，实际：%s", logged)
	}
	if strings.Contains(logged, "secret") {
		t.Fatalf("发送日志泄露了密码：%s", logged)
	}
	if strings.Contains(logged, "user:") {
		t.Fatalf("发送日志泄露了用户名：%s", logged)
	}
	if !strings.Contains(logged, "xxxxx") {
		t.Fatalf("发送日志应包含脱敏占位符，实际：%s", logged)
	}
}
