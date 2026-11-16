package controllers

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/httpapi"
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
func GetTmdbSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
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
	httpapi.Success(c, http.StatusOK, tmdbSettings, "")
}

// SaveTmdbSettings 保存 TMDB 设置。
func SaveTmdbSettings(c *gin.Context) {
	reqData := requests.TMDBSettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := reqData.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := models.SaveTmdbSettings(c.Request.Context(), models.ScrapeSettings{
		TmdbApiKey: reqData.TmdbAPIKey, TmdbAccessToken: reqData.TmdbAccessToken, TmdbUrl: reqData.TmdbURL, TmdbImageUrl: reqData.TmdbImageURL,
		TmdbLanguage: reqData.TmdbLanguage, TmdbImageLanguage: reqData.TmdbImageLanguage, TmdbEnableProxy: reqData.TmdbEnableProxy, FanartApiKey: reqData.FanartAPIKey,
	}); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存 TMDB 设置失败", Cause: err, Secrets: []string{reqData.TmdbAPIKey, reqData.TmdbAccessToken, reqData.FanartAPIKey}})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "TMDB 设置已保存")
}

// TestTmdbSettings 测试 TMDB 设置。
func TestTmdbSettings(c *gin.Context) {
	reqData := requests.TMDBSettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := reqData.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
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
	httpapi.Success(c, http.StatusOK, testResult, "")
}

// SaveAiSettings 保存 AI 识别设置。
func SaveAiSettings(c *gin.Context) {
	reqData := requests.AISettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := reqData.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := models.SaveAISettings(c.Request.Context(), models.ScrapeSettings{AiApiKey: reqData.AIAPIKey, AiBaseUrl: reqData.AIBaseURL, AiModelName: reqData.AIModelName, AiTimeout: reqData.AITimeout}); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存 AI 识别设置失败", Cause: err, Secrets: []string{reqData.AIAPIKey}})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "AI 识别设置已保存")
}

// TestAiSettings 测试 AI 识别设置。
func TestAiSettings(c *gin.Context) {
	reqData := requests.AISettingsRequest{}
	if err := c.ShouldBindJSON(&reqData); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := reqData.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if reqData.AIAPIKey == "" || reqData.AIBaseURL == "" || reqData.AIModelName == "" {
		var fields []httpapi.FieldError
		for _, field := range []struct {
			name, value string
		}{{"ai_api_key", reqData.AIAPIKey}, {"ai_base_url", reqData.AIBaseURL}, {"ai_model_name", reqData.AIModelName}} {
			if field.value == "" {
				fields = append(fields, httpapi.FieldError{Field: field.name, Message: "必须填写"})
			}
		}
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "请填写 API Key、接口地址和模型名称", FieldErrors: fields})
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
		status, code, message := http.StatusInternalServerError, "AI_TEST_FAILED", "AI 识别连接测试失败"
		if timeout, ok := errors.AsType[net.Error](testResult); errors.Is(testResult, context.DeadlineExceeded) || ok && timeout.Timeout() {
			status, code, message = http.StatusGatewayTimeout, "UPSTREAM_TIMEOUT", "AI 识别连接测试超时"
		}
		httpapi.Failure(c, status, httpapi.Error{Code: code, Message: message, Cause: testResult, Secrets: []string{reqData.AIAPIKey}})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "AI 识别连接测试成功")
}

// GetAiSettings 获取 AI 识别设置。
func GetAiSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store, private")
	snapshot := models.GetScrapeSettingsSnapshot()
	aiSettings := AiSettings{
		AiApiKey:    snapshot.AiApiKey,
		AiBaseUrl:   snapshot.AiBaseUrl,
		AiModelName: snapshot.AiModelName,
		AiTimeout:   snapshot.AiTimeout,
	}
	httpapi.Success(c, http.StatusOK, aiSettings, "")
}
