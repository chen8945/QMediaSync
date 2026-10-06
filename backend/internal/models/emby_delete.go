package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/v115open"
)

var (
	ErrEmbyRemoteFileAbsent   = errors.New("远端已确认不存在原文件")
	ErrEmbyDeleteUnverified   = errors.New("Emby 删除等待身份或原条目核验")
	ErrEmbyDeleteUnsupported  = errors.New("该来源不支持 Emby 联动删除")
	ErrEmbyDeleteDisabled     = errors.New("Emby 联动删除未获授权")
	ErrEmbyDeletionReappeared = errors.New("该文件此前已确认删除，同一位置又出现同名文件；为防误删已保留，等待再次核验")
)

// EmbyRemoteFile 是完整目录清单中的文件；Path 是目录，不能包含 FileName。
type EmbyRemoteFile struct {
	FileID           string `json:"file_id"`
	ParentID         string `json:"parent_id"`
	FileName         string `json:"file_name"`
	Path             string `json:"path"`
	PickCode         string `json:"pick_code"`
	SHA1             string `json:"sha1"`
	OpenlistObjectID string `json:"openlist_object_id"`
	OpenlistSHA1     string `json:"openlist_sha1"`
	OpenlistMD5      string `json:"openlist_md5"`
	FileSize         int64  `json:"file_size"`
	MTime            int64  `json:"mtime"`
	FileCreatedAt    int64  `json:"file_created_at"`
	IsDir            bool   `json:"is_dir"`
}

// EmbyDeleteProvider 只处理单个冻结文件，List 必须返回完整新鲜目录清单。
// Delete 必须在最后一次远端读取后调用非 nil guard，再发送一次不自动重放的删除请求。
type EmbyDeleteProvider interface {
	Stat(context.Context, EmbyFrozenFile) (EmbyRemoteFile, error)
	List(context.Context, EmbyFrozenFile) ([]EmbyRemoteFile, error)
	Delete(context.Context, EmbyFrozenFile, func() error) (bool, error)
}

type EmbyDeleteProviderFactory func(EmbyFrozenFile) (EmbyDeleteProvider, error)

// EmbyDeletionOwner 保存接收时的不可变快照以及只属于此快照的当前关联。
type EmbyDeletionOwner struct {
	Item     EmbyMediaItem       `json:"item"`
	Evidence EmbyItemEvidence    `json:"evidence"`
	Files    []EmbyFrozenFile    `json:"files"`
	Links    []EmbyMediaSyncFile `json:"links"`
}

// EmbyDeletionInput 必须在收件事务中冻结，不能在重试时重新按路径建立身份。
type EmbyDeletionInput struct {
	CleanupPolicy   EmbyCleanupPolicy    `json:"cleanup_policy,omitzero"`
	DirectoryScopes []EmbyDirectoryScope `json:"directory_scopes,omitempty"`
	ScopedFiles     []EmbyScopedFile     `json:"scoped_files,omitempty"`
	ServerID        string               `json:"server_id"`
	ServerConfigKey string               `json:"server_config_key"`
	ItemID          string               `json:"item_id"`
	ItemType        string               `json:"item_type"`
	Authorized      bool                 `json:"authorized"`
	Owners          []EmbyDeletionOwner  `json:"owners"`
	Sidecars        []EmbyFrozenFile     `json:"sidecars,omitempty"`
	DirectoryVideos []EmbyFrozenFile     `json:"directory_videos,omitempty"`
	CandidateKeys   []string             `json:"candidate_keys,omitempty"`
	Issues          []string             `json:"issues,omitempty"`
}

type EmbyDeletionOwnerRef struct {
	ItemID     string `json:"item_id"`
	SnapshotID uint   `json:"snapshot_id"`
	Generation int64  `json:"generation"`
}

// EmbyDeletionTarget 固定物理目标及其视频使用者，完成部分删除后也不能缩小使用者清单。
type EmbyDeletionTarget struct {
	ScopedMetadata  []EmbyFrozenFile       `json:"-"`
	Directory       *EmbyDirectoryScope    `json:"directory,omitempty"`
	CoveredKeys     []string               `json:"covered_keys,omitempty"`
	CoveredBy       string                 `json:"covered_by,omitempty"`
	Key             string                 `json:"key"`
	Kind            string                 `json:"kind"`
	File            EmbyFrozenFile         `json:"file"`
	Owners          []EmbyDeletionOwnerRef `json:"owners"`
	DirectoryVideos []EmbyRemoteFile       `json:"directory_videos,omitempty"`
	Reason          string                 `json:"reason,omitempty"`
}

type EmbyDeletionPlan struct {
	Input   EmbyDeletionInput    `json:"input"`
	Targets []EmbyDeletionTarget `json:"targets"`
	Issues  []string             `json:"issues,omitempty"`
}

type EmbyDeletionOutcome string

const (
	EmbyDeletionDeleted       EmbyDeletionOutcome = "deleted"
	EmbyDeletionAlreadyAbsent EmbyDeletionOutcome = "already_absent"
	EmbyDeletionUnresolved    EmbyDeletionOutcome = "unresolved"
	EmbyDeletionFailed        EmbyDeletionOutcome = "failed"
)

type EmbyDeletionResult struct {
	Key     string              `json:"key"`
	Outcome EmbyDeletionOutcome `json:"outcome"`
	Reason  string              `json:"reason,omitempty"`
}

// EmbyDeletionVerifier 由事件处理层在实际执行前核验服务器、原条目/STRM 和存活使用者。
// nil 不授权删除；返回 nil 才表示这些独立证据均已通过。
type EmbyDeletionVerifier func(context.Context, EmbyDeletionInput, EmbyDeletionTarget) error

// CaptureEmbyDeletionTx 与收件和 RegisterEmbyDeletionTx 使用同一事务；这里只冻结证据，不调用远端。
func CaptureEmbyDeletionTx(tx *gorm.DB, serverID, itemID, itemType string) (EmbyDeletionInput, error) {
	return captureEmbyDeletionTx(tx, serverID, itemID, itemType, nil)
}

func captureEmbyDeletionTx(tx *gorm.DB, serverID, itemID, itemType string, excluded map[string]bool) (EmbyDeletionInput, error) {
	input := EmbyDeletionInput{ServerID: serverID, ItemID: itemID, ItemType: itemType, CleanupPolicy: CurrentEmbyCleanupPolicy()}
	if _, err := parseEmbyItemID(itemID); err != nil {
		return input, err
	}
	switch itemType {
	case "Movie", "Episode", "Video", "Season", "Series":
	default:
		return input, ErrEmbyDeleteUnsupported
	}
	state, err := lockEmbyIndexState(tx)
	if err != nil {
		return input, err
	}
	var config EmbyConfig
	if err := tx.First(&config).Error; err != nil {
		return input, err
	}
	input.ServerConfigKey = EmbyServerConfigIdentity(&config)
	input.Authorized = config.SyncEnabled == 1 && config.EnableDeleteNetdisk == 1
	if serverID == "" || serverID != state.ServerID || state.ServerConfigKey != input.ServerConfigKey {
		return input, ErrEmbyIdentityAmbiguous
	}
	// 当前索引可能已由完整扫描清理；保留的 state→evidence 仍是唯一可用的历史身份。
	evidence, err := loadEmbyScopeEvidenceTx(tx, serverID, input.ServerConfigKey, itemID, itemType, excluded)
	if err != nil {
		return input, err
	}
	owners := make([]EmbyDeletionOwner, 0, len(evidence))
	selected := map[string]bool{}
	for _, entry := range evidence {
		// 同次收件已确认移出的条目不能由旧状态重新选入，也不带入其旁车或目录证据。
		if excluded[entry.ItemID] {
			continue
		}
		owner := EmbyDeletionOwner{Evidence: entry}
		if err := json.Unmarshal([]byte(entry.ItemJSON), &owner.Item); err != nil {
			return input, fmt.Errorf("解析 Emby 历史条目失败，ItemId %s: %w", entry.ItemID, err)
		}
		if err := json.Unmarshal([]byte(entry.FilesJSON), &owner.Files); err != nil {
			return input, fmt.Errorf("解析 Emby 历史文件 %s: %w", entry.ItemID, err)
		}
		owner.Item.SnapshotID = entry.ID
		match := owner.Item.ItemId == itemID && owner.Item.Type == itemType
		if itemType == "Season" {
			match = owner.Item.SeasonId == itemID && (owner.Item.Type == "Episode" || owner.Item.Type == "Video")
		}
		if itemType == "Series" {
			match = owner.Item.SeriesId == itemID && (owner.Item.Type == "Episode" || owner.Item.Type == "Video")
		}
		if match {
			selected[owner.Item.ItemId] = true
		}
		owners = append(owners, owner)
	}
	for _, owner := range owners {
		if owner.Item.Type == "Video" && owner.Item.PartOfItemID != "" &&
			(itemType == "Movie" && selected[itemID] && owner.Item.PartOfItemID == itemID || (itemType == "Season" || itemType == "Series") && selected[owner.Item.PartOfItemID]) {
			selected[owner.Item.ItemId] = true
		}
	}
	for _, owner := range owners {
		if !selected[owner.Item.ItemId] {
			continue
		}
		if err := tx.Where("emby_item_id = ? AND snapshot_id = ?", owner.Item.ItemIdInt, owner.Evidence.ID).Find(&owner.Links).Error; err != nil {
			return input, err
		}
		for i := range owner.Links {
			owner.Links[i].PickCode = sanitizeEmbyEvidencePath(owner.Links[i].PickCode)
		}
		input.Owners = append(input.Owners, owner)
		metadata, err := DecodeEmbyMetadata(owner.Evidence.SidecarsJSON)
		if err != nil {
			return input, err
		}
		for _, scoped := range metadata.ScopedFiles {
			if scoped.ScopeType == itemType && scoped.ScopeItemID == itemID || itemType == "Series" && scoped.ScopeType == "Season" && owner.Item.SeriesId == itemID && owner.Item.SeasonId == scoped.ScopeItemID {
				if !slices.Contains(input.ScopedFiles, scoped) {
					input.ScopedFiles = append(input.ScopedFiles, scoped)
				}
			}
		}
		for _, sidecar := range metadata.ExclusiveFiles {
			if !slices.Contains(input.Sidecars, sidecar) {
				input.Sidecars = append(input.Sidecars, sidecar)
			}
		}
	}
	if len(input.Owners) == 0 {
		input.Issues = append(input.Issues, "no_confirmed_item_evidence")
	}
	dirs := map[string]bool{}
	for _, owner := range input.Owners {
		for _, file := range owner.Files {
			key := embyDigest([]any{file.SourceType, file.AccountID, file.Path})
			if dirs[key] {
				continue
			}
			dirs[key] = true
			var siblings []SyncFile
			if err := tx.Where("source_type = ? AND account_id = ? AND path = ?", file.SourceType, file.AccountID, file.Path).Find(&siblings).Error; err != nil {
				return input, err
			}
			for _, sibling := range siblings {
				if !sibling.IsVideo && !sibling.IsMeta {
					continue
				}
				frozen, err := freezeEmbyFile(tx, sibling, "", sibling.LocalFilePath)
				if err != nil {
					return input, err
				}
				if sibling.IsVideo {
					input.DirectoryVideos = append(input.DirectoryVideos, frozen)
				}
			}
		}
	}
	input.DirectoryScopes, err = CaptureEmbyDirectoryScopesTx(tx, input)
	if err != nil {
		return input, err
	}
	return input, nil
}

// EmbyDeletionFileKey 将同账号、同物理代际去重，不依赖重复同步目录的账本行号。
func EmbyDeletionFileKey(file EmbyFrozenFile) string {
	return embyDigest([]any{file.SourceType, file.AccountID, file.AccountIdentity, file.FileID, file.PickCode, file.Path, file.FileName,
		file.SHA1, file.OpenlistObjectID, file.OpenlistSHA1, file.OpenlistMD5, file.FileSize, file.MTime})
}

// EmbyDeletionTargetKey 区分目录操作与原物理文件代际。
func EmbyDeletionTargetKey(target EmbyDeletionTarget) string {
	if target.Kind == "directory" && target.Directory != nil {
		return "directory:" + EmbyDirectoryScopeKey(*target.Directory)
	}
	return EmbyDeletionFileKey(target.File)
}

func embyOwnerRef(owner EmbyDeletionOwner) EmbyDeletionOwnerRef {
	return EmbyDeletionOwnerRef{ItemID: owner.Item.ItemId, SnapshotID: owner.Evidence.ID, Generation: owner.Evidence.Generation}
}

// BuildEmbyDeletionPlan 只补充已确认视频的专属旁车；调用方必须在执行前持久保存整个计划。
func BuildEmbyDeletionPlan(ctx context.Context, input EmbyDeletionInput, factory EmbyDeleteProviderFactory, verify ...EmbyDeletionVerifier) (EmbyDeletionPlan, error) {
	plan := EmbyDeletionPlan{Input: input, Issues: slices.Clone(input.Issues)}
	if !input.CleanupPolicy.AllowsJointBatch() {
		return plan, ErrEmbyDeleteUnsupported
	}
	if err := ValidateEmbyDeletionInputMetadata(input); err != nil {
		return plan, err
	}
	if factory == nil {
		factory = NewEmbyDeleteProvider
	}
	byKey := map[string]int{}
	for _, owner := range input.Owners {
		if len(owner.Files) == 0 {
			plan.Issues = append(plan.Issues, "no_confirmed_files:"+owner.Item.ItemId)
		}
		for _, file := range owner.Files {
			key := EmbyDeletionFileKey(file)
			if index, ok := byKey[key]; ok {
				if !slices.Contains(plan.Targets[index].Owners, embyOwnerRef(owner)) {
					plan.Targets[index].Owners = append(plan.Targets[index].Owners, embyOwnerRef(owner))
				}
				continue
			}
			byKey[key] = len(plan.Targets)
			plan.Targets = append(plan.Targets, EmbyDeletionTarget{Key: key, Kind: "video", File: file, Owners: []EmbyDeletionOwnerRef{embyOwnerRef(owner)}, Reason: file.Reason})
		}
	}
	AddEmbyScopedMetadataTargets(&plan)
	if input.CleanupPolicy.AllowsDirectoryContent() {
		if err := AddEmbyDirectoryTargets(ctx, &plan, factory, verify...); err != nil {
			return plan, err
		}
	}
	// 已获目录授权的成员无需再逐层列出旁车。文件模式仍核对完整同级清单。
	type directory struct {
		file    EmbyFrozenFile
		targets []int
	}
	dirs := map[string]*directory{}
	for i, target := range plan.Targets {
		if target.Kind != "video" || target.CoveredBy != "" {
			continue
		}
		key := embyDigest([]any{target.File.SourceType, target.File.AccountID, target.File.Path})
		if dirs[key] == nil {
			dirs[key] = &directory{file: target.File}
		}
		dirs[key].targets = append(dirs[key].targets, i)
	}
	keys := make([]string, 0, len(dirs))
	for key := range dirs {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		dir := dirs[key]
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		provider, err := factory(dir.file)
		if err != nil {
			plan.Issues = append(plan.Issues, "sidecar_inventory_unavailable:"+key)
			continue
		}
		listing, err := provider.List(ctx, dir.file)
		if err != nil || !embyValidListing(dir.file.Path, listing) {
			plan.Issues = append(plan.Issues, "sidecar_inventory_unavailable:"+key)
			continue
		}
		videos := embyDirectoryVideos(listing)
		for _, file := range input.DirectoryVideos {
			if file.SourceType == dir.file.SourceType && file.AccountID == dir.file.AccountID && embyRemoteDirectoriesMatch(file.SourceType, file.Path, dir.file.Path) {
				videos = embyMergeVideos(videos, []EmbyRemoteFile{embyRemoteFromFrozen(file)})
			}
		}
		// 原视频可能已被插件移除；冻结的视频仍必须参与旁车归属判断。
		for _, index := range dir.targets {
			videos = embyMergeVideos(videos, []EmbyRemoteFile{embyRemoteFromFrozen(plan.Targets[index].File)})
		}
		for _, remote := range listing {
			if remote.IsDir || !embyKnownSidecar(remote.FileName) {
				continue
			}
			refs, exclusive := embySidecarOwners(remote.FileName, videos, embyDirectoryTargets(plan, dir.file))
			if !exclusive {
				continue
			}
			// 旁车需要删除前的独立历史身份；新鲜路径和当前账本不能补造过去。
			matched, historical := false, false
			for _, frozen := range input.Sidecars {
				if frozen.SourceType != dir.file.SourceType || frozen.AccountID != dir.file.AccountID || !embyRemoteDirectoriesMatch(frozen.SourceType, frozen.Path, dir.file.Path) || frozen.FileName != remote.FileName {
					continue
				}
				historical = true
				if frozen.Reason != "" || !embyRemoteMatches(frozen, remote) {
					continue
				}
				key := EmbyDeletionFileKey(frozen)
				if _, exists := byKey[key]; exists {
					matched = true
					break
				}
				matched = true
				byKey[key] = len(plan.Targets)
				plan.Targets = append(plan.Targets, EmbyDeletionTarget{Key: key, Kind: "sidecar", File: frozen, Owners: refs, DirectoryVideos: videos})
				break
			}
			if !matched {
				reason := "sidecar_history_unconfirmed:"
				if historical {
					reason = "sidecar_identity_changed:"
				}
				issue := reason + key
				if !slices.Contains(plan.Issues, issue) {
					plan.Issues = append(plan.Issues, issue)
				}
			}
		}
	}
	return plan, nil
}

// ConfirmEmbyDeletionPlanInventory 在首次清单失败时，仅按已冻结的原对象 ID 确认缺失。
// 全部已知候选确认完成后才返回可保存的计划；调用方必须将计划与缺失结果原子保存。
func ConfirmEmbyDeletionPlanInventory(ctx context.Context, plan EmbyDeletionPlan, factory EmbyDeleteProviderFactory, verify EmbyDeletionVerifier) (EmbyDeletionPlan, []EmbyDeletionResult, error) {
	resolved := plan
	resolved.Targets, resolved.Issues = slices.Clone(plan.Targets), slices.Clone(plan.Issues)
	var results []EmbyDeletionResult
	for _, issue := range plan.Issues {
		if !strings.HasPrefix(issue, "sidecar_inventory_unavailable:") {
			continue
		}
		if !plan.Input.CleanupPolicy.AllowsJointBatch() || factory == nil || verify == nil {
			return plan, nil, ErrEmbyDeleteUnverified
		}
		inDirectory := func(file EmbyFrozenFile) bool {
			return issue == "sidecar_inventory_unavailable:"+embyDigest([]any{file.SourceType, file.AccountID, file.Path})
		}
		// 历史旁车是候选超集。这里只补齐缺失确认对象，不从它们推导删除权限。
		for _, file := range plan.Input.Sidecars {
			if !inDirectory(file) || slices.ContainsFunc(resolved.Targets, func(target EmbyDeletionTarget) bool { return target.Key == EmbyDeletionFileKey(file) }) {
				continue
			}
			target := EmbyDeletionTarget{Key: EmbyDeletionFileKey(file), Kind: "sidecar", File: file, Reason: file.Reason}
			for _, owner := range plan.Input.Owners {
				metadata, err := DecodeEmbyMetadata(owner.Evidence.SidecarsJSON)
				if err != nil {
					return plan, nil, err
				}
				if slices.Contains(metadata.ExclusiveFiles, file) {
					target.Owners = append(target.Owners, embyOwnerRef(owner))
				}
			}
			resolved.Targets = append(resolved.Targets, target)
		}
		var targets []EmbyDeletionTarget
		for _, target := range resolved.Targets {
			if target.Kind != "directory" && inDirectory(target.File) {
				targets = append(targets, target)
			}
		}
		if len(targets) == 0 {
			return plan, nil, ErrEmbyDeleteUnverified
		}
		for _, target := range targets {
			// OpenList 无法脱离原父目录清单定位原对象，路径缺失不是稳定 ID 缺失。
			if target.File.SourceType != SourceType115 && target.File.SourceType != SourceTypeBaiduPan || embyFrozenPhysicalID(target.File) == "" || target.Reason != "" || target.File.Reason != "" {
				return plan, nil, ErrEmbyDeleteUnverified
			}
			if err := validateEmbyDeletionTarget(ctx, resolved, target); err != nil {
				return plan, nil, err
			}
		}
		if err := validateEmbyDeletionBarrier(ctx, plan.Input, targets); err != nil {
			return plan, nil, err
		}
		if err := verify(ctx, plan.Input, embyDeletionBatchTarget(targets)); err != nil {
			return plan, nil, err
		}
		for _, target := range targets {
			if err := ctx.Err(); err != nil {
				return plan, nil, err
			}
			provider, err := factory(target.File)
			if err != nil {
				return plan, nil, err
			}
			remote, err := provider.Stat(ctx, target.File)
			if !errors.Is(err, ErrEmbyRemoteFileAbsent) {
				if err != nil {
					return plan, nil, err
				}
				if !embyRemoteMatches(target.File, remote) {
					return plan, nil, fmt.Errorf("%w: 原目录不可列举且已知文件身份或位置变化", ErrEmbyDeleteUnverified)
				}
				return plan, nil, fmt.Errorf("%w: NFO/字幕/图片等配套文件的清单暂时无法确认，且原文件仍存在", ErrEmbyDeleteUnverified)
			}
			results = append(results, EmbyDeletionResult{Key: target.Key, Outcome: EmbyDeletionAlreadyAbsent})
		}
		for _, target := range targets {
			if err := validateEmbyDeletionTarget(ctx, resolved, target); err != nil {
				return plan, nil, err
			}
		}
		if err := validateEmbyDeletionBarrier(ctx, plan.Input, targets); err != nil {
			return plan, nil, err
		}
		resolved.Issues = slices.DeleteFunc(resolved.Issues, func(remaining string) bool { return remaining == issue })
	}
	return resolved, results, nil
}

func embyValidListing(directory string, listing []EmbyRemoteFile) bool {
	seen := map[string]bool{}
	for _, file := range listing {
		if file.Path != directory || !embySafeName(file.FileName) || seen[file.FileName] {
			return false
		}
		seen[file.FileName] = true
	}
	return true
}

func embySafeName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func embyRemoteFromFrozen(file EmbyFrozenFile) EmbyRemoteFile {
	pickCode := file.PickCode
	if file.SourceType == SourceTypeBaiduPan {
		pickCode = ""
	}
	return EmbyRemoteFile{FileID: embyFrozenPhysicalID(file), ParentID: file.ParentID, FileName: file.FileName, Path: file.Path, PickCode: pickCode, SHA1: file.SHA1, OpenlistObjectID: file.OpenlistObjectID, OpenlistSHA1: file.OpenlistSHA1, OpenlistMD5: file.OpenlistMD5, FileSize: file.FileSize, MTime: file.MTime}
}

func embyDirectoryVideos(files []EmbyRemoteFile) []EmbyRemoteFile {
	var videos []EmbyRemoteFile
	for _, file := range files {
		// 未知后缀也作为潜在使用者参与判断，避免漏识别的视频借用相同 stem。
		if !file.IsDir && (embyVideoName(file.FileName) || !embyKnownSidecar(file.FileName)) {
			videos = append(videos, file)
		}
	}
	return videos
}

func embyDirectoryTargets(plan EmbyDeletionPlan, file EmbyFrozenFile) []EmbyDeletionTarget {
	var targets []EmbyDeletionTarget
	for _, target := range plan.Targets {
		if target.File.SourceType == file.SourceType && target.File.AccountID == file.AccountID && embyRemoteDirectoriesMatch(file.SourceType, target.File.Path, file.Path) {
			targets = append(targets, target)
		}
	}
	return targets
}

func embyVideoName(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".mkv", ".mp4", ".avi", ".mov", ".wmv", ".m4v", ".ts", ".m2ts", ".mts", ".mpg", ".mpeg", ".vob", ".iso", ".strm", ".rmvb", ".flv", ".webm", ".3gp", ".asf":
		return true
	}
	return false
}

func embyKnownSidecar(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".nfo", ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup", ".jpg", ".jpeg", ".png", ".webp", ".tbn":
		return true
	}
	return false
}

func embySidecarMatches(name, videoName string) bool {
	if embySharedSidecarName(name) {
		return false
	}
	stem := strings.TrimSuffix(videoName, path.Ext(videoName))
	ext := strings.ToLower(path.Ext(name))
	base := strings.TrimSuffix(name, path.Ext(name))
	if base == stem {
		return embyKnownSidecar(name)
	}
	switch ext {
	case ".jpg", ".jpeg", ".png", ".webp", ".tbn":
		for _, suffix := range []string{"-poster", "-fanart", "-thumb", "-banner", "-landscape", "-clearlogo", "-clearart", "-disc"} {
			if base == stem+suffix {
				return true
			}
		}
	case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup":
		// 点边界前缀即宽松候选，不再限定标签；归属由 embySidecarOwnerVideos 按最长视频 stem 收敛。
		return len(base) > len(stem)+1 && strings.HasPrefix(base, stem+".")
	}
	return false
}

func embySharedSidecarName(name string) bool {
	base := strings.ToLower(strings.TrimSuffix(name, path.Ext(name)))
	if strings.EqualFold(name, "tvshow.nfo") || strings.EqualFold(name, "season.nfo") {
		return true
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".tbn":
		switch base {
		case "poster", "folder", "fanart", "banner", "landscape", "thumb", "clearlogo", "clearart", "logo", "backdrop", "disc":
			return true
		}
		if strings.HasPrefix(base, "season") {
			return true
		}
	}
	return false
}

func embyMergeVideos(left, right []EmbyRemoteFile) []EmbyRemoteFile {
	result := slices.Clone(left)
	for _, file := range right {
		if !slices.ContainsFunc(result, func(existing EmbyRemoteFile) bool { return existing == file }) {
			result = append(result, file)
		}
	}
	return result
}

// embySidecarOwnerVideos 在宽松候选中取最长视频 stem 作为归属；并列最长全部保留，避免同 stem 不同容器漏判使用者。
func embySidecarOwnerVideos(name string, videos []EmbyRemoteFile) []EmbyRemoteFile {
	var owners []EmbyRemoteFile
	longest := -1
	for _, video := range videos {
		if !embySidecarMatches(name, video.FileName) {
			continue
		}
		stemLength := len(strings.TrimSuffix(video.FileName, path.Ext(video.FileName)))
		switch {
		case stemLength > longest:
			owners, longest = []EmbyRemoteFile{video}, stemLength
		case stemLength == longest:
			owners = append(owners, video)
		}
	}
	return owners
}

func embySidecarOwners(name string, videos []EmbyRemoteFile, targets []EmbyDeletionTarget) ([]EmbyDeletionOwnerRef, bool) {
	var refs []EmbyDeletionOwnerRef
	for _, video := range embySidecarOwnerVideos(name, videos) {
		found := false
		for _, target := range targets {
			if target.Kind == "video" && target.Reason == "" && embyRemoteMatches(target.File, video) {
				found = true
				for _, ref := range target.Owners {
					if !slices.Contains(refs, ref) {
						refs = append(refs, ref)
					}
				}
			}
		}
		if !found {
			return nil, false
		}
	}
	return refs, len(refs) > 0
}

func embyRemoteMatches(file EmbyFrozenFile, remote EmbyRemoteFile) bool {
	if remote.IsDir || !embyRemoteDirectoriesMatch(file.SourceType, remote.Path, file.Path) || remote.FileName != file.FileName || remote.FileID == "" || remote.FileID != embyFrozenPhysicalID(file) || remote.FileSize != file.FileSize {
		return false
	}
	if file.ParentID != "" && remote.ParentID != file.ParentID {
		return false
	}
	if file.SourceType != SourceTypeBaiduPan && file.SourceType != SourceTypeOpenList && file.PickCode != "" && remote.PickCode != file.PickCode {
		return false
	}
	if file.OpenlistObjectID != "" && file.OpenlistObjectID != remote.OpenlistObjectID {
		return false
	}
	known := false
	for _, pair := range [][2]string{{file.SHA1, remote.SHA1}, {file.OpenlistSHA1, remote.OpenlistSHA1}, {file.OpenlistMD5, remote.OpenlistMD5}} {
		if pair[0] != "" {
			if !strings.EqualFold(pair[0], pair[1]) {
				return false
			}
			known = true
		}
	}
	if file.MTime != 0 {
		if file.MTime != remote.MTime {
			return false
		}
		known = true
	}
	return known
}

func validateEmbyDeletionTarget(ctx context.Context, plan EmbyDeletionPlan, target EmbyDeletionTarget) error {
	if !plan.Input.Authorized {
		return ErrEmbyDeleteDisabled
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
	if target.Key != EmbyDeletionFileKey(target.File) || len(target.Owners) == 0 {
		return ErrEmbyIdentityAmbiguous
	}
	physicalEvidenceFound := false
	sidecarEvidenceFound := false
	for _, ref := range target.Owners {
		found := false
		for _, owner := range plan.Input.Owners {
			if embyOwnerRef(owner) != ref {
				continue
			}
			found = true
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
			if evidence != owner.Evidence || evidence.ServerID != plan.Input.ServerID || evidence.ServerConfigKey != plan.Input.ServerConfigKey {
				return ErrEmbyIdentityAmbiguous
			}
			metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
			if err != nil {
				return err
			}
			if embyJSON(owner.Files) != evidence.FilesJSON || owner.Item.ItemId != evidence.ItemID || owner.Item.Generation != evidence.Generation {
				return ErrEmbyIdentityAmbiguous
			}
			if target.Kind == "video" && !slices.ContainsFunc(owner.Files, func(file EmbyFrozenFile) bool { return EmbyDeletionFileKey(file) == target.Key }) {
				return ErrEmbyIdentityAmbiguous
			}
			if target.Kind == "sidecar" {
				if slices.Contains(metadata.ExclusiveFiles, target.File) {
					sidecarEvidenceFound = true
				}
			}
			if slices.Contains(owner.Files, target.File) {
				physicalEvidenceFound = true
			}
		}
		if !found {
			return ErrEmbyIdentityAmbiguous
		}
	}
	if target.Kind == "sidecar" {
		if !sidecarEvidenceFound || !slices.Contains(plan.Input.Sidecars, target.File) {
			return ErrEmbyIdentityAmbiguous
		}
	} else if target.Kind == "scoped_metadata" {
		if err := validateEmbyScopedDeletionTarget(ctx, plan, target); err != nil {
			return err
		}
	} else if target.Kind != "video" || !physicalEvidenceFound {
		return ErrEmbyDeleteUnsupported
	}
	file := target.File
	switch file.SourceType {
	case SourceType115, SourceTypeBaiduPan:
	case SourceTypeOpenList:
		if file.OpenlistObjectID == "" && file.OpenlistSHA1 == "" && file.OpenlistMD5 == "" {
			return fmt.Errorf("%w: OpenList 缺少对象或哈希身份", ErrEmbyDeleteUnverified)
		}
	default:
		return ErrEmbyDeleteUnsupported
	}
	var current SyncFile
	if err := conn.First(&current, file.SyncFileID).Error; err != nil {
		return err
	}
	if current.FileType == v115open.TypeDir || target.Kind == "video" && !current.IsVideo || (target.Kind == "sidecar" || target.Kind == "scoped_metadata") && (!current.IsMeta || current.IsVideo) {
		return ErrEmbyIdentityAmbiguous
	}
	frozen, err := freezeEmbyFile(conn, current, file.SourceID, file.LocalFilePath)
	if err != nil {
		return err
	}
	if frozen != file || frozen.Reason != "" {
		return fmt.Errorf("%w: 当前记录、账号或同步目录已变化", ErrEmbyDeleteUnverified)
	}
	directory, validDirectory := embyRemoteDirectory(file.Path)
	if !embySafeName(file.FileName) || !validDirectory || !filepath.IsAbs(file.LocalFilePath) || !embyRemotePathWithin(file.SourceType, file.RemoteRoot, path.Join(directory, file.FileName)) || !embyPathWithin(file.LocalRoot, file.LocalFilePath) {
		return ErrEmbyIdentityAmbiguous
	}
	var roots []SyncPath
	if err := conn.Where("source_type = ? AND account_id = ?", file.SourceType, file.AccountID).Find(&roots).Error; err != nil {
		return err
	}
	full := path.Join(directory, file.FileName)
	for _, root := range roots {
		remoteRoot := root.RemotePath
		if remoteRoot == "" {
			remoteRoot = "/"
		}
		if _, valid := embyRemoteDirectory(remoteRoot); !valid {
			return ErrEmbyIdentityAmbiguous
		}
		if embyRemotePathWithin(file.SourceType, full, remoteRoot) || embyPathWithin(file.LocalFilePath, root.GetFullLocalPath()) || file.FileID == root.BaseCid {
			return fmt.Errorf("%w: 目标是同步目录的根目录或其上级目录，禁止删除", ErrEmbyDeleteUnverified)
		}
	}
	// 从物理文件查所有当前使用者，覆盖重叠同步目录、其他版本/季和共享目标。
	var users []EmbyMediaItem
	if err := conn.Table("emby_media_items AS i").Select("DISTINCT i.*").
		Joins("JOIN emby_media_sync_files AS r ON r.emby_item_id = i.item_id_int").
		Joins("JOIN sync_files AS f ON f.id = r.sync_file_id").
		Where("f.source_type = ? AND f.account_id = ? AND (f.file_id = ? OR (f.path = ? AND f.file_name = ?))", file.SourceType, file.AccountID, file.FileID, file.Path, file.FileName).
		Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		if user.ServerId != plan.Input.ServerID || !slices.Contains(target.Owners, EmbyDeletionOwnerRef{ItemID: user.ItemId, SnapshotID: user.SnapshotID, Generation: user.Generation}) {
			return fmt.Errorf("%w: 文件仍被其他 Emby 条目使用", ErrEmbyDeleteUnverified)
		}
	}
	return nil
}

// FinalizeEmbyDeletionPlan 只清理已全部完成的原快照关联，保留其他使用者与不可变历史。
func FinalizeEmbyDeletionPlan(ctx context.Context, plan EmbyDeletionPlan, results []EmbyDeletionResult) error {
	completed := map[string]bool{}
	for _, result := range results {
		completed[result.Key] = result.Outcome == EmbyDeletionDeleted || result.Outcome == EmbyDeletionAlreadyAbsent
	}
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := lockEmbyIndexState(tx)
		if err != nil {
			return err
		}
		if state.ServerID != plan.Input.ServerID || state.ServerConfigKey != plan.Input.ServerConfigKey {
			return ErrEmbySnapshotStale
		}
		var config EmbyConfig
		if err := tx.First(&config).Error; err != nil {
			return err
		}
		if EmbyServerConfigIdentity(&config) != plan.Input.ServerConfigKey {
			return ErrEmbySnapshotStale
		}
		for _, owner := range plan.Input.Owners {
			if len(owner.Files) == 0 || embyOwnerHasPlanningIssue(plan, owner) {
				continue
			}
			ref, all, found := embyOwnerRef(owner), true, false
			for _, target := range plan.Targets {
				if !slices.Contains(target.Owners, ref) {
					continue
				}
				found = true
				if !completed[target.Key] {
					all = false
					break
				}
			}
			if !all || !found {
				continue
			}
			var current EmbyItemState
			if err := tx.Where("server_id = ? AND item_id = ?", plan.Input.ServerID, ref.ItemID).First(&current).Error; err != nil {
				return err
			}
			if !current.Deleted || current.SnapshotID != ref.SnapshotID || current.Generation != ref.Generation {
				continue
			}
			var evidence EmbyItemEvidence
			if err := tx.First(&evidence, ref.SnapshotID).Error; err != nil {
				return err
			}
			if evidence != owner.Evidence || evidence.ServerID != plan.Input.ServerID || evidence.ServerConfigKey != plan.Input.ServerConfigKey || current.IdentityKey != evidence.IdentityKey {
				return ErrEmbyIdentityAmbiguous
			}
			for _, link := range owner.Links {
				if err := tx.Where("id = ? AND emby_item_id = ? AND snapshot_id = ? AND sync_file_id = ?", link.ID, owner.Item.ItemIdInt, ref.SnapshotID, link.SyncFileId).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
					return err
				}
			}
			var remaining int64
			if err := tx.Model(&EmbyMediaSyncFile{}).Where("emby_item_id = ?", owner.Item.ItemIdInt).Count(&remaining).Error; err != nil {
				return err
			}
			if remaining != 0 {
				continue
			}
			if err := tx.Where("server_id = ? AND item_id = ? AND snapshot_id = ? AND generation = ?", plan.Input.ServerID, ref.ItemID, ref.SnapshotID, ref.Generation).Delete(&EmbyMediaItem{}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func embyOwnerHasPlanningIssue(plan EmbyDeletionPlan, owner EmbyDeletionOwner) bool {
	for _, issue := range plan.Issues {
		if issue == "candidate_unresolved" {
			continue
		}
		if strings.HasPrefix(issue, "no_confirmed_files:") {
			if issue == "no_confirmed_files:"+owner.Item.ItemId {
				return true
			}
			continue
		}
		if strings.HasPrefix(issue, "sidecar_inventory_unavailable:") {
			for _, file := range owner.Files {
				key := embyDigest([]any{file.SourceType, file.AccountID, file.Path})
				if issue == "sidecar_inventory_unavailable:"+key {
					return true
				}
			}
			continue
		}
		return true
	}
	return false
}
