package v115open

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
)

// OpenAPIError 保留 115 开放平台返回的原始错误信息。
type OpenAPIError struct {
	Code       int
	Message    string
	HTTPStatus int
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

// ErrDownloadURLNotReady 表示成功响应暂未包含可用下载地址。
var ErrDownloadURLNotReady = errors.New("115 下载地址尚未就绪")

// ErrPlaybackRequestNotSent 表示播放请求在发送 HTTP 前已退出，远端操作尚未执行。
var ErrPlaybackRequestNotSent = errors.New("115 播放请求未发送")

func playbackResponseError(status int, resp *RespBaseBool[json.RawMessage]) *OpenAPIError {
	code := 0
	if resp != nil {
		code = resp.Code
		if code == 0 {
			code = resp.Errno
		}
	}
	// 不把远端消息拼入播放日志，消息可能回显访问凭据或带签名的 URL。
	message := "115 播放接口请求失败"
	switch code {
	case 70004, 31004:
		message = "115 文件尚未上传完整"
	case 231011:
		message = "115 文件已删除"
	case 590075:
		message = "115 操作过于频繁"
	case 91005:
		message = "115 空间不足，无法复制文件"
	case 50003, 50015:
		message = "115 文件提取码不存在或已删除"
	}
	return &OpenAPIError{Code: code, HTTPStatus: status, Message: message}
}

// IsRateLimited 根据 HTTP 状态和已知 115 错误码判断限流。
func IsRateLimited(err error) bool {
	var apiErr *OpenAPIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.HTTPStatus == http.StatusTooManyRequests {
		return true
	}
	switch apiErr.Code {
	case REQUEST_MAX_LIMIT_CODE, REQUEST_RATE_LIMIT_CODE, REFRESH_TOO_FREQUENT, 590075:
		return true
	}
	return false
}

// IsAlreadyDeleted 判断删除目标是否已被 115 明确标记为删除，供清理幂等收敛。
func IsAlreadyDeleted(err error) bool {
	var apiErr *OpenAPIError
	return errors.As(err, &apiErr) && apiErr.Code == 231011
}

// IsPlaybackRetryable 判断副本取链是否值得在播放总时限内短暂重试。
// 未知业务错误、授权错误、限流和调用方取消均不在此处重试。
func IsPlaybackRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || IsRateLimited(err) {
		return false
	}
	if errors.Is(err, ErrDownloadURLNotReady) {
		return true
	}
	var apiErr *OpenAPIError
	if errors.As(err, &apiErr) {
		if apiErr.HTTPStatus == http.StatusUnauthorized || apiErr.HTTPStatus == http.StatusForbidden {
			return false
		}
		if apiErr.Code == 70004 || apiErr.Code == 31004 {
			return true
		}
		if apiErr.Code != 0 {
			return false
		}
		return apiErr.HTTPStatus == http.StatusRequestTimeout || apiErr.HTTPStatus >= http.StatusInternalServerError
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}
