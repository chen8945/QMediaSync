package controllers

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/v115auth"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
)

func friendlyAccountValidationMessage(err error) string {
	var validationErr validation.Error
	if !errors.As(err, &validationErr) {
		return err.Error()
	}

	switch validationErr.Field {
	case "id":
		return "请选择要操作的账号"
	case "source_type":
		return "请选择支持的网盘类型"
	case "name":
		if validationErr.Message == "不能为空" {
			return "请填写账号备注"
		}
		if validationErr.Message == "长度超出允许范围" {
			return "账号备注不能超过 64 个字符"
		}
	case "custom_app_name", "app_id_name":
		if validationErr.Message == "长度超出允许范围" {
			return "自定义应用名不能超过 64 个字符"
		}
	case "base_url":
		switch validationErr.Message {
		case "不能为空":
			return "请填写 OpenList 访问地址"
		case "必须是有效的 HTTP URL":
			return "OpenList 访问地址不太对，请填写类似 http://ip:5244 的地址"
		case "只支持 http 或 https":
			return "OpenList 访问地址只支持 http 或 https"
		}
	case "auth_type":
		return "请选择 OpenList 认证方式"
	case "username":
		return "请填写 OpenList 用户名"
	case "password":
		return "请填写 OpenList 密码"
	case "token":
		return "请填写 OpenList 令牌"
	case "confirmed":
		return "请先确认更换授权风险"
	}

	label := accountValidationFieldLabel(validationErr.Field)
	if label == "" {
		return "请求参数不正确，请检查后再试"
	}
	switch validationErr.Message {
	case "不能为空":
		return fmt.Sprintf("请填写%s", label)
	case "长度超出允许范围":
		return fmt.Sprintf("%s长度不符合要求", label)
	case "不能包含控制字符":
		return fmt.Sprintf("%s不能包含特殊控制字符", label)
	case "不是允许的取值":
		return fmt.Sprintf("%s不正确，请重新选择", label)
	}
	return fmt.Sprintf("%s%s", label, validationErr.Message)
}

func accountValidationFieldLabel(field string) string {
	switch field {
	case "source_type":
		return "网盘类型"
	case "name":
		return "账号备注"
	case "custom_app_name", "app_id_name":
		return "自定义应用名"
	case "base_url":
		return "OpenList 访问地址"
	case "auth_type":
		return "OpenList 认证方式"
	case "username":
		return "OpenList 用户名"
	case "password":
		return "OpenList 密码"
	case "token":
		return "OpenList 令牌"
	case "confirmed":
		return "更换授权确认"
	default:
		return ""
	}
}

func writeAccountValidationError(c *gin.Context, status int, err error) {
	if helpers.AppLogger != nil {
		helpers.AppLogger.Debugf("账号请求参数校验失败: %v", err)
	}
	c.JSON(status, APIResponse[any]{
		Code:    BadRequest,
		Message: friendlyAccountValidationMessage(err),
		Data:    nil,
	})
}

func accountPersistenceMessage(err error) string {
	switch {
	case errors.Is(err, models.ErrAccountNameTaken):
		return "账号备注已存在，请换一个"
	case errors.Is(err, models.ErrAccountUserIDTaken):
		return "当前账号已存在，不允许添加重复账号"
	default:
		return err.Error()
	}
}

// GetAccountList 获取所有开放平台账号列表
// @Summary 获取账号列表
// @Description 获取所有已配置的开放平台账号（如 115、OpenList 等）
// @Tags 账号管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /account/list [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetAccountList(c *gin.Context) {
	accounts, err := models.GetAllAccount()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询开放平台账号失败", Data: nil})
		return
	}
	type accountResp struct {
		ID                    uint                    `json:"id"`
		SourceType            models.SourceType       `json:"source_type"`
		Name                  string                  `json:"name"`
		AppId                 string                  `json:"app_id"`
		AppIdName             string                  `json:"app_id_name"`
		AppName               string                  `json:"app_name"`
		DisplayName           string                  `json:"display_name"`
		AuthSourceType        v115auth.AuthSourceType `json:"auth_source_type"`
		AuthProvider          v115auth.AuthProvider   `json:"auth_provider"`
		RequiresEncryptionKey bool                    `json:"requires_encryption_key"`
		Deprecated            bool                    `json:"deprecated"`
		Username              string                  `json:"username"`
		UserId                string                  `json:"user_id"`
		Authorized            bool                    `json:"authorized"`
		CreatedAt             int64                   `json:"created_at"`
		TokenFailedReason     string                  `json:"token_failed_reason"`
		BaseUrl               string                  `json:"base_url"`
		AuthType              string                  `json:"auth_type"`
	}
	resp := make([]accountResp, 0, len(accounts))
	for _, account := range accounts {
		a := accountResp{
			ID:                account.ID,
			SourceType:        account.SourceType,
			Name:              account.Name,
			AppId:             account.AppId,
			AppIdName:         strings.TrimSpace(account.AppIdName),
			AppName:           strings.TrimSpace(account.AppIdName),
			DisplayName:       strings.TrimSpace(account.AppIdName),
			Username:          account.Username,
			UserId:            string(account.UserId),
			Authorized:        account.Token != "" && account.TokenFailedReason == "",
			CreatedAt:         account.CreatedAt,
			TokenFailedReason: account.TokenFailedReason,
			BaseUrl:           account.BaseUrl,
		}
		if account.SourceType == models.SourceType115 {
			source := account.V115AuthSource()
			a.AuthSourceType = source.SourceType
			a.AuthProvider = source.Provider
			a.AppName = source.AppName
			a.AppIdName = source.AppName
			a.DisplayName = source.DisplayName
			a.RequiresEncryptionKey = source.RequiresEncryptionKey
			a.Deprecated = source.Deprecated
			if source.SourceType == v115auth.SourceTypeBuiltInRelay {
				a.AppId = ""
			}
		}
		if a.Name == "" {
			a.Name = account.Username
		}
		if account.SourceType == models.SourceTypeOpenList {
			if account.Password != "" {
				a.AuthType = "password"
			} else {
				a.AuthType = "token"
			}
		}
		resp = append(resp, a)
	}

	c.JSON(http.StatusOK, APIResponse[[]accountResp]{Code: Success, Message: "查询开放平台账号成功", Data: resp})
}

// CreateTmpAccount 创建临时开放平台账号
// @Summary 创建账号
// @Description 创建新的开放平台账号（支持 115、OpenList 等）
// @Tags 账号管理
// @Accept json
// @Produce json
// @Param source_type query string true "账号源类型"
// @Param name query string true "账号名称"
// @Param app_id query string false "APP ID（自定义时必需）"
// @Param app_id_name query string false "选择的 115 开放平台应用（QMediaSync、精选应用、自定义 APP ID）"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /account/create [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CreateTmpAccount(c *gin.Context) {
	tmpAccount := &requests.CreateAccountRequest{}
	if err := c.ShouldBind(tmpAccount); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := tmpAccount.Validate(); err != nil {
		writeAccountValidationError(c, http.StatusOK, err)
		return
	}
	// 创建临时账号
	var appId string
	var appIdName string
	if tmpAccount.SourceType == models.SourceTypeBaiduPan {
		appId = helpers.GlobalConfig.BaiDuPanAppId
	}

	if models.SourceType115 == tmpAccount.SourceType {
		source, err := v115auth.SourceFromCreateRequest(tmpAccount.AuthSourceType, tmpAccount.AuthProvider, tmpAccount.AppID, tmpAccount.SelectedApp, tmpAccount.CustomAppName)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		appId = source.StorageAppID()
		appIdName = source.StorageAppName()
	}
	authSourceType := v115auth.AuthSourceType("")
	authProvider := v115auth.AuthProvider("")
	if models.SourceType115 == tmpAccount.SourceType {
		source := v115auth.ResolveAccountSource(appId, appIdName)
		authSourceType = source.SourceType
		authProvider = source.Provider
		if tmpAccount.AuthSourceType != "" {
			authSourceType = tmpAccount.AuthSourceType
			authProvider = tmpAccount.AuthProvider
		}
	}
	account, err := models.CreateAccountWithAuthSource(
		strings.TrimSpace(tmpAccount.Name),
		tmpAccount.SourceType,
		appId,
		appIdName,
		authSourceType,
		authProvider,
	)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "创建开放平台账号失败：" + accountPersistenceMessage(err), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[models.Account]{Code: Success, Message: "创建开放平台账号成功", Data: *account})
}

// PrepareAccountAuthorization 准备更换已有账号的授权。
// 该接口只创建短时内存会话，不写入账号授权字段。
func PrepareAccountAuthorization(c *gin.Context) {
	req := &requests.PrepareAccountAuthorizationRequest{}
	if err := c.ShouldBind(req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, http.StatusBadRequest, err)
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	if account.SourceType != req.SourceType {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "更换授权只能使用与原账号相同的网盘类型", Data: nil})
		return
	}
	if account.SourceType != models.SourceType115 {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "当前账号暂不支持更换授权", Data: nil})
		return
	}
	source, err := v115auth.SourceFromCreateRequest(req.AuthSourceType, req.AuthProvider, req.AppID, req.AppIDName, req.CustomAppName)
	if err != nil || source.Deprecated {
		if err == nil {
			err = fmt.Errorf("已废弃的 115 授权来源不能作为更换目标")
		}
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	session, err := v115auth.CreateAuthorizationSession(account.ID, source)
	if err != nil {
		if errors.Is(err, v115auth.ErrAuthorizationSessionActive) {
			c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "该账号已有授权流程进行中，请先取消后再试", Data: nil})
			return
		}
		c.JSON(http.StatusInternalServerError, APIResponse[any]{Code: BadRequest, Message: "创建授权会话失败", Data: nil})
		return
	}
	// 清理不带 authorization_id 的旧二维码和 OAuth 流程；最终提交仍通过
	// 状态锁复核，避免已在远端请求中的旧结果回写新授权。
	deleteOpen115AuthStatesForAccount(account.ID)
	v115auth.DeleteLegacyOAuthStatesForAccount(account.ID)
	c.JSON(http.StatusOK, APIResponse[gin.H]{
		Code:    Success,
		Message: "授权会话已准备",
		Data: gin.H{
			"authorization_id": session.ID,
			"expires_in":       v115auth.AuthorizationSessionTTLSeconds,
		},
	})
}

// CancelAccountAuthorization 取消待提交或进行中的账号授权更换。
// 接口保持幂等，授权已经提交后再关闭弹窗也不会产生额外影响。
func CancelAccountAuthorization(c *gin.Context) {
	req := &requests.CancelAccountAuthorizationRequest{}
	if err := c.ShouldBind(req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, http.StatusBadRequest, err)
		return
	}
	if _, err := models.GetAccountById(req.AccountID); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	authorizationID := strings.TrimSpace(req.AuthorizationID)
	v115auth.CancelAuthorizationSession(authorizationID, req.AccountID)
	deleteOpen115AuthStatesForAuthorization(req.AccountID, authorizationID)
	v115auth.DeleteOAuthStatesForAuthorization(req.AccountID, authorizationID)
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "授权会话已取消", Data: nil})
}

// UpdateAccountInfo 更新账号资料
// @Summary 更新账号资料
// @Description 更新账号备注和自定义开放平台应用名，不修改授权凭据或 OpenList 连接配置
// @Tags 账号管理
// @Accept json
// @Produce json
// @Param id query integer true "账号 ID"
// @Param name query string false "账号备注"
// @Param app_id_name query string false "自定义应用名"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /account/update [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateAccountInfo(c *gin.Context) {
	req := &requests.UpdateAccountInfoRequest{}
	if err := c.ShouldBind(req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, http.StatusBadRequest, err)
		return
	}
	account, err := models.GetAccountById(req.ID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询开放平台账号失败", Data: nil})
		return
	}
	if err := account.UpdateInfo(strings.TrimSpace(req.Name), strings.TrimSpace(req.CustomAppName)); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新开放平台账号资料失败：" + accountPersistenceMessage(err), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新开放平台账号资料成功", Data: nil})
}

// DeleteAccount 删除开放平台账号
// @Summary 删除账号
// @Description 删除指定 ID 的开放平台账号
// @Tags 账号管理
// @Accept json
// @Produce json
// @Param id query integer true "账号 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /account/delete [delete]
// @Security JwtAuth
// @Security ApiKeyAuth
func DeleteAccount(c *gin.Context) {
	req := &requests.DeleteAccountRequest{}
	if err := c.ShouldBind(req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, http.StatusBadRequest, err)
		return
	}
	account, err := models.GetAccountById(req.ID)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询开放平台账号失败", Data: nil})
		return
	}
	err = account.Delete()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "删除开放平台账号成功", Data: nil})
}

// CreateOpenListAccount 创建或更新 OpenList 账号。
// @Summary 创建或更新 OpenList 账号
// @Description 创建新的 OpenList 账号或更新现有账号的凭证，支持用户名密码和 Token 认证；同认证方式更新可复用已有凭据，切换认证方式需提交目标方式凭据
// @Tags 账号管理
// @Accept json
// @Produce json
// @Param id query integer false "账号 ID（指定则为更新操作）"
// @Param base_url query string true "OpenList 服务器地址"
// @Param username query string true "用户名"
// @Param password query string true "密码"
// @Param token query string false "直接提供的访问 Token（优先使用）"
// @Param auth_type query string false "认证方式（password 或 token）"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /account/openlist [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func CreateOpenListAccount(c *gin.Context) {
	req := &requests.CreateOpenListAccountRequest{}
	if err := c.ShouldBind(req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, http.StatusBadRequest, err)
		return
	}
	if req.ID != 0 {
		account, err := models.GetAccountById(req.ID)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("查询 OpenList 账号失败：%s", err.Error()), Data: nil})
			return
		}
		if err := account.UpdateOpenList(req.BaseURL, req.Username, req.Password, req.Token, req.AuthType); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("更新 OpenList 账号失败：%s", accountPersistenceMessage(err)), Data: nil})
			return
		}
		c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "OpenList 账号已更新", Data: nil})
		return
	}
	// 创建 OpenList 账号
	_, err := models.CreateOpenListAccount(req.BaseURL, req.Username, req.Password, req.Token)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: fmt.Sprintf("创建 OpenList 账号失败：%s", accountPersistenceMessage(err)), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "OpenList 账号已创建", Data: nil})
}
