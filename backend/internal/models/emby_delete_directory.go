package models

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

type embyDeletionFinalGuardKey struct{}

// IsEmbyDeletionFinalGuard 表示已进入目录发送前检查，只能使用已完成且未过期的核验读取。
func IsEmbyDeletionFinalGuard(ctx context.Context) bool {
	final, _ := ctx.Value(embyDeletionFinalGuardKey{}).(bool)
	return final
}

// AddEmbyDirectoryTargets 只从已保存的媒体根选取目录，不向未证明的祖先上溯。
// 先检查保留成员，调用方随后只为未被覆盖的文件读取旁车清单。
func AddEmbyDirectoryTargets(ctx context.Context, plan *EmbyDeletionPlan, factory EmbyDeleteProviderFactory, verifiers ...EmbyDeletionVerifier) error {
	if plan == nil || !plan.Input.CleanupPolicy.AllowsDirectoryContent() || !slices.Contains([]string{"Movie", "Season", "Series"}, plan.Input.ItemType) {
		return nil
	}
	if factory == nil {
		factory = NewEmbyDeleteProvider
	}
	candidates := slices.Clone(plan.Input.DirectoryScopes)
	// 路径短的已证明媒体根优先；这里只消除包含关系，不寻找共同祖先。
	slices.SortFunc(candidates, func(a, b EmbyDirectoryScope) int {
		left, right := embyDirectoryFullPath(a), embyDirectoryFullPath(b)
		if len(left) != len(right) {
			return len(left) - len(right)
		}
		return strings.Compare(EmbyDirectoryScopeKey(a), EmbyDirectoryScopeKey(b))
	})
	for _, scope := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validEmbyDirectoryScope(scope) || scope.MediaType != plan.Input.ItemType || scope.ItemID != plan.Input.ItemID {
			continue
		}
		provider, err := factory(scope.Root)
		if err != nil {
			continue
		}
		directoryProvider, supported := provider.(EmbyDeleteDirectoryProvider)
		if !supported || !directoryProvider.SupportsDirectoryDelete() {
			continue
		}
		target := EmbyDeletionTarget{Key: "directory:" + EmbyDirectoryScopeKey(scope), Kind: "directory", File: scope.Root, Directory: &scope}
		blocked := false
		for _, member := range plan.Targets {
			if !embyDirectoryContainsFile(scope, member.File) {
				continue
			}
			if member.CoveredBy != "" || member.Kind == "directory" || member.Reason != "" || member.File.Reason != "" {
				blocked = true
				break
			}
			target.CoveredKeys = append(target.CoveredKeys, member.Key)
			for _, ref := range member.Owners {
				if !slices.Contains(target.Owners, ref) {
					target.Owners = append(target.Owners, ref)
				}
			}
		}
		if blocked || len(target.CoveredKeys) == 0 || len(target.Owners) == 0 {
			continue
		}
		if err := validateEmbyDirectoryTarget(ctx, *plan, target); err != nil {
			continue
		}
		if _, _, err := embyPrepareDirectory(ctx, scope, provider); err != nil && !errors.Is(err, ErrEmbyRemoteFileAbsent) {
			continue
		}
		if err := embyVerifyCompletedDirectoryMembers(ctx, plan.Input, target, factory); err != nil {
			continue
		}
		if len(verifiers) > 0 && verifiers[0] != nil {
			if err := verifiers[0](WithEmbyDirectoryProvider(ctx, provider), plan.Input, target); err != nil {
				continue
			}
		}
		// 已知旁车有自己的历史身份；未知文件只属于目录操作，不伪造逐文件结果。
		embyAddDirectorySidecars(plan, &target)
		for i := range plan.Targets {
			if slices.Contains(target.CoveredKeys, plan.Targets[i].Key) {
				plan.Targets[i].CoveredBy = target.Key
			}
		}
		plan.Targets = append(plan.Targets, target)
	}
	return nil
}

func embyDirectoryFullPath(scope EmbyDirectoryScope) string {
	return path.Join(scope.Root.Path, scope.Root.FileName)
}

func embyDirectoryContainsFile(scope EmbyDirectoryScope, file EmbyFrozenFile) bool {
	return file.SourceType == scope.Root.SourceType && file.AccountID == scope.Root.AccountID && file.AccountIdentity == scope.Root.AccountIdentity &&
		embyRemotePathWithin(file.SourceType, embyDirectoryFullPath(scope), path.Join(file.Path, file.FileName))
}

func embyAddDirectorySidecars(plan *EmbyDeletionPlan, directory *EmbyDeletionTarget) {
	for _, file := range plan.Input.Sidecars {
		if file.Reason != "" || !embyDirectoryContainsFile(*directory.Directory, file) {
			continue
		}
		key := EmbyDeletionFileKey(file)
		if slices.ContainsFunc(plan.Targets, func(target EmbyDeletionTarget) bool { return target.Key == key }) {
			continue
		}
		var videos []EmbyRemoteFile
		for _, video := range plan.Input.DirectoryVideos {
			if embySameBatchParent(file, video) {
				videos = embyMergeVideos(videos, []EmbyRemoteFile{embyRemoteFromFrozen(video)})
			}
		}
		for _, target := range plan.Targets {
			if target.Kind == "video" && embySameBatchParent(file, target.File) {
				videos = embyMergeVideos(videos, []EmbyRemoteFile{embyRemoteFromFrozen(target.File)})
			}
		}
		refs, exclusive := embySidecarOwners(file.FileName, videos, embyDirectoryTargets(*plan, file))
		if !exclusive {
			continue
		}
		plan.Targets = append(plan.Targets, EmbyDeletionTarget{Key: key, Kind: "sidecar", File: file, Owners: refs, DirectoryVideos: videos})
		directory.CoveredKeys = append(directory.CoveredKeys, key)
	}
}

func validateEmbyDirectoryTarget(ctx context.Context, plan EmbyDeletionPlan, target EmbyDeletionTarget) error {
	if !plan.Input.Authorized {
		return ErrEmbyDeleteDisabled
	}
	if !plan.Input.CleanupPolicy.AllowsDirectoryContent() || target.Kind != "directory" || target.Directory == nil || len(target.Owners) == 0 {
		return ErrEmbyDeleteUnverified
	}
	scope := *target.Directory
	if !validEmbyDirectoryScope(scope) || scope.Root != target.File || scope.MediaType != plan.Input.ItemType || scope.ItemID != plan.Input.ItemID || target.Key != "directory:"+EmbyDirectoryScopeKey(scope) ||
		!slices.ContainsFunc(plan.Input.DirectoryScopes, func(known EmbyDirectoryScope) bool { return embyJSON(known) == embyJSON(scope) }) {
		return ErrEmbyIdentityAmbiguous
	}
	seen := map[string]bool{}
	for _, key := range target.CoveredKeys {
		memberIndex := slices.IndexFunc(plan.Targets, func(member EmbyDeletionTarget) bool { return member.Key == key })
		if memberIndex < 0 || seen[key] {
			return ErrEmbyIdentityAmbiguous
		}
		seen[key] = true
		member := plan.Targets[memberIndex]
		if member.Kind == "directory" || !embyDirectoryContainsFile(scope, member.File) || member.CoveredBy != "" && member.CoveredBy != target.Key {
			return ErrEmbyIdentityAmbiguous
		}
		for _, ref := range member.Owners {
			if !slices.Contains(target.Owners, ref) {
				return ErrEmbyIdentityAmbiguous
			}
		}
	}
	if len(seen) == 0 {
		return ErrEmbyIdentityAmbiguous
	}
	conn := db.Db.WithContext(ctx)
	var config EmbyConfig
	if err := conn.First(&config).Error; err != nil {
		return err
	}
	if config.SyncEnabled != 1 || config.EnableDeleteNetdisk != 1 {
		return ErrEmbyDeleteDisabled
	}
	var index EmbyIndexState
	if err := conn.First(&index, 1).Error; err != nil {
		return err
	}
	if plan.Input.ServerID == "" || index.ServerID != plan.Input.ServerID || index.ServerConfigKey != plan.Input.ServerConfigKey || EmbyServerConfigIdentity(&config) != plan.Input.ServerConfigKey {
		return ErrEmbySnapshotStale
	}
	var account Account
	if err := conn.First(&account, scope.Root.AccountID).Error; err != nil {
		return err
	}
	if account.SourceType != scope.Root.SourceType || EmbyAccountIdentity(account) != scope.Root.AccountIdentity {
		return ErrEmbyIdentityAmbiguous
	}
	var root SyncPath
	if err := conn.First(&root, scope.Root.SyncPathID).Error; err != nil {
		return err
	}
	if root.SourceType != scope.Root.SourceType || root.AccountId != scope.Root.AccountID || root.GetFullLocalPath() != scope.Root.LocalRoot || root.RemotePath != scope.Root.RemoteRoot || root.BaseCid != scope.Root.RootFileID {
		return ErrEmbyIdentityAmbiguous
	}
	if !filepath.IsAbs(scope.LocalPath) || !embyPathWithin(root.GetFullLocalPath(), scope.LocalPath) || !embyRemotePathWithin(root.SourceType, root.RemotePath, embyDirectoryFullPath(scope)) {
		return ErrEmbyIdentityAmbiguous
	}
	var roots []SyncPath
	if err := conn.Where("source_type = ? AND account_id = ?", scope.Root.SourceType, scope.Root.AccountID).Find(&roots).Error; err != nil {
		return err
	}
	for _, protected := range roots {
		remote := protected.RemotePath
		if remote == "" {
			remote = "/"
		}
		if _, valid := embyRemoteDirectory(remote); !valid {
			return ErrEmbyIdentityAmbiguous
		}
		if scope.Root.FileID == protected.BaseCid || embyRemotePathWithin(scope.Root.SourceType, embyDirectoryFullPath(scope), remote) || embyPathWithin(scope.LocalPath, protected.GetFullLocalPath()) {
			return errors.New("媒体目录包含同步目录的根目录，禁止整目录删除")
		}
	}
	confirmed := false
	for _, ref := range target.Owners {
		ownerIndex := slices.IndexFunc(plan.Input.Owners, func(owner EmbyDeletionOwner) bool { return embyOwnerRef(owner) == ref })
		if ownerIndex < 0 {
			return ErrEmbyIdentityAmbiguous
		}
		owner := plan.Input.Owners[ownerIndex]
		var state EmbyItemState
		if err := conn.Where("server_id = ? AND item_id = ?", plan.Input.ServerID, ref.ItemID).First(&state).Error; err != nil {
			return err
		}
		if state.SnapshotID != ref.SnapshotID || state.Generation != ref.Generation {
			return ErrEmbySnapshotStale
		}
		var evidence EmbyItemEvidence
		if err := conn.First(&evidence, ref.SnapshotID).Error; err != nil {
			return err
		}
		if evidence != owner.Evidence || evidence.ServerID != plan.Input.ServerID || evidence.ServerConfigKey != plan.Input.ServerConfigKey || evidence.ItemID != ref.ItemID || evidence.Generation != ref.Generation || embyJSON(owner.Files) != evidence.FilesJSON {
			return ErrEmbyIdentityAmbiguous
		}
		metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(metadata.DirectoryScopes, func(known EmbyDirectoryScope) bool { return embyJSON(known) == embyJSON(scope) }) {
			confirmed = true
		}
	}
	if !confirmed {
		return ErrEmbyIdentityAmbiguous
	}
	return embyCheckDirectoryRetainers(ctx, plan.Input, target)
}

// 同时匹配标准绝对路径和历史根相对路径；LIKE 的特殊字符始终转义。
func embyDirectorySubtreeQuery(conn *gorm.DB, scope EmbyDirectoryScope) *gorm.DB {
	full, valid := embyRemoteDirectory(embyDirectoryFullPath(scope))
	if !valid {
		return conn.Where("1 = 0")
	}
	relative := strings.TrimPrefix(full, "/")
	escape := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_")
	return conn.Where("f.source_type = ? AND f.account_id = ?", scope.Root.SourceType, scope.Root.AccountID).
		Where("(f.path = ? OR f.path LIKE ? ESCAPE '!' OR f.path = ? OR f.path LIKE ? ESCAPE '!')", full, escape.Replace(full)+"/%", relative, escape.Replace(relative)+"/%")
}

// ListEmbyDirectoryLedgerFiles 一次读取实际子树账本，不请求陌生文件的归属。
func ListEmbyDirectoryLedgerFiles(ctx context.Context, scope EmbyDirectoryScope) ([]SyncFile, error) {
	if !validEmbyDirectoryScope(scope) {
		return nil, ErrEmbyIdentityAmbiguous
	}
	var files []SyncFile
	err := embyDirectorySubtreeQuery(db.Db.WithContext(ctx).Table("sync_files AS f"), scope).Select("f.*").Find(&files).Error
	return files, err
}

type embyDirectoryUser struct {
	SyncFile          `gorm:"embedded"`
	UserItemID        string
	UserServerID      string
	UserConfigKey     string
	UserSnapshotID    uint
	UserGeneration    int64
	CurrentSnapshotID uint
	CurrentGeneration int64
	OwnerDeleted      bool
	LiveSnapshotID    uint
	LiveGeneration    int64
	LiveServerID      string
}

func embyCheckDirectoryRetainers(ctx context.Context, input EmbyDeletionInput, target EmbyDeletionTarget) error {
	var users []embyDirectoryUser
	query := db.Db.WithContext(ctx).Table("sync_files AS f").
		Select("f.*, e.item_id AS user_item_id, e.server_id AS user_server_id, e.server_config_key AS user_config_key, r.snapshot_id AS user_snapshot_id, e.generation AS user_generation, s.snapshot_id AS current_snapshot_id, s.generation AS current_generation, s.deleted AS owner_deleted, i.snapshot_id AS live_snapshot_id, i.generation AS live_generation, i.server_id AS live_server_id").
		Joins("JOIN emby_media_sync_files AS r ON r.sync_file_id = f.id").
		Joins("LEFT JOIN emby_media_items AS i ON i.item_id_int = r.emby_item_id").
		Joins("LEFT JOIN emby_item_evidences AS e ON e.id = r.snapshot_id").
		Joins("LEFT JOIN emby_item_states AS s ON s.server_id = e.server_id AND s.item_id = e.item_id")
	if err := embyDirectorySubtreeQuery(query, *target.Directory).Scan(&users).Error; err != nil {
		return err
	}
	var pendingKeys []string
	for _, user := range users {
		ref := EmbyDeletionOwnerRef{ItemID: user.UserItemID, SnapshotID: user.UserSnapshotID, Generation: user.UserGeneration}
		current := user.UserServerID == input.ServerID && user.UserConfigKey == input.ServerConfigKey && user.UserSnapshotID != 0 && user.CurrentSnapshotID == user.UserSnapshotID && user.CurrentGeneration == user.UserGeneration &&
			(user.LiveSnapshotID == 0 || user.LiveSnapshotID == user.UserSnapshotID && user.LiveGeneration == user.UserGeneration && user.LiveServerID == input.ServerID)
		if current && slices.Contains(target.Owners, ref) {
			continue
		}
		// 仅被标记删除的同代际旧关联可以借用持久结果；新的使用者始终保留。
		if !current || !user.OwnerDeleted {
			return errors.New("媒体目录仍被其他 Emby 条目使用")
		}
		file := EmbyFrozenFile{SourceType: user.SourceType, AccountID: user.AccountId, AccountIdentity: target.File.AccountIdentity, FileID: user.FileId, PickCode: sanitizeEmbyEvidencePath(user.PickCode), Path: user.Path, FileName: user.FileName, SHA1: user.Sha1, OpenlistObjectID: user.OpenlistObjectId, OpenlistSHA1: user.OpenlistSHA1, OpenlistMD5: user.OpenlistMD5, FileSize: user.FileSize, MTime: user.MTime}
		key := EmbyDeletionFileKey(file)
		if !slices.Contains(pendingKeys, key) {
			pendingKeys = append(pendingKeys, key)
		}
	}
	if len(pendingKeys) == 0 {
		return nil
	}
	var successes []string
	for start := 0; start < len(pendingKeys); start += 500 {
		var batch []string
		end := min(start+500, len(pendingKeys))
		if err := db.Db.WithContext(ctx).Model(&EmbyWebhookTarget{}).
			Where("server_id = ? AND server_config_key = ? AND target_key IN ? AND outcome IN ?", input.ServerID, input.ServerConfigKey, pendingKeys[start:end], []EmbyDeletionOutcome{EmbyDeletionDeleted, EmbyDeletionAlreadyAbsent}).
			Distinct("target_key").Pluck("target_key", &batch).Error; err != nil {
			return err
		}
		successes = append(successes, batch...)
	}
	for _, key := range pendingKeys {
		if !slices.Contains(successes, key) {
			return errors.New("媒体目录中的其他成员尚未确认删除")
		}
	}
	return nil
}

// ExecuteEmbyDeletionDirectory 在稳定原目录上执行一次递归请求，并只确认原目录 ID。
// CoveredKeys 由持久调用方关联到本操作，不代表逐个查证历史文件在任何位置都不存在。
func ExecuteEmbyDeletionDirectory(ctx context.Context, plan EmbyDeletionPlan, target EmbyDeletionTarget, provider EmbyDeleteProvider, verify EmbyDeletionVerifier, beforeSend func() error, recovering ...bool) EmbyDeletionResult {
	ctx = WithEmbyDirectoryProvider(ctx, provider)
	result := EmbyDeletionResult{Key: target.Key, Outcome: EmbyDeletionUnresolved}
	fail := func(outcome EmbyDeletionOutcome, err error) EmbyDeletionResult {
		result.Outcome, result.Reason = outcome, redactEmbySnapshotError(err.Error())
		return result
	}
	directoryProvider, supported := provider.(EmbyDeleteDirectoryProvider)
	if !supported || !directoryProvider.SupportsDirectoryDelete() || verify == nil || beforeSend == nil || target.Directory == nil {
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
	if err := validateEmbyDirectoryTarget(ctx, plan, target); err != nil {
		return fail(EmbyDeletionUnresolved, err)
	}
	if err := registerEmbyDeletionTargets(ctx, plan, []EmbyDeletionTarget{target}); err != nil {
		return fail(EmbyDeletionUnresolved, err)
	}
	current, preparedAt, err := embyPrepareDirectory(ctx, *target.Directory, provider)
	absent := errors.Is(err, ErrEmbyRemoteFileAbsent)
	if err != nil && !absent {
		if errors.Is(err, ErrEmbyIdentityAmbiguous) || errors.Is(err, ErrEmbyDeleteUnverified) {
			return fail(EmbyDeletionUnresolved, err)
		}
		return fail(EmbyDeletionFailed, err)
	}
	verificationTarget := target
	if absent {
		// 原目录已明确缺失时只核验原条目／STRM 等保护，不要求读取不存在的目录。
		verificationTarget.Directory = nil
	}
	if err := verify(ctx, plan.Input, verificationTarget); err != nil {
		return fail(EmbyDeletionUnresolved, err)
	}
	if absent {
		result.Outcome, result.Reason = EmbyDeletionAlreadyAbsent, "original_directory_id_absent"
		return result
	}
	if !embyDirectoryIdentityMatches(*target.Directory, current) {
		return fail(EmbyDeletionUnresolved, errors.New("原媒体目录身份或位置已变化"))
	}
	if err := embyVerifyCompletedDirectoryMembers(ctx, plan.Input, target, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }); err != nil {
		return fail(EmbyDeletionUnresolved, err)
	}
	if len(recovering) > 0 && recovering[0] {
		for _, member := range plan.Targets {
			if !slices.Contains(target.CoveredKeys, member.Key) {
				continue
			}
			remote, err := provider.Stat(ctx, member.File)
			if errors.Is(err, ErrEmbyRemoteFileAbsent) {
				continue
			}
			if err != nil || !embyRemoteMatches(member.File, remote) {
				return fail(EmbyDeletionUnresolved, errors.New("未完成目录的已知成员身份或位置尚未确认"))
			}
		}
	}
	ok, err := directoryProvider.DeleteDirectory(ctx, *target.Directory, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateEmbyDirectoryTarget(ctx, plan, target); err != nil {
			return err
		}
		finalCtx := context.WithValue(ctx, embyDeletionFinalGuardKey{}, true)
		if err := verify(finalCtx, plan.Input, target); err != nil {
			return err
		}
		// guard 可能在同一个供应商队列内执行，不能再次排入云端读取。
		if time.Since(preparedAt) > 30*time.Second {
			return ErrEmbyDeleteUnverified
		}
		if err := validateEmbyDeletionBarrier(ctx, plan.Input, []EmbyDeletionTarget{target}); err != nil {
			return err
		}
		embyForgetDirectoryRead(ctx, *target.Directory)
		return beforeSend()
	})
	if err != nil {
		if errors.Is(err, ErrEmbyDeletionReappeared) {
			return fail(EmbyDeletionUnresolved, err)
		}
		return fail(EmbyDeletionFailed, err)
	}
	if !ok {
		return fail(EmbyDeletionFailed, errors.New("网盘未确认媒体目录删除成功"))
	}
	_, err = directoryProvider.StatDirectory(ctx, *target.Directory)
	if !errors.Is(err, ErrEmbyRemoteFileAbsent) {
		if err == nil {
			err = errors.New("网盘原目录仍存在，删除结果待确认")
		}
		return fail(EmbyDeletionFailed, err)
	}
	result.Outcome, result.Reason = EmbyDeletionDeleted, "confirmed_directory_operation"
	return result
}

func embyDirectoryIdentityMatches(scope EmbyDirectoryScope, remote EmbyRemoteFile) bool {
	return remote.IsDir && remote.FileID == scope.Root.FileID && remote.ParentID == scope.Root.ParentID && remote.FileName == scope.Root.FileName && embyRemoteDirectoriesMatch(scope.Root.SourceType, remote.Path, scope.Root.Path)
}

// ValidateEmbyDirectoryTarget 供持久执行层在恢复前复核原授权与当前保留成员。
func ValidateEmbyDirectoryTarget(ctx context.Context, plan EmbyDeletionPlan, target EmbyDeletionTarget) error {
	if target.Directory == nil {
		return fmt.Errorf("缺少媒体目录身份: %w", ErrEmbyDeleteUnverified)
	}
	return validateEmbyDirectoryTarget(ctx, plan, target)
}

// 持久成功只排除旧关联；原对象再出现时仍须保留，不把成功当成永久缺失。
func embyVerifyCompletedDirectoryMembers(ctx context.Context, input EmbyDeletionInput, target EmbyDeletionTarget, factory EmbyDeleteProviderFactory) error {
	files, err := ListEmbyDirectoryLedgerFiles(ctx, *target.Directory)
	if err != nil {
		return err
	}
	byKey := make(map[string]EmbyFrozenFile, len(files))
	keys := make([]string, 0, len(files))
	for _, file := range files {
		frozen := EmbyFrozenFile{SourceType: file.SourceType, AccountID: file.AccountId, AccountIdentity: target.File.AccountIdentity, FileID: file.FileId, ParentID: file.ParentId, PickCode: sanitizeEmbyEvidencePath(file.PickCode), Path: file.Path, FileName: file.FileName, SHA1: file.Sha1, OpenlistObjectID: file.OpenlistObjectId, OpenlistSHA1: file.OpenlistSHA1, OpenlistMD5: file.OpenlistMD5, FileSize: file.FileSize, MTime: file.MTime}
		key := EmbyDeletionFileKey(frozen)
		if _, exists := byKey[key]; !exists {
			byKey[key] = frozen
			keys = append(keys, key)
		}
	}
	for start := 0; start < len(keys); start += 500 {
		end := min(start+500, len(keys))
		var successes []string
		if err := db.Db.WithContext(ctx).Model(&EmbyWebhookTarget{}).
			Where("server_id = ? AND server_config_key = ? AND target_key IN ? AND outcome IN ?", input.ServerID, input.ServerConfigKey, keys[start:end], []EmbyDeletionOutcome{EmbyDeletionDeleted, EmbyDeletionAlreadyAbsent}).
			Distinct("target_key").Pluck("target_key", &successes).Error; err != nil {
			return err
		}
		for _, key := range successes {
			provider, err := factory(byKey[key])
			if err != nil {
				return err
			}
			if _, err := provider.Stat(ctx, byKey[key]); !errors.Is(err, ErrEmbyRemoteFileAbsent) {
				return errors.New("曾完成删除的原成员尚不能确认缺失，保留媒体目录")
			}
		}
	}
	return nil
}

// AddEmbyScopedMetadataTargets 加入独立证明归属的季级元数据，不靠文件名扩权。
func AddEmbyScopedMetadataTargets(plan *EmbyDeletionPlan) {
	if plan == nil || !plan.Input.CleanupPolicy.AllowsJointBatch() {
		return
	}
	for _, scoped := range plan.Input.ScopedFiles {
		if !embyScopedFileAuthorized(plan.Input, scoped) || scoped.File.Reason != "" {
			continue
		}
		key := EmbyDeletionFileKey(scoped.File)
		if slices.ContainsFunc(plan.Targets, func(target EmbyDeletionTarget) bool { return target.Key == key }) {
			continue
		}
		target := EmbyDeletionTarget{Key: key, Kind: "scoped_metadata", File: scoped.File}
		for _, owner := range plan.Input.Owners {
			metadata, err := DecodeEmbyMetadata(owner.Evidence.SidecarsJSON)
			if err != nil || !slices.Contains(metadata.ScopedFiles, scoped) {
				continue
			}
			if slices.ContainsFunc(metadata.DirectoryScopes, func(scope EmbyDirectoryScope) bool {
				return scope.MediaType == scoped.ScopeType && scope.ItemID == scoped.ScopeItemID && EmbyDirectoryScopeKey(scope) == scoped.ScopeDirectoryKey
			}) {
				target.Owners = append(target.Owners, embyOwnerRef(owner))
			}
		}
		if len(target.Owners) != 0 {
			plan.Targets = append(plan.Targets, target)
		}
	}
}

// 其余账号、当前账本、根与物理共享校验由 validateEmbyDeletionTarget 统一执行。
func validateEmbyScopedDeletionTarget(ctx context.Context, plan EmbyDeletionPlan, target EmbyDeletionTarget) error {
	if !plan.Input.CleanupPolicy.AllowsJointBatch() || target.Kind != "scoped_metadata" || len(target.Owners) == 0 {
		return ErrEmbyDeleteUnverified
	}
	matching := false
	var seasonIDs []string
	for _, scoped := range plan.Input.ScopedFiles {
		if scoped.File != target.File || !embyScopedFileAuthorized(plan.Input, scoped) {
			continue
		}
		matching = true
		if !slices.Contains(seasonIDs, scoped.ScopeItemID) {
			seasonIDs = append(seasonIDs, scoped.ScopeItemID)
		}
		for _, ref := range target.Owners {
			found := false
			for _, owner := range plan.Input.Owners {
				if embyOwnerRef(owner) != ref || owner.Item.SeasonId != scoped.ScopeItemID {
					continue
				}
				metadata, err := DecodeEmbyMetadata(owner.Evidence.SidecarsJSON)
				if err != nil {
					return err
				}
				if slices.Contains(metadata.ScopedFiles, scoped) && slices.ContainsFunc(metadata.DirectoryScopes, func(scope EmbyDirectoryScope) bool {
					return scope.MediaType == scoped.ScopeType && scope.ItemID == scoped.ScopeItemID && EmbyDirectoryScopeKey(scope) == scoped.ScopeDirectoryKey
				}) {
					found = true
				}
			}
			if !found {
				return ErrEmbyIdentityAmbiguous
			}
		}
	}
	if !matching {
		return ErrEmbyIdentityAmbiguous
	}
	var users []EmbyMediaItem
	if err := db.Db.WithContext(ctx).Where("server_id = ? AND season_id IN ?", plan.Input.ServerID, seasonIDs).Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		if !slices.Contains(target.Owners, EmbyDeletionOwnerRef{ItemID: user.ItemId, SnapshotID: user.SnapshotID, Generation: user.Generation}) {
			return errors.New("本季元数据仍被保留成员使用")
		}
	}
	return nil
}

func embyScopedFileAuthorized(input EmbyDeletionInput, scoped EmbyScopedFile) bool {
	if scoped.ScopeType != "Season" {
		return false
	}
	if input.ItemType == "Season" {
		return scoped.ScopeItemID == input.ItemID
	}
	if input.ItemType != "Series" {
		return false
	}
	return slices.ContainsFunc(input.Owners, func(owner EmbyDeletionOwner) bool {
		return owner.Item.SeriesId == input.ItemID && owner.Item.SeasonId == scoped.ScopeItemID
	})
}

// 配置路径可能落后于外部移动；除当前父链已证明的根外，按受保护原 ID 核位。
func embyVerifyDirectoryRootLocations(ctx context.Context, scope EmbyDirectoryScope, provider EmbyDeleteProvider, preparedAt *time.Time) error {
	var roots []SyncPath
	if err := db.Db.WithContext(ctx).Where("source_type = ? AND account_id = ?", scope.Root.SourceType, scope.Root.AccountID).Find(&roots).Error; err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, ancestor := range scope.Ancestors {
		seen[ancestor.FileID] = true
	}
	locator, supported := provider.(EmbyDirectoryLocator)
	for _, root := range roots {
		// 百度 BaseCid 是配置路径；当前祖先详情已验证本绑定根的实际 fsid。
		if scope.Root.SourceType == SourceTypeBaiduPan && root.ID == scope.Root.SyncPathID && slices.ContainsFunc(scope.Ancestors, func(ancestor EmbyDirectoryAncestor) bool {
			return embyRemoteDirectoriesMatch(root.SourceType, ancestor.Path, root.RemotePath)
		}) {
			continue
		}
		if root.BaseCid != "" && seen[root.BaseCid] {
			continue
		}
		if root.BaseCid == "" || !supported {
			return ErrEmbyDeleteUnverified
		}
		current, observedAt, err := embyDirectoryCachedRead(ctx, "protected:"+embyDigest([]any{scope.Root.SourceType, scope.Root.AccountID, scope.Root.AccountIdentity, root.BaseCid}), func() (EmbyRemoteFile, error) { return locator.LocateDirectory(ctx, root.BaseCid) })
		if observedAt.Before(*preparedAt) {
			*preparedAt = observedAt
		}
		if err != nil {
			return err
		}
		if !current.IsDir || current.FileID != root.BaseCid || !embySafeName(current.FileName) {
			return ErrEmbyIdentityAmbiguous
		}
		full := path.Join(current.Path, current.FileName)
		if _, valid := embyRemoteDirectory(full); !valid {
			return ErrEmbyIdentityAmbiguous
		}
		if embyRemotePathWithin(scope.Root.SourceType, embyDirectoryFullPath(scope), full) {
			return errors.New("其他同步目录的根目录位于该媒体目录内，禁止整目录删除")
		}
		seen[root.BaseCid] = true
	}
	return nil
}

type embyDirectoryChecksKey struct{}

type embyDirectoryRead struct {
	file EmbyRemoteFile
	err  error
	at   time.Time
}

type embyDirectoryChecks struct {
	mu                sync.Mutex
	active            bool
	reads             map[string]embyDirectoryRead
	listings          map[embyDeletionListingKey]embyDeletionListing
	listingGeneration uint64
}

// 仅一次已持有 syncscope 的执行可复用；释放范围立即使所有云端边界读取失效。
func beginEmbyDirectoryChecks(ctx context.Context) (context.Context, func()) {
	checks := &embyDirectoryChecks{active: true, reads: map[string]embyDirectoryRead{}, listings: map[embyDeletionListingKey]embyDeletionListing{}}
	return context.WithValue(ctx, embyDirectoryChecksKey{}, checks), func() {
		checks.mu.Lock()
		defer checks.mu.Unlock()
		checks.active = false
		clear(checks.reads)
		clear(checks.listings)
	}
}

func embyDirectoryCachedRead(ctx context.Context, key string, read func() (EmbyRemoteFile, error)) (EmbyRemoteFile, time.Time, error) {
	checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks)
	if checks != nil {
		checks.mu.Lock()
		cached, exists := checks.reads[key]
		active := checks.active
		checks.mu.Unlock()
		if !active {
			return EmbyRemoteFile{}, time.Time{}, ErrEmbyDeleteUnverified
		}
		if exists && time.Since(cached.at) < 30*time.Second {
			return cached.file, cached.at, cached.err
		}
	}
	at := time.Now()
	file, err := read()
	if checks != nil && (err == nil || errors.Is(err, ErrEmbyRemoteFileAbsent)) {
		checks.mu.Lock()
		if checks.active {
			checks.reads[key] = embyDirectoryRead{file: file, err: err, at: at}
		}
		checks.mu.Unlock()
	}
	return file, at, err
}

func embyPrepareDirectory(ctx context.Context, scope EmbyDirectoryScope, provider EmbyDeleteProvider) (EmbyRemoteFile, time.Time, error) {
	directory, supported := provider.(EmbyDeleteDirectoryProvider)
	if !supported {
		return EmbyRemoteFile{}, time.Time{}, ErrEmbyDeleteUnsupported
	}
	remote, at, err := embyDirectoryCachedRead(ctx, "root:"+EmbyDirectoryScopeKey(scope), func() (EmbyRemoteFile, error) { return directory.StatDirectory(ctx, scope) })
	if err != nil {
		return remote, at, err
	}
	if !embyDirectoryIdentityMatches(scope, remote) {
		return remote, at, ErrEmbyIdentityAmbiguous
	}
	if err := embyVerifyDirectoryRootLocations(ctx, scope, provider, &at); err != nil {
		return remote, at, err
	}
	return remote, at, nil
}

func embyForgetDirectoryRead(ctx context.Context, scope EmbyDirectoryScope) {
	if checks, _ := ctx.Value(embyDirectoryChecksKey{}).(*embyDirectoryChecks); checks != nil {
		checks.mu.Lock()
		defer checks.mu.Unlock()
		delete(checks.reads, "root:"+EmbyDirectoryScopeKey(scope))
	}
}
