package models

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/openlist"
)

func setupOpenListAuthorizationTest(t *testing.T) {
	t.Helper()
	setupOpenListAccountTest(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	helpers.InitEventBus()
	t.Cleanup(helpers.InitEventBus)
}

func createOpenListAuthorizationTestAccount(t *testing.T, baseURL string) *Account {
	t.Helper()
	account := &Account{
		SourceType: SourceTypeOpenList,
		Name:       "原账号",
		BaseUrl:    baseURL,
		Username:   "old-user",
		Password:   "old-password",
		Token:      "old-token",
		UserId:     "1",
	}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func loadOpenListAuthorizationTestAccount(t *testing.T, accountID uint) Account {
	t.Helper()
	var account Account
	if err := db.Db.First(&account, accountID).Error; err != nil {
		t.Fatal(err)
	}
	return account
}

func waitForOpenListAuthorizationSignal(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("OpenList 请求未到达受控边界")
	}
}

func TestOpenListRefreshPreservesSavedConfiguration(t *testing.T) {
	for _, stage := range []string{"login", "save_event"} {
		t.Run(stage, func(t *testing.T) {
			setupOpenListAuthorizationTest(t)
			ready, gate := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			block := func() { close(ready); <-gate }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/old/api/auth/login":
					if stage == "login" {
						block()
					}
					writeOpenListResponse(w, `{"code":200,"message":"success","data":{"token":"login-token"}}`)
				case "/old/api/me":
					if r.Header.Get("Authorization") != "login-token" {
						writeOpenListResponse(w, `{"code":401,"message":"expired","data":null}`)
						return
					}
					writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":1,"username":"old-user"}}`)
				case "/new/api/me":
					if r.Header.Get("Authorization") != "new-token" {
						t.Error("新配置收到了旧登录结果")
					}
					writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":2,"username":"new-user"}}`)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			account := createOpenListAuthorizationTestAccount(t, server.URL+"/old")
			client := account.GetOpenListClient()
			helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(event helpers.Event) helpers.EventResult {
				if stage == "save_event" {
					block()
				}
				return HandleOpenListTokenSaveSync(event)
			})
			var requests sync.WaitGroup
			defer func() { release(); requests.Wait(); server.Close() }()
			requests.Go(func() {
				if _, err := client.GetUserInfo(""); err != nil {
					t.Errorf("原地址的在途请求应能完成：%v", err)
				}
			})
			waitForOpenListAuthorizationSignal(t, ready)
			if err := account.UpdateOpenList(server.URL+"/new", "new-user", "", "new-token", "token"); err != nil {
				t.Fatalf("保存新配置失败：%v", err)
			}
			want := loadOpenListAuthorizationTestAccount(t, account.ID)
			release()
			requests.Wait()
			if got := loadOpenListAuthorizationTestAccount(t, account.ID); !reflect.DeepEqual(got, want) {
				t.Error("迟到的登录或保存事件改变了数据库中的新配置")
			}
			if got := client.GetAuthToken(); got != "new-token" {
				t.Errorf("迟到登录改变了共享 Token：%q", got)
			}
			if _, err := client.GetUserInfo(""); err != nil {
				t.Errorf("新地址请求失败：%v", err)
			}
		})
	}
}

func TestOpenListTokenSaveChecksOriginalCredentialsAtWrite(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]any
		deleted bool
	}{
		{name: "正常刷新"},
		{name: "地址变化但 Token 相同", changes: map[string]any{"base_url": "http://new.invalid"}},
		{name: "密码变化但 Token 相同", changes: map[string]any{"password": "new-password"}},
		{name: "用户名变化但 Token 相同", changes: map[string]any{"username": "new-user"}},
		{name: "Token 已更新", changes: map[string]any{"token": "new-token"}},
		{name: "来源已变化", changes: map[string]any{"source_type": SourceType115}},
		{name: "账号已删除", deleted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupOpenListAuthorizationTest(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeOpenListResponse(w, `{"code":200,"message":"success","data":{"token":"login-token"}}`)
			}))
			defer server.Close()
			account := createOpenListAuthorizationTestAccount(t, server.URL)
			client := account.GetOpenListClient()
			events := make(chan helpers.Event, 1)
			helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(event helpers.Event) helpers.EventResult {
				events <- event
				return helpers.EventResult{Success: true}
			})
			if _, err := client.GetToken(); err != nil {
				t.Fatal(err)
			}
			event := <-events
			ready, gate := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			// 在模型已经拿到事件、构造写入条件后改库，验证守卫由 UPDATE 原子判定。
			if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("test:pause_openlist_token_save", func(tx *gorm.DB) {
				if fields, ok := tx.Statement.Dest.(map[string]any); ok && fields["token"] == "login-token" {
					close(ready)
					<-gate
				}
			}); err != nil {
				t.Fatal(err)
			}
			var requests sync.WaitGroup
			defer func() { release(); requests.Wait() }()
			requests.Go(func() { HandleOpenListTokenSaveSync(event) })
			waitForOpenListAuthorizationSignal(t, ready)
			if tc.deleted {
				if err := db.Db.Delete(account).Error; err != nil {
					t.Fatal(err)
				}
			} else if tc.changes != nil {
				if err := db.Db.Model(&Account{}).Where("id = ?", account.ID).Updates(tc.changes).Error; err != nil {
					t.Fatal(err)
				}
			}
			release()
			requests.Wait()
			if tc.deleted {
				var count int64
				if err := db.Db.Model(&Account{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("迟到事件重建了已删除账号：count=%d, err=%v", count, err)
				}
				return
			}
			saved := loadOpenListAuthorizationTestAccount(t, account.ID)
			wantToken := "old-token"
			if tc.changes == nil {
				wantToken = "login-token"
				if saved.TokenExpiriesTime <= time.Now().Unix() || client.GetAuthToken() != "login-token" {
					t.Error("正常刷新未更新数据库有效期或共享 Token")
				}
			} else if token, ok := tc.changes["token"].(string); ok {
				wantToken = token
			}
			if saved.Token != wantToken {
				t.Errorf("保存事件 Token = %q，期望 %q", saved.Token, wantToken)
			}
		})
	}
}

func TestUpdateOpenListFailedValidationOrSavePreservesAuthorization(t *testing.T) {
	for _, stage := range []string{"login", "user_info", "save"} {
		t.Run(stage, func(t *testing.T) {
			setupOpenListAuthorizationTest(t)
			var logins, saves atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/auth/login":
					if logins.Add(1) > 1 || stage == "login" {
						writeOpenListResponse(w, `{"code":401,"message":"invalid credentials","data":null}`)
						return
					}
					writeOpenListResponse(w, `{"code":200,"message":"success","data":{"token":"candidate-token"}}`)
				case "/api/me":
					if stage == "user_info" {
						writeOpenListResponse(w, `{"code":401,"message":"invalid token","data":null}`)
						return
					}
					writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":2,"username":"new-user"}}`)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			account := createOpenListAuthorizationTestAccount(t, server.URL)
			original := *account
			client := account.GetOpenListClient()
			helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(event helpers.Event) helpers.EventResult {
				saves.Add(1)
				return HandleOpenListTokenSaveSync(event)
			})
			if stage == "save" {
				if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("test:fail_openlist_save", func(tx *gorm.DB) {
					_ = tx.AddError(errors.New("injected account save failure"))
				}); err != nil {
					t.Fatal(err)
				}
			}
			if err := account.UpdateOpenList(server.URL, "new-user", "new-password", "", "password"); err == nil {
				t.Fatal("无效验证或数据库失败应阻止保存")
			}
			if !reflect.DeepEqual(*account, original) {
				t.Error("失败验证或保存修改了调用方账号")
			}
			if saved := loadOpenListAuthorizationTestAccount(t, account.ID); !reflect.DeepEqual(saved, original) {
				t.Error("失败验证或保存修改了数据库中的原授权")
			}
			if client.GetAuthToken() != original.Token || client.Username != original.Username || client.Password != original.Password {
				t.Error("失败验证或保存修改了共享客户端的原授权")
			}
			if got := saves.Load(); got != 0 {
				t.Errorf("候选凭据验证触发了 %d 次 Token 保存事件", got)
			}
		})
	}
}

func TestUpdateOpenListRejectsStaleEdit(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprintf("account_deleted=%t", deleted), func(t *testing.T) {
			setupOpenListAuthorizationTest(t)
			ready, gate := make(chan struct{}), make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/me" {
					t.Errorf("意外请求：%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.Header.Get("Authorization") == "slow-token" {
					close(ready)
					<-gate
					writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":2,"username":"slow-user"}}`)
					return
				}
				writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":3,"username":"new-user"}}`)
			}))
			account := createOpenListAuthorizationTestAccount(t, server.URL)
			original := *account
			client := account.GetOpenListClient()
			var requests sync.WaitGroup
			defer func() { release(); requests.Wait(); server.Close() }()
			requests.Go(func() {
				if err := account.UpdateOpenList(server.URL, "slow-user", "", "slow-token", "token"); err == nil {
					t.Error("慢编辑不应覆盖新配置或重建已删除账号")
				}
			})
			waitForOpenListAuthorizationSignal(t, ready)
			var want Account
			if deleted {
				if err := db.Db.Delete(&Account{}, original.ID).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				newer := loadOpenListAuthorizationTestAccount(t, original.ID)
				if err := newer.UpdateOpenList(server.URL, "new-user", "", "new-token", "token"); err != nil {
					t.Fatal(err)
				}
				want = loadOpenListAuthorizationTestAccount(t, original.ID)
			}
			release()
			requests.Wait()
			if !reflect.DeepEqual(*account, original) {
				t.Error("拒绝慢编辑后修改了调用方的旧快照")
			}
			if deleted {
				var count int64
				if err := db.Db.Model(&Account{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("慢编辑重建了已删除账号：count=%d, err=%v", count, err)
				}
				return
			}
			if got := loadOpenListAuthorizationTestAccount(t, original.ID); !reflect.DeepEqual(got, want) {
				t.Error("慢编辑修改了数据库中的新配置")
			}
			if client.GetAuthToken() != "new-token" {
				t.Error("慢编辑修改了共享客户端的新 Token")
			}
		})
	}
}

func TestCreateOpenListAccountValidatesBeforePublishing(t *testing.T) {
	for _, tc := range []struct {
		name      string
		token     string
		failAt    string
		refresh   bool
		wantToken string
	}{
		{name: "使用已有 Token", token: "provided-token", wantToken: "provided-token"},
		{name: "使用密码登录", wantToken: "candidate-token"},
		{name: "用户信息查询刷新 Token", refresh: true, wantToken: "refreshed-token"},
		{name: "登录失败", failAt: "login"},
		{name: "用户信息验证失败", failAt: "user_info"},
		{name: "数据库保存失败", failAt: "save"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupOpenListAuthorizationTest(t)
			var logins, saves atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/auth/login":
					attempt := logins.Add(1)
					if tc.failAt == "login" || tc.failAt == "user_info" && attempt > 1 {
						writeOpenListResponse(w, `{"code":401,"message":"invalid credentials","data":null}`)
						return
					}
					token := "candidate-token"
					if attempt > 1 {
						token = "refreshed-token"
					}
					writeOpenListResponse(w, fmt.Sprintf(`{"code":200,"message":"success","data":{"token":%q}}`, token))
				case "/api/me":
					if tc.failAt == "user_info" || tc.refresh && r.Header.Get("Authorization") == "candidate-token" {
						writeOpenListResponse(w, `{"code":401,"message":"invalid token","data":null}`)
						return
					}
					writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":31,"username":"new-user"}}`)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			// 账号 ID 尚未生成的两个候选配置也不能通过缓存 0 互相覆盖。
			untouched := openlist.NewClient(0, server.URL+"/original", "original-user", "original-password", "original-token")
			helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(event helpers.Event) helpers.EventResult {
				saves.Add(1)
				return HandleOpenListTokenSaveSync(event)
			})
			if tc.failAt == "save" {
				if err := db.Db.Callback().Create().Before("gorm:begin_transaction").Register("test:fail_openlist_create", func(tx *gorm.DB) {
					_ = tx.AddError(errors.New("injected account create failure"))
				}); err != nil {
					t.Fatal(err)
				}
			}
			account, err := CreateOpenListAccount(server.URL, "new-user", "new-password", tc.token)
			if (err != nil) != (tc.failAt != "") {
				t.Fatalf("创建账号错误 = %v，失败阶段 = %q", err, tc.failAt)
			}
			if untouched.GetAuthToken() != "original-token" || untouched.BaseUrl != server.URL+"/original" {
				t.Error("验证候选账号时修改了其他客户端")
			}
			if got := saves.Load(); got != 0 {
				t.Errorf("候选账号验证发布了 %d 次 Token 保存事件", got)
			}
			var count int64
			if err := db.Db.Model(&Account{}).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if tc.failAt != "" {
				if account != nil || count != 0 {
					t.Error("失败创建留下了部分账号数据")
				}
				return
			}
			saved := loadOpenListAuthorizationTestAccount(t, account.ID)
			if count != 1 || saved.Token != tc.wantToken || account.Token != tc.wantToken || saved.UserId != "31" || saved.Name != "new-user" {
				t.Error("未同时保存验证后的 Token、配置和用户信息")
			}
			if tc.token != "" && logins.Load() != 0 {
				t.Error("已有有效 Token 不应触发登录")
			}
		})
	}
}

func TestCreateOpenListAccountsKeepCandidateClientsIsolated(t *testing.T) {
	setupOpenListAuthorizationTest(t)
	ready, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	var saves atomic.Int64
	helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, func(helpers.Event) helpers.EventResult {
		saves.Add(1)
		return helpers.EventResult{Success: true}
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a/api/auth/login":
			close(ready)
			<-gate
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"token":"token-a"}}`)
		case "/b/api/auth/login":
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"token":"token-b"}}`)
		case "/a/api/me", "/b/api/me":
			account := r.URL.Path[1:2]
			if r.Header.Get("Authorization") != "token-"+account {
				t.Error("并发创建向另一地址发送了错误 Token")
				writeOpenListResponse(w, `{"code":401,"message":"wrong token","data":null}`)
				return
			}
			userID := 1
			if account == "b" {
				userID = 2
			}
			writeOpenListResponse(w, fmt.Sprintf(`{"code":200,"message":"success","data":{"id":%d,"username":%q}}`, userID, account))
		default:
			t.Errorf("意外请求：%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	var requests sync.WaitGroup
	defer func() { release(); requests.Wait(); server.Close() }()
	var accountA *Account
	requests.Go(func() {
		var err error
		accountA, err = CreateOpenListAccount(server.URL+"/a", "a", "password-a", "")
		if err != nil {
			t.Errorf("账号 A 创建失败：%v", err)
		}
	})
	waitForOpenListAuthorizationSignal(t, ready)
	accountB, err := CreateOpenListAccount(server.URL+"/b", "b", "password-b", "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	requests.Wait()
	if accountA == nil {
		t.Fatal("账号 A 未完成创建")
	}
	if accountA.ID == accountB.ID || accountA.Token != "token-a" || accountB.Token != "token-b" || accountA.UserId != "1" || accountB.UserId != "2" {
		t.Error("并发新建账号的身份或凭据互相污染")
	}
	if saves.Load() != 0 {
		t.Error("并发新建账号发布了提前保存 Token 的事件")
	}
}
