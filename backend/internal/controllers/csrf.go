package controllers

import (
	"net/http"

	"qmediasync/internal/httpapi"

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
		c.Header("Cache-Control", "no-store, private")
		httpapi.Failure(c, http.StatusForbidden, httpapi.Error{Code: ErrorCodeRequestOriginInvalid, Message: "请求来源无效", Data: nil})
		return false
	}
	headerToken := c.Request.Header.Get(csrfHeaderName)
	cookie, err := c.Request.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" || headerToken == "" || headerToken != cookie.Value {
		c.Header("Cache-Control", "no-store, private")
		httpapi.Failure(c, http.StatusForbidden, httpapi.Error{Code: ErrorCodeCSRFTokenInvalid, Message: "CSRF 校验失败", Data: nil})
		return false
	}
	if !session.ValidateCSRFToken(headerToken) {
		c.Header("Cache-Control", "no-store, private")
		httpapi.Failure(c, http.StatusForbidden, httpapi.Error{Code: ErrorCodeCSRFTokenInvalid, Message: "CSRF 校验失败", Data: nil})
		return false
	}
	return true
}
