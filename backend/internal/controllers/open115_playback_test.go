package controllers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"

	"github.com/coocood/freecache"
)

func setup115PlaybackCache(t *testing.T) {
	t.Helper()
	previousCache, previousLogger := db.Cache, helpers.AppLogger
	db.Cache = db.CacheGlobal{CacheInstance: freecache.NewCache(1024 * 1024), CacheSize: 1024 * 1024}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		db.Cache, helpers.AppLogger = previousCache, previousLogger
	})
}

func Test115PlaybackSmartSkip(t *testing.T) {
	for _, tt := range []struct {
		name              string
		enabled           bool
		proxy, force      int
		other             playback.Slot
		expired, wantCopy bool
	}{
		{name: "disabled", other: playback.Slot{Mode: playback.ModeDirect, UA: "other"}},
		{name: "first-direct", enabled: true},
		{name: "same-slot", enabled: true, other: playback.Slot{Mode: playback.ModeDirect, UA: "player"}},
		{name: "expired-other", enabled: true, other: playback.Slot{Mode: playback.ModeDirect, UA: "other"}, expired: true},
		{name: "another-direct", enabled: true, other: playback.Slot{Mode: playback.ModeDirect, UA: "other"}, wantCopy: true},
		{name: "actual-proxy", enabled: true, proxy: 1, other: playback.Slot{Mode: playback.ModeDirect, UA: "other"}},
		{name: "forced-direct-with-disabled-control", enabled: true, proxy: 1, force: 1,
			other: playback.Slot{Mode: playback.ModeProxy, UA: "proxy"}, wantCopy: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup115PlaybackCache(t)
			manager := playback.NewManager()
			source := playback.SourceKey{AccountID: 1, UserID: t.Name(), PickCode: "original-pick"}
			slot := playback.Slot{Mode: v115URLPlaybackMode(tt.force, tt.proxy), UA: v115EffectiveUA(tt.force, tt.proxy, "player")}
			if tt.other.Mode != "" {
				expires := time.Now().Add(time.Minute)
				if tt.expired {
					expires = time.Now().Add(-time.Second)
				}
				manager.Record(source, tt.other, expires)
			}
			key := v115URLCacheKey(source.PickCode, tt.force, tt.proxy, "player")
			originalCalls, copyCalls := 0, 0
			original := func(context.Context) string { originalCalls++; return "https://original.invalid/video" }
			copyURL := func(context.Context) (string, error) { copyCalls++; return "https://copy.invalid/video", nil }
			result := resolve115URLMiss(context.Background(), manager, source, slot, key, tt.enabled, original, copyURL)
			if (copyCalls == 1) != tt.wantCopy || originalCalls+copyCalls != 1 {
				t.Fatalf("original=%d copy=%d wantCopy=%v", originalCalls, copyCalls, tt.wantCopy)
			}
			if cached := string(db.Cache.Get(key)); cached != result {
				t.Fatalf("URL 未写回原始键：cached=%q result=%q", cached, result)
			}
			// 关闭开关后仍复用已经生成的缓存，不重新取链或清除副本 URL。
			again := resolve115URLMiss(context.Background(), manager, source, slot, key, false, original, copyURL)
			if again != result || originalCalls+copyCalls != 1 {
				t.Fatal("缓存命中不应再次调用原文件或副本接口")
			}
		})
	}
}

func Test115PlaybackConcurrentColdRequests(t *testing.T) {
	for _, sameUA := range []bool{false, true} {
		t.Run(fmt.Sprintf("sameUA=%v", sameUA), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				setup115PlaybackCache(t)
				manager := playback.NewManager()
				source := playback.SourceKey{AccountID: 1, UserID: t.Name(), PickCode: "cold-file"}
				var originalCalls, copyCalls atomic.Int32
				start := make(chan struct{})
				originalStarted := make(chan struct{})
				releaseOriginal := make(chan struct{})
				results := make(chan string, 8)
				var wg sync.WaitGroup
				for i := range 8 {
					wg.Go(func() {
						<-start
						ua := fmt.Sprintf("player-%d", i)
						if sameUA {
							ua = "player"
						}
						slot := playback.Slot{Mode: playback.ModeDirect, UA: ua}
						key := v115URLCacheKey(source.PickCode, 1, 0, ua)
						// 与 HTTP 入口一致，相同缓存键合并；不同 UA 不等待原文件网络请求。
						if !keyLock.lockContext(t.Context(), key, v115URLCacheLockWait) {
							results <- ""
							return
						}
						defer keyLock.Unlock(key)
						results <- resolve115URLMiss(context.Background(), manager, source, slot, key, true,
							func(context.Context) string {
								if originalCalls.Add(1) == 1 {
									close(originalStarted)
								}
								<-releaseOriginal
								return "original-url"
							},
							func(context.Context) (string, error) {
								return fmt.Sprintf("copy-url-%d", copyCalls.Add(1)), nil
							},
						)
					})
				}
				close(start)
				<-originalStarted
				synctest.Wait()
				wantCopies, wantURLs := int32(7), 8
				if sameUA {
					wantCopies, wantURLs = 0, 1
				}
				if originalCalls.Load() != 1 || copyCalls.Load() != wantCopies {
					t.Errorf("原文件仍在取链时 original=%d copy=%d，期望 1/%d", originalCalls.Load(), copyCalls.Load(), wantCopies)
				}
				close(releaseOriginal)
				wg.Wait()
				close(results)
				unique := make(map[string]bool)
				for result := range results {
					unique[result] = true
				}
				if originalCalls.Load() != 1 || copyCalls.Load() != wantCopies || len(unique) != wantURLs {
					t.Fatalf("original=%d copy=%d unique=%d", originalCalls.Load(), copyCalls.Load(), len(unique))
				}
			})
		})
	}
}

func Test115PlaybackInvalidatedURLReentersDecisionAndFallsBack(t *testing.T) {
	setup115PlaybackCache(t)
	manager := playback.NewManager()
	source := playback.SourceKey{AccountID: 1, UserID: t.Name(), PickCode: "retry-file"}
	slot := playback.Slot{Mode: playback.ModeDirect, UA: "player-a"}
	other := playback.Slot{Mode: playback.ModeDirect, UA: "player-b"}
	key := v115URLCacheKey(source.PickCode, 1, 0, slot.UA)
	manager.Record(source, slot, time.Now().Add(playback.URLCacheTTL))
	manager.Record(source, other, time.Now().Add(playback.URLCacheTTL))
	db.Cache.Set(key, []byte("expired-url"), 3000)
	// HEAD 失效和 freecache 提前淘汰均删除 URL，不能丢失其他槽位。
	db.Cache.Delete(key)
	copyCalls := 0
	result := resolve115URLMiss(context.Background(), manager, source, slot, key, true,
		func(context.Context) string { return "fallback-url" },
		func(context.Context) (string, error) { copyCalls++; return "", errors.New("copy failed") },
	)
	if copyCalls != 1 || result != "fallback-url" || string(db.Cache.Get(key)) != result {
		t.Fatalf("失效重取未判定或未降级：copy=%d result=%q", copyCalls, result)
	}
}

func Test115PlaybackCanceledCopyDoesNotStartFallback(t *testing.T) {
	setup115PlaybackCache(t)
	manager := playback.NewManager()
	source := playback.SourceKey{AccountID: 1, UserID: t.Name(), PickCode: "canceled-copy"}
	slot := playback.Slot{Mode: playback.ModeDirect, UA: "waiting"}
	manager.Record(source, playback.Slot{Mode: playback.ModeDirect, UA: "other"}, time.Now().Add(playback.URLCacheTTL))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := false
	result := resolve115URLMiss(ctx, manager, source, slot, "canceled-copy-cache", true,
		func(context.Context) string { called = true; return "unexpected" },
		func(ctx context.Context) (string, error) { cancel(); return "", ctx.Err() },
	)
	if called || result != "" || len(db.Cache.Get("canceled-copy-cache")) != 0 {
		t.Fatal("播放器取消副本请求后不能再启动普通取链或发布缓存")
	}
}

func Test115PlaybackCanceledMissDoesNotReserve(t *testing.T) {
	setup115PlaybackCache(t)
	source := playback.SourceKey{AccountID: 1, UserID: t.Name(), PickCode: "waiting-file"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := playback.NewManager()
	called := false
	result := resolve115URLMiss(ctx, manager, source, playback.Slot{Mode: playback.ModeDirect, UA: "waiting"}, "waiting-cache", true,
		func(context.Context) string { called = true; return "unexpected" },
		func(context.Context) (string, error) { called = true; return "unexpected", nil },
	)
	if called || result != "" || len(db.Cache.Get("waiting-cache")) != 0 ||
		manager.HasOther(source, playback.Slot{Mode: playback.ModeDirect, UA: "observer"}, time.Now()) {
		t.Fatal("请求已取消时不能取链、发布缓存或预留槽位")
	}
}

func Test115PlaybackCopyDeadlineKeepsFallbackAndReservation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setup115PlaybackCache(t)
		manager := playback.NewManager()
		source := playback.SourceKey{AccountID: 1, UserID: "uid", PickCode: "budget-file"}
		slot := playback.Slot{Mode: playback.ModeDirect, UA: "player"}
		other := playback.Slot{Mode: playback.ModeDirect, UA: "other"}
		manager.Record(source, other, time.Now().Add(playback.URLCacheTTL))
		started := time.Now()
		fallbackCalled := false
		result := resolve115URLMiss(t.Context(), manager, source, slot, "budget-cache", true,
			func(ctx context.Context) string {
				fallbackCalled = true
				if ctx.Err() != nil || !manager.HasOther(source, other, time.Now()) {
					t.Fatal("普通取链必须使用仍有效的请求并保留当前 pending 槽位")
				}
				return "fallback-url"
			},
			func(ctx context.Context) (string, error) {
				deadline, ok := ctx.Deadline()
				if !ok || deadline.Sub(started) != 10*time.Second {
					t.Fatalf("副本预算未在调用前生效：%v, %v", deadline.Sub(started), ok)
				}
				<-ctx.Done()
				return "", ctx.Err()
			},
		)
		if !fallbackCalled || result != "fallback-url" || time.Since(started) != 10*time.Second {
			t.Fatalf("副本超时未在 10 秒后降级：result=%q elapsed=%v", result, time.Since(started))
		}
		if manager.HasOther(source, other, time.Now().Add(playback.URLCacheTTL)) {
			t.Fatal("降级成功后的 pending 未撤销，或槽位未按缓存期限过期")
		}
	})
}

func Test115PlaybackCacheAndSlotUseSignedExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setup115PlaybackCache(t)
		manager := playback.NewManager()
		source := playback.SourceKey{AccountID: 1, UserID: "uid", PickCode: "expiry-file"}
		slot := playback.Slot{Mode: playback.ModeDirect, UA: "player"}
		observer := playback.Slot{Mode: playback.ModeDirect, UA: "observer"}
		value := fmt.Sprintf("https://download.invalid/video?t=%d", time.Now().Add(10*time.Minute).Unix())
		key := "signed-expiry-cache"
		got := resolve115URLMiss(t.Context(), manager, source, slot, key, false,
			func(context.Context) string { return value }, nil,
		)
		if got != value || !manager.HasOther(source, observer, time.Now()) {
			t.Fatal("新链接没有发布到缓存及槽位")
		}
		time.Sleep(4 * time.Minute)
		if cached := string(db.Cache.Get(key)); cached != value {
			t.Fatal("安全期限前缓存过早失效")
		}
		got = resolve115URLMiss(t.Context(), manager, source, slot, key, true,
			func(context.Context) string { t.Fatal("命中缓存不应重取"); return "" }, nil,
		)
		if got != value {
			t.Fatal("命中缓存未复用已签发链接")
		}
		time.Sleep(time.Minute)
		if len(db.Cache.Get(key)) != 0 || manager.HasOther(source, observer, time.Now()) {
			t.Fatal("URL 和槽位应在 t 提前 5 分钟同时过期，命中不能续期")
		}
	})
}

func Test115PlaybackExpiredURLDoesNotPublishOrLeavePending(t *testing.T) {
	for _, copyEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("copy=%v", copyEnabled), func(t *testing.T) {
			setup115PlaybackCache(t)
			manager := playback.NewManager()
			source := playback.SourceKey{AccountID: 1, UserID: "uid", PickCode: "expired-file"}
			slot := playback.Slot{Mode: playback.ModeDirect, UA: "player"}
			other := playback.Slot{Mode: playback.ModeDirect, UA: "other"}
			if copyEnabled {
				manager.Record(source, other, time.Now().Add(playback.URLCacheTTL))
			}
			expired := fmt.Sprintf("https://download.invalid/video?t=%d", time.Now().Add(time.Minute).Unix())
			originalCalls := 0
			got := resolve115URLMiss(t.Context(), manager, source, slot, "expired-cache", copyEnabled,
				func(context.Context) string { originalCalls++; return expired },
				func(context.Context) (string, error) { return expired, nil },
			)
			if got != "" || originalCalls != 1 || len(db.Cache.Get("expired-cache")) != 0 || manager.HasOther(source, other, time.Now()) {
				t.Fatal("已到安全期限的 URL 不能返回或成为永久缓存，失败必须撤销 pending")
			}
		})
	}
}

func TestCopy115URLRequiresAccountScoped115FileIdentity(t *testing.T) {
	setup115PlaybackCache(t)
	setupControllerTestDB(t, &models.SyncFile{})
	account := &models.Account{BaseModel: models.BaseModel{ID: 1}, UserId: "uid-1"}
	for _, file := range []models.SyncFile{
		{AccountId: 2, SourceType: models.SourceType115, PickCode: "same-pick", FileId: "other-account"},
		{AccountId: 1, SourceType: models.SourceType123, PickCode: "same-pick", FileId: "other-provider"},
	} {
		if err := db.Db.Create(&file).Error; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := copy115URL(context.Background(), account, "same-pick", "player"); err == nil {
		t.Fatal("不能借用其他账号或其他来源的文件 ID")
	}
	file := models.SyncFile{AccountId: 1, SourceType: models.SourceType115, PickCode: "same-pick"}
	if err := db.Db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := copy115URL(context.Background(), account, "same-pick", "player"); err == nil {
		t.Fatal("没有 FileId 时不能发起复制")
	}
}
