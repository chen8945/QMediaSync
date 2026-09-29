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
	"time"

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

func TestFileTransferRetryPolicy(t *testing.T) {
	previous := helpers.OpenListLog
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.OpenListLog = previous })
	for _, operation := range []struct {
		name string
		call func(*Client, string, string, []string) ([]string, error)
	}{
		{name: "move", call: (*Client).MoveWithTasks},
		{name: "copy", call: (*Client).CopyWithTasks},
	} {
		for _, tc := range []struct {
			name           string
			refresh        bool
			networkFailure bool
		}{
			{name: "响应丢失不重发", networkFailure: true},
			{name: "认证恢复后成功", refresh: true},
			{name: "认证恢复后响应丢失不重发", refresh: true, networkFailure: true},
		} {
			t.Run(operation.name+"/"+tc.name, func(t *testing.T) {
				var writes, logins int
				client := NewTemporaryClient("http://openlist.invalid", "user", "password", "old-token")
				defer client.Close()
				client.client.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					isWrite := r.URL.Path == "/api/fs/"+operation.name
					resp, err := handlerTransport(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if r.URL.Path == "/api/auth/login" {
							logins++
							_, _ = io.WriteString(w, `{"code":200,"data":{"token":"new-token"}}`)
							return
						}
						if !isWrite || r.Method != http.MethodPost {
							t.Errorf("意外请求：%s %s", r.Method, r.URL.Path)
						}
						writes++
						if tc.refresh && writes == 1 {
							_, _ = io.WriteString(w, `{"code":401,"message":"expired"}`)
							return
						}
						if tc.refresh && r.Header.Get("Authorization") != "new-token" {
							t.Error("认证恢复后未使用新凭据")
						}
						_, _ = io.WriteString(w, `{"code":200,"data":{"tasks":[{"id":"accepted"}]}}`)
					}).RoundTrip(r)
					// 模拟上游已受理写入，但响应在返回途中丢失。
					if isWrite && tc.networkFailure && (!tc.refresh || writes > 1) {
						_ = resp.Body.Close()
						return nil, io.ErrUnexpectedEOF
					}
					return resp, err
				}))
				ids, err := operation.call(client, "/source", "/target", []string{"movie.mkv"})
				wantWrites, wantLogins := 1, 0
				if tc.refresh {
					wantWrites, wantLogins = 2, 1
				}
				if writes != wantWrites || logins != wantLogins {
					t.Errorf("写入 %d 次，登录 %d 次，期望 %d、%d", writes, logins, wantWrites, wantLogins)
				}
				if tc.networkFailure {
					if !errors.Is(err, io.ErrUnexpectedEOF) {
						t.Errorf("错误 = %v，期望保留网络错误", err)
					}
				} else if err != nil || len(ids) != 1 || ids[0] != "accepted" {
					t.Errorf("任务 ID = %v，错误 = %v", ids, err)
				}
			})
		}
	}
}

func TestRenameNoReplace(t *testing.T) {
	previous := helpers.OpenListLog
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.OpenListLog = previous })
	for _, tc := range []struct {
		name        string
		target      string
		unsupported bool
		legacy      bool
		wantErr     bool
	}{
		{name: "新名称", target: "new.mkv"},
		{name: "已有目标不覆盖", target: "existing.mkv", wantErr: true},
		{name: "自身名称", target: "source.mkv "},
		{name: "旧服务不支持时禁止回退", target: "new.mkv", unsupported: true, wantErr: true},
		{name: "刮削保留原批量接口", target: "new.mkv", legacy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"source.mkv ": "source", "existing.mkv": "destination"}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				if tc.legacy {
					if r.URL.Path != "/api/fs/batch_rename" {
						t.Errorf("刮削接口被修改：%s", r.URL.Path)
					}
					_, _ = io.WriteString(w, `{"code":200,"data":null}`)
					return
				}
				var req struct {
					Path      string `json:"path"`
					Name      string `json:"name"`
					Overwrite *bool  `json:"overwrite"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/api/fs/rename" || req.Path != "/source.mkv " || req.Name != tc.target || req.Overwrite == nil || *req.Overwrite {
					t.Errorf("重命名请求不符合不覆盖契约：%s %+v", r.URL.Path, req)
					_, _ = io.WriteString(w, `{"code":400,"message":"invalid request"}`)
					return
				}
				if tc.unsupported {
					w.WriteHeader(http.StatusNotFound)
					_, _ = io.WriteString(w, `{"code":404,"message":"unsupported"}`)
					return
				}
				if _, exists := files[req.Name]; exists && req.Name != "source.mkv " {
					_, _ = io.WriteString(w, `{"code":403,"message":"file exists"}`)
					return
				}
				content := files["source.mkv "]
				delete(files, "source.mkv ")
				files[req.Name] = content
				_, _ = io.WriteString(w, `{"code":200,"data":null}`)
			}))
			defer server.Close()
			client := NewTemporaryClient(server.URL, "", "", "fixture-token")
			defer client.Close()
			var err error
			if tc.legacy {
				err = client.Rename("/", "source.mkv ", tc.target)
			} else {
				err = client.RenameNoReplace("/source.mkv ", tc.target)
			}
			if (err != nil) != tc.wantErr || calls != 1 {
				t.Fatalf("错误=%v，期望失败=%v，请求次数=%d", err, tc.wantErr, calls)
			}
			if files["existing.mkv"] != "destination" || (tc.wantErr && files["source.mkv "] != "source") {
				t.Fatalf("不应修改的文件内容发生变化：%v", files)
			}
			if !tc.legacy && !tc.wantErr && files[tc.target] != "source" {
				t.Fatalf("重命名未生效：%v", files)
			}
		})
	}
}

func TestDirListPropagatesCancellation(t *testing.T) {
	previousLogger := helpers.OpenListLog
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.OpenListLog = previousLogger })
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/fs/dirs" {
			t.Errorf("意外路径：%s", r.URL.Path)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		select {
		case <-r.Context().Done():
		case <-time.After(3 * time.Second):
		}
	}))
	defer server.Close()
	client := &Client{client: resty.New().SetBaseURL(server.URL)}
	defer client.client.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := client.DirList(ctx, "/", false); result <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("目录请求未开始")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("期望取消错误，得到%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("目录请求没有随context取消")
	}
}
