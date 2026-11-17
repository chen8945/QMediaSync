// Package config 编排通知配置持久化与提交后的运行态应用。
package config

import (
	"context"
	"errors"
	"fmt"
	"log"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/validation"
)

var (
	ErrChannelNotFound     = errors.New("notification channel not found")
	ErrConfigNotFound      = errors.New("notification channel config not found")
	ErrChannelTypeMismatch = errors.New("notification channel type mismatch")
)

// Result 返回已提交的渠道及应用警告；开关、删除和规则操作不携带渠道数据。
type Result struct {
	Channel  notification.NotificationChannel
	Warnings []string
}

func loadChannel(ctx context.Context, id uint, channelType string, cfg any) (notification.NotificationChannel, error) {
	var channel notification.NotificationChannel
	if err := db.Db.WithContext(ctx).First(&channel, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return channel, fmt.Errorf("%w: %w", ErrChannelNotFound, err)
		}
		return channel, fmt.Errorf("load notification channel: %w", err)
	}
	if channel.ChannelType != channelType {
		return channel, ErrChannelTypeMismatch
	}
	if err := db.Db.WithContext(ctx).Where("channel_id = ?", id).First(cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return channel, fmt.Errorf("%w: %w", ErrConfigNotFound, err)
		}
		return channel, fmt.Errorf("load notification config: %w", err)
	}
	return channel, nil
}

func createChannel(
	ctx context.Context,
	edit notification.ChannelEdit,
	channelType string,
	save func(*gorm.DB, uint) error,
	secrets ...string,
) (Result, error) {
	channel := notification.NotificationChannel{
		ChannelType: channelType,
		ChannelName: edit.ChannelName,
		Description: edit.Description,
		IsEnabled:   true,
	}
	if err := models.CreateNotificationChannel(ctx, &channel, save); err != nil {
		return Result{}, fmt.Errorf("create notification channel: %w", err)
	}
	return Result{Channel: channel, Warnings: reloadChannel(channel.ID, secrets...)}, nil
}

func saveChannel(
	ctx context.Context,
	edit notification.ChannelEdit,
	channel notification.NotificationChannel,
	cfg any,
	fields map[string]any,
	secrets ...string,
) (Result, error) {
	err := models.UpdateNotificationChannel(
		ctx,
		&channel,
		cfg,
		fields,
		edit.ChannelName,
		edit.Description,
	)
	if err != nil {
		return Result{}, fmt.Errorf("update notification channel: %w", err)
	}
	return Result{Channel: channel, Warnings: reloadChannel(channel.ID, secrets...)}, nil
}

func reloadChannel(id uint, secrets ...string) []string {
	if manager := notificationmanager.GlobalEnhancedNotificationManager; manager != nil {
		// 持久化已提交，浏览器取消不能跳过同步应用。
		return applyWarning(manager.ReloadChannel(id), secrets...)
	}
	return nil
}

func applyWarning(err error, secrets ...string) []string {
	if err == nil {
		return nil
	}
	cause := helpers.RedactSensitiveLog(err.Error(), secrets...)
	if helpers.AppLogger != nil {
		helpers.AppLogger.Warnf("通知设置已保存，但应用失败：%s", cause)
	} else {
		log.Printf("通知设置已保存，但应用失败：%s", cause)
	}
	return []string{"设置已保存，但通知配置应用失败，请重新保存重试"}
}

// SetEnabled 只保存启用列并应用现有渠道，不覆盖并发配置编辑。
func SetEnabled(ctx context.Context, id uint, enabled bool) (Result, error) {
	if id == 0 {
		return Result{}, validation.New("channel_id", "不能为空")
	}
	update := db.Db.WithContext(ctx).
		Model(&notification.NotificationChannel{}).
		Where("id = ?", id).
		Update("is_enabled", enabled)
	if update.Error != nil {
		return Result{}, fmt.Errorf("update notification channel status: %w", update.Error)
	}
	if update.RowsAffected != 1 {
		return Result{}, ErrChannelNotFound
	}
	return Result{Warnings: reloadChannel(id)}, nil
}

// Delete 复用原子删除，并在提交后退役现有渠道。
func Delete(ctx context.Context, id uint) (Result, error) {
	if id == 0 {
		return Result{}, validation.New("channel_id", "不能为空")
	}
	if err := models.DeleteNotificationChannel(ctx, id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Result{}, fmt.Errorf("%w: %w", ErrChannelNotFound, err)
		}
		return Result{}, fmt.Errorf("delete notification channel: %w", err)
	}
	if manager := notificationmanager.GlobalEnhancedNotificationManager; manager != nil {
		manager.RemoveChannel(id)
	}
	return Result{}, nil
}

// UpdateRule 通过模型事务保存现有渠道的规则，再应用规则路由。
func UpdateRule(ctx context.Context, input notification.NotificationRule) (Result, error) {
	if input.ChannelID == 0 {
		return Result{}, validation.New("channel_id", "不能为空")
	}
	if input.EventType == "" {
		return Result{}, validation.New("event_type", "不能为空")
	}
	if err := models.UpdateNotificationRule(ctx, input); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Result{}, fmt.Errorf("%w: %w", ErrChannelNotFound, err)
		}
		return Result{}, fmt.Errorf("update notification rule: %w", err)
	}
	result := Result{}
	if manager := notificationmanager.GlobalEnhancedNotificationManager; manager != nil {
		result.Warnings = applyWarning(manager.ReloadRules())
	}
	return result, nil
}
