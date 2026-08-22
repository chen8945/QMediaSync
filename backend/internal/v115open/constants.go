package v115open

import (
	"fmt"

	"qmediasync/internal/helpers"
)

const (
	// API 常量
	ACCESS_TOKEN_AUTH_FAIL   = 40140126 // 刷新访问凭证
	ACCESS_TOKEN_EXPIRY_CODE = 40140125 // 刷新访问凭证
	ACCESS_AUTH_INVALID      = 40140124 // 刷新访问凭证
	REFRESH_TOKEN_INVALID    = 40140116 // 重新授权
	REQUEST_MAX_LIMIT_CODE   = 770004   // 访问频率过高
	REQUEST_RATE_LIMIT_CODE  = 406      // 已达到当前访问上限，购买更高等级 VIP 可获更多额度
	OPEN_BASE_URL            = "https://proapi.115.com"

	// 刷新访问凭证（/open/refreshToken）的错误码
	REFRESH_TOKEN_FORMAT_INVALID = 40140114 // refresh_token 格式错误（防篡改）
	REFRESH_TOKEN_SIGN_INVALID   = 40140115 // refresh_token 签名校验失败（防篡改）
	REFRESH_TOO_FREQUENT         = 40140117 // 刷新访问凭证过于频繁
	REFRESH_TOKEN_EXPIRED        = 40140119 // refresh_token 已过期，需重新授权
	REFRESH_TOKEN_CHECK_FAILED   = 40140120 // refresh_token 校验失败（防篡改）
	TOKEN_REFRESH_FAIL           = 40140121 // 刷新访问凭证失败，可重试

	// 重试配置
	DEFAULT_MAX_RETRIES = 3
	DEFAULT_RETRY_DELAY = 1

	// 超时配置
	DEFAULT_TIMEOUT = 30 // 秒
)

var DEFAULTUA = fmt.Sprintf("QMediaSync-GoClient/%s", helpers.Version)
