package controllers

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	v115auth "qmediasync/internal/v115/auth"
	v115open "qmediasync/internal/v115/openapi"

	"github.com/gin-gonic/gin"
)

func TestOpen115AuthStateExpires(t *testing.T) {
	open115AuthStates.Lock()
	open115AuthStates.items = map[string]*open115AuthState{}
	open115AuthStates.Unlock()
	data := &v115open.QrCodeDataReturn{Uid: "u1"}
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
	saveOpen115AuthState(1, &v115open.QrCodeDataReturn{Uid: "u2"})
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

	saveOpen115AuthState(1, &v115open.QrCodeDataReturn{Uid: "u3"})
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

func TestSave115AuthorizationRejectsNewCredentialsSafely(t *testing.T) {
	setupAccountControllerTest(t)
	var diagnostic bytes.Buffer
	previousLogger, previous115Logger := helpers.AppLogger, helpers.V115Log
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&diagnostic, "", 0)}
	helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { helpers.AppLogger, helpers.V115Log = previousLogger, previous115Logger })
	executor := v115open.GetGlobalExecutor()
	executor.Start()
	t.Cleanup(executor.Stop)

	const newAccess, newRefresh = "new-access-private-115", "new-refresh-private-115"
	account := models.Account{
		Name: "authorization-test", SourceType: models.SourceType115,
		AppId: "old-app", Token: "old-access-private-115", RefreshToken: "old-refresh-private-115",
		UserId: "100", Username: "old-user", TokenExpiriesTime: 123,
	}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	source := v115auth.Source{SourceType: v115auth.SourceTypeBuiltInAppID, Provider: v115auth.ProviderOfficialPKCE, AppID: "100197849"}
	session, err := v115auth.CreateAuthorizationSession(account.ID, source)
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/open/user/info" || r.Header.Get("Authorization") != "Bearer "+newAccess {
			t.Error("user information was not requested with the new access token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"state":false,"code":90001,"message":"authorization rejected `+newAccess+` `+newRefresh+` `+account.Token+` `+account.RefreshToken+`"}`)
	}))
	t.Cleanup(server.Close)
	transport := &http.Transport{}
	t.Cleanup(transport.CloseIdleConnections)
	client := v115open.NewClient(account.ID, source.AppID, newAccess, newRefresh)
	client.SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		request := r.Clone(r.Context())
		request.URL.Scheme, request.URL.Host = "http", strings.TrimPrefix(server.URL, "http://")
		return transport.RoundTrip(request)
	}))
	token := v115open.TokenData{AccessToken: newAccess, RefreshToken: newRefresh, ExpiresIn: 3600}
	for attempt := 0; attempt < 2; attempt++ {
		diagnostic.Reset()
		response := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(response)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/115/oauth-confirm", nil)
		if save115Authorization(c, &account, source, session.ID, client, token) {
			t.Fatal("failed user information request saved authorization")
		}
		result := decodeAPIMessage(t, response.Body)
		if response.Code != http.StatusBadGateway || result.Status != response.Code || result.ErrorCode != "UPSTREAM_ERROR" || result.Instance == "" || result.Data != nil {
			t.Fatalf("authorization failure contract: %s", response.Body)
		}
		if !strings.Contains(diagnostic.String(), result.Instance) || !strings.Contains(diagnostic.String(), "authorization rejected") {
			t.Fatal("failure lost its correlated upstream cause")
		}
		for _, secret := range []string{newAccess, newRefresh, account.Token, account.RefreshToken} {
			if strings.Contains(response.Body.String(), secret) || strings.Contains(diagnostic.String(), secret) {
				t.Error("authorization failure exposed a known credential")
			}
		}
		var stored models.Account
		if err := db.Db.First(&stored, account.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.Token != account.Token || stored.RefreshToken != account.RefreshToken || stored.AppId != account.AppId || stored.UserId != account.UserId || stored.Username != account.Username || stored.TokenExpiriesTime != account.TokenExpiriesTime {
			t.Fatal("failed authorization changed the saved account")
		}
		if _, ok := v115auth.GetAuthorizationSession(session.ID, account.ID); !ok {
			t.Fatal("failed authorization consumed the retryable session")
		}
	}
	if requests.Load() < 2 {
		t.Fatal("retry did not reach user information with the new credentials")
	}
}
