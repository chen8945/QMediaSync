package v115auth

import (
	"testing"
	"time"
)

func TestOAuthStateStore(t *testing.T) {
	ResetOAuthStatesForTest()
	now := time.Now().Unix()
	SaveOAuthState(OAuthState{
		State:       "state-1",
		AccountID:   10,
		Provider:    ProviderMoviePilot,
		RedirectURL: "http://127.0.0.1/callback",
		CreatedAt:   now,
		ExpiresAt:   now + 600,
	})

	state, ok := GetOAuthState("state-1", ProviderMoviePilot)
	if !ok {
		t.Fatal("应能读取未过期且 provider 匹配的 state")
	}
	if state.AccountID != 10 {
		t.Fatalf("account_id = %d，期望 10", state.AccountID)
	}
	if _, ok := GetOAuthState("state-1", ProviderOpenList); ok {
		t.Fatal("provider 不匹配的 state 不应可读")
	}

	DeleteOAuthState("state-1")
	if _, ok := GetOAuthState("state-1", ProviderMoviePilot); ok {
		t.Fatal("删除后的 state 不应可读")
	}
}

func TestOAuthStateExpires(t *testing.T) {
	ResetOAuthStatesForTest()
	now := time.Now().Unix()
	SaveOAuthState(OAuthState{
		State:     "expired",
		AccountID: 11,
		Provider:  ProviderCloudDrive,
		CreatedAt: now - 700,
		ExpiresAt: now - 100,
	})

	if _, ok := GetOAuthState("expired", ProviderCloudDrive); ok {
		t.Fatal("过期 state 不应可读")
	}
}

func TestOAuthStateClaimIsSingleUseUntilReleasedOrConsumed(t *testing.T) {
	ResetOAuthStatesForTest()
	t.Cleanup(ResetOAuthStatesForTest)
	now := time.Now().Unix()
	SaveOAuthState(OAuthState{
		State:     "claim-state",
		AccountID: 11,
		Provider:  ProviderMoviePilot,
		CreatedAt: now,
		ExpiresAt: now + 600,
	})

	if _, ok := ClaimOAuthState("claim-state", ProviderMoviePilot); !ok {
		t.Fatal("OAuth state 首次 claim 应成功")
	}
	if _, ok := ClaimOAuthState("claim-state", ProviderMoviePilot); ok {
		t.Fatal("同一 OAuth state 不应并发 claim 两次")
	}
	ReleaseOAuthState("claim-state")
	if _, ok := ClaimOAuthState("claim-state", ProviderMoviePilot); !ok {
		t.Fatal("release 后 OAuth state 应可重试")
	}
	ConsumeOAuthState("claim-state")
	if _, ok := GetOAuthState("claim-state", ProviderMoviePilot); ok {
		t.Fatal("consume 后 OAuth state 不应继续可读")
	}
}

func TestDeleteOAuthStatesForAuthorization(t *testing.T) {
	ResetOAuthStatesForTest()
	t.Cleanup(ResetOAuthStatesForTest)
	now := time.Now().Unix()
	SaveOAuthState(OAuthState{State: "same", AccountID: 1, Provider: ProviderMoviePilot, AuthorizationID: "auth-1", CreatedAt: now, ExpiresAt: now + 600})
	SaveOAuthState(OAuthState{State: "other", AccountID: 1, Provider: ProviderMoviePilot, AuthorizationID: "auth-2", CreatedAt: now, ExpiresAt: now + 600})
	DeleteOAuthStatesForAuthorization(1, "auth-1")
	if _, ok := GetOAuthState("same", ProviderMoviePilot); ok {
		t.Fatal("指定授权会话的 OAuth state 应被删除")
	}
	if _, ok := GetOAuthState("other", ProviderMoviePilot); !ok {
		t.Fatal("其他授权会话的 OAuth state 不应被删除")
	}
}

func TestDeleteLegacyOAuthStatesForAccount(t *testing.T) {
	ResetOAuthStatesForTest()
	t.Cleanup(ResetOAuthStatesForTest)
	now := time.Now().Unix()
	SaveOAuthState(OAuthState{State: "legacy", AccountID: 1, Provider: ProviderMoviePilot, CreatedAt: now, ExpiresAt: now + 600})
	SaveOAuthState(OAuthState{State: "replacement", AccountID: 1, Provider: ProviderMoviePilot, AuthorizationID: "auth-1", CreatedAt: now, ExpiresAt: now + 600})
	SaveOAuthState(OAuthState{State: "other-account", AccountID: 2, Provider: ProviderMoviePilot, CreatedAt: now, ExpiresAt: now + 600})

	DeleteLegacyOAuthStatesForAccount(1)
	if _, ok := GetOAuthState("legacy", ProviderMoviePilot); ok {
		t.Fatal("更换授权准备后，旧 OAuth state 应被删除")
	}
	if _, ok := GetOAuthState("replacement", ProviderMoviePilot); !ok {
		t.Fatal("带授权会话的 OAuth state 不应被旧流程清理")
	}
	if _, ok := GetOAuthState("other-account", ProviderMoviePilot); !ok {
		t.Fatal("其他账号的旧 OAuth state 不应被清理")
	}
}
