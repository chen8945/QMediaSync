package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/service/m3u8"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/web/cache"

	"github.com/gin-gonic/gin"
)

func TestGuardPrecedesProxyMiddlewares(t *testing.T) {
	previous := config.C
	config.C = &config.Config{Cache: &config.Cache{}}
	t.Cleanup(func() { config.C = previous })
	client := https.NewClient(https.ClientOptions{})
	responses := cache.NewStore(time.Hour)
	playlists := m3u8.NewStore(client)
	session := emby.NewSession(emby.SessionOptions{Origin: "http://invalid.test", Client: client, Cache: responses.NewScope()})
	t.Cleanup(func() {
		session.Close()
		_ = responses.Stop(context.Background())
		_ = playlists.Stop(context.Background())
		client.CloseIdleConnections()
	})
	router := NewRouter(session, func(c *gin.Context) { c.AbortWithStatus(http.StatusServiceUnavailable) }, playlists)
	// PlaybackInfo 原本会先校验上游凭据；门禁拒绝后不得访问未配置的上游。
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/Items/1/PlaybackInfo", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("门禁响应=%d", response.Code)
	}
}
