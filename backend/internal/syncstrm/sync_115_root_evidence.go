package syncstrm

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// observe115RootPaths 只复用正常扫描已返回的父链，不读取远端，也不纳管根外目录。
// 根详情发生在 Start115Sync 之前，资料不能放进随后会被重建的 Sync115。
func (s *SyncStrm) observe115RootPaths(ctx context.Context, parts []v115open.FileDetailPath, rootID string, observedAt int64) error {
	if s == nil || s.TmpSyncPath || s.IsFile || s.SyncPathId == 0 || s.Account == nil || s.Account.SourceType != models.SourceType115 || s.Sync == nil || s.Sync.ID == 0 {
		// 单文件后处理的同步器不执行完整后台账本，也不能发布整根观察。
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identity := models.EmbyAccountIdentity(*s.Account)
	if identity == "" || len(parts) == 0 {
		return nil
	}
	// 部分接口省略固定的网盘根节点；完整名称链仍从网盘根开始。
	nodes := []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}}
	parentID, fullPath := "0", "/"
	configuredRootIndex := -1
	for i, part := range parts {
		if rootID == "0" {
			break
		}
		if part.FileId == "0" && i == 0 {
			continue
		}
		if part.FileId == "" || part.FileId == "0" || part.Name == "" || part.Name == "." || part.Name == ".." || strings.ContainsAny(part.Name, "/\\\x00") {
			return fmt.Errorf("115 同步根祖先身份或名称不完整")
		}
		fullPath = path.Join(fullPath, part.Name)
		nodes = append(nodes, models.EmbyDirectoryAncestor{FileID: part.FileId, ParentID: parentID, Path: fullPath})
		if normalizeStrmRemotePath(fullPath) == normalizeStrmRemotePath(s.SourcePath) {
			configuredRootIndex = len(nodes) - 1
		}
		parentID = part.FileId
		if part.FileId == rootID {
			break
		}
	}
	root := nodes[len(nodes)-1]
	if root.FileID != rootID {
		if configuredRootIndex < 0 {
			// 上传辅助查询可位于同步根外，不能据此补造本根资料。
			return nil
		}
		// 同一路径已换成另一目录 ID，必须使原根失效，不能当作无关父链忽略。
		nodes = nodes[:configuredRootIndex+1]
		root = nodes[configuredRootIndex]
	}
	evidence := &models.SyncRootEvidence{
		SyncPathID: s.SyncPathId, SourceType: s.Account.SourceType, AccountID: s.Account.ID,
		AccountIdentity: identity, RootFileID: root.FileID, RootPath: root.Path,
		Ancestors: nodes, ObservedAt: observedAt,
	}
	if err := evidence.Validate(); err != nil {
		return err
	}
	s.rootEvidenceMu.Lock()
	defer s.rootEvidenceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.rootEvidenceConflict {
		return fmt.Errorf("115 同步根本轮返回了不同目录资料")
	}
	if root.FileID != rootID || normalizeStrmRemotePath(root.Path) != normalizeStrmRemotePath(s.SourcePath) {
		s.rootEvidenceConflict = true
		if err := models.InvalidateSyncRootEvidence(ctx, *evidence); err != nil {
			return fatalSyncError(fmt.Errorf("标记已变化的同步根目录资料失败：%w", err))
		}
		return fmt.Errorf("115 同步根当前身份或路径与任务入口不一致")
	}
	if old := s.rootEvidence; old != nil {
		if old.RootFileID != evidence.RootFileID || !slices.Equal(old.Ancestors, evidence.Ancestors) {
			s.rootEvidenceConflict = true
			if err := models.InvalidateSyncRootEvidence(ctx, *evidence); err != nil {
				return fatalSyncError(fmt.Errorf("标记已变化的同步根目录资料失败：%w", err))
			}
			return fmt.Errorf("115 同步根本轮返回了不同目录资料")
		}
		// 保留首次真实观察时间，不把重复使用或后台保存时间变成新观察。
		return nil
	}
	if err := models.InvalidateSyncRootEvidence(ctx, *evidence); err != nil {
		return fatalSyncError(fmt.Errorf("核对同步根历史目录资料失败：%w", err))
	}
	s.rootEvidence = evidence
	return nil
}

func (s *SyncStrm) observe115RootList(ctx context.Context, response *v115open.FileListResp, observedAt int64) error {
	parts := make([]v115open.FileDetailPath, 0, len(response.Path))
	for _, part := range response.Path {
		parts = append(parts, v115open.FileDetailPath{FileId: part.FileId.String(), Name: part.Name})
	}
	return s.observe115RootPaths(ctx, parts, s.SourcePathId, observedAt)
}

func (s *SyncStrm) saveRootEvidence() error {
	if err := s.Context.Err(); err != nil {
		return err
	}
	if s.rootEvidence == nil {
		return nil
	}
	s.scanMu.Lock()
	failed := len(s.scanFailures) > 0
	s.scanMu.Unlock()
	if failed || s.rootEvidenceConflict || s.sync115 != nil && s.sync115.pathConflict {
		s.Sync.Logger.Warnf("本轮目录扫描未完整确认，保留上次保存的同步根目录资料")
		return nil
	}
	if err := syncPositionTransaction(db.Db.WithContext(s.Context), s.rootEvidenceBaseID != s.rootEvidence.RootFileID, func(tx *gorm.DB) error {
		return models.SaveSyncRootEvidenceTx(tx, *s.rootEvidence, s.rootEvidenceBaseID)
	}); err != nil {
		return fmt.Errorf("保存同步根目录资料失败：%w", err)
	}
	return nil
}
