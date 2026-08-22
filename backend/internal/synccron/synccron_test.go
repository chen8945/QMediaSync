package synccron

import (
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/baidupan"
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
	oldV115Refresh := refreshV115Token
	oldBaiduRefresh := refreshBaiduToken
	oldPersist := persistAccountToken
	oldLoadAccounts := loadAccountsForTokenRefresh
	oldRetryCount := tokenPersistRetryCount
	oldRetryDelay := tokenPersistRetryDelay
	t.Cleanup(func() {
		refreshV115Token = oldV115Refresh
		refreshBaiduToken = oldBaiduRefresh
		persistAccountToken = oldPersist
		loadAccountsForTokenRefresh = oldLoadAccounts
		tokenPersistRetryCount = oldRetryCount
		tokenPersistRetryDelay = oldRetryDelay
	})
	tokenPersistRetryCount = 2
	tokenPersistRetryDelay = time.Millisecond
	pendingTokenPersists = make(map[uint]pendingToken)
	// 清空账号表，避免测试间的账号唯一键冲突
	if err := db.Db.Where("1 = 1").Delete(&models.Account{}).Error; err != nil {
		t.Fatalf("清理账号表失败: %v", err)
	}
}

// persistViaModels 使用真实数据库写入，供测试恢复默认落库行为
func persistViaModels(account *models.Account, token string, refreshToken string, expiresIn int64) error {
	return account.TryUpdateTokenIfCurrent(token, refreshToken, expiresIn)
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

func createExpiredBaiduAccount(t *testing.T) models.Account {
	t.Helper()
	account := models.Account{
		Name:              "百度网盘账号",
		SourceType:        models.SourceTypeBaiduPan,
		Token:             "old-access-token",
		RefreshToken:      "old-refresh-token",
		TokenExpiriesTime: time.Now().Unix() - 60,
		UserId:            "baidu-user-1",
		Username:          "百度用户",
	}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatalf("创建百度网盘账号失败: %v", err)
	}
	return account
}

func TestRefreshOAuthAccessTokenBaiduKeepsCredentialsOnRetryableFailure(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredBaiduAccount(t)
	refreshBaiduToken = func(accountId uint, refreshToken string) (*baidupan.RefreshResponse, error) {
		return nil, fmt.Errorf("百度刷新访问凭证响应格式异常：<html>")
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "old-access-token" || account.RefreshToken != "old-refresh-token" {
		t.Fatal("可重试失败后不应清空百度网盘数据库凭据")
	}
	if account.TokenFailedReason != "" {
		t.Fatalf("可重试失败不应写入失败原因，实际写入：%s", account.TokenFailedReason)
	}
}

func TestRefreshOAuthAccessTokenBaiduClearsCredentialsOnDeadRefreshToken(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredBaiduAccount(t)
	refreshBaiduToken = func(accountId uint, refreshToken string) (*baidupan.RefreshResponse, error) {
		return nil, &baidupan.OAuthError{Code: "invalid_grant", Description: "Refresh Token invalid"}
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "" || account.RefreshToken != "" {
		t.Fatal("凭证失效后应清空百度网盘数据库凭据")
	}
	if account.TokenFailedReason == "" {
		t.Fatal("凭证失效后应写入失败原因")
	}
}

func TestRefreshOAuthAccessTokenBaiduUpdatesCredentialsOnSuccess(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredBaiduAccount(t)
	refreshBaiduToken = func(accountId uint, refreshToken string) (*baidupan.RefreshResponse, error) {
		return &baidupan.RefreshResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    2592000,
		}, nil
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "new-access-token" || account.RefreshToken != "new-refresh-token" {
		t.Fatal("刷新成功后应写入百度网盘新凭据")
	}
	if account.TokenExpiriesTime <= time.Now().Unix() {
		t.Fatalf("刷新成功后应更新过期时间，实际：%d", account.TokenExpiriesTime)
	}
}

func TestRefreshOAuthAccessTokenKeepsPendingTokenOnPersistFailure(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	refreshCalls := 0
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		refreshCalls++
		return &v115open.TokenData{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    7200,
		}, nil
	}
	persistAttempts := 0
	persistAccountToken = func(account *models.Account, token string, refreshToken string, expiresIn int64) error {
		persistAttempts++
		return fmt.Errorf("database is locked")
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "old-access-token" || account.RefreshToken != "old-refresh-token" {
		t.Fatal("落库失败后不应改动数据库凭据")
	}
	if account.TokenFailedReason != "" {
		t.Fatalf("落库失败不应写入失败原因，实际写入：%s", account.TokenFailedReason)
	}
	if refreshCalls != 1 {
		t.Fatalf("本轮应只刷新一次，实际刷新 %d 次", refreshCalls)
	}
	if persistAttempts != tokenPersistRetryCount {
		t.Fatalf("落库应重试 %d 次，实际尝试 %d 次", tokenPersistRetryCount, persistAttempts)
	}
	pending, ok := pendingTokenPersists[created.ID]
	if !ok {
		t.Fatal("落库失败后应保留待写记录")
	}
	if pending.token != "new-access-token" || pending.refreshToken != "new-refresh-token" {
		t.Fatal("待写记录应保存轮换后的新凭据")
	}
	if pending.expectedToken != "old-access-token" || pending.expectedRefresh != "old-refresh-token" {
		t.Fatal("待写记录应保存轮换前的旧凭据作为补写守卫")
	}
}

func TestRefreshOAuthAccessTokenReplaysPendingTokenNextCycle(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		return &v115open.TokenData{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    7200,
		}, nil
	}
	persistAccountToken = func(account *models.Account, token string, refreshToken string, expiresIn int64) error {
		return fmt.Errorf("database is locked")
	}
	RefreshOAuthAccessToken()
	if _, ok := pendingTokenPersists[created.ID]; !ok {
		t.Fatal("第一轮落库失败后应保留待写记录")
	}

	// 数据库恢复后进入下一轮：只允许补写，不允许再刷新
	persistAccountToken = persistViaModels
	refreshCalls := 0
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		refreshCalls++
		return nil, fmt.Errorf("存在待写记录时不应再次刷新")
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "new-access-token" || account.RefreshToken != "new-refresh-token" {
		t.Fatal("第二轮应补写轮换后的新凭据")
	}
	if account.TokenExpiriesTime <= time.Now().Unix() {
		t.Fatalf("补写后应更新过期时间，实际：%d", account.TokenExpiriesTime)
	}
	if refreshCalls != 0 {
		t.Fatal("存在待写记录时不应再次发起刷新")
	}
	if _, ok := pendingTokenPersists[created.ID]; ok {
		t.Fatal("补写成功后应删除待写记录")
	}
}

func TestRefreshOAuthAccessTokenDropsPendingTokenWhenCredentialsReplaced(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		return &v115open.TokenData{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    7200,
		}, nil
	}
	persistAccountToken = func(account *models.Account, token string, refreshToken string, expiresIn int64) error {
		return fmt.Errorf("database is locked")
	}
	RefreshOAuthAccessToken()
	if _, ok := pendingTokenPersists[created.ID]; !ok {
		t.Fatal("第一轮落库失败后应保留待写记录")
	}

	// 模拟落库失败期间用户重新授权
	if err := db.Db.Model(&models.Account{}).Where("id = ?", created.ID).Updates(map[string]any{
		"token":               "reauth-access-token",
		"refresh_token":       "reauth-refresh-token",
		"token_expiries_time": time.Now().Unix() + 7200,
	}).Error; err != nil {
		t.Fatalf("模拟重新授权失败: %v", err)
	}
	persistAccountToken = persistViaModels
	refreshCalls := 0
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		refreshCalls++
		return nil, fmt.Errorf("新授权凭据未到期时不应刷新")
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "reauth-access-token" || account.RefreshToken != "reauth-refresh-token" {
		t.Fatal("重新授权后的凭据不应被待写记录覆盖")
	}
	if _, ok := pendingTokenPersists[created.ID]; ok {
		t.Fatal("凭据被覆盖后应丢弃待写记录")
	}
	if refreshCalls != 0 {
		t.Fatal("本轮不应发起刷新")
	}
}

func TestRefreshOAuthAccessTokenBaiduReplaysPendingToken(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredBaiduAccount(t)
	refreshBaiduToken = func(accountId uint, refreshToken string) (*baidupan.RefreshResponse, error) {
		return &baidupan.RefreshResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    2592000,
		}, nil
	}
	persistAccountToken = func(account *models.Account, token string, refreshToken string, expiresIn int64) error {
		return fmt.Errorf("database is locked")
	}
	RefreshOAuthAccessToken()
	pending, ok := pendingTokenPersists[created.ID]
	if !ok {
		t.Fatal("百度网盘落库失败后应保留待写记录")
	}
	if pending.source != models.SourceTypeBaiduPan {
		t.Fatalf("待写记录应标记百度网盘来源，实际：%s", pending.source)
	}

	persistAccountToken = persistViaModels
	refreshCalls := 0
	refreshBaiduToken = func(accountId uint, refreshToken string) (*baidupan.RefreshResponse, error) {
		refreshCalls++
		return nil, fmt.Errorf("存在待写记录时不应再次刷新")
	}

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "new-access-token" || account.RefreshToken != "new-refresh-token" {
		t.Fatal("第二轮应补写百度网盘轮换后的新凭据")
	}
	if refreshCalls != 0 {
		t.Fatal("存在待写记录时不应再次发起百度网盘刷新")
	}
	if _, ok := pendingTokenPersists[created.ID]; ok {
		t.Fatal("补写成功后应删除待写记录")
	}
}

func TestRefreshOAuthAccessTokenDoesNotRetryPersistOnCredentialChange(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		return &v115open.TokenData{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    7200,
		}, nil
	}
	persistAttempts := 0
	persistAccountToken = func(account *models.Account, token string, refreshToken string, expiresIn int64) error {
		persistAttempts++
		// 模拟刷新期间账号被并发重新授权，守卫不再匹配
		if persistAttempts == 1 {
			if err := db.Db.Model(&models.Account{}).Where("id = ?", account.ID).Updates(map[string]any{
				"token":               "reauth-access-token",
				"refresh_token":       "reauth-refresh-token",
				"token_expiries_time": time.Now().Unix() + 7200,
			}).Error; err != nil {
				t.Fatalf("模拟并发重新授权失败: %v", err)
			}
		}
		return account.TryUpdateTokenIfCurrent(token, refreshToken, expiresIn)
	}

	RefreshOAuthAccessToken()

	if persistAttempts != 1 {
		t.Fatalf("凭据被并发覆盖后不应重试落库，实际尝试 %d 次", persistAttempts)
	}
	if _, ok := pendingTokenPersists[created.ID]; ok {
		t.Fatal("凭据被并发覆盖后不应保留待写记录")
	}
	account := reloadAccount(t, created.ID)
	if account.Token != "reauth-access-token" || account.RefreshToken != "reauth-refresh-token" {
		t.Fatal("并发重新授权的凭据不应被覆盖")
	}
}

func TestRefreshOAuthAccessTokenKeepsPendingOnAccountQueryFailure(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	// 直接预置待写记录，模拟上一轮落库失败
	pendingTokenPersists[created.ID] = pendingToken{
		source:          models.SourceType115,
		expectedToken:   "old-access-token",
		expectedRefresh: "old-refresh-token",
		token:           "new-access-token",
		refreshToken:    "new-refresh-token",
		expiresIn:       7200,
		rotatedAt:       time.Now().Unix(),
	}
	refreshCalls := 0
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		refreshCalls++
		return nil, fmt.Errorf("数据库不可读时不应刷新")
	}
	loadAccountsForTokenRefresh = func() ([]models.Account, error) {
		return nil, fmt.Errorf("database is locked")
	}

	RefreshOAuthAccessToken()

	if _, ok := pendingTokenPersists[created.ID]; !ok {
		t.Fatal("账号列表查询失败时不得清理待写记录")
	}
	if refreshCalls != 0 {
		t.Fatal("账号列表查询失败时不应发起刷新")
	}

	// 数据库恢复后待写记录仍可正常补写
	loadAccountsForTokenRefresh = models.GetAllAccount
	persistAccountToken = persistViaModels
	RefreshOAuthAccessToken()
	account := reloadAccount(t, created.ID)
	if account.Token != "new-access-token" || account.RefreshToken != "new-refresh-token" {
		t.Fatal("数据库恢复后应补写待写记录")
	}
	if _, ok := pendingTokenPersists[created.ID]; ok {
		t.Fatal("补写成功后应删除待写记录")
	}
}

func TestRefreshOAuthAccessTokenReplayAnchorsExpiryAtRotationTime(t *testing.T) {
	setupTokenRefreshTest(t)
	created := createExpiredV115Account(t)
	// 轮换发生在一小时前：补写不得把过期时间顺延为补写时刻 + 7200
	pendingTokenPersists[created.ID] = pendingToken{
		source:          models.SourceType115,
		expectedToken:   "old-access-token",
		expectedRefresh: "old-refresh-token",
		token:           "new-access-token",
		refreshToken:    "new-refresh-token",
		expiresIn:       7200,
		rotatedAt:       time.Now().Unix() - 3600,
	}
	refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
		return nil, fmt.Errorf("存在待写记录时不应刷新")
	}
	persistAccountToken = persistViaModels

	RefreshOAuthAccessToken()

	account := reloadAccount(t, created.ID)
	if account.Token != "new-access-token" {
		t.Fatal("本轮应完成补写")
	}
	anchoredExpiry := time.Now().Unix() + 3600
	if account.TokenExpiriesTime > anchoredExpiry+60 || account.TokenExpiriesTime < anchoredExpiry-60 {
		t.Fatalf("补写应按轮换时刻锚定过期时间（约 %d），实际：%d", anchoredExpiry, account.TokenExpiriesTime)
	}
}
