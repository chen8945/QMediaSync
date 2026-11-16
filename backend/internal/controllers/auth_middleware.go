package controllers

import (
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
)

// 请求错误码独立于 HTTP 状态和历史业务 code，供客户端稳定分类。
const (
	ErrorCodeAuthenticationRequired = "AUTHENTICATION_REQUIRED"
	ErrorCodeAuthenticationInvalid  = "AUTHENTICATION_INVALID"
	ErrorCodeSessionInvalid         = "SESSION_INVALID"
	ErrorCodeRequestOriginInvalid   = "REQUEST_ORIGIN_INVALID"
	ErrorCodeCSRFTokenInvalid       = "CSRF_TOKEN_INVALID"
	ErrorCodeForbidden              = "FORBIDDEN"
)

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

// 凭据拒绝与存储故障使用不同的 HTTP 状态和会话语义。
func writeAuthenticationFailure(c *gin.Context, err error, code, message string) {
	c.Header("Cache-Control", "no-store, private")
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "认证服务暂时不可用，请稍后重试", Cause: err})
		return
	}
	setAuthenticationChallenge(c)
	httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: code, Message: message, Cause: err})
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
		writeAuthenticationFailure(c, err, ErrorCodeAuthenticationInvalid, "API Key 无效")
		return false
	}
	user, err := models.GetUserById(apiKeyModel.UserID)
	if err != nil || user == nil || user.ID == 0 {
		writeAuthenticationFailure(c, err, ErrorCodeAuthenticationInvalid, "API Key 无效")
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
		writeAuthenticationFailure(c, nil, ErrorCodeAuthenticationRequired, "登录凭证不存在")
		return false
	}
	loginUser, err := ValidateJWT(cookie.Value)
	if err != nil {
		writeAuthenticationFailure(c, nil, ErrorCodeAuthenticationInvalid, "登录凭证无效")
		return false
	}
	now := time.Now().Unix()
	session, err := models.GetActiveUserSession(loginUser.SessionID, now)
	if err != nil || session == nil || session.UserID != loginUser.ID {
		writeAuthenticationFailure(c, err, ErrorCodeSessionInvalid, "登录会话已失效")
		return false
	}
	user, err := models.GetUserById(loginUser.ID)
	if err != nil || user == nil || user.ID == 0 {
		writeAuthenticationFailure(c, err, ErrorCodeAuthenticationInvalid, "登录用户不存在")
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

func Cors() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			c.Header("Vary", "Origin")
		}
		if origin != "" && originAllowed(c, origin) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE,UPDATE")
			c.Header("Access-Control-Allow-Headers", "Authorization, X-API-Key, Content-Length, X-CSRF-Token, X-Restore-Receipt, Token, session, X_Requested_With, Accept, Origin, Host, Connection, Accept-Encoding, Accept-Language, DNT, X-CustomHeader, Keep-Alive, User-Agent, X-Requested-With, If-Modified-Since, Cache-Control, Content-Type, Pragma")
			c.Header("Access-Control-Expose-Headers", "Content-Length, Access-Control-Allow-Origin, Access-Control-Allow-Headers,Cache-Control,Content-Language,Content-Type,Expires,Last-Modified,Pragma,FooBar,Retry-After,WWW-Authenticate")
			c.Header("Access-Control-Max-Age", "172800")
			c.Header("Access-Control-Allow-Credentials", "true")
		}
		if c.Request.Method == http.MethodOptions {
			c.Writer.Header().Del("Allow")
			c.AbortWithStatus(http.StatusOK)
			return
		}
		c.Next()
	}
}
