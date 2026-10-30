package app

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/controllers"
	"qmediasync/internal/db"
	"qmediasync/internal/db/database"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/emby"
	"qmediasync/internal/github"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/synccron"
	"qmediasync/internal/syncstrm"
	v115open "qmediasync/internal/v115/openapi"
)

var requestStatWriter *models.RequestStatWriter

func (app *App) StartDatabase() error {
	if err := helpers.GlobalConfig.Db.Validate(); err != nil {
		return err
	}
	// 根据配置启动数据库连接
	if helpers.GlobalConfig.Db.Engine == helpers.DbEngineSqlite {
		// 如果是 SQLite，直接初始化 SQLite 连接
		sqliteFile := filepath.Join(helpers.ConfigDir, helpers.GlobalConfig.Db.SqliteFile)
		helpers.AppLogger.Infof("SQLite 数据库文件路径：%s", sqliteFile)
		db.Db = db.InitSqlite3(sqliteFile)
		if err := models.Migrate(); err != nil {
			return err
		}
		if err := models.ResetStaleEmbySyncRunOnStartup(); err != nil {
			return err
		}
		return nil
	}

	// 初始化数据库配置
	dbConfig := &database.Config{
		Host:         helpers.GlobalConfig.Db.PostgresConfig.Host,
		Port:         helpers.GlobalConfig.Db.PostgresConfig.Port,
		User:         helpers.GlobalConfig.Db.PostgresConfig.User,
		Password:     helpers.GlobalConfig.Db.PostgresConfig.Password,
		DBName:       helpers.GlobalConfig.Db.PostgresConfig.Database,
		SSLMode:      "disable",
		MaxOpenConns: helpers.GlobalConfig.Db.PostgresConfig.MaxOpenConns,
		MaxIdleConns: helpers.GlobalConfig.Db.PostgresConfig.MaxIdleConns,
	}
	if helpers.GlobalConfig.Db.PostgresConfig.SSL {
		dbConfig.SSLMode = "require"
	}
	if err := db.ConnectPostgres(dbConfig); err != nil {
		return err
	}
	if err := models.Migrate(); err != nil {
		return err
	}
	if err := models.ResetStaleEmbySyncRunOnStartup(); err != nil {
		return err
	}
	return nil
}

func configureInitialAdminSetup() error {
	hasUser, err := models.HasAnyUser()
	if err != nil {
		return err
	}
	token, err := controllers.ConfigureInitialSetup(!hasUser)
	if err != nil {
		return err
	}
	if token != "" {
		helpers.AppLogger.RequiredWarnf(
			"检测到系统尚未创建管理员，请使用以下初始化码完成首次管理员创建：%s",
			token,
		)
		helpers.AppLogger.RequiredWarnf("初始化码只会在本次启动日志中显示，创建管理员成功后立即失效")
	}
	return nil
}

func initTimeZone() {
	cstZone := time.FixedZone("CST", 8*3600)
	time.Local = cstZone
}

func initLogger() {
	logPath := filepath.Join(helpers.ConfigDir, "logs")
	os.MkdirAll(logPath, 0755) // 如果没有 logs 目录则创建
	syncLogPath := filepath.Join(helpers.ConfigDir, helpers.SyncLogDir())
	os.MkdirAll(syncLogPath, 0755) // 如果没有同步任务日志目录则创建
	logConfig := helpers.LogConfigSnapshot()
	helpers.AppLogger = helpers.NewLogger(logConfig.App, true, true)
	helpers.V115Log = helpers.NewLogger(logConfig.V115, false, true)
	helpers.OpenListLog = helpers.NewLogger(logConfig.OpenList, false, true)
	helpers.TMDBLog = helpers.NewLogger(logConfig.TMDB, false, true)
	helpers.BaiduPanLog = helpers.NewLogger(logConfig.BaiduPan, false, true)
	helpers.WarnUnsafeSensitiveLogIfEnabled()
}

func initOthers() {
	helpers.InitEventBus() // 初始化事件总线
	models.LoadSettings()  // 从数据库加载设置
	// 初始化 GitHub 访问管理器
	github.InitManager(models.SettingsGlobal.HttpProxy)
	helpers.AppLogger.Infof("已加载配置，准备初始化 115 请求队列，线程数：%d", models.SettingsGlobal.FileDetailThreads)
	qps := models.SettingsGlobal.FileDetailThreads
	if qps <= 0 {
		qps = 2
	}
	v115open.SetGlobalExecutorConfig(qps, qps*60, qps*3600)
	models.LoadScrapeSettings()          // 从数据库加载刮削设置
	models.InitDQ()                      // 初始化下载队列
	models.InitUQ()                      // 初始化上传队列
	models.InitNotificationManager()     // 初始化通知管理器
	controllers.StartListenTelegramBot() // 初始化 Telegram Bot 监听
	models.GetEmbyConfig()               // 加载 Emby 配置
	helpers.SubscribeSync(helpers.V115TokenInValidEvent, models.HandleV115TokenInvalid)
	helpers.SubscribeSync(helpers.SaveOpenListTokenEvent, models.HandleOpenListTokenSaveSync)
	models.FailAllRunningSyncTasks() // 将所有运行中的同步任务设置为失败状态
	ledgerCtx, cancelLedger := context.WithTimeout(context.Background(), 10*time.Second)
	if err := syncstrm.InitSyncBackgroundService(ledgerCtx); err != nil {
		helpers.AppLogger.Errorf("标记上次未结束的 STRM 同步文件记录更新任务失败：%v", err)
	}
	cancelLedger()

	// 设置 115 请求队列的非阻塞统计写入回调。
	requestStatWriter = models.NewRequestStatWriter()
	v115open.SetGlobalExecutorStatSaver(requestStatWriter.Enqueue)
	synccron.RefreshOAuthAccessToken() // 启动时刷新一次 115 的访问凭证，避免过期 Token 导致同步失败

	// 启动同步任务队列管理器
	synccron.InitNewSyncQueueManager()
	models.InitEmbyLibraryRefreshCoordinator()
	syncstrm.InitStrmGenerationWorker()
	directoryupload.InitDirectoryUploadService()
	// 初始化实时事件中心
	realtime.GlobalEventHub = realtime.NewEventHub()
	realtime.GlobalSyncTaskHub = realtime.NewSyncTaskHub()
	synccron.InitCron()       // 初始化定时任务（包含备份定时任务）
	synccron.InitSyncCron()   // 初始化同步目录的定时任务
	synccron.InitScrapeCron() // 初始化刮削目录的自定义定时任务
	synccron.InitTokenCron()  // 初始化定时刷新 115 的访问凭证
	// 初始化备份服务
	models.InitBackupService()
	// 将所有刮削中和整理中的记录改为未执行
	models.ResetScrapePathStatus()
	// 将所有刮削中改为待刮削
	models.UpdateScrapeMediaStatus(models.ScrapeMediaStatusScraping, models.ScrapeMediaStatusScanned, 0)
	// 将所有整理中的记录改为待整理
	models.UpdateScrapeMediaStatus(models.ScrapeMediaStatusRenaming, models.ScrapeMediaStatusScraped, 0)
	// 上传中的任务改为待上传
	models.UpdateUploadingToPending()
	// 下载中的任务改为待下载
	models.UpdateDownloadingToPending()
	helpers.Subscribe(helpers.BackupCronEevent, func(event helpers.Event) {
		backup.Backup(models.BackupTypeAuto, "定时备份")
	})
	helpers.Subscribe(helpers.StrmSyncCompleteEvent, func(event helpers.Event) {
		// 触发关联的刮削任务
		scrapePathIds := event.Data.([]uint)
		// 将任务添加到队列中
		for _, scrapePathId := range scrapePathIds {
			scrapePath := models.GetScrapePathByID(scrapePathId)
			if scrapePath == nil {
				helpers.AppLogger.Errorf("获取刮削目录失败：%v", scrapePathId)
				continue
			}
			taskObj := &synccron.NewSyncTask{
				ID:           scrapePathId,
				SourcePath:   "",
				SourcePathId: "",
				TargetPath:   "",
				AccountId:    scrapePath.AccountId,
				SourceType:   scrapePath.SourceType,
				IsFile:       false,
				TaskType:     synccron.SyncTaskTypeScrape,
			}
			if err := synccron.AddNewSyncTask(taskObj); err != nil {
				helpers.AppLogger.Errorf("添加刮削任务失败：%v", err)
			} else {
				helpers.AppLogger.Infof("创建刮削任务成功并已添加到执行队列，刮削目录 ID：%d", scrapePathId)
			}
		}
	})
}

// firstNonEmpty 返回参数中第一个非空字符串，用于按优先级取环境变量/默认值。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func initEnv() bool {
	log.Printf("当前版本号：%s，发布日期：%s\n", build.Version, build.PublishDate)
	// 将版本写入 helper
	helpers.Version = build.Version
	helpers.ReleaseDate = build.PublishDate
	// 加载环境变量配置
	helpers.LoadEnvFromFile(filepath.Join(helpers.RootDir, "config", ".env"))
	// 取值优先级：环境变量（config/.env 已经过上面的 LoadEnvFromFile 覆盖真实 env） > 编译期 ldflags 注入值。
	helpers.DEFAULT_SC_API_KEY = firstNonEmpty(os.Getenv("SC_API_KEY"), build.SCAPIKey)
	helpers.DEFAULT_TMDB_API_KEY = firstNonEmpty(os.Getenv("TMDB_API_KEY"), build.TMDBAPIKey)
	helpers.DEFAULT_TMDB_ACCESS_TOKEN = firstNonEmpty(os.Getenv("TMDB_ACCESS_TOKEN"), build.TMDBAccessToken)
	// FANART_API_KEY：DEFAULT_FANART_API_KEY 保存“环境变量 > ldflags”的默认基线，
	// 生效值先取默认基线，待加载刮削设置后再由 ScrapeSettings.ApplyKeyOverrides 按“UI 配置 > 默认”刷新。
	helpers.DEFAULT_FANART_API_KEY = firstNonEmpty(os.Getenv("FANART_API_KEY"), build.FanartAPIKey)
	helpers.FANART_API_KEY = helpers.DEFAULT_FANART_API_KEY
	helpers.OAuthRelayEncryptionKey = firstNonEmpty(os.Getenv("OAUTH_RELAY_ENCRYPTION_KEY"), build.OAuthRelayEncryptionKey)
	initTimeZone() // 设置东 8 区
	if err := getDataAndConfigDir(); err != nil {
		log.Printf("初始化配置目录失败：%v", err)
		return false
	}
	log.Printf("当前工作目录：%s\n", helpers.RootDir)
	log.Printf("当前配置文件目录：%s\n", helpers.ConfigDir)
	if err := checkLegacyDatabaseState(); err != nil {
		log.Printf("数据库状态不支持：%v", err)
		return false
	}
	if err := backup.CleanupTemporaryFiles(helpers.ConfigDir); err != nil {
		log.Printf("清理备份临时文件失败，保留未清理项：%v", err)
	}
	ipv4, _ := helpers.GetLocalIP()
	log.Printf("本机 IPv4 地址：%s\n", ipv4)
	// 检查配置文件是否存在
	helpers.IsFirstRun = !helpers.HasConfigFile()
	// 如果不存在，启动一个简易 Web 服务来配置数据库连接信息
	if helpers.IsFirstRun {
		log.Printf("配置文件不存在，启动简单配置服务：%s", helpers.ConfigFilePath())
		StartConfigWebServer()
		return false
	}
	configPath := helpers.ExistingConfigFilePath()
	log.Printf("配置文件存在，加载配置文件：%s", configPath)
	// 如果存在，则加载配置文件，进行其他的初始化工作
	err := helpers.InitConfig()
	if err != nil {
		log.Printf("初始化配置文件失败：%v", err)
		return false
	}
	if err := helpers.InitEncryptionKey(); err != nil {
		log.Printf("初始化本机加密密钥失败：%v\n", err)
		return false
	}
	initLogger()
	// 创建 App
	newApp()
	helpers.AppLogger.Infof("当前版本号：%s，发布日期：%s", build.Version, build.PublishDate)

	if err := QMSApp.StartDatabase(); err != nil {
		helpers.AppLogger.Errorf("数据库启动失败：%v", err)
		return false
	}
	// 后台任务和 Cron 启动前归并旧进程的备份状态，避免误改本次运行的任务。
	if err := models.ReconcileInterruptedBackups(); err != nil {
		helpers.AppLogger.Errorf("更新上次未结束的备份状态失败：%v", err)
		return false
	}

	if err := configureInitialAdminSetup(); err != nil {
		helpers.AppLogger.Errorf("初始化管理员创建状态失败：%v", err)
		return false
	}

	db.InitCache() // 初始化内存缓存
	initOthers()
	// 托盘和 HTTP 启动前完成恢复，失败时清理已启动的队列并交给主流程退出。
	if err := emby.StartWebhookWorker(); err != nil {
		helpers.AppLogger.Errorf("启动 Emby 通知后台处理失败：%v", err)
		QMSApp.Stop()
		return false
	}
	return true
}
