package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"qmediasync/internal/db"
)

// SyncRootEvidence 保存正常 STRM 扫描已观察到的同步根及根外祖先，不把祖先纳入管理范围。
// Ancestors 按网盘根至同步根排序，包含两端；ObservedAt 只来自真实网盘读取。
type SyncRootEvidence struct {
	BaseModel
	SyncPathID      uint                    `gorm:"uniqueIndex;not null" json:"sync_path_id"`
	SourceType      SourceType              `gorm:"type:text;not null" json:"source_type"`
	AccountID       uint                    `gorm:"not null" json:"account_id"`
	AccountIdentity string                  `gorm:"type:text;not null" json:"account_identity"`
	RootFileID      string                  `gorm:"type:text;not null" json:"root_file_id"`
	RootPath        string                  `gorm:"type:text;not null" json:"root_path"`
	AncestorsJSON   string                  `gorm:"type:text;not null" json:"-"`
	ObservedAt      int64                   `gorm:"not null" json:"observed_at"`
	InvalidatedAt   int64                   `gorm:"not null;default:0" json:"invalidated_at"`
	Ancestors       []EmbyDirectoryAncestor `gorm:"-" json:"ancestors"`
}

// InvalidateSyncRootEvidence 将本轮已确认矛盾的旧资料保留并失效。
// 首次观察只写入失效标记，防止失败后绕过本轮观察回用旧快照；成功账本才发布有效资料。
func InvalidateSyncRootEvidence(ctx context.Context, evidence SyncRootEvidence) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(evidence.Ancestors)
	if err != nil {
		return fmt.Errorf("编码同步根目录父链失败：%w", err)
	}
	evidence.AncestorsJSON = string(raw)
	evidence.InvalidatedAt = evidence.ObservedAt
	return db.Db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "sync_path_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"invalidated_at"}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL: "sync_root_evidences.observed_at <= excluded.observed_at AND " +
				"sync_root_evidences.invalidated_at <= excluded.invalidated_at AND (" +
				"sync_root_evidences.source_type <> excluded.source_type OR " +
				"sync_root_evidences.account_id <> excluded.account_id OR " +
				"sync_root_evidences.account_identity <> excluded.account_identity OR " +
				"sync_root_evidences.root_file_id <> excluded.root_file_id OR " +
				"sync_root_evidences.root_path <> excluded.root_path OR " +
				"sync_root_evidences.ancestors_json <> excluded.ancestors_json)",
		}}},
	}).Create(&evidence).Error
}

// Validate 检查单次观察包含连续、无重复且以当前同步根结束的完整物理父链。
func (e SyncRootEvidence) Validate() error {
	if e.SyncPathID == 0 || e.SourceType != SourceType115 || e.AccountID == 0 || e.AccountIdentity == "" || e.RootFileID == "" || e.ObservedAt <= 0 || len(e.Ancestors) == 0 {
		return fmt.Errorf("同步根目录资料身份或观察时间不完整")
	}
	seen := make(map[string]bool, len(e.Ancestors))
	for i, node := range e.Ancestors {
		normalized, valid := embyRemoteDirectory(node.Path)
		if node.FileID == "" || seen[node.FileID] || !valid || normalized != node.Path {
			return fmt.Errorf("同步根目录资料包含无效身份或路径")
		}
		seen[node.FileID] = true
		if i == 0 {
			if node.FileID != "0" || node.ParentID != "" || node.Path != "/" {
				return fmt.Errorf("同步根目录资料缺少网盘根")
			}
		} else if parent := e.Ancestors[i-1]; node.ParentID != parent.FileID || path.Dir(node.Path) != parent.Path || node.Path == parent.Path {
			return fmt.Errorf("同步根目录资料父链不连续")
		}
	}
	root := e.Ancestors[len(e.Ancestors)-1]
	if root.FileID != e.RootFileID || root.Path != e.RootPath {
		return fmt.Errorf("同步根目录资料与入口身份不一致")
	}
	return nil
}

// LoadSyncRootEvidenceBatch 批量读取持久同步根资料；缺行与读取失败分别返回。
func LoadSyncRootEvidenceBatch(ctx context.Context, syncPathIDs []uint) (map[uint]SyncRootEvidence, error) {
	return LoadSyncRootEvidenceBatchTx(db.Db.WithContext(ctx), syncPathIDs)
}

// LoadSyncRootEvidenceBatchTx 允许调用方在同一数据库快照内读取根资料和根下 SyncFile。
func LoadSyncRootEvidenceBatchTx(tx *gorm.DB, syncPathIDs []uint) (map[uint]SyncRootEvidence, error) {
	result := make(map[uint]SyncRootEvidence, len(syncPathIDs))
	cache := embyApplyCacheFrom(tx)
	requestedIDs := syncPathIDs
	if cache != nil {
		syncPathIDs = make([]uint, 0, len(requestedIDs))
		for _, id := range requestedIDs {
			if cache.rootEvidenceLoaded[id] {
				if row, ok := cache.rootEvidence[id]; ok {
					result[id] = row
				}
			} else {
				syncPathIDs = append(syncPathIDs, id)
			}
		}
		tx = tx.Clauses(clause.Locking{Strength: "SHARE"}).Session(&gorm.Session{})
	}
	for start := 0; start < len(syncPathIDs); start += 256 {
		var rows []SyncRootEvidence
		if err := tx.Where("sync_path_id IN ?", syncPathIDs[start:min(start+256, len(syncPathIDs))]).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("读取同步根目录资料失败：%w", err)
		}
		for _, row := range rows {
			if err := json.Unmarshal([]byte(row.AncestorsJSON), &row.Ancestors); err != nil {
				return nil, fmt.Errorf("读取同步根 %d 的目录父链失败：%w", row.SyncPathID, err)
			}
			if err := row.Validate(); err != nil {
				return nil, fmt.Errorf("同步根 %d 的目录资料无效：%w", row.SyncPathID, err)
			}
			result[row.SyncPathID] = row
		}
	}
	if cache != nil {
		for _, id := range requestedIDs {
			cache.rootEvidenceLoaded[id] = true
		}
		for id, row := range result {
			cache.rootEvidence[id] = row
		}
	}
	return result, nil
}

// SaveSyncRootEvidenceTx 在文件账本已提交后保存整根资料，同时核对并更新路径反查所得的根 ID。
// 调用方持有同步范围并管理事务及位置缓存失效；失败不得删除旧资料。
func SaveSyncRootEvidenceTx(tx *gorm.DB, evidence SyncRootEvidence, expectedRootID string) error {
	if err := evidence.Validate(); err != nil {
		return err
	}
	var root SyncPath
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&root, evidence.SyncPathID).Error; err != nil {
		return fmt.Errorf("核对同步根配置失败：%w", err)
	}
	var previous SyncRootEvidence
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("sync_path_id = ?", evidence.SyncPathID).Take(&previous).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("核对旧同步根目录资料失败：%w", err)
	}
	if evidence.ObservedAt < previous.ObservedAt || evidence.ObservedAt < previous.InvalidatedAt {
		return fmt.Errorf("同步根目录资料已有更新，本次旧观察未保存")
	}
	var account Account
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&account, evidence.AccountID).Error; err != nil {
		return fmt.Errorf("核对同步根账号失败：%w", err)
	}
	if root.SourceType != evidence.SourceType || root.AccountId != evidence.AccountID || account.SourceType != evidence.SourceType ||
		EmbyAccountIdentity(account) != evidence.AccountIdentity || !embyRemoteDirectoriesMatch(evidence.SourceType, root.RemotePath, evidence.RootPath) ||
		(root.BaseCid != expectedRootID && root.BaseCid != evidence.RootFileID) {
		return fmt.Errorf("同步根配置或账号已变化，本次目录资料未保存")
	}
	raw, err := json.Marshal(evidence.Ancestors)
	if err != nil {
		return fmt.Errorf("编码同步根目录父链失败：%w", err)
	}
	evidence.AncestorsJSON = string(raw)
	evidence.InvalidatedAt = 0
	if root.BaseCid != evidence.RootFileID {
		if err := tx.Model(&SyncPath{}).Where("id = ?", root.ID).Update("base_cid", evidence.RootFileID).Error; err != nil {
			return fmt.Errorf("保存同步根当前身份失败：%w", err)
		}
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "sync_path_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"source_type", "account_id", "account_identity", "root_file_id", "root_path", "ancestors_json", "observed_at", "invalidated_at", "updated_at"}),
		Where:     clause.Where{Exprs: []clause.Expression{clause.Expr{SQL: "excluded.observed_at >= sync_root_evidences.observed_at"}}},
	}).Create(&evidence).Error
}
