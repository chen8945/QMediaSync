package controllers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/v115open"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
)

type APIResponseCode int

const (
	Success    APIResponseCode = 200
	BadRequest APIResponseCode = 500
)

type APIResponse[T any] struct {
	Code    APIResponseCode `json:"code"`
	Message string          `json:"message"`
	Data    T               `json:"data"`
}

var runAuthBackgroundTask = func(fn func()) {
	go fn()
}

// JWTAuthMiddleware 基于 JWT 的认证中间件，用于验证用户是否登录。
func JWTAuthMiddleware() func(c *gin.Context) {
	return func(c *gin.Context) {
		apiKey := apiKeyFromRequest(c)
		if apiKey != "" {
			if !authenticateAPIKey(c, apiKey) {
				c.Abort()
				return
			}
			c.Next()
			return
		}

		if !authenticateCookieSession(c) {
			c.Abort()
			return
		}
		if !validateCSRF(c) {
			c.Abort()
			return
		}
		c.Next()
	}
}

func apiKeyFromRequest(c *gin.Context) string {
	apiKey := c.Request.Header.Get(apiKeyHeaderName)
	if apiKey == "" {
		apiKey = c.Query("api_key")
	}
	return apiKey
}

func authenticateAPIKey(c *gin.Context, apiKey string) bool {
	apiKeyModel, err := models.ValidateAPIKey(apiKey)
	if err != nil || apiKeyModel == nil {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "API Key 无效", Data: nil})
		return false
	}
	user, err := models.GetUserById(apiKeyModel.UserID)
	if err != nil || user == nil || user.ID == 0 {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "API Key 用户不存在", Data: nil})
		return false
	}
	SetCurrentUser(c, user, authMethodAPIKey)
	runAuthBackgroundTask(func() {
		_ = apiKeyModel.UpdateLastUsedAt()
	})
	return true
}

func authenticateCookieSession(c *gin.Context) bool {
	cookie, err := c.Request.Cookie(authCookieName)
	if err != nil || cookie.Value == "" {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "登录凭证不存在", Data: nil})
		return false
	}
	loginUser, err := ValidateJWT(cookie.Value)
	if err != nil {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "登录凭证无效", Data: nil})
		return false
	}
	now := time.Now().Unix()
	session, err := models.GetActiveUserSession(loginUser.SessionID, now)
	if err != nil || session.UserID != loginUser.ID {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "登录会话已失效", Data: nil})
		return false
	}
	user, err := models.GetUserById(loginUser.ID)
	if err != nil || user == nil || user.ID == 0 {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "登录用户不存在", Data: nil})
		return false
	}
	SetCurrentUser(c, user, authMethodSession)
	SetCurrentSession(c, session)
	if now-session.LastSeenAt >= 60 {
		runAuthBackgroundTask(func() {
			_ = models.TouchUserSession(session.SessionID, now)
		})
	}
	return true
}

func Proxy115(c *gin.Context) {
	var proxyReq requests.Proxy115Request
	if err := c.ShouldBindQuery(&proxyReq); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if proxyReq.URL == "" {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "缺少 URL 参数", Data: nil})
		return
	}
	if err := proxyReq.Validate(); err != nil {
		helpers.AppLogger.Warnf("拒绝反代非 115/百度网盘下载链接，url=%s，err=%v", proxyReq.URL, err)
		c.JSON(http.StatusForbidden, APIResponse[any]{Code: BadRequest, Message: "只允许反代 115 或百度网盘下载链接", Data: nil})
		return
	}
	target := proxyReq.URL
	helpers.AppLogger.Infof("反代网盘下载链接：%s", target)
	// 创建请求
	req, err := http.NewRequest("GET", target, nil)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求创建失败", Data: nil})
		return
	}
	// 仅转发 Range、Referer，避免将浏览器 Cookie 泄露给网盘。
	for k, v := range c.Request.Header {
		if k == "Range" || k == "Referer" {
			// helpers.AppLogger.Infof("响应头：%s=%s", k, v)
			req.Header[k] = v
		}
	}
	if proxyReq.BaiduPan != "" {
		req.Header.Set("User-Agent", "pan.baidu.com")
	} else {
		req.Header.Set("User-Agent", v115open.DEFAULTUA)
	}
	// 发起请求，重定向后的 URL 仍需保持在网盘下载域名白名单内。
	client := &http.Client{
		Timeout:       60 * time.Second,
		CheckRedirect: validateProxy115Redirect,
	}
	resp, err := client.Do(req)
	if err != nil {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		c.JSON(http.StatusBadGateway, APIResponse[any]{Code: BadRequest, Message: "反代请求失败：" + err.Error(), Data: nil})
		return
	}
	defer resp.Body.Close()
	// 复制响应头
	for k, v := range resp.Header {
		for _, vv := range v {
			c.Header(k, vv)
		}
	}
	c.Status(resp.StatusCode)
	// 复制响应内容
	_, _ = io.Copy(c.Writer, resp.Body)
}

func validateProxy115Redirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("重定向次数过多")
	}
	if err := validation.DownloadProxyURL("url", req.URL.String()); err != nil {
		return fmt.Errorf("拒绝重定向到非 115/百度网盘下载链接：%w", err)
	}
	return nil
}

func validateProxy115Target(target string) (bool, string) {
	parsedURL, err := url.Parse(target)
	if err != nil || parsedURL.Host == "" {
		return false, ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsedURL.Hostname(), "."))
	if err := validation.DownloadProxyURL("url", target); err != nil {
		return false, host
	}
	return true, host
}

func Cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method               // 请求方法
		origin := c.Request.Header.Get("Origin") // 请求头部
		// var headerKeys []string                  // 声明请求头 keys
		// for k := range c.Request.Header {
		// 	headerKeys = append(headerKeys, k)
		// }
		// headerStr := strings.Join(headerKeys, ", ")
		// if headerStr != "" {
		// 	headerStr = fmt.Sprintf("access-control-allow-origin, access-control-allow-headers, %s", headerStr)
		// } else {
		// 	headerStr = "access-control-allow-origin, access-control-allow-headers"
		// }
		if origin != "" {
			c.Header("Vary", "Origin")
		}
		if origin != "" && originAllowed(c, origin) {
			c.Header("Access-Control-Allow-Origin", origin)                                    // 允许访问当前请求来源
			c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE,UPDATE") // 服务器支持的跨域请求方法，避免浏览器重复预检。
			// Header 类型
			c.Header("Access-Control-Allow-Headers", "Authorization, X-API-Key, Content-Length, X-CSRF-Token, Token, session, X_Requested_With, Accept, Origin, Host, Connection, Accept-Encoding, Accept-Language, DNT, X-CustomHeader, Keep-Alive, User-Agent, X-Requested-With, If-Modified-Since, Cache-Control, Content-Type, Pragma")
			// 允许浏览器读取这些跨域响应头。
			c.Header("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin, Access-Control-Allow-Headers,Cache-Control,Content-Language,Content-Type,Expires,Last-Modified,Pragma,FooBar") // 跨域关键设置，让浏览器可以解析。
			c.Header("Access-Control-Max-Age", "172800")                                                                                                                                                           // 缓存预检请求信息，单位为秒。
			c.Header("Access-Control-Allow-Credentials", "true")                                                                                                                                                   // 允许前端开发环境携带 Cookie。
			c.Set("content-type", "application/json")                                                                                                                                                              // 设置返回格式为 JSON 。
		}

		// 放行所有 OPTIONS 方法
		if method == "OPTIONS" {
			c.JSON(http.StatusOK, "Options Request!")
		}
		// 处理请求
		c.Next() // 处理请求
	}
}

func IsFnOS(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "", Data: helpers.IsFnOS})
}

func RepairDB(c *gin.Context) {
	// 修复数据库，补齐缺失的表、字段和索引
	err := models.BatchCreateTable()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "修复数据库失败：" + err.Error(), Data: nil})
		return
	}
	// PostgreSQL 下修复数据库表的主键序列
	err = models.BatchRepairTableSeq()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "修复数据库表的主键序列失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已补齐数据库表结构，并完成主键序列检查", Data: nil})
}

func GetAnnounce(c *gin.Context) {
	// 从 https://api.mqfamily.top/desc.json 获取公告
	bytes, err := helpers.ReadFromUrl("https://api.mqfamily.top/desc.json", v115open.DEFAULTUA)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取公告失败：" + err.Error(), Data: nil})
		return
	}
	// helpers.AppLogger.Infof("获取到的公告：%s", string(bytes))
	// 解析 JSON
	type announce struct {
		ID      int    `json:"id"`
		Title   string `json:"title"`
		Content string `json:"content"`
		Time    string `json:"time"`
	}
	var announces []announce
	err = json.Unmarshal(bytes, &announces)
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "解析公告失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取公告成功", Data: announces})
}

func DeleteAllTabble(c *gin.Context) {
	// 重置数据库，删除所有表，重新初始化数据库
	err := models.BatchDropTable()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "删除数据库所有表失败：" + err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "已删除数据库所有表", Data: nil})
}
