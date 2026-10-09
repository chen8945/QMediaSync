package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

// EmbyDeletionSupplement 保存本次删除实际取得的身份，不改写删除前的来源或文件证据。
// ReadAt 是远端读取开始时间；持久化和重试不能为旧读取刷新时间。
type EmbyDeletionSupplement struct {
	Owner  EmbyDeletionOwnerRef `json:"owner"`
	Source EmbySnapshotSource   `json:"source"`
	File   EmbyFrozenFile       `json:"file"`
	ReadAt time.Time            `json:"read_at"`
}

func validateEmbyDeletionContext(conn *gorm.DB, input EmbyDeletionInput) (*EmbyConfig, error) {
	if !input.Authorized {
		return nil, ErrEmbyDeleteDisabled
	}
	var config EmbyConfig
	if err := conn.First(&config).Error; err != nil {
		return nil, err
	}
	if config.SyncEnabled != 1 || config.EnableDeleteNetdisk != 1 {
		return nil, ErrEmbyDeleteDisabled
	}
	var index EmbyIndexState
	if err := conn.First(&index, 1).Error; err != nil {
		return nil, err
	}
	if input.ServerID == "" || index.ServerID != input.ServerID || index.ServerConfigKey != input.ServerConfigKey || EmbyServerConfigIdentity(&config) != input.ServerConfigKey {
		return nil, ErrEmbySnapshotStale
	}
	return &config, nil
}

func validateEmbyDeletionOwnerEvidence(conn *gorm.DB, input EmbyDeletionInput, owner EmbyDeletionOwner, requireDeleted bool) error {
	ref := embyOwnerRef(owner)
	var state EmbyItemState
	if err := conn.Where("server_id = ? AND item_id = ?", input.ServerID, ref.ItemID).First(&state).Error; err != nil {
		return err
	}
	if state.SnapshotID != ref.SnapshotID || state.Generation != ref.Generation || requireDeleted && !state.Deleted {
		return ErrEmbySnapshotStale
	}
	var evidence EmbyItemEvidence
	if err := conn.First(&evidence, ref.SnapshotID).Error; err != nil {
		return err
	}
	if evidence != owner.Evidence || evidence.ServerID != input.ServerID || evidence.ServerConfigKey != input.ServerConfigKey || state.IdentityKey != evidence.IdentityKey ||
		embyJSON(owner.Files) != evidence.FilesJSON || owner.Item.ItemId != evidence.ItemID || owner.Item.Generation != evidence.Generation {
		return ErrEmbyIdentityAmbiguous
	}
	return nil
}

// ValidateEmbyDeletionSource 在补查前仅核验原来源、删除授权和当前状态，不需要补造物理身份。
func ValidateEmbyDeletionSource(ctx context.Context, input EmbyDeletionInput, owner EmbyDeletionOwner, source EmbySnapshotSource) error {
	conn := db.Db.WithContext(ctx)
	config, err := validateEmbyDeletionContext(conn, input)
	if err != nil {
		return err
	}
	if !input.CleanupPolicy.AllowsJointBatch() || !slices.ContainsFunc(input.Owners, func(frozen EmbyDeletionOwner) bool { return embyJSON(frozen) == embyJSON(owner) }) {
		return ErrEmbyIdentityAmbiguous
	}
	if err := validateEmbyDeletionOwnerEvidence(conn, input, owner, true); err != nil {
		return err
	}
	return validateEmbyFrozenDeletionSource(conn, config, owner, source)
}

func validateEmbyFrozenDeletionSource(conn *gorm.DB, config *EmbyConfig, owner EmbyDeletionOwner, source EmbySnapshotSource) error {
	if source.SyncFileID != 0 || source.ItemID != "" && source.ItemID != owner.Item.ItemId || source.AccountID == 0 || source.AccountIdentity == "" || source.ObjectID == "" {
		return ErrEmbyIdentityAmbiguous
	}
	var sources []EmbySnapshotSource
	if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &sources); err != nil {
		return err
	}
	if !slices.Contains(sources, source) || embySourceAlreadyFrozen(owner, source) {
		return ErrEmbyIdentityAmbiguous
	}
	if err := validateEmbySTRMReferenceTx(conn, config, source.Reference); err != nil {
		return err
	}
	var account Account
	if err := conn.First(&account, source.AccountID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return &EmbyDeletionOwnerConflict{IDs: []string{owner.Item.ItemId}, Err: fmt.Errorf("来源账号不存在: %w", err)}
		}
		return err
	}
	if account.SourceType != source.SourceType || EmbyAccountIdentity(account) != source.AccountIdentity {
		return &EmbyDeletionOwnerConflict{IDs: []string{owner.Item.ItemId}, Err: fmt.Errorf("%w: 来源账号已更换", ErrEmbyDeleteUnverified)}
	}
	switch source.SourceType {
	case SourceType115:
		if source.ObjectKind == "pickcode" || source.ObjectKind == "file_id" {
			return nil
		}
	case SourceTypeBaiduPan:
		if source.ObjectKind == "fs_id" {
			return nil
		}
	case SourceTypeOpenList:
		if source.ObjectKind == "openlist_id" {
			return nil
		}
	}
	return ErrEmbyDeleteUnverified
}

func embyDeletionPhysicalKey(file EmbyFrozenFile) string {
	id := embyFrozenPhysicalID(file)
	if file.SourceType == SourceTypeOpenList {
		id = file.OpenlistObjectID
	}
	if id == "" {
		return ""
	}
	return embyDigest([]any{file.SourceType, file.AccountID, file.AccountIdentity, id})
}

// 同一稳定对象可能由不同 PickCode 或 STRM 引用定位，仍须核对位置和内容代际。
func embySupplementFilesMatch(left, right EmbyFrozenFile) bool {
	if embyDeletionPhysicalKey(left) == "" || embyDeletionPhysicalKey(left) != embyDeletionPhysicalKey(right) {
		return false
	}
	l, r := embyRemoteFromFrozen(left), embyRemoteFromFrozen(right)
	l.Path, _ = embyRemoteDirectory(l.Path)
	r.Path, _ = embyRemoteDirectory(r.Path)
	l.PickCode, r.PickCode = "", ""
	return l == r
}

func embySourceAlreadyFrozen(owner EmbyDeletionOwner, source EmbySnapshotSource) bool {
	return slices.ContainsFunc(owner.Files, func(file EmbyFrozenFile) bool {
		if source.SourceType != "" && source.SourceType != file.SourceType ||
			source.AccountID != 0 && source.AccountID != file.AccountID ||
			source.AccountIdentity != "" && source.AccountIdentity != file.AccountIdentity {
			return false
		}
		return source.SyncFileID != 0 && source.SyncFileID == file.SyncFileID ||
			source.ObjectID == "" && source.ID != "" && source.ID == file.SourceID ||
			source.PickCode != "" && source.PickCode == file.PickCode
	})
}

func addEmbyDeletionSupplements(ctx context.Context, plan *EmbyDeletionPlan) error {
	for i, supplement := range plan.Supplements {
		// 同一来源只能绑定一次，不能在重试或同一计划内改指另一个对象。
		if slices.ContainsFunc(plan.Supplements[:i], func(previous EmbyDeletionSupplement) bool {
			return previous.Owner == supplement.Owner && previous.Source == supplement.Source
		}) {
			return ErrEmbyIdentityAmbiguous
		}
		index := slices.IndexFunc(plan.Targets, func(target EmbyDeletionTarget) bool {
			return target.Kind == "video" && embyDeletionPhysicalKey(target.File) != "" && embyDeletionPhysicalKey(target.File) == embyDeletionPhysicalKey(supplement.File)
		})
		if index == -1 {
			index = len(plan.Targets)
			plan.Targets = append(plan.Targets, EmbyDeletionTarget{Key: EmbyDeletionFileKey(supplement.File), Kind: "video", File: supplement.File})
		}
		target := &plan.Targets[index]
		if !embySupplementFilesMatch(target.File, supplement.File) {
			target.Reason = "source_identity_conflict"
		}
		if !slices.Contains(target.Owners, supplement.Owner) {
			target.Owners = append(target.Owners, supplement.Owner)
		}
		target.SourceBindings = append(target.SourceBindings, supplement)
	}
	for _, owner := range plan.Input.Owners {
		var sources []EmbySnapshotSource
		if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &sources); err != nil {
			return err
		}
		for _, source := range sources {
			if embySourceAlreadyFrozen(owner, source) || slices.ContainsFunc(plan.Supplements, func(supplement EmbyDeletionSupplement) bool {
				return supplement.Owner == embyOwnerRef(owner) && supplement.Source == source
			}) {
				continue
			}
			issue := "source_unresolved:" + owner.Item.ItemId
			if !slices.Contains(plan.Issues, issue) {
				plan.Issues = append(plan.Issues, issue)
			}
		}
	}
	for _, target := range plan.Targets {
		if len(target.SourceBindings) != 0 && target.Reason == "" {
			if err := validateEmbyDeletionTarget(ctx, *plan, target); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateEmbyTargetSourceBindings(conn *gorm.DB, config *EmbyConfig, plan EmbyDeletionPlan, target EmbyDeletionTarget) error {
	if target.File.ExternalSource && (target.Kind != "video" || len(target.SourceBindings) == 0) {
		return ErrEmbyIdentityAmbiguous
	}
	for _, binding := range target.SourceBindings {
		if !slices.ContainsFunc(plan.Supplements, func(supplement EmbyDeletionSupplement) bool { return embyJSON(supplement) == embyJSON(binding) }) ||
			!slices.Contains(target.Owners, binding.Owner) || !embySupplementFilesMatch(binding.File, target.File) {
			return ErrEmbyIdentityAmbiguous
		}
		ownerIndex := slices.IndexFunc(plan.Input.Owners, func(owner EmbyDeletionOwner) bool { return embyOwnerRef(owner) == binding.Owner })
		if ownerIndex == -1 {
			return ErrEmbyIdentityAmbiguous
		}
		if err := validateEmbyDeletionSupplement(conn, config, plan.Input.Owners[ownerIndex], binding); err != nil {
			return err
		}
	}
	// 同对象的其他引用不能通过删减 target.Owners 或 SourceBindings 被绕过。
	for _, binding := range plan.Supplements {
		if embyDeletionPhysicalKey(binding.File) == embyDeletionPhysicalKey(target.File) &&
			!slices.ContainsFunc(target.SourceBindings, func(present EmbyDeletionSupplement) bool { return embyJSON(present) == embyJSON(binding) }) {
			return ErrEmbyIdentityAmbiguous
		}
	}
	return nil
}

func validateEmbyDeletionSupplement(conn *gorm.DB, config *EmbyConfig, owner EmbyDeletionOwner, binding EmbyDeletionSupplement) error {
	file, source := binding.File, binding.Source
	if binding.Owner != embyOwnerRef(owner) || binding.ReadAt.IsZero() || binding.ReadAt.After(time.Now()) ||
		!file.ExternalSource || file.SyncFileID != 0 || file.SyncPathID != 0 || file.LocalRoot != "" || file.LocalFilePath != "" || file.RemoteRoot != "" || file.RootFileID != "" || file.FileCreatedAt != 0 || file.Reason != "" ||
		source.SyncFileID != 0 || source.ItemID != "" && source.ItemID != owner.Item.ItemId ||
		file.SourceType != source.SourceType || file.AccountID == 0 || file.AccountID != source.AccountID || file.AccountIdentity == "" || file.AccountIdentity != source.AccountIdentity || file.SourceID != source.ID || file.Reference != source.Reference {
		return ErrEmbyIdentityAmbiguous
	}
	if err := validateEmbyFrozenDeletionSource(conn, config, owner, source); err != nil {
		return err
	}
	if source.ObjectID == "" || embyDeletionPhysicalKey(file) == "" || !embyRemoteMatches(file, embyRemoteFromFrozen(file)) {
		return ErrEmbyIdentityAmbiguous
	}
	switch file.SourceType {
	case SourceType115:
		if file.ParentID == "" || !(source.ObjectKind == "pickcode" && source.ObjectID == file.PickCode || source.ObjectKind == "file_id" && source.ObjectID == file.FileID) {
			return ErrEmbyIdentityAmbiguous
		}
	case SourceTypeBaiduPan:
		if source.ObjectKind != "fs_id" || source.ObjectID != embyFrozenPhysicalID(file) {
			return ErrEmbyIdentityAmbiguous
		}
	case SourceTypeOpenList:
		// 删除期首次得到的对象 ID 不能证明路径自观察以来未被替换。
		if source.ObjectKind != "openlist_id" || source.ObjectID != file.OpenlistObjectID || !embyRemoteDirectoriesMatch(file.SourceType, file.FileID, path.Join(file.Path, file.FileName)) {
			return ErrEmbyDeleteUnverified
		}
	default:
		return ErrEmbyDeleteUnsupported
	}
	return nil
}

// ValidateEmbyDeletionSupplement 在远端补查结果进入首次计划前核验其来源和当前账号。
func ValidateEmbyDeletionSupplement(ctx context.Context, owner EmbyDeletionOwner, supplement EmbyDeletionSupplement) error {
	conn := db.Db.WithContext(ctx)
	var config EmbyConfig
	if err := conn.First(&config).Error; err != nil {
		return err
	}
	return validateEmbyDeletionSupplement(conn, &config, owner, supplement)
}
