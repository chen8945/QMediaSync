package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/requests"
)

const notificationTestItem = "0886c67586e970b3684cd408cf1bdeca"
const notificationTestUser = "33333333333333333333333333333333"

func setupMediaNotificationDB(t *testing.T) *gorm.DB {
	t.Helper()
	conn := setupControllerTestDB(t, &models.EmbyConfig{}, &models.JellyfinConfig{}, &models.EmbyIndexState{}, &models.EmbyItemState{},
		&models.EmbyItemEvidence{}, &models.EmbyItemMembership{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyWebhookRecord{},
		&models.EmbyWebhookTarget{}, &models.DbDownloadTask{}, &notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.CustomWebhookChannelConfig{})
	previousLogger := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previousLogger })
	return conn
}

func saveNotificationConfig(t *testing.T, conn *gorm.DB, kind models.MediaServerType, url string, notify, extract int) *models.EmbyConfig {
	t.Helper()
	config := &models.EmbyConfig{Enabled: true, MediaServerType: kind, EmbyUrl: url, EmbyApiKey: "test", EnableMediaNotification: notify, EnableExtractMediaInfo: extract, EnableDeleteNetdisk: 1, SyncEnabled: 1}
	if kind == models.MediaServerJellyfin {
		stored := models.JellyfinConfig(*config)
		if err := conn.Create(&stored).Error; err != nil {
			t.Fatal(err)
		}
		config.ID = stored.ID
	} else if err := conn.Create(config).Error; err != nil {
		t.Fatal(err)
	}
	if err := models.UpdateMediaLibraryConfig(kind, map[string]any{"enable_auth": 0}); err != nil {
		t.Fatal(err)
	}
	return config
}

func notificationTestPayload(kind models.MediaServerType, event string) string {
	if kind == models.MediaServerJellyfin {
		return fmt.Sprintf(`{"NotificationType":%q,"ServerId":"server","ItemId":%q,"ItemType":"Movie","Name":"payload title","UtcTimestamp":"2026-11-11T07:22:07.0848637Z","UserId":%q,"NotificationUsername":"reader","DeviceName":"TV","Client":"Jellyfin","Id":"session","PlaybackPositionTicks":600000000,"RunTimeTicks":1200000000}`, event, notificationTestItem, notificationTestUser)
	}
	return fmt.Sprintf(`{"Event":%q,"Server":{"Id":"server"},"Item":{"Id":%q,"Type":"Movie","Name":"payload title","Path":"/media/movie.strm"},"User":{"Id":%q,"Name":"reader"},"Session":{"DeviceName":"TV","Client":"Emby"},"PlaybackInfo":{"PositionTicks":600000000,"MediaSource":{"RunTimeTicks":1200000000}}}`, event, "20", notificationTestUser)
}

func TestMediaLibraryWebhookSendsSharedRules(t *testing.T) {
	conn := setupMediaNotificationDB(t)
	captured := make(chan map[string]string, 16)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message map[string]string
		if err := json.NewDecoder(r.Body).Decode(&message); err != nil {
			t.Error(err)
		}
		captured <- message
		w.WriteHeader(http.StatusNoContent)
	}))
	defer sink.Close()
	channel := notification.NotificationChannel{ChannelType: "webhook", ChannelName: "capture", IsEnabled: true}
	if err := conn.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&notification.CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: sink.URL, Method: "POST", Format: "json", Template: `{"title":"{{title}}","content":"{{content}}"}`}).Error; err != nil {
		t.Fatal(err)
	}
	for _, event := range []notification.NotificationType{notification.MediaAdded, notification.MediaRemoved, notification.PlaybackStart, notification.PlaybackStop} {
		if err := conn.Create(&notification.NotificationRule{ChannelID: channel.ID, EventType: string(event), IsEnabled: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	manager := notificationmanager.NewEnhancedNotificationManager(conn, nil)
	if err := manager.LoadChannels(); err != nil {
		t.Fatal(err)
	}
	previous := notificationmanager.GlobalEnhancedNotificationManager
	notificationmanager.GlobalEnhancedNotificationManager = manager
	t.Cleanup(func() { notificationmanager.GlobalEnhancedNotificationManager = previous })
	if err := StartMediaLibraryNotifications(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := StopMediaLibraryNotifications(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	router := gin.New()
	router.POST("/emby/webhook", Webhook)
	router.POST("/jellyfin/webhook", JellyfinWebhook)
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		product, prefix, itemID := "Emby", "/emby", "20"
		if kind == models.MediaServerJellyfin {
			product, prefix, itemID = "Jellyfin", "", notificationTestItem
		}
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/System/Info/Public":
				fmt.Fprintf(w, `{"Id":"server","ProductName":%q}`, product+" Server")
			case prefix + "/Users":
				fmt.Fprintf(w, `[{"Id":%q,"Name":"reader","Policy":{"EnableAllFolders":true,"EnableMediaPlayback":false}}]`, notificationTestUser)
			case prefix + "/Users/" + notificationTestUser + "/Items/" + itemID, prefix + "/Items/" + itemID:
				fmt.Fprintf(w, `{"Id":%q,"Type":"Movie","Name":"详情原文","Overview":"中文简介","DateCreated":"2026-11-11T07:00:00Z"}`, itemID)
			default:
				t.Errorf("unexpected notification request: %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		config := saveNotificationConfig(t, conn, kind, remote.URL, 1, 0)
		for _, event := range []struct{ emby, jellyfin, title string }{
			{"library.new", "ItemAdded", "入库"}, {"library.deleted", "ItemDeleted", "删除"}, {"playback.start", "PlaybackStart", "播放开始"}, {"playback.stop", "PlaybackStop", "播放停止"},
		} {
			t.Run(string(kind)+"/"+event.title, func(t *testing.T) {
				name := event.emby
				if kind == models.MediaServerJellyfin {
					name = event.jellyfin
				}
				response := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/"+string(kind)+"/webhook", strings.NewReader(notificationTestPayload(kind, name)))
				req.Header.Set("Content-Type", "text/plain; charset=utf-8")
				router.ServeHTTP(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("Webhook: %d %s", response.Code, response.Body.String())
				}
				select {
				case message := <-captured:
					if !strings.Contains(message["title"], product) || !strings.Contains(message["title"], event.title) || !strings.Contains(message["content"], "媒体服务器："+product) {
						t.Fatalf("渠道实际消息未标明本端或事件：%v", message)
					}
					if event.emby == "library.new" && !strings.Contains(message["content"], "详情原文") {
						t.Fatalf("入库通知没有使用原生详情：%v", message)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("启用的共同规则未收到实际通知")
				}
			})
		}
		if err := models.UpdateMediaLibraryConfig(kind, map[string]any{"enable_media_notification": 0}); err != nil {
			t.Fatal(err)
		}
		n := newMediaNotifier(config)
		n.sendDeletedMovie(t.Context(), "stopped")
		select {
		case message := <-captured:
			t.Fatalf("关闭本端通知后仍发送：%v", message)
		default:
		}
		remote.Close()
	}
	if err := conn.Model(&notification.NotificationRule{}).Where("event_type = ?", notification.MediaRemoved).Update("is_enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	manager.ReloadRules()
	if err := models.UpdateMediaLibraryConfig(models.MediaServerEmby, map[string]any{"enable_media_notification": 1}); err != nil {
		t.Fatal(err)
	}
	config, err := models.ReadMediaLibraryConfigSnapshot(models.MediaServerEmby)
	if err != nil {
		t.Fatal(err)
	}
	newMediaNotifier(config).sendDeletedMovie(t.Context(), "disabled rule")
	select {
	case message := <-captured:
		t.Fatalf("关闭的规则仍发送：%v", message)
	default:
	}
}

func TestMediaLibraryNotificationOffKeepsBusiness(t *testing.T) {
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		t.Run(string(kind), func(t *testing.T) {
			conn := setupMediaNotificationDB(t)
			var remoteCalls atomic.Int32
			product := "Emby Server"
			if kind == models.MediaServerJellyfin {
				product = "Jellyfin Server"
			}
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				remoteCalls.Add(1)
				fmt.Fprintf(w, `{"Id":"server","ProductName":%q}`, product)
			}))
			defer remote.Close()
			config := saveNotificationConfig(t, conn, kind, remote.URL, 0, 1)
			name := "library.new"
			if kind == models.MediaServerJellyfin {
				name = "ItemAdded"
			}
			body := notificationTestPayload(kind, name)
			var event EmbyEvent
			var envelope models.EmbyWebhookEnvelope
			if kind == models.MediaServerJellyfin {
				r, err := requests.ParseJellyfinWebhook([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				event, envelope = r.NotificationEvent(), r.ToEnvelope()
			} else {
				r, err := requests.ParseEmbyWebhook([]byte(body))
				if err != nil {
					t.Fatal(err)
				}
				event, envelope = r, r.ToEnvelope()
			}
			processMediaWebhook(t.Context(), config, event, envelope, models.EmbyPlaybackWebhook{}, time.Now())
			var task models.DbDownloadTask
			if err := conn.First(&task).Error; err != nil || task.Source != models.DownloadSource(kind+"_media") || task.MediaServerID != "server" {
				t.Fatalf("关闭通知误关自动提取：%+v %v", task, err)
			}
			router := gin.New()
			handler := Webhook
			if kind == models.MediaServerJellyfin {
				handler = JellyfinWebhook
			}
			router.POST("/webhook", handler)
			for _, deletion := range []bool{false, true} {
				eventName := name
				if deletion {
					eventName = "library.deleted"
					if kind == models.MediaServerJellyfin {
						eventName = "ItemDeleted"
					}
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(notificationTestPayload(kind, eventName))))
				if response.Code != http.StatusOK {
					t.Fatalf("关闭通知拒绝业务收件：%d %s", response.Code, response.Body.String())
				}
			}
			var records []models.EmbyWebhookRecord
			if err := conn.Find(&records).Error; err != nil || len(records) != 2 || !records[1].Authorized {
				t.Fatalf("同步/删除授权没有保留：%+v %v", records, err)
			}
			var frozen models.EmbyDeletionInput
			if err := json.Unmarshal([]byte(records[1].InputJSON), &frozen); err != nil || len(frozen.Owners) != 0 || len(frozen.Sidecars) != 0 {
				t.Fatalf("无历史GUID回调制造授权来源：%+v %v", frozen, err)
			}
			// 控制器不启动持久删除执行者；无来源的收件只保留待核验数据。
			if err := models.UpdateMediaLibraryConfig(kind, map[string]any{"enable_extract_media_info": 0}); err != nil {
				t.Fatal(err)
			}
			event.Item.ID = "11111111111111111111111111111111"
			processMediaWebhook(t.Context(), config, event, envelope, models.EmbyPlaybackWebhook{}, time.Now())
			var count int64
			if err := conn.Model(&models.DbDownloadTask{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("自动分项关闭仍新增提取：%d %v", count, err)
			}
		})
	}
}

func TestMediaLibraryNotificationsStopWaitsAndPreventsOverlap(t *testing.T) {
	if err := StartMediaLibraryNotifications(); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	queueMediaWebhook(func(ctx context.Context) { close(started); <-ctx.Done(); <-release })
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := StopMediaLibraryNotifications(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("停止没有等待在途任务：%v", err)
	}
	if err := StartMediaLibraryNotifications(); err == nil {
		t.Fatal("旧任务未退出却重新启动")
	}
	var late atomic.Int32
	queueMediaWebhook(func(context.Context) { late.Add(1) })
	close(release)
	if err := StopMediaLibraryNotifications(t.Context()); err != nil {
		t.Fatal(err)
	}
	if late.Load() != 0 {
		t.Fatal("停机期间继续接收通知任务")
	}
	if err := StartMediaLibraryNotifications(); err != nil {
		t.Fatal(err)
	}
	resumed := make(chan struct{})
	queueMediaWebhook(func(context.Context) { close(resumed) })
	<-resumed
	if err := StopMediaLibraryNotifications(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMediaLibraryNotificationHistoricalDisplay(t *testing.T) {
	event := EmbyEvent{}
	event.Event = "library.deleted"
	event.Server.ID = "server"
	event.Item.ID = notificationTestItem
	event.Item.Name = "plugin fallback"
	input := models.EmbyDeletionInput{ServerID: "server", Owners: []models.EmbyDeletionOwner{{Item: models.EmbyMediaItem{ServerId: "server", ItemId: notificationTestItem, Name: "原文 \\\"引号\"", SeriesName: "原剧名", SeriesId: "series", ParentIndexNumber: 1, IndexNumber: 2}}}}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	record := models.EmbyWebhookRecord{MediaServerType: models.MediaServerJellyfin, ServerID: "server", InputJSON: string(body)}
	got := notificationEventWithHistory(event, record)
	if got.Item.Name != input.Owners[0].Item.Name || got.Item.SeriesName != "原剧名" || got.Item.IndexNumber != 2 {
		t.Fatalf("未优先历史原文：%+v", got)
	}
	record.ServerID = "other"
	if got = notificationEventWithHistory(event, record); got.Item.Name != event.Item.Name {
		t.Fatal("采用了另一实例历史文字")
	}
}

func TestMediaLibrarySeriesBuffersIsolateConnections(t *testing.T) {
	conn := setupMediaNotificationDB(t)
	embyConfig := saveNotificationConfig(t, conn, models.MediaServerEmby, "http://same.invalid", 1, 0)
	jellyfinConfig := saveNotificationConfig(t, conn, models.MediaServerJellyfin, "http://same.invalid", 1, 0)
	if err := StartMediaLibraryNotifications(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := StopMediaLibraryNotifications(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	n := newMediaNotifier(embyConfig)
	n.addEpisode("series", 1, 1, "episode", "Emby", time.Now(), false)
	newMediaNotifier(jellyfinConfig).addEpisode("series", 2, 2, "episode", "Jellyfin", time.Now(), false)
	if err := models.UpdateMediaLibraryConfig(models.MediaServerEmby, map[string]any{"emby_api_key": "rotated"}); err != nil {
		t.Fatal(err)
	}
	rotated, err := models.ReadMediaLibraryConfigSnapshot(models.MediaServerEmby)
	if err != nil {
		t.Fatal(err)
	}
	newMediaNotifier(rotated).addEpisode("series", 3, 3, "episode", "Emby rotated", time.Now(), false)
	n.addEpisode("series", 9, 9, "stale", "old connection", time.Now(), false)
	future := time.Now().Add(time.Minute)
	added, _ := takeSeriesNotifications(future, models.MediaServerJellyfin)
	if len(added) != 1 || len(added[0].Seasons[2]) != 1 || len(added[0].Seasons[1]) != 0 {
		t.Fatalf("两端同ID缓冲串用：%+v", added)
	}
	added, _ = takeSeriesNotifications(future, models.MediaServerEmby)
	if len(added) != 2 {
		t.Fatalf("换连接的缓冲被旧连接吸收：%+v", added)
	}
	for _, series := range added {
		if len(series.Seasons[9]) != 0 {
			t.Fatal("旧连接继续加入缓冲")
		}
	}
}
