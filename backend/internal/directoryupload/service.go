package directoryupload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RemoteDirectory 是目录监控上传目标目录。
type RemoteDirectory struct {
	ID   string
	Path string
}

// RemoteFile 是目录监控上传前检查到的远端文件。
type RemoteFile struct {
	ID       string
	PickCode string
	SHA1     string
	Size     int64
	Mtime    int64
}

// RemoteClient 封装目录监控创建上传任务前需要的远端能力。
type RemoteClient interface {
	EnsureDir(ctx context.Context, rule *models.DirectoryUploadRule, relativeDir string) (RemoteDirectory, error)
	FindFile(ctx context.Context, parentID string, fileName string) (*RemoteFile, error)
	DeleteFile(ctx context.Context, parentID string, fileID string) error
}

var errStableFileNoRetry = errors.New("稳定文件不再重试")
var errPathResolvedOutsideMonitor = errors.New("文件真实路径越界")

var defaultIgnoredFileSuffixes = []string{
	".part",
	".tmp",
	".download",
	".aria2",
	".torrent",
}

var defaultIgnoredRecycleDirNames = map[string]struct{}{
	"@recycle": {},
	"#recycle": {},
	".trash":   {},
	".trashes": {},
}

// HandleStableFileOptions 是处理稳定文件的内部选项。
type HandleStableFileOptions struct {
	Force bool
}

type handleStableFileOptions = HandleStableFileOptions

// ServiceOptions 是目录监控上传服务依赖。
type ServiceOptions struct {
	Now                       func() time.Time
	RemoteClient              RemoteClient
	WatcherFactory            WatcherFactory
	watchModeDetector         watchModeDetector
	PollInterval              time.Duration
	StabilityCheckInterval    time.Duration
	ProcessedCleanupInterval  time.Duration
	ProcessedMissingSourceTTL time.Duration
}

type processedFile struct {
	expiresAt time.Time
}

type processedSourceState struct {
	scopeHash         string
	sourceKey         string
	sourceFingerprint string
	fileSize          int64
	mtimeNs           int64
}

type scanResult struct {
	Accepted int
	Snapshot map[string]string
}

type scanTrackFunc func(rel string, path string, fingerprint string, size int64)

type pollingSnapshotScanMode int

const (
	pollingSnapshotDiff pollingSnapshotScanMode = iota
	pollingSnapshotTrackAll
)

// Service 负责把稳定后的本地文件转换为上传队列任务。
type Service struct {
	now                    func() time.Time
	queue                  *StabilityQueue
	remoteClient           RemoteClient
	watcherFactory         WatcherFactory
	watchModeDetector      watchModeDetector
	pollInterval           time.Duration
	stabilityCheckInterval time.Duration
	cleanupInterval        time.Duration
	missingSourceTTL       time.Duration

	mutex          sync.Mutex
	processed      map[string]processedFile
	recentlyQueued map[string]processedFile
	runtimes       []*RuleRuntime
	scanExecutor   *scanExecutor
	cleanupCancel  context.CancelFunc
	cleanupDone    chan struct{}
}

// NewService 创建目录监控上传服务。
func NewService(options ServiceOptions) *Service {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	watchModeDetector := options.watchModeDetector
	if watchModeDetector == nil {
		watchModeDetector = newOSWatchModeDetector()
	}
	service := &Service{
		now:                    now,
		queue:                  NewStabilityQueue(StabilityQueueOptions{Now: now}),
		remoteClient:           options.RemoteClient,
		watcherFactory:         options.WatcherFactory,
		watchModeDetector:      watchModeDetector,
		pollInterval:           options.PollInterval,
		stabilityCheckInterval: options.StabilityCheckInterval,
		cleanupInterval:        options.ProcessedCleanupInterval,
		missingSourceTTL:       options.ProcessedMissingSourceTTL,
		processed:              make(map[string]processedFile),
		recentlyQueued:         make(map[string]processedFile),
	}
	service.scanExecutor = newScanExecutor(service.ScanSubtree)
	return service
}

// SetRemoteClient 设置远端客户端，主要供测试替换。
func (service *Service) SetRemoteClient(client RemoteClient) {
	if service == nil {
		return
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.remoteClient = client
}

// PendingPaths 返回指定规则当前待稳定文件路径。
func (service *Service) PendingPaths(ruleID uint) []string {
	if service == nil || service.queue == nil {
		return []string{}
	}
	return service.queue.PendingPaths(ruleID)
}

// TrackPath 将文件加入稳定性检查队列。
func (service *Service) TrackPath(ruleID uint, path string) {
	if service == nil || service.queue == nil {
		return
	}
	service.queue.Track(ruleID, path)
}

func (service *Service) trackCandidatePath(ctx context.Context, rule *models.DirectoryUploadRule, path string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if rule == nil {
		return false, errors.New("目录监控规则为空")
	}
	syncPath := models.GetSyncPathById(rule.SyncPathId)
	if syncPath == nil {
		return false, fmt.Errorf("同步目录不存在：%d", rule.SyncPathId)
	}
	info, err := statFileWithinMonitor(rule.MonitorPath, path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		if shouldSkipMonitorBoundaryError(err) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, nil
	}
	rel, err := safeRelativePath(rule.MonitorPath, path)
	if err != nil {
		return false, err
	}
	if !rule.Recursive && isNestedRelativePath(rel) {
		return false, nil
	}
	if shouldIgnorePath(rel, info.Name(), false, parseIgnorePatterns(rule.IgnorePatternsStr)) {
		return false, nil
	}
	if !shouldUploadByRule(rule, syncPath, info.Name()) {
		return false, nil
	}
	sourceFingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
	if service.trackRecentlyQueued(rule, rel, sourceFingerprint) {
		return false, nil
	}
	service.queue.Track(rule.ID, path)
	logDirectoryUploadCandidate(rule, "fsnotify", path, rel, sourceFingerprint, info.Size())
	return true, nil
}

// CheckStableFiles 执行指定规则的一轮稳定性检查。
func (service *Service) CheckStableFiles(rule *models.DirectoryUploadRule) ([]StableFile, error) {
	if service == nil || service.queue == nil {
		return []StableFile{}, nil
	}
	return service.queue.Check(rule)
}

// ScanRule 扫描目录上传规则，把候选视频文件加入稳定性队列。
func (service *Service) ScanRule(ctx context.Context, rule *models.DirectoryUploadRule) (int, error) {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	startedAt := service.now()
	root := ""
	if rule != nil {
		root = rule.MonitorPath
	}
	accepted, err := service.scanRoot(ctx, rule, root)
	if rule != nil {
		service.recordRuleRuntimeScan(rule.ID, startedAt, accepted, 0, err)
	}
	return accepted, err
}

// ScanSubtree 扫描监控目录下的新子目录，把候选文件加入稳定性队列。
func (service *Service) ScanSubtree(ctx context.Context, rule *models.DirectoryUploadRule, root string) (accepted int, err error) {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	startedAt := service.now()
	defer func() {
		if rule != nil {
			service.recordRuleRuntimeScan(rule.ID, startedAt, accepted, 0, err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if rule == nil {
		return 0, errors.New("目录监控规则为空")
	}
	if err := ensurePathInMonitor(rule.MonitorPath, root); err != nil {
		return 0, err
	}
	accepted, err = service.scanRoot(ctx, rule, root)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return accepted, nil
	}
	return accepted, err
}

// EnqueueScan 提交目录扫描任务，由共享执行器合并重复目录并限制并发。
func (service *Service) EnqueueScan(ctx context.Context, rule *models.DirectoryUploadRule, root string) {
	if service == nil || ctx == nil || ctx.Err() != nil || rule == nil || root == "" {
		return
	}
	executor := service.ensureScanExecutor()
	executor.Enqueue(ctx, scanRequest{rule: rule, root: root})
}

func (service *Service) enqueueRuntimeScan(ctx context.Context, runtime *RuleRuntime, rule *models.DirectoryUploadRule, root string) {
	if service == nil || runtime == nil || ctx == nil || ctx.Err() != nil || rule == nil || root == "" {
		return
	}
	executor := service.ensureScanExecutor()
	executor.Enqueue(ctx, scanRequest{
		rule: rule,
		root: root,
		scan: func(scanCtx context.Context, scanRule *models.DirectoryUploadRule, scanRoot string) (int, error) {
			return service.scanSubtreeWithRuntime(scanCtx, runtime, scanRule, scanRoot)
		},
	})
}

func (service *Service) enqueuePollingSnapshotScan(
	ctx context.Context,
	runtime *RuleRuntime,
	rule *models.DirectoryUploadRule,
	root string,
	mode pollingSnapshotScanMode,
) {
	if service == nil || runtime == nil || ctx == nil || ctx.Err() != nil || rule == nil || root == "" {
		return
	}
	executor := service.ensureScanExecutor()
	executor.Enqueue(ctx, scanRequest{
		rule: rule,
		root: root,
		scan: func(scanCtx context.Context, scanRule *models.DirectoryUploadRule, scanRoot string) (int, error) {
			return service.scanPollingSnapshot(scanCtx, runtime, scanRule, scanRoot, mode)
		},
	})
}

func (service *Service) ensureScanExecutor() *scanExecutor {
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.scanExecutor == nil {
		service.scanExecutor = newScanExecutor(service.ScanSubtree)
	}
	return service.scanExecutor
}

func (service *Service) scanRoot(ctx context.Context, rule *models.DirectoryUploadRule, root string) (int, error) {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	result, err := service.scanRootWithSnapshotAndTrack(ctx, rule, root, func(rel string, path string, fingerprint string, size int64) {
		service.queue.Track(rule.ID, path)
		logDirectoryUploadCandidate(rule, "scan", path, rel, fingerprint, size)
	})
	return result.Accepted, err
}

func (service *Service) scanRootWithSnapshot(ctx context.Context, rule *models.DirectoryUploadRule, root string) (scanResult, error) {
	return service.scanRootWithSnapshotAndTrack(ctx, rule, root, nil)
}

func (service *Service) scanPollingSnapshot(
	ctx context.Context,
	runtime *RuleRuntime,
	rule *models.DirectoryUploadRule,
	root string,
	mode pollingSnapshotScanMode,
) (int, error) {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	startedAt := service.now()
	result, err := service.scanRootWithSnapshot(ctx, rule, root)
	tracked := service.applyPollingSnapshotResult(runtime, rule, result, mode, err == nil)
	runtime.recordScan(service.now(), startedAt, result.Accepted, result.Accepted-tracked, err)
	return tracked, err
}

func (service *Service) scanSubtreeWithRuntime(
	ctx context.Context,
	runtime *RuleRuntime,
	rule *models.DirectoryUploadRule,
	root string,
) (int, error) {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	startedAt := service.now()
	if err := ctx.Err(); err != nil {
		runtime.recordScan(service.now(), startedAt, 0, 0, err)
		return 0, err
	}
	if rule == nil {
		err := errors.New("目录监控规则为空")
		runtime.recordScan(service.now(), startedAt, 0, 0, err)
		return 0, err
	}
	if err := ensurePathInMonitor(rule.MonitorPath, root); err != nil {
		runtime.recordScan(service.now(), startedAt, 0, 0, err)
		return 0, err
	}
	accepted, err := service.scanRoot(ctx, rule, root)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	runtime.recordScan(service.now(), startedAt, accepted, 0, err)
	return accepted, err
}

func (service *Service) applyPollingSnapshotResult(
	runtime *RuleRuntime,
	rule *models.DirectoryUploadRule,
	result scanResult,
	mode pollingSnapshotScanMode,
	replaceSnapshot bool,
) int {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	if service.queue == nil || runtime == nil || rule == nil {
		return 0
	}
	previous := runtime.snapshot()
	snapshot := make(map[string]string, len(result.Snapshot))
	localPaths := make(map[string]string, len(result.Snapshot))
	for rel, fingerprint := range result.Snapshot {
		cleanRel, localPath, ok := localPathFromSnapshotRel(rule.MonitorPath, rel)
		if !ok {
			helpers.AppLogger.Warnf("[目录上传] 规则 %d polling snapshot 跳过越界相对路径：%s", rule.ID, rel)
			continue
		}
		snapshot[cleanRel] = fingerprint
		localPaths[cleanRel] = localPath
	}
	if replaceSnapshot {
		runtime.replaceSnapshot(snapshot)
	}

	tracked := 0
	for rel, fingerprint := range snapshot {
		if mode == pollingSnapshotDiff {
			if previousFingerprint, ok := previous[rel]; ok && previousFingerprint == fingerprint {
				continue
			}
		}
		service.queue.Track(rule.ID, localPaths[rel])
		logDirectoryUploadCandidate(rule, "polling", localPaths[rel], rel, fingerprint, directoryUploadFingerprintSize(fingerprint))
		tracked++
	}
	return tracked
}

func localPathFromSnapshotRel(monitorPath string, rel string) (string, string, bool) {
	monitorPath = filepath.Clean(monitorPath)
	rel = pathpkg.Clean(rel)
	if pathpkg.IsAbs(rel) {
		return "", "", false
	}
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", "", false
	}
	if os.PathSeparator == '\\' && strings.Contains(rel, "\\") {
		return "", "", false
	}
	if rel == "" || rel == "." {
		return ".", monitorPath, true
	}
	localPath := filepath.Clean(filepath.Join(monitorPath, filepath.FromSlash(rel)))
	if err := ensurePathInMonitor(monitorPath, localPath); err != nil {
		return "", "", false
	}
	return rel, localPath, true
}

func logDirectoryUploadCandidate(rule *models.DirectoryUploadRule, source string, filePath string, rel string, fingerprint string, size int64) {
	if helpers.AppLogger == nil || rule == nil {
		return
	}
	helpers.AppLogger.Infof(
		"[目录上传] 监控到候选文件：rule_id=%d source=%s path=%s relative_path=%s size=%d fingerprint=%s",
		rule.ID,
		source,
		filePath,
		filepath.ToSlash(rel),
		size,
		fingerprint,
	)
}

func directoryUploadFingerprintSize(fingerprint string) int64 {
	parts := strings.Split(fingerprint, ":")
	if len(parts) < 3 || parts[0] != "v1" {
		return 0
	}
	size, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0
	}
	return size
}

func (service *Service) scanRootWithSnapshotAndTrack(
	ctx context.Context,
	rule *models.DirectoryUploadRule,
	root string,
	track scanTrackFunc,
) (scanResult, error) {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	syncPath, monitorPath, root, err := service.validateScanRoot(ctx, rule, root)
	if err != nil {
		return scanResult{Snapshot: map[string]string{}}, err
	}

	result := scanResult{Snapshot: map[string]string{}}
	ignorePatterns := parseIgnorePatterns(rule.IgnorePatternsStr)
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := relativePathInMonitor(monitorPath, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if rel != "." {
				if shouldIgnorePath(rel, entry.Name(), true, ignorePatterns) {
					return filepath.SkipDir
				}
				if !rule.Recursive {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !rule.Recursive && isNestedRelativePath(rel) {
			return nil
		}
		if shouldIgnorePath(rel, entry.Name(), false, ignorePatterns) || !shouldUploadByRule(rule, syncPath, entry.Name()) {
			return nil
		}
		// 使用 os.Stat 跟随 symlink，保持 snapshot fingerprint 与稳定性处理语义一致。
		info, err := statFileWithinMonitor(monitorPath, path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			if shouldSkipMonitorBoundaryError(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		fingerprint := models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano())
		result.Snapshot[rel] = fingerprint
		result.Accepted++
		if track != nil {
			track(rel, path, fingerprint, info.Size())
		}
		return nil
	})
	if walkErr != nil {
		return result, walkErr
	}
	return result, nil
}

func (service *Service) validateScanRoot(ctx context.Context, rule *models.DirectoryUploadRule, root string) (*models.SyncPath, string, string, error) {
	if ctx == nil {
		return nil, "", "", errors.New("扫描上下文为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	if rule == nil {
		return nil, "", "", errors.New("目录监控规则为空")
	}
	syncPath := models.GetSyncPathById(rule.SyncPathId)
	if syncPath == nil {
		return nil, "", "", fmt.Errorf("同步目录不存在：%d", rule.SyncPathId)
	}
	if strings.TrimSpace(rule.MonitorPath) == "" {
		return nil, "", "", errors.New("监控目录不能为空")
	}
	if strings.TrimSpace(root) == "" {
		return nil, "", "", errors.New("扫描目录不能为空")
	}
	monitorPath := filepath.Clean(rule.MonitorPath)
	root = filepath.Clean(root)
	if err := ensurePathInMonitor(monitorPath, root); err != nil {
		return nil, "", "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, "", "", fmt.Errorf("读取监控目录失败：%w", err)
	}
	if !info.IsDir() {
		return nil, "", "", fmt.Errorf("监控路径不是目录：%s", root)
	}
	if _, err := ensurePathResolvesWithinMonitor(monitorPath, root); err != nil {
		return nil, "", "", err
	}
	return syncPath, monitorPath, root, nil
}

// HandleStableFile 为已稳定的本地文件创建上传任务。
func (service *Service) HandleStableFile(ctx context.Context, rule *models.DirectoryUploadRule, filePath string) error {
	return service.handleStableFile(ctx, rule, filePath, handleStableFileOptions{})
}

func (service *Service) handleStableFile(ctx context.Context, rule *models.DirectoryUploadRule, filePath string, options handleStableFileOptions) error {
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if rule == nil {
		return errors.New("目录监控规则为空")
	}
	filePath = filepath.Clean(filePath)
	info, err := statFileWithinMonitor(rule.MonitorPath, filePath)
	if err != nil {
		if errors.Is(err, errPathResolvedOutsideMonitor) {
			return fmt.Errorf("%w：%w", errStableFileNoRetry, err)
		}
		return fmt.Errorf("读取稳定文件失败：%w", err)
	}
	if info.IsDir() {
		return nil
	}
	syncPath := models.GetSyncPathById(rule.SyncPathId)
	if syncPath == nil {
		return fmt.Errorf("%w：同步目录不存在：%d", errStableFileNoRetry, rule.SyncPathId)
	}
	if !shouldUploadByRule(rule, syncPath, info.Name()) {
		return nil
	}
	rel, err := safeRelativePath(rule.MonitorPath, filePath)
	if err != nil {
		return err
	}
	plan, err := service.buildTriggerPlan(ctx, rule, rel, filePath, info, options)
	if err != nil {
		return err
	}
	if plan.skip {
		return nil
	}
	sourceState := plan.sourceState

	relativeDir := filepath.ToSlash(filepath.Dir(rel))
	if relativeDir == "." {
		relativeDir = ""
	}
	remoteClient, err := service.remote(ctx, rule)
	if err != nil {
		return err
	}
	remoteDir, err := remoteClient.EnsureDir(ctx, rule, relativeDir)
	if err != nil {
		return fmt.Errorf("确认远端目录失败：%w", err)
	}
	fileName := info.Name()
	remoteFilePath := cleanRemoteFilePath(rule.RemoteRootPath, rel)
	task := &models.DbUploadTask{
		Source:              models.UploadSourceDirectoryMonitor,
		AccountId:           rule.AccountId,
		SyncPathId:          rule.SyncPathId,
		SourceType:          models.SourceType115,
		LocalFullPath:       filePath,
		RelativePath:        filepath.ToSlash(rel),
		SourceFingerprint:   sourceState.sourceFingerprint,
		RemoteFullPath:      remoteFilePath,
		RemotePathId:        remoteDir.ID,
		FileName:            fileName,
		Status:              models.UploadStatusPending,
		FileSize:            sourceState.fileSize,
		LocalMtime:          info.ModTime().Unix(),
		LocalMtimeNs:        sourceState.mtimeNs,
		UploadResult:        models.UploadResultUnknown,
		SourceCleanupStatus: cleanupInitialStatus(rule),
	}

	remoteFile, err := remoteClient.FindFile(ctx, remoteDir.ID, fileName)
	if err != nil {
		return fmt.Errorf("检查远端同名文件失败：%w", err)
	}
	if err := ensureStableFileStillWithinMonitor(rule.MonitorPath, filePath); err != nil {
		return err
	}
	if remoteFile != nil && remoteFile.ID != "" {
		if isSameRemoteFile(remoteFile, filePath, info.Size()) {
			task.Status = models.UploadStatusCompleted
			task.UploadResult = models.UploadResultRemoteExists
			task.UploadedBytes = info.Size()
			task.RemoteFileId = remoteFile.ID
			task.RemotePickCode = remoteFile.PickCode
			task.RemoteSha1 = remoteFile.SHA1
			task.EndTime = service.now().Unix()
			created, err := service.createDirectoryUploadTaskWithProcessedClaim(task, rule, rel, filePath, sourceState, options)
			if err != nil {
				return err
			}
			if !created {
				return nil
			}
			if err := task.EnqueueStrmGenerationAfterUploadAndMarkDirectoryProcessed(); err != nil {
				return fmt.Errorf("创建 STRM 生成任务失败：%w", err)
			}
			logDirectoryUploadRemoteExistsStrmTask(rule, task, latestStrmGenerationTaskIDForUpload(task.ID))
			service.markProcessed(rule, rel, sourceState.sourceFingerprint)
			return nil
		}

		switch rule.OverwriteMode {
		case "", models.DirectoryUploadOverwriteSkipSame:
			if err := service.upsertDirectoryUploadProcessed(rule, rel, filePath, sourceState, models.DirectoryUploadProcessedResultSkippedExisting, 0); err != nil {
				return fmt.Errorf("记录跳过已有远端文件失败：%w", err)
			}
			service.markProcessed(rule, rel, sourceState.sourceFingerprint)
			return nil
		case models.DirectoryUploadOverwriteFailConflict:
			if err := service.upsertDirectoryUploadProcessed(rule, rel, filePath, sourceState, models.DirectoryUploadProcessedResultFailed, 0); err != nil {
				return fmt.Errorf("记录远端同名冲突失败：%w", err)
			}
			return fmt.Errorf("%w：远端已存在同名文件且大小或 SHA1 不一致：%s", errStableFileNoRetry, remoteFilePath)
		case models.DirectoryUploadOverwriteReplaceConflict:
			if err := service.writePendingReplaceProcessed(rule, rel, filePath, sourceState); err != nil {
				return fmt.Errorf("记录待覆盖远端文件失败：%w", err)
			}
			if err := remoteClient.DeleteFile(ctx, remoteDir.ID, remoteFile.ID); err != nil {
				return fmt.Errorf("删除远端同名文件失败：%w", err)
			}
			task.ReplacedRemoteFileId = remoteFile.ID
		default:
			return fmt.Errorf("%w：不支持的同名文件处理方式：%s", errStableFileNoRetry, rule.OverwriteMode)
		}
	}

	if err := ensureStableFileStillWithinMonitor(rule.MonitorPath, filePath); err != nil {
		return err
	}
	created, err := service.createDirectoryUploadTaskWithProcessedClaim(task, rule, rel, filePath, sourceState, options)
	if err != nil {
		return err
	}
	if !created {
		return nil
	}
	return nil
}

func (service *Service) retryDirectoryUploadStrmEnqueue(uploadTaskID uint) error {
	if uploadTaskID == 0 {
		return errors.New("目录监控上传任务 ID 为空")
	}
	var task models.DbUploadTask
	if err := db.Db.First(&task, uploadTaskID).Error; err != nil {
		return fmt.Errorf("读取目录监控上传任务失败：%w", err)
	}
	if task.Source != models.UploadSourceDirectoryMonitor {
		return fmt.Errorf("上传任务来源不是目录监控：%s", task.Source)
	}
	if task.Status != models.UploadStatusCompleted {
		return fmt.Errorf("目录监控上传任务未完成：%s", task.Status.String())
	}
	return task.EnqueueStrmGenerationAfterUploadAndMarkDirectoryProcessed()
}

func buildProcessedSourceState(rule *models.DirectoryUploadRule, rel string, info os.FileInfo) processedSourceState {
	mtimeNs := info.ModTime().UnixNano()
	scopeHash := models.BuildDirectoryUploadScopeHash(rule)
	return processedSourceState{
		scopeHash:         scopeHash,
		sourceKey:         models.BuildDirectoryUploadSourceKey(scopeHash, rel),
		sourceFingerprint: models.BuildDirectoryUploadSourceFingerprint(info.Size(), mtimeNs),
		fileSize:          info.Size(),
		mtimeNs:           mtimeNs,
	}
}

func updateDirectoryUploadProcessedLastSeen(sourceKey string, lastSeenAt int64) error {
	return db.Db.Model(&models.DirectoryUploadProcessedFile{}).
		Where("source_key = ?", sourceKey).
		Updates(map[string]any{
			"last_seen_at": lastSeenAt,
		}).Error
}

func deleteDirectoryUploadProcessedIfUnchanged(record *models.DirectoryUploadProcessedFile) (bool, error) {
	if record == nil {
		return false, nil
	}
	result := db.Db.
		Where("source_key = ? AND source_fingerprint = ? AND result = ? AND upload_task_id = ?",
			record.SourceKey,
			record.SourceFingerprint,
			record.Result,
			record.UploadTaskId,
		).
		Delete(&models.DirectoryUploadProcessedFile{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func hasActiveDirectoryUploadTask(uploadTaskID uint, filePath string) (bool, error) {
	return hasActiveDirectoryUploadTaskWithDB(nil, uploadTaskID, filePath)
}

func hasActiveDirectoryUploadTaskWithDB(tx *gorm.DB, uploadTaskID uint, filePath string) (bool, error) {
	handle := tx
	if handle == nil {
		handle = db.Db
	}
	query := handle.Model(&models.DbUploadTask{}).
		Where("status IN ?", []models.UploadStatus{
			models.UploadStatusPending,
			models.UploadStatusUploading,
			models.UploadStatusRemoteCompletedPendingFinalize,
			models.UploadStatusRemoteCompletedFinalizing,
		})
	if uploadTaskID > 0 {
		query = query.Where("id = ?", uploadTaskID)
	} else {
		query = query.Where("source = ? AND local_full_path = ?", models.UploadSourceDirectoryMonitor, filePath)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return false, err
	}
	return total > 0, nil
}

func (service *Service) createDirectoryUploadTaskWithProcessedClaim(task *models.DbUploadTask, rule *models.DirectoryUploadRule, rel string, filePath string, state processedSourceState, options ...handleStableFileOptions) (bool, error) {
	var option handleStableFileOptions
	if len(options) > 0 {
		option = options[0]
	}
	var created bool
	err := db.Db.Transaction(func(tx *gorm.DB) error {
		claimed, supersededUploadTaskID, err := service.claimDirectoryUploadProcessedWithDB(tx, rule, rel, filePath, state, option)
		if err != nil || !claimed {
			return err
		}
		if err := models.SaveDirectoryMonitorUploadTaskWithDB(tx, task); err != nil {
			return err
		}
		if err := service.upsertDirectoryUploadProcessedWithDB(tx, rule, rel, filePath, state, models.DirectoryUploadProcessedResultQueued, task.ID); err != nil {
			return err
		}
		if err := service.cancelSupersededFailedUploadTaskWithDB(tx, supersededUploadTaskID, task.ID); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("创建目录监控上传任务失败：%w", err)
	}
	if created {
		models.PublishUploadTaskCreated(task)
		logDirectoryUploadTaskCreated(rule, task)
	}
	return created, nil
}

func logDirectoryUploadTaskCreated(rule *models.DirectoryUploadRule, task *models.DbUploadTask) {
	if helpers.AppLogger == nil || rule == nil || task == nil || task.Status != models.UploadStatusPending {
		return
	}
	helpers.AppLogger.Infof(
		"[目录上传] 已创建上传任务：rule_id=%d upload_task_id=%d path=%s remote_path=%s remote_parent_id=%s size=%d cleanup_status=%s",
		rule.ID,
		task.ID,
		task.LocalFullPath,
		task.RemoteFullPath,
		task.RemotePathId,
		task.FileSize,
		task.SourceCleanupStatus,
	)
}

func logDirectoryUploadRemoteExistsStrmTask(rule *models.DirectoryUploadRule, task *models.DbUploadTask, strmTaskID uint) {
	if helpers.AppLogger == nil || rule == nil || task == nil {
		return
	}
	helpers.AppLogger.Infof(
		"[目录上传] 远端已存在同内容文件，已创建 STRM 后处理任务：rule_id=%d upload_task_id=%d strm_generation_task_id=%d path=%s remote_path=%s remote_file_id=%s",
		rule.ID,
		task.ID,
		strmTaskID,
		task.LocalFullPath,
		task.RemoteFullPath,
		task.RemoteFileId,
	)
}

func latestStrmGenerationTaskIDForUpload(uploadTaskID uint) uint {
	if uploadTaskID == 0 {
		return 0
	}
	var task models.StrmGenerationTask
	if err := db.Db.Where("upload_task_id = ?", uploadTaskID).Order("id DESC").First(&task).Error; err != nil {
		return 0
	}
	return task.ID
}

func (service *Service) claimDirectoryUploadProcessedWithDB(tx *gorm.DB, rule *models.DirectoryUploadRule, rel string, filePath string, state processedSourceState, options handleStableFileOptions) (bool, uint, error) {
	claim := service.newDirectoryUploadProcessedRecord(rule, rel, filePath, state, models.DirectoryUploadProcessedResultQueued, 0)
	insert := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_key"}},
		DoNothing: true,
	}).Create(claim)
	if insert.Error != nil {
		return false, 0, insert.Error
	}
	if insert.RowsAffected > 0 {
		return true, 0, nil
	}

	var existing models.DirectoryUploadProcessedFile
	if err := tx.Where("source_key = ?", state.sourceKey).First(&existing).Error; err != nil {
		return false, 0, err
	}
	if existing.SourceFingerprint != state.sourceFingerprint ||
		existing.Result == models.DirectoryUploadProcessedResultFailed {
		claimed, err := service.updateDirectoryUploadProcessedClaimWithDB(tx, &existing, claim)
		if err != nil || claimed {
			return claimed, existing.UploadTaskId, err
		}
		return false, 0, nil
	}
	if existing.Result == models.DirectoryUploadProcessedResultPendingReplace {
		claimed, err := service.updateDirectoryUploadProcessedClaimWithDB(tx, &existing, claim)
		if err != nil || claimed {
			return claimed, existing.UploadTaskId, err
		}
		return false, 0, nil
	}
	if existing.Result == models.DirectoryUploadProcessedResultQueued {
		active, err := hasActiveDirectoryUploadTaskWithDB(tx, existing.UploadTaskId, filePath)
		if err != nil || active {
			return false, 0, err
		}
		claimed, err := service.updateDirectoryUploadProcessedClaimWithDB(tx, &existing, claim)
		if err != nil || claimed {
			return claimed, existing.UploadTaskId, err
		}
		return false, 0, nil
	}
	if options.Force {
		active, err := hasActiveDirectoryUploadTaskWithDB(tx, existing.UploadTaskId, filePath)
		if err != nil || active {
			return false, 0, err
		}
		claimed, err := service.updateDirectoryUploadProcessedClaimWithDB(tx, &existing, claim)
		if err != nil || claimed {
			return claimed, existing.UploadTaskId, err
		}
		return false, 0, nil
	}
	return false, 0, nil
}

func (service *Service) cancelSupersededFailedUploadTaskWithDB(tx *gorm.DB, oldUploadTaskID uint, newUploadTaskID uint) error {
	if oldUploadTaskID == 0 || oldUploadTaskID == newUploadTaskID {
		return nil
	}
	now := service.now().Unix()
	result := tx.Model(&models.DbUploadTask{}).
		Where("id = ? AND source = ? AND status = ?", oldUploadTaskID, models.UploadSourceDirectoryMonitor, models.UploadStatusFailed).
		Updates(map[string]any{
			"status":     models.UploadStatusCancelled,
			"error":      fmt.Sprintf("已被新的目录监控上传任务 %d 替代", newUploadTaskID),
			"end_time":   now,
			"updated_at": now,
		})
	return result.Error
}

func (service *Service) updateDirectoryUploadProcessedClaimWithDB(tx *gorm.DB, existing *models.DirectoryUploadProcessedFile, claim *models.DirectoryUploadProcessedFile) (bool, error) {
	result := tx.Model(&models.DirectoryUploadProcessedFile{}).
		Where("source_key = ? AND source_fingerprint = ? AND result = ? AND upload_task_id = ?",
			existing.SourceKey,
			existing.SourceFingerprint,
			existing.Result,
			existing.UploadTaskId,
		).
		Updates(map[string]any{
			"rule_id":            claim.RuleId,
			"sync_path_id":       claim.SyncPathId,
			"account_id":         claim.AccountId,
			"scope_hash":         claim.ScopeHash,
			"relative_path":      claim.RelativePath,
			"local_full_path":    claim.LocalFullPath,
			"source_fingerprint": claim.SourceFingerprint,
			"file_size":          claim.FileSize,
			"local_mtime_ns":     claim.LocalMtimeNs,
			"result":             claim.Result,
			"upload_task_id":     claim.UploadTaskId,
			"processed_at":       claim.ProcessedAt,
			"last_seen_at":       claim.LastSeenAt,
			"updated_at":         claim.UpdatedAt,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (service *Service) upsertDirectoryUploadProcessed(rule *models.DirectoryUploadRule, rel string, filePath string, state processedSourceState, result models.DirectoryUploadProcessedResult, uploadTaskID uint) error {
	return service.upsertDirectoryUploadProcessedWithDB(db.Db, rule, rel, filePath, state, result, uploadTaskID)
}

func (service *Service) writePendingReplaceProcessed(rule *models.DirectoryUploadRule, rel string, filePath string, state processedSourceState) error {
	return db.Db.Transaction(func(tx *gorm.DB) error {
		return service.upsertDirectoryUploadProcessedWithDB(tx, rule, rel, filePath, state, models.DirectoryUploadProcessedResultPendingReplace, 0)
	})
}

func (service *Service) upsertDirectoryUploadProcessedWithDB(tx *gorm.DB, rule *models.DirectoryUploadRule, rel string, filePath string, state processedSourceState, result models.DirectoryUploadProcessedResult, uploadTaskID uint) error {
	record := service.newDirectoryUploadProcessedRecord(rule, rel, filePath, state, result, uploadTaskID)
	return models.UpsertDirectoryUploadProcessedFileWithDB(tx, record)
}

func (service *Service) newDirectoryUploadProcessedRecord(rule *models.DirectoryUploadRule, rel string, filePath string, state processedSourceState, result models.DirectoryUploadProcessedResult, uploadTaskID uint) *models.DirectoryUploadProcessedFile {
	now := service.now().Unix()
	return &models.DirectoryUploadProcessedFile{
		RuleId:            rule.ID,
		SyncPathId:        rule.SyncPathId,
		AccountId:         rule.AccountId,
		ScopeHash:         state.scopeHash,
		SourceKey:         state.sourceKey,
		RelativePath:      filepath.ToSlash(rel),
		LocalFullPath:     filePath,
		SourceFingerprint: state.sourceFingerprint,
		FileSize:          state.fileSize,
		LocalMtimeNs:      state.mtimeNs,
		Result:            result,
		UploadTaskId:      uploadTaskID,
		ProcessedAt:       now,
		LastSeenAt:        now,
	}
}

func (service *Service) remote(ctx context.Context, rule *models.DirectoryUploadRule) (RemoteClient, error) {
	service.mutex.Lock()
	client := service.remoteClient
	service.mutex.Unlock()
	if client != nil {
		return client, nil
	}
	return newOpen115RemoteClient(ctx, rule)
}

func shouldUploadByRule(rule *models.DirectoryUploadRule, syncPath *models.SyncPath, name string) bool {
	if syncPath == nil {
		return false
	}
	if syncPath.IsValidVideoExt(name) {
		return true
	}
	return rule != nil && rule.UploadMetadata && syncPath.IsValidMetaExt(name)
}

func (service *Service) isProcessed(rule *models.DirectoryUploadRule, rel string, signature string) bool {
	key := processedKey(rule.ID, rel, signature)
	now := service.now()
	service.mutex.Lock()
	defer service.mutex.Unlock()
	item, ok := service.processed[key]
	if !ok {
		return false
	}
	if !item.expiresAt.IsZero() && now.After(item.expiresAt) {
		delete(service.processed, key)
		return false
	}
	return true
}

func (service *Service) markProcessed(rule *models.DirectoryUploadRule, rel string, signature string) {
	key := processedKey(rule.ID, rel, signature)
	ttl := time.Duration(rule.ProcessedCacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	service.processed[key] = processedFile{expiresAt: service.now().Add(ttl)}
}

func (service *Service) trackRecentlyQueued(rule *models.DirectoryUploadRule, rel string, signature string) bool {
	if service == nil || rule == nil {
		return false
	}
	key := processedKey(rule.ID, rel, signature)
	ttl := time.Duration(rule.ProcessedCacheTTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	now := time.Now()
	if service.now != nil {
		now = service.now()
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	if service.recentlyQueued == nil {
		service.recentlyQueued = make(map[string]processedFile)
	}
	cleanupExpiredProcessedFileMap(service.recentlyQueued, now)
	if item, ok := service.recentlyQueued[key]; ok {
		if item.expiresAt.IsZero() || now.Before(item.expiresAt) || now.Equal(item.expiresAt) {
			return true
		}
		delete(service.recentlyQueued, key)
	}
	service.recentlyQueued[key] = processedFile{expiresAt: now.Add(ttl)}
	return false
}

func (service *Service) cleanupExpiredMemoryCaches(now time.Time) {
	if service == nil {
		return
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	cleanupExpiredProcessedFileMap(service.processed, now)
	cleanupExpiredProcessedFileMap(service.recentlyQueued, now)
}

func cleanupExpiredProcessedFileMap(items map[string]processedFile, now time.Time) {
	for key, file := range items {
		if !file.expiresAt.IsZero() && now.After(file.expiresAt) {
			delete(items, key)
		}
	}
}

func processedKey(ruleID uint, rel string, sourceFingerprint string) string {
	return fmt.Sprintf("%d:%s:%s", ruleID, filepath.ToSlash(strings.ReplaceAll(rel, "\\", "/")), sourceFingerprint)
}

func cleanupInitialStatus(rule *models.DirectoryUploadRule) models.UploadSourceCleanupStatus {
	if rule != nil && rule.DeleteSourceAfterSuccess {
		return models.UploadSourceCleanupStatusPending
	}
	return models.UploadSourceCleanupStatusNone
}

func cleanRemoteFilePath(rootPath string, rel string) string {
	rootPath = strings.ReplaceAll(strings.TrimSpace(rootPath), "\\", "/")
	if rootPath == "" {
		rootPath = "/"
	}
	if !strings.HasPrefix(rootPath, "/") {
		rootPath = "/" + rootPath
	}
	parts := []string{rootPath}
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return pathpkg.Clean(pathpkg.Join(parts...))
}

func safeRelativePath(basePath string, path string) (string, error) {
	basePath = filepath.Clean(basePath)
	path = filepath.Clean(path)
	rel, err := filepath.Rel(basePath, path)
	if err != nil {
		return "", fmt.Errorf("计算相对路径失败：%w", err)
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("文件路径越界：%s", path)
	}
	return filepath.ToSlash(rel), nil
}

func statFileWithinMonitor(monitorPath string, path string) (os.FileInfo, error) {
	if _, err := ensurePathResolvesWithinMonitor(monitorPath, path); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if _, err := ensurePathResolvesWithinMonitor(monitorPath, path); err != nil {
		return nil, err
	}
	return info, nil
}

func ensureStableFileStillWithinMonitor(monitorPath string, path string) error {
	if _, err := ensurePathResolvesWithinMonitor(monitorPath, path); err != nil {
		if errors.Is(err, errPathResolvedOutsideMonitor) {
			return fmt.Errorf("%w：%w", errStableFileNoRetry, err)
		}
		return fmt.Errorf("校验稳定文件真实路径失败：%w", err)
	}
	return nil
}

func ensurePathResolvesWithinMonitor(monitorPath string, path string) (string, error) {
	monitorPath = filepath.Clean(monitorPath)
	path = filepath.Clean(path)
	if strings.TrimSpace(monitorPath) == "" {
		return "", errors.New("监控目录不能为空")
	}
	if _, err := relativePathInMonitor(monitorPath, path); err != nil {
		return "", err
	}
	realMonitorPath, err := filepath.EvalSymlinks(monitorPath)
	if err != nil {
		return "", fmt.Errorf("解析监控目录真实路径失败：%w", err)
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("解析源文件真实路径失败：%w", err)
	}
	realMonitorPath = filepath.Clean(realMonitorPath)
	realPath = filepath.Clean(realPath)
	rel, err := filepath.Rel(realMonitorPath, realPath)
	if err != nil {
		return "", fmt.Errorf("计算真实路径边界失败：%w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("%w：%s -> %s", errPathResolvedOutsideMonitor, path, realPath)
	}
	return realPath, nil
}

func shouldSkipMonitorBoundaryError(err error) bool {
	return errors.Is(err, errPathResolvedOutsideMonitor) || errors.Is(err, os.ErrNotExist)
}

func ensurePathInMonitor(monitorPath string, path string) error {
	_, err := relativePathInMonitor(monitorPath, path)
	return err
}

func relativePathInMonitor(monitorPath string, path string) (string, error) {
	monitorPath = filepath.Clean(monitorPath)
	path = filepath.Clean(path)
	if path == monitorPath {
		return ".", nil
	}
	return safeRelativePath(monitorPath, path)
}

func isNestedRelativePath(rel string) bool {
	rel = pathpkg.Clean(filepath.ToSlash(rel))
	return rel != "." && pathpkg.Dir(rel) != "."
}

func parseIgnorePatterns(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var patterns []string
	if err := json.Unmarshal([]byte(raw), &patterns); err == nil {
		return compactPatterns(patterns)
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	return compactPatterns(fields)
}

func compactPatterns(patterns []string) []string {
	result := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern != "" {
			result = append(result, pattern)
		}
	}
	return result
}

func shouldIgnorePath(rel string, name string, isDir bool, patterns []string) bool {
	if name == "" {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	lowerName := strings.ToLower(name)
	if isDir && isDefaultRecycleDirName(lowerName) {
		return true
	}
	for _, suffix := range defaultIgnoredFileSuffixes {
		if strings.HasSuffix(lowerName, suffix) {
			return true
		}
	}
	rel = filepath.ToSlash(rel)
	for _, pattern := range patterns {
		if matched, _ := pathpkg.Match(pattern, rel); matched {
			return true
		}
		if matched, _ := pathpkg.Match(pattern, name); matched {
			return true
		}
		if isDir && strings.TrimSuffix(pattern, "/") == rel {
			return true
		}
	}
	return false
}

func isDefaultRecycleDirName(lowerName string) bool {
	_, ok := defaultIgnoredRecycleDirNames[lowerName]
	return ok
}

func isSameRemoteFile(remoteFile *RemoteFile, localPath string, size int64) bool {
	if remoteFile == nil || remoteFile.ID == "" {
		return false
	}
	if remoteFile.Size != size {
		return false
	}
	if strings.TrimSpace(remoteFile.SHA1) == "" {
		return false
	}
	localSHA1, err := helpers.FileSHA1(localPath)
	if err != nil {
		return false
	}
	return strings.EqualFold(remoteFile.SHA1, localSHA1)
}

type open115RemoteClient struct {
	client *v115open.OpenClient
}

func newOpen115RemoteClient(_ context.Context, rule *models.DirectoryUploadRule) (RemoteClient, error) {
	if rule == nil {
		return nil, errors.New("目录监控规则为空")
	}
	account, err := models.GetAccountById(rule.AccountId)
	if err != nil {
		return nil, fmt.Errorf("获取 115 账号失败：%w", err)
	}
	if account == nil {
		return nil, fmt.Errorf("账号不存在：%d", rule.AccountId)
	}
	client := account.Get115Client()
	if client == nil {
		return nil, fmt.Errorf("账号 %d 未初始化 115 客户端", rule.AccountId)
	}
	return &open115RemoteClient{client: client}, nil
}

func (client *open115RemoteClient) EnsureDir(ctx context.Context, rule *models.DirectoryUploadRule, relativeDir string) (RemoteDirectory, error) {
	if client == nil || client.client == nil {
		return RemoteDirectory{}, errors.New("115 远端客户端为空")
	}
	if rule == nil {
		return RemoteDirectory{}, errors.New("目录监控规则为空")
	}
	parentID := rule.RemoteRootId
	if parentID == "" {
		return RemoteDirectory{}, errors.New("远端根目录 ID 为空")
	}
	rootPath := strings.TrimSpace(rule.RemoteRootPath)
	if rootPath == "" {
		rootPath = "/"
	}
	relativeDir = strings.Trim(filepath.ToSlash(relativeDir), "/")
	if relativeDir == "" || relativeDir == "." {
		return RemoteDirectory{ID: parentID, Path: cleanRemoteFilePath(rootPath, "")}, nil
	}

	currentPath := strings.TrimSuffix(cleanRemoteFilePath(rootPath, ""), "/")
	if currentPath == "." {
		currentPath = "/"
	}
	for _, segment := range strings.Split(relativeDir, "/") {
		segment = strings.TrimSpace(segment)
		if segment == "" || segment == "." {
			continue
		}
		nextPath := cleanRemoteFilePath(currentPath, segment)
		detail, err := client.client.GetFsDetailByPath(ctx, nextPath)
		if err == nil && detail != nil && detail.FileId != "" {
			parentID = detail.FileId
			currentPath = nextPath
			continue
		}
		createdID, err := client.client.MkDir(ctx, parentID, segment)
		if err != nil {
			return RemoteDirectory{}, fmt.Errorf("创建远端目录 %s 失败：%w", nextPath, err)
		}
		parentID = createdID
		currentPath = nextPath
	}
	return RemoteDirectory{ID: parentID, Path: currentPath}, nil
}

func (client *open115RemoteClient) FindFile(ctx context.Context, parentID string, fileName string) (*RemoteFile, error) {
	if client == nil || client.client == nil {
		return nil, errors.New("115 远端客户端为空")
	}
	if parentID == "" || fileName == "" {
		return nil, nil
	}
	const pageSize = 1150
	for offset := 0; ; offset += pageSize {
		resp, err := client.client.GetFsList(ctx, parentID, true, false, false, offset, pageSize)
		if err != nil {
			return nil, err
		}
		if resp == nil || len(resp.Data) == 0 {
			return nil, nil
		}
		for _, item := range resp.Data {
			if item.FileCategory != v115open.TypeFile || item.FileName != fileName {
				continue
			}
			return &RemoteFile{
				ID:       item.FileId,
				PickCode: item.PickCode,
				SHA1:     item.Sha1,
				Size:     item.FileSize,
				Mtime:    item.ModifiedAt(),
			}, nil
		}
		if len(resp.Data) < pageSize {
			return nil, nil
		}
	}
}

func (client *open115RemoteClient) DeleteFile(ctx context.Context, parentID string, fileID string) error {
	if client == nil || client.client == nil {
		return errors.New("115 远端客户端为空")
	}
	if parentID == "" || fileID == "" {
		return errors.New("远端文件信息为空")
	}
	success, err := client.client.Del(ctx, []string{fileID}, parentID)
	if err != nil {
		return err
	}
	if !success {
		return errors.New("115 删除接口返回失败")
	}
	return nil
}
