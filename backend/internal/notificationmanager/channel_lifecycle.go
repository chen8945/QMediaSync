package notificationmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
	"qmediasync/internal/validation"

	"gorm.io/gorm"
)

var newTelegramChannelHandler = NewTelegramChannelHandlerWithProxy

// LoadChannels 重载现有实例；逐实体重新读库，旧的列表不能卸载后来创建的渠道。
func (m *EnhancedNotificationManager) LoadChannels() error {
	var channels []notification.NotificationChannel
	if err := m.db.Select("id").Find(&channels).Error; err != nil {
		return err
	}
	ids := make(map[uint]struct{}, len(channels))
	for _, channel := range channels {
		ids[channel.ID] = struct{}{}
	}
	m.mu.RLock()
	for id := range m.handlers {
		ids[id] = struct{}{}
	}
	m.mu.RUnlock()
	var errs []error
	for id := range ids {
		if err := m.ReloadChannel(id); err != nil {
			errs = append(errs, err)
		}
	}
	if err := m.ReloadRules(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ReloadChannel 锁外准备一致候选，只有最新请求可以发布。
func (m *EnhancedNotificationManager) ReloadChannel(channelID uint) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return fmt.Errorf("通知管理器已停止")
	}
	m.revisions[channelID]++
	revision := m.revisions[channelID]
	m.mu.Unlock()
	var candidate *channelInfo
	var rules []notification.NotificationRule
	deleted := false
	err := m.db.Transaction(func(tx *gorm.DB) error {
		var channel notification.NotificationChannel
		if err := tx.First(&channel, channelID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				deleted = true
				return nil
			}
			return err
		}
		if !channel.IsEnabled {
			return nil
		}
		handler, err := m.createChannelHandler(tx, &channel)
		if err != nil {
			return err
		}
		if err := tx.Where("channel_id = ? AND is_enabled = ?", channelID, true).Find(&rules).Error; err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(context.Background())
		candidate = &channelInfo{handler: handler, config: &channel, ctx: ctx, cancel: cancel}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		retireChannel(candidate)
		return err
	}
	m.mu.Lock()
	if m.stopped || revision != m.revisions[channelID] {
		m.mu.Unlock()
		retireChannel(candidate)
		return fmt.Errorf("通知渠道应用已被后续重载或停止替代，请重新保存重试")
	}
	old := m.handlers[channelID]
	if candidate == nil {
		delete(m.handlers, channelID)
		if deleted {
			delete(m.telegramUpdates, channelID)
		}
	} else {
		m.handlers[channelID] = candidate
		if tg, ok := candidate.handler.(*TelegramChannelHandler); ok {
			// 回复未完成的更新尚未向 Telegram 确认，重载和重新启用需保留本地游标。
			if cursor, ok := m.telegramUpdates[channelID]; ok && cursor.token == tg.configToken() {
				tg.updateOffset = cursor.offset
			} else {
				m.telegramUpdates[channelID] = telegramUpdateCursor{token: tg.configToken(), offset: tg.updateOffset}
			}
			m.backgrounds[tg] = struct{}{}
		} else {
			delete(m.telegramUpdates, channelID)
		}
	}
	m.rulesRevision++
	m.replaceChannelRules(channelID, rules)
	started := m.started
	commands, commandRevision := m.telegramCommands, m.commandRevision
	m.mu.Unlock()
	if candidate != nil {
		if tg, ok := candidate.handler.(*TelegramChannelHandler); ok {
			tg.setCommandSnapshot(commands, commandRevision)
		}
	}
	retireChannel(old)
	m.pruneBackgrounds()
	if started {
		startChannel(candidate)
	}
	return nil
}

// ReloadRules 失败保留旧路由，锁外读库，发布时过滤当前登记。
func (m *EnhancedNotificationManager) ReloadRules() error {
	m.mu.Lock()
	m.rulesRevision++
	revision := m.rulesRevision
	for id := range m.revisions {
		m.revisions[id]++
	}
	m.mu.Unlock()
	var rules []notification.NotificationRule
	if err := m.db.Model(&notification.NotificationRule{}).
		Joins("JOIN notification_channels ON notification_channels.id = notification_rules.channel_id").
		Where("notification_rules.is_enabled = ? AND notification_channels.is_enabled = ?", true, true).
		Find(&rules).Error; err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopped || revision != m.rulesRevision {
		return fmt.Errorf("通知规则应用已被后续重载或停止替代，请重新保存重试")
	}
	routes := make(map[string][]uint)
	for _, rule := range rules {
		if _, ok := m.handlers[rule.ChannelID]; ok {
			routes[rule.EventType] = append(routes[rule.EventType], rule.ChannelID)
		}
	}
	m.rules = routes
	return nil
}

// RemoveChannel 同时失效准备中的候选和已有路由。
func (m *EnhancedNotificationManager) RemoveChannel(channelID uint) {
	m.mu.Lock()
	m.revisions[channelID]++
	old := m.handlers[channelID]
	delete(m.handlers, channelID)
	delete(m.telegramUpdates, channelID)
	m.replaceChannelRules(channelID, nil)
	m.mu.Unlock()
	retireChannel(old)
}

// replaceChannelRules 与渠道登记在同一状态锁下发布，不影响其他渠道。
func (m *EnhancedNotificationManager) replaceChannelRules(channelID uint, rules []notification.NotificationRule) {
	for event, ids := range m.rules {
		next := make([]uint, 0, len(ids))
		for _, id := range ids {
			if id != channelID {
				next = append(next, id)
			}
		}
		m.rules[event] = next
	}
	for _, rule := range rules {
		m.rules[rule.EventType] = append(m.rules[rule.EventType], channelID)
	}
}

func (m *EnhancedNotificationManager) pruneBackgrounds() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for handler := range m.backgrounds {
		if handler.finished() {
			delete(m.backgrounds, handler)
		}
	}
}

// StartAll 启动已登记后台；重复调用不重复启动。
func (m *EnhancedNotificationManager) StartAll() {
	m.mu.Lock()
	if m.started || m.stopped {
		m.mu.Unlock()
		return
	}
	m.started = true
	infos := make([]*channelInfo, 0, len(m.handlers))
	for _, info := range m.handlers {
		infos = append(infos, info)
	}
	m.mu.Unlock()
	for _, info := range infos {
		startChannel(info)
	}
}

// StopAll 关闭后台准入，迟到候选不能再次发布。
func (m *EnhancedNotificationManager) StopAll() {
	m.mu.Lock()
	m.stopped = true
	infos := make([]*channelInfo, 0, len(m.handlers))
	for _, info := range m.handlers {
		infos = append(infos, info)
	}
	m.handlers = make(map[uint]*channelInfo)
	m.telegramUpdates = make(map[uint]telegramUpdateCursor)
	m.rules = make(map[string][]uint)
	m.mu.Unlock()
	for _, info := range infos {
		retireChannel(info)
	}
}

// StopAllContext 取消并等待真实 Bot/命令后台，旧退役后台也必须完成。
func (m *EnhancedNotificationManager) StopAllContext(ctx context.Context) error {
	m.StopAll()
	m.mu.RLock()
	handlers := make([]*TelegramChannelHandler, 0, len(m.backgrounds))
	for handler := range m.backgrounds {
		handlers = append(handlers, handler)
	}
	m.mu.RUnlock()
	for _, handler := range handlers {
		if err := handler.wait(ctx); err != nil {
			return err
		}
	}
	m.pruneBackgrounds()
	return nil
}

func startChannel(info *channelInfo) {
	if info == nil || info.ctx == nil || info.ctx.Err() != nil {
		return
	}
	if bg, ok := info.handler.(BackgroundHandler); ok {
		bg.Start(info.ctx)
	}
}

func retireChannel(info *channelInfo) {
	if info == nil {
		return
	}
	if info.cancel != nil {
		info.cancel()
	}
	if bg, ok := info.handler.(BackgroundHandler); ok {
		bg.Stop()
	}
}

func (m *EnhancedNotificationManager) createChannelHandler(tx *gorm.DB, channel *notification.NotificationChannel) (ChannelHandler, error) {
	switch channel.ChannelType {
	case "telegram":
		var config notification.TelegramChannelConfig
		if err := tx.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Telegram 配置不存在：%v", err)
		}
		// 通过回调函数获取代理 URL
		var proxyURL string
		if m.getProxyURL != nil {
			proxyURL = m.getProxyURL()
		}
		// 代理地址可能带用户名密码，QLogger 只按键值对脱敏、识别不了 URL 里的 userinfo，必须在调用点遮蔽
		helpers.AppLogger.Infof("为 Telegram 渠道使用代理：%s", validation.RedactProxyURL(proxyURL))
		return newTelegramChannelHandler(&config, proxyURL), nil

	case "meow":
		var config notification.MeoWChannelConfig
		if err := tx.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("MeoW 配置不存在：%v", err)
		}
		return NewMeoWChannelHandler(&config), nil

	case "bark":
		var config notification.BarkChannelConfig
		if err := tx.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Bark 配置不存在：%v", err)
		}
		return NewBarkChannelHandler(&config), nil

	case "serverchan":
		var config notification.ServerChanChannelConfig
		if err := tx.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Server酱配置不存在：%v", err)
		}
		return NewServerChanChannelHandler(&config), nil

	case "webhook":
		var config notification.CustomWebhookChannelConfig
		if err := tx.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Webhook 配置不存在：%v", err)
		}
		return NewCustomWebhookChannelHandler(&config), nil

	default:
		return nil, fmt.Errorf("未知的渠道类型：%s", channel.ChannelType)
	}
}
