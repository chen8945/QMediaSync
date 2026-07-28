package directoryupload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/taskgate"
)

// RuleRuntimeMode 是目录监控规则运行模式。
type RuleRuntimeMode string

const (
	RuleRuntimeModeWatcher RuleRuntimeMode = "watcher"
	RuleRuntimeModePolling RuleRuntimeMode = "polling"
)

const (
	defaultProcessedCleanupInterval  = 24 * time.Hour
	defaultProcessedMissingSourceTTL = 30 * 24 * time.Hour
)

// RuleWatcher 是单条目录监控规则的事件监听器。
type RuleWatcher interface {
	Start(ctx context.Context) error
	Close() error
}

// WatcherFactory 创建目录事件监听器。
type WatcherFactory func(service *Service, rule *models.DirectoryUploadRule) (RuleWatcher, error)

// RuleRuntime 保存单条目录监控规则的运行状态。
type RuleRuntime struct {
	RuleID uint
	Mode   RuleRuntimeMode

	statusMu           sync.RWMutex
	configuredMode     string
	fallbackReason     string
	lastScanAt         int64
	lastScanDurationMs int64
	lastScanCandidates int
	lastScanSkipped    int
	lastScanError      string
	lastRuntimeError   string

	cancel          context.CancelFunc
	watcher         RuleWatcher
	done            chan struct{}
	once            sync.Once
	pollingMu       sync.Mutex
	pollingSnapshot map[string]string
}

// Stop 停止目录监控规则运行时。
func (runtime *RuleRuntime) Stop() {
	if runtime == nil {
		return
	}
	runtime.once.Do(func() {
		if runtime.cancel != nil {
			runtime.cancel()
		}
		if runtime.watcher != nil {
			_ = runtime.watcher.Close()
		}
		if runtime.done != nil {
			<-runtime.done
		}
	})
}

func (runtime *RuleRuntime) snapshot() map[string]string {
	if runtime == nil {
		return map[string]string{}
	}
	runtime.pollingMu.Lock()
	defer runtime.pollingMu.Unlock()

	snapshot := make(map[string]string, len(runtime.pollingSnapshot))
	for rel, fingerprint := range runtime.pollingSnapshot {
		snapshot[rel] = fingerprint
	}
	return snapshot
}

func (runtime *RuleRuntime) replaceSnapshot(snapshot map[string]string) {
	if runtime == nil {
		return
	}
	next := make(map[string]string, len(snapshot))
	for rel, fingerprint := range snapshot {
		next[rel] = fingerprint
	}
	runtime.pollingMu.Lock()
	defer runtime.pollingMu.Unlock()

	runtime.pollingSnapshot = next
}

// Start 启动所有已启用的目录上传规则。
func (service *Service) Start(ctx context.Context) error {
	if service == nil {
		return errors.New("目录上传服务为空")
	}
	rules, err := models.GetEnabledDirectoryUploadRules()
	if err != nil {
		return err
	}
	service.startProcessedCleanup(ctx)
	for _, rule := range rules {
		runtime, err := service.StartRule(ctx, rule)
		if err != nil {
			helpers.AppLogger.Warnf("[目录上传] 启动规则 %d 失败：%v", rule.ID, err)
			continue
		}
		service.mutex.Lock()
		service.runtimes = append(service.runtimes, runtime)
		service.mutex.Unlock()
	}
	return nil
}

// Stop 停止目录上传服务运行的所有规则。
func (service *Service) Stop() {
	if service == nil {
		return
	}
	service.mutex.Lock()
	runtimes := append([]*RuleRuntime(nil), service.runtimes...)
	service.runtimes = nil
	cleanupCancel := service.cleanupCancel
	cleanupDone := service.cleanupDone
	scanExecutor := service.scanExecutor
	service.cleanupCancel = nil
	service.cleanupDone = nil
	service.mutex.Unlock()
	if cleanupCancel != nil {
		cleanupCancel()
	}
	if cleanupDone != nil {
		<-cleanupDone
	}
	for _, runtime := range runtimes {
		runtime.Stop()
	}
	if scanExecutor != nil {
		scanExecutor.Wait()
	}
}

// StartRule 启动单条目录上传规则。
func (service *Service) StartRule(ctx context.Context, rule *models.DirectoryUploadRule) (*RuleRuntime, error) {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	if rule == nil {
		return nil, errors.New("目录监控规则为空")
	}
	ruleCtx, cancel := context.WithCancel(ctx)
	runtime := &RuleRuntime{
		RuleID: rule.ID,
		cancel: cancel,
		done:   make(chan struct{}),
	}

	if rule.StartupScanEnabled {
		if _, _, _, err := service.validateScanRoot(ruleCtx, rule, rule.MonitorPath); err != nil {
			cancel()
			return nil, fmt.Errorf("启动补偿扫描失败：%w", err)
		}
	}

	watcher, mode, fallbackReason, err := service.startRuleWatcher(ruleCtx, rule, runtime)
	if err != nil {
		cancel()
		return nil, err
	}
	runtime.watcher = watcher
	runtime.setRuntimeMode(configuredWatchMode(rule), mode, fallbackReason)
	if runtime.Mode == RuleRuntimeModePolling {
		if rule.StartupScanEnabled {
			service.enqueuePollingSnapshotScan(ruleCtx, runtime, rule, rule.MonitorPath, pollingSnapshotTrackAll)
		} else {
			startedAt := service.now()
			result, err := service.scanRootWithSnapshot(ruleCtx, rule, rule.MonitorPath)
			runtime.recordScan(service.now(), startedAt, result.Accepted, result.Accepted, err)
			if err != nil && (ruleCtx.Err() != nil || errors.Is(err, context.Canceled)) {
				cancel()
				return nil, fmt.Errorf("建立 polling baseline 失败：%w", err)
			}
			runtime.replaceSnapshot(result.Snapshot)
			if err != nil {
				helpers.AppLogger.Warnf("[目录上传] 规则 %d polling baseline 建立失败，后续 polling 将重试：%v", rule.ID, err)
			}
		}
	} else if rule.StartupScanEnabled {
		service.enqueueRuntimeScan(ruleCtx, runtime, rule, rule.MonitorPath)
	}

	var wg sync.WaitGroup
	if runtime.Mode == RuleRuntimeModePolling {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.runPollingLoop(ruleCtx, runtime, rule)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		service.runStabilityLoop(ruleCtx, rule)
	}()
	go func() {
		wg.Wait()
		close(runtime.done)
	}()
	return runtime, nil
}

func (service *Service) startRuleWatcher(
	ctx context.Context,
	rule *models.DirectoryUploadRule,
	runtime *RuleRuntime,
) (RuleWatcher, RuleRuntimeMode, string, error) {
	switch rule.WatchMode {
	case models.DirectoryUploadWatchModePolling:
		return nil, RuleRuntimeModePolling, "", nil
	case models.DirectoryUploadWatchModeFSNotify:
		watcher, err := service.startFSNotifyRuleWatcher(ctx, rule, runtime)
		if err != nil {
			return nil, "", "", fmt.Errorf("启动 watcher 失败：%w", err)
		}
		return watcher, RuleRuntimeModeWatcher, "", nil
	case "", models.DirectoryUploadWatchModeAuto:
	default:
		return nil, "", "", fmt.Errorf("不支持的监控模式：%s", rule.WatchMode)
	}

	decision := service.decideWatchMode(ctx, rule)
	if decision.Err != nil {
		return nil, "", "", decision.Err
	}
	if decision.Mode == RuleRuntimeModePolling {
		helpers.AppLogger.Warnf("[目录上传] 规则 %d auto 选择 polling：%s", rule.ID, decision.Reason)
		return nil, RuleRuntimeModePolling, decision.Reason, nil
	}
	helpers.AppLogger.Debugf("[目录上传] 规则 %d auto 选择 fsnotify：%s", rule.ID, decision.Reason)
	watcher, err := service.startFSNotifyRuleWatcher(ctx, rule, runtime)
	if err == nil {
		return watcher, RuleRuntimeModeWatcher, "", nil
	}
	if cancelErr := terminalContextError(ctx, err); cancelErr != nil {
		return nil, "", "", cancelErr
	}
	helpers.AppLogger.Warnf(
		"[目录上传] 规则 %d auto 选择 fsnotify（%s）但 watcher 启动失败，切换为 polling：%v",
		rule.ID,
		decision.Reason,
		err,
	)
	fallbackReason := fmt.Sprintf("auto 选择 fsnotify（%s）但 watcher 启动失败，切换为 polling：%v", decision.Reason, err)
	return nil, RuleRuntimeModePolling, fallbackReason, nil
}

func (service *Service) decideWatchMode(ctx context.Context, rule *models.DirectoryUploadRule) watchModeDecision {
	if service == nil || service.watchModeDetector == nil {
		return decideWatchMode(ctx, rule, newOSWatchModeDetector())
	}
	return decideWatchMode(ctx, rule, service.watchModeDetector)
}

func (service *Service) startFSNotifyRuleWatcher(
	ctx context.Context,
	rule *models.DirectoryUploadRule,
	runtime *RuleRuntime,
) (RuleWatcher, error) {
	if err := terminalContextError(ctx, nil); err != nil {
		return nil, err
	}
	factory := service.watcherFactory
	if factory == nil {
		factory = NewFSNotifyRuleWatcher
	}
	watcher, err := factory(service, rule)
	if err != nil {
		return nil, err
	}
	if watcher == nil {
		return nil, errors.New("watcher 工厂返回空实例")
	}
	if setter, ok := watcher.(interface{ setRuntimeErrorRecorder(*RuleRuntime) }); ok {
		setter.setRuntimeErrorRecorder(runtime)
	}
	if err := watcher.Start(ctx); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	if err := terminalContextError(ctx, nil); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	return watcher, nil
}

func (service *Service) runPollingLoop(ctx context.Context, runtime *RuleRuntime, rule *models.DirectoryUploadRule) {
	interval := service.pollingInterval(rule)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.enqueuePollingSnapshotScan(ctx, runtime, rule, rule.MonitorPath, pollingSnapshotDiff)
		}
	}
}

func (service *Service) runStabilityLoop(ctx context.Context, rule *models.DirectoryUploadRule) {
	interval := service.stabilityInterval(rule)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			service.processStableFiles(ctx, rule)
		}
	}
}

func (service *Service) processStableFiles(ctx context.Context, rule *models.DirectoryUploadRule) {
	files, err := service.CheckStableFiles(rule)
	if err != nil {
		helpers.AppLogger.Warnf("[目录上传] 规则 %d 稳定性检查失败：%v", rule.ID, err)
		return
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := service.HandleStableFile(ctx, rule, file.Path); err != nil {
			helpers.AppLogger.Warnf("[目录上传] 规则 %d 创建上传任务失败：%v", rule.ID, err)
			if ctx.Err() == nil && shouldRequeueStableFile(err) {
				service.TrackPath(rule.ID, file.Path)
			}
		}
	}
}

func shouldRequeueStableFile(err error) bool {
	return err != nil && !errors.Is(err, errStableFileNoRetry) && !errors.Is(err, os.ErrNotExist)
}

func (service *Service) startProcessedCleanup(ctx context.Context) {
	service.cleanupProcessedOnce()

	cleanupCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	service.mutex.Lock()
	if service.cleanupCancel != nil {
		service.mutex.Unlock()
		cancel()
		return
	}
	service.cleanupCancel = cancel
	service.cleanupDone = done
	service.mutex.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(service.processedCleanupInterval())
		defer ticker.Stop()
		for {
			select {
			case <-cleanupCtx.Done():
				return
			case <-ticker.C:
				service.cleanupProcessedOnce()
			}
		}
	}()
}

func (service *Service) cleanupProcessedOnce() {
	now := service.now()
	service.cleanupExpiredMemoryCaches(now)
	cleaned, cleanupErr := CleanupCompletedStrmDependencies(200)
	if cleanupErr != nil {
		helpers.AppLogger.Warnf("[目录上传] 补偿已完成 STRM 源文件清理失败：%v", cleanupErr)
	}
	if cleaned > 0 {
		helpers.AppLogger.Infof("[目录上传] 补偿清理已完成 STRM 源文件 %d 个", cleaned)
	}

	deleted, err := models.CleanupDirectoryUploadProcessedFiles(now, service.processedMissingSourceTTL())
	if err != nil {
		helpers.AppLogger.Warnf("[目录上传] 清理 processed 账本失败：%v", err)
		return
	}
	if deleted > 0 {
		helpers.AppLogger.Infof("[目录上传] 清理 processed 账本记录 %d 条", deleted)
	}
}

func (service *Service) processedCleanupInterval() time.Duration {
	if service.cleanupInterval > 0 {
		return service.cleanupInterval
	}
	return defaultProcessedCleanupInterval
}

func (service *Service) processedMissingSourceTTL() time.Duration {
	if service.missingSourceTTL > 0 {
		return service.missingSourceTTL
	}
	return defaultProcessedMissingSourceTTL
}

func (service *Service) pollingInterval(_ *models.DirectoryUploadRule) time.Duration {
	if service.pollInterval > 0 {
		return service.pollInterval
	}
	return time.Duration(models.DirectoryUploadDefaultRescanIntervalSeconds) * time.Second
}

func (service *Service) stabilityInterval(_ *models.DirectoryUploadRule) time.Duration {
	if service.stabilityCheckInterval > 0 {
		return service.stabilityCheckInterval
	}
	return time.Duration(models.DirectoryUploadDefaultStabilityCheckIntervalSeconds) * time.Second
}

var globalService struct {
	sync.Mutex
	service *Service
	cancel  context.CancelFunc
}

// InitDirectoryUploadService 初始化目录监控上传服务。
func InitDirectoryUploadService() {
	if err := InitDirectoryUploadServiceWithError(); err != nil {
		helpers.AppLogger.Errorf("[目录上传] 初始化失败：%v", err)
	}
}

// InitDirectoryUploadServiceWithError 初始化目录监控上传服务并返回启动错误。
func InitDirectoryUploadServiceWithError() error {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return err
	}
	defer releaseAdmission()
	globalService.Lock()
	defer globalService.Unlock()
	if globalService.service != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := NewService(ServiceOptions{})
	if err := service.Start(ctx); err != nil {
		cancel()
		return err
	}
	globalService.service = service
	globalService.cancel = cancel
	return nil
}

// StopDirectoryUploadService 停止目录监控上传服务。
func StopDirectoryUploadService() {
	globalService.Lock()
	service := globalService.service
	cancel := globalService.cancel
	globalService.service = nil
	globalService.cancel = nil
	globalService.Unlock()
	if cancel != nil {
		cancel()
	}
	if service != nil {
		service.Stop()
	}
}

// ReloadDirectoryUploadService 重载运行中的目录监控上传服务。
func ReloadDirectoryUploadService() {
	if err := ReloadDirectoryUploadServiceWithError(); err != nil {
		helpers.AppLogger.Errorf("[目录上传] 重载失败：%v", err)
	}
}

// ReloadDirectoryUploadServiceWithError 重载目录监控上传服务并返回启动错误。
func ReloadDirectoryUploadServiceWithError() error {
	globalService.Lock()
	running := globalService.service != nil
	globalService.Unlock()
	if !running {
		return nil
	}
	StopDirectoryUploadService()
	return InitDirectoryUploadServiceWithError()
}

// ScanRuleNow 使用运行中的目录上传服务执行一次补偿扫描。
func ScanRuleNow(ctx context.Context, rule *models.DirectoryUploadRule) (int, error) {
	if taskgate.IsBlocked() {
		return 0, taskgate.ErrTaskAdmissionBlocked
	}
	globalService.Lock()
	service := globalService.service
	globalService.Unlock()
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	return service.ScanRule(ctx, rule)
}
