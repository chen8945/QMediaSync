package v115open

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

func TestDeletionDetailMissingStopsRetryAndFailureLog(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name string
		body string
		code int
	}{
		{name: "missing directory", body: `{"state":false,"code":430004,"message":"missing"}`, code: 430004},
		{name: "deleted file", body: `{"state":false,"code":231011,"message":"deleted"}`, code: 231011},
		{name: "errno response", body: `{"state":false,"errno":430004,"error":"missing"}`, code: 430004},
		{name: "numeric state", body: `{"state":0,"code":430004,"message":"missing"}`, code: 430004},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			original := helpers.V115Log.Writer()
			helpers.V115Log.SetOutput(&logs)
			t.Cleanup(func() { helpers.V115Log.SetOutput(original) })
			transport := newCaptureOpenAPITransport(tt.body)
			client := newTestOpenClient(transport)
			detail, err := client.GetFsDetailByCidForDeletion(t.Context(), "9001")
			apiErr, ok := errors.AsType[*OpenAPIError](err)
			if detail != nil || !ok || apiErr.Code != tt.code || !IsAlreadyDeleted(err) {
				t.Fatalf("detail=%+v error=%v", detail, err)
			}
			if got := len(transport.requests); got != 1 {
				t.Errorf("明确缺失发出 %d 次请求，期望 1 次", got)
			}
			request := receiveCapturedRequest(t, transport)
			parsed, parseErr := url.Parse(request.URL)
			if parseErr != nil || request.Method != http.MethodGet || parsed.Path != "/open/folder/get_info" || parsed.Query().Get("file_id") != "9001" {
				t.Fatalf("错误的原 ID 核验请求：%+v，解析错误=%v", request, parseErr)
			}
			if strings.Contains(logs.String(), "重试") || strings.Contains(logs.String(), "[ERROR]") || strings.Contains(logs.String(), "调用文件详情接口失败") {
				t.Errorf("明确缺失仍记录重试或失败：%s", logs.String())
			}
		})
	}
}

func TestDeletionDetailDoesNotChangeOrdinaryLookupRetries(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	transport := newCaptureOpenAPITransport(`{"state":false,"code":430004,"message":"missing"}`)
	client := newTestOpenClient(transport)
	if _, err := client.GetFsDetailByCidForDeletion(t.Context(), "9001"); !IsAlreadyDeleted(err) {
		t.Fatal(err)
	}
	receiveCapturedRequest(t, transport)
	if _, err := client.GetFsDetailByCid(t.Context(), "9001"); !IsAlreadyDeleted(err) {
		t.Fatal(err)
	}
	if got := len(transport.requests); got != 4 {
		t.Fatalf("普通详情请求次数=%d，期望保持 4 次", got)
	}
}

func TestDeletionDetailRetriesUnconfirmedFailures(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name   string
		status int
		body   string
		err    error
	}{
		{name: "transport", err: io.ErrUnexpectedEOF},
		{name: "unauthorized", status: 401, body: `{"state":false,"code":430004}`},
		{name: "forbidden", status: 403, body: `{"state":false,"code":430004}`},
		{name: "http not found", status: 404, body: `{"state":false,"code":430004}`},
		{name: "rate limit", status: 429, body: `{"state":false,"code":430004}`},
		{name: "server error", status: 503, body: `{"state":false,"code":430004}`},
		{name: "unknown business error", status: 200, body: `{"state":false,"code":20018,"message":"文件不存在"}`},
		{name: "malformed response", status: 200, body: `not json`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(1, "app", "token", "refresh")
			calls := 0
			client.client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					if tt.err != nil {
						return nil, tt.err
					}
					return playbackTestResponse(req, tt.status, tt.body), nil
				}
				return playbackTestResponse(req, 200, `{"state":true,"data":{"file_id":"9001","file_name":"movie.mkv","paths":[{"file_id":"7001","file_name":"movie"}]}}`), nil
			}))
			detail, err := client.GetFsDetailByCidForDeletion(t.Context(), "9001")
			if err != nil || detail == nil || detail.FileId != "9001" || calls != 2 {
				t.Fatalf("未确认失败应重试读取，detail=%+v error=%v calls=%d", detail, err, calls)
			}
		})
	}
}

func TestDeletionDetailRejectsUnconfirmedEmptyResponses(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name string
		body string
	}{
		{name: "empty data", body: `{"state":true,"data":{}}`},
		{name: "null data", body: `{"state":true,"data":null}`},
		{name: "null response", body: `null`},
		{name: "plain absence text", body: `{"state":false,"message":"文件不存在"}`},
		{name: "missing state", body: `{}`},
		{name: "absence without state", body: `{"code":430004}`},
		{name: "absence with null state", body: `{"state":null,"code":430004}`},
		{name: "success with absence code", body: `{"state":true,"code":430004}`},
		{name: "numeric success with absence code", body: `{"state":1,"code":430004}`},
		{name: "success with absence errno", body: `{"state":true,"errno":430004}`},
		{name: "absence with conflicting errno", body: `{"state":false,"code":430004,"errno":40140125}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := newTestOpenClient(newCaptureOpenAPITransport(tt.body))
			detail, err := client.GetFsDetailByCidForDeletion(t.Context(), "9001")
			if detail != nil || err == nil || IsAlreadyDeleted(err) {
				t.Fatalf("无证明响应被当成缺失：detail=%+v error=%v", detail, err)
			}
		})
	}
}

func TestDeletionDetailRetryPolicyPreservesResponseAndCancellation(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	body := `{"state":false,"code":430004,"message":"missing"}`
	transport := newCaptureOpenAPITransport(body)
	client := newTestOpenClient(transport)
	options := &RequestConfig{
		MaxRetries: 3,
		RetryDelay: time.Hour,
		Timeout:    time.Second,
		RetryIf:    func(err error) bool { return !IsAlreadyDeleted(err) },
	}
	requestCtx, cancelRequest := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelRequest()
	response, raw, err := client.doAuthRequest(requestCtx, OPEN_BASE_URL+"/open/folder/get_info", client.client.R().SetMethod("GET"), options, nil)
	if response == nil || response.StatusCode() != 200 || string(raw) != body || !IsAlreadyDeleted(err) || len(transport.requests) != 1 {
		t.Fatalf("终态响应未保真：response=%v body=%s error=%v calls=%d", response, raw, err, len(transport.requests))
	}
	receiveCapturedRequest(t, transport)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.GetFsDetailByCidForDeletion(ctx, "9001"); !errors.Is(err, context.Canceled) || IsAlreadyDeleted(err) {
		t.Fatalf("取消未优先返回：%v", err)
	}
	if len(transport.requests) != 0 {
		t.Fatal("取消后仍发送 HTTP")
	}
}
