package emby

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/emby302/config"
	"qmediasync/emby302/web/cache"
	"qmediasync/internal/helpers"
)

func TestRandomItemsWithLimitCachesOnlyCompleteResponses(t *testing.T) {
	for _, tt := range []struct {
		name    string
		enabled bool
	}{
		{name: "cache_enabled", enabled: true},
		{name: "cache_disabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, mode := range []string{"upstream_read_error", "downstream_write_error", "complete"} {
				t.Run(mode, func(t *testing.T) {
					// 首块越过 HTTP 缓冲，完整响应等待客户端收到首字节后才发送尾块。
					partial := "{\n" + strings.Repeat(" ", 8192) + `"Items":[`
					complete := partial + `{"Id":"one"},{"Id":"two"}],"TotalRecordCount":2}`
					release := make(chan struct{}, 1)
					var calls atomic.Int32
					origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						call := calls.Add(1)
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Content-Length", strconv.Itoa(len(complete)))
						if call == 1 && mode != "downstream_write_error" {
							_, _ = io.WriteString(w, partial)
							w.(http.Flusher).Flush()
							if mode == "upstream_read_error" {
								return
							}
							<-release
							_, _ = io.WriteString(w, complete[len(partial):])
							return
						}
						_, _ = io.WriteString(w, complete)
					}))
					defer origin.Close()

					cacheConfig := &config.Cache{Enable: tt.enabled}
					if err := cacheConfig.Init(); err != nil {
						t.Fatal(err)
					}
					oldConfig, oldLogger := config.C, helpers.AppLogger
					config.C = &config.Config{
						Emby: &config.Emby{
							Host: origin.URL, ResortRandomItems: true, ProxyErrorStrategy: config.PeStrategyOrigin,
						},
						Cache: cacheConfig,
					}
					helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
					t.Cleanup(func() { config.C, helpers.AppLogger = oldConfig, oldLogger })
					t.Cleanup(cache.WaitingForHandleChan)

					router := gin.New()
					if config.C.Cache.Enable {
						router.Use(cache.CacheableRouteMarker(), cache.RequestCacher())
					}
					router.GET("/Users/1/Items/with_limit", RandomItemsWithLimit)
					router.GET("/Users/1/Items", ResortRandomItems)
					finished := make(chan error, 1)
					proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if mode == "downstream_write_error" && calls.Load() == 0 {
							w = partialFailingResponseWriter{ResponseWriter: w}
						}
						router.ServeHTTP(w, r)
						finished <- r.Context().Err()
					}))
					defer proxy.Close()
					defer close(release)
					client := proxy.Client()
					client.Timeout = 5 * time.Second
					client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

					// ParentId 参与两种缓存的标识，隔离每轮测试和重复运行。
					path := "/Users/1/Items/with_limit?SortBy=Random&Limit=500&api_key=test&ParentId=" + url.QueryEscape(t.TempDir())
					spaceKey := calcRandomItemsCacheKey(newItemsTestContext(t, path))
					fetch := func(path string, checkStreaming bool) (*http.Response, []byte, error) {
						t.Helper()
						resp, err := client.Get(proxy.URL + path)
						if err != nil {
							t.Fatal(err)
						}
						defer resp.Body.Close()
						var first [1]byte
						if checkStreaming {
							if _, err := io.ReadFull(resp.Body, first[:]); err != nil {
								t.Fatalf("first streamed byte: %v", err)
							}
							release <- struct{}{}
						}
						body, readErr := io.ReadAll(resp.Body)
						if checkStreaming {
							body = append(first[:], body...)
						}
						select {
						case err := <-finished:
							if err != nil {
								t.Fatalf("inbound request unexpectedly canceled: %v", err)
							}
						case <-time.After(5 * time.Second):
							t.Fatal("proxy handler did not finish")
						}
						cache.WaitingForHandleChan()
						return resp, body, readErr
					}

					started := time.Now()
					resp, body, readErr := fetch(path, mode == "complete")
					wantBody, wantReadErr := partial, io.ErrUnexpectedEOF
					if mode == "downstream_write_error" {
						wantBody = complete[:2]
					} else if mode == "complete" {
						wantBody, wantReadErr = complete, nil
					}
					if resp.StatusCode != http.StatusOK || string(body) != wantBody || !errors.Is(readErr, wantReadErr) || calls.Load() != 1 {
						t.Fatalf("first response: status=%d, bytes=%d, error=%v, origin calls=%d; want %d bytes without retry or appended content",
							resp.StatusCode, len(body), readErr, calls.Load(), len(wantBody))
					}
					if _, ok := cache.GetSpaceCache(ItemsCacheSpace, spaceKey); ok != (tt.enabled && mode == "complete") {
						t.Errorf("space cache exists = %t after %s", ok, mode)
					}

					resp, body, readErr = fetch(path, false)
					wantCalls := int32(2)
					if tt.enabled && mode == "complete" {
						wantCalls = 1
					}
					if resp.StatusCode != http.StatusOK || readErr != nil || string(body) != complete || calls.Load() != wantCalls {
						t.Fatalf("following response: status=%d, bytes=%d, error=%v, origin calls=%d; want %d complete bytes and %d origin calls",
							resp.StatusCode, len(body), readErr, calls.Load(), len(complete), wantCalls)
					}
					if rc, ok := cache.GetSpaceCache(ItemsCacheSpace, spaceKey); tt.enabled {
						if !ok || string(rc.BodyBytes()) != complete {
							t.Fatal("complete response must populate the random Items cache space")
						}
						deadline, err := strconv.ParseInt(rc.Header(cache.HeaderKeyExpired), 10, 64)
						if err != nil || deadline < started.Add(3*time.Hour).UnixMilli() || deadline > time.Now().Add(3*time.Hour).UnixMilli() {
							t.Fatalf("cache deadline = %d, error = %v; want three hours from the complete response", deadline, err)
						}
					} else if ok {
						t.Fatal("disabled cache must not populate the random Items cache space")
					}

					resp, body, readErr = fetch(strings.Replace(path, "/Items/with_limit?", "/Items?", 1), false)
					if readErr != nil || calls.Load() != wantCalls {
						t.Fatalf("random route: error=%v, origin calls=%d, want %d", readErr, calls.Load(), wantCalls)
					}
					if tt.enabled {
						var items ItemsHolder
						if err := json.Unmarshal(body, &items); err != nil || resp.StatusCode != http.StatusOK || len(items.Items) != 2 || items.TotalRecordCount != 2 {
							t.Fatalf("random route must return the complete cached list: status=%d, error=%v", resp.StatusCode, err)
						}
						gotItems := []string{string(items.Items[0]), string(items.Items[1])}
						slices.Sort(gotItems)
						if !slices.Equal(gotItems, []string{`{"Id":"one"}`, `{"Id":"two"}`}) {
							t.Fatalf("reordered Items = %v", gotItems)
						}
					} else if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != path {
						t.Fatal("uncached random route must redirect to the original with_limit request")
					}

					resp, body, readErr = fetch(path, false)
					if !tt.enabled {
						wantCalls++
					}
					if resp.StatusCode != http.StatusOK || readErr != nil || string(body) != complete || calls.Load() != wantCalls {
						t.Fatalf("repeated with_limit response: status=%d, bytes=%d, error=%v, origin calls=%d; want the original complete response and %d origin calls",
							resp.StatusCode, len(body), readErr, calls.Load(), wantCalls)
					}
				})
			}
		})
	}
}
