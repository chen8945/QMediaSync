package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	"qmediasync/internal/requests"
	v115auth "qmediasync/internal/v115/auth"
	v115open "qmediasync/internal/v115/openapi"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
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

// TryAcquire 供可跳过的后台工作非阻塞争用同一缓存键锁。
func (kl *KeyLockWithTimeout) TryAcquire(key string) bool {
	mutex, _ := kl.mutexes.LoadOrStore(key, &sync.Mutex{})
	return mutex.(*sync.Mutex).TryLock()
}

// LockWithTimeout 尝试获取锁，如果超时则返回 false
func (kl *KeyLockWithTimeout) LockWithTimeout(key string, timeout time.Duration) bool {
	return kl.lockContext(context.Background(), key, timeout)
}

func (kl *KeyLockWithTimeout) lockContext(ctx context.Context, key string, timeout time.Duration) bool {
	kl.global.Lock()
	mutex, _ := kl.mutexes.LoadOrStore(key, &sync.Mutex{})
	kl.global.Unlock()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return false
		}
		if mutex.(*sync.Mutex).TryLock() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
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
func Get115Status(c *gin.Context) {
	var req requests.AccountIDRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	client := account.Get115Client()
	var resp v115StatusResp
	// 获取用户信息
	userInfo, err := client.UserInfo()
	if err != nil {
		writeCloudUpstreamError(c, err, "获取 115 用户信息失败", account.Token, account.RefreshToken)
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
	httpapi.Success(c, http.StatusOK, resp, "获取 115 状态成功")
}

func GetFileDetail(c *gin.Context) {
	type fileDetailReq struct {
		AccountId uint   `json:"account_id" form:"account_id"`
		FileId    string `json:"file_id" form:"file_id"`
	}
	var req fileDetailReq
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountId)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	fullPath, err := models.GetPathByPathFileId(account, req.FileId)
	if err != nil {
		writeCloudUpstreamError(c, err, "查询文件详情失败")
		return
	}
	httpapi.Success(c, http.StatusOK, fullPath, "获取文件详情成功")
}

var keyLock KeyLockWithTimeout

const (
	v115URLCacheModeDirect = playback.ModeDirect
	v115URLCacheModeProxy  = playback.ModeProxy

	v115URLValidityCheckMaxWait = time.Duration(models.MaxURLValidityCheckTimeoutSeconds) * time.Second
)

var v115URLCacheLockWait = 10 * time.Second

func v115URLPlaybackMode(force int, localProxy int) string {
	if force == 0 && localProxy == 1 {
		return v115URLCacheModeProxy
	}
	return v115URLCacheModeDirect
}

// 115 直链的 f=1 要求 CDN 请求复用生成链接时完全一致的 User-Agent。
// 因此有效 UA 同时用于生成链接、缓存隔离和 HEAD 有效性检查；f=3 还要求保留 Cookie。
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
func Get115UrlByPickCode(c *gin.Context) {
	var req requests.RemoteFileURLRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	pickCode := req.PickCode
	userId := req.UserID
	var account *models.Account
	if userId == "" {
		// 查询 SyncFile
		syncFile, err := models.GetFileByPickCode(pickCode)
		if err != nil {
			writeCloudFileLookupError(c, err)
			return
		}
		account, err = models.ReadAccount(c.Request.Context(), syncFile.AccountId)
		if err != nil {
			writeAccountLookupError(c, err)
			return
		}
		helpers.AppLogger.Debugf("通过 PickCode 查询到 115 账号：%s", account.Username)
	} else {
		var err error
		// 通过 userId 查询账号
		account, err = models.GetAccountByUserId(userId)
		if err != nil {
			writeAccountLookupError(c, err)
			return
		}
		helpers.AppLogger.Debugf("通过用户 ID 查询到 115 账号：%s", account.Username)
	}
	requestUA := c.Request.UserAgent()
	localProxy, multiPlaybackEnabled := models.GetPlaybackSettings()
	ua := v115EffectiveUA(req.Force, localProxy, requestUA)
	client := account.Get115Client()
	helpers.AppLogger.Debugf("检查是否具有直链播放标记， force=%d", req.Force)
	cacheKey := v115URLCacheKey(pickCode, req.Force, localProxy, requestUA)
	helpers.AppLogger.Debugf("准备获取 115 文件下载链接：PickCode=%s，ua=%s，8095 播放=%d，加锁 10 秒", pickCode, ua, req.Force)
	ctx := c.Request.Context()
	playback.DefaultPreloader.Takeover(playbackPreloadIdentity(string(models.SourceType115), cacheKey))
	if !keyLock.lockContext(ctx, cacheKey, v115URLCacheLockWait) {
		helpers.AppLogger.Warnf("获取 115 下载链接缓存锁超时：PickCode=%s，ua=%s", pickCode, ua)
		httpapi.Failure(c, http.StatusServiceUnavailable, httpapi.Error{Code: "RESOURCE_BUSY", Message: "获取 115 下载链接超时，请稍后重试", Cause: ctx.Err()})
		return
	}
	defer keyLock.Unlock(cacheKey)

	helpers.AppLogger.Debugf("是否启用本地代理：%d", models.SettingsGlobal.LocalProxy)
	if v115URLPlaybackMode(req.Force, localProxy) == v115URLCacheModeProxy {
		helpers.AppLogger.Infof("因为直链标识=%d，本地播放代理开关=%d，所以使用默认 UA：%s", req.Force, localProxy, ua)
	}
	cachedUrl := string(db.Cache.Get(cacheKey))
	if cachedUrl != "" {
		fileName := helpers.URLFileName(cachedUrl)
		helpers.AppLogger.Infof("命中 115 下载链接缓存：文件=%q，PickCode=%s，UA=%q", fileName, pickCode, ua)
		if !models.IsURLValidityCheckEnabled() {
			helpers.AppLogger.Debugf("115 缓存直链 HEAD 检查已关闭：文件=%q，PickCode=%s，UA=%q", fileName, pickCode, ua)
		} else if valid := checkURLValidity(ctx, cachedUrl, ua, v115URLValidityCheckTimeout(models.URLValidityCheckTimeout())); ctx.Err() != nil {
			writeCloudUpstreamError(c, ctx.Err(), "获取 115 下载链接失败")
			return
		} else if !valid {
			helpers.AppLogger.Infof("115 缓存直链检查未通过，删除缓存并重新获取：文件=%q，PickCode=%s，UA=%q", fileName, pickCode, ua)
			db.Cache.Delete(cacheKey)
			cachedUrl = ""
		}
	}
	if cachedUrl == "" {
		source := playback.SourceKey{AccountID: account.ID, UserID: account.UserId, PickCode: pickCode}
		slot := playback.Slot{Mode: v115URLPlaybackMode(req.Force, localProxy), UA: ua}
		originURL := *c.Request.URL
		originURL.Scheme, originURL.Host = "http", c.Request.Host
		if c.Request.TLS != nil {
			originURL.Scheme = "https"
		}
		var downloadErr error
		cachedUrl, downloadErr = resolve115URLMiss(ctx, v115Playback, source, slot, cacheKey, originURL.String(), multiPlaybackEnabled,
			func(ctx context.Context) (string, error) {
				result, err := client.GetDownloadURLWithError(ctx, pickCode, ua, true)
				if err != nil {
					return "", err
				}
				return result.URL, nil
			},
			func(ctx context.Context) (string, error) { return copy115URL(ctx, account, pickCode, ua) },
		)
		if downloadErr != nil {
			if errors.Is(downloadErr, err115LocalFileLookup) {
				if !errors.Is(downloadErr, context.Canceled) && !errors.Is(downloadErr, context.DeadlineExceeded) {
					status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "查询 115 原文件失败"
					if errors.Is(downloadErr, gorm.ErrRecordNotFound) {
						status, code, message = http.StatusNotFound, "RESOURCE_NOT_FOUND", "原文件不存在"
					}
					httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: downloadErr, Secrets: []string{account.Token, account.RefreshToken}})
					return
				}
			}
			writeCloudUpstreamError(c, downloadErr, "获取 115 下载链接失败", account.Token, account.RefreshToken)
			return
		}
	}
	if ctx.Err() != nil {
		writeCloudUpstreamError(c, ctx.Err(), "获取 115 下载链接失败")
		return
	}
	fileName := helpers.URLFileName(cachedUrl)
	if req.Force == 0 && localProxy == 1 {
		helpers.AppLogger.Infof("通过本地代理访问 115 下载链接：文件=%q，PickCode=%s，UA=%q", fileName, pickCode, ua)
		proxyUrl := fmt.Sprintf("/proxy-115?url=%s", url.QueryEscape(cachedUrl))
		c.Redirect(http.StatusFound, proxyUrl)
		return
	}
	helpers.AppLogger.Infof("115 直链跳转：文件=%q，PickCode=%s，UA=%q，状态码=%d", fileName, pickCode, ua, http.StatusFound)
	c.Redirect(http.StatusFound, cachedUrl)
}

// GetLoginQrCodeOpen 获取 115 开放平台登录二维码。
func GetLoginQrCodeOpen(c *gin.Context) {
	var req requests.QRCodeOpenRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	source, client, err := getV115AuthorizationTarget(account, req.AuthorizationID)
	if err != nil {
		writeAccountPersistenceError(c, err, "授权来源不正确")
		return
	}
	if source.Provider != v115auth.ProviderOfficialPKCE {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "当前账号不是官方 PKCE 授权来源"})
		return
	}
	qrCodeData, err := client.GetQrCode()
	if err != nil {
		writeCloudUpstreamError(c, err, "获取二维码失败", account.Token, account.RefreshToken)
		return
	}
	saveOpen115AuthState(req.AccountID, qrCodeData, strings.TrimSpace(req.AuthorizationID))
	httpapi.Success(c, http.StatusOK, gin.H{
		"uid":     qrCodeData.Uid,
		"time":    qrCodeData.Time,
		"sign":    qrCodeData.Sign,
		"qrcode":  qrCodeData.Qrcode,
		"expires": 300,
	}, "获取二维码成功")
}

// GetQrCodeStatus 查询二维码扫码状态
func GetQrCodeStatus(c *gin.Context) {
	var req requests.QRCodeStatusRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	state, ok := getOpen115AuthState(req.AccountID, req.UID)
	if !ok {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "AUTHORIZATION_STATE_NOT_FOUND", Message: "二维码授权状态不存在或已过期"})
		return
	}
	if strings.TrimSpace(req.AuthorizationID) != state.AuthorizationID {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "AUTHORIZATION_MISMATCH", Message: "二维码授权会话不匹配"})
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	source, client, err := getV115AuthorizationTarget(account, state.AuthorizationID)
	if err != nil {
		writeAccountPersistenceError(c, err, "授权来源不正确")
		return
	}
	status, err := client.QrCodeScanStatus(&state.CodeData.QrCodeData)
	if err != nil {
		writeCloudUpstreamError(c, err, "获取二维码状态失败", account.Token, account.RefreshToken)
		return
	}
	setOpen115AuthLastStatus(req.AccountID, req.UID, status)
	if status != v115open.QrCodeScanStatusConfirmed {
		httpapi.Success(c, http.StatusOK, gin.H{"status": status.String(), "tip": status.Tip()}, "")
		return
	}
	if !markOpen115AuthTokenSaving(req.AccountID, req.UID) {
		httpapi.Success(c, http.StatusOK, gin.H{"status": "confirmed", "tip": "授权处理中"}, "")
		return
	}
	openToken, err := client.GetToken(state.CodeData)
	if err != nil || openToken == nil {
		resetOpen115AuthTokenSaving(req.AccountID, req.UID)
		writeCloudUpstreamError(c, err, "获取 Token 失败", account.Token, account.RefreshToken)
		return
	}
	userInfo, err := client.UserInfo()
	if err != nil {
		resetOpen115AuthTokenSaving(req.AccountID, req.UID)
		writeCloudUpstreamError(c, err, "获取 115 用户信息失败", account.Token, account.RefreshToken, openToken.AccessToken, openToken.RefreshToken)
		return
	}
	if !commitOpen115AuthState(req.AccountID, req.UID, func() bool {
		return save115AuthorizationWithUserInfo(c, account, source, state.AuthorizationID, *openToken, *userInfo)
	}) {
		resetOpen115AuthTokenSaving(req.AccountID, req.UID)
		if !c.Writer.Written() {
			httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "AUTHORIZATION_INACTIVE", Message: "二维码授权状态不存在或已取消"})
		}
		return
	}
	httpapi.Success(c, http.StatusOK, gin.H{"status": "confirmed", "tip": "授权成功"}, "")
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
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	authorizationID := strings.TrimSpace(req.AuthorizationID)
	source, _, err := getV115AuthorizationTarget(account, authorizationID)
	if err != nil {
		writeAccountPersistenceError(c, err, "授权来源不正确")
		return
	}
	if source.SourceType != v115auth.SourceTypeBuiltInRelay && source.SourceType != v115auth.SourceTypeThirdPartyService {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "当前账号不是网页授权来源"})
		return
	}
	provider, ok := v115auth.GetOAuthProvider(source.Provider)
	if !ok {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "不支持的 115 网页授权服务"})
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
		if source.SourceType == v115auth.SourceTypeBuiltInRelay && helpers.OAuthRelayEncryptionKey == "" {
			httpapi.Failure(c, http.StatusServiceUnavailable, httpapi.Error{Code: "OAUTH_RELAY_UNAVAILABLE", Message: "OAuth 中转未配置 OAUTH_RELAY_ENCRYPTION_KEY", Cause: err})
			return
		}
		writeCloudUpstreamError(c, err, "生成 OAuth 登录地址失败", account.Token, account.RefreshToken)
		return
	}
	httpapi.Success(c, http.StatusOK, OAuthURLResponse{
		AuthURL:   result.AuthURL,
		State:     result.State,
		Polling:   result.Polling,
		ExpiresIn: result.ExpiresIn,
	}, "获取 115 OAuth 登录地址成功")
}

func ConfirmOAuthCode(c *gin.Context) {
	var req requests.OAuthConfirmRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
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
			httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "AUTHORIZATION_MISMATCH", Message: "OAuth 授权会话不匹配"})
			return
		}
		authorizationID = callbackAuthorizationID
	}
	source, _, err := getV115AuthorizationTarget(account, authorizationID)
	if err != nil {
		writeAccountPersistenceError(c, err, "授权来源不正确")
		return
	}
	if source.SourceType != v115auth.SourceTypeBuiltInRelay && source.SourceType != v115auth.SourceTypeThirdPartyService {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "当前账号不是网页授权来源"})
		return
	}
	provider, ok := v115auth.GetOAuthProvider(source.Provider)
	if !ok {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "不支持的 115 网页授权服务"})
		return
	}
	if authorizationID != "" {
		payload["authorization_id"] = authorizationID
	}
	token, err := provider.Confirm(c.Request.Context(), payload)
	if err != nil {
		writeCloudUpstreamError(c, err, "确认 OAuth 登录失败", account.Token, account.RefreshToken, req.Data)
		return
	}
	if !token.Done {
		httpapi.Success(c, http.StatusOK, nil, "授权处理中")
		return
	}
	if !save115OAuthToken(c, account, source, authorizationID, token, "") {
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "OAuth 登录已确认")
}

func GetOAuthStatus(c *gin.Context) {
	var req requests.OAuthStatusRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	oauthState, ok := v115auth.GetOAuthStateForAccount(req.State, account.ID)
	if !ok {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "AUTHORIZATION_STATE_NOT_FOUND", Message: "授权状态不存在或已过期"})
		return
	}
	authorizationID := strings.TrimSpace(req.AuthorizationID)
	if oauthState.AuthorizationID != "" {
		if authorizationID != "" && authorizationID != oauthState.AuthorizationID {
			httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "AUTHORIZATION_MISMATCH", Message: "OAuth 授权会话不匹配"})
			return
		}
		authorizationID = oauthState.AuthorizationID
	} else if authorizationID != "" {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "AUTHORIZATION_MISMATCH", Message: "OAuth 授权会话不匹配"})
		return
	}
	source := account.V115AuthSource()
	if authorizationID != "" {
		targetSession, sessionOK := v115auth.GetAuthorizationSession(authorizationID, account.ID)
		if !sessionOK {
			httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "AUTHORIZATION_INACTIVE", Message: "授权会话不存在或已过期"})
			return
		}
		source = targetSession.Source
	} else if oauthState.Source.Provider != "" {
		source = oauthState.Source
	}
	if source.Provider != oauthState.Provider ||
		(oauthState.Source.Provider != "" &&
			(source.SourceType != oauthState.Source.SourceType || source.Provider != oauthState.Source.Provider || source.AppID != oauthState.Source.AppID)) {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "OAuth 授权服务不匹配"})
		return
	}
	provider, ok := v115auth.GetOAuthProvider(source.Provider)
	if !ok {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "不支持的 115 网页授权服务"})
		return
	}
	token, err := provider.Poll(c.Request.Context(), req.State)
	if err != nil {
		writeCloudUpstreamError(c, err, "查询 OAuth 授权状态失败", account.Token, account.RefreshToken)
		return
	}
	if token.Done {
		// PrepareAccountAuthorization 会在远端请求进行时清理旧状态；读取用户信息
		// 或写入凭据前需要再次确认，避免更换授权开始后旧结果仍然提交。
		currentState, stateStillActive := v115auth.GetOAuthStateForAccount(req.State, account.ID)
		if !stateStillActive || currentState.AuthorizationID != oauthState.AuthorizationID {
			v115auth.ReleaseOAuthState(req.State)
			httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "AUTHORIZATION_INACTIVE", Message: "授权状态不存在或已取消"})
			return
		}
		if !save115OAuthToken(c, account, source, authorizationID, token, req.State) {
			return
		}
	}
	httpapi.Success(c, http.StatusOK, gin.H{"done": token.Done}, "查询 OAuth 授权状态成功")
}

func getV115AuthorizationTarget(account *models.Account, authorizationID string) (v115auth.Source, *v115open.OpenClient, error) {
	source := account.V115AuthSource()
	authorizationID = strings.TrimSpace(authorizationID)
	if authorizationID != "" {
		session, ok := v115auth.GetAuthorizationSession(authorizationID, account.ID)
		if !ok {
			return v115auth.Source{}, nil, v115auth.ErrAuthorizationSessionInactive
		}
		source = session.Source
	}
	if source.SourceType == "" || source.Provider == "" {
		return v115auth.Source{}, nil, validation.New("auth_source_type", "115 授权来源无效")
	}
	if authorizationID != "" && source.Deprecated {
		return v115auth.Source{}, nil, validation.New("auth_source_type", "已废弃的 115 授权来源不能用于授权")
	}
	return source, v115open.NewClient(account.ID, source.AppID, account.Token, account.RefreshToken), nil
}

func begin115AuthorizationSession(c *gin.Context, account *models.Account, authorizationID string) (string, bool) {
	authorizationID = strings.TrimSpace(authorizationID)
	if authorizationID != "" && !v115auth.BeginAuthorizationSession(authorizationID, account.ID) {
		httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "AUTHORIZATION_INACTIVE", Message: "授权会话不存在、已过期或已取消"})
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
		writeCloudUpstreamError(c, err, "获取 115 用户信息失败", account.Token, account.RefreshToken, token.AccessToken, token.RefreshToken)
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
		writeAccountPersistenceError(c, err, "保存 115 授权失败", token.AccessToken, token.RefreshToken)
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
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}

	now := time.Now().Unix()
	stats, err := models.GetRequestStatsWindow(now, int(req.TimeWindow))
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询队列统计数据失败", Cause: err})
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

	httpapi.Success(c, http.StatusOK, responseData, "获取队列统计数据成功")
}

// SetQueueRateLimit 设置 115 开放平台请求队列的速率限制参数。
func SetQueueRateLimit(c *gin.Context) {
	var req requests.QueueRateLimitRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}

	// 设置全局执行器的速率限制配置
	v115open.SetGlobalExecutorConfig(req.QPS, req.QPM, req.QPH)

	helpers.AppLogger.Infof("115 开放平台队列速率限制已更新：QPS=%d，QPM=%d，QPH=%d", req.QPS, req.QPM, req.QPH)

	httpapi.Success(c, http.StatusOK, gin.H{
		"qps": req.QPS,
		"qpm": req.QPM,
		"qph": req.QPH,
	}, "速率限制配置已更新")
}

// GetRequestStatsByDay 获取指定日期范围内的请求统计（按天分组）
func GetRequestStatsByDay(c *gin.Context) {
	var req requests.QueueStatsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if req.StartDate == "" {
		req.StartDate = time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	}
	if req.EndDate == "" {
		req.EndDate = time.Now().Format("2006-01-02")
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
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
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询统计数据失败", Cause: err})
		return
	}

	// 获取总请求数和限流请求数
	totalCount, err := models.GetRequestStatsCount(startTime, endTime)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询统计数据失败", Cause: err})
		return
	}
	throttledCount, err := models.GetThrottledRequestsCount(startTime, endTime)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询统计数据失败", Cause: err})
		return
	}

	responseData := gin.H{
		"start_date":            req.StartDate,
		"end_date":              req.EndDate,
		"total_requests":        totalCount,
		"total_throttled":       throttledCount,
		"daily_stats":           dailyStats,
		"query_time_range_days": int(endDate.Sub(startDate).Hours() / 24),
	}

	httpapi.Success(c, http.StatusOK, responseData, "获取日统计数据成功")
}

// GetRequestStatsByHour 获取指定日期范围内的请求统计（按小时分组）
func GetRequestStatsByHour(c *gin.Context) {
	var req requests.QueueStatsRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if req.StartDate == "" {
		req.StartDate = time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	}
	if req.EndDate == "" {
		req.EndDate = time.Now().Format("2006-01-02")
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
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
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询统计数据失败", Cause: err})
		return
	}

	// 获取总请求数和限流请求数
	totalCount, err := models.GetRequestStatsCount(startTime, endTime)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询统计数据失败", Cause: err})
		return
	}
	throttledCount, err := models.GetThrottledRequestsCount(startTime, endTime)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "查询统计数据失败", Cause: err})
		return
	}

	responseData := gin.H{
		"start_date":            req.StartDate,
		"end_date":              req.EndDate,
		"total_requests":        totalCount,
		"total_throttled":       throttledCount,
		"hourly_stats":          hourlyStats,
		"query_time_range_days": int(endDate.Sub(startDate).Hours() / 24),
	}

	httpapi.Success(c, http.StatusOK, responseData, "获取小时统计数据成功")
}

// CleanOldRequestStats 清理旧的请求统计数据
func CleanOldRequestStats(c *gin.Context) {
	var req requests.CleanRequestStatsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}

	err := models.CleanOldRequestStats(req.Days)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "清理统计数据失败", Cause: err})
		return
	}

	helpers.AppLogger.Infof("已清理 %d 天前的请求统计数据", req.Days)

	httpapi.Success(c, http.StatusOK, nil, fmt.Sprintf("已清理 %d 天前的请求统计数据", req.Days))
}

// checkURLValidity 使用 HEAD 请求检查 URL 是否有效。
// 返回 true 表示 URL 有效（2xx 状态码），false 表示未通过校验。
// ua 参数必须复用生成直链时的 User-Agent；115 直链 f=1 不接受不同 UA，f=3 还需要 Cookie。
// 此函数只复用 UA，不负责获取或补充 f=3 所需的 Cookie。
func checkURLValidity(ctx context.Context, urlStr string, ua string, timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = time.Duration(models.DefaultURLValidityCheckTimeoutSeconds) * time.Second
	}
	fileName := helpers.URLFileName(urlStr)
	helpers.AppLogger.Debugf("115 缓存直链 HEAD 检查开始：文件=%q，UA=%q，超时=%s", fileName, ua, timeout)
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

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, urlStr, nil)
	if err != nil {
		helpers.AppLogger.Errorf("115 缓存直链 HEAD 请求创建失败：文件=%q，UA=%q，错误=%v", fileName, ua, helpers.URLRequestErrorForLog(err))
		return false
	}

	// 设置 User-Agent，这是关键；115 链接必须使用请求时的 UA。
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}

	resp, err := client.Do(req)
	if err != nil {
		helpers.AppLogger.Errorf("115 缓存直链 HEAD 请求失败：文件=%q，UA=%q，错误=%v", fileName, ua, helpers.URLRequestErrorForLog(err))
		return false
	}
	defer resp.Body.Close()

	// 2xx 状态码表示有效
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		helpers.AppLogger.Infof("115 缓存直链 HEAD 检查通过：文件=%q，UA=%q，状态码=%d", fileName, ua, resp.StatusCode)
		return true
	}

	helpers.AppLogger.Infof("115 缓存直链 HEAD 检查未通过：文件=%q，UA=%q，状态码=%d", fileName, ua, resp.StatusCode)
	return false
}
