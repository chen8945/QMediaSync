package requests

import (
	"strings"

	"qmediasync/internal/validation"
)

// HTTPProxyRequest HTTP 代理请求。
type HTTPProxyRequest struct {
	HTTPProxy                string `form:"http_proxy" json:"http_proxy"`
	Detailed                 int    `form:"detailed" json:"detailed"`
	PreserveProxyCredentials *bool  `form:"preserve_proxy_credentials" json:"preserve_proxy_credentials"`
}

// NormalizedHTTPProxy 返回去除首尾空白后的代理地址。
// 校验层对副本做 TrimSpace，保存和拨号必须用同一份规范化结果，否则带首尾空白的地址会通过校验但解析失败。
func (r HTTPProxyRequest) NormalizedHTTPProxy() string {
	return strings.TrimSpace(r.HTTPProxy)
}

// ValidateSave 校验保存 HTTP 代理请求。
func (r HTTPProxyRequest) ValidateSave() error {
	if err := validation.ProxyURL("http_proxy", r.HTTPProxy, true); err != nil {
		return err
	}
	return validation.OneOfInt("detailed", r.Detailed, []int{0, 1})
}

// ValidateTest 校验测试 HTTP 代理请求。
func (r HTTPProxyRequest) ValidateTest() error {
	if err := validation.ProxyURL("http_proxy", r.HTTPProxy, false); err != nil {
		return err
	}
	return validation.OneOfInt("detailed", r.Detailed, []int{0, 1})
}

// AccountIDRequest 账号 ID 请求。
type AccountIDRequest struct {
	AccountID uint `json:"account_id" form:"account_id"`
}

// Validate 校验账号 ID 请求。
func (r AccountIDRequest) Validate() error {
	return validation.PositiveID("account_id", r.AccountID)
}

// OAuthURLRequest OAuth 登录地址请求。
type OAuthURLRequest struct {
	AccountID       uint   `json:"account_id" form:"account_id"`
	RedirectURL     string `json:"redirect_url" form:"redirect_url"`
	AuthorizationID string `json:"authorization_id" form:"authorization_id"`
}

// Validate 校验 OAuth 登录地址请求。
func (r OAuthURLRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if err := validation.HTTPURL("redirect_url", r.RedirectURL, true); err != nil {
		return err
	}
	return validateAuthorizationID(r.AuthorizationID)
}

// OAuthConfirmRequest OAuth 确认请求。
type OAuthConfirmRequest struct {
	AccountID       uint              `json:"account_id" form:"account_id"`
	AuthorizationID string            `json:"authorization_id" form:"authorization_id"`
	Data            string            `json:"data" form:"data"`
	Payload         map[string]string `json:"payload" form:"payload"`
}

// Validate 校验 OAuth 确认请求。
func (r OAuthConfirmRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if strings.TrimSpace(r.Data) == "" && len(r.Payload) == 0 {
		return validation.New("data", "data 和 payload 不能同时为空")
	}
	return validateAuthorizationID(r.AuthorizationID)
}

// OAuthStatusRequest OAuth 状态请求。
type OAuthStatusRequest struct {
	AccountID       uint   `json:"account_id" form:"account_id"`
	State           string `json:"state" form:"state"`
	AuthorizationID string `json:"authorization_id" form:"authorization_id"`
}

// Validate 校验 OAuth 状态请求。
func (r OAuthStatusRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if err := validation.Length("state", r.State, 1, 512); err != nil {
		return err
	}
	return validateAuthorizationID(r.AuthorizationID)
}

// QRCodeOpenRequest 获取二维码请求。
type QRCodeOpenRequest struct {
	AccountID       uint   `json:"account_id" form:"account_id"`
	AuthorizationID string `json:"authorization_id" form:"authorization_id"`
}

// Validate 校验获取二维码请求。
func (r QRCodeOpenRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	return validateAuthorizationID(r.AuthorizationID)
}

// QRCodeStatusRequest 二维码状态请求。
type QRCodeStatusRequest struct {
	UID             string `json:"uid" form:"uid"`
	AccountID       uint   `json:"account_id" form:"account_id"`
	AuthorizationID string `json:"authorization_id" form:"authorization_id"`
}

// Validate 校验二维码状态请求。
func (r QRCodeStatusRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	if err := validation.Length("uid", r.UID, 1, 512); err != nil {
		return err
	}
	return validateAuthorizationID(r.AuthorizationID)
}

func validateAuthorizationID(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	return validation.Length("authorization_id", id, 1, 128)
}

// RemoteFileURLRequest 远程文件直链请求。
type RemoteFileURLRequest struct {
	UserID   string `json:"userid" form:"userid"`
	PickCode string `json:"pickcode" form:"pickcode"`
	Force    int    `json:"force" form:"force"`
}

// Validate 校验远程文件直链请求。
func (r RemoteFileURLRequest) Validate() error {
	if err := validation.NonBlank("pickcode", r.PickCode); err != nil {
		return err
	}
	return validation.OneOfInt("force", r.Force, []int{0, 1})
}

// Proxy115Request 115/百度下载反代请求。
type Proxy115Request struct {
	URL      string `json:"url" form:"url"`
	BaiduPan string `json:"baidupan" form:"baidupan"`
}

// Validate 校验下载反代请求。
func (r Proxy115Request) Validate() error {
	return validation.DownloadProxyURL("url", r.URL)
}

// QueueRateLimitRequest 请求队列限流配置。
type QueueRateLimitRequest struct {
	QPS int `json:"qps" binding:"required"`
	QPM int `json:"qpm" binding:"required"`
	QPH int `json:"qph" binding:"required"`
}

// Validate 校验请求队列限流配置。
func (r QueueRateLimitRequest) Validate() error {
	if err := validation.RangeInt("qps", r.QPS, 1, 1000); err != nil {
		return err
	}
	if err := validation.RangeInt("qpm", r.QPM, 1, 100000); err != nil {
		return err
	}
	return validation.RangeInt("qph", r.QPH, 1, 1000000)
}

// CleanRequestStatsRequest 清理请求统计请求。
type CleanRequestStatsRequest struct {
	Days int `json:"days" binding:"required"`
}

// Validate 校验清理请求统计请求。
func (r CleanRequestStatsRequest) Validate() error {
	return validation.RangeInt("days", r.Days, 1, 365)
}

// OpenListFileURLRequest OpenList 文件直链请求。
type OpenListFileURLRequest struct {
	AccountID uint   `json:"account_id" form:"account_id"`
	Path      string `json:"path" form:"path"`
}

// Validate 校验 OpenList 文件直链请求。
func (r OpenListFileURLRequest) Validate() error {
	if err := validation.PositiveID("account_id", r.AccountID); err != nil {
		return err
	}
	return validation.NonBlank("path", r.Path)
}
