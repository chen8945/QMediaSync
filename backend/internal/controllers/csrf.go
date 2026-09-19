package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func validateCSRF(c *gin.Context) bool {
	if !isUnsafeMethod(c.Request.Method) {
		return true
	}
	if CurrentAuthMethod(c) == authMethodAPIKey {
		return true
	}
	session, ok := CurrentSession(c)
	if !ok {
		return false
	}
	if !requestOriginAllowed(c) {
		c.JSON(http.StatusForbidden, APIResponse[any]{Code: BadRequest, Message: "请求来源无效", Data: nil, ErrorCode: ErrorCodeRequestOriginInvalid})
		return false
	}
	headerToken := c.Request.Header.Get(csrfHeaderName)
	cookie, err := c.Request.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" || headerToken == "" || headerToken != cookie.Value {
		c.JSON(http.StatusForbidden, APIResponse[any]{Code: BadRequest, Message: "CSRF 校验失败", Data: nil, ErrorCode: ErrorCodeCSRFTokenInvalid})
		return false
	}
	if !session.ValidateCSRFToken(headerToken) {
		c.JSON(http.StatusForbidden, APIResponse[any]{Code: BadRequest, Message: "CSRF 校验失败", Data: nil, ErrorCode: ErrorCodeCSRFTokenInvalid})
		return false
	}
	return true
}
