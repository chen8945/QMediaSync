package emby

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/emby302/config"
	"qmediasync/emby302/util/https"
	"qmediasync/internal/helpers"
	"qmediasync/internal/playback"

	"github.com/gin-gonic/gin"
)

type preloadResolved struct {
	path, ua string
	force    bool
}

type embyPreloadHarness struct {
	t          *testing.T
	adapter    *embyPreloader
	core       *playback.Preloader
	router     *gin.Engine
	server     *httptest.Server
	enabled    atomic.Bool
	generation atomic.Uint64
	metadata   atomic.Int32
	resolved   chan preloadResolved
	mu         sync.Mutex
	nodes      map[string]preloadItem
	infos      map[string]preloadPlaybackInfo
	upstream   func(http.ResponseWriter, *http.Request)
}

func newEmbyPreloadHarness(t *testing.T) *embyPreloadHarness {
	t.Helper()
	h := &embyPreloadHarness{t: t, core: playback.NewPreloader(), resolved: make(chan preloadResolved, 128), nodes: make(map[string]preloadItem), infos: make(map[string]preloadPlaybackInfo)}
	h.enabled.Store(true)
	h.generation.Store(1)
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.metadata.Add(1)
		if r.Header.Get("X-Emby-Token") != "user-token" || r.Header.Get("Accept") != "application/json" || r.Header.Get("Cookie") != "" {
			t.Errorf("background auth/headers=%v", r.Header)
		}
		if r.URL.Query().Get("UserId") != "10" {
			t.Errorf("background user=%q", r.URL.Query().Get("UserId"))
		}
		w.Header().Set("Content-Type", "application/json")
		if h.upstream != nil {
			h.upstream(w, r)
			return
		}
		t.Errorf("unexpected metadata request %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	old := config.C
	config.C = &config.Config{Emby: &config.Emby{Host: h.server.URL}}
	h.core.Start(t.Context())
	h.core.Apply(true, 1)
	stop := ConfigurePreloading(PreloadOptions{
		Instance: t.Name(), Coordinator: h.core,
		Enabled:       func() (bool, uint64) { return h.enabled.Load(), h.generation.Load() },
		TrustedOrigin: func(u *url.URL) bool { return u.Scheme == "http" && u.Host == "qms.invalid" },
		Resolve: func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
			u, err := url.Parse(raw)
			if err != nil || u.Path != "/115/newurl" {
				return "", nil, false
			}
			return preloadHash(raw, ua), func(context.Context, *playback.PreloadTask) playback.PreloadResult {
				h.resolved <- preloadResolved{raw, ua, force}
				return playback.PreloadResult{Outcome: playback.PreloadCached}
			}, true
		},
	})
	h.adapter = activeEmbyPreloader.Load()
	h.router = gin.New()
	h.router.Use(PreloadObserver())
	h.router.Any("/*path", func(c *gin.Context) {
		kind, item, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
		if c.Request.Body != nil {
			_, _ = io.ReadAll(c.Request.Body)
		}
		h.mu.Lock()
		node := h.nodes[item]
		info := h.infos[item]
		h.mu.Unlock()
		switch kind {
		case "detail":
			c.JSON(http.StatusOK, node)
		case "playback":
			c.JSON(http.StatusOK, info)
		case "stream":
			c.Redirect(http.StatusTemporaryRedirect, "http://download.invalid/video")
		default:
			c.Status(http.StatusNoContent)
		}
	})
	t.Cleanup(func() { stop(); h.core.Stop(); h.server.Close(); config.C = old })
	return h
}

func (h *embyPreloadHarness) request(method, path, device, client, ua string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set(HeaderFullAuthName, fmt.Sprintf(`MediaBrowser Client="%s", DeviceId="%s", Token="user-token"`, client, device))
	req.Header.Set("User-Agent", ua)
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *embyPreloadHarness) waitResolved() preloadResolved {
	h.t.Helper()
	select {
	case result := <-h.resolved:
		return result
	case <-time.After(5 * time.Second):
		h.t.Fatal("preload did not resolve")
		return preloadResolved{}
	}
}

func preloadTestItem(id, kind string) preloadItem {
	return preloadItem{ID: id, Type: kind, SeriesID: "90", SeasonID: "91", MediaSources: []preloadSource{{ID: "source-" + id, ItemID: id, Path: "http://qms.invalid/115/newurl?pickcode=" + id + "&userid=115-user"}}}
}

func TestPreloadRoutesAndCredentialScope(t *testing.T) {
	for _, test := range []struct{ name, method, path, want string }{
		{"movie", "GET", "/emby/Users/10/Items/20", "detail"},
		{"plain", "GET", "/Items/20", "detail"},
		{"guid", "GET", "/Users/12345678-1234-1234-1234-123456789abc/Items/0123456789abcdef0123456789abcdef", "detail"},
		{"list", "GET", "/Users/10/Items", ""},
		{"image", "GET", "/Items/20/Images/Primary", ""},
		{"nested", "GET", "/other/Users/10/Items/20", ""},
		{"unknown_id", "GET", "/Items/guess", ""},
		{"detail_post", "POST", "/Items/20", ""},
		{"progress", "POST", "/emby/Sessions/Playing/Progress", "progress"},
		{"stop_case", "POST", "/emby/SESSIONS/PLAYING/STOPPED", "stop"},
		{"start", "POST", "/Sessions/Playing", "start"},
		{"fake_progress", "POST", "/Sessions/Playing/Progress/other", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			kind, _, _, _ := preloadRoute(test.method, test.path)
			if kind != test.want {
				t.Fatalf("kind=%q want=%q", kind, test.want)
			}
		})
	}
	old := config.C
	config.C = &config.Config{Emby: &config.Emby{Host: "http://emby.invalid"}}
	defer func() { config.C = old }()
	request := httptest.NewRequest("GET", "/Users/10/Items/20?DeviceId=device&api_key=first", nil)
	request.Header.Set("User-Agent", "Mozilla/test")
	one, ok := newPreloadRequest(request, "test", "10", "", 1)
	if !ok {
		t.Fatal("valid scope rejected")
	}
	request.Header.Set("X-Emby-Token", "second")
	if _, ok := newPreloadRequest(request, "test", "10", "", 1); ok {
		t.Fatal("conflicting token accepted")
	}
	request.Header.Del("X-Emby-Token")
	request.URL.RawQuery = "DeviceId=another&api_key=first"
	two, ok := newPreloadRequest(request, "test", "10", "", 1)
	if !ok || one.scope == two.scope {
		t.Fatal("devices share scope")
	}
	request.URL.RawQuery = "DeviceId=device&api_key=first&UserId=11"
	if _, ok := newPreloadRequest(request, "test", "10", "", 1); ok {
		t.Fatal("conflicting user accepted")
	}
}

func TestPreloadClientIdentityCompatibility(t *testing.T) {
	newEmbyPreloadHarness(t)
	for _, description := range []string{
		`Emby UserId=10,Client=Yamby,Device=Android Phone,DeviceId=opaque-device,Version=2.1.1.0`,
		`Emby Client="Hills", Device="Phone", DeviceId="opaque-device", Version="1.10.1"`,
		`MediaBrowser DEVICEID = opaque-device, Client = Custom Player`,
	} {
		r := httptest.NewRequest(http.MethodGet, "/Users/10/Items/20", nil)
		r.Header.Set(HeaderFullAuthName, description)
		r.Header.Set("X-Emby-Token", "user-token")
		r.Header.Set("User-Agent", "Player/1.0")
		parsed, ok := newPreloadRequest(r, "fixture", "10", "", 1)
		if !ok || parsed.weakIdentity || parsed.ua != "Player/1.0" {
			t.Fatal("合法播放器描述未被识别")
		}
		r.Header.Set("X-Emby-Device-Id", "different")
		if _, ok := newPreloadRequest(r, "fixture", "10", "", 1); ok {
			t.Fatal("设备冲突被当成缺失后降级")
		}
	}
	r := httptest.NewRequest(http.MethodGet, "/Users/10/Items/20?api_key=user-token", nil)
	r.Header.Set("User-Agent", "Player/1.0")
	weak, ok := newPreloadRequest(r, "fixture", "10", "", 1)
	if !ok || !weak.weakIdentity {
		t.Fatal("缺少设备标识未降级")
	}
	r.Header.Set("User-Agent", "Player/1.1")
	changed, _ := newPreloadRequest(r, "fixture", "10", "", 1)
	r.Header.Set("User-Agent", "Player/1.0")
	r.Header.Set("X-Emby-Device-Id", "Player/1.0")
	strong, _ := newPreloadRequest(r, "fixture", "10", "", 1)
	if weak.scope == changed.scope || weak.scope == strong.scope {
		t.Fatal("UA 版本或显式设备身份发生作用域碰撞")
	}
}

func TestPreloadMissingDeviceUsesCurrentUAAndConfirmedNext(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	h.nodes["20"] = preloadTestItem("20", "Episode")
	h.nodes["20"].MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(preloadTestItem("21", "Episode"))
	}
	send := func(method, path string, body any) {
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set(HeaderFullAuthName, `Emby Client=Fixture Player, Token=user-token`)
		r.Header.Set("User-Agent", "Player/1.0")
		h.router.ServeHTTP(httptest.NewRecorder(), r)
	}
	send(http.MethodGet, "/Users/10/Items/20", nil)
	if result := h.waitResolved(); result.ua != "Player/1.0" {
		t.Fatal("无设备详情未使用本次完整 UA")
	}
	send(http.MethodGet, "/Videos/20/stream?MediaSourceId=source-20&PlaySessionId=current", nil)
	send(http.MethodPost, "/Sessions/Playing", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"})
	send(http.MethodPost, "/Sessions/Playing/Progress", map[string]any{
		"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current", "IsPaused": false,
		"PositionTicks": 54 * 60 * 10_000_000, "NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "21"}},
	})
	if result := h.waitResolved(); result.ua != "Player/1.0" || !strings.Contains(result.path, "pickcode=21") {
		t.Fatal("无设备的已确认播放未准备正确后继")
	}
	h.adapter.mu.Lock()
	defer h.adapter.mu.Unlock()
	for scope, device := range h.adapter.devices {
		request := preloadRequest{scope: scope, weakIdentity: true, generation: 1}
		if preloadUAKey(request, device) != "" {
			t.Fatal("弱身份允许跨次 UA 映射")
		}
		strong := request
		strong.weakIdentity = false
		key := preloadUAKey(strong, device)
		if h.adapter.uas.Lookup(key, 1) != "" {
			t.Fatal("弱身份真实取流保存了跨次 UA")
		}
		h.adapter.uas.Remember(key, "Wrong/2.0", 1)
		if h.adapter.preloadPlayerUA(request, device) != "Player/1.0" {
			t.Fatal("弱身份读取了跨次映射")
		}
	}
}

func TestPreloadDetailsReuseNaturalResponsesAndLatestSelection(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	const browser = "Mozilla/5.0 Fixture"
	for _, id := range []string{"20", "21", "22", "23", "24", "25", "26", "27", "28", "29", "30"} {
		h.nodes[id] = preloadTestItem(id, "Movie")
	}
	h.nodes["21"] = preloadTestItem("21", "Episode")
	// Two natural PlaybackInfo/detail arrival orders use their source without metadata or cold probing.
	for _, id := range []string{"22", "23"} {
		value := h.nodes[id]
		h.infos[id] = preloadPlaybackInfo{MediaSources: value.MediaSources}
		value.MediaSources = nil
		h.nodes[id] = value
	}
	h.request("POST", "/Items/22/PlaybackInfo", "before", "Emby Web", browser, map[string]any{})
	h.request("GET", "/Users/10/Items/22", "before", "Emby Web", browser, nil)
	h.request("GET", "/Users/10/Items/23", "after", "Emby Web", browser, nil)
	h.request("POST", "/Items/23/PlaybackInfo", "after", "Emby Web", browser, map[string]any{})
	h.request("GET", "/Users/10/Items/20", "movie", "Emby Web", browser, nil)
	h.request("GET", "/Users/10/Items/21", "episode", "Emby Web", browser, nil)
	value := h.nodes["24"]
	value.MediaSources = append(value.MediaSources, preloadSource{ID: "selected", ItemID: "24", Path: "http://qms.invalid/115/newurl?pickcode=selected&userid=115-user"})
	h.nodes["24"] = value
	h.request("GET", "/Users/10/Items/24?MediaSourceId=selected", "versions", "Emby Web", browser, nil)
	for _, id := range []string{"25", "26", "27"} {
		h.request("GET", "/Users/10/Items/"+id, "fast", "Emby Web", browser, nil)
	}
	// Actual stream UA can differ from the control client's UA and is reused for a later item.
	h.request("GET", "/Users/10/Items/28", "learned", "Fixture App", "Control/1", nil)
	h.request("GET", "/Videos/28/stream?MediaSourceId=source-28", "learned", "Fixture App", "Player/1", nil)
	h.request("GET", "/Users/10/Items/29", "learned", "Fixture App", "Control/1", nil)
	// PlaybackInfo alone creates no work; a first detail uses its current UA without learning.
	h.infos["30"] = preloadPlaybackInfo{MediaSources: h.nodes["30"].MediaSources}
	h.request("POST", "/Items/30/PlaybackInfo", "alone", "Emby Web", browser, map[string]any{})
	h.request("GET", "/Users/10/Items/30", "unknown", "Fixture App", "Control/1", nil)
	seen := make(map[string]preloadResolved)
	for range 8 {
		result := h.waitResolved()
		value, _ := url.Parse(result.path)
		seen[value.Query().Get("pickcode")] = result
		if !result.force {
			t.Fatal("Emby must request direct mode")
		}
	}
	for _, id := range []string{"20", "21", "22", "23", "selected", "27", "29", "30"} {
		if _, ok := seen[id]; !ok {
			t.Errorf("missing expected preload %s; got=%v", id, seen)
		}
	}
	if seen["29"].ua != "Player/1" {
		t.Errorf("learned UA=%q", seen["29"].ua)
	}
	if seen["30"].ua != "Control/1" {
		t.Errorf("first detail fallback UA=%q", seen["30"].ua)
	}
	if h.metadata.Load() != 0 {
		t.Errorf("natural source reuse made %d extra HTTP", h.metadata.Load())
	}
	h.core.Stop()
	if len(h.resolved) != 0 {
		t.Fatalf("unexpected extra candidates=%d", len(h.resolved))
	}
}

func TestPreloadProgressThresholdPauseAndCrossSeason(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	item := preloadTestItem("20", "Episode")
	item.MediaSources[0].RunTimeTicks = 90 * 60 * 10_000_000
	h.nodes["20"] = item
	next := preloadTestItem("21", "Episode")
	next.SeasonID = "92"
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Shows/90/Episodes" || r.URL.Query().Get("StartItemId") != "20" || r.URL.Query().Get("Limit") != "2" || r.URL.Query().Get("SeasonId") != "" {
			t.Errorf("successor query %s", r.URL)
		}
		_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{item, next}, TotalRecordCount: 2})
	}
	h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Mozilla/test", nil)
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Emby Web", "Mozilla/test", nil)
	h.request("POST", "/Sessions/Playing", "device", "Emby Web", "Mozilla/test", map[string]any{"ItemId": "20", "MediaSourceId": "source-20"})
	progress := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PositionTicks": 72 * 60 * 10_000_000, "IsPaused": false}
	for range 100 {
		h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	}
	if h.metadata.Load() != 0 {
		t.Fatal("80% of long content queried too early")
	}
	progress["PositionTicks"] = 81 * 60 * 10_000_000
	progress["IsPaused"] = true
	h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	if h.metadata.Load() != 0 {
		t.Fatal("paused progress queried successor")
	}
	progress["IsPaused"] = false
	h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	result := h.waitResolved()
	if !strings.Contains(result.path, "pickcode=21&") {
		t.Fatalf("successor=%s", result.path)
	}
	for range 100 {
		h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	}
	h.core.Stop()
	if h.metadata.Load() != 1 || len(h.resolved) != 0 {
		t.Errorf("duplicates: metadata=%d resolves=%d", h.metadata.Load(), len(h.resolved))
	}
}

func TestPreloadMetadataRelationsAndWarmPlaybackInfo(t *testing.T) {
	for _, test := range []struct {
		name      string
		item      preloadItem
		selected  string
		kind      playback.PreloadKind
		wantCalls int
		want      string
		serve     func(*testing.T, http.ResponseWriter, *http.Request)
	}{
		{"series_resume", preloadItem{ID: "90", Type: "Series"}, "", playback.PreloadDetail, 1, "20", func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("Filters") != "IsResumable" || r.URL.Query().Get("ParentId") != "90" {
				t.Error("resume query lost scope")
			}
			_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
		}},
		{"series_nextup", preloadItem{ID: "90", Type: "Series"}, "", playback.PreloadDetail, 2, "20", func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			items := []preloadItem{}
			if r.URL.Path == "/Shows/NextUp" {
				items = append(items, preloadTestItem("20", "Episode"))
			}
			_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
		}},
		{"season_compiled_special", preloadItem{ID: "91", Type: "Season", SeriesID: "90"}, "", playback.PreloadDetail, 2, "20", func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			item := preloadTestItem("20", "Episode")
			item.SeasonID = "specials"
			item.ParentID = "specials"
			if strings.HasSuffix(r.URL.Path, "Episodes") && (r.URL.Query().Get("SeasonId") != "91" || r.URL.Query().Get("StartItemId") != "20") {
				t.Error("special membership unscoped")
			}
			_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{item}})
		}},
		{"movie_part", func() preloadItem { i := preloadTestItem("20", "Movie"); i.PartCount = 2; return i }(), "", playback.PreloadNext, 1, "21", func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			item := preloadTestItem("21", "Video")
			item.ParentID = "20"
			_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{item}, TotalRecordCount: 1})
		}},
		{"video_part_parent", func() preloadItem { i := preloadTestItem("21", "Video"); i.ParentID = "20"; return i }(), "", playback.PreloadNext, 2, "22", func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/Items/") {
				item := preloadTestItem("20", "Movie")
				item.PartCount = 3
				_ = json.NewEncoder(w).Encode(item)
				return
			}
			one, two := preloadTestItem("21", "Video"), preloadTestItem("22", "Video")
			one.ParentID, two.ParentID = "20", "20"
			_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{one, two}, TotalRecordCount: 2})
		}},
		{"warm_missing_path", func() preloadItem {
			i := preloadTestItem("20", "Movie")
			i.MediaSources[0].Path = ""
			i.MediaSources[0].Container = "mkv"
			i.MediaSources[0].RunTimeTicks = 1000
			i.MediaSources[0].MediaStreams = []preloadStream{{"Video", "h264"}, {"Audio", "aac"}}
			return i
		}(), "", playback.PreloadDetail, 1, "20", func(t *testing.T, w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/Items/20/PlaybackInfo" || r.URL.Query().Get("IsPlayback") != "false" || r.URL.Query().Get("AutoOpenLiveStream") != "false" {
				t.Error("warm PI semantics lost")
			}
			_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: preloadTestItem("20", "Movie").MediaSources})
		}},
		{"cold_missing_path", func() preloadItem {
			i := preloadTestItem("20", "Movie")
			i.MediaSources[0].Path = ""
			i.MediaSources[0].Container = "strm"
			return i
		}(), "", playback.PreloadDetail, 0, "", nil},
		{"ordinary_movie", preloadTestItem("20", "Movie"), "", playback.PreloadNext, 0, "", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			if test.serve != nil {
				h.upstream = func(w http.ResponseWriter, r *http.Request) { test.serve(t, w, r) }
			}
			request := preloadRequest{scope: preloadHash(t.Name()), token: "user-token", user: "10", ua: "Mozilla/test", origin: h.server.URL, generation: 1, selected: test.selected}
			job := &embyPreloadJob{owner: h.adapter, request: request, item: test.item, ua: request.ua, kind: test.kind}
			done := make(chan struct{})
			h.core.Submit(playback.PreloadCandidate{Key: t.Name(), Scope: request.scope, Kind: playback.PreloadNext, Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
				defer close(done)
				return job.run(ctx, task)
			}})
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("metadata job did not finish")
			}
			if int(h.metadata.Load()) != test.wantCalls {
				t.Errorf("HTTP calls=%d want=%d", h.metadata.Load(), test.wantCalls)
			}
			if test.want == "" {
				if len(h.resolved) != 0 {
					t.Error("unexpected resolution")
				}
			} else {
				result := h.waitResolved()
				value, _ := url.Parse(result.path)
				if value.Query().Get("pickcode") != test.want {
					t.Errorf("target=%s want=%s", value.Query().Get("pickcode"), test.want)
				}
			}
		})
	}
}

func TestPreloadMetadataResponseBounds(t *testing.T) {
	zip := func(value []byte) []byte {
		var result bytes.Buffer
		writer := gzip.NewWriter(&result)
		_, _ = writer.Write(value)
		_ = writer.Close()
		return result.Bytes()
	}
	for _, test := range []struct {
		name                  string
		body                  []byte
		contentType, encoding string
		ok                    bool
	}{
		{"json", []byte(`{"Id":"20"}`), "application/json;charset=utf-8", "", true},
		{"gzip", zip([]byte(`{"Id":"20"}`)), "application/json", "gzip", true},
		{"html200", []byte(`<html>SPA</html>`), "text/html", "", false},
		{"wrong_encoding", []byte(`{}`), "application/json", "br", false},
		{"broken_gzip", []byte(`bad`), "application/json", "gzip", false},
		{"encoded_limit", bytes.Repeat([]byte(" "), preloadBodyLimit+1), "application/json", "", false},
		{"decoded_limit", zip(bytes.Repeat([]byte(" "), preloadBodyLimit+1)), "application/json", "gzip", false},
		{"trailing_json", []byte(`{} {}`), "application/json", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodePreloadJSON(test.body, http.Header{"Content-Type": {test.contentType}, "Content-Encoding": {test.encoding}})
			if (err == nil) != test.ok {
				t.Fatalf("err=%v ok=%v", err, test.ok)
			}
		})
	}
}

func TestPreloadObserverPreservesBytesAndDisabledPath(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	h.enabled.Store(false)
	router := gin.New()
	router.Use(PreloadObserver())
	const requestBody = `{"ItemId":"20","PositionTicks":12345,"IsPaused":false,"extra":"unchanged"}`
	response := []byte("raw response bytes\x00\n")
	router.POST("/Sessions/Playing/Progress", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		if string(body) != requestBody {
			t.Error("request bytes changed")
		}
		c.Header("X-Test", "preserved")
		c.Data(http.StatusOK, "application/octet-stream", response)
	})
	req := httptest.NewRequest("POST", "/Sessions/Playing/Progress?DeviceId=device&api_key=user-token", strings.NewReader(requestBody))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Test") != "preserved" || !bytes.Equal(rec.Body.Bytes(), response) {
		t.Fatal("response changed")
	}
	if h.metadata.Load() != 0 || len(h.resolved) != 0 || len(h.adapter.devices) != 0 {
		t.Fatal("disabled observer did work")
	}
}

func TestPreloadRepeatedNaturalResponsesDoNotResetMetadataBudget(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	item := preloadTestItem("20", "Movie")
	item.MediaSources = nil
	h.nodes["20"] = item
	metadata := make(chan struct{}, 4)
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		defer func() { metadata <- struct{}{} }()
		value := preloadTestItem("20", "Movie")
		if strings.Contains(r.URL.Path, "PlaybackInfo") {
			_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: value.MediaSources})
			return
		}
		value.MediaSources[0].Path = ""
		value.MediaSources[0].Container = "strm"
		_ = json.NewEncoder(w).Encode(value)
	}
	h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Mozilla/test", nil)
	select {
	case <-metadata:
	case <-time.After(4 * time.Second):
		t.Fatal("initial source query did not run")
	}
	// An equivalent observed response must not reopen the completed metadata-only skip.
	for range 100 {
		h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Mozilla/test", nil)
		h.request("POST", "/Items/20/PlaybackInfo", "device", "Emby Web", "Mozilla/test", map[string]any{})
	}
	time.Sleep(2100 * time.Millisecond)
	if h.metadata.Load() != 1 || len(h.resolved) != 0 {
		t.Fatalf("identical responses reopened preparation: HTTP=%d resolved=%d", h.metadata.Load(), len(h.resolved))
	}
	// A real normal response supplies new warm fields. It may spend the one remaining HTTP call.
	warm := preloadTestItem("20", "Movie").MediaSources
	warm[0].Path, warm[0].Container, warm[0].RunTimeTicks = "", "mkv", 1000
	warm[0].MediaStreams = []preloadStream{{"Video", "h264"}, {"Audio", "aac"}}
	h.mu.Lock()
	h.infos["20"] = preloadPlaybackInfo{MediaSources: warm}
	h.mu.Unlock()
	h.request("POST", "/Items/20/PlaybackInfo", "device", "Emby Web", "Mozilla/test", map[string]any{})
	h.waitResolved()
	for range 100 {
		h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Mozilla/test", nil)
		h.request("POST", "/Items/20/PlaybackInfo", "device", "Emby Web", "Mozilla/test", map[string]any{})
	}
	h.core.Stop()
	if h.metadata.Load() != 2 || len(h.resolved) != 0 {
		t.Fatalf("metadata budget was reset: HTTP=%d resolved=%d", h.metadata.Load(), len(h.resolved))
	}
}

func TestPreloadPermissionAndSourceChangesRevokeOlderDetails(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	const ua = "Mozilla/test"
	for _, id := range []string{"20", "21", "22", "23", "24", "26", "27", "28"} {
		h.nodes[id] = preloadTestItem(id, "Movie")
	}
	for _, id := range []string{"20", "21", "22", "23", "24", "28"} {
		h.request("GET", "/Users/10/Items/"+id, id, "Emby Web", ua, nil)
	}
	value := h.nodes["20"]
	value.PlayAccess = "None"
	h.nodes["20"] = value
	value = h.nodes["21"]
	value.IsLive = true
	h.nodes["21"] = value
	value = h.nodes["22"]
	value.Type = "Audio"
	h.nodes["22"] = value
	value = h.nodes["23"]
	value.MediaSources = append([]preloadSource(nil), value.MediaSources...)
	value.MediaSources[0].Path = "http://qms.invalid/115/newurl?pickcode=fresh23&userid=115-user"
	h.nodes["23"] = value
	value = h.nodes["24"]
	value.MediaSources = append(value.MediaSources, preloadSource{ID: "second", Path: "http://qms.invalid/115/newurl?pickcode=second&userid=115-user"})
	h.nodes["24"] = value
	value = h.nodes["28"]
	value.MediaSources = append([]preloadSource(nil), value.MediaSources...)
	denied := false
	value.MediaSources[0].SupportsDirectPlay, value.MediaSources[0].SupportsDirectStream = &denied, &denied
	h.nodes["28"] = value
	for _, id := range []string{"20", "21", "22", "23", "24", "28"} {
		h.request("GET", "/Users/10/Items/"+id, id, "Emby Web", ua, nil)
	}
	// A virtual transcoding source does not disqualify the single original source.
	value = h.nodes["26"]
	value.MediaSources = append(value.MediaSources, preloadSource{ID: "source" + MediaSourceIdSegment + "transcode"})
	h.nodes["26"] = value
	h.request("GET", "/Users/10/Items/26", "26", "Emby Web", ua, nil)
	// Earlier PI fills missing fields, but cannot overwrite a newer concrete detail path.
	old := preloadTestItem("27", "Movie")
	old.MediaSources[0].Path = "http://qms.invalid/115/newurl?pickcode=stale27&userid=115-user"
	h.infos["27"] = preloadPlaybackInfo{MediaSources: old.MediaSources}
	h.request("POST", "/Items/27/PlaybackInfo", "27", "Emby Web", ua, map[string]any{})
	h.request("GET", "/Users/10/Items/27", "27", "Emby Web", ua, nil)
	seen := make(map[string]bool)
	for range 3 {
		value, _ := url.Parse(h.waitResolved().path)
		seen[value.Query().Get("pickcode")] = true
	}
	h.core.Stop()
	if !seen["fresh23"] || !seen["26"] || !seen["27"] || h.metadata.Load() != 0 || len(h.resolved) != 0 {
		t.Fatalf("wrong surviving candidates=%v HTTP=%d extra=%d", seen, h.metadata.Load(), len(h.resolved))
	}
}

func TestPreloadCurrentCacheHitDoesNotConsumeSuccessorEligibility(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	item := preloadTestItem("20", "Episode")
	item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
	h.nodes["20"] = item
	next := preloadTestItem("21", "Episode")
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{item, next}})
	}
	h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Mozilla/test", nil)
	if !strings.Contains(h.waitResolved().path, "pickcode=20&") {
		t.Fatal("current detail did not use the cache result")
	}
	start := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session"}
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Emby Web", "Mozilla/test", nil)
	h.request("POST", "/Sessions/Playing", "device", "Emby Web", "Mozilla/test", start)
	progress := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false}
	h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	if !strings.Contains(h.waitResolved().path, "pickcode=21&") {
		t.Fatal("current cache hit suppressed next item")
	}
	for range 100 {
		h.request("POST", "/Sessions/Playing", "device", "Emby Web", "Mozilla/test", start)
		h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	}
	h.core.Stop()
	if h.metadata.Load() != 1 || len(h.resolved) != 0 {
		t.Fatalf("duplicate starts renewed next eligibility: HTTP=%d extra=%d", h.metadata.Load(), len(h.resolved))
	}
}

func TestPreloadObserverPreservesGzipAndActiveRequestBytes(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	item := preloadTestItem("20", "Movie")
	data, _ := json.Marshal(item)
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(data)
	_ = writer.Close()
	router := gin.New()
	router.Use(PreloadObserver())
	router.GET("/Users/10/Items/20", func(c *gin.Context) {
		c.Header("Content-Encoding", "gzip")
		c.Header("X-Original", "preserved")
		c.Data(http.StatusOK, "application/json", compressed.Bytes())
	})
	router.POST("/Sessions/Playing/Progress", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		c.Data(http.StatusOK, "application/json", body)
	})
	request := httptest.NewRequest("GET", "/Users/10/Items/20?api_key=user-token&DeviceId=device&X-Emby-Client=Emby+Web", nil)
	request.Header.Set("User-Agent", "Mozilla/test")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Encoding") != "gzip" || response.Header().Get("X-Original") != "preserved" || !bytes.Equal(response.Body.Bytes(), compressed.Bytes()) {
		t.Fatal("observer changed compressed response")
	}
	h.waitResolved()
	const body = "{\n  \"ItemId\":\"20\",\"PositionTicks\":0,\"IsPaused\":false,\"unknown\":\"original\"\n}\n"
	request = httptest.NewRequest("POST", "/Sessions/Playing/Progress?api_key=user-token&DeviceId=device", strings.NewReader(body))
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Body.String() != body || h.metadata.Load() != 0 {
		t.Fatal("active observer changed request bytes or queried unknown playback")
	}
}

func TestPreloadOutOfOrderDetailAndPlaybackInfoKeepLatestIntent(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	type gate struct{ entered, release, done chan struct{} }
	gates := make(map[string]gate)
	for _, name := range []string{"different", "same", "playback", "before_detail"} {
		gates[name] = gate{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	}
	router := gin.New()
	router.Use(PreloadObserver())
	router.Any("/*path", func(c *gin.Context) {
		kind, id, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
		if c.Request.Body != nil {
			_, _ = io.ReadAll(c.Request.Body)
		}
		name, old := c.Query("case"), c.Query("order") == "old"
		if old {
			close(gates[name].entered)
			<-gates[name].release
		}
		item := preloadTestItem(id, "Movie")
		if old {
			item.MediaSources[0].Path = "http://qms.invalid/115/newurl?pickcode=stale" + id + "&userid=115-user"
		}
		if kind == "playback" {
			if !old {
				item.MediaSources[0].Path = "http://qms.invalid/115/newurl?pickcode=fresh" + id + "&userid=115-user"
				if name == "same" {
					item.MediaSources[0].Path = ""
					item.MediaSources[0].Container, item.MediaSources[0].RunTimeTicks = "mkv", 1000
					item.MediaSources[0].MediaStreams = []preloadStream{{"Video", "h264"}, {"Audio", "aac"}}
				}
			}
			c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: item.MediaSources})
		} else {
			c.JSON(http.StatusOK, item)
		}
	})
	h.router = router
	startOld := func(name, method, path string) {
		go func() {
			defer close(gates[name].done)
			h.request(method, path+"?case="+name+"&order=old", name, "Emby Web", "Mozilla/test", map[string]any{})
		}()
		<-gates[name].entered
	}
	finishOld := func(name string) { close(gates[name].release); <-gates[name].done }
	startOld("different", "GET", "/Users/10/Items/40")
	h.request("GET", "/Users/10/Items/41?case=different", "different", "Emby Web", "Mozilla/test", nil)
	finishOld("different")
	startOld("same", "GET", "/Users/10/Items/42")
	h.request("GET", "/Users/10/Items/42?case=same", "same", "Emby Web", "Mozilla/test", nil)
	sameScope := preloadHash(t.Name(), config.C.Emby.Host, "user-token", "same")
	h.adapter.mu.Lock()
	revision := h.adapter.devices[sameScope].revision
	h.adapter.mu.Unlock()
	h.request("POST", "/Items/42/PlaybackInfo?case=same", "same", "Emby Web", "Mozilla/test", map[string]any{})
	finishOld("same")
	h.adapter.mu.Lock()
	if h.adapter.devices[sameScope].revision != revision {
		t.Error("same-item enrichment changed the debounce key")
	}
	h.adapter.mu.Unlock()
	// A late older PI cannot replace a newer PI's source or selected identity.
	h.request("GET", "/Users/10/Items/43?case=playback", "playback", "Emby Web", "Mozilla/test", nil)
	startOld("playback", "POST", "/Items/43/PlaybackInfo")
	h.request("POST", "/Items/43/PlaybackInfo?case=playback", "playback", "Emby Web", "Mozilla/test", map[string]any{})
	finishOld("playback")
	// A PI which began before a newer detail may fill missing fields, not overwrite its path.
	startOld("before_detail", "POST", "/Items/44/PlaybackInfo")
	h.request("GET", "/Users/10/Items/44?case=before_detail", "before_detail", "Emby Web", "Mozilla/test", nil)
	finishOld("before_detail")
	seen := make(map[string]bool)
	for range 4 {
		value, _ := url.Parse(h.waitResolved().path)
		seen[value.Query().Get("pickcode")] = true
	}
	h.core.Stop()
	for _, want := range []string{"41", "42", "fresh43", "44"} {
		if !seen[want] {
			t.Errorf("latest target %s missing; got %v", want, seen)
		}
	}
	if h.metadata.Load() != 0 || len(h.resolved) != 0 {
		t.Errorf("stale responses caused extra work: HTTP=%d extra=%d", h.metadata.Load(), len(h.resolved))
	}
}

func TestPreloadRecordedEmbyRelations(t *testing.T) {
	data, err := os.ReadFile("testdata/preload/emby_protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Samples []struct {
			Label   string          `json:"label"`
			Payload json.RawMessage `json:"payload"`
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	payloads := make(map[string]json.RawMessage)
	for _, sample := range fixture.Samples {
		payloads[sample.Label] = sample.Payload
	}
	item := func(label string) preloadItem {
		var value preloadItem
		if err := json.Unmarshal(payloads[label], &value); err != nil || !validPreloadItem(value) {
			t.Fatalf("invalid recorded item %s: %v", label, err)
		}
		return value
	}
	for _, test := range []struct {
		name, response, want string
		current              preloadItem
		pending              bool
		calls                int32
	}{
		{"resume", "emby-resumable-filter", "1000", preloadItem{ID: "1003", Type: "Series"}, true, 1},
		{"season_resume", "emby-resumable-filter", "1000", preloadItem{ID: "1002", Type: "Season", SeriesID: "1003"}, true, 1},
		{"successor", "emby-episodes-start", "1005", item("emby-item-episode"), false, 1},
		{"cross_season", "emby-cross-season-cursor", "1011", preloadItem{ID: "1007", Type: "Episode", SeriesID: "1009"}, false, 1},
		{"movie_part", "emby-parts-movie", "1017", item("emby-item-movie"), false, 1},
		{"video_part", "emby-parts-movie", "1019", item("emby-part-detail"), false, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				payload := payloads[test.response]
				if r.URL.Path == "/Users/10/Items/1014" {
					payload = payloads["emby-item-movie"]
				}
				_, _ = w.Write(payload)
			}
			request := preloadRequest{scope: preloadHash(t.Name()), token: "user-token", user: "10", origin: h.server.URL, generation: 1}
			job := &embyPreloadJob{owner: h.adapter, request: request, item: test.current}
			done := make(chan preloadItem, 1)
			h.core.Submit(playback.PreloadCandidate{Key: t.Name(), Scope: request.scope, Kind: playback.PreloadNext, Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
				var value preloadItem
				var err error
				if test.pending {
					value, err = job.pendingEpisode(ctx, task)
				} else {
					value, err = job.successor(ctx, task)
				}
				if err != nil {
					t.Errorf("recorded relation rejected: %v", err)
				}
				done <- value
				return playback.PreloadResult{Outcome: playback.PreloadCached}
			}})
			select {
			case got := <-done:
				if got.ID != test.want || h.metadata.Load() != test.calls {
					t.Errorf("recorded selection=%s HTTP=%d; want=%s HTTP=%d", got.ID, h.metadata.Load(), test.want, test.calls)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("recorded relation timed out")
			}
		})
	}
}

func TestPreloadInvalidProgressCancelsInFlightSuccessor(t *testing.T) {
	for _, reason := range []string{"seek", "unknown_pause", "source_change", "session_change", "forbidden"} {
		t.Run(reason, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			item := preloadTestItem("20", "Episode")
			item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			if reason == "source_change" {
				other := item.MediaSources[0]
				other.ID, other.Path = "another-source", other.Path+"&version=another"
				item.MediaSources = append(item.MediaSources, other)
			}
			h.nodes["20"] = item
			entered, canceled := make(chan struct{}), make(chan struct{})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
				close(canceled)
			}
			h.request("GET", "/Users/10/Items/20?MediaSourceId=source-20", "device", "Emby Web", "Mozilla/test", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Emby Web", "Mozilla/test", nil)
			h.request("POST", "/Sessions/Playing", "device", "Emby Web", "Mozilla/test", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session"})
			progress := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false}
			h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("successor query did not start")
			}
			endpoint := "/Sessions/Playing/Progress"
			switch reason {
			case "seek":
				progress["PositionTicks"] = 10 * 60 * 10_000_000
			case "unknown_pause":
				delete(progress, "IsPaused")
			case "source_change":
				progress["MediaSourceId"] = "another-source"
				endpoint = "/Sessions/Playing"
			case "session_change":
				progress["PlaySessionId"] = "another-session"
				endpoint = "/Sessions/Playing"
			case "forbidden":
				h.router = gin.New()
				h.router.Use(PreloadObserver())
				h.router.POST("/Sessions/Playing/Progress", func(c *gin.Context) {
					_, _ = io.ReadAll(c.Request.Body)
					c.Status(http.StatusForbidden)
				})
			}
			h.request("POST", endpoint, "device", "Emby Web", "Mozilla/test", progress)
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("invalid progress did not cancel metadata context")
			}
			h.core.Stop()
			if h.metadata.Load() != 1 || len(h.resolved) != 0 {
				t.Fatalf("invalid progress signed successor: HTTP=%d signed=%d", h.metadata.Load(), len(h.resolved))
			}
		})
	}
}

func TestPreloadChangedPlaybackQueueCancelsPreviousSuccessor(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	item := preloadTestItem("20", "Episode")
	item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
	h.nodes["20"] = item
	entered, canceled := make(chan struct{}), make(chan struct{})
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/10/Items/21":
			close(entered)
			<-r.Context().Done()
			close(canceled)
		case "/Users/10/Items/22":
			_ = json.NewEncoder(w).Encode(preloadTestItem("22", "Episode"))
		default:
			t.Errorf("unexpected successor request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
	h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Mozilla/test", nil)
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Emby Web", "Mozilla/test", nil)
	h.request("POST", "/Sessions/Playing", "device", "Emby Web", "Mozilla/test", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session"})
	progress := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false,
		"NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "21"}}}
	h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("original successor query did not start")
	}
	progress["NowPlayingQueue"] = []map[string]string{{"Id": "20"}, {"Id": "22"}}
	h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/test", progress)
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("changed playback queue retained previous successor")
	}
	if resolved := h.waitResolved(); !strings.Contains(resolved.path, "pickcode=22&") {
		t.Fatalf("changed queue resolved %s", resolved.path)
	}
	h.core.Stop()
	if h.metadata.Load() != 2 || len(h.resolved) != 0 {
		t.Fatalf("changed queue signed stale or repeated successor: HTTP=%d extra=%d", h.metadata.Load(), len(h.resolved))
	}
}

func TestPreloadControlChangeCancelsBeforeNewDetailResponds(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	h.nodes["20"] = preloadTestItem("20", "Movie")
	h.nodes["21"] = preloadTestItem("21", "Movie")
	h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/1", nil)
	h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1", nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	router := gin.New()
	router.Use(PreloadObserver())
	router.GET("/Users/10/Items/21", func(c *gin.Context) {
		close(entered)
		<-release
		c.JSON(http.StatusOK, preloadTestItem("21", "Movie"))
	})
	h.router = router
	go func() {
		defer close(done)
		h.request("GET", "/Users/10/Items/21", "device", "Another App", "Control/2", nil)
	}()
	<-entered
	// Let the earlier candidate's original debounce pass while the new detail is blocked.
	time.Sleep(2100 * time.Millisecond)
	if len(h.resolved) != 0 || h.metadata.Load() != 0 {
		t.Error("old playback UA remained eligible during control change")
	}
	close(release)
	<-done
	if result := h.waitResolved(); result.ua != "Control/2" {
		t.Fatalf("更新控制端没有回退到自身完整UA：%q", result.ua)
	}
	h.core.Stop()
	if len(h.resolved) != 0 {
		t.Fatal("控制端更新后仍准备了旧UA")
	}
}

func TestPreloadStalePlaybackResponsesDoNotRestoreIntent(t *testing.T) {
	for _, action := range []string{"stream-then-stop", "stream-then-control-change", "stream-then-control-playback", "start-then-new-detail", "playback-info-then-control-change"} {
		t.Run(action, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			h.nodes["20"], h.nodes["21"] = preloadTestItem("20", "Movie"), preloadTestItem("21", "Movie")
			client, ua := "Fixture App", "Control/1"
			if action == "start-then-new-detail" || action == "playback-info-then-control-change" {
				client, ua = "Emby Web", "Mozilla/old"
			}
			h.request("GET", "/Users/10/Items/20", "device", client, ua, nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", client, "Initial Player", nil)
			report := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session", "IsPaused": false}
			h.request("POST", "/Sessions/Playing", "device", client, ua, report)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			router := gin.New()
			router.Use(PreloadObserver())
			router.Any("/*path", func(c *gin.Context) {
				if c.Request.Body != nil {
					_, _ = io.ReadAll(c.Request.Body)
				}
				if c.Query("older") == "true" {
					close(entered)
					<-release
				}
				kind, item, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "detail":
					c.JSON(http.StatusOK, h.nodes[item])
				case "playback":
					c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: h.nodes[item].MediaSources})
				case "stream":
					c.Redirect(http.StatusTemporaryRedirect, "http://download.invalid/video")
				default:
					c.Status(http.StatusNoContent)
				}
			})
			h.router = router
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			go func() {
				defer close(done)
				switch action {
				case "start-then-new-detail":
					h.request("POST", "/Sessions/Playing?older=true", "device", client, ua, report)
				case "playback-info-then-control-change":
					h.request("POST", "/Items/20/PlaybackInfo?older=true", "device", client, ua, map[string]any{})
				default:
					h.request("GET", "/Videos/20/stream?MediaSourceId=source-20&older=true", "device", client, "Old Player", nil)
				}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("旧请求未进入阻塞点")
			}
			switch action {
			case "stream-then-stop":
				h.request("POST", "/Sessions/Playing/Stopped", "device", client, ua, report)
			case "start-then-new-detail":
				h.request("GET", "/Users/10/Items/21", "device", client, ua, nil)
			case "stream-then-control-playback":
				h.request("POST", "/Items/20/PlaybackInfo", "device", "Another App", "Control/2", map[string]any{})
			default:
				h.request("GET", "/Users/10/Items/20", "device", "Another App", "Control/2", nil)
			}
			close(release)
			<-done
			if action == "start-then-new-detail" {
				if result := h.waitResolved(); result.path != h.nodes["21"].MediaSources[0].Path {
					t.Fatalf("旧 start 撤销了更新详情：%+v", result)
				}
				return
			}
			h.adapter.mu.Lock()
			defer h.adapter.mu.Unlock()
			for _, device := range h.adapter.devices {
				if action == "stream-then-stop" {
					if device.playing.Active {
						t.Fatal("旧 stream 恢复了已停止的播放关联")
					}
				} else if device.playerUA != "" || device.controlUA != "Control/2" {
					t.Fatalf("旧响应恢复了旧控制身份或播放器 UA：control=%q player=%q", device.controlUA, device.playerUA)
				}
			}
		})
	}
}

func TestPreloadStoppedMatchesCurrentPlayback(t *testing.T) {
	for _, phase := range []string{"queued", "signing"} {
		for _, status := range []int{http.StatusNoContent, http.StatusUnauthorized, http.StatusForbidden} {
			for _, change := range []string{"item", "source", "session", "transcode_session", "internal_source", "current", "current_optional_fields"} {
				t.Run(fmt.Sprintf("%s/%d/%s", phase, status, change), func(t *testing.T) {
					h := newEmbyPreloadHarness(t)
					h.nodes["20"] = preloadTestItem("20", "Episode")
					h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
					h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/1", nil)
					h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"})
					h.adapter.mu.Lock()
					var scope string
					for key := range h.adapter.devices {
						scope = key
					}
					h.adapter.mu.Unlock()

					blocked, entered, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
					release := sync.OnceFunc(func() { close(blocked) })
					t.Cleanup(release)
					if phase == "queued" {
						if !h.core.Submit(playback.PreloadCandidate{Key: "blocker", Scope: "blocker", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
							close(entered)
							<-blocked
							return playback.PreloadResult{Outcome: playback.PreloadCached}
						}}) {
							t.Fatal("queue blocker rejected")
						}
						<-entered
					}
					var published atomic.Bool
					if !h.core.Submit(playback.PreloadCandidate{Key: "next", Scope: scope, Instance: t.Name(), Kind: playback.PreloadNext, Run: func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, "next") {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						if phase == "signing" {
							close(entered)
							<-blocked
						}
						task.Publish(func() { published.Store(true) })
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}}) {
						t.Fatal("current successor rejected")
					}
					if phase == "signing" {
						<-entered
					}
					report := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"}
					switch change {
					case "item":
						report["ItemId"] = "19"
					case "source":
						report["MediaSourceId"] = "old-source"
					case "session", "transcode_session":
						report["PlaySessionId"] = "old-session"
						if change == "transcode_session" {
							report["PlayMethod"] = "Transcode"
						}
					case "internal_source":
						report["MediaSourceId"] = "source-20" + MediaSourceIdSegment
					case "current_optional_fields":
						delete(report, "MediaSourceId")
						delete(report, "PlaySessionId")
					}
					ignored := change != "current" && change != "current_optional_fields"
					raw, _ := json.Marshal(report)
					router := gin.New()
					router.Use(PreloadObserver())
					router.Any("/*path", func(c *gin.Context) {
						got, err := io.ReadAll(c.Request.Body)
						if err != nil || !bytes.Equal(got, raw) {
							t.Errorf("stop request body changed: err=%v", err)
						}
						c.Status(status)
						if status != http.StatusNoContent {
							_, _ = c.Writer.WriteString("original stop response")
						}
					})
					h.router = router
					route := "/Sessions/Playing/Stopped"
					if change == "source" {
						route = "/emby/Sessions/Playing/Stopped"
					} else if change == "session" {
						route = "/emby/SESSIONS/PLAYING/STOPPED"
					}
					response := h.request("POST", route, "device", "Fixture App", "Control/1", report)
					wantBody := "original stop response"
					if status == http.StatusNoContent {
						wantBody = ""
					}
					if response.Code != status || response.Body.String() != wantBody {
						t.Error("stop response changed")
					}
					release()
					if !h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
						close(finished)
						return playback.PreloadResult{Outcome: playback.PreloadCached}
					}}) {
						t.Fatal("completion barrier rejected")
					}
					select {
					case <-finished:
					case <-time.After(time.Second):
						t.Fatal("successor did not drain")
					}
					if published.Load() != ignored {
						t.Errorf("successor published=%v, ignored old stop=%v", published.Load(), ignored)
					}
					h.adapter.mu.Lock()
					for _, device := range h.adapter.devices {
						if device.playing.Active != ignored {
							t.Errorf("current playback active=%v, ignored old stop=%v", device.playing.Active, ignored)
						}
					}
					h.adapter.mu.Unlock()
					if h.metadata.Load() != 0 || len(h.resolved) != 0 {
						t.Fatal("stop issued unexpected metadata/provider work")
					}
				})
			}
		}
	}
}

func TestPreloadStoppedDuringCurrentStream(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusForbidden} {
		for _, change := range []string{"item", "source", "session", "transcode_session", "current", "current_optional_fields", "unknown_current"} {
			t.Run(fmt.Sprintf("%d/%s", status, change), func(t *testing.T) {
				h := newEmbyPreloadHarness(t)
				item := preloadTestItem("20", "Episode")
				item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
				h.nodes["20"] = item
				h.upstream = func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/Users/10/Items/21" {
						t.Errorf("unexpected next-item request %s", r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(preloadTestItem("21", "Episode"))
				}
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
				if change != "unknown_current" {
					h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"})
				}
				entered, blocked, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(blocked) })
				t.Cleanup(release)
				router := gin.New()
				router.Use(PreloadObserver())
				router.Any("/*path", func(c *gin.Context) {
					kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
					if kind == "stream" {
						close(entered)
						<-blocked
						c.Redirect(http.StatusTemporaryRedirect, "http://download.invalid/video")
						return
					}
					_, _ = io.ReadAll(c.Request.Body)
					if kind == "stop" {
						c.Status(status)
					} else {
						c.Status(http.StatusNoContent)
					}
				})
				h.router = router
				go func() {
					defer close(done)
					h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/current", nil)
				}()
				<-entered
				report := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"}
				switch change {
				case "item":
					report["ItemId"] = "19"
				case "source":
					report["MediaSourceId"] = "old-source"
				case "session", "transcode_session":
					report["PlaySessionId"] = "old-session"
					if change == "transcode_session" {
						report["PlayMethod"] = "Transcode"
					}
				case "current_optional_fields":
					delete(report, "MediaSourceId")
					delete(report, "PlaySessionId")
				}
				if response := h.request("POST", "/emby/Sessions/Playing/Stopped", "device", "Fixture App", "Control/1", report); response.Code != status {
					t.Fatal("stop response changed")
				}
				release()
				<-done
				ignored := change != "current" && change != "current_optional_fields" && change != "unknown_current"
				h.adapter.mu.Lock()
				for _, device := range h.adapter.devices {
					if ignored && (!device.playing.Active || device.playing.UA != "Player/current") {
						t.Errorf("old stop fenced current stream: active=%v ua=%q", device.playing.Active, device.playing.UA)
					} else if !ignored && device.playing.Active {
						t.Error("late stream restored stopped playback")
					}
				}
				h.adapter.mu.Unlock()
				if t.Failed() || !ignored {
					return
				}
				h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false, "NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "21"}}})
				if result := h.waitResolved(); result.ua != "Player/current" || !strings.Contains(result.path, "pickcode=21&") {
					t.Errorf("current stream did not prepare next item with its actual UA: %+v", result)
				}
				if h.metadata.Load() != 1 {
					t.Errorf("next-item metadata requests=%d", h.metadata.Load())
				}
			})
		}
	}
}

type preloadStoppedTestBody struct {
	io.Reader
	terminal error
	reads    int
	closes   int
}

func (b *preloadStoppedTestBody) Read(data []byte) (int, error) {
	b.reads++
	n, err := b.Reader.Read(data)
	if err == io.EOF && b.terminal != nil {
		err = b.terminal
	}
	return n, err
}

func (b *preloadStoppedTestBody) Close() error {
	b.closes++
	return nil
}

func TestPreloadStoppedBodyObservationPreservesOriginalReads(t *testing.T) {
	for _, mode := range []string{"unread", "partial", "error", "malformed", "exact_limit", "overflow", "empty"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			h.nodes["20"] = preloadTestItem("20", "Episode")
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/current", nil)
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"})
			h.adapter.mu.Lock()
			var before uint64
			for _, device := range h.adapter.devices {
				before = device.progressStarted
			}
			h.adapter.mu.Unlock()
			raw := []byte(`{"ItemId":"20","MediaSourceId":"source-20","PlaySessionId":"old"}`)
			if mode == "malformed" {
				raw = []byte(`{"ItemId":`)
			} else if mode == "exact_limit" || mode == "overflow" {
				limit := 64 << 10
				if mode == "overflow" {
					limit++
				}
				raw = append(raw, bytes.Repeat([]byte(" "), limit-len(raw))...)
			} else if mode == "empty" {
				raw = nil
			}
			body := &preloadStoppedTestBody{Reader: bytes.NewReader(raw)}
			readErr := errors.New("fixture body read failure")
			if mode == "error" {
				body.terminal = readErr
			}
			router := gin.New()
			router.Use(PreloadObserver())
			router.POST("/Sessions/Playing/Stopped", func(c *gin.Context) {
				if mode == "unread" || mode == "partial" {
					if mode == "partial" {
						part := make([]byte, 5)
						if _, err := io.ReadFull(c.Request.Body, part); err != nil || !bytes.Equal(part, raw[:5]) {
							t.Errorf("partial body read changed: err=%v", err)
						}
					}
					_ = c.Request.Body.Close()
				} else {
					got, _, err := https.ExtractReqBody(c.Request.Body)
					if mode == "error" {
						if !errors.Is(err, readErr) {
							t.Errorf("body read error changed: %v", err)
						}
					} else if err != nil || !bytes.Equal(got, raw) {
						t.Errorf("body bytes changed: err=%v", err)
					}
				}
				c.Header("X-Original", "preserved")
				c.String(http.StatusForbidden, "original denied response")
			})
			request := httptest.NewRequest("POST", "/Sessions/Playing/Stopped", body)
			request.Header.Set(HeaderFullAuthName, `MediaBrowser Client="Fixture App", DeviceId="device", Token="user-token"`)
			request.Header.Set("User-Agent", "Control/1")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || response.Body.String() != "original denied response" || response.Header().Get("X-Original") != "preserved" {
				t.Error("original response changed")
			}
			if body.closes != 1 || mode == "unread" && body.reads != 0 || mode == "partial" && body.reads != 1 {
				t.Errorf("observer changed original body reads/close: reads=%d closes=%d", body.reads, body.closes)
			}
			h.adapter.mu.Lock()
			for _, device := range h.adapter.devices {
				if device.progressStarted != before || !device.playing.Active || device.playing.UA != "Player/current" {
					t.Error("unproven or old stop changed current playback")
				}
			}
			h.adapter.mu.Unlock()
			if h.metadata.Load() != 0 || len(h.resolved) != 0 {
				t.Fatal("body observation started unexpected background work")
			}
		})
	}
}

func TestPreloadStoppedRechecksPlaybackAfterSlowBodyOrResponse(t *testing.T) {
	for _, phase := range []string{"body", "response"} {
		for _, status := range []int{http.StatusNoContent, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s/%d", phase, status), func(t *testing.T) {
				h := newEmbyPreloadHarness(t)
				h.nodes["20"], h.nodes["21"] = preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
				h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/old", nil)
				h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "old"})
				entered, blocked, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				reader, writer := io.Pipe()
				release := sync.OnceFunc(func() { close(blocked); _ = writer.Close() })
				t.Cleanup(func() { release(); _ = reader.Close() })
				router := gin.New()
				router.Use(PreloadObserver())
				router.Any("/*path", func(c *gin.Context) {
					kind, item, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
					if kind == "stop" {
						got, _, err := https.ExtractReqBody(c.Request.Body)
						if err != nil || string(got) != `{"ItemId":"20","MediaSourceId":"source-20","PlaySessionId":"old"}` {
							t.Errorf("slow stop body changed: err=%v", err)
						}
						if phase == "response" {
							close(entered)
							<-blocked
						}
						c.Status(status)
						return
					}
					if c.Request.Body != nil {
						_, _ = io.ReadAll(c.Request.Body)
					}
					switch kind {
					case "detail":
						c.JSON(http.StatusOK, h.nodes[item])
					case "stream":
						c.Redirect(http.StatusTemporaryRedirect, "http://download.invalid/video")
					default:
						c.Status(http.StatusNoContent)
					}
				})
				h.router = router
				go func() {
					_, _ = io.WriteString(writer, `{"ItemId":"20","MediaSourceId":"source-20","PlaySessionId":"old"}`)
					if phase == "body" {
						close(entered)
					} else {
						_ = writer.Close()
					}
				}()
				go func() {
					defer close(done)
					request := httptest.NewRequest("POST", "/Sessions/Playing/Stopped", reader)
					request.Header.Set(HeaderFullAuthName, `MediaBrowser Client="Fixture App", DeviceId="device", Token="user-token"`)
					request.Header.Set("User-Agent", "Control/1")
					response := httptest.NewRecorder()
					router.ServeHTTP(response, request)
					if response.Code != status {
						t.Error("slow stop status changed")
					}
				}()
				<-entered
				newPlayback := make(chan struct{})
				go func() {
					defer close(newPlayback)
					h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1", nil)
					h.request("GET", "/Videos/21/stream?MediaSourceId=source-21", "device", "Fixture App", "Player/current", nil)
					h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "21", "MediaSourceId": "source-21", "PlaySessionId": "current"})
				}()
				select {
				case <-newPlayback:
				case <-time.After(time.Second):
					release()
					<-newPlayback
					t.Error("slow stop held the shared playback lock")
				}
				release()
				<-done
				h.adapter.mu.Lock()
				for _, device := range h.adapter.devices {
					if !device.playing.Active || device.playing.Item.ID != "21" || device.playing.Session != "current" || device.playing.UA != "Player/current" {
						t.Error("old stop completion cleared newer playback")
					}
				}
				h.adapter.mu.Unlock()
				if h.metadata.Load() != 0 || len(h.resolved) != 0 {
					t.Fatal("slow body observation started unexpected background work")
				}
			})
		}
	}
}

func TestPreloadOutOfOrderSeasonMembersKeepLatestResponse(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	h.nodes["91"] = preloadItem{ID: "91", Type: "Season", SeriesID: "90"}
	h.request("GET", "/Users/10/Items/91", "device", "Emby Web", "Mozilla/test", nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	router := gin.New()
	router.Use(PreloadObserver())
	router.GET("/Shows/90/Episodes", func(c *gin.Context) {
		id := "21"
		if c.Query("older") == "true" {
			id = "20"
			close(entered)
			<-release
		}
		c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{preloadTestItem(id, "Episode")}})
	})
	h.router = router
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		defer close(done)
		h.request("GET", "/Shows/90/Episodes?SeasonId=91&older=true", "device", "Emby Web", "Mozilla/test", nil)
	}()
	<-entered
	h.request("GET", "/Shows/90/Episodes?SeasonId=91", "device", "Emby Web", "Mozilla/test", nil)
	close(release)
	<-done
	h.adapter.mu.Lock()
	defer h.adapter.mu.Unlock()
	for _, device := range h.adapter.devices {
		if !device.members["21"] || device.members["20"] {
			t.Fatalf("旧 Episodes 恢复了过期成员关系：%v", device.members)
		}
	}
}

func TestPreloadOldPlaybackSelectionCannotReplaceNewDetail(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	a, b := preloadTestItem("20", "Movie"), preloadTestItem("21", "Movie")
	b.MediaSources[0].ItemID = "20"
	a.MediaSources = append(a.MediaSources, b.MediaSources[0])
	h.nodes["20"] = a
	h.request("GET", "/Users/10/Items/20?MediaSourceId=source-20", "device", "Emby Web", "Mozilla/test", nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	router := gin.New()
	router.Use(PreloadObserver())
	router.Any("/*path", func(c *gin.Context) {
		if c.Request.Body != nil {
			_, _ = io.ReadAll(c.Request.Body)
		}
		if c.Request.Method == http.MethodPost {
			close(entered)
			<-release
			c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: a.MediaSources})
			return
		}
		c.JSON(http.StatusOK, a)
	})
	h.router = router
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	go func() {
		defer close(done)
		h.request("POST", "/Items/20/PlaybackInfo?MediaSourceId=source-20", "device", "Emby Web", "Mozilla/test", map[string]any{})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("旧 PI 未进入阻塞点")
	}
	h.request("GET", "/Users/10/Items/20?MediaSourceId=source-21", "device", "Emby Web", "Mozilla/test", nil)
	close(release)
	<-done
	h.adapter.mu.Lock()
	for _, device := range h.adapter.devices {
		if device.selected != "source-21" {
			t.Errorf("旧 PI 选项覆盖新详情明确选源：%q", device.selected)
		}
	}
	h.adapter.mu.Unlock()
	if result := h.waitResolved(); result.path != b.MediaSources[0].Path {
		t.Fatalf("没有准备最新明确源：%+v", result)
	}
}

func TestPreloadUAMappingUsesExactControlAndSurvivesShortState(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	for _, id := range []string{"20", "21", "22", "23"} {
		h.nodes[id] = preloadTestItem(id, "Movie")
	}
	h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1.0", nil)
	if result := h.waitResolved(); result.ua != "Control/1.0" {
		t.Fatalf("首次详情未使用当前完整UA：%q", result.ua)
	}
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/1.0", nil)
	// 详情／进度资料释放后，只保留轻量映射。
	h.adapter.mu.Lock()
	clear(h.adapter.devices)
	h.adapter.mu.Unlock()
	h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1.0", nil)
	if result := h.waitResolved(); result.ua != "Player/1.0" {
		t.Fatalf("短资料过期后未复用独立映射：%q", result.ua)
	}
	h.request("GET", "/Videos/21/stream?MediaSourceId=source-21", "device", "Fixture App", "Player/2.0", nil)
	h.request("GET", "/Users/10/Items/22", "device", "Fixture App", "Control/1.0", nil)
	if result := h.waitResolved(); result.ua != "Player/2.0" {
		t.Fatalf("播放器更新没有替换完整映射：%q", result.ua)
	}
	h.request("GET", "/Users/10/Items/23", "device", "Fixture App", "Control/2.0", nil)
	if result := h.waitResolved(); result.ua != "Control/2.0" {
		t.Fatalf("控制端版本变化继承了旧映射：%q", result.ua)
	}
	if h.metadata.Load() != 0 {
		t.Fatalf("UA学习产生额外元数据：%d", h.metadata.Load())
	}
}

func TestPreloadNextUsesActualSessionUADespiteControlUA(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	current := preloadTestItem("20", "Episode")
	current.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
	h.nodes["20"] = current
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/10/Items/21" {
			t.Errorf("意外后继查询：%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(preloadTestItem("21", "Episode"))
	}
	h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Mozilla/control", nil)
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Emby Web", "Player/2.0", nil)
	h.request("POST", "/Sessions/Playing", "device", "Emby Web", "Mozilla/control", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session"})
	h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Mozilla/control", map[string]any{
		"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "session", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false,
		"NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "21"}},
	})
	if result := h.waitResolved(); result.ua != "Player/2.0" || !strings.Contains(result.path, "pickcode=21&") {
		t.Fatalf("控制端UA覆盖当前真实会话UA：%+v", result)
	}
}

func TestPreloadFailedStreamDoesNotUpdateMapping(t *testing.T) {
	for _, mode := range []string{"provider-failed", "stable-resolver-fallback"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			h.nodes["20"], h.nodes["21"] = preloadTestItem("20", "Movie"), preloadTestItem("21", "Movie")
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/1", nil)
			router := gin.New()
			router.Use(PreloadObserver())
			router.GET("/Videos/20/stream", func(c *gin.Context) {
				if mode == "provider-failed" {
					c.Status(http.StatusServiceUnavailable)
				} else {
					c.Redirect(http.StatusTemporaryRedirect, "http://qms.invalid/115/newurl?userid=115-user&pickcode=20&force=1")
				}
			})
			router.GET("/Users/10/Items/21", func(c *gin.Context) { c.JSON(http.StatusOK, h.nodes["21"]) })
			h.router = router
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/2", nil)
			h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1", nil)
			if result := h.waitResolved(); result.ua != "Player/1" {
				t.Fatalf("失败或回退307覆盖了成功映射：%q", result.ua)
			}
		})
	}
}

func TestPreloadNaturalResponsesAssociateForSixtySeconds(t *testing.T) {
	for _, order := range []string{"detail-first", "playback-first"} {
		for _, age := range []time.Duration{30 * time.Second, 61 * time.Second} {
			t.Run(fmt.Sprintf("%s/%s", order, age), func(t *testing.T) {
				h := newEmbyPreloadHarness(t)
				item := preloadTestItem("20", "Movie")
				h.nodes["20"] = item
				h.nodes["20"] = preloadItem{ID: "20", Type: "Movie"}
				h.infos["20"] = preloadPlaybackInfo{MediaSources: item.MediaSources}
				first, second := func() { h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil) }, func() {
					h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", map[string]any{})
				}
				if order == "playback-first" {
					first, second = second, first
				}
				first()
				h.adapter.mu.Lock()
				for _, device := range h.adapter.devices {
					if order == "detail-first" {
						device.detailAt = time.Now().Add(-age)
					} else {
						device.pendingAt = time.Now().Add(-age)
					}
				}
				h.adapter.mu.Unlock()
				second()
				if age < time.Minute {
					if result := h.waitResolved(); result.path != item.MediaSources[0].Path {
						t.Fatalf("15–60秒自然资料未关联：%+v", result)
					}
				} else {
					h.core.Stop()
					h.adapter.mu.Lock()
					for _, device := range h.adapter.devices {
						if len(device.detail.MediaSources) != 0 {
							t.Error("超过60秒仍关联了旧资料")
						}
					}
					h.adapter.mu.Unlock()
					if len(h.resolved) != 0 {
						t.Fatal("过期资料触发了签名")
					}
				}
				if h.metadata.Load() != 0 {
					t.Fatalf("关联自然响应产生额外HTTP：%d", h.metadata.Load())
				}
			})
		}
	}
}

func TestPreloadPendingEpisodeFinalThirdGET(t *testing.T) {
	for _, mode := range []string{"final-source", "final-season-membership"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			parent := preloadItem{ID: "90", Type: "Series"}
			candidate := preloadTestItem("21", "Episode")
			if mode == "final-source" {
				candidate.MediaSources = nil
			} else {
				parent = preloadItem{ID: "91", Type: "Season", SeriesID: "90"}
				candidate.SeasonID, candidate.ParentID = "special", "special"
			}
			h.nodes[parent.ID] = parent
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/10/Items":
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{}})
				case "/Shows/NextUp", "/Shows/90/Episodes":
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{candidate}})
				case "/Users/10/Items/21":
					_ = json.NewEncoder(w).Encode(preloadTestItem("21", "Episode"))
				default:
					t.Errorf("不允许的最终补查：%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			if result := h.waitResolved(); !strings.Contains(result.path, "pickcode=21&") {
				t.Fatalf("最终普通GET未完成正确候选：%+v", result)
			}
			if h.metadata.Load() != 3 {
				t.Fatalf("最终GET额度不符：%d", h.metadata.Load())
			}
		})
	}
}

func TestPreloadPendingEpisodeDoesNotSpendThirdGETOnUnknownOrPlaybackInfo(t *testing.T) {
	for _, mode := range []string{"membership-and-source-missing", "playbackinfo-missing"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			parent := preloadItem{ID: "91", Type: "Season", SeriesID: "90"}
			candidate := preloadTestItem("21", "Episode")
			if mode == "membership-and-source-missing" {
				candidate.MediaSources, candidate.SeasonID, candidate.ParentID = nil, "special", "special"
			} else {
				parent = preloadItem{ID: "90", Type: "Series"}
				candidate.MediaSources[0].Path = ""
				candidate.MediaSources[0].Container = "mkv"
				candidate.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
				candidate.MediaSources[0].MediaStreams = []preloadStream{{Type: "Video", Codec: "h264"}, {Type: "Audio", Codec: "aac"}}
			}
			h.nodes[parent.ID] = parent
			observed := make(chan struct{})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/10/Items":
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{}})
				case "/Shows/NextUp":
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{candidate}})
					close(observed)
				default:
					t.Errorf("第三次用于未知分支或PlaybackInfo：%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			select {
			case <-observed:
			case <-time.After(4 * time.Second):
				t.Fatal("正常前两次关系GET未执行")
			}
			finished := make(chan struct{})
			h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
				close(finished)
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}})
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("缺资料作业没有释放worker")
			}
			if h.metadata.Load() != 2 || len(h.resolved) != 0 {
				t.Fatalf("不合格最终分支突破额度：HTTP=%d resolved=%d", h.metadata.Load(), len(h.resolved))
			}
		})
	}
}

func TestPreloadAuthorizedAccountHintsAllowTwoRealDetailJobs(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	entered, release := make(chan string, 2), make(chan struct{})
	defer close(release)
	h.adapter.options.Account = func(raw string) playback.PreloadAccount {
		parsed, _ := url.Parse(raw)
		return playback.PreloadAccount{Provider: "115", UserID: parsed.Query().Get("userid")}
	}
	h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
		return preloadHash(raw, ua), func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			entered <- raw
			select {
			case <-release:
			case <-ctx.Done():
			}
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		}, true
	}
	for _, id := range []string{"20", "21"} {
		item := preloadTestItem(id, "Movie")
		item.MediaSources[0].Path = "http://qms.invalid/115/newurl?userid=owner-" + id + "&pickcode=" + id
		h.nodes[id] = item
		h.request("GET", "/Users/10/Items/"+id, "device-"+id, "Fixture App", "Control/1", nil)
	}
	for range 2 {
		select {
		case <-entered:
		case <-time.After(2500 * time.Millisecond):
			t.Fatal("真实详情提交未携带可信账号hint，第二账号仍被串行阻挡")
		}
	}
}

func TestPreloadNaturalPlaybackUsesCurrentControlVersion(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	item := preloadTestItem("20", "Movie")
	h.nodes["20"] = preloadItem{ID: "20", Type: "Movie"}
	h.infos["20"] = preloadPlaybackInfo{MediaSources: item.MediaSources}
	h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
	h.adapter.mu.Lock()
	for scope, device := range h.adapter.devices {
		h.adapter.uas.Remember(preloadUAKey(preloadRequest{scope: scope}, device), "Old Player", 1)
	}
	h.adapter.mu.Unlock()
	h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/2", map[string]any{})
	if result := h.waitResolved(); result.ua != "Control/2" {
		t.Fatalf("新版本自然PlaybackInfo借用了旧控制UA映射：%q", result.ua)
	}
}

func TestPreloadOlderControlPlaybackCannotEnrichNewVersion(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	router := gin.New()
	router.Use(PreloadObserver())
	router.Any("/*path", func(c *gin.Context) {
		if c.Request.Body != nil {
			_, _ = io.ReadAll(c.Request.Body)
		}
		if c.Request.Method == http.MethodPost {
			close(entered)
			<-release
			c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: preloadTestItem("20", "Movie").MediaSources})
		} else {
			c.JSON(http.StatusOK, preloadItem{ID: "20", Type: "Movie"})
		}
	})
	h.router = router
	go func() {
		defer close(done)
		h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", map[string]any{})
	}()
	<-entered
	h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/2", nil)
	close(release)
	<-done
	h.adapter.mu.Lock()
	defer h.adapter.mu.Unlock()
	for _, device := range h.adapter.devices {
		if len(device.detail.MediaSources) != 0 || device.controlUA != "Control/2" {
			t.Fatalf("旧控制版本响应污染当前详情：UA=%q sources=%d", device.controlUA, len(device.detail.MediaSources))
		}
	}
}

func TestPreloadNextDoesNotReusePreviousPlaybackUA(t *testing.T) {
	for _, change := range []string{"item", "source", "session"} {
		t.Run(change, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			for _, id := range []string{"20", "21"} {
				item := preloadTestItem(id, "Episode")
				item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
				h.nodes[id] = item
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Old Player", nil)
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "old-session"})
			id, source, session := "20", "source-20", "old-session"
			switch change {
			case "item":
				id, source = "21", "source-21"
				h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1", nil)
			case "source":
				h.nodes["20"].MediaSources[0].Path = "http://qms.invalid/115/newurl?pickcode=changed&userid=115-user"
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			case "session":
				session = "new-session"
			}
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": id, "MediaSourceId": source, "PlaySessionId": session})
			h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", map[string]any{"ItemId": id, "MediaSourceId": source, "PlaySessionId": session, "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false})
			h.adapter.mu.Lock()
			for _, device := range h.adapter.devices {
				if device.playing.UA != "" {
					t.Errorf("新播放关联继承前次实际UA：%q", device.playing.UA)
				}
			}
			h.adapter.mu.Unlock()
			h.core.Stop()
			if h.metadata.Load() != 0 {
				t.Fatalf("本次尚无真实UA却查询后继：%d", h.metadata.Load())
			}
		})
	}
}

func TestPreloadActualItemDoesNotRequireEpisodeDetail(t *testing.T) {
	for _, entry := range []string{"series", "season", "autoplay", "episode-control"} {
		t.Run(entry, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			current := preloadTestItem("20", "Episode")
			current.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			detail := preloadItem{ID: "90", Type: "Series"}
			switch entry {
			case "season":
				detail = preloadItem{ID: "91", Type: "Season", SeriesID: "90"}
			case "autoplay":
				detail = preloadTestItem("19", "Episode")
			case "episode-control":
				detail = current
			}
			h.nodes[detail.ID] = detail
			h.infos[current.ID] = preloadPlaybackInfo{MediaSources: current.MediaSources}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/10/Items/20":
					_ = json.NewEncoder(w).Encode(current)
				case "/Shows/90/Episodes":
					if r.URL.Query().Get("StartItemId") != current.ID {
						t.Error("successor used the browsing detail as its anchor")
					}
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{current, preloadTestItem("21", "Episode")}})
				default:
					t.Errorf("unexpected metadata request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}
			h.request("GET", "/Users/10/Items/"+detail.ID, "device", "Fixture App", "Control/1", nil)
			if entry == "autoplay" {
				h.request("GET", "/Videos/19/stream?MediaSourceId=source-19&PlaySessionId=previous", "device", "Fixture App", "Player/old", nil)
				h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "19", "MediaSourceId": "source-19", "PlaySessionId": "previous"})
			}
			h.request("GET", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20&PlaySessionId=current", "device", "Fixture App", "Player/2.3 exact", nil)
			report := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current", "IsPaused": false, "PositionTicks": 10 * 60 * 10_000_000}
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", report)
			for range 10 {
				h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", report)
			}
			if h.metadata.Load() != 0 {
				t.Fatal("playback start or early heartbeats fetched metadata")
			}
			h.adapter.mu.Lock()
			for _, device := range h.adapter.devices {
				if device.detail.ID != detail.ID || device.playing.Item.ID != current.ID || device.playing.UA != "Player/2.3 exact" {
					t.Errorf("independent association: detail=%s playing=%s UA=%q", device.detail.ID, device.playing.Item.ID, device.playing.UA)
				}
			}
			h.adapter.mu.Unlock()
			if t.Failed() {
				return
			}
			report["PositionTicks"] = 54 * 60 * 10_000_000
			for range 10 {
				h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", report)
			}
			result := h.waitResolved()
			if !strings.Contains(result.path, "pickcode=21&") || result.ua != "Player/2.3 exact" {
				t.Fatalf("successor path=%s UA=%q", result.path, result.ua)
			}
			h.core.Stop()
			wantCalls := int32(2)
			if entry == "episode-control" {
				wantCalls = 1
			}
			if h.metadata.Load() != wantCalls || len(h.resolved) != 0 {
				t.Fatalf("metadata=%d want=%d extra resolutions=%d", h.metadata.Load(), wantCalls, len(h.resolved))
			}
		})
	}
}

func TestPreloadActualItemRetainsSourceAndSessionBoundaries(t *testing.T) {
	for _, condition := range []string{"ambiguous-source", "stale-playbackinfo", "different-session", "changed-source", "denied-item", "ordinary-movie", "replaced-playbackinfo", "start-before-stream"} {
		t.Run(condition, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			item := preloadTestItem("20", "Episode")
			item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
			h.infos["20"] = preloadPlaybackInfo{MediaSources: item.MediaSources}
			selected := "source-20"
			if condition == "ambiguous-source" {
				other := item.MediaSources[0]
				other.ID = "another-source"
				h.infos["20"] = preloadPlaybackInfo{MediaSources: []preloadSource{item.MediaSources[0], other}}
				selected = ""
			}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/10/Items/20":
					value := item
					if condition == "changed-source" {
						value.MediaSources = append([]preloadSource(nil), item.MediaSources...)
						value.MediaSources[0].Path += "-changed"
					} else if condition == "denied-item" {
						w.WriteHeader(http.StatusForbidden)
						return
					} else if condition == "ordinary-movie" {
						value.Type = "Movie"
					}
					_ = json.NewEncoder(w).Encode(value)
				case "/Shows/90/Episodes":
					if condition != "start-before-stream" {
						t.Error("unconfirmed or ordinary movie playback queried a successor")
					}
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{item, preloadTestItem("21", "Episode")}})
				default:
					t.Errorf("unexpected metadata request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", nil)
			if condition == "stale-playbackinfo" {
				h.adapter.mu.Lock()
				for _, device := range h.adapter.devices {
					device.pendingAt = time.Now().Add(-preloadAssociationTTL - time.Second)
				}
				h.adapter.mu.Unlock()
			}
			report := map[string]any{"ItemId": "20", "MediaSourceId": selected, "PlaySessionId": "current", "IsPaused": false, "PositionTicks": 54 * 60 * 10_000_000}
			if selected == "" {
				delete(report, "MediaSourceId")
			}
			start := func() { h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", report) }
			if condition == "start-before-stream" {
				start()
			}
			session := "current"
			if condition == "different-session" {
				session = "previous"
			}
			query := url.Values{"PlaySessionId": {session}}
			if selected != "" {
				query.Set("MediaSourceId", selected)
			}
			h.request("GET", "/Videos/20/stream?"+query.Encode(), "device", "Fixture App", "Player/2.3 exact", nil)
			if condition != "start-before-stream" {
				start()
			}
			if condition == "replaced-playbackinfo" {
				other := item.MediaSources[0]
				other.ID = "another-source"
				h.infos["20"] = preloadPlaybackInfo{MediaSources: []preloadSource{item.MediaSources[0], other}}
				h.request("GET", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", nil)
			}
			for range 10 {
				h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", report)
			}
			done := make(chan struct{})
			h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
				close(done)
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}})
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("preload work did not drain")
			}
			wantMetadata, wantResolved := int32(0), 0
			switch condition {
			case "changed-source", "denied-item", "ordinary-movie":
				wantMetadata = 1
			case "start-before-stream":
				wantMetadata, wantResolved = 2, 1
			}
			if h.metadata.Load() != wantMetadata || len(h.resolved) != wantResolved {
				t.Fatalf("metadata=%d resolutions=%d want=%d/%d", h.metadata.Load(), len(h.resolved), wantMetadata, wantResolved)
			}
		})
	}
}

func TestPreloadActualStreamSessionMatchesStart(t *testing.T) {
	for _, test := range []struct {
		name, query              string
		startFirst, missingStart bool
		oldStop, afterStop       bool
		wantSignature            bool
	}{
		{name: "old-session-before-start", query: "&PlaySessionId=old-session"},
		{name: "old-session-after-start", query: "&PlaySessionId=old-session", startFirst: true},
		{name: "new-session-before-start", query: "&PlaySessionId=new-session", wantSignature: true},
		{name: "new-session-after-start", query: "&PlaySessionId=new-session", startFirst: true, wantSignature: true},
		{name: "unknown-stream-session-before-start", wantSignature: true},
		{name: "unknown-stream-session-after-start", startFirst: true, wantSignature: true},
		{name: "known-stream-without-start-session", query: "&PlaySessionId=new-session", missingStart: true, wantSignature: true},
		{name: "new-stream-before-old-stop", query: "&PlaySessionId=new-session", oldStop: true, wantSignature: true},
		{name: "new-stream-after-stop-without-start", query: "&PlaySessionId=new-session", afterStop: true, wantSignature: true},
		{name: "coherent-duplicate-session", query: "&PlaySessionId=new-session&playsessionid=new-session", wantSignature: true},
		{name: "conflicting-session", query: "&PlaySessionId=new-session&PlaySessionId=old-session", startFirst: true},
		{name: "conflicting-case-session", query: "&PlaySessionId=new-session&playsessionid=old-session", startFirst: true},
		{name: "empty-session", query: "&PlaySessionId=", startFirst: true},
		{name: "control-character-session", query: "&PlaySessionId=new%0Asession", startFirst: true},
		{name: "oversized-session", query: "&PlaySessionId=" + strings.Repeat("s", 4097), startFirst: true},
		{name: "malformed-session-query", query: "&PlaySessionId=%ZZ", startFirst: true},
	} {
		for _, includeSession := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/progress-session=%t", test.name, includeSession), func(t *testing.T) {
				h := newEmbyPreloadHarness(t)
				current := preloadTestItem("20", "Episode")
				current.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
				h.nodes["20"] = current
				h.upstream = func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/Users/10/Items/21" {
						t.Errorf("unexpected successor metadata: %s", r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(preloadTestItem("21", "Episode"))
				}
				h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return preloadHash(raw, ua), func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, raw) ||
							!task.Publish(func() { h.resolved <- preloadResolved{raw, ua, force} }) {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}, true
				}
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
				stream := func(query, ua string) {
					t.Helper()
					request := httptest.NewRequest(http.MethodGet, "/Videos/20/stream?MediaSourceId=source-20"+query, nil)
					request.Header.Set(HeaderFullAuthName, `MediaBrowser Client="Fixture App", DeviceId="device", Token="user-token"`)
					request.Header.Set("User-Agent", ua)
					before := request.URL.RawQuery
					response := httptest.NewRecorder()
					h.router.ServeHTTP(response, request)
					if response.Code != http.StatusTemporaryRedirect || request.URL.RawQuery != before || request.UserAgent() != ua {
						t.Fatal("stream observation changed ordinary playback")
					}
				}
				report := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "old-session"}
				send := func(suffix string) {
					t.Helper()
					if response := h.request("POST", "/Sessions/Playing"+suffix, "device", "Fixture App", "Control/1", report); response.Code != http.StatusNoContent {
						t.Fatal("playing report response changed")
					}
				}
				stream("&PlaySessionId=old-session", "Player/old")
				send("")
				if test.afterStop {
					send("/Stopped")
				}
				if test.startFirst {
					report["PlaySessionId"] = "new-session"
					send("")
				}
				stream(test.query, "Player/current")
				if test.oldStop {
					send("/Stopped")
				}
				if !test.startFirst && !test.afterStop {
					report["PlaySessionId"] = "new-session"
					if test.missingStart {
						delete(report, "PlaySessionId")
					}
					send("")
				}
				if includeSession {
					report["PlaySessionId"] = "new-session"
				} else {
					delete(report, "PlaySessionId")
				}
				report["IsPaused"], report["PositionTicks"] = false, 54*60*10_000_000
				report["NowPlayingQueue"] = []map[string]string{{"Id": "20"}, {"Id": "21"}}
				for range 10 {
					send("/Progress")
				}
				done := make(chan struct{})
				if !h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
					close(done)
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}}) {
					t.Fatal("completion barrier rejected")
				}
				select {
				case <-done:
				case <-time.After(3 * time.Second):
					t.Fatal("successor did not drain")
				}
				want := 0
				if test.wantSignature {
					want = 1
				}
				if len(h.resolved) != want || h.metadata.Load() != int32(want) {
					t.Fatalf("actual stream session proof: signatures=%d metadata=%d want=%d", len(h.resolved), h.metadata.Load(), want)
				}
				if want == 1 && (<-h.resolved).ua != "Player/current" {
					t.Fatal("next preload borrowed the prior stream UA")
				}
			})
		}
	}
}

func TestPreloadCompleteReportInvalidatesBeforeUpstream(t *testing.T) {
	for _, test := range []struct{ action, phase string }{
		{"pause", "queued"}, {"pause", "preparing"}, {"pause", "signing"},
		{"stop", "queued"}, {"stop", "preparing"}, {"stop", "signing"},
		{"transcode", "signing"}, {"pause-optional", "signing"}, {"stop-optional", "signing"},
		{"old-item", "signing"}, {"old-source", "signing"}, {"old-session", "signing"},
		{"ongoing", "signing"}, {"unread", "signing"}, {"partial", "signing"},
		{"malformed", "signing"}, {"read-error", "signing"}, {"read-error-then-eof", "signing"}, {"overflow", "signing"},
		{"pause-resume", "preparing"}, {"stop-resume", "preparing"},
	} {
		t.Run(test.action+"/"+test.phase, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			item := preloadTestItem("20", "Episode")
			item.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			h.nodes["20"] = item
			entered, released := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			t.Cleanup(release)
			var signatures, publications atomic.Int32
			var gated atomic.Bool
			gated.Store(true)
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if test.phase == "preparing" && gated.CompareAndSwap(true, false) {
					close(entered)
					select {
					case <-released:
					case <-r.Context().Done():
						return
					}
				}
				_ = json.NewEncoder(w).Encode(preloadTestItem("21", "Episode"))
			}
			h.adapter.options.Resolve = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return preloadHash(raw, ua), func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, raw) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					signatures.Add(1)
					if test.phase == "signing" && gated.CompareAndSwap(true, false) {
						close(entered)
						<-released
					}
					task.Publish(func() {
						publications.Add(1)
						h.resolved <- preloadResolved{path: raw, ua: ua}
					})
					return playback.PreloadResult{Outcome: playback.PreloadPublished}
				}, true
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20&PlaySessionId=current", "device", "Fixture App", "Player/current", nil)
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"})
			if test.phase == "queued" {
				if !h.core.Submit(playback.PreloadCandidate{Key: "blocker", Scope: "blocker", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
					close(entered)
					<-released
					return playback.PreloadResult{Outcome: playback.PreloadCached}
				}}) {
					t.Fatal("queue blocker rejected")
				}
				<-entered
			}
			progress := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current", "IsPaused": false, "PositionTicks": 54 * 60 * 10_000_000, "NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "21"}}}
			h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", progress)
			if test.phase != "queued" {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("successor did not reach gated stage")
				}
			}
			progress["IsPaused"] = test.action != "ongoing"
			switch test.action {
			case "old-item":
				progress["ItemId"] = "19"
			case "old-source":
				progress["MediaSourceId"] = "old-source"
			case "old-session":
				progress["PlaySessionId"] = "old-session"
			case "pause-optional", "stop-optional":
				delete(progress, "PlaySessionId")
				delete(progress, "MediaSourceId")
			case "transcode":
				progress["IsPaused"], progress["PlayMethod"] = false, "Transcode"
			}
			raw, _ := json.Marshal(progress)
			raw = append(append([]byte(" \n"), raw...), []byte(" \n")...)
			if test.action == "malformed" {
				raw = []byte(`{"ItemId":`)
			} else if test.action == "overflow" {
				raw = append(raw, bytes.Repeat([]byte(" "), (64<<10)+1-len(raw))...)
			}
			body := &preloadStoppedTestBody{Reader: bytes.NewReader(raw)}
			if strings.HasPrefix(test.action, "read-error") {
				body.terminal = errors.New("fixture read error")
			}
			reportRead, reportReleased, reportDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			releaseReport := sync.OnceFunc(func() { close(reportReleased) })
			t.Cleanup(releaseReport)
			router := gin.New()
			router.Use(PreloadObserver())
			router.Any("/*path", func(c *gin.Context) {
				if c.Request.URL.Query().Get("hold") == "" {
					_, _ = io.ReadAll(c.Request.Body)
					c.Status(http.StatusNoContent)
					return
				}
				if test.action == "partial" {
					prefix := make([]byte, 5)
					_, _ = io.ReadFull(c.Request.Body, prefix)
				} else if test.action != "unread" {
					got, err := io.ReadAll(c.Request.Body)
					if !bytes.Equal(got, raw) || (err != nil) != strings.HasPrefix(test.action, "read-error") {
						t.Error("original report bytes/error changed")
					}
					if test.action == "read-error-then-eof" {
						body.terminal = nil
						if retried, err := io.ReadAll(c.Request.Body); err != nil || len(retried) != 0 {
							t.Error("read error retry changed original reader")
						}
					}
				}
				_ = c.Request.Body.Close()
				close(reportRead)
				<-reportReleased
				if strings.HasSuffix(test.action, "-resume") {
					c.String(http.StatusForbidden, "original denied response")
				} else {
					c.Status(http.StatusNoContent)
				}
			})
			h.router = router
			endpoint := "/Sessions/Playing/Progress?hold=true"
			if strings.HasPrefix(test.action, "stop") {
				endpoint = "/Sessions/Playing/Stopped?hold=true"
			}
			response := httptest.NewRecorder()
			request := httptest.NewRequest("POST", endpoint, body)
			request.Header.Set(HeaderFullAuthName, `MediaBrowser Client="Fixture App", DeviceId="device", Token="user-token"`)
			request.Header.Set("User-Agent", "Control/1")
			go func() { defer close(reportDone); router.ServeHTTP(response, request) }()
			select {
			case <-reportRead:
			case <-time.After(3 * time.Second):
				t.Fatal("report was not consumed")
			}
			release()
			barrier := make(chan struct{})
			if !h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
				close(barrier)
				return playback.PreloadResult{Outcome: playback.PreloadCached}
			}}) {
				t.Fatal("drain barrier rejected")
			}
			select {
			case <-barrier:
			case <-time.After(3 * time.Second):
				t.Fatal("successor did not drain")
			}
			cancel := strings.HasPrefix(test.action, "pause") || strings.HasPrefix(test.action, "stop") || test.action == "transcode"
			wantPublished, wantSigned := int32(1), int32(1)
			if cancel {
				wantPublished = 0
				if test.phase != "signing" {
					wantSigned = 0
				}
			}
			if signatures.Load() != wantSigned || publications.Load() != wantPublished {
				t.Errorf("before upstream response: signatures=%d publications=%d want=%d/%d", signatures.Load(), publications.Load(), wantSigned, wantPublished)
			}
			if strings.HasSuffix(test.action, "-resume") {
				progress["IsPaused"] = false
				if test.action == "stop-resume" {
					h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", progress)
				}
				h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", progress)
				if result := h.waitResolved(); result.ua != "Player/current" {
					t.Error("resume lost confirmed actual UA")
				}
			}
			releaseReport()
			<-reportDone
			wantStatus := http.StatusNoContent
			if strings.HasSuffix(test.action, "-resume") {
				wantStatus = http.StatusForbidden
			}
			if response.Code != wantStatus || body.closes != 1 || test.action == "unread" && body.reads != 0 || test.action == "partial" && body.reads != 1 {
				t.Error("report forwarding/read/close changed")
			}
			if strings.HasSuffix(test.action, "-resume") {
				h.adapter.mu.Lock()
				for _, device := range h.adapter.devices {
					if !device.playing.Active || device.playing.Paused || device.playing.UA != "Player/current" {
						t.Error("old denied response replaced resumed playback")
					}
				}
				h.adapter.mu.Unlock()
			}
		})
	}
}

func TestPreloadPausedProgressRechecksAfterSlowBody(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	h.nodes["20"], h.nodes["21"] = preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
	h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Videos/20/stream?MediaSourceId=source-20&PlaySessionId=old", "device", "Fixture App", "Player/old", nil)
	h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "old"})
	reader, writer := io.Pipe()
	release := sync.OnceFunc(func() { _ = writer.Close() })
	t.Cleanup(func() { release(); _ = reader.Close() })
	router := gin.New()
	router.Use(PreloadObserver())
	router.Any("/*path", func(c *gin.Context) {
		kind, item, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
		if c.Request.Body != nil {
			_, _ = io.ReadAll(c.Request.Body)
		}
		switch kind {
		case "detail":
			c.JSON(http.StatusOK, h.nodes[item])
		case "stream":
			c.Redirect(http.StatusTemporaryRedirect, "http://download.invalid/video")
		default:
			c.Status(http.StatusNoContent)
		}
	})
	h.router = router
	written, done := make(chan struct{}), make(chan struct{})
	go func() {
		_, _ = io.WriteString(writer, `{"ItemId":"20","MediaSourceId":"source-20","PlaySessionId":"old","IsPaused":true}`)
		close(written)
	}()
	go func() {
		defer close(done)
		request := httptest.NewRequest("POST", "/Sessions/Playing/Progress", reader)
		request.Header.Set(HeaderFullAuthName, `MediaBrowser Client="Fixture App", DeviceId="device", Token="user-token"`)
		request.Header.Set("User-Agent", "Control/1")
		router.ServeHTTP(httptest.NewRecorder(), request)
	}()
	<-written
	newPlayback := make(chan struct{})
	go func() {
		defer close(newPlayback)
		h.request("GET", "/Users/10/Items/21", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Videos/21/stream?MediaSourceId=source-21&PlaySessionId=current", "device", "Fixture App", "Player/current", nil)
		h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "21", "MediaSourceId": "source-21", "PlaySessionId": "current"})
	}()
	select {
	case <-newPlayback:
	case <-time.After(time.Second):
		release()
		<-newPlayback
		t.Error("passive report body held playback lock")
	}
	release()
	<-done
	h.adapter.mu.Lock()
	for _, device := range h.adapter.devices {
		if !device.playing.Active || device.playing.Item.ID != "21" || device.playing.UA != "Player/current" || device.playing.Session != "current" || device.playing.Paused {
			t.Error("late old pause body replaced current actual playback")
		}
	}
	h.adapter.mu.Unlock()
	if h.metadata.Load() != 0 || len(h.resolved) != 0 {
		t.Error("old pause body started background work")
	}
}

func TestPreloadLateOrdinaryProgressKeepsCurrentPlayback(t *testing.T) {
	for _, stage := range []string{"queued", "signing", "stream"} {
		for _, mismatch := range []string{"item", "source", "session"} {
			t.Run(stage+"/"+mismatch, func(t *testing.T) {
				h := newEmbyPreloadHarness(t)
				h.nodes["20"] = preloadTestItem("20", "Episode")
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
				h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current"})
				blocked, entered, drained := make(chan struct{}), make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(blocked) })
				t.Cleanup(release)
				var published atomic.Bool
				if stage == "stream" {
					router := gin.New()
					router.Use(PreloadObserver())
					router.Any("/*path", func(c *gin.Context) {
						if c.Request.Method == http.MethodGet {
							close(entered)
							<-blocked
							c.Redirect(http.StatusTemporaryRedirect, "http://download.invalid/video")
							return
						}
						_, _ = io.ReadAll(c.Request.Body)
						c.Status(http.StatusNoContent)
					})
					h.router = router
					go func() {
						defer close(drained)
						h.request("GET", "/Videos/20/stream?MediaSourceId=source-20&PlaySessionId=current", "device", "Fixture App", "Player/current", nil)
					}()
					<-entered
				} else {
					h.request("GET", "/Videos/20/stream?MediaSourceId=source-20&PlaySessionId=current", "device", "Fixture App", "Player/current", nil)
					if stage == "queued" {
						h.core.Submit(playback.PreloadCandidate{Key: "blocker", Scope: "blocker", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
							close(entered)
							<-blocked
							return playback.PreloadResult{Outcome: playback.PreloadCached}
						}})
						<-entered
					}
				}
				h.adapter.mu.Lock()
				var scope string
				var sequence uint64
				for key, device := range h.adapter.devices {
					scope, sequence = key, device.progressStarted
				}
				h.adapter.mu.Unlock()
				if stage != "stream" {
					if !h.core.Submit(playback.PreloadCandidate{Key: "current-next", Scope: scope, Kind: playback.PreloadNext, Run: func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, "current-next") {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						if stage == "signing" {
							close(entered)
							<-blocked
						}
						task.Publish(func() { published.Store(true) })
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}}) {
						t.Fatal("current successor rejected")
					}
					if stage == "signing" {
						<-entered
					}
				}
				report := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PlaySessionId": "current", "IsPaused": false}
				switch mismatch {
				case "item":
					report["ItemId"] = "19"
				case "source":
					report["MediaSourceId"] = "old-source"
				case "session":
					report["PlaySessionId"] = "old-session"
				}
				h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", report)
				h.adapter.mu.Lock()
				if h.adapter.devices[scope].progressStarted != sequence {
					t.Error("old ordinary progress advanced current playback sequence")
				}
				h.adapter.mu.Unlock()
				release()
				if stage == "stream" {
					<-drained
					h.adapter.mu.Lock()
					if h.adapter.devices[scope].playing.UA != "Player/current" {
						t.Error("old ordinary progress blocked current stream UA")
					}
					h.adapter.mu.Unlock()
				} else {
					h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
						close(drained)
						return playback.PreloadResult{Outcome: playback.PreloadCached}
					}})
					select {
					case <-drained:
					case <-time.After(time.Second):
						t.Fatal("current successor did not drain")
					}
					if !published.Load() {
						t.Error("old ordinary progress revoked current successor")
					}
				}
			})
		}
	}
}

type preloadLogOutput struct {
	mu      sync.Mutex
	data    bytes.Buffer
	changed chan struct{}
}

func (output *preloadLogOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	n, err := output.data.Write(data)
	output.mu.Unlock()
	select {
	case output.changed <- struct{}{}:
	default:
	}
	return n, err
}

func (output *preloadLogOutput) text() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	return output.data.String()
}

func TestPreloadLogsKeepFinalTargetAndMetadataFailure(t *testing.T) {
	for _, result := range []string{"success", "query-failed", "no-relation"} {
		t.Run(result, func(t *testing.T) {
			output := &preloadLogOutput{changed: make(chan struct{}, 1)}
			old := helpers.AppLogger
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(output, "", 0)}
			helpers.AppLogger.SetLevel(helpers.LogLevelDebug)
			t.Cleanup(func() { helpers.AppLogger = old })
			h := newEmbyPreloadHarness(t)
			season, episode := 1, 4
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if result == "query-failed" {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				items := []preloadItem{}
				if result == "success" {
					item := preloadTestItem("21", "Episode")
					item.Name, item.SeriesName, item.IndexNumber, item.ParentIndexNumber = "单集标题", "测试剧名", &episode, &season
					items = append(items, item)
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
			}
			h.adapter.options.Resolve = func(raw, _ string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return raw, func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, raw) || !task.Publish(func() {}) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					return playback.PreloadResult{Outcome: playback.PreloadPublished}
				}, true
			}
			h.nodes["90"] = preloadItem{ID: "90", Type: "Series", Name: "旧剧集页面标题"}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			want := "预加载完成"
			if result == "query-failed" {
				want = "Emby HTTP=503"
			} else if result == "no-relation" {
				want = "未能确定待播放的单集或分段"
			}
			deadline := time.NewTimer(4 * time.Second)
			defer deadline.Stop()
			for !strings.Contains(output.text(), want) {
				select {
				case <-output.changed:
				case <-deadline.C:
					t.Fatalf("missing %q in logs: %s", want, output.text())
				}
			}
			h.core.Stop()
			got := output.text()
			if result == "success" {
				for _, want := range []string{`目标="测试剧名 S01E04"`, "触发=剧集页待播集", "入口=Emby", "网盘=115"} {
					if !strings.Contains(got, want) {
						t.Errorf("missing %q: %s", want, got)
					}
				}
			} else if strings.Count(got, "跳过预加载") != 1 || strings.Contains(got, "本次未预加载") {
				t.Errorf("duplicated or generic skip logs: %s", got)
			}
			if result == "query-failed" && strings.Contains(got, "未能确定待播放") || result == "no-relation" && strings.Contains(got, "HTTP=") {
				t.Errorf("query failure confused with missing relation: %s", got)
			}
			if h.metadata.Load() != 1 && result == "success" || h.metadata.Load() != 2 && result != "success" {
				t.Errorf("logging changed metadata budget: %d", h.metadata.Load())
			}
		})
	}
}

func TestPreloadDisplayFieldsDoNotRestrictSource(t *testing.T) {
	var item preloadItem
	if err := json.Unmarshal([]byte(`{"Id":"20","Type":"Episode","Name":123,"SeriesName":{"unknown":true},"IndexNumber":1.5,"ParentIndexNumber":1,"MediaSources":[{"Id":"source","Path":"http://qms.invalid/115/newurl?pickcode=fixture"}]}`), &item); err != nil {
		t.Fatal("optional display schema blocked observation", err)
	}
	if !validPreloadItem(item) || preloadItemTitle(item) != "" {
		t.Fatal("optional display fields changed source eligibility or leaked non-text data")
	}
	if _, valid := selectPreloadSource(item, ""); !valid {
		t.Fatal("optional display fields made the existing unique source ambiguous")
	}
}
