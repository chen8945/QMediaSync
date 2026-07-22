package emby

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"qmediasync/emby302/config"

	"github.com/gin-gonic/gin"
)

func TestCalcRandomItemsCacheKeySeparatesRandomPools(t *testing.T) {
	tests := []struct {
		name  string
		left  string
		right string
		equal bool
	}{
		{
			name:  "different API keys",
			left:  "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&api_key=key-one",
			right: "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&api_key=key-two",
		},
		{
			name:  "different parent IDs",
			left:  "/Users/1/Items?SortBy=Random&ParentId=library-one&Limit=500&api_key=key-one",
			right: "/Users/1/Items?SortBy=Random&ParentId=library-two&Limit=500&api_key=key-one",
		},
		{
			name:  "different effective limits",
			left:  "/Users/1/Items?SortBy=Random&ParentId=library&Limit=300&api_key=key-one",
			right: "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&api_key=key-one",
		},
		{
			name:  "missing limit and explicit default limit",
			left:  "/Users/1/Items?SortBy=Random&ParentId=library&api_key=key-one",
			right: "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&api_key=key-one",
			equal: true,
		},
		{
			name:  "limit above maximum and explicit maximum limit",
			left:  "/Users/1/Items?SortBy=Random&ParentId=library&Limit=501&api_key=key-one",
			right: "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&api_key=key-one",
			equal: true,
		},
		{
			name:  "invalid limit and explicit default limit",
			left:  "/Users/1/Items?SortBy=Random&ParentId=library&Limit=invalid&api_key=key-one",
			right: "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&api_key=key-one",
			equal: true,
		},
		{
			name:  "SortOrder does not change the random pool",
			left:  "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&SortOrder=Ascending&api_key=key-one",
			right: "/Users/1/Items?SortBy=Random&ParentId=library&Limit=500&SortOrder=Descending&api_key=key-one",
			equal: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			left := calcRandomItemsCacheKey(newItemsTestContext(t, tt.left))
			right := calcRandomItemsCacheKey(newItemsTestContext(t, tt.right))

			if tt.equal && left != right {
				t.Fatalf("cache key should match: %q != %q", left, right)
			}
			if !tt.equal && left == right {
				t.Fatalf("cache key should differ: %q", left)
			}
			if len(left) != 32 || len(right) != 32 {
				t.Fatalf("cache keys should be MD5 hashes: %q, %q", left, right)
			}
			if strings.Contains(left, "key-one") || strings.Contains(right, "key-two") {
				t.Fatal("cache key must not include an API key in plain text")
			}
		})
	}
}

func TestRandomItemsWithLimitUsesCopyOfOriginalRequestURL(t *testing.T) {
	var upstreamURI string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamURI = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Items":[]}`)
	}))
	defer upstream.Close()

	originalConfig := config.C
	config.C = &config.Config{Emby: &config.Emby{Host: upstream.URL}}
	t.Cleanup(func() {
		config.C = originalConfig
	})

	ctx := newItemsTestContext(
		t,
		"/Users/1/Items/with_limit?SortBy=Random&Limit=300&SortOrder=Descending&ParentId=library&api_key=key-one",
	)
	originalURI := ctx.Request.URL.RequestURI()

	RandomItemsWithLimit(ctx)

	if upstreamURI != "/Users/1/Items?Limit=300&ParentId=library&SortBy=Random&api_key=key-one" {
		t.Fatalf("unexpected upstream URI: %s", upstreamURI)
	}
	if ctx.Request.URL.RequestURI() != originalURI {
		t.Fatalf("original request URL changed: got %s, want %s", ctx.Request.URL.RequestURI(), originalURI)
	}
}

func TestRandomItemsWithLimitDefaultsBlankLimitTo500(t *testing.T) {
	var upstreamURI string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamURI = r.URL.RequestURI()
		_, _ = io.WriteString(w, `{"Items":[]}`)
	}))
	defer upstream.Close()

	originalConfig := config.C
	config.C = &config.Config{Emby: &config.Emby{Host: upstream.URL}}
	t.Cleanup(func() {
		config.C = originalConfig
	})

	ctx := newItemsTestContext(t, "/Users/1/Items/with_limit?SortBy=Random&Limit=+++&api_key=key-one")
	RandomItemsWithLimit(ctx)

	if !strings.Contains(upstreamURI, "Limit=500") {
		t.Fatalf("blank Limit should default to 500: %s", upstreamURI)
	}
}

func TestRandomItemsWithLimitCapsLimitTo500(t *testing.T) {
	var upstreamURI string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamURI = r.URL.RequestURI()
		_, _ = io.WriteString(w, `{"Items":[]}`)
	}))
	defer upstream.Close()

	originalConfig := config.C
	config.C = &config.Config{Emby: &config.Emby{Host: upstream.URL}}
	t.Cleanup(func() {
		config.C = originalConfig
	})

	ctx := newItemsTestContext(t, "/Users/1/Items/with_limit?SortBy=Random&Limit=501&api_key=key-one")
	RandomItemsWithLimit(ctx)

	if !strings.Contains(upstreamURI, "Limit=500") {
		t.Fatalf("Limit above 500 should be capped: %s", upstreamURI)
	}
}

func newItemsTestContext(t *testing.T, target string) *gin.Context {
	t.Helper()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return ctx
}
