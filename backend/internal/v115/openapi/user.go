package v115open

import (
	"context"
	"encoding/json"
	"fmt"

	"qmediasync/internal/helpers"
)

type UserInfoSpace struct {
	Size       int64  `json:"size"`
	SizeFormat string `json:"size_format"`
}
type UserInfo struct {
	UserId      json.Number `json:"user_id"`
	UserName    string      `json:"user_name"`
	UserFaceS   string      `json:"user_face_s"`
	UserFaceM   string      `json:"user_face_m"`
	UserFaceL   string      `json:"user_face_l"`
	RtSpaceInfo struct {
		AllTotal  UserInfoSpace `json:"all_total"`
		AllRemain UserInfoSpace `json:"all_remain"`
		AllUse    UserInfoSpace `json:"all_use"`
	} `json:"rt_space_info"`
	VipInfo struct {
		LevelName string `json:"level_name"`
		Expire    int64  `json:"expire"`
	} `json:"vip_info"`
} // 115 用户信息，每次都通过接口请求，作为全局变量使用

// 获取用户信息
// GET /open/user/info
// return {"state": bool}
func (c *OpenClient) UserInfo() (*UserInfo, error) {
	url := fmt.Sprintf("%s/open/user/info", OPEN_BASE_URL)
	req := c.client.R().SetMethod("GET")
	respData := &UserInfo{}
	_, _, err := c.doAuthRequest(context.Background(), url, req, MakeRequestConfig(1, 1, 15), respData)
	if err != nil {
		helpers.V115Log.Errorf("调用用户信息接口失败：%v", err)
		return nil, err
	}
	return respData, nil
}
