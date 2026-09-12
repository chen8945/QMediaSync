package openlist

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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

func TestUploadAuthenticationRecovery(t *testing.T) {
	setupConcurrentClientTest(t)
	helpers.InitEventBus()
	t.Cleanup(helpers.InitEventBus)
	// 超过 Resty 内容类型探测的 512 字节前缀，确保重发实际读到了完整文件。
	content := strings.Repeat("openlist upload content\n", 256)
	localFile := filepath.Join(t.TempDir(), "重试 upload.txt")
	if err := os.WriteFile(localFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	networkErr := errors.New("upload connection interrupted")
	for _, tc := range []struct {
		name           string
		refresh        bool
		networkFailure bool
		wantUploads    int
		wantLogins     int
	}{
		{name: "刷新后完整重发文件", refresh: true, wantUploads: 2, wantLogins: 1},
		{name: "普通网络错误不重发", networkFailure: true, wantUploads: 1},
		{name: "认证重发网络错误不再重发", refresh: true, networkFailure: true, wantUploads: 2, wantLogins: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var uploads, logins int
			client := NewClient(1, "http://openlist.invalid/openlist/", "user", "password", "old-token")
			client.client.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				isUpload := r.URL.Path == "/openlist/api/fs/form"
				resp, err := handlerTransport(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if r.URL.Path == "/openlist/api/auth/login" {
						logins++
						_, _ = io.WriteString(w, `{"code":200,"data":{"token":"new-token"}}`)
						return
					}
					if !isUpload {
						t.Errorf("意外请求：%s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
						return
					}
					uploads++
					if r.Method != http.MethodPut {
						t.Errorf("上传方法 = %s，期望 PUT", r.Method)
					}
					token := "old-token"
					if uploads > 1 {
						token = "new-token"
					}
					for key, want := range map[string]string{"Authorization": token, "As-Task": "true", "Overwrite": "false", "User-Agent": DEFAULTUA} {
						if got := r.Header.Get(key); got != want {
							t.Errorf("第 %d 次上传 %s = %q，期望 %q", uploads, key, got, want)
						}
					}
					if path, err := url.PathUnescape(r.Header.Get("File-Path")); err != nil || path != "/media/授权 upload.txt" {
						t.Errorf("上传目标路径 = %q，错误 = %v", path, err)
					}
					file, header, err := r.FormFile("file")
					if err != nil {
						t.Errorf("第 %d 次上传解析文件失败：%v", uploads, err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer file.Close()
					defer r.MultipartForm.RemoveAll()
					body, err := io.ReadAll(file)
					if err != nil || string(body) != content || header.Filename != filepath.Base(localFile) {
						t.Errorf("第 %d 次上传文件内容或文件名不完整：name=%q bytes=%d err=%v", uploads, header.Filename, len(body), err)
					}
					if tc.refresh && uploads == 1 {
						_, _ = io.WriteString(w, `{"code":401,"message":"expired","data":null}`)
						return
					}
					_, _ = io.WriteString(w, `{"code":200,"data":{"task":{"id":"uploaded"}}}`)
				}).RoundTrip(r)
				if isUpload && tc.networkFailure && (!tc.refresh || uploads > 1) {
					_ = resp.Body.Close()
					return nil, networkErr
				}
				return resp, err
			}))
			result, err := client.Upload(localFile, `media\授权 upload.txt`)
			if uploads != tc.wantUploads || logins != tc.wantLogins {
				t.Errorf("上传 %d 次，登录 %d 次，期望 %d、%d", uploads, logins, tc.wantUploads, tc.wantLogins)
			}
			if tc.networkFailure {
				if !errors.Is(err, networkErr) {
					t.Errorf("上传错误 = %v，期望网络错误", err)
				}
			} else if err != nil || result == nil || result.ID != "uploaded" {
				t.Errorf("上传结果 = %+v，错误 = %v", result, err)
			}
		})
	}
}
