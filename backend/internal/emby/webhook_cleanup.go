package emby

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// processCleanupDeletion 只处理收件时明确采用当前策略的工作。
func (worker *webhookWorker) processCleanupDeletion(ctx context.Context, record models.EmbyWebhookRecord, input models.EmbyDeletionInput) error {
	ctx, release, err := models.BeginEmbyDeletionExecution(ctx)
	if err != nil {
		return err
	}
	defer release()
	ctx = newEmbyDeletionVerificationContext(ctx)
	var survivors, protected []string
	var verificationFailure bool
	verificationCalls := 0
	observeVerification := func(err error) {
		if alive, ok := errors.AsType[*embySurvivingItemsError](err); ok {
			survivors = append(survivors, alive.IDs...)
		} else if shared, ok := errors.AsType[*embyProtectedOwnersError](err); ok {
			protected = append(protected, shared.IDs...)
		} else if _, ok := errors.AsType[*models.EmbyDeletionTargetConflict](err); ok {
			// 已明确的元数据保留者只形成目标级 unresolved，不是网络故障。
		} else if err != nil {
			verificationFailure = true
		}
	}
	verify := func(ctx context.Context, input models.EmbyDeletionInput, target models.EmbyDeletionTarget) error {
		verificationCalls++
		if err := models.CheckEmbyWebhookClaim(ctx, record); err != nil {
			return err
		}
		err := worker.verify(ctx, input, target)
		observeVerification(err)
		if alive, ok := errors.AsType[*embySurvivingItemsError](err); ok {
			return &models.EmbyDeletionOwnerConflict{IDs: alive.IDs, Err: err}
		}
		if shared, ok := errors.AsType[*embyProtectedOwnersError](err); ok {
			return &models.EmbyDeletionOwnerConflict{IDs: shared.IDs, Err: err}
		}
		return err
	}

	var plan models.EmbyDeletionPlan
	if record.PlanJSON != "" {
		if err := json.Unmarshal([]byte(record.PlanJSON), &plan); err != nil {
			return err
		}
		if !plan.Input.CleanupPolicy.AllowsJointBatch() {
			return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, "unsupported_cleanup_policy", 0, false)
		}
		if err := models.ValidateEmbyDeletionInputMetadata(plan.Input); err != nil {
			return finishEmbyWebhook(ctx, record, models.EmbyWebhookUnresolved, "invalid_metadata_evidence", 0, false)
		}
	} else {
		plan, err = models.BuildEmbyDeletionPlan(ctx, input, worker.provider, verify)
		if err != nil {
			return err
		}
		var absent []models.EmbyDeletionResult
		plan, absent, err = models.ConfirmEmbyDeletionPlanInventory(ctx, plan, worker.provider, verify)
		if err != nil {
			return err
		}
		if err := models.SaveEmbyWebhookPlan(ctx, record, plan, absent...); err != nil {
			return err
		}
		// 目录准入失败可正常降级；文件批次会独立核验并分类真实执行错误。
		verificationFailure = false
	}
	saved, err := models.LoadEmbyWebhookTargets(ctx, record.ID)
	if err != nil {
		return err
	}
	states := make(map[string]models.EmbyWebhookTarget, len(saved))
	for _, target := range saved {
		states[target.TargetKey] = target
	}
	complete := func(key string) bool {
		state := states[key]
		return state.Outcome == models.EmbyDeletionDeleted || state.Outcome == models.EmbyDeletionAlreadyAbsent
	}
	names := make(map[string]string, len(plan.Targets))
	kinds := make(map[string]string, len(plan.Targets))
	for _, target := range plan.Targets {
		names[target.Key] = target.File.FileName
		kinds[target.Key] = target.Kind
	}
	persist := func(attempt string, results []models.EmbyDeletionResult) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := persistWebhook(ctx, func() error { return models.SaveEmbyWebhookBatchResults(ctx, record, attempt, results) }); err != nil {
			return err
		}
		rows, err := models.LoadEmbyWebhookTargets(ctx, record.ID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			states[row.TargetKey] = row
		}
		if helpers.AppLogger != nil {
			item := embyWebhookLogItem(record.ItemID, embyWebhookRecordLabel(record))
			for _, result := range results {
				reasonLabel := embyWebhookReasonLabel(webhookLogURL.ReplaceAllString(result.Reason, "[URL]"))
				if reasonLabel != "" {
					reasonLabel = "，原因：" + reasonLabel
				}
				helpers.AppLogger.Infof("Emby 删除文件结果：通知 #%d，ItemId %s，%s %q，结果：%s%s", record.ID, item, embyDeletionTargetNoun(kinds[result.Key]),
					webhookLogURL.ReplaceAllString(names[result.Key], "[URL]"), embyDeletionOutcomeLabel(result.Outcome), reasonLabel)
			}
		}
		return nil
	}
	confirmExhausted := func(target models.EmbyDeletionTarget) error {
		provider, err := worker.provider(target.File)
		if err != nil {
			return err
		}
		result := models.ConfirmEmbyWebhookTargetAbsence(ctx, plan, target, provider, verify)
		// 确认不属于旧 claim 的发送，不补写旧 attempt，也不增加发送次数。
		return persist("", []models.EmbyDeletionResult{result})
	}
	// 根操作在前；正常成功后，覆盖成员与根在一个事务中完成，后续没有子项 Stat。
	for _, target := range plan.Targets {
		state, found := states[target.Key]
		if !found {
			return errors.New("Emby 删除目标尚未持久保存")
		}
		if target.Kind != "directory" || complete(target.Key) {
			continue
		}
		if state.Attempts >= models.EmbyWebhookTargetMaxAttempts {
			if err := confirmExhausted(target); err != nil {
				return err
			}
			continue
		}
		provider, err := worker.provider(target.File)
		if err != nil {
			return err
		}
		previous, err := models.FindEmbyWebhookSuccess(ctx, record, target)
		if err != nil {
			return err
		}
		if previous != nil {
			provider = embyCompletedDirectoryProvider{EmbyDeleteProvider: provider}
		}
		attempt := ""
		result := models.ExecuteEmbyDeletionDirectory(ctx, plan, target, provider, verify, func() error {
			var err error
			attempt, err = models.BeginEmbyWebhookAttempt(ctx, record, []models.EmbyDeletionTarget{target})
			return err
		}, state.Attempts > 0)
		if err := persist(attempt, []models.EmbyDeletionResult{result}); err != nil {
			return err
		}
	}
	var pending []models.EmbyDeletionTarget
	for _, target := range plan.Targets {
		state := states[target.Key]
		if target.Kind == "directory" || complete(target.Key) {
			continue
		}
		if state.Attempts >= models.EmbyWebhookTargetMaxAttempts {
			if err := confirmExhausted(target); err != nil {
				return err
			}
			continue
		}
		previous, err := models.FindEmbyWebhookSuccess(ctx, record, target)
		if err != nil {
			return err
		}
		if previous == nil {
			pending = append(pending, target)
			continue
		}
		provider, err := worker.provider(target.File)
		if err != nil {
			return err
		}
		// 同代际成功只允许确认缺失，原对象重现不能再次删除。
		results := models.ExecuteEmbyDeletionBatch(ctx, plan, []models.EmbyDeletionTarget{target}, embyCompletedProvider{provider}, verify, func([]models.EmbyDeletionTarget) error {
			return fmt.Errorf("%w，保留重新出现的对象", models.ErrEmbyDeletionReappeared)
		})
		if err := persist("", results); err != nil {
			return err
		}
	}
	groups, err := models.GroupEmbyDeletionTargets(pending, worker.provider)
	if err != nil {
		return err
	}
	executeGroup := func(group []models.EmbyDeletionTarget) error {
		if len(group) == 0 {
			return nil
		}
		provider, err := worker.provider(group[0].File)
		if err != nil {
			return err
		}
		attempt := ""
		beforeVerification := verificationCalls
		results := models.ExecuteEmbyDeletionBatch(ctx, plan, group, provider, verify, func(members []models.EmbyDeletionTarget) error {
			var err error
			attempt, err = models.BeginEmbyWebhookAttempt(ctx, record, members)
			return err
		})
		if verificationCalls == beforeVerification && slices.ContainsFunc(results, func(result models.EmbyDeletionResult) bool { return result.Outcome == models.EmbyDeletionUnresolved }) {
			// 本地新引用可先阻止云端写入，但仍须独立核验旧条目能否清理索引。
			merged := group[0]
			merged.Owners = slices.Clone(merged.Owners)
			var ids []string
			for _, target := range group {
				for _, ref := range target.Owners {
					if !slices.Contains(ids, ref.ItemID) {
						ids = append(ids, ref.ItemID)
					}
					if !slices.Contains(merged.Owners, ref) {
						merged.Owners = append(merged.Owners, ref)
					}
				}
			}
			localErr := verifyEmbyLocalDeletion(ctx, input, ids)
			observeVerification(localErr)
			if localErr == nil {
				_ = verify(ctx, input, merged)
			}
		}
		return persist(attempt, results)
	}
	remainingVideos := make(map[string]bool, len(pending))
	for _, target := range pending {
		if target.Kind == "video" {
			remainingVideos[target.Key] = true
		}
	}
	var deferred []models.EmbyDeletionTarget
	for _, group := range groups {
		groupVideos := make(map[string]bool, len(group))
		for _, target := range group {
			if target.Kind == "video" {
				groupVideos[target.Key] = true
				delete(remainingVideos, target.Key)
			}
		}
		var eligible []models.EmbyDeletionTarget
		var retained []models.EmbyDeletionResult
		for _, target := range group {
			blocked, waitForVideo := false, false
			if target.Kind != "video" {
				for _, video := range plan.Targets {
					// 同批视频由执行器核验；组外旧结果须区分后续待执行与已确认保留。
					if video.Kind != "video" || groupVideos[video.Key] || states[video.Key].Outcome != models.EmbyDeletionUnresolved || !slices.ContainsFunc(video.Owners, func(owner models.EmbyDeletionOwnerRef) bool {
						return slices.Contains(target.Owners, owner)
					}) {
						continue
					}
					if remainingVideos[video.Key] {
						waitForVideo = true
					} else {
						blocked = true
						break
					}
				}
			}
			switch {
			case blocked:
				retained = append(retained, models.EmbyDeletionResult{Key: target.Key, Outcome: models.EmbyDeletionUnresolved, Reason: "原视频在其他批次已确认需保留或身份不明，保留其元数据"})
			case waitForVideo:
				deferred = append(deferred, target)
			default:
				eligible = append(eligible, target)
			}
		}
		if len(retained) > 0 {
			if err := persist("", retained); err != nil {
				return err
			}
		}
		if err := executeGroup(eligible); err != nil {
			return err
		}
	}
	if len(deferred) > 0 {
		var eligible []models.EmbyDeletionTarget
		var retained []models.EmbyDeletionResult
		for _, target := range deferred {
			// 本轮暂缓的附件只在全部相关视频完成后恢复，不能仅检查触发暂缓的目标。
			blocked := slices.ContainsFunc(plan.Targets, func(video models.EmbyDeletionTarget) bool {
				return video.Kind == "video" && !complete(video.Key) && slices.ContainsFunc(video.Owners, func(owner models.EmbyDeletionOwnerRef) bool {
					return slices.Contains(target.Owners, owner)
				})
			})
			if blocked {
				retained = append(retained, models.EmbyDeletionResult{Key: target.Key, Outcome: models.EmbyDeletionUnresolved, Reason: "原视频尚未完成删除，保留本轮暂缓的元数据"})
			} else {
				eligible = append(eligible, target)
			}
		}
		if len(retained) > 0 {
			if err := persist("", retained); err != nil {
				return err
			}
		}
		deferredGroups, err := models.GroupEmbyDeletionTargets(eligible, worker.provider)
		if err != nil {
			return err
		}
		for _, group := range deferredGroups {
			if err := executeGroup(group); err != nil {
				return err
			}
		}
	}
	if len(survivors) > 0 {
		slices.Sort(survivors)
		if err := models.AdmitEmbyWebhookSurvivors(ctx, record, slices.Compact(survivors)); err != nil {
			return err
		}
	}
	if len(protected) > 0 {
		slices.Sort(protected)
		protected = slices.Compact(protected)
		if err := verifyEmbyLocalDeletion(ctx, input, protected); err != nil {
			return err
		}
		if err := models.FinalizeEmbyWebhookLocal(ctx, record, protected); err != nil {
			return err
		}
	}
	results := make([]models.EmbyDeletionResult, 0, len(states))
	unresolved, retry := len(plan.Targets) == 0 || len(plan.Issues) > 0, verificationFailure
	for _, state := range states {
		results = append(results, models.EmbyDeletionResult{Key: state.TargetKey, Outcome: state.Outcome, Reason: state.Reason})
		if state.Outcome == models.EmbyDeletionFailed {
			retry = true
		}
		if !complete(state.TargetKey) {
			unresolved = true
		}
	}
	if err := models.FinalizeEmbyDeletionPlan(ctx, plan, results); err != nil {
		return err
	}
	if retry {
		return errors.New("部分 Emby 删除目标未完成，保留身份并重新核验")
	}
	status, reason := models.EmbyWebhookDone, ""
	if unresolved {
		status, reason = models.EmbyWebhookUnresolved, "部分目标或目录证据不完整，已保留原因"
	}
	return finishEmbyWebhook(ctx, record, status, reason, 0, false)
}

type embyCompletedDirectoryProvider struct{ models.EmbyDeleteProvider }

func (p embyCompletedDirectoryProvider) SupportsDirectoryDelete() bool {
	directory, ok := p.EmbyDeleteProvider.(models.EmbyDeleteDirectoryProvider)
	return ok && directory.SupportsDirectoryDelete()
}

func (p embyCompletedDirectoryProvider) StatDirectory(ctx context.Context, scope models.EmbyDirectoryScope) (models.EmbyRemoteFile, error) {
	directory, ok := p.EmbyDeleteProvider.(models.EmbyDeleteDirectoryProvider)
	if !ok {
		return models.EmbyRemoteFile{}, models.ErrEmbyDeleteUnsupported
	}
	return directory.StatDirectory(ctx, scope)
}

func (embyCompletedDirectoryProvider) DeleteDirectory(context.Context, models.EmbyDirectoryScope, func() error) (bool, error) {
	return false, fmt.Errorf("%w，保留重新出现的对象", models.ErrEmbyDeletionReappeared)
}
