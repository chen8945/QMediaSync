package controllers

import (
	"net/http"

	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"

	"github.com/gin-gonic/gin"
)

// GetOpenListFileURL 获取 OpenList 文件直链
func GetOpenListFileUrl(c *gin.Context) {
	var req requests.OpenListFileURLRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "参数错误", Cause: err})
		return
	}
	if err := req.Validate(); err != nil {
		writeAccountValidationError(c, err)
		return
	}
	account, err := models.ReadAccount(c.Request.Context(), req.AccountID)
	if err != nil {
		writeAccountLookupError(c, err)
		return
	}
	client := account.GetOpenListClient()
	fileDetail, err := client.FileDetailContext(c.Request.Context(), req.Path)
	if err != nil {
		writeCloudUpstreamError(c, err, "获取文件详情失败", account.Token, account.Password)
		return
	}
	if fileDetail.RawURL == "" {
		httpapi.Failure(c, http.StatusBadGateway, httpapi.Error{Code: "UPSTREAM_RESPONSE_INVALID", Message: "文件详情中未找到直链"})
		return
	}
	// 302 重定向到直链
	c.Redirect(http.StatusFound, fileDetail.RawURL)
}
