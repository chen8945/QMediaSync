package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
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
// @Summary 查询百度网盘账号状态
// @Description 获取指定百度网盘账号的登录状态及存储信息
// @Tags 百度网盘
// @Accept json
// @Produce json
// @Param account_id query integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /auth/baidupan-status [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetBaiDuPanStatus(c *gin.Context) {
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
	client := account.GetBaiDuPanClient()
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取百度网盘用户信息失败：" + err.Error(), Data: nil})
		return
	}
	quota, err := client.GetQuota(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取百度网盘用户配额失败：" + err.Error(), Data: nil})
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
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取百度网盘状态成功", Data: BaiDuPanStatusResp{
		UserId:      *userInfo.Uk,
		Username:    *userInfo.BaiduName,
		MemberLevel: memberLevel,
		UsedSpace:   *quota.Used,
		TotalSpace:  *quota.Total,
	}})
}

// GetBaiDuPanOAuthURL 获取百度网盘 OAuth 登录地址。
// @Summary 获取百度网盘 OAuth 登录地址
// @Description 生成跳转到百度 OAuth 授权服务器的链接
// @Tags 百度网盘
// @Accept json
// @Produce json
// @Param account_id query string true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /baidupan/oauth-url [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetBaiDuPanOAuthUrl(c *gin.Context) {
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
	if account.SourceType != models.SourceTypeBaiduPan {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "当前账号不是百度网盘账号", Data: nil})
		return
	}

	clientId := account.AppId
	if clientId == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "账号缺少 APP ID 配置", Data: nil})
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "生成 OAuth 登录地址失败：" + err.Error(), Data: nil})
		return
	}

	// 构建授权 URL
	authServerUrl := fmt.Sprintf("%s/baidu.php", helpers.GlobalConfig.NewAuthServer)
	// 注意：redirect_uri 需要与百度开放平台配置保持一致
	oauthUrl := fmt.Sprintf("%s?action=code&state=%s", authServerUrl, stateEncoded)
	c.JSON(http.StatusOK, APIResponse[string]{Code: Success, Message: "获取百度网盘 OAuth 登录地址成功", Data: oauthUrl})
}

// ConfirmBaiDuPanOAuthCode 确认百度网盘 OAuth 登录。
// @Summary 确认百度网盘 OAuth 登录
// @Description 客户端将授权服务器返回的数据发送过来，换取 access token 和 refresh token 并入库
// @Tags 百度网盘
// @Accept json
// @Produce json
// @Param account_id body string true "账号 ID"
// @Param code body string true "授权码"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /baidupan/oauth-confirm [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ConfirmBaiDuPanOAuthCode(c *gin.Context) {
	var req requests.OAuthConfirmRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if req.Data == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "data 不能为空", Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	if account.SourceType != models.SourceTypeBaiduPan {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "当前账号不是百度网盘账号", Data: nil})
		return
	}
	// 对 req.Data 解密
	decryptedData, err := helpers.Decrypt(req.Data)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "确认 OAuth 登录失败：" + err.Error(), Data: nil})
		return
	}
	var data *baidupan.RefreshResponse
	err = json.Unmarshal([]byte(decryptedData), &data)
	if err != nil || data == nil {
		if err == nil {
			err = fmt.Errorf("授权数据为空")
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "确认 OAuth 登录失败：" + err.Error(), Data: nil})
		return
	}
	// 用临时客户端验证新凭据，验证成功后再和用户信息一起提交，避免
	// 唯一性冲突时留下新 Token 配旧用户信息的半更新状态。
	client := baidupan.NewBaiDuPanClientWithToken(data.AccessToken)
	userInfo, err := client.GetUserInfo(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "确认 OAuth 登录失败：" + err.Error(), Data: nil})
		return
	}
	if userInfo == nil || userInfo.Uk == nil || userInfo.BaiduName == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "确认 OAuth 登录失败：用户信息不完整", Data: nil})
		return
	}
	if err := account.ReplaceBaiDuPanAuthorization(
		data.AccessToken,
		data.RefreshToken,
		data.ExpiresIn,
		helpers.Int64ToString(*userInfo.Uk),
		*userInfo.BaiduName,
	); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新用户信息失败：" + accountPersistenceMessage(err), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "OAuth 登录已确认", Data: nil})
}

// 通过百度网盘文件的 fs_id 获取下载链接，参数名沿用 pickcode 以兼容 115。
func GetBaiduPanUrlByPickCode(c *gin.Context) {
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
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "文件标识不存在", Data: nil})
			return
		}
		var err error
		account, err = models.GetAccountById(syncFile.AccountId)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
			return
		}
	} else {
		var err error
		// 通过 userId 查询账号
		account, err = models.GetAccountByUserId(userId)
		if err != nil {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "用户 ID 不存在", Data: nil})
			return
		}
	}
	ua := c.Request.UserAgent()
	client := account.GetBaiDuPanClient()
	cacheKey := fmt.Sprintf("baidupanurl:%s, ua=%s", pickCode, ua)
	if keyLock.LockWithTimeout(cacheKey, 10*time.Second) {
		defer keyLock.Unlock(cacheKey)
		cachedUrl := string(db.Cache.Get(cacheKey))
		if cachedUrl == "" {
			fsDetail, err := client.GetFileDetail(context.Background(), pickCode, 1)
			if err != nil {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取百度网盘文件详情失败", Data: nil})
				return
			}
			cachedUrl = fmt.Sprintf("%s&access_token=%s", fsDetail.Dlink, account.Token)
			if cachedUrl == "" {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取百度网盘下载链接失败", Data: nil})
				return
			}
			helpers.AppLogger.Infof("从接口中查询到百度网盘下载链接：%s => %s", pickCode, cachedUrl)
			// 缓存 8 小时
			db.Cache.Set(cacheKey, []byte(cachedUrl), 27000)
		} else {
			helpers.AppLogger.Infof("从缓存中查询到百度网盘下载链接：%s => %s", pickCode, cachedUrl)
		}
		// 检查是否开启了本地播放代理，如果开启则跳转到代理链接
		// if models.SettingsGlobal.LocalProxy == 1 {
		// 跳转到本地代理
		proxyUrl := fmt.Sprintf("/proxy-115?baidupan=1&url=%s", url.QueryEscape(cachedUrl))
		helpers.AppLogger.Infof("通过本地代理访问百度网盘下载链接播放：%s", url.QueryEscape(cachedUrl))
		c.Redirect(http.StatusFound, proxyUrl)
		return
		// } else {
		// 	helpers.AppLogger.Infof("302 重定向到百度网盘下载链接播放：%s", url.QueryEscape(cachedURL))
		// 	c.Redirect(http.StatusFound, cachedURL)
		// 	return
		// }
	}

}
