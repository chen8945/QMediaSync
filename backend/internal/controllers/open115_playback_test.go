package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
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
	"github.com/gin-gonic/gin"
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
			result := resolve115URLMiss(context.Background(), manager, source, slot, key, "", tt.enabled, original, copyURL)
			if (copyCalls == 1) != tt.wantCopy || originalCalls+copyCalls != 1 {
				t.Fatalf("original=%d copy=%d wantCopy=%v", originalCalls, copyCalls, tt.wantCopy)
			}
			if cached := string(db.Cache.Get(key)); cached != result {
				t.Fatalf("URL 未写回原始键：cached=%q result=%q", cached, result)
			}
			// 关闭开关后仍复用已经生成的缓存，不重新取链或清除副本 URL。
			again := resolve115URLMiss(context.Background(), manager, source, slot, key, "", false, original, copyURL)
			if again != result || originalCalls+copyCalls != 1 {
				t.Fatal("缓存命中不应再次调用原文件或副本接口")
			}
		})
	}
}

func Test115PlaybackLogsLinksOnlyWhenNewURLIsPublished(t *testing.T) {
	previousLevel := helpers.ConfiguredLogLevel()
	helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
	t.Cleanup(func() { helpers.SetGlobalLogLevel(previousLevel) })
	for _, tt := range []struct {
		name string
		copy bool
		kind string
	}{
		{name: "原文件", kind: "原文件"},
		{name: "副本", copy: true, kind: "副本"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup115PlaybackCache(t)
			setupControllerTestDB(t, &models.Account{})
			previousSettings := models.SettingsGlobal
			models.SettingsGlobal = &models.Settings{}
			t.Cleanup(func() { models.SettingsGlobal = previousSettings })
			account := &models.Account{Name: "日志测试", SourceType: models.SourceType115, UserId: "log-user"}
			if err := db.Db.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
			manager := playback.NewManager()
			source := playback.SourceKey{AccountID: account.ID, UserID: account.UserId, PickCode: "log-pick"}
			slot := playback.Slot{Mode: playback.ModeDirect, UA: "Yamby/2.1.0.8"}
			if tt.copy {
				manager.Record(source, playback.Slot{Mode: playback.ModeDirect, UA: "RodelPlayer/2.2607.7.0"}, time.Now().Add(time.Hour))
			}
			key := v115URLCacheKey(source.PickCode, 1, 0, slot.UA)
			origin := "http://qms.test/115/url/video.mkv?pickcode=log-pick&userid=log-user&force=1"
			fetches := 0
			fetch := func(context.Context) string {
				fetches++
				return fmt.Sprintf("https://cdn.test/%%E5%%BD%%B1%%E7%%89%%87.mkv?generation=%d&k=a%%2Bb", fetches)
			}
			resolve := func() string {
				return resolve115URLMiss(t.Context(), manager, source, slot, key, origin, tt.copy, fetch,
					func(ctx context.Context) (string, error) {
						return fetch(ctx), nil
					},
				)
			}
			first := resolve()
			for _, want := range []string{`文件="影片.mkv"`, `UA="Yamby/2.1.0.8"`, "来源=" + tt.kind, "PickCode=log-pick"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("取链日志缺少 %q：%s", want, output.String())
				}
			}
			// 通过真实 HTTP 控制器重复命中同一缓存；仅产生简短命中和跳转日志。
			for range 3 {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodGet, origin, nil)
				c.Request.Header.Set("User-Agent", slot.UA)
				Get115UrlByPickCode(c)
				if w.Code != http.StatusFound || w.Header().Get("Location") != first {
					t.Fatalf("缓存响应 = %d %q", w.Code, w.Header().Get("Location"))
				}
			}
			if resolve() != first || fetches != 1 || strings.Count(output.String(), origin) != 1 || strings.Count(output.String(), first) != 1 {
				t.Fatalf("缓存命中不应重取或重复长链接：fetches=%d，日志=%s", fetches, output.String())
			}
			db.Cache.Delete(key)
			second := resolve()
			if second == first || fetches != 2 || strings.Count(output.String(), origin) != 2 || strings.Count(output.String(), second) != 1 {
				t.Fatalf("失效刷新应再输出一次成对地址：fetches=%d，日志=%s", fetches, output.String())
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
						results <- resolve115URLMiss(context.Background(), manager, source, slot, key, "", true,
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

func Test115PlaybackInvalidatedURLReentersIsolationDecision(t *testing.T) {
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
	copyCalls, originalCalls := 0, 0
	result := resolve115URLMiss(context.Background(), manager, source, slot, key, "", true,
		func(context.Context) string { originalCalls++; return "fallback-url" },
		func(context.Context) (string, error) { copyCalls++; return "", errors.New("copy failed") },
	)
	if copyCalls != 1 || originalCalls != 0 || result != "" || len(db.Cache.Get(key)) != 0 ||
		!manager.HasOther(source, slot, time.Now()) {
		t.Fatalf("隔离失败仍调用原文件或影响其他槽位：copy=%d original=%d result=%q", copyCalls, originalCalls, result)
	}
}

func Test115PlaybackIsolationFailurePreservesOtherSlotAndURL(t *testing.T) {
	for _, tt := range []struct {
		name    string
		err     error
		value   string
		pending bool
	}{
		{name: "复制失败", err: errors.New("copy failed")},
		{name: "其他 UA 正在取链", err: errors.New("copy failed"), pending: true},
		{name: "副本身份缺失", err: errors.New("缺少原文件 ID 或 PickCode")},
		{name: "空链接"},
		{name: "签名进入安全窗口", value: "https://copy.invalid/video?t=1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup115PlaybackCache(t)
			var logs bytes.Buffer
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			manager := playback.NewManager()
			source := playback.SourceKey{AccountID: 1, UserID: t.Name(), PickCode: "protected-file"}
			slot := playback.Slot{Mode: playback.ModeDirect, UA: "new-player"}
			other := playback.Slot{Mode: playback.ModeDirect, UA: "existing-player"}
			if tt.pending {
				manager.Begin(source, other, time.Now())
			} else {
				manager.Record(source, other, time.Now().Add(playback.URLCacheTTL))
			}
			key := v115URLCacheKey(source.PickCode, 1, 0, slot.UA)
			otherKey := v115URLCacheKey(source.PickCode, 1, 0, other.UA)
			const previousURL = "https://original.invalid/video?signature=keep"
			db.Cache.Set(otherKey, []byte(previousURL), 3000)
			originalCalls, copyCalls := 0, 0
			value := resolve115URLMiss(t.Context(), manager, source, slot, key, "", true,
				func(context.Context) string { originalCalls++; return "unexpected-original" },
				func(context.Context) (string, error) { copyCalls++; return tt.value, tt.err },
			)
			if value != "" || originalCalls != 0 || copyCalls != 1 || len(db.Cache.Get(key)) != 0 ||
				string(db.Cache.Get(otherKey)) != previousURL {
				t.Fatalf("失败不应降级或改写缓存：value=%q original=%d copy=%d", value, originalCalls, copyCalls)
			}
			if manager.HasOther(source, other, time.Now()) || !manager.HasOther(source, slot, time.Now()) {
				t.Fatal("本次预留应释放，其他 UA 的有效或在途槽位必须保留")
			}
			if !strings.Contains(logs.String(), "保护已有播放链接") || strings.Contains(logs.String(), "115 取链成功") {
				t.Fatalf("未记录隔离保护原因，或误报取链成功：%s", logs.String())
			}
		})
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
	result := resolve115URLMiss(ctx, manager, source, slot, "canceled-copy-cache", "", true,
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
	result := resolve115URLMiss(ctx, manager, source, playback.Slot{Mode: playback.ModeDirect, UA: "waiting"}, "waiting-cache", "", true,
		func(context.Context) string { called = true; return "unexpected" },
		func(context.Context) (string, error) { called = true; return "unexpected", nil },
	)
	if called || result != "" || len(db.Cache.Get("waiting-cache")) != 0 ||
		manager.HasOther(source, playback.Slot{Mode: playback.ModeDirect, UA: "observer"}, time.Now()) {
		t.Fatal("请求已取消时不能取链、发布缓存或预留槽位")
	}
}

func Test115PlaybackCopyDeadlineReleasesReservationWithoutFallback(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setup115PlaybackCache(t)
		manager := playback.NewManager()
		source := playback.SourceKey{AccountID: 1, UserID: "uid", PickCode: "budget-file"}
		slot := playback.Slot{Mode: playback.ModeDirect, UA: "player"}
		other := playback.Slot{Mode: playback.ModeDirect, UA: "other"}
		manager.Record(source, other, time.Now().Add(playback.URLCacheTTL))
		started := time.Now()
		fallbackCalled := false
		result := resolve115URLMiss(t.Context(), manager, source, slot, "budget-cache", "", true,
			func(ctx context.Context) string {
				fallbackCalled = true
				return "fallback-url"
			},
			func(ctx context.Context) (string, error) {
				deadline, ok := ctx.Deadline()
				if !ok || deadline.Sub(started) != 10*time.Second {
					t.Fatalf("副本预算未在调用前生效：%v, %v", deadline.Sub(started), ok)
				}
				if !manager.HasOther(source, other, time.Now()) {
					t.Fatal("副本取链期间必须保留当前 pending 槽位")
				}
				<-ctx.Done()
				return "", ctx.Err()
			},
		)
		if fallbackCalled || result != "" || time.Since(started) != 10*time.Second {
			t.Fatalf("副本超时仍尝试普通取链：result=%q elapsed=%v", result, time.Since(started))
		}
		if manager.HasOther(source, other, time.Now()) || !manager.HasOther(source, slot, time.Now()) {
			t.Fatal("失败必须撤销本次预留并保留其他 UA 的槽位")
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
		got := resolve115URLMiss(t.Context(), manager, source, slot, key, "", false,
			func(context.Context) string { return value }, nil,
		)
		if got != value || !manager.HasOther(source, observer, time.Now()) {
			t.Fatal("新链接没有发布到缓存及槽位")
		}
		time.Sleep(4 * time.Minute)
		if cached := string(db.Cache.Get(key)); cached != value {
			t.Fatal("安全期限前缓存过早失效")
		}
		got = resolve115URLMiss(t.Context(), manager, source, slot, key, "", true,
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
			got := resolve115URLMiss(t.Context(), manager, source, slot, "expired-cache", "", copyEnabled,
				func(context.Context) string { originalCalls++; return expired },
				func(context.Context) (string, error) { return expired, nil },
			)
			wantOriginalCalls := 1
			if copyEnabled {
				wantOriginalCalls = 0
			}
			if got != "" || originalCalls != wantOriginalCalls || len(db.Cache.Get("expired-cache")) != 0 || manager.HasOther(source, other, time.Now()) {
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

func TestGet115UrlByPickCodeIsolationFailureDoesNotRedirect(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		t.Run(fmt.Sprintf("indexed=%v", indexed), func(t *testing.T) {
			setup115PlaybackCache(t)
			setupControllerTestDB(t, &models.Account{}, &models.SyncFile{})
			previousManager, previousSettings := v115Playback, models.SettingsGlobal
			v115Playback = playback.NewManager()
			models.SettingsGlobal = &models.Settings{MultiPlaybackEnabled: 1}
			t.Cleanup(func() { v115Playback, models.SettingsGlobal = previousManager, previousSettings })
			account := &models.Account{Name: "隔离保护", SourceType: models.SourceType115, UserId: "protected-user"}
			if err := db.Db.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			source := playback.SourceKey{AccountID: account.ID, UserID: account.UserId, PickCode: "protected-pick"}
			if indexed {
				file := models.SyncFile{AccountId: account.ID, SourceType: models.SourceType115, PickCode: source.PickCode}
				if err := db.Db.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
			}
			other := playback.Slot{Mode: playback.ModeDirect, UA: "existing-player"}
			current := playback.Slot{Mode: playback.ModeDirect, UA: "new-player"}
			v115Playback.Record(source, other, time.Now().Add(playback.URLCacheTTL))
			oldKey := v115URLCacheKey(source.PickCode, 1, 0, other.UA)
			newKey := v115URLCacheKey(source.PickCode, 1, 0, current.UA)
			const previousURL = "https://cdn.test/already-playing.mkv?k=keep"
			db.Cache.Set(oldKey, []byte(previousURL), 3000)
			request := func(ua string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(w)
				c.Request = httptest.NewRequest(http.MethodGet,
					"http://qms.test/115/newurl?pickcode=protected-pick&userid=protected-user&force=1", nil)
				c.Request.Header.Set("User-Agent", ua)
				Get115UrlByPickCode(c)
				return w
			}
			for range 2 {
				w := request(current.UA)
				var response APIResponse[any]
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if w.Code != http.StatusOK || response.Code != BadRequest || response.Message != "获取 115 下载链接失败" ||
					w.Header().Get("Location") != "" || len(db.Cache.Get(newKey)) != 0 {
					t.Fatalf("隔离失败响应不应包含直链：HTTP=%d response=%+v Location=%q", w.Code, response, w.Header().Get("Location"))
				}
				if v115Playback.HasOther(source, other, time.Now()) || !v115Playback.HasOther(source, current, time.Now()) {
					t.Fatal("控制器未释放本次预留或丢失原播放槽位")
				}
			}
			if w := request(other.UA); w.Code != http.StatusFound || w.Header().Get("Location") != previousURL ||
				string(db.Cache.Get(oldKey)) != previousURL {
				t.Fatalf("原播放器不能再命中原签名：HTTP=%d Location=%q", w.Code, w.Header().Get("Location"))
			}
		})
	}
}
