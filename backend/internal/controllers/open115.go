package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/v115auth"
	"qmediasync/internal/v115open"

	"github.com/gin-gonic/gin"
)

type v115StatusResp struct {
	UserId      json.Number `json:"user_id"`
	Username    string      `json:"username"`
	UsedSpace   int64       `json:"used_space"`
	TotalSpace  int64       `json:"total_space"`
	MemberLevel string      `json:"member_level"`
	ExpireTime  string      `json:"expire_time"`
}

type KeyLockWithTimeout struct {
	mutexes sync.Map // key -> *sync.Mutex
	global  sync.Mutex
}

// LockWithTimeout 尝试获取锁，如果超时则返回 false
func (kl *KeyLockWithTimeout) LockWithTimeout(key string, timeout time.Duration) bool {
	kl.global.Lock()
	mutex, _ := kl.mutexes.LoadOrStore(key, &sync.Mutex{})
	kl.global.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	for {
		select {
		case <-ctx.Done():
			return false // 超时
		default:
			if mutex.(*sync.Mutex).TryLock() {
				return true // 成功获取锁
			}
			time.Sleep(10 * time.Millisecond) // 短暂等待后重试
		}
	}
}

func (kl *KeyLockWithTimeout) Unlock(key string) {
	kl.global.Lock()
	mutex, ok := kl.mutexes.Load(key)
	kl.global.Unlock()

	if ok {
		mutex.(*sync.Mutex).Unlock()
	}
}

// Get115Status 查询 115 账号状态。
// @Summary 查询 115 账号状态
// @Description 获取指定 115 账号的登录状态及存储信息
// @Tags 115 开放平台
// @Accept json
// @Produce json
// @Param account_id query integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /auth/115-status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func Get115Status(c *gin.Context) {
	var req requests.AccountIDRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	client := account.Get115Client()
	var resp v115StatusResp
	// 获取用户信息
	userInfo, err := client.UserInfo()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 115 用户信息失败：" + err.Error(), Data: nil})
		return
	}
	resp.UserId = userInfo.UserId
	resp.Username = userInfo.UserName
	resp.UsedSpace = userInfo.RtSpaceInfo.AllUse.Size
	resp.TotalSpace = userInfo.RtSpaceInfo.AllTotal.Size
	resp.MemberLevel = userInfo.VipInfo.LevelName
	if userInfo.VipInfo.Expire > 0 {
		resp.ExpireTime = helpers.FormatTimestamp(userInfo.VipInfo.Expire)
	} else {
		resp.ExpireTime = "未开通会员"
	}
	// 返回状态信息
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取 115 状态成功", Data: resp})
}

func GetFileDetail(c *gin.Context) {
	type fileDetailReq struct {
		AccountId uint   `json:"account_id" form:"account_id"`
		FileId    string `json:"file_id" form:"file_id"`
	}
	var req fileDetailReq
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountId)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	fullPath := models.GetPathByPathFileId(account, req.FileId)
	if fullPath == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "文件 ID 不存在或未找到对应路径", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取文件详情成功", Data: fullPath})
}

var keyLock KeyLockWithTimeout

const (
	v115URLCacheModeDirect = "direct"
	v115URLCacheModeProxy  = "proxy"

	v115URLValidityCheckMaxWait = time.Duration(models.MaxURLValidityCheckTimeoutSeconds) * time.Second
)

var v115URLCacheLockWait = 10 * time.Second

func v115URLPlaybackMode(force int, localProxy int) string {
	if force == 0 && localProxy == 1 {
		return v115URLCacheModeProxy
	}
	return v115URLCacheModeDirect
}

func v115EffectiveUA(force int, localProxy int, requestUA string) string {
	if v115URLPlaybackMode(force, localProxy) == v115URLCacheModeProxy {
		return v115open.DEFAULTUA
	}
	return requestUA
}

func v115URLCacheKey(pickCode string, force int, localProxy int, requestUA string) string {
	mode := v115URLPlaybackMode(force, localProxy)
	ua := v115EffectiveUA(force, localProxy, requestUA)
	return fmt.Sprintf("115url:%s, mode=%s, ua=%s", pickCode, mode, ua)
}

func v115URLValidityCheckTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		timeout = time.Duration(models.DefaultURLValidityCheckTimeoutSeconds) * time.Second
	}
	if timeout > v115URLValidityCheckMaxWait {
		return v115URLValidityCheckMaxWait
	}
	return timeout
}

// Get115URLByPickCode 查询 115 直链并重定向。
// @Summary 获取 115 文件直链
// @Description 根据 PickCode 查询 115 文件直链并按需 302 跳转
// @Tags 115 开放平台
// @Accept json
// @Produce json
// @Param pickcode query string true "文件 PickCode"
// @Param userid query string false "115 用户 ID"
// @Param force query integer false "是否强制直链播放，1 为直链，0 使用本地代理时会走代理"
// @Success 302 {string} string "重定向到文件直链"
// @Failure 200 {object} object
// @Router /115/newurl [get]
func Get115UrlByPickCode(c *gin.Context) {
	var req requests.RemoteFileURLRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	pickCode := req.PickCode
	userId := req.UserID
	var account *models.Account
	if userId == "" {
		// 查询 SyncFile
		syncFile := models.GetFileByPickCode(pickCode)
		if syncFile == nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "文件 PickCode 不存在", Data: nil})
			return
		}
		var err error
		account, err = models.GetAccountById(syncFile.AccountId)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
			return
		}
		// helpers.AppLogger.Infof("通过 PickCode 查询到 115 账号：%s", account.Username)
	} else {
		var err error
		// 通过 userId 查询账号
		account, err = models.GetAccountByUserId(userId)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "用户 ID 不存在", Data: nil})
			return
		}
		// helpers.AppLogger.Infof("通过用户 ID 查询到 115 账号：%s", account.Username)
	}
	requestUA := c.Request.UserAgent()
	localProxy := 0
	if models.SettingsGlobal != nil {
		localProxy = models.SettingsGlobal.LocalProxy
	}
	ua := v115EffectiveUA(req.Force, localProxy, requestUA)
	client := account.Get115Client()
	// helpers.AppLogger.Infof("检查是否具有直链播放标记， force=%d", req.Force)
	cacheKey := v115URLCacheKey(pickCode, req.Force, localProxy, requestUA)
	// helpers.AppLogger.Infof("准备获取 115 文件下载链接：PickCode=%s，ua=%s，8095 播放=%d，加锁 10 秒", pickCode, ua, req.Force)
	if !keyLock.LockWithTimeout(cacheKey, v115URLCacheLockWait) {
		helpers.AppLogger.Warnf("获取 115 下载链接缓存锁超时：PickCode=%s，ua=%s", pickCode, ua)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 115 下载链接超时，请稍后重试", Data: nil})
		return
	}
	defer keyLock.Unlock(cacheKey)

	// helpers.AppLogger.Debugf("是否启用本地代理：%d", models.SettingsGlobal.LocalProxy)
	if v115URLPlaybackMode(req.Force, localProxy) == v115URLCacheModeProxy {
		helpers.AppLogger.Infof("因为直链标识=%d，本地播放代理开关=%d，所以使用默认 UA：%s", req.Force, localProxy, ua)
	}
	cachedUrl := string(db.Cache.Get(cacheKey))
	if cachedUrl != "" {
		helpers.AppLogger.Infof("从缓存中查询到 115 下载链接：PickCode=%s，ua=%s => %s", pickCode, ua, cachedUrl)
		if !models.IsURLValidityCheckEnabled() {
			helpers.AppLogger.Infof("115 直链缓存有效性检查已关闭，直接使用缓存链接：PickCode=%s", req.PickCode)
		} else if !checkURLValidity(cachedUrl, ua, v115URLValidityCheckTimeout(models.URLValidityCheckTimeout())) {
			helpers.AppLogger.Infof("缓存链接已失效，删除缓存并重新获取：PickCode=%s", req.PickCode)
			db.Cache.Delete(cacheKey)
			cachedUrl = ""
		}
	}
	if cachedUrl == "" {
		cachedUrl = client.GetDownloadUrl(context.Background(), pickCode, ua, true)
		if cachedUrl == "" {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 115 下载链接失败", Data: nil})
			return
		}
		helpers.AppLogger.Infof("从接口中查询到 115 下载链接：PickCode=%s，ua=%s => %s", pickCode, ua, cachedUrl)
		// 缓存 50 分钟
		db.Cache.Set(cacheKey, []byte(cachedUrl), 3000)
	}
	if req.Force == 0 {
		if localProxy == 1 {
			// 跳转到本地代理
			helpers.AppLogger.Infof("通过本地代理访问 115 下载链接，Emby 端口播放：%s", cachedUrl)
			proxyUrl := fmt.Sprintf("/proxy-115?url=%s", url.QueryEscape(cachedUrl))
			c.Redirect(http.StatusFound, proxyUrl)
		} else {
			helpers.AppLogger.Infof("302 重定向到 115 下载链接，Emby 端口播放：%s", cachedUrl)
			c.Redirect(http.StatusFound, cachedUrl)
		}
	} else {
		helpers.AppLogger.Infof("302 重定向到 115 下载链接，直链播放：%s", cachedUrl)
		c.Redirect(http.StatusFound, cachedUrl)
	}
}

// GetLoginQrCodeOpen 获取 115 开放平台登录二维码。
// @Summary 获取 115 登录二维码
// @Description 生成 115 开放平台登录二维码
// @Tags 115 开放平台
// @Accept json
// @Produce json
// @Param account_id body integer true "账号 ID"
// @Param authorization_id body string false "更换授权会话 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /auth/115-qrcode-open [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetLoginQrCodeOpen(c *gin.Context) {
	var req requests.QRCodeOpenRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	source, client, err := getV115AuthorizationTarget(account, req.AuthorizationID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if source.Provider != v115auth.ProviderOfficialPKCE {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "当前账号不是官方 PKCE 授权来源", Data: nil})
		return
	}
	qrCodeData, err := client.GetQrCode()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取二维码失败：" + err.Error(), Data: nil})
		return
	}
	saveOpen115AuthState(req.AccountID, qrCodeData, strings.TrimSpace(req.AuthorizationID))
	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "获取二维码成功",
		Data: gin.H{
			"uid":     qrCodeData.Uid,
			"time":    qrCodeData.Time,
			"sign":    qrCodeData.Sign,
			"qrcode":  qrCodeData.Qrcode,
			"expires": 300,
		},
	})
}

// GetQrCodeStatus 查询二维码扫码状态
// @Summary 查询 115 二维码扫码状态
// @Description 查询指定二维码 UID 的扫码进度
// @Tags 115 开放平台
// @Accept json
// @Produce json
// @Param uid body string true "二维码 UID"
// @Param account_id body integer true "账号 ID"
// @Param authorization_id body string false "更换授权会话 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /auth/115-qrcode-status [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetQrCodeStatus(c *gin.Context) {
	var req requests.QRCodeStatusRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	state, ok := getOpen115AuthState(req.AccountID, req.UID)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "二维码授权状态不存在或已过期", Data: nil})
		return
	}
	if strings.TrimSpace(req.AuthorizationID) != state.AuthorizationID {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "二维码授权会话不匹配", Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	source, client, err := getV115AuthorizationTarget(account, state.AuthorizationID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	status, err := client.QrCodeScanStatus(&state.CodeData.QrCodeData)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取二维码状态失败：" + err.Error(), Data: nil})
		return
	}
	setOpen115AuthLastStatus(req.AccountID, req.UID, status)
	if status != v115open.QrCodeScanStatusConfirmed {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    Success,
			Message: "",
			Data:    gin.H{"status": status.String(), "tip": status.Tip()},
		})
		return
	}
	if !markOpen115AuthTokenSaving(req.AccountID, req.UID) {
		c.JSON(http.StatusOK, APIResponse[any]{
			Code:    Success,
			Message: "",
			Data:    gin.H{"status": "confirmed", "tip": "授权处理中"},
		})
		return
	}
	openToken, err := client.GetToken(state.CodeData)
	if err != nil || openToken == nil {
		resetOpen115AuthTokenSaving(req.AccountID, req.UID)
		errMsg := "获取 Token 失败"
		if err != nil {
			errMsg += "：" + err.Error()
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: errMsg, Data: nil})
		return
	}
	userInfo, err := client.UserInfo()
	if err != nil {
		resetOpen115AuthTokenSaving(req.AccountID, req.UID)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 115 用户信息失败：" + err.Error(), Data: nil})
		return
	}
	if !commitOpen115AuthState(req.AccountID, req.UID, func() bool {
		return save115AuthorizationWithUserInfo(c, account, source, state.AuthorizationID, *openToken, *userInfo)
	}) {
		resetOpen115AuthTokenSaving(req.AccountID, req.UID)
		if !c.Writer.Written() {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "二维码授权状态不存在或已取消", Data: nil})
		}
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{
		Code:    Success,
		Message: "",
		Data:    gin.H{"status": "confirmed", "tip": "授权成功"},
	})
}

type OAuthURLResponse struct {
	AuthURL   string `json:"auth_url"`
	State     string `json:"state,omitempty"`
	Polling   bool   `json:"polling"`
	ExpiresIn int64  `json:"expires_in,omitempty"`
}

// 生成并返回 115 OAuth 登录地址
func GetOAuthUrl(c *gin.Context) {
	var req requests.OAuthURLRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	authorizationID := strings.TrimSpace(req.AuthorizationID)
	source, _, err := getV115AuthorizationTarget(account, authorizationID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if source.SourceType != v115auth.SourceTypeBuiltInRelay && source.SourceType != v115auth.SourceTypeThirdPartyService {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "当前账号不是网页授权来源", Data: nil})
		return
	}
	provider, ok := v115auth.GetOAuthProvider(source.Provider)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的 115 网页授权服务", Data: nil})
		return
	}
	result, err := provider.BuildAuth(c.Request.Context(), v115auth.OAuthURLRequest{
		AccountID:       account.ID,
		AppID:           source.AppID,
		RedirectURL:     req.RedirectURL,
		Provider:        source.Provider,
		AuthorizationID: authorizationID,
		Source:          source,
	})
	if err != nil {
		message := "生成 OAuth 登录地址失败：" + err.Error()
		if source.SourceType == v115auth.SourceTypeBuiltInRelay && helpers.OAuthRelayEncryptionKey == "" {
			message = "OAuth 中转未配置 OAUTH_RELAY_ENCRYPTION_KEY"
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: message, Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[OAuthURLResponse]{Code: Success, Message: "获取 115 OAuth 登录地址成功", Data: OAuthURLResponse{
		AuthURL:   result.AuthURL,
		State:     result.State,
		Polling:   result.Polling,
		ExpiresIn: result.ExpiresIn,
	}})
}

func ConfirmOAuthCode(c *gin.Context) {
	var req requests.OAuthConfirmRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	payload := req.Payload
	if payload == nil {
		payload = map[string]string{}
	}
	if req.Data != "" {
		payload["data"] = req.Data
	}
	authorizationID := strings.TrimSpace(req.AuthorizationID)
	if callbackAuthorizationID := strings.TrimSpace(payload["authorization_id"]); callbackAuthorizationID != "" {
		if authorizationID != "" && authorizationID != callbackAuthorizationID {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "OAuth 授权会话不匹配", Data: nil})
			return
		}
		authorizationID = callbackAuthorizationID
	}
	source, _, err := getV115AuthorizationTarget(account, authorizationID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if source.SourceType != v115auth.SourceTypeBuiltInRelay && source.SourceType != v115auth.SourceTypeThirdPartyService {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "当前账号不是网页授权来源", Data: nil})
		return
	}
	provider, ok := v115auth.GetOAuthProvider(source.Provider)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的 115 网页授权服务", Data: nil})
		return
	}
	if authorizationID != "" {
		payload["authorization_id"] = authorizationID
	}
	token, err := provider.Confirm(c.Request.Context(), payload)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "确认 OAuth 登录失败：" + err.Error(), Data: nil})
		return
	}
	if !token.Done {
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权处理中", Data: nil})
		return
	}
	if !save115OAuthToken(c, account, source, authorizationID, token, "") {
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "OAuth 登录已确认", Data: nil})
}

func GetOAuthStatus(c *gin.Context) {
	var req requests.OAuthStatusRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	oauthState, ok := v115auth.GetOAuthStateForAccount(req.State, account.ID)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "授权状态不存在或已过期", Data: nil})
		return
	}
	authorizationID := strings.TrimSpace(req.AuthorizationID)
	if oauthState.AuthorizationID != "" {
		if authorizationID != "" && authorizationID != oauthState.AuthorizationID {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "OAuth 授权会话不匹配", Data: nil})
			return
		}
		authorizationID = oauthState.AuthorizationID
	} else if authorizationID != "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "OAuth 授权会话不匹配", Data: nil})
		return
	}
	source := account.V115AuthSource()
	if authorizationID != "" {
		targetSession, sessionOK := v115auth.GetAuthorizationSession(authorizationID, account.ID)
		if !sessionOK {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "授权会话不存在或已过期", Data: nil})
			return
		}
		source = targetSession.Source
	} else if oauthState.Source.Provider != "" {
		source = oauthState.Source
	}
	if source.Provider != oauthState.Provider ||
		(oauthState.Source.Provider != "" &&
			(source.SourceType != oauthState.Source.SourceType || source.Provider != oauthState.Source.Provider || source.AppID != oauthState.Source.AppID)) {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "OAuth 授权服务不匹配", Data: nil})
		return
	}
	provider, ok := v115auth.GetOAuthProvider(source.Provider)
	if !ok {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "不支持的 115 网页授权服务", Data: nil})
		return
	}
	token, err := provider.Poll(c.Request.Context(), req.State)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询 OAuth 授权状态失败：" + err.Error(), Data: nil})
		return
	}
	if token.Done {
		// PrepareAccountAuthorization 会在远端请求进行时清理旧状态；读取用户信息
		// 或写入凭据前需要再次确认，避免更换授权开始后旧结果仍然提交。
		currentState, stateStillActive := v115auth.GetOAuthStateForAccount(req.State, account.ID)
		if !stateStillActive || currentState.AuthorizationID != oauthState.AuthorizationID {
			v115auth.ReleaseOAuthState(req.State)
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "授权状态不存在或已取消", Data: nil})
			return
		}
		if !save115OAuthToken(c, account, source, authorizationID, token, req.State) {
			return
		}
	}
	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "查询 OAuth 授权状态成功", Data: gin.H{"done": token.Done}})
}

func getV115AuthorizationTarget(account *models.Account, authorizationID string) (v115auth.Source, *v115open.OpenClient, error) {
	source := account.V115AuthSource()
	authorizationID = strings.TrimSpace(authorizationID)
	if authorizationID != "" {
		session, ok := v115auth.GetAuthorizationSession(authorizationID, account.ID)
		if !ok {
			return v115auth.Source{}, nil, fmt.Errorf("授权会话不存在或已过期")
		}
		source = session.Source
	}
	if source.SourceType == "" || source.Provider == "" {
		return v115auth.Source{}, nil, fmt.Errorf("115 授权来源无效")
	}
	if authorizationID != "" && source.Deprecated {
		return v115auth.Source{}, nil, fmt.Errorf("已废弃的 115 授权来源不能用于授权")
	}
	return source, v115open.NewClient(account.ID, source.AppID, account.Token, account.RefreshToken), nil
}

func begin115AuthorizationSession(c *gin.Context, account *models.Account, authorizationID string) (string, bool) {
	authorizationID = strings.TrimSpace(authorizationID)
	if authorizationID != "" && !v115auth.BeginAuthorizationSession(authorizationID, account.ID) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "授权会话不存在、已过期或已取消", Data: nil})
		return "", false
	}
	return authorizationID, true
}

func save115Authorization(c *gin.Context, account *models.Account, source v115auth.Source, authorizationID string, client *v115open.OpenClient, token v115open.TokenData) bool {
	normalizedAuthorizationID, ok := begin115AuthorizationSession(c, account, authorizationID)
	if !ok {
		return false
	}
	authorizationID = normalizedAuthorizationID

	userInfo, err := client.UserInfo()
	if err != nil {
		if authorizationID != "" {
			v115auth.AbortAuthorizationSession(authorizationID)
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取 115 用户信息失败：" + err.Error(), Data: nil})
		return false
	}
	return commit115Authorization(c, account, source, authorizationID, token, *userInfo)
}

func save115AuthorizationWithUserInfo(c *gin.Context, account *models.Account, source v115auth.Source, authorizationID string, token v115open.TokenData, userInfo v115open.UserInfo) bool {
	normalizedAuthorizationID, ok := begin115AuthorizationSession(c, account, authorizationID)
	if !ok {
		return false
	}
	authorizationID = normalizedAuthorizationID
	return commit115Authorization(c, account, source, authorizationID, token, userInfo)
}

func commit115Authorization(c *gin.Context, account *models.Account, source v115auth.Source, authorizationID string, token v115open.TokenData, userInfo v115open.UserInfo) bool {
	commit := func() error {
		return account.ReplaceV115Authorization(source, token.AccessToken, token.RefreshToken, token.ExpiresIn, string(userInfo.UserId), userInfo.UserName)
	}
	var err error
	if authorizationID != "" {
		err = v115auth.CommitAuthorizationSession(authorizationID, account.ID, commit)
	} else {
		err = v115auth.CommitLegacyAuthorization(account.ID, func() error {
			return account.UpdateV115Authorization(source, token.AccessToken, token.RefreshToken, token.ExpiresIn, string(userInfo.UserId), userInfo.UserName)
		})
	}
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存 115 授权失败：" + accountPersistenceMessage(err), Data: nil})
		return false
	}
	return true
}

func save115OAuthToken(c *gin.Context, account *models.Account, source v115auth.Source, authorizationID string, token v115auth.OAuthTokenResult, oauthState string) bool {
	client := v115open.NewClient(account.ID, source.AppID, token.AccessToken, token.RefreshToken)
	openToken := v115open.TokenData{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		ExpiresIn:    token.ExpiresIn,
	}
	if !save115Authorization(c, account, source, authorizationID, client, openToken) {
		if oauthState != "" {
			v115auth.ReleaseOAuthState(oauthState)
		}
		return false
	}
	if oauthState != "" {
		v115auth.ConsumeOAuthState(oauthState)
	}
	return true
}

// GetQueueStats 获取 115 开放平台请求队列的统计数据。
func GetQueueStats(c *gin.Context) {
	var req requests.QueueStatsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	now := time.Now().Unix()
	stats, err := models.GetRequestStatsWindow(now, int(req.TimeWindow))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询队列统计数据失败：" + err.Error(), Data: nil})
		return
	}

	// 获取全局队列执行器
	executor := v115open.GetGlobalExecutor()

	// 获取限流状态
	throttleStatus := executor.GetThrottleStatus()

	throttleWaitTime := ""
	if throttleStatus.IsThrottled {
		throttleWaitTime = throttleStatus.WaitTime.String()
	}

	// 构建响应数据
	responseData := gin.H{
		"total_requests":           stats.TotalRequests,
		"qps_count":                stats.QPSCount,
		"qpm_count":                stats.QPMCount,
		"qph_count":                stats.QPHCount,
		"throttled_count":          stats.ThrottledCount,
		"avg_response_time_ms":     stats.AvgResponseTimeMS,
		"last_throttle_time":       nil,
		"throttle_wait_time":       throttleWaitTime,
		"throttle_recover_time":    nil,
		"time_window_seconds":      req.TimeWindow,
		"is_throttled":             throttleStatus.IsThrottled,
		"throttled_elapsed_time":   throttleStatus.ElapsedTime.String(),
		"throttled_remaining_time": throttleStatus.RemainingTime.String(),
	}

	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "获取队列统计数据成功", Data: responseData})
}

// SetQueueRateLimit 设置 115 开放平台请求队列的速率限制参数。
func SetQueueRateLimit(c *gin.Context) {
	var req requests.QueueRateLimitRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	// 设置全局执行器的速率限制配置
	v115open.SetGlobalExecutorConfig(req.QPS, req.QPM, req.QPH)

	helpers.AppLogger.Infof("115 开放平台队列速率限制已更新：QPS=%d，QPM=%d，QPH=%d", req.QPS, req.QPM, req.QPH)

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "速率限制配置已更新", Data: gin.H{
		"qps": req.QPS,
		"qpm": req.QPM,
		"qph": req.QPH,
	}})
}

// GetRequestStatsByDay 获取指定日期范围内的请求统计（按天分组）
func GetRequestStatsByDay(c *gin.Context) {
	var req requests.QueueStatsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.StartDate == "" {
		req.StartDate = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	}
	if req.EndDate == "" {
		req.EndDate = time.Now().Format("2006-01-02")
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	// 解析日期
	startDate, _ := time.ParseInLocation("2006-01-02", req.StartDate, time.Local)
	endDate, _ := time.ParseInLocation("2006-01-02", req.EndDate, time.Local)

	// 设置时间范围（开始时间为当天 0 点，结束时间为当天 23:59:59）
	startTime := startDate.Unix()
	endTime := endDate.Add(24*time.Hour - time.Second).Unix()

	// 获取按天分组的统计数据
	dailyStats, err := models.GetDailyRequestStats(startTime, endTime)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询统计数据失败：" + err.Error(), Data: nil})
		return
	}

	// 获取总请求数和限流请求数
	totalCount, _ := models.GetRequestStatsCount(startTime, endTime)
	throttledCount, _ := models.GetThrottledRequestsCount(startTime, endTime)

	responseData := gin.H{
		"start_date":            req.StartDate,
		"end_date":              req.EndDate,
		"total_requests":        totalCount,
		"total_throttled":       throttledCount,
		"daily_stats":           dailyStats,
		"query_time_range_days": int(endDate.Sub(startDate).Hours() / 24),
	}

	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "获取日统计数据成功", Data: responseData})
}

// GetRequestStatsByHour 获取指定日期范围内的请求统计（按小时分组）
func GetRequestStatsByHour(c *gin.Context) {
	var req requests.QueueStatsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if req.StartDate == "" {
		req.StartDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	}
	if req.EndDate == "" {
		req.EndDate = time.Now().Format("2006-01-02")
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	// 解析日期
	startDate, _ := time.ParseInLocation("2006-01-02", req.StartDate, time.Local)
	endDate, _ := time.ParseInLocation("2006-01-02", req.EndDate, time.Local)

	// 设置时间范围
	startTime := startDate.Unix()
	endTime := endDate.Add(24*time.Hour - time.Second).Unix()

	// 获取按小时分组的统计数据
	hourlyStats, err := models.GetHourlyRequestStats(startTime, endTime)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询统计数据失败：" + err.Error(), Data: nil})
		return
	}

	// 获取总请求数和限流请求数
	totalCount, _ := models.GetRequestStatsCount(startTime, endTime)
	throttledCount, _ := models.GetThrottledRequestsCount(startTime, endTime)

	responseData := gin.H{
		"start_date":            req.StartDate,
		"end_date":              req.EndDate,
		"total_requests":        totalCount,
		"total_throttled":       throttledCount,
		"hourly_stats":          hourlyStats,
		"query_time_range_days": int(endDate.Sub(startDate).Hours() / 24),
	}

	c.JSON(http.StatusOK, APIResponse[gin.H]{Code: Success, Message: "获取小时统计数据成功", Data: responseData})
}

// CleanOldRequestStats 清理旧的请求统计数据
func CleanOldRequestStats(c *gin.Context) {
	var req requests.CleanRequestStatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	err := models.CleanOldRequestStats(req.Days)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "清理统计数据失败：" + err.Error(), Data: nil})
		return
	}

	helpers.AppLogger.Infof("已清理 %d 天前的请求统计数据", req.Days)

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: fmt.Sprintf("已清理 %d 天前的请求统计数据", req.Days), Data: nil})
}

// checkURLValidity 使用 HEAD 请求检查 URL 是否有效。
// 返回 true 表示 URL 有效（2xx 状态码），false 表示 URL 已失效。
// ua 参数：必须使用当前请求的 User-Agent 访问 115 链接（否则返回 403）。
func checkURLValidity(urlStr string, ua string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = time.Duration(models.DefaultURLValidityCheckTimeoutSeconds) * time.Second
	}
	helpers.AppLogger.Infof("URL 有效性检查开始：%s，UA=%s，超时=%s", urlStr, ua, timeout)
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives:   true,    // 禁用长连接，请求完立即关闭
			TLSHandshakeTimeout: timeout, // TLS 握手超时与总超时保持一致
			MaxIdleConns:        0,       // 不保持空闲连接
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// 不跟随重定向，只检查第一次响应
			return http.ErrUseLastResponse
		},
	}

	req, err := http.NewRequest("HEAD", urlStr, nil)
	if err != nil {
		helpers.AppLogger.Errorf("创建 HEAD 请求失败：%v", err)
		return false
	}

	// 设置 User-Agent，这是关键；115 链接必须使用请求时的 UA。
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}

	resp, err := client.Do(req)
	if err != nil {
		helpers.AppLogger.Errorf("HEAD 请求失败：%v", err)
		return false
	}
	defer resp.Body.Close()

	// 2xx 状态码表示有效
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		helpers.AppLogger.Infof("URL 有效性检查通过：状态码=%d", resp.StatusCode)
		return true
	}

	helpers.AppLogger.Infof("URL 已失效：状态码=%d", resp.StatusCode)
	return false
}
