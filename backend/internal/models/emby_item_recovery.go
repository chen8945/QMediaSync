package models

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

// LoadEmbyItemRecoveryReadIDs 只为指定隐藏条目查找历史父项读取入口。
// 返回值不证明存活；调用方必须在实际父项快照中找到原条目后才能准入整组。
func LoadEmbyItemRecoveryReadIDs(ctx context.Context, token EmbyIndexToken, itemID string) (ids []string, err error) {
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := checkEmbyToken(tx, token); err != nil {
			return err
		}
		var state EmbyItemState
		if err := tx.Where("server_id = ? AND item_id = ?", token.ServerID, itemID).First(&state).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if state.SnapshotID > token.EvidenceHighWatermark || state.Revision > token.Revision {
			return ErrEmbySnapshotStale
		}
		if state.SnapshotID == 0 {
			return nil
		}
		var evidence EmbyItemEvidence
		if err := tx.First(&evidence, state.SnapshotID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if evidence.ServerID != token.ServerID || evidence.ServerConfigKey != token.ServerConfigKey || evidence.ItemID != state.ItemID || evidence.Generation != state.Generation || evidence.IdentityKey != state.IdentityKey {
			return nil
		}
		var item EmbyMediaItem
		if err := json.Unmarshal([]byte(evidence.ItemJSON), &item); err != nil {
			return err
		}
		if item.ItemId != state.ItemID || item.ServerId != token.ServerID || item.Generation != state.Generation {
			return nil
		}
		for _, id := range []string{item.PartOfItemID, item.VersionOfItemID} {
			if _, err := parseEmbyItemID(id); err == nil && id != itemID && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ids, nil
}
