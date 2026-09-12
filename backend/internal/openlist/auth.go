package openlist

import (
	"fmt"
	"net/http"

	"qmediasync/internal/helpers"
)

type TokenData struct {
	Token string `json:"token"`
}

// TokenSaveEvent 携带登录开始前的配置，供账号层条件保存；不得记录其中的凭据。
type TokenSaveEvent struct {
	AccountID     uint
	BaseURL       string
	Username      string
	Password      string
	PreviousToken string
	Token         string
}

// 获取开放平台 Token
// 用于自动登录开放平台
// POST /api/auth/login
func (c *Client) GetToken() (*TokenData, error) {
	return c.getToken(c.snapshot())
}

func (c *Client) getToken(state clientState) (*TokenData, error) {
	type tokenReq struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	reqData := &tokenReq{
		Username: state.username,
		Password: state.password,
	}
	var result Resp[TokenData]
	req := c.client.R().SetBody(reqData).SetMethod(http.MethodPost).SetResult(&result)
	_, err := c.doRequestWithState("/api/auth/login", req, MakeRequestConfig(0, 1, 5), state)
	if err != nil {
		helpers.OpenListLog.Errorf("OpenList 获取访问凭证失败：%s", err.Error())
		return nil, err
	}
	tokenData := result.Data
	helpers.OpenListLog.Infof("OpenList 获取访问凭证成功")
	if c.snapshot() == state && state.accountID != 0 {
		// 同步保存不持有客户端锁；数据库必须继续比较登录前的原始配置。
		helpers.PublishSync(helpers.SaveOpenListTokenEvent, TokenSaveEvent{
			AccountID:     state.accountID,
			BaseURL:       state.baseURL,
			Username:      state.username,
			Password:      state.password,
			PreviousToken: state.accessToken,
			Token:         tokenData.Token,
		})
	}
	// 回调期间也可能保存新配置，必须在回调结束后再次原子比较。
	c.stateMu.Lock()
	current := c.snapshotLocked()
	if current == state {
		c.AccessToken = tokenData.Token
	} else if current.sameLogin(state) && current.accessToken != "" {
		tokenData.Token = current.accessToken
	}
	c.stateMu.Unlock()
	return &tokenData, nil
}

type UserInfoResp struct {
	ID       uint   `json:"id"`
	Username string `json:"username"`
}
type RespWrapper struct {
	Code    int          `json:"code"`
	Message string       `json:"message"`
	Data    UserInfoResp `json:"data"`
}

// GetUserInfo 验证提供的 Token 是否有效
// 通过调用用户信息接口来验证 Token，并返回用户名和用户 ID
func (c *Client) GetUserInfo(token string) (*UserInfoResp, error) {

	var result RespWrapper
	req := c.client.R().
		SetHeader("Authorization", token).
		SetMethod(http.MethodGet).
		SetResult(&result)
	_, err := c.doRequest("/api/me", req, MakeRequestConfig(1, 1, 5))
	if err != nil {
		helpers.OpenListLog.Errorf("验证 Token 失败：%s", err.Error())
		return nil, err
	}
	if result.Code != http.StatusOK {
		return nil, fmt.Errorf("Token 验证失败：%s", result.Message)
	}
	helpers.OpenListLog.Infof("Token 验证成功，用户：%s（ID：%d）", result.Data.Username, result.Data.ID)
	return &result.Data, nil
}
