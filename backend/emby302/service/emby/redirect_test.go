package emby

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/emby302/config"
	"qmediasync/emby302/web/cache"
	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

func TestRedirect2OpenlistLinkResolverBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		ua     string
		status int
		qms    bool
	}{
		{"QMS filename route", "/115/url/video.mkv?pickcode=test", "RodelPlayer/2.2607.7.0", http.StatusFound, true},
		{"QMS legacy route", "/115/newurl?pickcode=test", "Yamby/2.1.0.8", http.StatusTemporaryRedirect, true},
		{"QMS replaces duplicate force", "/115/url/video.mkv?pickcode=test&force=0&force=2", "RodelPlayer/2.2607.7.0", http.StatusMovedPermanently, true},
		{"QMS starts query", "/115/newurl", "Yamby/2.1.0.8", http.StatusPermanentRedirect, true},
		{"QMS smartstrm filename", "/115/url/video.mkv?pickcode=test&path=smartstrm.mkv", "Yamby/2.1.0.8", http.StatusSeeOther, true},
		{"MoviePilot route", "/api/v1/plugin/P115StrmHelper/redirect_url?pickcode=test", "Yamby/2.1.0.8", http.StatusFound, false},
		{"smartstrm prefix", "/smartstrm/115/url/video.mkv?pickcode=test", "RodelPlayer/2.2607.7.0", http.StatusFound, false},
		{"query contains QMS route", "/redirect?url=/115/url/video.mkv", "Yamby/2.1.0.8", http.StatusFound, false},
		{"similar route", "/115/url-other/video.mkv?pickcode=test", "RodelPlayer/2.2607.7.0", http.StatusFound, false},
		{"legacy route suffix", "/115/newurl/other?pickcode=test", "Yamby/2.1.0.8", http.StatusFound, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cdnRequests atomic.Int32
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cdnRequests.Add(1)
				if r.UserAgent() != tt.ua || r.Header.Get("Range") != "bytes=0-1023" {
					t.Errorf("CDN headers: UA=%q Range=%q", r.UserAgent(), r.Header.Get("Range"))
				}
				w.WriteHeader(http.StatusPartialContent)
			}))
			defer cdn.Close()
			target := cdn.URL + "/%E5%BD%B1%E7%89%87%20%2B%252F.mkv?k=a%2fb%2B+z&t=2000000000&x=1&x=2"
			var resolverRequests atomic.Int32
			resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resolverRequests.Add(1)
				if r.Method != http.MethodGet || r.UserAgent() != tt.ua || r.Header.Get("Range") != "bytes=0-1023" {
					t.Errorf("resolver request: method=%q UA=%q Range=%q", r.Method, r.UserAgent(), r.Header.Get("Range"))
				}
				force := r.URL.Query()["force"]
				if tt.qms {
					if len(force) != 1 || force[0] != "1" {
						t.Errorf("force values = %v, want exactly [1]", force)
					}
					w.Header().Set("Location", target)
					w.WriteHeader(tt.status)
					return
				}
				if len(force) != 0 {
					t.Errorf("third-party resolver received force=%v", force)
				}
				if r.URL.Path != "/next" {
					w.Header().Set("Location", "/next")
				} else {
					w.Header().Set("Location", target)
				}
				w.WriteHeader(http.StatusFound)
			}))
			defer resolver.Close()

			rec, output := runSTRMRedirectTest(t, resolver.URL+tt.path, tt.ua)
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != target {
				t.Fatalf("player response: status=%d Location=%q, want 307 and unmodified %q", rec.Code, rec.Header().Get("Location"), target)
			}
			wantCDNRequests, wantResolverRequests, wantStatus := int32(1), int32(2), http.StatusPartialContent
			if tt.qms {
				wantCDNRequests, wantResolverRequests, wantStatus = 0, 1, tt.status
				if strings.Contains(output, resolver.URL+tt.path) || strings.Contains(output, target) {
					t.Fatalf("QMS outer redirect repeated long links: %s", output)
				}
			} else if !strings.Contains(output, "获取最终重定向链接成功") {
				t.Fatalf("third-party resolution log missing: %s", output)
			}
			if cdnRequests.Load() != wantCDNRequests || resolverRequests.Load() != wantResolverRequests {
				t.Fatalf("requests: CDN=%d resolver=%d, want CDN=%d resolver=%d", cdnRequests.Load(), resolverRequests.Load(), wantCDNRequests, wantResolverRequests)
			}
			for _, field := range []string{
				`文件="影片 +%2F.mkv"`, `UA="` + tt.ua + `"`, fmt.Sprintf("接口状态=%d", wantStatus), "跳转状态=307", "目标域名=127.0.0.1",
			} {
				if !strings.Contains(output, field) {
					t.Errorf("missing log field %q: %s", field, output)
				}
			}
		})
	}
}

func TestRedirect2OpenlistLinkQMS115Failures(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		location   string
		wantStatus int
	}{
		{"non-redirect success", http.StatusOK, "valid", http.StatusOK},
		{"upstream error", http.StatusForbidden, "valid", http.StatusForbidden},
		{"missing location", http.StatusFound, "", http.StatusFound},
		{"relative location", http.StatusFound, "/movie.mkv", http.StatusFound},
		{"scheme-relative location", http.StatusFound, "//cdn.invalid/movie.mkv", http.StatusFound},
		{"non-HTTP location", http.StatusFound, "ftp://cdn.invalid/movie.mkv", http.StatusFound},
		{"missing host", http.StatusFound, "https:///movie.mkv", http.StatusFound},
		{"port without host", http.StatusFound, "https://:443/movie.mkv", http.StatusFound},
		{"malformed Location rejected by HTTP client", http.StatusFound, "https://cdn.invalid/%zz", 0},
		{"malformed scheme-relative Location", http.StatusFound, "//private-user:private-pass@cdn.invalid/%zz", 0},
		{"network failure", 0, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cdnRequests atomic.Int32
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cdnRequests.Add(1)
			}))
			defer cdn.Close()
			location := tt.location
			if location == "valid" {
				location = cdn.URL + "/movie.mkv"
			}
			resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", location)
				w.WriteHeader(tt.status)
			}))
			defer resolver.Close()
			if tt.status == 0 {
				resolver.Close()
			}
			origin := resolver.URL + "/115/newurl?pickcode=test&force=0"
			rec, output := runSTRMRedirectTest(t, origin, "Yamby/2.1.0.8")
			want := resolver.URL + "/115/newurl?force=1&pickcode=test"
			if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != want {
				t.Fatalf("fallback: status=%d Location=%q, want 307 and %q", rec.Code, rec.Header().Get("Location"), want)
			}
			if cdnRequests.Load() != 0 {
				t.Fatalf("CDN requests = %d, want 0", cdnRequests.Load())
			}
			if !strings.Contains(output, "QMS 115 取链失败，回退原始链接") || !strings.Contains(output, fmt.Sprintf("接口状态=%d", tt.wantStatus)) {
				t.Fatalf("missing truthful fallback log: %s", output)
			}
			if strings.Contains(output, "获取最终重定向链接成功") || strings.Contains(output, "http://") || strings.Contains(output, "https://") {
				t.Fatalf("unexpected success or repeated long URL in failure log: %s", output)
			}
			if strings.Contains(output, "private-user") || strings.Contains(output, "private-pass") {
				t.Fatalf("invalid Location credentials escaped into logs: %s", output)
			}
		})
	}
}

func TestGetFinalRedirectLinkClosesQMSResponseBody(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			closed := make(chan struct{})
			release := make(chan struct{})
			resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "1024")
				w.Header().Set("Location", "https://cdn.invalid/movie.mkv")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, "unfinished body")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					close(closed)
				case <-release:
				}
			}))
			defer resolver.Close()
			defer close(release)
			getFinalRedirectLink(resolver.URL+"/115/newurl?pickcode=test", make(http.Header))
			select {
			case <-closed:
			case <-time.After(3 * time.Second):
				t.Fatal("resolver body was not closed")
			}
		})
	}
}

func TestRedirectCacheExpiresAt(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, tt := range []struct {
		name string
		url  string
		qms  bool
		want time.Time
	}{
		{name: "签名剩余六分钟", url: "https://cdn.invalid/file?t=1800000360", qms: true, want: now.Add(time.Minute)},
		{name: "十分钟上限", url: "https://cdn.invalid/file?t=1800003600", qms: true, want: now.Add(10 * time.Minute)},
		{name: "恰好进入安全窗口", url: "https://cdn.invalid/file?t=1800000300", qms: true},
		{name: "签名已过期", url: "https://cdn.invalid/file?t=1799999999", qms: true},
		{name: "缺少期限", url: "https://cdn.invalid/file", qms: true},
		{name: "空期限", url: "https://cdn.invalid/file?t=", qms: true},
		{name: "错误期限", url: "https://cdn.invalid/file?t=invalid", qms: true},
		{name: "负数期限", url: "https://cdn.invalid/file?t=-1", qms: true},
		{name: "期限溢出", url: "https://cdn.invalid/file?t=9223372036854775808", qms: true},
		{name: "重复期限", url: "https://cdn.invalid/file?t=1800000360&t=1800003600", qms: true},
		{name: "损坏查询参数", url: "https://cdn.invalid/file?t=1800000360&x=%zz", qms: true},
		{name: "识别第三方返回的115域名", url: "https://cdn.115cdn.net/file?t=1800000360", want: now.Add(time.Minute)},
		{name: "115根域名无期限", url: "https://115cdn.net/file"},
		{name: "其他服务保留原上限", url: "https://cdn.invalid/file?t=invalid", want: now.Add(10 * time.Minute)},
		{name: "相似域名不是115", url: "https://evil115cdn.net/file?t=1800000360", want: now.Add(10 * time.Minute)},
		{name: "非HTTP地址", url: "ftp://cdn.invalid/file?t=1800000360", qms: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := redirectCacheExpiresAt(tt.url, tt.qms, now); !got.Equal(tt.want) {
				t.Fatalf("缓存截止时间 = %v，期望 %v", got, tt.want)
			}
		})
	}
}

// 缓存联调经过实际 STRM 解析器与缓存中间件，CDN 只记录是否被意外跟随访问。
func TestRedirect2OpenlistLinkCacheSeparatesUA(t *testing.T) {
	var cdnRequests, resolverRequests atomic.Int32
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnRequests.Add(1)
	}))
	defer cdn.Close()
	expires := time.Now().Add(6 * time.Minute).Unix()
	linkForUA := func(ua string) string {
		return fmt.Sprintf("%s/movie.mkv?t=%d&f=1&c=2&ua=%s", cdn.URL, expires, url.QueryEscape(ua))
	}
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resolverRequests.Add(1)
		if r.Header.Get("Range") != "bytes=0-1023" || r.URL.Query().Get("force") != "1" {
			t.Errorf("取链请求被改写：Range=%q query=%q", r.Header.Get("Range"), r.URL.RawQuery)
		}
		w.Header().Set("Location", linkForUA(r.UserAgent()))
		w.WriteHeader(http.StatusFound)
	}))
	defer resolver.Close()
	router, _ := newSTRMRedirectTestRouter(t, resolver.URL+"/115/newurl?pickcode=test", true)
	cacheID := t.TempDir()
	for i, ua := range []string{"Player/1.2", "Player/2.1", "Player/1.2", "Player/2.1"} {
		rec := serveCachedSTRMRedirect(t, router, cacheID, ua)
		if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != linkForUA(ua) {
			t.Fatalf("UA %s 取得了错误的跳转：status=%d Location=%q", ua, rec.Code, rec.Header().Get("Location"))
		}
		if i < 2 {
			deadline, err := strconv.ParseInt(rec.Result().Header.Get(cache.HeaderKeyExpired), 10, 64)
			if err != nil || deadline != time.Unix(expires, 0).Add(-5*time.Minute).UnixMilli() {
				t.Fatalf("六分钟签名没有传递一分钟缓存截止时间：%d，错误=%v", deadline, err)
			}
		}
	}
	if resolverRequests.Load() != 2 || cdnRequests.Load() != 0 {
		t.Fatalf("缓存命中或纯跳转异常：resolver=%d CDN=%d", resolverRequests.Load(), cdnRequests.Load())
	}
}

func TestRedirect2OpenlistLinkCacheExpiresBeforeSignature(t *testing.T) {
	var resolverRequests atomic.Int32
	expires := time.Now().Add(5*time.Minute + 3*time.Second).Unix()
	resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := resolverRequests.Add(1)
		w.Header().Set("Location", fmt.Sprintf("https://cdn.invalid/movie.mkv?t=%d&attempt=%d", expires, attempt))
		w.WriteHeader(http.StatusFound)
	}))
	defer resolver.Close()
	router, _ := newSTRMRedirectTestRouter(t, resolver.URL+"/115/url/movie.mkv?pickcode=test", true)
	cacheID := t.TempDir()
	first := serveCachedSTRMRedirect(t, router, cacheID, "ExpiryPlayer")
	second := serveCachedSTRMRedirect(t, router, cacheID, "ExpiryPlayer")
	if first.Code != http.StatusTemporaryRedirect || second.Header().Get("Location") != first.Header().Get("Location") || resolverRequests.Load() != 1 {
		t.Fatal("到期前应复用同一条签名链接")
	}
	deadline := time.Unix(expires, 0).Add(-5 * time.Minute)
	// 只等当前链接进入五分钟安全窗口，不依赖十秒一次的后台清理。
	time.Sleep(time.Until(deadline.Add(time.Millisecond)))
	third := serveCachedSTRMRedirect(t, router, cacheID, "ExpiryPlayer")
	if third.Code != http.StatusTemporaryRedirect || resolverRequests.Load() != 2 || third.Header().Get("Location") == first.Header().Get("Location") {
		t.Fatal("缓存到期后必须立即重新解析")
	}
	serveCachedSTRMRedirect(t, router, cacheID, "ExpiryPlayer")
	if resolverRequests.Load() != 3 {
		t.Fatal("重新解析得到的安全窗口内链接不能再次缓存")
	}
}

func TestRedirect2OpenlistLinkDoesNotCacheUnusableResolution(t *testing.T) {
	for _, tt := range []struct {
		name   string
		query  string
		status int
	}{
		{name: "无签名期限", status: http.StatusFound},
		{name: "无效签名期限", query: "t=invalid", status: http.StatusFound},
		{name: "已进入安全窗口", query: fmt.Sprintf("t=%d", time.Now().Add(4*time.Minute).Unix()), status: http.StatusFound},
		{name: "非跳转成功回退", query: "t=2000000000", status: http.StatusOK},
		{name: "取链失败回退", query: "t=2000000000", status: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			resolver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://cdn.invalid/movie.mkv?"+tt.query)
				w.WriteHeader(tt.status)
			}))
			defer resolver.Close()
			router, _ := newSTRMRedirectTestRouter(t, resolver.URL+"/115/newurl?pickcode=test", true)
			cacheID := t.TempDir()
			for range 2 {
				rec := serveCachedSTRMRedirect(t, router, cacheID, "RetryPlayer")
				if rec.Code != http.StatusTemporaryRedirect {
					t.Fatalf("跳转状态 = %d", rec.Code)
				}
				if tt.status != http.StatusFound && rec.Header().Get("Location") != resolver.URL+"/115/newurl?force=1&pickcode=test" {
					t.Fatal("取链失败应回退同一个强制直链接口")
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("没有可信有效期或取链失败仍被缓存：解析次数=%d", calls.Load())
			}
		})
	}
}

// 每轮测试使用独立的 cacheID，同一轮的请求复用该标识以验证缓存命中。
func serveCachedSTRMRedirect(t *testing.T, router *gin.Engine, cacheID, ua string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?MediaSourceId=ms&api_key=test&case="+url.QueryEscape(cacheID), nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	cache.WaitingForHandleChan()
	return rec
}

func newSTRMRedirectTestRouter(t *testing.T, source string, cached bool) (*gin.Engine, *bytes.Buffer) {
	t.Helper()
	var output bytes.Buffer
	oldLogger, oldConfig, oldMode := helpers.AppLogger, config.C, gin.Mode()
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() {
		helpers.AppLogger, config.C = oldLogger, oldConfig
		gin.SetMode(oldMode)
	})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]string{"Id": "ms", "Path": source}}})
	}))
	t.Cleanup(origin.Close)
	cacheConfig := &config.Cache{Enable: cached, Expired: "1h"}
	if err := cacheConfig.Init(); err != nil {
		t.Fatal(err)
	}
	config.C = &config.Config{Emby: &config.Emby{Host: origin.URL}, Cache: cacheConfig}
	router := gin.New()
	if cached {
		router.Use(cache.CacheableRouteMarker(), cache.RequestCacher())
		t.Cleanup(cache.WaitingForHandleChan)
	}
	router.GET("/Videos/movie/stream", Redirect2OpenlistLink)
	return router, &output
}

func runSTRMRedirectTest(t *testing.T, source, ua string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	router, output := newSTRMRedirectTestRouter(t, source, false)
	req := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?MediaSourceId=ms&api_key=test", nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec, output.String()
}
