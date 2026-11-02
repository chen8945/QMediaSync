package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coocood/freecache"

	"qmediasync/internal/db"
	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/emby302/web/webport"
	"qmediasync/internal/playback"
)

func TestMediaResponsePathsDisplayDecodedURLs(t *testing.T) {
	for _, source := range []struct{ name, path, want string }{
		{
			"MoviePilot 查询参数",
			"http://moviepilot:3000/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=opaque&file_name=%E7%94%B5%E5%BD%B1%2001%26%23%2B.mkv&x=2&x=1",
			"http://moviepilot:3000/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=opaque&file_name=电影 01&#+.mkv&x=2&x=1",
		},
		{
			"LitePan 签名路径",
			"https://litepan/api/strm/play/1/ZmlsZQ/t/token/n/%E7%94%B5%E5%BD%B1%3B01.mkv/s/path-signature",
			"https://litepan/api/strm/play/1/ZmlsZQ/t/token/n/电影;01.mkv/s/path-signature",
		},
		{
			"OpenList 原始查询串",
			"http://openlist:5244/d/%E7%94%B5%E5%BD%B1%2F01.mkv?sign=a%2fb+z&x=2&x=1",
			"http://openlist:5244/d/电影/01.mkv?sign=a/b+z&x=2&x=1",
		},
		{"本地相对路径", "media/%E7%94%B5%E5%BD%B1%2001.mkv", "media/电影 01.mkv"},
		{"只解码一次并保留加号", "http://strm.test/file%252Fname.mkv?name=A%2526B+C", "http://strm.test/file%2Fname.mkv?name=A%26B+C"},
		{"非法转义回退", "http://strm.test/file.mkv?name=%ZZ", "http://strm.test/file.mkv?name=%ZZ"},
	} {
		t.Run(source.name, func(t *testing.T) {
			router, _ := newSTRMRedirectTestRouter(t, source.path, false)
			router.Use(MediaPathDisplay())
			config.C.VideoPreview = &config.VideoPreview{}
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				sources := []any{map[string]any{"Id": "ms", "Path": source.path, "Container": "mkv"}}
				item := map[string]any{"Id": "movie", "Type": "Folder", "Path": source.path, "MediaSources": sources}
				var body any
				switch r.URL.Path {
				case "/Items/movie/PlaybackInfo":
					body = map[string]any{"MediaSources": sources}
				case "/Users/10/Items/20":
					body = item
				case "/Users/10/Items":
					body = map[string]any{"Items": []any{item}}
				case "/Users/10/Items/Latest":
					body = []any{item}
				default:
					t.Errorf("unexpected upstream path %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_ = json.NewEncoder(w).Encode(body)
			}))
			defer origin.Close()
			config.C.Emby.Host = origin.URL
			router.POST("/Items/movie/PlaybackInfo", TransferPlaybackInfo)
			router.GET("/Users/10/Items/20", LoadCacheItems)
			router.GET("/Users/10/Items", ProxyAddItemsPreviewInfo)
			router.GET("/Users/10/Items/Latest", ProxyLatestItems)
			for _, route := range []struct{ name, method, path string }{
				{"PlaybackInfo", http.MethodPost, "/Items/movie/PlaybackInfo"},
				{"单项详情", http.MethodGet, "/Users/10/Items/20"},
				{"普通列表", http.MethodGet, "/Users/10/Items?IncludeItemTypes=Movie"},
				{"最新项目", http.MethodGet, "/Users/10/Items/Latest"},
			} {
				t.Run(route.name, func(t *testing.T) {
					response := httptest.NewRecorder()
					path := route.path + "?api_key=test"
					if route.name == "普通列表" {
						path = route.path + "&api_key=test"
					}
					router.ServeHTTP(response, httptest.NewRequest(route.method, path, nil))
					if response.Code != http.StatusOK {
						t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
					}
					if response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) {
						t.Fatalf("Content-Length=%s body=%d", response.Header().Get("Content-Length"), response.Body.Len())
					}
					type itemPaths struct {
						Path         string
						MediaSources []struct{ Path string }
					}
					var item itemPaths
					switch route.name {
					case "普通列表":
						var list struct{ Items []itemPaths }
						if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
							t.Fatal(err)
						}
						if len(list.Items) != 1 {
							t.Fatalf("Items=%v", list.Items)
						}
						item = list.Items[0]
					case "最新项目":
						var list []itemPaths
						if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
							t.Fatal(err)
						}
						if len(list) != 1 {
							t.Fatalf("latest=%v", list)
						}
						item = list[0]
					default:
						if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
							t.Fatal(err)
						}
					}
					got := item.Path
					if route.name != "单项详情" {
						if len(item.MediaSources) != 1 {
							t.Fatalf("MediaSources=%v", item.MediaSources)
						}
						got = item.MediaSources[0].Path
					}
					if got != source.want {
						t.Fatalf("Path changed: got %q want %q", got, source.want)
					}
				})
			}
		})
	}
}

func TestMediaResponsePathsPreserveLiveItems(t *testing.T) {
	const raw = "http://strm.test/live%2Fstream?name=A%26B"
	const displayed = "http://strm.test/live/stream?name=A&B"
	router, _ := newSTRMRedirectTestRouter(t, raw, false)
	router.Use(MediaPathDisplay())
	config.C.VideoPreview = &config.VideoPreview{}
	live := map[string]any{
		"Id": "live", "Type": "TvChannel", "Path": raw,
		"MediaSources": []any{map[string]any{"Id": "live-source", "Path": raw}},
	}
	normal := map[string]any{
		"Id": "normal", "Type": "Folder", "Path": raw,
		"MediaSources": []any{map[string]any{"Id": "normal-source", "Path": raw}},
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/Users/10/Items/20":
			body = live
		case "/Users/10/Items":
			body = map[string]any{"Items": []any{live, normal}}
		case "/Users/10/Items/Latest":
			body = []any{live, normal}
		default:
			t.Errorf("unexpected live upstream request: %s", r.URL.RequestURI())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer origin.Close()
	config.C.Emby.Host = origin.URL
	router.GET("/Users/10/Items/20", LoadCacheItems)
	router.GET("/Users/10/Items", ProxyAddItemsPreviewInfo)
	router.GET("/Users/10/Items/Latest", ProxyLatestItems)
	for _, path := range []string{"/Users/10/Items/20?api_key=test", "/Users/10/Items?IncludeItemTypes=Movie&api_key=test", "/Users/10/Items/Latest?api_key=test"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			type itemPaths struct {
				Id, Path     string
				MediaSources []struct{ Path string }
			}
			var items []itemPaths
			switch path {
			case "/Users/10/Items/20?api_key=test":
				items = make([]itemPaths, 1)
				if err := json.Unmarshal(response.Body.Bytes(), &items[0]); err != nil {
					t.Fatal(err)
				}
			case "/Users/10/Items/Latest?api_key=test":
				if err := json.Unmarshal(response.Body.Bytes(), &items); err != nil {
					t.Fatal(err)
				}
			default:
				var list struct{ Items []itemPaths }
				if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
					t.Fatal(err)
				}
				items = list.Items
			}
			if response.Code != http.StatusOK || len(items) == 0 {
				t.Fatalf("live response: status=%d body=%s", response.Code, response.Body.String())
			}
			for _, item := range items {
				want := displayed
				if item.Id == "live" {
					want = raw
				}
				if item.Path != want || len(item.MediaSources) != 1 || item.MediaSources[0].Path != want {
					t.Fatalf("live/normal item display changed: %s", response.Body.String())
				}
			}
		})
	}
}

func TestItemDetailPreservesSourceThroughPlaybackInfoAndPreload(t *testing.T) {
	oldCache := db.Cache
	db.Cache = db.CacheGlobal{CacheInstance: freecache.NewCache(1 << 20)}
	t.Cleanup(func() { db.Cache = oldCache })
	const sourcePath = "/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=opaque&file_name=%E7%94%B5%E5%BD%B1%2001%26%2B.mkv&x=2&x=1"
	const ua = "Player/1.0"
	var sourceCalls, cdnCalls, playbackInfoCalls atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnCalls.Add(1)
		if r.UserAgent() != ua || r.Header.Get("Range") != "bytes=0-0" {
			t.Errorf("CDN headers=%v", r.Header)
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(http.StatusPartialContent)
	}))
	defer cdn.Close()
	target := cdn.URL + "/movie.mkv?sig=a%2fb+z&x=2&x=1"
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sourceCalls.Add(1)
		if r.URL.RequestURI() != sourcePath || r.UserAgent() != ua {
			t.Errorf("source request changed: URI=%q UA=%q", r.URL.RequestURI(), r.UserAgent())
			http.Error(w, "STRM URL was changed", http.StatusBadRequest)
			return
		}
		w.Header().Set("Location", target)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()
	raw := source.URL + sourcePath
	router, _ := newSTRMRedirectTestRouter(t, raw, false)
	config.C.VideoPreview = &config.VideoPreview{}
	config.C.Ssl = &config.Ssl{}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		sources := []any{map[string]any{"Id": "ms", "Path": raw, "Container": "strm"}}
		if r.URL.Path == "/Users/10/Items/20" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "20", "Type": "Movie", "Path": raw, "MediaSources": sources})
			return
		}
		playbackInfoCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": sources})
	}))
	defer origin.Close()
	config.C.Emby.Host = origin.URL
	core := playback.NewPreloader()
	core.Start(t.Context())
	core.Apply(true, 1)
	defer core.Stop()
	stop := ConfigurePreloading(PreloadOptions{
		Instance: t.Name(), Coordinator: core,
		Enabled: func() (bool, uint64) { return true, 1 },
		Account: playback.ExternalPreloadAccount,
		Resolve: func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
			return playback.NewExternalPreload(raw, ua, https.Transport())
		},
	})
	defer stop()
	router.Use(MediaPathDisplay(), PreloadObserver())
	router.GET("/Users/10/Items/20", LoadCacheItems)
	router.POST("/Items/:id/PlaybackInfo", TransferPlaybackInfo)
	server := httptest.NewServer(router)
	defer server.Close()
	internal, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	oldPort := webport.HTTP
	webport.HTTP = internal.Port()
	defer func() { webport.HTTP = oldPort }()

	request := httptest.NewRequest(http.MethodGet, "/Users/10/Items/20?api_key=user-token&DeviceId=device", nil)
	request.Header.Set("User-Agent", ua)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	var item struct {
		Path         string
		MediaSources []struct{ Path string }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	displayed := source.URL + "/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=opaque&file_name=电影 01&+.mkv&x=2&x=1"
	if response.Code != http.StatusOK || item.Path != displayed || len(item.MediaSources) != 1 || item.MediaSources[0].Path != displayed {
		t.Fatalf("detail/PlaybackInfo changed source: status=%d body=%s", response.Code, response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := playback.CachedExternalLink(raw, ua); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("detail did not publish the original source's preload result")
		}
		time.Sleep(10 * time.Millisecond)
	}
	core.Stop()
	request = httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?MediaSourceId=ms&api_key=test", nil)
	request.Header.Set("User-Agent", ua)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect || response.Header().Get("Location") != target || sourceCalls.Load() != 1 || cdnCalls.Load() != 1 || playbackInfoCalls.Load() != 2 {
		t.Fatalf("ordinary playback did not reuse preload: status=%d Location=%q source=%d CDN=%d PlaybackInfo=%d", response.Code, response.Header().Get("Location"), sourceCalls.Load(), cdnCalls.Load(), playbackInfoCalls.Load())
	}
}

func TestMediaResponseCacheKeepsOriginalPath(t *testing.T) {
	const raw = "http://strm.test/file%252F%E7%94%B5%E5%BD%B1.mkv?name=A%26B%2BC&x=2&x=1"
	router, _ := newSTRMRedirectTestRouter(t, raw, false)
	config.C.Cache.Enable = true
	if err := config.C.Cache.Init(); err != nil {
		t.Fatal(err)
	}
	config.C.VideoPreview = &config.VideoPreview{}
	router.Use(MediaPathDisplay(), cache.CacheableRouteMarker(), cache.RequestCacher())
	router.POST("/Items/movie/PlaybackInfo", TransferPlaybackInfo)
	path := "/Items/movie/PlaybackInfo?api_key=" + url.QueryEscape(t.Name()+t.TempDir())
	for range 2 {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
		var body struct {
			MediaSources []struct{ Path, DirectStreamUrl string }
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(body.MediaSources) != 1 || body.MediaSources[0].Path != "http://strm.test/file%2F电影.mkv?name=A&B+C&x=2&x=1" || body.MediaSources[0].DirectStreamUrl == "" {
			t.Fatalf("display/cache response=%s", response.Body.String())
		}
		if response.Header().Get("Content-Length") != strconv.Itoa(response.Body.Len()) {
			t.Fatalf("display Content-Length=%s body=%d", response.Header().Get("Content-Length"), response.Body.Len())
		}
		cache.WaitingForHandleChan()
		cached, ok := cache.GetRequestCache(httptest.NewRequest(http.MethodPost, path, nil))
		if !ok {
			t.Fatal("response cache missing")
		}
		var original struct{ MediaSources []struct{ Path string } }
		if err := json.Unmarshal(cached.BodyBytes(), &original); err != nil || len(original.MediaSources) != 1 || original.MediaSources[0].Path != raw {
			t.Fatalf("internal cached Path=%+v error=%v", original, err)
		}
		if cached.Header("Content-Length") != strconv.Itoa(len(cached.BodyBytes())) {
			t.Fatal("internal cache Content-Length does not match original body")
		}
	}
}
