package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
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
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	v115open "qmediasync/internal/v115/openapi"

	"github.com/coocood/freecache"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

func setup115PlaybackSettings(t *testing.T, settings *models.Settings) {
	t.Helper()
	previous, previousSnapshot := models.SettingsGlobal, models.GetPlaybackSettingsSnapshot()
	previousThreads := models.GetThreadSettingsSnapshot()
	validityEnabled := settings.URLValidityCheckEnabled
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatal(err)
	}
	// GORM 的默认值会把显式 0 改成 1，关闭校验的场景需显式落库。
	if err := db.Db.Model(settings).Update("url_validity_check_enabled", validityEnabled).Error; err != nil {
		t.Fatal(err)
	}
	models.SettingsGlobal = &models.Settings{}
	models.LoadSettings()
	t.Cleanup(func() {
		fields := previousThreads.ToMap()
		fields["local_proxy"], fields["multi_playback_enabled"] = previousSnapshot.LocalProxy, previousSnapshot.MultiPlaybackEnabled
		fields["strm_preload_enabled"], fields["strm_base_url"] = previousSnapshot.PreloadEnabled, previousSnapshot.StrmBaseURL
		if err := db.Db.Model(settings).Updates(fields).Error; err != nil {
			t.Error(err)
		} else {
			models.LoadSettings()
		}
		models.SettingsGlobal = previous
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
			original := func(context.Context) (string, error) { originalCalls++; return "https://original.invalid/video", nil }
			copyURL := func(context.Context) (string, error) { copyCalls++; return "https://copy.invalid/video", nil }
			result, err := resolve115URLMiss(context.Background(), manager, source, slot, key, "", tt.enabled, original, copyURL)
			if err != nil {
				t.Fatal(err)
			}
			if (copyCalls == 1) != tt.wantCopy || originalCalls+copyCalls != 1 {
				t.Fatalf("original=%d copy=%d wantCopy=%v", originalCalls, copyCalls, tt.wantCopy)
			}
			if cached := string(db.Cache.Get(key)); cached != result {
				t.Fatalf("URL 未写回原始键：cached=%q result=%q", cached, result)
			}
			// 关闭开关后仍复用已经生成的缓存，不重新取链或清除副本 URL。
			again, err := resolve115URLMiss(context.Background(), manager, source, slot, key, "", false, original, copyURL)
			if err != nil {
				t.Fatal(err)
			}
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
			setupControllerTestDB(t, &models.Account{}, &models.Settings{})
			setup115PlaybackSettings(t, &models.Settings{})
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
			fetch := func(context.Context) (string, error) {
				fetches++
				return fmt.Sprintf("https://cdn.test/%%E5%%BD%%B1%%E7%%89%%87.mkv?generation=%d&k=a%%2Bb", fetches), nil
			}
			resolve := func() string {
				value, err := resolve115URLMiss(t.Context(), manager, source, slot, key, origin, tt.copy, fetch,
					func(ctx context.Context) (string, error) {
						return fetch(ctx)
					},
				)
				if err != nil {
					t.Fatal(err)
				}
				return value
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
						value, err := resolve115URLMiss(context.Background(), manager, source, slot, key, "", true,
							func(context.Context) (string, error) {
								if originalCalls.Add(1) == 1 {
									close(originalStarted)
								}
								<-releaseOriginal
								return "original-url", nil
							},
							func(context.Context) (string, error) {
								return fmt.Sprintf("copy-url-%d", copyCalls.Add(1)), nil
							},
						)
						if err != nil {
							t.Error(err)
						}
						results <- value
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
	result, err := resolve115URLMiss(context.Background(), manager, source, slot, key, "", true,
		func(context.Context) (string, error) { originalCalls++; return "fallback-url", nil },
		func(context.Context) (string, error) { copyCalls++; return "", errors.New("copy failed") },
	)
	if err == nil || copyCalls != 1 || originalCalls != 0 || result != "" || len(db.Cache.Get(key)) != 0 ||
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
			value, err := resolve115URLMiss(t.Context(), manager, source, slot, key, "", true,
				func(context.Context) (string, error) { originalCalls++; return "unexpected-original", nil },
				func(context.Context) (string, error) { copyCalls++; return tt.value, tt.err },
			)
			if err == nil || tt.err != nil && !errors.Is(err, tt.err) || value != "" || originalCalls != 0 || copyCalls != 1 || len(db.Cache.Get(key)) != 0 ||
				string(db.Cache.Get(otherKey)) != previousURL {
				t.Fatalf("失败不应降级或改写缓存：value=%q original=%d copy=%d", value, originalCalls, copyCalls)
			}
			if manager.HasOther(source, other, time.Now()) || !manager.HasOther(source, slot, time.Now()) {
				t.Fatal("本次预留应释放，其他 UA 的有效或在途槽位必须保留")
			}
			if logs.Len() != 0 {
				t.Fatalf("提前记录未脱敏原因：logs=%s", logs.String())
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
	result, err := resolve115URLMiss(ctx, manager, source, slot, "canceled-copy-cache", "", true,
		func(context.Context) (string, error) { called = true; return "unexpected", nil },
		func(ctx context.Context) (string, error) { cancel(); return "", ctx.Err() },
	)
	if !errors.Is(err, context.Canceled) || called || result != "" || len(db.Cache.Get("canceled-copy-cache")) != 0 {
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
	result, err := resolve115URLMiss(ctx, manager, source, playback.Slot{Mode: playback.ModeDirect, UA: "waiting"}, "waiting-cache", "", true,
		func(context.Context) (string, error) { called = true; return "unexpected", nil },
		func(context.Context) (string, error) { called = true; return "unexpected", nil },
	)
	if !errors.Is(err, context.Canceled) || called || result != "" || len(db.Cache.Get("waiting-cache")) != 0 ||
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
		result, err := resolve115URLMiss(t.Context(), manager, source, slot, "budget-cache", "", true,
			func(ctx context.Context) (string, error) {
				fallbackCalled = true
				return "fallback-url", nil
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
		if !errors.Is(err, context.DeadlineExceeded) || fallbackCalled || result != "" || time.Since(started) != 10*time.Second {
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
		got, err := resolve115URLMiss(t.Context(), manager, source, slot, key, "", false,
			func(context.Context) (string, error) { return value, nil }, nil,
		)
		if err != nil || got != value || !manager.HasOther(source, observer, time.Now()) {
			t.Fatal("新链接没有发布到缓存及槽位")
		}
		time.Sleep(4 * time.Minute)
		if cached := string(db.Cache.Get(key)); cached != value {
			t.Fatal("安全期限前缓存过早失效")
		}
		got, err = resolve115URLMiss(t.Context(), manager, source, slot, key, "", true,
			func(context.Context) (string, error) { t.Fatal("命中缓存不应重取"); return "", nil }, nil,
		)
		if err != nil || got != value {
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
			got, err := resolve115URLMiss(t.Context(), manager, source, slot, "expired-cache", "", copyEnabled,
				func(context.Context) (string, error) { originalCalls++; return expired, nil },
				func(context.Context) (string, error) { return expired, nil },
			)
			wantOriginalCalls := 1
			if copyEnabled {
				wantOriginalCalls = 0
			}
			if err == nil || got != "" || originalCalls != wantOriginalCalls || len(db.Cache.Get("expired-cache")) != 0 || manager.HasOther(source, other, time.Now()) {
				t.Fatal("已到安全期限的 URL 不能返回或成为永久缓存，失败必须撤销 pending")
			}
		})
	}
}

func TestCopy115URLRequiresAccountScoped115FileIdentity(t *testing.T) {
	setup115PlaybackCache(t)
	setupControllerTestDB(t, &models.SyncFile{})
	account := &models.Account{ID: 1, UserId: "uid-1"}
	for _, file := range []models.SyncFile{
		{AccountId: 2, SourceType: models.SourceType115, PickCode: "same-pick", FileId: "other-account"},
		{AccountId: 1, SourceType: models.SourceTypeBaiduPan, PickCode: "same-pick", FileId: "other-provider"},
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
	for _, tt := range []struct {
		name                            string
		indexed, sdkFailure, sdkTimeout bool
		readError                       error
		status                          int
		code                            string
	}{
		{name: "missing-local-file", status: http.StatusNotFound, code: "RESOURCE_NOT_FOUND"},
		{name: "missing-remote-identity", indexed: true, status: http.StatusBadGateway, code: "UPSTREAM_ERROR"},
		{name: "local-database-error", indexed: true, readError: errors.New("private local file read failure"), status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
		{name: "local-read-deadline", indexed: true, readError: context.DeadlineExceeded, status: http.StatusGatewayTimeout, code: "UPSTREAM_TIMEOUT"},
		{name: "local-read-canceled", indexed: true, readError: context.Canceled, status: http.StatusServiceUnavailable, code: "REQUEST_CANCELED"},
		{name: "local-read-timeout", indexed: true, readError: &net.DNSError{Err: "private file read timeout", IsTimeout: true}, status: http.StatusInternalServerError, code: "INTERNAL_ERROR"},
		{name: "sdk-error", indexed: true, sdkFailure: true, status: http.StatusBadGateway, code: "UPSTREAM_ERROR"},
		{name: "sdk-timeout", indexed: true, sdkFailure: true, sdkTimeout: true, status: http.StatusGatewayTimeout, code: "UPSTREAM_TIMEOUT"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup115PlaybackCache(t)
			conn := setupControllerTestDB(t, &models.Account{}, &models.SyncFile{}, &models.Settings{})
			setup115PlaybackSettings(t, &models.Settings{MultiPlaybackEnabled: 1})
			previousManager, previousLogger := v115Playback, helpers.V115Log
			v115Playback = playback.NewManager()
			helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			t.Cleanup(func() { v115Playback = previousManager; helpers.V115Log = previousLogger })
			executor := v115open.GetGlobalExecutor()
			executor.Start()
			t.Cleanup(executor.Stop)
			var logs bytes.Buffer
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&logs, "", 0)}
			account := &models.Account{Name: "隔离保护", SourceType: models.SourceType115, UserId: "protected-user", Token: "private-copy-token", RefreshToken: "private-copy-refresh"}
			if err := conn.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			source := playback.SourceKey{AccountID: account.ID, UserID: account.UserId, PickCode: "protected-pick"}
			if tt.indexed {
				file := models.SyncFile{AccountId: account.ID, SourceType: models.SourceType115, PickCode: source.PickCode}
				if tt.sdkFailure {
					file.FileId = "original-file"
				}
				if err := conn.Create(&file).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tt.readError != nil {
				if err := conn.Callback().Query().Before("gorm:query").Register("fixture_copy_local_read_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						tx.AddError(tt.readError)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			apiCalls, originalCalls := 0, 0
			transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
				apiCalls++
				if r.URL.Path != "/open/folder/get_info" {
					originalCalls++
					t.Errorf("unexpected upstream request: %s", r.URL.Path)
				}
				if tt.sdkTimeout {
					return nil, &net.DNSError{Err: "private SDK timeout", IsTimeout: true}
				}
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"state":false,"code":90001,"message":"private SDK failure"}`))}, nil
			})
			originalClient := account.Get115Client()
			originalClient.SetTransport(transport)
			t.Cleanup(func() { originalClient.SetTransport(http.DefaultTransport) })
			copyClient := v115open.NewPlaybackClient(account.ID, account.AppId, account.Token, account.RefreshToken)
			copyClient.SetTransport(transport)
			t.Cleanup(func() { copyClient.SetTransport(http.DefaultTransport) })
			other := playback.Slot{Mode: playback.ModeDirect, UA: "existing-player"}
			current := playback.Slot{Mode: playback.ModeDirect, UA: "new-player"}
			v115Playback.Record(source, other, time.Now().Add(playback.URLCacheTTL))
			oldKey := v115URLCacheKey(source.PickCode, 1, 0, other.UA)
			newKey := v115URLCacheKey(source.PickCode, 1, 0, current.UA)
			const previousURL = "https://cdn.test/already-playing.mkv?k=keep"
			db.Cache.Set(oldKey, []byte(previousURL), 3000)
			router := gin.New()
			router.GET("/115/newurl", Get115UrlByPickCode)
			request := func(ua string) *httptest.ResponseRecorder {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/115/newurl?pickcode=protected-pick&userid=protected-user&force=1", nil)
				req.Header.Set("User-Agent", ua)
				router.ServeHTTP(w, req)
				return w
			}
			for range 2 {
				w := request(current.UA)
				assertScrapeFailure(t, w, tt.status, tt.code)
				if w.Header().Get("Location") != "" || len(db.Cache.Get(newKey)) != 0 {
					t.Fatalf("isolation failure published URL: %s", w.Body)
				}
				var response httpapi.APIResponse[any]
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(logs.String(), response.Instance) || strings.Contains(logs.String(), account.Token) || strings.Contains(logs.String(), account.RefreshToken) {
					t.Fatalf("unsafe or uncorrelated cause: %s", &logs)
				}
				if tt.readError != nil && !strings.Contains(logs.String(), tt.readError.Error()) {
					t.Fatalf("local cause lost: %s", &logs)
				}
				if v115Playback.HasOther(source, other, time.Now()) || !v115Playback.HasOther(source, current, time.Now()) {
					t.Fatal("controller lost existing slot or retained new reservation")
				}
			}
			wantCalls := 0
			if tt.sdkFailure {
				wantCalls = 2
				sdkCause := "90001"
				if tt.sdkTimeout {
					sdkCause = "private SDK timeout"
				}
				if !strings.Contains(logs.String(), sdkCause) {
					t.Fatalf("SDK cause lost: %s", &logs)
				}
			}
			if apiCalls != wantCalls || originalCalls != 0 {
				t.Fatalf("local failure queried upstream or copy fell back: api=%d original=%d", apiCalls, originalCalls)
			}
			if w := request(other.UA); w.Code != http.StatusFound || w.Header().Get("Location") != previousURL || string(db.Cache.Get(oldKey)) != previousURL {
				t.Fatalf("existing URL lost: HTTP=%d Location=%q", w.Code, w.Header().Get("Location"))
			}
		})
	}
}
