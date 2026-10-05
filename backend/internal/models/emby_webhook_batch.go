package models

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

// EmbyWebhookTargetMaxAttempts 限制每个目标参与的实际删除发送次数，不包含收件的只读重试。
const EmbyWebhookTargetMaxAttempts = 4

// ErrEmbyWebhookAttemptLimit 表示至少一个实际参与目标已不能登记新的删除发送。
var ErrEmbyWebhookAttemptLimit = errors.New("Emby 删除目标发送次数已达上限")

// EmbyDeletionAttempt 在发送前持久登记；一次共同写入共用同一个 ID。
type EmbyDeletionAttempt struct {
	ID         string   `json:"id"`
	ClaimToken string   `json:"claim_token"`
	TargetKeys []string `json:"target_keys"`
	StartedAt  int64    `json:"started_at"`
	FinishedAt int64    `json:"finished_at,omitempty"`
}

// EmbyWebhookTargetExecution 保存冻结目标及其操作历史，操作历史不改写冻结计划。
type EmbyWebhookTargetExecution struct {
	EmbyDeletionTarget
	ExecutionAttempts []EmbyDeletionAttempt `json:"execution_attempts,omitempty"`
}

// ConfirmEmbyWebhookTargetAbsence 在发送次数耗尽后只确认原对象；失败结果的 FinishedAt 不证明远端未执行。
func ConfirmEmbyWebhookTargetAbsence(ctx context.Context, plan EmbyDeletionPlan, target EmbyDeletionTarget, provider EmbyDeleteProvider, verify EmbyDeletionVerifier) EmbyDeletionResult {
	result := EmbyDeletionResult{Key: target.Key, Outcome: EmbyDeletionUnresolved}
	fail := func(outcome EmbyDeletionOutcome, err error) EmbyDeletionResult {
		result.Outcome, result.Reason = outcome, redactEmbySnapshotError(err.Error())
		return result
	}
	if !plan.Input.CleanupPolicy.AllowsJointBatch() || provider == nil || verify == nil || target.Reason != "" || target.File.Reason != "" {
		return fail(EmbyDeletionUnresolved, ErrEmbyDeleteUnverified)
	}
	if !slices.ContainsFunc(plan.Targets, func(known EmbyDeletionTarget) bool { return embyJSON(known) == embyJSON(target) }) {
		return fail(EmbyDeletionUnresolved, ErrEmbyIdentityAmbiguous)
	}
	release, err := acquireEmbyDeletionScope(ctx)
	if err != nil {
		return fail(EmbyDeletionFailed, err)
	}
	defer release()
	validate := func() error {
		if target.Kind == "directory" {
			if err := validateEmbyDirectoryTarget(ctx, plan, target); err != nil {
				return err
			}
		} else if err := validateEmbyDeletionTarget(ctx, plan, target); err != nil {
			return err
		}
		return validateEmbyDeletionBarrier(ctx, plan.Input, []EmbyDeletionTarget{target})
	}
	if err := validate(); err != nil {
		return fail(EmbyDeletionUnresolved, err)
	}
	verificationTarget := target
	// 只确认原 ID 缺失，不申请目录内容删除权限；已消失根的列表不能阻断恢复。
	// 原目录的冻结范围与身份仍由 validate 核对，条目、STRM 与存活来源核验保持。
	verificationTarget.Directory = nil
	if err := verify(ctx, plan.Input, verificationTarget); err != nil {
		return fail(EmbyDeletionUnresolved, err)
	}
	var remote EmbyRemoteFile
	if target.Kind == "directory" {
		directory, ok := provider.(EmbyDeleteDirectoryProvider)
		if !ok {
			return fail(EmbyDeletionUnresolved, ErrEmbyDeleteUnsupported)
		}
		remote, err = directory.StatDirectory(ctx, *target.Directory)
	} else {
		remote, err = provider.Stat(ctx, target.File)
	}
	// 远端读取期间账号、账本或代际变化时，旧查询不能生成新的完成结果。
	if validateErr := validate(); validateErr != nil {
		return fail(EmbyDeletionUnresolved, validateErr)
	}
	if errors.Is(err, ErrEmbyRemoteFileAbsent) {
		result.Outcome = EmbyDeletionAlreadyAbsent
		if target.Kind == "directory" {
			// 根缺失不能推导 CoveredKeys 缺失，worker 仍须逐个确认已知成员。
			result.Reason = "original_directory_id_absent"
		}
		return result
	}
	if err != nil {
		return fail(EmbyDeletionFailed, err)
	}
	if target.Kind == "directory" && !embyDirectoryIdentityMatches(*target.Directory, remote) || target.Kind != "directory" && !embyRemoteMatches(target.File, remote) {
		result.Reason = "删除发送次数已达上限，原对象身份或位置已变化"
	} else {
		result.Reason = "删除发送次数已达上限，原对象仍存在"
	}
	return result
}

// BeginEmbyWebhookAttempt 在短事务确认 claim 和原目标后登记实际写入成员。
func BeginEmbyWebhookAttempt(ctx context.Context, record EmbyWebhookRecord, targets []EmbyDeletionTarget) (string, error) {
	var attemptID string
	err := db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		var plan EmbyDeletionPlan
		if err := json.Unmarshal([]byte(current.PlanJSON), &plan); err != nil {
			return err
		}
		if !plan.Input.CleanupPolicy.AllowsJointBatch() || len(targets) == 0 {
			return ErrEmbyIdentityAmbiguous
		}
		keys := make([]string, 0, len(targets))
		for _, target := range targets {
			if !slices.ContainsFunc(plan.Targets, func(known EmbyDeletionTarget) bool { return embyJSON(known) == embyJSON(target) }) {
				return ErrEmbyIdentityAmbiguous
			}
			keys = append(keys, target.Key)
			keys = append(keys, target.CoveredKeys...)
		}
		slices.Sort(keys)
		keys = slices.Compact(keys)
		var rows []EmbyWebhookTarget
		if err := tx.Where("record_id = ? AND target_key IN ?", record.ID, keys).Order("id").Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) != len(keys) {
			return ErrEmbyIdentityAmbiguous
		}
		requested := map[string]bool{}
		for _, target := range targets {
			requested[target.Key] = true
		}
		rows = slices.DeleteFunc(rows, func(row EmbyWebhookTarget) bool {
			return !requested[row.TargetKey] && (row.Outcome == EmbyDeletionDeleted || row.Outcome == EmbyDeletionAlreadyAbsent)
		})
		keys = keys[:0]
		for _, row := range rows {
			keys = append(keys, row.TargetKey)
		}
		slices.Sort(keys)
		attempts := make([]int, 0, len(rows))
		for _, row := range rows {
			if row.Outcome == EmbyDeletionDeleted || row.Outcome == EmbyDeletionAlreadyAbsent {
				return ErrEmbyIdentityAmbiguous
			}
			attempts = append(attempts, row.Attempts)
		}
		// 同一实际发送 guard 的重复调用复用未完成登记，不重复消耗次数。
		var leader EmbyWebhookTargetExecution
		if err := json.Unmarshal([]byte(rows[0].TargetJSON), &leader); err != nil {
			return err
		}
		if len(leader.ExecutionAttempts) > 0 {
			last := leader.ExecutionAttempts[len(leader.ExecutionAttempts)-1]
			if last.ClaimToken == record.ClaimToken && last.FinishedAt == 0 && slices.Equal(last.TargetKeys, keys) {
				attemptID = last.ID
				return nil
			}
		}
		// 目录失败后的文件回退可能让 covered 成员先耗尽；整目录发送同样受其上限约束。
		for _, row := range rows {
			if row.Attempts >= EmbyWebhookTargetMaxAttempts {
				return ErrEmbyWebhookAttemptLimit
			}
		}
		attemptID = embyDigest([]any{record.ID, record.ClaimToken, keys, attempts})
		attempt := EmbyDeletionAttempt{ID: attemptID, ClaimToken: record.ClaimToken, TargetKeys: keys, StartedAt: time.Now().Unix()}
		for i, row := range rows {
			var execution EmbyWebhookTargetExecution
			if err := json.Unmarshal([]byte(row.TargetJSON), &execution); err != nil {
				return err
			}
			if execution.Key != row.TargetKey {
				return ErrEmbyIdentityAmbiguous
			}
			entry := attempt
			if i != 0 {
				entry.TargetKeys = nil
			}
			execution.ExecutionAttempts = append(execution.ExecutionAttempts, entry)
			encoded := embyJSON(execution)
			changed := row
			changed.TargetJSON = encoded
			changed.Attempts++
			if err := checkEmbyWebhookSize(changed); err != nil {
				return err
			}
			if err := tx.Model(&row).Updates(map[string]any{"target_json": encoded, "attempts": gorm.Expr("attempts + 1")}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return attemptID, nil
}

// SaveEmbyWebhookBatchResults 原子保存逐项确认；目录成功覆盖的已知成员与父操作一起提交。
func SaveEmbyWebhookBatchResults(ctx context.Context, record EmbyWebhookRecord, attemptID string, results []EmbyDeletionResult) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		var plan EmbyDeletionPlan
		if err := json.Unmarshal([]byte(current.PlanJSON), &plan); err != nil {
			return err
		}
		if !plan.Input.CleanupPolicy.AllowsJointBatch() {
			return ErrEmbyIdentityAmbiguous
		}
		if attemptID != "" {
			var attempted []EmbyWebhookTarget
			if err := tx.Where("record_id = ?", record.ID).Find(&attempted).Error; err != nil {
				return err
			}
			found := false
			for _, row := range attempted {
				var execution EmbyWebhookTargetExecution
				if err := json.Unmarshal([]byte(row.TargetJSON), &execution); err != nil {
					return err
				}
				for _, attempt := range execution.ExecutionAttempts {
					if attempt.ID == attemptID && attempt.ClaimToken == record.ClaimToken {
						found = true
					}
				}
			}
			if !found {
				return ErrEmbyIdentityAmbiguous
			}
		}
		seen := map[string]bool{}
		for _, result := range results {
			if seen[result.Key] {
				return ErrEmbyIdentityAmbiguous
			}
			seen[result.Key] = true
		}
		pending := slices.Clone(results)
		for _, result := range results {
			if result.Outcome != EmbyDeletionDeleted || result.Reason != "confirmed_directory_operation" {
				continue
			}
			for _, target := range plan.Targets {
				if target.Key != result.Key || target.Kind != "directory" {
					continue
				}
				for _, covered := range target.CoveredKeys {
					if seen[covered] {
						return ErrEmbyIdentityAmbiguous
					}
					seen[covered] = true
					if !slices.ContainsFunc(plan.Targets, func(child EmbyDeletionTarget) bool { return child.Key == covered && child.CoveredBy == target.Key }) {
						return ErrEmbyIdentityAmbiguous
					}
					pending = append(pending, EmbyDeletionResult{Key: covered, Outcome: result.Outcome, Reason: "covered_by_directory:" + target.Key})
				}
			}
		}
		for _, result := range pending {
			if !slices.Contains([]EmbyDeletionOutcome{EmbyDeletionDeleted, EmbyDeletionAlreadyAbsent, EmbyDeletionFailed, EmbyDeletionUnresolved}, result.Outcome) {
				return ErrEmbyIdentityAmbiguous
			}
			var row EmbyWebhookTarget
			if err := tx.Where("record_id = ? AND target_key = ?", record.ID, result.Key).First(&row).Error; err != nil {
				return err
			}
			if row.Outcome == EmbyDeletionDeleted || row.Outcome == EmbyDeletionAlreadyAbsent {
				continue
			}
			var execution EmbyWebhookTargetExecution
			if err := json.Unmarshal([]byte(row.TargetJSON), &execution); err != nil {
				return err
			}
			found := false
			for i := range execution.ExecutionAttempts {
				if execution.ExecutionAttempts[i].ID == attemptID && attemptID != "" {
					if execution.ExecutionAttempts[i].ClaimToken != record.ClaimToken {
						return ErrEmbyWebhookClaimLost
					}
					found = true
					execution.ExecutionAttempts[i].FinishedAt = time.Now().Unix()
				}
			}
			if attemptID != "" && !found && result.Outcome == EmbyDeletionDeleted {
				return ErrEmbyIdentityAmbiguous
			}
			changed := row
			changed.TargetJSON, changed.Outcome, changed.Reason = embyJSON(execution), result.Outcome, embyWebhookReason(result.Reason)
			if err := checkEmbyWebhookSize(changed); err != nil {
				return err
			}
			if err := tx.Model(&row).Updates(map[string]any{"target_json": changed.TargetJSON, "outcome": changed.Outcome, "reason": changed.Reason}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// CheckEmbyWebhookClaim 只读取当前领取，不在网络等待期间持有事务。
func CheckEmbyWebhookClaim(ctx context.Context, record EmbyWebhookRecord) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, err := lockEmbyWebhookClaim(tx, record)
		return err
	})
}
