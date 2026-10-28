package models

import (
	"context"
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/playback"
	"qmediasync/internal/v115auth"
)

// 保持一个即将发布的旧身份作业，检查提交通知与发布门禁的真实配合。
func activeAccountPreload(t *testing.T, account *Account) (*playback.PreloadTask, func() bool) {
	t.Helper()
	previous := playback.DefaultPreloader
	preloader := playback.NewPreloader()
	playback.DefaultPreloader = preloader
	preloader.Apply(true, 1)
	preloader.Start(t.Context())
	started := make(chan *playback.PreloadTask, 1)
	finish := make(chan struct{})
	published := make(chan bool, 1)
	if !preloader.Submit(playback.PreloadCandidate{
		Key: "old-account-source", Scope: "authenticated-device", Kind: playback.PreloadNext,
		Account: playback.PreloadAccount{Provider: string(account.SourceType), AccountID: account.ID, UserID: account.UserId},
		Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			if !task.BeginSignature(playback.PreloadAccount{Provider: string(account.SourceType), AccountID: account.ID, UserID: account.UserId}, "old-account-source") {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}
			started <- task
			select {
			case <-finish:
			case <-ctx.Done():
			}
			published <- task.Publish(func() {})
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		},
	}) {
		t.Fatal("旧身份作业未接纳")
	}
	t.Cleanup(func() {
		preloader.Stop()
		playback.DefaultPreloader = previous
	})
	var task *playback.PreloadTask
	select {
	case task = <-started:
	case <-time.After(time.Second):
		t.Fatal("旧身份作业未启动")
	}
	return task, func() bool {
		close(finish)
		select {
		case ok := <-published:
			return ok
		case <-time.After(time.Second):
			t.Fatal("旧身份作业未结束")
			return false
		}
	}
}

func TestAccountCommitCancelsOldPreload(t *testing.T) {
	source := v115auth.Source{SourceType: v115auth.SourceTypeBuiltInAppID, Provider: v115auth.ProviderOfficialPKCE, AppID: "100197849"}
	for _, test := range []struct {
		name string
		run  func(*testing.T, *Account)
	}{
		{"token_refresh", func(t *testing.T, account *Account) {
			if !account.UpdateToken("new-token", "new-refresh", 3600) {
				t.Fatal("更新 Token 失败")
			}
		}},
		{"conditional_refresh", func(t *testing.T, account *Account) {
			if err := account.TryUpdateTokenIfCurrent("new-token", "new-refresh", 3600); err != nil {
				t.Fatal(err)
			}
		}},
		{"clear_token", func(t *testing.T, account *Account) { account.ClearToken("invalid") }},
		{"conditional_clear", func(t *testing.T, account *Account) {
			if !account.ClearTokenIfCredentialsMatch("old-token", "old-refresh", "invalid") {
				t.Fatal("条件清除失败")
			}
		}},
		{"user_identity", func(t *testing.T, account *Account) {
			if !account.UpdateUser("new-user", "new name") {
				t.Fatal("身份更新失败")
			}
		}},
		{"authorization", func(t *testing.T, account *Account) {
			if err := account.ReplaceV115Authorization(source, "new-token", "new-refresh", 3600, "new-user", "new name"); err != nil {
				t.Fatal(err)
			}
		}},
		{"legacy_authorization", func(t *testing.T, account *Account) {
			if err := account.UpdateV115Authorization(source, "new-token", "new-refresh", 3600, "new-user", "new name"); err != nil {
				t.Fatal(err)
			}
		}},
		{"existing_full_account", func(t *testing.T, account *Account) {
			if CreateAccountFull(SourceType115, "new-app", "existing", "new-token", "new-refresh", account.UserId, "new name", 3600) == nil {
				t.Fatal("已有账号覆盖失败")
			}
		}},
		{"delete", func(t *testing.T, account *Account) {
			if err := db.Db.AutoMigrate(&SyncPath{}); err != nil {
				t.Fatal(err)
			}
			if err := account.Delete(); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			setupOpenListAccountTest(t)
			account := &Account{SourceType: SourceType115, AppId: "old-app", UserId: "old-user", Token: "old-token", RefreshToken: "old-refresh"}
			if err := db.Db.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			task, finish := activeAccountPreload(t, account)
			test.run(t, account)
			if task.Valid() || finish() {
				t.Fatal("账号成功提交后旧作业仍能发布")
			}
		})
	}
}

func TestAccountFailedWriteKeepsPreload(t *testing.T) {
	for _, test := range []string{"database_failure", "stale_refresh", "stale_clear", "duplicate_identity", "missing_account"} {
		t.Run(test, func(t *testing.T) {
			setupOpenListAccountTest(t)
			account := &Account{SourceType: SourceType115, UserId: "old-user", Token: "old-token", RefreshToken: "old-refresh"}
			if err := db.Db.Create(account).Error; err != nil {
				t.Fatal(err)
			}
			task, finish := activeAccountPreload(t, account)
			switch test {
			case "database_failure":
				if err := db.Db.Callback().Update().Before("gorm:update").Register("test:preload_commit_failure", func(tx *gorm.DB) { tx.AddError(errors.New("write failed")) }); err != nil {
					t.Fatal(err)
				}
				if account.UpdateToken("new", "refresh", 3600) {
					t.Fatal("失败写入被接受")
				}
			case "stale_refresh", "stale_clear":
				account.Token = "stale-token"
				if test == "stale_refresh" && account.UpdateTokenIfCurrent("new", "refresh", 3600) {
					t.Fatal("过期刷新被接受")
				}
				if test == "stale_clear" && account.ClearTokenIfCurrent("stale failure") {
					t.Fatal("过期清除被接受")
				}
			case "duplicate_identity":
				if err := db.Db.Create(&Account{SourceType: SourceType115, UserId: "occupied"}).Error; err != nil {
					t.Fatal(err)
				}
				if account.UpdateUser("occupied", "name") {
					t.Fatal("重复身份被接受")
				}
			case "missing_account":
				if err := db.Db.Delete(account).Error; err != nil {
					t.Fatal(err)
				}
				if account.UpdateUser("new-user", "name") {
					t.Fatal("不存在账号的身份更新被接受")
				}
			}
			if !task.Valid() || !finish() {
				t.Fatal("未提交的账号更新撤销了有效作业")
			}
		})
	}
}

func TestBaiduAuthorizationCancelsOldPreload(t *testing.T) {
	setupOpenListAccountTest(t)
	account := &Account{SourceType: SourceTypeBaiduPan, UserId: "old-user", Token: "old-token", RefreshToken: "old-refresh"}
	if err := db.Db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	task, finish := activeAccountPreload(t, account)
	if err := account.ReplaceBaiDuPanAuthorization("new-token", "new-refresh", 3600, "new-user", "name"); err != nil {
		t.Fatal(err)
	}
	if task.Valid() || finish() {
		t.Fatal("百度授权更新后旧作业仍能发布")
	}
}
