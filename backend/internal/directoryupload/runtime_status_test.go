package directoryupload

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"

	"github.com/fsnotify/fsnotify"
)

func TestRuntimeStatusReportsModeFallbackScanAndPendingCount(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeAuto
	rule.StartupScanEnabled = false
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("更新目录监控规则失败: %v", err)
	}

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
			return nil, errors.New("watcher unavailable")
		},
	})
	ctx := t.Context()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("启动目录监控服务失败: %v", err)
	}
	defer service.Stop()

	service.mutex.Lock()
	if len(service.runtimes) != 1 {
		service.mutex.Unlock()
		t.Fatalf("runtime count=%d，期望 1", len(service.runtimes))
	}
	runtime := service.runtimes[0]
	service.mutex.Unlock()

	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(700, 0))
	tracked, err := service.scanPollingSnapshot(context.Background(), runtime, rule, monitorPath, pollingSnapshotDiff)
	if err != nil {
		t.Fatalf("执行 polling 快照扫描失败: %v", err)
	}
	if tracked != 1 {
		t.Fatalf("tracked=%d，期望新增文件入队", tracked)
	}

	statuses := service.runtimeStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runtime status count=%d，期望 1", len(statuses))
	}
	status := statuses[0]
	if status.RuleID != rule.ID {
		t.Fatalf("rule_id=%d，期望 %d", status.RuleID, rule.ID)
	}
	if status.ConfiguredMode != string(models.DirectoryUploadWatchModeAuto) {
		t.Fatalf("configured_mode=%q，期望 auto", status.ConfiguredMode)
	}
	if status.ActualMode != string(RuleRuntimeModePolling) {
		t.Fatalf("actual_mode=%q，期望 polling", status.ActualMode)
	}
	if !strings.Contains(status.FallbackReason, "watcher unavailable") {
		t.Fatalf("fallback_reason=%q，期望包含 watcher 启动失败原因", status.FallbackReason)
	}
	if status.LastScanAt == 0 {
		t.Fatal("last_scan_at 应记录最近扫描完成时间")
	}
	if status.LastScanDurationMs < 0 {
		t.Fatalf("last_scan_duration_ms=%d，不应为负数", status.LastScanDurationMs)
	}
	if status.LastScanCandidates != 1 || status.LastScanSkipped != 0 {
		t.Fatalf("scan stats=%+v，期望候选 1 跳过 0", status)
	}
	if status.LastError != "" {
		t.Fatalf("last_error=%q，成功扫描后不应保留错误", status.LastError)
	}
	if status.PendingCount != 1 {
		t.Fatalf("pending_count=%d，期望 1", status.PendingCount)
	}
}

func TestRuntimeStatusRecordsPollingScanError(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := filepath.Join(t.TempDir(), "missing")
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModePolling
	rule.StartupScanEnabled = false
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("更新目录监控规则失败: %v", err)
	}

	service := NewService(ServiceOptions{
		PollInterval:           time.Hour,
		StabilityCheckInterval: time.Hour,
	})
	ctx := t.Context()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("polling baseline 失败不应阻止服务启动: %v", err)
	}
	defer service.Stop()

	statuses := service.runtimeStatuses()
	if len(statuses) != 1 {
		t.Fatalf("runtime status count=%d，期望 1", len(statuses))
	}
	status := statuses[0]
	if status.ConfiguredMode != string(models.DirectoryUploadWatchModePolling) {
		t.Fatalf("configured_mode=%q，期望 polling", status.ConfiguredMode)
	}
	if status.ActualMode != string(RuleRuntimeModePolling) {
		t.Fatalf("actual_mode=%q，期望 polling", status.ActualMode)
	}
	if status.FallbackReason != "" {
		t.Fatalf("fallback_reason=%q，显式 polling 不应记录降级原因", status.FallbackReason)
	}
	if status.LastScanAt == 0 {
		t.Fatal("baseline 错误也应记录 last_scan_at")
	}
	if status.LastScanCandidates != 0 || status.LastScanSkipped != 0 {
		t.Fatalf("scan stats=%+v，错误发生在扫描前时应为 0", status)
	}
	if !strings.Contains(status.LastError, "读取监控目录失败") {
		t.Fatalf("last_error=%q，期望记录 polling scan 错误", status.LastError)
	}
}

func TestRuntimeStatusMissingSubtreeScanDoesNotLeaveLastError(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{RuleID: rule.ID}
	runtime.setRuntimeMode(configuredWatchMode(rule), RuleRuntimeModeWatcher, "")
	service.runtimes = []*RuleRuntime{runtime}

	accepted, err := service.ScanSubtree(
		context.Background(),
		rule,
		filepath.Join(monitorPath, "deleted-before-scan"),
	)
	if err != nil {
		t.Fatalf("缺失子目录扫描应按成功处理: %v", err)
	}
	if accepted != 0 {
		t.Fatalf("accepted=%d，缺失子目录不应发现候选文件", accepted)
	}
	if status := runtime.status(); status.LastError != "" {
		t.Fatalf("last_error=%q，缺失子目录归一为成功后不应残留错误", status.LastError)
	}
}

func TestRuntimeStatusScanRuleRecordsOrdinaryError(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := filepath.Join(t.TempDir(), "missing")
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{RuleID: rule.ID}
	runtime.setRuntimeMode(configuredWatchMode(rule), RuleRuntimeModeWatcher, "")
	service.runtimes = []*RuleRuntime{runtime}

	if _, err := service.ScanRule(context.Background(), rule); err == nil {
		t.Fatal("缺失监控根目录扫描应返回错误")
	}
	if status := runtime.status(); !strings.Contains(status.LastError, "读取监控目录失败") {
		t.Fatalf("last_error=%q，普通扫描错误应记录到运行状态", status.LastError)
	}
}

func TestRuntimeStatusWatcherErrorSurvivesSuccessfulScan(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	filePath := filepath.Join(monitorPath, "movie.mkv")
	writeFileWithMtime(t, filePath, []byte("movie"), time.Unix(720, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{RuleID: rule.ID}
	runtime.setRuntimeMode(configuredWatchMode(rule), RuleRuntimeModeWatcher, "")
	service.runtimes = []*RuleRuntime{runtime}

	runtime.recordError(errors.New("fsnotify queue overflow"))
	if _, err := service.ScanRule(context.Background(), rule); err != nil {
		t.Fatalf("成功扫描不应返回错误: %v", err)
	}

	status := runtime.status()
	if !strings.Contains(status.LastError, "fsnotify queue overflow") {
		t.Fatalf("last_error=%q，成功扫描不应清空 watcher 错误", status.LastError)
	}
	if status.LastScanCandidates != 1 {
		t.Fatalf("last_scan_candidates=%d，期望成功扫描仍记录候选数", status.LastScanCandidates)
	}
}

func TestRuntimeStatusScanErrorClearedBySuccessfulScan(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	validMonitorPath := t.TempDir()
	missingMonitorPath := filepath.Join(t.TempDir(), "missing")
	_, rule := createDirectoryUploadRuleForTest(t, missingMonitorPath)
	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{RuleID: rule.ID}
	runtime.setRuntimeMode(configuredWatchMode(rule), RuleRuntimeModeWatcher, "")
	service.runtimes = []*RuleRuntime{runtime}

	if _, err := service.ScanRule(context.Background(), rule); err == nil {
		t.Fatal("缺失监控根目录扫描应返回错误")
	}
	if status := runtime.status(); !strings.Contains(status.LastError, "读取监控目录失败") {
		t.Fatalf("last_error=%q，期望先记录扫描错误", status.LastError)
	}

	rule.MonitorPath = validMonitorPath
	if _, err := service.ScanRule(context.Background(), rule); err != nil {
		t.Fatalf("成功扫描不应返回错误: %v", err)
	}
	if status := runtime.status(); status.LastError != "" {
		t.Fatalf("last_error=%q，成功扫描应清空扫描来源错误", status.LastError)
	}
}

func TestFSNotifyWatcherErrorRecordsRuntimeStatus(t *testing.T) {
	runtime := &RuleRuntime{RuleID: 99}
	runtime.setRuntimeMode(string(models.DirectoryUploadWatchModeFSNotify), RuleRuntimeModeWatcher, "")
	watcher := &fsNotifyRuleWatcher{
		rule:                 &models.DirectoryUploadRule{ID: runtime.RuleID},
		runtimeErrorRecorder: runtime,
	}

	watcher.recordError(errors.New("fsnotify queue overflow"))

	if status := runtime.status(); !strings.Contains(status.LastError, "fsnotify queue overflow") {
		t.Fatalf("last_error=%q，watcher 错误应直接写入对应 runtime", status.LastError)
	}
}

func TestFSNotifyWatcherNewDirectoryScanRecordsDirectRuntimeStatus(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitorPath := t.TempDir()
	childPath := filepath.Join(monitorPath, "season")
	if err := os.Mkdir(childPath, 0o755); err != nil {
		t.Fatalf("创建测试子目录失败: %v", err)
	}
	filePath := filepath.Join(childPath, "episode.mkv")
	writeFileWithMtime(t, filePath, []byte("episode"), time.Unix(730, 0))
	_, rule := createDirectoryUploadRuleForTest(t, monitorPath)
	rule.WatchMode = models.DirectoryUploadWatchModeFSNotify
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatalf("更新目录监控规则失败: %v", err)
	}
	service := NewService(ServiceOptions{})
	runtime := &RuleRuntime{RuleID: rule.ID}
	runtime.setRuntimeMode(configuredWatchMode(rule), RuleRuntimeModeWatcher, "")
	watcher := &fsNotifyRuleWatcher{
		service:              service,
		rule:                 rule,
		runtimeErrorRecorder: runtime,
	}

	watcher.handleEvent(context.Background(), fsnotify.Event{Name: childPath, Op: fsnotify.Create})

	waitForPendingPath(t, service, rule.ID, filePath)
	waitForRuntimeScanCandidates(t, runtime, 1)
}

func waitForRuntimeScanCandidates(t *testing.T, runtime *RuleRuntime, expected int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := runtime.status()
		if status.LastScanAt != 0 && status.LastScanCandidates == expected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待 runtime scan 状态超时: got=%+v want candidates=%d", runtime.status(), expected)
}
