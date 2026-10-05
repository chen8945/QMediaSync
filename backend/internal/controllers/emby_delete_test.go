package controllers

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

func setupEmbyReceiptController(t *testing.T, enabled bool) (*gorm.DB, *gin.Engine) {
	t.Helper()
	conn := setupControllerTestDB(t, &models.EmbyConfig{}, &models.EmbyIndexState{}, &models.EmbyItemState{},
		&models.EmbyItemEvidence{}, &models.EmbyItemMembership{}, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyWebhookRecord{},
		&models.EmbyWebhookTarget{}, &models.SyncFile{}, &models.SyncPath{}, &models.Account{})
	previousConfig, previousLogger := models.GlobalEmbyConfig, helpers.AppLogger
	t.Cleanup(func() { models.GlobalEmbyConfig, helpers.AppLogger = previousConfig, previousLogger })
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	config := &models.EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "offline-only", SyncEnabled: 1}
	if enabled {
		config.EnableDeleteNetdisk = 1
	}
	if err := conn.Create(config).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(config).Update("enable_auth", 0).Error; err != nil {
		t.Fatal(err)
	}
	models.GlobalEmbyConfig = config
	if err := conn.Create(&models.EmbyMediaItem{ItemId: "20", ItemIdInt: 20, Type: "Video"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.EmbyMediaSyncFile{EmbyItemId: 20, SyncFileId: 1}).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.POST("/emby/webhook", Webhook)
	return conn, router
}

const controllerVideoDeleted = `{"Event":"library.deleted","Server":{"Id":"server"},"Item":{"Id":"20","Type":"Video","Path":"/media/part2.strm"}}`

func TestEmbyVideoDeletePersistedBeforeAcknowledgement(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "local_only", true: "linked_unverified"}[enabled], func(t *testing.T) {
			conn, router := setupEmbyReceiptController(t, enabled)
			for range 2 {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/emby/webhook", strings.NewReader(controllerVideoDeleted)))
				if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != `{"message":"webhook"}` {
					t.Fatalf("compatible saved response: status=%d body=%s", response.Code, response.Body.String())
				}
			}
			var records []models.EmbyWebhookRecord
			if err := conn.Find(&records).Error; err != nil || len(records) != 2 {
				t.Fatalf("every receipt persists independently: %d %v", len(records), err)
			}
			for _, record := range records {
				if record.Authorized != enabled || record.InputJSON == "" || record.ItemType != "Video" {
					t.Fatalf("receipt auth/input lost: %+v", record)
				}
			}
			var count int64
			if err := conn.Model(&models.EmbyMediaSyncFile{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("HTTP receipt cannot clear index or await deletion: %d %v", count, err)
			}
		})
	}
}

func TestEmbyWebhookSaveFailureNotAcknowledged(t *testing.T) {
	conn, router := setupEmbyReceiptController(t, true)
	if err := conn.Migrator().DropTable(&models.EmbyWebhookRecord{}); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/emby/webhook", strings.NewReader(controllerVideoDeleted)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unsaved receipt must not return success: %d %s", response.Code, response.Body.String())
	}
	var count int64
	if err := conn.Model(&models.EmbyMediaSyncFile{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("save failure lost index: %d %v", count, err)
	}
}

func TestEmbyWebhookUnconfiguredNotAcknowledged(t *testing.T) {
	conn, router := setupEmbyReceiptController(t, false)
	if err := conn.Model(&models.EmbyConfig{}).Where("id > 0").Update("emby_url", "").Error; err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/emby/webhook", strings.NewReader(controllerVideoDeleted)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured receipt cannot imply saved: %d", response.Code)
	}
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/emby/webhook", strings.NewReader(`{"Event":"system.test"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("unrelated unconfigured callback keeps compatible response: %d", response.Code)
	}
}

func TestEmbyWebhookLimitsAndSanitizesInput(t *testing.T) {
	conn, router := setupEmbyReceiptController(t, false)
	var logs bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"duplicate", `{"Event":"library.deleted","Event":"deep.delete"}`, http.StatusBadRequest},
		{"oversized", strings.Repeat("x", requests.EmbyWebhookMaxBytes+1), http.StatusRequestEntityTooLarge},
		{"unknown", `{"Event":"system.notificationtest","Description":"https://user:password@media.invalid/a?api_key=secret"}`, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/emby/webhook", strings.NewReader(test.body)))
			if response.Code != test.status {
				t.Fatalf("status %d, want %d", response.Code, test.status)
			}
		})
	}
	body := `{"Event":"deep.delete","Title":"untrusted secret title","Description":"Item Name:\nname\n\nItem Path:\n/media/movie\n\nMount Paths:\nhttps://user:password@media.invalid/a?api_key=secret&pickcode=fixture-code","Server":{"Id":"server"},"Item":{"Id":"20","Type":"Video","Path":"/media/part2.strm"}}`
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/emby/webhook", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("valid deep must save: %d %s", response.Code, response.Body.String())
	}
	var records []models.EmbyWebhookRecord
	if err := conn.Find(&records).Error; err != nil || len(records) != 1 {
		t.Fatalf("only valid managed input should persist: %d %v", len(records), err)
	}
	stored, _ := json.Marshal(records)
	for _, text := range []string{logs.String(), string(stored), response.Body.String()} {
		for _, secret := range []string{"password", "api_key", "untrusted secret title"} {
			if strings.Contains(text, secret) {
				t.Fatalf("credentials/raw prose leaked: %s", secret)
			}
		}
	}
}
