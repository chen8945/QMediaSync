package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupNotificationApplicationTest(t *testing.T, getProxyURL func() string) (*gorm.DB, *notificationmanager.EnhancedNotificationManager) {
	t.Helper()
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "notifications.sqlite")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(2)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := conn.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&models.NotificationChannel{}, &models.NotificationRule{}, &models.CustomWebhookChannelConfig{}, &models.TelegramChannelConfig{}); err != nil {
		t.Fatal(err)
	}
	oldDB, oldManager, oldLogger := db.Db, notificationmanager.GlobalEnhancedNotificationManager, helpers.AppLogger
	db.Db = conn
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	manager := notificationmanager.NewEnhancedNotificationManager(conn, getProxyURL)
	notificationmanager.GlobalEnhancedNotificationManager = manager
	t.Cleanup(func() {
		db.Db = oldDB
		notificationmanager.GlobalEnhancedNotificationManager = oldManager
		helpers.AppLogger = oldLogger
	})
	return conn, manager
}

func TestNotificationSavedApplyWarningCanRetryWithoutRestart(t *testing.T) {
	conn, manager := setupNotificationApplicationTest(t, nil)
	received := make(chan struct{}, 1)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received <- struct{}{} }))
	defer remote.Close()
	failReload := true
	if err := conn.Callback().Query().Before("gorm:query").Register("test:reload_failure", func(tx *gorm.DB) {
		if failReload && tx.Statement.Table == "notification_channels" {
			tx.AddError(errors.New("reload unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/create", CreateCustomWebhookChannel)
	router.PUT("/update", UpdateCustomWebhookChannel)
	send := func(method, path string, payload any) map[string]json.RawMessage {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("response=%d %s", response.Code, response.Body.String())
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if string(result["code"]) != "0" {
			t.Fatalf("write=%s", response.Body.String())
		}
		return result
	}
	result := send(http.MethodPost, "/create", map[string]any{"channel_name": "webhook", "endpoint": remote.URL, "method": "POST", "format": "text", "template": "{{title}}"})
	var warnings []string
	if err := json.Unmarshal(result["warnings"], &warnings); err != nil {
		t.Fatal(err)
	}
	if len(warnings) == 0 {
		t.Fatal("saved-but-unapplied response hid its warning")
	}
	var saved notificationChannelResponse
	if err := json.Unmarshal(result["data"], &saved); err != nil {
		t.Fatal(err)
	}
	failReload = false
	var persisted models.NotificationChannel
	if err := conn.First(&persisted, saved.ID).Error; err != nil {
		t.Fatalf("warning discarded committed config: %v", err)
	}
	result = send(http.MethodPut, "/update", map[string]any{"channel_id": saved.ID, "channel_name": "retry"})
	if err := json.Unmarshal(result["warnings"], &warnings); err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("retry warnings=%v", warnings)
	}
	if err := manager.SendNotification(context.Background(), &notification.Notification{Type: notification.SystemAlert, Title: "new config"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("same-process retry did not apply to the next notification")
	}
}

func TestNotificationSupersededSaveWarnsWhenNewerReloadFails(t *testing.T) {
	prepared, release := make(chan struct{}), make(chan struct{})
	var blockPrepare, failReload atomic.Bool
	conn, manager := setupNotificationApplicationTest(t, func() string {
		if blockPrepare.Load() {
			close(prepared)
			<-release
		}
		return ""
	})
	channel := models.NotificationChannel{ChannelType: "telegram", ChannelName: "before", IsEnabled: true}
	if err := models.CreateNotificationChannel(context.Background(), &channel, func(tx *gorm.DB, id uint) error {
		return tx.Create(&models.TelegramChannelConfig{ChannelID: id, BotToken: "token", ChatID: "123"}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.LoadChannels(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Callback().Query().Before("gorm:query").Register("test:newer_reload_failure", func(tx *gorm.DB) {
		if failReload.Load() && tx.Statement.Table == "notification_channels" {
			tx.AddError(errors.New("newer reload unavailable"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.PUT("/update", UpdateTelegramChannel)
	body, err := json.Marshal(map[string]any{"channel_id": channel.ID, "channel_name": "saved"})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/update", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, req)
		return response
	}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	completed := make(chan struct{})
	responses := make(chan *httptest.ResponseRecorder, 1)
	t.Cleanup(func() {
		unblock()
		select {
		case <-completed:
		case <-time.After(time.Second):
			t.Error("pending save did not finish")
		}
	})
	blockPrepare.Store(true)
	go func() { defer close(completed); responses <- request() }()
	select {
	case <-prepared:
	case <-time.After(time.Second):
		t.Fatal("saved settings did not begin application")
	}
	failReload.Store(true)
	if err := manager.ReloadChannel(channel.ID); err == nil {
		t.Fatal("newer failed preparation was hidden")
	}
	unblock()
	var response *httptest.ResponseRecorder
	select {
	case response = <-responses:
	case <-time.After(time.Second):
		t.Fatal("superseded save did not finish")
	}
	var result struct {
		Code     int                         `json:"code"`
		Data     notificationChannelResponse `json:"data"`
		Warnings []string                    `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Code != 0 || result.Data.ChannelName != "saved" || len(result.Warnings) == 0 {
		t.Fatalf("superseded save response=%d %s", response.Code, response.Body.String())
	}
	if current := manager.GetChannels(); len(current) != 1 || current[0].ChannelName != "before" {
		t.Fatalf("failed applications changed effective channel: %v", current)
	}
	blockPrepare.Store(false)
	failReload.Store(false)
	response = request()
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != 0 || len(result.Warnings) != 0 {
		t.Fatalf("retry response=%s", response.Body.String())
	}
	if current := manager.GetChannels(); len(current) != 1 || current[0].ChannelName != "saved" {
		t.Fatalf("same-process retry did not apply saved channel: %v", current)
	}
}
