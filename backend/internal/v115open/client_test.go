package v115open

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestGetClientUpdatesCachedAppID(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	first := GetClient(12, "old-app", "old-token", "old-refresh")
	second := GetClient(12, "new-app", "new-token", "new-refresh")
	if first != second {
		t.Fatal("同一账号应复用缓存客户端")
	}
	want := clientCredentials{appID: "new-app", accessToken: "new-token", refreshToken: "new-refresh"}
	if got := second.credentialSnapshot(); got != want {
		t.Fatalf("缓存客户端未更新授权信息: %#v", got)
	}
}

func TestGetCachedClientDoesNotOverwriteCachedAuth(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	current := GetClient(12, "new-app", "new-token", "new-refresh")
	stale := GetCachedClient(12, "old-app", "old-token", "old-refresh")
	if stale != current {
		t.Fatal("已有账号应复用共享客户端")
	}
	want := clientCredentials{appID: "new-app", accessToken: "new-token", refreshToken: "new-refresh"}
	if got := stale.credentialSnapshot(); got != want {
		t.Fatalf("旧账号快照覆盖了共享客户端凭据: %#v", got)
	}
}

func TestNewClientDoesNotEnterCache(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	temporary := NewClient(12, "temporary-app", "token", "refresh")
	cachedClientsMutex.RLock()
	_, ok := cachedClients["12"]
	cachedClientsMutex.RUnlock()
	if ok || temporary.credentialSnapshot().appID != "temporary-app" {
		t.Fatalf("临时客户端不应写入缓存: cached=%v client=%#v", ok, temporary)
	}
}

func TestUpdateTokenIfCurrentSkipsReplacedCredentials(t *testing.T) {
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})

	client := GetClient(12, "app", "new-token", "new-refresh")
	if UpdateTokenIfCurrent(12, "old-token", "old-refresh", "stale-token", "stale-refresh") {
		t.Fatal("共享客户端凭据已替换时不应接受旧刷新结果")
	}
	if UpdateTokenIfCurrent(
		12,
		"new-token",
		"old-refresh",
		"",
		"",
	) {
		t.Fatal("刷新令牌不匹配时不应清空当前凭据")
	}
	if UpdateTokenIfCurrent(
		12,
		"old-token",
		"new-refresh",
		"",
		"",
	) {
		t.Fatal("访问令牌不匹配时不应清空当前凭据")
	}
	want := clientCredentials{appID: "app", accessToken: "new-token", refreshToken: "new-refresh"}
	if got := client.credentialSnapshot(); got != want {
		t.Fatalf("旧刷新结果改变了新共享凭据: %#v", got)
	}
	if !UpdateTokenIfCurrent(12, "new-token", "new-refresh", "latest-token", "latest-refresh") {
		t.Fatal("当前共享客户端凭据应允许条件更新")
	}
	want = clientCredentials{appID: "app", accessToken: "latest-token", refreshToken: "latest-refresh"}
	if got := client.credentialSnapshot(); got != want {
		t.Fatalf("条件更新未应用: %#v", got)
	}

	for range 200 {
		client.SetAuthToken("expected", "expected")
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Go(func() {
			<-start
			UpdateTokenIfCurrent(
				12,
				"expected",
				"expected",
				"stale",
				"stale",
			)
		})
		workers.Go(func() {
			<-start
			client.SetAuthToken("replaced", "replaced")
		})
		close(start)
		workers.Wait()
		want = clientCredentials{appID: "app", accessToken: "replaced", refreshToken: "replaced"}
		if got := client.credentialSnapshot(); got != want {
			t.Fatalf("条件更新覆盖了并发替换的新凭据：%#v", got)
		}
	}
}

func withEmptyClientCache(t *testing.T) {
	t.Helper()
	cachedClientsMutex.Lock()
	original := cachedClients
	cachedClients = map[string]*OpenClient{}
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		cachedClients = original
		cachedClientsMutex.Unlock()
	})
}

// 保留真实全局队列，只暂停测试期间的限速等待，不改变单例的生命周期。
func withUnlimitedOpenAPIRequests(t *testing.T) {
	t.Helper()
	ensureOpenAPITestLoggers()
	executor := GetGlobalExecutor()
	executor.RLock()
	limiters := []*rate.Limiter{executor.qpsLimiter, executor.qpmLimiter, executor.qphLimiter}
	executor.RUnlock()
	for _, limiter := range limiters {
		original := limiter.Limit()
		limiter.SetLimit(rate.Inf)
		t.Cleanup(func() { limiter.SetLimit(original) })
	}
}

// 请求经真实全局队列执行，覆盖入队前读取凭据与各更新入口之间的竞争。
func TestOpenClientRequestsConcurrentWithCredentialUpdates(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	withEmptyClientCache(t)

	tests := []struct {
		name       string
		replaceApp bool
		allowEmpty bool
		update     func(*OpenClient, string, string) error
	}{
		{
			name: "直接设置令牌",
			update: func(client *OpenClient, _, next string) error {
				client.SetAuthToken(next, next)
				return nil
			},
		},
		{
			name: "按账号更新令牌",
			update: func(client *OpenClient, _, next string) error {
				UpdateToken(client.AccountId, next, next)
				return nil
			},
		},
		{
			name:       "替换整组授权",
			replaceApp: true,
			update: func(client *OpenClient, _, next string) error {
				GetClient(
					client.AccountId,
					next,
					next,
					next,
				)
				GetCachedClient(
					client.AccountId,
					"stale",
					"stale",
					"stale",
				)
				return nil
			},
		},
		{
			name: "条件更新令牌",
			update: func(client *OpenClient, previous, next string) error {
				if !UpdateTokenIfCurrent(
					client.AccountId,
					previous,
					previous,
					next,
					next,
				) {
					return fmt.Errorf("当前凭据的条件刷新不应失败")
				}
				return nil
			},
		},
		{
			name: "二维码取令牌",
			update: func(client *OpenClient, _, _ string) error {
				_, err := client.GetToken(&QrCodeDataReturn{QrCodeData: QrCodeData{Uid: "test-uid"}})
				return err
			},
		},
		{
			name: "刷新令牌",
			update: func(client *OpenClient, _, _ string) error {
				_, err := client.RefreshToken("")
				return err
			},
		},
		{
			name:       "清空并恢复令牌",
			allowEmpty: true,
			update: func(client *OpenClient, _, next string) error {
				client.SetAuthToken("", "")
				runtime.Gosched()
				client.SetAuthToken(next, next)
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var authRequests atomic.Int32
			transport := &refreshStubTransport{
				response: `{"state":true,"code":0,"data":{
					"access_token":"remote","refresh_token":"remote","uid":"test-uid"
				}}`,
				onRequest: func(req *http.Request) {
					switch req.URL.Path {
					case "/open/user/info":
						authRequests.Add(1)
						token := req.Header.Get("Authorization")
						if !strings.HasPrefix(token, "Bearer ") || token == "Bearer " {
							t.Errorf("请求未使用完整访问令牌：%q", token)
						}
					case "/open/authDeviceCode":
						if err := req.ParseForm(); err != nil {
							t.Error(err)
						}
						if appID := req.Form.Get("client_id"); !strings.HasPrefix(appID, "version-") {
							t.Errorf("二维码请求应用 ID 无效：%q", appID)
						}
					}
				},
			}
			client := GetClient(
				12,
				"version-0",
				"version-0",
				"version-0",
			)
			client.client.SetTransport(transport)
			const readers, requests = 4, 40
			start := make(chan struct{})
			var workers sync.WaitGroup
			workers.Go(func() {
				<-start
				previous := "version-0"
				for i := range 800 {
					next := fmt.Sprintf("version-%d", i+1)
					if err := tt.update(client, previous, next); err != nil {
						t.Error(err)
						return
					}
					previous = next
					runtime.Gosched()
				}
			})
			for reader := range readers {
				workers.Go(func() {
					<-start
					for range requests {
						credentials := client.credentialSnapshot()
						wantApp := "version-0"
						if tt.replaceApp {
							wantApp = credentials.accessToken
						}
						if credentials.accessToken != credentials.refreshToken || credentials.appID != wantApp {
							t.Errorf("读到了不一致的凭据快照：%#v", credentials)
							return
						}
						req := client.client.R().SetMethod("GET")
						_, _, err := client.doAuthRequest(
							t.Context(),
							"https://proapi.115.com/open/user/info",
							req,
							&RequestConfig{BypassRateLimit: true, Timeout: 3 * time.Second},
							nil,
						)
						if err != nil {
							expectedEmpty := tt.allowEmpty && err.Error() == "115 账号授权失效，请在网盘账号管理中重新授权"
							if !expectedEmpty {
								t.Errorf("并发请求失败：%v", err)
								return
							}
						}
						// ponytail: 共享 RandStr 尚不支持并发调用，仅保留一个二维码读取协程与凭据更新并发。
						if reader == 0 {
							if _, err := client.GetQrCode(); err != nil {
								t.Errorf("并发获取二维码失败：%v", err)
								return
							}
						}
					}
				})
			}
			close(start)
			workers.Wait()
			if got := authRequests.Load(); !tt.allowEmpty && got != readers*requests {
				t.Fatalf("实际认证请求 %d 次，期望 %d 次", got, readers*requests)
			}
		})
	}
}

func TestOpenClientRequestsUseCredentialsAfterClearAndReauthorization(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	withEmptyClientCache(t)
	headers := make(chan string, 1)
	transport := &refreshStubTransport{
		response: `{"state":true,"code":0,"data":{}}`,
		onRequest: func(req *http.Request) {
			headers <- req.Header.Get("Authorization")
		},
	}
	client := GetClient(
		12,
		"old-app",
		"old-token",
		"old-refresh",
	)
	client.client.SetTransport(transport)
	checkRequest := func(token string) {
		t.Helper()
		before := transport.requests.Load()
		_, err := client.UserInfo()
		if token == "" {
			if err == nil || transport.requests.Load() != before {
				t.Fatal("清空凭据后应拒绝请求且不发送 HTTP 请求")
			}
			return
		}
		if err != nil {
			t.Fatalf("读取用户信息失败：%v", err)
		}
		if got := <-headers; got != "Bearer "+token {
			t.Fatalf("请求令牌 = %q，期望 %q", got, "Bearer "+token)
		}
	}

	checkRequest("old-token")
	UpdateToken(12, "rotated-token", "rotated-refresh")
	checkRequest("rotated-token")
	if !UpdateTokenIfCurrent(
		12,
		"rotated-token",
		"rotated-refresh",
		"",
		"",
	) {
		t.Fatal("当前凭据应允许条件清空")
	}
	if got := client.credentialSnapshot(); got != (clientCredentials{appID: "old-app"}) {
		t.Fatalf("清空令牌不应改变应用 ID：%#v", got)
	}
	checkRequest("")
	GetClient(
		12,
		"new-app",
		"new-token",
		"new-refresh",
	)
	GetCachedClient(
		12,
		"old-app",
		"old-token",
		"old-refresh",
	)
	NewClient(
		12,
		"temporary-app",
		"temporary-token",
		"temporary-refresh",
	)
	if UpdateTokenIfCurrent(
		12,
		"rotated-token",
		"rotated-refresh",
		"",
		"",
	) {
		t.Fatal("旧凭据的清空不应覆盖重新授权")
	}
	checkRequest("new-token")
}
