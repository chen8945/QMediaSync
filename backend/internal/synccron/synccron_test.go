package synccron

import (
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// TestMain 在所有测试前一次性初始化全局数据库和日志。
// 队列处理器 goroutine 可能跨测试存活并读取这些全局变量，
// 测试期间不再写入，避免数据竞争。
func TestMain(m *testing.M) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		os.Stderr.WriteString("打开测试数据库失败: " + err.Error() + "\n")
		os.Exit(1)
	}
	// 内存 SQLite 每个连接独立建库，固定单连接与生产 SQLite 行为一致
	sqlDB, err := testDB.DB()
	if err != nil {
		os.Stderr.WriteString("获取测试数据库底层连接失败: " + err.Error() + "\n")
		os.Exit(1)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := testDB.AutoMigrate(&models.Account{}); err != nil {
		os.Stderr.WriteString("迁移 Account 失败: " + err.Error() + "\n")
		os.Exit(1)
	}
	db.Db = testDB
	os.Exit(m.Run())
}

func TestSyncRecordRetentionDays(t *testing.T) {
	if syncRecordRetentionDays != 7 {
		t.Fatalf("syncRecordRetentionDays = %d, want 7", syncRecordRetentionDays)
	}
}

func setupTokenRefreshTest(t *testing.T) {
	t.Helper()
	oldRefresh := refreshV115Token
	t.Cleanup(func() {
		refreshV115Token = oldRefresh
	})
	// 清空账号表，避免测试间的账号唯一键冲突
	if err := db.Db.Where("1 = 1").Delete(&models.Account{}).Error; err != nil {
		t.Fatalf("清理账号表失败: %v", err)
	}
}

func createExpiredV115Account(t *testing.T) models.Account {
	t.Helper()
	account := models.Account{
		Name:              "115 账号",
		SourceType:        models.SourceType115,
		AppId:             "test-app-id",
		Token:             "old-access-token",
		RefreshToken:      "old-refresh-token",
		TokenExpiriesTime: time.Now().Unix() - 60,
		UserId:            "115-user-1",
		Username:          "115 用户",
	}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建 115 账号失败: %v", err)
	}
	return account
}

func reloadAccount(t *testing.T, id uint) models.Account {
	t.Helper()
	var account models.Account
	if err := db.Db.First(&account, id).Error; err != nil {
		t.Fatalf("查询账号失败: %v", err)
	}
	return account
}

func TestRefreshOAuthAccessTokenKeepsCredentialsOnRetryableFailure(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		return nil, v115open.NewOpenAPIError(v115open.REFRESH_TOO_FREQUENT, "刷新太频繁")
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "old-access-token" || account.RefreshToken != "old-refresh-token" {
		t.Fatal("可重试失败后不应清空数据库凭据")
	}
	if account.TokenFailedReason != "" {
		t.Fatalf("可重试失败不应写入失败原因，实际写入：%s", account.TokenFailedReason)
	}
}

func TestRefreshOAuthAccessTokenClearsCredentialsOnDeadRefreshToken(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		return nil, v115open.NewOpenAPIError(v115open.REFRESH_TOKEN_INVALID, "no auth")
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "" || account.RefreshToken != "" {
		t.Fatal("凭证失效后应清空数据库凭据")
	}
	if account.TokenFailedReason == "" {
		t.Fatal("凭证失效后应写入失败原因")
	}
}

func TestRefreshOAuthAccessTokenUpdatesCredentialsOnSuccess(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		return &v115open.TokenData{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    7200,
		}, nil
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "new-access-token" || account.RefreshToken != "new-refresh-token" {
		t.Fatal("刷新成功后应写入新凭据")
	}
	if account.TokenFailedReason != "" {
		t.Fatalf("刷新成功后不应保留失败原因，实际保留：%s", account.TokenFailedReason)
	}
	if account.TokenExpiriesTime <= time.Now().Unix() {
		t.Fatalf("刷新成功后应更新过期时间，实际：%d", account.TokenExpiriesTime)
	}
}
