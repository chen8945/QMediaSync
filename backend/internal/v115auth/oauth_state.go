package v115auth

import (
	"sync"
	"time"
)

const OAuthStateTTLSeconds int64 = 600

type OAuthState struct {
	State           string
	AccountID       uint
	Provider        AuthProvider
	RedirectURL     string
	AuthorizationID string
	Source          Source
	CreatedAt       int64
	ExpiresAt       int64
}

var oauthStates = struct {
	sync.Mutex
	items    map[string]OAuthState
	inFlight map[string]bool
}{
	items:    map[string]OAuthState{},
	inFlight: map[string]bool{},
}

func SaveOAuthState(state OAuthState) {
	now := time.Now().Unix()
	if state.CreatedAt == 0 {
		state.CreatedAt = now
	}
	if state.ExpiresAt == 0 {
		state.ExpiresAt = state.CreatedAt + OAuthStateTTLSeconds
	}
	oauthStates.Lock()
	oauthStates.items[state.State] = state
	delete(oauthStates.inFlight, state.State)
	oauthStates.Unlock()
}

func getOAuthState(state string, matches func(OAuthState) bool) (OAuthState, bool) {
	oauthStates.Lock()
	defer oauthStates.Unlock()
	item, ok := oauthStates.items[state]
	if !ok {
		return OAuthState{}, false
	}
	if item.ExpiresAt <= time.Now().Unix() {
		delete(oauthStates.items, state)
		delete(oauthStates.inFlight, state)
		return OAuthState{}, false
	}
	if !matches(item) {
		return OAuthState{}, false
	}
	return item, true
}

func GetOAuthState(state string, provider AuthProvider) (OAuthState, bool) {
	return getOAuthState(state, func(item OAuthState) bool {
		return item.Provider == provider
	})
}

func GetOAuthStateForAccount(state string, accountID uint) (OAuthState, bool) {
	return getOAuthState(state, func(item OAuthState) bool {
		return item.AccountID == accountID
	})
}

// ClaimOAuthState 将同一授权状态的轮询串行化。
// 控制器调用 ReleaseOAuthState 或 ConsumeOAuthState 前，会一直保留该状态的占用。
func ClaimOAuthState(state string, provider AuthProvider) (OAuthState, bool) {
	oauthStates.Lock()
	defer oauthStates.Unlock()
	item, ok := oauthStates.items[state]
	if !ok || item.Provider != provider || item.ExpiresAt <= time.Now().Unix() || oauthStates.inFlight[state] {
		if ok && item.ExpiresAt <= time.Now().Unix() {
			delete(oauthStates.items, state)
			delete(oauthStates.inFlight, state)
		}
		return OAuthState{}, false
	}
	if oauthStates.inFlight == nil {
		oauthStates.inFlight = map[string]bool{}
	}
	oauthStates.inFlight[state] = true
	return item, true
}

func ReleaseOAuthState(state string) {
	oauthStates.Lock()
	delete(oauthStates.inFlight, state)
	oauthStates.Unlock()
}

func ConsumeOAuthState(state string) {
	oauthStates.Lock()
	delete(oauthStates.items, state)
	delete(oauthStates.inFlight, state)
	oauthStates.Unlock()
}

func deleteOAuthStatesForAccount(accountID uint, matches func(OAuthState) bool) {
	oauthStates.Lock()
	defer oauthStates.Unlock()
	for state, item := range oauthStates.items {
		if item.AccountID == accountID && matches(item) {
			delete(oauthStates.items, state)
			delete(oauthStates.inFlight, state)
		}
	}
}

// DeleteOAuthStatesForAuthorization 清理属于指定授权更换会话的所有 OAuth 轮询状态。
func DeleteOAuthStatesForAuthorization(accountID uint, authorizationID string) {
	deleteOAuthStatesForAccount(accountID, func(item OAuthState) bool {
		return item.AuthorizationID == authorizationID
	})
}

// DeleteLegacyOAuthStatesForAccount 清理账号进入授权更换流程前创建的旧 OAuth 轮询状态。
func DeleteLegacyOAuthStatesForAccount(accountID uint) {
	deleteOAuthStatesForAccount(accountID, func(item OAuthState) bool {
		return item.AuthorizationID == ""
	})
}

func DeleteOAuthState(state string) {
	ConsumeOAuthState(state)
}

func CleanupExpiredOAuthStates(now int64) {
	oauthStates.Lock()
	for state, item := range oauthStates.items {
		if item.ExpiresAt <= now {
			delete(oauthStates.items, state)
			delete(oauthStates.inFlight, state)
		}
	}
	oauthStates.Unlock()
}

func ResetOAuthStatesForTest() {
	oauthStates.Lock()
	oauthStates.items = map[string]OAuthState{}
	oauthStates.inFlight = map[string]bool{}
	oauthStates.Unlock()
}
