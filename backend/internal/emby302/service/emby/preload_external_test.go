package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coocood/freecache"
	"github.com/gin-gonic/gin"

	"qmediasync/internal/db"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/playback"
)

func TestExternalPreloadSharedWithEmbyPlayback(t *testing.T) {
	for _, path := range []string{"/plugin/%E7%89%87%E5%90%8D%2Fredirect?key=a%2fb+z&x=1&x=2", "/115/newurl?opaque=third-party"} {
		t.Run(path, func(t *testing.T) {
			oldCache := db.Cache
			db.Cache = db.CacheGlobal{CacheInstance: freecache.NewCache(1 << 20)}
			t.Cleanup(func() { db.Cache = oldCache })
			var sourceCalls, cdnCalls atomic.Int32
			const firstUA = "Player/1.0"
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cdnCalls.Add(1)
				wantRange := "bytes=0-0"
				if r.UserAgent() != firstUA {
					wantRange = "bytes=0-1023"
				}
				if r.Header.Get("Range") != wantRange || r.Header.Get(HeaderFullAuthName) != "" || r.Header.Get("Cookie") != "" {
					t.Errorf("CDN headers: %v", r.Header)
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusPartialContent)
			}))
			defer cdn.Close()
			target := func(ua string) string { return cdn.URL + "/movie%2Bmkv?sig=a%2fb+z&x=1&x=2&ua=" + url.QueryEscape(ua) }
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls.Add(1)
				if r.URL.RequestURI() != path {
					t.Errorf("source URL changed: got %q want %q", r.URL.RequestURI(), path)
				}
				w.Header().Set("Location", target(r.UserAgent()))
				w.WriteHeader(http.StatusSeeOther)
			}))
			defer source.Close()
			raw := source.URL + path
			h := newEmbyPreloadHarness(t)
			h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return playback.NewExternalPreload(raw, ua, https.Transport())
			}
			h.adapter.options.Account = playback.ExternalPreloadAccount
			item := preloadTestItem("20", "Movie")
			item.MediaSources[0].Path = raw
			h.nodes["20"] = item
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", firstUA, nil)
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, ok := playback.CachedExternalLink(raw, firstUA); ok {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("第三方详情未完成预加载")
				}
				time.Sleep(10 * time.Millisecond)
			}
			h.core.Stop()
			if h.metadata.Load() != 0 || sourceCalls.Load() != 1 || cdnCalls.Load() != 1 {
				t.Fatalf("已知 HTTP 源应直接取链，不要求 codec/时长或额外 PlaybackInfo：metadata=%d source=%d CDN=%d", h.metadata.Load(), sourceCalls.Load(), cdnCalls.Load())
			}
			router, _ := newSTRMRedirectTestRouter(t, raw, false)
			for index, ua := range []string{firstUA, "Player/2.0"} {
				request := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?MediaSourceId=ms&api_key=test", nil)
				request.Header.Set("User-Agent", ua)
				request.Header.Set("Range", "bytes=0-1023")
				response := httptest.NewRecorder()
				router.ServeHTTP(response, request)
				if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != target(ua) {
					t.Fatalf("UA %q 正式播放未复用正确结果：status=%d location=%q", ua, response.Code, response.Header().Get("Location"))
				}
				if want := int32(index + 1); sourceCalls.Load() != want || cdnCalls.Load() != want {
					t.Fatalf("预加载缓存未复用或跨 UA 复用：source=%d CDN=%d want=%d", sourceCalls.Load(), cdnCalls.Load(), want)
				}
				// 相同路径的其他程序仍保留既有普通 QMS 分支，缓存命中已在上面覆盖。
				if path == "/115/newurl?opaque=third-party" {
					break
				}
			}
		})
	}
}

func TestExternalPreloadLearnsActualPlaybackUA(t *testing.T) {
	for _, path := range []string{"/d/电影.mkv", "/d/%E7%94%B5%E5%BD%B1.mkv", "/d/movie.mkv"} {
		t.Run(path, func(t *testing.T) {
			var failedCalls atomic.Int32
			failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				failedCalls.Add(1)
				w.WriteHeader(http.StatusForbidden)
			}))
			defer failed.Close()
			h := newEmbyPreloadHarness(t)
			defer h.core.Stop()
			h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				if !playback.ValidExternalURL(raw) {
					return "", nil, false
				}
				return playback.ExternalLinkIdentity(raw, ua), func(context.Context, *playback.PreloadTask) playback.PreloadResult {
					h.resolved <- preloadResolved{raw, ua, force}
					return playback.PreloadResult{Outcome: playback.PreloadCached}
				}, true
			}
			for _, id := range []string{"20", "21"} {
				item := preloadTestItem(id, "Movie")
				item.MediaSources[0].Path = failed.URL + path + "?id=" + id
				h.nodes[id] = item
			}
			streamRouter, _ := newSTRMRedirectTestRouter(t, h.nodes["20"].MediaSources[0].Path, false)
			streamRouter.Use(PreloadObserver())
			streamRouter.GET("/Videos/20/stream", Redirect2OpenlistLink)
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1.0", nil)
			if result := h.waitResolved(); result.ua != "Control/1.0" {
				t.Fatalf("首次第三方详情未使用当前 UA：%q", result.ua)
			}
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Actual Player/2.0", nil)
			previousRouter := h.router
			h.router = streamRouter
			response := h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Failed Player/3.0", nil)
			if response.Code != http.StatusTemporaryRedirect || failedCalls.Load() != 1 {
				t.Fatalf("未经过真实失败回退：status=%d calls=%d", response.Code, failedCalls.Load())
			}
			wantLocation := failed.URL + path + "?id=20"
			if path == "/d/电影.mkv" {
				wantLocation = failed.URL + "/d/%e7%94%b5%e5%bd%b1.mkv?id=20"
			}
			if response.Header().Get("Location") != wantLocation {
				t.Fatalf("失败回退改变：Location=%q want=%q", response.Header().Get("Location"), wantLocation)
			}
			h.adapter.mu.Lock()
			for _, device := range h.adapter.devices {
				if device.playerUA != "Actual Player/2.0" || device.playing.UA != "Actual Player/2.0" {
					t.Errorf("失败回退覆盖当前实际 UA：player=%q playing=%q", device.playerUA, device.playing.UA)
				}
			}
			h.adapter.mu.Unlock()
			h.router = previousRouter
			h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1.0", nil)
			if result := h.waitResolved(); result.ua != "Actual Player/2.0" {
				t.Fatalf("第三方实际播放未更新后续 UA：%q", result.ua)
			}
			if h.metadata.Load() != 0 {
				t.Fatalf("第三方 UA 学习增加元数据请求：%d", h.metadata.Load())
			}
		})
	}
}

func TestExternalPreloadSuccessfulResolutionLearnsUA(t *testing.T) {
	for _, test := range []struct {
		name, path string
		cacheHit   bool
		noExpiry   bool
	}{
		{name: "unchanged direct URL", path: "/d/movie.mkv"},
		{name: "Chinese direct URL", path: "/d/电影.mkv"},
		{name: "Chinese target without cache expiry", path: "/115/newurl", noExpiry: true},
		{name: "response cache hit", path: "/d/movie.mkv", cacheHit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			oldCache := db.Cache
			db.Cache = db.CacheGlobal{CacheInstance: freecache.NewCache(1 << 20)}
			t.Cleanup(func() { db.Cache = oldCache })
			var sourceCalls atomic.Int32
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sourceCalls.Add(1)
				if test.noExpiry {
					w.Header().Set("Location", "http://cdn.115cdn.net/电影.mkv")
					w.WriteHeader(http.StatusFound)
					return
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusPartialContent)
			}))
			defer source.Close()
			raw := source.URL + test.path
			h := newEmbyPreloadHarness(t)
			defer h.core.Stop()
			h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return playback.ExternalLinkIdentity(raw, ua), func(context.Context, *playback.PreloadTask) playback.PreloadResult {
					h.resolved <- preloadResolved{raw, ua, force}
					return playback.PreloadResult{Outcome: playback.PreloadCached}
				}, true
			}
			for _, id := range []string{"20", "21"} {
				item := preloadTestItem(id, "Movie")
				item.MediaSources[0].Path = raw + "?id=" + id
				h.nodes[id] = item
			}
			streamRouter, _ := newSTRMRedirectTestRouter(t, h.nodes["20"].MediaSources[0].Path, false)
			streamRouter.Use(PreloadObserver())
			if test.cacheHit {
				streamRouter.Use(cache.CacheableRouteMarker(), cache.RequestCacher())
				t.Cleanup(cache.WaitingForHandleChan)
			}
			handlerCalls := 0
			streamRouter.GET("/Videos/20/stream", func(c *gin.Context) {
				handlerCalls++
				Redirect2OpenlistLink(c)
			})
			detailRouter := h.router
			streamURL := "/Videos/20/stream?MediaSourceId=source-20&case=" + url.QueryEscape(t.TempDir())
			stream := func() *httptest.ResponseRecorder {
				h.router = streamRouter
				response := h.request("GET", streamURL, "device", "Fixture App", "Actual Player/2.0", nil)
				h.router = detailRouter
				if response.Code != http.StatusTemporaryRedirect || response.Header().Get(strmResolvedKey) != "" {
					t.Fatalf("成功取链响应异常：status=%d headers=%v", response.Code, response.Header())
				}
				return response
			}
			if test.cacheHit {
				stream()
				cache.WaitingForHandleChan()
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1.0", nil)
			h.waitResolved()
			response := stream()
			if test.noExpiry && response.Header().Get(cache.HeaderKeyExpired) != "-1" {
				t.Fatalf("无签名期限的成功链接不应缓存：%v", response.Header())
			}
			if handlerCalls != 1 || sourceCalls.Load() != 1 {
				t.Fatalf("响应缓存未真正命中：handler=%d source=%d", handlerCalls, sourceCalls.Load())
			}
			h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1.0", nil)
			if result := h.waitResolved(); result.ua != "Actual Player/2.0" {
				t.Fatalf("成功解析未学习实际 UA：%q", result.ua)
			}
		})
	}
}

func TestExternalPreloadSameOriginStreamSupportsUAAndNext(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
		if !playback.ValidExternalURL(raw) {
			return "", nil, false
		}
		return playback.ExternalLinkIdentity(raw, ua), func(context.Context, *playback.PreloadTask) playback.PreloadResult {
			h.resolved <- preloadResolved{raw, ua, force}
			return playback.PreloadResult{Outcome: playback.PreloadCached}
		}, true
	}
	for _, id := range []string{"20", "21", "22"} {
		item := preloadTestItem(id, "Episode")
		item.MediaSources[0].Path = "http://qms.invalid/moviepilot/redirect?id=" + id
		item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
		h.nodes[id] = item
	}
	h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1.0", nil)
	if result := h.waitResolved(); result.ua != "Control/1.0" {
		t.Fatalf("首次详情 UA = %q", result.ua)
	}
	previousRouter := h.router
	h.router = gin.New()
	h.router.Use(PreloadObserver())
	h.router.GET("/Videos/20/stream", func(c *gin.Context) {
		c.Redirect(http.StatusTemporaryRedirect, "http://qms.invalid/openlist/d/movie.mkv")
	})
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Actual Player/2.0", nil)
	h.router = previousRouter
	h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1.0", nil)
	if result := h.waitResolved(); result.ua != "Actual Player/2.0" {
		t.Fatalf("同域第三方最终地址未更新实际 UA：%q", result.ua)
	}
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/10/Items/22" {
			t.Errorf("非预期后继查询：%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(h.nodes["22"])
	}
	h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1.0", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session"})
	h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1.0", map[string]any{
		"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false,
		"NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "22"}},
	})
	if result := h.waitResolved(); result.path != h.nodes["22"].MediaSources[0].Path || result.ua != "Actual Player/2.0" {
		t.Fatalf("同域成功取流未建立后继资格：%+v", result)
	}
}
