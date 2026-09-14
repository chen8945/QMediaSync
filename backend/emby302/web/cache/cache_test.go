package cache

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
)

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
