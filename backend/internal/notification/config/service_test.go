package config

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/validation"
)

func setupConfigTest(t *testing.T) *gorm.DB {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "notifications.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	err = conn.AutoMigrate(
		&notification.NotificationChannel{},
		&notification.NotificationRule{},
		&notification.TelegramChannelConfig{},
		&notification.MeoWChannelConfig{},
		&notification.BarkChannelConfig{},
		&notification.ServerChanChannelConfig{},
		&notification.CustomWebhookChannelConfig{},
	)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	oldDB, oldManager, oldLogger := db.Db, notificationmanager.GlobalEnhancedNotificationManager, helpers.AppLogger
	db.Db = conn
	notificationmanager.GlobalEnhancedNotificationManager = nil
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		db.Db = oldDB
		notificationmanager.GlobalEnhancedNotificationManager = oldManager
		helpers.AppLogger = oldLogger
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	return conn
}

func TestWebhookEditPreservesPatchSemantics(t *testing.T) {
	conn := setupConfigTest(t)
	ctx := t.Context()
	created, err := CreateWebhook(ctx, notification.WebhookInput{
		Edit: notification.ChannelEdit{ChannelName: "webhook"},
		Config: notification.CustomWebhookChannelConfig{
			Endpoint:  "https://example.com/hook",
			Method:    "post",
			Format:    "text",
			Template:  "original {{title}}",
			AuthType:  "bearer",
			AuthToken: "retained-secret",
		},
		Headers: map[string]string{" X-Trace ": "retained"},
	})
	if err != nil {
		t.Fatal(err)
	}
	edit := notification.ChannelEdit{ChannelID: created.Channel.ID}
	// 只改格式时不重新验证未提供的旧模板。
	formatPatch := notification.WebhookInput{Edit: edit, Config: notification.CustomWebhookChannelConfig{Format: "JSON"}}
	if _, err := UpdateWebhook(ctx, formatPatch); err != nil {
		t.Fatal(err)
	}
	var cfg notification.CustomWebhookChannelConfig
	read := func() {
		t.Helper()
		if err := conn.Where("channel_id = ?", edit.ChannelID).First(&cfg).Error; err != nil {
			t.Fatal(err)
		}
	}
	read()
	if cfg.Method != "POST" || cfg.Format != "json" {
		t.Fatalf("method/format changed: method=%s format=%s", cfg.Method, cfg.Format)
	}
	retainedPayload := cfg.Template == "original {{title}}"
	retainedAuth := cfg.AuthToken == "retained-secret"
	retainedHeaders := cfg.Headers == `{"X-Trace":"retained"}`
	if !retainedPayload || !retainedAuth || !retainedHeaders {
		t.Fatalf("unspecified fields changed: template=%s headers=%s", cfg.Template, cfg.Headers)
	}
	if _, err := UpdateWebhook(ctx, notification.WebhookInput{Edit: edit, Headers: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	read()
	if cfg.Headers != "{}" || cfg.AuthToken != "retained-secret" {
		t.Fatal("explicit empty headers did not clear only headers")
	}
	invalidPatch := notification.WebhookInput{Edit: edit, Config: notification.CustomWebhookChannelConfig{Template: "{"}}
	_, err = UpdateWebhook(ctx, invalidPatch)
	field, ok := errors.AsType[validation.Error](err)
	if !ok || field.Field != "template" {
		t.Fatalf("invalid template error=%v", err)
	}
	read()
	if cfg.Template != "original {{title}}" {
		t.Fatal("invalid patch reached persistence")
	}
	_, err = UpdateTelegram(ctx, edit, notification.TelegramChannelConfig{})
	if !errors.Is(err, ErrChannelTypeMismatch) {
		t.Fatalf("wrong channel type error=%v", err)
	}
	missingInput := notification.WebhookInput{Edit: notification.ChannelEdit{ChannelID: created.Channel.ID + 1}}
	_, err = UpdateWebhook(ctx, missingInput)
	if !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("missing channel error=%v", err)
	}
	databaseFailure := errors.New("notification read unavailable")
	if err := conn.Callback().Query().Before("gorm:query").Register("test:notification_read_failure", func(tx *gorm.DB) {
		if tx.Statement.Table == "notification_channels" {
			tx.AddError(databaseFailure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	_, err = UpdateWebhook(ctx, notification.WebhookInput{Edit: edit})
	if !errors.Is(err, databaseFailure) || errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("database error misclassified=%v", err)
	}
}

func TestChannelApplyCompletesAfterRequestCancellation(t *testing.T) {
	conn := setupConfigTest(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	preparations := 0
	manager := notificationmanager.NewEnhancedNotificationManager(conn, func() string {
		preparations++
		cancel()
		return ""
	})
	notificationmanager.GlobalEnhancedNotificationManager = manager
	result, err := CreateTelegram(
		ctx,
		notification.ChannelEdit{ChannelName: "committed"},
		notification.TelegramChannelConfig{BotToken: "token", ChatID: "123"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) || len(result.Warnings) != 0 || preparations != 1 {
		t.Fatalf(
			"post-commit apply did not finish: warnings=%v prepares=%d canceled=%v",
			result.Warnings,
			preparations,
			ctx.Err(),
		)
	}
	if channels := manager.GetChannels(); len(channels) != 1 || channels[0].ID != result.Channel.ID {
		t.Fatal("cancellation hid the committed channel from runtime")
	}
	_, err = CreateTelegram(
		ctx,
		notification.ChannelEdit{ChannelName: "canceled"},
		notification.TelegramChannelConfig{BotToken: "second-token", ChatID: "456"},
	)
	if !errors.Is(err, context.Canceled) || preparations != 1 {
		t.Fatalf("canceled write applied: prepares=%d error=%v", preparations, err)
	}
	var count int64
	if err := conn.Model(&notification.NotificationChannel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("canceled write persisted: channels=%d", count)
	}
}

func TestNewRulePreservesRequestedEnabledState(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
	}{{"disabled", false}, {"enabled", true}} {
		t.Run(test.name, func(t *testing.T) {
			conn := setupConfigTest(t)
			channel := notification.NotificationChannel{ChannelType: "webhook", ChannelName: "existing"}
			if err := conn.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			input := notification.NotificationRule{ChannelID: channel.ID, EventType: string(notification.SystemAlert), IsEnabled: test.enabled}
			if _, err := UpdateRule(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			var saved notification.NotificationRule
			if err := conn.Where("channel_id = ? AND event_type = ?", input.ChannelID, input.EventType).First(&saved).Error; err != nil {
				t.Fatal(err)
			}
			if saved.IsEnabled != test.enabled || saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
				t.Fatalf("saved rule: enabled=%v created_at=%v updated_at=%v", saved.IsEnabled, saved.CreatedAt, saved.UpdatedAt)
			}
		})
	}
}

func TestRuleRejectsUnavailableChannelWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name          string
		deleted       bool
		lookupFailure bool
		canceled      bool
	}{
		{name: "missing"},
		{name: "deleted", deleted: true},
		{name: "database failure", lookupFailure: true},
		{name: "canceled", canceled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := setupConfigTest(t)
			channel := notification.NotificationChannel{ChannelType: "webhook", ChannelName: "existing"}
			if err := conn.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			id := channel.ID
			if tc.deleted {
				if err := models.DeleteNotificationChannel(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			} else if !tc.lookupFailure && !tc.canceled {
				id++
			}
			lookupErr := errors.New("notification channel lookup failed")
			if tc.lookupFailure {
				if err := conn.Callback().Query().Before("gorm:query").Register("test:rule_channel_lookup_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "notification_channels" {
						tx.AddError(lookupErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			result, err := UpdateRule(ctx, notification.NotificationRule{ChannelID: id, EventType: string(notification.SystemAlert)})
			if tc.lookupFailure {
				if removeErr := conn.Callback().Query().Remove("test:rule_channel_lookup_failure"); removeErr != nil {
					t.Fatal(removeErr)
				}
				if !errors.Is(err, lookupErr) || errors.Is(err, ErrChannelNotFound) {
					t.Errorf("渠道查询错误被吞掉或误报不存在: %v", err)
				}
			} else if tc.canceled {
				if !errors.Is(err, context.Canceled) {
					t.Errorf("取消原因未保留: %v", err)
				}
			} else if !errors.Is(err, ErrChannelNotFound) {
				t.Errorf("不可用渠道未拒绝规则写入: %v", err)
			}
			var count int64
			if err := conn.Model(&notification.NotificationRule{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != 0 || len(result.Warnings) != 0 {
				t.Errorf("拒绝后仍持久化或应用规则: count=%d result=%+v", count, result)
			}
		})
	}
}

func TestChannelRuleStatusAndDeleteApplyToExistingManager(t *testing.T) {
	conn := setupConfigTest(t)
	received := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- struct{}{}
	}))
	t.Cleanup(remote.Close)
	manager := notificationmanager.NewEnhancedNotificationManager(conn, nil)
	notificationmanager.GlobalEnhancedNotificationManager = manager
	created, err := CreateWebhook(t.Context(), notification.WebhookInput{
		Edit: notification.ChannelEdit{ChannelName: "webhook"},
		Config: notification.CustomWebhookChannelConfig{
			Endpoint: remote.URL,
			Method:   "POST",
			Format:   "text",
			Template: "{{title}}",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	send := func(wantDelivery bool) {
		t.Helper()
		if err := manager.SendNotification(t.Context(), &notification.Notification{Type: notification.SystemAlert, Title: "test"}); err != nil {
			t.Fatal(err)
		}
		select {
		case <-received:
			if !wantDelivery {
				t.Fatal("disabled or deleted notification still delivered")
			}
		default:
			if wantDelivery {
				t.Fatal("committed notification change did not apply")
			}
		}
	}
	send(true)
	rule := notification.NotificationRule{ChannelID: created.Channel.ID, EventType: string(notification.SystemAlert)}
	if _, err := UpdateRule(t.Context(), rule); err != nil {
		t.Fatal(err)
	}
	send(false)
	for _, enabled := range []bool{false, true} {
		if _, err := SetEnabled(t.Context(), created.Channel.ID, enabled); err != nil {
			t.Fatal(err)
		}
		send(false)
	}
	rule.IsEnabled = true
	if _, err := UpdateRule(t.Context(), rule); err != nil {
		t.Fatal(err)
	}
	send(true)
	if _, err := Delete(t.Context(), created.Channel.ID); err != nil {
		t.Fatal(err)
	}
	send(false)
	if _, err := SetEnabled(t.Context(), created.Channel.ID, true); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("deleted channel status error=%v", err)
	}
}
