package models

import (
	"context"
	"fmt"
	"strings"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115auth"
	"qmediasync/internal/v115open"
)

type Account struct {
	BaseModel
	Name              string                  `json:"name"` // 账号备注，仅供用户自己识别账号使用，唯一
	SourceType        SourceType              `json:"source_type"`
	AppId             string                  `json:"app_id"`
	AppIdName         string                  `json:"app_id_name"` // 自定义开放平台应用显示名，内置应用不使用该字段
	AuthSourceType    v115auth.AuthSourceType `json:"auth_source_type" gorm:"type:string;size:64"`
	AuthProvider      v115auth.AuthProvider   `json:"auth_provider" gorm:"type:string;size:64"`
	Token             string                  `json:"token" gorm:"type:string;size:512"`
	RefreshToken      string                  `json:"refresh_token" gorm:"type:string;size:512"`
	TokenExpiriesTime int64                   `json:"token_expiries_time"`
	UserId            string                  `json:"user_id"`                                         // 账号对应的用户 ID，唯一
	Username          string                  `json:"username" gorm:"type:string;size:32"`             // 网盘对应的用户名或者 OpenList 登录用户名
	Password          string                  `json:"password" gorm:"type:string;size:256"`            // OpenList 的用户密码
	BaseUrl           string                  `json:"base_url" gorm:"type:string;size:1024"`           // OpenList 的访问地址 HTTP[s]://ip:port
	TokenFailedReason string                  `json:"token_failed_reason" gorm:"type:string;size:256"` // 刷新 Token 失败的原因
}

const (
	openListAuthTypePassword = "password"
	openListAuthTypeToken    = "token"
)

func (account *Account) TableName() string {
	return "account"
}

// 更新 Token 和 refreshToken
func (account *Account) UpdateToken(token string, refreshToken string, expiresTime int64) bool {
	now := time.Now().Unix()
	account.Token = token
	account.RefreshToken = refreshToken
	account.TokenExpiriesTime = now + expiresTime
	account.TokenFailedReason = ""

	updateData := make(map[string]any)
	updateData["token"] = token
	updateData["refresh_token"] = refreshToken
	updateData["token_expiries_time"] = account.TokenExpiriesTime
	updateData["token_failed_reason"] = account.TokenFailedReason
	err := db.Db.Model(account).Where("id = ?", account.ID).Updates(updateData).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新开放平台登录凭据失败：%v", err)
		return false
	}
	return true
}

// 更新开放平台账号对应的用户信息
func (account *Account) UpdateUser(userId string, username string) bool {
	account.UserId = userId
	account.Username = username
	updateData := make(map[string]any)
	updateData["user_id"] = userId
	updateData["username"] = username
	err := db.Db.Model(account).Where("id = ?", account.ID).Updates(updateData).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新开放平台账号用户信息失败：%v", err)
		return false
	}
	// helpers.AppLogger.Debugf("更新开放平台账号用户信息成功：%v", account)
	return true
}

// 如果是 normal 模式，创建一个新的客户端，不启用限速器
func (account *Account) Get115Client() *v115open.OpenClient {
	return v115open.GetClient(account.ID, account.AppId, account.Token, account.RefreshToken)
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

func (account *Account) ClearToken(reason string) {
	account.Token = ""
	account.RefreshToken = ""
	account.TokenExpiriesTime = 0
	account.TokenFailedReason = reason
	// 保存到数据库
	err := db.Db.Save(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("清空开放平台访问凭证失败：%v", err)
		return
	}
}

func (account *Account) UpdateOpenList(baseUrl string, username string, password string, token string, authType string) error {
	oldUsername := account.Username
	oldPassword := account.Password
	oldBaseUrl := account.BaseUrl
	oldToken := account.Token
	oldUserId := account.UserId
	restore := func() {
		account.BaseUrl = oldBaseUrl
		account.Username = oldUsername
		account.Password = oldPassword
		account.Token = oldToken
		account.UserId = oldUserId
	}

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
	account.BaseUrl = baseUrl
	account.Username = username
	var userInfo *openlist.UserInfoResp
	var client *openlist.Client
	switch authType {
	case openListAuthTypeToken:
		if token == "" {
			if oldPassword != "" {
				restore()
				return fmt.Errorf("切换为 Token 认证需要提供新的 Token")
			}
			token = oldToken
		}
		if token == "" {
			restore()
			return fmt.Errorf("OpenList Token 不能为空")
		}
		// Token 认证不保留旧密码，避免账号列表和 Token 刷新流程误判为密码认证。
		account.Password = ""
		account.Token = token
		client = account.GetOpenListClient()
		var err error
		if userInfo, err = client.GetUserInfo(account.Token); err != nil {
			helpers.AppLogger.Errorf("验证 OpenList Token 失败：%v", err)
			restore()
			return err
		}
		helpers.AppLogger.Infof("使用提供的 Token 更新 OpenList 账号成功")
	case openListAuthTypePassword:
		if oldAuthType != openListAuthTypePassword && !usernameProvided {
			restore()
			return fmt.Errorf("切换为用户名密码认证需要提供用户名")
		}
		if oldAuthType != openListAuthTypePassword && !passwordProvided {
			restore()
			return fmt.Errorf("切换为用户名密码认证需要提供密码")
		}
		if strings.TrimSpace(password) == "" {
			password = oldPassword
		}
		if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
			restore()
			return fmt.Errorf("OpenList 用户名和密码不能为空")
		}
		account.Password = password
		credentialsChanged := oldAuthType != openListAuthTypePassword || username != oldUsername || password != oldPassword
		needsNewToken := credentialsChanged || baseUrl != oldBaseUrl || oldToken == ""
		if needsNewToken {
			// 认证方式或登录凭据发生变化时重新获取 Token，避免继续使用旧 Token。
			account.Token = ""
		}
		client = account.GetOpenListClient()
		var err error
		if needsNewToken {
			tokenData, getTokenErr := client.GetToken()
			if getTokenErr != nil {
				helpers.AppLogger.Errorf("更新 OpenList 账号 Token 失败：%v", getTokenErr)
				restore()
				return getTokenErr
			}
			account.Token = tokenData.Token
		} else {
			// 同为密码认证且配置未变化时复用已有 Token，避免编辑备注时重复登录。
			account.Token = oldToken
		}
		if userInfo, err = client.GetUserInfo(account.Token); err != nil {
			helpers.AppLogger.Errorf("获取 OpenList 用户信息失败：%v", err)
			restore()
			return err
		}
		// GetUserInfo 遇到过期 Token 时会在 client 内自动刷新，保存刷新后的实际 Token。
		account.Token = client.AccessToken
	}
	if userInfo == nil {
		restore()
		return fmt.Errorf("更新 OpenList 账号需要提供有效凭据")
	}
	account.UserId = fmt.Sprintf("%d", userInfo.ID)
	// 保存到数据库
	err := db.Db.Save(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新 OpenList 账号失败：%v", err)
		restore()
		return err
	}
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
	account.Name = name
	updateData := map[string]any{
		"name": name,
	}
	source := account.V115AuthSource()
	if account.SourceType == SourceType115 && source.SourceType == v115auth.SourceTypeCustomAppID {
		account.AppIdName = appIdName
		updateData["app_id_name"] = appIdName
	}
	err := db.Db.Model(account).Where("id = ?", account.ID).Updates(updateData).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新开放平台账号资料失败：%v", err)
		return err
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

	var userInfo *openlist.UserInfoResp
	// 如果提供了 Token，优先使用 Token，否则使用用户名密码获取 Token
	if token != "" {
		client := account.GetOpenListClient()
		var err error
		if userInfo, err = client.GetUserInfo(token); err != nil {
			helpers.AppLogger.Errorf("验证 OpenList Token 失败：%v", err)
			return nil, err
		}
		helpers.AppLogger.Infof("使用提供的 Token 创建 OpenList 账号成功")
	} else {
		client := account.GetOpenListClient()
		tokenData, clientErr := client.GetToken()
		if clientErr != nil {
			helpers.AppLogger.Errorf("验证 OpenList 账号失败：%v", clientErr)
			return nil, clientErr
		} else {
			helpers.AppLogger.Infof("获取 OpenList 账号 Token 成功")
		}
		account.Token = tokenData.Token
		var err error
		if userInfo, err = client.GetUserInfo(token); err != nil {
			helpers.AppLogger.Errorf("获取 OpenList 用户信息失败：%v", err)
			return nil, err
		}
	}
	account.UserId = fmt.Sprintf("%d", userInfo.ID)
	account.Name = userInfo.Username

	helpers.AppLogger.Infof("创建 OpenList 账号成功，用户 ID：%s，用户名：%s", account.UserId, account.Name)

	// 插入数据库，如果插入失败则报错
	err := db.Db.Save(account).Error
	if err != nil {
		helpers.AppLogger.Errorf("创建 OpenList 账号失败：%v", err)
		return nil, err
	}
	return account, nil
}

// 创建 115 账号，如果 userId 已经存在，则更新
// token：115 账号的 Token
// refreshToken：115 账号的 Refresh Token
// userId：115 账号对应的用户 ID
// username：115 账号对应的用户名
// expiresTime：Token 的过期时间
func CreateAccountFull(sourceType SourceType, AppId string, name string, token string, refreshToken string, userId string, username string, expiresTime int64) *Account {
	// 先检查 userId 是否已经存在
	account, err := GetAccountByUserId(userId)
	updateOrCreate := "create"
	if err == nil {
		// 说明 userId 已经存在
		helpers.AppLogger.Errorf("开放平台账号对应的用户 ID 已经存在：%v", userId)
		updateOrCreate = "update"
	} else {
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
	if updateOrCreate == "update" {
		err := db.Db.Save(account).Error
		if err != nil {
			helpers.AppLogger.Errorf("保存开放平台账号失败：%v", err)
			return nil
		}
		return account
	} else {
		err := db.Db.Save(account).Error
		if err != nil {
			helpers.AppLogger.Errorf("创建开放平台账号失败：%v", err)
			return nil
		}
		return account
	}
}

// 通过 userId 查询开放平台账号
func GetAccountByUserId(userId string) (*Account, error) {
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
	account.ClearToken(eventData["reason"].(string))
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
	helpers.AppLogger.Warnf("收到 OpenList 访问凭证保存同步事件，开始处理")

	eventData := event.Data.(map[string]any)
	account, err := GetAccountById(eventData["account_id"].(uint))
	if err != nil {
		helpers.AppLogger.Errorf("查询 OpenList 账号失败：%v", err)
		return helpers.EventResult{
			Success: false,
			Error:   err,
			Data:    nil,
		}
	}
	// expiresTime = now+ 48 小时
	expiresTime := int64(48 * 60 * 60)
	suc := account.UpdateToken(eventData["token"].(string), "", expiresTime)

	if suc {
		helpers.AppLogger.Infof("OpenList 访问凭证保存成功")
		return helpers.EventResult{
			Success: true,
			Error:   nil,
			Data:    nil,
		}
	} else {
		helpers.AppLogger.Warn("OpenList 访问凭证保存失败")
		return helpers.EventResult{
			Success: false,
			Error:   fmt.Errorf("OpenList 访问凭证保存失败"),
			Data:    nil,
		}
	}
}
