package v115open

import (
	"qmediasync/internal/helpers"

	"fmt"
	"time"
)

type QrCodeScanStatus int // 二维码扫码状态
const (
	QrCodeScanStatusExpired    QrCodeScanStatus = 5 // 已过期
	QrCodeScanStatusNotScanned QrCodeScanStatus = 2 // 未扫码
	QrCodeScanStatusScanned    QrCodeScanStatus = 3 // 已扫码
	QrCodeScanStatusConfirmed  QrCodeScanStatus = 4 // 已确认
)

type QrCodeData struct {
	Uid    string `json:"uid"`
	Time   int64  `json:"time"`
	Sign   string `json:"sign"`
	Qrcode string `json:"qrcode"`
}

type QrCodeDataReturn struct {
	QrCodeData
	CodeVerifier string `json:"code_verifier"` // 用于开放平台登录
}

type QrCodeStatus struct {
	Msg    string `json:"msg"`
	Status int    `json:"status"` // 1. 已扫码，待确认；2. 已确认，结束轮询。
}

type TokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// 获取 115 开放平台登录二维码
// POST https://passportapi.115.com/open/authDeviceCode
func (c *OpenClient) GetQrCode() (*QrCodeDataReturn, error) {
	data := make(map[string]string)
	codeVerifier := helpers.RandStr(64)
	data["client_id"] = c.AppId
	data["code_challenge"] = GenCodeChallenge(codeVerifier)
	data["code_challenge_method"] = "sha256"
	req := c.client.R().SetFormData(data).SetMethod("POST")
	qrData := StructOrArray[QrCodeData]{}
	_, respData, err := c.doRequest("https://passportapi.115.com/open/authDeviceCode", req, MakeRequestConfig(0, 0, 0))
	if err != nil {
		helpers.V115Log.Errorf("获取 115 开放平台授权二维码失败：%s", err.Error())
		return nil, err
	}
	if respData.State != 1 {
		helpers.V115Log.Errorf("获取 115 开放平台授权二维码失败：%+v", respData)
		return nil, NewOpenAPIResponseError(respData.Code, respData.Errno, respData.Message, respData.Error, "获取 115 开放平台授权二维码失败")
	}
	// 解析 respData.Data
	qrData.UnmarshalJSON(respData.Data)
	if qrData.Value == nil {
		// 获取二维码失败
		helpers.V115Log.Errorf("获取二维码失败：%+v", qrData)
		return nil, fmt.Errorf("获取二维码失败")
	}
	var returnData QrCodeDataReturn
	returnData.CodeVerifier = codeVerifier
	returnData.QrCodeData = *qrData.Value
	return &returnData, nil
}

// 查询扫码状态
// GET https://qrcodeapi.115.com/get/status/
// body: {"uid": "", time: 0, sign: ""}
func (c *OpenClient) QrCodeScanStatus(codeData *QrCodeData) (QrCodeScanStatus, error) {
	data := make(map[string]string)
	data["uid"] = codeData.Uid
	data["time"] = fmt.Sprint(codeData.Time)
	data["sign"] = codeData.Sign
	req := c.client.R().SetQueryParams(data).SetMethod("GET")
	_, respData, err := c.doRequest("https://qrcodeapi.115.com/get/status/", req, MakeRequestConfig(1, 1, 60))
	if err != nil {
		helpers.V115Log.Errorf("获取二维码状态失败：%s", err.Error())
		return QrCodeScanStatusExpired, err
	}
	if respData.State != 1 {
		helpers.V115Log.Errorf("获取二维码状态失败：%+v", respData)
		return QrCodeScanStatusExpired, NewOpenAPIResponseError(respData.Code, respData.Errno, respData.Message, respData.Error, "获取二维码状态失败")
	}
	qrStatusData := StructOrArray[QrCodeStatus]{}
	qrStatusData.UnmarshalJSON(respData.Data)
	if qrStatusData.Value == nil {
		helpers.V115Log.Errorf("获取二维码状态失败：%+v", qrStatusData)
		return QrCodeScanStatusExpired, fmt.Errorf("获取二维码状态失败")
	}
	qrCodeStatus := qrStatusData.Value
	if qrCodeStatus.Status == 0 {
		return QrCodeScanStatusNotScanned, nil
	}
	if qrCodeStatus.Status == 1 {
		helpers.V115Log.Info("二维码已扫码，待确认")
		return QrCodeScanStatusScanned, nil
	}
	if qrCodeStatus.Status == 2 {
		helpers.V115Log.Info("二维码已确认")
		return QrCodeScanStatusConfirmed, nil
	}
	return QrCodeScanStatusExpired, nil
}

func (s QrCodeScanStatus) String() string {
	switch s {
	case QrCodeScanStatusNotScanned:
		return "waiting"
	case QrCodeScanStatusScanned:
		return "scanned"
	case QrCodeScanStatusConfirmed:
		return "confirmed"
	default:
		return "expired"
	}
}

func (s QrCodeScanStatus) Tip() string {
	switch s {
	case QrCodeScanStatusNotScanned:
		return "等待扫码"
	case QrCodeScanStatusScanned:
		return "已扫码，请在 115 客户端确认"
	case QrCodeScanStatusConfirmed:
		return "授权成功"
	default:
		return "二维码已过期"
	}
}

// 获取 115 开放平台 Token
// 用于自动登录 115 开放平台
// POST https://passportapi.115.com/open/deviceCodeToToken
func (c *OpenClient) GetToken(codeData *QrCodeDataReturn) (*TokenData, error) {
	data := make(map[string]string)
	data["uid"] = codeData.Uid
	data["code_verifier"] = codeData.CodeVerifier
	req := c.client.R().SetFormData(data).SetMethod("POST")
	_, respData, err := c.doRequest("https://passportapi.115.com/open/deviceCodeToToken", req, MakeRequestConfig(0, 0, 0))
	if err != nil {
		helpers.V115Log.Errorf("115 开放平台获取访问凭证失败：%s", err.Error())
		return nil, err
	}
	tokenDataOrArray := StructOrArray[TokenData]{}
	tokenDataOrArray.UnmarshalJSON(respData.Data)
	tokenData := tokenDataOrArray.Value
	if tokenData == nil {
		helpers.V115Log.Errorf("获取访问凭证失败：%+v", tokenDataOrArray)
		return nil, fmt.Errorf("获取访问凭证失败")
	}
	helpers.V115Log.Infof("115 开放平台获取访问凭证成功")
	// 给客户端设置新的 Token
	c.SetAuthToken(tokenData.AccessToken, tokenData.RefreshToken)
	return tokenData, nil
}

// 刷新 115 开放平台的 access_token
// https://passportapi.115.com/open/refreshToken
func (c *OpenClient) RefreshToken(refreshToken string) (*TokenData, error) {
	if refreshToken == "" {
		refreshToken = c.RefreshTokenStr
	}
	expectedAccessToken := c.AccessToken
	expectedRefreshToken := refreshToken
	// helpers.AppLogger.Infof("115 开放平台刷新访问凭证，refresh_token=%s", refreshToken)
	data := make(map[string]string)
	data["refresh_token"] = refreshToken
	req := c.client.R().SetFormData(data).SetMethod("POST")
	refreshConfig := &RequestConfig{
		MaxRetries: 0,
		RetryDelay: 0,
		Timeout:    300 * time.Second,
	}
	_, respData, err := c.doRequest("https://passportapi.115.com/open/refreshToken", req, refreshConfig)
	if err != nil {
		c.SetAuthToken("", "")
		helpers.AppLogger.Errorf("115 开放平台刷新访问凭证失败：%v", err)
		// 清空 Token，让用户重新授权
		helpers.PublishSync(helpers.V115TokenInValidEvent, map[string]any{
			"account_id":    c.AccountId,
			"reason":        err.Error(),
			"token":         expectedAccessToken,
			"refresh_token": expectedRefreshToken,
		})
		return nil, err
	}
	if respData.State != 1 {
		c.SetAuthToken("", "")
		helpers.AppLogger.Errorf("115 开放平台刷新访问凭证失败：%+v", respData)
		// 清空 Token，让用户重新授权
		helpers.PublishSync(helpers.V115TokenInValidEvent, map[string]any{
			"account_id":    c.AccountId,
			"reason":        respData.Message,
			"token":         expectedAccessToken,
			"refresh_token": expectedRefreshToken,
		})
		return nil, NewOpenAPIResponseError(respData.Code, respData.Errno, respData.Message, respData.Error, "115 开放平台刷新访问凭证失败")
	}
	tokenDataOrArray := StructOrArray[TokenData]{}
	tokenDataOrArray.UnmarshalJSON(respData.Data)
	tokenData := tokenDataOrArray.Value
	if tokenData == nil {
		helpers.AppLogger.Errorf("115 获取访问凭证失败：%+v", tokenDataOrArray)
		return nil, fmt.Errorf("115 获取访问凭证失败")
	}
	helpers.AppLogger.Infof("115 开放平台刷新访问凭证成功")
	// 给客户端设置新的 Token
	c.SetAuthToken(tokenData.AccessToken, tokenData.RefreshToken)
	// 通知修改数据库
	return tokenData, nil
}
