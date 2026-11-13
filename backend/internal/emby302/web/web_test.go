package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/emby302/config"

	"github.com/gin-gonic/gin"
)

func TestEmbyRestartRequired(t *testing.T) {
	previous := activeListener.Load()
	t.Cleanup(func() { activeListener.Store(previous) })
	activeListener.Store(nil)
	if EmbyRestartRequired(false, "http://emby", "key") || !EmbyRestartRequired(true, "http://emby", "key") {
		t.Fatal("未启动代理时，应只对完整且已启用的配置提示重启")
	}
	activeListener.Store(&listenerConfig{host: "http://emby", apiKey: "key"})
	if EmbyRestartRequired(true, "http://emby", "key") {
		t.Fatal("原连接重复保存不应要求重启")
	}
	for _, candidate := range []struct {
		enabled   bool
		host, key string
	}{{false, "http://emby", "key"}, {true, "http://new-emby", "key"}, {true, "http://emby", "new-key"}} {
		if !EmbyRestartRequired(candidate.enabled, candidate.host, candidate.key) {
			t.Fatal("启停或连接变化未提示重启")
		}
	}
}

func TestGuardPrecedesProxyMiddlewares(t *testing.T) {
	previous := config.C
	config.C = &config.Config{Cache: &config.Cache{}}
	t.Cleanup(func() { config.C = previous })
	router := gin.New()
	initRouter(router, func(c *gin.Context) { c.AbortWithStatus(http.StatusServiceUnavailable) })
	// PlaybackInfo 原本会先校验上游凭据；门禁拒绝后不得访问未配置的上游。
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/Items/1/PlaybackInfo", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("门禁响应=%d", response.Code)
	}
}
