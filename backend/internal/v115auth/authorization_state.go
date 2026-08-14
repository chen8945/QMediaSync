package v115auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

const AuthorizationSessionTTLSeconds int64 = 600

type AuthorizationSession struct {
	ID        string
	AccountID uint
	Source    Source
	CreatedAt int64
	ExpiresAt int64
}

var authorizationSessions = struct {
	sync.Mutex
	items           map[string]AuthorizationSession
	consuming       map[string]bool
	activeByAccount map[uint]string
}{
	items:           map[string]AuthorizationSession{},
	consuming:       map[string]bool{},
	activeByAccount: map[uint]string{},
}

type legacyAuthorizationState struct {
	generation        uint64
	replacementActive bool
}

var legacyAuthorizationStates = struct {
	sync.RWMutex
	items map[uint]legacyAuthorizationState
}{items: map[uint]legacyAuthorizationState{}}

func snapshotLegacyAuthorizationState(accountID uint) legacyAuthorizationState {
	legacyAuthorizationStates.RLock()
	defer legacyAuthorizationStates.RUnlock()
	return legacyAuthorizationStates.items[accountID]
}

func invalidateLegacyAuthorizationState(accountID uint) {
	legacyAuthorizationStates.Lock()
	state := legacyAuthorizationStates.items[accountID]
	state.generation++
	state.replacementActive = true
	legacyAuthorizationStates.items[accountID] = state
	legacyAuthorizationStates.Unlock()
}

func clearLegacyAuthorizationActive(accountID uint) {
	legacyAuthorizationStates.Lock()
	state := legacyAuthorizationStates.items[accountID]
	state.replacementActive = false
	legacyAuthorizationStates.items[accountID] = state
	legacyAuthorizationStates.Unlock()
}

func removeActiveAuthorizationSessionLocked(id string) {
	for accountID, activeID := range authorizationSessions.activeByAccount {
		if activeID == id {
			delete(authorizationSessions.activeByAccount, accountID)
			clearLegacyAuthorizationActive(accountID)
		}
	}
}

func deleteAuthorizationSessionLocked(id string) {
	removeActiveAuthorizationSessionLocked(id)
	delete(authorizationSessions.items, id)
	delete(authorizationSessions.consuming, id)
}

func cleanupExpiredAuthorizationSessionsLocked(now int64) {
	for id, item := range authorizationSessions.items {
		if item.ExpiresAt <= now {
			deleteAuthorizationSessionLocked(id)
		}
	}
}

func CreateAuthorizationSession(accountID uint, source Source) (AuthorizationSession, error) {
	idBytes := make([]byte, 24)
	if _, err := rand.Read(idBytes); err != nil {
		return AuthorizationSession{}, err
	}

	now := time.Now().Unix()
	session := AuthorizationSession{
		ID:        hex.EncodeToString(idBytes),
		AccountID: accountID,
		Source:    source,
		CreatedAt: now,
		ExpiresAt: now + AuthorizationSessionTTLSeconds,
	}

	authorizationSessions.Lock()
	defer authorizationSessions.Unlock()
	cleanupExpiredAuthorizationSessionsLocked(now)
	if activeID := authorizationSessions.activeByAccount[accountID]; activeID != "" {
		if _, ok := authorizationSessions.items[activeID]; ok {
			return AuthorizationSession{}, ErrAuthorizationSessionActive
		}
		delete(authorizationSessions.activeByAccount, accountID)
	}
	if authorizationSessions.activeByAccount == nil {
		authorizationSessions.activeByAccount = map[uint]string{}
	}
	invalidateLegacyAuthorizationState(accountID)
	authorizationSessions.items[session.ID] = session
	authorizationSessions.activeByAccount[accountID] = session.ID
	return session, nil
}

func GetAuthorizationSession(id string, accountID uint) (AuthorizationSession, bool) {
	id = strings.TrimSpace(id)
	if id == "" {
		return AuthorizationSession{}, false
	}

	now := time.Now().Unix()
	authorizationSessions.Lock()
	defer authorizationSessions.Unlock()
	session, ok := authorizationSessions.items[id]
	if !ok {
		return AuthorizationSession{}, false
	}
	if session.ExpiresAt <= now || (accountID != 0 && session.AccountID != accountID) {
		if session.ExpiresAt <= now {
			deleteAuthorizationSessionLocked(id)
		}
		return AuthorizationSession{}, false
	}
	return session, true
}

// BeginAuthorizationSession 为最终授权流程占用会话。
// 同一时间只允许一个请求获取用户信息并提交授权。
func BeginAuthorizationSession(id string, accountID uint) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	now := time.Now().Unix()
	authorizationSessions.Lock()
	defer authorizationSessions.Unlock()
	session, ok := authorizationSessions.items[id]
	if !ok || session.ExpiresAt <= now || session.AccountID != accountID || authorizationSessions.consuming[id] {
		if ok && session.ExpiresAt <= now {
			deleteAuthorizationSessionLocked(id)
		}
		return false
	}
	if authorizationSessions.consuming == nil {
		authorizationSessions.consuming = map[string]bool{}
	}
	authorizationSessions.consuming[id] = true
	return true
}

// AbortAuthorizationSession 释放失败请求占用的会话，使其可以重试。
func AbortAuthorizationSession(id string) {
	id = strings.TrimSpace(id)
	authorizationSessions.Lock()
	defer authorizationSessions.Unlock()
	delete(authorizationSessions.consuming, id)
}

// CommitAuthorizationSession 在持有会话锁时执行数据库提交。
// 如果取消操作先获取锁，会话会被删除并阻止提交；如果取消操作在提交后到达，
// 会话已经被消费，取消操作只会无效返回。
func CommitAuthorizationSession(id string, accountID uint, commit func() error) error {
	id = strings.TrimSpace(id)
	authorizationSessions.Lock()
	defer authorizationSessions.Unlock()
	session, ok := authorizationSessions.items[id]
	if !ok {
		delete(authorizationSessions.consuming, id)
		removeActiveAuthorizationSessionLocked(id)
		return ErrAuthorizationSessionInactive
	}
	if session.ExpiresAt <= time.Now().Unix() {
		deleteAuthorizationSessionLocked(id)
		return ErrAuthorizationSessionInactive
	}
	if session.AccountID != accountID || !authorizationSessions.consuming[id] {
		return ErrAuthorizationSessionInactive
	}
	if err := commit(); err != nil {
		delete(authorizationSessions.consuming, id)
		return err
	}
	deleteAuthorizationSessionLocked(id)
	return nil
}

// CommitLegacyAuthorization 将不带更换会话的旧授权请求与更换会话生命周期串行化，
// 避免更换授权开始后旧结果继续提交。
func CommitLegacyAuthorization(accountID uint, commit func() error) error {
	return commitLegacyAuthorization(accountID, snapshotLegacyAuthorizationState(accountID), commit)
}

func commitLegacyAuthorization(accountID uint, startedState legacyAuthorizationState, commit func() error) error {
	now := time.Now().Unix()
	authorizationSessions.Lock()
	defer authorizationSessions.Unlock()
	cleanupExpiredAuthorizationSessionsLocked(now)
	currentState := snapshotLegacyAuthorizationState(accountID)
	if startedState.replacementActive || currentState.generation != startedState.generation {
		if currentState.replacementActive {
			return ErrAuthorizationSessionActive
		}
		return ErrAuthorizationSessionInactive
	}
	if currentState.replacementActive {
		return ErrAuthorizationSessionActive
	}

	if activeID := authorizationSessions.activeByAccount[accountID]; activeID != "" {
		if _, ok := authorizationSessions.items[activeID]; ok {
			return ErrAuthorizationSessionActive
		}
		delete(authorizationSessions.activeByAccount, accountID)
		clearLegacyAuthorizationActive(accountID)
	}
	return commit()
}

// CancelAuthorizationSession 取消待提交或进行中的授权会话。
func CancelAuthorizationSession(id string, accountID uint) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	authorizationSessions.Lock()
	defer authorizationSessions.Unlock()
	session, ok := authorizationSessions.items[id]
	if !ok || session.AccountID != accountID {
		return false
	}
	if session.ExpiresAt <= time.Now().Unix() {
		deleteAuthorizationSessionLocked(id)
		return false
	}
	// CommitAuthorizationSession 执行数据库写入时持有同一把互斥锁，
	// 因此取消操作不会与正在执行的回调产生竞态。此处删除会话后，即使已取消的
	// 回调不再返回 CommitAuthorizationSession，也能立即释放账号占用。
	deleteAuthorizationSessionLocked(id)
	return true
}

func DeleteAuthorizationSession(id string) {
	authorizationSessions.Lock()
	id = strings.TrimSpace(id)
	deleteAuthorizationSessionLocked(id)
	authorizationSessions.Unlock()
}

var (
	ErrAuthorizationSessionInactive = errors.New("授权会话不存在、已过期或已取消")
	ErrAuthorizationSessionActive   = errors.New("该账号已有授权会话进行中")
)

func ResetAuthorizationSessionsForTest() {
	authorizationSessions.Lock()
	authorizationSessions.items = map[string]AuthorizationSession{}
	authorizationSessions.consuming = map[string]bool{}
	authorizationSessions.activeByAccount = map[uint]string{}
	authorizationSessions.Unlock()
	legacyAuthorizationStates.Lock()
	legacyAuthorizationStates.items = map[uint]legacyAuthorizationState{}
	legacyAuthorizationStates.Unlock()
}
