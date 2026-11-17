package controllers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/openlist"
	"qmediasync/internal/requests"
	v115auth "qmediasync/internal/v115/auth"
	v115open "qmediasync/internal/v115/openapi"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func friendlyAccountValidationMessage(err error) string {
	validationErr, ok := errors.AsType[validation.Error](err)
	if !ok {
		return "请求参数不正确，请检查后再试"
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

func writeAccountValidationError(c *gin.Context, err error) {
	failure := httpapi.Error{
		Code:    "INVALID_REQUEST",
		Message: friendlyAccountValidationMessage(err),
		Cause:   err,
	}
	if fieldErr, ok := errors.AsType[validation.Error](err); ok {
		failure.FieldErrors = []httpapi.FieldError{{Field: fieldErr.Field, Message: fieldErr.Message}}
	}
	httpapi.Failure(c, http.StatusBadRequest, failure)
}

func writeAccountLookupError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "查询账号失败"
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code, message = http.StatusNotFound, "ACCOUNT_NOT_FOUND", "账号 ID 不存在"
	}
	httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
}

func writeCloudFileLookupError(c *gin.Context, err error) {
	status, code, message := http.StatusInternalServerError, "INTERNAL_ERROR", "查询文件记录失败"
	if errors.Is(err, gorm.ErrRecordNotFound) {
		status, code, message = http.StatusNotFound, "FILE_NOT_FOUND", "文件标识不存在"
	}
	httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err})
}

func writeAccountPersistenceError(c *gin.Context, err error, message string, secrets ...string) {
	if verification, ok := errors.AsType[*models.OpenListVerificationError](err); ok {
		secrets = append(secrets, verification.Secrets...)
		writeCloudUpstreamError(c, err, "验证 OpenList 账号失败", secrets...)
		return
	}
	status, code := http.StatusInternalServerError, "INTERNAL_ERROR"
	switch {
	case errors.Is(err, models.ErrAccountNameTaken):
		status, code, message = http.StatusConflict, "ACCOUNT_NAME_TAKEN", "账号备注已存在，请换一个"
	case errors.Is(err, models.ErrAccountUserIDTaken):
		status, code, message = http.StatusConflict, "ACCOUNT_USER_ID_TAKEN", "当前账号已存在，不允许添加重复账号"
	case errors.Is(err, models.ErrAccountAuthorizationChanged):
		status, code, message = http.StatusConflict, "ACCOUNT_AUTHORIZATION_CHANGED", "账号配置已更新，请重新加载后重试"
	case errors.Is(err, models.ErrAccountHasSyncPaths):
		status, code, message = http.StatusConflict, "ACCOUNT_HAS_SYNC_PATHS", "账号仍有关联的同步目录，请先删除关联目录"
	case errors.Is(err, v115auth.ErrAuthorizationSessionInactive):
		status, code, message = http.StatusConflict, "AUTHORIZATION_INACTIVE", "授权会话不存在、已过期或已取消"
	case errors.Is(err, v115auth.ErrAuthorizationSessionActive):
		status, code, message = http.StatusConflict, "AUTHORIZATION_IN_PROGRESS", "该账号已有授权流程进行中，请先取消后再试"
	case errors.Is(err, openlist.ErrTokenExpired):
		status, code, message = http.StatusBadRequest, "UPSTREAM_CREDENTIALS_INVALID", "网盘访问凭据无效，请重新授权"
	}
	if _, ok := errors.AsType[validation.Error](err); ok {
		writeAccountValidationError(c, err)
		return
	}
	httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err, Secrets: secrets})
}

func writeCloudUpstreamError(c *gin.Context, err error, message string, secrets ...string) {
	status, code := http.StatusBadGateway, "UPSTREAM_ERROR"
	if errors.Is(err, context.Canceled) {
		status, code = http.StatusServiceUnavailable, "REQUEST_CANCELED"
	} else if errors.Is(err, context.DeadlineExceeded) {
		status, code = http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT"
	} else if networkErr, ok := errors.AsType[net.Error](err); ok && networkErr.Timeout() {
		status, code = http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT"
	} else if _, ok := errors.AsType[*baidupan.TokenInvalidError](err); ok || errors.Is(err, openlist.ErrTokenExpired) {
		status, code, message = http.StatusBadRequest, "UPSTREAM_CREDENTIALS_INVALID", "网盘访问凭据无效，请重新授权"
	} else if apiErr, ok := errors.AsType[*v115open.OpenAPIError](err); ok {
		switch {
		case apiErr.HTTPStatus == http.StatusUnauthorized || v115open.IsRefreshTokenDead(err) ||
			apiErr.Code == v115open.ACCESS_TOKEN_AUTH_FAIL || apiErr.Code == v115open.ACCESS_TOKEN_EXPIRY_CODE || apiErr.Code == v115open.ACCESS_AUTH_INVALID:
			status, code, message = http.StatusBadRequest, "UPSTREAM_CREDENTIALS_INVALID", "网盘访问凭据无效，请重新授权"
		case v115open.IsRateLimited(err):
			status, code, message = http.StatusServiceUnavailable, "UPSTREAM_RATE_LIMITED", "网盘访问频率受限，请稍后重试"
		}
	}
	httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: err, Secrets: secrets})
}

// GetAccountList 获取所有开放平台账号列表
func GetAccountList(c *gin.Context) {
	accounts, err := models.ReadAccounts(c.Request.Context())
	if err != nil {
		writeAccountPersistenceError(c, err, "查询开放平台账号失败")
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

	httpapi.Success(c, http.StatusOK, resp, "查询开放平台账号成功")
}

// CreateTmpAccount 创建临时开放平台账号
func CreateTmpAccount(c *gin.Context) {
	tmpAccount := &requests.CreateAccountRequest{}
	if err := c.ShouldBind(tmpAccount); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := tmpAccount.Validate(); err != nil {
		writeAccountValidationError(c, err)
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
			writeAccountValidationError(c, err)
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
		writeAccountPersistenceError(c, err, "创建开放平台账号失败")
		return
	}
	httpapi.Success(c, http.StatusCreated, *account, "创建开放平台账号成功")
}

// PrepareAccountAuthorization 准备更换已有账号的授权。
// 该接口只创建短时内存会话，不写入账号授权字段。
func PrepareAccountAuthorization(c *gin.Context) {
	req := &requests.PrepareAccountAuthorizationRequest{}
	if err := c.ShouldBind(req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
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
	if account.SourceType != req.SourceType {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "更换授权只能使用与原账号相同的网盘类型"})
		return
	}
	if account.SourceType != models.SourceType115 {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "当前账号暂不支持更换授权"})
		return
	}
	source, err := v115auth.SourceFromCreateRequest(req.AuthSourceType, req.AuthProvider, req.AppID, req.AppIDName, req.CustomAppName)
	if err != nil || source.Deprecated {
		if err == nil {
			err = fmt.Errorf("已废弃的 115 授权来源不能作为更换目标")
		}
		writeAccountValidationError(c, err)
		return
	}
	session, err := v115auth.CreateAuthorizationSession(account.ID, source)
	if err != nil {
		writeAccountPersistenceError(c, err, "创建授权会话失败")
		return
	}
	// 清理不带 authorization_id 的旧二维码和 OAuth 流程；最终提交仍通过
	// 状态锁复核，避免已在远端请求中的旧结果回写新授权。
	deleteOpen115AuthStatesForAccount(account.ID)
	v115auth.DeleteLegacyOAuthStatesForAccount(account.ID)
	httpapi.Success(c, http.StatusOK, gin.H{
		"authorization_id": session.ID,
		"expires_in":       v115auth.AuthorizationSessionTTLSeconds,
	}, "授权会话已准备")
}

// CancelAccountAuthorization 取消待提交或进行中的账号授权更换。
// 接口保持幂等，授权已经提交后再关闭弹窗也不会产生额外影响。
func CancelAccountAuthorization(c *gin.Context) {
	req := &requests.CancelAccountAuthorizationRequest{}
	if err := c.ShouldBind(req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	if _, err := models.ReadAccount(c.Request.Context(), req.AccountID); err != nil {
		writeAccountLookupError(c, err)
		return
	}
	authorizationID := strings.TrimSpace(req.AuthorizationID)
	v115auth.CancelAuthorizationSession(authorizationID, req.AccountID)
	deleteOpen115AuthStatesForAuthorization(req.AccountID, authorizationID)
	v115auth.DeleteOAuthStatesForAuthorization(req.AccountID, authorizationID)
	httpapi.Success(c, http.StatusOK, nil, "授权会话已取消")
}

// UpdateAccountInfo 更新账号资料
func UpdateAccountInfo(c *gin.Context) {
	req := &requests.UpdateAccountInfoRequest{}
	if err := c.ShouldBind(req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.ID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	if err := account.UpdateInfo(strings.TrimSpace(req.Name), strings.TrimSpace(req.CustomAppName)); err != nil {
		writeAccountPersistenceError(c, err, "更新开放平台账号资料失败")
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "更新开放平台账号资料成功")
}

// DeleteAccount 删除开放平台账号
func DeleteAccount(c *gin.Context) {
	req := &requests.DeleteAccountRequest{}
	if err := c.ShouldBind(req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.ID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	err = account.Delete()
	if err != nil {
		writeAccountPersistenceError(c, err, "删除开放平台账号失败")
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "删除开放平台账号成功")
}

// CreateOpenListAccount 创建或更新 OpenList 账号。
func CreateOpenListAccount(c *gin.Context) {
	req := &requests.CreateOpenListAccountRequest{}
	if err := c.ShouldBind(req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	if req.ID != 0 {
		account, err := models.ReadAccount(c.Request.Context(), req.ID)
		if err != nil {
			writeAccountLookupError(c, err)
			return
		}
		if err := account.UpdateOpenList(req.BaseURL, req.Username, req.Password, req.Token, req.AuthType); err != nil {
			writeAccountPersistenceError(c, err, "更新 OpenList 账号失败", req.Password, req.Token, account.Password, account.Token)
			return
		}
		httpapi.Success(c, http.StatusOK, nil, "OpenList 账号已更新")
		return
	}
	// 创建 OpenList 账号
	_, err := models.CreateOpenListAccount(req.BaseURL, req.Username, req.Password, req.Token)
	if err != nil {
		writeAccountPersistenceError(c, err, "创建 OpenList 账号失败", req.Password, req.Token)
		return
	}
	httpapi.Success(c, http.StatusCreated, nil, "OpenList 账号已创建")
}
