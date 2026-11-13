package notificationmanager

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"
	"qmediasync/internal/validation"
)

// TelegramChannelHandler Telegram 渠道处理器
type TelegramChannelHandler struct {
	config             *notification.TelegramChannelConfig
	proxyURL           string // 系统代理 URL
	bot                *helpers.TelegramBot
	updateOffset       *atomic.Int64
	listenMu           sync.Mutex
	listenCancel       context.CancelFunc
	listenToken        *struct{}
	stopped            bool
	done               chan struct{}
	commandRevision    uint64
	initBotFunc        func() error
	startListeningFunc func(context.Context, map[string]func([]string) helpers.CommandResponse)
	customCommands     map[string]func([]string) helpers.CommandResponse // 保存从外部注入的命令
}

type telegramUpdateCursor struct {
	token  string
	offset *atomic.Int64
}

func NewTelegramChannelHandler(config *notification.TelegramChannelConfig) *TelegramChannelHandler {
	return &TelegramChannelHandler{
		config:       config,
		proxyURL:     "",
		updateOffset: new(atomic.Int64),
	}
}

func NewTelegramChannelHandlerWithProxy(config *notification.TelegramChannelConfig, proxyURL string) *TelegramChannelHandler {
	return &TelegramChannelHandler{
		config:       config,
		proxyURL:     proxyURL,
		updateOffset: new(atomic.Int64),
	}
}

func (h *TelegramChannelHandler) GetChannelType() string {
	return "telegram"
}

func (h *TelegramChannelHandler) IsHealthy() bool {
	if h.config.BotToken == "" || h.config.ChatID == "" {
		return false
	}
	return true
}

func (h *TelegramChannelHandler) Send(ctx context.Context, notification *notification.Notification) error {
	message := h.formatMessage(notification)
	// 验证配置
	if h.config == nil {
		return fmt.Errorf("Telegram 渠道配置为空")
	}
	if h.config.BotToken == "" {
		return fmt.Errorf("Telegram Bot Token 为空")
	}
	if h.config.ChatID == "" {
		return fmt.Errorf("Telegram Chat ID 为空")
	}

	if h.proxyURL != "" {
		helpers.AppLogger.Debugf("使用系统代理发送 Telegram 消息：%s", validation.RedactProxyURL(h.proxyURL))
	}
	bot, err := helpers.NewTelegramBotContext(ctx, h.config.BotToken, h.config.ChatID, h.proxyURL)
	if err != nil {
		return fmt.Errorf("创建 Telegram 机器人失败：%w", err)
	}
	defer bot.CloseIdleConnections()
	// 如果有图片，先尝试发送图片（用标题作为 caption）。
	if notification.Image != "" {
		if perr := bot.SendPhoto(notification.Image, message); perr != nil {
			helpers.AppLogger.Errorf("发送 Telegram 图片失败：%s", helpers.RedactSensitiveLog(perr.Error(), h.config.BotToken))
			// 不中断，继续发送文本
			return perr
		}
		return nil
	}

	// 再发送文本消息
	retries := 2
	if h.proxyURL != "" {
		retries = 3
	}
	return bot.SendMessageWithRetry(message, retries)
}

func (h *TelegramChannelHandler) formatMessage(notification *notification.Notification) string {
	// timestamp := notification.Timestamp.Format("2006-01-02 15:04:05")

	var message strings.Builder
	message.WriteString(fmt.Sprintf("<b>%s</b>\n", notification.Title))
	message.WriteString(fmt.Sprintf("%s\n", notification.Content))

	if len(notification.Metadata) > 0 {
		for key, value := range notification.Metadata {
			message.WriteString(fmt.Sprintf("<b>%s:</b> %v\n", key, value))
		}
		message.WriteString("\n")
	}

	// message += fmt.Sprintf("⏰ <b>时间：</b> %s", timestamp)
	return message.String()
}

func (h *TelegramChannelHandler) initBot(ctx context.Context) error {
	bot, err := helpers.NewTelegramBotContext(ctx, h.config.BotToken, h.config.ChatID, h.proxyURL)
	if err != nil {
		return err
	}
	h.bot = bot
	h.bot.SetMenuContent()
	return nil
}

func (h *TelegramChannelHandler) SetCommands(cmds map[string]func([]string) helpers.CommandResponse) {
	commands := copyCommands(cmds)
	h.listenMu.Lock()
	h.commandRevision++
	h.customCommands = commands
	h.listenMu.Unlock()
}

func (h *TelegramChannelHandler) setCommandSnapshot(commands map[string]func([]string) helpers.CommandResponse, revision uint64) {
	h.listenMu.Lock()
	defer h.listenMu.Unlock()
	if revision < h.commandRevision {
		return
	}
	h.commandRevision = revision
	h.customCommands = commands
}

// Start 在后台初始化；已退役或取消的候选不能再次启动。
func (h *TelegramChannelHandler) Start(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	h.listenMu.Lock()
	if h.stopped || h.listenCancel != nil {
		h.listenMu.Unlock()
		return
	}
	listenCtx, cancel := context.WithCancel(ctx)
	token := &struct{}{}
	h.listenCancel = cancel
	h.listenToken = token
	done := make(chan struct{})
	h.done = done
	commands := h.customCommands
	h.listenMu.Unlock()
	go func() {
		defer close(done)
		defer cancel()
		defer h.clearListenState(token)
		if listenCtx.Err() != nil {
			return
		}
		var err error
		if h.initBotFunc != nil {
			err = h.initBotFunc()
		} else {
			err = h.initBot(listenCtx)
		}
		if err != nil {
			helpers.AppLogger.Errorf("初始化 Telegram Bot 失败：%s", helpers.RedactSensitiveLog(err.Error(), h.configToken()))
			return
		}
		if h.bot != nil {
			defer h.bot.CloseIdleConnections()
		}
		if listenCtx.Err() != nil {
			return
		}
		h.startListening(listenCtx, commands)
	}()
}

func (h *TelegramChannelHandler) startListening(ctx context.Context, cmds map[string]func([]string) helpers.CommandResponse) {
	if h.startListeningFunc != nil {
		h.startListeningFunc(ctx, cmds)
		return
	}
	if h.bot == nil {
		helpers.AppLogger.Errorf("Telegram Bot 未初始化")
		return
	}
	h.bot.StartListening(ctx, cmds, h.updateOffset)
}

func (h *TelegramChannelHandler) clearListenState(listenToken *struct{}) {
	h.listenMu.Lock()
	defer h.listenMu.Unlock()

	if h.listenToken == listenToken {
		h.listenCancel = nil
		h.listenToken = nil
	}
}

func (h *TelegramChannelHandler) Stop() {
	h.listenMu.Lock()
	h.stopped = true
	cancel := h.listenCancel
	h.listenCancel = nil
	h.listenToken = nil
	h.listenMu.Unlock()

	if cancel != nil {
		cancel()
	}
}

func (h *TelegramChannelHandler) configToken() string {
	if h.config == nil {
		return ""
	}
	return h.config.BotToken
}

func (h *TelegramChannelHandler) wait(ctx context.Context) error {
	h.listenMu.Lock()
	done := h.done
	h.listenMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *TelegramChannelHandler) finished() bool {
	h.listenMu.Lock()
	defer h.listenMu.Unlock()
	if !h.stopped {
		return false
	}
	if h.done == nil {
		return true
	}
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}
