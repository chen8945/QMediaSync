package controllers

import (
	"net/http"
	"time"

	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func ListUserSessions(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "用户未登录", Data: nil, ErrorCode: ErrorCodeAuthenticationRequired})
		return
	}
	currentSessionID := ""
	if session, ok := CurrentSession(c); ok {
		currentSessionID = session.SessionID
	}
	sessions, err := models.ListActiveUserSessions(user.ID, currentSessionID, time.Now().Unix())
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取登录设备失败", Data: nil})
		return
	}
	items := make([]gin.H, 0, len(sessions))
	for i := range sessions {
		items = append(items, buildSessionResponse(&sessions[i], currentSessionID))
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取登录设备成功", Data: items})
}

func RevokeUserSessionAction(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "用户未登录", Data: nil, ErrorCode: ErrorCodeAuthenticationRequired})
		return
	}
	sessionID := c.Param("session_id")
	if current, ok := CurrentSession(c); ok && current.SessionID == sessionID {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "当前设备请使用退出登录", Data: nil})
		return
	}
	if err := models.RevokeUserSession(user.ID, sessionID, "device_revoked"); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "撤销登录设备失败", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "登录设备已撤销", Data: nil})
}

func RevokeOtherUserSessionsAction(c *gin.Context) {
	user, ok := CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "用户未登录", Data: nil, ErrorCode: ErrorCodeAuthenticationRequired})
		return
	}
	currentSessionID := ""
	if current, ok := CurrentSession(c); ok {
		currentSessionID = current.SessionID
	}
	if err := models.RevokeOtherUserSessions(user.ID, currentSessionID, "device_revoked_all"); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "撤销其他登录设备失败", Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "其他登录设备已撤销", Data: nil})
}
