package controllers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	"qmediasync/internal/requests"

	"github.com/gin-gonic/gin"
)

// BaiDuPanStatusResp 百度网盘状态响应
type BaiDuPanStatusResp struct {
	UserId      int64  `json:"user_id"`
	Username    string `json:"username"`
	UsedSpace   int64  `json:"used_space"`
	TotalSpace  int64  `json:"total_space"`
	MemberLevel string `json:"member_level"`
}

// GetBaiDuPanStatus 查询百度网盘账号状态
func GetBaiDuPanStatus(c *gin.Context) {
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
	client := account.GetBaiDuPanClient()
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		writeCloudUpstreamError(c, err, "获取百度网盘用户信息失败", account.Token, account.RefreshToken)
		return
	}
	quota, err := client.GetQuota(c.Request.Context())
	if err != nil {
		writeCloudUpstreamError(c, err, "获取百度网盘用户配额失败", account.Token, account.RefreshToken)
		return
	}
	if userInfo == nil || userInfo.Uk == nil || userInfo.BaiduName == nil || userInfo.VipType == nil || quota == nil || quota.Used == nil || quota.Total == nil {
		httpapi.Failure(c, http.StatusBadGateway, httpapi.Error{Code: "UPSTREAM_RESPONSE_INVALID", Message: "百度网盘返回的用户信息不完整"})
		return
	}
	var memberLevel string
	switch *userInfo.VipType {
	case 0:
		memberLevel = "非会员"
	case 1:
		memberLevel = "VIP"
	case 2:
		memberLevel = "SVIP"
	default:
	}
	httpapi.Success(c, http.StatusOK, BaiDuPanStatusResp{
		UserId:      *userInfo.Uk,
		Username:    *userInfo.BaiduName,
		MemberLevel: memberLevel,
		UsedSpace:   *quota.Used,
		TotalSpace:  *quota.Total,
	}, "获取百度网盘状态成功")
}

// GetBaiDuPanOAuthURL 获取百度网盘 OAuth 登录地址。
func GetBaiDuPanOAuthUrl(c *gin.Context) {
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
	if account.SourceType != models.SourceTypeBaiduPan {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "当前账号不是百度网盘账号"})
		return
	}

	clientId := account.AppId
	if clientId == "" {
		httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "ACCOUNT_CONFIGURATION_INVALID", Message: "账号缺少 APP ID 配置"})
		return
	}

	// 生成 state 参数
	type stateData struct {
		State       string `json:"state"`
		Time        int64  `json:"time"`
		ClientId    string `json:"client_id"`
		AccountId   string `json:"account_id"`
		RedirectUrl string `json:"redirect_url"`
	}
	stateObj := stateData{
		State:       helpers.RandStr(16),
		Time:        time.Now().Unix(),
		ClientId:    clientId,
		AccountId:   helpers.IntToString(int(req.AccountID)),
		RedirectUrl: fmt.Sprintf("%s?source=baidupan", req.RedirectURL),
	}
	// helpers.AppLogger.Infof("生成 OAuth 登录地址状态参数：%+v", stateObj)
	stateJson, _ := json.Marshal(stateObj)
	stateEncoded, err := helpers.Encrypt(string(stateJson))
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "生成 OAuth 登录地址失败", Cause: err})
		return
	}

	// 构建授权 URL
	authServerUrl := fmt.Sprintf("%s/baidu.php", helpers.GlobalConfig.NewAuthServer)
	// 注意：redirect_uri 需要与百度开放平台配置保持一致
	oauthUrl := fmt.Sprintf("%s?action=code&state=%s", authServerUrl, stateEncoded)
	httpapi.Success(c, http.StatusOK, oauthUrl, "获取百度网盘 OAuth 登录地址成功")
}

// ConfirmBaiDuPanOAuthCode 确认百度网盘 OAuth 登录。
func ConfirmBaiDuPanOAuthCode(c *gin.Context) {
	var req requests.OAuthConfirmRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	if req.Data == "" {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "data 不能为空"})
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	if account.SourceType != models.SourceTypeBaiduPan {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "当前账号不是百度网盘账号"})
		return
	}
	// 对 req.Data 解密
	decryptedData, err := helpers.Decrypt(req.Data)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_AUTHORIZATION_PAYLOAD", Message: "授权数据无效", Cause: err})
		return
	}
	var data *baidupan.RefreshResponse
	err = json.Unmarshal([]byte(decryptedData), &data)
	if err != nil || data == nil {
		if err == nil {
			err = fmt.Errorf("授权数据为空")
		}
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_AUTHORIZATION_PAYLOAD", Message: "授权数据无效", Cause: err})
		return
	}
	// 用临时客户端验证新凭据，验证成功后再和用户信息一起提交，避免
	// 唯一性冲突时留下新 Token 配旧用户信息的半更新状态。
	client := baidupan.NewBaiDuPanClientWithToken(data.AccessToken)
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		writeCloudUpstreamError(c, err, "确认 OAuth 登录失败", data.AccessToken, data.RefreshToken)
		return
	}
	if userInfo == nil || userInfo.Uk == nil || userInfo.BaiduName == nil {
		httpapi.Failure(c, http.StatusBadGateway, httpapi.Error{Code: "UPSTREAM_RESPONSE_INVALID", Message: "确认 OAuth 登录失败：用户信息不完整"})
		return
	}
	if err := account.ReplaceBaiDuPanAuthorization(
		data.AccessToken,
		data.RefreshToken,
		data.ExpiresIn,
		helpers.Int64ToString(*userInfo.Uk),
		*userInfo.BaiduName,
	); err != nil {
		writeAccountPersistenceError(c, err, "更新用户信息失败", data.AccessToken, data.RefreshToken)
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "OAuth 登录已确认")
}

// 通过百度网盘文件的 fs_id 获取下载链接，参数名沿用 pickcode 以兼容 115。
func GetBaiduPanUrlByPickCode(c *gin.Context) {
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
	} else {
		var err error
		// 通过 userId 查询账号
		account, err = models.GetAccountByUserId(userId)
		if err != nil {
			writeAccountLookupError(c, err)
			return
		}
	}
	ua := c.Request.UserAgent()
	client := account.GetBaiDuPanClient()
	cacheKey := baiduURLCacheKey(pickCode, ua)
	ctx := c.Request.Context()
	playback.DefaultPreloader.Takeover(playbackPreloadIdentity(string(models.SourceTypeBaiduPan), cacheKey))
	if keyLock.lockContext(ctx, cacheKey, 10*time.Second) {
		defer keyLock.Unlock(cacheKey)
		cachedUrl := string(db.Cache.Get(cacheKey))
		if cachedUrl == "" {
			fsDetail, err := client.GetFileDetail(ctx, pickCode, 1)
			if err != nil || ctx.Err() != nil {
				if ctx.Err() != nil {
					err = ctx.Err()
				}
				writeCloudUpstreamError(c, err, "获取百度网盘文件详情失败", account.Token)
				return
			}
			if fsDetail == nil || fsDetail.Dlink == "" {
				httpapi.Failure(c, http.StatusBadGateway, httpapi.Error{Code: "UPSTREAM_RESPONSE_INVALID", Message: "百度网盘返回的下载链接为空"})
				return
			}
			cachedUrl = fmt.Sprintf("%s&access_token=%s", fsDetail.Dlink, account.Token)
			helpers.AppLogger.Infof("从接口中查询到百度网盘下载链接：%s => %s", pickCode, cachedUrl)
			// 缓存 8 小时
			db.Cache.Set(cacheKey, []byte(cachedUrl), 27000)
		} else {
			helpers.AppLogger.Infof("从缓存中查询到百度网盘下载链接：%s => %s", pickCode, cachedUrl)
		}
		if ctx.Err() != nil {
			writeCloudUpstreamError(c, ctx.Err(), "获取百度网盘下载链接失败", account.Token)
			return
		}
		proxyUrl := fmt.Sprintf("/proxy-115?baidupan=1&url=%s", url.QueryEscape(cachedUrl))
		helpers.AppLogger.Infof("通过本地代理访问百度网盘下载链接播放：%s", url.QueryEscape(cachedUrl))
		c.Redirect(http.StatusFound, proxyUrl)
		return
	}
	httpapi.Failure(c, http.StatusServiceUnavailable, httpapi.Error{Code: "RESOURCE_BUSY", Message: "获取百度网盘下载链接超时，请稍后重试", Cause: ctx.Err()})
}
