package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestPutCacheHonorsAbsoluteDeadline(t *testing.T) {
	scope := newCacheScope(t)
	now := time.Now().UnixMilli()
	tests := []struct {
		name    string
		expired string
		wantHit bool
	}{
		{name: "disabled", expired: "-1"},
		{name: "past", expired: strconv.FormatInt(now-1, 10)},
		{name: "deadline", expired: strconv.FormatInt(now, 10)},
		{name: "zero", expired: "0"},
		{name: "invalid", expired: "invalid"},
		{name: "overflow", expired: "9223372036854775808"},
		{name: "future", expired: strconv.FormatInt(now+30_000, 10), wantHit: true},
		{name: "default", wantHit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := t.Name()
			space := t.Name()
			before := time.Now().UnixMilli()
			scope.putCache(key, http.StatusOK, []byte{}, respHeader{expired: tt.expired, space: space, spaceKey: key})
			after := time.Now().UnixMilli()
			rc, ok := scope.getCache(key)
			if ok != tt.wantHit {
				t.Fatalf("cache hit = %v, want %v", ok, tt.wantHit)
			}
			if _, ok := scope.GetSpaceCache(space, key); ok != tt.wantHit {
				t.Fatalf("space hit = %v, want %v", ok, tt.wantHit)
			}
			if !tt.wantHit {
				scope.store.mu.Lock()
				_, stored := scope.requests[key]
				scope.store.mu.Unlock()
				if stored {
					t.Fatal("expired response was stored")
				}
				return
			}
			if tt.expired == "" {
				if rc.expired < before+time.Minute.Milliseconds() || rc.expired > after+time.Minute.Milliseconds() {
					t.Fatalf("default deadline = %d, want one minute from write", rc.expired)
				}
			} else if strconv.FormatInt(rc.expired, 10) != tt.expired {
				t.Fatalf("deadline = %d, want %s", rc.expired, tt.expired)
			}
		})
	}
}

func TestCacheReadsRejectExpiredEntriesWithoutRenewing(t *testing.T) {
	scope := newCacheScope(t)
	now := time.Now().UnixMilli()
	tests := []struct {
		name    string
		expired int64
		wantHit bool
	}{
		{name: "past", expired: now - 1},
		{name: "deadline", expired: now},
		{name: "future", expired: now + time.Hour.Milliseconds(), wantHit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := t.Name()
			rc := &respCache{cacheKey: key, expired: tt.expired}
			scope.store.mu.Lock()
			scope.requests[key] = rc
			scope.putSpaceCacheLocked(key, key, rc)
			scope.store.mu.Unlock()
			for range 2 {
				if _, ok := scope.getCache(key); ok != tt.wantHit {
					t.Errorf("cache hit = %v, want %v", ok, tt.wantHit)
				}
				if _, ok := scope.GetSpaceCache(key, key); ok != tt.wantHit {
					t.Errorf("space hit = %v, want %v", ok, tt.wantHit)
				}
			}
			if rc.expired != tt.expired {
				t.Fatal("cache read renewed the deadline")
			}
		})
	}
}

func TestRequestCacherSnapshotsConcurrentResponses(t *testing.T) {
	scope := newCacheScope(t)
	const count = 32
	var calls atomic.Int32
	router := gin.New()
	router.Use(scope.RequestCacher())
	router.GET("/:id", func(c *gin.Context) {
		calls.Add(1)
		id, _ := strconv.Atoi(c.Param("id"))
		c.Header(HeaderKeyExpired, Duration(time.Minute))
		c.Header("X-Response-ID", c.Param("id"))
		c.Status(http.StatusOK + id%2)
		_, _ = c.Writer.Write([]byte("body:" + c.Param("id")))
	})
	// 每轮使用独立键，确保重复执行验证不会命中上一轮的数据。
	query := "?run=" + strconv.FormatInt(time.Now().UnixNano(), 10)
	request := func(id int) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest("GET", "/"+strconv.Itoa(id)+query, nil))
		return response
	}
	var workers sync.WaitGroup
	for id := range count {
		workers.Go(func() { request(id) })
	}
	workers.Wait()
	for id := range count {
		response := request(id)
		if response.Code != http.StatusOK+id%2 || response.Header().Get("X-Response-ID") != strconv.Itoa(id) ||
			response.Body.String() != "body:"+strconv.Itoa(id) {
			t.Fatalf("cached response %d changed: code=%d, header=%v, body=%q", id, response.Code, response.Header(), response.Body.String())
		}
	}
	if got := calls.Load(); got != count {
		t.Fatalf("handler calls = %d, want %d", got, count)
	}
}

func newCacheScope(t *testing.T) *Scope {
	t.Helper()
	store := NewStore(time.Minute)
	t.Cleanup(func() {
		if err := store.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return store.NewScope()
}

func TestScopesIsolateResponsesAndRejectLateWrites(t *testing.T) {
	first := newCacheScope(t)
	second := first.store.NewScope()
	request := httptest.NewRequest("GET", "/Items/item?api_key=same", nil)
	header := http.Header{HeaderKeySpace: {"items"}, HeaderKeySpaceKey: {"same"}}
	first.PutRequestCache(request, 200, []byte("first"), header)
	if _, ok := second.GetRequestCache(request); ok {
		t.Fatal("new connection reused old response")
	}
	if _, ok := second.GetSpaceCache("items", "same"); ok {
		t.Fatal("new connection reused old space")
	}
	old, _ := first.GetRequestCache(request)
	second.PutRequestCache(request, 200, []byte("second"), header)
	first.Close()
	first.PutRequestCache(request, 200, []byte("late first"), header)
	old.Update(0, []byte("late update"), nil)
	if _, ok := first.GetRequestCache(request); ok {
		t.Fatal("closed scope revived")
	}
	response, ok := second.GetSpaceCache("items", "same")
	if !ok || string(response.BodyBytes()) != "second" {
		t.Fatal("late response changed new connection")
	}
}

func TestStoreSharesCapacityAcrossScopes(t *testing.T) {
	first := newCacheScope(t)
	second := first.store.NewScope()
	for id := range MaxCacheNum {
		first.putCache(strconv.Itoa(id), 200, []byte("first"), respHeader{})
	}
	second.putCache("second", 200, []byte("second"), respHeader{})
	if _, ok := first.getCache("0"); ok {
		t.Fatal("new scope did not share eviction budget")
	}
	if _, ok := second.getCache("second"); !ok {
		t.Fatal("latest response missing")
	}
	response, _ := second.getCache("second")
	response.Update(0, make([]byte, int(MaxCacheSize)+1), nil)
	if _, ok := second.getCache("second"); ok {
		t.Fatal("response update escaped capacity budget")
	}
	if _, ok := first.getCache("1"); !ok {
		t.Fatal("oversized update evicted other responses")
	}
}
