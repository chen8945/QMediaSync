package controllers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func setupPreloadProvider(t *testing.T) (*playback.Preloader, *preparedPlaybackPreload) {
	t.Helper()
	setup115PlaybackCache(t)
	p := playback.NewPreloader()
	p.Start(t.Context())
	p.Apply(true, models.GetPlaybackSettingsSnapshot().Generation)
	t.Cleanup(p.Stop)
	previous := v115Playback
	v115Playback = playback.NewManager()
	t.Cleanup(func() { v115Playback = previous })
	account := &models.Account{BaseModel: models.BaseModel{ID: 7}, SourceType: models.SourceType115, UserId: "fixture-user", Token: "fixture-token"}
	prepared := &preparedPlaybackPreload{
		account: account, source: playback.SourceKey{AccountID: account.ID, UserID: account.UserId, PickCode: "fixture-pick"},
		slot:     playback.Slot{Mode: playback.ModeDirect, UA: "fixture-player"},
		cacheKey: v115URLCacheKey("fixture-pick", 1, 0, "fixture-player"),
	}
	prepared.identity = playbackPreloadIdentity("115", prepared.cacheKey)
	prepared.sign115 = func(context.Context) (*v115open.DownloadUrlResult, error) {
		return &v115open.DownloadUrlResult{URL: fmt.Sprintf("https://a.115cdn.net/video?t=%d&k=do-not-log-secret", time.Now().Add(15*time.Minute).Unix())}, nil
	}
	return p, prepared
}

func submitPreloadProvider(t *testing.T, p *playback.Preloader, prepared *preparedPlaybackPreload, run func(context.Context, *playback.PreloadTask) playback.PreloadResult) {
	t.Helper()
	if !p.Submit(playback.PreloadCandidate{Key: t.Name(), Scope: t.Name(), Kind: playback.PreloadNext, Source: prepared.identity, Run: run}) {
		t.Fatal("未接纳候选")
	}
}

func TestPlaybackPreloadKeepsExistingCacheAndSlotContracts(t *testing.T) {
	for _, state := range []string{"miss", "cached", "busy", "other-slot", "failed", "invalid-url", "expired"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p, prepared := setupPreloadProvider(t)
				var calls atomic.Int32
				original := prepared.sign115
				prepared.sign115 = func(ctx context.Context) (*v115open.DownloadUrlResult, error) {
					calls.Add(1)
					switch state {
					case "failed":
						return nil, &v115open.OpenAPIError{Code: 70004}
					case "invalid-url":
						return &v115open.DownloadUrlResult{URL: "https://media.invalid/do-not-log-secret"}, nil
					case "expired":
						return &v115open.DownloadUrlResult{URL: "https://a.115cdn.net/video?t=1"}, nil
					}
					return original(ctx)
				}
				if state == "cached" {
					db.Cache.Set(prepared.cacheKey, []byte("existing-url"), 60)
				}
				if state == "busy" {
					if !keyLock.TryAcquire(prepared.cacheKey) {
						t.Fatal("无法占用原键锁")
					}
					defer keyLock.Unlock(prepared.cacheKey)
				}
				other := playback.Slot{Mode: playback.ModeDirect, UA: "another-player"}
				if state == "other-slot" {
					v115Playback.Record(prepared.source, other, time.Now().Add(time.Hour))
				}
				results := make(chan playback.PreloadResult, 1)
				submitPreloadProvider(t, p, prepared, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					result := warmPlaybackPreload(ctx, task, prepared)
					results <- result
					return result
				})
				synctest.Wait()
				result := <-results
				wantCalls := int32(1)
				if state == "cached" || state == "busy" || state == "other-slot" {
					wantCalls = 0
				}
				if calls.Load() != wantCalls {
					t.Fatalf("签名次数=%d，期望=%d", calls.Load(), wantCalls)
				}
				if state == "miss" {
					if result.Outcome != playback.PreloadPublished || len(db.Cache.Get(prepared.cacheKey)) == 0 || !v115Playback.HasOther(prepared.source, other, time.Now()) {
						t.Fatal("未按原键和安全期限发布槽位")
					}
				} else if state == "cached" {
					if result.Outcome != playback.PreloadCached || string(db.Cache.Get(prepared.cacheKey)) != "existing-url" {
						t.Fatal("缓存命中重写了现有值")
					}
				} else if len(db.Cache.Get(prepared.cacheKey)) != 0 || v115Playback.HasOther(prepared.source, other, time.Now()) {
					t.Fatal("跳过或失败后残留缓存或在途槽位")
				}
			})
		})
	}
}

func TestPlaybackPreloadOrdinaryTakeoverReusesActiveSignature(t *testing.T) {
	setupControllerTestDB(t, &models.Account{})
	synctest.Test(t, func(t *testing.T) {
		p, prepared := setupPreloadProvider(t)
		prepared.account.ID = 9000007
		prepared.source.AccountID = prepared.account.ID
		if err := db.Db.Create(prepared.account).Error; err != nil {
			t.Fatal(err)
		}
		var unexpectedCalls atomic.Int32
		prepared.account.Get115Client().SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			unexpectedCalls.Add(1)
			return proxy115TestResponse(r, http.StatusUnauthorized, `{"state":false,"code":40140124}`, nil), nil
		}))
		previous := playback.DefaultPreloader
		playback.DefaultPreloader = p
		defer func() { playback.DefaultPreloader = previous }()
		started, release := make(chan struct{}), make(chan struct{})
		var calls atomic.Int32
		original := prepared.sign115
		prepared.sign115 = func(ctx context.Context) (*v115open.DownloadUrlResult, error) {
			calls.Add(1)
			close(started)
			<-release
			return original(ctx)
		}
		submitPreloadProvider(t, p, prepared, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			return warmPlaybackPreload(ctx, task, prepared)
		})
		<-started
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodGet, "/115/newurl?pickcode=fixture-pick&userid=fixture-user&force=1", nil)
		c.Request.Header.Set("User-Agent", prepared.slot.UA)
		done := make(chan struct{})
		go func() { Get115UrlByPickCode(c); close(done) }()
		synctest.Wait()
		close(release)
		<-done
		if response.Code != http.StatusFound || calls.Load() != 1 || unexpectedCalls.Load() != 0 || !strings.Contains(response.Header().Get("Location"), "115cdn.net") {
			t.Fatalf("普通播放未复用在途签名：HTTP=%d，sign=%d", response.Code, calls.Load())
		}
	})
}

func TestPlaybackPreloadCancellationDoesNotPublishLateURL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, prepared := setupPreloadProvider(t)
		started := make(chan struct{})
		prepared.sign115 = func(ctx context.Context) (*v115open.DownloadUrlResult, error) {
			close(started)
			<-ctx.Done()
			return &v115open.DownloadUrlResult{URL: "https://a.115cdn.net/late"}, nil
		}
		submitPreloadProvider(t, p, prepared, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			return warmPlaybackPreload(ctx, task, prepared)
		})
		<-started
		p.Apply(false, models.GetPlaybackSettingsSnapshot().Generation+1)
		synctest.Wait()
		if len(db.Cache.Get(prepared.cacheKey)) != 0 || v115Playback.HasOther(prepared.source, playback.Slot{UA: "other"}, time.Now()) {
			t.Fatal("关闭后晚到链接仍发布或残留槽位")
		}
		if !keyLock.TryAcquire(prepared.cacheKey) {
			t.Fatal("取消后未释放原键锁")
		}
		keyLock.Unlock(prepared.cacheKey)
	})
}

func TestPlaybackPreloadConstructorRejectsUnknownTargets(t *testing.T) {
	for _, raw := range []string{
		"https://third.invalid/video.mp4", "/115/url/", "/Items/123", "/115/newurl?pickcode=", "/115/newurl?pickcode=a&pickcode=b",
		"/115/newurl?pickcode=a&force=2", "/115/newurl?pickcode=a&force=x", "/115/newurl?pickcode=a&userid=a&userid=b", "https://secret@qms.test/115/newurl?pickcode=a",
	} {
		if _, _, ok := NewPlaybackPreload(PlaybackPreloadSource{URL: raw, UA: "player"}); ok {
			t.Errorf("接受了无效目标 %q", raw)
		}
		if account := PlaybackPreloadAccount(raw); account != (playback.PreloadAccount{}) {
			t.Errorf("无效目标产生了账号提示：%+v", account)
		}
	}
	for _, raw := range []string{"/115/newurl?pickcode=a", "/115/url/video.mkv?pickcode=a&userid=user", "/baidupan/url/video.mkv?pickcode=123&userid=user"} {
		if _, run, ok := NewPlaybackPreload(PlaybackPreloadSource{URL: raw, UA: "player", ForceDirect: true}); !ok || run == nil {
			t.Errorf("拒绝已有稳定入口 %q", raw)
		}
	}
}

func TestPlaybackPreloadAccountHintDoesNotReadDatabase(t *testing.T) {
	setupControllerTestDB(t, &models.Account{})
	var queries atomic.Int32
	if err := db.Db.Callback().Query().Before("gorm:query").Register("preload-account-hint", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		raw      string
		provider string
		userID   string
	}{
		{"115-encoded-uid", "/115/newurl?pickcode=a&userid=User%2B001", "115", "User+001"},
		{"115-filename-route", "https://qms.invalid/115/url/a.mkv?pickcode=a&userid=USER-2&force=1", "115", "USER-2"},
		{"baidu-separate-provider", "/baidupan/url/a.mkv?pickcode=123&userid=User%2B001", string(models.SourceTypeBaiduPan), "User+001"},
		{"unknown-uid", "/115/newurl?pickcode=a", "", ""},
		{"blank-uid", "/115/newurl?pickcode=a&userid=%20", "", ""},
		{"overlong-uid", "/115/newurl?pickcode=a&userid=" + strings.Repeat("a", 257), "", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			account := PlaybackPreloadAccount(test.raw)
			if account.Provider != test.provider || account.UserID != test.userID || account.AccountID != 0 {
				t.Errorf("账号提示=%+v，期望 provider=%q userid=%q", account, test.provider, test.userID)
			}
		})
	}
	if queries.Load() != 0 {
		t.Fatalf("账号提示执行了 %d 次数据库查询", queries.Load())
	}
}

func TestPlaybackPreloadFactoryBaiduCachesForOrdinaryPlayback(t *testing.T) {
	router, target := setupBaiduPlaybackTest(t)
	previousTransport := http.DefaultTransport
	var calls, queries atomic.Int32
	http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "pan.baidu.com" || r.URL.Query().Get("method") != "filemetas" {
			t.Fatal("后台访问了媒体或非文件元数据接口")
		}
		fileID := strings.Trim(r.URL.Query().Get("fsids"), "[]")
		return proxy115TestResponse(r, 200, fmt.Sprintf(`{"errno":0,"list":[{"fs_id":%s,"dlink":"https://d.pcs.baidu.com/file?key=private"}]}`, fileID), nil), nil
	})
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	if err := db.Db.Callback().Query().Before("gorm:query").Register("preload-query-count", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		p := playback.NewPreloader()
		p.Start(t.Context())
		p.Apply(true, models.GetPlaybackSettingsSnapshot().Generation)
		defer p.Stop()
		identity, run, ok := NewPlaybackPreload(PlaybackPreloadSource{URL: target, UA: "Baidu Player"})
		if !ok || !p.Submit(playback.PreloadCandidate{Key: t.Name(), Scope: t.Name(), Source: identity, Kind: playback.PreloadNext, Run: run}) {
			t.Fatal("未接纳已授权百度候选")
		}
		synctest.Wait()
		if calls.Load() != 1 || queries.Load() != 1 || len(db.Cache.Get(baiduURLCacheKey("123", "Baidu Player"))) == 0 || len(db.Cache.Get(baiduURLCacheKey("123", "other"))) != 0 {
			t.Fatalf("取链/账号读取/UA 缓存不符：calls=%d queries=%d", calls.Load(), queries.Load())
		}
		response := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("User-Agent", "Baidu Player")
		router.ServeHTTP(response, request)
		if response.Code != http.StatusFound || !strings.HasPrefix(response.Header().Get("Location"), "/proxy-115?baidupan=1&") || calls.Load() != 1 || queries.Load() != 2 {
			t.Fatalf("普通播放未复用百度缓存或改变代理契约：HTTP=%d calls=%d queries=%d", response.Code, calls.Load(), queries.Load())
		}
		identity, run, ok = NewPlaybackPreload(PlaybackPreloadSource{URL: strings.Replace(target, "pickcode=123", "pickcode=124", 1), UA: "Baidu Player"})
		if !ok || !p.Submit(playback.PreloadCandidate{Key: t.Name() + "-second", Scope: t.Name(), Source: identity, Kind: playback.PreloadNext, Run: run}) {
			t.Fatal("未接纳同账号下一候选")
		}
		synctest.Wait()
		if calls.Load() != 1 || queries.Load() != 3 {
			t.Fatalf("账号间隔内提前取链或重复查库：calls=%d queries=%d", calls.Load(), queries.Load())
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if calls.Load() != 2 || queries.Load() != 3 || len(db.Cache.Get(baiduURLCacheKey("124", "Baidu Player"))) == 0 {
			t.Fatalf("配额恢复未复用已核验账号：calls=%d queries=%d", calls.Load(), queries.Load())
		}
	})
}

func TestPlaybackPreloadFactoryCredentialChangeDuringLookupCancels(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("without-userid=%v", legacy), func(t *testing.T) {
			setupControllerTestDB(t, &models.Account{}, &models.SyncFile{})
			setup115PlaybackCache(t)
			account := &models.Account{SourceType: models.SourceType115, UserId: "lookup-user", Token: "private"}
			if err := db.Db.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			if legacy {
				if err := db.Db.Create(&models.SyncFile{SourceType: models.SourceType115, AccountId: account.ID, PickCode: "lookup-pick"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			synctest.Test(t, func(t *testing.T) {
				p := playback.NewPreloader()
				p.Start(t.Context())
				p.Apply(true, models.GetPlaybackSettingsSnapshot().Generation)
				defer p.Stop()
				var lookups atomic.Int32
				if err := db.Db.Callback().Query().After("gorm:query").Register("preload-credential-race", func(query *gorm.DB) {
					if query.Statement.Table == "account" {
						lookups.Add(1)
						p.CancelAccount(playback.PreloadAccount{Provider: "115", AccountID: account.ID, UserID: account.UserId})
					}
				}); err != nil {
					t.Fatal(err)
				}
				raw := "/115/newurl?pickcode=lookup-pick"
				if !legacy {
					raw += "&userid=lookup-user"
				}
				identity, run, ok := NewPlaybackPreload(PlaybackPreloadSource{URL: raw, UA: "fixture-player"})
				cacheKey := v115URLCacheKey("lookup-pick", 0, models.GetPlaybackSettingsSnapshot().LocalProxy, "fixture-player")
				db.Cache.Set(cacheKey, []byte("existing-url"), 60)
				results := make(chan playback.PreloadResult, 1)
				if !ok || !p.Submit(playback.PreloadCandidate{Key: t.Name(), Scope: t.Name(), Source: identity, Kind: playback.PreloadNext, Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					result := run(ctx, task)
					results <- result
					return result
				}}) {
					t.Fatal("候选未接纳")
				}
				synctest.Wait()
				if result := <-results; lookups.Load() != 1 || result.Outcome != playback.PreloadSkipped || string(db.Cache.Get(cacheKey)) != "existing-url" {
					t.Fatal("查询期间授权变更后仍发布")
				}
			})
		})
	}
}

func TestPlaybackPreloadLogsAreSafeAndOutsideKeyLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p, prepared := setupPreloadProvider(t)
		previousLevel := helpers.ConfiguredLogLevel()
		helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
		defer helpers.SetGlobalLogLevel(previousLevel)
		var output bytes.Buffer
		helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.MultiWriter(&output, preloadLockCheckingWriter{t: t, key: prepared.cacheKey}), "", 0)}
		submitPreloadProvider(t, p, prepared, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			return warmPlaybackPreload(ctx, task, prepared)
		})
		synctest.Wait()
		if !strings.Contains(output.String(), "预加载完成") || strings.Contains(output.String(), "do-not-log-secret") || strings.Contains(output.String(), "115cdn.net") || strings.Contains(output.String(), "fixture-token") {
			t.Fatalf("后台日志缺失或泄漏：%s", output.String())
		}
	})
}

func TestPlaybackPreloadLogReasons(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		reason string
		status int
		code   int
	}{
		{name: "canceled", err: context.Canceled, reason: "canceled"},
		{name: "deadline", err: context.DeadlineExceeded, reason: "timeout"},
		{name: "dial", err: &net.OpError{Op: "dial", Err: errors.New("https://secret.invalid/raw-body")}, reason: "connection_failed"},
		{name: "timeout", err: &net.OpError{Op: "read", Err: context.DeadlineExceeded}, reason: "timeout"},
		{name: "network_timeout", err: &net.OpError{Op: "read", Err: &net.DNSError{IsTimeout: true}}, reason: "timeout_provider"},
		{name: "provider", err: &v115open.OpenAPIError{HTTPStatus: 503, Code: 990001}, reason: "provider_failed", status: 503, code: 990001},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := playbackPreloadError(tc.err)
			if result.Reason != tc.reason || result.StatusSource != "网盘" || result.HTTPStatus != tc.status || result.BusinessCode != tc.code {
				t.Fatalf("安全原因或状态来源丢失：%+v", result)
			}
		})
	}
}

type preloadLockCheckingWriter struct {
	t   *testing.T
	key string
}

func (w preloadLockCheckingWriter) Write(data []byte) (int, error) {
	if !keyLock.TryAcquire(w.key) {
		w.t.Error("后台日志仍持有同键锁")
	} else {
		keyLock.Unlock(w.key)
	}
	return len(data), nil
}

func TestPlaybackPreloadErrorClassificationIsNarrow(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want playback.PreloadOutcome
	}{
		{&v115open.OpenAPIError{HTTPStatus: 503}, playback.PreloadTransient},
		{&net.OpError{Op: "dial", Err: errors.New("secret")}, playback.PreloadTransient},
		{&net.OpError{Op: "dial", Err: &net.DNSError{IsNotFound: true}}, playback.PreloadFailed},
		{&v115open.OpenAPIError{HTTPStatus: 503, Code: 990001}, playback.PreloadFailed},
		{&v115open.OpenAPIError{Code: 70004}, playback.PreloadFailed},
		{&v115open.OpenAPIError{Code: 31004}, playback.PreloadFailed},
		{v115open.ErrDownloadURLNotReady, playback.PreloadFailed},
		{&v115open.OpenAPIError{HTTPStatus: 429}, playback.PreloadRisk},
		{&v115open.OpenAPIError{Code: v115open.ACCESS_AUTH_INVALID}, playback.PreloadUnauthorized},
		{context.Canceled, playback.PreloadSkipped}, {context.DeadlineExceeded, playback.PreloadSkipped},
	} {
		if got := playbackPreloadError(tc.err).Outcome; got != tc.want {
			t.Errorf("分类=%v，期望=%v", got, tc.want)
		}
	}
}
