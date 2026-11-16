package controllers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
)

// GetScrapePathes 获取刮削路径列表
func GetScrapePathes(c *gin.Context) {
	sourceType := c.Query("source_type")
	scrapePathes, err := models.ReadScrapePaths(c.Request.Context(), sourceType)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "获取刮削目录列表失败", Cause: err})
		return
	}
	for _, scrapePath := range scrapePathes {
		// 检查是否正在运行
		scrapePath.IsTaskRunning = synccron.CheckNewTaskStatus(scrapePath.ID, synccron.SyncTaskTypeScrape)
	}
	httpapi.Success(c, http.StatusOK, scrapePathes, "")
}

// GetScrapePath 获取刮削路径详情
func GetScrapePath(c *gin.Context) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	scrapePath, err := models.ReadScrapePath(c.Request.Context(), idReq.ID)
	if err != nil {
		writeScrapePathError(c, err, "获取刮削目录失败")
		return
	}
	if scrapePath.EnableAi == "" {
		scrapePath.EnableAi = models.AiActionOff
	}
	httpapi.Success(c, http.StatusOK, scrapePath, "")
}

// SaveScrapePath 保存刮削路径
func SaveScrapePath(c *gin.Context) {
	var req requests.SaveScrapePathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	var oldScrapePath *models.ScrapePath
	if req.ID > 0 {
		var err error
		oldScrapePath, err = models.ReadScrapePath(c.Request.Context(), req.ID)
		if err != nil {
			writeScrapePathError(c, err, "获取刮削目录失败")
			return
		}
		if err := req.ValidateUpdate(oldScrapePath); err != nil {
			httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
			return
		}
	} else if err := req.ValidateCreate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	reqData := req.ToModel()
	// 如果是 115，用 ID 查询实际目录
	if reqData.SourceType == models.SourceType115 {
		// 用 ID 查询实际目录
		account, err := models.ReadAccount(c.Request.Context(), reqData.AccountId)
		if err != nil {
			writePathOperationError(c, err, "获取账号信息失败")
			return
		}
		// 用 ID 查询实际目录
		sourcePath, err := models.GetPathByPathFileId(account, reqData.SourcePathId)
		if err != nil {
			writeCloudUpstreamError(c, err, "查询来源目录失败", account.Token, account.RefreshToken, account.Password)
			return
		}
		if sourcePath == "" {
			httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "RESOURCE_NOT_FOUND", Message: "来源目录不存在"})
			return
		}
		reqData.SourcePath = sourcePath
		if reqData.DestPathId != "" {
			destPath, err := models.GetPathByPathFileId(account, reqData.DestPathId)
			if err != nil {
				writeCloudUpstreamError(c, err, "查询目标目录失败", account.Token, account.RefreshToken, account.Password)
				return
			}
			if destPath == "" {
				httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "RESOURCE_NOT_FOUND", Message: "目标目录不存在"})
				return
			}
			reqData.DestPath = destPath
		}
	}
	helpers.AppLogger.Infof("最大线程数：%d", reqData.MaxThreads)

	if err := reqData.Save(); err != nil {
		writeScrapePathError(c, err, "保存刮削目录失败")
		return
	}

	completeScrapePathSave(c, "刮削目录已保存")
}

// DeleteScrapePath 删除刮削路径
func DeleteScrapePath(c *gin.Context) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	if _, err := models.ReadScrapePath(c.Request.Context(), idReq.ID); err != nil {
		writeScrapePathError(c, err, "获取刮削目录失败")
		return
	}
	if err := models.DeleteScrapePath(idReq.ID); err != nil {
		writeScrapePathError(c, err, "删除刮削目录失败")
		return
	}

	completeScrapePathSave(c, "刮削目录已删除")
}

// ScanScrapePath 启动刮削路径扫描
func ScanScrapePath(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 查询 ScrapePath
	scrapePath, err := models.ReadScrapePath(c.Request.Context(), req.ID)
	if err != nil {
		writeScrapePathError(c, err, "获取刮削目录失败")
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
		writeTaskEnqueueError(c, err, "添加刮削任务失败")
		return
	}

	httpapi.Success(c, http.StatusAccepted, gin.H{"task_id": taskObj.ID, "status": "submitted"}, "刮削任务已添加到队列")
}

// ToggleScrapePathCron 切换刮削路径的定时任务
func ToggleScrapePathCron(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 使用 ID 查询 ScrapePath
	scrapePath, err := models.ReadScrapePath(c.Request.Context(), req.ID)
	if err != nil {
		writeScrapePathError(c, err, "获取刮削目录失败")
		return
	}
	// 切换定时刮削
	err = scrapePath.ToggleCron()
	if err != nil {
		writeScrapePathError(c, err, "切换定时刮削失败")
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
	httpapi.Success(c, http.StatusOK, nil, message, warnings...)
}

func writeScrapePathError(c *gin.Context, err error, message string) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "SCRAPE_PATH_NOT_FOUND", Message: "刮削目录不存在", Cause: err})
		return
	}
	httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: message, Cause: err})
}
