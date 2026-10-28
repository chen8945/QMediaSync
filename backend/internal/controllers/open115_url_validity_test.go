package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/fnosproxy"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"github.com/coocood/freecache"
	"github.com/gin-gonic/gin"
)

func TestCheckURLValidityUsesHEADUserAgentAndTotalTimeout(t *testing.T) {
	tests := []struct {
		name       string
		userAgent  string
		statusCode int
		location   string
		delay      time.Duration
		timeout    time.Duration
		wantValid  bool
	}{
		{
			name:       "HEAD 返回 2xx 且 UA 匹配时有效",
			userAgent:  "qms-test",
			statusCode: http.StatusNoContent,
			timeout:    time.Second,
			wantValid:  true,
		},
		{
			name:       "非 2xx 状态无效",
			userAgent:  "qms-test",
			statusCode: http.StatusForbidden,
			timeout:    time.Second,
			wantValid:  false,
		},
		{
			name:       "超过总超时无效",
			userAgent:  "qms-test",
			statusCode: http.StatusNoContent,
			delay:      50 * time.Millisecond,
			timeout:    10 * time.Millisecond,
			wantValid:  false,
		},
		{
			name:       "非法绝对 Location 不回显地址",
			userAgent:  "qms-test",
			statusCode: http.StatusFound,
			location:   "https://private-user:private-pass@cdn.invalid/%zz?k=signature",
			timeout:    time.Second,
		},
		{
			name:       "非法协议相对 Location 不回显地址",
			userAgent:  "qms-test",
			statusCode: http.StatusFound,
			location:   "//private-user:private-pass@cdn.invalid/%zz?k=signature",
			timeout:    time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			previousLogger, previousLevel := helpers.AppLogger, helpers.ConfiguredLogLevel()
			helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
			helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
			t.Cleanup(func() {
				helpers.AppLogger = previousLogger
				helpers.SetGlobalLogLevel(previousLevel)
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead {
					t.Fatalf("请求方法 = %s，期望 HEAD", r.Method)
				}
				if got := r.UserAgent(); got != tt.userAgent {
					t.Fatalf("User-Agent = %q，期望 %q", got, tt.userAgent)
				}
				if tt.delay > 0 {
					time.Sleep(tt.delay)
				}
				if tt.location != "" {
					w.Header().Set("Location", tt.location)
				}
				w.WriteHeader(tt.statusCode)
			}))
			defer server.Close()

			target := server.URL + "/%E5%BD%B1%E7%89%87.mkv?k=signature"
			if got := checkURLValidity(t.Context(), target, tt.userAgent, tt.timeout); got != tt.wantValid {
				t.Fatalf("checkURLValidity() = %v，期望 %v", got, tt.wantValid)
			}
			for _, want := range []string{"HEAD", `文件="影片.mkv"`, `UA="` + tt.userAgent + `"`} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("HEAD 日志缺少 %q：%s", want, output.String())
				}
			}
			for _, unwanted := range []string{server.URL, "signature", "private-user", "private-pass"} {
				if strings.Contains(output.String(), unwanted) {
					t.Errorf("HEAD 检查不应输出地址或凭据 %q：%s", unwanted, output.String())
				}
			}
		})
	}
}

func TestV115URLValidityCheckTimeoutCapsUnderCacheLockWait(t *testing.T) {
	tests := []struct {
		name       string
		configured time.Duration
		expected   time.Duration
	}{
		{
			name:       "配置未超过锁等待窗口时保持原值",
			configured: 3 * time.Second,
			expected:   3 * time.Second,
		},
		{
			name:       "配置超过锁等待窗口时裁剪到最大校验等待",
			configured: 30 * time.Second,
			expected:   v115URLValidityCheckMaxWait,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := v115URLValidityCheckTimeout(tt.configured); got != tt.expected {
				t.Fatalf("v115URLValidityCheckTimeout(%s) = %s，期望 %s", tt.configured, got, tt.expected)
			}
		})
	}
}

func TestGet115UrlByPickCodeSkipsHEADWhenURLValidityCheckDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupControllerTestDB(t, &models.Account{}, &models.Settings{})

	originalCache := db.Cache
	db.Cache = db.CacheGlobal{
		CacheInstance: freecache.NewCache(1024 * 1024),
		CacheSize:     1024 * 1024,
	}
	setup115PlaybackSettings(t, &models.Settings{
		URLValidityCheckEnabled:        0,
		URLValidityCheckTimeoutSeconds: 1,
	})
	t.Cleanup(func() {
		db.Cache = originalCache
	})

	account := &models.Account{
		Name:       "115 测试账号",
		SourceType: models.SourceType115,
		UserId:     "user-115-skip-head",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败：%v", err)
	}

	var headRequests atomic.Int64
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headRequests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer targetServer.Close()

	pickCode := "pick-skip-head"
	userAgent := "qms-test-agent"
	cachedURL := targetServer.URL + "/download"
	cacheKey := v115URLCacheKey(pickCode, 1, models.SettingsGlobal.LocalProxy, userAgent)
	db.Cache.Set(cacheKey, []byte(cachedURL), 3000)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/115/newurl?pickcode="+pickCode+"&userid="+account.UserId+"&force=1", nil)
	req.Header.Set("User-Agent", userAgent)
	c.Request = req

	Get115UrlByPickCode(c)

	if got := headRequests.Load(); got != 0 {
		t.Fatalf("关闭 URL 有效性检查后 HEAD 请求数 = %d，期望 0", got)
	}
	if w.Code != http.StatusFound {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusFound)
	}
	if got := w.Header().Get("Location"); got != cachedURL {
		t.Fatalf("重定向地址 = %q，期望缓存地址 %q", got, cachedURL)
	}
}

func TestGet115UrlByPickCode获取缓存锁超时时返回错误响应(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupControllerTestDB(t, &models.Account{}, &models.Settings{})

	originalCache := db.Cache
	originalLockWait := v115URLCacheLockWait
	db.Cache = db.CacheGlobal{
		CacheInstance: freecache.NewCache(1024 * 1024),
		CacheSize:     1024 * 1024,
	}
	setup115PlaybackSettings(t, &models.Settings{
		URLValidityCheckEnabled:        1,
		URLValidityCheckTimeoutSeconds: 3,
	})
	v115URLCacheLockWait = 20 * time.Millisecond
	t.Cleanup(func() {
		db.Cache = originalCache
		v115URLCacheLockWait = originalLockWait
	})

	account := &models.Account{
		Name:       "115 锁超时测试账号",
		SourceType: models.SourceType115,
		UserId:     "user-115-lock-timeout",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败：%v", err)
	}

	pickCode := "pick-lock-timeout"
	userAgent := "qms-lock-timeout-agent"
	cacheKey := v115URLCacheKey(pickCode, 1, models.SettingsGlobal.LocalProxy, userAgent)
	if !keyLock.LockWithTimeout(cacheKey, time.Millisecond) {
		t.Fatal("预先获取缓存锁失败")
	}
	defer keyLock.Unlock(cacheKey)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(http.MethodGet, "/115/newurl?pickcode="+pickCode+"&userid="+account.UserId+"&force=1", nil)
	req.Header.Set("User-Agent", userAgent)
	c.Request = req

	Get115UrlByPickCode(c)

	if w.Code != http.StatusOK {
		t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusOK)
	}
	if body := w.Body.String(); !strings.Contains(body, "获取 115 下载链接超时") {
		t.Fatalf("响应体 = %q，期望包含锁超时错误", body)
	}
}

func TestGet115UrlByPickCode关闭校验时按播放模式隔离缓存(t *testing.T) {
	gin.SetMode(gin.TestMode)
	setupControllerTestDB(t, &models.Account{}, &models.Settings{})

	originalCache := db.Cache
	db.Cache = db.CacheGlobal{
		CacheInstance: freecache.NewCache(1024 * 1024),
		CacheSize:     1024 * 1024,
	}
	setup115PlaybackSettings(t, &models.Settings{
		LocalProxy:                     1,
		URLValidityCheckEnabled:        0,
		URLValidityCheckTimeoutSeconds: 1,
	})
	t.Cleanup(func() {
		db.Cache = originalCache
	})

	account := &models.Account{
		Name:       "115 缓存隔离测试账号",
		SourceType: models.SourceType115,
		UserId:     "user-115-cache-mode",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败：%v", err)
	}

	pickCode := "pick-cache-mode"
	userAgent := "qms-client-agent"
	legacyCachedURL := "https://legacy.example.invalid/proxy-bound.mp4"
	directCachedURL := "https://direct.example.invalid/video.mp4"
	proxyCachedURL := "https://proxy.example.invalid/video.mp4"

	db.Cache.Set("115url:"+pickCode+", ua="+userAgent, []byte(legacyCachedURL), 3000)
	db.Cache.Set("115url:"+pickCode+", mode=direct, ua="+userAgent, []byte(directCachedURL), 3000)
	db.Cache.Set("115url:"+pickCode+", mode=proxy, ua="+v115open.DEFAULTUA, []byte(proxyCachedURL), 3000)

	tests := []struct {
		name             string
		force            string
		expectedLocation string
	}{
		{
			name:             "直连请求使用 direct 缓存",
			force:            "1",
			expectedLocation: directCachedURL,
		},
		{
			name:             "代理请求使用 proxy 缓存",
			force:            "0",
			expectedLocation: "/proxy-115?url=" + url.QueryEscape(proxyCachedURL),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			req := httptest.NewRequest(http.MethodGet, "/115/newurl?pickcode="+pickCode+"&userid="+account.UserId+"&force="+tt.force, nil)
			req.Header.Set("User-Agent", userAgent)
			c.Request = req

			Get115UrlByPickCode(c)

			if w.Code != http.StatusFound {
				t.Fatalf("HTTP 状态码 = %d，期望 %d", w.Code, http.StatusFound)
			}
			if got := w.Header().Get("Location"); got != tt.expectedLocation {
				t.Fatalf("重定向地址 = %q，期望 %q", got, tt.expectedLocation)
			}
		})
	}
}

const urlValidityTestUA = "qms-head-cancel-test"

func setup115URLValidityPlayback(t *testing.T, cachedURL string) (*gin.Engine, string, string, *atomic.Int64) {
	t.Helper()
	setupControllerTestDB(t, &models.Account{}, &models.Settings{})
	setup115PlaybackCache(t)
	setup115PlaybackSettings(t, &models.Settings{URLValidityCheckEnabled: 1, URLValidityCheckTimeoutSeconds: 9})
	previousLogger := helpers.V115Log
	helpers.V115Log = helpers.AppLogger
	t.Cleanup(func() { helpers.V115Log = previousLogger })
	account := &models.Account{ID: 115999, SourceType: models.SourceType115, UserId: "head-cancel-user"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	apiCalls := &atomic.Int64{}
	client := account.Get115Client()
	client.SetTransport(roundTripFunc(func(*http.Request) (*http.Response, error) {
		apiCalls.Add(1)
		return nil, fmt.Errorf("缓存有效时不应重新取链")
	}))
	t.Cleanup(func() { client.SetTransport(http.DefaultTransport) })
	cacheKey := v115URLCacheKey("head-cancel-pick", 1, 0, urlValidityTestUA)
	db.Cache.Set(cacheKey, []byte(cachedURL), 3000)
	router := gin.New()
	router.GET("/115/newurl", Get115UrlByPickCode)
	return router, "/115/newurl?pickcode=head-cancel-pick&userid=" + account.UserId + "&force=1", cacheKey, apiCalls
}

func TestGet115URLCancelsCachedHEADWithoutInvalidation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		deadline bool
		status   int
	}{
		{name: "取消后保留有效缓存且不跳转", status: http.StatusNoContent},
		{name: "期限结束不把校验中断当作失效", deadline: true, status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, headCanceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead || r.UserAgent() != urlValidityTestUA {
					t.Errorf("HEAD 方法或 UA 错误：%s %q", r.Method, r.UserAgent())
				}
				close(started)
				select {
				case <-r.Context().Done():
					close(headCanceled)
				case <-release:
					w.WriteHeader(tc.status)
				}
			}))
			defer cdn.Close()
			cachedURL := cdn.URL + "/video.mkv?k=private-signature"
			router, target, cacheKey, apiCalls := setup115URLValidityPlayback(t, cachedURL)
			ctx, cancel := context.WithCancel(t.Context())
			if tc.deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 500*time.Millisecond)
			}
			defer cancel()
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
			req.Header.Set("User-Agent", urlValidityTestUA)
			finished := make(chan struct{})
			go func() { defer close(finished); router.ServeHTTP(w, req) }()
			defer func() {
				close(release)
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("115 取链请求未退出")
				}
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("未进入 115 缓存 HEAD 校验")
			}
			if !tc.deadline {
				cancel()
			}
			<-ctx.Done()
			select {
			case <-finished:
			case <-time.After(500 * time.Millisecond):
				t.Fatal("请求取消或超时后 HEAD 校验仍在运行")
			}
			select {
			case <-headCanceled:
			case <-time.After(time.Second):
				t.Error("CDN HEAD 请求没有随控制器请求取消")
			}
			if string(db.Cache.Get(cacheKey)) != cachedURL || apiCalls.Load() != 0 {
				t.Error("取消 HEAD 校验不应删除已有缓存或重新取链")
			}
			var response APIResponse[any]
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusOK || response.Code != BadRequest || response.Message != "获取 115 下载链接失败" || w.Header().Get("Location") != "" {
				t.Fatalf("取消后应沿用失败响应且不能跳转：HTTP=%d response=%+v", w.Code, response)
			}
		})
	}
}

func TestFnosApplyCancels115CachedHEAD(t *testing.T) {
	started, headCanceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.UserAgent() != urlValidityTestUA {
			t.Errorf("HEAD 方法或 UA 错误：%s %q", r.Method, r.UserAgent())
		}
		close(started)
		select {
		case <-r.Context().Done():
			close(headCanceled)
		case <-release:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer cdn.Close()
	cachedURL := cdn.URL + "/video.mkv?k=private-signature"
	router, target, cacheKey, apiCalls := setup115URLValidityPlayback(t, cachedURL)
	root := t.TempDir()
	sourceURL := "http://private-qms:12333" + target
	file := filepath.Join(root, "episode.strm")
	if err := os.WriteFile(file, []byte(sourceURL), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{
			"file_stream":           map[string]any{"guid": "115-file", "path": file, "type": 1, "can_play": 1, "play_error": ""},
			"video_stream":          map[string]any{"media_guid": "115-file", "codec_type": "video"},
			"cloud_storage_info":    map[string]any{"valid": true, "disabled": false, "cloud_storage_type": 9001},
			"direct_link_qualities": []any{map[string]any{"url": sourceURL, "is_m3u8": false}},
		}})
	}))
	defer upstream.Close()
	nextUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "new-instance") }))
	defer nextUpstream.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	service := fnosproxy.NewService(fnosproxy.Options{
		HTTPHost:     "127.0.0.1:12333",
		HTTPPort:     port,
		MainHandler:  router,
		OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
	})
	if err := service.Start(models.FnosProxyConfig{Enabled: true, FnosURL: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	client := &http.Client{Timeout: 12 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	proxyURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		req, _ := http.NewRequest(http.MethodPost, proxyURL+"/v/api/v1/stream", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", urlValidityTestUA)
		response, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	}()
	defer func() {
		close(release)
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("飞牛原生播放请求未结束")
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("飞牛原生播放未进入真实 115 缓存 HEAD 校验")
	}
	config := models.FnosProxyConfig{Enabled: true, FnosURL: nextUpstream.URL}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	switchStarted := time.Now()
	if err := service.Apply(ctx, config); err != nil {
		t.Fatalf("单次切换飞牛实例失败：%v", err)
	}
	if elapsed := time.Since(switchStarted); elapsed >= 2*time.Second {
		t.Fatalf("HEAD 取消后切换耗时过长：%s", elapsed)
	}
	select {
	case <-headCanceled:
	case <-time.After(time.Second):
		t.Fatal("切换实例后 CDN HEAD 未取消")
	}
	if string(db.Cache.Get(cacheKey)) != cachedURL || apiCalls.Load() != 0 {
		t.Fatal("切换取消不能使缓存失效或重新取链")
	}
	if status := service.Status(config); !status.Running || status.RestartRequired || status.LastError != "" {
		t.Fatalf("切换后运行状态异常：%+v", status)
	}
	response, err := client.Get(proxyURL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "new-instance" {
		t.Fatalf("新实例未提供服务：body=%q err=%v", body, err)
	}
}
