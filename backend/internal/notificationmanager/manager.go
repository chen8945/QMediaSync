package notificationmanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
	"qmediasync/internal/validation"

	"gorm.io/gorm"
)

// ============ 增强的通知管理器 ============

// EnhancedNotificationManager 增强的通知管理器
type EnhancedNotificationManager struct {
	handlers         map[uint]*channelInfo // key：ChannelID，value：handler + config
	rules            map[string][]uint     // key：EventType，value：ChannelIDs
	telegramCommands map[string]func([]string) helpers.CommandResponse
	mu               sync.RWMutex
	db               *gorm.DB
	getProxyURL      func() string // 获取代理 URL 的回调函数
}

type channelInfo struct {
	handler ChannelHandler
	config  *notification.NotificationChannel
}

// GlobalEnhancedNotificationManager 全局增强的通知管理器
var GlobalEnhancedNotificationManager *EnhancedNotificationManager

// NewEnhancedNotificationManager 创建增强的通知管理器
// getProxyURL：获取系统代理 URL 的回调函数，返回空字符串表示不使用代理
func NewEnhancedNotificationManager(db *gorm.DB, getProxyURL func() string) *EnhancedNotificationManager {
	return &EnhancedNotificationManager{
		handlers:    make(map[uint]*channelInfo),
		rules:       make(map[string][]uint),
		db:          db,
		getProxyURL: getProxyURL,
	}
}

// LoadChannels 从数据库加载启用的渠道
func (m *EnhancedNotificationManager) LoadChannels() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.handlers = make(map[uint]*channelInfo)

	// 加载所有启用的通知渠道
	var channels []notification.NotificationChannel
	if err := m.db.Where("is_enabled = ?", true).Find(&channels).Error; err != nil {
		helpers.AppLogger.Errorf("加载通知渠道失败：%v", err)
		return err
	}

	// 为每个渠道创建处理器
	for _, channel := range channels {
		handler, err := m.createChannelHandler(&channel)
		if err != nil {
			helpers.AppLogger.Warnf("创建渠道处理器失败 [%s]：%v", channel.ChannelType, err)
			continue
		}

		m.handlers[channel.ID] = &channelInfo{
			handler: handler,
			config:  &channel,
		}
	}

	m.loadEnabledRulesLocked()

	helpers.AppLogger.Infof("已加载 %d 个通知渠道", len(m.handlers))
	return nil
}

func (m *EnhancedNotificationManager) loadEnabledRulesLocked() {
	m.rules = make(map[string][]uint)
	var rules []notification.NotificationRule
	err := m.db.
		Model(&notification.NotificationRule{}).
		Joins("JOIN notification_channels ON notification_channels.id = notification_rules.channel_id").
		Where("notification_rules.is_enabled = ? AND notification_channels.is_enabled = ?", true, true).
		Find(&rules).Error
	if err != nil {
		helpers.AppLogger.Warnf("加载通知规则失败：%v", err)
		return
	}
	for _, rule := range rules {
		if _, ok := m.handlers[rule.ChannelID]; ok {
			m.rules[rule.EventType] = append(m.rules[rule.EventType], rule.ChannelID)
		}
	}
}

// StartAll 启动所有支持后台运行的渠道处理器
func (m *EnhancedNotificationManager) StartAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	helpers.AppLogger.Info("正在启动所有通知渠道监听器…")

	count := 0
	for _, info := range m.handlers {
		if bg, ok := info.handler.(BackgroundHandler); ok {
			// 将 main 传入的 ctx 传递给每一个 handler
			bg.Start(context.Background())
			count++
		}
	}

	helpers.AppLogger.Infof("成功启动 %d 个后台监听器", count)
}

func (m *EnhancedNotificationManager) createChannelHandler(channel *notification.NotificationChannel) (ChannelHandler, error) {
	switch channel.ChannelType {
	case "telegram":
		var config notification.TelegramChannelConfig
		if err := m.db.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Telegram 配置不存在：%v", err)
		}
		// 通过回调函数获取代理 URL
		var proxyURL string
		if m.getProxyURL != nil {
			proxyURL = m.getProxyURL()
		}
		// 代理地址可能带用户名密码，QLogger 只按键值对脱敏、识别不了 URL 里的 userinfo，必须在调用点遮蔽
		helpers.AppLogger.Infof("为 Telegram 渠道使用代理：%s", validation.RedactProxyURL(proxyURL))
		var handler *TelegramChannelHandler
		if proxyURL != "" {
			handler = NewTelegramChannelHandlerWithProxy(&config, proxyURL)
		} else {
			handler = NewTelegramChannelHandler(&config)
		}
		m.applyTelegramCommands(handler)
		return handler, nil

	case "meow":
		var config notification.MeoWChannelConfig
		if err := m.db.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("MeoW 配置不存在：%v", err)
		}
		return NewMeoWChannelHandler(&config), nil

	case "bark":
		var config notification.BarkChannelConfig
		if err := m.db.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Bark 配置不存在：%v", err)
		}
		return NewBarkChannelHandler(&config), nil

	case "serverchan":
		var config notification.ServerChanChannelConfig
		if err := m.db.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Server酱配置不存在：%v", err)
		}
		return NewServerChanChannelHandler(&config), nil

	case "webhook":
		var config notification.CustomWebhookChannelConfig
		if err := m.db.Where("channel_id = ?", channel.ID).First(&config).Error; err != nil {
			return nil, fmt.Errorf("Webhook 配置不存在：%v", err)
		}
		return NewCustomWebhookChannelHandler(&config), nil

	default:
		return nil, fmt.Errorf("未知的渠道类型：%s", channel.ChannelType)
	}
}

func (m *EnhancedNotificationManager) applyTelegramCommands(handler ChannelHandler) {
	if tg, ok := handler.(*TelegramChannelHandler); ok && m.telegramCommands != nil {
		tg.SetCommands(m.telegramCommands)
	}
}

// SendNotification 发送通知到所有相关渠道
func (m *EnhancedNotificationManager) SendNotification(ctx context.Context, notification *notification.Notification) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.handlers) == 0 {
		return nil
	}

	// 获取此事件类型启用的渠道
	channelIDs := m.rules[string(notification.Type)]
	if len(channelIDs) == 0 {
		helpers.AppLogger.Debugf("事件类型 %s 没有启用的通知规则，跳过发送", notification.Type)
		return nil
	}

	var errs []error
	for _, channelID := range channelIDs {
		info, ok := m.handlers[channelID]
		if !ok {
			continue
		}

		// 为每个通知发送创建子 context，超时 15 秒
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		if err := info.handler.Send(sendCtx, notification); err != nil {
			helpers.AppLogger.Errorf("渠道 [%s] 发送失败：%v", info.config.ChannelType, err)
			errs = append(errs, err)
		} else {
			helpers.AppLogger.Debugf("渠道 [%s] 发送成功", info.config.ChannelType)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分渠道发送失败：%v", errs)
	}
	return nil
}

// ReloadChannel 重新加载单个渠道
func (m *EnhancedNotificationManager) ReloadChannel(channelID uint) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if oldInfo, exists := m.handlers[channelID]; exists {
		if bg, ok := oldInfo.handler.(BackgroundHandler); ok {
			helpers.AppLogger.Infof("停止旧的后台渠道：%d", channelID)
			bg.Stop()
		}
	}

	var channel notification.NotificationChannel
	if err := m.db.Where("id = ?", channelID).First(&channel).Error; err != nil {
		delete(m.handlers, channelID)
		m.loadEnabledRulesLocked()
		return fmt.Errorf("渠道不存在：%v", err)
	}

	if !channel.IsEnabled {
		delete(m.handlers, channelID)
		m.loadEnabledRulesLocked()
		return nil
	}

	handler, err := m.createChannelHandler(&channel)
	if err != nil {
		delete(m.handlers, channelID)
		m.loadEnabledRulesLocked()
		return err
	}

	m.handlers[channelID] = &channelInfo{
		handler: handler,
		config:  &channel,
	}

	if bg, ok := handler.(BackgroundHandler); ok {
		bg.Start(context.Background())
	}

	m.loadEnabledRulesLocked()
	helpers.AppLogger.Infof("已重新加载渠道：%s", channel.ChannelName)
	return nil
}

// ReloadRules 重新加载通知规则，不重建渠道处理器
func (m *EnhancedNotificationManager) ReloadRules() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.loadEnabledRulesLocked()
}

// RemoveChannel 卸载单个通知渠道
func (m *EnhancedNotificationManager) RemoveChannel(channelID uint) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if oldInfo, exists := m.handlers[channelID]; exists {
		if bg, ok := oldInfo.handler.(BackgroundHandler); ok {
			helpers.AppLogger.Infof("停止后台渠道：%d", channelID)
			bg.Stop()
		}
		delete(m.handlers, channelID)
	}
	m.loadEnabledRulesLocked()
}

// GetChannels 获取所有渠道
func (m *EnhancedNotificationManager) GetChannels() []notification.NotificationChannel {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var channels []notification.NotificationChannel
	for _, info := range m.handlers {
		channels = append(channels, *info.config)
	}
	return channels
}

// RegisterTelegramCommands 将自定义命令逻辑注入到所有 Telegram 渠道中
func (m *EnhancedNotificationManager) RegisterTelegramCommands(cmds map[string]func([]string) helpers.CommandResponse) {
	m.mu.Lock() // 修改内部状态，加写锁
	defer m.mu.Unlock()

	m.telegramCommands = cmds
	for _, info := range m.handlers {
		// 类型断言：检查这个 handler 是不是 TelegramChannelHandler
		if tg, ok := info.handler.(*TelegramChannelHandler); ok {
			tg.SetCommands(cmds)
		}
	}
}
