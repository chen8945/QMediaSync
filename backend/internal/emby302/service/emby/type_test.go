package emby_test

import (
	"strings"
	"testing"

	"qmediasync/internal/emby302/service/emby"
)

func TestItemInfoStringRedactsAPIKey(t *testing.T) {
	itemInfo := emby.ItemInfo{
		Id:              "123",
		ApiKey:          "emby-secret",
		ApiKeyType:      emby.Query,
		ApiKeyName:      emby.QueryApiKeyName,
		PlaybackInfoUri: "/Items/123/PlaybackInfo?api_key=emby-secret",
		RouteType:       emby.RouteSyncDownload,
	}

	got := itemInfo.String()
	if strings.Contains(got, "emby-secret") {
		t.Fatalf("ItemInfo.String() 不应输出 API Key: %s", got)
	}
	if !strings.Contains(got, "******") {
		t.Fatalf("ItemInfo.String() 应包含脱敏占位符: %s", got)
	}
}

func TestItemInfoSensitiveStringKeepsAPIKey(t *testing.T) {
	itemInfo := emby.ItemInfo{
		Id:              "123",
		ApiKey:          "emby-secret",
		ApiKeyType:      emby.Query,
		ApiKeyName:      emby.QueryApiKeyName,
		PlaybackInfoUri: "/Items/123/PlaybackInfo?api_key=emby-secret",
		RouteType:       emby.RouteSyncDownload,
	}

	got := itemInfo.SensitiveString()
	if !strings.Contains(got, "emby-secret") {
		t.Fatalf("ItemInfo.SensitiveString() 应保留完整 API Key: %s", got)
	}
}
