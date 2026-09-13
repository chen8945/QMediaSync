package v115open

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"qmediasync/internal/helpers"
)

// 保留实测的混合分页结构、字符串 cid，以及超过浮点数和 int64 范围的数字 ID。
const recycleListFixture = `{"state":true,"message":"","code":0,"data":{"offset":0,"limit":200,"count":"2","rb_pass":1,"3517378013962437756":{"id":"3517378013962437756","file_name":"qms-0123456789abcdef0123456789abcdef","type":"2","file_size":"0","dtime":"1789320134","status":"0","cid":"3349194923640482178","parent_name":"多端播放"},"922337203685477580801":{"id":922337203685477580801,"file_name":"test.mkv","type":1,"dtime":1789320135,"status":-1,"cid":922337203685477580802,"parent_name":"其他目录"}}}`

func newRecycleTestClient(t *testing.T, playback bool, transport http.RoundTripper) *OpenClient {
	t.Helper()
	client := NewClient(1, "app", "test-token", "test-refresh")
	client.playback = playback
	client.client.SetTransport(transport)
	t.Cleanup(func() {
		if err := client.client.Close(); err != nil {
			t.Error(err)
		}
	})
	return client
}

func TestListRecycle(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, playback := range []bool{false, true} {
		t.Run(fmt.Sprintf("播放客户端=%t", playback), func(t *testing.T) {
			var calls atomic.Int32
			client := newRecycleTestClient(t, playback, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				query := req.URL.Query()
				if req.Method != http.MethodGet || req.URL.Path != "/open/rb/list" || len(query) != 2 || query.Get("offset") != "0" || query.Get("limit") != "200" {
					t.Errorf("列表请求不符合契约：%s %s", req.Method, req.URL)
				}
				if req.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("列表请求未使用凭据快照")
				}
				return playbackTestResponse(req, http.StatusOK, recycleListFixture), nil
			}))
			result, err := client.ListRecycle(t.Context(), 0, RecyclePageLimit)
			if err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || result.Offset != 0 || result.Limit != 200 || result.Count != 2 || len(result.Entries) != 2 {
				t.Fatalf("列表分页或请求次数错误：%+v，calls=%d", result, calls.Load())
			}
			for _, entry := range result.Entries {
				switch entry.ID.String() {
				case "3517378013962437756":
					if entry.ParentID.String() != "3349194923640482178" || entry.Type != "2" || entry.Status != "0" || entry.DeletedAt != "1789320134" || entry.ParentName != "多端播放" {
						t.Errorf("实测条目解析错误：%+v", entry)
					}
				case "922337203685477580801":
					if entry.ParentID.String() != "922337203685477580802" || entry.Type != "1" || entry.Status != "-1" {
						t.Errorf("数字字段丢失精度：%+v", entry)
					}
				default:
					t.Errorf("未知 ID：%s", entry.ID)
				}
			}
			if client.playback != playback || client.credentialSnapshot().accessToken != "test-token" {
				t.Fatal("回收站方法修改了原客户端")
			}
		})
	}
}

func TestListRecycleValidatesPaginationAndIdentity(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name, body string
		wantError  bool
	}{
		{name: "空回收站", body: `{"state":true,"code":0,"data":{"offset":0,"limit":200,"count":"0","rb_pass":1}}`},
		{name: "缺少 offset", body: strings.Replace(recycleListFixture, `"offset":0,`, "", 1), wantError: true},
		{name: "缺少 limit", body: strings.Replace(recycleListFixture, `"limit":200,`, "", 1), wantError: true},
		{name: "缺少 count", body: strings.Replace(recycleListFixture, `"count":"2",`, "", 1), wantError: true},
		{name: "负偏移", body: strings.Replace(recycleListFixture, `"offset":0`, `"offset":-1`, 1), wantError: true},
		{name: "负总数", body: strings.Replace(recycleListFixture, `"count":"2"`, `"count":"-1"`, 1), wantError: true},
		{name: "小数总数", body: strings.Replace(recycleListFixture, `"count":"2"`, `"count":1.5`, 1), wantError: true},
		{name: "总数溢出", body: strings.Replace(recycleListFixture, `"count":"2"`, `"count":"922337203685477580801"`, 1), wantError: true},
		{name: "空分页字段", body: strings.Replace(recycleListFixture, `"limit":200`, `"limit":null`, 1), wantError: true},
		{name: "错误页码", body: strings.Replace(recycleListFixture, `"offset":0`, `"offset":200`, 1), wantError: true},
		{name: "零分页量", body: strings.Replace(recycleListFixture, `"limit":200`, `"limit":0`, 1), wantError: true},
		{name: "分页量超限", body: strings.Replace(recycleListFixture, `"limit":200`, `"limit":201`, 1), wantError: true},
		{name: "条目多于总数", body: strings.Replace(recycleListFixture, `"count":"2"`, `"count":"1"`, 1), wantError: true},
		{name: "ID 与索引不符", body: strings.Replace(recycleListFixture, `"id":"3517378013962437756"`, `"id":"2"`, 1), wantError: true},
		{name: "零 ID", body: strings.ReplaceAll(recycleListFixture, "3517378013962437756", "0"), wantError: true},
		{name: "无效父目录 ID 不能回显凭据", body: strings.Replace(recycleListFixture, `"cid":"3349194923640482178"`, `"cid":"secret-token"`, 1), wantError: true},
		{name: "错误数组结构", body: `{"state":true,"code":0,"data":[]}`, wantError: true},
		{name: "空 data", body: `{"state":true,"code":0,"data":null}`, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := newCaptureOpenAPITransport(tt.body)
			client := newRecycleTestClient(t, false, transport)
			result, err := client.ListRecycle(t.Context(), 0, RecyclePageLimit)
			if (err != nil) != tt.wantError || len(transport.requests) != 1 {
				t.Fatalf("列表验证错误：result=%+v，err=%v，calls=%d", result, err, len(transport.requests))
			}
			if err != nil && strings.Contains(err.Error(), "secret-token") {
				t.Fatal("解析错误回显了凭据")
			}
			if !tt.wantError && (result.Count != 0 || len(result.Entries) != 0) {
				t.Fatalf("空列表解析错误：%+v", result)
			}
		})
	}
}

func TestListRecycleKeepsEntriesWithUntrustedDeletionTimes(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name, replacement string
	}{
		{name: "无效删除时间", replacement: `"dtime":"invalid",`},
		{name: "空删除时间", replacement: `"dtime":"",`},
		{name: "缺失删除时间"},
		{name: "null 删除时间", replacement: `"dtime":null,`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(recycleListFixture, `"dtime":"1789320134",`, tt.replacement, 1)
			transport := newCaptureOpenAPITransport(body)
			client := newRecycleTestClient(t, false, transport)
			result, err := client.ListRecycle(t.Context(), 0, RecyclePageLimit)
			if err != nil {
				t.Fatal(err)
			}
			if result.Count != 2 || len(result.Entries) != 2 || len(transport.requests) != 1 {
				t.Fatalf("不可信时间影响了整页结果：%+v", result)
			}
			for _, entry := range result.Entries {
				if entry.ID == "3517378013962437756" && entry.DeletedAt != "" {
					t.Errorf("不可信时间没有留空：%q", entry.DeletedAt)
				}
				if entry.ID == "922337203685477580801" && entry.DeletedAt != "1789320135" {
					t.Errorf("同页有效条目的时间发生变化：%q", entry.DeletedAt)
				}
			}
		})
	}
}

func TestRecycleWrites(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	const largeID = "922337203685477580801"
	for _, playback := range []bool{false, true} {
		for _, tt := range []struct {
			name, body string
			restore    bool
			ids        []string
			wantError  bool
		}{
			{name: "实测删除空数组", body: `[]`},
			{name: "文档删除 ID 数组", body: `["` + largeID + `","42"]`},
			{name: "删除最大批量", body: `[]`, ids: strings.Split(strings.Repeat("1,", RecycleBatchLimit-1)+"2", ",")},
			{name: "实测还原空数组", restore: true, body: `[]`},
			{name: "还原带空白的空数组", restore: true, body: "[ \n\t ]"},
			{name: "还原逐项成功", restore: true, body: `{"` + largeID + `":{"state":true,"errno":0},"42":{"state":true,"errno":0}}`},
			{name: "还原逐项失败", restore: true, body: `{"` + largeID + `":{"state":true,"errno":0},"42":{"state":false,"errno":7,"error":"secret-token"}}`, wantError: true},
			{name: "还原有错误码", restore: true, body: `{"` + largeID + `":{"state":true,"errno":7},"42":{"state":true,"errno":0}}`, wantError: true},
			{name: "还原缺少 ID", restore: true, body: `{"` + largeID + `":{"state":true,"errno":0}}`, wantError: true},
			{name: "还原缺少逐项状态", restore: true, body: `{"` + largeID + `":{},"42":{"state":true}}`, wantError: true},
			{name: "还原缺少逐项错误码", restore: true, body: `{"` + largeID + `":{"state":true},"42":{"state":true,"errno":0}}`, wantError: true},
			{name: "还原逐项错误码为 null", restore: true, body: `{"` + largeID + `":{"state":true,"errno":null},"42":{"state":true,"errno":0}}`, wantError: true},
			{name: "还原未知非空数组", restore: true, body: `[{"state":true}]`, wantError: true},
			{name: "还原 null", restore: true, body: `null`, wantError: true},
			{name: "还原缺少 data", restore: true, wantError: true},
		} {
			t.Run(fmt.Sprintf("播放客户端=%t/%s", playback, tt.name), func(t *testing.T) {
				ids := tt.ids
				if ids == nil {
					ids = []string{largeID, "42"}
				}
				path := "/open/rb/del"
				if tt.restore {
					path = "/open/rb/revert"
				}
				var calls atomic.Int32
				client := newRecycleTestClient(t, playback, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					if req.Method != http.MethodPost || req.URL.Path != path || req.URL.RawQuery != "" || req.Header.Get("Authorization") != "Bearer test-token" {
						t.Errorf("回收站写请求错误：%s %s", req.Method, req.URL)
					}
					if err := req.ParseMultipartForm(1 << 20); err != nil {
						t.Errorf("回收站写请求不是 multipart：%v", err)
					} else {
						defer req.MultipartForm.RemoveAll()
						if len(req.MultipartForm.Value) != 1 || req.FormValue("tid") != strings.Join(ids, ",") {
							t.Error("tid 缺失、精度丢失或混入了额外字段")
						}
					}
					body := `{"state":true,"message":"","code":0`
					if tt.body != "" {
						body += `,"data":` + tt.body
					}
					return playbackTestResponse(req, http.StatusOK, body+`}`), nil
				}))
				var err error
				if tt.restore {
					err = client.RestoreRecycle(t.Context(), ids)
				} else {
					err = client.DeleteRecycle(t.Context(), ids)
				}
				if (err != nil) != tt.wantError || calls.Load() != 1 {
					t.Fatalf("回收站写操作错误：err=%v，calls=%d", err, calls.Load())
				}
				if err != nil {
					var apiErr *OpenAPIError
					if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 200 || strings.Contains(err.Error(), "secret-token") {
						t.Fatalf("还原错误未保留安全状态：%v", err)
					}
				}
			})
		}
	}
}

func TestRecycleRejectsInvalidInputsWithoutHTTP(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	transport := newCaptureOpenAPITransport(`{"state":true,"data":[]}`)
	client := newRecycleTestClient(t, false, transport)
	for _, page := range [][2]int{{-1, 200}, {0, 0}, {0, -1}, {0, 201}} {
		if _, err := client.ListRecycle(t.Context(), page[0], page[1]); err == nil {
			t.Errorf("未拒绝无效分页：%v", page)
		}
	}
	for _, ids := range [][]string{nil, {}, {""}, {"0"}, {"000"}, {"-1"}, {"+1"}, {"1.5"}, {"1e2"}, {" 1"}, {"１"}, {"1,2"}, {"1", ""}, strings.Split(strings.Repeat("1,", RecycleBatchLimit)+"2", ",")} {
		if err := client.DeleteRecycle(t.Context(), ids); err == nil {
			t.Errorf("删除未拒绝无效 ID，count=%d", len(ids))
		}
		if err := client.RestoreRecycle(t.Context(), ids); err == nil {
			t.Errorf("还原未拒绝无效 ID，count=%d", len(ids))
		}
	}
	if len(transport.requests) != 0 {
		t.Fatal("无效参数仍然发送了回收站请求")
	}
}

func TestRecycleErrorsPreserveStatusWithoutSecrets(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, playback := range []bool{false, true} {
		for _, tt := range []struct {
			name, body   string
			status, code int
		}{
			{name: "业务失败", status: 200, code: 430004, body: `{"state":false,"code":430004,"message":"secret-token"}`},
			{name: "状态失败但错误码为零", status: 200, body: `{"state":false,"code":0,"message":"secret-token"}`},
			{name: "状态成功但错误码非零", status: 200, code: 7, body: `{"state":true,"code":7,"message":"secret-token"}`},
			{name: "errno 非零", status: 200, code: 9, body: `{"state":true,"errno":9,"error":"secret-token"}`},
			{name: "授权失败", status: 401, code: ACCESS_TOKEN_EXPIRY_CODE, body: `{"state":false,"code":40140125,"message":"secret-token"}`},
			{name: "HTTP 限流", status: 429, body: `{"state":true,"data":[]}`},
			{name: "网关无效 JSON", status: 503, body: `<html>secret-token</html>`},
			{name: "HTTP 非成功", status: 302, body: `{"state":true,"data":[]}`},
		} {
			t.Run(fmt.Sprintf("播放客户端=%t/%s", playback, tt.name), func(t *testing.T) {
				GetGlobalExecutor().SetThrottledForTesting(false)
				t.Cleanup(func() { GetGlobalExecutor().SetThrottledForTesting(false) })
				var calls atomic.Int32
				client := newRecycleTestClient(t, playback, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					return playbackTestResponse(req, tt.status, tt.body), nil
				}))
				for _, call := range []func() error{
					func() error { _, err := client.ListRecycle(t.Context(), 0, 200); return err },
					func() error { return client.DeleteRecycle(t.Context(), []string{"1"}) },
					func() error { return client.RestoreRecycle(t.Context(), []string{"1"}) },
				} {
					GetGlobalExecutor().SetThrottledForTesting(false)
					err := call()
					var apiErr *OpenAPIError
					if !errors.As(err, &apiErr) || apiErr.Code != tt.code || apiErr.HTTPStatus != tt.status || strings.Contains(err.Error(), "secret-token") {
						t.Fatalf("丢失安全状态：%v", err)
					}
				}
				if calls.Load() != 3 {
					t.Fatalf("单次 API 出现额外请求：%d", calls.Load())
				}
			})
		}
	}
}

func TestRecycleCancellationDoesNotRetry(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, before := range []bool{true, false} {
		for _, name := range []string{"list", "delete", "restore"} {
			t.Run(fmt.Sprintf("%s/请求前取消=%t", name, before), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if before {
					cancel()
				}
				var calls atomic.Int32
				client := newRecycleTestClient(t, false, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
					calls.Add(1)
					cancel()
					<-req.Context().Done()
					return nil, req.Context().Err()
				}))
				var err error
				switch name {
				case "list":
					_, err = client.ListRecycle(ctx, 0, 200)
				case "delete":
					err = client.DeleteRecycle(ctx, []string{"1"})
				case "restore":
					err = client.RestoreRecycle(ctx, []string{"1"})
				}
				wantCalls := int32(1)
				if before {
					wantCalls = 0
				}
				if !errors.Is(err, context.Canceled) || calls.Load() != wantCalls {
					t.Fatalf("取消没有传播或请求重发：err=%v，calls=%d", err, calls.Load())
				}
			})
		}
	}
}

func TestRecycleDoesNotLogRemoteMessages(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	original := helpers.V115Log.Writer()
	var logs strings.Builder
	helpers.V115Log.SetOutput(&logs)
	t.Cleanup(func() { helpers.V115Log.SetOutput(original) })
	client := newRecycleTestClient(t, false, newCaptureOpenAPITransport(`{"state":false,"code":7,"message":"secret-token","error":"secret-refresh","data":{"token":"secret-token"}}`))
	if err := client.DeleteRecycle(t.Context(), []string{"1"}); err == nil {
		t.Fatal("业务失败没有返回错误")
	}
	if strings.Contains(logs.String(), "secret-token") || strings.Contains(logs.String(), "secret-refresh") {
		t.Fatal("回收站日志回显远端凭据")
	}
}
