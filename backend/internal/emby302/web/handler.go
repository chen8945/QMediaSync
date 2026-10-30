package web

import (
	"net/http"
	"regexp"

	"qmediasync/internal/emby302/constant"
	"qmediasync/internal/emby302/util/logs"

	"github.com/gin-gonic/gin"
)

// MatchRouteKey 存储在 Gin 上下文中的路由匹配字段
const MatchRouteKey = "matchRoute"

// globalDftHandler 全局默认兜底的请求处理器
func globalDftHandler(c *gin.Context) {
	if c.Request.Method == http.MethodHead {
		c.String(http.StatusOK, "")
		return
	}

	// 依次匹配路由规则, 找到其他的处理器
	for _, rule := range rules {
		reg := rule[0].(*regexp.Regexp)
		if reg.MatchString(c.Request.RequestURI) {
			c.Set(MatchRouteKey, reg.String())
			c.Set(constant.RouteSubMatchGinKey, reg.FindStringSubmatch(c.Request.RequestURI))
			rule[1].(gin.HandlerFunc)(c)
			return
		}
	}
}

// compileRules 编译路由的正则表达式
func compileRules(rs [][2]any) [][2]any {
	newRs := make([][2]any, 0, len(rs))
	for _, rule := range rs {
		reg, err := regexp.Compile(rule[0].(string))
		if err != nil {
			logs.Error("路由正则编译失败, pattern: %v, 错误: %v", rule[0], err)
			continue
		}
		rule[0] = reg

		rawHandler, ok := rule[1].(func(*gin.Context))
		if !ok {
			logs.Error("请求处理器无效, pattern: %v", rule[0])
			continue
		}
		var handler gin.HandlerFunc = rawHandler
		rule[1] = handler
		newRs = append(newRs, rule)
	}
	return newRs
}
