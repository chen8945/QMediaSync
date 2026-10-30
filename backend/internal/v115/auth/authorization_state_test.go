package v115auth

import (
	"errors"
	"testing"
)

func TestAuthorizationSessionLifecycle(t *testing.T) {
	ResetAuthorizationSessionsForTest()
	t.Cleanup(ResetAuthorizationSessionsForTest)

	source := Source{
		SourceType: SourceTypeBuiltInAppID,
		Provider:   ProviderOfficialPKCE,
		AppID:      "100197849",
		AppName:    "QMediaSync",
	}
	session, err := CreateAuthorizationSession(12, source)
	if err != nil {
		t.Fatalf("创建授权会话失败: %v", err)
	}
	if session.ID == "" || session.ExpiresAt <= session.CreatedAt {
		t.Fatalf("授权会话时间或 ID 无效: %#v", session)
	}

	got, ok := GetAuthorizationSession(session.ID, 12)
	if !ok || got.Source.AppID != source.AppID {
		t.Fatalf("无法读取账号绑定的授权会话: %#v, %v", got, ok)
	}
	if _, ok := GetAuthorizationSession(session.ID, 13); ok {
		t.Fatal("其他账号不应读取授权会话")
	}

	DeleteAuthorizationSession(session.ID)
	if _, ok := GetAuthorizationSession(session.ID, 12); ok {
		t.Fatal("删除后的授权会话不应继续可用")
	}
}

func TestAuthorizationSessionAllowsOnlyOneActiveSessionPerAccount(t *testing.T) {
	ResetAuthorizationSessionsForTest()
	t.Cleanup(ResetAuthorizationSessionsForTest)

	source := Source{SourceType: SourceTypeBuiltInAppID, Provider: ProviderOfficialPKCE, AppID: "app"}
	first, err := CreateAuthorizationSession(12, source)
	if err != nil {
		t.Fatalf("创建首个授权会话失败: %v", err)
	}
	if _, err := CreateAuthorizationSession(12, source); !errors.Is(err, ErrAuthorizationSessionActive) {
		t.Fatalf("重复创建授权会话错误 = %v，期望 ErrAuthorizationSessionActive", err)
	}

	if !CancelAuthorizationSession(first.ID, 12) {
		t.Fatal("取消首个授权会话失败")
	}
	if _, err := CreateAuthorizationSession(12, source); err != nil {
		t.Fatalf("取消后应允许创建新授权会话: %v", err)
	}
}

func TestAuthorizationSessionExpires(t *testing.T) {
	ResetAuthorizationSessionsForTest()
	t.Cleanup(ResetAuthorizationSessionsForTest)

	authorizationSessions.Lock()
	authorizationSessions.items["expired"] = AuthorizationSession{
		ID:        "expired",
		AccountID: 12,
		ExpiresAt: 1,
	}
	authorizationSessions.Unlock()

	if _, ok := GetAuthorizationSession("expired", 12); ok {
		t.Fatal("过期的授权会话不应继续可用")
	}
}

func TestAuthorizationSessionClaimCancelPreventsCommit(t *testing.T) {
	ResetAuthorizationSessionsForTest()
	t.Cleanup(ResetAuthorizationSessionsForTest)

	session, err := CreateAuthorizationSession(12, Source{SourceType: SourceTypeBuiltInAppID, Provider: ProviderOfficialPKCE, AppID: "app"})
	if err != nil {
		t.Fatalf("创建授权会话失败: %v", err)
	}
	if !BeginAuthorizationSession(session.ID, 12) {
		t.Fatal("授权会话应能被首次 claim")
	}
	if BeginAuthorizationSession(session.ID, 12) {
		t.Fatal("同一授权会话不应被重复 claim")
	}
	if !CancelAuthorizationSession(session.ID, 12) {
		t.Fatal("取消 in-flight 授权会话失败")
	}
	if _, ok := GetAuthorizationSession(session.ID, 12); ok {
		t.Fatal("取消 in-flight 授权会话后不应继续占用账号")
	}
	next, err := CreateAuthorizationSession(12, Source{SourceType: SourceTypeBuiltInAppID, Provider: ProviderOfficialPKCE, AppID: "next-app"})
	if err != nil {
		t.Fatalf("取消后应立即允许创建新授权会话: %v", err)
	}
	DeleteAuthorizationSession(next.ID)
	called := false
	if err := CommitAuthorizationSession(session.ID, 12, func() error {
		called = true
		return nil
	}); !errors.Is(err, ErrAuthorizationSessionInactive) {
		t.Fatalf("取消后的 commit 错误 = %v", err)
	}
	if called {
		t.Fatal("取消后的授权会话不应执行最终写入")
	}
	if _, err := CreateAuthorizationSession(12, Source{SourceType: SourceTypeBuiltInAppID, Provider: ProviderOfficialPKCE, AppID: "next-app"}); err != nil {
		t.Fatalf("取消后的 claim 会话应释放账号占用: %v", err)
	}
}

func TestLegacyAuthorizationCommitIsBlockedByActiveReplacement(t *testing.T) {
	ResetAuthorizationSessionsForTest()
	t.Cleanup(ResetAuthorizationSessionsForTest)

	source := Source{SourceType: SourceTypeBuiltInAppID, Provider: ProviderOfficialPKCE, AppID: "app"}
	if _, err := CreateAuthorizationSession(12, source); err != nil {
		t.Fatalf("创建授权会话失败: %v", err)
	}
	called := false
	if err := CommitLegacyAuthorization(12, func() error {
		called = true
		return nil
	}); !errors.Is(err, ErrAuthorizationSessionActive) {
		t.Fatalf("活动更换会话下的旧授权提交错误 = %v", err)
	}
	if called {
		t.Fatal("活动更换会话下不应执行旧授权提交")
	}
}

func TestLegacyAuthorizationCommitIsInvalidatedAfterReplacement(t *testing.T) {
	ResetAuthorizationSessionsForTest()
	t.Cleanup(ResetAuthorizationSessionsForTest)

	startedState := snapshotLegacyAuthorizationState(12)
	session, err := CreateAuthorizationSession(12, Source{SourceType: SourceTypeBuiltInAppID, Provider: ProviderOfficialPKCE, AppID: "new-app"})
	if err != nil || !BeginAuthorizationSession(session.ID, 12) {
		t.Fatalf("准备更换授权会话失败: %v", err)
	}
	if err := CommitAuthorizationSession(session.ID, 12, func() error { return nil }); err != nil {
		t.Fatalf("提交更换授权会话失败: %v", err)
	}

	called := false
	if err := commitLegacyAuthorization(12, startedState, func() error {
		called = true
		return nil
	}); !errors.Is(err, ErrAuthorizationSessionInactive) {
		t.Fatalf("更换授权完成后的旧提交错误 = %v", err)
	}
	if called {
		t.Fatal("更换授权完成后的旧提交不应执行写入")
	}

	if err := CommitLegacyAuthorization(12, func() error { return nil }); err != nil {
		t.Fatalf("新一轮无会话授权不应被旧代次阻塞: %v", err)
	}
}

func TestAuthorizationSessionCommitConsumesAfterSuccessfulWrite(t *testing.T) {
	ResetAuthorizationSessionsForTest()
	t.Cleanup(ResetAuthorizationSessionsForTest)

	session, err := CreateAuthorizationSession(12, Source{SourceType: SourceTypeBuiltInAppID, Provider: ProviderOfficialPKCE, AppID: "app"})
	if err != nil || !BeginAuthorizationSession(session.ID, 12) {
		t.Fatalf("准备授权会话失败: %v", err)
	}
	called := false
	if err := CommitAuthorizationSession(session.ID, 12, func() error {
		called = true
		return nil
	}); err != nil || !called {
		t.Fatalf("成功 commit 结果 = %v, called=%v", err, called)
	}
	if _, ok := GetAuthorizationSession(session.ID, 12); ok {
		t.Fatal("成功 commit 后授权会话应被消费")
	}
}
