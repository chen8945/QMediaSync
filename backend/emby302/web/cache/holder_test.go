package cache

import (
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
	oldDefault := DefaultExpired
	DefaultExpired = func() time.Duration { return time.Minute }
	t.Cleanup(func() { DefaultExpired = oldDefault })
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
			t.Cleanup(func() {
				cacheMap.Delete(key)
				spaceMap.Delete(space)
			})
			before := time.Now().UnixMilli()
			putCache(key, http.StatusOK, []byte{}, respHeader{expired: tt.expired, space: space, spaceKey: key})
			WaitingForHandleChan()
			after := time.Now().UnixMilli()
			rc, ok := getCache(key)
			if ok != tt.wantHit {
				t.Fatalf("cache hit = %v, want %v", ok, tt.wantHit)
			}
			if _, ok := GetSpaceCache(space, key); ok != tt.wantHit {
				t.Fatalf("space hit = %v, want %v", ok, tt.wantHit)
			}
			if !tt.wantHit {
				if _, stored := cacheMap.Load(key); stored {
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
			cacheMap.Store(key, rc)
			putSpaceCache(key, key, rc)
			t.Cleanup(func() {
				cacheMap.Delete(key)
				spaceMap.Delete(key)
			})
			for range 2 {
				if _, ok := getCache(key); ok != tt.wantHit {
					t.Errorf("cache hit = %v, want %v", ok, tt.wantHit)
				}
				if _, ok := GetSpaceCache(key, key); ok != tt.wantHit {
					t.Errorf("space hit = %v, want %v", ok, tt.wantHit)
				}
			}
			if rc.expired != tt.expired {
				t.Fatal("cache read renewed the deadline")
			}
		})
	}
}

func TestQueuedResponseExpiringBeforeStorageIsDropped(t *testing.T) {
	oldDefault := DefaultExpired
	DefaultExpired = func() time.Duration { return time.Minute }
	t.Cleanup(func() { DefaultExpired = oldDefault })
	blocker := &respCache{cacheKey: t.Name() + "/blocker", expired: time.Now().Add(time.Minute).UnixMilli()}
	blocker.mu.Lock()
	cacheHandleWaitGroup.Add(1)
	preCacheChan <- blocker
	key := t.Name()
	t.Cleanup(func() {
		cacheMap.Delete(blocker.cacheKey)
		cacheMap.Delete(key)
		spaceMap.Delete(key)
	})
	deadline := time.Now().Add(30 * time.Millisecond)
	putCache(key, http.StatusTemporaryRedirect, []byte{}, respHeader{
		expired:  strconv.FormatInt(deadline.UnixMilli(), 10),
		space:    key,
		spaceKey: key,
		header:   http.Header{"Location": {"https://example.invalid/signed"}},
	})
	time.Sleep(time.Until(deadline) + time.Millisecond)
	blocker.mu.Unlock()
	WaitingForHandleChan()
	if _, ok := cacheMap.Load(key); ok {
		t.Fatal("response expired in the queue was still stored")
	}
	if _, ok := getSpace(key).Load(key); ok {
		t.Fatal("response expired in the queue was published to a cache space")
	}
}

func TestRequestCacherSnapshotsConcurrentResponses(t *testing.T) {
	const count = 32
	var calls atomic.Int32
	router := gin.New()
	router.Use(RequestCacher())
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
	WaitingForHandleChan()
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
