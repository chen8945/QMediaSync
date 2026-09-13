package emby

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/emby302/config"
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

func runSTRMRedirectTest(t *testing.T, source, ua string) (*httptest.ResponseRecorder, string) {
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
	config.C = &config.Config{Emby: &config.Emby{Host: origin.URL}}
	router := gin.New()
	router.GET("/Videos/movie/stream", Redirect2OpenlistLink)
	req := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?MediaSourceId=ms&api_key=test", nil)
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Range", "bytes=0-1023")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec, output.String()
}
