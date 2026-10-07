package models

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// EmbyItemMembership 定位 state 当前引用的历史成员；当前媒体索引清理不删除此投影。
// 它只缩小候选范围，删除授权仍由对应 state 和不可变证据决定。
type EmbyItemMembership struct {
	BaseModel
	ServerID        string `json:"server_id" gorm:"uniqueIndex:idx_emby_membership_item,priority:1;index:idx_emby_membership_part,priority:1;index:idx_emby_membership_season,priority:1;index:idx_emby_membership_series,priority:1"`
	ItemID          string `json:"item_id" gorm:"uniqueIndex:idx_emby_membership_item,priority:2"`
	ServerConfigKey string `json:"server_config_key" gorm:"index:idx_emby_membership_part,priority:2;index:idx_emby_membership_season,priority:2;index:idx_emby_membership_series,priority:2"`
	SnapshotID      uint   `json:"snapshot_id"`
	ItemType        string `json:"item_type"`
	PartOfItemID    string `json:"part_of_item_id" gorm:"index:idx_emby_membership_part,priority:3"`
	SeasonID        string `json:"season_id" gorm:"index:idx_emby_membership_season,priority:3"`
	SeriesID        string `json:"series_id" gorm:"index:idx_emby_membership_series,priority:3"`
}

func makeEmbyItemMembership(state EmbyItemState, evidence EmbyItemEvidence) (EmbyItemMembership, error) {
	if state.SnapshotID != evidence.ID || state.ItemID != evidence.ItemID || state.ServerID != evidence.ServerID ||
		state.Generation != evidence.Generation || state.IdentityKey != evidence.IdentityKey {
		return EmbyItemMembership{}, ErrEmbyIdentityAmbiguous
	}
	item, err := embyObservedEvidenceItem(evidence, state.ServerID, evidence.ServerConfigKey)
	if err != nil {
		return EmbyItemMembership{}, err
	}
	return EmbyItemMembership{
		ServerID: state.ServerID, ItemID: state.ItemID, ServerConfigKey: evidence.ServerConfigKey,
		SnapshotID: evidence.ID, ItemType: item.Type,
		PartOfItemID: item.PartOfItemID, SeasonID: item.SeasonId, SeriesID: item.SeriesId,
	}, nil
}

func saveEmbyItemMembershipTx(tx *gorm.DB, state EmbyItemState, evidence EmbyItemEvidence) error {
	row, err := makeEmbyItemMembership(state, evidence)
	if err != nil {
		return err
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "server_id"}, {Name: "item_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"server_config_key", "snapshot_id", "item_type", "part_of_item_id", "season_id", "series_id", "updated_at",
		}),
	}).Create(&row).Error
}

// RebuildEmbyItemMembership 在维护事务中从 state 指向的原证据重建成员索引。
// 恢复备份在同一事务中导入原表并重建索引，不采用备份中的派生行；失败整体回滚。
func RebuildEmbyItemMembership(conn *gorm.DB) error {
	return conn.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&EmbyIndexState{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + 0")).Error; err != nil {
			return err
		}
		if err := tx.AutoMigrate(&EmbyItemMembership{}); err != nil {
			return err
		}
		if err := tx.Where("1 = 1").Delete(&EmbyItemMembership{}).Error; err != nil {
			return err
		}
		var afterID uint
		for {
			var states []EmbyItemState
			if err := tx.Where("id > ? AND snapshot_id <> 0", afterID).Order("id").Limit(embyObservationBatchSize).Find(&states).Error; err != nil {
				return err
			}
			if len(states) == 0 {
				return nil
			}
			ids := make([]uint, 0, len(states))
			for _, state := range states {
				ids = append(ids, state.SnapshotID)
			}
			evidence, err := loadEmbyObservedEvidenceTx(tx, ids)
			if err != nil {
				return err
			}
			rows := make([]EmbyItemMembership, 0, len(states))
			for _, state := range states {
				entry, ok := evidence[state.SnapshotID]
				if !ok {
					return fmt.Errorf("Emby 条目 %s 缺少证据 %d: %w", state.ItemID, state.SnapshotID, gorm.ErrRecordNotFound)
				}
				row, err := makeEmbyItemMembership(state, entry)
				if err != nil {
					return fmt.Errorf("重建 Emby 成员 %s: %w", state.ItemID, err)
				}
				rows = append(rows, row)
			}
			if err := tx.CreateInBatches(&rows, 50).Error; err != nil {
				return err
			}
			afterID = states[len(states)-1].ID
		}
	})
}

func embyItemMembershipQuery(tx *gorm.DB, serverID, serverConfigKey string) *gorm.DB {
	return tx.Model(&EmbyItemMembership{}).Table("emby_item_memberships AS m").
		Joins("JOIN emby_item_states AS s ON s.server_id = m.server_id AND s.item_id = m.item_id AND s.snapshot_id = m.snapshot_id").
		Where("m.server_id = ? AND m.server_config_key = ?", serverID, serverConfigKey)
}

// embyStateEvidenceCandidateIDsTx 供早期观察与冻结共用；extraParents 保留历史观察父项的分段候选。
func embyStateEvidenceCandidateIDsTx(tx *gorm.DB, serverID, serverConfigKey, itemID, itemType string, extraParents []string) ([]string, error) {
	candidates := map[string]bool{itemID: true}
	for _, id := range extraParents {
		candidates[id] = true
	}
	var scopeColumn string
	switch itemType {
	case "Movie":
		// Movie 的分段在下面统一查询；独立版本不属于删除成员。
	case "Season":
		scopeColumn = "m.season_id"
	case "Series":
		scopeColumn = "m.series_id"
	default:
		return []string{itemID}, nil
	}
	if scopeColumn != "" {
		var ids []string
		if err := embyItemMembershipQuery(tx, serverID, serverConfigKey).
			Where(scopeColumn+" = ? AND m.item_type IN ?", itemID, []string{"Episode", "Video"}).Pluck("m.item_id", &ids).Error; err != nil {
			return nil, err
		}
		for _, id := range ids {
			candidates[id] = true
		}
	}
	// 分段可能只有 PartOf，没有重复 Season/Series 字段；仅扩展真实 Video 边。
	for ids := range slices.Chunk(slices.Sorted(maps.Keys(candidates)), embyObservationBatchSize) {
		var parts []string
		if err := embyItemMembershipQuery(tx, serverID, serverConfigKey).
			Where("m.part_of_item_id IN ? AND m.item_type = ?", ids, "Video").Pluck("m.item_id", &parts).Error; err != nil {
			return nil, err
		}
		for _, id := range parts {
			candidates[id] = true
		}
	}
	return slices.Sorted(maps.Keys(candidates)), nil
}

func loadEmbyScopeEvidenceTx(tx *gorm.DB, serverID, serverConfigKey, itemID, itemType string, excluded map[string]bool) ([]EmbyItemEvidence, error) {
	ids, err := embyStateEvidenceCandidateIDsTx(tx, serverID, serverConfigKey, itemID, itemType, nil)
	if err != nil {
		return nil, err
	}
	ids = slices.DeleteFunc(ids, func(id string) bool { return excluded[id] })
	var evidence []EmbyItemEvidence
	for batch := range slices.Chunk(ids, embyObservationBatchSize) {
		var rows []EmbyItemEvidence
		if err := tx.Table("emby_item_evidences AS e").Select("e.*").
			Joins("JOIN emby_item_states AS s ON s.snapshot_id = e.id AND s.server_id = e.server_id AND s.item_id = e.item_id").
			Where("s.server_id = ? AND s.item_id IN ? AND e.server_config_key = ?", serverID, batch, serverConfigKey).
			Order("e.id").Find(&rows).Error; err != nil {
			return nil, err
		}
		evidence = append(evidence, rows...)
	}
	slices.SortFunc(evidence, func(a, b EmbyItemEvidence) int { return cmp.Compare(a.ID, b.ID) })
	return evidence, nil
}
