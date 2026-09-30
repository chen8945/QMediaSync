package directoryupload

import (
	"context"
	"errors"
	"time"

	"qmediasync/internal/models"
)

// RuleRuntimeStatus 是目录监控规则当前运行状态快照。
type RuleRuntimeStatus struct {
	RuleID             uint   `json:"rule_id"`
	ConfiguredMode     string `json:"configured_mode"`
	ActualMode         string `json:"actual_mode"`
	FallbackReason     string `json:"fallback_reason"`
	LastScanAt         int64  `json:"last_scan_at"`
	LastScanDurationMs int64  `json:"last_scan_duration_ms"`
	LastScanCandidates int    `json:"last_scan_candidates"`
	LastScanSkipped    int    `json:"last_scan_skipped"`
	LastError          string `json:"last_error"`
	PendingCount       int    `json:"pending_count"`
}

// GetDirectoryUploadRuntimeStatuses 返回全局目录监控服务的运行状态。
func GetDirectoryUploadRuntimeStatuses() []RuleRuntimeStatus {
	globalService.Lock()
	service := globalService.service
	globalService.Unlock()
	if service == nil {
		return []RuleRuntimeStatus{}
	}
	return service.runtimeStatuses()
}

func (service *Service) runtimeStatuses() []RuleRuntimeStatus {
	if service == nil {
		return []RuleRuntimeStatus{}
	}
	service.mutex.Lock()
	runtimes := append([]*RuleRuntime(nil), service.runtimes...)
	service.mutex.Unlock()

	statuses := make([]RuleRuntimeStatus, 0, len(runtimes))
	for _, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		status := runtime.status()
		status.PendingCount = len(service.PendingPaths(runtime.RuleID))
		statuses = append(statuses, status)
	}
	return statuses
}

func (service *Service) recordRuleRuntimeScan(
	ruleID uint,
	startedAt time.Time,
	candidates int,
	skipped int,
	err error,
) {
	runtime := service.runtimeByRuleID(ruleID)
	if runtime == nil {
		return
	}
	runtime.recordScan(service.now(), startedAt, candidates, skipped, err)
}

func (service *Service) recordRuleRuntimeError(ruleID uint, err error) {
	runtime := service.runtimeByRuleID(ruleID)
	if runtime == nil {
		return
	}
	runtime.recordError(err)
}

func (service *Service) runtimeByRuleID(ruleID uint) *RuleRuntime {
	if service == nil || ruleID == 0 {
		return nil
	}
	service.mutex.Lock()
	defer service.mutex.Unlock()
	for _, runtime := range service.runtimes {
		if runtime != nil && runtime.RuleID == ruleID {
			return runtime
		}
	}
	return nil
}

func (runtime *RuleRuntime) setRuntimeMode(
	configuredMode string,
	actualMode RuleRuntimeMode,
	fallbackReason string,
) {
	if runtime == nil {
		return
	}
	runtime.statusMu.Lock()
	defer runtime.statusMu.Unlock()
	runtime.configuredMode = configuredMode
	runtime.Mode = actualMode
	runtime.fallbackReason = fallbackReason
}

func (runtime *RuleRuntime) recordScan(now time.Time, startedAt time.Time, candidates int, skipped int, err error) {
	if runtime == nil {
		return
	}
	if candidates < 0 {
		candidates = 0
	}
	if skipped < 0 {
		skipped = 0
	}
	if skipped > candidates {
		skipped = candidates
	}
	durationMs := max(now.Sub(startedAt).Milliseconds(), 0)

	runtime.statusMu.Lock()
	defer runtime.statusMu.Unlock()
	runtime.lastScanAt = now.Unix()
	runtime.lastScanDurationMs = durationMs
	runtime.lastScanCandidates = candidates
	runtime.lastScanSkipped = skipped
	runtime.lastScanError = runtimeErrorString(err)
}

func (runtime *RuleRuntime) recordError(err error) {
	if runtime == nil {
		return
	}
	lastError := runtimeErrorString(err)
	if lastError == "" {
		return
	}
	runtime.statusMu.Lock()
	defer runtime.statusMu.Unlock()
	runtime.lastRuntimeError = lastError
}

func (runtime *RuleRuntime) status() RuleRuntimeStatus {
	if runtime == nil {
		return RuleRuntimeStatus{}
	}
	runtime.statusMu.RLock()
	defer runtime.statusMu.RUnlock()
	lastError := runtime.lastRuntimeError
	if runtime.lastScanError != "" {
		lastError = runtime.lastScanError
	}
	return RuleRuntimeStatus{
		RuleID:             runtime.RuleID,
		ConfiguredMode:     runtime.configuredMode,
		ActualMode:         string(runtime.Mode),
		FallbackReason:     runtime.fallbackReason,
		LastScanAt:         runtime.lastScanAt,
		LastScanDurationMs: runtime.lastScanDurationMs,
		LastScanCandidates: runtime.lastScanCandidates,
		LastScanSkipped:    runtime.lastScanSkipped,
		LastError:          lastError,
	}
}

func configuredWatchMode(rule *models.DirectoryUploadRule) string {
	if rule == nil || rule.WatchMode == "" {
		return string(models.DirectoryUploadWatchModeAuto)
	}
	return string(rule.WatchMode)
}

func runtimeErrorString(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	return err.Error()
}
