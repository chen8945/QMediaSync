package models

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"gorm.io/gorm"
)

const embyObservationBatchSize = 500

// EmbyObservedEvidenceIndex 只定位历史观察候选；授权仍核验原收件引用、不可变证据和当前状态。
// RefIndex 保留每个原始引用的位置，同一条目的多份预期状态不能提前合并。
type EmbyObservedEvidenceIndex struct {
	BaseModel
	RecordID        uint   `json:"record_id" gorm:"uniqueIndex:idx_emby_observed_ref,priority:1"`
	RefIndex        int    `json:"ref_index" gorm:"uniqueIndex:idx_emby_observed_ref,priority:2"`
	EvidenceID      uint   `json:"evidence_id"`
	ServerID        string `json:"server_id" gorm:"index:idx_emby_observed_item,priority:1;index:idx_emby_observed_part,priority:1;index:idx_emby_observed_season,priority:1;index:idx_emby_observed_series,priority:1"`
	ServerConfigKey string `json:"server_config_key" gorm:"index:idx_emby_observed_item,priority:2;index:idx_emby_observed_part,priority:2;index:idx_emby_observed_season,priority:2;index:idx_emby_observed_series,priority:2"`
	ItemID          string `json:"item_id" gorm:"index:idx_emby_observed_item,priority:3"`
	PartOfItemID    string `json:"part_of_item_id" gorm:"index:idx_emby_observed_part,priority:3"`
	SeasonID        string `json:"season_id" gorm:"index:idx_emby_observed_season,priority:3"`
	SeriesID        string `json:"series_id" gorm:"index:idx_emby_observed_series,priority:3"`
}

func embyObservedEvidenceItem(evidence EmbyItemEvidence, serverID, serverConfigKey string) (EmbyMediaItem, error) {
	var item EmbyMediaItem
	if evidence.ID == 0 || evidence.ServerID != serverID || evidence.ServerConfigKey != serverConfigKey {
		return item, ErrEmbyIdentityAmbiguous
	}
	if err := json.Unmarshal([]byte(evidence.ItemJSON), &item); err != nil {
		return item, err
	}
	if item.ItemId != evidence.ItemID || item.ServerId != evidence.ServerID || item.Generation != evidence.Generation {
		return item, ErrEmbyIdentityAmbiguous
	}
	if _, err := parseEmbyItemID(item.ItemId); err != nil {
		return item, err
	}
	return item, nil
}

func makeEmbyObservedIndex(record EmbyWebhookRecord, refIndex int, evidence EmbyItemEvidence) (EmbyObservedEvidenceIndex, error) {
	item, err := embyObservedEvidenceItem(evidence, record.ServerID, record.ServerConfigKey)
	if err != nil {
		return EmbyObservedEvidenceIndex{}, err
	}
	return EmbyObservedEvidenceIndex{
		RecordID: record.ID, RefIndex: refIndex, EvidenceID: evidence.ID,
		ServerID: record.ServerID, ServerConfigKey: record.ServerConfigKey,
		ItemID: evidence.ItemID, PartOfItemID: item.PartOfItemID, SeasonID: item.SeasonId, SeriesID: item.SeriesId,
	}, nil
}

func loadEmbyObservedEvidenceTx(tx *gorm.DB, ids []uint) (map[uint]EmbyItemEvidence, error) {
	result := make(map[uint]EmbyItemEvidence, len(ids))
	for batch := range slices.Chunk(ids, embyObservationBatchSize) {
		var rows []EmbyItemEvidence
		if err := tx.Select("id", "server_id", "server_config_key", "item_id", "generation", "identity_key", "item_json").Where("id IN ?", batch).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			result[row.ID] = row
		}
	}
	return result, nil
}

// RebuildEmbyObservedEvidenceIndex 在维护事务内由原收件和证据重建派生索引，不补造删除证据。
// 恢复备份须在同一事务中替换原表并重建派生表；失败时原表和旧索引一并回滚。
func RebuildEmbyObservedEvidenceIndex(conn *gorm.DB) error {
	return conn.Transaction(func(tx *gorm.DB) error {
		// 与正常观察保存共用索引版本写锁；空库不创建虚构的服务器状态。
		if err := tx.Model(&EmbyIndexState{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + 0")).Error; err != nil {
			return err
		}
		if err := tx.AutoMigrate(&EmbyObservedEvidenceIndex{}); err != nil {
			return err
		}
		if err := tx.Where("1 = 1").Delete(&EmbyObservedEvidenceIndex{}).Error; err != nil {
			return err
		}
		var afterID uint
		for {
			var records []EmbyWebhookRecord
			if err := tx.Select("id", "server_id", "server_config_key", "observation_json").
				Where("id > ? AND observed_at > 0 AND observation_json <> ''", afterID).
				Order("id").Limit(100).Find(&records).Error; err != nil {
				return err
			}
			if len(records) == 0 {
				return nil
			}
			refsByRecord := make(map[uint][]embyObservedRef, len(records))
			evidenceIDs := map[uint]bool{}
			for _, record := range records {
				var refs []embyObservedRef
				if err := json.Unmarshal([]byte(record.ObservationJSON), &refs); err != nil {
					return fmt.Errorf("解析 Emby 观察 %d: %w", record.ID, err)
				}
				refsByRecord[record.ID] = refs
				for _, ref := range refs {
					evidenceIDs[ref.EvidenceID] = true
				}
			}
			evidence, err := loadEmbyObservedEvidenceTx(tx, slices.Sorted(maps.Keys(evidenceIDs)))
			if err != nil {
				return err
			}
			var rows []EmbyObservedEvidenceIndex
			for _, record := range records {
				for position, ref := range refsByRecord[record.ID] {
					entry, ok := evidence[ref.EvidenceID]
					if !ok {
						return fmt.Errorf("Emby 观察 %d 缺少证据 %d: %w", record.ID, ref.EvidenceID, gorm.ErrRecordNotFound)
					}
					row, err := makeEmbyObservedIndex(record, position, entry)
					if err != nil {
						return fmt.Errorf("重建 Emby 观察 %d: %w", record.ID, err)
					}
					rows = append(rows, row)
				}
			}
			if len(rows) > 0 {
				if err := tx.CreateInBatches(&rows, 50).Error; err != nil {
					return err
				}
			}
			afterID = records[len(records)-1].ID
		}
	})
}

func embyObservationScopeID(item EmbyMediaItem, itemType string) string {
	switch itemType {
	case "Movie":
		return item.PartOfItemID
	case "Season":
		return item.SeasonId
	case "Series":
		return item.SeriesId
	default:
		return ""
	}
}

func embyObservedIndexQuery(tx *gorm.DB, record EmbyWebhookRecord) *gorm.DB {
	return tx.Model(&EmbyObservedEvidenceIndex{}).Table("emby_observed_evidence_indices AS o").
		Joins("JOIN emby_webhook_records AS r ON r.id = o.record_id AND r.server_id = o.server_id AND r.server_config_key = o.server_config_key").
		Where("o.server_id = ? AND o.server_config_key = ? AND r.observed_at > 0 AND r.observation_json <> ''", record.ServerID, record.ServerConfigKey)
}

func embyObservedCandidateIDsTx(tx *gorm.DB, record EmbyWebhookRecord) ([]string, error) {
	candidates := map[string]bool{record.ItemID: true}
	var scopeColumn string
	switch record.ItemType {
	case "Movie":
		scopeColumn = "o.part_of_item_id"
	case "Season":
		scopeColumn = "o.season_id"
	case "Series":
		scopeColumn = "o.series_id"
	default:
		return []string{record.ItemID}, nil
	}
	var historicalIDs []string
	if err := embyObservedIndexQuery(tx, record).Where(scopeColumn+" = ?", record.ItemID).
		Distinct("o.item_id").Pluck("o.item_id", &historicalIDs).Error; err != nil {
		return nil, err
	}
	for _, id := range historicalIDs {
		candidates[id] = true
	}
	// 普通同步的旧成员也可能只有一份已移出的观察；查询 state 的持久成员投影，
	// 并纳入历史观察父项的当前分段，不读取无关条目的 JSON。
	currentIDs, err := embyStateEvidenceCandidateIDsTx(tx, record.ServerID, record.ServerConfigKey, record.ItemID, record.ItemType, historicalIDs)
	if err != nil {
		return nil, err
	}
	for _, id := range currentIDs {
		candidates[id] = true
	}
	return slices.Sorted(maps.Keys(candidates)), nil
}

type embyObservedBatch struct {
	refs     []embyObservedRef
	evidence map[uint]EmbyItemEvidence
	states   map[string]EmbyItemState
}

func loadEmbyObservedBatchTx(tx *gorm.DB, record EmbyWebhookRecord) (embyObservedBatch, error) {
	result := embyObservedBatch{states: map[string]EmbyItemState{}}
	itemIDs, err := embyObservedCandidateIDsTx(tx, record)
	if err != nil {
		return result, err
	}
	var indexed []EmbyObservedEvidenceIndex
	recordIDs, evidenceIDs := map[uint]bool{}, map[uint]bool{}
	for ids := range slices.Chunk(itemIDs, embyObservationBatchSize) {
		var rows []EmbyObservedEvidenceIndex
		// 找到 item 后取其全部归属下的引用，才能看见较新的移出观察。
		if err := embyObservedIndexQuery(tx, record).Select("o.*").Where("o.item_id IN ?", ids).Find(&rows).Error; err != nil {
			return result, err
		}
		for _, row := range rows {
			recordIDs[row.RecordID], evidenceIDs[row.EvidenceID] = true, true
		}
		indexed = append(indexed, rows...)
	}
	if len(indexed) == 0 {
		return result, nil
	}
	refsByRecord := map[uint][]embyObservedRef{}
	for ids := range slices.Chunk(slices.Sorted(maps.Keys(recordIDs)), embyObservationBatchSize) {
		var records []EmbyWebhookRecord
		if err := tx.Select("id", "server_id", "server_config_key", "observed_at", "observation_json").Where("id IN ?", ids).Find(&records).Error; err != nil {
			return result, err
		}
		for _, observed := range records {
			if observed.ServerID != record.ServerID || observed.ServerConfigKey != record.ServerConfigKey || observed.ObservedAt <= 0 {
				return result, ErrEmbyIdentityAmbiguous
			}
			var refs []embyObservedRef
			if err := json.Unmarshal([]byte(observed.ObservationJSON), &refs); err != nil {
				return result, err
			}
			refsByRecord[observed.ID] = refs
		}
	}
	result.evidence, err = loadEmbyObservedEvidenceTx(tx, slices.Sorted(maps.Keys(evidenceIDs)))
	if err != nil {
		return result, err
	}
	observedItemIDs := map[string]bool{}
	for _, row := range indexed {
		refs := refsByRecord[row.RecordID]
		if row.RefIndex < 0 || row.RefIndex >= len(refs) || refs[row.RefIndex].EvidenceID != row.EvidenceID {
			return result, ErrEmbyIdentityAmbiguous
		}
		evidence, ok := result.evidence[row.EvidenceID]
		if !ok {
			return result, gorm.ErrRecordNotFound
		}
		if evidence.ItemID != row.ItemID || evidence.ServerID != record.ServerID || evidence.ServerConfigKey != record.ServerConfigKey {
			return result, ErrEmbyIdentityAmbiguous
		}
		result.refs = append(result.refs, refs[row.RefIndex])
		observedItemIDs[row.ItemID] = true
	}
	for ids := range slices.Chunk(slices.Sorted(maps.Keys(observedItemIDs)), embyObservationBatchSize) {
		var states []EmbyItemState
		if err := tx.Where("server_id = ? AND item_id IN ?", record.ServerID, ids).Find(&states).Error; err != nil {
			return result, err
		}
		for _, state := range states {
			result.states[state.ItemID] = state
		}
	}
	return result, nil
}
