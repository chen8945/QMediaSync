package directoryupload

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"qmediasync/internal/models"

	"gorm.io/gorm"
)

type triggerReason string

const (
	triggerReasonStableFile               triggerReason = "stable_file"
	triggerReasonForceReprocess           triggerReason = "force_reprocess"
	triggerReasonMemoryProcessed          triggerReason = "memory_processed"
	triggerReasonNoProcessedRecord        triggerReason = "no_processed_record"
	triggerReasonSourceFingerprintChanged triggerReason = "source_fingerprint_changed"
	triggerReasonTerminalProcessed        triggerReason = "terminal_processed"
	triggerReasonAwaitingStrmRetried      triggerReason = "awaiting_strm_retried"
	triggerReasonAwaitingStrmTaskMissing  triggerReason = "awaiting_strm_task_missing"
	triggerReasonAwaitingStrmChanged      triggerReason = "awaiting_strm_changed"
	triggerReasonQueuedActive             triggerReason = "queued_active"
	triggerReasonQueuedInactiveRetry      triggerReason = "queued_inactive_retry"
	triggerReasonPendingReplaceRetry      triggerReason = "pending_replace_retry"
	triggerReasonFailedRetry              triggerReason = "failed_retry"
	triggerReasonActiveUploadTask         triggerReason = "active_upload_task"
	triggerReasonCreateUploadTask         triggerReason = "create_upload_task"
)

type triggerPlan struct {
	sourceState processedSourceState
	force       bool
	skip        bool
	reasons     []triggerReason
}

func (plan *triggerPlan) addReason(reason triggerReason) {
	if reason == "" {
		return
	}
	if slices.Contains(plan.reasons, reason) {
		return
	}
	plan.reasons = append(plan.reasons, reason)
}

func (plan triggerPlan) hasReason(reason triggerReason) bool {
	return slices.Contains(plan.reasons, reason)
}

func (plan triggerPlan) reasonString() string {
	if len(plan.reasons) == 0 {
		return ""
	}
	parts := make([]string, 0, len(plan.reasons))
	for _, reason := range plan.reasons {
		parts = append(parts, string(reason))
	}
	return strings.Join(parts, ",")
}

func (service *Service) buildTriggerPlan(
	ctx context.Context,
	rule *models.DirectoryUploadRule,
	rel string,
	filePath string,
	info os.FileInfo,
	options handleStableFileOptions,
) (triggerPlan, error) {
	if err := ctx.Err(); err != nil {
		return triggerPlan{}, err
	}
	if service == nil {
		service = NewService(ServiceOptions{})
	}
	if rule == nil {
		return triggerPlan{}, errors.New("目录监控规则为空")
	}
	if info == nil {
		return triggerPlan{}, errors.New("稳定文件信息为空")
	}

	plan := triggerPlan{
		sourceState: buildProcessedSourceState(rule, rel, info),
		force:       options.Force,
	}
	plan.addReason(triggerReasonStableFile)
	if options.Force {
		plan.addReason(triggerReasonForceReprocess)
	} else if err := service.applyProcessedLedgerToTriggerPlan(&plan, rule, rel, filePath); err != nil {
		return plan, err
	}
	if plan.skip {
		return plan, nil
	}

	if !models.CheckCanUploadByLocalPath(models.UploadSourceDirectoryMonitor, filePath) {
		plan.skip = true
		plan.addReason(triggerReasonActiveUploadTask)
		return plan, nil
	}
	plan.addReason(triggerReasonCreateUploadTask)
	return plan, nil
}

func (service *Service) applyProcessedLedgerToTriggerPlan(plan *triggerPlan, rule *models.DirectoryUploadRule, rel string, filePath string) error {
	if service.isProcessed(rule, rel, plan.sourceState.sourceFingerprint) {
		plan.skip = true
		plan.addReason(triggerReasonMemoryProcessed)
		return nil
	}

	record, err := models.FindDirectoryUploadProcessedBySourceKey(plan.sourceState.sourceKey)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			plan.addReason(triggerReasonNoProcessedRecord)
			return nil
		}
		return fmt.Errorf("查询目录监控源文件处理记录失败：%w", err)
	}
	if record.SourceFingerprint != plan.sourceState.sourceFingerprint {
		plan.addReason(triggerReasonSourceFingerprintChanged)
		return nil
	}

	now := service.now().Unix()
	switch {
	case models.IsDirectoryUploadProcessedTerminal(record.Result):
		if err := updateDirectoryUploadProcessedLastSeen(record.SourceKey, now); err != nil {
			return fmt.Errorf("更新目录监控源文件最后发现时间失败：%w", err)
		}
		service.markProcessed(rule, rel, plan.sourceState.sourceFingerprint)
		plan.skip = true
		plan.addReason(triggerReasonTerminalProcessed)
	case models.IsDirectoryUploadProcessedAwaitingStrm(record.Result):
		if err := service.retryDirectoryUploadStrmEnqueue(record.UploadTaskId); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				deleted, deleteErr := deleteDirectoryUploadProcessedIfUnchanged(record)
				if deleteErr != nil {
					return fmt.Errorf("清理缺失上传任务的 STRM 等待记录失败：%w", deleteErr)
				}
				if !deleted {
					plan.skip = true
					plan.addReason(triggerReasonAwaitingStrmChanged)
					return nil
				}
				plan.addReason(triggerReasonAwaitingStrmTaskMissing)
				return nil
			}
			return fmt.Errorf("重试 STRM 入队失败：%w", err)
		}
		service.markProcessed(rule, rel, plan.sourceState.sourceFingerprint)
		plan.skip = true
		plan.addReason(triggerReasonAwaitingStrmRetried)
	case record.Result == models.DirectoryUploadProcessedResultQueued:
		active, err := hasActiveDirectoryUploadTask(record.UploadTaskId, filePath)
		if err != nil {
			return fmt.Errorf("检查目录监控源文件上传任务状态失败：%w", err)
		}
		if active {
			if err := updateDirectoryUploadProcessedLastSeen(record.SourceKey, now); err != nil {
				return fmt.Errorf("更新目录监控源文件最后发现时间失败：%w", err)
			}
			plan.skip = true
			plan.addReason(triggerReasonQueuedActive)
			return nil
		}
		plan.addReason(triggerReasonQueuedInactiveRetry)
	case record.Result == models.DirectoryUploadProcessedResultPendingReplace:
		plan.addReason(triggerReasonPendingReplaceRetry)
	case record.Result == models.DirectoryUploadProcessedResultFailed:
		plan.addReason(triggerReasonFailedRetry)
	default:
		plan.addReason(triggerReasonSourceFingerprintChanged)
	}
	return nil
}
