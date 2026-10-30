package emby

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/constant"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/util/strs"
	"qmediasync/internal/emby302/util/urls"

	"github.com/gin-gonic/gin"
)

// AuthUri 鉴权地址
//
// 通过此 URI 可以判断客户端传递的 api_key 是否被 Emby 服务器认可
const AuthUri = "/emby/Auth/Keys"

// validApiKeys 记录已经校验通过的 api_key, 下次不再重复校验
//
// 这个 map 不做大小限制。Emby 源服务器中的合法 api_key 数量有限,
// 这里无需额外限制。
var validApiKeys = sync.Map{}

// ApiKeyType 标记 Emby 支持的不同 api_key 传递方式
type ApiKeyType string

const (
	Query  ApiKeyType = "query"  // query 参数中的 api_key
	Header ApiKeyType = "header" // 请求头中的 Authorization
)

const (
	QueryApiKeyName    = "api_key"
	QueryTokenName     = "X-Emby-Token"
	HeaderAuthName     = "Authorization"
	HeaderFullAuthName = "X-Emby-Authorization"
)

const UnauthorizedResp = "Access token is invalid or expired."

// AuthorizationTokenExtractReg 匹配 Authorization 头中 Token 字段
var AuthorizationTokenExtractReg = regexp.MustCompile(`(?i)token="([^"]+)"`)

// ApiKeyChecker 对指定 API 进行鉴权
//
// 该中间件会将客户端传递的 api_key 发送给 Emby 服务器。如果 Emby 返回 401,
// 说明该 api_key 未通过校验, 阻断客户端请求。
func ApiKeyChecker() gin.HandlerFunc {

	patterns := []*regexp.Regexp{
		regexp.MustCompile(constant.Reg_ResourceStream),
		regexp.MustCompile(constant.Reg_PlaybackInfo),
		regexp.MustCompile(constant.Reg_ItemDownload),
		regexp.MustCompile(constant.Reg_ItemSyncDownload),
		regexp.MustCompile(constant.Reg_ProxyPlaylist),
		regexp.MustCompile(constant.Reg_ProxyTs),
		regexp.MustCompile(constant.Reg_ProxySubtitle),
		regexp.MustCompile(constant.Reg_ShowEpisodes),
		regexp.MustCompile(constant.Reg_UserItems),
	}

	return func(c *gin.Context) {
		// 1 取出 api_key
		kType, kName, apiKey := getApiKey(c)

		// 2 如果该 key 已经被信任, 跳过校验
		if _, ok := validApiKeys.Load(apiKey); ok {
			return
		}

		// 3 判断当前请求的 URI 是否需要校验
		needCheck := false
		for _, pattern := range patterns {
			if pattern.MatchString(c.Request.RequestURI) {
				needCheck = true
				break
			}
		}
		if !needCheck {
			return
		}

		// 4 发出请求验证 api_key
		u := config.C.Emby.Host + AuthUri
		var header http.Header
		if kType == Query {
			u = urls.AppendArgs(u, kName, apiKey)
		} else {
			header = make(http.Header)
			header.Set(kName, apiKey)
		}
		resp, err := https.Get(u).Header(header).Do()
		if err != nil {
			logs.Error("鉴权失败: %v", err)
			c.Abort()
			return
		}
		defer resp.Body.Close()
		bodyBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			logs.Error("鉴权中间件读取源服务器响应失败: %v", err)
			bodyBytes = []byte(UnauthorizedResp)
		}
		respBody := strings.TrimSpace(string(bodyBytes))

		// 5 判断是否被源服务器拒绝
		if resp.StatusCode == http.StatusUnauthorized && respBody == UnauthorizedResp {
			c.String(http.StatusUnauthorized, "鉴权失败")
			c.Abort()
			return
		}

		// 6 校验通过, 加入信任集合
		validApiKeys.Store(apiKey, struct{}{})
	}
}

// getApiKey 获取请求中的 api_key 信息
func getApiKey(c *gin.Context) (keyType ApiKeyType, keyName string, apiKey string) {
	if c == nil {
		return Query, "", ""
	}

	keyName = QueryApiKeyName
	keyType = Query
	apiKey = c.Query(keyName)
	if strs.AllNotEmpty(apiKey) {
		return
	}

	keyName = QueryTokenName
	apiKey = c.Query(keyName)
	if strs.AllNotEmpty(apiKey) {
		return
	}

	keyType = Header
	apiKey = c.GetHeader(keyName)
	if strs.AllNotEmpty(apiKey) {
		if AuthorizationTokenExtractReg.MatchString(apiKey) {
			keyType = Query
			keyName = QueryTokenName
			apiKey = AuthorizationTokenExtractReg.FindStringSubmatch(apiKey)[1]
		}
		return
	}

	keyName = HeaderAuthName
	apiKey = c.GetHeader(keyName)
	if strs.AllNotEmpty(apiKey) {
		if AuthorizationTokenExtractReg.MatchString(apiKey) {
			keyType = Query
			keyName = QueryTokenName
			apiKey = AuthorizationTokenExtractReg.FindStringSubmatch(apiKey)[1]
		}
		return
	}

	keyName = HeaderFullAuthName
	apiKey = c.GetHeader(keyName)
	if strs.AllNotEmpty(apiKey) {
		if AuthorizationTokenExtractReg.MatchString(apiKey) {
			keyType = Query
			keyName = QueryTokenName
			apiKey = AuthorizationTokenExtractReg.FindStringSubmatch(apiKey)[1]
		}
		return
	}

	return
}
