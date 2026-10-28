package fnosproxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
)

type preloadTestSign struct{ url, ua string }

func preloadTestOptions(t *testing.T, root string) (Options, <-chan preloadTestSign) {
	t.Helper()
	preloader := playback.NewPreloader()
	preloader.Start(t.Context())
	preloader.Apply(true, 1)
	t.Cleanup(preloader.Stop)
	signs := make(chan preloadTestSign, 8)
	return Options{
		Preloader:    preloader,
		OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
		MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "https://cdn.115cdn.net/normal")
			w.WriteHeader(http.StatusFound)
		}),
		NewPreload: func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
			if force {
				t.Error("飞牛预加载被强制改成 direct")
			}
			return "prepared", func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
				if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}
				if !task.Publish(func() { signs <- preloadTestSign{raw, ua} }) {
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}
				return playback.PreloadResult{Outcome: playback.PreloadPublished}
			}, true
		},
	}, signs
}

func preloadTestFile(t *testing.T, root, name string) string {
	t.Helper()
	file := filepath.Join(root, name+".strm")
	if err := os.WriteFile(file, []byte("http://private-qms:12333/115/newurl?userid=owner&pickcode="+name+"&force=0"), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func preloadTestRequest(t *testing.T, base, method, endpoint, body string, native bool) []byte {
	t.Helper()
	request := preloadTestHTTP(t, base+endpoint, method, body, native)
	response, err := noRedirectClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		t.Fatalf("HTTP %d", response.StatusCode)
	}
	return result
}

func preloadTestHTTP(t *testing.T, target, method, body string, native bool) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("User-Agent", "Confirmed Player")
	if native {
		request.Header.Set("Cookie", "Trim-MC-token=native-session; auth_token=qms; csrf_token=qms")
		request.Header.Set("Authorization", "native-session")
		request.Header.Set("Authx", "untouched-native-signature")
	} else {
		request.Header.Set("X-Emby-Authorization", `MediaBrowser Token="valid", DeviceId="device"`)
	}
	return request
}

func waitPreloadSign(t *testing.T, signs <-chan preloadTestSign) preloadTestSign {
	t.Helper()
	select {
	case sign := <-signs:
		return sign
	case <-time.After(4 * time.Second):
		t.Fatal("合格详情没有进入取链回调")
		return preloadTestSign{}
	}
}

func TestPreloadClientIdentityCompatibility(t *testing.T) {
	inst := &instance{preloadID: "fixture"}
	request := func(description, ua string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/emby/Users/user/Items/movie", nil)
		r.Header.Set("X-Emby-Token", "valid")
		r.Header.Set("X-Emby-Authorization", description)
		r.Header.Set("User-Agent", ua)
		return r
	}
	for _, description := range []string{
		`Emby UserId=user,Client=Yamby,Device=Android Phone,DeviceId=opaque / Id_1,Version=2.1.1.0`,
		`Emby Client="Hills", Device="Phone", DeviceId="opaque / Id_1", Version="1.10.1"`,
	} {
		r := request(description, "Player/1.0")
		if device, valid := preloadDeviceID(r); !valid || device != "opaque / Id_1" {
			t.Fatal("播放器设备格式未被兼容")
		}
		if _, valid := inst.preloadScope(r, false); !valid {
			t.Fatal("合法播放器作用域被拒绝")
		}
		r.Header.Set("X-Emby-Device-Id", "different")
		if _, valid := inst.preloadScope(r, false); valid {
			t.Fatal("设备冲突被当成缺失后降级")
		}
	}
	weak, valid := inst.preloadScope(request(`Emby Client=Fixture`, "Player/1.0"), false)
	changed, _ := inst.preloadScope(request(`Emby Client=Fixture`, "Player/1.1"), false)
	strong, _ := inst.preloadScope(request(`Emby DeviceId=Player/1.0`, "Player/1.0"), false)
	if !valid || weak == changed || weak == strong {
		t.Fatal("缺设备的 UA 版本或显式设备身份未隔离")
	}
	for _, description := range []string{`Emby DeviceId=""`, `Emby DeviceId=A, DeviceId=B`, `Emby Client=A, Client=B`, `Emby DeviceId="unfinished`} {
		if _, valid := inst.preloadScope(request(description, "Player/1.0"), false); valid {
			t.Fatal("无效或冲突的明确身份被当成缺失")
		}
	}
}

func TestPreloadMissingDeviceUsesCurrentUAAndConfirmedNext(t *testing.T) {
	root := t.TempDir()
	items := map[string]map[string]any{}
	for _, id := range []string{"current", "next"} {
		items[id] = map[string]any{"Id": id, "Type": "Episode", "SeriesId": "series", "SeasonId": "season", "MediaSources": []any{map[string]any{
			"Id": "source-" + id, "Path": preloadTestFile(t, root, id), "RunTimeTicks": 60 * 60 * 10_000_000,
		}}}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/current":
			_ = json.NewEncoder(w).Encode(items["current"])
		case "/Shows/series/Episodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{items["current"], items["next"]}})
		case "/Sessions/Playing", "/Sessions/Playing/Progress":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("意外上游请求：%s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	options, _ := preloadTestOptions(t, root)
	signs := make(chan preloadTestSign, 4)
	options.NewPreload = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
		return raw, func(context.Context, *playback.PreloadTask) playback.PreloadResult {
			signs <- preloadTestSign{raw, ua}
			return playback.PreloadResult{Outcome: playback.PreloadCached}
		}, true
	}
	service, _, base := startService(t, upstream.URL, options)
	send := func(method, endpoint, body string) {
		r := preloadTestHTTP(t, base+endpoint, method, body, false)
		r.Header.Set("X-Emby-Authorization", `Emby Client=Fixture Player, Token=valid`)
		response, err := noRedirectClient().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
	}
	send(http.MethodGet, "/emby/Users/user/Items/current", "")
	if sign := waitPreloadSign(t, signs); sign.ua != "Confirmed Player" {
		t.Fatal("缺设备详情未使用本次完整 UA")
	}
	send(http.MethodGet, "/emby/Shows/series/Episodes?UserId=user&SeasonId=season", "")
	send(http.MethodGet, "/emby/Videos/current/stream?MediaSourceId=source-current&PlaySessionId=current", "")
	send(http.MethodPost, "/emby/Sessions/Playing", `{"ItemId":"current","MediaSourceId":"source-current","PlaySessionId":"current"}`)
	send(http.MethodPost, "/emby/Sessions/Playing/Progress", `{"ItemId":"current","MediaSourceId":"source-current","PlaySessionId":"current","IsPaused":false,"PositionTicks":32400000000}`)
	if sign := waitPreloadSign(t, signs); sign.ua != "Confirmed Player" || !strings.Contains(sign.url, "pickcode=next") {
		t.Fatal("缺设备的已确认播放未准备正确后继")
	}
	inst := service.current
	inst.preload.mu.Lock()
	defer inst.preload.mu.Unlock()
	for scope, device := range inst.preload.devices {
		if !device.weakIdentity || inst.preloadUAKey(scope, device) != "" {
			t.Fatal("规范化的固定客户端描述被当成真实设备")
		}
		strong := device
		strong.weakIdentity = false
		key := inst.preloadUAKey(scope, strong)
		if inst.preload.uas.Lookup(key, inst.preload.generation) != "" {
			t.Fatal("弱身份真实取流保存了跨次 UA")
		}
		inst.preload.uas.Remember(key, "Wrong/2.0", inst.preload.generation)
		if inst.preloadDetailUA(scope, device) != "Confirmed Player" {
			t.Fatal("弱身份读取了跨次映射")
		}
	}
}

func TestPreloadCompatibleDetailKeepsBytesAndUsesLatestSource(t *testing.T) {
	root := t.TempDir()
	learn := preloadTestFile(t, root, "learn")
	files := map[string]string{"a": preloadTestFile(t, root, "a"), "b": preloadTestFile(t, root, "b"), "c": preloadTestFile(t, root, "c")}
	var users, items atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/Users/Me":
			users.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user","Policy":{"EnableMediaPlayback":true}}`)
		case "/Users/user/Items/learn":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "learn", "MediaSources": []any{map[string]any{"Id": "source", "Path": learn}}})
		default:
			id := strings.TrimPrefix(r.URL.Path, "/Users/user/Items/")
			file, ok := files[id]
			if !ok {
				t.Errorf("额外接口：%s", r.URL.Path)
				http.NotFound(w, r)
				return
			}
			items.Add(1)
			encoded, _ := json.Marshal(map[string]any{"Id": id, "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}, "Unknown": json.RawMessage(`9007199254740993`)})
			_, _ = w.Write(append([]byte(" \n"), encoded...))
		}
	}))
	defer upstream.Close()
	options, signs := preloadTestOptions(t, root)
	var roots, origins atomic.Int64
	rootLookup, originLookup := options.OutputRoots, options.KnownOrigins
	options.OutputRoots = func(ctx context.Context) ([]string, error) { roots.Add(1); return rootLookup(ctx) }
	options.KnownOrigins = func(ctx context.Context) ([]string, error) { origins.Add(1); return originLookup(ctx) }
	_, _, base := startService(t, upstream.URL, options)
	preloadTestRequest(t, base, http.MethodGet, "/emby/Videos/learn/stream?MediaSourceId=source", "", false)
	roots.Store(0)
	origins.Store(0)
	for _, id := range []string{"a", "b", "c", "c", "c"} {
		body := preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+id, "", false)
		if !bytes.HasPrefix(body, []byte(" \n")) || !bytes.Contains(body, []byte("9007199254740993")) {
			t.Fatal("详情原始字节被改写")
		}
	}
	sign := waitPreloadSign(t, signs)
	if !strings.Contains(sign.url, "pickcode=c") || sign.ua != "Confirmed Player" {
		t.Fatalf("候选或 UA 不符：%+v", sign)
	}
	if users.Load() != 2 || items.Load() != 5 {
		t.Fatalf("重复详情产生额外补查：users=%d items=%d", users.Load(), items.Load())
	}
	if roots.Load() != 2 || origins.Load() != 2 {
		t.Fatalf("冷来源复核次数不符：roots=%d origins=%d", roots.Load(), origins.Load())
	}
	t.Logf("实际冷来源核验：OutputRoots=%d，KnownOrigins=%d；重复详情不增加回调", roots.Load(), origins.Load())
	select {
	case <-signs:
		t.Fatal("快速浏览产生多次取链")
	default:
	}
}

func TestPreloadNativeBridgeBothResponseOrders(t *testing.T) {
	for _, infoFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "详情后补源", true: "已有源后详情"}[infoFirst], func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "native")
			var bridges, users atomic.Int64
			const requestBody = "{ \"item_guid\": \"logical\", \"unknown\": [1,2] }\n"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case nativeStreamPath:
					payload := nativeStreamPayload(file, "http://private-qms:12333/115/newurl?userid=owner&pickcode=native&force=0")
					payload["data"].(map[string]any)["header"] = map[string]any{"User-Agent": []string{"Confirmed Player"}}
					_ = json.NewEncoder(w).Encode(payload)
				case "/v/api/v1/item/logical":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Episode","parent_guid":"season","can_play":1,"duration":0}}`)
				case "/v/api/v1/play/info":
					body, _ := io.ReadAll(r.Body)
					if string(body) != requestBody || r.Header.Get("Authx") != "untouched-native-signature" || r.Header.Get("Authorization") != "native-session" {
						t.Error("原生签名输入被修改")
					}
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","parent_guid":"season","type":"Episode","item":{"guid":"logical","type":"Episode","can_play":1}}}`)
				case "/Users/Me", "/Users/user/Items/file":
					if r.Header.Get("Authx") != "" || r.Header.Get("X-Emby-Token") != "native-session" || r.URL.Query().Get("api_key") != "native-session" || r.Header.Get("Accept-Encoding") != "identity" {
						t.Error("补源重放原生签名或使用错误凭据")
					}
					if !strings.Contains(r.Header.Get("X-Emby-Authorization"), `Token="native-session"`) {
						t.Error("补源缺少兼容客户端描述")
					}
					if _, err := r.Cookie("auth_token"); err == nil {
						t.Error("QMS Cookie 发往补源接口")
					}
					if r.URL.Path == "/Users/Me" {
						users.Add(1)
						_, _ = io.WriteString(w, `{"Id":"user"}`)
						return
					}
					bridges.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "Type": "Episode", "RunTimeTicks": 40000000, "MediaSources": []any{map[string]any{"Id": "logical", "Path": file, "RunTimeTicks": 2400000000}}})
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			var roots, origins atomic.Int64
			rootLookup, originLookup := options.OutputRoots, options.KnownOrigins
			options.OutputRoots = func(ctx context.Context) ([]string, error) { roots.Add(1); return rootLookup(ctx) }
			options.KnownOrigins = func(ctx context.Context) ([]string, error) { origins.Add(1); return originLookup(ctx) }
			service, _, base := startService(t, upstream.URL, options)
			preloadTestRequest(t, base, http.MethodPost, nativeStreamPath, `{}`, true)
			// 缓存只复用同凭据、文件身份和来源，不跳过每次根目录复核。
			request := preloadTestHTTP(t, base+"/v/api/v1/play/info", http.MethodPost, "{}", true)
			headers, token, valid := preloadCredentials(request, true)
			if !valid {
				t.Fatal("原生会话凭据无效")
			}
			request.Header = playbackMetadataHeaders(headers)
			request.URL.RawQuery = "api_key=" + token
			if _, ok := service.current.resolveSource(t.Context(), request, Source{ItemID: "file", ID: "logical", Path: file}); !ok {
				t.Fatal("无法预先建立来源缓存")
			}
			roots.Store(0)
			origins.Store(0)
			detail := func() { preloadTestRequest(t, base, http.MethodGet, "/v/api/v1/item/logical", "", true) }
			info := func() { preloadTestRequest(t, base, http.MethodPost, "/v/api/v1/play/info", requestBody, true) }
			if infoFirst {
				info()
				detail()
			} else {
				detail()
				info()
			}
			sign := waitPreloadSign(t, signs)
			if !strings.Contains(sign.url, "pickcode=native") || sign.ua != "Confirmed Player" || users.Load() != 1 || bridges.Load() != 1 {
				t.Fatalf("原生桥接失败或超出两次请求：sign=%+v users=%d bridge=%d", sign, users.Load(), bridges.Load())
			}
			if roots.Load() != 2 || origins.Load() != 1 {
				t.Fatalf("未命中同凭据来源缓存或省略根复核：roots=%d origins=%d", roots.Load(), origins.Load())
			}
			t.Logf("原生热来源核验：OutputRoots=%d，KnownOrigins=%d；桥接 GET=%d", roots.Load(), origins.Load(), users.Load()+bridges.Load())
		})
	}
}

func TestPreloadObservationRejectsUnknownSchemasAndPreservesEncodedBody(t *testing.T) {
	for _, test := range []struct {
		name, encoding string
		body           []byte
	}{
		{name: "HTML", body: []byte("<html>SPA</html>")},
		{name: "空 Id", body: []byte(`{"Id":"","Type":"Episode"}`)},
		{name: "错误业务码", body: []byte(`{"code":1,"data":{"guid":"logical","type":"Movie","can_play":1}}`)},
		{name: "未知压缩", encoding: "br", body: []byte(`{"Id":"movie","Type":"Movie"}`)},
		{name: "坏 gzip", encoding: "gzip", body: []byte("bad-gzip")},
		{name: "编码超限", body: bytes.Repeat([]byte("x"), metadataLimit+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			options, signs := preloadTestOptions(t, t.TempDir())
			service := NewService(options)
			inst, err := service.prepare(models.FnosProxyConfig{Enabled: true, FnosURL: "http://127.0.0.1:12345"})
			if err != nil {
				t.Fatal(err)
			}
			defer inst.closeResources()
			request := httptest.NewRequest(http.MethodGet, "/Users/user/Items/movie", nil)
			request.Header.Set("X-Emby-Authorization", `MediaBrowser Token="valid", DeviceId="device"`)
			response := &http.Response{Request: request, StatusCode: 200, Header: http.Header{"Content-Encoding": {test.encoding}}, Body: io.NopCloser(bytes.NewReader(test.body))}
			if err := inst.observePreloadResponse(response); err != nil {
				t.Fatal(err)
			}
			actual, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || !bytes.Equal(actual, test.body) || response.Header.Get("Content-Encoding") != test.encoding {
				t.Fatal("观察改变了回退正文或编码")
			}
			select {
			case <-signs:
				t.Fatal("未知 schema 触发取链")
			default:
			}
		})
	}
}

func TestPreloadDecodeCompressedLimit(t *testing.T) {
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	_, _ = writer.Write(bytes.Repeat([]byte("x"), metadataLimit+1))
	_ = writer.Close()
	if _, ok := decodeMetadata(encoded.Bytes(), "gzip"); ok {
		t.Fatal("解压后超过限额仍被接受")
	}
}

// 样本来自实施前本地 STRM 实测：原生服务 0.8.43 / Web 0.9.8；仅替换本地文件路径。
func preloadRelationFixture(t *testing.T, label string) map[string]any {
	t.Helper()
	body, err := os.ReadFile("testdata/preload_relations.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]map[string]any
	if err := json.Unmarshal(body, &fixtures); err != nil {
		t.Fatal(err)
	}
	fixture, ok := fixtures[label]
	if !ok {
		t.Fatalf("缺少协议样本 %s", label)
	}
	return fixture
}

func preloadQueueFixture(t *testing.T, root string) (map[string]any, []map[string]any) {
	t.Helper()
	payload := preloadRelationFixture(t, "fnos-compat-start-actual-id")
	var items []map[string]any
	for index, raw := range payload["Items"].([]any) {
		item := raw.(map[string]any)
		source := item["MediaSources"].([]any)[0].(map[string]any)
		source["Path"] = preloadTestFile(t, root, []string{"current", "next"}[index])
		items = append(items, item)
	}
	return payload, items
}

func preloadSeedPlayer(t *testing.T, inst *instance, native bool, actual ...Source) {
	t.Helper()
	request := preloadTestHTTP(t, "http://proxy.invalid/", http.MethodGet, "", native)
	if native {
		inst.rememberNativePlayer(request, request.UserAgent(), "")
	} else {
		source := Source{}
		if len(actual) > 0 {
			source = actual[0]
			request.URL.Path = "/emby/Videos/" + source.ItemID + "/stream"
			inst.beginPreloadRequest(request)
		}
		inst.rememberCompatiblePlayer(request, source)
	}
}

func waitPreloadWorker(t *testing.T, preloader *playback.Preloader) {
	t.Helper()
	done := make(chan struct{})
	key := fingerprint([]any{t.Name(), time.Now().UnixNano()})
	if !preloader.Submit(playback.PreloadCandidate{Key: key, Scope: key, Kind: playback.PreloadNext,
		Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
			close(done)
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		}}) {
		t.Fatal("无法建立 worker 同步点")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("worker 未结束当前工作")
	}
}

func TestPreloadRepeatedNaturalSourceOnlyEnrichesChangedEvidence(t *testing.T) {
	root := t.TempDir()
	first, second := preloadTestFile(t, root, "first"), preloadTestFile(t, root, "second")
	var currentPath atomic.Value
	currentPath.Store(first)
	var users, factories atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/Me" {
			users.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": currentPath.Load().(string)}}})
	}))
	defer upstream.Close()
	options, _ := preloadTestOptions(t, root)
	prepared := make(chan struct{}, 8)
	options.NewPreload = func(string, string, bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
		factories.Add(1)
		return "source", func(context.Context, *playback.PreloadTask) playback.PreloadResult {
			prepared <- struct{}{}
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		}, true
	}
	service, _, base := startService(t, upstream.URL, options)
	inst := service.current
	preloadSeedPlayer(t, inst, false)
	detail := func() { preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/movie", "", false) }
	detail()
	select {
	case <-prepared:
	case <-time.After(4 * time.Second):
		t.Fatal("初始准备未发生")
	}
	waitPreloadWorker(t, options.Preloader)
	request := preloadTestHTTP(t, base+"/emby/Items/movie/PlaybackInfo", http.MethodPost, "{}", false)
	observe := func(file string) {
		currentPath.Store(file)
		inst.beginPreloadRequest(request)
		inst.observeCompatibleSource(request, "movie", []Source{{ItemID: "movie", ID: "source", Path: file}}, map[string]any{})
	}
	for range 100 {
		observe(first)
		detail()
	}
	waitPreloadWorker(t, options.Preloader)
	if factories.Load() != 1 || users.Load() != 1 {
		t.Fatalf("相同响应重开机会：factory=%d user=%d", factories.Load(), users.Load())
	}
	observe(second)
	select {
	case <-prepared:
	case <-time.After(4 * time.Second):
		t.Fatal("真正变化的源资料没有获得一次补齐机会")
	}
	waitPreloadWorker(t, options.Preloader)
	for range 100 {
		observe(second)
		detail()
	}
	waitPreloadWorker(t, options.Preloader)
	if factories.Load() != 2 || users.Load() != 1 {
		t.Fatalf("新增资料没有保持一次性：factory=%d user=%d", factories.Load(), users.Load())
	}
}

func TestPreloadTrustedOriginPrefixIsNormalizedForProvider(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "prefix.strm")
	if err := os.WriteFile(file, []byte("http://private-qms:12333/prefix/115/newurl?userid=owner&pickcode=prefix"), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/Me" {
			_, _ = io.WriteString(w, `{"Id":"user"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
	}))
	defer upstream.Close()
	options, signs := preloadTestOptions(t, root)
	var origins atomic.Int64
	options.KnownOrigins = func(context.Context) ([]string, error) {
		origins.Add(1)
		return []string{"http://private-qms:12333/prefix"}, nil
	}
	factory := options.NewPreload
	options.NewPreload = func(raw, ua string, direct bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
		if !strings.HasPrefix(raw, "http://private-qms:12333/115/newurl?") {
			t.Errorf("来源前缀未被规范化：%s", raw)
			return "", nil, false
		}
		return factory(raw, ua, direct)
	}
	service, _, base := startService(t, upstream.URL, options)
	preloadSeedPlayer(t, service.current, false)
	request := preloadTestHTTP(t, base+"/emby/Users/user/Items/movie?api_key=valid", http.MethodGet, "", false)
	if _, ok := service.current.resolveSource(t.Context(), request, Source{ItemID: "movie", ID: "source", Path: file}); !ok {
		t.Fatal("无法预先建立兼容来源缓存")
	}
	origins.Store(0)
	preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/movie", "", false)
	if sign := waitPreloadSign(t, signs); strings.Contains(sign.url, "/prefix/") {
		t.Fatal("内部回调拿到了外部部署前缀")
	}
	if origins.Load() != 1 {
		t.Fatalf("兼容热来源未复用缓存：KnownOrigins=%d", origins.Load())
	}
}

func TestPreloadNativeFixedProxyCanPrepareFirstDetail(t *testing.T) {
	previousDB, previousSettings := db.Db, models.SettingsGlobal
	pool, err := db.OpenMaintenanceSQL(sqlite.DriverName, filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(sqlite.Dialector{Conn: pool}, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = pool.Close()
		t.Fatal(err)
	}
	db.Db = database
	if err := database.AutoMigrate(&models.Settings{}); err != nil {
		t.Fatal(err)
	}
	stored := models.Settings{LocalProxy: 1}
	if err := database.Create(&stored).Error; err != nil {
		t.Fatal(err)
	}
	models.LoadSettings()
	t.Cleanup(func() {
		stored.LocalProxy = 0
		if err := database.Model(&stored).Update("local_proxy", 0).Error; err != nil {
			t.Error(err)
		}
		models.LoadSettings()
		models.SettingsGlobal = previousSettings
		db.Db = previousDB
		_ = pool.Close()
	})
	root := t.TempDir()
	file := preloadTestFile(t, root, "first-proxy")
	var users, bridges atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v/api/v1/item/logical":
			_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Movie","can_play":1}}`)
		case "/v/api/v1/play/info":
			_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
		case "/Users/Me":
			users.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/file":
			bridges.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "MediaSources": []any{map[string]any{"Id": "logical", "Path": file}}})
		default:
			t.Errorf("首项 proxy 意外接口 %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	options, signs := preloadTestOptions(t, root)
	_, _, base := startService(t, upstream.URL, options)
	preloadTestRequest(t, base, http.MethodGet, "/v/api/v1/item/logical", "", true)
	preloadTestRequest(t, base, http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"logical"}`, true)
	if sign := waitPreloadSign(t, signs); sign.ua != "Confirmed Player" || users.Load() != 1 || bridges.Load() != 1 {
		t.Fatalf("固定 proxy 首项被播放器学习阻挡：%+v users=%d bridges=%d", sign, users.Load(), bridges.Load())
	}
}

func TestPreloadSlowOldDetailCannotReplaceLatestResponse(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"old": preloadTestFile(t, root, "old"), "latest": preloadTestFile(t, root, "latest")}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/Me" {
			_, _ = io.WriteString(w, `{"Id":"user"}`)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/Users/user/Items/")
		if id == "old" {
			close(entered)
			<-release
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": files[id]}}})
	}))
	defer upstream.Close()
	options, signs := preloadTestOptions(t, root)
	service, _, base := startService(t, upstream.URL, options)
	preloadSeedPlayer(t, service.current, false)
	go func() {
		defer close(finished)
		preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/old", "", false)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("旧详情请求没有抵达")
	}
	preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/latest", "", false)
	close(release)
	<-finished
	if sign := waitPreloadSign(t, signs); !strings.Contains(sign.url, "pickcode=latest") {
		t.Fatalf("慢返回的旧页覆盖最新页：%+v", sign)
	}
}

func TestPreloadLifecycleCancelsInflightMetadata(t *testing.T) {
	for _, apply := range []bool{false, true} {
		t.Run(map[bool]string{false: "Stop", true: "Apply"}[apply], func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "movie")
			entered, canceled := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Users/Me" {
					close(entered)
					<-r.Context().Done()
					close(canceled)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, config, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/movie", "", false)
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("后台补查没有开始")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if apply {
				config.PathMappings = []models.FnosPathMapping{{FnosRoot: "/changed", LocalRoot: root}}
				if err := service.Apply(ctx, config); err != nil {
					t.Fatal(err)
				}
			} else if err := service.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("实例失效后元数据请求仍未取消")
			}
			waitPreloadWorker(t, options.Preloader)
			select {
			case <-signs:
				t.Fatal("旧实例在停止/替换后发布")
			default:
			}
		})
	}
}

func TestPreloadCompatibleSeriesSeasonUsesUniqueObservedResume(t *testing.T) {
	for _, kind := range []string{"Series", "Season"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			series, season := stringField(items[0], "SeriesId"), stringField(items[0], "SeasonId")
			id := series
			if kind == "Season" {
				id = season
			}
			var users atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					users.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				case "/Users/user/Items/" + id:
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Type": kind, "SeriesId": series})
				default:
					t.Errorf("剧季页产生额外接口：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+id, "", false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes?SortBy=IndexNumber&SortOrder=Ascending", "", false)
			sign := waitPreloadSign(t, signs)
			if !strings.Contains(sign.url, "pickcode=next") || users.Load() != 1 {
				t.Fatalf("没有采用真实样本中唯一未看完的 E2：%+v users=%d", sign, users.Load())
			}
		})
	}
}

func TestPreloadObservedQueueDoesNotUseAnotherUsersProgress(t *testing.T) {
	for _, queueFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "详情后队列", true: "队列后详情"}[queueFirst], func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			series := stringField(items[0], "SeriesId")
			var users atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					users.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + series:
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": series, "Type": "Series"})
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					t.Errorf("跨用户观察发起额外请求 %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, false)
			detail := func() { preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+series, "", false) }
			list := func() {
				preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes?UserId=another-user", "", false)
			}
			if queueFirst {
				list()
				detail()
			} else {
				detail()
				list()
			}
			waitPreloadWorker(t, options.Preloader)
			if users.Load() != 0 {
				t.Fatal("其他用户的续播关系触发了后台补查")
			}
			select {
			case <-signs:
				t.Fatal("把另一个用户的续播当作当前用户候选")
			default:
			}
		})
	}
}

func TestPreloadNativeTVSeasonSelectsNaturalPlayInfo(t *testing.T) {
	for _, kind := range []string{"TV", "Season"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "selected")
			detail := preloadRelationFixture(t, "fnos-native-detail-"+kind)
			info := preloadRelationFixture(t, "fnos-native-playinfo-episode")
			source := preloadRelationFixture(t, "fnos-native-to-compat-file")
			source["MediaSources"].([]any)[0].(map[string]any)["Path"] = file
			itemID := stringField(detail["data"].(map[string]any), "guid")
			var users, bridges atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v/api/v1/item/" + itemID:
					_ = json.NewEncoder(w).Encode(detail)
				case "/v/api/v1/play/info":
					var request map[string]any
					_ = json.NewDecoder(r.Body).Decode(&request)
					if request["item_guid"] != itemID {
						t.Error("play/info 未保留原剧/季请求")
					}
					_ = json.NewEncoder(w).Encode(info)
				case "/Users/Me":
					users.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + stringField(source, "Id"):
					bridges.Add(1)
					_ = json.NewEncoder(w).Encode(source)
				default:
					t.Errorf("意外接口 %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, true)
			preloadTestRequest(t, base, http.MethodGet, "/v/api/v1/item/"+itemID, "", true)
			preloadTestRequest(t, base, http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"`+itemID+`"}`, true)
			sign := waitPreloadSign(t, signs)
			if !strings.Contains(sign.url, "pickcode=selected") || users.Load() != 1 || bridges.Load() != 1 {
				t.Fatalf("原生剧/季未复用明确选集：%+v users=%d bridges=%d", sign, users.Load(), bridges.Load())
			}
		})
	}
}

func TestPreloadCompatibleProgressUsesProvenQueueAndSourceDuration(t *testing.T) {
	root := t.TempDir()
	queue, items := preloadQueueFixture(t, root)
	series, current, source := stringField(items[0], "SeriesId"), stringField(items[0], "Id"), stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
	var users atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/Me":
			users.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/" + current:
			_ = json.NewEncoder(w).Encode(items[0])
		case "/Shows/" + series + "/Episodes":
			_ = json.NewEncoder(w).Encode(queue)
		case "/Sessions/Playing", "/Sessions/Playing/Progress":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("进度不应主动查列表/媒体：%s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	options, signs := preloadTestOptions(t, root)
	service, _, base := startService(t, upstream.URL, options)
	preloadSeedPlayer(t, service.current, false, itemSources(current, items[0])...)
	preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
	preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
	report := `{"ItemId":"` + current + `","MediaSourceId":"` + source + `","PlaySessionId":"session","IsPaused":false,"RunTimeTicks":40000000,"PositionTicks":2060000000}`
	preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing", report, false)
	for range 100 {
		preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
	}
	sign := waitPreloadSign(t, signs)
	if !strings.Contains(sign.url, "pickcode=next") || users.Load() != 1 {
		t.Fatalf("没有依据 240s 选中源时长预加载下一项：%+v users=%d", sign, users.Load())
	}
	waitPreloadWorker(t, options.Preloader)
	select {
	case <-signs:
		t.Fatal("重复心跳多次取链")
	default:
	}
}

func TestPreloadProgressRevokesQueuedAndActiveNext(t *testing.T) {
	for _, action := range []string{"pause", "unknown-pause", "stop", "new-session", "new-source", "new-source-path", "seek", "transcode", "denied"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			if action == "new-source" {
				items[0]["MediaSources"] = append(items[0]["MediaSources"].([]any), map[string]any{"Id": "new-source", "Path": preloadTestFile(t, root, "new-source")})
			}
			entered, canceled := make(chan struct{}), make(chan struct{})
			var users, factories atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					if users.Add(1) != 1 {
						if strings.HasPrefix(action, "new-source") && users.Load() == 2 {
							_, _ = io.WriteString(w, `{"Id":"user"}`)
							return
						}
						t.Error("撤销后仍重试元数据")
						w.WriteHeader(http.StatusForbidden)
						return
					}
					close(entered)
					<-r.Context().Done()
					close(canceled)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					if r.URL.Query().Get("denied") == "true" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer upstream.Close()
			options, _ := preloadTestOptions(t, root)
			options.NewPreload = func(string, string, bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				factories.Add(1)
				return "", nil, false
			}
			service, _, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, false, itemSources(current, items[0])...)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "session", "IsPaused": false, "PositionTicks": 2060000000,
				"RunTimeTicks": items[0]["MediaSources"].([]any)[0].(map[string]any)["RunTimeTicks"]}
			send := func(route string) {
				body, _ := json.Marshal(report)
				preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing"+route, string(body), false)
			}
			send("")
			send("/Progress")
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("合格进度没有进入受限补查")
			}
			switch action {
			case "pause":
				report["IsPaused"] = true
				send("/Progress")
			case "unknown-pause":
				delete(report, "IsPaused")
				send("/Progress")
			case "stop":
				send("/Stopped")
			case "new-session":
				report["PlaySessionId"] = "new-session"
				send("")
			case "new-source", "new-source-path":
				selected := "new-source"
				if action == "new-source-path" {
					selected = source
					items[0]["MediaSources"].([]any)[0].(map[string]any)["Path"] = preloadTestFile(t, root, "changed-path")
				}
				preloadTestRequest(t, base, http.MethodGet, "/emby/Videos/"+current+"/stream?MediaSourceId="+selected, "", false)
				service.current.preload.mu.Lock()
				for _, device := range service.current.preload.devices {
					if device.playingID != "" || device.session != "" {
						t.Error("真实新源仍保留旧播放关联")
					}
				}
				service.current.preload.mu.Unlock()
			case "seek":
				report["PositionTicks"] = 10000000
				send("/Progress")
			case "transcode":
				report["PlayMethod"] = "Transcode"
				send("/Progress")
			case "denied":
				body, _ := json.Marshal(report)
				request := preloadTestHTTP(t, base+"/emby/Sessions/Playing/Progress?denied=true", http.MethodPost, string(body), false)
				response, err := noRedirectClient().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if response.StatusCode != http.StatusForbidden {
					t.Fatal("拒绝响应被改写")
				}
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("失效进度未取消在途下一项")
			}
			waitPreloadWorker(t, options.Preloader)
			wantUsers := int64(1)
			if strings.HasPrefix(action, "new-source") {
				wantUsers = 2
			}
			if users.Load() != wantUsers || factories.Load() != 0 {
				t.Fatalf("取消后继续补查或准备签名：users=%d factory=%d", users.Load(), factories.Load())
			}
		})
	}
}

func TestPreloadOutOfOrderProgressKeepsLatestIntent(t *testing.T) {
	for _, action := range []string{"progress-then-pause", "progress-then-stop", "start-then-stop"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			entered, release := make(chan struct{}), make(chan struct{})
			var users atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					users.Add(1)
					w.WriteHeader(http.StatusForbidden)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					if r.URL.Query().Get("older") == "true" {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer upstream.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, false, itemSources(current, items[0])...)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "session", "IsPaused": false, "PositionTicks": 2060000000}
			body, _ := json.Marshal(report)
			preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing", string(body), false)
			olderRoute := "/emby/Sessions/Playing/Progress?older=true"
			if action == "start-then-stop" {
				olderRoute = "/emby/Sessions/Playing?older=true"
			}
			request := preloadTestHTTP(t, base+olderRoute, http.MethodPost, string(body), false)
			done := make(chan error, 1)
			go func() {
				response, err := noRedirectClient().Do(request)
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("旧请求未进入真实上游阻塞点")
			}
			if action == "progress-then-pause" {
				report["IsPaused"] = true
				paused, _ := json.Marshal(report)
				preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing/Progress", string(paused), false)
			} else {
				preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing/Stopped", string(body), false)
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("旧请求未正常返回")
			}
			if action == "start-then-stop" {
				preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing/Progress", string(body), false)
			}
			waitPreloadWorker(t, options.Preloader)
			if users.Load() != 0 {
				t.Fatalf("旧响应恢复了已暂停或停止的后继预加载：users=%d", users.Load())
			}
			select {
			case <-signs:
				t.Fatal("旧响应恢复了已撤销的后继签名")
			default:
			}
		})
	}
}

func TestPreloadNoNextForMovieOrUnidentifiedSession(t *testing.T) {
	for _, movie := range []bool{false, true} {
		t.Run(map[bool]string{false: "未确认实际取流", true: "普通电影"}[movie], func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
			item := items[0]
			if movie {
				current = "movie"
				item = map[string]any{"Id": current, "Type": "Movie", "MediaSources": items[0]["MediaSources"]}
			}
			var users atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					users.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(item)
				case "/Users/user/Items/" + series:
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": series, "Type": "Series"})
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			if movie {
				preloadSeedPlayer(t, service.current, false, itemSources(current, item)...)
			} else {
				preloadSeedPlayer(t, service.current, false)
			}
			preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			session := ""
			if movie {
				session = "session"
			}
			report := `{"ItemId":"` + current + `","PlaySessionId":"` + session + `","IsPaused":false,"PositionTicks":2060000000}`
			preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing", report, false)
			preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
			waitPreloadWorker(t, options.Preloader)
			if users.Load() != 0 {
				t.Fatal("没有可靠剧集关联仍发生补查")
			}
			select {
			case <-signs:
				t.Fatal("普通电影或无会话进度触发了后继")
			default:
			}
			if !movie {
				for _, raw := range queue["Items"].([]any) {
					user := raw.(map[string]any)["UserData"].(map[string]any)
					user["Played"], user["PlaybackPositionTicks"] = false, float64(10000000)
				}
				preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+series, "", false)
				preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
				waitPreloadWorker(t, options.Preloader)
				if users.Load() != 0 {
					t.Fatal("多续播候选歧义仍发生补查")
				}
			}
		})
	}
}

func TestPreloadNativeFirstDetailFallsBackAndBareRecordDoesNotAddQueries(t *testing.T) {
	root := t.TempDir()
	detail := preloadRelationFixture(t, "fnos-native-detail-Episode")
	info := preloadRelationFixture(t, "fnos-native-playinfo-episode")
	id := stringField(detail["data"].(map[string]any), "guid")
	var bridge, records atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v/api/v1/item/" + id:
			_ = json.NewEncoder(w).Encode(detail)
		case "/v/api/v1/play/info":
			_ = json.NewEncoder(w).Encode(info)
		case "/v/api/v1/play/record":
			records.Add(1)
			_, _ = io.WriteString(w, `{"code":0,"data":true}`)
		default:
			bridge.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		}
	}))
	defer upstream.Close()
	options, signs := preloadTestOptions(t, root)
	service, _, base := startService(t, upstream.URL, options)
	preloadTestRequest(t, base, http.MethodGet, "/v/api/v1/item/"+id, "", true)
	preloadTestRequest(t, base, http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"`+id+`"}`, true)
	timer := time.NewTimer(2200 * time.Millisecond)
	defer timer.Stop()
	for range 100 {
		preloadTestRequest(t, base, http.MethodPost, "/v/api/v1/play/record", `{"item_guid":"`+id+`","ts":238,"duration":240}`, true)
	}
	<-timer.C
	if bridge.Load() != 2 || records.Load() != 100 {
		t.Fatalf("首次详情补源或原生 record 次数不符：bridge=%d records=%d", bridge.Load(), records.Load())
	}
	preloadSeedPlayer(t, service.current, true)
	for range 100 {
		preloadTestRequest(t, base, http.MethodPost, "/v/api/v1/play/record", `{"item_guid":"`+id+`","ts":238,"duration":240}`, true)
	}
	waitPreloadWorker(t, options.Preloader)
	if bridge.Load() != 2 {
		t.Fatal("学到 UA 后裸原生 record 仍不应产生后继")
	}
	select {
	case <-signs:
		t.Fatal("缺少可信源或裸原生 record 触发签名")
	default:
	}
}

func preloadCompleteOlderRequest(t *testing.T, request *http.Request, entered, release chan struct{}, newer func()) {
	t.Helper()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	done := make(chan error, 1)
	go func() {
		response, err := noRedirectClient().Do(request)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("旧请求未进入真实阻塞点")
	}
	newer()
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("旧请求未正常结束")
	}
}

func TestPreloadOutOfOrderNaturalSourcesKeepLatestSelection(t *testing.T) {
	for _, mode := range []string{"native-info", "compatible-info", "episodes"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"old": preloadTestFile(t, root, "old"), "latest": preloadTestFile(t, root, "latest")}
			entered, release := make(chan struct{}), make(chan struct{})
			sources := func(which string) map[string]any {
				return map[string]any{"Id": which + "-source", "Path": files[which]}
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				which := "latest"
				if r.URL.Query().Get("older") == "true" {
					which = "old"
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				switch r.URL.Path {
				case "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/v/api/v1/item/series":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"series","type":"TV","can_play":1}}`)
				case "/v/api/v1/play/info":
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"guid": which + "-source", "media_guid": which, "video_guid": "video", "type": "Episode", "item": map[string]any{"guid": which + "-source", "type": "Episode", "can_play": 1}}})
				case "/Users/user/Items/series":
					_, _ = io.WriteString(w, `{"Id":"series","Type":"Series"}`)
				case "/Users/user/Items/movie":
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "Type": "Movie", "MediaSources": []any{sources("old"), sources("latest")}})
				case "/Items/movie/PlaybackInfo":
					_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{sources(which)}})
				case "/Shows/series/Episodes":
					var values []any
					for _, id := range []string{"old", "latest"} {
						position := 0
						if id == which {
							position = 10000000
						}
						values = append(values, map[string]any{"Id": id, "Type": "Episode", "SeriesId": "series", "SeasonId": "season", "MediaSources": []any{sources(id)}, "UserData": map[string]any{"Played": false, "PlaybackPositionTicks": position}})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Items": values})
				case "/Users/user/Items/old", "/Users/user/Items/latest":
					id := strings.TrimPrefix(r.URL.Path, "/Users/user/Items/")
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Type": "Episode", "MediaSources": []any{sources(id)}})
				default:
					t.Errorf("意外出站：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			native := mode == "native-info"
			preloadSeedPlayer(t, service.current, native)
			method, detail, endpoint, body := http.MethodPost, "/emby/Users/user/Items/movie", "/emby/Items/movie/PlaybackInfo", "{}"
			if native {
				detail, endpoint, body = "/v/api/v1/item/series", "/v/api/v1/play/info", `{"item_guid":"series"}`
			} else if mode == "episodes" {
				method, detail, endpoint, body = http.MethodGet, "/emby/Users/user/Items/series", "/emby/Shows/series/Episodes", ""
			}
			preloadTestRequest(t, base, http.MethodGet, detail, "", native)
			older, latest := endpoint+"?older=true", endpoint
			if mode == "compatible-info" {
				older += "&MediaSourceId=old-source"
				latest += "?MediaSourceId=latest-source"
			}
			request := preloadTestHTTP(t, base+older, method, body, native)
			preloadCompleteOlderRequest(t, request, entered, release, func() {
				preloadTestRequest(t, base, method, latest, body, native)
			})
			if sign := waitPreloadSign(t, signs); !strings.Contains(sign.url, "pickcode=latest") {
				t.Fatalf("旧自然响应覆盖最新选项：%+v", sign)
			}
		})
	}
}

func TestPreloadNativePlayerRequiresLatestExplicitHeader(t *testing.T) {
	for _, mode := range []string{"out-of-order", "new-control-info", "missing-header", "missing-user-agent"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "native")
			entered, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v/api/v1/play/info" {
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
					return
				}
				payload := nativeStreamPayload(file, "http://private-qms:12333/115/newurl?userid=owner&pickcode=native&force=0")
				data := payload["data"].(map[string]any)
				data["header"] = map[string]any{"User-Agent": []string{"External Player"}}
				if r.URL.Query().Get("older") == "true" {
					data["header"] = map[string]any{"User-Agent": []string{"Confirmed Player"}}
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				} else if mode == "missing-header" {
					delete(data, "header")
				} else if mode == "missing-user-agent" {
					data["header"] = map[string]any{"Accept": []string{"video/*"}}
				}
				_ = json.NewEncoder(w).Encode(payload)
			}))
			defer upstream.Close()
			options, _ := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, true)
			if mode == "out-of-order" || mode == "new-control-info" {
				request := preloadTestHTTP(t, base+nativeStreamPath+"?older=true", http.MethodPost, "{}", true)
				if mode == "new-control-info" {
					request.Header.Set("X-Emby-Device-Id", "device")
				}
				preloadCompleteOlderRequest(t, request, entered, release, func() {
					if mode == "out-of-order" {
						preloadTestRequest(t, base, http.MethodPost, nativeStreamPath, "{}", true)
						return
					}
					request := preloadTestHTTP(t, base+"/v/api/v1/play/info", http.MethodPost, `{"item_guid":"logical"}`, true)
					request.Header.Set("X-Emby-Device-Id", "device")
					request.Header.Set("User-Agent", "Control/2")
					response, err := noRedirectClient().Do(request)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				})
			} else {
				preloadTestRequest(t, base, http.MethodPost, nativeStreamPath, "{}", true)
			}
			request := preloadTestHTTP(t, base+nativeStreamPath, http.MethodPost, "{}", true)
			if mode == "new-control-info" {
				request.Header.Set("X-Emby-Device-Id", "device")
			}
			scope, _ := service.current.preloadScope(request, true)
			service.current.preload.mu.Lock()
			ua := service.current.preload.devices[scope].playerUA
			service.current.preload.mu.Unlock()
			want := ""
			if mode == "out-of-order" {
				want = "External Player"
			}
			if ua != want {
				t.Fatalf("旧响应或缺失明确 header 仍恢复 UA 学习：%q", ua)
			}
		})
	}
}

func TestPreloadStaleCompatibleStreamKeepsLatestIntent(t *testing.T) {
	for _, action := range []string{"new-detail", "new-control-info", "stop"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{"learn": preloadTestFile(t, root, "learn"), "latest": preloadTestFile(t, root, "latest")}
			entered, release := make(chan struct{}), make(chan struct{})
			var users atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Users/Me" {
					if users.Add(1) == 1 {
						close(entered)
						<-release
					}
					_, _ = io.WriteString(w, `{"Id":"user"}`)
					return
				}
				if r.URL.Path == "/Items/learn/PlaybackInfo" {
					_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "source", "Path": files["learn"]}}})
					return
				}
				if strings.HasPrefix(r.URL.Path, "/Sessions/") {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				id := strings.TrimPrefix(r.URL.Path, "/Users/user/Items/")
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": files[id]}}})
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			if action == "new-detail" {
				preloadSeedPlayer(t, service.current, false)
			}
			request := preloadTestHTTP(t, base+"/emby/Videos/learn/stream?MediaSourceId=source", http.MethodGet, "", false)
			preloadCompleteOlderRequest(t, request, entered, release, func() {
				if action == "new-detail" {
					preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/latest", "", false)
				} else if action == "new-control-info" {
					request := preloadTestHTTP(t, base+"/emby/Items/learn/PlaybackInfo", http.MethodPost, "{}", false)
					request.Header.Set("User-Agent", "Control/2")
					response, err := noRedirectClient().Do(request)
					if err != nil {
						t.Fatal(err)
					}
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				} else {
					preloadTestRequest(t, base, http.MethodPost, "/emby/Sessions/Playing/Stopped", `{"ItemId":"learn","PlaySessionId":"session"}`, false)
				}
			})
			if action == "new-detail" {
				if result := waitPreloadSign(t, signs); !strings.Contains(result.url, "pickcode=latest") {
					t.Fatalf("旧真实播放撤销了新详情：%+v", result)
				}
			} else {
				service.current.preload.mu.Lock()
				defer service.current.preload.mu.Unlock()
				for _, device := range service.current.preload.devices {
					if device.playerUA != "" {
						t.Fatal("旧真实播放在停止或控制版本改变后恢复 UA 学习")
					}
				}
			}
		})
	}
}

func TestPreloadNewDetailKeepsPriorityOverOlderSource(t *testing.T) {
	for _, order := range []string{"source-before-detail", "source-after-detail"} {
		t.Run(order, func(t *testing.T) {
			root := t.TempDir()
			old, latest := preloadTestFile(t, root, "old"), preloadTestFile(t, root, "latest")
			entered, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("older") == "true" {
					close(entered)
					<-release
				}
				switch r.URL.Path {
				case "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Items/movie/PlaybackInfo":
					_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "old", "Path": old}}})
				default:
					sources := []any{map[string]any{"Id": "old", "Path": old}, map[string]any{"Id": "latest", "Path": latest}}
					if r.URL.Query().Get("latest") == "true" {
						sources = sources[1:]
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "Type": "Movie", "MediaSources": sources})
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			preloadSeedPlayer(t, service.current, false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/movie", "", false)
			newer := func() {
				preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/movie?latest=true&MediaSourceId=latest", "", false)
			}
			if order == "source-before-detail" {
				preloadTestRequest(t, base, http.MethodPost, "/emby/Items/movie/PlaybackInfo?MediaSourceId=old", "{}", false)
				newer()
			} else {
				request := preloadTestHTTP(t, base+"/emby/Items/movie/PlaybackInfo?older=true&MediaSourceId=old", http.MethodPost, "{}", false)
				preloadCompleteOlderRequest(t, request, entered, release, newer)
			}
			if result := waitPreloadSign(t, signs); !strings.Contains(result.url, "pickcode=latest") {
				t.Fatalf("旧补源覆盖更新详情：%+v", result)
			}
		})
	}
}

func TestPreloadPlayerMappingsFollowSuccessfulActualUA(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatible", true: "native-cookie"}[native], func(t *testing.T) {
			root := t.TempDir()
			files := make(map[string]string)
			for _, id := range []string{"20", "21", "22", "23", "24"} {
				files[id] = preloadTestFile(t, root, id)
			}
			var mediaUA atomic.Value
			mediaUA.Store("Player/1.0")
			var fail atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case strings.HasPrefix(r.URL.Path, "/Users/user/Items/"):
					id := strings.TrimPrefix(r.URL.Path, "/Users/user/Items/")
					sourceID := "source-" + id
					if native {
						sourceID = "logical-" + id
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Type": "Movie", "MediaSources": []any{map[string]any{"Id": sourceID, "Path": files[id]}}})
				case strings.HasPrefix(r.URL.Path, "/v/api/v1/item/"):
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"guid": strings.TrimPrefix(r.URL.Path, "/v/api/v1/item/"), "type": "Movie", "can_play": 1}})
				case r.URL.Path == "/v/api/v1/play/info":
					var body struct {
						ItemGUID string `json:"item_guid"`
					}
					_ = json.NewDecoder(r.Body).Decode(&body)
					id := strings.TrimPrefix(body.ItemGUID, "logical-")
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"guid": body.ItemGUID, "media_guid": id, "video_guid": "video-" + id, "type": "Movie", "item": map[string]any{"guid": body.ItemGUID, "type": "Movie", "can_play": 1}}})
				case r.URL.Path == nativeStreamPath:
					id := r.URL.Query().Get("item")
					payload := nativeStreamPayload(files[id], "http://private-qms:12333/115/newurl?userid=owner&pickcode="+id+"&force=0")
					data := payload["data"].(map[string]any)
					data["file_stream"].(map[string]any)["guid"] = id
					data["video_stream"].(map[string]any)["media_guid"] = id
					data["header"] = map[string]any{"User-Agent": []string{mediaUA.Load().(string)}}
					_ = json.NewEncoder(w).Encode(payload)
				default:
					t.Errorf("意外上游请求：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, _ := preloadTestOptions(t, root)
			signs, actual := make(chan string, 8), make(chan string, 8)
			options.NewPreload = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					signs <- ua
					return playback.PreloadResult{Outcome: playback.PreloadCached}
				}, true
			}
			options.MainHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				actual <- r.UserAgent()
				if fail.Load() {
					http.Error(w, "temporary failure", http.StatusServiceUnavailable)
					return
				}
				w.Header().Set("Location", "https://cdn.115cdn.net/normal")
				w.WriteHeader(http.StatusFound)
			})
			service, _, base := startService(t, upstream.URL, options)
			send := func(method, endpoint, body, ua string) {
				request := preloadTestHTTP(t, base+endpoint, method, body, native)
				request.Header.Set("User-Agent", ua)
				response, err := noRedirectClient().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			detail := func(id, control, expected string) {
				if native {
					send(http.MethodGet, "/v/api/v1/item/logical-"+id, "", control)
					send(http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"logical-`+id+`"}`, control)
				} else {
					send(http.MethodGet, "/emby/Users/user/Items/"+id, "", control)
				}
				select {
				case ua := <-signs:
					if ua != expected {
						t.Fatalf("详情映射UA不符：got=%q want=%q", ua, expected)
					}
				case <-time.After(4 * time.Second):
					t.Fatal("首次或映射详情没有准备")
				}
			}
			stream := func(id, player string) {
				mediaUA.Store(player)
				if native {
					send(http.MethodPost, nativeStreamPath+"?item="+id, "{}", "Control/1.0")
				} else {
					send(http.MethodGet, "/emby/Videos/"+id+"/stream?MediaSourceId=source-"+id, "", player)
				}
				select {
				case ua := <-actual:
					if ua != player {
						t.Fatalf("预测UA覆盖了真实取链UA：got=%q want=%q", ua, player)
					}
				default:
					t.Fatal("未经过原播放入口")
				}
			}
			detail("20", "Control/1.0", "Control/1.0")
			stream("20", "Player/1.0")
			service.current.preload.mu.Lock()
			clear(service.current.preload.devices)
			service.current.preload.mu.Unlock()
			detail("21", "Control/1.0", "Player/1.0")
			fail.Store(true)
			stream("21", "Player/2.0")
			detail("22", "Control/1.0", "Player/1.0")
			fail.Store(false)
			stream("22", "Player/2.0")
			detail("23", "Control/1.0", "Player/2.0")
			detail("24", "Control/2.0", "Control/2.0")
		})
	}
}

type preloadWriterCapabilities struct {
	*httptest.ResponseRecorder
	flushed, hijacked bool
}

func (w *preloadWriterCapabilities) Flush() { w.flushed = true }

func (w *preloadWriterCapabilities) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijacked = true
	return nil, nil, nil
}

func TestPreloadPlaybackWriterPreservesResultAndCapabilities(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		location string
		want     bool
	}{
		{"ok", 200, "", true},
		{"range", 206, "", true},
		{"redirect", 307, "https://cdn.115cdn.net/media", true},
		{"failed", 503, "https://cdn.115cdn.net/media", false},
		{"invalid-redirect", 302, "javascript:bad", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			underlying := &preloadWriterCapabilities{ResponseRecorder: httptest.NewRecorder()}
			observed := &preloadPlaybackWriter{ResponseWriter: underlying}
			observed.Header().Set("Location", test.location)
			observed.WriteHeader(test.status)
			_, _ = observed.Write([]byte("unchanged"))
			if observed.successful() != test.want || underlying.Code != test.status || underlying.Body.String() != "unchanged" || observed.Unwrap() != underlying {
				t.Fatal("响应状态、正文、判定或Unwrap发生变化")
			}
			if err := http.NewResponseController(observed).Flush(); err != nil || !underlying.flushed {
				t.Fatalf("Flush没有委托：%v", err)
			}
			if _, _, err := http.NewResponseController(observed).Hijack(); err != nil || !underlying.hijacked {
				t.Fatalf("Hijack没有委托：%v", err)
			}
		})
	}
}

func TestPreloadNativeNaturalAssociationForSixtySeconds(t *testing.T) {
	for _, infoFirst := range []bool{false, true} {
		for _, age := range []time.Duration{30 * time.Second, 61 * time.Second} {
			t.Run(fmt.Sprintf("info-first=%t/age=%s", infoFirst, age), func(t *testing.T) {
				root := t.TempDir()
				file := preloadTestFile(t, root, "native")
				var supplemental atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/v/api/v1/item/logical":
						_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Movie","can_play":1}}`)
					case "/v/api/v1/play/info":
						_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
					case "/Users/Me":
						supplemental.Add(1)
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/file":
						supplemental.Add(1)
						_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "logical", "Path": file}}})
					default:
						t.Errorf("意外接口：%s", r.URL.Path)
					}
				}))
				defer upstream.Close()
				options, signs := preloadTestOptions(t, root)
				service, _, base := startService(t, upstream.URL, options)
				detail := func() { preloadTestRequest(t, base, http.MethodGet, "/v/api/v1/item/logical", "", true) }
				info := func() {
					preloadTestRequest(t, base, http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"logical"}`, true)
				}
				first, second := detail, info
				if infoFirst {
					first, second = info, detail
				}
				first()
				service.current.preload.mu.Lock()
				for scope, device := range service.current.preload.devices {
					if infoFirst {
						device.infoAt = time.Now().Add(-age)
					} else {
						device.detailAt = time.Now().Add(-age)
					}
					service.current.preload.devices[scope] = device
				}
				service.current.preload.mu.Unlock()
				second()
				if age < time.Minute {
					if sign := waitPreloadSign(t, signs); sign.ua != "Confirmed Player" || supplemental.Load() != 2 {
						t.Fatalf("15–60秒关联未复用正常首项路径：%+v requests=%d", sign, supplemental.Load())
					}
				} else {
					options.Preloader.Stop()
					if len(signs) != 0 || supplemental.Load() != 0 {
						t.Fatalf("超过60秒仍补源或签名：sign=%d supplemental=%d", len(signs), supplemental.Load())
					}
				}
			})
		}
	}
}

func TestPreloadNaturalInfoUsesCurrentControlVersion(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "20")
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/20":
					item := map[string]any{"Id": "20", "Type": "Movie"}
					if native || r.UserAgent() == "Control/2" {
						source := "source"
						if native {
							source = "logical"
						}
						item["MediaSources"] = []any{map[string]any{"Id": source, "Path": file}}
					}
					_ = json.NewEncoder(w).Encode(item)
				case "/Items/20/PlaybackInfo":
					_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
				case "/v/api/v1/item/logical":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Movie","can_play":1}}`)
				case "/v/api/v1/play/info":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"20","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			send := func(method, endpoint, body, ua string) {
				request := preloadTestHTTP(t, base+endpoint, method, body, native)
				request.Header.Set("X-Emby-Device-Id", "device")
				request.Header.Set("User-Agent", ua)
				response, err := noRedirectClient().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			detail, info, body := "/emby/Users/user/Items/20", "/emby/Items/20/PlaybackInfo", "{}"
			if native {
				detail, info, body = "/v/api/v1/item/logical", "/v/api/v1/play/info", `{"item_guid":"logical"}`
			}
			send(http.MethodGet, detail, "", "Control/1")
			inst := service.current
			inst.preload.mu.Lock()
			for scope, device := range inst.preload.devices {
				inst.preload.uas.Remember(inst.preloadUAKey(scope, device), "Old Player", inst.preload.generation)
			}
			inst.preload.mu.Unlock()
			send(http.MethodPost, info, body, "Control/2")
			if sign := waitPreloadSign(t, signs); sign.ua != "Control/2" {
				t.Fatalf("新自然资料借用旧完整控制UA映射：%q", sign.ua)
			}
		})
	}
}

func TestPreloadOlderControlMetadataCannotEnrichNewVersion(t *testing.T) {
	for _, mode := range []string{"native-info", "compatible-info", "episodes"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "old")
			entered, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("older") == "true" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				switch r.URL.Path {
				case "/v/api/v1/item/series":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"series","type":"TV","can_play":1}}`)
				case "/v/api/v1/play/info":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"episode","media_guid":"file","video_guid":"video","type":"Episode","item":{"guid":"episode","type":"Episode","can_play":1}}}`)
				case "/Users/user/Items/series":
					_, _ = io.WriteString(w, `{"Id":"series","Type":"Series"}`)
				case "/Users/user/Items/movie":
					_, _ = io.WriteString(w, `{"Id":"movie","Type":"Movie"}`)
				case "/Items/movie/PlaybackInfo":
					_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "old-source", "Path": file}}})
				case "/Shows/series/Episodes":
					var items []any
					for _, id := range []string{"first", "second"} {
						items = append(items, map[string]any{"Id": id, "Type": "Episode", "SeriesId": "series", "SeasonId": "season", "MediaSources": []any{map[string]any{"Id": id + "-source", "Path": file}}, "UserData": map[string]any{"Played": false, "PlaybackPositionTicks": 10000000}})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"Items": items})
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, _ := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			native := mode == "native-info"
			method, detail, endpoint, body := http.MethodPost, "/emby/Users/user/Items/movie", "/emby/Items/movie/PlaybackInfo", "{}"
			if native {
				detail, endpoint, body = "/v/api/v1/item/series", "/v/api/v1/play/info", `{"item_guid":"series"}`
			} else if mode == "episodes" {
				method, detail, endpoint, body = http.MethodGet, "/emby/Users/user/Items/series", "/emby/Shows/series/Episodes", ""
			}
			request := func(method, endpoint, body, ua string) *http.Request {
				r := preloadTestHTTP(t, base+endpoint, method, body, native)
				r.Header.Set("X-Emby-Device-Id", "device")
				r.Header.Set("User-Agent", ua)
				return r
			}
			send := func(ua string) {
				response, err := noRedirectClient().Do(request(http.MethodGet, detail, "", ua))
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			send("Control/1")
			preloadCompleteOlderRequest(t, request(method, endpoint+"?older=true", body, "Control/1"), entered, release, func() { send("Control/2") })
			inst := service.current
			inst.preload.mu.Lock()
			defer inst.preload.mu.Unlock()
			for _, device := range inst.preload.devices {
				if device.controlUA != "Control/2" || device.info.file != "" || len(device.source.sources) != 0 || len(device.queue) != 0 {
					t.Fatalf("旧控制版本的慢响应仍补入新详情：control=%q info=%q sources=%d queue=%d", device.controlUA, device.info.file, len(device.source.sources), len(device.queue))
				}
			}
		})
	}
}

func TestPreloadNextDoesNotReusePreviousPlaybackUA(t *testing.T) {
	for _, change := range []string{"same-session", "new-stream", "item", "source", "session"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			thirdData, _ := json.Marshal(items[1])
			var third map[string]any
			_ = json.Unmarshal(thirdData, &third)
			third["Id"] = "third"
			third["MediaSources"] = []any{map[string]any{"Id": "third-source", "Path": preloadTestFile(t, root, "third")}}
			queue["Items"] = append(queue["Items"].([]any), third)
			series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			next := stringField(items[1], "Id")
			var users atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					users.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Users/user/Items/" + next:
					_ = json.NewEncoder(w).Encode(items[1])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				case "/Sessions/Playing", "/Sessions/Playing/Progress":
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			}))
			defer upstream.Close()
			options, signs := preloadTestOptions(t, root)
			service, _, base := startService(t, upstream.URL, options)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadTestRequest(t, base, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			stream := func(ua string) {
				request := preloadTestHTTP(t, base+"/emby/Videos/"+current+"/stream?MediaSourceId="+source, http.MethodGet, "", false)
				request.Header.Set("User-Agent", ua)
				response, err := noRedirectClient().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			stream("Actual Player/1")
			report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "old-session", "IsPaused": false, "PositionTicks": 2060000000}
			send := func(endpoint string) {
				body, _ := json.Marshal(report)
				preloadTestRequest(t, base, http.MethodPost, endpoint, string(body), false)
			}
			send("/emby/Sessions/Playing")
			switch change {
			case "new-stream":
				stream("Actual Player/2")
				report["PlaySessionId"] = "new-session"
			case "item":
				report["ItemId"], report["MediaSourceId"] = next, stringField(items[1]["MediaSources"].([]any)[0].(map[string]any), "Id")
				preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+next, "", false)
			case "source":
				items[0]["MediaSources"].([]any)[0].(map[string]any)["Path"] = preloadTestFile(t, root, "changed")
				preloadTestRequest(t, base, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			case "session":
				report["PlaySessionId"] = "new-session"
			}
			send("/emby/Sessions/Playing")
			before := users.Load()
			send("/emby/Sessions/Playing/Progress")
			service.current.preload.mu.Lock()
			for scope := range service.current.preload.devices {
				options.Preloader.CancelDetail(scope)
			}
			service.current.preload.mu.Unlock()
			waitPreloadWorker(t, options.Preloader)
			if change == "same-session" || change == "new-stream" {
				want := "Actual Player/1"
				if change == "new-stream" {
					want = "Actual Player/2"
				}
				if len(signs) != 1 || users.Load() != before+1 {
					t.Fatalf("本会话真实播放未触发后继：signs=%d users=%d", len(signs), users.Load()-before)
				}
				if sign := <-signs; sign.ua != want {
					t.Fatalf("控制UA覆盖实际会话UA：got=%q want=%q", sign.ua, want)
				}
				return
			}
			if len(signs) != 0 || users.Load() != before {
				t.Fatalf("尚无本次真实UA仍准备后继：signs=%d users=%d", len(signs), users.Load()-before)
			}
		})
	}
}

func preloadTestInstance(t *testing.T, options Options, handler http.Handler) *instance {
	t.Helper()
	upstream := httptest.NewServer(handler)
	t.Cleanup(upstream.Close)
	service := NewService(options)
	inst, err := service.prepare(models.FnosProxyConfig{Enabled: true, FnosURL: upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(inst.closeResources)
	return inst
}

func preloadServeInstance(t *testing.T, inst *instance, method, endpoint, body string, native bool) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	inst.ServeHTTP(response, preloadTestHTTP(t, "http://proxy.example"+endpoint, method, body, native))
	return response
}

func TestPreloadNativeBusinessDenialRevokesDetail(t *testing.T) {
	for _, reason := range []string{"can-play", "play-error"} {
		for _, phase := range []string{"debounce", "signing", "older-denial", "playing-next"} {
			t.Run(reason+"/"+phase, func(t *testing.T) {
				root := t.TempDir()
				file := preloadTestFile(t, root, "business-denied")
				options, _ := preloadTestOptions(t, root)
				var signatures, published atomic.Int32
				entered, release := make(chan struct{}, 1), make(chan struct{})
				options.NewPreload = func(raw, _ string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
							signatures.Add(1)
							entered <- struct{}{}
							if phase == "signing" {
								select {
								case <-release:
								case <-ctx.Done():
								}
							}
							task.Publish(func() { published.Add(1) })
						}
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}, true
				}
				denied := " \n{\"code\":0,\"data\":{\"guid\":\"logical\",\"type\":\"Movie\",\"can_play\":0},\"unknown\":9007199254740993}\n"
				if reason == "play-error" {
					denied = strings.Replace(denied, `"can_play":0`, `"can_play":1,"play_error":"permission denied"`, 1)
				}
				if phase == "debounce" {
					denied = strings.Replace(denied, `"type":"Movie",`, "", 1)
				}
				olderEntered, olderRelease := make(chan struct{}), make(chan struct{})
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("denied") == "true" {
						if r.URL.Query().Get("older") == "true" {
							close(olderEntered)
							select {
							case <-olderRelease:
							case <-r.Context().Done():
								return
							}
						}
						_, _ = io.WriteString(w, denied)
						return
					}
					switch r.URL.Path {
					case "/Users/Me":
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/file":
						_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "logical", "Path": file}}})
					case "/v/api/v1/item/logical":
						_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Movie","can_play":1}}`)
					case "/v/api/v1/play/info":
						_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
					default:
						t.Errorf("意外接口：%s", r.URL.Path)
					}
				}))
				const detail, info, body = "/v/api/v1/item/logical", "/v/api/v1/play/info", `{"item_guid":"logical"}`
				preloadServeInstance(t, inst, http.MethodPost, info, body, true)
				preloadServeInstance(t, inst, http.MethodGet, detail, "", true)
				var nextPublished atomic.Int32
				if phase == "playing-next" {
					scope, _ := inst.preloadScope(preloadTestHTTP(t, "http://proxy.example"+detail, http.MethodGet, "", true), true)
					nextEntered := make(chan struct{})
					if !options.Preloader.Submit(playback.PreloadCandidate{Key: "playing-next", Scope: scope, Instance: inst.preloadID, Kind: playback.PreloadNext,
						Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
							if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 2, UserID: "other-owner"}, "playing-next") {
								return playback.PreloadResult{Outcome: playback.PreloadSkipped}
							}
							close(nextEntered)
							select {
							case <-release:
							case <-ctx.Done():
							}
							task.Publish(func() { nextPublished.Add(1) })
							return playback.PreloadResult{Outcome: playback.PreloadPublished}
						}}) {
						t.Fatal("无法建立独立的当前播放后继")
					}
					select {
					case <-nextEntered:
					case <-time.After(time.Second):
						t.Fatal("独立的后继未开始准备")
					}
				}
				if phase == "signing" {
					select {
					case <-entered:
					case <-time.After(4 * time.Second):
						t.Fatal("初始详情未进入签名")
					}
				}
				if phase == "older-denial" {
					proxy := httptest.NewServer(inst)
					defer proxy.Close()
					request := preloadTestHTTP(t, proxy.URL+detail+"?denied=true&older=true", http.MethodGet, "", true)
					preloadCompleteOlderRequest(t, request, olderEntered, olderRelease, func() {
						preloadServeInstance(t, inst, http.MethodGet, detail, "", true)
					})
				} else if response := preloadServeInstance(t, inst, http.MethodGet, detail+"?denied=true", "", true); response.Code != http.StatusOK || response.Body.String() != denied {
					t.Fatal("业务拒绝响应的状态或原始字节被改写")
				}
				close(release)
				preloadServeInstance(t, inst, http.MethodPost, info, body, true)
				if phase != "signing" {
					time.Sleep(2200 * time.Millisecond)
				}
				waitPreloadWorker(t, options.Preloader)
				wantSigns, wantPublished := int32(0), int32(0)
				if phase == "signing" || phase == "older-denial" {
					wantSigns = 1
				}
				if phase == "older-denial" {
					wantPublished = 1
				}
				if signatures.Load() != wantSigns || published.Load() != wantPublished {
					t.Fatalf("业务拒绝撤销边界不符：signatures=%d published=%d，期望 %d/%d", signatures.Load(), published.Load(), wantSigns, wantPublished)
				}
				if phase == "playing-next" && nextPublished.Load() != 1 {
					t.Fatal("详情拒绝误撤销了独立的当前播放后继")
				}
			})
		}
	}
}

func TestPreloadStoppedMatchesCurrentPlayback(t *testing.T) {
	for _, change := range []string{"old-item", "old-session", "old-source", "old-item-before-start", "matching", "missing-session", "missing-source", "missing-both", "stream-before-start"} {
		for _, status := range []int{http.StatusNoContent, http.StatusForbidden} {
			t.Run(fmt.Sprintf("%s/status=%d", change, status), func(t *testing.T) {
				root := t.TempDir()
				queue, items := preloadQueueFixture(t, root)
				series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
				source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
				options, _ := preloadTestOptions(t, root)
				var signatures, published atomic.Int32
				entered, release := make(chan struct{}, 1), make(chan struct{})
				options.NewPreload = func(raw, _ string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
							signatures.Add(1)
							entered <- struct{}{}
							select {
							case <-release:
							case <-ctx.Done():
							}
							task.Publish(func() { published.Add(1) })
						}
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}, true
				}
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/Users/Me":
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/" + current:
						_ = json.NewEncoder(w).Encode(items[0])
					case "/Shows/" + series + "/Episodes":
						_ = json.NewEncoder(w).Encode(queue)
					case "/Sessions/Playing/Stopped":
						w.WriteHeader(status)
					default:
						w.WriteHeader(http.StatusNoContent)
					}
				}))
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
				if change == "old-item-before-start" {
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", `{"ItemId":"previous-episode","MediaSourceId":"previous-source","PlaySessionId":"previous-session"}`, false)
				}
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Videos/"+current+"/stream?MediaSourceId="+source, "", false)
				report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "current-session", "IsPaused": false, "PositionTicks": 2060000000}
				encoded, _ := json.Marshal(report)
				if change != "stream-before-start" && change != "old-item-before-start" {
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", string(encoded), false)
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", string(encoded), false)
					select {
					case <-entered:
					case <-time.After(time.Second):
						t.Fatal("当前播放未开始准备后继")
					}
				}
				stop := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "current-session"}
				switch change {
				case "old-item", "old-item-before-start":
					stop["ItemId"] = "previous-episode"
				case "old-session":
					stop["PlaySessionId"] = "previous-session"
				case "old-source":
					stop["MediaSourceId"] = "previous-source"
				case "missing-session":
					delete(stop, "PlaySessionId")
				case "missing-source":
					delete(stop, "MediaSourceId")
				case "missing-both":
					delete(stop, "PlaySessionId")
					delete(stop, "MediaSourceId")
				}
				body, _ := json.Marshal(stop)
				if response := preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Stopped", string(body), false); response.Code != status {
					t.Fatal("停止响应状态被改写")
				}
				stale := strings.HasPrefix(change, "old-")
				inst.preload.mu.Lock()
				for _, device := range inst.preload.devices {
					wantItem, wantSession := current, "current-session"
					if change == "old-item-before-start" {
						wantItem, wantSession = "", ""
					}
					if stale && (device.playingID != wantItem || device.session != wantSession || device.playerUA != "Confirmed Player" || device.playerSource.ItemID != current || device.playerSource.ID != source) {
						t.Error("迟到停止清除了当前播放或实际 UA")
					}
					if !stale && status == http.StatusNoContent && (device.playingID != "" || device.playerUA != "") {
						t.Error("匹配停止没有清除当前播放或实际 UA")
					}
				}
				inst.preload.mu.Unlock()
				if stale {
					if change == "old-item-before-start" {
						preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", string(encoded), false)
					}
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", string(encoded), false)
					if change == "old-item-before-start" {
						select {
						case <-entered:
						case <-time.After(time.Second):
							t.Fatal("迟到停止后新播放不能继续准备后继")
						}
					}
				}
				close(release)
				waitPreloadWorker(t, options.Preloader)
				wantPublished, wantSignatures := int32(0), int32(1)
				if stale {
					wantPublished = 1
				}
				if change == "stream-before-start" {
					wantSignatures = 0
				}
				if signatures.Load() != wantSignatures || published.Load() != wantPublished {
					t.Fatalf("停止后签名/发布不符：got=%d/%d want=%d/%d", signatures.Load(), published.Load(), wantSignatures, wantPublished)
				}
			})
		}
	}
}

func TestPreloadStoppedDuringCurrentStream(t *testing.T) {
	for _, stale := range []bool{true, false} {
		for _, status := range []int{http.StatusNoContent, http.StatusForbidden} {
			t.Run(fmt.Sprintf("stale=%t/status=%d", stale, status), func(t *testing.T) {
				root := t.TempDir()
				queue, items := preloadQueueFixture(t, root)
				series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
				source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
				options, signs := preloadTestOptions(t, root)
				entered, release := make(chan struct{}), make(chan struct{})
				options.MainHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Location", "https://cdn.115cdn.net/current")
					w.WriteHeader(http.StatusFound)
				})
				var stopBody []byte
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/Users/Me":
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/" + current:
						_ = json.NewEncoder(w).Encode(items[0])
					case "/Shows/" + series + "/Episodes":
						_ = json.NewEncoder(w).Encode(queue)
					case "/Sessions/Playing/Stopped":
						body, _ := io.ReadAll(r.Body)
						if !bytes.Equal(body, stopBody) {
							t.Error("停止报告正文被改写")
						}
						w.WriteHeader(status)
					default:
						w.WriteHeader(http.StatusNoContent)
					}
				}))
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
				report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "current-session", "IsPaused": false, "PositionTicks": 2060000000}
				body, _ := json.Marshal(report)
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", string(body), false)
				stopBody = body
				if stale {
					stopBody = []byte(`{"ItemId":"previous-episode","MediaSourceId":"previous-source","PlaySessionId":"previous-session"}`)
				}
				proxy := httptest.NewServer(inst)
				defer proxy.Close()
				request := preloadTestHTTP(t, proxy.URL+"/emby/Videos/"+current+"/stream?MediaSourceId="+source, http.MethodGet, "", false)
				preloadCompleteOlderRequest(t, request, entered, release, func() {
					if response := preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Stopped", string(stopBody), false); response.Code != status {
						t.Error("停止响应状态被改写")
					}
				})
				inst.preload.mu.Lock()
				for _, device := range inst.preload.devices {
					wantUA := ""
					if stale {
						wantUA = "Confirmed Player"
					}
					if device.playerUA != wantUA {
						t.Errorf("停止报告改变了错误的取流关联：UA=%q want=%q", device.playerUA, wantUA)
					}
				}
				inst.preload.mu.Unlock()
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", string(body), false)
				waitPreloadWorker(t, options.Preloader)
				wantSigns := 0
				if stale {
					wantSigns = 1
				}
				if len(signs) != wantSigns {
					t.Fatalf("停止后的后继机会不符：signs=%d want=%d", len(signs), wantSigns)
				}
			})
		}
	}
}

func TestPreloadLatestDeniedDetailRevokesPreparation(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			for _, phase := range []string{"debounce", "signing"} {
				t.Run(fmt.Sprintf("native=%t/status=%d/%s", native, status, phase), func(t *testing.T) {
					root := t.TempDir()
					file := preloadTestFile(t, root, "denied")
					options, _ := preloadTestOptions(t, root)
					signs := make(chan preloadTestSign, 8)
					entered, release := make(chan struct{}, 1), make(chan struct{})
					var signatures atomic.Int32
					options.NewPreload = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
						return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
							if task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
								signatures.Add(1)
								entered <- struct{}{}
								if phase == "signing" {
									select {
									case <-release:
									case <-ctx.Done():
									}
								}
								task.Publish(func() { signs <- preloadTestSign{raw, ua} })
							}
							return playback.PreloadResult{Outcome: playback.PreloadPublished}
						}, true
					}
					const denied = " \n{\"message\":\"permission denied\",\"unknown\":9007199254740993}\n"
					inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Query().Get("denied") == "true" {
							w.WriteHeader(status)
							_, _ = io.WriteString(w, denied)
							return
						}
						switch r.URL.Path {
						case "/Users/Me":
							_, _ = io.WriteString(w, `{"Id":"user"}`)
						case "/Users/user/Items/file":
							_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "logical", "Path": file}}})
						case "/Items/file/PlaybackInfo":
							_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "logical", "Path": file}}})
						case "/v/api/v1/item/logical":
							_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Movie","can_play":1}}`)
						case "/v/api/v1/play/info":
							_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
						default:
							t.Errorf("意外请求：%s", r.URL.Path)
						}
					}))
					detail, info, body := "/emby/Users/user/Items/file", "/emby/Items/file/PlaybackInfo", "{}"
					if native {
						detail, info, body = "/v/api/v1/item/logical", "/v/api/v1/play/info", `{"item_guid":"logical"}`
						preloadServeInstance(t, inst, http.MethodPost, info, body, native)
					}
					preloadServeInstance(t, inst, http.MethodGet, detail, "", native)
					if phase == "signing" {
						select {
						case <-entered:
						case <-time.After(4 * time.Second):
							t.Fatal("初始详情未进入签名")
						}
					}
					response := preloadServeInstance(t, inst, http.MethodGet, detail+"?denied=true", "", native)
					if response.Code != status || response.Body.String() != denied {
						t.Fatal("拒绝详情的状态或原始字节被改写")
					}
					close(release)
					preloadServeInstance(t, inst, http.MethodPost, info, body, native)
					if phase == "debounce" {
						time.Sleep(2200 * time.Millisecond)
					}
					waitPreloadWorker(t, options.Preloader)
					want := int32(0)
					if phase == "signing" {
						want = 1
					}
					if len(signs) != 0 || signatures.Load() != want {
						t.Fatalf("最新拒绝详情后仍签名或发布：signatures=%d published=%d", signatures.Load(), len(signs))
					}
				})
			}
		}
	}
}

func TestPreloadLatestEpisodesRevokesUnprovenNext(t *testing.T) {
	for _, invalid := range []string{"missing-anchor", "last-anchor", "empty", "only-current", "cross-season", "cross-series", "unchanged", "progress-only", "changed-successor", "changed-next-source", "ambiguous-next-source", "older-invalid", "other-user"} {
		t.Run(invalid, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			updated := map[string]any{"Items": []any{items[0], items[1]}}
			switch invalid {
			case "missing-anchor":
				third := map[string]any{"Id": "third", "Type": "Episode", "SeriesId": series, "SeasonId": stringField(items[1], "SeasonId")}
				updated["Items"] = []any{items[1], third}
			case "last-anchor":
				updated["Items"] = []any{items[1], items[0]}
			case "empty", "other-user":
				updated["Items"] = []any{}
			case "only-current":
				updated["Items"] = []any{items[0]}
			case "cross-season":
				next := make(map[string]any, len(items[1]))
				for key, value := range items[1] {
					next[key] = value
				}
				next["SeasonId"] = "other-season"
				updated["Items"] = []any{items[0], next}
			case "cross-series":
				next := make(map[string]any, len(items[1]))
				for key, value := range items[1] {
					next[key] = value
				}
				next["SeriesId"] = "other-series"
				updated["Items"] = []any{items[0], next}
			case "changed-successor":
				next := make(map[string]any, len(items[1]))
				for key, value := range items[1] {
					next[key] = value
				}
				next["Id"] = "changed-next"
				next["MediaSources"] = []any{map[string]any{"Id": "changed-source", "Path": preloadTestFile(t, root, "changed-next")}}
				updated["Items"] = []any{items[0], next}
			case "changed-next-source", "ambiguous-next-source":
				next := make(map[string]any, len(items[1]))
				for key, value := range items[1] {
					next[key] = value
				}
				next["MediaSources"] = []any{map[string]any{"Id": stringField(items[1]["MediaSources"].([]any)[0].(map[string]any), "Id"), "Path": preloadTestFile(t, root, "changed-next")}}
				if invalid == "ambiguous-next-source" {
					next["MediaSources"] = append(next["MediaSources"].([]any), items[1]["MediaSources"].([]any)...)
					next["MediaSources"].([]any)[1] = map[string]any{"Id": "other-source", "Path": preloadTestFile(t, root, "other-next")}
				}
				updated["Items"] = []any{items[0], next}
			}
			entered, canceled, release := make(chan struct{}, 1), make(chan struct{}, 1), make(chan struct{})
			olderEntered, olderRelease := make(chan struct{}), make(chan struct{})
			var block atomic.Bool
			options, signs := preloadTestOptions(t, root)
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					if block.Load() {
						entered <- struct{}{}
						select {
						case <-r.Context().Done():
							canceled <- struct{}{}
							return
						case <-release:
						}
					}
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					payload := queue
					if r.URL.Query().Get("older") == "true" {
						close(olderEntered)
						select {
						case <-olderRelease:
						case <-r.Context().Done():
							return
						}
						payload = map[string]any{"Items": []any{}}
					}
					if r.URL.Query().Get("updated") == "true" {
						payload = updated
					}
					_ = json.NewEncoder(w).Encode(payload)
				case "/Sessions/Playing", "/Sessions/Playing/Progress":
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("意外接口：%s", r.URL.Path)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Videos/"+current+"/stream?MediaSourceId="+source, "", false)
			report := `{"ItemId":"` + current + `","MediaSourceId":"` + source + `","PlaySessionId":"session","IsPaused":false,"PositionTicks":2060000000}`
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
			if invalid == "unchanged" {
				inst.preload.mu.Lock()
				for scope, device := range inst.preload.devices {
					device.queueAt = time.Now().Add(-2 * time.Minute)
					inst.preload.devices[scope] = device
				}
				inst.preload.mu.Unlock()
			}
			block.Store(true)
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("合格进度未进入下一项补查")
			}
			if invalid == "progress-only" {
				inst.preload.mu.Lock()
				for scope, device := range inst.preload.devices {
					device.queue = device.queue[:1]
					inst.preload.devices[scope] = device
				}
				inst.preload.mu.Unlock()
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
			} else if invalid == "older-invalid" {
				proxy := httptest.NewServer(inst)
				defer proxy.Close()
				request := preloadTestHTTP(t, proxy.URL+"/emby/Shows/"+series+"/Episodes?older=true", http.MethodGet, "", false)
				preloadCompleteOlderRequest(t, request, olderEntered, olderRelease, func() {
					preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes?updated=true", "", false)
				})
			} else {
				endpoint := "/emby/Shows/" + series + "/Episodes?updated=true"
				if invalid == "other-user" {
					endpoint += "&UserId=other-user"
				}
				preloadServeInstance(t, inst, http.MethodGet, endpoint, "", false)
			}
			if invalid != "unchanged" && invalid != "older-invalid" && invalid != "other-user" {
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Error("最新列表失去后继依据后未立即取消旧补查")
				}
			}
			close(release)
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
			waitPreloadWorker(t, options.Preloader)
			want := 0
			if invalid == "unchanged" || invalid == "older-invalid" || invalid == "changed-successor" || invalid == "changed-next-source" || invalid == "other-user" {
				want = 1
			}
			if len(signs) != want {
				t.Fatalf("后继证据更新后签名数不符：got=%d want=%d", len(signs), want)
			}
			if (invalid == "changed-successor" || invalid == "changed-next-source") && !strings.Contains((<-signs).url, "pickcode=changed-next") {
				t.Fatal("后继变化后没有改用新证据")
			}
		})
	}
}

func TestPreloadLatestEpisodesSourceRevokesSigningNext(t *testing.T) {
	for _, change := range []string{"changed", "ambiguous", "missing", "recovered"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			series, current := stringField(items[0], "SeriesId"), stringField(items[0], "Id")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			updatedNext := make(map[string]any, len(items[1]))
			for key, value := range items[1] {
				updatedNext[key] = value
			}
			if change == "missing" {
				delete(updatedNext, "MediaSources")
			} else {
				updatedNext["MediaSources"] = []any{map[string]any{"Id": "replacement", "Path": preloadTestFile(t, root, "replacement")}}
				if change == "ambiguous" || change == "recovered" {
					updatedNext["MediaSources"] = append(updatedNext["MediaSources"].([]any), items[1]["MediaSources"].([]any)...)
				}
			}
			options, _ := preloadTestOptions(t, root)
			entered, blocked, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(blocked) })
			t.Cleanup(release)
			var published atomic.Bool
			var preparations atomic.Int32
			recovered := make(chan struct{}, 1)
			options.NewPreload = func(raw, _ string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				if change == "recovered" && preparations.Add(1) > 1 {
					return raw, func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
							t.Error("restored source bypassed the existing account interval")
						}
						recovered <- struct{}{}
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}, true
				}
				return raw, func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
					defer close(completed)
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					close(entered)
					<-blocked
					task.Publish(func() { published.Store(true) })
					return playback.PreloadResult{Outcome: playback.PreloadPublished}
				}, true
			}
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					if r.URL.Query().Has("updated") {
						_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{items[0], updatedNext}})
					} else {
						_ = json.NewEncoder(w).Encode(queue)
					}
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Videos/"+current+"/stream?MediaSourceId="+source, "", false)
			report := `{"ItemId":"` + current + `","MediaSourceId":"` + source + `","PlaySessionId":"session","IsPaused":false,"PositionTicks":2060000000}`
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("next source did not enter signing")
			}
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes?updated", "", false)
			release()
			<-completed
			if published.Load() != (change == "missing") {
				t.Fatalf("old source publication=%v after %s", published.Load(), change)
			}
			if change == "recovered" {
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
				select {
				case <-recovered:
				case <-time.After(time.Second):
					t.Fatal("restored unique source did not obtain a fresh preparation opportunity")
				}
			}
		})
	}
}

func TestPreloadOlderDeniedDetailKeepsNewSuccess(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "latest")
			entered, release := make(chan struct{}), make(chan struct{})
			options, signs := preloadTestOptions(t, root)
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("older") == "true" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.WriteHeader(http.StatusForbidden)
					_, _ = io.WriteString(w, "original denied bytes")
					return
				}
				switch r.URL.Path {
				case "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/file":
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "logical", "Path": file}}})
				case "/v/api/v1/item/logical":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Movie","can_play":1}}`)
				case "/v/api/v1/play/info":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
				default:
					t.Errorf("意外接口：%s", r.URL.Path)
				}
			}))
			proxy := httptest.NewServer(inst)
			defer proxy.Close()
			detail := "/emby/Users/user/Items/file"
			if native {
				detail = "/v/api/v1/item/logical"
				preloadServeInstance(t, inst, http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"logical"}`, true)
			}
			request := preloadTestHTTP(t, proxy.URL+detail+"?older=true", http.MethodGet, "", native)
			preloadCompleteOlderRequest(t, request, entered, release, func() {
				preloadServeInstance(t, inst, http.MethodGet, detail, "", native)
			})
			if sign := waitPreloadSign(t, signs); !strings.Contains(sign.url, "pickcode=latest") {
				t.Fatal("旧拒绝撤销了新成功详情")
			}
		})
	}
}

func TestPreloadSameItemFreshDetailRefreshesObservation(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%t", native), func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "fresh")
			options, signs := preloadTestOptions(t, root)
			var details atomic.Int32
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/file":
					item := map[string]any{"Id": "file", "Type": "Movie"}
					if native || details.Add(1) > 1 {
						item["MediaSources"] = []any{map[string]any{"Id": "logical", "Path": file}}
					}
					_ = json.NewEncoder(w).Encode(item)
				case "/v/api/v1/item/logical":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","type":"Movie","can_play":1}}`)
				case "/v/api/v1/play/info":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"logical","media_guid":"file","video_guid":"video","type":"Movie","item":{"guid":"logical","type":"Movie","can_play":1}}}`)
				default:
					t.Errorf("意外接口：%s", r.URL.Path)
				}
			}))
			detail := "/emby/Users/user/Items/file"
			if native {
				detail = "/v/api/v1/item/logical"
			}
			preloadServeInstance(t, inst, http.MethodGet, detail, "", native)
			inst.preload.mu.Lock()
			for scope, device := range inst.preload.devices {
				device.detailAt = time.Now().Add(-2 * time.Minute)
				inst.preload.devices[scope] = device
			}
			inst.preload.mu.Unlock()
			preloadServeInstance(t, inst, http.MethodGet, detail, "", native)
			if native {
				preloadServeInstance(t, inst, http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"logical"}`, true)
			}
			if sign := waitPreloadSign(t, signs); !strings.Contains(sign.url, "pickcode=fresh") {
				t.Fatal("同项新详情未刷新关联时间以准备来源")
			}
		})
	}
}

func TestPreloadSameItemFreshDetailRetainsRetryBudget(t *testing.T) {
	root := t.TempDir()
	file := preloadTestFile(t, root, "retry")
	options, _ := preloadTestOptions(t, root)
	var users, attempts, factories atomic.Int32
	options.NewPreload = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
		factories.Add(1)
		return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}
			if attempts.Add(1) == 1 {
				return playback.PreloadResult{Outcome: playback.PreloadTransient, HTTPStatus: http.StatusServiceUnavailable}
			}
			return playback.PreloadResult{Outcome: playback.PreloadCached}
		}, true
	}
	inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/Me":
			users.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/file":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "Type": "Movie", "MediaSources": []any{map[string]any{"Id": "logical", "Path": file}}})
		default:
			t.Errorf("意外接口：%s", r.URL.Path)
		}
	}))
	detail := func() { preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/file", "", false) }
	detail()
	time.Sleep(2200 * time.Millisecond)
	waitPreloadWorker(t, options.Preloader)
	if attempts.Load() != 1 {
		t.Fatal("初始详情没有唯一签名尝试")
	}
	detail()
	waitPreloadWorker(t, options.Preloader)
	if attempts.Load() != 1 {
		t.Fatal("冷却内重复详情重置了签名机会")
	}
	time.Sleep(30 * time.Second)
	if attempts.Load() != 1 {
		t.Fatal("没有新信号却自动重试")
	}
	detail()
	time.Sleep(2200 * time.Millisecond)
	waitPreloadWorker(t, options.Preloader)
	for range 3 {
		detail()
	}
	time.Sleep(2200 * time.Millisecond)
	waitPreloadWorker(t, options.Preloader)
	if attempts.Load() != 2 || users.Load() != 1 || factories.Load() != 1 {
		t.Fatalf("新详情未保留准备结果或重试上限：attempts=%d users=%d factories=%d", attempts.Load(), users.Load(), factories.Load())
	}
}

func TestPreloadNativeCredentialsNormalizeCompatibleClient(t *testing.T) {
	for _, authorization := range []string{"", "native-session", "Bearer native-session"} {
		t.Run(authorization, func(t *testing.T) {
			request := preloadTestHTTP(t, "http://proxy.example/v/api/v1/play/info", http.MethodPost, `{ "item_guid": "logical" }`, true)
			request.Header.Set("Authorization", authorization)
			original := request.Header.Clone()
			headers, token, valid := preloadCredentials(request, true)
			if !valid || token != "native-session" || !strings.Contains(headers.Get("X-Emby-Authorization"), `Token="native-session"`) || !strings.Contains(headers.Get("X-Emby-Authorization"), `Client="`) {
				t.Fatalf("原生凭据没有转换成兼容 GET 凭据：valid=%t token-present=%t client-present=%t", valid, token != "", headers.Get("X-Emby-Authorization") != "")
			}
			if headers.Get("Authorization") != "" || playbackMetadataHeaders(headers).Get("Authx") != "" {
				t.Fatal("后台凭据保留了原生签名输入")
			}
			before, _ := json.Marshal(original)
			after, _ := json.Marshal(request.Header)
			if !bytes.Equal(before, after) {
				t.Fatal("规范化修改了原始请求头")
			}
		})
	}
	for _, conflict := range []string{"authorization", "token", "duplicate-cookie", "duplicate-authorization", "invalid-cookie"} {
		t.Run(conflict, func(t *testing.T) {
			request := preloadTestHTTP(t, "http://proxy.example/v/api/v1/play/info", http.MethodPost, `{}`, true)
			switch conflict {
			case "authorization":
				request.Header.Set("Authorization", "other-session")
			case "token":
				request.Header.Set("X-Emby-Token", "other-session")
			case "duplicate-cookie":
				request.Header.Add("Cookie", "Trim-MC-token=native-session")
			case "duplicate-authorization":
				request.Header.Add("Authorization", "native-session")
			case "invalid-cookie":
				request.Header.Set("Cookie", "Trim-MC-token=")
			}
			if _, _, valid := preloadCredentials(request, true); valid {
				t.Fatal("冲突或非法原生凭据被接受")
			}
		})
	}
}

func TestPreloadNativeOptionalVideoGUID(t *testing.T) {
	for _, field := range []string{"missing", "valid", "empty", "invalid", "number", "missing-logical", "missing-file"} {
		t.Run(field, func(t *testing.T) {
			options, _ := preloadTestOptions(t, t.TempDir())
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "unexpected metadata", http.StatusInternalServerError)
			}))
			request := preloadTestHTTP(t, "http://proxy.example/v/api/v1/play/info", http.MethodPost, `{"item_guid":"logical"}`, true)
			inst.capturePreloadBody(httptest.NewRecorder(), request)
			inst.beginPreloadRequest(request)
			payload := map[string]any{"code": 0, "data": map[string]any{"guid": "logical", "media_guid": "file", "type": "Movie", "item": map[string]any{"guid": "logical", "type": "Movie", "can_play": 1}}}
			data := payload["data"].(map[string]any)
			switch field {
			case "valid":
				data["video_guid"] = "video"
			case "empty":
				data["video_guid"] = ""
			case "invalid":
				data["video_guid"] = "../video"
			case "number":
				data["video_guid"] = 17
			case "missing-logical":
				delete(data, "guid")
			case "missing-file":
				delete(data, "media_guid")
			}
			body, _ := json.Marshal(payload)
			response := &http.Response{Request: request, StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}
			if err := inst.observePreloadResponse(response); err != nil {
				t.Fatal(err)
			}
			forwarded, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if !bytes.Equal(body, forwarded) {
				t.Fatal("观察改变了自然响应")
			}
			scope, _ := inst.preloadScope(request, true)
			inst.preload.mu.Lock()
			info := inst.preload.devices[scope].info
			inst.preload.mu.Unlock()
			want := field == "missing" || field == "valid"
			if (info.logical == "logical" && info.file == "file") != want {
				t.Fatalf("可选 video_guid 准入不符：field=%s accepted=%t want=%t", field, info.logical != "", want)
			}
		})
	}
}

func TestPreloadCompatibleMissingSessionUsesConfirmedEpoch(t *testing.T) {
	for _, change := range []string{"missing-session", "missing-source", "known-session", "no-stream", "other-device", "wrong-item", "wrong-source", "wrong-session", "ambiguous-source", "changed-source-path", "pause", "unknown-pause", "restart", "restart-stream", "new-explicit-session", "stopped"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			options, signs := preloadTestOptions(t, root)
			var metadata atomic.Int32
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					metadata.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			stream := func() {
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Videos/"+current+"/stream?MediaSourceId="+source, "", false)
			}
			if change != "no-stream" {
				stream()
			}
			report := map[string]any{"ItemId": current, "MediaSourceId": source, "IsPaused": false, "PositionTicks": 2060000000}
			if change == "missing-source" || change == "ambiguous-source" {
				delete(report, "MediaSourceId")
			}
			if change == "ambiguous-source" || change == "changed-source-path" {
				other := map[string]any{"Id": "other-source", "Path": preloadTestFile(t, root, "changed")}
				if change == "ambiguous-source" {
					items[0]["MediaSources"] = append(items[0]["MediaSources"].([]any), other)
				} else {
					items[0]["MediaSources"].([]any)[0].(map[string]any)["Path"] = other["Path"]
				}
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			}
			if change == "known-session" {
				report["PlaySessionId"] = "confirmed-session"
			}
			send := func(endpoint string) {
				body, _ := json.Marshal(report)
				request := preloadTestHTTP(t, "http://proxy.example/emby/Sessions/Playing"+endpoint, http.MethodPost, string(body), false)
				if change == "other-device" && endpoint == "/Progress" {
					request.Header.Set("X-Emby-Authorization", `MediaBrowser Token="valid", DeviceId="other-device"`)
				}
				inst.ServeHTTP(httptest.NewRecorder(), request)
			}
			send("")
			switch change {
			case "known-session":
				delete(report, "PlaySessionId")
			case "wrong-item":
				report["ItemId"] = "other-item"
			case "wrong-source":
				report["MediaSourceId"] = "other-source"
			case "wrong-session":
				report["PlaySessionId"] = "unconfirmed-session"
			case "pause":
				report["IsPaused"] = true
			case "unknown-pause":
				delete(report, "IsPaused")
			case "restart":
				send("")
			case "restart-stream":
				stream()
				send("")
			case "new-explicit-session":
				report["PlaySessionId"] = "new-session"
				send("")
			case "stopped":
				send("/Stopped")
			}
			before := metadata.Load()
			for range 10 {
				send("/Progress")
			}
			inst.preload.mu.Lock()
			for scope := range inst.preload.devices {
				options.Preloader.CancelDetail(scope)
			}
			inst.preload.mu.Unlock()
			waitPreloadWorker(t, options.Preloader)
			want := 0
			if change == "missing-session" || change == "missing-source" || change == "known-session" || change == "restart-stream" {
				want = 1
			}
			if len(signs) != want || metadata.Load()-before != int32(want) {
				t.Fatalf("本代次后继请求不符：signs=%d metadata=%d want=%d", len(signs), metadata.Load()-before, want)
			}
		})
	}
}

func TestPreloadNativeSeasonParentEnrichment(t *testing.T) {
	for _, phase := range []string{"before-info", "during-info", "after-info", "https-referrer", "navigation", "foreign-referrer", "non-http-referrer", "lookalike-season-path", "other-parent", "latest-denial", "business-denial", "older-denial", "old-parent-after-navigation"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			file := preloadTestFile(t, root, "season-parent")
			options, signs := preloadTestOptions(t, root)
			entered, release := make(chan struct{}), make(chan struct{})
			var metadata atomic.Int32
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("hold") == "true" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				switch r.URL.Path {
				case "/Users/Me":
					metadata.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/file":
					metadata.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "MediaSources": []any{map[string]any{"Id": "episode", "Path": file}}})
				case "/v/api/v1/item/season":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"season","type":"Season","parent_guid":"parent","can_play":1}}`)
				case "/v/api/v1/item/parent", "/v/api/v1/item/other-parent":
					if phase == "latest-denial" || phase == "older-denial" {
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, "denied")
						return
					}
					canPlay := 1
					if phase == "business-denial" {
						canPlay = 0
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"guid": strings.TrimPrefix(r.URL.Path, "/v/api/v1/item/"), "type": "TV", "can_play": canPlay}})
				case "/v/api/v1/item/movie":
					_, _ = io.WriteString(w, `{"code":0,"data":{"guid":"movie","type":"Movie","can_play":1}}`)
				case "/v/api/v1/play/info":
					var request map[string]any
					_ = json.NewDecoder(r.Body).Decode(&request)
					logical, kind := "episode", "Episode"
					if request["item_guid"] == "movie" {
						logical, kind = "movie", "Movie"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"guid": logical, "media_guid": "file", "video_guid": "video", "parent_guid": "season", "type": kind, "item": map[string]any{"guid": logical, "type": kind, "can_play": 1}}})
				default:
					t.Errorf("意外接口：%s", r.URL.Path)
				}
			}))
			proxy := httptest.NewServer(inst)
			defer proxy.Close()
			request := func(method, endpoint, body, referer string) *http.Request {
				r := preloadTestHTTP(t, proxy.URL+endpoint, method, body, true)
				r.Header.Set("Referer", proxy.URL+referer)
				return r
			}
			send := func(r *http.Request) {
				response, err := noRedirectClient().Do(r)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
			}
			detail := func() { send(request(http.MethodGet, "/v/api/v1/item/season", "", "/v/tv/parent")) }
			info := func() {
				send(request(http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"season"}`, "/v/tv/season/season"))
			}
			parent := request(http.MethodGet, "/v/api/v1/item/parent", "", "/v/tv/season/season")
			detail()
			switch phase {
			case "before-info":
				send(parent)
				info()
			case "during-info":
				pending := request(http.MethodPost, "/v/api/v1/play/info?hold=true", `{"item_guid":"season"}`, "/v/tv/season/season")
				preloadCompleteOlderRequest(t, pending, entered, release, func() { send(parent) })
			case "older-denial", "old-parent-after-navigation":
				info()
				parent.URL.RawQuery = "hold=true"
				preloadCompleteOlderRequest(t, parent, entered, release, func() {
					if phase == "older-denial" {
						detail()
						info()
					} else {
						send(request(http.MethodGet, "/v/api/v1/item/movie", "", "/v/movie/movie"))
						send(request(http.MethodPost, "/v/api/v1/play/info", `{"item_guid":"movie"}`, "/v/movie/movie"))
					}
				})
			default:
				info()
				switch phase {
				case "https-referrer":
					parent.Header.Set("Referer", strings.Replace(proxy.URL, "http://", "https://", 1)+"/v/tv/season/season")
				case "navigation":
					parent.Header.Set("Referer", proxy.URL+"/v/tv/parent")
				case "foreign-referrer":
					parent.Header.Set("Referer", "http://foreign.invalid/v/tv/season/season")
				case "non-http-referrer":
					parent.Header.Set("Referer", strings.Replace(proxy.URL, "http://", "ftp://", 1)+"/v/tv/season/season")
				case "lookalike-season-path":
					parent.Header.Set("Referer", proxy.URL+"/v/tv/season/season-extra")
				case "other-parent":
					parent.URL.Path = "/v/api/v1/item/other-parent"
				}
				send(parent)
			}
			want := phase == "before-info" || phase == "during-info" || phase == "after-info" || phase == "https-referrer" || phase == "older-denial" || phase == "old-parent-after-navigation"
			if want {
				if phase == "old-parent-after-navigation" {
					// 新 Movie 的逻辑源必须匹配；旧父响应不可把它改回 Season。
					inst.preload.mu.Lock()
					for _, device := range inst.preload.devices {
						if device.detail.id != "movie" || device.info.logical != "movie" {
							t.Error("旧父资料覆盖了新导航")
						}
					}
					inst.preload.mu.Unlock()
					return
				}
				waitPreloadSign(t, signs)
				if metadata.Load() != 2 {
					t.Fatalf("父资料导致额外补查：%d", metadata.Load())
				}
				return
			}
			time.Sleep(2200 * time.Millisecond)
			waitPreloadWorker(t, options.Preloader)
			if metadata.Load() != 0 || len(signs) != 0 {
				t.Fatalf("导航或权限拒绝没有撤销旧候选：metadata=%d signs=%d", metadata.Load(), len(signs))
			}
		})
	}
}

func TestPreloadMissingSessionNewStartIntentFencesOldUA(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			options, signs := preloadTestOptions(t, root)
			entered, release := make(chan struct{}), make(chan struct{})
			var metadata atomic.Int32
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("hold") == "true" {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					w.WriteHeader(status)
					return
				}
				switch r.URL.Path {
				case "/Users/Me":
					metadata.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Videos/"+current+"/stream?MediaSourceId="+source, "", false)
			body := `{"ItemId":"` + current + `","MediaSourceId":"` + source + `","IsPaused":false,"PositionTicks":2060000000}`
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", body, false)
			before := metadata.Load()
			proxy := httptest.NewServer(inst)
			defer proxy.Close()
			request := preloadTestHTTP(t, proxy.URL+"/emby/Sessions/Playing?hold=true", http.MethodPost, body, false)
			preloadCompleteOlderRequest(t, request, entered, release, func() {
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", body, false)
				waitPreloadWorker(t, options.Preloader)
				if len(signs) != 0 || metadata.Load() != before {
					t.Error("新开始响应未完成时，缺会话进度复用了旧播放 UA")
				}
			})
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", body, false)
			waitPreloadWorker(t, options.Preloader)
			if len(signs) != 0 || metadata.Load() != before {
				t.Fatal("迟到开始响应恢复了旧代次播放 UA")
			}
		})
	}
}

func TestPreloadMissingSessionStreamOrdering(t *testing.T) {
	for _, phase := range []string{"new-start", "stop", "old-stop", "fresh-after-start", "failed-stream"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			options, signs := preloadTestOptions(t, root)
			entered, release := make(chan struct{}), make(chan struct{})
			var streams, metadata atomic.Int32
			options.MainHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if streams.Add(1) == 2 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					if phase == "failed-stream" {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
				}
				w.Header().Set("Location", "https://cdn.115cdn.net/current")
				w.WriteHeader(http.StatusFound)
			})
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					metadata.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			stream := "/emby/Videos/" + current + "/stream?MediaSourceId=" + source
			preloadServeInstance(t, inst, http.MethodGet, stream, "", false)
			body := `{"ItemId":"` + current + `","MediaSourceId":"` + source + `","IsPaused":false,"PositionTicks":2060000000}`
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", body, false)
			if phase == "fresh-after-start" || phase == "failed-stream" {
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", body, false)
			}
			proxy := httptest.NewServer(inst)
			defer proxy.Close()
			pending := preloadTestHTTP(t, proxy.URL+stream, http.MethodGet, "", false)
			preloadCompleteOlderRequest(t, pending, entered, release, func() {
				switch phase {
				case "new-start":
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", body, false)
				case "stop":
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Stopped", body, false)
				case "old-stop":
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Stopped", `{"ItemId":"old-episode"}`, false)
				}
			})
			before := metadata.Load()
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", body, false)
			waitPreloadWorker(t, options.Preloader)
			want := 0
			if phase == "old-stop" || phase == "fresh-after-start" {
				want = 1
			}
			if len(signs) != want || metadata.Load()-before != int32(want) {
				t.Fatalf("取流与新代次交错不符：signs=%d metadata=%d want=%d", len(signs), metadata.Load()-before, want)
			}
		})
	}
}

func TestPreloadCompatibleStreamSessionMatchesNewStart(t *testing.T) {
	for _, test := range []struct {
		name, query   string
		startFirst    bool
		missingStart  bool
		oldStop       bool
		wantSignature bool
	}{
		{name: "old-session-before-start", query: "&PlaySessionId=old-session"},
		{name: "new-session-before-start", query: "&PlaySessionId=new-session", wantSignature: true},
		{name: "stream-session-without-start-session", query: "&PlaySessionId=new-session", missingStart: true, wantSignature: true},
		{name: "unknown-session-before-start", wantSignature: true},
		{name: "new-session-before-old-stop", query: "&PlaySessionId=new-session", oldStop: true, wantSignature: true},
		{name: "old-session-after-start", query: "&PlaySessionId=old-session", startFirst: true},
		{name: "new-session-after-start", query: "&PlaySessionId=new-session", startFirst: true, wantSignature: true},
		{name: "unknown-session-after-start", startFirst: true, wantSignature: true},
		{name: "duplicate-session", query: "&PlaySessionId=new-session&PlaySessionId=old-session"},
		{name: "invalid-session", query: "&PlaySessionId=..%2Fsession"},
	} {
		for _, includeSession := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/session=%t", test.name, includeSession), func(t *testing.T) {
				root := t.TempDir()
				queue, items := preloadQueueFixture(t, root)
				current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
				source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
				options, signs := preloadTestOptions(t, root)
				var metadata atomic.Int32
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/Users/Me":
						metadata.Add(1)
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/" + current:
						_ = json.NewEncoder(w).Encode(items[0])
					case "/Shows/" + series + "/Episodes":
						_ = json.NewEncoder(w).Encode(queue)
					default:
						w.WriteHeader(http.StatusNoContent)
					}
				}))
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
				stream := func(query, ua string) {
					request := preloadTestHTTP(t, "http://proxy.example/emby/Videos/"+current+"/stream?MediaSourceId="+source+query, http.MethodGet, "", false)
					request.Header.Set("User-Agent", ua)
					inst.ServeHTTP(httptest.NewRecorder(), request)
				}
				report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "old-session", "IsPaused": false, "PositionTicks": 2060000000}
				send := func(endpoint string) {
					body, _ := json.Marshal(report)
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing"+endpoint, string(body), false)
				}
				stream("&PlaySessionId=old-session", "Player/old")
				send("")
				if test.startFirst {
					report["PlaySessionId"] = "new-session"
					send("")
				}
				stream(test.query, "Player/current")
				if test.oldStop {
					send("/Stopped")
				}
				if !test.startFirst {
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
				before := metadata.Load()
				for range 10 {
					send("/Progress")
				}
				waitPreloadWorker(t, options.Preloader)
				want := 0
				if test.wantSignature {
					want = 1
				}
				if len(signs) != want || metadata.Load()-before != int32(want) {
					t.Fatalf("真实流的会话关联不符：signs=%d metadata=%d want=%d", len(signs), metadata.Load()-before, want)
				}
				if want == 1 && (<-signs).ua != "Player/current" {
					t.Fatal("新会话借用了旧流 UA")
				}
			})
		}
	}
}

func TestPreloadRepeatedPlayingPreservesNextOpportunity(t *testing.T) {
	for _, phase := range []string{"queued", "preparing", "signing"} {
		for _, action := range []string{"repeat", "repeat-optional-source", "repeat-optional-pause", "repeat-during-range", "new-session", "new-source", "new-source-path", "ambiguous-source", "sessionless-restart", "transcode", "transcode-pending-response", "paused", "paused-pending-response", "denied"} {
			t.Run(phase+"/"+action, func(t *testing.T) {
				root := t.TempDir()
				queue, items := preloadQueueFixture(t, root)
				current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
				source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
				options, _ := preloadTestOptions(t, root)
				entered, released := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				t.Cleanup(release)
				var background atomic.Bool
				var metadata, signatures, published atomic.Int32
				options.NewPreload = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						signatures.Add(1)
						if phase == "signing" {
							close(entered)
							select {
							case <-released:
							case <-ctx.Done():
								return playback.PreloadResult{Outcome: playback.PreloadSkipped}
							}
						}
						if ua != "Confirmed Player" || !strings.Contains(raw, "pickcode=next") {
							t.Errorf("wrong actual UA or successor: ua=%q", ua)
						}
						if !task.Publish(func() { published.Add(1) }) {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}, true
				}
				rangeEntered, rangeReleased := make(chan struct{}), make(chan struct{})
				releaseRange := sync.OnceFunc(func() { close(rangeReleased) })
				t.Cleanup(releaseRange)
				reportEntered, reportReleased := make(chan struct{}), make(chan struct{})
				releaseReport := sync.OnceFunc(func() { close(reportReleased) })
				t.Cleanup(releaseReport)
				options.MainHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Range") != "" {
						close(rangeEntered)
						select {
						case <-rangeReleased:
						case <-r.Context().Done():
							return
						}
					}
					w.Header().Set("Location", "https://cdn.115cdn.net/current")
					w.WriteHeader(http.StatusFound)
				})
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("hold") != "" {
						close(reportEntered)
						select {
						case <-reportReleased:
						case <-r.Context().Done():
							return
						}
					}
					switch r.URL.Path {
					case "/Users/Me":
						if background.Load() && strings.Contains(r.URL.Query().Get("Fields"), "SeriesId") {
							metadata.Add(1)
							if phase == "preparing" {
								close(entered)
								select {
								case <-released:
								case <-r.Context().Done():
									return
								}
							}
						}
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/" + current:
						_ = json.NewEncoder(w).Encode(items[0])
					case "/Shows/" + series + "/Episodes":
						_ = json.NewEncoder(w).Encode(queue)
					default:
						if r.URL.Query().Get("denied") != "" {
							w.WriteHeader(http.StatusForbidden)
							_, _ = io.WriteString(w, "original denied report")
							return
						}
						w.WriteHeader(http.StatusNoContent)
					}
				}))
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
				stream := "/emby/Videos/" + current + "/stream?MediaSourceId=" + source + "&PlaySessionId=session"
				preloadServeInstance(t, inst, http.MethodGet, stream, "", false)
				report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "session", "IsPaused": false, "PositionTicks": 2060000000}
				send := func(route string) *httptest.ResponseRecorder {
					t.Helper()
					body, _ := json.Marshal(report)
					return preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing"+route, string(body), false)
				}
				send("")
				if phase == "queued" {
					if !options.Preloader.Submit(playback.PreloadCandidate{Key: "blocker", Scope: "blocker", Kind: playback.PreloadNext,
						Run: func(ctx context.Context, _ *playback.PreloadTask) playback.PreloadResult {
							close(entered)
							select {
							case <-released:
							case <-ctx.Done():
							}
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}}) {
						t.Fatal("queue blocker rejected")
					}
					<-entered
				}
				background.Store(true)
				send("/Progress")
				if phase != "queued" {
					select {
					case <-entered:
					case <-time.After(3 * time.Second):
						t.Fatal("successor did not enter gated phase")
					}
				}
				var rangeDone chan struct{}
				if action == "repeat-during-range" {
					rangeDone = make(chan struct{})
					request := preloadTestHTTP(t, "http://proxy.example"+stream, http.MethodGet, "", false)
					request.Header.Set("Range", "bytes=0-1")
					go func() {
						inst.ServeHTTP(httptest.NewRecorder(), request)
						close(rangeDone)
					}()
					select {
					case <-rangeEntered:
					case <-time.After(3 * time.Second):
						t.Fatal("actual Range did not enter")
					}
				}
				switch strings.TrimSuffix(action, "-pending-response") {
				case "repeat-optional-source":
					delete(report, "MediaSourceId")
				case "repeat-optional-pause":
					delete(report, "IsPaused")
				case "new-session":
					report["PlaySessionId"] = "new-session"
				case "new-source":
					report["MediaSourceId"] = "new-source"
				case "new-source-path", "ambiguous-source":
					original := items[0]["MediaSources"].([]any)[0].(map[string]any)
					changed := make(map[string]any, len(original))
					for key, value := range original {
						changed[key] = value
					}
					changed["Path"] = preloadTestFile(t, root, "changed-path")
					items[0]["MediaSources"] = []any{changed}
					if action == "ambiguous-source" {
						changed["Id"] = "other-source"
						items[0]["MediaSources"] = []any{original, changed}
						delete(report, "MediaSourceId")
					}
					preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
				case "sessionless-restart":
					delete(report, "PlaySessionId")
				case "transcode":
					report["PlayMethod"] = "Transcode"
				case "paused":
					report["IsPaused"] = true
				}
				query := ""
				if action == "denied" {
					query = "?denied=true"
				}
				var response *httptest.ResponseRecorder
				if strings.HasSuffix(action, "-pending-response") {
					responses := make(chan *httptest.ResponseRecorder, 1)
					go func() { responses <- send("?hold=true") }()
					select {
					case <-reportEntered:
					case <-time.After(3 * time.Second):
						t.Fatal("Playing report did not reach upstream")
					}
					release()
					waitPreloadWorker(t, options.Preloader)
					releaseReport()
					select {
					case response = <-responses:
					case <-time.After(3 * time.Second):
						t.Fatal("Playing report did not drain")
					}
				} else {
					response = send(query)
				}
				if action == "denied" {
					if response.Code != http.StatusForbidden || response.Body.String() != "original denied report" {
						t.Fatal("denied Playing response changed")
					}
				} else if response.Code != http.StatusNoContent {
					t.Fatal("Playing response changed")
				}
				repeated := strings.HasPrefix(action, "repeat")
				if action == "repeat" || action == "repeat-optional-source" {
					for range 10 {
						send("/Progress")
					}
				}
				release()
				waitPreloadWorker(t, options.Preloader)
				releaseRange()
				if rangeDone != nil {
					select {
					case <-rangeDone:
					case <-time.After(3 * time.Second):
						t.Fatal("actual Range did not drain")
					}
				}
				if repeated {
					report["IsPaused"] = false
					for range 10 {
						send("/Progress")
					}
					waitPreloadWorker(t, options.Preloader)
				}
				wantSign, wantMetadata, wantPublished := int32(0), int32(1), int32(0)
				if repeated {
					wantSign, wantPublished = 1, 1
				} else if phase == "queued" {
					wantMetadata = 0
				} else if phase == "signing" {
					wantSign = 1
				}
				if signatures.Load() != wantSign || metadata.Load() != wantMetadata || published.Load() != wantPublished {
					t.Fatalf("Playing opportunity: signatures=%d metadata=%d published=%d want=%d/%d/%d", signatures.Load(), metadata.Load(), published.Load(), wantSign, wantMetadata, wantPublished)
				}
			})
		}
	}
}

func TestPreloadLatestSourceAmbiguityRevokesDetail(t *testing.T) {
	for _, entry := range []string{"detail", "playback-info", "playback-info-mixed"} {
		for _, action := range []string{"debounce", "signing", "explicit", "partial", "older", "older-than-info", "recover", "refreshed-detail"} {
			if entry != "detail" && action == "older-than-info" || entry == "detail" && action == "refreshed-detail" || entry == "playback-info-mixed" && action != "debounce" && action != "signing" && action != "explicit" && action != "older" {
				continue
			}
			t.Run(entry+"/"+action, func(t *testing.T) {
				root := t.TempDir()
				first, second := preloadTestFile(t, root, "first"), preloadTestFile(t, root, "second")
				if entry == "playback-info-mixed" {
					second = filepath.Join(root, "local-video.mkv")
				}
				media := []any{map[string]any{"Id": "first", "Path": first}, map[string]any{"Id": "second", "Path": second}}
				options, _ := preloadTestOptions(t, root)
				entered, released := make(chan struct{}, 1), make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				t.Cleanup(release)
				var signatures atomic.Int32
				published := make(chan string, 2)
				options.NewPreload = func(raw, _ string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						signatures.Add(1)
						entered <- struct{}{}
						if action == "signing" {
							select {
							case <-released:
							case <-ctx.Done():
							}
						}
						task.Publish(func() { published <- raw })
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}, true
				}
				olderEntered, olderReleased := make(chan struct{}), make(chan struct{})
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("older") != "" {
						close(olderEntered)
						select {
						case <-olderReleased:
						case <-r.Context().Done():
							return
						}
					}
					if r.URL.Path == "/Users/Me" {
						_, _ = io.WriteString(w, `{"Id":"user"}`)
						return
					}
					payload := map[string]any{"Id": "movie", "Type": "Movie", "MediaSources": media}
					if r.URL.Query().Get("view") == "single" || action == "partial" && strings.Contains(r.URL.Query().Get("Fields"), "SeriesId") {
						payload["MediaSources"] = media[:1]
					} else if r.URL.Query().Get("view") == "partial" {
						delete(payload, "MediaSources")
					}
					body, _ := json.Marshal(payload)
					_, _ = w.Write(append([]byte(" \n"), body...))
				}))
				const detail = "/emby/Users/user/Items/movie"
				preloadServeInstance(t, inst, http.MethodGet, detail+"?view=single", "", false)
				if action == "refreshed-detail" {
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Items/movie/PlaybackInfo", "{}", false)
					preloadServeInstance(t, inst, http.MethodGet, detail+"?view=single", "", false)
				}
				if action == "signing" {
					select {
					case <-entered:
					case <-time.After(4 * time.Second):
						t.Fatal("initial detail did not enter signature")
					}
				}
				method, endpoint, body := http.MethodGet, detail, ""
				if entry != "detail" {
					method, endpoint, body = http.MethodPost, "/emby/Items/movie/PlaybackInfo", "{}"
				}
				endpoint += "?view=multiple"
				if action == "explicit" || action == "older-than-info" {
					endpoint += "&MediaSourceId=first"
				} else if action == "partial" {
					endpoint = strings.Replace(endpoint, "view=multiple", "view=partial", 1)
				}
				if action == "older" || action == "older-than-info" {
					proxy := httptest.NewServer(inst)
					defer proxy.Close()
					request := preloadTestHTTP(t, proxy.URL+endpoint+"&older=true", method, body, false)
					preloadCompleteOlderRequest(t, request, olderEntered, olderReleased, func() {
						if action == "older-than-info" {
							preloadServeInstance(t, inst, http.MethodPost, "/emby/Items/movie/PlaybackInfo?MediaSourceId=second", "{}", false)
						} else if entry == "playback-info-mixed" {
							preloadServeInstance(t, inst, http.MethodGet, detail+"?view=multiple&MediaSourceId=first", "", false)
						} else {
							preloadServeInstance(t, inst, http.MethodGet, detail+"?view=single", "", false)
						}
					})
				} else {
					response := preloadServeInstance(t, inst, method, endpoint, body, false)
					if response.Code != http.StatusOK || entry == "detail" && !strings.HasPrefix(response.Body.String(), " \n") {
						t.Fatal("natural response status or detail bytes changed")
					}
				}
				if action == "recover" {
					preloadServeInstance(t, inst, http.MethodGet, detail+"?view=multiple&MediaSourceId=second", "", false)
				}
				release()
				time.Sleep(2200 * time.Millisecond)
				waitPreloadWorker(t, options.Preloader)
				want := 1
				if action == "debounce" || action == "signing" || action == "refreshed-detail" {
					want = 0
				}
				if len(published) != want || want == 0 && action != "signing" && signatures.Load() != 0 || action == "signing" && signatures.Load() != 1 {
					t.Fatalf("obsolete source survived: signatures=%d publications=%d want=%d", signatures.Load(), len(published), want)
				}
				if want == 1 {
					pick := "first"
					if action == "recover" || action == "older-than-info" {
						pick = "second"
					}
					if !strings.Contains(<-published, "pickcode="+pick) {
						t.Fatal("wrong source published after latest observation")
					}
				}
			})
		}
	}
}

func TestPreloadLatestResumeRevokesDetail(t *testing.T) {
	for _, kind := range []string{"Series", "Season"} {
		for _, action := range []string{"empty", "multiple", "changed", "same", "other-user", "other-series", "other-season", "older-empty", "episode", "signing", "source-ambiguous", "source-changed", "source-partial", "info-ambiguous", "info-changed", "info-explicit", "info-refreshed", "info-older", "source-older", "source-ambiguous-signing", "info-ambiguous-signing"} {
			if kind == "Series" && action == "other-season" {
				continue
			}
			t.Run(kind+"/"+action, func(t *testing.T) {
				root := t.TempDir()
				queue, items := preloadQueueFixture(t, root)
				series, season := stringField(items[0], "SeriesId"), stringField(items[0], "SeasonId")
				id := series
				if kind == "Season" {
					id = season
				}
				if action == "episode" {
					id = stringField(items[1], "Id")
				}
				updated := map[string]any{"Items": []any{items[0], items[1]}}
				if action == "empty" || action == "signing" || action == "other-user" || action == "other-series" || action == "other-season" || action == "older-empty" || action == "episode" {
					updated["Items"] = []any{}
				} else if action == "multiple" || action == "changed" {
					first, second := make(map[string]any), make(map[string]any)
					for key, value := range items[0] {
						first[key] = value
					}
					for key, value := range items[1] {
						second[key] = value
					}
					first["UserData"] = map[string]any{"Played": false, "PlaybackPositionTicks": 10000000}
					if action == "changed" {
						second["UserData"] = map[string]any{"Played": true, "PlaybackPositionTicks": 0}
					}
					updated["Items"] = []any{first, second}
				}
				resumeID := stringField(items[1], "Id")
				latestResume := make(map[string]any)
				for key, value := range items[1] {
					latestResume[key] = value
				}
				if strings.HasPrefix(action, "source-") || strings.HasPrefix(action, "info-") {
					media := items[1]["MediaSources"].([]any)
					replacement := map[string]any{"Id": "alternate", "Path": preloadTestFile(t, root, "replacement")}
					latestResume["MediaSources"] = []any{media[0], replacement}
					if strings.HasSuffix(action, "changed") {
						replacement["Id"] = stringField(media[0].(map[string]any), "Id")
						latestResume["MediaSources"] = []any{replacement}
					} else if action == "source-partial" {
						delete(latestResume, "MediaSources")
					}
					updated["Items"] = []any{items[0], latestResume}
				}
				options, _ := preloadTestOptions(t, root)
				entered, released := make(chan struct{}, 1), make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				t.Cleanup(release)
				published := make(chan string, 2)
				var signatures atomic.Int32
				options.NewPreload = func(raw, _ string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
							signatures.Add(1)
							entered <- struct{}{}
							if action == "signing" || strings.HasSuffix(action, "-signing") {
								select {
								case <-released:
								case <-ctx.Done():
								}
							}
							task.Publish(func() { published <- raw })
						}
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}, true
				}
				olderEntered, olderReleased := make(chan struct{}), make(chan struct{})
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("older") != "" {
						close(olderEntered)
						select {
						case <-olderReleased:
						case <-r.Context().Done():
							return
						}
					}
					switch r.URL.Path {
					case "/Users/Me":
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Items/" + resumeID + "/PlaybackInfo", "/Users/user/Items/" + resumeID:
						if action == "source-partial" {
							_ = json.NewEncoder(w).Encode(items[1])
						} else {
							_ = json.NewEncoder(w).Encode(latestResume)
						}
					case "/Users/user/Items/" + id:
						if action == "episode" {
							_ = json.NewEncoder(w).Encode(items[1])
						} else {
							_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Type": kind, "SeriesId": series})
						}
					default:
						if r.URL.Query().Get("updated") != "" || r.URL.Query().Get("older") != "" {
							_ = json.NewEncoder(w).Encode(updated)
						} else {
							_ = json.NewEncoder(w).Encode(queue)
						}
					}
				}))
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+id, "", false)
				endpoint := "/emby/Shows/" + series + "/Episodes"
				preloadServeInstance(t, inst, http.MethodGet, endpoint, "", false)
				var independentPublications atomic.Int32
				if kind == "Series" && action == "empty" {
					scope, _ := inst.preloadScope(preloadTestHTTP(t, "http://proxy.example/emby/Users/user/Items/"+id, http.MethodGet, "", false), false)
					nextEntered := make(chan struct{})
					account := playback.PreloadAccount{Provider: "115", AccountID: 2, UserID: "independent-owner"}
					if !options.Preloader.Submit(playback.PreloadCandidate{Key: "independent-next", Scope: scope, Kind: playback.PreloadNext, Account: account,
						Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
							if task.BeginSignature(account, "independent-next") {
								close(nextEntered)
								select {
								case <-released:
								case <-ctx.Done():
								}
								task.Publish(func() { independentPublications.Add(1) })
							}
							return playback.PreloadResult{Outcome: playback.PreloadPublished}
						}}) {
						t.Fatal("independent next rejected")
					}
					select {
					case <-nextEntered:
					case <-time.After(time.Second):
						t.Fatal("independent next did not start")
					}
				}
				if action == "signing" || strings.HasSuffix(action, "-signing") {
					select {
					case <-entered:
					case <-time.After(4 * time.Second):
						t.Fatal("initial resume detail did not enter signature")
					}
				}
				if action == "older-empty" || action == "info-older" || action == "source-older" {
					proxy := httptest.NewServer(inst)
					defer proxy.Close()
					method, oldEndpoint, body := http.MethodGet, endpoint, ""
					if action == "info-older" {
						method, oldEndpoint, body = http.MethodPost, "/emby/Items/"+resumeID+"/PlaybackInfo", "{}"
					}
					request := preloadTestHTTP(t, proxy.URL+oldEndpoint+"?older=true", method, body, false)
					preloadCompleteOlderRequest(t, request, olderEntered, olderReleased, func() {
						if action == "source-older" {
							preloadServeInstance(t, inst, http.MethodPost, "/emby/Items/"+resumeID+"/PlaybackInfo?MediaSourceId=alternate", "{}", false)
						} else {
							preloadServeInstance(t, inst, http.MethodGet, endpoint, "", false)
						}
					})
				} else if strings.HasPrefix(action, "info-") {
					endpoint = "/emby/Items/" + resumeID + "/PlaybackInfo"
					if action == "info-explicit" || action == "info-refreshed" {
						endpoint += "?MediaSourceId=alternate"
					}
					preloadServeInstance(t, inst, http.MethodPost, endpoint, "{}", false)
					if action == "info-refreshed" {
						preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+id, "", false)
					}
				} else {
					if action == "other-series" {
						endpoint = "/emby/Shows/other-series/Episodes"
					}
					endpoint += "?updated=true"
					if action == "other-user" {
						endpoint += "&UserId=other-user"
					} else if action == "other-season" {
						endpoint += "&SeasonId=other-season"
					}
					preloadServeInstance(t, inst, http.MethodGet, endpoint, "", false)
				}
				release()
				time.Sleep(2200 * time.Millisecond)
				waitPreloadWorker(t, options.Preloader)
				if kind == "Series" && action == "empty" && independentPublications.Load() != 1 {
					t.Error("detail revocation canceled an independent playing-next task")
				}
				want := 1
				if action == "empty" || action == "multiple" || action == "signing" || strings.Contains(action, "ambiguous") {
					want = 0
				}
				if len(published) != want || want == 0 && action != "signing" && !strings.HasSuffix(action, "-signing") && signatures.Load() != 0 {
					t.Fatalf("resume invalidation failed: signatures=%d publications=%d want=%d", signatures.Load(), len(published), want)
				}
				if want != 0 {
					pick := "next"
					if action == "changed" {
						pick = "current"
					} else if strings.HasSuffix(action, "changed") || action == "info-explicit" || action == "info-refreshed" || action == "source-older" {
						pick = "replacement"
					}
					if !strings.Contains(<-published, "pickcode="+pick) {
						t.Fatal("latest unique resume was not used")
					}
				}
			})
		}
	}
}

func TestPreloadProgressInvalidatesBeforeUpstream(t *testing.T) {
	for _, test := range []struct{ action, phase string }{
		{"pause", "queued"}, {"pause", "preparing"}, {"pause", "signing"},
		{"transcode", "queued"}, {"transcode", "signing"}, {"missing-session", "preparing"},
		{"missing-source", "signing"}, {"old-item", "signing"}, {"old-source", "preparing"},
		{"old-session", "signing"}, {"ongoing", "signing"}, {"old-session-during-range", "signing"},
	} {
		t.Run(test.action+"/"+test.phase, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
			source := stringField(items[0]["MediaSources"].([]any)[0].(map[string]any), "Id")
			options, _ := preloadTestOptions(t, root)
			entered, released := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			t.Cleanup(release)
			reportEntered, reportReleased := make(chan []byte, 1), make(chan struct{})
			releaseReport := sync.OnceFunc(func() { close(reportReleased) })
			t.Cleanup(releaseReport)
			rangeEntered, rangeReleased := make(chan struct{}), make(chan struct{})
			releaseRange := sync.OnceFunc(func() { close(rangeReleased) })
			t.Cleanup(releaseRange)
			var background atomic.Bool
			var metadata, signatures, publications atomic.Int32
			options.NewPreload = func(raw, _ string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					signatures.Add(1)
					if test.phase == "signing" {
						close(entered)
						select {
						case <-released:
						case <-ctx.Done():
						}
					}
					task.Publish(func() { publications.Add(1) })
					return playback.PreloadResult{Outcome: playback.PreloadPublished}
				}, true
			}
			options.MainHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "" {
					close(rangeEntered)
					select {
					case <-rangeReleased:
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Location", "https://cdn.115cdn.net/current")
				w.WriteHeader(http.StatusFound)
			})
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("hold") != "" {
					body, _ := io.ReadAll(r.Body)
					reportEntered <- body
					select {
					case <-reportReleased:
					case <-r.Context().Done():
						return
					}
				}
				switch r.URL.Path {
				case "/Users/Me":
					if background.Load() && strings.Contains(r.URL.Query().Get("Fields"), "SeriesId") {
						metadata.Add(1)
						if test.phase == "preparing" {
							close(entered)
							select {
							case <-released:
							case <-r.Context().Done():
								return
							}
						}
					}
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			stream := "/emby/Videos/" + current + "/stream?MediaSourceId=" + source + "&PlaySessionId=session"
			preloadServeInstance(t, inst, http.MethodGet, stream, "", false)
			report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "session", "IsPaused": false, "PositionTicks": 2060000000}
			original, _ := json.Marshal(report)
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", string(original), false)
			if test.phase == "queued" {
				if !options.Preloader.Submit(playback.PreloadCandidate{Key: "blocker", Scope: "blocker", Kind: playback.PreloadNext,
					Run: func(ctx context.Context, _ *playback.PreloadTask) playback.PreloadResult {
						close(entered)
						select {
						case <-released:
						case <-ctx.Done():
						}
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}}) {
					t.Fatal("queue blocker rejected")
				}
				<-entered
			}
			background.Store(true)
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", string(original), false)
			if test.phase != "queued" {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("next item did not enter gated phase")
				}
			}
			var rangeDone chan struct{}
			if test.action == "old-session-during-range" {
				rangeDone = make(chan struct{})
				request := preloadTestHTTP(t, "http://proxy.example"+stream, http.MethodGet, "", false)
				request.Header.Set("Range", "bytes=0-1")
				request.Header.Set("User-Agent", "Current Range Player/2")
				go func() {
					inst.ServeHTTP(httptest.NewRecorder(), request)
					close(rangeDone)
				}()
				select {
				case <-rangeEntered:
				case <-time.After(3 * time.Second):
					t.Fatal("range stream did not reach handler")
				}
			}
			if test.action != "ongoing" {
				report["IsPaused"] = true
			}
			switch test.action {
			case "transcode":
				report["IsPaused"], report["PlayMethod"] = false, "Transcode"
			case "missing-session":
				delete(report, "PlaySessionId")
			case "missing-source":
				delete(report, "MediaSourceId")
			case "old-item":
				report["ItemId"] = "old-item"
			case "old-source":
				report["MediaSourceId"] = "old-source"
			case "old-session", "old-session-during-range":
				report["PlaySessionId"] = "old-session"
			}
			encoded, _ := json.Marshal(report)
			body := " \n" + string(encoded) + " \n"
			responses := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				request := preloadTestHTTP(t, "http://proxy.example/emby/Sessions/Playing/Progress?hold=true", http.MethodPost, body, false)
				request.GetBody = nil
				response := httptest.NewRecorder()
				inst.ServeHTTP(response, request)
				responses <- response
			}()
			select {
			case forwarded := <-reportEntered:
				if string(forwarded) != body {
					t.Error("Progress request bytes changed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Progress did not reach upstream")
			}
			release()
			waitPreloadWorker(t, options.Preloader)
			want := int32(0)
			if test.action == "ongoing" || strings.HasPrefix(test.action, "old-") {
				want = 1
			}
			if publications.Load() != want || want == 0 && test.phase != "signing" && signatures.Load() != 0 {
				t.Errorf("pending Progress did not invalidate correctly: metadata=%d signatures=%d publications=%d want=%d", metadata.Load(), signatures.Load(), publications.Load(), want)
			}
			releaseReport()
			select {
			case response := <-responses:
				if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
					t.Error("Progress response changed")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Progress did not drain")
			}
			releaseRange()
			if rangeDone != nil {
				select {
				case <-rangeDone:
				case <-time.After(3 * time.Second):
					t.Fatal("range stream did not drain")
				}
				inst.preload.mu.Lock()
				for _, device := range inst.preload.devices {
					if device.playerUA != "Current Range Player/2" {
						t.Error("old Progress fenced the current actual-stream UA")
					}
				}
				inst.preload.mu.Unlock()
			}
		})
	}
}

func TestPreloadRangeProgressKeepsConfirmedUA(t *testing.T) {
	for _, action := range []string{"serial", "stream", "authorization", "resolution", "optional-session", "optional-source", "new-ua", "new-session", "invalid-session", "new-source", "new-path", "known-new-path", "known-new-path-authorization", "known-queue-path-authorization", "authorization-denied", "resolution-denied", "stopped"} {
		t.Run(action, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
			media := items[0]["MediaSources"].([]any)[0].(map[string]any)
			source := stringField(media, "Id")
			options, _ := preloadTestOptions(t, root)
			signs := make(chan preloadTestSign, 8)
			var signatures atomic.Int32
			options.NewPreload = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return raw, func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					signatures.Add(1)
					task.Publish(func() { signs <- preloadTestSign{raw, ua} })
					return playback.PreloadResult{Outcome: playback.PreloadPublished}
				}, true
			}
			entered, released := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			t.Cleanup(release)
			var holdAuthorization, holdResolution atomic.Bool
			options.OutputRoots = func(ctx context.Context) ([]string, error) {
				if holdResolution.CompareAndSwap(true, false) {
					close(entered)
					select {
					case <-released:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return []string{root}, nil
			}
			options.MainHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "" && !strings.Contains(action, "authorization") && action != "resolution" && action != "resolution-denied" {
					close(entered)
					select {
					case <-released:
					case <-r.Context().Done():
						return
					}
				}
				w.Header().Set("Location", "https://cdn.115cdn.net/current")
				w.WriteHeader(http.StatusFound)
			})
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Users/Me" && holdAuthorization.CompareAndSwap(true, false) {
					close(entered)
					select {
					case <-released:
					case <-r.Context().Done():
						return
					}
					if action == "authorization-denied" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
				}
				switch r.URL.Path {
				case "/Users/Me":
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					_ = json.NewEncoder(w).Encode(queue)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			preloadServeInstance(t, inst, "GET", "/emby/Users/user/Items/"+current, "", false)
			preloadServeInstance(t, inst, "GET", "/emby/Shows/"+series+"/Episodes", "", false)
			stream := "/emby/Videos/" + current + "/stream?MediaSourceId=" + source + "&PlaySessionId=session"
			preloadServeInstance(t, inst, "GET", stream, "", false)
			report := map[string]any{"ItemId": current, "MediaSourceId": source, "PlaySessionId": "session", "IsPaused": false, "PositionTicks": 100000000}
			send := func(route string) {
				t.Helper()
				encoded, _ := json.Marshal(report)
				request := preloadTestHTTP(t, "http://proxy.example/emby/Sessions/Playing"+route, "POST", string(encoded), false)
				request.GetBody = nil
				response := httptest.NewRecorder()
				inst.ServeHTTP(response, request)
				if response.Code != http.StatusNoContent {
					t.Errorf("report status=%d", response.Code)
				}
			}
			send("")
			if action == "new-source" {
				media["Id"] = "new-source"
				stream = strings.ReplaceAll(stream, source, "new-source")
			} else if action == "new-path" || strings.HasPrefix(action, "known-new-path") || action == "known-queue-path-authorization" {
				media["Path"] = preloadTestFile(t, root, "changed")
				if strings.HasPrefix(action, "known-new-path") {
					preloadServeInstance(t, inst, "GET", "/emby/Users/user/Items/"+current, "", false)
				} else if action == "known-queue-path-authorization" {
					preloadServeInstance(t, inst, "GET", "/emby/Shows/"+series+"/Episodes", "", false)
				}
			} else if action == "resolution-denied" {
				media["Path"] = filepath.Join(root, "absent.strm")
			} else if action == "new-session" {
				stream = strings.ReplaceAll(stream, "PlaySessionId=session", "PlaySessionId=new-session")
			} else if action == "invalid-session" {
				stream = strings.ReplaceAll(stream, "PlaySessionId=session", "PlaySessionId=invalid/session")
			} else if action == "optional-session" {
				stream = strings.ReplaceAll(stream, "&PlaySessionId=session", "")
				delete(report, "PlaySessionId")
			} else if action == "optional-source" {
				stream = strings.ReplaceAll(stream, "MediaSourceId="+source+"&", "")
				delete(report, "MediaSourceId")
			}
			if strings.Contains(action, "authorization") {
				holdAuthorization.Store(true)
			} else if action == "resolution" || action == "resolution-denied" {
				holdResolution.Store(true)
			}
			request := preloadTestHTTP(t, "http://proxy.example"+stream, "GET", "", false)
			request.Header.Set("Range", "bytes=0-1")
			if action == "new-ua" {
				request.Header.Set("User-Agent", "Confirmed Player/2")
			}
			done := make(chan struct{})
			go func() { defer close(done); inst.ServeHTTP(httptest.NewRecorder(), request) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("Range did not enter gated phase")
			}
			if action == "known-new-path-authorization" || action == "known-queue-path-authorization" {
				inst.preload.mu.Lock()
				for _, device := range inst.preload.devices {
					if device.playerUA != "" {
						t.Error("known changed source retained old UA during authorization")
					}
				}
				inst.preload.mu.Unlock()
			}
			if action == "serial" {
				release()
				<-done
			} else if action == "stopped" {
				send("/Stopped")
			} else {
				send("/Progress")
			}
			release()
			<-done
			report["PositionTicks"] = 2060000000
			send("/Progress")
			allowed := action == "serial" || action == "stream" || action == "authorization" || action == "resolution" || strings.HasPrefix(action, "optional-")
			if allowed {
				if signed := waitPreloadSign(t, signs); signed.ua != "Confirmed Player" || !strings.Contains(signed.url, "pickcode=next") {
					t.Errorf("wrong next UA/source: %+v", signed)
				}
			} else {
				waitPreloadWorker(t, options.Preloader)
				if len(signs) != 0 || signatures.Load() != 0 {
					t.Error("unproved Range borrowed previous actual UA")
				}
				if action == "new-ua" {
					recovery := preloadTestHTTP(t, "http://proxy.example"+stream, "GET", "", false)
					recovery.Header.Set("User-Agent", "Confirmed Player/2")
					inst.ServeHTTP(httptest.NewRecorder(), recovery)
					send("")
					send("/Progress")
					if signed := waitPreloadSign(t, signs); signed.ua != "Confirmed Player/2" {
						t.Error("fresh successful stream did not recover exact new UA")
					}
				}
			}
		})
	}
}

func TestPreloadDisplayDoesNotChangeSourceIdentity(t *testing.T) {
	for _, native := range []bool{false, true} {
		payload := map[string]any{"Id": "episode", "Type": "Episode", "Name": "单集标题", "SeriesName": "测试剧名", "IndexNumber": float64(4), "ParentIndexNumber": float64(1), "MediaSources": []any{map[string]any{"Id": "source", "Path": "/fixtures/episode.strm"}}}
		if native {
			payload = map[string]any{"title": "单集标题", "series_title": "测试剧名", "season_number": float64(1), "episode_number": float64(4)}
		}
		if got := preloadDisplayFrom(payload, native).title(); got != "测试剧名 S01E04" {
			t.Errorf("native=%v title=%q", native, got)
		}
		if !native {
			before, _ := compatiblePreloadItem(payload)
			delete(payload, "Name")
			delete(payload, "SeriesName")
			after, valid := compatiblePreloadItem(payload)
			if !valid || fingerprint(before.sources) != fingerprint(after.sources) || after.display.title() != "" {
				t.Error("missing display fields changed source identity or admission")
			}
		}
	}
}

func TestPreloadMetadataFailurePreservesSourceAndBudget(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		reason string
	}{
		{errors.New("upstream must not be logged"), http.StatusServiceUnavailable, "metadata_request_failed"},
		{playback.ErrPreloadMetadataBudget, 0, "metadata_budget"},
		{playback.ErrPreloadCanceled, 0, "canceled"},
		{context.DeadlineExceeded, 0, "timeout"},
	} {
		result := preloadMetadataFailure(test.err, test.status)
		if result.Reason != test.reason || result.HTTPStatus != test.status || result.StatusSource != "飞牛" || result.Outcome != playback.PreloadSkipped {
			t.Errorf("metadata result=%+v", result)
		}
	}
}
