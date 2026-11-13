package emby

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/helpers"
)

func setupDownloadTest(t *testing.T, origin string) *Session {
	t.Helper()
	oldConfig, oldLogger := config.C, helpers.AppLogger
	config.C = &config.Config{Emby: &config.Emby{Host: origin}}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { config.C, helpers.AppLogger = oldConfig, oldLogger })
	if err := config.C.Emby.Init(); err != nil {
		t.Fatal(err)
	}
	return newTestSession(t)
}

func TestDownloadPermissionFailsClosed(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{"allowed", 200, `{"Items":[{"Id":"123","CanDownload":true}]}`, 307},
		{"denied", 200, `{"Items":[{"Id":"123","CanDownload":false}]}`, 403},
		{"invalid token with valid JSON", 401, `{"Items":[{"Id":"123","CanDownload":true}]}`, 401},
		{"upstream forbidden", 403, `{"Items":[]}`, 403},
		{"upstream failure with valid JSON", 500, `{"Items":[{"Id":"123","CanDownload":true}]}`, 502},
		{"missing item", 200, `{"Items":[]}`, 404},
		{"wrong item", 200, `{"Items":[{"Id":"999","CanDownload":true}]}`, 502},
		{"missing permission", 200, `{"Items":[{"Id":"123"}]}`, 502},
		{"string permission", 200, `{"Items":[{"Id":"123","CanDownload":"true"}]}`, 502},
		{"null permission", 200, `{"Items":[{"Id":"123","CanDownload":null}]}`, 502},
		{"invalid JSON", 200, `{"Items":`, 502},
		{"unexpected shape", 200, `{"Items":{}}`, 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var mediaCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.URL.Path != "/Items" || r.Method != http.MethodGet || q.Get("Ids") != "123" || q.Get("Fields") != "CanDownload" || q.Get("Limit") != "1" || q.Has("UserId") || r.Header.Get("X-Emby-Token") != "reader-token" {
					t.Errorf("unexpected permission request: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer origin.Close()
			// 配置留空经 Init 得到 direct，也必须检查权限。
			session := setupDownloadTest(t, origin.URL)
			router := gin.New()
			router.Use(session.DownloadStrategyChecker())
			router.Any("/Items/123/Download", func(c *gin.Context) {
				mediaCalls.Add(1)
				c.Redirect(http.StatusTemporaryRedirect, "https://cdn.invalid/file.mkv")
			})
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				req := httptest.NewRequest(method, "/Items/123/Download?UserId=spoofed-admin", nil)
				req.Header.Set("X-Emby-Token", "reader-token")
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != tt.want || tt.want != 307 && rec.Header().Get("Location") != "" || method == http.MethodHead && rec.Body.Len() != 0 {
					t.Fatalf("%s status=%d Location=%q body=%s, want %d", method, rec.Code, rec.Header().Get("Location"), rec.Body.String(), tt.want)
				}
			}
			wantCalls := int32(0)
			if tt.want == 307 {
				wantCalls = 2
			}
			if mediaCalls.Load() != wantCalls {
				t.Fatalf("media calls=%d, want %d", mediaCalls.Load(), wantCalls)
			}
		})
	}
}

func TestDownloadPermissionRecheckedBeforeCachedRedirect(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	var permissionCalls, handlerCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		permissionCalls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "123", "CanDownload": allowed.Load()}}})
	}))
	defer origin.Close()
	session := setupDownloadTest(t, origin.URL)
	router := gin.New()
	router.Use(session.DownloadStrategyChecker(), cache.CacheableRouteMarker(), session.Cache.RequestCacher())
	router.GET("/Items/123/Download", func(c *gin.Context) {
		handlerCalls.Add(1)
		c.Header(cache.HeaderKeyExpired, cache.Duration(time.Minute))
		c.Redirect(http.StatusTemporaryRedirect, "https://cdn.invalid/file.mkv")
	})
	request := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "/Items/123/Download?api_key=reader-token", nil)
	}
	perform := func(want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, request())
		if rec.Code != want || want != 307 && rec.Header().Get("Location") != "" {
			t.Fatalf("status=%d Location=%q, want %d", rec.Code, rec.Header().Get("Location"), want)
		}
	}
	perform(307)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := session.Cache.GetRequestCache(request()); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("authorized redirect did not enter cache")
		}
		time.Sleep(time.Millisecond)
	}
	perform(307)
	allowed.Store(false)
	perform(403)
	if permissionCalls.Load() != 3 || handlerCalls.Load() != 1 {
		t.Fatalf("permission calls=%d handler calls=%d; cached redirects must recheck permissions", permissionCalls.Load(), handlerCalls.Load())
	}
}

func TestSyncDownloadPreservesCredentialsThroughOrdinaryDownload(t *testing.T) {
	const token = "reader+token&value"
	const source = "source+with &/?"
	for _, mode := range []string{"api_key", "query_token", "header_token", "quoted_authorization", "unquoted_authorization"} {
		t.Run(mode, func(t *testing.T) {
			var permissionCalls, targetsCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				key := q.Get("api_key")
				if key == "" {
					key = q.Get("X-Emby-Token")
				}
				if key == "" {
					key = r.Header.Get("X-Emby-Token")
				}
				if mode == "unquoted_authorization" {
					if r.Header.Get("Authorization") != "Emby Token="+token+", UserId=spoofed-admin" {
						t.Errorf("full header lost: %q", r.Header.Get("Authorization"))
					}
				} else if key != token {
					t.Errorf("token=%q, want %q", key, token)
				}
				if q.Has("UserId") {
					t.Error("client UserId was forwarded as permission identity")
				}
				switch r.URL.Path {
				case "/Sync/Targets":
					targetsCalls.Add(1)
					_, _ = io.WriteString(w, `[{"Id":"device&1"}]`)
				case "/Sync/Items/Ready":
					if q.Get("TargetId") != "device&1" {
						t.Errorf("TargetId=%q", q.Get("TargetId"))
					}
					_ = json.NewEncoder(w).Encode([]any{map[string]any{"SyncJobItemId": 9, "Item": map[string]any{"Id": "123", "MediaSources": []any{map[string]any{"Id": source}}}}})
				case "/Items":
					permissionCalls.Add(1)
					if q.Get("Ids") != "123" {
						t.Errorf("permission requested sync ID instead of media ID: %s", r.URL)
					}
					_, _ = io.WriteString(w, `{"Items":[{"Id":"123","CanDownload":true}]}`)
				default:
					t.Errorf("unexpected route %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer origin.Close()
			session := setupDownloadTest(t, origin.URL)
			router := gin.New()
			router.Use(session.DownloadStrategyChecker(), cache.CacheableRouteMarker(), session.Cache.RequestCacher())
			router.GET("/Sync/JobItems/9/File", session.HandleSyncDownload)
			router.GET("/Items/123/Download", func(c *gin.Context) {
				if !c.GetBool(downloadRequestKey) || c.Query("MediaSourceId") != source {
					t.Errorf("ordinary download lost marker/source: %s", c.Request.URL)
				}
				c.String(http.StatusOK, "download authorized")
			})
			proxy := httptest.NewServer(router)
			defer proxy.Close()
			query := url.Values{"UserId": {"spoofed-admin"}}
			headers := make(http.Header)
			switch mode {
			case "api_key":
				query.Set("api_key", token)
			case "query_token":
				query.Set("X-Emby-Token", token)
			case "header_token":
				headers.Set("X-Emby-Token", token)
			case "quoted_authorization":
				headers.Set("Authorization", `Emby Token="`+token+`", UserId="spoofed-admin"`)
			case "unquoted_authorization":
				headers.Set("Authorization", "Emby Token="+token+", UserId=spoofed-admin")
			}
			for range 2 {
				req, err := http.NewRequest(http.MethodGet, proxy.URL+"/Sync/JobItems/9/File?"+query.Encode(), nil)
				if err != nil {
					t.Fatal(err)
				}
				req.Header = headers.Clone()
				response, err := proxy.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if response.StatusCode != 200 || err != nil || string(body) != "download authorized" || response.Request.URL.Path != "/Items/123/Download" {
					t.Fatalf("sync status=%d body=%s target=%s error=%v", response.StatusCode, body, response.Request.URL, err)
				}
			}
			if targetsCalls.Load() != 2 || permissionCalls.Load() != 4 {
				t.Fatalf("mapping calls=%d permission calls=%d; sync cache bypassed checks", targetsCalls.Load(), permissionCalls.Load())
			}
		})
	}
}

func TestSyncDownloadFailuresDoNotFallBack(t *testing.T) {
	const validReady = `[{"SyncJobItemId":9,"Item":{"Id":"123","MediaSources":[{"Id":"source"}]}}]`
	for _, tt := range []struct {
		name, ready, permission, failPath string
		failStatus, want                  int
	}{
		{"denied", validReady, `{"Items":[{"Id":"123","CanDownload":false}]}`, "", 0, 403},
		{"missing permission", validReady, `{"Items":[{"Id":"123"}]}`, "", 0, 502},
		{"missing job", `[{"SyncJobItemId":8}]`, "", "", 0, 404},
		{"fractional job ID", `[{"SyncJobItemId":9.8}]`, "", "", 0, 502},
		{"missing source", `[{"SyncJobItemId":9,"Item":{"Id":"123"}}]`, "", "", 0, 502},
		{"targets forbidden", "", "", "/Sync/Targets", 403, 403},
		{"ready failure with valid JSON", `[]`, "", "/Sync/Items/Ready", 500, 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var fallbackCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == tt.failPath {
					w.WriteHeader(tt.failStatus)
				}
				switch r.URL.Path {
				case "/Sync/Targets":
					_, _ = io.WriteString(w, `[{"Id":"device"}]`)
				case "/Sync/Items/Ready":
					_, _ = io.WriteString(w, tt.ready)
				case "/Items":
					_, _ = io.WriteString(w, tt.permission)
				default:
					fallbackCalls.Add(1)
					_, _ = io.WriteString(w, "native sync file without a download permission check")
				}
			}))
			defer origin.Close()
			session := setupDownloadTest(t, origin.URL)
			router := gin.New()
			router.Use(session.DownloadStrategyChecker())
			router.Any("/Sync/JobItems/9/File", session.HandleSyncDownload)
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(method, "/Sync/JobItems/9/File?api_key=reader-token", nil))
				if rec.Code != tt.want || rec.Header().Get("Location") != "" || method == http.MethodHead && rec.Body.Len() != 0 {
					t.Fatalf("sync status=%d Location=%q body=%s, want %d", rec.Code, rec.Header().Get("Location"), rec.Body.String(), tt.want)
				}
			}
			if fallbackCalls.Load() != 0 {
				t.Fatalf("failed mapping/permission triggered %d native sync downloads", fallbackCalls.Load())
			}
		})
	}
}

func TestDownloadPermissionCancellationStopsProcessing(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer origin.Close()
	session := setupDownloadTest(t, origin.URL)
	router := gin.New()
	router.Use(session.DownloadStrategyChecker())
	router.GET("/Items/123/Download", func(c *gin.Context) { t.Error("canceled check reached download") })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	completed := make(chan struct{})
	go func() {
		router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/Items/123/Download?api_key=reader-token", nil))
		close(completed)
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("permission request did not start")
	}
	for _, done := range []<-chan struct{}{finished, completed} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("permission request did not honor cancellation")
		}
	}
}

func TestDownloadWithoutCredentialsStopsBeforeEmby(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer origin.Close()
	session := setupDownloadTest(t, origin.URL)
	router := gin.New()
	router.Use(session.DownloadStrategyChecker())
	router.GET("/Items/123/Download", func(c *gin.Context) { t.Error("anonymous request reached download") })
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/Items/123/Download", nil))
	if rec.Code != http.StatusUnauthorized || calls.Load() != 0 {
		t.Fatalf("anonymous status=%d upstream calls=%d", rec.Code, calls.Load())
	}
}

func TestDownloadAliasesCannotFallThrough(t *testing.T) {
	const ready = `[{"SyncJobItemId":9,"Item":{"Id":"123","MediaSources":[{"Id":"source"}]}}]`
	for _, path := range []string{
		"/Items/%31%32%33/Download?api_key=reader-token",
		"/Items/123/Download/?api_key=reader-token",
		"/Items//123/Download/?api_key=reader-token",
		"/Sync/JobItems/%39/File?api_key=reader-token",
		"/Sync/JobItems/9/File/?api_key=reader-token",
		"/Sync//JobItems/9/File/?api_key=reader-token",
		"/Sync/JobItems/8/../9/File?api_key=reader-token",
		"/Sync/JobItems/9/File?api_key=reader-token&foo=/Videos/999/stream",
		"/%53ync/JobItems/9/File?api_key=reader-token",
	} {
		t.Run(path, func(t *testing.T) {
			var permissionCalls, fallbackCalls atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Sync/Targets":
					_, _ = io.WriteString(w, `[{"Id":"device"}]`)
				case "/Sync/Items/Ready":
					_, _ = io.WriteString(w, ready)
				case "/Items":
					permissionCalls.Add(1)
					if r.URL.Query().Get("Ids") != "123" {
						t.Errorf("wrong authorized item: %s", r.URL)
					}
					_, _ = io.WriteString(w, `{"Items":[{"Id":"123","CanDownload":false}]}`)
				default:
					fallbackCalls.Add(1)
				}
			}))
			defer origin.Close()
			session := setupDownloadTest(t, origin.URL)
			router := gin.New()
			router.Use(session.DownloadStrategyChecker())
			router.Any("/*path", func(c *gin.Context) {
				fallbackCalls.Add(1)
				session.ProxyOrigin(c)
			})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != 403 || rec.Header().Get("Location") != "" || permissionCalls.Load() != 1 || fallbackCalls.Load() != 0 {
				t.Fatalf("alias status=%d permission calls=%d fallback calls=%d", rec.Code, permissionCalls.Load(), fallbackCalls.Load())
			}
		})
	}
}
