package v115open

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

func TestPreloadRequestDoesNotFollowRedirectWhileOrdinaryPlaybackDoes(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	var apiCalls, mediaCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			apiCalls.Add(1)
			http.Redirect(w, r, "/media", http.StatusFound)
			return
		}
		mediaCalls.Add(1)
		_, _ = io.WriteString(w, `{"state":true,"data":{}}`)
	}))
	t.Cleanup(server.Close)
	client := NewPlaybackClient(1, "app", "token", "")
	for _, background := range []bool{true, false} {
		ctx := t.Context()
		if background {
			ctx = WithPreloadOperation(ctx)
		}
		req := client.client.R().SetMethod(http.MethodGet)
		_, _, err := client.doPlaybackRequest(ctx, server.URL+"/api", req, MakeRequestConfig(0, 0, 2), nil)
		if background && err == nil || !background && err != nil {
			t.Fatalf("background=%v error=%v", background, err)
		}
		if background && mediaCalls.Load() != 0 {
			t.Fatal("预加载跟随 API 跳转访问了媒体地址")
		}
	}
	if apiCalls.Load() != 2 || mediaCalls.Load() != 1 {
		t.Fatalf("API=%d media=%d", apiCalls.Load(), mediaCalls.Load())
	}
}

func TestPreloadRequestBodyLimitAndRetryAfter(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/risk" {
			w.Header().Set("Retry-After", "1200")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"state":false,"errno":590075,"message":"do-not-log-secret"}`)
			return
		}
		_, _ = io.WriteString(w, `{"state":true,"data":{"padding":"`+strings.Repeat("x", 70000)+`"}}`)
	}))
	t.Cleanup(server.Close)
	client := NewPlaybackClient(1, "app", "token", "")
	call := func(ctx context.Context, path string) error {
		req := client.client.R().SetMethod(http.MethodGet)
		_, _, err := client.doPlaybackRequest(ctx, server.URL+path, req, MakeRequestConfig(0, 0, 2), nil)
		return err
	}
	if err := call(WithPreloadOperation(t.Context()), "/body"); err == nil {
		t.Fatal("后台接受了超限响应")
	}
	if err := call(t.Context(), "/body"); err != nil {
		t.Fatalf("后台限额影响了普通播放：%v", err)
	}
	err := call(WithPreloadOperation(t.Context()), "/risk")
	apiErr, ok := errors.AsType[*OpenAPIError](err)
	if !ok || apiErr.HTTPStatus != 429 || apiErr.Code != 590075 || apiErr.RetryAfter != 20*time.Minute {
		t.Fatalf("风控分类未保留：%v", err)
	}
	GetGlobalExecutor().SetThrottledForTesting(false)
	if calls.Load() != 3 {
		t.Fatalf("后台执行了隐式重试：%d", calls.Load())
	}
}

func TestPreloadRequestDoesNotWaitForProviderLogger(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	executor := GetGlobalExecutor()
	executor.SetThrottledForTesting(false)
	t.Cleanup(func() { executor.SetThrottledForTesting(false) })
	previousLevel := helpers.ConfiguredLogLevel()
	helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
	t.Cleanup(func() { helpers.SetGlobalLogLevel(previousLevel) })
	blocked, release := make(chan struct{}, 1), make(chan struct{})
	previousLogger := helpers.V115Log
	helpers.V115Log = &helpers.QLogger{Logger: log.New(preloadBlockingWriter{blocked: blocked, release: release}, "", 0)}
	t.Cleanup(func() { helpers.V115Log = previousLogger })
	defer close(release)
	client := NewPlaybackClient(1, "app", "private-token", "")
	setPlaybackTestTransport(t, client, playbackTransportFunc(func(r *http.Request) (*http.Response, error) {
		return playbackTestResponse(r, 429, `{"state":false,"errno":590075,"message":"private-url"}`), nil
	}))
	done := make(chan error, 1)
	go func() {
		_, err := client.GetDownloadURLWithError(WithPreloadOperation(t.Context()), "fixture-pick", "player", false)
		done <- err
	}()
	select {
	case err := <-done:
		if !IsRateLimited(err) || !executor.throttleManager.IsThrottled() {
			t.Fatal("静默日志改变了全局风控状态")
		}
	case <-blocked:
		t.Fatal("后台请求在持同键锁期间写提供方日志")
	case <-time.After(time.Second):
		t.Fatal("后台请求未及时结束")
	}
}

type preloadBlockingWriter struct {
	blocked chan<- struct{}
	release <-chan struct{}
}

func (w preloadBlockingWriter) Write(p []byte) (int, error) {
	select {
	case w.blocked <- struct{}{}:
	default:
	}
	<-w.release
	return len(p), nil
}
