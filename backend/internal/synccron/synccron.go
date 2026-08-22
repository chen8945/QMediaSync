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
	"qmediasync/internal/v115open"

	"github.com/robfig/cron/v3"
)

const syncRecordRetentionDays = 7

// refreshV115Token 使用账号快照客户端刷新 115 访问凭证；变量形式便于测试注入失败场景。
// 使用快照客户端刷新，避免远端旧结果先修改共享客户端，再被条件落库拒绝。
var refreshV115Token = func(account models.Account) (*v115open.TokenData, error) {
	client := v115open.NewClient(account.ID, account.AppId, account.Token, account.RefreshToken)
	return client.RefreshToken(account.RefreshToken)
}

// refreshBaiduToken 通过授权中转刷新百度网盘访问凭证；变量形式便于测试注入失败场景。
var refreshBaiduToken = func(accountId uint, refreshToken string) (*baidupan.RefreshResponse, error) {
	return baidupan.RefreshToken(accountId, refreshToken)
}

// persistAccountToken 将轮换后的凭据写入数据库；变量形式便于测试注入落库失败场景。
var persistAccountToken = func(account *models.Account, token string, refreshToken string, expiresIn int64) error {
	return account.TryUpdateTokenIfCurrent(token, refreshToken, expiresIn)
}

// loadAccountsForTokenRefresh 读取全部账号；变量形式便于测试注入查询失败场景。
var loadAccountsForTokenRefresh = models.GetAllAccount

// 轮换结果落库的重试参数；使用变量便于测试缩短重试等待
var (
	tokenPersistRetryCount = 5
	tokenPersistRetryDelay = 10 * time.Second
)

// pendingToken 保存刷新成功但落库失败的轮换结果，等待后续轮次补写。
// 只在 RefreshOAuthAccessToken 单飞执行期间访问，无需加锁。
type pendingToken struct {
	source          models.SourceType
	expectedToken   string // 轮换开始时的旧 access token，作为补写守卫
	expectedRefresh string // 轮换开始时的旧 refresh token
	token           string
	refreshToken    string
	expiresIn       int64
	rotatedAt       int64 // 轮换完成时刻，补写时按它锚定过期时间
}

var pendingTokenPersists = make(map[uint]pendingToken)

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
	// 检查是否已在运行，防止并发执行
	if !atomic.CompareAndSwapInt32(&tokenRefreshRunning, 0, 1) {
		helpers.AppLogger.Warn("Token 刷新任务已在运行，跳过本次执行")
		return
	}

	// 使用 defer 确保函数结束时释放锁
	defer atomic.StoreInt32(&tokenRefreshRunning, 0)

	// 刷新 115 的访问凭证
	// 取所有 115 类型的账号
	accounts, err := loadAccountsForTokenRefresh()
	if err != nil {
		// 数据库不可读时保留待写记录原样返回，避免误清理后用旧凭据刷新
		helpers.AppLogger.Errorf("查询账号列表失败，本轮跳过凭证刷新与待写补传：%v", err)
		return
	}
	now := time.Now().Unix()
	for _, account := range accounts {
		// 上一轮轮换结果落库失败时优先补写，补写成功前不再发起刷新，
		// 避免用已被轮换消费的旧 refresh_token 触发判死清空
		if pending, ok := pendingTokenPersists[account.ID]; ok {
			replayPendingTokenPersist(&account, pending)
			continue
		}
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
			expectedToken := account.Token
			expectedRefreshToken := account.RefreshToken
			tokenData, err := refreshV115Token(account)
			if err != nil {
				if !v115open.IsRefreshTokenDead(err) {
					// 网络错误或可重试的业务失败保留凭据，等待下一轮定时刷新
					helpers.AppLogger.Warnf("刷新 115 访问凭证失败，保留凭据等待下轮重试，账号 ID：%d：%s", account.ID, err.Error())
					continue
				}
				helpers.AppLogger.Errorf("刷新 115 访问凭证失败：%s", err.Error())
				// 清空 Token
				if !account.ClearTokenIfCurrent(err.Error()) {
					continue
				}
				v115open.UpdateTokenIfCurrent(account.ID, expectedToken, expectedRefreshToken, "", "")
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
			// 更新账号的 Token；落库失败时保留待写记录，避免轮换结果随响应丢弃
			if err := persistTokenWithRetry(&account, tokenData.AccessToken, tokenData.RefreshToken, tokenData.ExpiresIn); err != nil {
				if !models.IsTokenCredentialsChanged(err) {
					pendingTokenPersists[account.ID] = pendingToken{
						source:          models.SourceType115,
						expectedToken:   expectedToken,
						expectedRefresh: expectedRefreshToken,
						token:           tokenData.AccessToken,
						refreshToken:    tokenData.RefreshToken,
						expiresIn:       tokenData.ExpiresIn,
						rotatedAt:       time.Now().Unix(),
					}
					helpers.AppLogger.Errorf("更新 115 账号 Token 失败，已保留待写记录等待下轮补写，账号 ID：%d", account.ID)
				}
				continue
			}
			// 更新其他客户端的 Token
			v115open.UpdateTokenIfCurrent(account.ID, expectedToken, expectedRefreshToken, tokenData.AccessToken, tokenData.RefreshToken)
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
			expectedToken := account.Token
			// 向授权服务器发送刷新请求，拿到新 Token
			resp, err := refreshBaiduToken(account.ID, account.RefreshToken)
			if err != nil {
				if !baidupan.IsRefreshTokenDead(err) {
					// 网络错误或可重试的业务失败保留凭据，等待下一轮定时刷新
					helpers.AppLogger.Warnf("刷新百度网盘 Token 失败，保留凭据等待下轮重试，账号 ID：%d：%s", account.ID, err.Error())
					continue
				}
				helpers.AppLogger.Errorf("刷新百度网盘 Token 失败：%s", err.Error())
				// 清空 Token
				if !account.ClearTokenIfCurrent(err.Error()) {
					continue
				}
				baidupan.UpdateTokenIfCurrent(account.ID, expectedToken, "")
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
			// 更新账号的 Token；落库失败时保留待写记录，避免轮换结果随响应丢弃
			if err := persistTokenWithRetry(&account, resp.AccessToken, resp.RefreshToken, resp.ExpiresIn); err != nil {
				if !models.IsTokenCredentialsChanged(err) {
					pendingTokenPersists[account.ID] = pendingToken{
						source:          models.SourceTypeBaiduPan,
						expectedToken:   expectedToken,
						expectedRefresh: account.RefreshToken,
						token:           resp.AccessToken,
						refreshToken:    resp.RefreshToken,
						expiresIn:       resp.ExpiresIn,
						rotatedAt:       time.Now().Unix(),
					}
					helpers.AppLogger.Errorf("更新百度网盘账号 Token 失败，已保留待写记录等待下轮补写，账号 ID：%d", account.ID)
				}
				continue
			}
			// 更新其他客户端的 Token
			baidupan.UpdateTokenIfCurrent(account.ID, expectedToken, resp.AccessToken)
			// 刷新成功，更新账号的 Token
			helpers.AppLogger.Infof("刷新百度网盘账号 Token 成功，账号 ID：%d，新到期时间：%d => %s", account.ID, resp.ExpiresIn, time.Unix(resp.ExpiresIn, 0).Format("2006-01-02 15:04:05"))
			continue
		}
	}

	// 清理已删除账号的待写记录
	accountIds := make(map[uint]struct{}, len(accounts))
	for _, account := range accounts {
		accountIds[account.ID] = struct{}{}
	}
	for accountId := range pendingTokenPersists {
		if _, ok := accountIds[accountId]; !ok {
			delete(pendingTokenPersists, accountId)
		}
	}
}

// persistTokenWithRetry 带重试写入轮换后的凭据；凭据被并发覆盖时重试无意义，立即返回。
func persistTokenWithRetry(account *models.Account, token string, refreshToken string, expiresIn int64) error {
	var err error
	for attempt := 1; attempt <= tokenPersistRetryCount; attempt++ {
		if err = persistAccountToken(account, token, refreshToken, expiresIn); err == nil {
			return nil
		}
		if models.IsTokenCredentialsChanged(err) {
			return err
		}
		if attempt < tokenPersistRetryCount {
			time.Sleep(tokenPersistRetryDelay)
		}
	}
	return err
}

// replayPendingTokenPersist 补写上一轮落库失败的轮换结果。
// 无论补写成功、凭据已被覆盖还是仍然失败，本轮都不再发起刷新：
// 旧的 refresh_token 已被服务端消费，再刷新只会触发判死清空。
func replayPendingTokenPersist(account *models.Account, pending pendingToken) {
	if account.Token == pending.token && account.RefreshToken == pending.refreshToken {
		// 轮换结果已通过其他路径写入数据库（如写库实际成功但返回了错误）
		syncCachedToken(account, pending)
		delete(pendingTokenPersists, account.ID)
		helpers.AppLogger.Infof("账号 %d 的轮换结果已写入，无需补写", account.ID)
		return
	}
	if account.Token != pending.expectedToken || account.RefreshToken != pending.expectedRefresh {
		// 凭据已被重新授权等操作覆盖，丢弃待写结果
		delete(pendingTokenPersists, account.ID)
		helpers.AppLogger.Infof("账号 %d 凭据已更新，丢弃待写的轮换结果", account.ID)
		return
	}
	// 按轮换时刻锚定过期时间，补写延迟不得延长凭证有效期
	expiresIn := pending.rotatedAt + pending.expiresIn - time.Now().Unix()
	if expiresIn < 1 {
		expiresIn = 1
	}
	if err := persistTokenWithRetry(account, pending.token, pending.refreshToken, expiresIn); err != nil {
		if models.IsTokenCredentialsChanged(err) {
			delete(pendingTokenPersists, account.ID)
			helpers.AppLogger.Infof("账号 %d 凭据已更新，丢弃待写的轮换结果", account.ID)
			return
		}
		helpers.AppLogger.Warnf("补写账号 %d Token 失败，继续保留待写记录：%v", account.ID, err)
		return
	}
	delete(pendingTokenPersists, account.ID)
	syncCachedToken(account, pending)
	helpers.AppLogger.Infof("补写账号 %d Token 成功", account.ID)
}

// syncCachedToken 把轮换结果同步到对应来源的内存客户端缓存。
func syncCachedToken(account *models.Account, pending pendingToken) {
	switch account.SourceType {
	case models.SourceType115:
		v115open.UpdateTokenIfCurrent(account.ID, pending.expectedToken, pending.expectedRefresh, pending.token, pending.refreshToken)
	case models.SourceTypeBaiduPan:
		baidupan.UpdateTokenIfCurrent(account.ID, pending.expectedToken, pending.token)
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
	if TokenCron != nil {
		TokenCron.Stop()
	}
	TokenCron = cron.New()
	TokenCron.AddFunc("*/5 * * * *", func() {
		// helpers.AppLogger.Info("定时刷新 115 的访问凭证")
		RefreshOAuthAccessToken()
	})
	TokenCron.Start()
}

// 初始化定时任务
func InitCron() {
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
