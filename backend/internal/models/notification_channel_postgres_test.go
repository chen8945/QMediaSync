//go:build integration

package models

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestNotificationPersistencePostgres(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	useNotificationPersistenceDB(t, conn)
	testNotificationPersistence(t, conn)
}

func TestNotificationWriteDeleteSerializationPostgres(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deleteFirst bool
		editChannel bool
	}{
		{name: "rule first"},
		{name: "delete first", deleteFirst: true},
		{name: "channel edit first", editChannel: true},
		{name: "delete before channel edit", deleteFirst: true, editChannel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, schema := setupEmbySnapshotPostgres(t)
			useNotificationPersistenceDB(t, conn)
			channel := NotificationChannel{ChannelType: "webhook", ChannelName: "existing"}
			if err := conn.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			config := CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: "https://example.invalid/old"}
			if err := conn.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			channelID, edited := channel.ID, channel
			ctx, cancel := context.WithCancel(t.Context())
			reached, release := make(chan int, 1), make(chan struct{})
			var paused atomic.Bool
			pause := func(tx *gorm.DB) {
				table := "notification_rules"
				if tc.editChannel {
					table = "custom_webhook_channel_configs"
				}
				if tc.deleteFirst {
					table = "notification_channels"
				}
				if tx.Statement.Table != table || !paused.CompareAndSwap(false, true) {
					return
				}
				var pid int
				if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
					tx.AddError(err)
					return
				}
				reached <- pid
				<-release
			}
			if tc.deleteFirst {
				if err := conn.Callback().Delete().Before("gorm:delete").Register("test:notification_pause", pause); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Callback().Delete().Remove("test:notification_pause") })
			} else if tc.editChannel {
				if err := conn.Callback().Update().Before("gorm:update").Register("test:notification_pause", pause); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Callback().Update().Remove("test:notification_pause") })
			} else {
				if err := conn.Callback().Query().Before("gorm:query").Register("test:notification_pause", pause); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = conn.Callback().Query().Remove("test:notification_pause") })
			}
			var workers sync.WaitGroup
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(func() { cancel(); unblock(); workers.Wait() })
			first := func() error {
				if tc.editChannel {
					return UpdateNotificationChannel(ctx, &edited, &config, map[string]any{"endpoint": "https://example.invalid/new"}, "updated", "")
				}
				return UpdateNotificationRule(ctx, NotificationRule{ChannelID: channelID, EventType: string(SystemAlert), IsEnabled: false})
			}
			second := func() error { return DeleteNotificationChannel(ctx, channelID) }
			if tc.deleteFirst {
				first, second = second, first
			}
			firstResult, secondResult := make(chan error, 1), make(chan error, 1)
			workers.Go(func() { firstResult <- first() })
			var blockerPID int
			select {
			case blockerPID = <-reached:
			case <-time.After(5 * time.Second):
				t.Fatal("首个事务未到达通知写入或渠道删除边界")
			}
			workers.Go(func() { secondResult <- second() })
			waitEmbyPostgresLock(t, conn, schema, blockerPID, secondResult)
			unblock()
			if err := receiveEmbyPostgresResult(t, firstResult); err != nil {
				t.Fatalf("先进入的事务失败: %v", err)
			}
			err := receiveEmbyPostgresResult(t, secondResult)
			if tc.deleteFirst {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Errorf("删除后仍可保存通知配置: %v", err)
				}
			} else if err != nil {
				t.Errorf("通知配置保存后未能删除渠道: %v", err)
			}
			for _, model := range []any{&NotificationChannel{}, &NotificationRule{}, &CustomWebhookChannelConfig{}} {
				var count int64
				if err := conn.Model(model).Count(&count).Error; err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Errorf("并发删除遗留记录: %T count=%d", model, count)
				}
			}
		})
	}
}
