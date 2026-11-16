package config

import (
	"context"

	"gorm.io/gorm"

	"qmediasync/internal/notification"
)

// CreateTelegram 校验并创建 Telegram 渠道，提交后应用现有通知管理器。
func CreateTelegram(
	ctx context.Context,
	edit notification.ChannelEdit,
	cfg notification.TelegramChannelConfig,
) (Result, error) {
	if err := notification.ValidateTelegramCreate(edit, cfg); err != nil {
		return Result{}, err
	}
	return createChannel(
		ctx,
		edit,
		"telegram",
		func(tx *gorm.DB, id uint) error {
			cfg.ChannelID = id
			return tx.Create(&cfg).Error
		},
		cfg.BotToken,
	)
}

// UpdateTelegram 只保存本次非空补丁，保留既有字段所有权。
func UpdateTelegram(
	ctx context.Context,
	edit notification.ChannelEdit,
	input notification.TelegramChannelConfig,
) (Result, error) {
	if err := notification.ValidateTelegramUpdate(edit, input); err != nil {
		return Result{}, err
	}
	var cfg notification.TelegramChannelConfig
	channel, err := loadChannel(ctx, edit.ChannelID, "telegram", &cfg)
	if err != nil {
		return Result{}, err
	}
	fields := make(map[string]any)
	if input.BotToken != "" {
		fields["bot_token"] = input.BotToken
	}
	if input.ChatID != "" {
		fields["chat_id"] = input.ChatID
	}
	return saveChannel(
		ctx,
		edit,
		channel,
		&cfg,
		fields,
		cfg.BotToken,
		input.BotToken,
	)
}

// CreateMeoW 校验并创建 MeoW 渠道，提交后应用现有通知管理器。
func CreateMeoW(
	ctx context.Context,
	edit notification.ChannelEdit,
	cfg notification.MeoWChannelConfig,
) (Result, error) {
	if err := notification.ValidateMeoWCreate(edit, cfg); err != nil {
		return Result{}, err
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "http://api.chuckfang.com"
	}
	return createChannel(
		ctx,
		edit,
		"meow",
		func(tx *gorm.DB, id uint) error {
			cfg.ChannelID = id
			return tx.Create(&cfg).Error
		},
		cfg.Nickname,
	)
}

// UpdateMeoW 只保存本次非空补丁，保留既有字段所有权。
func UpdateMeoW(
	ctx context.Context,
	edit notification.ChannelEdit,
	input notification.MeoWChannelConfig,
) (Result, error) {
	if err := notification.ValidateMeoWUpdate(edit, input); err != nil {
		return Result{}, err
	}
	var cfg notification.MeoWChannelConfig
	channel, err := loadChannel(ctx, edit.ChannelID, "meow", &cfg)
	if err != nil {
		return Result{}, err
	}
	fields := make(map[string]any)
	if input.Nickname != "" {
		fields["nickname"] = input.Nickname
	}
	if input.Endpoint != "" {
		fields["endpoint"] = input.Endpoint
	}
	return saveChannel(
		ctx,
		edit,
		channel,
		&cfg,
		fields,
		cfg.Nickname,
		input.Nickname,
	)
}

// CreateBark 校验并创建 Bark 渠道，提交后应用现有通知管理器。
func CreateBark(
	ctx context.Context,
	edit notification.ChannelEdit,
	cfg notification.BarkChannelConfig,
) (Result, error) {
	if err := notification.ValidateBarkCreate(edit, cfg); err != nil {
		return Result{}, err
	}
	if cfg.ServerURL == "" {
		cfg.ServerURL = "https://api.day.app"
	}
	if cfg.Sound == "" {
		cfg.Sound = "alert"
	}
	return createChannel(
		ctx,
		edit,
		"bark",
		func(tx *gorm.DB, id uint) error {
			cfg.ChannelID = id
			return tx.Create(&cfg).Error
		},
		cfg.DeviceKey,
	)
}

// UpdateBark 只保存本次非空补丁，保留既有字段所有权。
func UpdateBark(
	ctx context.Context,
	edit notification.ChannelEdit,
	input notification.BarkChannelConfig,
) (Result, error) {
	if err := notification.ValidateBarkUpdate(edit, input); err != nil {
		return Result{}, err
	}
	var cfg notification.BarkChannelConfig
	channel, err := loadChannel(ctx, edit.ChannelID, "bark", &cfg)
	if err != nil {
		return Result{}, err
	}
	fields := make(map[string]any)
	if input.DeviceKey != "" {
		fields["device_key"] = input.DeviceKey
	}
	if input.ServerURL != "" {
		fields["server_url"] = input.ServerURL
	}
	if input.Sound != "" {
		fields["sound"] = input.Sound
	}
	if input.Icon != "" {
		fields["icon"] = input.Icon
	}
	return saveChannel(
		ctx,
		edit,
		channel,
		&cfg,
		fields,
		cfg.DeviceKey,
		input.DeviceKey,
	)
}

// CreateServerChan 校验并创建 ServerChan 渠道，提交后应用现有通知管理器。
func CreateServerChan(
	ctx context.Context,
	edit notification.ChannelEdit,
	cfg notification.ServerChanChannelConfig,
) (Result, error) {
	if err := notification.ValidateServerChanCreate(edit, cfg); err != nil {
		return Result{}, err
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = "https://sc.ftqq.com"
	}
	return createChannel(
		ctx,
		edit,
		"serverchan",
		func(tx *gorm.DB, id uint) error {
			cfg.ChannelID = id
			return tx.Create(&cfg).Error
		},
		cfg.SCKEY,
	)
}

// UpdateServerChan 只保存本次非空补丁，保留既有字段所有权。
func UpdateServerChan(
	ctx context.Context,
	edit notification.ChannelEdit,
	input notification.ServerChanChannelConfig,
) (Result, error) {
	if err := notification.ValidateServerChanUpdate(edit, input); err != nil {
		return Result{}, err
	}
	var cfg notification.ServerChanChannelConfig
	channel, err := loadChannel(ctx, edit.ChannelID, "serverchan", &cfg)
	if err != nil {
		return Result{}, err
	}
	fields := make(map[string]any)
	if input.SCKEY != "" {
		fields["sckey"] = input.SCKEY
	}
	if input.Endpoint != "" {
		fields["endpoint"] = input.Endpoint
	}
	return saveChannel(
		ctx,
		edit,
		channel,
		&cfg,
		fields,
		cfg.SCKEY,
		input.SCKEY,
	)
}
