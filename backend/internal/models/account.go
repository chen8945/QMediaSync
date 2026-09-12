package models

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115auth"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type Account struct {
	BaseModel
	Name              string                  `json:"name" gorm:"uniqueIndex:idx_account_name,where:name <> ''"` // 账号备注，仅供用户自己识别账号使用，非空唯一
	SourceType        SourceType              `json:"source_type"`
	AppId             string                  `json:"app_id"`
	AppIdName         string                  `json:"app_id_name"` // 自定义开放平台应用显示名，内置应用不使用该字段
	AuthSourceType    v115auth.AuthSourceType `json:"auth_source_type" gorm:"type:string;size:64"`
	AuthProvider      v115auth.AuthProvider   `json:"auth_provider" gorm:"type:string;size:64"`
	Token             string                  `json:"token" gorm:"type:string;size:512"`
	RefreshToken      string                  `json:"refresh_token" gorm:"type:string;size:512"`
	TokenExpiriesTime int64                   `json:"token_expiries_time"`
	UserId            string                  `json:"user_id" gorm:"uniqueIndex:idx_account_user_id,where:user_id <> ''"` // 账号对应的用户 ID，非空唯一
	Username          string                  `json:"username" gorm:"type:string;size:32"`                                // 网盘对应的用户名或者 OpenList 登录用户名
	Password          string                  `json:"password" gorm:"type:string;size:256"`                               // OpenList 的用户密码
	BaseUrl           string                  `json:"base_url" gorm:"type:string;size:1024"`                              // OpenList 的访问地址 HTTP[s]://ip:port
	TokenFailedReason string                  `json:"token_failed_reason" gorm:"type:string;size:256"`                    // 刷新 Token 失败的原因
}

var (
	ErrAccountNameTaken   = errors.New("账号备注已存在")
	ErrAccountUserIDTaken = errors.New("当前账号已存在，不允许添加重复账号")
)

const (
	openListAuthTypePassword = "password"
	openListAuthTypeToken    = "token"
)

// 串行化 OpenList 最终写库及缓存安装；账号间写入出现争用后改为按账号锁。
var openListAuthorizationMu sync.Mutex

func (account *Account) TableName() string {
	return "account"
}

func ensureAccountIdentityAvailable(tx *gorm.DB, accountID uint, name string, userID string) error {
	if strings.TrimSpace(name) != "" {
		query := tx.Model(&Account{}).Where("name = ?", name)
		if accountID != 0 {
			query = query.Where("id <> ?", accountID)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAccountNameTaken
		}
	}
	if strings.TrimSpace(userID) != "" {
		query := tx.Model(&Account{}).Where("user_id = ?", userID)
		if accountID != 0 {
			query = query.Where("id <> ?", accountID)
		}
		var count int64
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrAccountUserIDTaken
		}
	}
	return nil
}

func mapAccountIdentityError(err error, updateData map[string]any) error {
	if err == nil {
		return nil
	}

	errorText := strings.ToLower(err.Error())
	isDuplicate := errors.Is(err, gorm.ErrDuplicatedKey) ||
		strings.Contains(errorText, "unique constraint") ||
		strings.Contains(errorText, "unique violation") ||
		strings.Contains(errorText, "duplicate key")
	if !isDuplicate {
		return err
	}

	if strings.Contains(errorText, accountUserIDUniqueIndexName) || strings.Contains(errorText, "user_id") {
		return ErrAccountUserIDTaken
	}
	if strings.Contains(errorText, accountNameUniqueIndexName) || strings.Contains(errorText, "account.name") {
		return ErrAccountNameTaken
	}

	_, userIDChanged := updateData["user_id"]
	_, nameChanged := updateData["name"]
	if userIDChanged && !nameChanged {
		return ErrAccountUserIDTaken
	}
	if nameChanged && !userIDChanged {
		return ErrAccountNameTaken
	}
	return err
}

func updateAccountWithIdentity(accountID uint, updateData map[string]any) error {
	// 账号授权字段在一条 UPDATE 中提交，唯一索引负责并发冲突判定。
	// 不再先读后写，避免 SQLite 读快照升级为写事务时出现 SQLITE_BUSY_SNAPSHOT。
	result := db.Db.Model(&Account{}).Where("id = ?", accountID).Updates(updateData)
	if result.Error != nil {
		return mapAccountIdentityError(result.Error, updateData)
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

type accountTokenSnapshot struct {
	token        string
	refreshToken string
}

func persistAccountTokenFields(accountID uint, updateData map[string]any, expected *accountTokenSnapshot) error {
	query := db.Db.Model(&Account{}).Where("id = ?", accountID)
	if expected != nil {
		query = query.Where("token = ? AND refresh_token = ?", expected.token, expected.refreshToken)
	}
	result := query.Updates(updateData)
	if result.Error != nil {
		return result.Error
	}
	if expected != nil && result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (account *Account) applyTokenFields(token string, refreshToken string, expiresAt int64, reason string) {
	account.Token = token
	account.RefreshToken = refreshToken
	account.TokenExpiriesTime = expiresAt
	account.TokenFailedReason = reason
}

func (account *Account) updateToken(token string, refreshToken string, expiresTime int64, expected *accountTokenSnapshot) error {
	expiresAt := time.Now().Unix() + expiresTime
	if err := persistAccountTokenFields(account.ID, map[string]any{
		"token":               token,
		"refresh_token":       refreshToken,
		"token_expiries_time": expiresAt,
		"token_failed_reason": "",
	}, expected); err != nil {
		if expected != nil && errors.Is(err, gorm.ErrRecordNotFound) {
			helpers.AppLogger.Infof("账号 %d 凭据已更新，跳过过期 Token 写入", account.ID)
		} else {
			helpers.AppLogger.Errorf("更新开放平台登录凭据失败：%v", err)
		}
		return err
	}
	account.applyTokenFields(token, refreshToken, expiresAt, "")
	return nil
}

// 更新 Token 和 refreshToken
func (account *Account) UpdateToken(token string, refreshToken string, expiresTime int64) bool {
	return account.updateToken(token, refreshToken, expiresTime, nil) == nil
}

// UpdateTokenIfCurrent 仅当数据库中的凭据仍与当前账号快照一致时更新 Token。
// 用于远端刷新耗时较长的场景，避免旧刷新结果覆盖后续授权。
func (account *Account) UpdateTokenIfCurrent(token string, refreshToken string, expiresTime int64) bool {
	expected := &accountTokenSnapshot{token: account.Token, refreshToken: account.RefreshToken}
	return account.updateToken(token, refreshToken, expiresTime, expected) == nil
}

// TryUpdateTokenIfCurrent 与 UpdateTokenIfCurrent 行为一致，但返回错误，
// 便于调用方通过 IsTokenCredentialsChanged 区分凭据被并发覆盖和数据库写入失败。
func (account *Account) TryUpdateTokenIfCurrent(token string, refreshToken string, expiresTime int64) error {
	expected := &accountTokenSnapshot{token: account.Token, refreshToken: account.RefreshToken}
	return account.updateToken(token, refreshToken, expiresTime, expected)
}

// IsTokenCredentialsChanged 判断凭据写入失败是否因数据库中的凭据已被并发修改，此类失败重试无意义。
func IsTokenCredentialsChanged(err error) bool {
	return err != nil && errors.Is(err, gorm.ErrRecordNotFound)
}

// 更新开放平台账号对应的用户信息
func (account *Account) UpdateUser(userId string, username string) bool {
	err := updateAccountWithIdentity(account.ID, map[string]any{
		"user_id":  userId,
		"username": username,
	})
	if err != nil {
		helpers.AppLogger.Errorf("更新开放平台账号用户信息失败：%v", err)
		return false
	}
	account.UserId = userId
	account.Username = username
	// helpers.AppLogger.Debugf("更新开放平台账号用户信息成功：%v", account)
	return true
}

// ReplaceV115Authorization 原子替换 115 账号的授权来源、凭据和用户信息。
// 账号 ID 及其名称、同步目录和其他关联记录不会被修改。
func (account *Account) ReplaceV115Authorization(source v115auth.Source, token string, refreshToken string, expiresTime int64, userId string, username string) error {
	return account.replaceV115Authorization(source, token, refreshToken, expiresTime, userId, username, true)
}

// UpdateV115Authorization 更新旧的无会话 115 授权路径。
// 历史账号可能仍使用已废弃的来源，因此来源是否可作为更换目标不在这里判断。
func (account *Account) UpdateV115Authorization(source v115auth.Source, token string, refreshToken string, expiresTime int64, userId string, username string) error {
	return account.replaceV115Authorization(source, token, refreshToken, expiresTime, userId, username, false)
}

func (account *Account) replaceV115Authorization(source v115auth.Source, token string, refreshToken string, expiresTime int64, userId string, username string, rejectDeprecated bool) error {
	if account == nil || account.ID == 0 {
		return fmt.Errorf("账号不存在")
	}
	if account.SourceType != SourceType115 {
		return fmt.Errorf("账号来源不是 115，不能替换 115 授权")
	}
	if rejectDeprecated && source.Deprecated {
		return fmt.Errorf("已废弃的 115 授权来源不能作为更换目标")
	}
	if source.SourceType == "" || source.Provider == "" {
		return fmt.Errorf("115 授权来源无效")
	}
	if strings.TrimSpace(token) == "" || strings.TrimSpace(refreshToken) == "" {
		return fmt.Errorf("115 访问凭证不能为空")
	}

	updatedAt := time.Now().Unix() + expiresTime
	updateData := map[string]any{
		"app_id":              source.StorageAppID(),
		"app_id_name":         source.StorageAppName(),
		"auth_source_type":    source.SourceType,
		"auth_provider":       source.Provider,
		"token":               token,
		"refresh_token":       refreshToken,
		"token_expiries_time": updatedAt,
		"user_id":             userId,
		"username":            username,
		"token_failed_reason": "",
	}

	if err := updateAccountWithIdentity(account.ID, updateData); err != nil {
		helpers.AppLogger.Errorf("原子替换 115 账号授权失败：%v", err)
		return err
	}

	account.AppId = source.StorageAppID()
	account.AppIdName = source.StorageAppName()
	account.AuthSourceType = source.SourceType
	account.AuthProvider = source.Provider
	account.Token = token
	account.RefreshToken = refreshToken
	account.TokenExpiriesTime = updatedAt
	account.UserId = userId
	account.Username = username
	account.TokenFailedReason = ""
	// 提交成功后再刷新共享客户端，避免失败流程污染旧授权。
	v115open.GetClient(account.ID, account.AppId, account.Token, account.RefreshToken)
	return nil
}

// ReplaceBaiDuPanAuthorization 原子更新百度网盘凭据和用户信息。
// 依赖唯一索引判定用户身份冲突，在一条 UPDATE 中同时写入凭据与身份。
func (account *Account) ReplaceBaiDuPanAuthorization(token string, refreshToken string, expiresTime int64, userId string, username string) error {
	if account == nil || account.ID == 0 {
		return fmt.Errorf("账号不存在")
	}
	if account.SourceType != SourceTypeBaiduPan {
		return fmt.Errorf("账号来源不是百度网盘")
	}
	if strings.TrimSpace(token) == "" || strings.TrimSpace(refreshToken) == "" {
		return fmt.Errorf("百度网盘访问凭证不能为空")
	}

	updatedAt := time.Now().Unix() + expiresTime
	updateData := map[string]any{
		"token":               token,
		"refresh_token":       refreshToken,
		"token_expiries_time": updatedAt,
		"user_id":             userId,
		"username":            username,
		"token_failed_reason": "",
	}
	if err := updateAccountWithIdentity(account.ID, updateData); err != nil {
		helpers.AppLogger.Errorf("原子更新百度网盘授权失败：%v", err)
		return err
	}

	account.Token = token
	account.RefreshToken = refreshToken
	account.TokenExpiriesTime = updatedAt
	account.UserId = userId
	account.Username = username
	account.TokenFailedReason = ""
	baidupan.NewBaiDuPanClient(account.ID, account.Token)
	return nil
}

// 如果是 normal 模式，创建一个新的客户端，不启用限速器
func (account *Account) Get115Client() *v115open.OpenClient {
	return v115open.GetCachedClient(account.ID, account.AppId, account.Token, account.RefreshToken)
}

func (account *Account) V115AuthSource() v115auth.Source {
	if account.AuthSourceType != "" || account.AuthProvider != "" {
		switch account.AuthSourceType {
		case v115auth.SourceTypeBuiltInAppID:
			if source, ok := v115auth.FindSource(v115auth.SourceTypeBuiltInAppID, account.AuthProvider, account.AppId); ok {
				return source
			}
		case v115auth.SourceTypeBuiltInRelay:
			if source, ok := v115auth.FindSource(v115auth.SourceTypeBuiltInRelay, account.AuthProvider, account.AppId); ok {
				return source
			}
		case v115auth.SourceTypeThirdPartyService:
			if source, ok := v115auth.FindSource(v115auth.SourceTypeThirdPartyService, account.AuthProvider, account.AppId); ok {
				return source
			}
		case v115auth.SourceTypeCustomAppID:
			name := strings.TrimSpace(account.AppIdName)
			if name == "" {
				name = v115auth.CustomAppName
			}
			return v115auth.Source{SourceType: v115auth.SourceTypeCustomAppID, Provider: v115auth.ProviderOfficialPKCE, AppID: account.AppId, AppName: name, DisplayName: name}
		}
	}
	return v115auth.ResolveAccountSource(account.AppId, account.AppIdName)
}

func (account *Account) GetOpenListClient() *openlist.Client {
	return openlist.NewClient(account.ID, account.BaseUrl, account.Username, account.Password, account.Token)
}

func (account *Account) GetBaiDuPanClient() *baidupan.Client {
	return baidupan.NewBaiDuPanClient(account.ID, account.Token)
}

func (account *Account) Delete() error {
	// 检查是否有关联的同步目录没有删除
	syncPaths := GetAllSyncPathByAccountId(account.ID)
	if len(syncPaths) > 0 {
		helpers.AppLogger.Errorf("开放平台账号 %v 有关联的同步目录，不能删除", account.ID)
		return fmt.Errorf("开放平台账号 %v 有关联的同步目录，不能删除", account.ID)
	}

	err := db.Db.Delete(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("删除开放平台账号失败：%v", err)
		return err
	}
	return nil
}

func (account *Account) clearToken(reason string, expected *accountTokenSnapshot) bool {
	if err := persistAccountTokenFields(account.ID, map[string]any{
		"token":               "",
		"refresh_token":       "",
		"token_expiries_time": 0,
		"token_failed_reason": reason,
	}, expected); err != nil {
		if expected != nil && errors.Is(err, gorm.ErrRecordNotFound) {
			helpers.AppLogger.Infof("账号 %d 凭据已更新，跳过过期 Token 清理", account.ID)
		} else {
			helpers.AppLogger.Errorf("清空开放平台访问凭证失败：%v", err)
		}
		return false
	}
	account.applyTokenFields("", "", 0, reason)
	return true
}

func (account *Account) ClearToken(reason string) {
	account.clearToken(reason, nil)
}

// ClearTokenIfCurrent 仅当数据库中的凭据仍与当前账号快照一致时清空 Token。
func (account *Account) ClearTokenIfCurrent(reason string) bool {
	expected := &accountTokenSnapshot{token: account.Token, refreshToken: account.RefreshToken}
	return account.clearToken(reason, expected)
}

// ClearTokenIfCredentialsMatch 仅清空仍匹配远端请求开始时凭据的账号。
func (account *Account) ClearTokenIfCredentialsMatch(expectedToken string, expectedRefreshToken string, reason string) bool {
	expected := &accountTokenSnapshot{token: expectedToken, refreshToken: expectedRefreshToken}
	return account.clearToken(reason, expected)
}

func openListAccountIfCurrent(accountID uint, baseURL, username, password, token string) *gorm.DB {
	// WHERE 包含原密码与 Token，禁止 GORM 将它们展开到错误或慢 SQL 日志。
	return db.Db.Session(&gorm.Session{Logger: logger.Discard}).Model(&Account{}).
		Where("id = ? AND source_type = ? AND base_url = ? AND username = ? AND password = ? AND token = ?",
			accountID, SourceTypeOpenList, baseURL, username, password, token)
}

func (account *Account) UpdateOpenList(baseUrl string, username string, password string, token string, authType string) error {
	candidate := *account
	oldUsername := account.Username
	oldPassword := account.Password
	oldBaseUrl := account.BaseUrl
	oldToken := account.Token

	token = strings.TrimSpace(token)
	authType = strings.TrimSpace(authType)
	if authType == "" {
		switch {
		case token != "":
			authType = openListAuthTypeToken
		case strings.TrimSpace(username) != "" || strings.TrimSpace(password) != "":
			authType = openListAuthTypePassword
		case oldPassword != "":
			authType = openListAuthTypePassword
		default:
			authType = openListAuthTypeToken
		}
	}
	if authType != openListAuthTypePassword && authType != openListAuthTypeToken {
		return fmt.Errorf("不支持的 OpenList 认证方式：%s", authType)
	}

	usernameProvided := strings.TrimSpace(username) != ""
	passwordProvided := strings.TrimSpace(password) != ""
	if strings.TrimSpace(username) == "" {
		username = oldUsername
	}
	oldAuthType := openListAuthTypeToken
	if oldPassword != "" {
		oldAuthType = openListAuthTypePassword
	}
	candidate.BaseUrl = baseUrl
	candidate.Username = username
	needsNewToken := false
	switch authType {
	case openListAuthTypeToken:
		if token == "" {
			if oldPassword != "" {
				return fmt.Errorf("切换为 Token 认证需要提供新的 Token")
			}
			token = oldToken
		}
		if token == "" {
			return fmt.Errorf("OpenList Token 不能为空")
		}
		// Token 认证不保留旧密码，避免账号列表和 Token 刷新流程误判为密码认证。
		candidate.Password = ""
		candidate.Token = token
	case openListAuthTypePassword:
		if oldAuthType != openListAuthTypePassword && !usernameProvided {
			return fmt.Errorf("切换为用户名密码认证需要提供用户名")
		}
		if oldAuthType != openListAuthTypePassword && !passwordProvided {
			return fmt.Errorf("切换为用户名密码认证需要提供密码")
		}
		if strings.TrimSpace(password) == "" {
			password = oldPassword
		}
		if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
			return fmt.Errorf("OpenList 用户名和密码不能为空")
		}
		candidate.Password = password
		credentialsChanged := oldAuthType != openListAuthTypePassword || username != oldUsername || password != oldPassword
		needsNewToken = credentialsChanged || baseUrl != oldBaseUrl || oldToken == ""
		if needsNewToken {
			// 认证方式或登录凭据发生变化时重新获取 Token，避免继续使用旧 Token。
			candidate.Token = ""
		}
	}
	client := openlist.NewTemporaryClient(candidate.BaseUrl, candidate.Username, candidate.Password, candidate.Token)
	defer client.Close()
	if needsNewToken {
		if _, err := client.GetToken(); err != nil {
			helpers.AppLogger.Errorf("更新 OpenList 账号 Token 失败：%v", err)
			return err
		}
	}
	userInfo, err := client.GetUserInfo(client.GetAuthToken())
	if err != nil {
		helpers.AppLogger.Errorf("验证 OpenList 账号失败：%v", err)
		return err
	}
	// 用户信息查询可能自动刷新 Token，保存最终验证过的实际值。
	candidate.Token = client.GetAuthToken()
	candidate.UserId = fmt.Sprintf("%d", userInfo.ID)
	if err := ensureAccountIdentityAvailable(db.Db, account.ID, candidate.Name, candidate.UserId); err != nil {
		return err
	}
	updateData := map[string]any{
		"base_url": candidate.BaseUrl,
		"username": candidate.Username,
		"password": candidate.Password,
		"token":    candidate.Token,
		"user_id":  candidate.UserId,
	}
	// 网络验证已结束；数据库提交与缓存安装保持同序，避免慢编辑覆盖后来的配置。
	openListAuthorizationMu.Lock()
	defer openListAuthorizationMu.Unlock()
	result := openListAccountIfCurrent(account.ID, oldBaseUrl, oldUsername, oldPassword, oldToken).Updates(updateData)
	if result.Error != nil {
		helpers.AppLogger.Errorf("更新 OpenList 账号失败：%v", result.Error)
		return mapAccountIdentityError(result.Error, updateData)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("OpenList 账号配置已更新或账号已删除，请重新加载后重试")
	}
	*account = candidate
	account.GetOpenListClient()
	return nil
}

// 使用 name 创建一个临时账号，供用户后续授权绑定
// name：账号备注
func CreateAccountByName(name string, srouceType SourceType, appId string, appIdName string) (*Account, error) {
	return CreateAccountWithAuthSource(name, srouceType, appId, appIdName, "", "")
}

func CreateAccountWithAuthSource(name string, srouceType SourceType, appId string, appIdName string, authSourceType v115auth.AuthSourceType, authProvider v115auth.AuthProvider) (*Account, error) {
	account := &Account{}
	account.Name = name
	account.SourceType = srouceType
	account.AppId = appId
	account.AppIdName = appIdName
	account.AuthSourceType = authSourceType
	account.AuthProvider = authProvider
	account.Token = ""
	account.RefreshToken = ""
	account.TokenExpiriesTime = 0
	account.UserId = ""
	account.Username = ""

	if err := ensureAccountIdentityAvailable(db.Db, 0, account.Name, ""); err != nil {
		return nil, err
	}
	// 插入数据库，如果插入失败则报错
	err := db.Db.Save(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("创建开放平台账号失败：%v", err)
		return nil, err
	}
	return account, nil
}

// 更新账号资料，不修改授权凭据和连接配置
func (account *Account) UpdateInfo(name string, appIdName string) error {
	oldName := account.Name
	oldAppIDName := account.AppIdName
	updateData := map[string]any{
		"name": name,
	}
	source := account.V115AuthSource()
	if account.SourceType == SourceType115 && source.SourceType == v115auth.SourceTypeCustomAppID {
		updateData["app_id_name"] = appIdName
	}
	err := db.Db.Transaction(func(tx *gorm.DB) error {
		if err := ensureAccountIdentityAvailable(tx, account.ID, name, ""); err != nil {
			return err
		}
		result := tx.Model(&Account{}).Where("id = ?", account.ID).Updates(updateData)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		account.Name = oldName
		account.AppIdName = oldAppIDName
		helpers.AppLogger.Errorf("更新开放平台账号资料失败：%v", err)
		return err
	}
	account.Name = name
	if _, ok := updateData["app_id_name"]; ok {
		account.AppIdName = appIdName
	}
	return nil
}

// 创建 OpenList 账号
// baseURL：OpenList 的访问地址
// username：OpenList 的登录用户名
// password：OpenList 的登录密码
// token：直接提供的 Token（优先使用）
func CreateOpenListAccount(baseUrl string, username string, password string, token string) (*Account, error) {
	account := &Account{}
	account.Name = username
	account.SourceType = SourceTypeOpenList
	account.AppId = ""
	account.BaseUrl = baseUrl
	account.Username = username
	account.Password = password
	account.Token = token

	client := openlist.NewTemporaryClient(baseUrl, username, password, token)
	defer client.Close()
	// 如果提供了 Token，优先使用 Token，否则使用用户名密码获取 Token
	if token == "" {
		if _, err := client.GetToken(); err != nil {
			helpers.AppLogger.Errorf("验证 OpenList 账号失败：%v", err)
			return nil, err
		}
	}
	userInfo, err := client.GetUserInfo(client.GetAuthToken())
	if err != nil {
		helpers.AppLogger.Errorf("获取 OpenList 用户信息失败：%v", err)
		return nil, err
	}
	account.Token = client.GetAuthToken()
	account.UserId = fmt.Sprintf("%d", userInfo.ID)
	account.Name = userInfo.Username

	if err := ensureAccountIdentityAvailable(db.Db, 0, account.Name, account.UserId); err != nil {
		return nil, err
	}

	// 验证完成后才同时保存配置和 Token，并安装共享客户端。
	openListAuthorizationMu.Lock()
	defer openListAuthorizationMu.Unlock()
	err = db.Db.Session(&gorm.Session{Logger: logger.Discard}).Create(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("创建 OpenList 账号失败：%v", err)
		return nil, err
	}
	account.GetOpenListClient()
	helpers.AppLogger.Infof("创建 OpenList 账号成功，用户 ID：%s，用户名：%s", account.UserId, account.Name)
	return account, nil
}

// 创建 115 账号；如果 userId 已经存在，则更新已有账号。
// token：115 账号的 Token
// refreshToken：115 账号的 Refresh Token
// userId：115 账号对应的用户 ID
// username：115 账号对应的用户名
// expiresTime：Token 的过期时间
func CreateAccountFull(sourceType SourceType, AppId string, name string, token string, refreshToken string, userId string, username string, expiresTime int64) *Account {
	var account *Account
	if strings.TrimSpace(userId) != "" {
		if existing, err := GetAccountByUserId(userId); err == nil {
			account = existing
		}
	}
	if account == nil {
		account = &Account{}
	}
	now := time.Now().Unix()
	account.SourceType = sourceType
	account.AppId = AppId
	account.Name = name
	account.Token = token
	account.RefreshToken = refreshToken
	account.TokenExpiriesTime = now + expiresTime
	account.UserId = userId
	account.Username = username
	if err := ensureAccountIdentityAvailable(db.Db, account.ID, account.Name, account.UserId); err != nil {
		helpers.AppLogger.Errorf("保存开放平台账号失败：%v", err)
		return nil
	}
	if err := db.Db.Save(account).Error; err != nil {
		helpers.AppLogger.Errorf("保存开放平台账号失败：%v", err)
		return nil
	}
	return account
}

// 通过 userId 查询开放平台账号
func GetAccountByUserId(userId string) (*Account, error) {
	if strings.TrimSpace(userId) == "" {
		return nil, gorm.ErrRecordNotFound
	}
	account := &Account{}
	err := db.Db.Where("user_id = ?", userId).First(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("查询开放平台账号失败：%v", err)
		return nil, err
	}
	return account, nil
}

// 通过 ID 查询开放平台账号
func GetAccountById(id uint) (*Account, error) {
	account := &Account{}
	err := db.Db.Where("id = ?", id).First(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("查询开放平台账号失败：%v", err)
		return nil, err
	}
	return account, nil
}

// 通过 sourceType 查询账号列表
func GetAccountBySourceType(sourceType SourceType) ([]*Account, error) {
	accounts := []*Account{}
	err := db.Db.Where("source_type = ?", sourceType).Find(&accounts).Error
	if err != nil {
		helpers.AppLogger.Errorf("查询开放平台账号失败：%v", err)
		return nil, err
	}
	return accounts, nil
}

// 查询账号列表，全部返回
func GetAllAccount() ([]Account, error) {
	var accounts []Account
	err := db.Db.Order("id desc").Find(&accounts).Error
	if err != nil {
		helpers.AppLogger.Errorf("查询开放平台账号失败：%v", err)
		return nil, err
	}
	return accounts, nil
}

// 根据 fileId 获取文件夹的路径
func GetPathByPathFileId(account *Account, fileId string) string {
	client := account.Get115Client()
	ctx := context.Background()
	detail, err := client.GetFsDetailByCid(ctx, fileId)
	if err != nil {
		helpers.AppLogger.Errorf("查询文件详情失败：%v", err)
		return ""
	}
	// 生成完整路径
	baseDir := detail.GetFullPath()
	return baseDir + "/" + detail.FileName
}

// 处理 115 访问凭证失效事件（异步版本）
func HandleV115TokenInvalid(event helpers.Event) helpers.EventResult {
	eventData := event.Data.(map[string]interface{})
	helpers.AppLogger.Infof("收到 V115 访问凭证失效事件，开始处理，账号 ID：%d", eventData["account_id"].(uint))
	account, err := GetAccountById(eventData["account_id"].(uint))
	if err != nil {
		helpers.AppLogger.Errorf("查询开放平台账号失败：%v", err)
		return helpers.EventResult{
			Success: false,
			Error:   err,
			Data:    nil,
		}
	}
	expectedToken, _ := eventData["token"].(string)
	expectedRefreshToken, _ := eventData["refresh_token"].(string)
	if expectedRefreshToken == "" {
		err := fmt.Errorf("V115 访问凭证失效事件缺少原始 refresh_token")
		helpers.AppLogger.Warnf("账号 %d %v，跳过清空凭证", account.ID, err)
		return helpers.EventResult{Success: false, Error: err, Data: nil}
	}
	if !account.ClearTokenIfCredentialsMatch(expectedToken, expectedRefreshToken, eventData["reason"].(string)) {
		helpers.AppLogger.Infof("账号 %d 凭据已更新，跳过过期凭证清理", account.ID)
		return helpers.EventResult{Success: true, Error: nil, Data: nil}
	}
	v115open.UpdateTokenIfCurrent(account.ID, expectedToken, expectedRefreshToken, "", "")
	ctx := context.Background()
	notif := &Notification{
		Type:      SystemAlert,
		Title:     "🔐 115 开放平台访问凭证已失效",
		Content:   fmt.Sprintf("账号 ID：%d\n用户名：%s\n请重新授权\n⏰ 时间：%s", int(account.ID), account.Username, time.Now().Format("2006-01-02 15:04:05")),
		Timestamp: time.Now(),
		Priority:  HighPriority,
	}
	if notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif); err != nil {
			helpers.AppLogger.Errorf("发送访问凭证失效通知失败：%v", err)
		}
	}
	return helpers.EventResult{
		Success: true,
		Error:   nil,
		Data:    nil,
	}
}

// 处理 OpenList 访问凭证保存事件（同步版本）
func HandleOpenListTokenSaveSync(event helpers.Event) helpers.EventResult {
	eventData, ok := event.Data.(openlist.TokenSaveEvent)
	if !ok || eventData.AccountID == 0 || eventData.Token == "" {
		return helpers.EventResult{Success: false, Error: fmt.Errorf("OpenList 访问凭证保存事件缺少有效的登录快照")}
	}
	openListAuthorizationMu.Lock()
	defer openListAuthorizationMu.Unlock()
	result := openListAccountIfCurrent(eventData.AccountID, eventData.BaseURL, eventData.Username, eventData.Password, eventData.PreviousToken).
		Updates(map[string]any{
			"token":               eventData.Token,
			"refresh_token":       "",
			"token_expiries_time": time.Now().Unix() + 48*60*60,
			"token_failed_reason": "",
		})
	if result.Error != nil {
		helpers.AppLogger.Errorf("OpenList 账号 %d 访问凭证保存失败：%v", eventData.AccountID, result.Error)
		return helpers.EventResult{Success: false, Error: result.Error}
	}
	if result.RowsAffected == 0 {
		helpers.AppLogger.Infof("OpenList 账号 %d 配置已更新或已删除，跳过旧登录结果", eventData.AccountID)
	}
	return helpers.EventResult{Success: true}
}
