package controllers

import (
	"net/http"
	"time"

	"qmediasync/internal/httpapi"

	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func ListUserSessions(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	user, ok := CurrentUser(c)
	if !ok {
		setAuthenticationChallenge(c)
		httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: ErrorCodeAuthenticationRequired, Message: "用户未登录", Data: nil})
		return
	}
	currentSessionID := ""
	if session, ok := CurrentSession(c); ok {
		currentSessionID = session.SessionID
	}
	sessions, err := models.ListActiveUserSessions(user.ID, currentSessionID, time.Now().Unix())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取登录设备失败", Cause: err})
		return
	}
	items := make([]gin.H, 0, len(sessions))
	for i := range sessions {
		items = append(items, buildSessionResponse(&sessions[i], currentSessionID))
	}
	httpapi.Success(c, http.StatusOK, items, "获取登录设备成功")
}

func RevokeUserSessionAction(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	user, ok := CurrentUser(c)
	if !ok {
		setAuthenticationChallenge(c)
		httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: ErrorCodeAuthenticationRequired, Message: "用户未登录", Data: nil})
		return
	}
	sessionID := c.Param("session_id")
	if current, ok := CurrentSession(c); ok && current.SessionID == sessionID {
		httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "CURRENT_SESSION_REVOKE_CONFLICT", Message: "当前设备请使用退出登录"})
		return
	}
	if err := models.RevokeUserSession(user.ID, sessionID, "device_revoked"); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "撤销登录设备失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "登录设备已撤销")
}

func RevokeOtherUserSessionsAction(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	user, ok := CurrentUser(c)
	if !ok {
		setAuthenticationChallenge(c)
		httpapi.Failure(c, http.StatusUnauthorized, httpapi.Error{Code: ErrorCodeAuthenticationRequired, Message: "用户未登录", Data: nil})
		return
	}
	currentSessionID := ""
	if current, ok := CurrentSession(c); ok {
		currentSessionID = current.SessionID
	}
	if err := models.RevokeOtherUserSessions(user.ID, currentSessionID, "device_revoked_all"); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "撤销其他登录设备失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "其他登录设备已撤销")
}
