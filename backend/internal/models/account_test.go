package models

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func setupOpenListAccountTest(t *testing.T) {
	t.Helper()
	oldDB := db.Db
	oldAppLogger := helpers.AppLogger
	oldOpenListLog := helpers.OpenListLog
	t.Cleanup(func() {
		db.Db = oldDB
		helpers.AppLogger = oldAppLogger
		helpers.OpenListLog = oldOpenListLog
	})

	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	db.Db = testDB
	if err := db.Db.AutoMigrate(&Account{}); err != nil {
		t.Fatalf("迁移 Account 失败: %v", err)
	}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.OpenListLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
}

func writeOpenListResponse(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestUpdateOpenListTokenAuthClearsPassword(t *testing.T) {
	setupOpenListAccountTest(t)

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		authorization = r.Header.Get("Authorization")
		writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":7,"username":"token-user"}}`)
	}))
	defer server.Close()

	account := &Account{
		SourceType: SourceTypeOpenList,
		BaseUrl:    server.URL,
		Username:   "old-user",
		Password:   "old-password",
		Token:      "old-token",
		UserId:     "1",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	if err := account.UpdateOpenList(server.URL, "old-user", "", "new-token", "token"); err != nil {
		t.Fatalf("切换 Token 认证失败: %v", err)
	}
	if authorization != "new-token" {
		t.Fatalf("OpenList 请求 Token = %q，期望 new-token", authorization)
	}
	if account.Password != "" {
		t.Fatalf("切换 Token 认证后内存密码 = %q，期望为空", account.Password)
	}

	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取更新后的账号失败: %v", err)
	}
	if saved.Password != "" {
		t.Fatalf("切换 Token 认证后数据库密码 = %q，期望为空", saved.Password)
	}
}

func TestUpdateOpenListTokenAuthReusesToken(t *testing.T) {
	setupOpenListAccountTest(t)

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			t.Errorf("同认证方式复用凭据时不应请求 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		authorization = r.Header.Get("Authorization")
		writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":8,"username":"token-user"}}`)
	}))
	defer server.Close()

	account := &Account{
		SourceType: SourceTypeOpenList,
		BaseUrl:    server.URL,
		Username:   "old-user",
		Token:      "old-token",
		UserId:     "1",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	if err := account.UpdateOpenList(server.URL, "", "", "", "token"); err != nil {
		t.Fatalf("复用 Token 认证失败: %v", err)
	}
	if authorization != "old-token" {
		t.Fatalf("复用 Token 认证请求 Token = %q，期望 old-token", authorization)
	}
}

func TestUpdateOpenListPasswordAuthReusesCredentials(t *testing.T) {
	setupOpenListAccountTest(t)

	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" {
			t.Errorf("同认证方式复用凭据时不应请求 %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		authorization = r.Header.Get("Authorization")
		writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":9,"username":"password-user"}}`)
	}))
	defer server.Close()

	account := &Account{
		SourceType: SourceTypeOpenList,
		BaseUrl:    server.URL,
		Username:   "old-user",
		Password:   "old-password",
		Token:      "old-token",
		UserId:     "1",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	if err := account.UpdateOpenList(server.URL, "", "", "", "password"); err != nil {
		t.Fatalf("复用用户名密码认证失败: %v", err)
	}
	if authorization != "old-token" {
		t.Fatalf("复用用户名密码认证请求 Token = %q，期望 old-token", authorization)
	}
}

func TestUpdateOpenListPasswordAuthPersistsRefreshedToken(t *testing.T) {
	setupOpenListAccountTest(t)

	var loginCount int
	var meTokens []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			loginCount++
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"token":"refreshed-token"}}`)
		case "/api/me":
			meToken := r.Header.Get("Authorization")
			meTokens = append(meTokens, meToken)
			if meToken == "old-token" {
				writeOpenListResponse(w, `{"code":401,"message":"token expired","data":null}`)
				return
			}
			if meToken != "refreshed-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":11,"username":"password-user"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	account := &Account{
		SourceType: SourceTypeOpenList,
		BaseUrl:    server.URL,
		Username:   "old-user",
		Password:   "old-password",
		Token:      "old-token",
		UserId:     "1",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	if err := account.UpdateOpenList(server.URL, "", "", "", "password"); err != nil {
		t.Fatalf("复用密码认证并刷新 Token 失败: %v", err)
	}
	if loginCount != 1 {
		t.Fatalf("自动刷新登录次数 = %d，期望 1", loginCount)
	}
	if len(meTokens) != 2 || meTokens[0] != "old-token" || meTokens[1] != "refreshed-token" {
		t.Fatalf("/api/me 使用的 Token = %#v，期望 [old-token refreshed-token]", meTokens)
	}
	if account.Token != "refreshed-token" {
		t.Fatalf("自动刷新后内存 Token = %q，期望 refreshed-token", account.Token)
	}

	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取自动刷新后的账号失败: %v", err)
	}
	if saved.Token != "refreshed-token" {
		t.Fatalf("自动刷新后数据库 Token = %q，期望 refreshed-token", saved.Token)
	}
}

func TestUpdateOpenListPasswordAuthSwitchesFromToken(t *testing.T) {
	setupOpenListAccountTest(t)

	var loginUsername string
	var loginPassword string
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			var request struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("解析登录请求失败: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			loginUsername = request.Username
			loginPassword = request.Password
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"token":"password-token"}}`)
		case "/api/me":
			authorization = r.Header.Get("Authorization")
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":10,"username":"password-user"}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	account := &Account{
		SourceType: SourceTypeOpenList,
		BaseUrl:    server.URL,
		Username:   "old-user",
		Token:      "old-token",
		UserId:     "1",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}

	if err := account.UpdateOpenList(server.URL, "new-user", "new-password", "", "password"); err != nil {
		t.Fatalf("切换用户名密码认证失败: %v", err)
	}
	if loginUsername != "new-user" || loginPassword != "new-password" {
		t.Fatalf("登录凭据 = %q/%q，期望 new-user/new-password", loginUsername, loginPassword)
	}
	if authorization != "password-token" {
		t.Fatalf("密码认证请求 Token = %q，期望 password-token", authorization)
	}
	if account.Password != "new-password" || account.Token != "password-token" {
		t.Fatalf("切换后认证材料 = %q/%q，期望 new-password/password-token", account.Password, account.Token)
	}

	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取切换后的账号失败: %v", err)
	}
	if saved.Password != "new-password" || saved.Token != "password-token" {
		t.Fatalf("数据库认证材料 = %q/%q，期望 new-password/password-token", saved.Password, saved.Token)
	}
}

func TestUpdateOpenListAuthSwitchRequiresNewCredentials(t *testing.T) {
	t.Run("密码切换到 Token", func(t *testing.T) {
		account := &Account{
			Username: "old-user",
			Password: "old-password",
			Token:    "old-token",
			BaseUrl:  "http://old.example.com",
		}
		err := account.UpdateOpenList(account.BaseUrl, "", "", "", "token")
		if err == nil {
			t.Fatal("切换到 Token 时缺少新 Token，期望返回错误")
		}
		if account.Password != "old-password" || account.Token != "old-token" {
			t.Fatalf("失败更新不应修改旧认证材料: %q/%q", account.Password, account.Token)
		}
	})

	t.Run("Token 切换到密码", func(t *testing.T) {
		account := &Account{
			Username: "old-user",
			Token:    "old-token",
			BaseUrl:  "http://old.example.com",
		}
		err := account.UpdateOpenList(account.BaseUrl, "old-user", "", "", "password")
		if err == nil {
			t.Fatal("切换到用户名密码时缺少新密码，期望返回错误")
		}
		if account.Password != "" || account.Token != "old-token" {
			t.Fatalf("失败更新不应修改旧认证材料: %q/%q", account.Password, account.Token)
		}
	})

	t.Run("Token 切换到密码不能复用旧用户名", func(t *testing.T) {
		account := &Account{
			Username: "old-user",
			Token:    "old-token",
			BaseUrl:  "http://old.example.com",
		}
		err := account.UpdateOpenList(account.BaseUrl, "", "new-password", "", "password")
		if err == nil {
			t.Fatal("切换到用户名密码时缺少新用户名，期望返回错误")
		}
		if account.Password != "" || account.Token != "old-token" {
			t.Fatalf("失败更新不应修改旧认证材料: %q/%q", account.Password, account.Token)
		}
	})
}
