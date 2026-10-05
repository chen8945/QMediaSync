package controllers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
)

func TestEmbyWebhookReceiptDoesNotWaitForScopeOrUseRequestLifetime(t *testing.T) {
	conn, router := setupEmbyReceiptController(t, true)
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequestWithContext(ctx, http.MethodPost, "/emby/webhook", strings.NewReader(controllerVideoDeleted)))
	if response.Code != http.StatusOK || ctx.Err() != nil {
		t.Fatalf("receipt must return while scope is busy: status=%d err=%v", response.Code, ctx.Err())
	}
	cancel()
	var records []models.EmbyWebhookRecord
	if err := conn.Find(&records).Error; err != nil || len(records) != 1 || records[0].InputJSON == "" {
		t.Fatalf("request cancellation cannot remove saved work: %+v err=%v", records, err)
	}
	var count int64
	if err := conn.Model(&models.EmbyMediaSyncFile{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("receipt must preserve unfinished index: count=%d err=%v", count, err)
	}
}
