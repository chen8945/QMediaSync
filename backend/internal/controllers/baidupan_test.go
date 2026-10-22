package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/fnosproxy"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func setupBaiduPlaybackTest(t *testing.T) (*gin.Engine, string) {
	t.Helper()
	setupControllerTestDB(t, &models.Account{})
	setup115PlaybackCache(t)
	previousLogger := helpers.BaiduPanLog
	helpers.BaiduPanLog = helpers.AppLogger
	t.Cleanup(func() { helpers.BaiduPanLog = previousLogger })
	account := &models.Account{SourceType: models.SourceTypeBaiduPan, UserId: "baidu-playback-user", Token: "fixture-token"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	router.GET("/baidupan/url/*filename", GetBaiduPanUrlByPickCode)
	return router, "/baidupan/url/video.mp4?userid=" + account.UserId + "&pickcode=123"
}

func TestGetBaiduPanURLPropagatesCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deadline    bool
		lateSuccess bool
	}{
		{name: "请求取消"},
		{name: "请求超时", deadline: true},
		{name: "取消后接口仍返回成功", lateSuccess: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router, target := setupBaiduPlaybackTest(t)
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			previousTransport := http.DefaultTransport
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				started <- r.Context()
				select {
				case <-r.Context().Done():
					if tc.lateSuccess {
						return proxy115TestResponse(r, http.StatusOK, `{"errno":0,"list":[{"dlink":"https://d.pcs.baidu.com/file?id=123"}]}`, nil), nil
					}
					return nil, r.Context().Err()
				case <-release:
					return nil, context.Canceled
				}
			})
			t.Cleanup(func() { http.DefaultTransport = previousTransport })
			ctx, cancel := context.WithCancel(t.Context())
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
			request.Header.Set("User-Agent", "Baidu Player")
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
					t.Error("百度取链处理器未结束")
				}
			}()
			var outbound context.Context
			select {
			case outbound = <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("没有发送百度文件详情请求")
			}
			if tc.deadline {
				want, _ := ctx.Deadline()
				if got, ok := outbound.Deadline(); !ok || !got.Equal(want) {
					t.Fatal("百度出站请求没有继承元数据截止时间")
				}
			} else {
				cancel()
			}
			for _, signal := range []<-chan struct{}{outbound.Done(), finished} {
				select {
				case <-signal:
				case <-time.After(3 * time.Second):
					t.Fatal("请求取消或超时没有传到实际百度请求")
				}
			}
			if response.Code == http.StatusFound || response.Header().Get("Location") != "" {
				t.Fatal("取消后仍返回播放直链")
			}
			if len(db.Cache.Get("baidupanurl:123, ua=Baidu Player")) != 0 {
				t.Fatal("取消后仍回填百度直链缓存")
			}
		})
	}
}

func TestGetBaiduPanURLCancelsCacheLockWait(t *testing.T) {
	router, target := setupBaiduPlaybackTest(t)
	const cacheKey = "baidupanurl:123, ua=Waiting Player"
	if !keyLock.lockContext(t.Context(), cacheKey, time.Second) {
		t.Fatal("无法占用测试缓存锁")
	}
	var calls atomic.Int64
	previousTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, context.Canceled
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
	request.Header.Set("User-Agent", "Waiting Player")
	response := httptest.NewRecorder()
	started, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		close(started)
		router.ServeHTTP(response, request)
	}()
	defer func() {
		keyLock.Unlock(cacheKey)
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("缓存锁释放后取链处理器未结束")
		}
	}()
	<-started
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("取消后仍等待被占用的百度缓存锁")
	}
	if calls.Load() != 0 || response.Code == http.StatusFound || response.Header().Get("Location") != "" {
		t.Fatal("缓存锁等待取消后仍发起取链或返回直链")
	}
}

func TestGetBaiduPanURLKeepsCacheAndLocalProxy(t *testing.T) {
	router, target := setupBaiduPlaybackTest(t)
	previousTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	calls := 0
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("method") != "filemetas" || r.URL.Query().Get("dlink") != "1" {
			t.Error("未请求百度文件下载链接")
		}
		return proxy115TestResponse(r, http.StatusOK, `{"errno":0,"list":[{"dlink":"https://d.pcs.baidu.com/file?id=123"}]}`, nil), nil
	})
	for _, ua := range []string{"First Player", "First Player", "Second Player"} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("User-Agent", ua)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		location, err := url.Parse(response.Header().Get("Location"))
		if err != nil || response.Code != http.StatusFound || location.Path != "/proxy-115" ||
			location.Query().Get("baidupan") != "1" || location.Query().Get("url") != "https://d.pcs.baidu.com/file?id=123&access_token=fixture-token" {
			t.Fatal("百度播放不再转交既有本地代理")
		}
		if len(db.Cache.Get(fmt.Sprintf("baidupanurl:123, ua=%s", ua))) == 0 {
			t.Fatal("正常取链没有缓存成功结果")
		}
	}
	if calls != 2 {
		t.Fatalf("百度详情请求次数 = %d，期望同 UA 命中缓存、不同 UA 独立取链", calls)
	}
}

func TestFnosNativePlaybackStopCancelsBaiduDetail(t *testing.T) {
	router, target := setupBaiduPlaybackTest(t)
	started, canceled, release := make(chan context.Context, 1), make(chan struct{}), make(chan struct{})
	previousTransport := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("method") != "filemetas" {
			return nil, fmt.Errorf("非百度文件详情请求：%s", r.URL.Path)
		}
		started <- r.Context()
		select {
		case <-r.Context().Done():
			close(canceled)
			return nil, r.Context().Err()
		case <-release:
			return nil, context.Canceled
		}
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	root := t.TempDir()
	sourceURL := "http://private-qms:12333" + target
	file := filepath.Join(root, "episode.strm")
	if err := os.WriteFile(file, []byte(sourceURL), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"file_stream":           map[string]any{"guid": "baidu-file", "path": file, "type": 1, "can_play": 1, "play_error": ""},
			"video_stream":          map[string]any{"media_guid": "baidu-file", "codec_type": "video"},
			"cloud_storage_info":    map[string]any{"valid": true, "disabled": false, "cloud_storage_type": 9001},
			"direct_link_qualities": []any{map[string]any{"url": sourceURL, "is_m3u8": false}},
		}})
	}))
	defer upstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	service := fnosproxy.NewService(fnosproxy.Options{
		HTTPHost:     "127.0.0.1:12333",
		HTTPPort:     port,
		MainHandler:  router,
		OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
	})
	if err := service.Start(models.FnosProxyConfig{Enabled: true, FnosURL: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/v/api/v1/stream", port), "application/json", strings.NewReader(`{}`))
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	defer func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("飞牛原生播放请求未结束")
		}
	}()
	var outgoing context.Context
	select {
	case outgoing = <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("飞牛原生播放未进入真实百度取链控制器")
	}
	if deadline, ok := outgoing.Deadline(); !ok || time.Until(deadline) <= 25*time.Second || time.Until(deadline) > 30*time.Second {
		t.Fatal("飞牛的 30 秒元数据期限没有传到百度出站请求")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := service.Stop(ctx); err != nil {
		t.Fatalf("停止飞牛实例没有在期限内完成：%v", err)
	}
	if outgoing.Err() != context.Canceled {
		t.Fatal("停止飞牛实例后百度出站请求仍未取消")
	}
	for _, signal := range []<-chan struct{}{canceled, finished} {
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatal("停止飞牛实例后百度取链或原生播放仍未结束")
		}
	}
}
