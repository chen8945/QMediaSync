package app

import (
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"qmediasync/internal/controllers"
	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

func parseBuildUnixTime(value string) int64 {
	if value == "" {
		return 0
	}

	if timestamp, err := helpers.ParseRFC3339Unix(value); err == nil {
		return timestamp
	}

	layouts := []string{
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.Unix()
		}
	}

	return 0
}

// 设置路由
func setRouter(r *gin.Engine) {
	r.Use(controllers.RestoreMaintenanceMiddleware())
	webStatisPath := filepath.Join(helpers.RootDir, "web_statics")
	// if helpers.IsFnOS {
	// 	webStatisPath = filepath.Join(helpers.RootDir, "www")
	// }
	r.LoadHTMLFiles(filepath.Join(webStatisPath, "index.html"))
	r.StaticFile("/favicon.ico", filepath.Join(webStatisPath, "favicon.ico")) // 提供站点图标
	r.StaticFS("/assets", http.Dir(filepath.Join(webStatisPath, "assets")))   // 提供前端静态资源
	// 返回前端单页应用入口
	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.html", gin.H{})
	})
	r.POST("/emby/webhook", controllers.Webhook)                           // 接收 Emby 事件回调
	r.POST("/api/login", controllers.LoginAction)                          // 用户登录
	r.POST("/api/strm/webhook", controllers.StrmWebhook)                   // 接收外部 STRM 生成任务
	r.GET("/api/setup/status", controllers.SetupStatusAction)              // 查询首个管理员初始化状态
	r.POST("/api/setup/admin", controllers.CreateInitialAdminAction)       // 创建首个管理员
	r.GET("/api/session", controllers.SessionAction)                       // 获取当前登录会话
	r.GET("/115/url/*filename", controllers.Get115UrlByPickCode)           // 查询 115 直链，按 PickCode 查询，支持 ISO，路径最后一部分为 .扩展名格式
	r.GET("/115/newurl", controllers.Get115UrlByPickCode)                  // 查询 115 直链，按 PickCode 查询
	r.GET("/baidupan/url/*filename", controllers.GetBaiduPanUrlByPickCode) // 查询百度网盘直链，按 fs_id 查询，支持 ISO，路径最后一部分为 .扩展名格式

	r.GET("/openlist/url", controllers.GetOpenListFileUrl) // 查询 OpenList 直链

	r.GET("/proxy-115", controllers.Proxy115)                      // 115 CDN 反代路由
	r.POST("/api/update-fn-access-path", controllers.UpdateFNPath) // 更新飞牛访问路径

	// 需要 JWT 验证的 API 路由
	api := r.Group("/api")
	api.Use(controllers.JWTAuthMiddleware())
	api.GET("/strm/tasks", controllers.ListStrmResults)
	{
		api.GET("/scrape/tmp-image", controllers.ScrapeTmpImage)           // 获取临时图片
		api.GET("/scrape/records/export", controllers.ExportScrapeRecords) // 导出刮削记录
		api.GET("/logs/stream", controllers.LogStream)                     // SSE 日志查看
		api.GET("/events/stream", controllers.EventStream)                 // SSE 事件推送
		api.GET("/sync/tasks/:id/stream", controllers.SyncTaskStream)      // 同步任务详情实时流
		api.GET("/logs/old", controllers.GetOldLogs)                       // 通过 HTTP 获取旧日志
		api.GET("/logs/download", controllers.DownloadLogFile)             // 下载日志文件
		api.GET("/path/is-fn-os", controllers.IsFnOS)                      // 查询是否是飞牛环境

		// 获取应用版本与运行环境信息
		api.GET("/version", func(c *gin.Context) {
			c.JSON(http.StatusOK, map[string]any{
				"version":    build.Version,
				"build_time": parseBuildUnixTime(build.PublishDate),
				"date":       build.PublishDate,
				"isWindows":  runtime.GOOS == "windows",
				"isRelease":  helpers.IsRelease,
			})
		})
		api.POST("/database/delete-all-table", controllers.DeleteAllTabble) // 删除所有表
		api.GET("/announce", controllers.GetAnnounce)                       // 获取公告
		api.POST("/database/repair", controllers.RepairDB)                  // 修复数据库表结构和主键序列
		api.POST("/auth/115-qrcode-open", controllers.GetLoginQrCodeOpen)   // 获取 115 开放平台登录二维码
		api.POST("/auth/115-qrcode-status", controllers.GetQrCodeStatus)    // 查询 115 二维码扫码状态
		api.GET("/115/status", controllers.Get115Status)                    // 查询 115 状态
		api.GET("/115/appids", controllers.GetV115AppIDSources)             // 查询 115 开放平台 APP ID 目录
		api.GET("/115/oauth-url", controllers.GetOAuthUrl)                  // 获取 115 OAuth 登录地址
		api.POST("115/oauth-confirm", controllers.ConfirmOAuthCode)         // 确认 115 OAuth 登录
		api.GET("/115/oauth-status", controllers.GetOAuthStatus)            // 查询 115 OAuth 授权状态
		api.GET("/115/queue/stats", controllers.GetQueueStats)              // 获取 115 开放平台请求队列统计数据
		api.POST("/115/queue/rate-limit", controllers.SetQueueRateLimit)    // 设置 115 开放平台请求队列速率限制
		api.GET("/115/stats/daily", controllers.GetRequestStatsByDay)       // 获取 115 请求统计（按天）
		api.GET("/115/stats/hourly", controllers.GetRequestStatsByHour)     // 获取 115 请求统计（按小时）
		api.POST("/115/stats/clean", controllers.CleanOldRequestStats)      // 清理旧的请求统计数据
		// 百度网盘相关路由
		api.GET("/baidupan/oauth-url", controllers.GetBaiDuPanOAuthUrl)           // 获取百度网盘 OAuth 登录地址
		api.POST("/baidupan/oauth-confirm", controllers.ConfirmBaiDuPanOAuthCode) // 确认百度网盘 OAuth 登录
		api.GET("/baidupan/status", controllers.GetBaiDuPanStatus)                // 查询百度网盘状态

		api.GET("/update/last", controllers.GetLastRelease)         // 获取最新版本
		api.POST("/update/to-version", controllers.UpdateToVersion) // 获取更新版本
		api.GET("/update/progress", controllers.UpdateProgress)     // 获取更新进度
		api.POST("/update/cancel", controllers.CancelUpdate)        // 取消更新

		api.GET("/user/info", controllers.GetUserInfo)                                      // 获取当前用户信息
		api.POST("/logout", controllers.LogoutAction)                                       // 退出当前登录会话
		api.GET("/user/sessions", controllers.ListUserSessions)                             // 获取当前用户登录设备
		api.DELETE("/user/sessions/:session_id", controllers.RevokeUserSessionAction)       // 撤销指定登录设备
		api.POST("/user/sessions/revoke-others", controllers.RevokeOtherUserSessionsAction) // 撤销其他登录设备
		api.GET("/user/two-factor/status", controllers.GetTwoFactorStatus)                  // 获取两步验证状态
		api.POST("/user/two-factor/setup", controllers.SetupTwoFactor)                      // 创建两步验证配置草稿
		api.POST("/user/two-factor/enable", controllers.EnableTwoFactor)                    // 启用两步验证
		api.POST("/user/two-factor/disable", controllers.DisableTwoFactor)                  // 关闭两步验证
		api.GET("/path/sort-options", controllers.GetBrowseSortOptions)                     // 浏览排序能力
		api.GET("/path/list", controllers.GetPathList)                                      // 目录列表
		api.POST("/path/create", controllers.CreateDir)                                     // 创建目录接口
		api.DELETE("/path", controllers.DeleteDir)                                          // 删除目录接口
		api.POST("/path/delete-batch", controllers.DeleteFiles)                             // 批量删除网盘文件接口
		api.POST("/path/move", controllers.MoveFiles)                                       // 批量移动网盘文件接口
		api.POST("/path/copy", controllers.CopyFiles)                                       // 批量复制网盘文件接口
		api.POST("/path/rename", controllers.RenameFile)                                    // 重命名网盘文件接口
		api.GET("/path/files", controllers.GetNetFileList)                                  // 查询网盘文件列表
		api.POST("/user/change", controllers.ChangePassword)                                // 修改当前用户密码

		api.POST("/setting/http-proxy", controllers.UpdateHttpProxy)    // 更改 HTTP 代理
		api.GET("/setting/http-proxy", controllers.GetHttpProxy)        // 获取 HTTP 代理
		api.POST("/setting/test-http-proxy", controllers.TestHttpProxy) // 测试 HTTP 代理
		api.GET("/setting/log", controllers.GetLogSetting)              // 获取日志设置
		api.POST("/setting/log", controllers.UpdateLogSetting)          // 更新日志设置
		// api.GET("/setting/telegram", controllers.GetTelegram)                                      // 获取 Telegram 消息通知配置
		// api.POST("/setting/telegram", controllers.UpdateTelegram)                                  // 更改 Telegram 消息通知配置
		// api.POST("/telegram/test", controllers.TestTelegram)                                       // 测试 Telegram 连通性
		api.GET("/setting/notification/channels", controllers.GetNotificationChannels)             // 获取所有通知渠道
		api.POST("/setting/notification/channels/telegram", controllers.CreateTelegramChannel)     // 创建 Telegram 渠道
		api.GET("/setting/notification/channels/telegram/:id", controllers.GetTelegramChannel)     // 查询 Telegram 渠道
		api.PUT("/setting/notification/channels/telegram", controllers.UpdateTelegramChannel)      // 更新 Telegram 渠道
		api.POST("/setting/notification/channels/meow", controllers.CreateMeoWChannel)             // 创建 MeoW 渠道
		api.GET("/setting/notification/channels/meow/:id", controllers.GetMeoWChannel)             // 查询 MeoW 渠道
		api.PUT("/setting/notification/channels/meow", controllers.UpdateMeoWChannel)              // 更新 MeoW 渠道
		api.POST("/setting/notification/channels/bark", controllers.CreateBarkChannel)             // 创建 Bark 渠道
		api.GET("/setting/notification/channels/bark/:id", controllers.GetBarkChannel)             // 查询 Bark 渠道
		api.PUT("/setting/notification/channels/bark", controllers.UpdateBarkChannel)              // 更新 Bark 渠道
		api.POST("/setting/notification/channels/serverchan", controllers.CreateServerChanChannel) // 创建 Server 酱渠道
		api.GET("/setting/notification/channels/serverchan/:id", controllers.GetServerChanChannel) // 查询 Server 酱渠道
		api.PUT("/setting/notification/channels/serverchan", controllers.UpdateServerChanChannel)  // 更新 Server 酱渠道
		api.POST("/setting/notification/channels/webhook", controllers.CreateCustomWebhookChannel) // 创建自定义 Webhook 渠道
		api.GET("/setting/notification/channels/webhook/:id", controllers.GetCustomWebhookChannel) // 查询自定义 Webhook 渠道
		api.PUT("/setting/notification/channels/webhook", controllers.UpdateCustomWebhookChannel)  // 更新自定义 Webhook 渠道
		api.POST("/setting/notification/channels/status", controllers.UpdateChannelStatus)         // 启用/禁用渠道
		api.DELETE("/setting/notification/channels/:id", controllers.DeleteChannel)                // 删除渠道
		api.GET("/setting/notification/rules", controllers.GetNotificationRules)                   // 获取通知规则
		api.PUT("/setting/notification/rules", controllers.UpdateNotificationRule)                 // 更新通知规则
		api.POST("/setting/notification/channels/test", controllers.TestChannelConnection)         // 测试通知渠道连接
		api.GET("/setting/strm-config", controllers.GetStrmConfig)                                 // 获取 STRM 配置
		api.POST("/setting/strm-config", controllers.UpdateStrmConfig)                             // 更新 STRM 配置
		api.GET("/setting/cron", controllers.GetCronNextTime)                                      // 获取 Cron 表达式的下 5 次执行时间
		api.POST("/cron/validate", controllers.ValidateCron)                                       // 验证 Cron 表达式并返回描述
		api.POST("/setting/emby/parse", controllers.ParseEmby)                                     // 解析 Emby 媒体信息
		api.GET("/setting/emby-config", controllers.GetEmbyConfig)                                 // 获取新的 Emby 配置
		api.POST("/setting/emby-config", controllers.UpdateEmbyConfig)                             // 更新新的 Emby 配置
		api.POST("/setting/threads", controllers.UpdateThreads)                                    // 更新线程数
		api.GET("/setting/threads", controllers.GetThreads)                                        // 获取线程数

		api.POST("/emby/sync/start", controllers.StartEmbySync)     // 手动启动 Emby 同步
		api.GET("/emby/sync/status", controllers.GetEmbySyncStatus) // 获取 Emby 同步状态
		api.GET("/emby/libraries", controllers.GetEmbyLibraries)    // 获取 Emby 媒体库列表
		// 删除媒体库与同步目录关联

		api.POST("/sync/start", controllers.StartSync)                       // 启动同步
		api.GET("/sync/records", controllers.GetSyncRecords)                 // 同步列表
		api.GET("/sync/task", controllers.GetSyncTask)                       // 获取同步任务详情
		api.GET("/sync/path-list", controllers.GetSyncPathList)              // 获取同步路径列表
		api.POST("/sync/paths", controllers.CreateSyncPathAggregate)         // 原子创建同步路径
		api.PUT("/sync/paths/:id", controllers.UpdateSyncPathAggregate)      // 原子更新同步路径
		api.POST("/sync/path-delete", controllers.DeleteSyncPath)            // 删除同步路径
		api.POST("/sync/path/stop", controllers.StopSyncByPath)              // 停止同步路径的同步任务
		api.POST("/sync/path/start", controllers.StartSyncByPath)            // 启动同步路径的同步任务
		api.POST("/sync/path/full-start", controllers.FullStart115Sync)      // 启动 115 的全量同步任务
		api.POST("/sync/delete-records", controllers.DelSyncRecords)         // 批量删除同步记录
		api.POST("/sync/path/toggle-cron", controllers.ToggleSyncByPath)     // 关闭或开启同步目录的定时同步
		api.GET("/sync/path/:id", controllers.GetSyncPathById)               // 获取同步路径详情
		api.GET("/sync/path/:id/scrape-paths", controllers.GetRelScrapePath) // 获取同步路径关联的刮削路径
		api.POST("/sync/path/scrape-paths", controllers.SaveRelScrapePath)   // 更新同步路径关联的刮削路径
		api.POST("/sync/manual", controllers.ManualSync)                     // 手动同步

		api.GET("/directory-upload/rules", controllers.ListDirectoryUploadRules)                                  // 获取目录监控上传规则
		api.POST("/directory-upload/sync-paths/:sync_path_id/scan", controllers.ScanDirectoryUploadSyncPathRules) // 手动触发目录监控补偿扫描
		api.GET("/directory-upload/runtime-status", controllers.GetDirectoryUploadRuntimeStatuses)                // 获取目录监控运行状态

		api.GET("/account/list", controllers.GetAccountList)                                // 获取开放平台账号列表
		api.POST("/account/add", controllers.CreateTmpAccount)                              // 创建开放平台账号
		api.POST("/account/authorization/prepare", controllers.PrepareAccountAuthorization) // 准备更换账号授权
		api.POST("/account/authorization/cancel", controllers.CancelAccountAuthorization)   // 取消更换账号授权
		api.POST("/account/update", controllers.UpdateAccountInfo)                          // 更新开放平台账号资料
		api.POST("/account/delete", controllers.DeleteAccount)                              // 删除开放平台账号
		api.POST("/account/openlist", controllers.CreateOpenListAccount)                    // 创建 OpenList 账号

		// API Key 管理接口
		api.POST("/api-keys", controllers.CreateAPIKey)                 // 创建 API Key
		api.GET("/api-keys", controllers.ListAPIKeys)                   // 获取 API Key 列表
		api.PUT("/api-keys/:id/status", controllers.UpdateAPIKeyStatus) // 更新 API Key 状态
		api.DELETE("/api-keys/:id", controllers.DeleteAPIKey)           // 删除 API Key

		api.GET("/scrape/movie-genre", controllers.GetMovieGenre)                     // 获取电影类别
		api.GET("/scrape/tvshow-genre", controllers.GetTvshowGenre)                   // 获取电视剧类别
		api.GET("/scrape/language", controllers.GetLanguage)                          // 获取语言数组
		api.GET("/scrape/countries", controllers.GetCountries)                        // 获取国家数组
		api.GET("/scrape/tmdb", controllers.GetTmdbSettings)                          // 获取 TMDB 设置
		api.POST("/scrape/tmdb", controllers.SaveTmdbSettings)                        // 保存 TMDB 设置
		api.POST("/scrape/tmdb-test", controllers.TestTmdbSettings)                   // 测试 TMDB 设置
		api.GET("/scrape/ai-settings", controllers.GetAiSettings)                     // 获取 AI 识别设置
		api.POST("/scrape/ai-settings", controllers.SaveAiSettings)                   // 保存 AI 识别设置
		api.POST("/scrape/ai-test", controllers.TestAiSettings)                       // 测试 AI 识别设置
		api.GET("/scrape/movie-categories", controllers.GetMovieCategories)           // 获取电影分类列表
		api.GET("/scrape/tvshow-categories", controllers.GetTvshowCategories)         // 获取电视剧分类列表
		api.POST("/scrape/movie-categories", controllers.SaveMovieCategory)           // 保存电影分类
		api.POST("/scrape/tvshow-categories", controllers.SaveTvshowCategory)         // 保存电视剧分类
		api.DELETE("/scrape/movie-categories/:id", controllers.DeleteMovieCategory)   // 删除电影分类
		api.DELETE("/scrape/tvshow-categories/:id", controllers.DeleteTvshowCategory) // 删除电视剧分类
		api.GET("/scrape/pathes", controllers.GetScrapePathes)                        // 获取刮削路径列表
		api.POST("/scrape/pathes", controllers.SaveScrapePath)                        // 保存刮削路径列表
		api.DELETE("/scrape/pathes/:id", controllers.DeleteScrapePath)                // 删除刮削路径
		api.GET("/scrape/pathes/:id", controllers.GetScrapePath)                      // 获取刮削路径详情
		api.POST("/scrape/pathes/start", controllers.ScanScrapePath)                  // 扫描刮削路径
		api.POST("/scrape/pathes/stop", controllers.StopScrape)                       // 停止刮削任务
		api.POST("/scrape/pathes/toggle-cron", controllers.ToggleScrapePathCron)      // 关闭或开启刮削路径的定时刮削
		api.GET("/scrape/records", controllers.GetScrapeRecords)                      // 获取刮削记录
		api.POST("/scrape/re-scrape", controllers.ReScrape)                           // 重新刮削记录
		api.POST("/scrape/clear-failed", controllers.ClearFailedScrapeRecords)        // 清除所有刮削失败的记录
		api.POST("/scrape/truncate-all", controllers.TruncateAllScrapeRecords)        // 一键清空所有刮削记录
		api.DELETE("/scrape/records", controllers.DeleteScrapeMediaFile)              // 删除刮削记录
		api.POST("/scrape/finish", controllers.FinishScrapeMediaFile)                 // 完成刮削记录
		api.POST("/scrape/rename-failed", controllers.RenameFailedScrapeMediaFile)    // 标记所有失败的记录为待整理
		api.POST("/scrape/sync-pathes", controllers.SaveScrapeStrmPath)               // 保存刮削目录关联的同步目录
		api.GET("/scrape/sync-pathes", controllers.GetScrapeStrmPaths)                // 获取刮削目录关联的同步目录
		api.GET("/scrape/tmdb-search", controllers.TmdbSearch)                        // 搜索 TMDB 媒体

		api.GET("/upload/queue", controllers.UploadList)                                             // 获取上传队列列表
		api.POST("/upload/queue/clear-pending", controllers.ClearPendingUploadTasks)                 // 清除上传队列中未开始的任务
		api.POST("/upload/queue/start", controllers.StartUploadQueue)                                // 启动上传队列
		api.POST("/upload/queue/stop", controllers.StopUploadQueue)                                  // 停止上传队列
		api.GET("/upload/queue/status", controllers.UploadQueueStatus)                               // 查询上传队列状态
		api.POST("/upload/queue/clear-success-failed", controllers.ClearUploadSuccessAndFailedTasks) // 清除上传队列中已完成和失败的任务
		api.POST("/upload/queue/retry-failed", controllers.RetryFailedUploadTasks)                   // 重试所有失败的上传任务

		api.GET("/download/queue", controllers.DownloadList)                                             // 获取下载队列列表
		api.POST("/download/queue/clear-pending", controllers.ClearPendingDownloadTasks)                 // 清除下载队列中未开始的任务
		api.POST("/download/queue/start", controllers.StartDownloadQueue)                                // 启动下载队列
		api.POST("/download/queue/stop", controllers.StopDownloadQueue)                                  // 停止下载队列
		api.GET("/download/queue/status", controllers.DownloadQueueStatus)                               // 查询下载队列状态
		api.POST("/download/queue/clear-success-failed", controllers.ClearDownloadSuccessAndFailedTasks) // 清除下载队列中已完成和失败的任务
		api.POST("/download/queue/retry-failed", controllers.RetryFailedDownloadTasks)                   // 重试所有失败的下载任务

		// 备份与恢复相关路由
		api.GET("/backup/list", controllers.GetBackupList)               // 获取备份列表
		api.GET("/backup/files", controllers.GetBackupFiles)             // 获取本地备份文件列表
		api.GET("/backup/records/:id", controllers.GetBackupRecord)      // 获取备份记录详情
		api.POST("/backup/create", controllers.CreateBackup)             // 创建手动备份
		api.DELETE("/backup/records/:id", controllers.DeleteBackup)      // 删除备份记录
		api.POST("/backup/restore", controllers.RestoreFromBackup)       // 从备份恢复
		api.POST("/backup/upload-restore", controllers.UploadAndRestore) // 上传文件并恢复
		api.GET("/backup/download/:id", controllers.DownloadBackup)      // 下载备份文件
		api.GET("/backup/config", controllers.GetBackupConfig)           // 获取备份配置
		api.PUT("/backup/config", controllers.UpdateBackupConfig)        // 更新备份配置
		api.GET("/backup/status", controllers.GetBackupStatus)           // 获取备份状态

	}
}
