package models

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/notification"
)

// NotificationChannel 通知渠道基础配置 - 别名供 models 包使用
type NotificationChannel = notification.NotificationChannel

// TelegramChannelConfig Telegram 渠道配置 - 别名供 models 包使用
type TelegramChannelConfig = notification.TelegramChannelConfig

// MeoWChannelConfig MeoW 渠道配置 - 别名供 models 包使用
type MeoWChannelConfig = notification.MeoWChannelConfig

// BarkChannelConfig Bark 渠道配置 - 别名供 models 包使用
type BarkChannelConfig = notification.BarkChannelConfig

// ServerChanChannelConfig Server酱渠道配置 - 别名供 models 包使用
type ServerChanChannelConfig = notification.ServerChanChannelConfig

// NotificationRule 通知规则 - 别名供 models 包使用
type NotificationRule = notification.NotificationRule

// NotificationType 通知类型枚举 - 从 internal/notification 导入
type NotificationType = notification.NotificationType

const (
	SyncFinished   NotificationType = notification.SyncFinished
	SyncError      NotificationType = notification.SyncError
	ScrapeFinished NotificationType = notification.ScrapeFinished
	ScrapeError    NotificationType = notification.ScrapeError
	SystemAlert    NotificationType = notification.SystemAlert
	MediaAdded     NotificationType = notification.MediaAdded
	MediaRemoved   NotificationType = notification.MediaRemoved
	PlaybackStart  NotificationType = notification.PlaybackStart
	PlaybackPause  NotificationType = notification.PlaybackPause
	PlaybackStop   NotificationType = notification.PlaybackStop
)

// NotificationPriority 通知优先级 - 从 internal/notification 导入
type NotificationPriority = notification.NotificationPriority

const (
	HighPriority   NotificationPriority = notification.HighPriority
	NormalPriority NotificationPriority = notification.NormalPriority
	LowPriority    NotificationPriority = notification.LowPriority
)

// Notification 统一通知对象 - 从 internal/notification 导入
type Notification = notification.Notification

// CustomWebhookChannelConfig 自定义 Webhook 渠道配置 - 别名供 models 包使用
type CustomWebhookChannelConfig = notification.CustomWebhookChannelConfig

// CreateNotificationChannel 在同一事务内保存渠道、类型配置和默认规则。
func CreateNotificationChannel(ctx context.Context, channel *NotificationChannel, saveConfig func(*gorm.DB, uint) error) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(channel).Error; err != nil {
			return err
		}
		if err := saveConfig(tx, channel.ID); err != nil {
			return err
		}
		rules := make([]NotificationRule, 0, len(notification.AllNotificationTypes))
		for _, event := range notification.AllNotificationTypes {
			rules = append(rules, NotificationRule{ChannelID: channel.ID, EventType: string(event), IsEnabled: true})
		}
		return tx.Create(&rules).Error
	})
}

// UpdateNotificationChannel 只保存请求自有字段，不用旧渠道覆盖并发启用状态。
func UpdateNotificationChannel(ctx context.Context, channel *NotificationChannel, config any, fields map[string]any, name, description string) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current NotificationChannel
		if err := tx.First(&current, channel.ID).Error; err != nil {
			return err
		}
		if current.ChannelType != channel.ChannelType {
			return fmt.Errorf("通知渠道类型已变化")
		}
		if len(fields) > 0 {
			result := tx.Model(config).Where("channel_id = ?", channel.ID).Updates(fields)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		basic := make(map[string]any)
		if name != "" {
			basic["channel_name"] = name
		}
		if description != "" {
			basic["description"] = description
		}
		if len(basic) > 0 {
			result := tx.Model(&current).Updates(basic)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		*channel = current
		return nil
	})
}

// DeleteNotificationChannel 原子删除规则、类型配置和渠道。
func DeleteNotificationChannel(ctx context.Context, id uint) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var channel NotificationChannel
		if err := tx.First(&channel, id).Error; err != nil {
			return err
		}
		if err := tx.Where("channel_id = ?", id).Delete(&NotificationRule{}).Error; err != nil {
			return err
		}
		var config any
		switch channel.ChannelType {
		case "telegram":
			config = &TelegramChannelConfig{}
		case "meow":
			config = &MeoWChannelConfig{}
		case "bark":
			config = &BarkChannelConfig{}
		case "serverchan":
			config = &ServerChanChannelConfig{}
		case "webhook":
			config = &CustomWebhookChannelConfig{}
		}
		if config != nil {
			if err := tx.Where("channel_id = ?", id).Delete(config).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&channel).Error
	})
}
