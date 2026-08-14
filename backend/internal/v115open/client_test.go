package v115open

import "testing"

func TestGetClientUpdatesCachedAppID(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	first := GetClient(12, "old-app", "old-token", "old-refresh")
	second := GetClient(12, "new-app", "new-token", "new-refresh")
	if first != second {
		t.Fatal("同一账号应复用缓存客户端")
	}
	if second.AppId != "new-app" || second.AccessToken != "new-token" || second.RefreshTokenStr != "new-refresh" {
		t.Fatalf("缓存客户端未更新授权信息: %#v", second)
	}
}

func TestGetCachedClientDoesNotOverwriteCachedAuth(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	current := GetClient(12, "new-app", "new-token", "new-refresh")
	stale := GetCachedClient(12, "old-app", "old-token", "old-refresh")
	if stale != current {
		t.Fatal("已有账号应复用共享客户端")
	}
	if stale.AppId != "new-app" || stale.AccessToken != "new-token" || stale.RefreshTokenStr != "new-refresh" {
		t.Fatalf("旧账号快照覆盖了共享客户端凭据: %#v", stale)
	}
}

func TestNewClientDoesNotEnterCache(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	temporary := NewClient(12, "temporary-app", "token", "refresh")
	cachedClientsMutex.RLock()
	_, ok := cachedClients["12"]
	cachedClientsMutex.RUnlock()
	if ok || temporary.AppId != "temporary-app" {
		t.Fatalf("临时客户端不应写入缓存: cached=%v client=%#v", ok, temporary)
	}
}

func TestUpdateTokenIfCurrentSkipsReplacedCredentials(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	client := GetClient(12, "app", "new-token", "new-refresh")
	if UpdateTokenIfCurrent(12, "old-token", "old-refresh", "stale-token", "stale-refresh") {
		t.Fatal("共享客户端凭据已替换时不应接受旧刷新结果")
	}
	if client.AccessToken != "new-token" || client.RefreshTokenStr != "new-refresh" {
		t.Fatalf("旧刷新结果改变了新共享凭据: %#v", client)
	}
	if !UpdateTokenIfCurrent(12, "new-token", "new-refresh", "latest-token", "latest-refresh") {
		t.Fatal("当前共享客户端凭据应允许条件更新")
	}
	if client.AccessToken != "latest-token" || client.RefreshTokenStr != "latest-refresh" {
		t.Fatalf("条件更新未应用: %#v", client)
	}
}
