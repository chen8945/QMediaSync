package controllers

import (
	"net/http"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"

	"github.com/gin-gonic/gin"
)

// GetOpenListFileURL 获取 OpenList 文件直链
// @Summary 获取 OpenList 文件直链
// @Description 根据路径查询 OpenList 文件直链并 302 重定向
// @Tags OpenList
// @Accept json
// @Produce json
// @Param account_id query integer true "账号 ID"
// @Param path query string true "文件路径"
// @Success 302 {string} string "重定向到文件直链"
// @Failure 200 {object} object
// @Router /openlist/url [get]
func GetOpenListFileUrl(c *gin.Context) {
	var req requests.OpenListFileURLRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "参数错误", Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	account, err := models.GetAccountById(req.AccountID)
	if err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "账号 ID 不存在", Data: nil})
		return
	}
	client := account.GetOpenListClient()
	fileDetail, err := client.FileDetailContext(c.Request.Context(), req.Path)
	if err != nil {
		helpers.AppLogger.Errorf("[下载] 获取文件详情失败：%v", err)
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "获取文件详情失败", Data: nil})
		return
	}
	if fileDetail.RawURL == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "文件详情中未找到直链", Data: nil})
		return
	}
	// 302 重定向到直链
	c.Redirect(http.StatusFound, fileDetail.RawURL)
}
