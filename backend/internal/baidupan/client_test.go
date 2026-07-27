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
