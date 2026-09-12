package models

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"qmediasync/internal/db"

	"gorm.io/gorm"
)

func TestUpdateOpenListKeepsCommitAndCacheOrder(t *testing.T) {
	setupOpenListAuthorizationTest(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first/api/me":
			if r.Header.Get("Authorization") != "first-token" {
				t.Error("第一次编辑未使用候选 Token 验证")
			}
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":2,"username":"first-user"}}`)
		case "/second/api/me":
			if r.Header.Get("Authorization") != "second-token" {
				t.Error("第二次编辑未使用候选 Token 验证")
			}
			writeOpenListResponse(w, `{"code":200,"message":"success","data":{"id":3,"username":"second-user"}}`)
		default:
			t.Errorf("意外请求：%s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	account := createOpenListAuthorizationTestAccount(t, server.URL+"/old")
	client := account.GetOpenListClient()
	firstCommitted, releaseFirst := make(chan struct{}), make(chan struct{})
	secondValidated, secondDone := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseFirst) })
	// 第一条 SQL 已提交并释放 SQLite 连接，但 UpdateOpenList 尚未安装缓存。
	if err := db.Db.Callback().Update().After("gorm:commit_or_rollback_transaction").
		Register("test:pause_openlist_after_commit", func(tx *gorm.DB) {
			if fields, ok := tx.Statement.Dest.(map[string]any); ok && fields["token"] == "first-token" {
				close(firstCommitted)
				<-releaseFirst
			}
		}); err != nil {
		t.Fatal(err)
	}
	// 第二次编辑已结束 HTTP 验证和最后一次身份查询，后续只剩提交与缓存安装。
	if err := db.Db.Callback().Query().After("gorm:after_query").
		Register("test:openlist_second_validated", func(tx *gorm.DB) {
			if len(tx.Statement.Vars) > 0 && tx.Statement.Vars[0] == "3" {
				close(secondValidated)
			}
		}); err != nil {
		t.Fatal(err)
	}
	var requests sync.WaitGroup
	defer func() { release(); requests.Wait() }()
	requests.Go(func() {
		if err := account.UpdateOpenList(server.URL+"/first", "first-user", "", "first-token", "token"); err != nil {
			t.Errorf("第一次编辑失败：%v", err)
		}
	})
	waitForOpenListAuthorizationSignal(t, firstCommitted)
	secondAccount := loadOpenListAuthorizationTestAccount(t, account.ID)
	requests.Go(func() {
		defer close(secondDone)
		if err := secondAccount.UpdateOpenList(server.URL+"/second", "second-user", "", "second-token", "token"); err != nil {
			t.Errorf("第二次编辑失败：%v", err)
		}
	})
	waitForOpenListAuthorizationSignal(t, secondValidated)
	select {
	case <-secondDone:
		t.Error("第二次编辑越过了第一次编辑尚未完成的缓存安装")
	case <-time.After(time.Second):
		// Mutex 不属于 synctest 的持久阻塞；在受控提交边界验证第二次编辑保持等待。
	}
	release()
	requests.Wait()
	saved := loadOpenListAuthorizationTestAccount(t, account.ID)
	if saved.BaseUrl != server.URL+"/second" || saved.Token != "second-token" || saved.UserId != "3" {
		t.Error("第二次编辑未完整保存新授权")
	}
	if client.BaseUrl != saved.BaseUrl || client.Username != saved.Username || client.Password != saved.Password || client.GetAuthToken() != saved.Token {
		t.Error("较早提交的配置覆盖了共享缓存，导致数据库与客户端不一致")
	}
}
