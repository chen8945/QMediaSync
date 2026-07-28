package controllers

import (
	"net/http"
	"strings"

	"qmediasync/internal/backup"

	"github.com/gin-gonic/gin"
)

// BackupStatusPath 是维护期间唯一放行的备份状态查询路径。
// 它位于认证之前，只凭 operation ID 与请求头令牌读取外部状态文件，不触碰业务数据库。
const BackupStatusPath = "/api/backup/status"

// MaintenanceMiddleware 在备份或恢复启用维护屏障后，对全部业务 API、登录与 Webhook 返回 HTTP 503。
// 它必须注册在任何会读写业务数据库的认证链之前，且不因 HTTP 方法、Cookie 或 API Key 被绕过；
// 只有静态资源和独立状态查询是例外。
func MaintenanceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !backup.InMaintenance() || isMaintenanceExempt(c.Request) {
			c.Next()
			return
		}
		c.Header("Cache-Control", "no-store")
		c.AbortWithStatusJSON(http.StatusServiceUnavailable, APIResponse[any]{
			Code:    BadRequest,
			Message: "系统正在备份或恢复，暂时无法处理请求",
			Data:    nil,
		})
	}
}

// isMaintenanceExempt 判断请求是否属于维护期间仍需放行的静态资源或状态查询。
func isMaintenanceExempt(request *http.Request) bool {
	switch request.URL.Path {
	case BackupStatusPath:
		return request.Method == http.MethodGet
	case "/", "/favicon.ico":
		return true
	}
	return strings.HasPrefix(request.URL.Path, "/assets/")
}
