package embyclientrestgo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConnectionProductAndIdentityReads(t *testing.T) {
	for _, product := range []Product{ProductEmby, ProductJellyfin} {
		for _, first := range []string{"product", "identity"} {
			t.Run(string(product)+"/"+first, func(t *testing.T) {
				var pings, publics atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/base/System/Ping":
						pings.Add(1)
						if product == ProductEmby {
							fmt.Fprint(w, "Emby Server")
						} else {
							fmt.Fprint(w, `"Jellyfin Server"`)
						}
					case "/base/System/Info/Public":
						publics.Add(1)
						if product == ProductEmby {
							fmt.Fprint(w, `{"Id":"real-server","ServerName":"Jellyfin"}`)
						} else {
							fmt.Fprint(w, `{"Id":"real-server","ProductName":"Jellyfin Server"}`)
						}
					default:
						t.Errorf("unexpected request %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				defer server.Close()
				c := NewClientForProduct(server.URL+"/base/", "key", product)
				if first == "product" {
					result, err := c.CheckConnection(t.Context())
					if err != nil || result.Product != product || result.Diagnostic != nil {
						t.Fatalf("%+v %v", result, err)
					}
					if publics.Load() != 0 {
						t.Fatal("product-only request read Public")
					}
				}
				for range 2 {
					id, err := c.GetServerID(t.Context())
					if err != nil || id != "real-server" {
						t.Fatalf("id=%s err=%v", id, err)
					}
					result, err := c.CheckConnection(t.Context())
					if err != nil || result.Product != product {
						t.Fatalf("%+v %v", result, err)
					}
				}
				wantPing := int32(1)
				if first == "identity" && product == ProductJellyfin {
					wantPing = 0
				}
				if publics.Load() != 1 || pings.Load() != wantPing {
					t.Fatalf("Public=%d Ping=%d", publics.Load(), pings.Load())
				}
			})
		}
	}
}

func TestBrandFailureDoesNotBlockOrCacheAndMismatchGuardsBusiness(t *testing.T) {
	for _, response := range []struct {
		name, body string
		status     int
	}{
		{"forbidden", "", 403}, {"offline", "", 503}, {"malformed", `"Jellyfin Server`, 200}, {"unknown", "Other", 200}, {"instance name", "My Jellyfin", 200}, {"empty", "", 200},
	} {
		t.Run(response.name, func(t *testing.T) {
			var pings, business atomic.Int32
			recovered := atomic.Bool{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Ping":
					pings.Add(1)
					if recovered.Load() {
						fmt.Fprint(w, `"Jellyfin Server"`)
					} else {
						w.WriteHeader(response.status)
						fmt.Fprint(w, response.body)
					}
				case "/emby/Library/MediaFolders":
					business.Add(1)
					fmt.Fprint(w, `{"Items":[]}`)
				case "/System/Info/Public":
					fmt.Fprint(w, `{"Id":"real"}`)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			c := NewClientForProduct(server.URL, "key", ProductEmby)
			result, err := c.CheckConnection(t.Context())
			if err != nil || result.Product != ProductUnknown || result.Diagnostic == nil {
				t.Fatalf("%+v %v", result, err)
			}
			if _, err := c.GetAllMediaLibraries(); err != nil {
				t.Fatal(err)
			}
			if business.Load() != 1 || pings.Load() < 2 {
				t.Fatal("brand failure blocked business or was cached")
			}
			recovered.Store(true)
			if _, err := c.GetAllMediaLibraries(); err == nil {
				t.Fatal("confirmed mismatch allowed business")
			}
			if business.Load() != 1 {
				t.Fatal("mismatch forwarded business")
			}
		})
	}
}

func TestPingPrefixFallbackOnlyOn404(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/base/System/Ping" {
					w.WriteHeader(status)
					fmt.Fprint(w, "Other")
					return
				}
				if r.URL.Path != "/base/emby/System/Ping" {
					t.Errorf("unexpected %s", r.URL.Path)
				}
				fmt.Fprint(w, "Emby Server")
			}))
			defer server.Close()
			result, err := NewClient(server.URL+"/base", "key").CheckConnection(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if status == 404 {
				want = 2
			}
			if len(paths) != want || (result.Product == ProductEmby) != (status == 404) {
				t.Fatalf("paths=%v result=%+v", paths, result)
			}
		})
	}
}

func TestMissingIdentityRemainsFailureAfterConfirmedProduct(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Ping") {
			fmt.Fprint(w, "Emby Server")
			return
		}
		fmt.Fprint(w, `{"Id":" ","ProductName":"Emby Server"}`)
	}))
	defer server.Close()
	c := NewClient(server.URL, "key")
	if _, err := c.CheckConnection(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetServerID(t.Context()); err == nil {
		t.Fatal("blank identity accepted")
	}
}

func TestConcurrentPingMergeAndWaitingCancellation(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		fmt.Fprint(w, `"Jellyfin Server"`)
	}))
	defer server.Close()
	c := NewClient(server.URL, "key")
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := c.CheckConnection(t.Context()); err != nil {
			t.Error(err)
		}
	})
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := c.CheckConnection(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("cancelled waiter: %v", err)
	}
	for range 8 {
		wg.Go(func() {
			if _, err := c.CheckConnection(t.Context()); err != nil {
				t.Error(err)
			}
		})
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("pings=%d", calls.Load())
	}
}

func TestFailedBrandProbeRunsOncePerPagedOperation(t *testing.T) {
	var pings, pages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Ping":
			pings.Add(1)
			w.WriteHeader(http.StatusForbidden)
		case "/emby/Items":
			pages.Add(1)
			id := "1"
			if r.URL.Query().Get("StartIndex") == "1" {
				id = "2"
			}
			fmt.Fprintf(w, `{"Items":[{"Id":%q,"Type":"Movie"}],"TotalRecordCount":2}`, id)
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClientForProduct(server.URL, "key", ProductJellyfin)
	for range 2 {
		items := 0
		if err := client.FetchMediaItemsByLibraryID(t.Context(), EmbyItemsQuery{Limit: 1}, func(item BaseItemDtoV2) error {
			items++
			return nil
		}); err != nil || items != 2 {
			t.Fatalf("unknown-product compatibility paging: items=%d err=%v", items, err)
		}
	}
	if pings.Load() != 2 || pages.Load() != 4 {
		t.Fatalf("probe repeated inside pages or failure cached between operations: pings=%d pages=%d", pings.Load(), pages.Load())
	}
}

func TestEmbyBusinessErrorsDoNotExposeCredentialURL(t *testing.T) {
	const apiKey = "private-api-key-for-error-test"
	for _, offline := range []bool{false, true} {
		t.Run(fmt.Sprintf("offline=%v", offline), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/System/Ping" {
					fmt.Fprint(w, "Emby Server")
					return
				}
				if r.URL.Query().Get("api_key") != apiKey {
					t.Error("test did not exercise Emby query authentication")
				}
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, r.URL.String())
			}))
			defer server.Close()
			if offline {
				server.Close()
			}
			_, err := NewClientForProduct(server.URL, apiKey, ProductEmby).GetAllMediaLibraries()
			if err == nil || strings.Contains(err.Error(), apiKey) || strings.Contains(err.Error(), server.URL) || strings.Contains(err.Error(), "api_key=") {
				t.Fatalf("business error exposed the credential URL: %v", err)
			}
			if !offline && !strings.Contains(err.Error(), "500") {
				t.Fatalf("HTTP error lost its status diagnostic: %v", err)
			}
		})
	}
}
