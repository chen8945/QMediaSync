package controllers

import (
	"fmt"
	"sync"
	"time"

	"qmediasync/internal/v115open"
)

type open115AuthState struct {
	AccountId       uint
	AuthorizationID string
	CodeData        *v115open.QrCodeDataReturn
	CreatedAt       int64
	LastStatus      v115open.QrCodeScanStatus
	TokenSaved      bool
}

var open115AuthStates = struct {
	sync.RWMutex
	items map[string]*open115AuthState
}{items: map[string]*open115AuthState{}}

func open115AuthStateKey(accountId uint, uid string) string {
	return fmt.Sprintf("%d:%s", accountId, uid)
}

func saveOpen115AuthState(accountId uint, data *v115open.QrCodeDataReturn, authorizationID ...string) {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	authID := ""
	if len(authorizationID) > 0 {
		authID = authorizationID[0]
	}
	open115AuthStates.items[open115AuthStateKey(accountId, data.Uid)] = &open115AuthState{
		AccountId:       accountId,
		AuthorizationID: authID,
		CodeData:        data,
		CreatedAt:       time.Now().Unix(),
		LastStatus:      v115open.QrCodeScanStatusNotScanned,
	}
}

func getOpen115AuthState(accountId uint, uid string) (*open115AuthState, bool) {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	key := open115AuthStateKey(accountId, uid)
	state, ok := open115AuthStates.items[key]
	if !ok {
		return nil, false
	}
	if time.Now().Unix()-state.CreatedAt > 300 {
		delete(open115AuthStates.items, key)
		return nil, false
	}
	return state, true
}

func deleteOpen115AuthState(accountId uint, uid string) {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	delete(open115AuthStates.items, open115AuthStateKey(accountId, uid))
}

func deleteOpen115AuthStatesForAuthorization(accountId uint, authorizationID string) {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	for key, state := range open115AuthStates.items {
		if state.AccountId == accountId && state.AuthorizationID == authorizationID {
			delete(open115AuthStates.items, key)
		}
	}
}

func deleteOpen115AuthStatesForAccount(accountId uint) {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	for key, state := range open115AuthStates.items {
		if state.AccountId == accountId {
			delete(open115AuthStates.items, key)
		}
	}
}

func markOpen115AuthTokenSaving(accountId uint, uid string) bool {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	state, ok := open115AuthStates.items[open115AuthStateKey(accountId, uid)]
	if !ok || state.TokenSaved {
		return false
	}
	state.TokenSaved = true
	return true
}

func resetOpen115AuthTokenSaving(accountId uint, uid string) {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	if state, ok := open115AuthStates.items[open115AuthStateKey(accountId, uid)]; ok {
		state.TokenSaved = false
	}
}

func setOpen115AuthLastStatus(accountId uint, uid string, status v115open.QrCodeScanStatus) {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	if state, ok := open115AuthStates.items[open115AuthStateKey(accountId, uid)]; ok {
		state.LastStatus = status
	}
}

// commitOpen115AuthState 将最终写入与状态失效串行化。
// 避免已经等待远端 API 返回的旧二维码请求在更换授权开始后继续提交。
func commitOpen115AuthState(accountId uint, uid string, commit func() bool) bool {
	open115AuthStates.Lock()
	defer open115AuthStates.Unlock()
	key := open115AuthStateKey(accountId, uid)
	state, ok := open115AuthStates.items[key]
	if !ok || !state.TokenSaved {
		return false
	}
	if time.Now().Unix()-state.CreatedAt > 300 {
		delete(open115AuthStates.items, key)
		return false
	}
	if !commit() {
		return false
	}
	delete(open115AuthStates.items, key)
	return true
}
