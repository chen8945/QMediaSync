package models

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/syncscope"
)

type embyDeletionScopeKey struct{}

// EmbyDeletionOwnerConflict 标识实时核验明确阻止的旧成员；未知错误不能缩小范围。
type EmbyDeletionOwnerConflict struct {
	IDs []string
	Err error
}

func (e *EmbyDeletionOwnerConflict) Error() string { return e.Err.Error() }
func (e *EmbyDeletionOwnerConflict) Unwrap() error { return e.Err }

// EmbyDeletionTargetConflict 仅阻止有明确保留使用者的目标，保留同批其他文件。
type EmbyDeletionTargetConflict struct {
	Keys []string
	Err  error
}

func (e *EmbyDeletionTargetConflict) Error() string { return e.Err.Error() }
func (e *EmbyDeletionTargetConflict) Unwrap() error { return e.Err }

// BeginEmbyDeletionExecution 在一次领取期间持有范围；释放后不得复用其核验上下文。
func BeginEmbyDeletionExecution(ctx context.Context) (context.Context, func(), error) {
	release, err := syncscope.Acquire(ctx, syncscope.Scope{Global: true})
	if err != nil {
		return ctx, nil, err
	}
	held, cancel := context.WithCancel(context.WithValue(ctx, embyDeletionScopeKey{}, true))
	held, invalidate := beginEmbyDirectoryChecks(held)
	return held, func() { cancel(); invalidate(); release() }, nil
}

func acquireEmbyDeletionScope(ctx context.Context) (func(), error) {
	if held, _ := ctx.Value(embyDeletionScopeKey{}).(bool); held {
		return func() {}, ctx.Err()
	}
	return syncscope.Acquire(ctx, syncscope.Scope{Global: true})
}

// GroupEmbyDeletionTargets 按真实来源、账号和父目录分组，不因重复 SyncPath 拆包。
func GroupEmbyDeletionTargets(targets []EmbyDeletionTarget, factory EmbyDeleteProviderFactory) ([][]EmbyDeletionTarget, error) {
	var groups [][]EmbyDeletionTarget
	for _, target := range targets {
		if target.Kind != "video" && target.Kind != "sidecar" && target.Kind != "scoped_metadata" {
			continue
		}
		provider, err := factory(target.File)
		if err != nil {
			return nil, err
		}
		batch, ok := provider.(EmbyDeleteBatchProvider)
		if !ok {
			groups = append(groups, []EmbyDeletionTarget{target})
			continue
		}
		maxFiles, maxBytes := batch.BatchLimits()
		if maxFiles < 1 || maxBytes < 1 {
			return nil, ErrEmbyDeleteUnsupported
		}
		added := false
		for i, group := range groups {
			if !embySameBatchParent(group[0].File, target.File) || len(group) >= maxFiles {
				continue
			}
			files := make([]EmbyFrozenFile, 0, len(group)+1)
			for _, member := range group {
				files = append(files, member.File)
			}
			files = append(files, target.File)
			size, err := EmbyDeleteBatchPayloadSize(files)
			if err != nil {
				return nil, err
			}
			if size <= maxBytes {
				groups[i] = append(groups[i], target)
				added = true
				break
			}
		}
		if !added {
			size, err := EmbyDeleteBatchPayloadSize([]EmbyFrozenFile{target.File})
			if err != nil || size > maxBytes {
				return nil, fmt.Errorf("删除目标超出批次请求边界: %w", ErrEmbyDeleteUnverified)
			}
			groups = append(groups, []EmbyDeletionTarget{target})
		}
	}
	return groups, nil
}

func embySameBatchParent(left, right EmbyFrozenFile) bool {
	return left.SourceType == right.SourceType && left.AccountID == right.AccountID && left.AccountIdentity == right.AccountIdentity &&
		left.ParentID == right.ParentID && embyRemoteDirectoriesMatch(left.SourceType, left.Path, right.Path)
}

func registerEmbyDeletionTargets(ctx context.Context, plan EmbyDeletionPlan, targets []EmbyDeletionTarget) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockEmbyIndexState(tx); err != nil {
			return err
		}
		var ids []string
		for _, target := range targets {
			for _, ref := range target.Owners {
				var state EmbyItemState
				if err := tx.Where("server_id = ? AND item_id = ?", plan.Input.ServerID, ref.ItemID).First(&state).Error; err != nil {
					return err
				}
				if state.SnapshotID != ref.SnapshotID || state.Generation != ref.Generation {
					return ErrEmbySnapshotStale
				}
				if !state.Deleted && !slices.Contains(ids, ref.ItemID) {
					ids = append(ids, ref.ItemID)
				}
			}
		}
		if len(ids) > 0 {
			_, err := RegisterEmbyDeletionTx(tx, plan.Input.ServerID, ids)
			return err
		}
		return nil
	})
}

// 删除屏障一旦被存活条目释放，旧执行者不能继续使用原负查询结果。
func validateEmbyDeletionBarrier(ctx context.Context, input EmbyDeletionInput, targets []EmbyDeletionTarget) error {
	refs := map[string]EmbyDeletionOwnerRef{}
	var ids []string
	for _, target := range targets {
		for _, ref := range target.Owners {
			if previous, exists := refs[ref.ItemID]; exists && previous != ref {
				return ErrEmbyIdentityAmbiguous
			}
			if _, exists := refs[ref.ItemID]; !exists {
				ids = append(ids, ref.ItemID)
			}
			refs[ref.ItemID] = ref
		}
	}
	var states []EmbyItemState
	if err := db.Db.WithContext(ctx).Where("server_id = ? AND item_id IN ?", input.ServerID, ids).Find(&states).Error; err != nil {
		return err
	}
	if len(states) != len(refs) {
		return ErrEmbySnapshotStale
	}
	for _, state := range states {
		ref := refs[state.ItemID]
		if !state.Deleted || state.SnapshotID != ref.SnapshotID || state.Generation != ref.Generation {
			return ErrEmbySnapshotStale
		}
	}
	return nil
}

func embyDeletionBatchTarget(targets []EmbyDeletionTarget) EmbyDeletionTarget {
	merged := targets[0]
	merged.Owners = slices.Clone(merged.Owners)
	merged.ScopedMetadata = nil
	for _, target := range targets {
		if target.Kind == "scoped_metadata" {
			merged.ScopedMetadata = append(merged.ScopedMetadata, target.File)
		}
	}
	for _, target := range targets[1:] {
		for _, ref := range target.Owners {
			if !slices.Contains(merged.Owners, ref) {
				merged.Owners = append(merged.Owners, ref)
			}
		}
	}
	return merged
}

// ExecuteEmbyDeletionBatch 共同提交选定视频与专属旁车，逐原对象确认部分结果。
// beforeSend 必须先持久登记实际发送的成员，失败即禁止写请求。
func ExecuteEmbyDeletionBatch(ctx context.Context, plan EmbyDeletionPlan, targets []EmbyDeletionTarget, provider EmbyDeleteProvider, verify EmbyDeletionVerifier, beforeSend func([]EmbyDeletionTarget) error) []EmbyDeletionResult {
	results := make([]EmbyDeletionResult, 0, len(targets))
	failAll := func(outcome EmbyDeletionOutcome, err error) []EmbyDeletionResult {
		for _, target := range targets {
			results = append(results, EmbyDeletionResult{Key: target.Key, Outcome: outcome, Reason: redactEmbySnapshotError(err.Error())})
		}
		return results
	}
	if len(targets) == 0 {
		return results
	}
	if !plan.Input.CleanupPolicy.AllowsJointBatch() || provider == nil || verify == nil || beforeSend == nil {
		return failAll(EmbyDeletionUnresolved, ErrEmbyDeleteUnverified)
	}
	release, err := acquireEmbyDeletionScope(ctx)
	if err != nil {
		return failAll(EmbyDeletionFailed, err)
	}
	defer release()
	blockedOwners := map[EmbyDeletionOwnerRef]bool{}
	reject := func(target EmbyDeletionTarget, outcome EmbyDeletionOutcome, err error) {
		results = append(results, EmbyDeletionResult{Key: target.Key, Outcome: outcome, Reason: redactEmbySnapshotError(err.Error())})
		if target.Kind == "video" {
			for _, ref := range target.Owners {
				blockedOwners[ref] = true
			}
		}
	}
	protectDependentMetadata := func(members []EmbyDeletionTarget) []EmbyDeletionTarget {
		var eligible []EmbyDeletionTarget
		for _, target := range members {
			if target.Kind != "video" && slices.ContainsFunc(target.Owners, func(ref EmbyDeletionOwnerRef) bool { return blockedOwners[ref] }) {
				reject(target, EmbyDeletionUnresolved, errors.New("配套文件的原视频仍需保留或无法确认"))
				continue
			}
			eligible = append(eligible, target)
		}
		return eligible
	}
	var eligible []EmbyDeletionTarget
	for _, target := range targets {
		if target.Kind != "video" && target.Kind != "sidecar" && target.Kind != "scoped_metadata" || target.Reason != "" || target.File.Reason != "" ||
			!embySameBatchParent(targets[0].File, target.File) || !slices.ContainsFunc(plan.Targets, func(known EmbyDeletionTarget) bool { return embyJSON(known) == embyJSON(target) }) {
			reject(target, EmbyDeletionUnresolved, ErrEmbyIdentityAmbiguous)
			continue
		}
		if err := validateEmbyDeletionTarget(ctx, plan, target); err != nil {
			// 只隔离已明确的成员冲突。数据库／未知查询错误仍阻止整个共同请求。
			if !errors.Is(err, ErrEmbyDeleteUnverified) && !errors.Is(err, ErrEmbyIdentityAmbiguous) && !errors.Is(err, ErrEmbySnapshotStale) && !errors.Is(err, ErrEmbyDeleteUnsupported) && !errors.Is(err, gorm.ErrRecordNotFound) {
				results = results[:0]
				return failAll(EmbyDeletionUnresolved, err)
			}
			reject(target, EmbyDeletionUnresolved, err)
			continue
		}
		eligible = append(eligible, target)
	}
	targets = protectDependentMetadata(eligible)
	if len(targets) == 0 {
		return results
	}
	if err := registerEmbyDeletionTargets(ctx, plan, targets); err != nil {
		return failAll(EmbyDeletionUnresolved, err)
	}
	for len(targets) > 0 {
		err := verify(ctx, plan.Input, embyDeletionBatchTarget(targets))
		if err == nil {
			break
		}
		if conflict, known := errors.AsType[*EmbyDeletionTargetConflict](err); known && len(conflict.Keys) > 0 {
			for _, key := range conflict.Keys {
				if !slices.ContainsFunc(targets, func(target EmbyDeletionTarget) bool { return target.Key == key }) {
					return failAll(EmbyDeletionUnresolved, err)
				}
			}
			eligible = nil
			for _, target := range targets {
				if slices.Contains(conflict.Keys, target.Key) {
					reject(target, EmbyDeletionUnresolved, err)
				} else {
					eligible = append(eligible, target)
				}
			}
			targets = protectDependentMetadata(eligible)
			continue
		}
		conflict, known := errors.AsType[*EmbyDeletionOwnerConflict](err)
		if !known || len(conflict.IDs) == 0 {
			return failAll(EmbyDeletionUnresolved, err)
		}
		for _, id := range conflict.IDs {
			if !slices.ContainsFunc(targets, func(target EmbyDeletionTarget) bool {
				return slices.ContainsFunc(target.Owners, func(ref EmbyDeletionOwnerRef) bool { return ref.ItemID == id })
			}) {
				// 整剧／整季外层条目仍存在等全局冲突不得缩为某个 Episode。
				return failAll(EmbyDeletionUnresolved, err)
			}
		}
		eligible = nil
		for _, target := range targets {
			if slices.ContainsFunc(target.Owners, func(ref EmbyDeletionOwnerRef) bool { return slices.Contains(conflict.IDs, ref.ItemID) }) {
				reject(target, EmbyDeletionUnresolved, err)
			} else {
				eligible = append(eligible, target)
			}
		}
		targets = protectDependentMetadata(eligible)
		// 异常时重新核验剩余 owner 集合，完整清单继续使用同一有界上下文。
	}
	if len(targets) == 0 {
		return results
	}
	listing, err := provider.List(ctx, targets[0].File)
	if err != nil || !embyValidListing(targets[0].File.Path, listing) {
		// 目录已经不存在时，不能让原父目录列表阻断已知原 ID 的异常恢复。
		// 这里只确认结果，不在缺少完整旁车清单时发送任何删除。
		outcome := EmbyDeletionFailed
		if err == nil {
			err, outcome = ErrEmbyIdentityAmbiguous, EmbyDeletionUnresolved
		}
		for _, target := range targets {
			remote, statErr := provider.Stat(ctx, target.File)
			switch {
			case errors.Is(statErr, ErrEmbyRemoteFileAbsent):
				results = append(results, EmbyDeletionResult{Key: target.Key, Outcome: EmbyDeletionAlreadyAbsent})
			case statErr != nil:
				reject(target, EmbyDeletionFailed, statErr)
			case !embyRemoteMatches(target.File, remote):
				reject(target, EmbyDeletionUnresolved, errors.New("原目录不可列举且已知文件身份或位置变化"))
			default:
				reject(target, outcome, err)
			}
		}
		return results
	}
	eligible = nil
	for _, target := range targets {
		if target.Kind != "sidecar" {
			eligible = append(eligible, target)
			continue
		}
		videos := embyMergeVideos(target.DirectoryVideos, embyDirectoryVideos(listing))
		refs, exclusive := embySidecarOwners(target.File.FileName, videos, embyDirectoryTargets(plan, target.File))
		if !exclusive || len(refs) != len(target.Owners) {
			reject(target, EmbyDeletionUnresolved, errors.New("配套文件存在需保留或来源不明的视频"))
			continue
		}
		if slices.ContainsFunc(refs, func(ref EmbyDeletionOwnerRef) bool { return !slices.Contains(target.Owners, ref) }) {
			reject(target, EmbyDeletionUnresolved, ErrEmbyIdentityAmbiguous)
			continue
		}
		eligible = append(eligible, target)
	}
	targets = eligible
	var pending []EmbyDeletionTarget
	for _, target := range targets {
		matching := slices.ContainsFunc(listing, func(remote EmbyRemoteFile) bool { return embyRemoteMatches(target.File, remote) })
		if !matching {
			remote, err := provider.Stat(ctx, target.File)
			if errors.Is(err, ErrEmbyRemoteFileAbsent) {
				results = append(results, EmbyDeletionResult{Key: target.Key, Outcome: EmbyDeletionAlreadyAbsent})
				continue
			}
			if err != nil {
				reject(target, EmbyDeletionFailed, err)
				continue
			}
			if !embyRemoteMatches(target.File, remote) {
				reject(target, EmbyDeletionUnresolved, errors.New("远端文件身份或内容已变化"))
				continue
			}
		}
		pending = append(pending, target)
	}
	pending = protectDependentMetadata(pending)
	if len(pending) == 0 {
		return results
	}
	guard := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, target := range pending {
			if err := validateEmbyDeletionTarget(ctx, plan, target); err != nil {
				return err
			}
		}
		if err := verify(ctx, plan.Input, embyDeletionBatchTarget(pending)); err != nil {
			return err
		}
		if err := validateEmbyDeletionBarrier(ctx, plan.Input, pending); err != nil {
			return err
		}
		return beforeSend(pending)
	}
	files := make([]EmbyFrozenFile, 0, len(pending))
	for _, target := range pending {
		files = append(files, target.File)
	}
	var sent bool
	var sendErr error
	if batch, ok := provider.(EmbyDeleteBatchProvider); ok {
		sent, sendErr = batch.DeleteBatch(ctx, files, guard)
	} else if len(files) == 1 {
		sent, sendErr = provider.Delete(ctx, files[0], guard)
	} else {
		sendErr = ErrEmbyDeleteUnsupported
	}
	// 无论整体响应是否成功，都按冻结原身份确认；只把真实缺失成员记为完成。
	for _, target := range pending {
		remote, statErr := provider.Stat(ctx, target.File)
		result := EmbyDeletionResult{Key: target.Key, Outcome: EmbyDeletionFailed}
		switch {
		case errors.Is(statErr, ErrEmbyRemoteFileAbsent):
			result.Outcome = EmbyDeletionDeleted
		case statErr != nil:
			result.Reason = redactEmbySnapshotError(statErr.Error())
		case !embyRemoteMatches(target.File, remote):
			result.Outcome, result.Reason = EmbyDeletionUnresolved, "请求后原文件身份或位置变化，保留核验"
		case errors.Is(sendErr, ErrEmbyDeletionReappeared):
			result.Outcome, result.Reason = EmbyDeletionUnresolved, redactEmbySnapshotError(sendErr.Error())
		case sendErr != nil:
			result.Reason = redactEmbySnapshotError(sendErr.Error())
		case !sent:
			result.Reason = "网盘未确认批量删除，原文件仍存在"
		default:
			result.Reason = "网盘返回成功但原文件仍存在"
		}
		results = append(results, result)
	}
	return results
}
