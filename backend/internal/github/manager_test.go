package github

import (
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func TestInitManager(t *testing.T) {
	// 重置默认管理器
	defaultManager = nil

	InitManager("http://proxy.example.com:8080")

	if defaultManager == nil {
		t.Error("InitManager 应创建非 nil 的管理器")
	}

	if defaultManager.testTimeout != 3*time.Second {
		t.Errorf("期望超时时间为 3 秒，实际为 %v", defaultManager.testTimeout)
	}

	if defaultManager.cacheValid != 10*time.Minute {
		t.Errorf("期望缓存有效期为 10 分钟，实际为 %v", defaultManager.cacheValid)
	}

	if defaultManager.httpProxy != "http://proxy.example.com:8080" {
		t.Errorf("期望 HTTP 代理为 http://proxy.example.com:8080，实际为 %v", defaultManager.httpProxy)
	}

	// 验证 GitHubProxyURL 常量
	if GithubProxyURL != "https://gh.llkk.cc" {
		t.Errorf("期望 GitHubProxyURL 为 https://gh.llkk.cc，实际为 %v", GithubProxyURL)
	}
}

func TestGetManager(t *testing.T) {
	// 重置默认管理器
	defaultManager = nil

	manager := GetManager()

	if manager == nil {
		t.Error("GetManager 应返回非 nil 的管理器")
	}

	// 再次调用应返回同一个实例
	manager2 := GetManager()
	if manager != manager2 {
		t.Error("GetManager 应返回单例")
	}
}

func TestManager_TestConnection(t *testing.T) {
	manager := &Manager{
		testTimeout: 3 * time.Second,
	}

	tests := []struct {
		name     string
		connType ConnectionType
		proxyURL string
	}{
		{
			name:     "测试直连",
			connType: ConnectionTypeDirect,
			proxyURL: "",
		},
		{
			name:     "测试无效代理",
			connType: ConnectionTypeProxy,
			proxyURL: "http://invalid-proxy:8080",
		},
		{
			name:     "测试无效 GitHub 代理",
			connType: ConnectionTypeGitHubProxy,
			proxyURL: "http://invalid-github-proxy:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 这里只验证不会 panic，结果可能因环境而异。
			got := manager.TestConnection(tt.connType, tt.proxyURL)
			t.Logf("连接测试结果: %v (连接类型: %s, 代理: %s)", got, tt.connType, tt.proxyURL)
		})
	}
}

func TestManager_TestConnection_InvalidProxyURL(t *testing.T) {
	manager := &Manager{
		testTimeout: 3 * time.Second,
	}

	// 测试无效的代理 URL
	got := manager.TestConnection(ConnectionTypeProxy, "://invalid-url")
	if got {
		t.Error("无效 URL 应返回 false")
	}
}

func TestManager_GetBestConnection(t *testing.T) {
	manager := &Manager{
		testTimeout: 3 * time.Second,
		cacheValid:  10 * time.Minute,
	}

	// 测试获取最佳连接
	access, err := manager.GetBestConnection()
	if err != nil {
		t.Logf("获取 GitHub 连接失败（可能网络不可达）: %v", err)
		return
	}

	if access == nil {
		t.Error("access 不应为 nil")
	}

	if access.Client == nil {
		t.Error("Client 不应为 nil")
	}

	if access.LastTested.IsZero() {
		t.Error("LastTested不应为零值")
	}

	// 验证缓存
	access2, err := manager.GetBestConnection()
	if err != nil {
		t.Error("第二次调用不应失败")
	}

	if access2.Type != access.Type {
		t.Errorf("缓存失败: 第一次 %s, 第二次 %s", access.Type, access2.Type)
	}

	// Cached 字段表示是否从缓存读取，第二次调用应该是 true。
	if !access2.Cached {
		t.Error("第二次调用应使用缓存（Cached 应为 true）")
	}
}

func TestManager_GetClient(t *testing.T) {
	manager := &Manager{
		testTimeout: 3 * time.Second,
		cacheValid:  10 * time.Minute,
	}

	client, err := manager.GetClient()
	if err != nil {
		t.Logf("获取 GitHub 客户端失败（可能网络不可达）: %v", err)
		return
	}

	if client == nil {
		t.Error("Client 不应为 nil")
	}

	if client.Timeout != 30*time.Second {
		t.Errorf("Client 超时应为 30 秒，实际为 %v", client.Timeout)
	}
}

func TestManager_GetClientWithCache_NoCache(t *testing.T) {
	manager := &Manager{
		testTimeout: 3 * time.Second,
		cacheValid:  10 * time.Minute,
	}

	// 没有缓存时调用
	_, err := manager.GetClientWithCache()
	if err == nil {
		t.Error("没有缓存时应返回错误")
	}
}

func TestManager_GetClientWithCache_WithCache(t *testing.T) {
	manager := &Manager{
		testTimeout: 3 * time.Second,
		cacheValid:  10 * time.Minute,
		current: &GitHubAccess{
			Type:   ConnectionTypeDirect,
			Client: &http.Client{Timeout: 30 * time.Second},
		},
	}

	client, err := manager.GetClientWithCache()
	if err != nil {
		t.Errorf("有缓存时不应返回错误: %v", err)
	}

	if client == nil {
		t.Error("Client 不应为 nil")
	}
}

func TestManager_ClearCache(t *testing.T) {
	manager := &Manager{
		testTimeout: 3 * time.Second,
		cacheValid:  10 * time.Minute,
	}

	// 设置缓存
	manager.current = &GitHubAccess{
		Type:       ConnectionTypeDirect,
		Client:     &http.Client{Timeout: 30 * time.Second},
		LastTested: time.Now(),
		Cached:     true,
	}

	// 清除缓存
	manager.ClearCache()

	if manager.current != nil {
		t.Error("缓存应被清除")
	}
}

func TestManager_ConnectionPriority(t *testing.T) {
	// 这个测试模拟有缓存的场景
	manager := &Manager{
		testTimeout: 3 * time.Second,
		cacheValid:  10 * time.Minute,
	}

	// 先设置一个代理缓存
	proxyURL := "http://proxy.example.com:8080"
	proxy, _ := url.Parse(proxyURL)

	manager.current = &GitHubAccess{
		Type: ConnectionTypeProxy,
		Client: &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{Proxy: http.ProxyURL(proxy)},
		},
		ProxyURL:   proxyURL,
		LastTested: time.Now(),
		Cached:     true,
	}

	// 应该立即返回缓存的代理连接
	access, err := manager.GetBestConnection()
	if err != nil {
		t.Errorf("应返回缓存的连接: %v", err)
	}

	if access.Type != ConnectionTypeProxy {
		t.Errorf("应返回代理连接，实际为%v", access.Type)
	}

	if !access.Cached {
		t.Error("应标记为缓存")
	}
}

func TestConnectionType_String(t *testing.T) {
	tests := []struct {
		name string
		ct   ConnectionType
		want string
	}{
		{
			name: "直连",
			ct:   ConnectionTypeDirect,
			want: "direct",
		},
		{
			name: "代理",
			ct:   ConnectionTypeProxy,
			want: "proxy",
		},
		{
			name: "GitHub 代理",
			ct:   ConnectionTypeGitHubProxy,
			want: "github_proxy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.ct) != tt.want {
				t.Errorf("期望%q, 实际%q", tt.want, tt.ct)
			}
		})
	}
}

type idleCloseTransport struct {
	closed int
}

func (tr *idleCloseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP request")
}

func (tr *idleCloseTransport) CloseIdleConnections() {
	tr.closed++
}

func TestManagerRetiresPrivateConnections(t *testing.T) {
	for _, action := range []string{"clear", "update", "expired", "cached"} {
		for _, shared := range []bool{false, true} {
			name := action + "/private"
			if shared {
				name = action + "/shared"
			}
			t.Run(name, func(t *testing.T) {
				transport := &idleCloseTransport{}
				client := &http.Client{Transport: transport}
				if shared {
					previous := http.DefaultTransport
					http.DefaultTransport = transport
					t.Cleanup(func() { http.DefaultTransport = previous })
					client.Transport = nil
				}
				manager := &Manager{
					current:    &GitHubAccess{Client: client, LastTested: time.Now().Add(-time.Hour)},
					cacheValid: time.Minute,
					httpProxy:  "://invalid-url",
				}
				switch action {
				case "clear":
					manager.ClearCache()
				case "update":
					previous := defaultManager
					defaultManager = manager
					t.Cleanup(func() { defaultManager = previous })
					UpdateConfig("://new-invalid-url")
					if manager.httpProxy != "://new-invalid-url" {
						t.Fatal("proxy configuration was not updated")
					}
				case "expired":
					if _, err := manager.GetBestConnection(); err == nil {
						t.Fatal("invalid proxy must fail without making a network request")
					}
				case "cached":
					manager.current.LastTested = time.Now()
					if _, err := manager.GetBestConnection(); err != nil {
						t.Fatal(err)
					}
				}
				wantClosed := 1
				if shared || action == "cached" {
					wantClosed = 0
				}
				if transport.closed != wantClosed {
					t.Fatalf("CloseIdleConnections calls = %d, want %d", transport.closed, wantClosed)
				}
				cached, err := manager.GetClientWithCache()
				if action == "expired" || action == "cached" {
					if err != nil || cached != client {
						t.Fatalf("cached client changed: client = %p, error = %v", cached, err)
					}
				} else if err == nil {
					t.Fatal("cleared cache still returned a client")
				}
			})
		}
	}
}
