package emby

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/emby302/config"
	"qmediasync/emby302/web/cache"
	"qmediasync/internal/helpers"
)

func TestProxySubtitlesCachesOnlyCompleteResponses(t *testing.T) {
	for _, mode := range []string{"client_cancel", "upstream_read_error", "complete"} {
		t.Run(mode, func(t *testing.T) {
			partial := "WEBVTT\n\n"
			if mode == "client_cancel" {
				// 足够大的首块越过 HTTP 缓冲，让客户端在回源结束前收到数据。
				partial += strings.Repeat("00:00.000 --> 00:01.000\nsubtitle\n\n", 1024)
			}
			complete := partial + "00:01.000 --> 00:02.000\nlast subtitle\n"
			var calls atomic.Int32
			release := make(chan struct{})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				w.Header().Set("Content-Type", "text/vtt")
				w.Header().Set("Content-Length", strconv.Itoa(len(complete)))
				if call == 1 && mode != "complete" {
					_, _ = io.WriteString(w, partial)
					w.(http.Flusher).Flush()
					if mode == "client_cancel" {
						select {
						case <-r.Context().Done():
						case <-release:
						}
					}
					return
				}
				_, _ = io.WriteString(w, complete)
			}))
			defer origin.Close()
			oldConfig, oldLogger := config.C, helpers.AppLogger
			config.C = &config.Config{Emby: &config.Emby{Host: origin.URL}}
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			t.Cleanup(func() { config.C, helpers.AppLogger = oldConfig, oldLogger })

			router := gin.New()
			router.Use(cache.CacheableRouteMarker(), cache.RequestCacher())
			router.GET("/Videos/1/Subtitles/0/Stream.vtt", ProxySubtitles)
			finished := make(chan error, 3)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				router.ServeHTTP(w, r)
				finished <- r.Context().Err()
			}))
			defer proxy.Close()
			defer close(release)
			client := proxy.Client()
			client.Timeout = 5 * time.Second
			target := proxy.URL + "/Videos/1/Subtitles/0/Stream.vtt?case=" + strconv.FormatInt(time.Now().UnixNano(), 10)
			waitFinished := func() error {
				t.Helper()
				select {
				case err := <-finished:
					cache.WaitingForHandleChan()
					return err
				case <-time.After(5 * time.Second):
					t.Fatal("proxy handler did not finish")
					return nil
				}
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "client_cancel" {
				_, err = io.ReadFull(resp.Body, make([]byte, 1))
				cancel()
				_ = resp.Body.Close()
				if err != nil {
					t.Fatalf("first streamed byte: %v", err)
				}
				if err = waitFinished(); !errors.Is(err, context.Canceled) {
					t.Fatalf("inbound request error = %v, want context.Canceled", err)
				}
			} else {
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if mode == "upstream_read_error" {
					if string(body) != partial || !errors.Is(readErr, io.ErrUnexpectedEOF) {
						t.Fatalf("partial response = %q, error = %v", body, readErr)
					}
				} else if string(body) != complete || readErr != nil {
					t.Fatalf("complete response = %q, error = %v", body, readErr)
				}
				if err = waitFinished(); err != nil {
					t.Fatalf("inbound request unexpectedly canceled: %v", err)
				}
			}

			for range 2 {
				resp, err := client.Get(target)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				waitFinished()
				if resp.StatusCode != http.StatusOK || readErr != nil || string(body) != complete {
					t.Fatalf("following response: status=%d, bytes=%d, error=%v; want %d complete bytes",
						resp.StatusCode, len(body), readErr, len(complete))
				}
			}
			wantCalls := int32(2)
			if mode == "complete" {
				wantCalls = 1
			}
			if got := calls.Load(); got != wantCalls {
				t.Fatalf("origin calls = %d, want %d; complete responses must remain cacheable", got, wantCalls)
			}
		})
	}
}

func TestProxyPassHandlersDoNotRetryPartialResponses(t *testing.T) {
	for _, tt := range []struct {
		name      string
		handler   gin.HandlerFunc
		failWrite bool
	}{
		{name: "origin_read", handler: ProxyOrigin},
		{name: "download_read", handler: DownloadStrategyChecker()},
		{name: "episodes_read", handler: ResortEpisodes},
		{name: "origin_write", handler: ProxyOrigin, failWrite: true},
		{name: "download_write", handler: DownloadStrategyChecker(), failWrite: true},
		{name: "episodes_write", handler: ResortEpisodes, failWrite: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Length", "10")
				if tt.failWrite {
					w.Header().Set("Content-Length", "4")
				}
				_, _ = io.WriteString(w, "part")
			}))
			defer origin.Close()
			oldConfig, oldLogger := config.C, helpers.AppLogger
			config.C = &config.Config{Emby: &config.Emby{
				Host: origin.URL, DownloadStrategy: config.DlStrategyOrigin, ProxyErrorStrategy: config.PeStrategyOrigin,
			}}
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			t.Cleanup(func() { config.C, helpers.AppLogger = oldConfig, oldLogger })
			router := gin.New()
			router.GET("/Items/1/Download", tt.handler)
			rec := httptest.NewRecorder()
			var writer http.ResponseWriter = rec
			wantBody := "part"
			if tt.failWrite {
				writer = partialFailingResponseWriter{ResponseWriter: rec}
				wantBody = "pa"
			}
			router.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, "/Items/1/Download", nil))
			if got := calls.Load(); got != 1 || rec.Body.String() != wantBody {
				t.Fatalf("origin calls = %d, body = %q; partial response must not be retried or appended", got, rec.Body.String())
			}
			if got := rec.Header().Get(cache.HeaderKeyExpired); got != "-1" {
				t.Fatalf("Expired = %q, want -1 after a response copy failure", got)
			}
		})
	}
}

type partialFailingResponseWriter struct {
	http.ResponseWriter
}

func (w partialFailingResponseWriter) Write(body []byte) (int, error) {
	n, _ := w.ResponseWriter.Write(body[:min(2, len(body))])
	return n, errors.New("client write failed")
}
