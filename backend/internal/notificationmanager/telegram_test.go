package notificationmanager

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/notification"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type telegramTestHTTPClient struct {
	ctx context.Context
	*http.Client
}

func (c telegramTestHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return c.Client.Do(request.WithContext(c.ctx))
}

func TestNotificationTelegramReloadPreservesHandledUpdates(t *testing.T) {
	for _, change := range []string{"proxy", "channel", "disabled", "bot"} {
		t.Run(change, func(t *testing.T) {
			manager, db, _ := setupNotificationManagerTest(t)
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			if err := db.AutoMigrate(&notification.TelegramChannelConfig{}); err != nil {
				t.Fatal(err)
			}
			channel := notification.NotificationChannel{ChannelType: "telegram", IsEnabled: true}
			if err := db.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			config := notification.TelegramChannelConfig{ChannelID: channel.ID, BotToken: "old-token", ChatID: "123"}
			if err := db.Create(&config).Error; err != nil {
				t.Fatal(err)
			}
			var proxy atomic.Value
			proxy.Store("http://first.invalid:8080")
			manager.getProxyURL = func() string { return proxy.Load().(string) }
			var commands, replies atomic.Int32
			replyStarted, replyCanceled := make(chan struct{}), make(chan struct{})
			type pollEvent struct{ token, proxy string }
			confirmed, stopped := make(chan string, 4), make(chan pollEvent, 4)
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					return
				}
				w.Header().Set("Content-Type", "application/json")
				method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				switch method {
				case "getMe":
					_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test"}}`)
				case "getUpdates":
					offset, _ := strconv.Atoi(r.Form.Get("offset"))
					if offset <= 41 {
						update := tgbotapi.Update{UpdateID: 41, Message: &tgbotapi.Message{
							MessageID: 1, Chat: &tgbotapi.Chat{ID: 123}, Text: "/scrape",
							Entities: []tgbotapi.MessageEntity{{Type: "bot_command", Offset: 0, Length: 7}},
						}}
						_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []tgbotapi.Update{update}})
						return
					}
					confirmed <- strings.TrimPrefix(strings.Split(r.URL.Path, "/")[1], "bot")
					<-r.Context().Done()
				case "sendMessage":
					if replies.Add(1) == 1 {
						close(replyStarted)
						<-r.Context().Done()
						close(replyCanceled)
						return
					}
					_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":2,"chat":{"id":123},"text":"accepted"}}`)
				default:
					_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
				}
			}))
			oldConstructor := newTelegramChannelHandler
			newTelegramChannelHandler = func(config *notification.TelegramChannelConfig, proxyURL string) *TelegramChannelHandler {
				handler := NewTelegramChannelHandlerWithProxy(config, proxyURL)
				handler.initBotFunc = func() error { return nil }
				handler.startListeningFunc = func(ctx context.Context, commands map[string]func([]string) helpers.CommandResponse) {
					defer func() { stopped <- pollEvent{token: config.BotToken, proxy: proxyURL} }()
					api, err := tgbotapi.NewBotAPIWithClient(config.BotToken, remote.URL+"/bot%s/%s", telegramTestHTTPClient{ctx: ctx, Client: remote.Client()})
					if err != nil {
						t.Error(err)
						return
					}
					bot := &helpers.TelegramBot{Token: config.BotToken, ChatID: config.ChatID, Client: api}
					bot.StartListening(ctx, commands, handler.updateOffset)
				}
				return handler
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				if err := manager.StopAllContext(ctx); err != nil {
					t.Error(err)
				}
				newTelegramChannelHandler = oldConstructor
				remote.Close()
			})
			manager.RegisterTelegramCommands(map[string]func([]string) helpers.CommandResponse{"scrape": func([]string) helpers.CommandResponse {
				commands.Add(1)
				return helpers.CommandResponse{Text: "accepted"}
			}})
			if err := manager.LoadChannels(); err != nil {
				t.Fatal(err)
			}
			manager.StartAll()
			select {
			case <-replyStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("command reply did not reach the server")
			}
			if commands.Load() != 1 {
				t.Fatal("initial command was not executed once")
			}
			expected := int32(1)
			if change == "proxy" {
				proxy.Store("http://second.invalid:8080")
				if err := manager.LoadChannels(); err != nil {
					t.Fatal(err)
				}
			} else if change == "disabled" {
				for _, enabled := range []bool{false, true} {
					if err := db.Model(&channel).Update("is_enabled", enabled).Error; err != nil {
						t.Fatal(err)
					}
					if err := manager.ReloadChannel(channel.ID); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				if change == "bot" {
					expected = 2
					if err := db.Model(&config).Update("bot_token", "new-token").Error; err != nil {
						t.Fatal(err)
					}
				} else if err := db.Model(&channel).Update("channel_name", "renamed").Error; err != nil {
					t.Fatal(err)
				}
				if err := manager.ReloadChannel(channel.ID); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case token := <-confirmed:
				wanted := "old-token"
				if change == "bot" {
					wanted = "new-token"
				}
				if token != wanted {
					t.Fatalf("replacement bot=%s, want %s", token, wanted)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("replacement listener did not acknowledge updates")
			}
			select {
			case <-replyCanceled:
			case <-time.After(2 * time.Second):
				t.Fatal("retired listener left its reply request running")
			}
			select {
			case event := <-stopped:
				if event.token != "old-token" || event.proxy != "http://first.invalid:8080" {
					t.Fatalf("retired listener=%+v", event)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("retired listener did not finish")
			}
			if got := commands.Load(); got != expected {
				t.Fatalf("command executions=%d, want %d after %s reload", got, expected, change)
			}
		})
	}
}
