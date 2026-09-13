package controllers

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
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
			if got := checkURLValidity(target, tt.userAgent, tt.timeout); got != tt.wantValid {
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
	setupControllerTestDB(t, &models.Account{})

	originalCache := db.Cache
	originalSettings := models.SettingsGlobal
	db.Cache = db.CacheGlobal{
		CacheInstance: freecache.NewCache(1024 * 1024),
		CacheSize:     1024 * 1024,
	}
	models.SettingsGlobal = &models.Settings{
		SettingURLValidityCheck: models.SettingURLValidityCheck{
			URLValidityCheckEnabled:        0,
			URLValidityCheckTimeoutSeconds: 1,
		},
	}
	t.Cleanup(func() {
		db.Cache = originalCache
		models.SettingsGlobal = originalSettings
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
	setupControllerTestDB(t, &models.Account{})

	originalCache := db.Cache
	originalSettings := models.SettingsGlobal
	originalLockWait := v115URLCacheLockWait
	db.Cache = db.CacheGlobal{
		CacheInstance: freecache.NewCache(1024 * 1024),
		CacheSize:     1024 * 1024,
	}
	models.SettingsGlobal = &models.Settings{
		SettingURLValidityCheck: models.SettingURLValidityCheck{
			URLValidityCheckEnabled:        1,
			URLValidityCheckTimeoutSeconds: 3,
		},
	}
	v115URLCacheLockWait = 20 * time.Millisecond
	t.Cleanup(func() {
		db.Cache = originalCache
		models.SettingsGlobal = originalSettings
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
	setupControllerTestDB(t, &models.Account{})

	originalCache := db.Cache
	originalSettings := models.SettingsGlobal
	db.Cache = db.CacheGlobal{
		CacheInstance: freecache.NewCache(1024 * 1024),
		CacheSize:     1024 * 1024,
	}
	models.SettingsGlobal = &models.Settings{
		SettingStrm: models.SettingStrm{
			LocalProxy: 1,
		},
		SettingURLValidityCheck: models.SettingURLValidityCheck{
			URLValidityCheckEnabled:        0,
			URLValidityCheckTimeoutSeconds: 1,
		},
	}
	t.Cleanup(func() {
		db.Cache = originalCache
		models.SettingsGlobal = originalSettings
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
