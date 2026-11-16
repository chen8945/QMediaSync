package controllers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/emby"
	"qmediasync/internal/github"
	"qmediasync/internal/helpers"
	"qmediasync/internal/httpapi"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
	"qmediasync/internal/validation"
)

// github 包不能引用 helpers（helpers/net.go 已导入 github，反向依赖会形成 import 循环），
// 默认只能写 stdlib log、绕过 QLogger 的级别控制和脱敏。这里从同时依赖两者的 controllers 侧注入。
// 用闭包而非 helpers.AppLogger.Infof 方法值：AppLogger 在日志初始化时会被重新赋值，必须每次调用时再读。
func init() {
	github.SetLogPrintf(func(format string, args ...any) {
		helpers.AppLogger.Infof(format, args...)
	})
}

// LogSettingResponse 日志设置响应。
type LogSettingResponse struct {
	Level      string   `json:"level"`
	Levels     []string `json:"levels"`
	MaxSizeMB  int      `json:"maxSizeMB"`
	MaxBackups int      `json:"maxBackups"`
	MaxAgeDays int      `json:"maxAgeDays"`
}

func currentLogSettingResponse() LogSettingResponse {
	logConfig := helpers.LogConfigSnapshot()
	return LogSettingResponse{
		Level:      helpers.ConfiguredLogLevel().String(),
		Levels:     helpers.LogLevelNames(),
		MaxSizeMB:  logConfig.MaxSizeMB,
		MaxBackups: logConfig.MaxBackups,
		MaxAgeDays: logConfig.MaxAgeDays,
	}
}

// GetLogSetting 获取日志设置。
func GetLogSetting(c *gin.Context) {
	httpapi.Success(c, http.StatusOK, currentLogSettingResponse(), "获取日志设置成功")
}

// UpdateLogSetting 更新日志设置。
func UpdateLogSetting(c *gin.Context) {
	var req requests.UpdateLogSettingRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	level, _ := helpers.ParseLogLevel(req.Level)
	logConfig := helpers.LogConfigSnapshot()
	maxSizeMB := logConfig.MaxSizeMB
	if req.MaxSizeMB != nil {
		maxSizeMB = *req.MaxSizeMB
	}
	maxBackups := logConfig.MaxBackups
	if req.MaxBackups != nil {
		maxBackups = *req.MaxBackups
	}
	maxAgeDays := logConfig.MaxAgeDays
	if req.MaxAgeDays != nil {
		maxAgeDays = *req.MaxAgeDays
	}

	if err := helpers.SaveLogSetting(helpers.LogSetting{
		Level:      level,
		MaxSizeMB:  maxSizeMB,
		MaxBackups: maxBackups,
		MaxAgeDays: maxAgeDays,
	}); err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "保存日志设置失败", Cause: err})
		return
	}

	httpapi.Success(c, http.StatusOK, currentLogSettingResponse(), "日志设置已更新")
}

// ParseEmby 手动解析 Emby 媒体信息。
func ParseEmby(c *gin.Context) { parseMediaLibrary(c, models.MediaServerEmby) }

// ParseJellyfin 手动解析 Jellyfin 媒体信息。
func ParseJellyfin(c *gin.Context) { parseMediaLibrary(c, models.MediaServerJellyfin) }
func parseMediaLibrary(c *gin.Context, kind models.MediaServerType) {
	if err := emby.StartParseMediaLibraryInfo(kind); err != nil {
		if errors.Is(err, emby.ErrMediaLibraryUnavailable) {
			httpapi.Failure(c, http.StatusConflict, httpapi.Error{Code: "MEDIA_LIBRARY_UNAVAILABLE", Message: "请先启用并配置媒体库，或等待正在运行的任务完成", Cause: err})
		} else {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "启动媒体信息提取失败", Cause: err})
		}
		return
	}
	httpapi.Success(c, http.StatusAccepted, gin.H{"status": "submitted"}, "媒体信息提取任务已开始")
}

// UpdateHttpProxy 更新出站代理设置。
func UpdateHttpProxy(c *gin.Context) {
	var req requests.HTTPProxyRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.ValidateSave(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	warnings, err := models.SaveHTTPProxy(c.Request.Context(), req.NormalizedHTTPProxy(), req.PreserveProxyCredentials)
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "更新出站代理设置失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "出站代理设置已更新", warnings...)
}

// GetHttpProxy 获取出站代理设置。
func GetHttpProxy(c *gin.Context) {
	// 获取设置
	stored := models.GetHTTPProxy()
	// 代理地址可带凭据，接口一律回传脱敏结果；前端用显式 preserve_proxy_credentials 标志还原当前凭据。
	redacted := validation.RedactProxyURL(stored)
	credentialsMasked := "0"
	if redacted != stored {
		credentialsMasked = "1"
	}
	httpProxy := map[string]string{
		"http_proxy":         redacted,
		"credentials_masked": credentialsMasked,
	}
	httpapi.Success(c, http.StatusOK, httpProxy, "获取出站代理设置成功")
}

// TestHttpProxy 测试出站代理连接。
func TestHttpProxy(c *gin.Context) {
	var req requests.HTTPProxyRequest
	// 获取请求参数
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.ValidateTest(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	// 用户不修改地址直接点测试时，前端显式要求保留凭据，再用当前真实凭据拨号。
	httpProxy := models.ResolveHTTPProxy(req.NormalizedHTTPProxy(), models.GetHTTPProxy(), req.PreserveProxyCredentials)
	detailed := req.Detailed == 1

	if detailed {
		// 使用高级测试，返回详细结果
		result, err := helpers.TestHttpProxyAdvancedWithContext(c.Request.Context(), httpProxy)
		if err != nil {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "PROXY_TEST_FAILED", Message: "出站代理连接测试失败", Cause: err})
			return
		}

		if result.Success {
			httpapi.Success(c, http.StatusOK, result, "出站代理连接测试成功")
		} else {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "PROXY_TEST_FAILED", Message: "出站代理连接测试失败", Cause: errors.New(result.ErrorMessage)})
		}
	} else {
		// 使用简单测试
		success, err := helpers.TestHttpProxyWithContext(c.Request.Context(), httpProxy)
		if err != nil {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "PROXY_TEST_FAILED", Message: "出站代理连接测试失败", Cause: err})
			return
		}

		if success {
			httpapi.Success(c, http.StatusOK, nil, "出站代理连接测试成功")
		} else {
			httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "PROXY_TEST_FAILED", Message: "出站代理连接测试失败"})
		}
	}
}

// GetStrmConfig 获取 STRM 配置
func GetStrmConfig(c *gin.Context) {
	// 获取设置
	strm, multiPlaybackEnabled, preloadEnabled := models.SettingsGlobal.StrmConfigSnapshot()
	data := strm.ToMap(false, true)
	data["multi_playback_enabled"] = multiPlaybackEnabled
	data["strm_preload_enabled"] = preloadEnabled
	httpapi.Success(c, http.StatusOK, data, "获取 STRM 配置成功")
}

// UpdateStrmConfig 更新 STRM 配置
func UpdateStrmConfig(c *gin.Context) {
	// 获取请求参数
	var req requests.UpdateStrmConfigRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	modelReq := req.ToModel()
	previousStrm, _ := models.SettingsGlobal.StrmSnapshot()
	// 更新设置
	if !models.SettingsGlobal.UpdateStrm(modelReq, req.MultiPlaybackEnabled, req.StrmPreloadEnabled) {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "更新 STRM 配置失败"})
		return
	}
	currentStrm, _ := models.SettingsGlobal.StrmSnapshot()
	if previousStrm.Cron != currentStrm.Cron {
		// 如果 Cron 发生变化，重启任务
		synccron.InitCron()
	}
	httpapi.Success(c, http.StatusOK, nil, "STRM 配置已更新")
}

// GetCronNextTime 获取 Cron 表达式的下次执行时间。
func GetCronNextTime(c *gin.Context) {
	var req requests.GetCronNextTimeRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	times := helpers.GetNextTimeByCronStr(req.NormalizedCron(), 5)
	if times == nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.Error{Code: "INVALID_REQUEST", Message: "仅支持 5 位 cron 表达式或 robfig 描述符", Data: nil})
		return
	}
	var timeStrs []string
	for _, t := range times {
		timeStrs = append(timeStrs, t.Format("2006-01-02 15:04:05"))
	}
	httpapi.Success(c, http.StatusOK, timeStrs, "获取下次执行时间成功")
}

// ValidateCron 验证 Cron 表达式并返回描述。
func ValidateCron(c *gin.Context) {
	var req requests.ValidateCronRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}

	// 解析 Cron 表达式为可读描述
	scrapePath := &models.ScrapePath{}
	description := scrapePath.ParseCronDescription(req.NormalizedCronExpression())

	httpapi.Success(c, http.StatusOK, map[string]string{
		"description": description,
	}, "Cron 表达式有效")
}

// GetThreads 获取线程配置
func GetThreads(c *gin.Context) {
	httpapi.Success(c, http.StatusOK, models.GetThreadSettingsSnapshot(), "获取线程数成功")
}

// UpdateThreads 更新线程配置
func UpdateThreads(c *gin.Context) {
	var req requests.UpdateThreadsRequest
	if err := c.ShouldBind(&req); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	if err := req.Validate(); err != nil {
		httpapi.Failure(c, http.StatusBadRequest, httpapi.InvalidRequest(err))
		return
	}
	warnings, err := models.SaveThreadSettings(c.Request.Context(), req.ToUpdate())
	if err != nil {
		httpapi.Failure(c, http.StatusInternalServerError, httpapi.Error{Code: "INTERNAL_ERROR", Message: "更新线程数失败", Cause: err})
		return
	}
	httpapi.Success(c, http.StatusOK, nil, "更新线程数成功", warnings...)
}
