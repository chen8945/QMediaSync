package openlist

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

func TestFileListRefreshDefaults(t *testing.T) {
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}

	tests := []struct {
		name        string
		call        func(*Client) (*FileListResp, error)
		wantRefresh bool
	}{
		{
			name: "FileList 默认刷新上游",
			call: func(client *Client) (*FileListResp, error) {
				return client.FileList(context.Background(), "/", 1, 100)
			},
			wantRefresh: true,
		},
		{
			name: "FileListWithRefresh 可显式关闭刷新",
			call: func(client *Client) (*FileListResp, error) {
				return client.FileListWithRefresh(context.Background(), "/", 1, 100, false)
			},
			wantRefresh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotRefresh bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/fs/list" {
					t.Fatalf("path = %s，期望 /api/fs/list", r.URL.Path)
				}
				var req struct {
					Refresh bool `json:"refresh"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("解析请求体失败：%v", err)
				}
				gotRefresh = req.Refresh
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"content":[],"total":0}}`))
			}))
			defer server.Close()

			client := &Client{client: resty.New().SetBaseURL(server.URL)}
			_, err := tt.call(client)
			if err != nil {
				t.Fatalf("FileList 调用失败：%v", err)
			}
			if gotRefresh != tt.wantRefresh {
				t.Fatalf("refresh = %v，期望 %v", gotRefresh, tt.wantRefresh)
			}
		})
	}
}

func TestKnownHashesOnlyUsesExplicitAlgorithms(t *testing.T) {
	sha1, md5 := KnownHashes(map[string]string{
		" SHA1 ":     " remote-sha1 ",
		"md5":        "remote-md5",
		"hashinfo":   "vendor-value",
		"unknown":    "unknown-value",
		"sha-1-like": "not-sha1",
	})
	if sha1 != "remote-sha1" || md5 != "remote-md5" {
		t.Fatalf("KnownHashes() = (%q, %q)，期望只返回明确算法键", sha1, md5)
	}
}
