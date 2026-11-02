package emby

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/web/cache"
)

func TestFetchFullPlaybackInfoKeepsProcessedOriginalSources(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "no_cache", true: "cache"}[enabled], func(t *testing.T) {
			const raw = "http://strm.test/movie%2F%E7%94%B5%E5%BD%B1.mkv?name=A%26B%2BC&x=2&x=1"
			newSTRMRedirectTestRouter(t, raw, false)
			config.C.Cache.Enable = enabled
			if err := config.C.Cache.Init(); err != nil {
				t.Fatal(err)
			}
			config.C.VideoPreview = &config.VideoPreview{}
			var calls atomic.Int32
			key := t.Name() + t.TempDir()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/Items/20/PlaybackInfo" || r.URL.Query().Get("MediaSourceId") != "" || r.Header.Get("X-Emby-Token") != key {
					t.Errorf("internal request changed: path=%q headers=%v", r.URL.RequestURI(), r.Header)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"PlaySessionId": "session", "PlayAccess": "Full", "Custom": "unchanged",
					"MediaSources": []any{map[string]any{
						"Id": "ms", "ItemId": "20", "Path": raw, "Container": "strm", "IsRemote": true,
						"SupportsTranscoding": true, "TranscodingUrl": "/old/transcode",
					}},
				})
			}))
			defer origin.Close()
			config.C.Emby.Host = origin.URL
			item := ItemInfo{
				Id: "20", MsInfo: MsInfo{RawId: "selected", OriginId: "ms"},
				ApiKey: key, ApiKeyType: Header, ApiKeyName: "X-Emby-Token",
				PlaybackInfoUri: "/Items/20/PlaybackInfo?MediaSourceId=ms",
			}
			for attempt := range 2 {
				body, err := fetchFullPlaybackInfo(item)
				if err != nil {
					t.Fatal(err)
				}
				if body.Attr("PlayAccess").Val() != "Full" || body.Attr("Custom").Val() != "unchanged" || body.Attr("PlaySessionId").Val() != "session" {
					t.Fatalf("metadata changed: %s", body.String())
				}
				sources, ok := body.Attr("MediaSources").Done()
				if !ok || sources.Len() != 1 {
					t.Fatalf("sources=%s", body.String())
				}
				source, _ := sources.Idx(0).Done()
				if source.Attr("Path").Val() != raw || source.Attr("Id").Val() != "ms" || source.Attr("ItemId").Val() != "20" {
					t.Fatalf("original source changed: %s", source.String())
				}
				if direct, _ := source.Attr("DirectStreamUrl").String(); !strings.HasPrefix(direct, "/videos/20/stream?MediaSourceId=ms&X-Emby-Token=") || source.Attr("SupportsTranscoding").Val() != false {
					t.Fatalf("PlaybackInfo processing lost: %s", source.String())
				}
				if _, exists := source.Attr("TranscodingUrl").Done(); exists {
					t.Fatal("internal full PlaybackInfo bypassed normal processing")
				}
				cache.WaitingForHandleChan()
				wantCalls := int32(attempt + 1)
				if enabled {
					wantCalls = 1
				}
				if calls.Load() != wantCalls {
					t.Fatalf("full PlaybackInfo HTTP=%d want=%d", calls.Load(), wantCalls)
				}
			}
		})
	}
}

func TestMediaPathDisplayPreservesOtherMetadata(t *testing.T) {
	const raw = `{"Id":"20","PlayAccess":"None","RunTimeTicks":9007199254740993,"MediaSources":[{"Id":"ms","Path":"http://strm.test/A%252FB?name=A%26B%2BC","DirectStreamUrl":"/Videos/20/stream?x=A%26B"},{"Id":"ms%%preview","Path":"http://strm.test/A%252FB?name=A%26B%2BC","SupportsTranscoding":true,"TranscodingUrl":"/master.m3u8?sig=a%2fb+z"}]}`
	original := []byte(raw)
	body, changed := decodeMediaResponsePaths(original, true)
	if !changed {
		t.Fatal("display did not decode")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var item map[string]any
	if err := decoder.Decode(&item); err != nil {
		t.Fatal(err)
	}
	if item["PlayAccess"] != "None" || item["RunTimeTicks"] != json.Number("9007199254740993") {
		t.Fatalf("permission/numeric metadata changed: %s", body)
	}
	sources := item["MediaSources"].([]any)
	for index, source := range sources {
		if source.(map[string]any)["Path"] != "http://strm.test/A%2FB?name=A&B+C" {
			t.Fatalf("source %d decoded more than once: %s", index, body)
		}
	}
	if sources[0].(map[string]any)["Id"] != "ms" || sources[0].(map[string]any)["DirectStreamUrl"] != "/Videos/20/stream?x=A%26B" || sources[1].(map[string]any)["Id"] != "ms%%preview" || sources[1].(map[string]any)["TranscodingUrl"] != "/master.m3u8?sig=a%2fb+z" || sources[1].(map[string]any)["SupportsTranscoding"] != true {
		t.Fatalf("source IDs/URLs/transcoding changed: %s", body)
	}
	if !bytes.Equal(original, []byte(raw)) {
		t.Fatal("display changed the original source response")
	}
}

func TestMediaPathDisplayPreservesPassThroughResponses(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		encoding string
		parts    []string
		route    string
	}{
		{"streamed", 200, "", []string{`{"MediaSources":[`, `{"Path":"http://strm.test/A%26B"}]}`}, ""},
		{"error", 403, "", []string{`{"Path":"http://strm.test/A%26B"}`}, ""},
		{"compressed", 200, "gzip", []string{`{"Path":"http://strm.test/A%26B"}`}, ""},
		{"native", 200, "", []string{`{"MediaSources":[{"Path":"/media/A%252FB"}]}`}, ""},
		{"live", 200, "", []string{`{"MediaSources":[{"Path":"http://strm.test/A%26B","IsInfiniteStream":true}]}`}, ""},
		{"live_detail", 200, "", []string{`{"Type":"TvChannel","Path":"http://strm.test/A%26B","MediaSources":[{"Path":"http://strm.test/A%26B"}]}`}, "/Users/10/Items/20"},
		{"live_items", 200, "", []string{`{"Items":[{"IsLive":true,"Path":"http://strm.test/A%26B","MediaSources":[{"Path":"http://strm.test/A%26B"}]}]}`}, "/Users/10/Items"},
		{"live_latest", 200, "", []string{`[{"IsInfiniteStream":true,"Path":"http://strm.test/A%26B","MediaSources":[{"Path":"http://strm.test/A%26B"}]}]`}, "/Users/10/Items/Latest"},
	} {
		t.Run(test.name, func(t *testing.T) {
			method, route := http.MethodPost, test.route
			if route == "" {
				route = "/Items/20/PlaybackInfo"
			} else {
				method = http.MethodGet
			}
			router := gin.New()
			router.Use(MediaPathDisplay())
			router.Handle(method, route, func(c *gin.Context) {
				c.Status(test.status)
				c.Header("Content-Encoding", test.encoding)
				c.Header("Content-Length", strconv.Itoa(len(strings.Join(test.parts, ""))))
				for _, part := range test.parts {
					_, _ = c.Writer.WriteString(part)
				}
			})
			response := httptest.NewRecorder()
			requestPath := route
			if route == "/Users/10/Items" {
				requestPath += "?IncludeItemTypes=Movie"
			}
			router.ServeHTTP(response, httptest.NewRequest(method, requestPath, nil))
			if response.Code != test.status || response.Body.String() != strings.Join(test.parts, "") {
				t.Fatalf("pass-through changed: status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMediaPathDisplayPreservesSplitJSONWrites(t *testing.T) {
	const prefix = `{"Path":"http://strm.test/A%26B","Padding":"`
	const suffix = `"}`
	raw := prefix + strings.Repeat("x", 32*1024-len(prefix)-len(suffix)) + suffix
	for _, knownLength := range []bool{true, false} {
		t.Run(map[bool]string{true: "known_length", false: "unknown_length"}[knownLength], func(t *testing.T) {
			writeErrors := make(chan error, 2)
			router := gin.New()
			router.Use(MediaPathDisplay())
			router.POST("/Items/20/PlaybackInfo", func(c *gin.Context) {
				if knownLength {
					c.Header("Content-Length", strconv.Itoa(len(raw)+1))
				}
				_, err := c.Writer.WriteString(raw)
				writeErrors <- err
				_, err = c.Writer.WriteString("\n")
				writeErrors <- err
			})
			server := httptest.NewServer(router)
			defer server.Close()
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/Items/20/PlaybackInfo", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := <-writeErrors; err != nil {
					t.Errorf("split response write: %v", err)
				}
			}
			if response.StatusCode != http.StatusOK || string(body) != raw+"\n" {
				t.Fatalf("split response changed: status=%d bytes=%d want=%d", response.StatusCode, len(body), len(raw)+1)
			}
		})
	}
}

func TestMediaPathDisplayPreservesLiveSources(t *testing.T) {
	const raw = `{"Type":"Movie","Path":"http://strm.test/A%26B","MediaSources":[{"Path":"http://strm.test/A%26B"},{"Path":"http://strm.test/live%2Fstream","IsInfiniteStream":true}]}`
	body, changed := decodeMediaResponsePaths([]byte(raw), false)
	var item struct {
		Path         string
		MediaSources []struct{ Path string }
	}
	if err := json.Unmarshal(body, &item); err != nil {
		t.Fatal(err)
	}
	if !changed || item.Path != "http://strm.test/A&B" || len(item.MediaSources) != 2 || item.MediaSources[0].Path != "http://strm.test/A&B" || item.MediaSources[1].Path != "http://strm.test/live%2Fstream" {
		t.Fatalf("normal and live sources changed: %s", body)
	}
}

func TestSelectedPlaybackInfoCacheMissReturnsOriginalSource(t *testing.T) {
	const raw = "http://strm.test/A%26B.mkv?x=2&x=1"
	router, _ := newSTRMRedirectTestRouter(t, raw, false)
	config.C.VideoPreview = &config.VideoPreview{}
	config.C.Cache.Enable = true
	if err := config.C.Cache.Init(); err != nil {
		t.Fatal(err)
	}
	router.Use(MediaPathDisplay())
	router.POST("/Items/movie/PlaybackInfo", TransferPlaybackInfo)
	path := "/Items/movie/PlaybackInfo?MediaSourceId=ms&api_key=" + url.QueryEscape(t.Name()+t.TempDir())
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, path, nil))
	var body struct{ MediaSources []struct{ Id, Path string } }
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK || len(body.MediaSources) != 1 || body.MediaSources[0].Id != "ms" || body.MediaSources[0].Path != "http://strm.test/A&B.mkv?x=2&x=1" {
		t.Fatalf("selected PlaybackInfo: status=%d body=%s error=%v", response.Code, response.Body.String(), err)
	}
	cache.WaitingForHandleChan()
}

func TestMediaPathDisplayWriteFailureDoesNotCache(t *testing.T) {
	router, _ := newSTRMRedirectTestRouter(t, "http://strm.test/A%26B.mkv", false)
	config.C.VideoPreview = &config.VideoPreview{}
	config.C.Cache.Enable = true
	if err := config.C.Cache.Init(); err != nil {
		t.Fatal(err)
	}
	router.Use(MediaPathDisplay(), cache.CacheableRouteMarker(), cache.RequestCacher())
	router.POST("/Items/movie/PlaybackInfo", TransferPlaybackInfo)
	path := "/Items/movie/PlaybackInfo?api_key=" + url.QueryEscape(t.Name()+t.TempDir())
	response := httptest.NewRecorder()
	router.ServeHTTP(partialFailingResponseWriter{response}, httptest.NewRequest(http.MethodPost, path, nil))
	if response.Body.Len() != 2 {
		t.Fatalf("failed write body=%s", response.Body.String())
	}
	cache.WaitingForHandleChan()
	if _, ok := cache.GetRequestCache(httptest.NewRequest(http.MethodPost, path, nil)); ok {
		t.Fatal("failed display write published a response cache")
	}
}
