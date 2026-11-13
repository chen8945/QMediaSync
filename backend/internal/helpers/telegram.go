package helpers

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TelegramBot 结构体用于处理 Telegram 机器人操作
type TelegramBot struct {
	Token      string
	ChatID     string
	Client     *tgbotapi.BotAPI
	ctx        context.Context
	httpClient *http.Client
}

// TelegramResponse Telegram API 响应结构
type TelegramResponse struct {
	OK          bool   `json:"ok"`
	Result      any    `json:"result"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

// TelegramMessage 发送消息的结构
type TelegramMessage struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// CommandResponse 命令响应结构
type CommandResponse struct {
	Text        string
	ReplyMarkup any
}

// maskToken 掩码 Token，用于日志输出
func maskToken(token string) string {
	if len(token) <= 8 {
		return "***"
	}
	return token[:4] + "***" + token[len(token)-4:]
}

// NewTelegramBot 创建新的 Telegram 机器人实例。
func NewTelegramBot(token, chatID string) *TelegramBot {
	bot, err := NewTelegramBotContext(context.Background(), token, chatID, "")
	if err != nil {
		AppLogger.Errorf("创建 Telegram 机器人失败：%s", RedactSensitiveLog(err.Error(), token))
		return nil
	}
	return bot
}

// NewTelegramBotWithProxy 创建带代理的 Telegram 机器人实例。
func NewTelegramBotWithProxy(token, chatID, proxyURL string) (*TelegramBot, error) {
	return NewTelegramBotContext(context.Background(), token, chatID, proxyURL)
}

type telegramHTTPClient struct {
	ctx context.Context
	*http.Client
}

func (c telegramHTTPClient) Do(req *http.Request) (*http.Response, error) {
	return c.Client.Do(req.WithContext(c.ctx))
}

// NewTelegramBotContext 将初始化、发送及轮询的实际 HTTP 请求绑定到拥有者。
func NewTelegramBotContext(ctx context.Context, token, chatID, proxyURL string) (*TelegramBot, error) {
	if token == "" || chatID == "" {
		return nil, fmt.Errorf("Telegram Token 和 Chat ID 不能为空")
	}
	transport, err := GetProxyTransport(proxyURL)
	if err != nil {
		return nil, err
	}
	if proxyURL == "" {
		transport.Proxy = http.ProxyFromEnvironment
	}
	// Telegram 轮询可等待 60 秒，由拥有者 context 和总超时控制退出。
	transport.ResponseHeaderTimeout = 0
	client := &http.Client{Timeout: 120 * time.Second, Transport: transport}
	api, err := tgbotapi.NewBotAPIWithClient(token, "https://api.telegram.org/bot%s/%s", telegramHTTPClient{ctx: ctx, Client: client})
	if err != nil {
		client.CloseIdleConnections()
		return nil, err
	}
	return &TelegramBot{Token: token, ChatID: chatID, Client: api, ctx: ctx, httpClient: client}, nil
}

// CloseIdleConnections 释放本实例的私有连接池。
func (bot *TelegramBot) CloseIdleConnections() {
	if bot.httpClient != nil {
		bot.httpClient.CloseIdleConnections()
	}
}

// SendMessage 发送消息到 Telegram
func (bot *TelegramBot) SendMessage(text string) error {
	if bot == nil {
		return fmt.Errorf("Telegram Bot 实例不能为空")
	}
	if bot.Client == nil {
		return fmt.Errorf("Telegram Bot Client 不能为空")
	}
	if bot.Token == "" {
		return fmt.Errorf("Telegram Bot Token 不能为空")
	}
	if bot.ChatID == "" {
		return fmt.Errorf("Telegram Chat ID 不能为空")
	}

	msg := tgbotapi.NewMessage(StringToInt64(bot.ChatID), text)
	msg.ParseMode = "HTML"
	_, err := bot.Client.Send(msg)
	if err != nil {
		return fmt.Errorf("发送消息失败：%v", err)
	}
	return nil

	// url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", bot.Token)

	// message := TelegramMessage{
	// 	ChatID:    bot.ChatID,
	// 	Text:      text,
	// 	ParseMode: "HTML",
	// }

	// jsonData, err := json.Marshal(message)
	// if err != nil {
	// 	return fmt.Errorf("序列化消息失败：%v", err)
	// }

	// req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	// if err != nil {
	// 	return fmt.Errorf("创建请求失败：%v", err)
	// }

	// req.Header.Set("Content-Type", "application/json")

	// resp, err := bot.Client.Do(req)
	// if err != nil {
	// 	return fmt.Errorf("发送请求失败：%v", err)
	// }
	// defer resp.Body.Close()

	// body, err := io.ReadAll(resp.Body)
	// if err != nil {
	// 	return fmt.Errorf("读取响应失败：%v", err)
	// }

	// var telegramResp TelegramResponse
	// if err := json.Unmarshal(body, &telegramResp); err != nil {
	// 	return fmt.Errorf("解析响应失败：%v", err)
	// }

	// if !telegramResp.OK {
	// 	return fmt.Errorf("Telegram API 错误 [%d]：%s", telegramResp.ErrorCode, telegramResp.Description)
	// }

	// return nil
}

// SendPhoto 发送图片到 Telegram，支持本地文件路径或网络 URL
func (bot *TelegramBot) SendPhoto(image string, caption string) error {
	if bot == nil {
		return fmt.Errorf("Telegram Bot 实例不能为空")
	}
	if bot.Client == nil {
		return fmt.Errorf("Telegram Bot Client 不能为空")
	}
	if bot.Token == "" {
		return fmt.Errorf("Telegram Bot Token 不能为空")
	}
	if bot.ChatID == "" {
		return fmt.Errorf("Telegram Chat ID 不能为空")
	}

	var file tgbotapi.RequestFileData
	// 判断是否为 URL
	if strings.HasPrefix(strings.ToLower(image), "http://") || strings.HasPrefix(strings.ToLower(image), "https://") {
		file = tgbotapi.FileURL(image)
	} else {
		file = tgbotapi.FilePath(image)
	}

	msg := tgbotapi.NewPhoto(StringToInt64(bot.ChatID), file)
	if caption != "" {
		// Telegram 照片 caption 上限约为 1024 个字符，这里做简单截断。
		if len([]rune(caption)) > 1024 {
			// 保留前 1024 个字符。
			runes := []rune(caption)
			caption = string(runes[:1024])
		}
		msg.Caption = caption
		msg.ParseMode = "HTML"
	}

	_, err := bot.Client.Send(msg)
	if err != nil {
		return fmt.Errorf("发送图片失败：%v", err)
	}
	return nil
}

// SendMessageWithRetry 带重试机制的发送消息
func (bot *TelegramBot) SendMessageWithRetry(text string, maxRetries int) error {
	var lastError error
	if bot == nil {
		return fmt.Errorf("Telegram Bot 实例不能为空")
	}
	ctx := bot.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 重试前等待，使用指数退避
			waitTime := time.Duration(attempt*attempt) * time.Second
			AppLogger.Infof("Telegram 消息发送失败，%d 秒后重试（第 %d 次尝试）", waitTime/time.Second, attempt)
			timer := time.NewTimer(waitTime)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}

		if err := ctx.Err(); err != nil {
			return err
		}
		err := bot.SendMessage(text)
		if err == nil {
			if attempt > 0 {
				AppLogger.Infof("Telegram 消息重试发送成功（第 %d 次尝试）", attempt)
			}
			return nil
		}

		lastError = err
		AppLogger.Warnf("Telegram 消息发送失败（第 %d 次尝试）：%s", attempt+1, RedactSensitiveLog(err.Error(), bot.Token))

		// 如果是超时错误，继续重试
		if isTimeoutError(err) {
			continue
		}

		// 如果是其他类型的错误，立即返回
		break
	}

	return fmt.Errorf("经过 %d 次重试后仍然失败：%v", maxRetries+1, lastError)
}

// isTimeoutError 检查是否是超时错误
func isTimeoutError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())
	timeoutKeywords := []string{
		"timeout",
		"tls handshake timeout",
		"context deadline exceeded",
		"connection timeout",
		"dial timeout",
	}

	for _, keyword := range timeoutKeywords {
		if strings.Contains(errStr, keyword) {
			return true
		}
	}

	return false
}

// TestConnection 测试 Telegram 机器人连接
func (bot *TelegramBot) TestConnection() error {
	if bot.Token == "" {
		return fmt.Errorf("Telegram Bot Token 不能为空")
	}

	// 如果提供了 Chat ID，测试发送消息
	if bot.ChatID == "" {
		return fmt.Errorf("Telegram Chat ID 不能为空")
	}
	return bot.SendMessage("🤖 Telegram 机器人连接测试成功！\n\n这是一条测试消息，说明你的机器人配置正确。")
}

// TestTelegramBot 测试 Telegram 机器人连接的便捷函数
func TestTelegramBot(token, chatID, httpProxy string) error {
	if httpProxy == "" {
		bot := NewTelegramBot(token, chatID)
		if bot == nil {
			return fmt.Errorf("创建 Telegram 机器人失败")
		}
		return bot.TestConnection()
	} else {
		bot, err := NewTelegramBotWithProxy(token, chatID, httpProxy)
		if err != nil {
			return fmt.Errorf("创建带代理的 Telegram 机器人失败：%v", err)
		}
		return bot.TestConnection()
	}
}

// StartListening 启动监听 Telegram 命令
func (bot *TelegramBot) StartListening(ctx context.Context, handleCommand map[string]func([]string) CommandResponse, offset *atomic.Int64) {
	if bot.Client == nil {
		AppLogger.Errorf("Bot Client 未初始化")
		return
	}

	// 或者更直接地调用 RemoveWebhook 方法（如果可用）。但标准方法是设置空的 Webhook。
	// 检查库的版本，通常通过 bot.RemoveWebhook() 或 bot.Request(DeleteWebhookConfig{}) 实现。
	// 对于 v5 版本，可以创建一个 DeleteWebhook 的请求配置。
	// 简单且通用的方式是设置一个空的 Webhook 来覆盖并清除旧的。
	// 注意：以下方式可能不是最优雅的，但能达到清除目的。
	// 更准确的做法是：
	_, err := bot.Client.Request(tgbotapi.DeleteWebhookConfig{}) // 如果库支持
	if err != nil {
		AppLogger.Errorf("清除 Webhook 时出错（可能没有设置过）：%s", RedactSensitiveLog(err.Error(), bot.Token))
	} else {
		AppLogger.Infof("已尝试清除 Webhook。")
	}

	// 配置轮询参数
	u := tgbotapi.NewUpdate(int(offset.Load()))
	u.Timeout = 60

	AppLogger.Infof("Telegram Bot 控制监听已启动")
	for ctx.Err() == nil {
		u.Offset = int(offset.Load())
		updates, err := bot.Client.GetUpdates(u)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			AppLogger.Warnf("Telegram 命令轮询失败：%s", RedactSensitiveLog(err.Error(), bot.Token))
			timer := time.NewTimer(3 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		for _, update := range updates {
			if ctx.Err() != nil {
				return
			}
			// 先领取更新，重载后重投的旧命令不能再次执行。
			current := offset.Load()
			for int64(update.UpdateID) >= current && !offset.CompareAndSwap(current, int64(update.UpdateID)+1) {
				current = offset.Load()
			}
			if int64(update.UpdateID) < current {
				continue
			}
			var cmd string
			var args []string
			var chatID int64

			if update.Message != nil && update.Message.IsCommand() {
				// 处理文字命令 /xxxx
				cmd = update.Message.Command()
				args = strings.Fields(update.Message.CommandArguments())
				chatID = update.Message.Chat.ID
			} else if update.CallbackQuery != nil {
				// 处理按钮点击
				bot.Client.Request(tgbotapi.NewCallback(update.CallbackQuery.ID, ""))
				data := update.CallbackQuery.Data
				// 解析命令和参数
				parts := strings.Fields(data)
				if len(parts) > 0 {
					cmd = parts[0]
					args = parts[1:]
				} else {
					cmd = data
					args = []string{}
				}
				chatID = update.CallbackQuery.Message.Chat.ID
			} else {
				continue
			}

			// --- 权限检查 ---
			// 重点：只响应配置中指定的 Chat ID，防止其他人控制程序
			if bot.ChatID != "" && fmt.Sprintf("%d", chatID) != bot.ChatID {
				continue
			}

			// --- 处理命令 ---
			var response CommandResponse
			if logic, ok := handleCommand[cmd]; ok {
				response = logic(args)
			} else {
				switch cmd {
				case "start", "help":
					response.Text = `👋 <b>欢迎使用 QMediaSync Bot</b>

📋 <b>命令列表：</b>
📊 /status - 查看系统运行状态
🚀 /strm_sync - 执行全量 STRM 同步
🔄 /strm_inc - 执行增量 STRM 同步
🎬 /scrape - 执行刮削任务
📋 /get_strm_path - 查看 STRM 同步路径
🧹 /get_scrape_path - 查看刮削路径

⚡ <b>同步模式说明：</b>
• <b>全量同步：</b>会删除所有缓存数据（不会删除本地文件），然后重新执行同步，适合处理大量网盘文件变更。
• <b>增量同步：</b>只处理新增文件，无法感知文件夹重命名等变更。

⚡ <b>同步/刮削命令：</b>
• 不加参数时，会执行所有同步/刮削路径。
• 可在命令后加序号指定目录，序号见同步/刮削目录设置。例如：/scrape #1
`
					// 构建内联键盘
					keyboard := tgbotapi.NewInlineKeyboardMarkup(
						tgbotapi.NewInlineKeyboardRow(
							tgbotapi.NewInlineKeyboardButtonData("🚀 全量同步", "strm_sync"),
							tgbotapi.NewInlineKeyboardButtonData("🔄 增量同步", "strm_inc"),
							tgbotapi.NewInlineKeyboardButtonData("🎬 刮削任务", "scrape"),
						),
						tgbotapi.NewInlineKeyboardRow(
							tgbotapi.NewInlineKeyboardButtonData("📋 同步路径", "get_strm_path"),
							tgbotapi.NewInlineKeyboardButtonData("🧹 刮削路径", "get_scrape_path"),
							tgbotapi.NewInlineKeyboardButtonData("📊📊 系统状态", "status"),
						),
					)
					response.ReplyMarkup = keyboard
				case "status":
					response.Text = "📊 <b>系统状态</b>\n运行状态：正常\n时间：" + time.Now().Format("2006-01-02 15:04:05")
				default:
					response.Text = "❓ 未知命令，输入 /help 查看帮助"

				}
			}

			// 回复结果
			if response.Text != "" {
				reply := tgbotapi.NewMessage(chatID, response.Text)
				reply.ParseMode = "HTML"

				// 如果有内联键盘，则添加到回复中
				if response.ReplyMarkup != nil {
					reply.ReplyMarkup = response.ReplyMarkup
				}

				bot.Client.Send(reply)
			}

		}

	}
}

func (bot *TelegramBot) SetMenuContent() {
	type menuItem struct {
		Command     string
		Description string
	}

	menu := []menuItem{

		{"strm_sync", "🚀 执行 STRM 全量同步"},
		{"strm_inc", "🔄 执行 STRM 增量同步"},
		{"scrape", "🎬 执行刮削任务"},
		{"get_strm_path", "📋 查看 STRM 同步路径"},
		{"get_scrape_path", "🧹 查看刮削路径"},
		// {"strm_scrape", "🔄🎬 先同步后刮削"},
		{"help", "📋 显示功能操作指南"},
		{"status", "📊 查看系统运行状态"},
	}

	var tgCommands []tgbotapi.BotCommand
	for _, item := range menu {
		tgCommands = append(tgCommands, tgbotapi.BotCommand{
			Command:     item.Command,
			Description: item.Description,
		})
	}
	scope := tgbotapi.NewBotCommandScopeAllPrivateChats()
	cfg := tgbotapi.NewSetMyCommandsWithScope(scope, tgCommands...)
	_, err := bot.Client.Request(cfg)
	if err != nil {
		AppLogger.Errorf("设置 Bot 菜单失败：%v", err)
	}
}
