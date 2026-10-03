package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
)

// syncBackgroundService 管理后台文件记录更新、通知和后续任务。
type syncBackgroundService struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	stopping bool
	workers  sync.WaitGroup
	done     chan struct{}
}

func newSyncBackgroundService() *syncBackgroundService {
	ctx, cancel := context.WithCancel(context.Background())
	return &syncBackgroundService{ctx: ctx, cancel: cancel, done: make(chan struct{})}
}

var defaultSyncBackgroundService = newSyncBackgroundService()

// InitSyncBackgroundService 在新同步入队前标记上次未正常结束的后台任务。
func InitSyncBackgroundService(ctx context.Context) error {
	return models.InterruptRunningSyncLedgers(ctx)
}

// StopSyncBackgroundService 取消后台工作并在给定期限内等待其实际退出。
func StopSyncBackgroundService(ctx context.Context) error {
	return defaultSyncBackgroundService.stop(ctx)
}

func (service *syncBackgroundService) submit(run func(context.Context)) bool {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.stopping {
		return false
	}
	service.workers.Add(1)
	go func() {
		defer service.workers.Done()
		run(service.ctx)
	}()
	return true
}

func (service *syncBackgroundService) stop(ctx context.Context) error {
	service.mu.Lock()
	if !service.stopping {
		service.stopping = true
		service.cancel()
		go func() {
			service.workers.Wait()
			close(service.done)
		}()
	}
	service.mu.Unlock()
	select {
	case <-service.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backgroundLedgerView 在文件处理结束后，把缓存和目录使用权交给后台。
// 后台单独保存失败文件的保护信息，前台只保留已经确定的生成结果。
func (s *SyncStrm) backgroundLedgerView(record *models.Sync) *SyncStrm {
	account := &models.Account{BaseModel: models.BaseModel{ID: s.Account.ID}, SourceType: s.Account.SourceType, BaseUrl: s.Account.BaseUrl}
	s.scanMu.Lock()
	view := &SyncStrm{
		Sync: record, Account: account,
		SourcePath: s.SourcePath, SourcePathId: s.SourcePathId, TargetPath: s.TargetPath, SyncPathId: s.SyncPathId,
		memSyncCache: s.memSyncCache, scopeRelease: s.scopeRelease, scopeLocalRoot: s.scopeLocalRoot,
		scanFiles: maps.Clone(s.scanFiles), scanFailures: maps.Clone(s.scanFailures),
		protectedFileIDs: maps.Clone(s.protectedFileIDs), protectedPickCodes: maps.Clone(s.protectedPickCodes),
		protectedLocalPaths: maps.Clone(s.protectedLocalPaths), missingCleanupReason: s.missingCleanupReason,
		protectedLocalDirectories: maps.Clone(s.protectedLocalDirectories),
	}
	s.scanMu.Unlock()
	s.memSyncCache = nil
	s.scopeRelease = nil
	s.scopeLocalRoot = ""
	return view
}

func generationSnapshot(record *models.Sync) models.Sync {
	snapshot := *record
	if record.ScanResult != nil {
		result := *record.ScanResult
		result.Failures = slices.Clone(record.ScanResult.Failures)
		snapshot.ScanResult = &result
	}
	if record.LedgerStatus != nil {
		status := *record.LedgerStatus
		snapshot.LedgerStatus = &status
	}
	if record.LedgerFinishedAt != nil {
		finishedAt := *record.LedgerFinishedAt
		snapshot.LedgerFinishedAt = &finishedAt
	}
	return snapshot
}

func (s *SyncStrm) startBackground(withLedger bool) {
	service := s.backgroundService
	if service == nil {
		service = defaultSyncBackgroundService
	}
	record := generationSnapshot(s.Sync)
	sourceType, ownsLogger := s.Account.SourceType, s.ownsLogger
	targets := s.drainEmbyRefreshTargets()
	var ledger *SyncStrm
	if withLedger {
		ledgerRecord := generationSnapshot(&record)
		ledger = s.backgroundLedgerView(&ledgerRecord)
	} else {
		s.releaseScope()
	}
	run := func(ctx context.Context) {
		if ownsLogger {
			defer record.Logger.Close()
		}
		// 通知送达、下游队列与账本互不等待；所有日志使用者退出后才关闭句柄。
		var callbacks sync.WaitGroup
		callbacks.Go(func() { record.NotifyGeneration(ctx, sourceType) })
		if withLedger {
			callbacks.Go(func() { submitGenerationDownstream(ctx, &record, targets) })
			ledger.Context = ctx
			ledger.runBackgroundLedger()
		}
		callbacks.Wait()
	}
	if service.submit(run) {
		return
	}
	// 关闭期间不再运行新账本，也不能留下永远待执行的记录。
	if ledger != nil {
		ledger.Context = service.ctx
		ledger.finishBackgroundLedger(realtime.SyncLedgerInterrupted, fmt.Errorf("后台服务已停止，同步文件记录更新未开始"))
		ledger.releaseScope()
	}
	record.Logger.Warnf("后台服务已停止，跳过尚未发送的生成通知及下游提交：sync_id=%d", record.ID)
	if ownsLogger {
		record.Logger.Close()
	}
}

func (s *SyncStrm) runBackgroundLedger() {
	defer s.releaseScope()
	if err := s.Context.Err(); err != nil {
		s.finishBackgroundLedger(realtime.SyncLedgerInterrupted, err)
		return
	}
	// 删除历史只移除展示记录，不能丢弃已经生成的文件账本。
	if err := s.Sync.UpdateLedger(s.Context, realtime.SyncLedgerRunning, nil, ""); err != nil && !errors.Is(err, models.ErrSyncLedgerRecordDeleted) {
		s.finishBackgroundLedger(realtime.SyncLedgerFailed, fmt.Errorf("保存同步文件记录更新的开始状态失败：%w", err))
		return
	}
	s.Sync.Logger.Infof("开始更新同步文件记录：sync_id=%d，sync_path_id=%d", s.Sync.ID, s.SyncPathId)
	err := s.handleTempTableDiff()
	status := realtime.SyncLedgerCompleted
	if s.Context.Err() != nil {
		status, err = realtime.SyncLedgerInterrupted, s.Context.Err()
	} else if err != nil {
		status = realtime.SyncLedgerFailed
	}
	s.finishBackgroundLedger(status, err)
}

func (s *SyncStrm) finishBackgroundLedger(status realtime.SyncLedgerStatus, cause error) {
	finishedAt := time.Now().Unix()
	message := "同步文件记录更新结束"
	switch status {
	case realtime.SyncLedgerCompleted:
		message = "同步文件记录更新完成"
	case realtime.SyncLedgerFailed:
		message = "同步文件记录更新失败"
	case realtime.SyncLedgerInterrupted:
		message = "同步文件记录更新中断"
	}
	reason := ""
	if cause != nil {
		reason = helpers.RedactSensitiveLog(cause.Error())
		s.Sync.Logger.Errorf(
			"%s：sync_id=%d，结果=%s，错误=%s",
			message,
			s.Sync.ID,
			status,
			reason,
		)
	} else {
		s.Sync.Logger.Infof(
			"%s：sync_id=%d，结果=%s",
			message,
			s.Sync.ID,
			status,
		)
	}
	// 服务停止后仍留出 5 秒保存结果；保存失败就保留未确认状态。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.Context), 5*time.Second)
	defer cancel()
	if err := s.Sync.UpdateLedger(ctx, status, &finishedAt, reason); err != nil && !errors.Is(err, models.ErrSyncLedgerRecordDeleted) {
		s.Sync.Logger.Errorf("同步文件记录更新结果未能保存，结束时间暂不可确认：sync_id=%d，错误=%v", s.Sync.ID, err)
		effectiveSyncLogger().Errorf("同步文件记录更新结果未能保存，结束时间暂不可确认：sync_id=%d，错误=%v", s.Sync.ID, err)
	}
}

func submitGenerationDownstream(ctx context.Context, record *models.Sync, targets []models.EmbyRefreshTarget) {
	if ctx.Err() != nil {
		return
	}
	if shouldRequestEmbyLibraryRefresh(int64(record.NewMeta), int64(record.NewStrm)) {
		record.Logger.Info("有新的元数据文件或 STRM 文件，提交 Emby 媒体库刷新任务")
		if len(targets) == 0 {
			targets = []models.EmbyRefreshTarget{{TargetType: models.EmbyRefreshTargetTypeLibrary}}
		}
		if err := models.RequestEmbyRefreshTargets(record.SyncPathId, targets); err != nil {
			record.Logger.Errorf("提交 Emby 媒体库刷新任务失败：%v", err)
		}
	}
	if ctx.Err() != nil {
		return
	}
	if record.NewStrm <= 0 {
		record.Logger.Info("没有新的 STRM 生成，跳过关联的刮削任务")
		return
	}
	record.Logger.Info("准备触发关联的刮削任务")
	var syncPath models.SyncPath
	if err := db.Db.WithContext(ctx).First(&syncPath, record.SyncPathId).Error; err != nil {
		record.Logger.Errorf("查询关联刮削目录失败：%v", err)
		return
	}
	if ids := syncPath.GetScrapePathIds(); len(ids) > 0 {
		helpers.Publish(helpers.StrmSyncCompleteEvent, ids)
	} else {
		record.Logger.Info("关联的刮削目录为空，跳过触发刮削任务")
	}
}
