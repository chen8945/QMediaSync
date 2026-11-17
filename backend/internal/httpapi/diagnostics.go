package httpapi

import (
	"log"
	"net/http"

	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

const instanceContextKey = "qms.httpapi.failure_instance"

// SafeCause 返回用于 HTTP 层诊断的安全摘要，保留来源并清理已知密钥。
func SafeCause(cause error, secrets ...string) string {
	if cause == nil {
		return ""
	}
	return helpers.SafeLogError(cause, secrets...)
}

func requestInstance(c *gin.Context) string {
	if instance := c.GetString(instanceContextKey); instance != "" {
		return instance
	}
	instance := "/request-errors/" + helpers.RandStr(24)
	c.Set(instanceContextKey, instance)
	return instance
}

func logFailure(c *gin.Context, response APIResponse[any], cause error, secrets []string, writeFailed bool) {
	path := c.FullPath()
	if path == "" {
		path = "<unmatched>"
	}
	detail := SafeCause(cause, secrets...)
	level := helpers.LogLevelInfo
	internalFailure := response.Status >= http.StatusInternalServerError
	if response.Status == http.StatusServiceUnavailable && (response.ErrorCode == "DATABASE_MAINTENANCE" || cause == nil) {
		internalFailure = false
	}
	if internalFailure || writeFailed {
		level = helpers.LogLevelError
	}
	const format = "API 请求失败 instance=%s method=%s path=%q status=%d error_code=%s cause=%q"
	args := []any{response.Instance, c.Request.Method, path, response.Status, response.ErrorCode, detail}
	if helpers.AppLogger == nil {
		log.Printf("[%s] "+format, append([]any{level.Label()}, args...)...)
	} else if level == helpers.LogLevelError {
		helpers.AppLogger.Errorf(format, args...)
	} else {
		helpers.AppLogger.Infof(format, args...)
	}
}
