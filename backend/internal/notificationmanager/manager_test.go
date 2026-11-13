package notificationmanager

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
)

func setupNotificationManagerTest(t *testing.T) (*EnhancedNotificationManager, *gorm.DB, *bytes.Buffer) {
	t.Helper()
	testDb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	if err := testDb.AutoMigrate(&notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.CustomWebhookChannelConfig{}); err != nil {
		t.Fatalf("迁移通知测试表失败: %v", err)
	}
	var buf bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&buf, "", 0)}
	manager := NewEnhancedNotificationManager(testDb, func() string { return "" })
	return manager, testDb, &buf
}

type testBackgroundHandler struct {
	channelType string
	stopCount   int
	sendCount   atomic.Int32
}

func (h *testBackgroundHandler) Send(context.Context, *notification.Notification) error {
	h.sendCount.Add(1)
	return nil
}

func (h *testBackgroundHandler) GetChannelType() string {
	if h.channelType == "" {
		return "test"
	}
	return h.channelType
}

func (h *testBackgroundHandler) IsHealthy() bool {
	return true
}

func (h *testBackgroundHandler) Start(context.Context) {}

func (h *testBackgroundHandler) Stop() {
	h.stopCount++
}

func TestSendNotificationWithoutChannelsIsQuiet(t *testing.T) {
	manager, _, buf := setupNotificationManagerTest(t)
	if err := manager.LoadChannels(); err != nil {
		t.Fatalf("加载渠道失败: %v", err)
	}
	notif := &notification.Notification{
		Type:      notification.SyncFinished,
		Title:     "同步完成",
		Timestamp: time.Now(),
	}
	if err := manager.SendNotification(context.Background(), notif); err != nil {
		t.Fatalf("未配置通知时应静默返回，实际错误: %v", err)
	}
	if strings.Contains(buf.String(), "未找到事件类型") {
		t.Fatalf("未配置通知时不应写未找到规则日志，实际日志：%s", buf.String())
	}
}

func TestLoadChannelsRoutesEnabledRulesForDuplicateChannelTypes(t *testing.T) {
	manager, testDb, _ := setupNotificationManagerTest(t)
	channels := []notification.NotificationChannel{
		{ChannelType: "webhook", ChannelName: "Webhook A", IsEnabled: true},
		{ChannelType: "webhook", ChannelName: "Webhook B", IsEnabled: true},
		{ChannelType: "webhook", ChannelName: "Webhook C", IsEnabled: false},
	}
	if err := testDb.Create(&channels).Error; err != nil {
		t.Fatalf("创建通知渠道失败: %v", err)
	}
	if err := testDb.Model(&notification.NotificationChannel{}).Where("id = ?", channels[2].ID).Update("is_enabled", false).Error; err != nil {
		t.Fatalf("禁用通知渠道失败: %v", err)
	}
	for _, channel := range channels {
		if err := testDb.Create(&notification.CustomWebhookChannelConfig{
			ChannelID: channel.ID,
			Endpoint:  "https://example.invalid/webhook",
			Method:    "POST",
			Template:  `{"title":"{{title}}"}`,
			Format:    "json",
		}).Error; err != nil {
			t.Fatalf("创建 webhook 配置失败: %v", err)
		}
		if err := testDb.Create(&notification.NotificationRule{
			ChannelID: channel.ID,
			EventType: string(notification.SyncFinished),
			IsEnabled: true,
		}).Error; err != nil {
			t.Fatalf("创建通知规则失败: %v", err)
		}
	}

	if err := manager.LoadChannels(); err != nil {
		t.Fatalf("加载通知渠道失败: %v", err)
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if got := len(manager.rules[string(notification.SyncFinished)]); got != 2 {
		t.Fatalf("sync_finish 启用渠道数量 = %d，期望 2", got)
	}
}

func TestReloadRulesPreservesBackgroundHandlers(t *testing.T) {
	manager, testDb, _ := setupNotificationManagerTest(t)
	channel := notification.NotificationChannel{ChannelType: "telegram", ChannelName: "Telegram", IsEnabled: true}
	if err := testDb.Create(&channel).Error; err != nil {
		t.Fatalf("创建通知渠道失败: %v", err)
	}
	handler := &testBackgroundHandler{channelType: "telegram"}
	manager.handlers[channel.ID] = &channelInfo{handler: handler, config: &channel}
	rule := notification.NotificationRule{
		ChannelID: channel.ID,
		EventType: string(notification.SyncFinished),
		IsEnabled: true,
	}
	if err := testDb.Create(&rule).Error; err != nil {
		t.Fatalf("创建通知规则失败: %v", err)
	}

	manager.ReloadRules()

	manager.mu.RLock()
	gotHandler := manager.handlers[channel.ID].handler
	gotRuleIDs := append([]uint(nil), manager.rules[string(notification.SyncFinished)]...)
	manager.mu.RUnlock()
	if gotHandler != handler {
		t.Fatal("刷新通知规则不应替换已有后台 handler")
	}
	if handler.stopCount != 0 {
		t.Fatalf("刷新通知规则不应停止后台 handler，实际停止次数: %d", handler.stopCount)
	}
	if len(gotRuleIDs) != 1 || gotRuleIDs[0] != channel.ID {
		t.Fatalf("sync_finish 规则路由 = %v，期望仅包含渠道 %d", gotRuleIDs, channel.ID)
	}
}

func TestRemoveChannelStopsBackgroundHandlerAndDropsRules(t *testing.T) {
	manager, testDb, _ := setupNotificationManagerTest(t)
	channel := notification.NotificationChannel{ChannelType: "telegram", ChannelName: "Telegram", IsEnabled: true}
	if err := testDb.Create(&channel).Error; err != nil {
		t.Fatalf("创建通知渠道失败: %v", err)
	}
	handler := &testBackgroundHandler{channelType: "telegram"}
	manager.handlers[channel.ID] = &channelInfo{handler: handler, config: &channel}
	if err := testDb.Create(&notification.NotificationRule{
		ChannelID: channel.ID,
		EventType: string(notification.SyncFinished),
		IsEnabled: true,
	}).Error; err != nil {
		t.Fatalf("创建通知规则失败: %v", err)
	}
	manager.ReloadRules()

	manager.RemoveChannel(channel.ID)

	manager.mu.RLock()
	_, handlerExists := manager.handlers[channel.ID]
	gotRuleIDs := append([]uint(nil), manager.rules[string(notification.SyncFinished)]...)
	manager.mu.RUnlock()
	if handlerExists {
		t.Fatal("卸载通知渠道后不应保留 handler")
	}
	if handler.stopCount != 1 {
		t.Fatalf("卸载通知渠道应停止一次后台 handler，实际停止次数: %d", handler.stopCount)
	}
	if len(gotRuleIDs) != 0 {
		t.Fatalf("卸载通知渠道后不应保留规则路由，实际: %v", gotRuleIDs)
	}
}

func TestTelegramChannelHandlerStartInitializesBotInBackground(t *testing.T) {
	initStarted := make(chan struct{})
	allowInitReturn := make(chan struct{})
	listeningStarted := make(chan struct{}, 1)
	handler := &TelegramChannelHandler{
		initBotFunc: func() error {
			close(initStarted)
			<-allowInitReturn
			return nil
		},
		startListeningFunc: func(context.Context, map[string]func([]string) helpers.CommandResponse) {
			listeningStarted <- struct{}{}
		},
	}

	done := make(chan struct{})
	go func() {
		handler.Start(context.Background())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Telegram handler Start 不应等待 Bot 初始化完成")
	}
	select {
	case <-initStarted:
	case <-time.After(time.Second):
		t.Fatal("Telegram handler Start 应在后台启动 Bot 初始化")
	}

	close(allowInitReturn)
	select {
	case <-listeningStarted:
	case <-time.After(time.Second):
		t.Fatal("Bot 初始化完成后应进入监听")
	}
}

func TestTelegramChannelHandlerStopBeforeInitCompletesSkipsListening(t *testing.T) {
	initStarted := make(chan struct{})
	allowInitReturn := make(chan struct{})
	initReturned := make(chan struct{})
	listeningStarted := make(chan struct{}, 1)
	handler := &TelegramChannelHandler{
		initBotFunc: func() error {
			close(initStarted)
			<-allowInitReturn
			close(initReturned)
			return nil
		},
		startListeningFunc: func(context.Context, map[string]func([]string) helpers.CommandResponse) {
			listeningStarted <- struct{}{}
		},
	}

	handler.Start(context.Background())
	select {
	case <-initStarted:
	case <-time.After(time.Second):
		t.Fatal("Telegram handler Start 应在后台启动 Bot 初始化")
	}

	handler.Stop()
	close(allowInitReturn)
	select {
	case <-initReturned:
	case <-time.After(time.Second):
		t.Fatal("测试中的 Bot 初始化未按预期结束")
	}
	select {
	case <-listeningStarted:
		t.Fatal("停止 Telegram handler 后，不应在初始化完成后继续进入监听")
	case <-time.After(100 * time.Millisecond):
	}
}

type blockedNotificationHandler struct {
	started chan struct{}
	release chan struct{}
}

func (h *blockedNotificationHandler) Send(context.Context, *notification.Notification) error {
	close(h.started)
	<-h.release
	return nil
}
func (*blockedNotificationHandler) GetChannelType() string { return "test" }
func (*blockedNotificationHandler) IsHealthy() bool        { return true }

func TestNotificationReloadDoesNotWaitForOldSend(t *testing.T) {
	manager, conn, _ := setupNotificationManagerTest(t)
	received := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received <- struct{}{} }))
	defer remote.Close()
	channel := notification.NotificationChannel{ChannelType: "webhook", ChannelName: "new", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&notification.CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: remote.URL, Method: "POST", Format: "text", Template: "{{title}}"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&notification.NotificationRule{ChannelID: channel.ID, EventType: string(notification.SystemAlert), IsEnabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	old := &blockedNotificationHandler{started: make(chan struct{}), release: make(chan struct{})}
	manager.handlers[channel.ID] = &channelInfo{handler: old, config: &channel}
	if err := manager.ReloadRules(); err != nil {
		t.Fatal(err)
	}
	message := &notification.Notification{Type: notification.SystemAlert, Title: "test"}
	done := make(chan error, 1)
	go func() { done <- manager.SendNotification(context.Background(), message) }()
	<-old.started
	reloaded := make(chan error, 1)
	go func() { reloaded <- manager.ReloadChannel(channel.ID) }()
	select {
	case err := <-reloaded:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(old.release)
		t.Fatal("slow send blocked reload")
	}
	if err := manager.SendNotification(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("next send did not use new config")
	}
	close(old.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNotificationPrepareFailureKeepsEffectiveChannel(t *testing.T) {
	manager, conn, _ := setupNotificationManagerTest(t)
	channel := notification.NotificationChannel{ChannelType: "webhook", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	old := &testBackgroundHandler{}
	manager.handlers[channel.ID] = &channelInfo{handler: old, config: &channel}
	if err := manager.ReloadChannel(channel.ID); err == nil {
		t.Fatal("missing config must fail")
	}
	if got := manager.GetChannels(); len(got) != 1 {
		t.Fatalf("effective channels=%v", got)
	}
	if old.stopCount != 0 {
		t.Fatal("prepare failure retired effective handler")
	}
	if err := conn.Create(&notification.CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: "https://example.invalid", Method: "POST", Format: "text", Template: "{{title}}"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.ReloadChannel(channel.ID); err != nil {
		t.Fatal(err)
	}
	if old.stopCount != 1 {
		t.Fatal("successful retry did not retire old handler")
	}
}

func TestTelegramRetiredHandlerCannotStartAgain(t *testing.T) {
	initialized := make(chan struct{}, 1)
	handler := &TelegramChannelHandler{initBotFunc: func() error { initialized <- struct{}{}; return nil }}
	handler.Stop()
	handler.Start(context.Background())
	select {
	case <-initialized:
		t.Fatal("retired candidate started")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTelegramCommandsAreCapturedWithoutMapMutation(t *testing.T) {
	manager, _, _ := setupNotificationManagerTest(t)
	initialized := make(chan struct{})
	release := make(chan struct{})
	got := make(chan helpers.CommandResponse, 1)
	commands := map[string]func([]string) helpers.CommandResponse{"help": func([]string) helpers.CommandResponse { return helpers.CommandResponse{Text: "old"} }}
	manager.RegisterTelegramCommands(commands)
	handler := &TelegramChannelHandler{
		initBotFunc: func() error { close(initialized); <-release; return nil },
		startListeningFunc: func(_ context.Context, cmds map[string]func([]string) helpers.CommandResponse) {
			got <- cmds["help"](nil)
		},
	}
	handler.SetCommands(manager.telegramCommands)
	handler.Start(context.Background())
	<-initialized
	commands["help"] = func([]string) helpers.CommandResponse { return helpers.CommandResponse{Text: "changed"} }
	handler.SetCommands(commands)
	close(release)
	select {
	case result := <-got:
		if result.Text != "old" {
			t.Fatalf("captured commands=%v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("listener not started")
	}
}

func TestNotificationLateCandidateCannotReviveDisabledChannel(t *testing.T) {
	manager, _, _ := setupNotificationManagerTest(t)
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "notifications.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.TelegramChannelConfig{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	manager.db = conn
	channel := notification.NotificationChannel{ChannelType: "telegram", ChannelName: "before", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&notification.TelegramChannelConfig{ChannelID: channel.ID, BotToken: "token", ChatID: "123"}).Error; err != nil {
		t.Fatal(err)
	}
	prepared, release := make(chan struct{}), make(chan struct{})
	manager.getProxyURL = func() string { close(prepared); <-release; return "" }
	done := make(chan error, 1)
	go func() { done <- manager.ReloadChannel(channel.ID) }()
	<-prepared
	if err := conn.Model(&notification.NotificationChannel{}).Where("id = ?", channel.ID).Update("is_enabled", false).Error; err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := manager.ReloadChannel(channel.ID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("superseded candidate reported application success")
		}
	case <-time.After(time.Second):
		t.Fatal("old candidate did not complete")
	}
	if got := manager.GetChannels(); len(got) != 0 {
		t.Fatalf("old candidate revived disabled channel: %v", got)
	}
	manager.StartAll()
	if err := manager.StopAllContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationShutdownWaitsForRetiredTelegramCommand(t *testing.T) {
	manager, _, _ := setupNotificationManagerTest(t)
	started, release := make(chan struct{}), make(chan struct{})
	handler := &TelegramChannelHandler{
		initBotFunc:        func() error { return nil },
		startListeningFunc: func(context.Context, map[string]func([]string) helpers.CommandResponse) { close(started); <-release },
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager.handlers[1] = &channelInfo{handler: handler, config: &notification.NotificationChannel{ID: 1}, ctx: ctx, cancel: cancel}
	manager.backgrounds[handler] = struct{}{}
	manager.StartAll()
	<-started
	manager.RemoveChannel(1)
	limit, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	if err := manager.StopAllContext(limit); !errors.Is(err, context.DeadlineExceeded) {
		close(release)
		t.Fatalf("shutdown falsely reported completion: %v", err)
	}
	close(release)
	limit2, stop2 := context.WithTimeout(context.Background(), time.Second)
	defer stop2()
	if err := manager.StopAllContext(limit2); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationSupersededRulesWarnAndPreserveRoutingUntilRetry(t *testing.T) {
	manager, conn, _ := setupNotificationManagerTest(t)
	channel := notification.NotificationChannel{ChannelType: "telegram", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	handler := &testBackgroundHandler{}
	manager.handlers[channel.ID] = &channelInfo{handler: handler, config: &channel}
	rule := notification.NotificationRule{ChannelID: channel.ID, EventType: string(notification.SystemAlert), IsEnabled: true}
	if err := conn.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.ReloadRules(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&rule).Update("is_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	prepared, release := make(chan struct{}), make(chan struct{})
	var prepareCalls atomic.Int32
	var failReload atomic.Bool
	if err := conn.Callback().Query().After("gorm:query").Register("test:hold_rules", func(tx *gorm.DB) {
		if tx.Statement.Table == "notification_rules" && prepareCalls.Add(1) == 1 {
			close(prepared)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Callback().Query().Before("gorm:query").Register("test:fail_rules", func(tx *gorm.DB) {
		if failReload.Load() && tx.Statement.Table == "notification_rules" {
			tx.AddError(errors.New("newer rules unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.ReloadRules() }()
	<-prepared
	failReload.Store(true)
	if err := manager.ReloadRules(); err == nil {
		close(release)
		t.Fatal("newer rules failure was hidden")
	}
	close(release)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("superseded rule application reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("superseded rule application did not finish")
	}
	message := &notification.Notification{Type: notification.SystemAlert}
	if err := manager.SendNotification(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if handler.sendCount.Load() != 1 {
		t.Fatal("failed rule preparations did not preserve effective routing")
	}
	failReload.Store(false)
	if err := manager.ReloadRules(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SendNotification(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if handler.sendCount.Load() != 1 || handler.stopCount != 0 {
		t.Fatal("rule retry must disable routing while preserving the background handler")
	}
}

func TestNotificationRulePreparationFailureKeepsWholeChannelUntilRetry(t *testing.T) {
	manager, conn, _ := setupNotificationManagerTest(t)
	var oldSends, newSends atomic.Int32
	server := func(counter *atomic.Int32) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { counter.Add(1) }))
	}
	oldRemote, newRemote := server(&oldSends), server(&newSends)
	defer oldRemote.Close()
	defer newRemote.Close()
	channel := notification.NotificationChannel{ChannelType: "webhook", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	config := notification.CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: oldRemote.URL, Method: "POST", Format: "text", Template: "{{title}}"}
	if err := conn.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	rule := notification.NotificationRule{ChannelID: channel.ID, EventType: string(notification.SystemAlert), IsEnabled: true}
	if err := conn.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.LoadChannels(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&config).Update("endpoint", newRemote.URL).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&rule).Update("is_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	var failRules atomic.Bool
	failRules.Store(true)
	if err := conn.Callback().Query().Before("gorm:query").Register("test:fail_channel_rules", func(tx *gorm.DB) {
		if failRules.Load() && tx.Statement.Table == "notification_rules" {
			tx.AddError(errors.New("rules unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReloadChannel(channel.ID); err == nil {
		t.Fatal("rule preparation failure was hidden")
	}
	message := &notification.Notification{Type: notification.SystemAlert, Title: "test"}
	if err := manager.SendNotification(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if oldSends.Load() != 1 || newSends.Load() != 0 {
		t.Fatal("failed rules preparation mixed the new handler with old routing")
	}
	failRules.Store(false)
	if err := manager.ReloadChannel(channel.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.SendNotification(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if oldSends.Load() != 1 || newSends.Load() != 0 {
		t.Fatal("retry did not apply the saved disabled rule")
	}
	if err := conn.Model(&rule).Update("is_enabled", true).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.ReloadRules(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SendNotification(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	if newSends.Load() != 1 {
		t.Fatal("successful retry did not retain the newly applied handler")
	}
}

func TestNotificationLateRulesCannotOverwritePublishedChannelRouting(t *testing.T) {
	manager, conn, _ := setupNotificationManagerTest(t)
	channel := notification.NotificationChannel{ChannelType: "webhook", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&notification.CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: "https://example.invalid", Method: "POST", Format: "text", Template: "{{title}}"}).Error; err != nil {
		t.Fatal(err)
	}
	rule := notification.NotificationRule{ChannelID: channel.ID, EventType: string(notification.SystemAlert), IsEnabled: true}
	if err := conn.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if err := manager.LoadChannels(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&rule).Update("is_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	prepared, release := make(chan struct{}), make(chan struct{})
	var queries atomic.Int32
	if err := conn.Callback().Query().After("gorm:query").Register("test:hold_old_rules", func(tx *gorm.DB) {
		if tx.Statement.Table == "notification_rules" && queries.Add(1) == 1 {
			close(prepared)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- manager.ReloadRules() }()
	<-prepared
	if err := conn.Model(&rule).Update("is_enabled", true).Error; err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := manager.ReloadChannel(channel.ID); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("old rule snapshot reported success")
	}
	manager.mu.RLock()
	ids := append([]uint(nil), manager.rules[string(notification.SystemAlert)]...)
	manager.mu.RUnlock()
	if len(ids) != 1 || ids[0] != channel.ID {
		t.Fatal("old rules overwrote the newly published channel route")
	}
}

func TestNotificationProxyReloadRetainsCommandsAndRetiresListeners(t *testing.T) {
	manager, conn, _ := setupNotificationManagerTest(t)
	if err := conn.AutoMigrate(&notification.TelegramChannelConfig{}); err != nil {
		t.Fatal(err)
	}
	channel := notification.NotificationChannel{ChannelType: "telegram", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&notification.TelegramChannelConfig{ChannelID: channel.ID, BotToken: "token", ChatID: "123"}).Error; err != nil {
		t.Fatal(err)
	}
	var proxy atomic.Value
	proxy.Store("http://first.invalid:8080")
	manager.getProxyURL = func() string { return proxy.Load().(string) }
	type listenerEvent struct{ proxy, command string }
	started, stopped := make(chan listenerEvent, 4), make(chan string, 4)
	var mu sync.Mutex
	active := make(map[string]int)
	oldConstructor := newTelegramChannelHandler
	newTelegramChannelHandler = func(config *notification.TelegramChannelConfig, proxyURL string) *TelegramChannelHandler {
		handler := NewTelegramChannelHandlerWithProxy(config, proxyURL)
		handler.initBotFunc = func() error { return nil }
		handler.startListeningFunc = func(ctx context.Context, commands map[string]func([]string) helpers.CommandResponse) {
			mu.Lock()
			active[proxyURL]++
			mu.Unlock()
			command := "missing"
			if callback := commands["status"]; callback != nil {
				command = callback(nil).Text
			}
			started <- listenerEvent{proxy: proxyURL, command: command}
			<-ctx.Done()
			mu.Lock()
			active[proxyURL]--
			mu.Unlock()
			stopped <- proxyURL
		}
		return handler
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := manager.StopAllContext(ctx); err != nil {
			t.Error(err)
		}
		newTelegramChannelHandler = oldConstructor
	})
	manager.RegisterTelegramCommands(map[string]func([]string) helpers.CommandResponse{"status": func([]string) helpers.CommandResponse { return helpers.CommandResponse{Text: "registered"} }})
	if err := manager.LoadChannels(); err != nil {
		t.Fatal(err)
	}
	manager.StartAll()
	assertStarted := func(expected string) {
		t.Helper()
		select {
		case event := <-started:
			if event.proxy != expected || event.command != "registered" {
				t.Fatalf("listener=%+v", event)
			}
		case <-time.After(time.Second):
			t.Fatal("listener did not start")
		}
	}
	assertStopped := func(expected string) {
		t.Helper()
		select {
		case value := <-stopped:
			if value != expected {
				t.Fatalf("retired proxy=%s", value)
			}
		case <-time.After(time.Second):
			t.Fatal("old listener was not canceled")
		}
	}
	assertStarted("http://first.invalid:8080")
	proxy.Store("http://second.invalid:8080")
	if err := manager.LoadChannels(); err != nil {
		t.Fatal(err)
	}
	assertStarted("http://second.invalid:8080")
	assertStopped("http://first.invalid:8080")
	if err := manager.LoadChannels(); err != nil {
		t.Fatal(err)
	}
	assertStarted("http://second.invalid:8080")
	assertStopped("http://second.invalid:8080")
	manager.StartAll()
	mu.Lock()
	first, second := active["http://first.invalid:8080"], active["http://second.invalid:8080"]
	mu.Unlock()
	if first != 0 || second != 1 {
		t.Fatalf("active listeners: first=%d second=%d", first, second)
	}
}

func TestNotificationLateChannelCannotOverwriteReloadedRules(t *testing.T) {
	manager, _, _ := setupNotificationManagerTest(t)
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "channel-rules.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.TelegramChannelConfig{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	manager.db = conn
	channel := notification.NotificationChannel{ChannelType: "telegram", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&notification.TelegramChannelConfig{ChannelID: channel.ID, BotToken: "token", ChatID: "123"}).Error; err != nil {
		t.Fatal(err)
	}
	rule := notification.NotificationRule{ChannelID: channel.ID, EventType: string(notification.SystemAlert), IsEnabled: true}
	if err := conn.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	handler := &testBackgroundHandler{}
	manager.handlers[channel.ID] = &channelInfo{handler: handler, config: &channel}
	if err := manager.ReloadRules(); err != nil {
		t.Fatal(err)
	}
	prepared, release := make(chan struct{}), make(chan struct{})
	manager.getProxyURL = func() string { close(prepared); <-release; return "" }
	done := make(chan error, 1)
	go func() { done <- manager.ReloadChannel(channel.ID) }()
	<-prepared
	if err := conn.Model(&rule).Update("is_enabled", false).Error; err != nil {
		close(release)
		t.Fatal(err)
	}
	if err := manager.ReloadRules(); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("old channel preparation reported success")
	}
	if err := manager.SendNotification(context.Background(), &notification.Notification{Type: notification.SystemAlert}); err != nil {
		t.Fatal(err)
	}
	if handler.sendCount.Load() != 0 || handler.stopCount != 0 {
		t.Fatal("old channel preparation overwrote newer rules or retired the effective handler")
	}
}
