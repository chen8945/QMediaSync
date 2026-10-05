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

// embyObservedRef 保存 GET 前后的乐观版本，晚到的读不能成为删除授权。
type embyObservedRef struct {
	EvidenceID    uint   `json:"evidence_id"`
	StateID       uint   `json:"expected_state_id"`
	StateRevision int64  `json:"expected_state_revision"`
	SnapshotID    uint   `json:"expected_snapshot_id"`
	Generation    int64  `json:"expected_generation"`
	IdentityKey   string `json:"expected_identity_key"`
	Revision      int64  `json:"revision"`
}

// LoadEmbyDeletionBarriers 在读取前 token 下分页取得待核验状态，不把索引消失当作删除授权。
func LoadEmbyDeletionBarriers(ctx context.Context, token EmbyIndexToken, afterID uint, limit int) (states []EmbyItemState, err error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrEmbyIdentityAmbiguous
	}
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkEmbyToken(tx, token); err != nil {
			return err
		}
		return tx.Where("server_id = ? AND deleted = ? AND id > ?", token.ServerID, true, afterID).Order("id").Limit(limit).Find(&states).Error
	})
	return states, err
}

// LoadEmbyDeletionBarrierReadIDs 为隐藏分段补充可信历史父项的只读入口，绝不据此准入成员。
// 只有后续实际 GET/AdditionalParts 返回且完整核验的物理 ID 才能传给准入函数。
func LoadEmbyDeletionBarrierReadIDs(ctx context.Context, token EmbyIndexToken, barriers []EmbyItemState) (ids []string, err error) {
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkEmbyToken(tx, token); err != nil {
			return err
		}
		appendID := func(id string) {
			if _, err := parseEmbyItemID(id); err == nil && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		for _, barrier := range barriers {
			var current EmbyItemState
			if err := tx.First(&current, barrier.ID).Error; err != nil {
				return err
			}
			if current != barrier || !current.Deleted || current.ServerID != token.ServerID || current.SnapshotID > token.EvidenceHighWatermark {
				return ErrEmbySnapshotStale
			}
			appendID(current.ItemID)
			if current.SnapshotID == 0 {
				continue
			}
			var evidence EmbyItemEvidence
			if err := tx.First(&evidence, current.SnapshotID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			if evidence.ServerID != token.ServerID || evidence.ServerConfigKey != token.ServerConfigKey || evidence.ItemID != current.ItemID || evidence.Generation != current.Generation || evidence.IdentityKey != current.IdentityKey {
				continue
			}
			var item EmbyMediaItem
			if err := json.Unmarshal([]byte(evidence.ItemJSON), &item); err != nil {
				return err
			}
			if item.ItemId != current.ItemID || item.ServerId != token.ServerID || item.Generation != current.Generation {
				continue
			}
			appendID(item.PartOfItemID)
			appendID(item.VersionOfItemID)
		}
		return nil
	})
	if err != nil {
		ids = nil
	}
	return ids, err
}

// AdmitEmbyObservedSurvivors 仅供完整真实 GET 已证实存活的 new/modified 主项及物理成员使用。
// 返回 true 后必须丢弃本次响应并重取 token/GET，不能提交解除屏障前读取的数据。
func AdmitEmbyObservedSurvivors(ctx context.Context, record EmbyWebhookRecord, token EmbyIndexToken, itemIDs []string) (admitted bool, err error) {
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		if current.Event != "library.new" && current.Event != "library.modified" || !slices.Contains(itemIDs, current.ItemID) {
			return ErrEmbyIdentityAmbiguous
		}
		if current.ServerID != token.ServerID || current.ServerConfigKey != token.ServerConfigKey {
			return ErrEmbySnapshotStale
		}
		admitted, err = admitEmbyVerifiedSurvivorsTx(tx, token, itemIDs)
		return err
	})
	if err != nil {
		admitted = false
	}
	return admitted, err
}

// AdmitEmbyVerifiedSurvivors 供普通同步恢复完整实际读取到的存活物理项，不能传推测的成员 ID。
// 一次原子释放整组屏障；返回 true 使旧 token 失效，必须从新读取重新开始同步。
func AdmitEmbyVerifiedSurvivors(ctx context.Context, token EmbyIndexToken, itemIDs []string) (admitted bool, err error) {
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		admitted, err = admitEmbyVerifiedSurvivorsTx(tx, token, itemIDs)
		return err
	})
	if err != nil {
		admitted = false
	}
	return admitted, err
}

func admitEmbyVerifiedSurvivorsTx(tx *gorm.DB, token EmbyIndexToken, itemIDs []string) (bool, error) {
	if err := checkEmbyToken(tx, token); err != nil {
		return false, err
	}
	var states []EmbyItemState
	seen := map[string]bool{}
	for _, itemID := range itemIDs {
		if _, err := parseEmbyItemID(itemID); err != nil {
			return false, err
		}
		if seen[itemID] {
			continue
		}
		seen[itemID] = true
		var state EmbyItemState
		if err := tx.Where("server_id = ? AND item_id = ?", token.ServerID, itemID).First(&state).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return false, err
		}
		if !state.Deleted {
			continue
		}
		if state.SnapshotID > token.EvidenceHighWatermark || state.Revision > token.Revision {
			return false, ErrEmbySnapshotStale
		}
		states = append(states, state)
	}
	if len(states) == 0 {
		return false, nil
	}
	for _, state := range states {
		result := tx.Model(&EmbyItemState{}).Where("id = ? AND server_id = ? AND item_id = ? AND revision = ? AND snapshot_id = ? AND deleted = ?", state.ID, token.ServerID, state.ItemID, state.Revision, state.SnapshotID, true).Update("deleted", false)
		if result.Error != nil {
			return false, result.Error
		}
		if result.RowsAffected != 1 {
			return false, ErrEmbySnapshotStale
		}
	}
	result := tx.Model(&EmbyIndexState{}).Where("id = 1 AND revision = ?", token.Revision).UpdateColumn("revision", gorm.Expr("revision + 1"))
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, ErrEmbySnapshotStale
	}
	return true, nil
}

// SaveEmbyObservedEvidence 不更改当前索引、关联或同步水位；删除收件才条件采用。
func SaveEmbyObservedEvidence(ctx context.Context, record EmbyWebhookRecord, token EmbyIndexToken, snapshots []EmbyItemSnapshot) error {
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := lockEmbyWebhookClaim(tx, record)
		if err != nil {
			return err
		}
		if current.Event != "library.new" && current.Event != "library.modified" {
			return ErrEmbyIdentityAmbiguous
		}
		if current.ServerID != token.ServerID || current.ServerConfigKey != token.ServerConfigKey {
			return ErrEmbySnapshotStale
		}
		if err := checkEmbyToken(tx, token); err != nil {
			return err
		}
		var refs []embyObservedRef
		for i := range snapshots {
			snapshot := snapshots[i]
			id, err := parseEmbyItemID(snapshot.Item.ItemId)
			if err != nil {
				return err
			}
			snapshot.Item.ItemIdInt, snapshot.Item.ServerId = id, token.ServerID
			var state EmbyItemState
			err = tx.Where("server_id = ? AND item_id = ?", token.ServerID, snapshot.Item.ItemId).First(&state).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			if state.Deleted {
				return ErrEmbyItemDeleted
			}
			if state.SnapshotID > token.EvidenceHighWatermark {
				return ErrEmbySnapshotStale
			}
			ref := embyObservedRef{StateID: state.ID, StateRevision: state.Revision, SnapshotID: state.SnapshotID, Generation: state.Generation, IdentityKey: state.IdentityKey, Revision: token.Revision}
			evidence, _, err := buildEmbySnapshotEvidence(tx, token, &snapshot, &state)
			if err != nil {
				return err
			}
			if err := tx.Create(&evidence).Error; err != nil {
				return err
			}
			ref.EvidenceID = evidence.ID
			refs = append(refs, ref)
		}
		current.ObservationJSON = embyJSON(refs)
		if err := checkEmbyWebhookSize(current); err != nil {
			return err
		}
		return tx.Model(current).Updates(map[string]any{"observation_json": current.ObservationJSON, "observed_at": time.Now().Unix()}).Error
	})
}

func adoptEmbyObservedEvidenceTx(tx *gorm.DB, index *EmbyIndexState, record EmbyWebhookRecord) error {
	var records []EmbyWebhookRecord
	if err := tx.Where("server_id = ? AND server_config_key = ? AND observed_at > 0 AND observation_json <> ''", record.ServerID, record.ServerConfigKey).Order("id DESC").Find(&records).Error; err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, observed := range records {
		var refs []embyObservedRef
		if err := json.Unmarshal([]byte(observed.ObservationJSON), &refs); err != nil {
			return err
		}
		for _, ref := range refs {
			var evidence EmbyItemEvidence
			if err := tx.First(&evidence, ref.EvidenceID).Error; err != nil {
				return err
			}
			if evidence.ServerID != record.ServerID || evidence.ServerConfigKey != record.ServerConfigKey {
				return ErrEmbyIdentityAmbiguous
			}
			if seen[evidence.ItemID] {
				continue
			}
			seen[evidence.ItemID] = true
			var item EmbyMediaItem
			if err := json.Unmarshal([]byte(evidence.ItemJSON), &item); err != nil {
				return err
			}
			// 只采用与本次主项、季剧或电影分段明确有关的历史观察。
			relevant := item.ItemId == record.ItemID
			if record.ItemType == "Movie" {
				relevant = relevant || item.PartOfItemID == record.ItemID
			}
			if record.ItemType == "Season" {
				relevant = relevant || item.SeasonId == record.ItemID
			}
			if record.ItemType == "Series" {
				relevant = relevant || item.SeriesId == record.ItemID
			}
			if !relevant {
				continue
			}
			var state EmbyItemState
			err := tx.Where("server_id = ? AND item_id = ?", record.ServerID, evidence.ItemID).First(&state).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			// 保存时已经检查全局读取 token；采用时只拒绝此条目发生过的变化。
			// 无关条目的删除不能丢掉本条目观察，而删除后再释放屏障仍会改变 Revision。
			if state.Deleted || state.ID != ref.StateID || state.Revision != ref.StateRevision || state.SnapshotID != ref.SnapshotID || state.Generation != ref.Generation || state.IdentityKey != ref.IdentityKey {
				continue
			}
			state.ServerID, state.ItemID = record.ServerID, evidence.ItemID
			state.IdentityKey, state.Generation, state.SnapshotID, state.Revision = evidence.IdentityKey, evidence.Generation, evidence.ID, index.Revision
			if err := tx.Save(&state).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

// freezeEmbySnapshotSidecars 在读取 Emby 物理身份时冻结已有旁车，不在删除收件时补造历史。
func freezeEmbySnapshotSidecars(tx *gorm.DB, files []EmbyFrozenFile) ([]EmbyFrozenFile, error) {
	sidecars := []EmbyFrozenFile{}
	seen := map[uint]bool{}
	for _, video := range files {
		var candidates []SyncFile
		if err := tx.Where("source_type = ? AND account_id = ? AND path = ? AND is_meta = ? AND is_video = ?", video.SourceType, video.AccountID, video.Path, true, false).Order("id").Find(&candidates).Error; err != nil {
			return nil, err
		}
		for _, candidate := range candidates {
			if seen[candidate.ID] || !embySidecarMatches(candidate.FileName, video.FileName) {
				continue
			}
			seen[candidate.ID] = true
			frozen, err := freezeEmbyFile(tx, candidate, "", candidate.LocalFilePath)
			if err != nil {
				return nil, err
			}
			sidecars = append(sidecars, frozen)
		}
	}
	return sidecars, nil
}
