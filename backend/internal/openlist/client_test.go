package openlist

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/helpers"
)

// 通过真实客户端和本地 HTTP 替身覆盖缓存创建、配置快照及 Token 刷新竞态。
func setupConcurrentClientTest(t *testing.T) {
	t.Helper()
	oldAppLog, oldOpenListLog := helpers.AppLogger, helpers.OpenListLog
	logger := &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.AppLogger, helpers.OpenListLog = logger, logger
	cachedClientsMutex.Lock()
	oldClients := cachedClients
	cachedClients = make(map[string]*Client)
	cachedClientsMutex.Unlock()
	t.Cleanup(func() {
		cachedClientsMutex.Lock()
		for _, client := range cachedClients {
			_ = client.client.Close()
		}
		cachedClients = oldClients
		cachedClientsMutex.Unlock()
		helpers.AppLogger, helpers.OpenListLog = oldAppLog, oldOpenListLog
	})
}

func TestNewClientConcurrentCache(t *testing.T) {
	for _, tc := range []struct {
		name        string
		sameAccount bool
	}{
		{name: "同账号复用客户端", sameAccount: true},
		{name: "不同账号独立客户端"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupConcurrentClientTest(t)
			clients := make([]*Client, 16)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range clients {
				wg.Go(func() {
					<-start
					accountID := uint(i + 1)
					if tc.sameAccount {
						accountID = 1
					}
					clients[i] = NewClient(accountID, "http://openlist.invalid", "user", "password", "token")
				})
			}
			close(start)
			wg.Wait()
			for i, client := range clients {
				accountID := uint(i + 1)
				if tc.sameAccount {
					accountID = 1
				}
				if client.AccountId != accountID {
					t.Errorf("客户端账号 = %d，期望 %d", client.AccountId, accountID)
				}
				if got := NewClient(accountID, "http://openlist.invalid", "user", "password", "token"); got != client {
					t.Errorf("账号 %d 未复用首次返回的客户端", accountID)
				}
			}
		})
	}
}

func TestClientConcurrentConfigurationRequests(t *testing.T) {
	setupConcurrentClientTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		config := strings.Split(r.URL.Path, "/")[1]
		if got := r.Header.Get("Authorization"); got != "token-"+config {
			t.Errorf("请求路径 %s 与 Token %q 不属于同一配置", r.URL.Path, got)
		}
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"id":1,"username":"user","task":{"id":"uploaded"}}}`)
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(file, []byte("upload"), 0600); err != nil {
		t.Fatal(err)
	}
	client := NewClient(1, server.URL+"/one/", "", "", "token-one")
	stop := make(chan struct{})
	var updates sync.WaitGroup
	updates.Go(func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			config := []string{"one", "two"}[i%2]
			NewClient(1, server.URL+"/"+config+"/", "", "", "token-"+config)
			runtime.Gosched()
		}
	})
	var requests sync.WaitGroup
	for range 4 {
		requests.Go(func() {
			for i := range 20 {
				var err error
				if i%2 == 0 {
					_, err = client.GetUserInfo("")
				} else {
					_, err = client.UploadUseHttp(file, "/upload.txt")
				}
				if err != nil {
					t.Errorf("并发配置更新时请求失败：%v", err)
					return
				}
			}
		})
	}
	requests.Wait()
	close(stop)
	updates.Wait()
}

func TestClientConcurrentTokenRefresh(t *testing.T) {
	setupConcurrentClientTest(t)
	helpers.InitEventBus()
	t.Cleanup(helpers.InitEventBus)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/auth/login" {
			var credentials struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
				t.Errorf("解析登录请求失败：%v", err)
			}
			if credentials.Username != credentials.Password {
				t.Errorf("登录用户名和密码来自不同配置：%+v", credentials)
			}
			_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"token":"shared-token"}}`)
			return
		}
		if r.Header.Get("Authorization") != "shared-token" {
			_, _ = io.WriteString(w, `{"code":401,"message":"expired","data":null}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"id":1,"username":"user"}}`)
	}))
	defer server.Close()
	client := NewClient(1, server.URL, "user", "user", "expired-token")
	var callbacks atomic.Int64
	helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(event helpers.Event) helpers.EventResult {
		data := event.Data.(map[string]any)
		if data["account_id"] != uint(1) || data["token"] != "shared-token" {
			t.Errorf("刷新事件数据不正确：%v", data)
		}
		// 同步订阅者可以重新获取客户端，不能被调用方持有的缓存锁或状态锁阻塞。
		if got := NewClient(1, server.URL, "callback", "callback", "shared-token"); got != client {
			t.Error("Token 回调未复用客户端")
		}
		callbacks.Add(1)
		return helpers.EventResult{Success: true}
	})
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range 200 {
			credentials := []string{"first", "second"}[i%2]
			NewClient(1, server.URL, credentials, credentials, "expired-token")
			client.SetAuthToken("shared-token")
			if token := client.GetAuthToken(); token != "shared-token" {
				t.Errorf("访问凭证 = %q，期望刷新后的 Token", token)
			}
			runtime.Gosched()
		}
	})
	for range 4 {
		wg.Go(func() {
			for i := range 10 {
				var err error
				if i%2 == 0 {
					_, err = client.GetToken()
				} else {
					_, err = client.GetUserInfo("")
				}
				if err != nil {
					t.Errorf("并发刷新 Token 失败：%v", err)
					return
				}
			}
		})
	}
	wg.Wait()
	if callbacks.Load() < 20 {
		t.Fatalf("Token 保存事件次数 = %d，期望至少 20", callbacks.Load())
	}
}

func TestClientTokenRefreshIsolatesBaseURLs(t *testing.T) {
	setupConcurrentClientTest(t)
	helpers.InitEventBus()
	t.Cleanup(helpers.InitEventBus)
	loginA, loginB, releaseA := make(chan struct{}), make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseA) })
	var loginsA, loginsB, requestsB atomic.Int64
	var requests sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/a/api/auth/login":
			if loginsA.Add(1) == 1 {
				close(loginA)
			}
			<-releaseA
			_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"token":"refreshed-a"}}`)
		case "/b/api/auth/login":
			if loginsB.Add(1) == 1 {
				close(loginB)
			}
			_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"token":"refreshed-b"}}`)
		case "/a/api/me", "/b/api/me":
			endpoint := strings.Split(r.URL.Path, "/")[1]
			token := r.Header.Get("Authorization")
			if endpoint == "b" {
				requestsB.Add(1)
			}
			if token != "expired-"+endpoint && token != "refreshed-"+endpoint {
				t.Errorf("地址 %s 收到了其他地址的 Token %q", endpoint, token)
			}
			if token != "refreshed-"+endpoint {
				_, _ = io.WriteString(w, `{"code":401,"message":"expired","data":null}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"id":1,"username":"user"}}`)
		default:
			t.Errorf("意外请求：%s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer func() {
		release()
		requests.Wait()
		server.Close()
	}()
	client := NewClient(1, server.URL+"/a/", "user", "password", "expired-a")
	errors := make(chan error, 2)
	requests.Go(func() { _, err := client.GetUserInfo(""); errors <- err })
	select {
	case <-loginA:
	case <-time.After(2 * time.Second):
		t.Fatal("地址 A 未开始刷新 Token")
	}
	NewClient(1, server.URL+"/b/", "user", "password", "expired-b")
	requests.Go(func() { _, err := client.GetUserInfo(""); errors <- err })
	// B 必须能在 A 的登录仍受阻时独立刷新，不能等待或复用 A 的 Token。
	select {
	case <-loginB:
	case <-time.After(2 * time.Second):
		t.Error("地址 B 的登录被地址 A 的刷新阻塞")
	}
	release()
	requests.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Errorf("刷新后请求失败：%v", err)
		}
	}
	if loginsA.Load() != 1 || loginsB.Load() != 1 || requestsB.Load() != 2 {
		t.Errorf("登录次数 A=%d，B=%d，B 用户信息请求次数=%d，期望 1、1、2", loginsA.Load(), loginsB.Load(), requestsB.Load())
	}
}

type handlerTransport http.HandlerFunc

func (h handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		defer r.Body.Close()
	}
	recorder := httptest.NewRecorder()
	h(recorder, r)
	return recorder.Result(), nil
}

func TestClientConcurrentTokenRefreshDeduplicatesLogins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		setupConcurrentClientTest(t)
		helpers.InitEventBus()
		t.Cleanup(helpers.InitEventBus)
		const workerCount = 4
		var logins, unauthorized atomic.Int64
		gate := make(chan struct{})
		transport := handlerTransport(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/api/auth/login":
				logins.Add(1)
				<-gate
				_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"token":"shared-token"}}`)
			case "/api/me":
				if r.Header.Get("Authorization") != "shared-token" {
					unauthorized.Add(1)
					_, _ = io.WriteString(w, `{"code":401,"message":"expired","data":null}`)
					return
				}
				_, _ = io.WriteString(w, `{"code":200,"message":"success","data":{"id":1,"username":"user"}}`)
			default:
				t.Errorf("意外请求：%s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
			}
		})
		client := NewClient(1, "http://openlist.invalid", "user", "user", "expired-token")
		client.client.SetTransport(transport)
		var wg sync.WaitGroup
		for range workerCount {
			wg.Go(func() {
				if _, err := client.GetUserInfo(""); err != nil {
					t.Errorf("并发刷新后请求失败：%v", err)
				}
			})
		}
		// 内存 HTTP 替身让 Wait 确认所有请求都已停在刷新屏障，无需猜测调度时长。
		synctest.Wait()
		if got := unauthorized.Load(); got != workerCount {
			t.Errorf("首次 401 次数 = %d，期望 %d", got, workerCount)
		}
		close(gate)
		wg.Wait()
		if got := logins.Load(); got != 1 {
			t.Fatalf("登录请求次数 = %d，期望去重后 1 次", got)
		}
	})
}
