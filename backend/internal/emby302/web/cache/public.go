package cache

import (
	"bytes"
	"net/http"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/util/jsons"
)

// GetRequestCache 复用 HTTP 请求身份读取未经过展示改写的内部响应。
func GetRequestCache(request *http.Request) (RespCache, bool) {
	if request == nil || request.Context().Err() != nil {
		return nil, false
	}
	key, err := calcCacheKey(&gin.Context{Request: request})
	if err != nil {
		return nil, false
	}
	if response, ok := getCache(key); ok {
		return response, true
	}
	return nil, false
}

// PutRequestCache 按现有截止时间和快照规则缓存内部处理结果。
func PutRequestCache(request *http.Request, code int, body []byte, header http.Header) {
	if request == nil || request.Context().Err() != nil || https.IsErrorStatus(code) {
		return
	}
	key, err := calcCacheKey(&gin.Context{Request: request})
	if err != nil {
		return
	}
	putCache(key, code, append([]byte(nil), body...), respHeader{
		expired: header.Get(HeaderKeyExpired), space: header.Get(HeaderKeySpace),
		spaceKey: header.Get(HeaderKeySpaceKey), header: header.Clone(),
	})
}

// RespCache 公开对外暴露的缓存接口
type RespCache interface {
	// Code 响应码
	Code() int

	// Body 克隆一个响应体, 转换为缓冲区
	Body() *bytes.Buffer

	// BodyBytes 克隆一个响应体
	BodyBytes() []byte

	// JsonBody 将响应体转换成 JSON 返回
	JsonBody() (*jsons.Item, error)

	// Header 获取响应头属性
	Header(key string) string

	// Headers 获取克隆响应头
	Headers() http.Header

	// Space 获取缓存空间名称
	Space() string

	// SpaceKey 获取缓存空间 key
	SpaceKey() string

	// Update 更新缓存
	//
	// code 传递零值时, 会自动忽略更新
	//
	// body 传递 nil 时, 会自动忽略更新,
	// 传递空切片时, 会认为是一个空响应体进行更新
	//
	// header 传递 nil 时, 会自动忽略更新,
	// 不为 nil 时, 缓存的响应头会被清空, 并设置为新值
	Update(code int, body []byte, header http.Header)
}
