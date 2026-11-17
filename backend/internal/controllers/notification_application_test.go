package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
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
		expectedStatus := http.StatusOK
		if method == http.MethodPost {
			expectedStatus = http.StatusCreated
		}
		if response.Code != expectedStatus {
			t.Fatalf("response=%d %s", response.Code, response.Body.String())
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if string(result["status"]) != fmt.Sprint(expectedStatus) {
			t.Fatalf("write=%s", response.Body.String())
		}
		return result
	}
	result := send(http.MethodPost, "/create", map[string]any{"channel_name": "webhook", "endpoint": remote.URL, "method": "POST", "format": "text", "template": "{{title}}"})
	var warnings []string
	if raw := result["warnings"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &warnings); err != nil {
			t.Fatal(err)
		}
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
	warnings = nil
	if raw := result["warnings"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &warnings); err != nil {
			t.Fatal(err)
		}
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
		Status   int                         `json:"status"`
		Data     notificationChannelResponse `json:"data"`
		Warnings []string                    `json:"warnings"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || result.Status != http.StatusOK || result.Data.ChannelName != "saved" || len(result.Warnings) == 0 {
		t.Fatalf("superseded save response=%d %s", response.Code, response.Body.String())
	}
	if current := manager.GetChannels(); len(current) != 1 || current[0].ChannelName != "before" {
		t.Fatalf("failed applications changed effective channel: %v", current)
	}
	blockPrepare.Store(false)
	failReload.Store(false)
	response = request()
	result.Warnings = nil
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusOK || len(result.Warnings) != 0 {
		t.Fatalf("retry response=%s", response.Body.String())
	}
	if current := manager.GetChannels(); len(current) != 1 || current[0].ChannelName != "saved" {
		t.Fatalf("same-process retry did not apply saved channel: %v", current)
	}
}

func TestNotificationWriteErrorsDistinguishInputMissingAndDatabase(t *testing.T) {
	conn, manager := setupNotificationApplicationTest(t, nil)
	channel := models.NotificationChannel{ChannelType: "webhook", ChannelName: "existing", IsEnabled: true}
	if err := models.CreateNotificationChannel(t.Context(), &channel, func(tx *gorm.DB, id uint) error {
		return tx.Create(&models.CustomWebhookChannelConfig{
			ChannelID: id,
			Endpoint:  "https://example.com/hook",
			Method:    "POST",
			Format:    "json",
			Template:  `{"text":"{{title}}"}`,
		}).Error
	}); err != nil {
		t.Fatal(err)
	}
	var originalRules []models.NotificationRule
	if err := conn.Order("id").Find(&originalRules).Error; err != nil {
		t.Fatal(err)
	}
	failRead := false
	if err := conn.Callback().Query().Before("gorm:query").Register("test:notification_error_projection", func(tx *gorm.DB) {
		if failRead && tx.Statement.Table == "notification_channels" {
			tx.AddError(errors.New("private-database-cause"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	type errorCase struct {
		name            string
		method          string
		handler         gin.HandlerFunc
		payload         map[string]any
		id              uint
		template        string
		databaseFailure bool
		status          int
		code            string
		field           string
	}
	tests := []errorCase{
		{
			name:     "invalid template",
			id:       channel.ID,
			template: "{",
			status:   http.StatusBadRequest,
			code:     "INVALID_REQUEST",
			field:    "template",
		},
		{name: "missing channel", id: channel.ID + 1, status: http.StatusNotFound, code: "NOTIFICATION_CHANNEL_NOT_FOUND"},
		{
			name: "rule missing channel", method: http.MethodPut, handler: UpdateNotificationRule,
			payload: map[string]any{"channel_id": channel.ID + 1, "event_type": "system_alert", "is_enabled": false},
			status:  http.StatusNotFound, code: "NOTIFICATION_CHANNEL_NOT_FOUND",
		},
		{
			name: "rule channel database failure", method: http.MethodPut, handler: UpdateNotificationRule,
			payload:         map[string]any{"channel_id": channel.ID, "event_type": "system_alert", "is_enabled": false},
			databaseFailure: true, status: http.StatusInternalServerError, code: "INTERNAL_ERROR",
		},
		{
			name:            "database failure",
			id:              channel.ID,
			databaseFailure: true,
			status:          http.StatusInternalServerError,
			code:            "INTERNAL_ERROR",
		},
	}
	for _, operation := range []struct {
		name    string
		method  string
		handler gin.HandlerFunc
		payload map[string]any
		fields  []string
	}{
		{"create telegram", http.MethodPost, CreateTelegramChannel, map[string]any{"channel_name": "telegram", "bot_token": "token", "chat_id": "chat"}, []string{"channel_name", "bot_token", "chat_id"}},
		{"create meow", http.MethodPost, CreateMeoWChannel, map[string]any{"channel_name": "meow", "nickname": "qms"}, []string{"channel_name", "nickname"}},
		{"create bark", http.MethodPost, CreateBarkChannel, map[string]any{"channel_name": "bark", "device_key": "key"}, []string{"channel_name", "device_key"}},
		{"create serverchan", http.MethodPost, CreateServerChanChannel, map[string]any{"channel_name": "serverchan", "sc_key": "key"}, []string{"channel_name", "sc_key"}},
		{"update telegram", http.MethodPut, UpdateTelegramChannel, map[string]any{}, []string{"channel_id"}},
		{"update meow", http.MethodPut, UpdateMeoWChannel, map[string]any{}, []string{"channel_id"}},
		{"update bark", http.MethodPut, UpdateBarkChannel, map[string]any{}, []string{"channel_id"}},
		{"update serverchan", http.MethodPut, UpdateServerChanChannel, map[string]any{}, []string{"channel_id"}},
		{"update status", http.MethodPost, UpdateChannelStatus, map[string]any{"is_enabled": false}, []string{"channel_id"}},
		{"update rule", http.MethodPut, UpdateNotificationRule, map[string]any{"channel_id": channel.ID, "event_type": "system_alert", "is_enabled": false}, []string{"channel_id", "event_type"}},
	} {
		for _, field := range operation.fields {
			zero := any("")
			if field == "channel_id" {
				zero = 0
			}
			for _, input := range []struct {
				name  string
				value any
			}{
				{"missing", nil}, {"null", nil}, {"zero", zero},
				{"wrong type", map[string]string{"private": "private-request-value"}},
			} {
				payload := make(map[string]any, len(operation.payload))
				for key, value := range operation.payload {
					payload[key] = value
				}
				if input.name != "missing" {
					payload[field] = input.value
				} else {
					delete(payload, field)
				}
				wantField := field
				if input.name == "wrong type" {
					wantField = ""
				}
				tests = append(tests, errorCase{
					name:   operation.name + "/" + field + "/" + input.name,
					method: operation.method, handler: operation.handler, payload: payload,
					status: http.StatusBadRequest, code: "INVALID_REQUEST", field: wantField,
				})
			}
		}
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failRead = test.databaseFailure
			method, handler := test.method, test.handler
			if method == "" {
				method, handler = http.MethodPut, UpdateCustomWebhookChannel
			}
			payload := test.payload
			if payload == nil {
				payload = map[string]any{"channel_id": test.id, "template": test.template}
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(method, "/write", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router := gin.New()
			router.Handle(method, "/write", handler)
			router.ServeHTTP(response, request)
			var result httpapi.APIResponse[any]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.status || result.Status != test.status {
				t.Fatalf("error projection=%d %s", response.Code, response.Body.String())
			}
			if result.ErrorCode != test.code || result.Instance == "" {
				t.Fatalf("error diagnosis=%s", response.Body.String())
			}
			if result.Data != nil {
				t.Fatalf("failed write returned success data=%v", result.Data)
			}
			if result.Message == "" || bytes.Contains(response.Body.Bytes(), []byte("private-database-cause")) || bytes.Contains(response.Body.Bytes(), []byte("private-request-value")) {
				t.Fatal("internal cause leaked through notification response")
			}
			if test.field != "" && (len(result.FieldErrors) != 1 || result.FieldErrors[0].Field != test.field) {
				t.Fatalf("field location=%v", result.FieldErrors)
			}
			if test.field == "" && len(result.FieldErrors) != 0 {
				t.Fatalf("unknown error exposed field details=%v", result.FieldErrors)
			}
		})
	}
	failRead = false
	var count int64
	if err := conn.Model(&models.NotificationChannel{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 || len(manager.GetChannels()) != 0 {
		t.Fatal("invalid writes persisted or applied notification channels")
	}
	var persisted models.NotificationChannel
	if err := conn.First(&persisted, channel.ID).Error; err != nil {
		t.Fatal(err)
	}
	if persisted.ChannelName != channel.ChannelName || persisted.IsEnabled != channel.IsEnabled {
		t.Fatal("invalid writes changed the existing channel")
	}
	var rules []models.NotificationRule
	if err := conn.Order("id").Find(&rules).Error; err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rules, originalRules) {
		t.Fatal("invalid writes changed notification rules")
	}
}

func TestNotificationConnectionUnclassifiedSendFailureUsesSafeServerError(t *testing.T) {
	setupNotificationApplicationTest(t, nil)
	remoteCause := "remote-private-failure"
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, remoteCause)
	}))
	defer remote.Close()
	channel := models.NotificationChannel{ChannelType: "webhook", ChannelName: "test", IsEnabled: true}
	if err := models.CreateNotificationChannel(t.Context(), &channel, func(tx *gorm.DB, id uint) error {
		return tx.Create(&models.CustomWebhookChannelConfig{
			ChannelID: id,
			Endpoint:  remote.URL,
			Method:    "POST",
			Format:    "text",
			Template:  "{{title}}",
		}).Error
	}); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{"channel_id": channel.ID})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router := gin.New()
	router.POST("/test", TestChannelConnection)
	router.ServeHTTP(response, request)
	var result httpapi.APIResponse[any]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusInternalServerError || result.Status != response.Code {
		t.Fatalf("unclassified send failure response=%d %s", response.Code, response.Body.String())
	}
	if result.ErrorCode != "NOTIFICATION_TEST_FAILED" || result.Instance == "" {
		t.Fatalf("send failure diagnosis=%s", response.Body.String())
	}
	if result.Data != nil || bytes.Contains(response.Body.Bytes(), []byte(remoteCause)) {
		t.Fatalf("send failure leaked its cause=%s", response.Body.String())
	}
}

func TestNotificationCredentialsAreNotCacheable(t *testing.T) {
	const credential = "test-private-notification-credential"
	for _, test := range []struct {
		channelType string
		handler     gin.HandlerFunc
		config      func(uint) any
	}{
		{"telegram", GetTelegramChannel, func(id uint) any {
			return &models.TelegramChannelConfig{ChannelID: id, BotToken: credential}
		}},
		{"meow", GetMeoWChannel, func(id uint) any {
			return &models.MeoWChannelConfig{ChannelID: id, Nickname: credential}
		}},
		{"bark", GetBarkChannel, func(id uint) any {
			return &models.BarkChannelConfig{ChannelID: id, DeviceKey: credential}
		}},
		{"serverchan", GetServerChanChannel, func(id uint) any {
			return &models.ServerChanChannelConfig{ChannelID: id, SCKEY: credential}
		}},
		{"webhook", GetCustomWebhookChannel, func(id uint) any {
			return &models.CustomWebhookChannelConfig{ChannelID: id, AuthToken: credential}
		}},
	} {
		t.Run(test.channelType, func(t *testing.T) {
			conn, _ := setupNotificationApplicationTest(t, nil)
			config := test.config(0)
			if err := conn.AutoMigrate(config); err != nil {
				t.Fatal(err)
			}
			channel := models.NotificationChannel{ChannelType: test.channelType, ChannelName: "test"}
			if err := models.CreateNotificationChannel(t.Context(), &channel, func(tx *gorm.DB, id uint) error {
				return tx.Create(test.config(id)).Error
			}); err != nil {
				t.Fatal(err)
			}
			router := gin.New()
			router.GET("/:id", test.handler)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/%d", channel.ID), nil))
			if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(credential)) {
				t.Fatal("credential query did not return the stored configuration")
			}
			if response.Header().Get("Cache-Control") != "no-store, private" {
				t.Fatalf("credential response cache policy=%q", response.Header().Get("Cache-Control"))
			}
		})
	}
}
