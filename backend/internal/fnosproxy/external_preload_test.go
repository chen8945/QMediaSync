package fnosproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/coocood/freecache"

	"qmediasync/internal/db"
	"qmediasync/internal/playback"
)

func TestExternalSourceDoesNotRequireLocalMount(t *testing.T) {
	var roots atomic.Int64
	service := NewService(Options{
		OutputRoots: func(context.Context) ([]string, error) {
			roots.Add(1)
			return nil, errors.New("no mount")
		},
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333/qms"}, nil },
	})
	inst := &instance{service: service, ctx: t.Context(), cache: newSourceCache()}
	request := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?api_key=valid", nil)
	const raw = "http://moviepilot:3000/resolve/a%2Fb?token=a%2Bb+z&x=1&x=2"
	for _, source := range []Source{
		{ItemID: "movie", ID: "source", Path: "/unmounted/movie.strm", URL: raw},
		{ItemID: "movie", ID: "source", Path: raw},
	} {
		resolved, ok := inst.resolveSource(t.Context(), request, source)
		if !ok || resolved.url != raw || resolved.source != source || roots.Load() != 0 {
			t.Fatalf("已授权 HTTP 源仍依赖本地文件：resolved=%+v ok=%t roots=%d", resolved, ok, roots.Load())
		}
	}
	for _, raw := range []string{
		"http://private-qms:12333/qms/115/newurl?pickcode=one&userid=owner",
		"http://private-qms:12333/qms/115/newurl?pickcode=one&pickcode=two",
		"http://private-qms:12333/qms/115/%75rl/movie?pickcode=one",
		"http://private-qms:12333/qms/foo/../115/newurl?pickcode=one",
		"http://private-qms:12333/qms/../qms/115/newurl?pickcode=one",
		"http://private-qms:12333/qms/foo/../../qms/115/newurl?pickcode=one",
		"http://private-qms:12333/qms/115/url/?pickcode=one",
	} {
		if _, ok := inst.resolveSource(t.Context(), request, Source{ItemID: "movie", ID: "source", Path: "/unmounted/movie.strm", URL: raw}); ok {
			t.Fatal("自有端点绕过了原安全文件核验")
		}
	}
	if roots.Load() != 7 {
		t.Fatal("自有端点没有经过原路径读取")
	}
}

func TestInternalEquivalentOriginsCannotBypassFileValidation(t *testing.T) {
	for _, test := range []struct{ origin, target string }{
		{"http://qms.local/qms", "http://QMS.LOCAL:80/qms"},
		{"http://qms.local/qms", "http://QMS.LOCAL:080/qms"},
		{"http://qms.local:80/qms", "http://QMS.LOCAL/qms"},
		{"https://qms.local/qms", "https://QMS.LOCAL:443/qms"},
		{"https://qms.local/qms", "https://QMS.LOCAL:00443/qms"},
		{"https://qms.local:443/qms", "https://QMS.LOCAL/qms"},
		{"http://[::1]/qms", "http://[::1]:80/qms"},
		{"https://[::1]:443/qms", "https://[::1]/qms"},
	} {
		t.Run(test.origin+"_"+test.target, func(t *testing.T) {
			var roots atomic.Int64
			service := NewService(Options{
				OutputRoots: func(context.Context) ([]string, error) {
					roots.Add(1)
					return nil, errors.New("no mount")
				},
				KnownOrigins: func(context.Context) ([]string, error) { return []string{test.origin}, nil },
			})
			inst := &instance{service: service, ctx: t.Context(), cache: newSourceCache()}
			request := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?api_key=valid", nil)
			for _, query := range []string{"pickcode=one&userid=owner", "pickcode=one&pickcode=two"} {
				raw := test.target + "/115/newurl?" + query
				target, internal := inst.classifyInternalURL(t.Context(), raw)
				if !internal || (target != nil) != (query == "pickcode=one&userid=owner") {
					t.Fatalf("等价来源未按自有端点分类：target=%v internal=%t", target, internal)
				}
				if _, ok := inst.resolveSource(t.Context(), request, Source{ItemID: "movie", ID: "source", Path: "/unmounted/movie.strm", URL: raw}); ok {
					t.Fatal("默认端口差异绕过自有文件或参数校验")
				}
			}
			if roots.Load() != 2 {
				t.Fatal("等价自有地址未经过原安全文件读取")
			}
		})
	}
}

func TestInternalRootEndpointsWithConfiguredPrefix(t *testing.T) {
	root := t.TempDir()
	service := NewService(Options{
		OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://qms.local/qms"}, nil },
	})
	inst := &instance{service: service, ctx: t.Context(), cache: newSourceCache()}
	request := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?api_key=valid", nil)
	for _, test := range []struct{ endpoint, query string }{
		{"/115/url/video.mkv", "pickcode=one&userid=owner"},
		{"/115/newurl", "pickcode=one&userid=owner"},
		{"/baidupan/url/video.mkv", "pickcode=one&userid=owner"},
		{"/openlist/url", "account_id=1&path=%2Fvideo.mkv"},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			for _, query := range []string{test.query, test.query + "&" + test.query} {
				raw := "http://qms.local:80" + test.endpoint + "?" + query
				target, internal := inst.classifyInternalURL(t.Context(), raw)
				if !internal || (target != nil) != (query == test.query) {
					t.Fatalf("配置前缀使自有根端点降级第三方：target=%v internal=%t", target, internal)
				}
				if target != nil && (target.Path != test.endpoint || target.RawQuery != query) {
					t.Fatal("根端点或原始参数被改写")
				}
				if _, ok := inst.resolveSource(t.Context(), request, Source{ItemID: "movie", ID: "source", Path: "/unmounted/movie.strm", URL: raw}); ok {
					t.Fatal("自有根端点绕过原安全文件核验")
				}
			}
			file := filepath.Join(root, "movie.strm")
			raw := "http://qms.local:80" + test.endpoint + "?" + test.query
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			resolved, ok := inst.resolveSource(t.Context(), request, Source{ItemID: "movie", ID: "source", Path: file, URL: raw})
			if !ok || resolved.fileInfo == nil || len(inst.cache.entries) != 1 {
				t.Fatal("已安全核验的自有根端点没有复用原来源缓存")
			}
		})
	}
}

func TestExternalPreloadSharedByCompatibleAndNativePlayback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		oldCache := db.Cache
		db.Cache = db.CacheGlobal{CacheInstance: freecache.NewCache(1 << 20)}
		t.Cleanup(func() { db.Cache = oldCache })
		var resolverCalls, cdnCalls, roots, handoffs atomic.Int64
		cdn := preloadTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cdnCalls.Add(1)
			if r.UserAgent() != "Confirmed Player" || r.Header.Get("Range") != "bytes=0-0" {
				t.Error("第三方预加载没有保留 UA 或使用小 Range")
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusPartialContent)
		}))
		const final = "http://cdn.example.com/video%2Fpart.mkv?token=keep%2Braw&x=1&x=2"
		resolver := preloadTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			resolverCalls.Add(1)
			if r.RequestURI != "/resolve/a%2Fb?token=a%2Bb+z&x=1&x=2" {
				t.Errorf("第三方签名被改写：%s", r.RequestURI)
			}
			for _, name := range []string{"Authorization", "Cookie", "Authx", "X-Emby-Token", "X-Emby-Authorization"} {
				if r.Header.Get(name) != "" {
					t.Errorf("媒体服务器凭据泄漏到第三方：%s", name)
				}
			}
			w.Header().Set("Location", final)
			w.WriteHeader(http.StatusSeeOther)
		}))
		const raw = "http://moviepilot:3000/resolve/a%2Fb?token=a%2Bb+z&x=1&x=2"
		item := map[string]any{"Id": "movie", "Type": "Movie", "Path": "/unmounted/movie.strm",
			"MediaSources": []any{map[string]any{"Id": "source", "Path": raw}}}
		var nativeUA atomic.Value
		nativeUA.Store("Confirmed Player")
		upstream := preloadTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/Users/Me":
				_, _ = io.WriteString(w, `{"Id":"user","Policy":{"EnableMediaPlayback":true}}`)
			case "/Users/user/Items/movie":
				if r.URL.Query().Get("ambiguous") == "true" {
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "Type": "Movie", "MediaSources": []any{
						map[string]any{"Id": "source", "Path": raw}, map[string]any{"Id": "other", "Path": raw + "&version=2"},
					}})
				} else {
					_ = json.NewEncoder(w).Encode(item)
				}
			case nativeStreamPath:
				payload := nativeStreamPayload("/unmounted/movie.strm", raw)
				payload["data"].(map[string]any)["header"] = map[string]any{"User-Agent": []string{nativeUA.Load().(string)}}
				_ = json.NewEncoder(w).Encode(payload)
			default:
				t.Errorf("不应补查未知媒体信息：%s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		options, signs := preloadTestOptions(t, t.TempDir())
		options.OutputRoots = func(context.Context) ([]string, error) {
			roots.Add(1)
			return nil, errors.New("no mount")
		}
		options.MainHandler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) { handoffs.Add(1) })
		service, client, base := startPreloadService(t, upstream, options)
		dial := service.current.transport.DialContext
		service.current.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			switch address {
			case "moviepilot:3000":
				return resolver.Client().Transport.(*http.Transport).DialContext(ctx, network, address)
			case "cdn.example.com:80":
				return cdn.Client().Transport.(*http.Transport).DialContext(ctx, network, address)
			default:
				return dial(ctx, network, address)
			}
		}
		preloadTestRequest(t, client, base, http.MethodGet, "/emby/Users/user/Items/movie?ambiguous=true", "", false)
		time.Sleep(2100 * time.Millisecond)
		synctest.Wait()
		if resolverCalls.Load() != 0 {
			t.Fatal("多源未选定仍预加载第一份第三方地址")
		}
		preloadTestRequest(t, client, base, http.MethodGet, "/emby/Users/user/Items/movie?ambiguous=true&MediaSourceId=source", "", false)
		time.Sleep(2100 * time.Millisecond)
		synctest.Wait()
		waitPreloadWorker(t, options.Preloader)
		if cached, ok := playback.CachedExternalLink(raw, "Confirmed Player"); !ok || cached.URL != final {
			t.Fatalf("第三方详情没有完成共享缓存发布：%+v ok=%t resolver=%d CDN=%d", cached, ok, resolverCalls.Load(), cdnCalls.Load())
		}
		for _, ua := range []string{"Confirmed Player", "Another Player"} {
			request := preloadTestHTTP(t, base+"/emby/Videos/movie/stream?MediaSourceId=source", http.MethodGet, "", false)
			request.Header.Set("User-Agent", ua)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			want := raw
			if ua == "Confirmed Player" {
				want = final
			}
			if response.StatusCode != http.StatusTemporaryRedirect || response.Header.Get("Location") != want {
				t.Fatalf("兼容取流没有按实际 UA 查缓存：HTTP=%d Location=%s", response.StatusCode, response.Header.Get("Location"))
			}
			nativeUA.Store(ua)
			body := preloadTestRequest(t, client, base, http.MethodPost, nativeStreamPath, "{}", true)
			var payload map[string]any
			if json.Unmarshal(body, &payload) != nil {
				t.Fatal("原生响应不是 JSON")
			}
			data := payload["data"].(map[string]any)
			quality := data["direct_link_qualities"].([]any)[0].(map[string]any)
			if quality["url"] != want || !bytes.Contains(body, []byte("9007199254740993")) {
				t.Fatal("原生取流没有复用同一缓存，或修改未知字段")
			}
			request = preloadTestHTTP(t, base+nativeStreamPath, http.MethodPost, "{}", true)
			scope, _ := service.current.preloadScope(request, true)
			service.current.preload.mu.Lock()
			actualUA := service.current.preload.devices[scope].playerUA
			service.current.preload.mu.Unlock()
			if actualUA != ua {
				t.Fatal("第三方原生响应没有记录本次明确实际 UA")
			}
		}
		if resolverCalls.Load() != 1 || cdnCalls.Load() != 1 || roots.Load() != 0 || handoffs.Load() != 0 || len(signs) != 0 {
			t.Fatalf("第三方前台 miss 增加取链或进入自有文件/账号流程：resolver=%d CDN=%d roots=%d handoffs=%d", resolverCalls.Load(), cdnCalls.Load(), roots.Load(), handoffs.Load())
		}
	})
}

func TestExternalNativeCacheMissKeepsOriginalResponse(t *testing.T) {
	const raw = "http://moviepilot:3000/resolve/a%2Fb?x=1&x=2"
	encoded, _ := json.Marshal(nativeStreamPayload("/unmounted/movie.strm", "\""+raw+"\""))
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", "unchanged")
		_, _ = w.Write(encoded)
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{})
	response, err := noRedirectClient().Post(base+nativeStreamPath, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || !bytes.Equal(body, encoded) || response.Header.Get("ETag") != "unchanged" {
		t.Fatal("第三方缓存 miss 没有保留原生响应和原始地址")
	}
}

func TestExternalPlaybackWaitsForExistingPreload(t *testing.T) {
	for _, mode := range []string{"compatible", "native"} {
		runPreloadTest(t, mode, func(t *testing.T) {
			oldCache := db.Cache
			db.Cache = db.CacheGlobal{CacheInstance: freecache.NewCache(1 << 20)}
			t.Cleanup(func() { db.Cache = oldCache })
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int64
			upstream := preloadTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path == "/start" {
					w.Header().Set("Location", "/final")
					w.WriteHeader(http.StatusTemporaryRedirect)
					return
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				w.Header().Set("Content-Type", "video/mp4")
				w.WriteHeader(http.StatusPartialContent)
			}))
			const ua = "Actual Player"
			raw := upstream.URL + "/start"
			identity, run, ok := playback.NewExternalPreload(raw, ua, upstream.Client().Transport)
			if !ok {
				t.Fatal("第三方候选无法构造")
			}
			preloader := playback.NewPreloader()
			preloader.Start(t.Context())
			preloader.Apply(true, 1)
			t.Cleanup(preloader.Stop)
			if !preloader.Submit(playback.PreloadCandidate{Key: identity, Source: identity, Scope: "background", Kind: playback.PreloadNext, Run: run}) {
				t.Fatal("第三方候选不能提交")
			}
			<-entered
			inst := &instance{service: NewService(Options{Preloader: preloader}), ctx: t.Context()}
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/Videos/movie/stream", nil)
			request.Header.Set("User-Agent", ua)
			result := make(chan string, 1)
			go func() {
				resolved := sourceRecord{url: raw}
				if mode == "native" {
					location, _ := inst.nativePlaybackURL(request, resolved)
					result <- location
				} else {
					response := httptest.NewRecorder()
					inst.serveResolved(response, request, resolved)
					result <- response.Header().Get("Location")
				}
			}()
			synctest.Wait()
			select {
			case <-result:
				t.Fatal("后台已在取链时仍提前返回原地址")
			default:
			}
			close(release)
			if got := <-result; got != upstream.URL+"/final" || calls.Load() != 2 {
				t.Fatalf("没有接手同一条在途链路：Location=%s requests=%d", got, calls.Load())
			}
		})
	}
}
