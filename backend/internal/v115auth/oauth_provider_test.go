package v115auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

func TestOAuthProviderRegistry(t *testing.T) {
	for _, provider := range []AuthProvider{
		ProviderQMediaSync,
		ProviderMQFamily,
		ProviderMoviePilot,
		ProviderCloudDrive,
	} {
		if _, ok := GetOAuthProvider(provider); !ok {
			t.Fatalf("未注册 OAuth provider: %s", provider)
		}
	}
	if _, ok := GetOAuthProvider(ProviderOpenList); ok {
		t.Fatal("OpenList 网页授权已删除，不应注册 OAuth provider")
	}
}

func TestOAuthProviderRelayBuildAuth(t *testing.T) {
	oldKey := helpers.OAuthRelayEncryptionKey
	oldAuthServer := helpers.GlobalConfig.AuthServer
	oldNewAuthServer := helpers.GlobalConfig.NewAuthServer
	t.Cleanup(func() {
		helpers.OAuthRelayEncryptionKey = oldKey
		helpers.GlobalConfig.AuthServer = oldAuthServer
		helpers.GlobalConfig.NewAuthServer = oldNewAuthServer
	})

	helpers.OAuthRelayEncryptionKey = "shared-secret"
	helpers.GlobalConfig.NewAuthServer = "https://oauth.qmediasync.cn"
	provider, ok := GetOAuthProvider(ProviderQMediaSync)
	if !ok {
		t.Fatal("未找到 qmediasync provider")
	}
	result, err := provider.BuildAuth(context.Background(), OAuthURLRequest{
		AccountID:   1,
		AppID:       BuiltInRelayQMediaSync,
		RedirectURL: "http://127.0.0.1:1233",
		Provider:    ProviderQMediaSync,
	})
	if err != nil {
		t.Fatalf("生成内置中转授权地址失败: %v", err)
	}
	if !strings.HasPrefix(result.AuthURL, "https://oauth.qmediasync.cn/115.php?action=code&state=") {
		t.Fatalf("授权地址 = %s", result.AuthURL)
	}
	if result.Polling {
		t.Fatal("内置中转不应标记为轮询授权")
	}
}

func TestOAuthProviderRelayRequiresEncryptionKey(t *testing.T) {
	oldKey := helpers.OAuthRelayEncryptionKey
	t.Cleanup(func() { helpers.OAuthRelayEncryptionKey = oldKey })
	helpers.OAuthRelayEncryptionKey = ""

	provider, ok := GetOAuthProvider(ProviderMQFamily)
	if !ok {
		t.Fatal("未找到 mqfamily provider")
	}
	_, err := provider.BuildAuth(context.Background(), OAuthURLRequest{
		AccountID:   1,
		AppID:       BuiltInRelayQ115STRM,
		RedirectURL: "http://127.0.0.1:1233",
		Provider:    ProviderMQFamily,
	})
	if err == nil {
		t.Fatal("缺少 OAUTH_RELAY_ENCRYPTION_KEY 时应返回错误")
	}
}

func TestOAuthProviderCloudDriveBuildAuthUsesRedirectState(t *testing.T) {
	provider := cloudDriveOAuthProvider{}
	result, err := provider.BuildAuth(context.Background(), OAuthURLRequest{
		AccountID:   4,
		RedirectURL: "http://127.0.0.1:12333/#/cloud-accounts",
		Provider:    ProviderCloudDrive,
	})
	if err != nil {
		t.Fatalf("生成 CloudDrive 授权地址失败: %v", err)
	}
	authURL, err := url.Parse(result.AuthURL)
	if err != nil {
		t.Fatalf("解析 CloudDrive 授权地址失败: %v", err)
	}
	query := authURL.Query()
	if authURL.Scheme != "https" || authURL.Host != "passportapi.115.com" || authURL.Path != "/open/authorize" {
		t.Fatalf("CloudDrive 授权地址 = %s", result.AuthURL)
	}
	if query.Get("client_id") != "100195313" {
		t.Fatalf("client_id = %s", query.Get("client_id"))
	}
	if query.Get("redirect_uri") != "https://redirect115.zhenyunpan.com" {
		t.Fatalf("redirect_uri = %s", query.Get("redirect_uri"))
	}
	if query.Get("response_type") != "code" {
		t.Fatalf("response_type = %s", query.Get("response_type"))
	}
	if query.Get("state") != "http://127.0.0.1:12333/#/cloud-accounts?account_id=4&source=115" {
		t.Fatalf("state = %s", query.Get("state"))
	}
}

func TestMoviePilotPollClaimsOAuthStateDuringRequest(t *testing.T) {
	ResetOAuthStatesForTest()
	t.Cleanup(ResetOAuthStatesForTest)
	SaveOAuthState(OAuthState{
		State:     "poll-state",
		AccountID: 1,
		Provider:  ProviderMoviePilot,
		ExpiresAt: time.Now().Unix() + 600,
	})

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"done":false}`))
	}))
	defer server.Close()
	provider := moviePilotOAuthProvider{authServer: server.URL, client: server.Client()}

	resultCh := make(chan error, 1)
	go func() {
		_, err := provider.Poll(context.Background(), "poll-state")
		resultCh <- err
	}()
	<-entered
	if _, err := provider.Poll(context.Background(), "poll-state"); err == nil {
		t.Fatal("同一 OAuth state 的并发轮询应被拒绝")
	}
	close(release)
	if err := <-resultCh; err != nil {
		t.Fatalf("首次轮询失败: %v", err)
	}
	if _, ok := GetOAuthState("poll-state", ProviderMoviePilot); !ok {
		t.Fatal("未完成授权的 OAuth state 应保留供下次轮询")
	}
}
