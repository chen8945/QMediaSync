package models

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/db"

	"github.com/glebarez/sqlite"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRecoverAdmin(t *testing.T) {
	testRecoverAdmin(t, func(t *testing.T) *gorm.DB {
		conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "recovery.db")+"?_pragma=foreign_keys(1)"), &gorm.Config{
			Logger: logger.Default.LogMode(logger.Silent),
		})
		if err != nil {
			t.Fatal(err)
		}
		sqlDB, err := conn.DB()
		if err != nil {
			t.Fatal(err)
		}
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() {
			if err := sqlDB.Close(); err != nil {
				t.Error(err)
			}
		})
		return conn
	})
}

func testRecoverAdmin(t *testing.T, openDB func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, tc := range []struct {
		name        string
		deleteAdmin bool
		brokenHash  bool
		failure     string
	}{
		{name: "reset"},
		{name: "reset with unreadable old credentials", brokenHash: true},
		{name: "delete", deleteAdmin: true},
		{name: "reset rollback", failure: "sessions"},
		{name: "delete rollback", deleteAdmin: true, failure: "delete"},
		{name: "missing authentication table", failure: "missing table"},
		{name: "missing administrator", failure: "empty"},
		{name: "multiple administrators", failure: "multiple"},
		{name: "canceled", failure: "canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := openDB(t)
			if err := conn.AutoMigrate(&User{}, &UserSession{}, &ApiKey{}, &Account{}); err != nil {
				t.Fatal(err)
			}
			hash, err := bcrypt.GenerateFromPassword([]byte("old-password-123"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			if tc.brokenHash {
				hash = []byte("damaged hash")
			}
			user := User{
				BaseModel: BaseModel{ID: 42}, Username: "admin42", Password: string(hash),
				TwoFactorEnabled: true, TwoFactorSecret: "unreadable ciphertext", TwoFactorPendingSecret: "unreadable pending ciphertext",
			}
			if tc.failure == "empty" {
				result, err := RecoverAdmin(t.Context(), conn, tc.deleteAdmin)
				if err == nil || result != nil {
					t.Fatalf("空用户表恢复应失败且不交付结果，result=%v err=%v", result != nil, err)
				}
				return
			}
			if err := conn.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			key := ApiKey{UserID: user.ID, Name: "existing", KeyHash: HashAPIKey("qms-existing-key"), KeyPrefix: "qms-exis", IsActive: true}
			if err := conn.Create(&key).Error; err != nil {
				t.Fatal(err)
			}
			for i, session := range []UserSession{
				{SessionID: "active", TokenID: "token-active", ExpiresAt: time.Now().Unix() + 3600},
				{SessionID: "expired", TokenID: "token-expired", ExpiresAt: 1},
				{SessionID: "revoked", TokenID: "token-revoked", RevokedAt: 1, RevokeReason: "logout"},
			} {
				session.UserID, session.Username, session.CSRFTokenHash = user.ID, user.Username, "csrf"
				if err := conn.Create(&session).Error; err != nil {
					t.Fatalf("创建会话 %d：%v", i, err)
				}
			}
			account := Account{Name: "business account", UserId: "42", Token: "business-token"}
			if err := conn.Create(&account).Error; err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch tc.failure {
			case "sessions":
				if err := conn.Callback().Update().Before("gorm:update").Register("fail_recovery_sessions", func(tx *gorm.DB) {
					if tx.Statement.Table == "user_sessions" {
						tx.AddError(errors.New("injected session failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			case "delete":
				if err := conn.Callback().Delete().Before("gorm:delete").Register("fail_recovery_delete", func(tx *gorm.DB) {
					if tx.Statement.Table == "users" {
						tx.AddError(errors.New("injected administrator delete failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			case "missing table":
				if err := conn.Migrator().DropTable(&ApiKey{}); err != nil {
					t.Fatal(err)
				}
			case "multiple":
				other := User{SingletonKey: 2, Username: "other", Password: "old"}
				if err := conn.Session(&gorm.Session{SkipHooks: true}).Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			result, err := RecoverAdmin(ctx, conn, tc.deleteAdmin)
			var saved User
			userErr := conn.First(&saved, user.ID).Error
			if tc.failure != "" {
				if err == nil || result != nil {
					t.Fatalf("失败不能交付新凭据，result=%v err=%v", result != nil, err)
				}
				if userErr != nil || saved.Password != user.Password || !saved.TwoFactorEnabled || saved.TwoFactorSecret != user.TwoFactorSecret || saved.TwoFactorPendingSecret != user.TwoFactorPendingSecret {
					t.Fatal("失败后管理员认证信息发生变化")
				}
				var sessions []UserSession
				if err := conn.Order("id").Find(&sessions).Error; err != nil || len(sessions) != 3 || sessions[0].RevokedAt != 0 || sessions[1].RevokedAt != 0 || sessions[2].RevokeReason != "logout" {
					t.Fatal("失败后浏览器会话发生变化")
				}
				if tc.failure != "missing table" {
					var count int64
					if err := conn.Model(&ApiKey{}).Where("key_hash = ?", key.KeyHash).Count(&count).Error; err != nil || count != 1 {
						t.Fatal("失败后 API Key 未完整保留")
					}
				}
			} else if tc.deleteAdmin {
				if err != nil || result == nil || !result.Deleted || result.Password != "" || !errors.Is(userErr, gorm.ErrRecordNotFound) {
					t.Fatalf("删除管理员结果不正确：%v", err)
				}
				for _, table := range []any{&User{}, &UserSession{}, &ApiKey{}} {
					var count int64
					if err := conn.Model(table).Count(&count).Error; err != nil || count != 0 {
						t.Fatalf("认证数据未清理：%T count=%d err=%v", table, count, err)
					}
				}
				previousDB := db.Db
				db.Db = conn
				t.Cleanup(func() { db.Db = previousDB })
				if _, err := ValidateAPIKey("qms-existing-key"); err == nil {
					t.Fatal("删除后旧 API Key 仍有效")
				}
				if _, err := CreateInitialAdmin("newadmin", "new-admin-123"); err != nil {
					t.Fatalf("删除后不能复用初始化流程：%v", err)
				}
			} else {
				if err != nil || result == nil || result.Deleted || userErr != nil || result.UserID != 42 || result.Username != user.Username {
					t.Fatalf("重置管理员结果不正确：%v", err)
				}
				if len(result.Password) != 24 || ValidateUserPassword(result.Password) != nil || bcrypt.CompareHashAndPassword([]byte(saved.Password), []byte(result.Password)) != nil {
					t.Fatal("生成密码不符合契约或未正确保存")
				}
				if bcrypt.CompareHashAndPassword([]byte(saved.Password), []byte("old-password-123")) == nil {
					t.Fatal("旧密码仍有效")
				}
				cost, err := bcrypt.Cost([]byte(saved.Password))
				if err != nil || cost != UserPasswordBcryptCost || saved.Username != user.Username || saved.TwoFactorEnabled || saved.TwoFactorSecret != "" || saved.TwoFactorPendingSecret != "" {
					t.Fatal("密码成本、用户名或两步验证状态不符合契约")
				}
				var sessions []UserSession
				if err := conn.Order("id").Find(&sessions).Error; err != nil || len(sessions) != 3 {
					t.Fatal("重置后未保留会话审计")
				}
				for _, session := range sessions {
					if session.RevokedAt == 0 {
						t.Fatal("旧会话仍未撤销")
					}
				}
				if sessions[2].RevokeReason != "logout" || sessions[2].RevokedAt != 1 {
					t.Fatal("覆盖了既有撤销审计")
				}
				var savedKey ApiKey
				if err := conn.First(&savedKey, key.ID).Error; err != nil || savedKey.KeyHash != key.KeyHash || !savedKey.IsActive {
					t.Fatal("重置后 API Key 未保留")
				}
			}
			var savedAccount Account
			if err := conn.First(&savedAccount, account.ID).Error; err != nil || savedAccount.Token != account.Token || savedAccount.UserId != account.UserId {
				t.Fatal("恢复修改了业务账号数据")
			}
		})
	}
}
