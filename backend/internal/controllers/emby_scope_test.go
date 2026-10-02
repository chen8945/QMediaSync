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

	"github.com/gin-gonic/gin"
)

func TestEmbyWebhookCancelledDeleteKeepsLocalIndex(t *testing.T) {
	testDB := setupControllerTestDB(t, &models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{})
	previousConfig := models.GlobalEmbyConfig
	models.GlobalEmbyConfig = &models.EmbyConfig{EmbyUrl: "http://unused.invalid", EmbyApiKey: "test", EnableDeleteNetdisk: 1}
	t.Cleanup(func() { models.GlobalEmbyConfig = previousConfig })
	item := &models.EmbyMediaItem{ItemId: "episode", SeasonId: "season"}
	if err := testDB.Create(item).Error; err != nil {
		t.Fatal(err)
	}
	relation := &models.EmbyMediaSyncFile{EmbyItemId: item.ID, SyncFileId: 1}
	if err := testDB.Create(relation).Error; err != nil {
		t.Fatal(err)
	}
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, "/emby/webhook",
		strings.NewReader(`{"Event":"library.deleted","Item":{"Type":"Season","Id":"season"}}`))
	response := httptest.NewRecorder()
	router := gin.New()
	router.POST("/emby/webhook", Webhook)
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || ctx.Err() == nil {
		t.Fatalf("应等待至取消并保留原响应格式：status=%d err=%v", response.Code, ctx.Err())
	}
	for _, model := range []any{&models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}} {
		var count int64
		if err := testDB.Model(model).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("取消后应保留本地记录 %T：count=%d err=%v", model, count, err)
		}
	}
}
