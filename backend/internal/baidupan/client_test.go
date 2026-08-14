package baidupan

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openapiclient "qmediasync/openxpanapi"
)

func TestGetFileDetailRejectsEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errno": 0, "list": []}`))
	}))
	defer server.Close()

	config := openapiclient.NewConfiguration()
	config.OperationServers["MultimediafileApiService.Xpanmultimediafilemetas"][0].URL = server.URL
	client := &Client{
		client:      openapiclient.NewAPIClient(config),
		accessToken: "test-token",
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("空文件列表不应触发 panic: %v", recovered)
		}
	}()

	_, err := client.GetFileDetail(context.Background(), "123", 1)
	if err == nil || !strings.Contains(err.Error(), "文件详情为空") {
		t.Fatalf("空文件列表错误 = %v，期望明确的文件详情为空错误", err)
	}
}

func TestUpdateTokenIfCurrentSkipsReplacedCredentials(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*Client{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	client := NewBaiDuPanClient(12, "new-token")
	if UpdateTokenIfCurrent(12, "old-token", "stale-token") {
		t.Fatal("共享客户端凭据已替换时不应接受旧刷新结果")
	}
	if client.accessToken != "new-token" {
		t.Fatalf("旧刷新结果改变了新共享凭据: %q", client.accessToken)
	}
	if !UpdateTokenIfCurrent(12, "new-token", "latest-token") {
		t.Fatal("当前共享客户端凭据应允许条件更新")
	}
	if client.accessToken != "latest-token" {
		t.Fatalf("条件更新未应用: %q", client.accessToken)
	}
}
