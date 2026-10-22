package controllers

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/helpers"
)

func TestGetOpenListFileURLPropagatesCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(strconv.FormatBool(deadline), func(t *testing.T) {
			started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			account := setupOpenListFileOperationTest(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.ReadAll(r.Body)
				close(started)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
					_, _ = io.WriteString(w, `{"code":200,"data":{"raw_url":"https://cdn.example/video"}}`)
				}
			})
			previousLogger := helpers.AppLogger
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			t.Cleanup(func() { helpers.AppLogger = previousLogger })
			router := gin.New()
			router.GET("/openlist/url", GetOpenListFileUrl)
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/openlist/url?account_id="+strconv.Itoa(int(account.ID))+"&path=/movie.mp4", nil).WithContext(ctx)
			response := httptest.NewRecorder()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				router.ServeHTTP(response, request)
			}()
			defer func() {
				close(release)
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("取链处理器未结束")
				}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("没有发送 OpenList 文件详情请求")
			}
			if !deadline {
				cancel()
			}
			for _, signal := range []<-chan struct{}{canceled, finished} {
				select {
				case <-signal:
				case <-time.After(3 * time.Second):
					t.Fatal("请求取消或超时没有传到实际 OpenList 请求")
				}
			}
			if response.Code == http.StatusFound || response.Header().Get("Location") != "" {
				t.Fatal("取消后仍返回播放直链")
			}
		})
	}
}
