package notificationmanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"

	"gorm.io/gorm"
)

// EnhancedNotificationManager 拥有稳定的通知登记与后台生命周期。
type EnhancedNotificationManager struct {
	handlers         map[uint]*channelInfo
	rules            map[string][]uint
	telegramCommands map[string]func([]string) helpers.CommandResponse
	telegramUpdates  map[uint]telegramUpdateCursor
	commandRevision  uint64
	backgrounds      map[*TelegramChannelHandler]struct{}
	mu               sync.RWMutex
	db               *gorm.DB
	getProxyURL      func() string
	revisions        map[uint]uint64
	rulesRevision    uint64
	started          bool
	stopped          bool
}

type channelInfo struct {
	handler ChannelHandler
	config  *notification.NotificationChannel
	ctx     context.Context
	cancel  context.CancelFunc
}

var GlobalEnhancedNotificationManager *EnhancedNotificationManager

// NewEnhancedNotificationManager 创建模块内稳定的通知管理器。
func NewEnhancedNotificationManager(db *gorm.DB, getProxyURL func() string) *EnhancedNotificationManager {
	return &EnhancedNotificationManager{
		handlers: make(map[uint]*channelInfo), rules: make(map[string][]uint),
		telegramUpdates: make(map[uint]telegramUpdateCursor),
		revisions:       make(map[uint]uint64), backgrounds: make(map[*TelegramChannelHandler]struct{}), db: db, getProxyURL: getProxyURL,
	}
}

// SendNotification 捕获本次目标；发送不占登记锁，已开始的发送保留原配置。
func (m *EnhancedNotificationManager) SendNotification(ctx context.Context, n *notification.Notification) error {
	m.mu.RLock()
	targets := make([]*channelInfo, 0, len(m.rules[string(n.Type)]))
	for _, id := range m.rules[string(n.Type)] {
		if info := m.handlers[id]; info != nil {
			targets = append(targets, info)
		}
	}
	m.mu.RUnlock()
	var errs []error
	for _, info := range targets {
		sendCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := info.handler.Send(sendCtx, n)
		cancel()
		if err != nil {
			helpers.AppLogger.Errorf("渠道 [%s] 发送失败：%v", info.config.ChannelType, err)
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("部分渠道发送失败：%v", errs)
	}
	return nil
}

// GetChannels 返回已应用渠道的副本。
func (m *EnhancedNotificationManager) GetChannels() []notification.NotificationChannel {
	m.mu.RLock()
	defer m.mu.RUnlock()
	channels := make([]notification.NotificationChannel, 0, len(m.handlers))
	for _, info := range m.handlers {
		channels = append(channels, *info.config)
	}
	return channels
}

// RegisterTelegramCommands 发布命令副本，不在登记锁内调用渠道。
func (m *EnhancedNotificationManager) RegisterTelegramCommands(cmds map[string]func([]string) helpers.CommandResponse) {
	commands := copyCommands(cmds)
	m.mu.Lock()
	m.telegramCommands = commands
	m.commandRevision++
	revision := m.commandRevision
	handlers := make([]*TelegramChannelHandler, 0)
	for _, info := range m.handlers {
		if tg, ok := info.handler.(*TelegramChannelHandler); ok {
			handlers = append(handlers, tg)
		}
	}
	m.mu.Unlock()
	for _, handler := range handlers {
		handler.setCommandSnapshot(commands, revision)
	}
}

func copyCommands(cmds map[string]func([]string) helpers.CommandResponse) map[string]func([]string) helpers.CommandResponse {
	result := make(map[string]func([]string) helpers.CommandResponse, len(cmds))
	for name, command := range cmds {
		result[name] = command
	}
	return result
}
