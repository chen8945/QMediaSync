package v115open

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"qmediasync/internal/helpers"

	"golang.org/x/time/rate"
	"resty.dev/v3"
)

type playbackTransportFunc func(*http.Request) (*http.Response, error)

func (f playbackTransportFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func playbackTestResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

// 测试替身只替换当前包装，不修改生产共享客户端的配置。
func setPlaybackTestTransport(t *testing.T, client *OpenClient, transport http.RoundTripper) {
	t.Helper()
	client.client = resty.NewWithClient(&http.Client{Transport: transport})
	t.Cleanup(func() {
		client.client.Client().CloseIdleConnections()
		if err := client.client.Close(); err != nil {
			t.Error(err)
		}
	})
}

func TestPlaybackClientsReuseConnectionsAndIsolateCredentials(t *testing.T) {
	withEmptyClientCache(t)
	withUnlimitedOpenAPIRequests(t)
	clients := []*OpenClient{
		NewPlaybackClient(1, "app-1", "token-1", "refresh-1"),
		NewPlaybackClient(2, "app-2", "token-2", "refresh-2"),
	}
	if clients[0].client != clients[1].client {
		t.Fatal("不同操作没有复用 HTTP 客户端")
	}
	var invalid atomic.Int32
	addresses := make(chan string, 66)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		account := strings.TrimPrefix(r.URL.Path, "/")
		if r.Header.Get("Authorization") != "Bearer token-"+account ||
			r.UserAgent() != "agent-"+account || len(r.Cookies()) != 0 {
			invalid.Add(1)
		}
		addresses <- r.RemoteAddr
		http.SetCookie(w, &http.Cookie{Name: "account", Value: account})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"state":true,"data":{}}`)
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	request := func(client *OpenClient) error {
		req := client.client.R().SetMethod(http.MethodGet).
			SetHeader("User-Agent", fmt.Sprintf("agent-%d", client.AccountId))
		_, _, err := client.doPlaybackRequest(ctx, fmt.Sprintf("%s/%d", server.URL, client.AccountId), req, MakeRequestConfig(0, 0, 5), nil)
		return err
	}
	for _, client := range clients {
		if err := request(client); err != nil {
			t.Fatal(err)
		}
	}
	if first, second := <-addresses, <-addresses; first != second {
		t.Fatal("连续播放操作没有复用已建立的连接")
	}
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			client := clients[i%len(clients)]
			GetClient(client.AccountId, "new-app", "new-token", "new-refresh")
			UpdateToken(client.AccountId, "latest-token", "latest-refresh")
			if err := request(client); err != nil {
				t.Errorf("并发请求失败：%v", err)
			}
		})
	}
	wg.Wait()
	if invalid.Load() != 0 {
		t.Fatal("共享连接池发生了凭据、UA 或 Cookie 串用")
	}
}

func TestNewPlaybackClientKeepsOriginalCredentials(t *testing.T) {
	withEmptyClientCache(t)
	withUnlimitedOpenAPIRequests(t)
	client := NewPlaybackClient(12, "old-app", "old-token", "old-refresh")
	GetClient(12, "new-app", "new-token", "new-refresh")
	UpdateToken(12, "latest-token", "latest-refresh")
	var authHeader string
	setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
		authHeader = req.Header.Get("Authorization")
		return playbackTestResponse(req, 200, `{"state":true,"data":{"1":{"url":{"url":"https://example.test/video"}}}}`), nil
	}))
	if _, err := client.GetDownloadURLWithError(t.Context(), "pick", "agent", true); err != nil {
		t.Fatal(err)
	}
	if authHeader != "Bearer old-token" {
		t.Fatal("共享客户端替换影响了独立播放客户端的凭据")
	}
}

func TestPlaybackClientCancellationStopsHTTPRequest(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	client := NewPlaybackClient(1, "app", "token", "refresh")
	started := make(chan struct{})
	stopped := make(chan struct{})
	setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(stopped)
		return nil, req.Context().Err()
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := client.GetDownloadURLWithError(ctx, "pick", "agent", true)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("HTTP 请求未启动")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误 = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("播放调用没有响应取消")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("播放取消未传播至 HTTP 请求")
	}
}

func TestPlaybackClientReturnsErrorsWithoutInnerRetry(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	tests := []struct {
		name      string
		status    int
		body      string
		code      int
		throttled bool
		retryable bool
	}{
		{name: "访问凭证失效", status: 200, body: `{"state":false,"code":40140125,"message":"secret-value"}`, code: ACCESS_TOKEN_EXPIRY_CODE},
		{name: "业务限流", status: 200, body: `{"state":false,"code":770004}`, code: REQUEST_MAX_LIMIT_CODE, throttled: true},
		{name: "errno 限流", status: 200, body: `{"state":false,"errno":770004}`, code: REQUEST_MAX_LIMIT_CODE, throttled: true},
		{name: "操作频率限流", status: 200, body: `{"state":false,"errno":590075}`, code: 590075, throttled: true},
		{name: "副本未上传完整", status: 200, body: `{"state":false,"errno":70004,"message":"secret-value"}`, code: 70004, retryable: true},
		{name: "文档未上传完整", status: 200, body: `{"state":false,"code":31004}`, code: 31004, retryable: true},
		{name: "空间不足", status: 200, body: `{"state":false,"errno":91005}`, code: 91005},
		{name: "HTTP 限流", status: 429, body: "too many requests", throttled: true},
		{name: "服务暂不可用", status: 503, body: "unavailable", retryable: true},
		{name: "未知业务错误", status: 200, body: `{"state":false,"code":990001}`, code: 990001},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			GetGlobalExecutor().SetThrottledForTesting(false)
			t.Cleanup(func() { GetGlobalExecutor().SetThrottledForTesting(false) })
			client := NewPlaybackClient(1, "app", "token", "refresh")
			var calls atomic.Int32
			setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				return playbackTestResponse(req, tt.status, tt.body), nil
			}))
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := client.GetDownloadURLWithError(ctx, "pick", "agent", true)
			var apiErr *OpenAPIError
			if !errors.As(err, &apiErr) || apiErr.Code != tt.code || apiErr.HTTPStatus != tt.status {
				t.Fatalf("未保留原始状态与业务码：%v", err)
			}
			if calls.Load() != 1 {
				t.Fatalf("播放请求执行了 %d 次", calls.Load())
			}
			if IsRateLimited(err) != tt.throttled || IsPlaybackRetryable(err) != tt.retryable {
				t.Fatalf("错误分类不匹配：%v", err)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatal("播放错误回显了远端敏感消息")
			}
		})
	}
}

func TestPlaybackCopyReportsUnsentRateLimitWait(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	executor := GetGlobalExecutor()
	for _, limiterName := range []string{"QPS", "QPM", "QPH"} {
		t.Run(limiterName, func(t *testing.T) {
			limiter := rate.NewLimiter(rate.Every(time.Hour), 1)
			if !limiter.Allow() {
				t.Fatal("无法耗尽初始配额")
			}
			executor.Lock()
			target := &executor.qpsLimiter
			switch limiterName {
			case "QPM":
				target = &executor.qpmLimiter
			case "QPH":
				target = &executor.qphLimiter
			}
			original := *target
			*target = limiter
			executor.Unlock()
			t.Cleanup(func() {
				executor.Lock()
				*target = original
				executor.Unlock()
			})
			client := NewPlaybackClient(1, "app", "token", "refresh")
			transport := newCaptureOpenAPITransport(`{"state":true,"data":[]}`)
			setPlaybackTestTransport(t, client, transport)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := client.CopyWithResult(ctx, []string{"1"}, "2", true)
			if !errors.Is(err, ErrPlaybackRequestNotSent) || ctx.Err() != nil || len(transport.requests) != 0 {
				t.Fatalf("未发送的复制请求缺少标记或已耗尽预算：err=%v，ctx=%v，HTTP=%d", err, ctx.Err(), len(transport.requests))
			}
			if !strings.Contains(err.Error(), limiterName+" 限制错误") ||
				IsRateLimited(err) || IsPlaybackRetryable(err) || executor.GetThrottleStatus().IsThrottled {
				t.Fatalf("未保留等待原因或误判为平台限流/取链重试：%v", err)
			}
		})
	}
}

func TestPlaybackCopyReadFailurePreservesRejection(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, status := range []int{200, 400, 401, 403, 408, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			GetGlobalExecutor().SetThrottledForTesting(false)
			t.Cleanup(func() { GetGlobalExecutor().SetThrottledForTesting(false) })
			client := NewPlaybackClient(1, "app", "token", "refresh")
			var calls atomic.Int32
			setPlaybackTestTransport(t, client, playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				response := playbackTestResponse(req, status, "")
				response.Body = io.NopCloser(io.MultiReader(strings.NewReader(`{"state":`), iotest.ErrReader(io.ErrUnexpectedEOF)))
				return response, nil
			}))
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := client.CopyWithResult(ctx, []string{"1"}, "2", true)
			rejected := status >= 400 && status < 500 && status != 408
			var apiErr *OpenAPIError
			if rejected {
				if !errors.As(err, &apiErr) || apiErr.HTTPStatus != status || apiErr.Code != 0 {
					t.Errorf("截断响应丢失明确拒绝状态：HTTP=%d，err=%v", status, err)
				}
			} else if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Errorf("结果不明应保留读取错误：HTTP=%d，err=%v", status, err)
			}
			if calls.Load() != 1 || ctx.Err() != nil || IsRateLimited(err) != (status == 429) ||
				errors.Is(err, ErrPlaybackRequestNotSent) {
				t.Errorf("请求次数或错误分类不符：HTTP=%d，calls=%d，err=%v", status, calls.Load(), err)
			}
		})
	}
}

func TestPlaybackClientSkipsHTTPDuringGlobalThrottle(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	executor := GetGlobalExecutor()
	executor.SetThrottledForTesting(true)
	t.Cleanup(func() { executor.SetThrottledForTesting(false) })
	transport := newCaptureOpenAPITransport(`{"state":true,"data":[]}`)
	client := NewPlaybackClient(1, "app", "token", "refresh")
	setPlaybackTestTransport(t, client, transport)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err := client.CopyWithResult(ctx, []string{"1"}, "2", true)
	if !IsRateLimited(err) {
		t.Fatalf("全局限流未立即返回限流错误：%v", err)
	}
	if len(transport.requests) != 0 {
		t.Fatal("全局限流期间仍发送了复制请求")
	}
}

func TestPlaybackClientRejectsInvalidInputsWithoutHTTP(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	transport := newCaptureOpenAPITransport(`{"state":true,"data":[]}`)
	client := NewPlaybackClient(1, "app", "token", "refresh")
	setPlaybackTestTransport(t, client, transport)
	if _, err := client.CopyWithResult(t.Context(), nil, "1", true); err == nil {
		t.Fatal("空文件列表未拒绝")
	}
	if _, err := client.CopyWithResult(t.Context(), []string{"1"}, "", true); err == nil {
		t.Fatal("空目标目录未拒绝")
	}
	if _, err := client.CopyWithResult(t.Context(), []string{""}, "1", true); err == nil {
		t.Fatal("空文件 ID 未拒绝")
	}
	if _, err := client.GetDownloadURLWithError(t.Context(), "", "agent", true); err == nil {
		t.Fatal("空提取码未拒绝")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := client.CopyWithResult(ctx, []string{"1"}, "2", true); !errors.Is(err, context.Canceled) {
		t.Fatalf("已取消请求错误 = %v", err)
	}
	if len(transport.requests) != 0 {
		t.Fatal("无效或已取消请求仍发送到了 115")
	}
}

func TestPlaybackClientDoesNotLogSignedURL(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	logFile, err := os.CreateTemp(t.TempDir(), "playback-log")
	if err != nil {
		t.Fatal(err)
	}
	original := helpers.V115Log.Writer()
	originalLevel := helpers.ConfiguredLogLevel()
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	helpers.V115Log.SetOutput(logFile)
	t.Cleanup(func() {
		helpers.SetGlobalLogLevel(originalLevel)
		helpers.V115Log.SetOutput(original)
		if err := logFile.Close(); err != nil {
			t.Error(err)
		}
	})
	const signedURL = "https://download.example/file?token=secret-signature"
	transport := newCaptureOpenAPITransport(`{"state":true,"data":{"1":{"url":{"url":"` + signedURL + `"}}}}`)
	client := NewPlaybackClient(1, "app", "secret-access-token", "refresh")
	setPlaybackTestTransport(t, client, transport)
	ctx := WithPlaybackOperation(t.Context(), "qms-test-operation")
	result, err := client.GetDownloadURLWithError(ctx, "pick", "agent", true)
	if err != nil || result.URL != signedURL {
		t.Fatalf("取链失败：%v", err)
	}
	logs, err := os.ReadFile(logFile.Name())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logs), "secret-signature") || strings.Contains(string(logs), "secret-access-token") {
		t.Fatal("播放日志泄露了带签名 URL 或访问令牌")
	}
	for _, field := range []string{"account_id=1", `operation="qms-test-operation"`, "phase=/open/ufile/downurl", "queue_wait_ms=", "http_ms=", "HTTP=200", "outcome=ok"} {
		if !strings.Contains(string(logs), field) {
			t.Errorf("播放阶段日志缺少 %s", field)
		}
	}
}

func TestPlaybackDownloadResultRejectsAmbiguousIdentity(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name      string
		data      string
		retryable bool
	}{
		{name: "URL 未就绪", data: `{"1":{"pick_code":"pick"}}`, retryable: true},
		{name: "返回其他文件", data: `{"1":{"pick_code":"another","url":{"url":"https://example.test/video"}}}`},
		{name: "多个身份不随机选择", data: `{"1":{"url":{"url":"https://example.test/one"}},"2":{"url":{"url":"https://example.test/two"}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transport := newCaptureOpenAPITransport(`{"state":true,"data":` + tt.data + `}`)
			client := NewPlaybackClient(1, "app", "token", "refresh")
			setPlaybackTestTransport(t, client, transport)
			result, err := client.GetDownloadURLWithError(t.Context(), "pick", "agent", true)
			if result != nil || err == nil || IsPlaybackRetryable(err) != tt.retryable {
				t.Fatalf("身份核验结果错误：%v", err)
			}
		})
	}
}

func TestPlaybackFSMethodsPreserveAPIError(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	actions := []struct {
		name string
		call func(*OpenClient) error
	}{
		{name: "路径详情", call: func(c *OpenClient) error { _, err := c.GetFsDetailByPath(t.Context(), "/多端播放"); return err }},
		{name: "ID 详情", call: func(c *OpenClient) error { _, err := c.GetFsDetailByCid(t.Context(), "1"); return err }},
		{name: "列表", call: func(c *OpenClient) error {
			_, err := c.GetFsList(t.Context(), "0", true, true, true, 0, 20)
			return err
		}},
		{name: "建目录", call: func(c *OpenClient) error { _, err := c.MkDir(t.Context(), "0", "多端播放"); return err }},
		{name: "删除", call: func(c *OpenClient) error { _, err := c.Del(t.Context(), []string{"1"}, "2"); return err }},
	}
	for _, action := range actions {
		t.Run(action.name, func(t *testing.T) {
			transport := newCaptureOpenAPITransport(`{"state":false,"code":40140125,"message":"token expired"}`)
			client := NewPlaybackClient(1, "app", "token", "refresh")
			setPlaybackTestTransport(t, client, transport)
			var apiErr *OpenAPIError
			if err := action.call(client); !errors.As(err, &apiErr) || apiErr.Code != ACCESS_TOKEN_EXPIRY_CODE {
				t.Fatalf("FS 方法丢失了可分类错误：%v", err)
			}
			if len(transport.requests) != 1 {
				t.Fatal("FS 方法在播放客户端中触发了内层重试")
			}
		})
	}
}

func TestPlaybackDeleteAlreadyDeletedDoesNotLogFailure(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	var logs bytes.Buffer
	original := helpers.V115Log.Writer()
	helpers.V115Log.SetOutput(&logs)
	t.Cleanup(func() { helpers.V115Log.SetOutput(original) })
	client := NewPlaybackClient(1, "app", "token", "refresh")
	transport := newCaptureOpenAPITransport(`{"state":false,"errno":231011}`)
	setPlaybackTestTransport(t, client, transport)
	_, err := client.Del(t.Context(), []string{"123"}, "456")
	if !IsAlreadyDeleted(err) || len(transport.requests) != 1 {
		t.Fatalf("删除未保留幂等错误或执行了重试：%v", err)
	}
	if strings.Contains(logs.String(), "调用文件删除接口失败") {
		t.Fatal("已删除的副本仍输出删除失败日志")
	}
}
