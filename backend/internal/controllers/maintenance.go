package controllers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/backup"
	"qmediasync/internal/helpers"
)

const errorCodeRestoreReceiptInvalid = "RESTORE_RECEIPT_INVALID"

// RestoreMaintenanceMiddleware 在认证查询前关闭业务入口，恢复凭据允许读取进度及结束后重启。
func RestoreMaintenanceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if c.Request.Method == http.MethodGet && path == "/api/backup/status" {
			if receipt := c.GetHeader("X-Restore-Receipt"); receipt != "" {
				c.Header("Cache-Control", "no-store, private")
				result, ok := backup.RestoreResultWithReceipt(receipt)
				if !ok {
					c.AbortWithStatusJSON(http.StatusUnauthorized, APIResponse[any]{Code: BadRequest, Message: "恢复凭据无效", Data: nil, ErrorCode: errorCodeRestoreReceiptInvalid})
					return
				}
				c.AbortWithStatusJSON(http.StatusOK, APIResponse[backup.BackupOrRestoreResult]{Code: Success, Message: "获取备份状态成功", Data: *result})
				return
			}
		}
		if path == "/api/backup/restart" {
			if c.Request.Method != http.MethodPost {
				c.Header("Allow", http.MethodPost)
				c.AbortWithStatus(http.StatusMethodNotAllowed)
				return
			}
			restartAfterRestore(c, backup.RestartWithReceipt)
			return
		}
		// 静态资源允许维护页面重载，不访问数据库。
		if c.Request.Method == http.MethodGet && (path == "/" || path == "/favicon.ico" || strings.HasPrefix(path, "/assets/")) {
			c.Next()
			return
		}
		release, ok := backup.AcquireRuntimeRequest()
		if !ok {
			c.Header("Retry-After", "30")
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, APIResponse[any]{Code: BadRequest, Message: "数据库恢复维护中，请等待结果并重启应用", Data: nil, ErrorCode: "DATABASE_MAINTENANCE"})
			return
		}
		defer release()
		c.Next()
	}
}

func restartAfterRestore(c *gin.Context, restart func(string) error) {
	c.Header("Cache-Control", "no-store, private")
	if !requestOriginAllowed(c) {
		c.AbortWithStatusJSON(http.StatusForbidden, APIResponse[any]{Code: BadRequest, Message: "请求来源无效", Data: nil, ErrorCode: ErrorCodeRequestOriginInvalid})
		return
	}
	err := restart(c.GetHeader("X-Restore-Receipt"))
	if err != nil {
		status, message, errorCode := http.StatusServiceUnavailable, "重启准备失败，请重试或手动重启服务", "RESTORE_RESTART_PREPARATION_FAILED"
		switch {
		case errors.Is(err, backup.ErrRestoreReceipt):
			status, message, errorCode = http.StatusUnauthorized, "恢复凭据无效", errorCodeRestoreReceiptInvalid
		case errors.Is(err, backup.ErrRestartNotReady):
			status, message, errorCode = http.StatusConflict, "当前恢复任务尚不能重启", "RESTORE_RESTART_NOT_READY"
		case errors.Is(err, backup.ErrRestartUnsupported):
			message, errorCode = "当前运行环境不支持自动重启，请手动重启服务", "RESTORE_RESTART_UNSUPPORTED"
		default:
			if helpers.AppLogger != nil {
				helpers.AppLogger.Errorf("恢复后重启准备失败：%v", err)
			}
		}
		c.AbortWithStatusJSON(status, APIResponse[any]{Code: BadRequest, Message: message, Data: nil, ErrorCode: errorCode})
		return
	}
	c.AbortWithStatusJSON(http.StatusOK, APIResponse[backup.BackupOrRestoreResult]{Code: Success, Message: "已请求重启服务", Data: *backup.GetRunningResult()})
}
