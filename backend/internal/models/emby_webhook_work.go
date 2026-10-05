package models

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"slices"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

const (
	EmbyWebhookPending    = "pending"
	EmbyWebhookRunning    = "running"
	EmbyWebhookRetry      = "retry"
	EmbyWebhookUnresolved = "unresolved"
	EmbyWebhookDone       = "done"
)

// EmbyWebhookMaxStoredBytes 低于备份 JSON Lines 的 16 MiB 单行限制，覆盖 JSON 转义开销。
const EmbyWebhookMaxStoredBytes = 8 << 20

var ErrEmbyWebhookClaimLost = errors.New("Emby 通知工作领取已失效")

// EmbyWebhookCandidate 仅保存脱敏候选，不能替代独立历史身份。
type EmbyWebhookCandidate struct {
	Path     string `json:"path"`
	PickCode string `json:"pick_code"`
}

// EmbyWebhookEnvelope 是受支持通知的有限字段；不保存原始正文或用户凭据。
type EmbyWebhookEnvelope struct {
	Event             string                 `json:"event"`
	ServerID          string                 `json:"server_id"`
	ItemServerID      string                 `json:"item_server_id,omitempty"`
	ItemID            string                 `json:"item_id"`
	ItemType          string                 `json:"item_type"`
	ItemPath          string                 `json:"item_path,omitempty"`
	ParentID          string                 `json:"parent_id,omitempty"`
	SeriesID          string                 `json:"series_id,omitempty"`
	SeasonID          string                 `json:"season_id,omitempty"`
	ExtraType         string                 `json:"extra_type,omitempty"`
	IndexNumber       *int                   `json:"index_number,omitempty"`
	ParentIndexNumber *int                   `json:"parent_index_number,omitempty"`
	IsFolder          bool                   `json:"is_folder,omitempty"`
	Date              string                 `json:"date,omitempty"`
	Source            string                 `json:"source"`
	DeepItemPath      string                 `json:"deep_item_path,omitempty"`
	DeepMountPaths    string                 `json:"deep_mount_paths,omitempty"`
	Candidates        []EmbyWebhookCandidate `json:"candidates,omitempty"`
	Issues            []string               `json:"issues,omitempty"`
	Blocked           bool                   `json:"blocked,omitempty"`
}

// EmbyWebhookRecord 每次接收独立保存，接收时授权、删除输入及计划不可提升或替换。
type EmbyWebhookRecord struct {
	BaseModel
	Event            string `json:"event"`
	ServerID         string `json:"server_id" gorm:"index:idx_emby_webhook_server,priority:1"`
	ServerConfigKey  string `json:"server_config_key" gorm:"index:idx_emby_webhook_server,priority:2"`
	ItemID           string `json:"item_id"`
	ItemType         string `json:"item_type"`
	Authorized       bool   `json:"authorized"`
	PayloadJSON      string `json:"payload_json" gorm:"type:text"`
	InputJSON        string `json:"input_json" gorm:"type:text"`
	PlanJSON         string `json:"plan_json" gorm:"type:text"`
	ObservationJSON  string `json:"observation_json" gorm:"type:text"`
	Status           string `json:"status" gorm:"index:idx_emby_webhook_queue,priority:1"`
	ClaimToken       string `json:"claim_token"`
	Reason           string `json:"reason" gorm:"type:text"`
	Attempts         int    `json:"attempts"`
	NextAttemptAt    int64  `json:"next_attempt_at" gorm:"index:idx_emby_webhook_queue,priority:2"`
	DeletionRevision int64  `json:"deletion_revision"`
	ObservedAt       int64  `json:"observed_at"`
}

// EmbyWebhookTarget 先于任何远端执行写入；相同物理代际可复用已确认成功。
type EmbyWebhookTarget struct {
	BaseModel
	RecordID        uint                `json:"record_id" gorm:"uniqueIndex:idx_emby_webhook_target,priority:1"`
	TargetKey       string              `json:"target_key" gorm:"uniqueIndex:idx_emby_webhook_target,priority:2;index:idx_emby_webhook_success,priority:3"`
	ServerID        string              `json:"server_id" gorm:"index:idx_emby_webhook_success,priority:1"`
	ServerConfigKey string              `json:"server_config_key" gorm:"index:idx_emby_webhook_success,priority:2"`
	TargetJSON      string              `json:"target_json" gorm:"type:text"`
	Outcome         EmbyDeletionOutcome `json:"outcome"`
	Reason          string              `json:"reason" gorm:"type:text"`
	Attempts        int                 `json:"attempts"`
}

// SaveEmbyWebhook 在短事务内冻结授权、可采用的早期观察和删除屏障，失败不确认接收。
func SaveEmbyWebhook(ctx context.Context, envelope EmbyWebhookEnvelope) (record EmbyWebhookRecord, err error) {
	if !slices.Contains([]string{"library.new", "library.modified", "library.deleted", "deep.delete"}, envelope.Event) {
		return record, ErrEmbyDeleteUnsupported
	}
	if _, err := parseEmbyItemID(envelope.ItemID); err != nil {
		return record, err
	}
	envelope = sanitizeEmbyWebhookEnvelope(envelope)
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := lockEmbyIndexState(tx)
		if err != nil {
			return err
		}
		var config EmbyConfig
		if err := tx.First(&config).Error; err != nil {
			return err
		}
		record = EmbyWebhookRecord{Event: envelope.Event, ServerID: envelope.ServerID, ServerConfigKey: EmbyServerConfigIdentity(&config), ItemID: envelope.ItemID, ItemType: envelope.ItemType, Authorized: config.SyncEnabled == 1 && config.EnableDeleteNetdisk == 1, PayloadJSON: embyJSON(envelope), Status: EmbyWebhookPending}
		if envelope.Event == "library.deleted" || envelope.Event == "deep.delete" {
			input := EmbyDeletionInput{ServerID: record.ServerID, ServerConfigKey: record.ServerConfigKey, ItemID: record.ItemID, ItemType: record.ItemType, Authorized: record.Authorized}
			if envelope.Blocked || state.ServerID != record.ServerID || state.ServerConfigKey != record.ServerConfigKey || record.ServerID == "" {
				input.Issues = []string{"receipt_identity_unverified"}
			} else if !slices.Contains([]string{"Movie", "Episode", "Video", "Season", "Series"}, record.ItemType) {
				input.Issues = []string{"unsupported_item_type"}
			} else {
				if err := adoptEmbyObservedEvidenceTx(tx, state, record); err != nil {
					return err
				}
				input, err = CaptureEmbyDeletionTx(tx, record.ServerID, record.ItemID, record.ItemType)
				if err != nil {
					return err
				}
				// 明确的路径冲突不能因 item ID 相同而覆盖新身份；无路径仍需后续完整核验。
				for _, owner := range input.Owners {
					if owner.Item.ItemId == record.ItemID && envelope.ItemPath != "" && owner.Item.Path != envelope.ItemPath {
						input.Owners = nil
						input.Sidecars = nil
						input.Issues = append(input.Issues, "receipt_path_conflict")
						record.Status, record.Reason = EmbyWebhookUnresolved, "receipt_path_conflict"
						break
					}
				}
				input.Issues = append(input.Issues, envelope.Issues...)
				if !matchEmbyWebhookCandidates(&input, envelope.Candidates) {
					record.Status, record.Reason = EmbyWebhookUnresolved, "candidate_identity_conflict"
				}
				ids := []string{record.ItemID}
				for _, owner := range input.Owners {
					ids = append(ids, owner.Item.ItemId)
				}
				slices.Sort(ids)
				if record.Status != EmbyWebhookUnresolved {
					record.DeletionRevision, err = RegisterEmbyDeletionTx(tx, record.ServerID, slices.Compact(ids))
					if err != nil {
						return err
					}
				}
			}
			record.InputJSON = embyJSON(input)
		} else if envelope.Blocked {
			record.Status, record.Reason = EmbyWebhookUnresolved, "receipt_identity_unverified"
		}
		if err := checkEmbyWebhookSize(record); err != nil {
			return err
		}
		return tx.Create(&record).Error
	})
	return record, err
}

func sanitizeEmbyWebhookEnvelope(envelope EmbyWebhookEnvelope) EmbyWebhookEnvelope {
	envelope.ItemPath = sanitizeEmbyEvidencePath(envelope.ItemPath)
	envelope.DeepItemPath = redactEmbySnapshotError(envelope.DeepItemPath)
	envelope.DeepMountPaths = redactEmbySnapshotError(envelope.DeepMountPaths)
	envelope.Candidates = slices.Clone(envelope.Candidates)
	for i := range envelope.Candidates {
		envelope.Candidates[i].Path = sanitizeEmbyEvidencePath(envelope.Candidates[i].Path)
		envelope.Candidates[i].PickCode = sanitizeEmbyEvidencePath(envelope.Candidates[i].PickCode)
	}
	envelope.Issues = slices.Clone(envelope.Issues)
	for i := range envelope.Issues {
		envelope.Issues[i] = redactEmbySnapshotError(envelope.Issues[i])
	}
	return envelope
}

// ClaimEmbyWebhook 优先尚未观察的新事件，领取与同步提交共享 SQL 锁；不永久去重 item。
func ClaimEmbyWebhook(ctx context.Context, now int64) (record *EmbyWebhookRecord, err error) {
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockEmbyIndexState(tx); err != nil {
			return err
		}
		var next EmbyWebhookRecord
		err := tx.Where("status IN ? AND next_attempt_at <= ?", []string{EmbyWebhookPending, EmbyWebhookRetry}, now).
			Order("CASE WHEN event IN ('library.new','library.modified') AND observed_at = 0 THEN 0 ELSE 1 END").Order("id").First(&next).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var token [16]byte
		if _, err := rand.Read(token[:]); err != nil {
			return err
		}
		next.ClaimToken, next.Status = hex.EncodeToString(token[:]), EmbyWebhookRunning
		if err := tx.Model(&next).Updates(map[string]any{"claim_token": next.ClaimToken, "status": next.Status}).Error; err != nil {
			return err
		}
		record = &next
		return nil
	})
	return record, err
}

// RecoverEmbyWebhookWork 只在 worker 启动且无其他执行者时调用，保留计划、结果与重试次数。
func RecoverEmbyWebhookWork(ctx context.Context) error {
	return db.Db.WithContext(ctx).Model(&EmbyWebhookRecord{}).Where("status = ?", EmbyWebhookRunning).
		Updates(map[string]any{"status": EmbyWebhookRetry, "claim_token": ""}).Error
}

func lockEmbyWebhookClaim(tx *gorm.DB, record EmbyWebhookRecord) (*EmbyWebhookRecord, error) {
	if _, err := lockEmbyIndexState(tx); err != nil {
		return nil, err
	}
	var current EmbyWebhookRecord
	if err := tx.Where("id = ? AND claim_token = ? AND status = ?", record.ID, record.ClaimToken, EmbyWebhookRunning).First(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrEmbyWebhookClaimLost
		}
		return nil, err
	}
	if record.ClaimToken == "" {
		return nil, ErrEmbyWebhookClaimLost
	}
	return &current, nil
}

// MarkEmbyWebhookObserved 标记本次早期 GET 已尝试，无证据时不制造观察快照。
func MarkEmbyWebhookObserved(ctx context.Context, record EmbyWebhookRecord) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		return tx.Model(current).Update("observed_at", time.Now().Unix()).Error
	})
}

// SaveEmbyWebhookPlan 先保存不可变计划与全部目标，重复保存只能接受相同计划。
func SaveEmbyWebhookPlan(ctx context.Context, record EmbyWebhookRecord, plan EmbyDeletionPlan) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		if current.InputJSON == "" || embyJSON(plan.Input) != current.InputJSON {
			return ErrEmbyIdentityAmbiguous
		}
		encoded := embyJSON(plan)
		sized := *current
		sized.PlanJSON = encoded
		if err := checkEmbyWebhookSize(sized); err != nil {
			return err
		}
		if current.PlanJSON != "" {
			if current.PlanJSON != encoded {
				return ErrEmbyIdentityAmbiguous
			}
			return nil
		}
		seen := map[string]bool{}
		for _, target := range plan.Targets {
			if target.Key == "" || target.Key != EmbyDeletionFileKey(target.File) || seen[target.Key] {
				return ErrEmbyIdentityAmbiguous
			}
			seen[target.Key] = true
			entry := EmbyWebhookTarget{RecordID: current.ID, TargetKey: target.Key, ServerID: current.ServerID, ServerConfigKey: current.ServerConfigKey, TargetJSON: embyJSON(target)}
			if err := checkEmbyWebhookSize(entry); err != nil {
				return err
			}
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
		}
		return tx.Model(current).Update("plan_json", encoded).Error
	})
}

func LoadEmbyWebhookTargets(ctx context.Context, recordID uint) ([]EmbyWebhookTarget, error) {
	var targets []EmbyWebhookTarget
	err := db.Db.WithContext(ctx).Where("record_id = ?", recordID).Order("id").Find(&targets).Error
	return targets, err
}

// SaveEmbyWebhookResult 成功终态不能回退；取消不调用此函数，未知远端结果以 failed 留待核验。
func SaveEmbyWebhookResult(ctx context.Context, record EmbyWebhookRecord, result EmbyDeletionResult) error {
	if !slices.Contains([]EmbyDeletionOutcome{EmbyDeletionDeleted, EmbyDeletionAlreadyAbsent, EmbyDeletionUnresolved, EmbyDeletionFailed}, result.Outcome) {
		return ErrEmbyIdentityAmbiguous
	}
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		if current.PlanJSON == "" {
			return ErrEmbyIdentityAmbiguous
		}
		var target EmbyWebhookTarget
		if err := tx.Where("record_id = ? AND target_key = ?", current.ID, result.Key).First(&target).Error; err != nil {
			return err
		}
		if target.Outcome == EmbyDeletionDeleted || target.Outcome == EmbyDeletionAlreadyAbsent {
			return nil
		}
		updates := map[string]any{"outcome": result.Outcome, "reason": embyWebhookReason(result.Reason)}
		if result.Outcome == EmbyDeletionFailed {
			updates["attempts"] = gorm.Expr("attempts + 1")
		}
		return tx.Model(&target).Updates(updates).Error
	})
}

// FindEmbyWebhookSuccess 仅复用同服务连接、同物理代际的成功，不按外层 item ID 去重。
func FindEmbyWebhookSuccess(ctx context.Context, record EmbyWebhookRecord, target EmbyDeletionTarget) (*EmbyDeletionResult, error) {
	if !record.Authorized || target.Key != EmbyDeletionFileKey(target.File) {
		return nil, nil
	}
	var prior EmbyWebhookTarget
	err := db.Db.WithContext(ctx).Where("server_id = ? AND server_config_key = ? AND target_key = ? AND outcome IN ?", record.ServerID, record.ServerConfigKey, target.Key, []EmbyDeletionOutcome{EmbyDeletionDeleted, EmbyDeletionAlreadyAbsent}).Order("id").First(&prior).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &EmbyDeletionResult{Key: target.Key, Outcome: prior.Outcome, Reason: "reused_confirmed_physical_generation"}, nil
}

// FinishEmbyWebhook 释放领取；忙时和取消不得消耗网络失败次数。
func FinishEmbyWebhook(ctx context.Context, record EmbyWebhookRecord, status, reason string, nextAt int64, consumeAttempt bool) error {
	if !slices.Contains([]string{EmbyWebhookRetry, EmbyWebhookUnresolved, EmbyWebhookDone}, status) {
		return ErrEmbyIdentityAmbiguous
	}
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		updates := map[string]any{"status": status, "reason": embyWebhookReason(reason), "next_attempt_at": nextAt, "claim_token": ""}
		if consumeAttempt {
			updates["attempts"] = gorm.Expr("attempts + 1")
		}
		return tx.Model(current).Updates(updates).Error
	})
}

// AdmitEmbyWebhookSurvivors 只能释放本次屏障和冻结代际，不能释放更晚的删除。
func AdmitEmbyWebhookSurvivors(ctx context.Context, record EmbyWebhookRecord, itemIDs []string) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		var input EmbyDeletionInput
		if err := json.Unmarshal([]byte(current.InputJSON), &input); err != nil {
			return err
		}
		for _, id := range itemIDs {
			var state EmbyItemState
			if err := tx.Where("server_id = ? AND item_id = ?", current.ServerID, id).First(&state).Error; err != nil {
				return err
			}
			if !state.Deleted {
				continue
			}
			if state.Revision != current.DeletionRevision {
				return ErrEmbySnapshotStale
			}
			for _, owner := range input.Owners {
				if owner.Item.ItemId == id && (state.SnapshotID != owner.Evidence.ID || state.Generation != owner.Evidence.Generation) {
					return ErrEmbySnapshotStale
				}
			}
			if id != current.ItemID && !slices.ContainsFunc(input.Owners, func(owner EmbyDeletionOwner) bool { return owner.Item.ItemId == id }) {
				return ErrEmbyIdentityAmbiguous
			}
			if err := AdmitEmbyItemGenerationTx(tx, current.ServerID, id, current.DeletionRevision); err != nil {
				return err
			}
		}
		return nil
	})
}

// FinalizeEmbyWebhookLocal 仅清显式列出的冻结旧条目；调用方必须独立核验这些原 ID 已消失。
// 关闭联动删除或已确认新 ID/共享源保护时可用，不因云端失败、路径消失或任意查询错误调用。
func FinalizeEmbyWebhookLocal(ctx context.Context, record EmbyWebhookRecord, itemIDs []string) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		var input EmbyDeletionInput
		if err := json.Unmarshal([]byte(current.InputJSON), &input); err != nil {
			return err
		}
		var config EmbyConfig
		if err := tx.First(&config).Error; err != nil {
			return err
		}
		if EmbyServerConfigIdentity(&config) != current.ServerConfigKey {
			return ErrEmbySnapshotStale
		}
		var index EmbyIndexState
		if err := tx.First(&index, 1).Error; err != nil {
			return err
		}
		if index.ServerID != current.ServerID || index.ServerConfigKey != current.ServerConfigKey {
			return ErrEmbySnapshotStale
		}
		for _, id := range itemIDs {
			if !slices.ContainsFunc(input.Owners, func(owner EmbyDeletionOwner) bool { return owner.Item.ItemId == id }) {
				return ErrEmbyIdentityAmbiguous
			}
		}
		for _, owner := range input.Owners {
			if !slices.Contains(itemIDs, owner.Item.ItemId) {
				continue
			}
			var state EmbyItemState
			if err := tx.Where("server_id = ? AND item_id = ?", current.ServerID, owner.Item.ItemId).First(&state).Error; err != nil {
				return err
			}
			if state.SnapshotID != owner.Evidence.ID || state.Generation != owner.Evidence.Generation || state.Revision != current.DeletionRevision || !state.Deleted {
				continue
			}
			var evidence EmbyItemEvidence
			if err := tx.First(&evidence, owner.Evidence.ID).Error; err != nil {
				return err
			}
			if evidence != owner.Evidence || evidence.ServerID != current.ServerID || evidence.ServerConfigKey != current.ServerConfigKey {
				return ErrEmbyIdentityAmbiguous
			}
			for _, link := range owner.Links {
				if err := tx.Where("id = ? AND emby_item_id = ? AND snapshot_id = ? AND sync_file_id = ?", link.ID, owner.Item.ItemIdInt, owner.Evidence.ID, link.SyncFileId).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
					return err
				}
			}
			if err := tx.Where("server_id = ? AND item_id = ? AND snapshot_id = ? AND generation = ?", current.ServerID, owner.Item.ItemId, owner.Evidence.ID, owner.Evidence.Generation).Delete(&EmbyMediaItem{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func checkEmbyWebhookSize(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(encoded) > EmbyWebhookMaxStoredBytes {
		return errors.New("Emby 通知证据超过持久记录大小限制")
	}
	return nil
}

// embyWebhookReason 限制后续远端错误的大小，避免诊断更新突破备份单行上限。
func embyWebhookReason(reason string) string {
	reason = redactEmbySnapshotError(reason)
	const maxBytes = 4096
	if len(reason) <= maxBytes {
		return reason
	}
	end := maxBytes - len("…")
	for !utf8.RuneStart(reason[end]) {
		end--
	}
	return reason[:end] + "…"
}

// matchEmbyWebhookCandidates 只核对已确认历史范围，候选本身不能新增删除目标。
func matchEmbyWebhookCandidates(input *EmbyDeletionInput, candidates []EmbyWebhookCandidate) bool {
	consistent := true
	for _, candidate := range candidates {
		matched := false
		for _, owner := range input.Owners {
			var sources []EmbySnapshotSource
			if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &sources); err != nil {
				input.Issues = append(input.Issues, "candidate_history_invalid")
				consistent = false
				continue
			}
			for _, file := range owner.Files {
				pathMatch := candidate.Path != "" && (candidate.Path == file.LocalFilePath || candidate.Path == path.Join(file.Path, file.FileName))
				for _, source := range sources {
					if source.ID == file.SourceID && candidate.Path != "" && candidate.Path == source.Path {
						pathMatch = true
					}
				}
				codeMatch := candidate.PickCode != "" && candidate.PickCode == file.PickCode
				if pathMatch && candidate.PickCode != "" && file.PickCode != "" && !codeMatch {
					input.Issues = append(input.Issues, "candidate_identity_conflict")
					consistent = false
				}
				if codeMatch || pathMatch {
					matched = true
					key := EmbyDeletionFileKey(file)
					if !slices.Contains(input.CandidateKeys, key) {
						input.CandidateKeys = append(input.CandidateKeys, key)
					}
				}
			}
		}
		if !matched {
			input.Issues = append(input.Issues, "candidate_unresolved")
		}
	}
	return consistent
}
