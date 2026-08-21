package controllers

import (
	"net/http"
	"net/url"
	"strings"

	"qmediasync/internal/emby"
	"qmediasync/internal/github"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/synccron"
	"qmediasync/internal/v115open"
	"qmediasync/internal/validation"

	"github.com/gin-gonic/gin"
)

// github 包不能引用 helpers（helpers/net.go 已导入 github，反向依赖会形成 import 循环），
// 默认只能写 stdlib log、绕过 QLogger 的级别控制和脱敏。这里从同时依赖两者的 controllers 侧注入。
// 用闭包而非 helpers.AppLogger.Infof 方法值：AppLogger 在日志初始化时会被重新赋值，必须每次调用时再读。
func init() {
	github.SetLogPrintf(func(format string, args ...any) {
		helpers.AppLogger.Infof(format, args...)
	})
}

// 允许控制器测试替换运行时配置应用函数，生产环境使用 115 全局执行器实现。
var setGlobalExecutorConfig = v115open.SetGlobalExecutorConfig

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

// func UpdateEmby(c *gin.Context) {
// 	type updateEmbyRequest struct {
// 		EmbyUrl    string `form:"emby_url" json:"emby_url"`         // Emby URL
// 		EmbyApiKey string `form:"emby_api_key" json:"emby_api_key"` // Emby API Key
// 	}
// 	// 获取请求参数
// 	var req updateEmbyRequest
// 	if err := c.ShouldBind(&req); err != nil {
// 		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
// 		return
// 	}
// 	// 更新设置
// 	if !models.SettingsGlobal.UpdateEmbyUrl(req.EmbyUrl, req.EmbyApiKey) {
// 		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新 Emby URL 失败", Data: nil})
// 		return
// 	}

// 	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "Emby URL 已更新", Data: nil})
// }

// func GetEmby(c *gin.Context) {
// 	// 获取设置
// 	models.LoadSettings() // 确保设置已加载
// 	emby := make(map[string]string)
// 	emby["emby_url"] = models.GlobalEmbyConfig.EmbyUrl
// 	emby["emby_api_key"] = models.GlobalEmbyConfig.EmbyApiKey
// 	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取 Emby 设置成功", Data: emby})
// }

// GetLogSetting 获取日志设置。
// @Summary 获取日志设置
// @Description 获取当前运行日志等级
// @Tags 系统设置
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/log [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetLogSetting(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[LogSettingResponse]{
		Code:    Success,
		Message: "获取日志设置成功",
		Data:    currentLogSettingResponse(),
	})
}

// UpdateLogSetting 更新日志设置。
// @Summary 更新日志设置
// @Description 更新运行日志等级，保存后立即生效
// @Tags 系统设置
// @Accept json
// @Produce json
// @Param level body string true "日志等级：debug/info/warn/error"
// @Param maxSizeMB body int false "单文件最大大小 MB，缺省时沿用当前配置"
// @Param maxBackups body int false "保留备份数，缺省时沿用当前配置"
// @Param maxAgeDays body int false "保留天数，缺省时沿用当前配置"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/log [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateLogSetting(c *gin.Context) {
	var req requests.UpdateLogSettingRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
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
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "保存日志设置失败：" + err.Error(), Data: nil})
		return
	}

	c.JSON(http.StatusOK, APIResponse[LogSettingResponse]{
		Code:    Success,
		Message: "日志设置已更新",
		Data:    currentLogSettingResponse(),
	})
}

// ParseEmby 手动解析 Emby 媒体信息。
// @Summary 解析 Emby 媒体信息
// @Description 手动触发 Emby 媒体信息解析任务
// @Tags 系统设置
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/emby/parse [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ParseEmby(c *gin.Context) {
	if models.GlobalEmbyConfig.EmbyUrl == "" || models.GlobalEmbyConfig.EmbyApiKey == "" {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请先填写 Emby URL 和 Emby API Key，才能提取媒体信息", Data: nil})
		return
	}
	if emby.EmbyMediaInfoStart {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "Emby 媒体信息解析任务正在运行，请稍后再试", Data: nil})
		return
	}
	emby.StartParseEmbyMediaInfo()
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "Emby 媒体信息解析任务已开始", Data: nil})
}

// // UpdateTelegram 更新 Telegram Bot 配置
// // @Summary 更新 Telegram 配置
// // @Description 启用或配置 Telegram 通知 Bot
// // @Tags 系统设置
// // @Accept json
// // @Produce json
// // @Param enabled body integer true "是否启用，1 启用 0 禁用"
// // @Param token body string false "Telegram Bot Token"
// // @Param chat_id body string false "Telegram Chat ID"
// // @Success 200 {object} object
// // @Failure 200 {object} object
// // @Router /setting/telegram [post]
// // @Security JwtAuth
// // @Security ApiKeyAuth
// func UpdateTelegram(c *gin.Context) {
// 	type updateTelegramRequest struct {
// 		Enabled int    `form:"enabled" json:"enabled"` // 是否启用 Telegram 通知，"1" 表示启用，"0" 表示禁用
// 		Token   string `form:"token" json:"token"`     // Telegram Bot 的 Token
// 		ChatId  string `form:"chat_id" json:"chat_id"` // Telegram Chat ID
// 	}
// 	// 获取请求参数
// 	var req updateTelegramRequest
// 	if err := c.ShouldBind(&req); err != nil {
// 		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
// 		return
// 	}
// 	enabled := req.Enabled == 1
// 	token := req.Token
// 	chatId := req.ChatId

// 	// 如果启用 Telegram，则需要验证 token 和 chatId
// 	if enabled && (token == "" || chatId == "") {
// 		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "启用 Telegram 通知时，Token 和 Chat ID 不能为空", Data: nil})
// 		return
// 	}
// 	// 更新设置
// 	if !models.SettingsGlobal.UpdateTelegramBot(enabled, token, chatId) {
// 		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新 Telegram Bot 设置失败", Data: nil})
// 		return
// 	}

// 	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "Telegram Bot 设置已更新", Data: nil})
// }

// // GetTelegram 获取 Telegram Bot 配置
// // @Summary 获取 Telegram 配置
// // @Description 获取当前的 Telegram Bot 通知配置
// // @Tags 系统设置
// // @Accept json
// // @Produce json
// // @Success 200 {object} object
// // @Failure 200 {object} object
// // @Router /setting/telegram [get]
// // @Security JwtAuth
// // @Security ApiKeyAuth
// func GetTelegram(c *gin.Context) {
// 	// 获取设置
// 	models.LoadSettings() // 确保设置已加载
// 	telegramBot := make(map[string]string)
// 	if models.SettingsGlobal.UseTelegram == 1 {
// 		telegramBot["enabled"] = "1"
// 	} else {
// 		telegramBot["enabled"] = "0"
// 	}
// 	telegramBot["token"] = models.SettingsGlobal.TelegramBotToken
// 	telegramBot["chat_id"] = models.SettingsGlobal.TelegramChatId
// 	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取 Telegram Bot 设置成功", Data: telegramBot})
// }

// resolveSubmittedHTTPProxy 按请求的显式意图保留当前代理凭据。
//
// GetHttpProxy 只回传脱敏地址。前端未编辑输入框时显式传 preserveProxyCredentials，
// 只有提交地址与已存地址的端点一致，才可以替换 User 字段。
func resolveSubmittedHTTPProxy(submitted string, preserveProxyCredentials bool) string {
	if submitted == "" || !preserveProxyCredentials || models.SettingsGlobal.HttpProxy == "" {
		return submitted
	}

	stored, err := url.Parse(models.SettingsGlobal.HttpProxy)
	if err != nil {
		return submitted
	}
	if stored.User == nil {
		return submitted
	}
	parsed, err := url.Parse(submitted)
	if err != nil {
		return submitted
	}
	if !sameHTTPProxyEndpoint(stored, parsed) {
		return submitted
	}
	parsed.User = stored.User
	return parsed.String()
}

// sameHTTPProxyEndpoint 判断两个代理 URL 是否指向相同的协议、主机和端口。
// User 信息、路径和查询参数不参与端点比较，避免将存储凭据转发到其他代理端点。
func sameHTTPProxyEndpoint(left *url.URL, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

// shouldPreserveProxyCredentials 兼容未升级的前端：它们不会提交显式标志，
// 仍按历史约定将当前脱敏回传值视为未编辑。新前端始终提交该字段，因此不再依赖字符串相等推断意图。
func shouldPreserveProxyCredentials(req requests.HTTPProxyRequest) bool {
	if req.PreserveProxyCredentials != nil {
		return *req.PreserveProxyCredentials
	}
	return req.NormalizedHTTPProxy() != "" && req.NormalizedHTTPProxy() == validation.RedactProxyURL(models.SettingsGlobal.HttpProxy)
}

// UpdateHttpProxy 更新出站代理设置。
// @Summary 更新出站代理
// @Description 更新系统出站请求使用的代理配置，支持 http、https、socks5 和 socks5h。preserve_proxy_credentials 为 true 且端点一致时保留当前凭据
// @Tags 系统设置
// @Accept json
// @Produce json
// @Param http_proxy body string false "代理地址，支持 http、https、socks5 和 socks5h"
// @Param preserve_proxy_credentials body boolean false "端点一致时是否保留当前代理用户名和密码"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/http-proxy [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateHttpProxy(c *gin.Context) {
	var req requests.HTTPProxyRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.ValidateSave(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	httpProxy := resolveSubmittedHTTPProxy(req.NormalizedHTTPProxy(), shouldPreserveProxyCredentials(req))
	// 更新设置
	if !models.SettingsGlobal.UpdateHttpProxy(httpProxy) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新出站代理设置失败", Data: nil})
		return
	}
	github.UpdateConfig(httpProxy) // 更新 GitHub 配置
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "出站代理设置已更新", Data: nil})
}

// GetHttpProxy 获取出站代理设置。
// @Summary 获取出站代理
// @Description 获取当前生效的出站代理配置，地址中的用户名和密码已脱敏
// @Tags 系统设置
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/http-proxy [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetHttpProxy(c *gin.Context) {
	// 获取设置
	models.LoadSettings() // 确保设置已加载
	stored := models.SettingsGlobal.HttpProxy
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
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取出站代理设置成功", Data: httpProxy})
}

// TestHttpProxy 测试出站代理连接。
// @Summary 测试出站代理
// @Description 测试指定代理的连接有效性，支持 http、https、socks5 和 socks5h
// @Tags 系统设置
// @Accept json
// @Produce json
// @Param http_proxy body string true "代理地址，支持 http、https、socks5 和 socks5h"
// @Param detailed body integer false "是否返回详细测试结果，1 返回 0 不返回"
// @Param preserve_proxy_credentials body boolean false "端点一致时是否保留当前代理用户名和密码"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/test-http-proxy [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func TestHttpProxy(c *gin.Context) {
	var req requests.HTTPProxyRequest
	// 获取请求参数
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.ValidateTest(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	// 用户不修改地址直接点测试时，前端显式要求保留凭据，再用当前真实凭据拨号。
	httpProxy := resolveSubmittedHTTPProxy(req.NormalizedHTTPProxy(), shouldPreserveProxyCredentials(req))
	detailed := req.Detailed == 1

	if detailed {
		// 使用高级测试，返回详细结果
		result, err := helpers.TestHttpProxyAdvancedWithContext(c.Request.Context(), httpProxy)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "连接失败：" + err.Error(), Data: nil})
			return
		}

		if result.Success {
			c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "出站代理连接测试成功", Data: result})
		} else {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "连接失败：" + result.ErrorMessage, Data: nil})
		}
	} else {
		// 使用简单测试
		success, err := helpers.TestHttpProxyWithContext(c.Request.Context(), httpProxy)
		if err != nil {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "连接失败：" + err.Error(), Data: nil})
			return
		}

		if success {
			c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "出站代理连接测试成功", Data: nil})
		} else {
			c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "出站代理连接测试失败", Data: nil})
		}
	}
}

// TestTelegram 测试 Telegram Bot 连接。
// @Summary 测试 Telegram 连接
// @Description 测试指定 Telegram Bot 的连接有效性
// @Tags 系统设置
// @Accept json
// @Produce json
// @Param token body string true "Telegram Bot Token"
// @Param chat_id body string true "Telegram Chat ID"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /telegram/test [post]
// @Security JwtAuth
// @Security ApiKeyAuth
// func TestTelegram(c *gin.Context) {
// 	type testTelegramRequest struct {
// 		Token  string `form:"token" json:"token" binding:"required"`     // Telegram Bot 的 Token
// 		ChatId string `form:"chat_id" json:"chat_id" binding:"required"` // Telegram Chat ID
// 	}
// 	// 获取请求参数
// 	var req testTelegramRequest
// 	if err := c.ShouldBind(&req); err != nil {
// 		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
// 		return
// 	}
// 	token := req.Token
// 	chatId := req.ChatId

// 	// 数据校验
// 	if token == "" {
// 		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "Telegram Bot Token 不能为空", Data: nil})
// 		return
// 	}
// 	if chatId == "" {
// 		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "Telegram Chat ID 不能为空", Data: nil})
// 		return
// 	}

// 	// 测试 Telegram 机器人连接
// 	err := helpers.TestTelegramBot(token, chatId, models.SettingsGlobal.HttpProxy)
// 	if err != nil {
// 		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "连接失败：" + err.Error(), Data: nil})
// 		return
// 	}

// 	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "Telegram Bot 连接测试成功", Data: nil})
// }

// GetStrmConfig 获取 STRM 配置
// @Summary 获取 STRM 配置
// @Description 获取 STRM 同步相关的配置项
// @Tags 系统设置
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/strm-config [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetStrmConfig(c *gin.Context) {
	// 获取设置
	models.LoadSettings() // 确保设置已加载
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取 STRM 配置成功", Data: models.SettingsGlobal.SettingStrm.ToMap(false, true)})
}

// UpdateStrmConfig 更新 STRM 配置
// @Summary 更新 STRM 配置
// @Description 更新 STRM 同步相关的配置项（包括 URL、Cron、扩展名等）
// @Tags 系统设置
// @Accept json
// @Produce json
// @Param strm_base_url body string true "STRM 基础 URL"
// @Param cron body string true "Cron 表达式"
// @Param meta_ext body []string true "元数据扩展名"
// @Param video_ext body []string true "视频扩展名"
// @Param min_video_size body integer false "最小视频大小（MB）"
// @Param upload_meta body integer false "是否上传元数据，1 上传 0 不上传"
// @Param delete_dir body integer false "是否删除空目录，1 删除 0 不删除"
// @Param local_proxy body integer false "是否启用本地代理，1 启用 0 禁用"
// @Param exclude_name body []string false "排除的文件名"
// @Param download_meta body integer false "是否下载元数据，1 下载 0 不下载"
// @Param add_path body integer false "是否添加路径，1 添加 2 不添加"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/strm-config [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateStrmConfig(c *gin.Context) {
	// 获取请求参数
	var req requests.UpdateStrmConfigRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	modelReq := req.ToModel()
	oldCron := models.SettingsGlobal.Cron
	// 更新设置
	if !models.SettingsGlobal.UpdateStrm(modelReq) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新 STRM 配置失败", Data: nil})
		return
	}
	if oldCron != models.SettingsGlobal.Cron {
		// 如果 Cron 发生变化，重启任务
		synccron.InitCron()
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "STRM 配置已更新", Data: nil})
}

// GetCronNextTime 获取 Cron 表达式的下次执行时间。
// @Summary 获取 Cron 执行时间
// @Description 计算 Cron 表达式的下 5 次执行时间
// @Tags 系统设置
// @Accept json
// @Produce json
// @Param cron query string true "Cron 表达式"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/cron [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetCronNextTime(c *gin.Context) {
	var req requests.GetCronNextTimeRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	times := helpers.GetNextTimeByCronStr(req.NormalizedCron(), 5)
	if times == nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "仅支持 5 位 cron 表达式或 robfig 描述符", Data: nil})
		return
	}
	var timeStrs []string
	for _, t := range times {
		timeStrs = append(timeStrs, t.Format("2006-01-02 15:04:05"))
	}
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取下次执行时间成功", Data: timeStrs})
}

// ValidateCron 验证 Cron 表达式并返回描述。
// @Summary 验证 Cron 表达式
// @Description 验证 Cron 表达式的有效性并返回可读描述
// @Tags Cron
// @Accept json
// @Produce json
// @Param cron_expression body string true "Cron 表达式"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /cron/validate [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func ValidateCron(c *gin.Context) {
	var req requests.ValidateCronRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}

	// 解析 Cron 表达式为可读描述
	scrapePath := &models.ScrapePath{}
	description := scrapePath.ParseCronDescription(req.NormalizedCronExpression())

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "Cron 表达式有效", Data: map[string]string{
		"description": description,
	}})
}

// GetThreads 获取线程配置
// @Summary 获取线程数配置
// @Description 获取当前下载和文件详情查询的线程数配置
// @Tags 系统设置
// @Accept json
// @Produce json
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/threads [get]
// @Security JwtAuth
// @Security ApiKeyAuth
func GetThreads(c *gin.Context) {
	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "获取线程数成功", Data: models.SettingsGlobal.ThreadAndRapidWait()})
}

// UpdateThreads 更新线程配置
// @Summary 更新线程数配置
// @Description 更新下载和文件详情查询的线程数，115 接口速率保存后立即生效
// @Tags 系统设置
// @Accept json
// @Produce json
// @Param download_threads body integer true "下载 QPS"
// @Param file_detail_threads body integer true "115 接口 QPS"
// @Success 200 {object} object
// @Failure 200 {object} object
// @Router /setting/threads [post]
// @Security JwtAuth
// @Security ApiKeyAuth
func UpdateThreads(c *gin.Context) {
	var req requests.UpdateThreadsRequest
	if err := c.ShouldBind(&req); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: "请求参数错误：" + err.Error(), Data: nil})
		return
	}
	if err := req.Validate(); err != nil {
		c.JSON(http.StatusBadRequest, APIResponse[any]{Code: BadRequest, Message: err.Error(), Data: nil})
		return
	}
	modelReq := req.ToModel(models.SettingsGlobal.SettingUploadRapidWait, models.SettingsGlobal.SettingURLValidityCheck)
	downloadThreads := modelReq.DownloadThreads
	// 更新设置，传递当前的百度网盘限速值
	if !models.SettingsGlobal.UpdateThreads(modelReq) {
		c.JSON(http.StatusOK, APIResponse[any]{Code: BadRequest, Message: "更新线程数失败", Data: nil})
		return
	}

	// 动态更新下载队列的并发数
	models.UpdateGlobalDownloadQueueConcurrency(downloadThreads)
	// 保存成功后立即更新 115 请求队列，后续请求使用新的接口速率配置。
	fileDetailThreads := modelReq.FileDetailThreads
	setGlobalExecutorConfig(fileDetailThreads, fileDetailThreads*60, fileDetailThreads*3600)

	c.JSON(http.StatusOK, APIResponse[any]{Code: Success, Message: "更新线程数成功", Data: nil})
}
