package config

import (
	"context"
	"encoding/json"
	"fmt"

	"gorm.io/gorm"

	"qmediasync/internal/notification"
)

// CreateWebhook 校验并保存渠道、配置和默认规则，提交后应用。
func CreateWebhook(ctx context.Context, input notification.WebhookInput) (Result, error) {
	if err := notification.ValidateWebhookCreate(&input); err != nil {
		return Result{}, err
	}
	cfg := input.Config
	if input.Headers != nil {
		data, err := json.Marshal(input.Headers)
		if err != nil {
			return Result{}, fmt.Errorf("encode webhook headers: %w", err)
		}
		cfg.Headers = string(data)
	}
	return createChannel(
		ctx,
		input.Edit,
		"webhook",
		func(tx *gorm.DB, id uint) error {
			cfg.ChannelID = id
			return tx.Create(&cfg).Error
		},
		cfg.AuthToken,
		cfg.AuthPass,
	)
}

// UpdateWebhook 根据存量方法和格式校验本次补丁，不重验整个最终配置。
func UpdateWebhook(ctx context.Context, input notification.WebhookInput) (Result, error) {
	if input.Edit.ChannelID == 0 {
		return Result{}, notification.ValidateWebhookUpdate(&input, "", "")
	}
	var cfg notification.CustomWebhookChannelConfig
	channel, err := loadChannel(ctx, input.Edit.ChannelID, "webhook", &cfg)
	if err != nil {
		return Result{}, err
	}
	if err := notification.ValidateWebhookUpdate(&input, cfg.Method, cfg.Format); err != nil {
		return Result{}, err
	}
	fields := make(map[string]any)
	if input.Config.Endpoint != "" {
		fields["endpoint"] = input.Config.Endpoint
	}
	if input.Config.Method != "" {
		fields["method"] = input.Config.Method
	}
	if input.Config.Template != "" {
		fields["template"] = input.Config.Template
	}
	if input.Config.Format != "" {
		fields["format"] = input.Config.Format
	}
	if input.Config.QueryParam != "" {
		fields["query_param"] = input.Config.QueryParam
	}
	if input.Config.AuthType != "" {
		fields["auth_type"] = input.Config.AuthType
	}
	if input.Config.AuthToken != "" {
		fields["auth_token"] = input.Config.AuthToken
	}
	if input.Config.AuthUser != "" {
		fields["auth_user"] = input.Config.AuthUser
	}
	if input.Config.AuthPass != "" {
		fields["auth_pass"] = input.Config.AuthPass
	}
	if input.Config.AuthHeaderKey != "" {
		fields["auth_header_key"] = input.Config.AuthHeaderKey
	}
	if input.Config.AuthQueryKey != "" {
		fields["auth_query_key"] = input.Config.AuthQueryKey
	}
	if input.Headers != nil {
		data, err := json.Marshal(input.Headers)
		if err != nil {
			return Result{}, fmt.Errorf("encode webhook headers: %w", err)
		}
		fields["headers"] = string(data)
	}
	return saveChannel(
		ctx,
		input.Edit,
		channel,
		&cfg,
		fields,
		cfg.AuthToken,
		cfg.AuthPass,
		input.Config.AuthToken,
		input.Config.AuthPass,
	)
}
