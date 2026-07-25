package v115open

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"

	"resty.dev/v3"
)

func TestFileModifiedAtPrefersOfficialModificationTime(t *testing.T) {
	tests := []struct {
		name string
		file File
		want int64
	}{
		{name: "优先列表修改时间", file: File{Utime: 200, Ptime: 100}, want: 200},
		{name: "修改时间缺失回退上传时间", file: File{Ptime: 100}, want: 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.file.ModifiedAt(); got != tt.want {
				t.Fatalf("ModifiedAt() = %d，期望 %d", got, tt.want)
			}
		})
	}
}

func TestFileDetailModifiedAtAndFolderCount(t *testing.T) {
	var detail FileDetail
	if err := json.Unmarshal([]byte(`{"utime":"200","ptime":"100","folder_count":3}`), &detail); err != nil {
		t.Fatalf("解析 115 文件详情失败：%v", err)
	}
	if got := detail.ModifiedAt(); got != 200 {
		t.Fatalf("详情 ModifiedAt() = %d，期望 200", got)
	}
	if got := detail.FolderCount.String(); got != "3" {
		t.Fatalf("FolderCount = %s，期望 3", got)
	}

	detail.Utime = "invalid"
	if got := detail.ModifiedAt(); got != 100 {
		t.Fatalf("无效 utime 回退结果 = %d，期望 100", got)
	}
}

type capturedOpenAPIRequest struct {
	Method string
	URL    string
	Body   string
}

type captureOpenAPITransport struct {
	response string
	requests chan capturedOpenAPIRequest
}

func newCaptureOpenAPITransport(response string) *captureOpenAPITransport {
	return &captureOpenAPITransport{
		response: response,
		requests: make(chan capturedOpenAPIRequest, 8),
	}
}

func (t *captureOpenAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	t.requests <- capturedOpenAPIRequest{
		Method: req.Method,
		URL:    req.URL.String(),
		Body:   string(body),
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(t.response)),
		Request:    req,
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func newTestOpenClient(transport *captureOpenAPITransport) *OpenClient {
	ensureOpenAPITestLoggers()
	return &OpenClient{
		AppId:           "test-app-id",
		AccountId:       1,
		client:          resty.New().SetTransport(transport),
		AccessToken:     "test-access-token",
		RefreshTokenStr: "test-refresh-token",
	}
}

func ensureOpenAPITestLoggers() {
	if helpers.AppLogger == nil {
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
	if helpers.V115Log == nil {
		helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	}
}

func receiveCapturedRequest(t *testing.T, transport *captureOpenAPITransport) capturedOpenAPIRequest {
	t.Helper()

	select {
	case req := <-transport.requests:
		return req
	case <-time.After(3 * time.Second):
		t.Fatal("未捕获到 115 OpenAPI 请求")
		return capturedOpenAPIRequest{}
	}
}

func mustParseURLPath(t *testing.T, rawURL string) string {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("解析请求 URL 失败：%v", err)
	}
	return parsedURL.Path
}

func TestOpenClient_FileMutationEndpointsUseOfficialPaths(t *testing.T) {
	tests := []struct {
		name     string
		response string
		wantPath string
		action   func(context.Context, *OpenClient) error
	}{
		{
			name:     "重命名使用文件更新接口",
			response: `{"state":true,"code":0,"data":{}}`,
			wantPath: "/open/ufile/update",
			action: func(ctx context.Context, client *OpenClient) error {
				_, err := client.ReName(ctx, "file-id", "new-name")
				return err
			},
		},
		{
			name:     "移动使用文件移动接口",
			response: `{"state":true,"code":0,"data":[]}`,
			wantPath: "/open/ufile/move",
			action: func(ctx context.Context, client *OpenClient) error {
				_, err := client.Move(ctx, []string{"file-id"}, "target-cid")
				return err
			},
		},
		{
			name:     "复制使用文件复制接口",
			response: `{"state":true,"code":0,"data":[]}`,
			wantPath: "/open/ufile/copy",
			action: func(ctx context.Context, client *OpenClient) error {
				_, err := client.Copy(ctx, []string{"file-id"}, "target-cid", false)
				return err
			},
		},
		{
			name:     "删除使用文件删除接口",
			response: `{"state":true,"code":0,"data":[]}`,
			wantPath: "/open/ufile/delete",
			action: func(ctx context.Context, client *OpenClient) error {
				_, err := client.Del(ctx, []string{"file-id"}, "parent-cid")
				return err
			},
		},
		{
			name:     "新建文件夹使用新建文件夹接口",
			response: `{"state":true,"code":0,"data":{"file_name":"new-dir","file_id":"new-dir-id"}}`,
			wantPath: "/open/folder/add",
			action: func(ctx context.Context, client *OpenClient) error {
				_, err := client.MkDir(ctx, "parent-cid", "new-dir")
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := newCaptureOpenAPITransport(tt.response)
			client := newTestOpenClient(transport)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()

			if err := tt.action(ctx, client); err != nil {
				t.Fatalf("调用 115 OpenAPI 失败：%v", err)
			}

			req := receiveCapturedRequest(t, transport)
			if req.Method != http.MethodPost {
				t.Fatalf("请求方法 = %s，want %s", req.Method, http.MethodPost)
			}
			gotPath := mustParseURLPath(t, req.URL)
			if gotPath != tt.wantPath {
				t.Fatalf("请求路径 = %s，want %s", gotPath, tt.wantPath)
			}
		})
	}
}
