package openlist_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/service/openlist"
)

func TestFetchContextCancellation(t *testing.T) {
	for _, tt := range []struct {
		name          string
		nilContext    bool
		parentTimeout time.Duration
		cancelParent  bool
		responseBody  bool
		redirect      bool
	}{
		{name: "default_timeout_waiting_for_headers"},
		{name: "nil_context_timeout", nilContext: true},
		{name: "shorter_parent_timeout", parentTimeout: 2 * time.Second},
		{name: "default_timeout_reading_body", responseBody: true},
		{name: "shorter_parent_timeout_reading_body", parentTimeout: 2 * time.Second, responseBody: true},
		{name: "redirect_shares_timeout", responseBody: true, redirect: true},
		{name: "parent_cancellation_after_redirect", cancelParent: true, responseBody: true, redirect: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setOpenlistConfig(t, "http://openlist.invalid")
				ctx := context.Background()
				wantDuration := 30 * time.Second
				wantError := context.DeadlineExceeded
				if tt.nilContext {
					ctx = nil
				} else if tt.cancelParent {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					defer cancel()
					wantDuration = 15 * time.Second
					wantError = context.Canceled
					time.AfterFunc(wantDuration, cancel)
				} else if tt.parentTimeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tt.parentTimeout)
					defer cancel()
					wantDuration = tt.parentTimeout
				}

				started := time.Now()
				requestCount := 0
				var body *cancellationBody
				client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					requestCount++
					if _, ok := req.Context().Deadline(); !ok {
						return nil, errors.New("请求缺少截止时间")
					}
					if tt.redirect && req.URL.Path != "/result" {
						time.Sleep(10 * time.Second)
						return &http.Response{
							StatusCode: http.StatusTemporaryRedirect,
							Header:     http.Header{"Location": {"/result"}},
							Body:       http.NoBody,
							Request:    req,
						}, nil
					}
					if tt.responseBody {
						body = &cancellationBody{ctx: req.Context()}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: req}, nil
					}
					<-req.Context().Done()
					return nil, req.Context().Err()
				})}
				client.CheckRedirect = func(*http.Request, []*http.Request) error {
					return http.ErrUseLastResponse
				}

				err := openlist.FetchContext(ctx, client, "/api/fs/get", http.MethodPost, nil, nil, nil, false)
				if !errors.Is(err, wantError) {
					t.Fatalf("请求错误 = %v，期望 %v", err, wantError)
				}
				if elapsed := time.Since(started); elapsed != wantDuration {
					t.Fatalf("请求耗时 = %v，期望 %v", elapsed, wantDuration)
				}
				wantRequests := 1
				if tt.redirect {
					wantRequests = 2
				}
				if requestCount != wantRequests {
					t.Fatalf("请求次数 = %d，期望 %d", requestCount, wantRequests)
				}
				if tt.responseBody && (body == nil || !body.closed) {
					t.Fatal("读取失败后未关闭响应体")
				}
			})
		})
	}
}

func TestFetchContextSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != "/api/fs/get" {
			t.Errorf("请求 = %s %s", req.Method, req.URL.Path)
		}
		if req.Header.Get("Authorization") != "test-openlist-token" {
			t.Error("未使用 OpenList Token")
		}
		_, _ = io.WriteString(w, `{"code":200,"data":{"raw_url":"https://media.invalid/movie.mkv"}}`)
	}))
	defer server.Close()
	setOpenlistConfig(t, server.URL)
	header := http.Header{"Authorization": {"original"}}
	var result openlist.FsGet
	if err := openlist.FetchContext(t.Context(), server.Client(), "/api/fs/get", http.MethodPost, header, map[string]any{"path": "/movie.mkv"}, &result, false); err != nil {
		t.Fatal(err)
	}
	if result.RawUrl != "https://media.invalid/movie.mkv" {
		t.Fatalf("文件直链 = %q", result.RawUrl)
	}
	if header.Get("Authorization") != "original" || header.Get("Content-Type") != "" {
		t.Fatalf("调用方请求头被修改：%v", header)
	}
}

func setOpenlistConfig(t *testing.T, host string) {
	t.Helper()
	previous := config.C
	config.C = &config.Config{Openlist: &config.Openlist{Host: host, Token: "test-openlist-token"}}
	t.Cleanup(func() { config.C = previous })
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type cancellationBody struct {
	ctx    context.Context
	closed bool
}

func (b *cancellationBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}

func (b *cancellationBody) Close() error {
	b.closed = true
	return nil
}
