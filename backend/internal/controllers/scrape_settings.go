package controllers

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
)

// 基础配置
// TMDB API Key 或 Access Token 设置
// 1. TMDB API Key 或 Access Token 设置查询和保存
// 2. AI 识别设置查询和保存
// 分类规则
// 1 分类列表（分电影和电视剧）
// 2. 分类添加和编辑（分电影和电视剧）
// 分类数据
// 1. 电影分类数组
// 2. 电视剧分类数组
// 语言数组
// 1. 语言数组

type TmdbSettings struct {
	TmdbApiKey        string `json:"tmdb_api_key" form:"tmdb_api_key"`
	TmdbAccessToken   string `json:"tmdb_access_token" form:"tmdb_access_token"`
	TmdbUrl           string `json:"tmdb_url" form:"tmdb_url"`
	TmdbImageUrl      string `json:"tmdb_image_url" form:"tmdb_image_url"`
	TmdbLanguage      string `json:"tmdb_language" form:"tmdb_language"`
	TmdbImageLanguage string `json:"tmdb_image_language" form:"tmdb_image_language"`
	TmdbEnableProxy   bool   `json:"tmdb_enable_proxy" form:"tmdb_enable_proxy"`
	FanartApiKey      string `json:"fanart_api_key" form:"fanart_api_key"`
}

type AiSettings struct {
	EnableAi    models.AiAction `json:"enable_ai" form:"enable_ai"`
	AiApiKey    string          `json:"ai_api_key" form:"ai_api_key"`
	AiBaseUrl   string          `json:"ai_base_url" form:"ai_base_url"`
	AiModelName string          `json:"ai_model_name" form:"ai_model_name"`
	AiPrompt    string          `json:"ai_prompt" form:"ai_prompt"`
	AiTimeout   int             `json:"ai_timeout" form:"ai_timeout"`
}

// GetTmdbSettings 获取 TMDB 设置。
// @Summary 获取 TMDB 设置
// @Description 获取当前的 TMDB API 配置
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/tmdb [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetTmdbSettings(c *gin.Context) {
	snapshot := models.GetScrapeSettingsSnapshot()
	tmdbSettings := TmdbSettings{
		TmdbApiKey:        snapshot.TmdbApiKey,
		TmdbAccessToken:   snapshot.TmdbAccessToken,
		TmdbUrl:           snapshot.TmdbUrl,
		TmdbImageUrl:      snapshot.TmdbImageUrl,
		TmdbLanguage:      snapshot.TmdbLanguage,
		TmdbImageLanguage: snapshot.TmdbImageLanguage,
		TmdbEnableProxy:   snapshot.TmdbEnableProxy,
		FanartApiKey:      snapshot.FanartApiKey,
	}
	c.JSON(http.StatusOK, APIResponse[TmdbSettings]{Code: Success, Message: "", Data: tmdbSettings})
}

// SaveTmdbSettings 保存 TMDB 设置。
// @Summary 保存 TMDB 设置
// @Description 保存或更新 TMDB API 配置
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param tmdb_api_key body string false "TMDB API Key"
// @Param tmdb_access_token body string false "TMDB Access Token"
// @Param tmdb_url body string false "TMDB 服务器地址"
// @Param tmdb_image_url body string false "TMDB 图片服务器地址"
// @Param tmdb_language body string false "TMDB 默认语言"
// @Param tmdb_image_language body string false "TMDB 图片语言"
// @Param tmdb_enable_proxy body boolean false "是否启用代理"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/tmdb [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func SaveTmdbSettings(c *gin.Context) {
	reqData := requests.TMDBSettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := reqData.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := models.SaveTmdbSettings(c.Request.Context(), models.ScrapeSettings{
		TmdbApiKey: reqData.TmdbAPIKey, TmdbAccessToken: reqData.TmdbAccessToken, TmdbUrl: reqData.TmdbURL, TmdbImageUrl: reqData.TmdbImageURL,
		TmdbLanguage: reqData.TmdbLanguage, TmdbImageLanguage: reqData.TmdbImageLanguage, TmdbEnableProxy: reqData.TmdbEnableProxy, FanartApiKey: reqData.FanartAPIKey,
	}); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "TMDB 设置已保存", Data: nil})
}

// TestTmdbSettings 测试 TMDB 设置。
// @Summary 测试 TMDB 连接
// @Description 测试指定的 TMDB 配置是否有效
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param tmdb_api_key body string false "TMDB API Key"
// @Param tmdb_access_token body string false "TMDB Access Token"
// @Param tmdb_url body string false "TMDB 服务器地址"
// @Param tmdb_image_url body string false "TMDB 图片服务器地址"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/tmdb-test [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func TestTmdbSettings(c *gin.Context) {
	reqData := requests.TMDBSettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := reqData.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	tmpScrapeSetting := &models.ScrapeSettings{
		TmdbApiKey:        reqData.TmdbAPIKey,
		TmdbAccessToken:   reqData.TmdbAccessToken,
		TmdbUrl:           reqData.TmdbURL,
		TmdbImageUrl:      reqData.TmdbImageURL,
		TmdbLanguage:      reqData.TmdbLanguage,
		TmdbImageLanguage: reqData.TmdbImageLanguage,
		TmdbEnableProxy:   reqData.TmdbEnableProxy,
		HttpProxy:         models.GetHTTPProxy(),
	}
	testResult := tmpScrapeSetting.TestTmdb(c.Request.Context())
	c.JSON(http.StatusOK, APIResponse[bool]{Code: Success, Message: "", Data: testResult})
}

// SaveAiSettings 保存 AI 识别设置。
// @Summary 保存 AI 识别设置
// @Description 保存或更新 AI 识别模型的配置
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param enable_ai body string false "是否启用 AI 识别"
// @Param ai_api_key body string false "AI API Key"
// @Param ai_base_url body string false "AI 服务器地址"
// @Param ai_model_name body string false "AI 模型名称"
// @Param ai_timeout body integer false "AI 超时时间"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/ai-settings [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func SaveAiSettings(c *gin.Context) {
	reqData := requests.AISettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := reqData.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := models.SaveAISettings(c.Request.Context(), models.ScrapeSettings{AiApiKey: reqData.AIAPIKey, AiBaseUrl: reqData.AIBaseURL, AiModelName: reqData.AIModelName, AiTimeout: reqData.AITimeout}); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "AI 识别设置已保存", Data: nil})
}

// TestAiSettings 测试 AI 识别设置。
// @Summary 测试 AI 识别连接
// @Description 测试指定的 AI 模型配置是否有效
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Param ai_api_key body string true "AI API Key"
// @Param ai_base_url body string true "AI 服务器地址"
// @Param ai_model_name body string true "AI 模型名称"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/ai-test [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func TestAiSettings(c *gin.Context) {
	reqData := requests.AISettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if err := reqData.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	if reqData.AIAPIKey == "" || reqData.AIBaseURL == "" || reqData.AIModelName == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请填写 API Key、接口地址和模型名称", Data: nil})
		return
	}
	tmpScrapeSetting := &models.ScrapeSettings{
		AiApiKey:    reqData.AIAPIKey,
		AiBaseUrl:   reqData.AIBaseURL,
		AiModelName: reqData.AIModelName,
		AiTimeout:   reqData.AITimeout, AiPrompt: reqData.AIPrompt,
	}
	testResult := tmpScrapeSetting.TestAi(c.Request.Context())
	if testResult != nil {
		c.JSON(http.StatusOK, APIResponse[error]{Code: BadRequest, Message: helpers.RedactSensitiveLog(testResult.Error(), reqData.AIAPIKey), Data: nil})
		return
	}
	c.JSON(http.StatusOK, APIResponse[error]{Code: Success, Message: "AI 识别连接测试成功", Data: nil})
}

// GetAiSettings 获取 AI 识别设置。
// @Summary 获取 AI 识别设置
// @Description 获取当前的 AI 识别模型配置
// @Tags 刮削管理
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /scrape/ai-settings [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetAiSettings(c *gin.Context) {
	snapshot := models.GetScrapeSettingsSnapshot()
	aiSettings := AiSettings{
		AiApiKey:    snapshot.AiApiKey,
		AiBaseUrl:   snapshot.AiBaseUrl,
		AiModelName: snapshot.AiModelName,
		AiTimeout:   snapshot.AiTimeout,
	}
	c.JSON(http.StatusOK, APIResponse[AiSettings]{Code: Success, Message: "", Data: aiSettings})
}
