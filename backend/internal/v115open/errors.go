package v115open

import (
	"errors"
	"fmt"
)

// OpenAPIError 保留 115 开放平台返回的原始错误信息。
type OpenAPIError struct {
	Code    int
	Message string
}

func NewOpenAPIError(code int, message string) *OpenAPIError {
	if message == "" {
		message = "未知错误"
	}
	return &OpenAPIError{Code: code, Message: message}
}

func NewOpenAPIResponseError(code int, errno int, message string, errorText string, fallback string) error {
	if code == 0 {
		code = errno
	}
	if message == "" {
		message = errorText
	}
	if code == 0 && message == "" {
		return fmt.Errorf("%s", fallback)
	}
	return NewOpenAPIError(code, message)
}

func (e *OpenAPIError) Error() string {
	if e.Code == 0 {
		return fmt.Sprintf("115 接口错误：%s", e.Message)
	}
	return fmt.Sprintf("115 接口错误（%d）：%s", e.Code, e.Message)
}

// IsRefreshTokenDead 判断刷新访问凭证返回的错误是否表示 refresh_token 已无法继续使用。
// 仅 115 明确判定 refresh_token 无效、过期或校验失败时返回 true；
// 网络错误和可重试的业务失败（如频控、40140121）返回 false，调用方应保留凭据等待下次刷新。
func IsRefreshTokenDead(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *OpenAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case REFRESH_TOKEN_FORMAT_INVALID, REFRESH_TOKEN_SIGN_INVALID, REFRESH_TOKEN_INVALID, REFRESH_TOKEN_EXPIRED, REFRESH_TOKEN_CHECK_FAILED:
		return true
	}
	return false
}
