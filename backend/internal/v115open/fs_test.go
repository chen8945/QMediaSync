package v115open

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"
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

func TestFileDetailPathsAcceptOfficialNumericIDs(t *testing.T) {
	var detail FileDetail
	err := json.Unmarshal([]byte(`{"paths":[{"file_id":0,"file_name":"根目录"},{"file_id":2323423573680609857,"file_name":"多端播放"},{"file_id":"legacy-id","file_name":"旧字符串"}]}`), &detail)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Paths) != 3 || detail.Paths[0].FileId != "0" || detail.Paths[1].FileId != "2323423573680609857" || detail.Paths[2].FileId != "legacy-id" {
		t.Fatalf("详情路径 ID 解析错误：%#v", detail.Paths)
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
	client := NewClient(
		1,
		"test-app-id",
		"test-access-token",
		"test-refresh-token",
	)
	client.client.SetTransport(transport)
	return client
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

func TestGetFsListWithOptionsEnablesExplicitOrdering(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name        string
		options     FileListOptions
		customOrder string
	}{
		{name: "默认排序不覆盖网盘设置"},
		{name: "完整排序", options: FileListOptions{Order: "user_ptime", Asc: "0"}, customOrder: "2"},
		{name: "仅排序字段", options: FileListOptions{Order: "file_name"}, customOrder: "2"},
		{name: "仅排序方向", options: FileListOptions{Asc: "1"}, customOrder: "2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := newCaptureOpenAPITransport(`{"state":true,"data":[]}`)
			client := newTestOpenClient(transport)
			if _, err := client.GetFsListWithOptions(t.Context(), "123", true, false, true, 0, 20, tt.options); err != nil {
				t.Fatal(err)
			}
			request := receiveCapturedRequest(t, transport)
			endpoint, err := url.Parse(request.URL)
			if err != nil {
				t.Fatal(err)
			}
			query := endpoint.Query()
			if query.Get("custom_order") != tt.customOrder || query.Get("o") != tt.options.Order || query.Get("asc") != tt.options.Asc {
				t.Fatalf("排序参数错误：%v", query)
			}
			if tt.customOrder == "" && (query.Has("custom_order") || query.Has("o") || query.Has("asc")) {
				t.Fatal("空排序选项仍发送了排序参数")
			}
		})
	}
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

func TestOpenClientCopyWithResultPreservesCandidateIdentity(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	tests := []struct {
		name string
		data string
		want []CopyCandidate
	}{
		{name: "对象数组", data: `[{"file_id":"2323423573680609857","pick_code":"copy-pick"}]`, want: []CopyCandidate{{FileID: "2323423573680609857", PickCode: "copy-pick"}}},
		{name: "数字 ID 数组不丢精度", data: `[2323423573680609857]`, want: []CopyCandidate{{FileID: "2323423573680609857"}}},
		{name: "字符串 ID 数组", data: `["2323423573680609857"]`, want: []CopyCandidate{{FileID: "2323423573680609857"}}},
		{name: "空数组是合法成功结果", data: `[]`},
		{name: "对象数字 ID", data: `[{"file_id":2323423573680609857}]`, want: []CopyCandidate{{FileID: "2323423573680609857"}}},
		{name: "未知对象保持原数据", data: `[{"source_id":"100","target_id":"200"}]`},
		{name: "未知顶层保持原数据", data: `{"ids":[123]}`},
		{name: "拒绝负数小数零和布尔值", data: `[-1,1.5,0,true,{"file_id":null}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := newCaptureOpenAPITransport(`{"state":true,"data":` + tt.data + `}`)
			client := NewPlaybackClient(1, "app", "token", "refresh")
			setPlaybackTestTransport(t, client, transport)
			result, err := client.CopyWithResult(t.Context(), []string{"100"}, "200", true)
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Data) != tt.data || !reflect.DeepEqual(result.Candidates, tt.want) {
				t.Fatalf("复制结果 = %#v，候选期望 %#v", result, tt.want)
			}
			req := receiveCapturedRequest(t, transport)
			values, err := url.ParseQuery(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if values.Get("nodupli") != "0" || values.Get("file_id") != "100" || values.Get("pid") != "200" {
				t.Fatalf("复制参数不匹配：%v", values)
			}
		})
	}
}

func TestOpenClientCopyKeepsLegacyDuplicateFlag(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name      string
		overwrite bool
		state     bool
		response  string
		wantFlag  string
	}{
		{name: "允许重复", overwrite: true, state: true, response: `{"state":true,"data":[]}`, wantFlag: "0"},
		{name: "禁止重复", state: true, response: `{"state":true,"data":[]}`, wantFlag: "1"},
		{name: "保留失败布尔值", response: `{"state":false,"data":[]}`, wantFlag: "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := newCaptureOpenAPITransport(tt.response)
			client := newTestOpenClient(transport)
			ok, err := client.Copy(t.Context(), []string{"1"}, "2", tt.overwrite)
			if err != nil || ok != tt.state {
				t.Fatalf("原 Copy 返回值改变：ok=%v err=%v", ok, err)
			}
			req := receiveCapturedRequest(t, transport)
			values, err := url.ParseQuery(req.Body)
			if err != nil || values.Get("nodupli") != tt.wantFlag {
				t.Fatalf("原 Copy 重复标记改变：%s", req.Body)
			}
		})
	}
}
