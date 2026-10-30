package emby

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/helpers"
)

func TestProxyOriginCancellationLogLevel(t *testing.T) {
	for _, mode := range []string{"client_cancel", "deadline", "upstream_read", "client_write", "uncanceled_context_error", "canceled_with_write_error"} {
		t.Run(mode, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "client_cancel" {
					close(started)
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				w.Header().Set("Content-Length", "4")
				if mode == "upstream_read" {
					w.Header().Set("Content-Length", "10")
				}
				_, _ = io.WriteString(w, "part")
			}))
			defer origin.Close()
			defer close(release)
			var output bytes.Buffer
			oldConfig, oldLogger, oldLevel := config.C, helpers.AppLogger, helpers.ConfiguredLogLevel()
			config.C = &config.Config{Emby: &config.Emby{Host: origin.URL}}
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
			helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
			t.Cleanup(func() {
				config.C, helpers.AppLogger = oldConfig, oldLogger
				helpers.SetGlobalLogLevel(oldLevel)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "deadline" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer deadlineCancel()
			}
			rec := httptest.NewRecorder()
			var writer http.ResponseWriter = rec
			switch mode {
			case "client_write", "canceled_with_write_error":
				writer = canceledProxyWriter{ResponseWriter: rec, err: errors.New("client write failed"), cancel: cancel, cancelOnWrite: mode == "canceled_with_write_error"}
			case "uncanceled_context_error":
				writer = canceledProxyWriter{ResponseWriter: rec, err: context.Canceled}
			}
			router := gin.New()
			router.GET("/Items/1/Images/Primary", ProxyOrigin)
			finished := make(chan struct{})
			go func() {
				router.ServeHTTP(writer, httptest.NewRequestWithContext(ctx, http.MethodGet, "/Items/1/Images/Primary", nil))
				close(finished)
			}()
			if mode == "client_cancel" {
				select {
				case <-started:
					cancel()
				case <-time.After(5 * time.Second):
					t.Fatal("upstream request did not start")
				}
			}
			select {
			case <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("proxy request did not finish")
			}
			want := "[ERROR] 代理异常:"
			if mode == "client_cancel" {
				want = "[DEBUG] 代理请求已取消:"
			}
			if got := output.String(); !strings.Contains(got, want) || (mode == "client_cancel" && strings.Contains(got, "[ERROR]")) {
				t.Fatalf("log = %q, want %q", got, want)
			}
			if got := rec.Header().Get(cache.HeaderKeyExpired); got != "-1" {
				t.Fatalf("Expired = %q, want -1", got)
			}
		})
	}
}

type canceledProxyWriter struct {
	http.ResponseWriter
	err           error
	cancel        context.CancelFunc
	cancelOnWrite bool
}

func (w canceledProxyWriter) Write([]byte) (int, error) {
	if w.cancelOnWrite {
		w.cancel()
	}
	return 0, w.err
}
