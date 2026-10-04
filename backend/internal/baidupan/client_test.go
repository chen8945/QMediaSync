package baidupan

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	openapiclient "qmediasync/openxpanapi"
)

func TestFileManagerChecksItemErrors(t *testing.T) {
	ensureBaiduPanTestLoggers()
	items := []MoveOrCopyItem{{Path: "/source/A", Dest: "/target", NewName: "B"}}
	operations := []struct {
		name string
		call func(*Client) error
	}{
		{name: "删除", call: func(c *Client) error { return c.Del(t.Context(), []string{"/source/A"}) }},
		{name: "重命名", call: func(c *Client) error { return c.Rename(t.Context(), "/source/A", "B") }},
		{name: "批量重命名", call: func(c *Client) error {
			return c.RenameBatch(t.Context(), []ReNameItem{{Path: "/source/A", NewName: "B"}})
		}},
		{name: "移动", call: func(c *Client) error { return c.Move(t.Context(), "/source/A", "/target", "B") }},
		{name: "批量移动", call: func(c *Client) error { return c.MoveBatch(t.Context(), items) }},
		{name: "复制", call: func(c *Client) error { return c.Copy(t.Context(), "/source/A", "/target") }},
		{name: "批量复制", call: func(c *Client) error { return c.CopyBatch(t.Context(), items) }},
	}
	for _, tc := range []struct {
		name      string
		body      string
		wantErr   bool
		wantToken bool
	}{
		{name: "全部成功", body: `{"errno":0,"info":[{"errno":0},{"errno":0}]}`},
		{name: "旧版无逐项结果", body: `{"errno":0}`},
		{name: "部分失败", body: `{"errno":0,"info":[{"errno":0},{"errno":-9}]}`, wantErr: true},
		{name: "全部失败", body: `{"errno":0,"info":[{"errno":-9},{"errno":-7}]}`, wantErr: true},
		{name: "顶层失败优先", body: `{"errno":-6,"info":[{"errno":-9}]}`, wantErr: true, wantToken: true},
		{name: "逐项凭据错误", body: `{"errno":0,"info":[{"errno":20016}]}`, wantErr: true, wantToken: true},
		{name: "逐项格式错误", body: `{"errno":0,"info":{"errno":-9}}`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Query().Get("method") != "filemanager" {
					t.Errorf("非文件管理请求：%s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			config := openapiclient.NewConfiguration()
			for _, name := range []string{"copy", "delete", "move", "rename"} {
				config.OperationServers["FilemanagerApiService.Filemanager"+name][0].URL = server.URL
			}
			client := &Client{client: openapiclient.NewAPIClient(config)}
			client.SetAuthToken("test-token")
			for _, operation := range operations {
				t.Run(operation.name, func(t *testing.T) {
					err := operation.call(client)
					if (err != nil) != tc.wantErr {
						t.Fatalf("错误 = %v，期望失败 = %v", err, tc.wantErr)
					}
					if _, ok := errors.AsType[*TokenInvalidError](err); ok != tc.wantToken {
						t.Fatalf("凭据错误分类不符：%v", err)
					}
				})
			}
			if calls != len(operations) {
				t.Fatalf("请求次数 = %d，期望 %d，操作不能自动重发", calls, len(operations))
			}
		})
	}
}

func TestGetFileDetailRejectsEmptyList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"errno": 0, "list": []}`))
	}))
	defer server.Close()

	config := openapiclient.NewConfiguration()
	config.OperationServers["MultimediafileApiService.Xpanmultimediafilemetas"][0].URL = server.URL
	client := &Client{
		client: openapiclient.NewAPIClient(config),
	}
	client.SetAuthToken("test-token")

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

func resetBaiduClientCache(t *testing.T) {
	t.Helper()
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*Client{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})
}

// 所有 Token 写入入口都必须允许与持有同一客户端的请求并发。
func TestClientConcurrentTokenUpdates(t *testing.T) {
	ensureBaiduPanTestLoggers()
	for _, tt := range []struct {
		name   string
		update func(*Client, string)
	}{
		{name: "缓存命中", update: func(_ *Client, token string) { NewBaiDuPanClient(1, token) }},
		{name: "刷新 Token", update: func(_ *Client, token string) { UpdateToken(1, token) }},
		{name: "直接设置", update: (*Client).SetAuthToken},
		{name: "条件刷新", update: func(_ *Client, token string) {
			expected := "initial-token"
			if token == "initial-token" {
				expected = "updated-token"
			}
			UpdateTokenIfCurrent(1, expected, token)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetBaiduClientCache(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				token := r.URL.Query().Get("access_token")
				if token != "initial-token" && token != "updated-token" {
					t.Error("请求未使用完整的 Token 快照")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"errno":0,"total":1024}`)
			}))
			defer server.Close()
			client := NewBaiDuPanClient(1, "initial-token")
			client.client.GetConfig().OperationServers["UserinfoApiService.Apiquota"][0].URL = server.URL
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			start := make(chan struct{})
			var workers sync.WaitGroup
			workers.Go(func() {
				<-start
				for i := range 200 {
					token := "updated-token"
					if i%2 == 1 {
						token = "initial-token"
					}
					tt.update(client, token)
					runtime.Gosched()
				}
			})
			for range 4 {
				workers.Go(func() {
					<-start
					for range 10 {
						if _, err := client.GetQuota(ctx); err != nil {
							t.Errorf("并发更新 Token 时请求失败：%v", err)
							return
						}
					}
				})
			}
			close(start)
			workers.Wait()
			tt.update(client, "updated-token")
			if client.getAccessToken() != "updated-token" {
				t.Fatal("更新未对已持有的共享客户端生效")
			}
		})
	}
}

func TestUpdateTokenIfCurrentSkipsReplacedCredentials(t *testing.T) {
	resetBaiduClientCache(t)

	client := NewBaiDuPanClient(12, "new-token")
	if UpdateTokenIfCurrent(12, "old-token", "stale-token") {
		t.Fatal("共享客户端凭据已替换时不应接受旧刷新结果")
	}
	if got := client.getAccessToken(); got != "new-token" {
		t.Fatalf("旧刷新结果改变了新共享凭据: %q", got)
	}
	if !UpdateTokenIfCurrent(12, "new-token", "latest-token") {
		t.Fatal("当前共享客户端凭据应允许条件更新")
	}
	if got := client.getAccessToken(); got != "latest-token" {
		t.Fatalf("条件更新未应用: %q", got)
	}
	if !UpdateTokenIfCurrent(12, "latest-token", "") || client.getAccessToken() != "" {
		t.Fatal("当前凭据应允许条件清空")
	}
	if !UpdateTokenIfCurrent(12, "", "restored-token") {
		t.Fatal("空凭据应允许重新授权")
	}
	if UpdateTokenIfCurrent(99, "", "unexpected-token") {
		t.Fatal("条件更新不能创建不存在的缓存客户端")
	}
}

// 长上传后续分片与创建请求必须读取刷新后的 Token，不能固定整次上传的凭据。
func TestUploadReadsTokenForEachRequest(t *testing.T) {
	ensureBaiduPanTestLoggers()
	client := NewBaiDuPanClientWithToken("precreate-token")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := r.URL.Query().Get("method")
		if r.URL.Query().Get("access_token") != method+"-token" {
			t.Errorf("%s 请求未读取更新后的 Token", method)
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch method {
		case "precreate":
			client.SetAuthToken("upload-token")
			_, _ = io.WriteString(w, `{"errno":0,"uploadid":"upload-id","block_list":[0]}`)
		case "upload":
			client.SetAuthToken("create-token")
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{"errno":0,"md5":"chunk-md5"}`)
		case "create":
			_, _ = io.WriteString(w, `{"errno":0,"fs_id":123}`)
		default:
			t.Errorf("意外的上传请求：%s", method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	config := client.client.GetConfig()
	for _, operation := range []string{"Xpanfileprecreate", "Pcssuperfile2", "Xpanfilecreate"} {
		config.OperationServers["FileuploadApiService."+operation][0].URL = server.URL
	}
	localPath := filepath.Join(t.TempDir(), "upload.nfo")
	if err := os.WriteFile(localPath, []byte("upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := client.Upload(t.Context(), localPath, "/upload.nfo")
	if err != nil || result == nil || result.GetFsId() != 123 {
		t.Fatalf("更新 Token 后上传结果 = %+v，错误 = %v", result, err)
	}
	if requests.Load() != 3 {
		t.Fatalf("上传请求次数 = %d，期望 3", requests.Load())
	}
}

func TestGetFileListRejectsMissingList(t *testing.T) {
	ensureBaiduPanTestLoggers()
	for _, tc := range []struct {
		name, body string
		wantErr    bool
	}{
		{name: "empty array", body: `{"errno":0,"list":[]}`},
		{name: "missing list", body: `{"errno":0}`, wantErr: true},
		{name: "null list", body: `{"errno":0,"list":null}`, wantErr: true},
		{name: "null body", body: `null`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			config := openapiclient.NewConfiguration()
			config.OperationServers["FileinfoApiService.Xpanfilelist"][0].URL = server.URL
			c := &Client{client: openapiclient.NewAPIClient(config)}
			c.SetAuthToken("test")
			_, err := c.GetFileList(t.Context(), "/media", 0, 1, 0, 1000)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestGetFileDetailDistinguishesAbsentFromInvalid(t *testing.T) {
	ensureBaiduPanTestLoggers()
	for _, tc := range []struct {
		name, body string
		absent     bool
	}{
		{name: "confirmed empty", body: `{"errno":0,"list":[]}`, absent: true},
		{name: "missing list", body: `{"errno":0}`},
		{name: "null list", body: `{"errno":0,"list":null}`},
		{name: "missing status", body: `{"list":[]}`},
		{name: "null entry", body: `{"errno":0,"list":[null]}`},
		{name: "business failure", body: `{"errno":-9,"list":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			config := openapiclient.NewConfiguration()
			config.OperationServers["MultimediafileApiService.Xpanmultimediafilemetas"][0].URL = server.URL
			c := &Client{client: openapiclient.NewAPIClient(config)}
			c.SetAuthToken("test")
			_, err := c.GetFileDetail(t.Context(), "10", 0)
			if err == nil || errors.Is(err, ErrFileAbsent) != tc.absent {
				t.Fatalf("err=%v", err)
			}
		})
	}
}
