package controllers

import (
	"testing"
	"time"

	"qmediasync/internal/v115open"
)

func TestOpen115AuthStateExpires(t *testing.T) {
	open115AuthStates.Lock()
	open115AuthStates.items = map[string]*open115AuthState{}
	open115AuthStates.Unlock()
	data := &v115open.QrCodeDataReturn{QrCodeData: v115open.QrCodeData{Uid: "u1"}}
	saveOpen115AuthState(1, data)
	state, ok := getOpen115AuthState(1, "u1")
	if !ok || state.CodeData.Uid != "u1" {
		t.Fatalf("授权状态未保存成功")
	}
	state.CreatedAt = time.Now().Add(-301 * time.Second).Unix()
	if _, ok := getOpen115AuthState(1, "u1"); ok {
		t.Fatalf("过期授权状态不应继续可用")
	}
	open115AuthStates.RLock()
	remaining := len(open115AuthStates.items)
	open115AuthStates.RUnlock()
	if remaining != 0 {
		t.Fatalf("过期授权状态应从内存清理，剩余 %d 条", remaining)
	}
}

func TestMarkOpen115AuthTokenSavingOnlyOnce(t *testing.T) {
	open115AuthStates.Lock()
	open115AuthStates.items = map[string]*open115AuthState{}
	open115AuthStates.Unlock()
	saveOpen115AuthState(1, &v115open.QrCodeDataReturn{QrCodeData: v115open.QrCodeData{Uid: "u2"}})
	if !markOpen115AuthTokenSaving(1, "u2") {
		t.Fatalf("第一次标记换 token 应成功")
	}
	if markOpen115AuthTokenSaving(1, "u2") {
		t.Fatalf("重复标记换 token 应失败")
	}
	resetOpen115AuthTokenSaving(1, "u2")
	if !markOpen115AuthTokenSaving(1, "u2") {
		t.Fatalf("重置后应允许再次标记换 token")
	}
}

func TestInvalidatedOpen115AuthStateCannotCommit(t *testing.T) {
	open115AuthStates.Lock()
	open115AuthStates.items = map[string]*open115AuthState{}
	open115AuthStates.Unlock()

	saveOpen115AuthState(1, &v115open.QrCodeDataReturn{QrCodeData: v115open.QrCodeData{Uid: "u3"}})
	if !markOpen115AuthTokenSaving(1, "u3") {
		t.Fatal("标记换 token 应成功")
	}
	deleteOpen115AuthStatesForAccount(1)

	committed := false
	if commitOpen115AuthState(1, "u3", func() bool {
		committed = true
		return true
	}) {
		t.Fatal("失效后的二维码状态不应继续提交")
	}
	if committed {
		t.Fatal("失效后的二维码状态不应调用提交回调")
	}
}
