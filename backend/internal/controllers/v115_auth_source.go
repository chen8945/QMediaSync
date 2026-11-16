package controllers

import (
	"net/http"
	"strconv"

	"qmediasync/internal/httpapi"
	v115auth "qmediasync/internal/v115/auth"

	"github.com/gin-gonic/gin"
)

// GetV115AppIDSources 查询 115 开放平台 APP ID 目录。
func GetV115AppIDSources(c *gin.Context) {
	offset, _ := strconv.Atoi(c.DefaultQuery("offset", "0"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	result := v115auth.SearchBuiltInAppIDSources(c.Query("keyword"), offset, limit)
	httpapi.Success(c, http.StatusOK, result, "查询 115 开放平台 APP ID 目录成功")
}
