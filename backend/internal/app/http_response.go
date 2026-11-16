package app

import (
	"errors"
	"net/http"
	"strings"

	"qmediasync/internal/httpapi"

	"github.com/gin-gonic/gin"
)

// nativeAPIPath 标识成功时遵循文件或流协议的路由。
func nativeAPIPath(path string) bool {
	switch path {
	case "/api/scrape/tmp-image", "/api/scrape/records/export", "/api/logs/stream", "/api/events/stream", "/api/logs/download":
		return true
	}
	return strings.HasPrefix(path, "/api/backup/download/") ||
		(strings.HasPrefix(path, "/api/sync/tasks/") && strings.HasSuffix(path, "/stream"))
}

func ownJSONRequest(c *gin.Context) bool {
	return c.Request.Method != http.MethodHead && strings.HasPrefix(c.Request.URL.Path, "/api/")
}

func configureHTTPResponses(r *gin.Engine) {
	r.Use(func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if !ownJSONRequest(c) || nativeAPIPath(c.Request.URL.Path) {
					panic(recovered)
				}
				c.Abort()
				cause, ok := recovered.(error)
				if !ok {
					cause = errors.New("HTTP handler panic")
				}
				httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "请求处理失败", Cause: cause})
			}
		}()
		c.Next()
	})
	r.NoRoute(func(c *gin.Context) {
		if ownJSONRequest(c) {
			httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "RESOURCE_NOT_FOUND", Message: "请求接口不存在"})
		}
	})
	r.HandleMethodNotAllowed = true
	r.NoMethod(func(c *gin.Context) {
		if ownJSONRequest(c) && !nativeAPIPath(c.Request.URL.Path) {
			httpapi.Failure(c, http.StatusMethodNotAllowed, httpapi.Error{Code: "METHOD_NOT_ALLOWED", Message: "请求方法不支持"})
			return
		}
		// 普通 API 的方法拒绝不改变原生路由原有的 404 行为。
		c.Writer.Header().Del("Allow")
		c.String(http.StatusNotFound, "404 page not found")
	})
}
