package synccron

import (
	"context"
	"fmt"
	"sync"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"github.com/robfig/cron/v3"
)

var scrapeCronState struct {
	sync.Mutex
	apply    sync.Mutex
	revision uint64
	current  *cron.Cron
	entries  map[uint]*scrapeCronEntry
}

type scrapeCronEntry struct {
	expression string
	accountID  uint
	sourceType models.SourceType
	schedule   cron.Schedule
	entryID    cron.EntryID
}

var loadScrapeCronPaths = func() ([]*models.ScrapePath, error) {
	var paths []*models.ScrapePath
	err := db.Db.Order("id DESC").Find(&paths).Error
	return paths, err
}

var enqueueScrapeCronTask = AddNewSyncTask

// InitScrapeCron 初始化目录定时任务，配置错误沿既有启动日志反馈。
func InitScrapeCron() {
	if err := InitScrapeCronWithError(); err != nil {
		helpers.AppLogger.Errorf("应用刮削定时任务失败：%v", err)
	}
}

// InitScrapeCronWithError 完整应用已保存的开关与表达式，慢读取不阻塞状态锁。
func InitScrapeCronWithError() error {
	scrapeCronState.Lock()
	scrapeCronState.revision++
	revision := scrapeCronState.revision
	scrapeCronState.Unlock()
	paths, err := loadScrapeCronPaths()
	if err != nil {
		return err
	}
	entries := make(map[uint]*scrapeCronEntry)
	for _, path := range paths {
		if !path.EnableCron || path.CronExpression == "" {
			continue
		}
		schedule, err := cron.ParseStandard(path.CronExpression)
		if err != nil {
			return fmt.Errorf("添加刮削目录 %d 定时任务失败：%w", path.ID, err)
		}
		entries[path.ID] = &scrapeCronEntry{expression: path.CronExpression, accountID: path.AccountId, sourceType: path.SourceType, schedule: schedule}
	}
	// 注册与停止串行，Cron 控制通道的等待不占回调读取的状态锁。
	scrapeCronState.apply.Lock()
	defer scrapeCronState.apply.Unlock()
	scrapeCronState.Lock()
	if revision != scrapeCronState.revision {
		scrapeCronState.Unlock()
		return fmt.Errorf("刮削定时应用已被后续重载或停止替代，请重新保存重试")
	}
	plan, previous := scrapeCronState.current, scrapeCronState.entries
	scrapeCronState.Unlock()
	if plan == nil {
		plan = cron.New()
	}
	var added []cron.EntryID
	for id, entry := range entries {
		if old := previous[id]; old != nil && old.expression == entry.expression && old.accountID == entry.accountID && old.sourceType == entry.sourceType {
			entries[id] = old
			continue
		}
		entry.entryID = plan.Schedule(entry.schedule, cron.FuncJob(func() {
			scrapeCronState.Lock()
			current := scrapeCronState.current == plan && scrapeCronState.entries[id] == entry
			scrapeCronState.Unlock()
			if !current {
				return
			}
			task := &NewSyncTask{ID: id, AccountId: entry.accountID, SourceType: entry.sourceType, TaskType: SyncTaskTypeScrape}
			if err := enqueueScrapeCronTask(task); err != nil {
				helpers.AppLogger.Errorf("将刮削任务添加到队列失败：%v", err)
			}
		}))
		added = append(added, entry.entryID)
	}
	scrapeCronState.Lock()
	if revision != scrapeCronState.revision {
		scrapeCronState.Unlock()
		for _, id := range added {
			plan.Remove(id)
		}
		return fmt.Errorf("刮削定时应用已被后续重载或停止替代，请重新保存重试")
	}
	scrapeCronState.current = plan
	scrapeCronState.entries = entries
	scrapeCronState.Unlock()
	for id, old := range previous {
		if entries[id] != old {
			plan.Remove(old.entryID)
		}
	}
	plan.Start()
	return nil
}

// StopScrapeCron 失效旧计划及准备中的候选；等待回调退出在调用方锁外完成。
func StopScrapeCron() context.Context {
	scrapeCronState.apply.Lock()
	defer scrapeCronState.apply.Unlock()
	scrapeCronState.Lock()
	scrapeCronState.revision++
	old := scrapeCronState.current
	scrapeCronState.current = nil
	scrapeCronState.entries = nil
	scrapeCronState.Unlock()
	if old != nil {
		return old.Stop()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
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
