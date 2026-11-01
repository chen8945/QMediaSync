package cache

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

func TestRequestCacherSkipsCanceledRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/"+t.Name(), nil)
	key := keyForRequest(t, req)
	t.Cleanup(func() {
		cacheMap.Delete(key)
		spaceMap.Delete(t.Name())
	})
	router := gin.New()
	router.Use(RequestCacher())
	router.GET("/"+t.Name(), func(c *gin.Context) {
		c.Header(HeaderKeyExpired, Duration(time.Minute))
		c.Header(HeaderKeySpace, t.Name())
		c.Header(HeaderKeySpaceKey, key)
		_, _ = c.Writer.Write([]byte("canceled response"))
		cancel()
	})
	router.ServeHTTP(httptest.NewRecorder(), req)
	WaitingForHandleChan()
	if _, ok := getCache(key); ok {
		t.Fatal("canceled response was cached")
	}
	if _, ok := GetSpaceCache(t.Name(), key); ok {
		t.Fatal("canceled response was published to a cache space")
	}
}

func TestCalcCacheKeyPreservesRequestValues(t *testing.T) {
	tests := []struct {
		name   string
		first  *http.Request
		second *http.Request
	}{
		{
			name:   "user agent character order",
			first:  cacheKeyRequest("GET", "/stream", "", http.Header{"User-Agent": {"Player/1.2"}}),
			second: cacheKeyRequest("GET", "/stream", "", http.Header{"User-Agent": {"Player/2.1"}}),
		},
		{
			name:   "query value character order",
			first:  cacheKeyRequest("GET", "/stream?value=12", "", nil),
			second: cacheKeyRequest("GET", "/stream?value=21", "", nil),
		},
		{
			name:   "body character order",
			first:  cacheKeyRequest("POST", "/stream", "12", nil),
			second: cacheKeyRequest("POST", "/stream", "21", nil),
		},
		{
			name:   "header value boundaries",
			first:  cacheKeyRequest("GET", "/stream", "", http.Header{"X-Value": {"a|b"}}),
			second: cacheKeyRequest("GET", "/stream", "", http.Header{"X-Value": {"a", "b"}}),
		},
		{
			name:   "header value order",
			first:  cacheKeyRequest("GET", "/stream", "", http.Header{"X-Value": {"a", "b"}}),
			second: cacheKeyRequest("GET", "/stream", "", http.Header{"X-Value": {"b", "a"}}),
		},
		{
			name:   "header name boundaries",
			first:  cacheKeyRequest("GET", "/stream", "", http.Header{"X-A": {"a;X-B=b"}}),
			second: cacheKeyRequest("GET", "/stream", "", http.Header{"X-A": {"a"}, "X-B": {"b"}}),
		},
		{
			name:   "body and header boundaries",
			first:  cacheKeyRequest("POST", "/stream", "X-A=a;", nil),
			second: cacheKeyRequest("POST", "/stream", "", http.Header{"X-A": {"a"}}),
		},
		{
			name:   "raw header bytes",
			first:  cacheKeyRequest("GET", "/stream", "", http.Header{"X-Value": {"\xff"}}),
			second: cacheKeyRequest("GET", "/stream", "", http.Header{"X-Value": {"\xfe"}}),
		},
		{
			name:   "method",
			first:  cacheKeyRequest("GET", "/stream", "", nil),
			second: cacheKeyRequest("HEAD", "/stream", "", nil),
		},
		{
			name:   "URL path",
			first:  cacheKeyRequest("GET", "/stream/12", "", nil),
			second: cacheKeyRequest("GET", "/stream/21", "", nil),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if keyForRequest(t, tt.first) == keyForRequest(t, tt.second) {
				t.Fatal("different request values shared a cache key")
			}
		})
	}
}

func TestCalcCacheKeyStableWithoutMutatingRequest(t *testing.T) {
	firstHeader := make(http.Header)
	firstHeader.Set("User-Agent", "Player/1.2")
	firstHeader["X-Value"] = []string{"a", "b"}
	firstHeader.Set("Range", "bytes=0-1")
	secondHeader := make(http.Header)
	secondHeader.Set("Range", "bytes=2-3")
	secondHeader["X-Value"] = []string{"a", "b"}
	secondHeader.Set("User-Agent", "Player/1.2")
	const body = "{\"value\": \"12\"}\x00\xff"
	first := cacheKeyRequest("POST", "/stream?z=12&StartTimeTicks=1&a=x%20y", body, firstHeader)
	second := cacheKeyRequest("POST", "/stream?a=x+y&z=12&StartTimeTicks=2", body, secondHeader)
	wantURL := *first.URL
	wantURI := first.RequestURI
	wantHeader := first.Header.Clone()
	key := keyForRequest(t, first)
	if key != keyForRequest(t, second) || key != keyForRequest(t, first) {
		t.Fatal("equivalent requests did not reuse the same cache key")
	}
	if *first.URL != wantURL || first.RequestURI != wantURI || !reflect.DeepEqual(first.Header, wantHeader) {
		t.Fatal("cache key calculation changed the forwarded request")
	}
	gotBody, err := io.ReadAll(first.Body)
	if err != nil || !bytes.Equal(gotBody, []byte(body)) {
		t.Fatalf("request body changed: body=%q, err=%v", gotBody, err)
	}
}

func TestCalcCacheKeyRejectsMalformedQueryWithoutChangingRequest(t *testing.T) {
	req := cacheKeyRequest("POST", "/stream?value=%zz", "body", nil)
	wantURL := *req.URL
	if _, err := calcCacheKey(&gin.Context{Request: req}); err == nil {
		t.Fatal("malformed query produced a cache key")
	}
	body, err := io.ReadAll(req.Body)
	if err != nil || string(body) != "body" || *req.URL != wantURL {
		t.Fatal("failed cache key calculation changed the request")
	}
}

func cacheKeyRequest(method, target, body string, header http.Header) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	req.Header = header
	return req
}

func keyForRequest(t *testing.T, req *http.Request) string {
	t.Helper()
	c := &gin.Context{Request: req}
	key, err := calcCacheKey(c)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestRequestCacherLogsOnlyPlaybackRedirectHits(t *testing.T) {
	var output bytes.Buffer
	oldLogger, oldLevel := helpers.AppLogger, helpers.ConfiguredLogLevel()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
	t.Cleanup(func() {
		helpers.AppLogger = oldLogger
		helpers.SetGlobalLogLevel(oldLevel)
	})
	for _, test := range []struct {
		name, path, location string
		status               int
		canceled, want       bool
	}{
		{"video", "/Videos/movie/stream", "https://cdn.example/movie.mkv?k=signature", 307, false, true},
		{"audio", "/emby/Audio/music/universal.mp3", "https://cdn.example/music.mp3?k=signature", 307, false, true},
		{"metadata", "/Items/movie/PlaybackInfo", "https://cdn.example/movie.mkv", 307, false, false},
		{"subtitle", "/Videos/movie/Subtitles/0/0/Stream.srt", "https://cdn.example/movie.srt", 307, false, false},
		{"list", "/Users/user/Items/with_limit?SortBy=Random", "https://cdn.example/list", 307, false, false},
		{"download", "/Items/123/Download", "https://cdn.example/movie.mkv", 307, false, false},
		{"nonredirect", "/Videos/movie/stream", "", 200, false, false},
		{"missing location", "/Videos/movie/stream", "", 307, false, false},
		{"canceled", "/Videos/movie/stream", "https://cdn.example/movie.mkv", 307, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := httptest.NewRequestWithContext(ctx, http.MethodGet, test.path, nil)
			request.Header.Set("User-Agent", "Cache Player")
			key := keyForRequest(t, request)
			putCache(key, test.status, []byte("cached body"), respHeader{
				expired: Duration(time.Minute), header: http.Header{"Location": {test.location}},
			})
			WaitingForHandleChan()
			t.Cleanup(func() { cacheMap.Delete(key) })
			if _, ok := getCache(key); !ok {
				t.Fatal("test response was not cached")
			}
			if test.canceled {
				cancel()
			}
			router := gin.New()
			router.Use(CacheableRouteMarker(), RequestCacher())
			router.NoRoute(func(*gin.Context) { t.Error("cached response reached handler") })
			output.Reset()
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			got := output.String()
			wantCount := 0
			if test.want {
				wantCount = 1
			}
			if strings.Count(got, "播放请求命中缓存") != wantCount || response.Code != test.status {
				t.Fatalf("unexpected cached response/log: code=%d log=%s", response.Code, got)
			}
			if test.want && (!strings.Contains(got, "入口=Emby，缓存=播放跳转") || !strings.Contains(got, `UA="Cache Player"`)) {
				t.Fatalf("missing cache context: %s", got)
			}
			if strings.Contains(got, "signature") || strings.Contains(got, "https://") {
				t.Fatal("cached redirect log exposed signed URL")
			}
		})
	}
}
