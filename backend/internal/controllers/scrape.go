package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetMovieGenre 获取电影分类
func GetMovieGenre(c *gin.Context) {
	movieGenre := helpers.MovieGenres
	httpapi.Success(c, http.StatusOK, movieGenre, "")
}

// GetTvshowGenre 获取电视剧分类
func GetTvshowGenre(c *gin.Context) {
	tvshowGenre := helpers.TvshowGenres
	httpapi.Success(c, http.StatusOK, tvshowGenre, "")
}

// GetLanguage 获取语言列表
func GetLanguage(c *gin.Context) {
	language := helpers.Languages
	httpapi.Success(c, http.StatusOK, language, "")
}

// GetCountries 获取国家列表
func GetCountries(c *gin.Context) {
	countries := helpers.Countries
	httpapi.Success(c, http.StatusOK, countries, "")
}

// GetMovieCategories 获取电影分类列表
func GetMovieCategories(c *gin.Context) {
	movieGenre, err := models.ReadMovieCategories(c.Request.Context())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取电影分类失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, movieGenre, "")
}

// GetTvshowCategories 获取电视剧分类列表
func GetTvshowCategories(c *gin.Context) {
	tvshowGenre, err := models.ReadTVShowCategories(c.Request.Context())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取电视剧分类失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, tvshowGenre, "")
}

// SaveMovieCategory 保存电影分类
func SaveMovieCategory(c *gin.Context) {
	reqData := requests.MovieCategoryRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := reqData.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	movieCategory := &models.MovieCategory{
		ID:   reqData.ID,
		Name: reqData.Name,
	}
	if err := movieCategory.Save(reqData.Name, reqData.GenreIDArray, reqData.LanguageArray); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存电影分类失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "电影分类已保存")
}

// SaveTvshowCategory 保存电视剧分类
func SaveTvshowCategory(c *gin.Context) {
	reqData := requests.TVShowCategoryRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := reqData.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	tvshowCategory := &models.TvShowCategory{
		ID:   reqData.ID,
		Name: reqData.Name,
	}
	if err := tvshowCategory.Save(reqData.Name, reqData.GenreIDArray, reqData.CountryArray); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存电视剧分类失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "电视剧分类已保存")
}

// DeleteMovieCategory 删除电影分类
func DeleteMovieCategory(c *gin.Context) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	if err := models.DeleteMovieCategory(idReq.ID); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "删除电影分类失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "电影分类已删除")
}

// DeleteTvshowCategory 删除电视剧分类
func DeleteTvshowCategory(c *gin.Context) {
	idReq, err := requests.ParsePositiveIDRequest(c.Param("id"))
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := models.DeleteTvshowCategory(idReq.ID); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "删除电视剧分类失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "电视剧分类已删除")
}

// GetScrapeRecords 获取刮削记录
func GetScrapeRecords(c *gin.Context) {
	page := helpers.StringToInt(c.Query("page"))
	if page == 0 {
		page = 1
	}
	pageSize := helpers.StringToInt(c.Query("pageSize"))
	if pageSize == 0 {
		pageSize = 100
	}
	mediaType := c.Query("type")
	status := c.Query("status")
	name := c.Query("name")
	scrapePathesCache := make(map[uint]*models.ScrapePath)
	total, scrapeRecords, err := models.GetScrapeMediaFiles(c.Request.Context(), page, pageSize, mediaType, status, name)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取刮削记录失败", Cause: err})
		return
	}
	type scrapeMediaResp struct {
		ID              uint   `json:"id"`
		Type            string `json:"type"`
		Path            string `json:"path"`
		FileName        string `json:"file_name"`
		MediaName       string `json:"media_name"`
		OriginalName    string `json:"original_name"`
		Year            int    `json:"year"`
		SeasonNumber    int    `json:"season_number"`
		EpisodeNumber   int    `json:"episode_number"`
		Genre           string `json:"genre"`
		Country         string `json:"country"`
		Language        string `json:"language"`
		Status          string `json:"status"`
		TmdbID          int64  `json:"tmdb_id"`
		NewPath         string `json:"new_path"` // 二级分类 + 文件夹
		NewFile         string `json:"new_file"` // 新文件名
		EpisodeName     string `json:"episode_name"`
		Resolution      string `json:"resolution"`       // 分辨率
		ResolutionLevel string `json:"resolution_level"` // 分辨率等级
		IsHDR           bool   `json:"is_hdr"`           // 是否 HDR
		AudioCount      int    `json:"audio_count"`      // 音频轨道数量
		SubtitleCount   int    `json:"subtitle_count"`   // 字幕轨道数量
		CreatedAt       int64  `json:"created_at"`       // 创建时间
		UpdatedAt       int64  `json:"updated_at"`       // 更新时间
		ScrapedAt       int64  `json:"scraped_at"`       // 刮削时间
		ScannedAt       int64  `json:"scanned_at"`       // 扫描时间
		RenamedAt       int64  `json:"renamed_at"`       // 整理时间
		FailedReason    string `json:"failed_reason"`    // 失败原因
		CategoryName    string `json:"category_name"`    // 分类名称
		NewDestPath     string `json:"new_dest_path"`    // 新路径
		NewDestName     string `json:"new_dest_name"`    // 新文件名
		PathIsScraping  bool   `json:"path_is_scraping"` // 是否正在刮削
		PathIsRenaming  bool   `json:"path_is_renaming"` // 是否正在整理
		SourceFullPath  string `json:"source_full_path"` // 原始路径
		DestFullPath    string `json:"dest_full_path"`   // 目标路径
		SourceType      string `json:"source_type"`      // 原始类型
		RenameType      string `json:"rename_type"`      // 重命名类型
		ScrapeType      string `json:"scrape_type"`      // 刮削类型
	}
	type scrapeListResp struct {
		Total int64              `json:"total"`
		List  []*scrapeMediaResp `json:"list"`
	}
	resp := scrapeListResp{
		Total: total,
		List:  make([]*scrapeMediaResp, len(scrapeRecords)),
	}
	for i, scrapeMedia := range scrapeRecords {
		var scrapePath *models.ScrapePath
		var ok bool
		if scrapePath, ok = scrapePathesCache[scrapeMedia.ScrapePathId]; !ok {
			scrapePath, err = models.ReadScrapePath(c.Request.Context(), scrapeMedia.ScrapePathId)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				scrapePath = &models.ScrapePath{}
			} else if err != nil {
				httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取刮削目录失败", Cause: err})
				return
			}
			scrapePathesCache[scrapeMedia.ScrapePathId] = scrapePath
		}
		sourcePath := scrapeMedia.GetRemoteFullMoviePath()
		destPath := scrapeMedia.GetDestFullMoviePath()
		if scrapeMedia.MediaType == models.MediaTypeTvShow {
			sourcePath = scrapeMedia.GetRemoteFullSeasonPath()
			destPath = scrapeMedia.GetDestFullSeasonPath()
		}
		sourcePath = filepath.ToSlash(filepath.Join(sourcePath, scrapeMedia.VideoFilename))
		destPath = filepath.ToSlash(filepath.Join(destPath, scrapeMedia.NewVideoBaseName+scrapeMedia.VideoExt))

		resp.List[i] = &scrapeMediaResp{
			Type:            string(scrapeMedia.MediaType),
			ID:              scrapeMedia.ID,
			Path:            scrapeMedia.Path,
			FileName:        scrapeMedia.VideoFilename,
			MediaName:       scrapeMedia.Name,
			Year:            scrapeMedia.Year,
			SeasonNumber:    scrapeMedia.SeasonNumber,
			EpisodeNumber:   scrapeMedia.EpisodeNumber,
			Status:          string(scrapeMedia.Status),
			TmdbID:          scrapeMedia.TmdbId,
			NewPath:         filepath.ToSlash(filepath.Join(scrapeMedia.CategoryName, scrapeMedia.NewPathName)),
			NewFile:         scrapeMedia.NewVideoBaseName + scrapeMedia.VideoExt,
			Resolution:      scrapeMedia.Resolution,
			ResolutionLevel: scrapeMedia.ResolutionLevel,
			IsHDR:           scrapeMedia.IsHDR,
			AudioCount:      len(scrapeMedia.AudioCodec),
			SubtitleCount:   len(scrapeMedia.SubtitleCodec),
			CreatedAt:       scrapeMedia.CreatedAt,
			UpdatedAt:       scrapeMedia.UpdatedAt,
			ScrapedAt:       scrapeMedia.ScrapeTime,
			ScannedAt:       scrapeMedia.ScanTime,
			RenamedAt:       scrapeMedia.RenameTime,
			CategoryName:    scrapeMedia.CategoryName,
			NewDestPath:     scrapeMedia.NewPathName,
			NewDestName:     scrapeMedia.NewVideoBaseName + scrapeMedia.VideoExt,
			FailedReason:    scrapeMedia.FailedReason,
			SourceFullPath:  sourcePath,
			DestFullPath:    destPath,
			SourceType:      string(scrapePath.SourceType),
			RenameType:      string(scrapePath.RenameType),
			ScrapeType:      string(scrapePath.ScrapeType),
		}
		if scrapeMedia.MediaType == models.MediaTypeTvShow {
			if scrapeMedia.MediaEpisode != nil {
				resp.List[i].EpisodeName = scrapeMedia.MediaEpisode.EpisodeName
			}
			resp.List[i].NewPath = filepath.ToSlash(filepath.Join(resp.List[i].NewPath, scrapeMedia.NewSeasonPathName))
		}
		if scrapePath.IsScraping && slices.Contains([]models.ScrapeMediaStatus{models.ScrapeMediaStatusScanned, models.ScrapeMediaStatusScraping}, scrapeMedia.Status) {
			resp.List[i].PathIsScraping = scrapePath.IsScraping
		}
		// if scrapeMedia.Media != nil {
		// 	if len(scrapeMedia.Media.Genres) > 0 {
		// 		for _, genre := range scrapeMedia.Media.Genres {
		// 			resp.List[i].Genre += genre.Name + ", "
		// 		}
		// 	}
		// 	if len(scrapeMedia.Media.OriginCountry) > 0 {
		// 		countries := ""
		// 		for _, country := range scrapeMedia.Media.OriginCountry {
		// 			if countries == "" {
		// 				countries = helpers.GetCountryName(country)
		// 			} else {
		// 				countries += ", " + helpers.GetCountryName(country)
		// 			}

		// 		}
		// 		resp.List[i].Country = countries
		// 	}
		// 	if scrapeMedia.Media.OriginalLanguage != "" {
		// 		resp.List[i].Language = helpers.GetLanguageName(scrapeMedia.Media.OriginalLanguage)
		// 	}
		// 	if scrapeMedia.Media.OriginalName != "" {
		// 		resp.List[i].OriginalName = scrapeMedia.Media.OriginalName
		// 	}
		// }
	}
	httpapi.Success(c, http.StatusOK, resp, "")
}

// ScrapeTmpImage 获取刮削临时图片
func ScrapeTmpImage(c *gin.Context) {
	var req requests.TmpImageRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	imageRootPath := filepath.Join(helpers.ConfigDir, "tmp", "刮削临时文件")
	if req.MediaType == models.MediaTypeTvShow {
		imageRootPath = filepath.Join(imageRootPath, "电视剧")
	} else {
		imageRootPath = filepath.Join(imageRootPath, "电影或其他")
	}
	var err error
	imagePath, err := helpers.SafeJoin(imageRootPath, req.Path)
	if err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "图片路径不合法", Cause: err})
		return
	}
	// 读取文件
	file, err := os.Open(imagePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "SCRAPE_IMAGE_NOT_FOUND", Message: "刮削图片不存在", Cause: err})
		} else {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取刮削图片失败", Cause: err})
		}
		return
	}
	defer file.Close()
	// 读取文件内容
	fileContent, err := io.ReadAll(file)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取图片内容失败", Cause: err})
		return
	}
	// 返回文件内容
	c.Data(http.StatusOK, "image/jpeg", fileContent)
}

// ExportScrapeRecords 导出刮削记录。
func ExportScrapeRecords(c *gin.Context) {
	var req requests.IDCSVRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	scrapeRecords, err := models.GetScrapeMediaFilesByIds(c.Request.Context(), req.NormalizedIDs())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取导出记录失败", Cause: err})
		return
	}
	if len(scrapeRecords) == 0 {
		httpapi.Success(c, http.StatusOK, nil, "没有找到要导出的记录")
		return
	}
	// 生成导出文件内容
	type exportMediaResult struct {
		Path          string `json:"path"`
		FileName      string `json:"filename"`
		TvPath        string `json:"tv_path"`
		Name          string `json:"name"`
		Year          int    `json:"year"`
		SeasonNumber  int    `json:"season_number"`
		EpisodeNumber int    `json:"episode_number"`
	}
	exportMediaList := make([]exportMediaResult, 0)
	for _, scrapeMedia := range scrapeRecords {
		exportMediaList = append(exportMediaList, exportMediaResult{
			Path:          scrapeMedia.Path,
			FileName:      scrapeMedia.VideoFilename,
			TvPath:        scrapeMedia.TvshowPath,
			Name:          scrapeMedia.Name,
			Year:          scrapeMedia.Year,
			SeasonNumber:  scrapeMedia.SeasonNumber,
			EpisodeNumber: scrapeMedia.EpisodeNumber,
		})
	}
	// JSON 格式化 exportMediaList
	exportMediaListJson, err := json.MarshalIndent(exportMediaList, "", "  ")
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "生成导出记录失败", Cause: err})
		return
	}
	c.Header("Content-Disposition", "attachment; filename=刮削记录.json")
	// 返回 JSON 文件内容，触发浏览器下载
	c.Data(http.StatusOK, "application/json", exportMediaListJson)
}

// ReScrape 重新刮削记录
func ReScrape(c *gin.Context) {
	var req requests.RescrapeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 使用 ID 查询 ScrapeMediaFile
	scrapeMedia, err := models.ReadScrapeMediaFile(c.Request.Context(), req.ID)
	if err != nil {
		status, failure := scrapeLookupError(err, "SCRAPE_RECORD_NOT_FOUND", "没有找到要重新刮削的记录")
		httpapi.Failure(c, status, failure)
		return
	}
	_, err = models.ReadScrapePath(c.Request.Context(), scrapeMedia.ScrapePathId)
	if err != nil {
		status, failure := scrapeLookupError(err, "SCRAPE_PATH_NOT_FOUND", "没有找到这条记录所属的刮削目录")
		httpapi.Failure(c, status, failure)
		return
	}
	oldStatus := scrapeMedia.Status
	err = scrapeMedia.ReScrape(c.Request.Context(), req.Name, req.Year, req.TmdbID, req.Season, req.Episode)
	if err != nil {
		status, failure := rescrapeError(err)
		httpapi.Failure(c, status, failure)
		return
	}
	if oldStatus == models.ScrapeMediaStatusRenamed {
		synccron.StartScrapeRollbackCron() // 触发一次
		httpapi.Success(c, http.StatusAccepted, gin.H{"id": scrapeMedia.ID, "status": scrapeMedia.Status}, "已提交回滚，下次扫描会按新的名称和年份重新刮削")
	} else {
		data := make(map[string]any)
		data["name"] = scrapeMedia.Name
		data["year"] = scrapeMedia.Year
		data["tmdb_id"] = scrapeMedia.TmdbId
		httpapi.Success(c, http.StatusOK, data, "下次扫描会按新的名称和年份重新刮削")
	}
}

// 清除所有刮削失败的记录
func ClearFailedScrapeRecords(c *gin.Context) {
	err := models.ClearFailedScrapeRecords([]uint{})
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "清除失败的刮削记录失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "失败的刮削记录已清除")
}

// FinishScrapeMediaFile 完成刮削记录
func FinishScrapeMediaFile(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 使用 ID 查询 ScrapeMediaFile
	scrapeMedia, err := models.ReadScrapeMediaFile(c.Request.Context(), req.ID)
	if err != nil {
		status, failure := scrapeLookupError(err, "SCRAPE_RECORD_NOT_FOUND", "没有找到要整理的记录")
		httpapi.Failure(c, status, failure)
		return
	}
	if err := scrapeMedia.FinishFromRenaming(); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存整理状态失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "记录已标记为已整理")
}

// DeleteScrapeMediaFile 删除刮削记录
func DeleteScrapeMediaFile(c *gin.Context) {
	var req requests.IDCSVRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 删除记录
	err := models.ClearFailedScrapeRecords(req.NormalizedIDs())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "删除刮削记录失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "记录已删除")
}

// RenameFailedScrapeMediaFile 标记记录为待整理
func RenameFailedScrapeMediaFile(c *gin.Context) {
	var req requests.IDCSVRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 将这些 ID 对应的记录标记为待整理
	err := models.RenameFailedScrapeRecords(req.NormalizedIDs())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "标记刮削记录失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "所选记录已标记为待整理")
}

// StopScrape 停止刮削任务
func StopScrape(c *gin.Context) {
	var req requests.PositiveIDRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	synccron.CancelNewSyncTask(req.ID, synccron.SyncTaskTypeScrape)
	httpapi.Success(c, http.StatusOK, nil, "刮削任务已停止")
}

// TruncateAllScrapeRecords 一键清空所有刮削记录
func TruncateAllScrapeRecords(c *gin.Context) {
	err := models.TruncateAllScrapeRecords()
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "清空刮削记录失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "所有刮削记录已清空")
}

func SaveScrapeStrmPath(c *gin.Context) {
	var req requests.SaveScrapeStrmPathRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	scrapePath, err := models.ReadScrapePath(c.Request.Context(), req.ScrapePathID)
	if err != nil {
		status, failure := scrapeLookupError(err, "SCRAPE_PATH_NOT_FOUND", "刮削目录不存在")
		httpapi.Failure(c, status, failure)
		return
	}
	if err := scrapePath.SaveStrmPath(req.SyncPathIDs); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存关联同步目录失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "关联同步目录已保存")
}

func GetScrapeStrmPaths(c *gin.Context) {
	type GetScrapePathStrmPathsReq struct {
		ID uint `json:"scrape_path_id" form:"scrape_path_id"`
	}
	var req GetScrapePathStrmPathsReq
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	scrapePath, err := models.ReadScrapePath(c.Request.Context(), req.ID)
	if err != nil {
		status, failure := scrapeLookupError(err, "SCRAPE_PATH_NOT_FOUND", "没有找到要操作的刮削目录")
		httpapi.Failure(c, status, failure)
		return
	}
	ssp := scrapePath.GetRelatStrmPath()
	syncPathIds := make([]uint, 0)
	for _, sp := range ssp {
		syncPathIds = append(syncPathIds, sp.StrmPathID)
	}

	httpapi.Success(c, http.StatusOK, syncPathIds, "获取关联同步目录成功")
}

type TmdbSearchResp struct {
	TmdbID        int    `json:"tmdb_id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	Year          int    `json:"year"`
	PosterUrl     string `json:"poster_url"`
	Overview      string `json:"overview"`
}

func TmdbSearch(c *gin.Context) {
	var req requests.TMDBSearchRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	settings := models.GetScrapeSettingsSnapshot()
	tmdbClient, err := settings.GetTmdbClient()
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "创建 TMDB 客户端失败", Cause: err, Secrets: []string{settings.GetTmdbApiKey(), settings.GetTmdbAccessToken()}})
		return
	}
	defer tmdbClient.Close()
	switch req.Type {
	case models.MediaTypeMovie:
		if req.TmdbID == 0 {
			// 搜索电影
			resp, err := tmdbClient.SearchMovie(c.Request.Context(), req.Name, req.Year, settings.GetTmdbLanguage(), true, false)
			if err != nil {
				status, failure := tmdbQueryError(err, "搜索电影失败", settings.GetTmdbApiKey(), settings.GetTmdbAccessToken())
				httpapi.Failure(c, status, failure)
				return
			}
			if len(resp.Results) == 0 {
				httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "TMDB_MEDIA_NOT_FOUND", Message: "没有找到电影"})
				return
			}
			// 转换为响应结构体
			tmdbResp := make([]TmdbSearchResp, 0)
			for _, r := range resp.Results {
				tmdbResp = append(tmdbResp, TmdbSearchResp{
					TmdbID:        int(r.ID),
					Title:         r.Title,
					OriginalTitle: r.OriginalTitle,
					Year:          helpers.ParseYearFromDate(r.ReleaseDate),
					PosterUrl:     settings.ImageURL(r.PosterPath),
					Overview:      r.Overview,
				})
			}
			httpapi.Success(c, http.StatusOK, tmdbResp, "搜索电影成功")
			return
		} else {
			resp, err := tmdbClient.GetMovieDetail(c.Request.Context(), int64(req.TmdbID), settings.GetTmdbLanguage())
			if err != nil {
				status, failure := tmdbQueryError(err, "获取电影详情失败", settings.GetTmdbApiKey(), settings.GetTmdbAccessToken())
				httpapi.Failure(c, status, failure)
				return
			}
			tmdbResp := make([]TmdbSearchResp, 0)
			// 转换为响应结构体
			tmdbResp = append(tmdbResp, TmdbSearchResp{
				TmdbID:        int(resp.ID),
				Title:         resp.Title,
				OriginalTitle: resp.OriginalTitle,
				Year:          helpers.ParseYearFromDate(resp.ReleaseDate),
				PosterUrl:     settings.ImageURL(resp.PosterPath),
				Overview:      resp.Overview,
			})
			httpapi.Success(c, http.StatusOK, tmdbResp, "获取电影详情成功")
			return
		}
	case models.MediaTypeTvShow:
		if req.TmdbID == 0 {
			// 搜索电视剧
			resp, err := tmdbClient.SearchTv(c.Request.Context(), req.Name, req.Year, settings.GetTmdbLanguage(), true)
			if err != nil {
				status, failure := tmdbQueryError(err, "搜索电视剧失败", settings.GetTmdbApiKey(), settings.GetTmdbAccessToken())
				httpapi.Failure(c, status, failure)
				return
			}
			if len(resp.Results) == 0 {
				httpapi.Failure(c, http.StatusNotFound, httpapi.Error{Code: "TMDB_MEDIA_NOT_FOUND", Message: "没有找到电视剧"})
				return
			}
			// 转换为响应结构体
			tmdbResp := make([]TmdbSearchResp, 0)
			for _, r := range resp.Results {
				tmdbResp = append(tmdbResp, TmdbSearchResp{
					TmdbID:        int(r.ID),
					Title:         r.Name,
					OriginalTitle: r.OriginalName,
					Year:          helpers.ParseYearFromDate(r.FirstAirDate),
					PosterUrl:     settings.ImageURL(r.PosterPath),
					Overview:      r.Overview,
				})
			}
			httpapi.Success(c, http.StatusOK, tmdbResp, "搜索电视剧成功")
			return
		} else {
			resp, err := tmdbClient.GetTvDetail(c.Request.Context(), int64(req.TmdbID), settings.GetTmdbLanguage())
			if err != nil {
				status, failure := tmdbQueryError(err, "获取电视剧详情失败", settings.GetTmdbApiKey(), settings.GetTmdbAccessToken())
				httpapi.Failure(c, status, failure)
				return
			}
			tmdbResp := make([]TmdbSearchResp, 0)
			// 转换为响应结构体
			tmdbResp = append(tmdbResp, TmdbSearchResp{
				TmdbID:        int(resp.ID),
				Title:         resp.Name,
				OriginalTitle: resp.OriginalName,
				Year:          helpers.ParseYearFromDate(resp.FirstAirDate),
				PosterUrl:     settings.ImageURL(resp.PosterPath),
				Overview:      resp.Overview,
			})
			httpapi.Success(c, http.StatusOK, tmdbResp, "获取电视剧详情成功")
			return
		}
	default:
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请求参数错误：类型必须是 movie 或 tv_show", Data: nil})
		return
	}

}

func scrapeLookupError(err error, code, message string) (int, httpapi.Error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return http.StatusNotFound, httpapi.Error{Code: code, Message: message, Cause: err}
	}
	return http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "读取刮削数据失败", Cause: err}
}

func rescrapeError(err error) (int, httpapi.Error) {
	if upstream, ok := errors.AsType[*models.ScrapeTMDBError](err); ok {
		return tmdbQueryError(err, "重新刮削失败", upstream.Secrets...)
	}
	switch {
	case errors.Is(err, models.ErrScrapeMediaAmbiguous):
		return http.StatusBadRequest, httpapi.Error{Code: "SCRAPE_MEDIA_AMBIGUOUS", Message: "查询到多个匹配结果，请输入 TMDB ID 后重新识别", Cause: err}
	case errors.Is(err, models.ErrScrapeMediaNotFound):
		return http.StatusNotFound, httpapi.Error{Code: "TMDB_MEDIA_NOT_FOUND", Message: "没有找到匹配的媒体", Cause: err}
	default:
		settings := models.GetScrapeSettingsSnapshot()
		return http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "重新刮削失败", Cause: err, Secrets: []string{settings.GetTmdbApiKey(), settings.GetTmdbAccessToken()}}
	}
}

func tmdbQueryError(err error, message string, secrets ...string) (int, httpapi.Error) {
	status, code := http.StatusBadGateway, "UPSTREAM_ERROR"
	if timeout, ok := errors.AsType[net.Error](err); errors.Is(err, context.DeadlineExceeded) || ok && timeout.Timeout() {
		status, code = http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT"
	}
	return status, httpapi.Error{Code: code, Message: message, Cause: err, Secrets: secrets}
}
