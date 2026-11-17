package models

import (
	"context"
	"errors"
	"testing"

	"qmediasync/internal/db"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupNotificationPersistenceTest(t *testing.T) *gorm.DB {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	useNotificationPersistenceDB(t, conn)
	return conn
}

func useNotificationPersistenceDB(t *testing.T, conn *gorm.DB) {
	t.Helper()
	old := db.Db
	db.Db = conn
	t.Cleanup(func() { db.Db = old })
	if err := conn.AutoMigrate(&NotificationChannel{}, &NotificationRule{}, &CustomWebhookChannelConfig{}); err != nil {
		t.Fatal(err)
	}
}

func testNotificationPersistence(t *testing.T, conn *gorm.DB) {
	t.Helper()
	channel := NotificationChannel{ChannelType: "webhook", ChannelName: "before", IsEnabled: true}
	config := CustomWebhookChannelConfig{Endpoint: "https://example.invalid/old", Method: "POST", Format: "text", Template: "{{content}}"}
	failure := errors.New("rule write failed")
	if err := conn.Callback().Create().Before("gorm:create").Register("test:fail_rule", func(tx *gorm.DB) {
		if tx.Statement.Table == "notification_rules" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err := CreateNotificationChannel(context.Background(), &channel, func(tx *gorm.DB, id uint) error { config.ChannelID = id; return tx.Create(&config).Error })
	if !errors.Is(err, failure) {
		t.Fatalf("create failure = %v", err)
	}
	for _, model := range []any{&NotificationChannel{}, &CustomWebhookChannelConfig{}, &NotificationRule{}} {
		var count int64
		if err := conn.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed create left %T rows: %d", model, count)
		}
	}
	if err := conn.Callback().Create().Remove("test:fail_rule"); err != nil {
		t.Fatal(err)
	}
	channel.ID = 0
	config.ID = 0
	if err := CreateNotificationChannel(context.Background(), &channel, func(tx *gorm.DB, id uint) error { config.ChannelID = id; return tx.Create(&config).Error }); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&NotificationChannel{}).Where("id = ?", channel.ID).Update("is_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if err := UpdateNotificationChannel(context.Background(), &channel, &config, map[string]any{"endpoint": "https://example.invalid/new"}, "after", ""); err != nil {
		t.Fatal(err)
	}
	if channel.IsEnabled {
		t.Fatal("config save overwrote concurrent disable")
	}
	if err := conn.First(&config, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "https://example.invalid/new" {
		t.Fatal("config was not updated")
	}
	if err := conn.Callback().Update().Before("gorm:update").Register("test:fail_channel", func(tx *gorm.DB) {
		if tx.Statement.Table == "notification_channels" {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = UpdateNotificationChannel(context.Background(), &channel, &config, map[string]any{"endpoint": "https://example.invalid/rejected"}, "rejected", "")
	if !errors.Is(err, failure) {
		t.Fatalf("update failure = %v", err)
	}
	if err := conn.First(&config, config.ID).Error; err != nil {
		t.Fatal(err)
	}
	if config.Endpoint != "https://example.invalid/new" {
		t.Fatal("partial config write survived transaction failure")
	}
	if err := conn.Callback().Update().Remove("test:fail_channel"); err != nil {
		t.Fatal(err)
	}
	var rule NotificationRule
	for _, missing := range []bool{false, true} {
		if missing {
			if err := conn.Delete(&rule).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := UpdateNotificationRule(t.Context(), NotificationRule{ChannelID: channel.ID, EventType: string(SystemAlert), IsEnabled: false}); err != nil {
			t.Fatal(err)
		}
		rule = NotificationRule{}
		if err := conn.Where("channel_id = ? AND event_type = ?", channel.ID, SystemAlert).First(&rule).Error; err != nil {
			t.Fatal(err)
		}
		if rule.IsEnabled || rule.CreatedAt.IsZero() || rule.UpdatedAt.IsZero() {
			t.Fatalf("规则未保留显式 false 或时间字段: %+v", rule)
		}
	}
	if err := DeleteNotificationChannel(context.Background(), channel.ID); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&NotificationChannel{}, &CustomWebhookChannelConfig{}, &NotificationRule{}} {
		var count int64
		if err := conn.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("delete left %T rows: %d", model, count)
		}
	}
}

func TestNotificationPersistence(t *testing.T) {
	testNotificationPersistence(t, setupNotificationPersistenceTest(t))
}
