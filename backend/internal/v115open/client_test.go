package v115open

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"

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
				_, err := client.GetToken(&QrCodeDataReturn{Uid: "test-uid"})
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
			var qrChallenges sync.Map
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
						if method := req.Form.Get("code_challenge_method"); method != "sha256" {
							t.Errorf("二维码请求挑战算法 = %q，期望 sha256", method)
						}
						qrChallenges.Store(req.Form.Get("code_challenge"), struct{}{})
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
			for range readers {
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
							apiErr, ok := errors.AsType[*OpenAPIError](err)
							expectedEmpty := tt.allowEmpty && ok && apiErr.Code == ACCESS_AUTH_INVALID
							if !expectedEmpty {
								t.Errorf("并发请求失败：%v", err)
								return
							}
						}
						qrCode, err := client.GetQrCode()
						if err != nil {
							t.Errorf("并发获取二维码失败：%v", err)
							return
						}
						if len(qrCode.CodeVerifier) != 64 {
							t.Errorf("二维码验证串长度 = %d，期望 64", len(qrCode.CodeVerifier))
							return
						}
						hash := sha256.Sum256([]byte(qrCode.CodeVerifier))
						challenge := base64.StdEncoding.EncodeToString(hash[:])
						if _, ok := qrChallenges.Load(challenge); !ok {
							t.Error("二维码请求挑战值与返回的验证串不匹配")
							return
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

type cancelRetryLogWriter struct {
	cancel context.CancelFunc
}

func (w cancelRetryLogWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "秒后重试") {
		w.cancel()
	}
	return len(p), nil
}

func TestDoAuthRequestCancellationStopsRetryWait(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	previousLogger := helpers.V115Log
	helpers.V115Log = &helpers.QLogger{Logger: log.New(cancelRetryLogWriter{cancel: cancel}, "", 0)}
	t.Cleanup(func() { helpers.V115Log = previousLogger })
	transport := &refreshStubTransport{response: `{"state":false,"code":12345,"message":"fixture failure"}`}
	client := newRefreshTestClient(transport)
	started := time.Now()
	_, _, err := client.doAuthRequest(ctx, OPEN_BASE_URL+"/open/ufile/files", client.client.R().SetMethod("GET"),
		&RequestConfig{BypassRateLimit: true, Timeout: time.Second, MaxRetries: 3, RetryDelay: time.Second}, nil)
	if !errors.Is(err, context.Canceled) || transport.requests.Load() != 1 || time.Since(started) >= 500*time.Millisecond {
		t.Fatalf("取消后不能等待或重试：错误=%v，请求数=%d，耗时=%s", err, transport.requests.Load(), time.Since(started))
	}
	_, _, err = client.doAuthRequest(ctx, OPEN_BASE_URL+"/open/ufile/files", client.client.R().SetMethod("GET"),
		&RequestConfig{BypassRateLimit: true, Timeout: time.Second}, nil)
	if !errors.Is(err, context.Canceled) || transport.requests.Load() != 1 {
		t.Fatal("已取消请求不能再次进入队列")
	}
}

func TestOpenClientSourceReadCancellationStopsHTTP(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, operation := range []struct {
		name     string
		endpoint string
		call     func(context.Context, *OpenClient) error
	}{
		{
			name:     "download_url",
			endpoint: "/open/ufile/downurl",
			call: func(ctx context.Context, client *OpenClient) error {
				_, err := client.GetDownloadURLWithError(ctx, "pick", "agent", false)
				return err
			},
		},
		{
			name:     "deletion_detail",
			endpoint: "/open/folder/get_info",
			call: func(ctx context.Context, client *OpenClient) error {
				_, err := client.GetFsDetailByCidForDeletion(ctx, "9001")
				return err
			},
		},
	} {
		for _, timing := range []struct {
			name   string
			before bool
		}{
			{name: "before_send", before: true},
			{name: "in_flight"},
		} {
			t.Run(operation.name+"/"+timing.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if timing.before {
					cancel()
				}
				var calls atomic.Int32
				transportDone := make(chan error, 1)
				client := NewClient(1, "app", "token", "refresh")
				client.SetTransport(playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
					if calls.Add(1) > 1 {
						return nil, errors.New("取消后的来源请求发生重试")
					}
					if req.URL.Path != operation.endpoint {
						t.Errorf("来源读取端点 = %s，期望 %s", req.URL.Path, operation.endpoint)
					}
					cancel()
					var transportErr error
					select {
					case <-req.Context().Done():
						transportErr = req.Context().Err()
					case <-time.After(3 * time.Second):
						transportErr = errors.New("来源读取取消未传播至 HTTP 请求")
					}
					transportDone <- transportErr
					return nil, transportErr
				}))
				if err := operation.call(ctx, client); !errors.Is(err, context.Canceled) {
					t.Fatalf("来源读取取消错误 = %v", err)
				}
				wantCalls := int32(0)
				if !timing.before {
					wantCalls = 1
					select {
					case err := <-transportDone:
						if !errors.Is(err, context.Canceled) {
							t.Error(err)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("取消后来源 HTTP 请求未退出")
					}
				}
				if calls.Load() != wantCalls {
					t.Fatalf("来源读取 HTTP 请求数 = %d，期望 %d", calls.Load(), wantCalls)
				}
			})
		}
	}
}

func TestDoAuthRequestPreservesAuthenticationErrors(t *testing.T) {
	withUnlimitedOpenAPIRequests(t)
	for _, tt := range []struct {
		name         string
		code, status int
		emptyToken   bool
	}{
		{name: "missing_credentials", code: ACCESS_AUTH_INVALID, emptyToken: true},
		{name: "expired", code: ACCESS_TOKEN_EXPIRY_CODE},
		{name: "invalid_access", code: ACCESS_AUTH_INVALID},
		{name: "auth_failed", code: ACCESS_TOKEN_AUTH_FAIL},
		{name: "invalid_refresh", code: REFRESH_TOKEN_INVALID},
		{name: "http_unauthorized", status: http.StatusUnauthorized},
		{name: "directory_unavailable", code: 430004},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int64
			transport := playbackTransportFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}
				body := fmt.Sprintf(`{"state":false,"code":%d,"message":"fixture failure"}`, tt.code)
				if tt.status == http.StatusUnauthorized {
					body = "unauthorized"
				}
				return playbackTestResponse(req, status, body), nil
			})
			client := newRefreshTestClient(transport)
			if tt.emptyToken {
				client.SetAuthToken("", "")
			}
			_, _, err := client.doAuthRequest(t.Context(), OPEN_BASE_URL+"/open/ufile/files", client.client.R().SetMethod("GET"),
				&RequestConfig{BypassRateLimit: true, Timeout: time.Second}, nil)
			apiErr, ok := errors.AsType[*OpenAPIError](err)
			if !ok || apiErr.Code != tt.code || apiErr.HTTPStatus != tt.status {
				t.Fatalf("error=%+v; want code=%d status=%d", err, tt.code, tt.status)
			}
			want := int64(1)
			if tt.emptyToken {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("requests=%d, want=%d", calls.Load(), want)
			}
		})
	}
}
