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
	"qmediasync/internal/v115auth"
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

func TestAccountIdentityIndexesAllowTemporaryEmptyValuesAndRejectDuplicates(t *testing.T) {
	setupOpenListAccountTest(t)

	if !db.Db.Migrator().HasIndex(&Account{}, "idx_account_name") || !db.Db.Migrator().HasIndex(&Account{}, "idx_account_user_id") {
		t.Fatal("Account 应创建 Name 和 UserId 非空唯一索引")
	}
	if err := db.Db.Create([]*Account{
		{Name: "", UserId: ""},
		{Name: "", UserId: ""},
	}).Error; err != nil {
		t.Fatalf("临时账号的空 Name/UserId 不应互相冲突: %v", err)
	}
	if err := db.Db.Create(&Account{Name: "家庭账号", UserId: "user-1"}).Error; err != nil {
		t.Fatalf("创建首个账号失败: %v", err)
	}
	if err := db.Db.Create(&Account{Name: "家庭账号", UserId: "user-2"}).Error; err == nil {
		t.Fatal("重复 Name 应被唯一索引拒绝")
	}
	if err := db.Db.Create(&Account{Name: "另一账号", UserId: "user-1"}).Error; err == nil {
		t.Fatal("重复 UserId 应被唯一索引拒绝")
	}
}

func TestReplaceV115AuthorizationRejectsDuplicateUserID(t *testing.T) {
	setupOpenListAccountTest(t)
	account := &Account{
		Name:         "待更换账号",
		SourceType:   SourceType115,
		AppId:        "old-app",
		Token:        "old-token",
		RefreshToken: "old-refresh",
		UserId:       "old-user",
	}
	other := &Account{Name: "其他账号", SourceType: SourceType115, UserId: "occupied-user"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建待更换账号失败: %v", err)
	}
	if err := db.Db.Create(other).Error; err != nil {
		t.Fatalf("创建其他账号失败: %v", err)
	}

	source := v115auth.Source{
		SourceType: v115auth.SourceTypeBuiltInAppID,
		Provider:   v115auth.ProviderOfficialPKCE,
		AppID:      "100197849",
		AppName:    "QMediaSync",
	}
	if err := account.ReplaceV115Authorization(source, "new-token", "new-refresh", 3600, "occupied-user", "新用户"); err == nil {
		t.Fatal("重复 UserId 应阻止授权替换")
	}
	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if saved.AppId != "old-app" || saved.Token != "old-token" || saved.RefreshToken != "old-refresh" || saved.UserId != "old-user" {
		t.Fatalf("唯一性冲突不应覆盖旧授权: %+v", saved)
	}
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

func TestReplaceV115AuthorizationKeepsAccountIdentityAndUpdatesAllAuthFields(t *testing.T) {
	setupOpenListAccountTest(t)

	account := &Account{
		Name:              "原账号",
		SourceType:        SourceType115,
		AppId:             "old-app",
		AppIdName:         "旧应用",
		AuthSourceType:    v115auth.SourceTypeBuiltInAppID,
		AuthProvider:      v115auth.ProviderOfficialPKCE,
		Token:             "old-token",
		RefreshToken:      "old-refresh",
		UserId:            "old-user",
		Username:          "旧用户",
		TokenFailedReason: "旧错误",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}
	originalID := account.ID

	source := v115auth.Source{
		SourceType: v115auth.SourceTypeBuiltInAppID,
		Provider:   v115auth.ProviderOfficialPKCE,
		AppID:      "100197849",
		AppName:    "QMediaSync",
	}
	if err := account.ReplaceV115Authorization(source, "new-token", "new-refresh", 3600, "same-or-different-user", "新用户"); err != nil {
		t.Fatalf("替换 115 授权失败: %v", err)
	}

	var saved Account
	if err := db.Db.First(&saved, originalID).Error; err != nil {
		t.Fatalf("读取替换后的账号失败: %v", err)
	}
	if saved.ID != originalID || saved.Name != "原账号" {
		t.Fatalf("账号身份被改变: %#v", saved)
	}
	if saved.AppId != source.AppID || saved.AppIdName != source.AppName || saved.AuthProvider != source.Provider ||
		saved.Token != "new-token" || saved.RefreshToken != "new-refresh" || saved.UserId != "same-or-different-user" || saved.Username != "新用户" || saved.TokenFailedReason != "" {
		t.Fatalf("授权字段未完整原子更新: %#v", saved)
	}
}

func TestStaleTokenRefreshCannotOverwriteNewAuthorization(t *testing.T) {
	setupOpenListAccountTest(t)
	account := &Account{
		SourceType:   SourceType115,
		AppId:        "old-app",
		Token:        "old-token",
		RefreshToken: "old-refresh",
		UserId:       "old-user",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建旧授权账号失败: %v", err)
	}
	staleAccount := *account

	replacement := &Account{}
	if err := db.Db.First(replacement, account.ID).Error; err != nil {
		t.Fatalf("读取更换授权账号失败: %v", err)
	}
	source := v115auth.Source{
		SourceType: v115auth.SourceTypeBuiltInAppID,
		Provider:   v115auth.ProviderOfficialPKCE,
		AppID:      "100197849",
		AppName:    "QMediaSync",
	}
	if err := replacement.ReplaceV115Authorization(source, "new-token", "new-refresh", 3600, "new-user", "新用户"); err != nil {
		t.Fatalf("替换授权失败: %v", err)
	}

	if staleAccount.UpdateTokenIfCurrent("stale-token", "stale-refresh", 3600) {
		t.Fatal("旧刷新结果不应覆盖新授权")
	}
	if staleAccount.ClearTokenIfCurrent("旧刷新失败") {
		t.Fatal("旧刷新失败不应清空新授权")
	}

	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取最终账号失败: %v", err)
	}
	if saved.Token != "new-token" || saved.RefreshToken != "new-refresh" || saved.UserId != "new-user" {
		t.Fatalf("旧刷新结果改变了新授权: %#v", saved)
	}
}

func TestReplaceV115AuthorizationRejectsDeprecatedTargetWithoutWriting(t *testing.T) {
	setupOpenListAccountTest(t)
	account := &Account{SourceType: SourceType115, AppId: "old-app", Token: "old-token", RefreshToken: "old-refresh"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建测试账号失败: %v", err)
	}
	deprecated := v115auth.Source{SourceType: v115auth.SourceTypeBuiltInRelay, Provider: v115auth.ProviderMQFamily, AppID: v115auth.BuiltInRelayQ115STRM, Deprecated: true}
	if err := account.ReplaceV115Authorization(deprecated, "new-token", "new-refresh", 60, "user", "name"); err == nil {
		t.Fatal("已废弃目标应被拒绝")
	}
	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取账号失败: %v", err)
	}
	if saved.AppId != "old-app" || saved.Token != "old-token" || saved.UserId != "" {
		t.Fatalf("拒绝目标时不应写入旧账号: %#v", saved)
	}
}

func TestUpdateV115AuthorizationKeepsLegacyDeprecatedSourceUsable(t *testing.T) {
	setupOpenListAccountTest(t)
	account := &Account{
		SourceType:   SourceType115,
		AppId:        "100197665",
		Token:        "old-token",
		RefreshToken: "old-refresh",
		UserId:       "old-user",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建历史账号失败: %v", err)
	}
	deprecated := v115auth.Source{SourceType: v115auth.SourceTypeBuiltInAppID, Provider: v115auth.ProviderOfficialPKCE, AppID: "100197665", AppName: "Q115-STRM", Deprecated: true}
	if err := account.UpdateV115Authorization(deprecated, "new-token", "new-refresh", 60, "new-user", "新用户"); err != nil {
		t.Fatalf("旧授权路径不应拒绝废弃来源: %v", err)
	}
	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取历史账号失败: %v", err)
	}
	if saved.Token != "new-token" || saved.RefreshToken != "new-refresh" || saved.UserId != "new-user" || saved.Username != "新用户" {
		t.Fatalf("历史授权路径未完整更新凭据: %#v", saved)
	}
}

func TestReplaceBaiDuPanAuthorizationIsAtomic(t *testing.T) {
	setupOpenListAccountTest(t)
	account := &Account{
		SourceType:        SourceTypeBaiduPan,
		AppId:             "baidu-app",
		Token:             "old-token",
		RefreshToken:      "old-refresh",
		UserId:            "old-user",
		Username:          "旧用户",
		TokenFailedReason: "旧错误",
	}
	other := &Account{SourceType: SourceTypeBaiduPan, UserId: "occupied-user"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatalf("创建百度账号失败: %v", err)
	}
	if err := db.Db.Create(other).Error; err != nil {
		t.Fatalf("创建其他百度账号失败: %v", err)
	}

	if err := account.ReplaceBaiDuPanAuthorization("new-token", "new-refresh", 3600, "occupied-user", "新用户"); err == nil {
		t.Fatal("重复百度用户 ID 应阻止凭据和用户信息一起更新")
	}
	var saved Account
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取冲突后的百度账号失败: %v", err)
	}
	if saved.Token != "old-token" || saved.RefreshToken != "old-refresh" || saved.UserId != "old-user" || saved.Username != "旧用户" || saved.TokenFailedReason != "旧错误" {
		t.Fatalf("百度授权冲突不应产生部分更新: %#v", saved)
	}

	if err := account.ReplaceBaiDuPanAuthorization("new-token", "new-refresh", 3600, "new-user", "新用户"); err != nil {
		t.Fatalf("百度授权原子更新失败: %v", err)
	}
	if err := db.Db.First(&saved, account.ID).Error; err != nil {
		t.Fatalf("读取成功后的百度账号失败: %v", err)
	}
	if saved.Token != "new-token" || saved.RefreshToken != "new-refresh" || saved.UserId != "new-user" || saved.Username != "新用户" || saved.TokenFailedReason != "" {
		t.Fatalf("百度授权字段未完整更新: %#v", saved)
	}
}

func TestCreateAccountFullReusesUniqueUserID(t *testing.T) {
	setupOpenListAccountTest(t)

	first := CreateAccountFull(SourceType115, "app", "第一个账号", "token-1", "refresh-1", "same-user", "用户", 3600)
	second := CreateAccountFull(SourceType115, "app", "第二个账号", "token-2", "refresh-2", "same-user", "用户", 3600)
	if first == nil || second == nil {
		t.Fatal("相同 user_id 的账号应复用已有记录")
	}
	if first.ID != second.ID {
		t.Fatalf("相同 user_id 应复用同一记录: first=%d second=%d", first.ID, second.ID)
	}
	var count int64
	if err := db.Db.Model(&Account{}).Where("user_id = ?", "same-user").Count(&count).Error; err != nil {
		t.Fatalf("统计 user_id 失败: %v", err)
	}
	if count != 1 {
		t.Fatalf("相同 user_id 应只保留一条记录，实际 %d 条", count)
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
