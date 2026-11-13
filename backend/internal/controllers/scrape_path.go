package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
)

// GetScrapePathes 获取刮削路径列表
// @Summary 获取刮削路径列表
// @Description 获取所有配置的刮削路径列表
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/pathes [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetScrapePathes(c *gin.Context) {
	sourceType := c.Query("source_type")
	scrapePathes := models.GetScrapePathes(sourceType)
	for _, scrapePath := range scrapePathes {
		// 检查是否正在运行
		scrapePath.IsTaskRunning = synccron.CheckNewTaskStatus(scrapePath.ID, synccron.SyncTaskTypeScrape)
	}
	c.JSON(http.StatusOK, APIResponse[[]*models.ScrapePath]{Code: Success, Message: "", Data: scrapePathes})
}

// GetScrapePath 获取刮削路径详情
// @Summary 获取刮削路径详情
// @Description 根据 ID 获取指定刮削路径的详细配置
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param id path integer true "刮削路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/pathes/:id [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetScrapePath(c *gin.Context) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "ID 参数格式错误", Data: nil})
		return
	}
	scrapePath := models.GetScrapePathByID(idReq.ID)
	if scrapePath == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "刮削目录不存在", Data: nil})
		return
	}
	if scrapePath.EnableAi == "" {
		scrapePath.EnableAi = models.AiActionOff
	}
	c.JSON(http.StatusOK, APIResponse[*models.ScrapePath]{Code: Success, Message: "", Data: scrapePath})
}

// SaveScrapePath 保存刮削路径
// @Summary 保存刮削路径
// @Description 创建或更新刮削路径配置
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param id body integer false "路径 ID，不填为新增"
// @Param source_type body integer true "来源类型"
// @Param source_path body string true "来源路径"
// @Param dest_path body string true "目标路径"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/pathes [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func SaveScrapePath(c *gin.Context) {
	var req requests.SaveScrapePathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	var oldScrapePath *models.ScrapePath
	if req.ID > 0 {
		oldScrapePath = models.GetScrapePathByID(req.ID)
		if err := req.ValidateUpdate(oldScrapePath); err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
	} else if err := req.ValidateCreate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	reqData := req.ToModel()
	// 如果是 115，用 ID 查询实际目录
	if reqData.SourceType == models.SourceType115 {
		// 用 ID 查询实际目录
		account, err := models.GetAccountById(reqData.AccountId)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
			return
		}
		// 用 ID 查询实际目录
		sourcePath := models.GetPathByPathFileId(account, reqData.SourcePathId)
		if sourcePath == "" {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询来源目录失败", Data: nil})
			return
		}
		reqData.SourcePath = sourcePath
		if reqData.DestPathId != "" {
			destPath := models.GetPathByPathFileId(account, reqData.DestPathId)
			if destPath == "" {
				c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "查询目标目录失败", Data: nil})
				return
			}
			reqData.DestPath = destPath
		}
	}
	helpers.AppLogger.Infof("最大线程数：%d", reqData.MaxThreads)

	if err := reqData.Save(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	completeScrapePathSave(c, "刮削目录已保存")
}

// DeleteScrapePath 删除刮削路径
// @Summary 删除刮削路径
// @Description 删除指定的刮削路径
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param id path integer true "路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/pathes/:id [delete]
// @Security JwtAuth
// @Security ApiKeyAuth
func DeleteScrapePath(c *gin.Context) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "ID 参数格式错误", Data: nil})
		return
	}

	if err := models.DeleteScrapePath(idReq.ID); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	completeScrapePathSave(c, "刮削目录已删除")
}

// ScanScrapePath 启动刮削路径扫描
// @Summary 启动刮削任务
// @Description 启动指定刮削路径的扫描任务
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param id body integer true "路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/pathes/start [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ScanScrapePath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 查询 ScrapePath
	scrapePath := models.GetScrapePathByID(req.ID)
	if scrapePath == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "刮削目录不存在", Data: nil})
		return
	}
	// 添加刮削任务到队列
	taskObj := &synccron.NewSyncTask{
		ID:           scrapePath.ID,
		SourcePath:   "",
		SourcePathId: "",
		TargetPath:   "",
		AccountId:    scrapePath.AccountId,
		SourceType:   scrapePath.SourceType,
		IsFile:       false,
		TaskType:     synccron.SyncTaskTypeScrape,
	}
	if err := synccron.AddNewSyncTask(taskObj); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "添加刮削任务失败：" + err.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "刮削任务已添加到队列", Data: nil})
}

// ToggleScrapePathCron 切换刮削路径的定时任务
// @Summary 切换定时刮削
// @Description 开启或关闭刮削路径的定时刮削任务
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param id body integer true "路径 ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/pathes/toggle-cron [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ToggleScrapePathCron(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 使用 ID 查询 ScrapePath
	scrapePath := models.GetScrapePathByID(req.ID)
	if scrapePath == nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "没有找到要操作的刮削目录", Data: nil})
		return
	}
	// 切换定时刮削
	err := scrapePath.ToggleCron()
	if err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "切换定时刮削失败：" + err.Error(), Data: nil})
		return
	}

	completeScrapePathSave(c, "定时刮削状态已切换")
}

func completeScrapePathSave(c *gin.Context, message string) {
	warnings := make([]string, 0)
	if err := synccron.InitScrapeCronWithError(); err != nil {
		helpers.AppLogger.Warnf("刮削目录已保存，但定时任务应用失败：%v", err)
		warnings = append(warnings, "设置已保存，但定时任务应用失败，请重新保存重试")
	}
	c.JSON(http.StatusOK, gin.H{"code": Success, "message": message, "data": nil, "warnings": warnings})
}
