package v115open

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelOnceGuardedChecksBeforeHTTPAndNeverReplays(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	guardErr := errors.New("删除授权已变化")
	for _, tt := range []struct {
		name     string
		guardErr error
		body     string
		status   int
		ioErr    error
		wantOK   bool
	}{
		{name: "guard rejected", guardErr: guardErr},
		{name: "success", body: `{"state":true}`, wantOK: true},
		{name: "partial failure", body: `{"state":false,"code":430004}`},
		{name: "unknown response", body: `not json`},
		{name: "transport failure", ioErr: io.ErrUnexpectedEOF},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"state":false,"code":430004}`},
		{name: "forbidden", status: http.StatusForbidden, body: `{"state":false,"code":430004}`},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `{"state":false,"code":430004}`},
		{name: "server error", status: http.StatusServiceUnavailable, body: `{"state":false,"code":430004}`},
		{name: "expired token", body: `{"state":false,"code":40140125}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(1, "app", "token", "refresh")
			guardCalls, httpCalls := 0, 0
			client.client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				httpCalls++
				if guardCalls != 1 {
					t.Errorf("删除发送前 guard 调用次数=%d", guardCalls)
				}
				body, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				values, err := url.ParseQuery(string(body))
				if err != nil || req.Method != http.MethodPost || req.URL.Path != "/open/ufile/delete" || values.Get("file_ids") != "9001,9002,9003" || values.Get("parent_id") != "7001" {
					t.Errorf("错误删除请求：%s %s body=%s err=%v", req.Method, req.URL, body, err)
				}
				const wantBody = "file_ids=9001%2C9002%2C9003&parent_id=7001"
				if string(body) != wantBody {
					t.Errorf("删除表单编码或长度不符：body=%s bytes=%d want=%d", body, len(body), len(wantBody))
				}
				if tt.ioErr != nil {
					return nil, tt.ioErr
				}
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				return playbackTestResponse(req, status, tt.body), nil
			}))
			ok, err := client.DelOnceGuarded(t.Context(), []string{"9001", "9002", "9003"}, "7001", func() error {
				guardCalls++
				return tt.guardErr
			})
			if ok != tt.wantOK || (err == nil) != tt.wantOK || guardCalls != 1 {
				t.Fatalf("ok=%v err=%v guardCalls=%d", ok, err, guardCalls)
			}
			wantHTTP := 1
			if tt.guardErr != nil {
				wantHTTP = 0
				if !errors.Is(err, guardErr) {
					t.Fatalf("guard 原错误丢失：%v", err)
				}
			}
			if httpCalls != wantHTTP {
				t.Fatalf("HTTP 次数=%d，期望 %d", httpCalls, wantHTTP)
			}
		})
	}
}

func TestDelOnceGuardedInFlightCancellationDoesNotReplay(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	client := NewClient(1, "app", "token", "refresh")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	transportDone := make(chan struct{})
	var calls atomic.Int64
	var transportCanceled atomic.Bool
	client.client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
		defer close(transportDone)
		calls.Add(1)
		cancel()
		select {
		case <-req.Context().Done():
			transportCanceled.Store(true)
			return nil, req.Context().Err()
		case <-time.After(3 * time.Second):
			return nil, errors.New("HTTP 请求未继承删除取消")
		}
	}))
	ok, err := client.DelOnceGuarded(ctx, []string{"9001"}, "7001", func() error { return nil })
	select {
	case <-transportDone:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后 HTTP 请求未结束")
	}
	if ok || !errors.Is(err, context.Canceled) || calls.Load() != 1 || !transportCanceled.Load() {
		t.Fatalf("取消后删除未收敛：ok=%v error=%v HTTP=%d", ok, err, calls.Load())
	}
}

func TestDelOnceGuardedCancellationAfterCheckDoesNotSend(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	transport := newCaptureOpenAPITransport(`{"state":true}`)
	client := newTestOpenClient(transport)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan struct{})
	ok, err := client.DelOnceGuarded(ctx, []string{"9001"}, "7001", func() error {
		defer close(finished)
		cancel()
		return nil
	})
	<-finished
	if ok || !errors.Is(err, context.Canceled) || len(transport.requests) != 0 {
		t.Fatalf("取消仍发出删除：ok=%v error=%v calls=%d", ok, err, len(transport.requests))
	}
}

func TestDeletionQueueRechecksAfterWaiting(t *testing.T) {
	ensureOpenAPITestLoggers()
	executor := NewQueueExecutor(1000, 60000, 3600000)
	executor.workerCount = 1
	executor.Start()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() { unblock(); executor.Stop() })
	client := NewClient(1, "app", "token", "refresh")
	var calls atomic.Int64
	client.client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return playbackTestResponse(req, 200, `{"state":true}`), nil
	}))
	first := &QueuedRequest{
		URL: OPEN_BASE_URL + "/open/ufile/delete", Method: http.MethodPost,
		Request: client.client.R().SetMethod(http.MethodPost).SetResponseDoNotParse(true),
		Ctx:     t.Context(), ResponseChan: make(chan *RequestResponse, 1), CreatedAt: time.Now(),
	}
	executor.EnqueueRequest(first)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("首个请求未执行")
	}
	var allowed atomic.Bool
	allowed.Store(true)
	var guardCalls atomic.Int64
	guardErr := errors.New("等待期间授权失效")
	second := &QueuedRequest{
		URL: OPEN_BASE_URL + "/open/ufile/delete", Method: http.MethodPost,
		Request: client.client.R().SetMethod(http.MethodPost).SetResponseDoNotParse(true),
		Ctx:     t.Context(), ResponseChan: make(chan *RequestResponse, 1), CreatedAt: time.Now(),
		BeforeSend: func() error {
			guardCalls.Add(1)
			if !allowed.Load() {
				return guardErr
			}
			return nil
		},
	}
	executor.EnqueueRequest(second)
	if guardCalls.Load() != 0 {
		t.Fatal("guard 提前在排队前执行")
	}
	allowed.Store(false)
	unblock()
	<-first.ResponseChan
	select {
	case response := <-second.ResponseChan:
		if !errors.Is(response.Error, guardErr) || calls.Load() != 1 || guardCalls.Load() != 1 {
			t.Fatalf("排队后未拒绝删除：error=%v HTTP=%d guard=%d", response.Error, calls.Load(), guardCalls.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("排队核验未返回")
	}
}
