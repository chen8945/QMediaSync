package synccron

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/scrape"
	"qmediasync/internal/taskgate"
	"qmediasync/internal/v115open"

	"github.com/robfig/cron/v3"
)

const syncRecordRetentionDays = 7

var GlobalCron *cron.Cron
var SyncCron *cron.Cron
var ScrapeCron *cron.Cron
var TokenCron *cron.Cron

var tokenRefreshRunning int32 = 0

func selectScheduledEmbySyncMode(config *models.EmbyConfig, now time.Time) string {
	if config == nil || config.EnableDailyFirstFullSync != 1 {
		return models.EmbySyncModeIncremental
	}
	if config.LastFullSyncAt <= 0 {
		return models.EmbySyncModeFull
	}

	loc := now.Location()
	lastFullSyncAt := time.Unix(config.LastFullSyncAt, 0).In(loc)
	now = now.In(loc)
	nowYear, nowMonth, nowDay := now.Date()
	lastYear, lastMonth, lastDay := lastFullSyncAt.Date()
	if nowYear == lastYear && nowMonth == lastMonth && nowDay == lastDay {
		return models.EmbySyncModeIncremental
	}
	return models.EmbySyncModeFull
}

func StartSyncCron() {
	if taskgate.IsBlocked() {
		return
	}
	// 查询所有同步目录
	syncPaths, _ := models.GetSyncPathList(1, 10000000, true, "")
	if len(syncPaths) == 0 {
		// helpers.AppLogger.Info("没有找到同步目录")
		return
	}
	for _, syncPath := range syncPaths {
		// 没开启定时任务，或已配置自定义 Cron 表达式的同步目录跳过
		if syncPath.SettingStrm.Cron != "" {
			helpers.AppLogger.Infof("同步目录 %d 已启用自定义定时任务，Cron 表达式：%s", syncPath.ID, syncPath.SettingStrm.Cron)
			continue
		}
		// 将同步目录 ID 添加到处理队列，而不是直接执行
		taskObj := &NewSyncTask{
			ID:           syncPath.ID,
			SourcePath:   "",
			SourcePathId: "",
			TargetPath:   "",
			AccountId:    syncPath.AccountId,
			IsFile:       false,
			TaskType:     SyncTaskTypeStrm,
			SourceType:   syncPath.SourceType,
		}
		if err := AddNewSyncTask(taskObj); err != nil {
			helpers.AppLogger.Errorf("将同步任务添加到队列失败：%s", err.Error())
			continue
		}
	}
}

// 开始刮削整理任务（通用定时任务）
func startScrapeCron() {
	if taskgate.IsBlocked() {
		return
	}
	// 查询所有刮削目录
	scrapePaths := models.GetScrapePathes("")
	if len(scrapePaths) == 0 {
		// helpers.AppLogger.Info("没有找到刮削目录")
		return
	}
	for _, scrapePath := range scrapePaths {
		// 未启用定时任务的跳过
		if !scrapePath.EnableCron {
			continue
		}
		// 如果自定义了 cron 字段，则不走通用定时任务
		if scrapePath.CronExpression != "" {
			helpers.AppLogger.Infof("刮削目录 %d 已启用自定义定时任务，Cron 表达式：%s，跳过通用定时任务", scrapePath.ID, scrapePath.CronExpression)
			continue
		}
		// 将刮削目录 ID 添加到处理队列，而不是直接执行
		taskObj := &NewSyncTask{
			ID:           scrapePath.ID,
			SourcePath:   "",
			SourcePathId: "",
			TargetPath:   "",
			AccountId:    scrapePath.AccountId,
			IsFile:       false,
			TaskType:     SyncTaskTypeScrape,
			SourceType:   scrapePath.SourceType,
		}
		if err := AddNewSyncTask(taskObj); err != nil {
			helpers.AppLogger.Errorf("将刮削任务添加到队列失败：%s", err.Error())
			continue
		} else {
			helpers.AppLogger.Infof("刮削任务已创建并加入执行队列，刮削目录 ID：%d，刮削目录：%s，目标目录：%s", scrapePath.ID, scrapePath.SourcePath, scrapePath.DestPath)
		}
	}
}

func RefreshOAuthAccessToken() {
	if taskgate.IsBlocked() {
		return
	}
	// 检查是否已在运行，防止并发执行
	if !atomic.CompareAndSwapInt32(&tokenRefreshRunning, 0, 1) {
		helpers.AppLogger.Warn("Token 刷新任务已在运行，跳过本次执行")
		return
	}

	// 使用 defer 确保函数结束时释放锁
	defer atomic.StoreInt32(&tokenRefreshRunning, 0)

	// 刷新 115 的访问凭证
	// 取所有 115 类型的账号
	accounts, _ := models.GetAllAccount()
	now := time.Now().Unix()
	for _, account := range accounts {
		if account.RefreshToken == "" {
			helpers.AppLogger.Infof("账号 %d 没有刷新 Token，跳过", account.ID)
			continue
		}
		if account.SourceType == models.SourceType115 {
			// helpers.AppLogger.Infof("当前时间：%d, 过期时间：%d", now, account.TokenExpiriesTime-3600)
			if account.TokenExpiriesTime-1800 > now {
				// helpers.AppLogger.Infof("115 账号 Token 未过期，账号 ID：%d，115 用户名：%s，过期时间：%s", account.ID, account.Username, time.Unix(account.TokenExpiriesTime-1800, 0).Format("2006-01-02 15:04:05"))
				continue
			}
			helpers.AppLogger.Infof("开始刷新 115 账号 Token，账号 ID：%d，115 用户名：%s", account.ID, account.Username)
			// 刷新 115 的访问凭证
			client := account.Get115Client()
			tokenData, err := client.RefreshToken(account.RefreshToken)
			if err != nil {
				helpers.AppLogger.Errorf("刷新 115 访问凭证失败：%s", err.Error())
				// 清空 Token
				account.ClearToken(err.Error())
				ctx := context.Background()
				notif := &models.Notification{
					Type:      models.SystemAlert,
					Title:     "🔐 115 开放平台访问凭证已失效",
					Content:   fmt.Sprintf("账号 ID：%d\n用户名：%s\n请重新授权\n⏰ 时间：%s", int(account.ID), account.Username, time.Now().Format("2006-01-02 15:04:05")),
					Timestamp: time.Now(),
					Priority:  models.HighPriority,
				}
				if notificationmanager.GlobalEnhancedNotificationManager != nil {
					if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif); err != nil {
						helpers.AppLogger.Errorf("发送访问凭证失效通知失败：%v", err)
					}
				}
				continue
			}
			// 更新账号的 Token
			if suc := account.UpdateToken(tokenData.AccessToken, tokenData.RefreshToken, tokenData.ExpiresIn); !suc {
				helpers.AppLogger.Errorf("更新 115 账号 Token 失败")
				continue
			}
			// 更新其他客户端的 Token
			v115open.UpdateToken(account.ID, tokenData.AccessToken, tokenData.RefreshToken)
			// 刷新成功，更新账号的 Token
			helpers.AppLogger.Infof("刷新 115 账号 Token 成功，账号 ID：%d，新到期时间：%d => %s", account.ID, tokenData.ExpiresIn, time.Unix(account.TokenExpiriesTime, 0).Format("2006-01-02 15:04:05"))
			continue
		}
		if account.SourceType == models.SourceTypeBaiduPan {
			// 刷新百度网盘的访问凭证
			if account.TokenExpiriesTime-86400 > now {
				// helpers.AppLogger.Infof("百度网盘账号 Token 未过期，账号 ID：%d，百度网盘用户名：%s，过期时间：%s", account.ID, account.Username, time.Unix(account.TokenExpiriesTime-86400, 0).Format("2006-01-02 15:04:05"))
				continue
			}
			// 向授权服务器发送刷新请求，拿到新 Token
			resp, err := baidupan.RefreshToken(account.ID, account.RefreshToken)
			if err != nil {
				helpers.AppLogger.Errorf("刷新百度网盘 Token 失败：%s", err.Error())
				// 清空 Token
				account.ClearToken(err.Error())
				ctx := context.Background()
				notif := &models.Notification{
					Type:      models.SystemAlert,
					Title:     "🔐 百度网盘开放平台访问凭证已失效",
					Content:   fmt.Sprintf("账号 ID：%d\n用户名：%s\n请重新授权\n⏰ 时间：%s", int(account.ID), account.Username, time.Now().Format("2006-01-02 15:04:05")),
					Timestamp: time.Now(),
					Priority:  models.HighPriority,
				}
				if notificationmanager.GlobalEnhancedNotificationManager != nil {
					if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif); err != nil {
						helpers.AppLogger.Errorf("发送访问凭证失效通知失败：%v", err)
					}
				}
				continue
			}
			// 更新账号的 Token
			if suc := account.UpdateToken(resp.AccessToken, resp.RefreshToken, resp.ExpiresIn); !suc {
				helpers.AppLogger.Errorf("更新百度网盘账号 Token 失败")
				continue
			}
			// 更新其他客户端的 Token
			baidupan.UpdateToken(account.ID, resp.AccessToken)
			// 刷新成功，更新账号的 Token
			helpers.AppLogger.Infof("刷新百度网盘账号 Token 成功，账号 ID：%d，新到期时间：%d => %s", account.ID, resp.ExpiresIn, time.Unix(resp.ExpiresIn, 0).Format("2006-01-02 15:04:05"))
			continue
		}
	}
}

func startClearDownloadUploadTasks() {
	helpers.AppLogger.Info("开始清除 3 天前的上传任务")
	models.ClearExpireUploadTasks()
	helpers.AppLogger.Info("开始清除 3 天前的下载任务")
	models.ClearExpireDownloadTasks()
}

var RollBackCronStart bool = false

func StartScrapeRollbackCron() {
	if RollBackCronStart {
		helpers.AppLogger.Info("刮削回滚任务已在运行")
		return
	}
	RollBackCronStart = true
	defer func() {
		RollBackCronStart = false
	}()
	go func() {
		limit := 10
		offset := 0
		for {
			// 从数据库中获取所有状态为回滚中的记录
			var mediaFiles []*models.ScrapeMediaFile
			err := db.Db.Where("status = ?", models.ScrapeMediaStatusRollbacking).Limit(limit).Offset(offset).Find(&mediaFiles).Error
			if err != nil {
				helpers.AppLogger.Errorf("获取刮削失败的媒体文件失败：%v", err)
				return
			}
			if len(mediaFiles) == 0 {
				// helpers.AppLogger.Info("没有刮削失败的媒体文件")
				return
			}
			helpers.AppLogger.Infof("获取到 %d 个刮削失败的媒体文件", len(mediaFiles))
			// 遍历所有媒体文件，进行回滚操作
			for _, mediaFile := range mediaFiles {
				scrapePath := models.GetScrapePathByID(mediaFile.ScrapePathId)
				scrape := scrape.NewScrape(scrapePath)
				err := scrape.Rollback(mediaFile)
				if err != nil {
					helpers.AppLogger.Errorf("回滚媒体文件 %s 失败：%v", mediaFile.Name, err)
				} else {
					helpers.AppLogger.Infof("成功回滚媒体文件 %s", mediaFile.Name)
				}
			}
			// 每次处理完休息 10 秒
			time.Sleep(10 * time.Second)
		}
	}()

}

func InitTokenCron() {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return
	}
	defer releaseAdmission()
	if TokenCron != nil {
		TokenCron.Stop()
	}
	TokenCron = cron.New()
	TokenCron.AddFunc("*/2 * * * *", func() {
		// helpers.AppLogger.Info("定时刷新 115 的访问凭证")
		RefreshOAuthAccessToken()
	})
	TokenCron.Start()
}

// 初始化定时任务
func InitCron() {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return
	}
	defer releaseAdmission()
	if GlobalCron != nil {
		GlobalCron.Stop()
	}
	GlobalCron = cron.New()
	GlobalCron.AddFunc("0 1 * * *", func() {
		startClearDownloadUploadTasks()
	})
	GlobalCron.AddFunc(models.SettingsGlobal.Cron, func() {
		// helpers.AppLogger.Info("启动 115 网盘同步任务")
		StartSyncCron()
	})
	GlobalCron.AddFunc("0 0 * * *", func() {
		// 每天 0 点清理过期的同步记录
		// helpers.AppLogger.Info("清理过期的同步记录")
		models.ClearExpiredSyncRecords(syncRecordRetentionDays) // 保留最近 7 天的同步记录和对应日志
	})

	GlobalCron.AddFunc("*/13 * * * *", func() {
		// helpers.AppLogger.Info("启动刮削任务")
		startScrapeCron()
	})
	if config, err := models.GetEmbyConfig(); err == nil {
		if config.EmbyApiKey != "" && config.EmbyUrl != "" && config.SyncEnabled == 1 {
			GlobalCron.AddFunc(config.SyncCron, func() {
				config, err := models.GetEmbyConfigFromDB()
				if err != nil {
					helpers.AppLogger.Errorf("获取 Emby 配置失败：%v", err)
					return
				}
				switch selectScheduledEmbySyncMode(config, time.Now()) {
				case models.EmbySyncModeFull:
					if _, err := emby.PerformEmbySync(); err != nil {
						helpers.AppLogger.Errorf("每日首次全量同步 Emby 条目到本地失败：%v", err)
					}
				default:
					if _, err := emby.PerformEmbyIncrementalSync(); err != nil {
						helpers.AppLogger.Errorf("增量同步 Emby 条目到本地失败：%v", err)
					}
				}
			})
		}
	}
	GlobalCron.AddFunc("*/2 * * * *", func() {
		// helpers.AppLogger.Info("启动刮削回滚任务")
		StartScrapeRollbackCron()
	})
	GlobalCron.AddFunc("0 * * * *", func() {
		// 每小时清理一次请求统计数据，只保留最近 24 小时
		if err := models.CleanOldRequestStatsByHours(24); err != nil {
			helpers.AppLogger.Errorf("清理请求统计数据失败：%v", err)
		} else {
			helpers.AppLogger.Infof("已清理 24 小时前的请求统计数据")
		}
	})
	GlobalCron.AddFunc("0 4 * * *", func() {
		// 每天 4 点补齐数据库表结构，并检查 PostgreSQL 主键序列
		err := models.BatchCreateTable()
		if err != nil {
			helpers.AppLogger.Errorf("修复数据库失败：%v", err)
			return
		} else {
			helpers.AppLogger.Infof("已补齐数据库表结构（不影响已存在的表和数据）")
		}
		if err := models.BatchRepairTableSeq(); err != nil {
			helpers.AppLogger.Errorf("修复数据库表的主键序列失败：%v", err)
		} else {
			helpers.AppLogger.Infof("已完成数据库表主键序列检查")
		}
	})

	addBackupCron()

	GlobalCron.Start()
}

// 初始化 STRM 同步目录的定时任务
func InitSyncCron() {
	if err := InitSyncCronWithError(); err != nil {
		helpers.AppLogger.Errorf("初始化同步目录定时任务失败：%v", err)
	}
}

// InitSyncCronWithError 初始化 STRM 同步目录定时任务并返回配置错误。
func InitSyncCronWithError() error {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return err
	}
	defer releaseAdmission()
	if SyncCron != nil {
		helpers.AppLogger.Info("已存在同步目录的定时任务，先停止")
		SyncCron.Stop()
	}
	SyncCron = cron.New()
	// 查询所有同步目录
	syncPaths, _ := models.GetSyncPathList(1, 10000000, true, "")
	if len(syncPaths) == 0 {
		helpers.AppLogger.Info("没有启用定时任务的同步目录")
		return nil
	}
	for _, syncPath := range syncPaths {
		if syncPath.Cron == "" {
			helpers.AppLogger.Infof("同步目录 %d 未启用自定义的定时任务", syncPath.ID)
			continue
		}
		helpers.AppLogger.Infof("已添加同步目录 %d 的定时任务，Cron 表达式：%s", syncPath.ID, syncPath.Cron)
		if _, err := SyncCron.AddFunc(syncPath.Cron, func() {
			// 将同步目录 ID 添加到处理队列，而不是直接执行
			taskObj := &NewSyncTask{
				ID:           syncPath.ID,
				SourcePath:   "",
				SourcePathId: "",
				TargetPath:   "",
				AccountId:    syncPath.AccountId,
				IsFile:       false,
				TaskType:     SyncTaskTypeStrm,
				SourceType:   syncPath.SourceType,
			}
			if err := AddNewSyncTask(taskObj); err != nil {
				helpers.AppLogger.Errorf("将同步任务添加到队列失败：%s", err.Error())
				return
			}
		}); err != nil {
			return fmt.Errorf("添加同步目录 %d 定时任务失败：%w", syncPath.ID, err)
		}
	}
	SyncCron.Start()
	return nil
}

// 初始化刮削目录的自定义定时任务
func InitScrapeCron() {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return
	}
	defer releaseAdmission()
	if ScrapeCron != nil {
		helpers.AppLogger.Info("已存在刮削目录的定时任务，先停止")
		ScrapeCron.Stop()
	}
	ScrapeCron = cron.New()

	// 查询所有刮削目录
	scrapePaths := models.GetScrapePathes("")
	if len(scrapePaths) == 0 {
		helpers.AppLogger.Info("没有启用自定义定时任务的刮削目录")
		return
	}

	for _, scrapePath := range scrapePaths {
		// 未启用定时任务或没有自定义 Cron 表达式的跳过
		if !scrapePath.EnableCron || scrapePath.CronExpression == "" {
			helpers.AppLogger.Infof("刮削目录 %d 未启用自定义定时任务", scrapePath.ID)
			continue
		}

		helpers.AppLogger.Infof("已添加刮削目录 %d 的定时任务，Cron 表达式：%s", scrapePath.ID, scrapePath.CronExpression)
		scrapePathID := scrapePath.ID // 捕获变量
		ScrapeCron.AddFunc(scrapePath.CronExpression, func() {
			// 将刮削目录 ID 添加到处理队列，而不是直接执行
			taskObj := &NewSyncTask{
				ID:           scrapePathID,
				SourcePath:   "",
				SourcePathId: "",
				TargetPath:   "",
				AccountId:    scrapePath.AccountId,
				IsFile:       false,
				TaskType:     SyncTaskTypeScrape,
				SourceType:   scrapePath.SourceType,
			}
			if err := AddNewSyncTask(taskObj); err != nil {
				helpers.AppLogger.Errorf("将刮削任务添加到队列失败：%s", err.Error())
				return
			}
			helpers.AppLogger.Infof("刮削任务已创建并加入执行队列，刮削目录 ID：%d，刮削目录：%s，目标目录：%s", scrapePathID, scrapePath.SourcePath, scrapePath.DestPath)
		})
	}
	ScrapeCron.Start()
}

func addBackupCron() {
	backupConfig := models.GetOrCreateBackupConfig()
	if backupConfig.BackupEnabled == 0 || backupConfig.BackupCron == "" {
		return
	}
	_, err := GlobalCron.AddFunc(backupConfig.BackupCron, func() {
		helpers.AppLogger.Info("开始执行定时自动备份")
		helpers.Publish(helpers.BackupCronEevent, nil)
	})

	if err != nil {
		helpers.AppLogger.Errorf("添加备份定时任务失败：%v", err)
	} else {
		helpers.AppLogger.Infof("已添加自动备份定时任务，Cron 表达式：%s", backupConfig.BackupCron)
	}
}
