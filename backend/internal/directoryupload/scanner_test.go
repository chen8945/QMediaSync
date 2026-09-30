package directoryupload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestStartRuleStartupScanAddsExistingFilesToStabilityQueue(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.StartupScanEnabled = true

	service := NewService(ServiceOptions{})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("启动目录监控规则失败: %v", err)
	}
	defer runtime.Stop()

	waitForPendingPath(t, service, rule.ID, filePath)
}

func TestRuntimeStatusRecordsStartupWatcherScan(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "startup.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(710, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeFSNotify
	rule.StartupScanEnabled = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("更新目录监控规则失败: %v", err)
	}

	service := NewService(ServiceOptions{
		StabilityCheckInterval: time.Hour,
		WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
			return &fakeRuleWatcher{}, nil
		},
	})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("启动 fsnotify 目录监控规则失败: %v", err)
	}
	defer runtime.Stop()

	waitForPendingPath(t, service, rule.ID, filePath)

	status := runtime.status()
	if status.ConfiguredMode != string(models.DirectoryUploadWatchModeFSNotify) {
		t.Fatalf("configured_mode=%q，期望 fsnotify", status.ConfiguredMode)
	}
	if status.ActualMode != string(RuleRuntimeModeWatcher) {
		t.Fatalf("actual_mode=%q，期望 watcher", status.ActualMode)
	}
	if status.LastScanAt == 0 {
		t.Fatal("启动扫描应记录 last_scan_at")
	}
	if status.LastScanCandidates != 1 || status.LastScanSkipped != 0 {
		t.Fatalf("scan stats=%+v，期望启动扫描候选 1 跳过 0", status)
	}
}

func TestStartRulePollingFindsNewFiles(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	rule.StartupScanEnabled = false
	rule.RescanIntervalSeconds = 60

	service := NewService(ServiceOptions{PollInterval: 10 * time.Millisecond})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("启动 polling 目录监控规则失败: %v", err)
	}
	defer runtime.Stop()

	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
	waitForPendingPath(t, service, rule.ID, filePath)
}

func TestPollingSnapshotTracksOnlyNewAndChangedFiles(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	rule.StartupScanEnabled = false

	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{}
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(100, 0))
	tracked, err := service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行 polling 快照扫描失败: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("tracked=%d，期望首次发现文件入队", tracked)
	}
	waitForPendingPath(t, service, rule.ID, filePath)

	removePendingPathForTest(t, service, rule.ID, filePath)
	tracked, err = service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行未变化文件 polling 快照扫描失败: %v", err)
	}
	if tracked != 0 {
		t.Fatalf("tracked=%d，期望未变化文件不重复入队", tracked)
	}
	assertNoPendingPath(t, service, rule.ID, filePath)

	writeFileWithMtime(t, filePath, []byte("movie changed"), time.Unix(101, 0))
	tracked, err = service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行变化文件 polling 快照扫描失败: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("tracked=%d，期望 fingerprint 变化后重新入队", tracked)
	}
	waitForPendingPath(t, service, rule.ID, filePath)
}

func TestPollingSnapshotBuildsBaselineWhenStartupScanDisabled(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(200, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	rule.StartupScanEnabled = false

	service := NewService(ServiceOptions{
		PollInterval:           time.Hour,
		StabilityCheckInterval: time.Hour,
	})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("启动 polling 目录监控规则失败: %v", err)
	}
	defer runtime.Stop()

	assertNoPendingPath(t, service, rule.ID, filePath)

	writeFileWithMtime(t, filePath, []byte("movie changed"), time.Unix(201, 0))
	tracked, err := service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行 baseline 后 polling 快照扫描失败: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("tracked=%d，期望 baseline 后文件变化入队", tracked)
	}
	waitForPendingPath(t, service, rule.ID, filePath)
}

func TestPollingSnapshotStartupScanDoesNotRepeatSameFingerprint(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(300, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	rule.StartupScanEnabled = true

	service := NewService(ServiceOptions{
		PollInterval:           time.Hour,
		StabilityCheckInterval: time.Hour,
	})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("启动 polling 目录监控规则失败: %v", err)
	}
	defer runtime.Stop()

	waitForPendingPath(t, service, rule.ID, filePath)
	removePendingPathForTest(t, service, rule.ID, filePath)
	tracked, err := service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行启动扫描后的 polling 快照扫描失败: %v", err)
	}
	if tracked != 0 {
		t.Fatalf("tracked=%d，期望启动扫描初始化 snapshot 后不重复入队", tracked)
	}
	assertNoPendingPath(t, service, rule.ID, filePath)

	writeFileWithMtime(t, filePath, []byte("movie changed"), time.Unix(301, 0))
	tracked, err = service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行启动扫描后的变化文件 polling 快照扫描失败: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("tracked=%d，期望启动扫描后 fingerprint 变化重新入队", tracked)
	}
	waitForPendingPath(t, service, rule.ID, filePath)
}

func TestPollingSnapshotPartialErrorTracksDiscoveredChangesWithoutReplacingSnapshot(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie changed"), time.Unix(400, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling

	runtime := &RuleRuntime{}
	runtime.replaceSnapshot(map[string]string{
		"movie.mkv": "v1:5:399000000000",
		"old.mkv":   "v1:3:398000000000",
	})
	service := NewService(ServiceOptions{})
	result := scanResult{
		Accepted: 1,
		Snapshot: map[string]string{
			"movie.mkv": models.BuildDirectoryUploadSourceFingerprint(int64(len("movie changed")), time.Unix(400, 0).UnixNano()),
		},
	}

	tracked := service.applyPollingSnapshotResult(runtime, rule, result, pollingSnapshotDiff, false)

	if tracked != 1 {
		t.Fatalf("tracked=%d，期望部分扫描错误时仍提交已发现的变化文件", tracked)
	}
	waitForPendingPath(t, service, rule.ID, filePath)
	snapshot := runtime.snapshot()
	if _, ok := snapshot["old.mkv"]; !ok {
		t.Fatalf("部分扫描错误不应整体替换 snapshot，got=%v", snapshot)
	}
}

func TestPollingSnapshotSkipsUnsafeRelativePath(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{}
	result := scanResult{
		Accepted: 1,
		Snapshot: map[string]string{
			"../outside.mkv": models.BuildDirectoryUploadSourceFingerprint(1, time.Unix(410, 0).UnixNano()),
		},
	}

	tracked := service.applyPollingSnapshotResult(runtime, rule, result, pollingSnapshotDiff, true)

	if tracked != 0 {
		t.Fatalf("tracked=%d，期望跳过越界 relative_path", tracked)
	}
	if got := service.PendingPaths(rule.ID); len(got) != 0 {
		t.Fatalf("越界 relative_path 不应加入稳定性队列，got=%v", got)
	}
	if snapshot := runtime.snapshot(); len(snapshot) != 0 {
		t.Fatalf("越界 relative_path 不应写入 polling snapshot，got=%v", snapshot)
	}
}

func TestLocalPathFromSnapshotRelPreservesBackslashFilenameOnPOSIX(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 使用反斜杠作为路径分隔符，POSIX 合法反斜杠文件名场景不适用")
	}
	monitorPath := t.TempDir()
	tests := []struct {
		name          string
		rel           string
		wantCleanRel  string
		wantLocalPath string
	}{
		{
			name:          "文件名中包含反斜杠",
			rel:           "movie\\part.mkv",
			wantCleanRel:  "movie\\part.mkv",
			wantLocalPath: filepath.Join(monitorPath, "movie\\part.mkv"),
		},
		{
			name:          "文件名以反斜杠开头",
			rel:           "\\leading.mkv",
			wantCleanRel:  "\\leading.mkv",
			wantLocalPath: filepath.Join(monitorPath, "\\leading.mkv"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanRel, localPath, ok := localPathFromSnapshotRel(monitorPath, tt.rel)
			if !ok {
				t.Fatalf("rel=%q 应恢复为监控目录内合法文件路径", tt.rel)
			}
			if cleanRel != tt.wantCleanRel {
				t.Fatalf("cleanRel=%q，期望保留反斜杠文件名 %q", cleanRel, tt.wantCleanRel)
			}
			if localPath != tt.wantLocalPath {
				t.Fatalf("localPath=%q，期望 %q", localPath, tt.wantLocalPath)
			}
		})
	}
}

func TestPollingSnapshotTracksSymlinkTargetFingerprintChange(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下 symlink 权限和路径分隔符语义不同，跳过 POSIX symlink fingerprint 回归测试")
	}
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	targetPath := filepath.Join(monitorPath, "target.bin")
	linkPath := filepath.Join(monitorPath, "linked.mkv")
	writeFileWithMtime(t, targetPath, []byte("target"), time.Unix(430, 0))
	if err := os.Symlink(targetPath, linkPath); err != nil {
		t.Skipf("当前平台不支持创建 symlink: %v", err)
	}
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling

	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{}
	tracked, err := service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行 symlink polling 快照扫描失败: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("tracked=%d，期望首次发现 symlink 入队", tracked)
	}
	waitForPendingPath(t, service, rule.ID, linkPath)

	removePendingPathForTest(t, service, rule.ID, linkPath)
	writeFileWithMtime(t, targetPath, []byte("target changed"), time.Unix(431, 0))
	tracked, err = service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行 symlink 目标变化后的 polling 快照扫描失败: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("tracked=%d，期望 symlink 目标 fingerprint 变化后重新入队", tracked)
	}
	waitForPendingPath(t, service, rule.ID, linkPath)
}

func TestPollingSnapshotSkipsSymlinkEscapingMonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下 symlink 权限和路径分隔符语义不同")
	}
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	outsideTarget := filepath.Join(t.TempDir(), "outside.mkv")
	linkPath := filepath.Join(monitorPath, "linked.mkv")
	writeFileWithMtime(t, outsideTarget, []byte("outside"), time.Unix(431, 0))
	if err := os.Symlink(outsideTarget, linkPath); err != nil {
		t.Skipf("当前平台不支持创建 symlink: %v", err)
	}
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling

	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{}
	tracked, err := service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行越界 symlink polling 快照扫描失败: %v", err)
	}
	if tracked != 0 {
		t.Fatalf("tracked=%d，期望越界 symlink 不入队", tracked)
	}
	if pending := service.PendingPaths(rule.ID); len(pending) != 0 {
		t.Fatalf("pending paths=%v，期望越界 symlink 不进入稳定性队列", pending)
	}
}

func TestScanRootWithSnapshotKeepsPartialResultWhenWalkDirFails(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "a.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(420, 0))
	errorDir := filepath.Join(monitorPath, "zzz")
	if err := os.Mkdir(errorDir, 0o755); err != nil {
		t.Fatalf("创建错误触发目录失败: %v", err)
	}
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})
	replacedDir := false

	result, err := service.scanRootWithSnapshotAndTrack(context.Background(), rule, monitorPath, func(rel string, _ string, _ string, _ int64) {
		if rel != "a.mkv" || replacedDir {
			return
		}
		replacedDir = true
		if removeErr := os.Remove(errorDir); removeErr != nil {
			t.Fatalf("删除错误触发目录失败: %v", removeErr)
		}
		if writeErr := os.WriteFile(errorDir, []byte("not a directory"), 0o644); writeErr != nil {
			t.Fatalf("写入错误触发文件失败: %v", writeErr)
		}
	})

	if err == nil {
		t.Fatal("期望 WalkDir 中途错误返回错误")
	}
	if result.Accepted != 1 {
		t.Fatalf("accepted=%d，期望保留错误前已发现文件", result.Accepted)
	}
	if _, ok := result.Snapshot["a.mkv"]; !ok {
		t.Fatalf("partial snapshot 未包含错误前已发现文件，got=%v", result.Snapshot)
	}
}

func TestStartRulePollingBaselineFailureDoesNotBlockStartup(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := filepath.Join(t.TempDir(), "missing")
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	rule.StartupScanEnabled = false

	service := NewService(ServiceOptions{
		PollInterval:           time.Hour,
		StabilityCheckInterval: time.Hour,
	})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("polling baseline 建立失败不应阻止规则启动: %v", err)
	}
	defer runtime.Stop()
	if runtime.Mode != RuleRuntimeModePolling {
		t.Fatalf("runtime mode=%s，期望 polling", runtime.Mode)
	}
}

func TestStartRulePollingBaselineCanceledContextReturnsError(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	rule.StartupScanEnabled = false
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	service := NewService(ServiceOptions{})
	runtime, err := service.StartRule(ctx, rule)
	if runtime != nil {
		runtime.Stop()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("baseline ctx 取消应返回 context.Canceled，got=%v", err)
	}
}

func TestScanSubtreeEnqueueMergesSameRuleAndRoot(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var calls atomic.Int32

	service := NewService(ServiceOptions{})
	service.scanExecutor = newScanExecutorWithScanFunc(1, func(ctx context.Context, _ *models.DirectoryUploadRule, _ string) (int, error) {
		calls.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		close(finished)
		return 0, nil
	})

	service.EnqueueScan(context.Background(), rule, monitorPath)
	waitForSignal(t, started, "等待服务扫描启动")
	service.EnqueueScan(context.Background(), rule, monitorPath)
	service.EnqueueScan(context.Background(), rule, filepath.Join(monitorPath, "."))

	if got := calls.Load(); got != 1 {
		t.Fatalf("scan calls=%d，期望同一目录连续提交只执行一次", got)
	}
	close(release)
	waitForSignal(t, finished, "等待服务扫描结束")
}

func TestStartRuleStartupScanPreflightFailsForInvalidRoot(t *testing.T) {
	tests := []struct {
		name      string
		prepare   func(t *testing.T, rule *models.DirectoryUploadRule)
		wantError string
	}{
		{
			name: "监控目录不存在",
			prepare: func(t *testing.T, rule *models.DirectoryUploadRule) {
				rule.MonitorPath = filepath.Join(t.TempDir(), "missing")
			},
			wantError: "启动补偿扫描失败",
		},
		{
			name: "监控路径不是目录",
			prepare: func(t *testing.T, rule *models.DirectoryUploadRule) {
				filePath := filepath.Join(t.TempDir(), "movie.mkv")
				writeFileWithMtime(t, filePath, []byte("movie"), time.Now())
				rule.MonitorPath = filePath
			},
			wantError: "启动补偿扫描失败",
		},
		{
			name: "同步目录缺失",
			prepare: func(t *testing.T, rule *models.DirectoryUploadRule) {
				rule.SyncPathId = 999_999
			},
			wantError: "启动补偿扫描失败",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			rule.WatchMode = models.DirectoryUploadWatchModePolling
			rule.StartupScanEnabled = true
			tt.prepare(t, rule)

			service := NewService(ServiceOptions{})
			runtime, err := service.StartRule(context.Background(), rule)
			if runtime != nil {
				runtime.Stop()
			}
			if err == nil {
				t.Fatal("启动查漏基础校验失败时 StartRule 应返回错误")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("StartRule error=%v，期望包含 %q", err, tt.wantError)
			}
		})
	}
}

func TestStartRuleAutoFallsBackToPollingWhenWatcherFails(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeAuto
	rule.StartupScanEnabled = false
	var watcherFactoryCalls atomic.Int32

	service := NewService(ServiceOptions{
		PollInterval: 10 * time.Millisecond,
		watchModeDetector: &fakeWatchModeDetector{
			filesystemType: "ext4",
			limits: inotifyLimits{
				Available:        true,
				MaxUserWatches:   1024,
				MaxUserInstances: 128,
			},
			directoryCount: 1,
		},
		WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
			watcherFactoryCalls.Add(1)
			return nil, errors.New("watcher unavailable")
		},
	})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("auto fallback 启动失败: %v", err)
	}
	defer runtime.Stop()
	if runtime.Mode != RuleRuntimeModePolling {
		t.Fatalf("runtime mode=%s，期望 fallback 到 polling", runtime.Mode)
	}
	if got := watcherFactoryCalls.Load(); got != 1 {
		t.Fatalf("watcher factory calls=%d，期望 auto 先尝试创建 watcher", got)
	}

	filePath := filepath.Join(monitorPath, "episode.mp4")
	writeFileWithMtime(t, filePath, []byte("episode"), time.Now())
	waitForPendingPath(t, service, rule.ID, filePath)
}

func TestStartRuleAutoUsesPollingDecisionWithoutCreatingWatcher(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeAuto
	rule.StartupScanEnabled = false
	var watcherFactoryCalls atomic.Int32

	service := NewService(ServiceOptions{
		PollInterval:           time.Hour,
		StabilityCheckInterval: time.Hour,
		watchModeDetector: &fakeWatchModeDetector{
			filesystemType: "fuse.mergerfs",
			limits: inotifyLimits{
				Available:        true,
				MaxUserWatches:   1024,
				MaxUserInstances: 128,
			},
			directoryCount: 1,
		},
		WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
			watcherFactoryCalls.Add(1)
			return nil, errors.New("不应创建 watcher")
		},
	})
	runtime, err := service.StartRule(context.Background(), rule)
	if err != nil {
		t.Fatalf("auto 决策为 polling 时启动失败: %v", err)
	}
	defer runtime.Stop()
	if runtime.Mode != RuleRuntimeModePolling {
		t.Fatalf("runtime mode=%s，期望 polling", runtime.Mode)
	}
	if got := watcherFactoryCalls.Load(); got != 0 {
		t.Fatalf("watcher factory calls=%d，auto 决策 polling 时不应创建 watcher", got)
	}
}

type fakeRuleWatcher struct {
	startErr error
	onStart  func()
	closed   atomic.Bool
}

func (watcher *fakeRuleWatcher) Start(context.Context) error {
	if watcher.onStart != nil {
		watcher.onStart()
	}
	return watcher.startErr
}

func (watcher *fakeRuleWatcher) Close() error {
	watcher.closed.Store(true)
	return nil
}

type runtimeAwareFakeWatcher struct {
	recorder        *RuleRuntime
	startRecorder   *RuleRuntime
	startRecorderCh chan *RuleRuntime
}

func (watcher *runtimeAwareFakeWatcher) setRuntimeErrorRecorder(runtime *RuleRuntime) {
	watcher.recorder = runtime
}

func (watcher *runtimeAwareFakeWatcher) Start(context.Context) error {
	watcher.startRecorder = watcher.recorder
	if watcher.startRecorderCh != nil {
		watcher.startRecorderCh <- watcher.recorder
	}
	return nil
}

func (watcher *runtimeAwareFakeWatcher) Close() error {
	return nil
}

func TestStartRuleAutoCancellationDoesNotFallbackToPolling(t *testing.T) {
	tests := []struct {
		name     string
		ctx      func() context.Context
		detector *fakeWatchModeDetector
	}{
		{
			name: "detector 返回 context.Canceled",
			ctx: func() context.Context {
				return context.Background()
			},
			detector: &fakeWatchModeDetector{
				filesystemErr: context.Canceled,
				limits: inotifyLimits{
					Available:        true,
					MaxUserWatches:   1024,
					MaxUserInstances: 128,
				},
				directoryCount: 1,
			},
		},
		{
			name: "启动上下文已取消",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			detector: &fakeWatchModeDetector{
				filesystemType: "ext4",
				limits: inotifyLimits{
					Available:        true,
					MaxUserWatches:   1024,
					MaxUserInstances: 128,
				},
				directoryCount: 1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitorPath := t.TempDir()
			_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
			rule.WatchMode = models.DirectoryUploadWatchModeAuto
			rule.StartupScanEnabled = false
			var watcherFactoryCalls atomic.Int32

			service := NewService(ServiceOptions{
				watchModeDetector: tt.detector,
				WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
					watcherFactoryCalls.Add(1)
					return &fakeRuleWatcher{}, nil
				},
			})
			runtime, err := service.StartRule(tt.ctx(), rule)
			if runtime != nil {
				runtime.Stop()
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("StartRule error=%v，期望 context.Canceled", err)
			}
			if runtime != nil {
				t.Fatalf("取消时不应返回可用 runtime，got=%+v", runtime)
			}
			if got := watcherFactoryCalls.Load(); got != 0 {
				t.Fatalf("watcher factory calls=%d，取消时不应继续创建 watcher", got)
			}
		})
	}
}

func TestStartFSNotifyRuleWatcherInjectsRuntimeBeforeStart(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeFSNotify
	rule.StartupScanEnabled = false
	expectedRuntime := &RuleRuntime{RuleID: rule.ID}
	startRecorderCh := make(chan *RuleRuntime, 1)
	fakeWatcher := &runtimeAwareFakeWatcher{startRecorderCh: startRecorderCh}

	service := NewService(ServiceOptions{
		WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
			return fakeWatcher, nil
		},
	})
	watcher, err := service.startFSNotifyRuleWatcher(context.Background(), rule, expectedRuntime)
	if err != nil {
		t.Fatalf("启动 fake watcher 失败: %v", err)
	}
	if watcher != fakeWatcher {
		t.Fatalf("watcher=%+v，期望返回 fake watcher", watcher)
	}
	select {
	case got := <-startRecorderCh:
		if got != expectedRuntime {
			t.Fatalf("Start 中 recorder=%p，期望注入 runtime %p", got, expectedRuntime)
		}
	default:
		t.Fatal("fake watcher Start 未被调用")
	}
	if fakeWatcher.startRecorder != expectedRuntime {
		t.Fatalf("startRecorder=%p，期望注入 runtime %p", fakeWatcher.startRecorder, expectedRuntime)
	}
}

func TestStartRuleAutoWatcherStartCanceledDoesNotFallbackToPolling(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeAuto
	rule.StartupScanEnabled = false
	var watcherFactoryCalls atomic.Int32

	service := NewService(ServiceOptions{
		PollInterval:           time.Hour,
		StabilityCheckInterval: time.Hour,
		watchModeDetector: &fakeWatchModeDetector{
			filesystemType: "ext4",
			limits: inotifyLimits{
				Available:        true,
				MaxUserWatches:   1024,
				MaxUserInstances: 128,
			},
			directoryCount: 1,
		},
		WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
			watcherFactoryCalls.Add(1)
			return &fakeRuleWatcher{startErr: context.Canceled}, nil
		},
	})

	runtime, err := service.StartRule(context.Background(), rule)
	if runtime != nil {
		runtime.Stop()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("watcher Start 取消时 StartRule error=%v，期望 context.Canceled", err)
	}
	if runtime != nil {
		t.Fatalf("watcher Start 取消时不应 fallback 并返回 runtime，got=%+v", runtime)
	}
	if got := watcherFactoryCalls.Load(); got != 1 {
		t.Fatalf("watcher factory calls=%d，期望只尝试创建一次 watcher", got)
	}
}

func TestStartFSNotifyRuleWatcherClosesWatcherWhenContextCanceledAfterSuccessfulStart(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.StartupScanEnabled = false
	ctx, cancel := context.WithCancel(context.Background())
	fakeWatcher := &fakeRuleWatcher{onStart: cancel}

	service := NewService(ServiceOptions{
		WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
			return fakeWatcher, nil
		},
	})
	watcher, err := service.startFSNotifyRuleWatcher(ctx, rule, &RuleRuntime{RuleID: rule.ID})
	if watcher != nil {
		_ = watcher.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("watcher Start 成功后 ctx 取消应返回 context.Canceled，got=%v", err)
	}
	if watcher != nil {
		t.Fatalf("ctx 取消时不应返回 watcher，got=%+v", watcher)
	}
	if !fakeWatcher.closed.Load() {
		t.Fatal("ctx 取消时应关闭已启动的 watcher")
	}
}

func TestFSNotifyWatcherStartReturnsCanceledWhenContextCanceledDuringRecursiveAdd(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("真实 fsnotify 递归 Add 取消回归测试只在 Linux inotify 环境运行")
	}
	monitorPath := t.TempDir()
	const directoryCount = 4096
	for i := range directoryCount {
		if err := os.Mkdir(filepath.Join(monitorPath, fmt.Sprintf("dir-%04d", i)), 0o755); err != nil {
			t.Fatalf("创建测试目录失败: %v", err)
		}
	}

	service := NewService(ServiceOptions{})
	rule := &models.DirectoryUploadRule{
		MonitorPath: monitorPath,
		Recursive:   true,
	}
	watcher := &fsNotifyRuleWatcher{service: service, rule: rule}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	t.Cleanup(func() {
		_ = watcher.Close()
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- watcher.Start(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for {
		select {
		case err := <-errCh:
			if err != nil {
				t.Fatalf("取消前 watcher Start 已失败: %v", err)
			}
			t.Fatal("取消前 watcher Start 已成功返回，测试未覆盖递归 Add 期间取消")
		default:
		}

		watcher.mutex.Lock()
		started := watcher.watcher != nil
		watcher.mutex.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待真实 fsnotify watcher 初始化超时")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("递归 Add 期间 ctx 取消时 Start error=%v，期望 context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("递归 Add 期间 ctx 取消后 watcher Start 未返回")
	}
}

func TestStartRuleFSNotifyFailsWhenWatcherFails(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeFSNotify
	rule.StartupScanEnabled = false

	service := NewService(ServiceOptions{
		WatcherFactory: func(*Service, *models.DirectoryUploadRule) (RuleWatcher, error) {
			return nil, errors.New("watcher unavailable")
		},
	})
	if _, err := service.StartRule(context.Background(), rule); err == nil {
		t.Fatal("fsnotify 模式下 watcher 初始化失败应直接报错")
	}
}

func TestStartRuleRejectsLegacyWatcherMode(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchMode("watcher")
	rule.StartupScanEnabled = false

	service := NewService(ServiceOptions{})
	if _, err := service.StartRule(context.Background(), rule); err == nil {
		t.Fatal("旧 watcher 监控模式不应继续兼容")
	}
}

func TestStartRunsProcessedCleanupOnStartupAndInterval(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	clock := &fakeClock{now: time.Unix(200_000, 0)}
	oldPath := filepath.Join(t.TempDir(), "missing-startup.mkv")
	startupRecord := &models.DirectoryUploadProcessedFile{
		SourceKey:         "cleanup-startup",
		LocalFullPath:     oldPath,
		SourceFingerprint: "v1:1:1",
		Result:            models.DirectoryUploadProcessedResultUploaded,
		ProcessedAt:       clock.Now().Add(-48 * time.Hour).Unix(),
		LastSeenAt:        clock.Now().Add(-48 * time.Hour).Unix(),
	}
	if err := db.Db.Create(startupRecord).Error; err != nil {
		t.Fatalf("创建启动清理 processed 记录失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	service := NewService(ServiceOptions{
		Now:                       clock.Now,
		ProcessedCleanupInterval:  10 * time.Millisecond,
		ProcessedMissingSourceTTL: 24 * time.Hour,
	})
	if err := service.Start(ctx); err != nil {
		cancel()
		t.Fatalf("启动目录上传服务失败: %v", err)
	}
	defer func() {
		cancel()
		service.Stop()
	}()

	waitForProcessedCount(t, 0)

	intervalPath := filepath.Join(t.TempDir(), "missing-interval.mkv")
	intervalRecord := &models.DirectoryUploadProcessedFile{
		SourceKey:         "cleanup-interval",
		LocalFullPath:     intervalPath,
		SourceFingerprint: "v1:2:2",
		Result:            models.DirectoryUploadProcessedResultRemoteExists,
		ProcessedAt:       clock.Now().Add(-48 * time.Hour).Unix(),
		LastSeenAt:        clock.Now().Add(-48 * time.Hour).Unix(),
	}
	if err := db.Db.Create(intervalRecord).Error; err != nil {
		t.Fatalf("创建周期清理 processed 记录失败: %v", err)
	}
	waitForProcessedCount(t, 0)
	if _, err := os.Stat(intervalPath); !os.IsNotExist(err) {
		t.Fatalf("测试源文件应保持缺失状态: %s err=%v", intervalPath, err)
	}
}

func TestPollingIntervalUsesBuiltInDefault(t *testing.T) {
	rule := &models.DirectoryUploadRule{RescanIntervalSeconds: 999}
	service := NewService(ServiceOptions{})

	if got := service.pollingInterval(rule); got != 30*time.Second {
		t.Fatalf("polling interval = %s，期望使用内置 30s", got)
	}
}

func TestPollingIntervalAllowsTestOverride(t *testing.T) {
	rule := &models.DirectoryUploadRule{RescanIntervalSeconds: 999}
	service := NewService(ServiceOptions{PollInterval: 10 * time.Millisecond})

	if got := service.pollingInterval(rule); got != 10*time.Millisecond {
		t.Fatalf("polling interval = %s，期望测试注入优先", got)
	}
}

func TestStabilityIntervalUsesBuiltInDefault(t *testing.T) {
	rule := &models.DirectoryUploadRule{StabilityCheckIntervalSeconds: 999}
	service := NewService(ServiceOptions{})

	if got := service.stabilityInterval(rule); got != 2*time.Second {
		t.Fatalf("stability interval = %s，期望使用内置 2s", got)
	}
}

func TestStabilityIntervalAllowsTestOverride(t *testing.T) {
	rule := &models.DirectoryUploadRule{StabilityCheckIntervalSeconds: 999}
	service := NewService(ServiceOptions{StabilityCheckInterval: 10 * time.Millisecond})

	if got := service.stabilityInterval(rule); got != 10*time.Millisecond {
		t.Fatalf("stability interval = %s，期望测试注入优先", got)
	}
}

func waitForPendingPath(t *testing.T, service *Service, ruleID uint, filePath string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if slices.Contains(service.PendingPaths(ruleID), filePath) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 pending path 超时: %s, got=%v", filePath, service.PendingPaths(ruleID))
}

func removePendingPathForTest(t *testing.T, service *Service, ruleID uint, filePath string) {
	t.Helper()
	if service == nil || service.queue == nil {
		t.Fatal("目录上传服务稳定性队列为空")
	}
	service.queue.mutex.Lock()
	defer service.queue.mutex.Unlock()

	delete(service.queue.candidates[ruleID], filePath)
}

func assertNoPendingPath(t *testing.T, service *Service, ruleID uint, filePath string) {
	t.Helper()
	for _, pending := range service.PendingPaths(ruleID) {
		if pending == filePath {
			t.Fatalf("文件不应在稳定性队列中: %s, got=%v", filePath, service.PendingPaths(ruleID))
		}
	}
}

func waitForProcessedCount(t *testing.T, want int64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var got int64
		if err := db.Db.Model(&models.DirectoryUploadProcessedFile{}).Count(&got).Error; err != nil {
			t.Fatalf("统计 processed 记录失败: %v", err)
		}
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var got int64
	if err := db.Db.Model(&models.DirectoryUploadProcessedFile{}).Count(&got).Error; err != nil {
		t.Fatalf("统计 processed 记录失败: %v", err)
	}
	t.Fatalf("等待 processed 记录数量超时: got=%d want=%d", got, want)
}
