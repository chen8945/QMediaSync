package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"qmediasync/internal/db"
)

var (
	ErrEmbySnapshotStale     = errors.New("Emby 索引读取版本已失效")
	ErrEmbyItemDeleted       = errors.New("Emby 条目等待删除核验")
	ErrEmbyIdentityAmbiguous = errors.New("Emby 物理来源身份不明确")
)

// EmbyIndexState 保存单服务器索引接收水位；SQL 写锁同时协调同步提交和删除登记。
type EmbyIndexState struct {
	BaseModel
	ServerID        string `json:"server_id"`
	ConfigKey       string `json:"config_key"`
	ServerConfigKey string `json:"server_config_key"`
	Revision        int64  `json:"revision"`
}

// EmbyItemState 的代际不依赖普通 UpdatedAt；清理当前索引不会删除此记录。
type EmbyItemState struct {
	BaseModel
	ServerID    string `json:"server_id" gorm:"uniqueIndex:idx_emby_item_state,priority:1"`
	ItemID      string `json:"item_id" gorm:"uniqueIndex:idx_emby_item_state,priority:2"`
	Generation  int64  `json:"generation"`
	SnapshotID  uint   `json:"snapshot_id"`
	IdentityKey string `json:"identity_key"`
	Deleted     bool   `json:"deleted"`
	Revision    int64  `json:"revision"`
}

// EmbyItemEvidence 保存不可变身份与成员证据，不能随全量索引清理删除。
type EmbyItemEvidence struct {
	BaseModel
	ServerID        string `json:"server_id" gorm:"index:idx_emby_evidence_item,priority:1"`
	ConfigKey       string `json:"config_key"`
	ServerConfigKey string `json:"server_config_key"`
	ItemID          string `json:"item_id" gorm:"index:idx_emby_evidence_item,priority:2"`
	Generation      int64  `json:"generation"`
	IdentityKey     string `json:"identity_key"`
	EvidenceKey     string `json:"evidence_key"`
	ItemJSON        string `json:"item_json" gorm:"type:text"`
	SourcesJSON     string `json:"sources_json" gorm:"type:text"`
	FilesJSON       string `json:"files_json" gorm:"type:text"`
	SidecarsJSON    string `json:"sidecars_json" gorm:"type:text"`
}

// EmbyFrozenFile 是不含凭据的物理文件证据。Reason 非空的证据不能直接用于删除。
type EmbyFrozenFile struct {
	SyncFileID       uint       `json:"sync_file_id"`
	SyncPathID       uint       `json:"sync_path_id"`
	SourceType       SourceType `json:"source_type"`
	AccountID        uint       `json:"account_id"`
	AccountIdentity  string     `json:"account_identity"`
	LocalRoot        string     `json:"local_root"`
	RemoteRoot       string     `json:"remote_root"`
	RootFileID       string     `json:"root_file_id"`
	FileID           string     `json:"file_id"`
	ParentID         string     `json:"parent_id"`
	FileName         string     `json:"file_name"`
	Path             string     `json:"path"`
	LocalFilePath    string     `json:"local_file_path"`
	PickCode         string     `json:"pick_code"`
	SHA1             string     `json:"sha1"`
	FileSize         int64      `json:"file_size"`
	MTime            int64      `json:"mtime"`
	FileCreatedAt    int64      `json:"file_created_at"`
	SourceID         string     `json:"source_id"`
	OpenlistObjectID string     `json:"openlist_object_id"`
	OpenlistSHA1     string     `json:"openlist_sha1"`
	OpenlistMD5      string     `json:"openlist_md5"`
	Reason           string     `json:"reason,omitempty"`
}

// EmbySnapshotSource 保留物理媒体源身份和播放路径，不混用 Item.Path。
type EmbySnapshotSource struct {
	ID       string `json:"id"`
	ItemID   string `json:"item_id"`
	Path     string `json:"path"`
	PickCode string `json:"pick_code"`
}

type EmbyItemSnapshot struct {
	Item                   EmbyMediaItem
	Sources                []EmbySnapshotSource
	LibraryName            string
	MembersComplete        bool
	VersionMembershipKnown bool
}

type EmbyIndexToken struct {
	ServerID        string
	ConfigKey       string
	ServerConfigKey string
	Revision        int64
	// EvidenceHighWatermark 阻止早期观察采用远端读取开始后才提交的条目状态。
	EvidenceHighWatermark uint
}

// EmbyServerConfigIdentity 独立标识服务连接，不含同步选库，不能把同步范围当作删除白名单。
func EmbyServerConfigIdentity(config *EmbyConfig) string {
	if config == nil {
		return ""
	}
	return embyDigest([]any{config.ID, strings.TrimRight(config.EmbyUrl, "/"), config.EmbyApiKey})
}

// EmbyConfigIdentity 绑定配置行、服务地址与服务授权；不保存明文凭据。
func EmbyConfigIdentity(config *EmbyConfig) string {
	if config == nil {
		return ""
	}
	selected := ""
	if config.SyncAllLibraries == 0 {
		libraries := []string{}
		if config.SelectedLibraries != "" {
			if err := json.Unmarshal([]byte(config.SelectedLibraries), &libraries); err != nil {
				selected = config.SelectedLibraries
			}
		}
		if selected == "" {
			slices.Sort(libraries)
			selected = embyJSON(slices.Compact(libraries))
		}
	}
	return embyDigest([]any{EmbyServerConfigIdentity(config), config.SyncEnabled, config.SyncAllLibraries, selected})
}

// ReadEmbyConfigSnapshot 读取独立配置值，不改写供旧调用方使用的全局缓存。
func ReadEmbyConfigSnapshot() (*EmbyConfig, error) {
	var config EmbyConfig
	if err := db.Db.First(&config).Error; err != nil {
		return nil, err
	}
	normalizeEmbySyncMode(&config)
	return &config, nil
}

// BeginEmbyIndexRead 必须在读取条目或第一页之前调用；服务器 ID 必须来自实际服务。
func BeginEmbyIndexRead(serverID string, config *EmbyConfig) (token EmbyIndexToken, err error) {
	if strings.TrimSpace(serverID) == "" || config == nil {
		return token, ErrEmbyIdentityAmbiguous
	}
	resetCursor := false
	err = db.Db.Transaction(func(tx *gorm.DB) error {
		state, err := lockEmbyIndexState(tx)
		if err != nil {
			return err
		}
		if err := checkEmbyConfig(tx, EmbyConfigIdentity(config)); err != nil {
			return err
		}
		key := EmbyConfigIdentity(config)
		if state.ServerID != serverID || state.ConfigKey != key {
			resetCursor = state.ServerID != ""
			if resetCursor {
				if err := tx.Model(&EmbyConfig{}).Where("id = ?", config.ID).Updates(map[string]any{"last_saved_cursor_at": 0, "last_full_sync_at": 0, "last_incremental_sync_at": 0, "last_sync_time": 0, "last_success_sync_mode": ""}).Error; err != nil {
					return err
				}
			}
			state.ServerID, state.ConfigKey, state.Revision = serverID, key, state.Revision+1
			state.ServerConfigKey = EmbyServerConfigIdentity(config)
			if err := tx.Save(state).Error; err != nil {
				return err
			}
		}
		var evidenceID uint
		if err := tx.Model(&EmbyItemEvidence{}).Select("COALESCE(MAX(id), 0)").Scan(&evidenceID).Error; err != nil {
			return err
		}
		token = EmbyIndexToken{ServerID: serverID, ConfigKey: key, ServerConfigKey: state.ServerConfigKey, Revision: state.Revision, EvidenceHighWatermark: evidenceID}
		return nil
	})
	if err == nil && resetCursor {
		config.LastSavedCursorAt, config.LastFullSyncAt, config.LastIncrementalSyncAt, config.LastSyncTime = 0, 0, 0, 0
		config.LastSuccessSyncMode = ""
	}
	return token, err
}

func lockEmbyIndexState(tx *gorm.DB) (*EmbyIndexState, error) {
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&EmbyIndexState{BaseModel: BaseModel{ID: 1}}).Error; err != nil {
		return nil, err
	}
	// UPDATE 在 PostgreSQL 取得行锁，在 SQLite 取得写锁；不得先读后无条件写。
	if err := tx.Model(&EmbyIndexState{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + 0")).Error; err != nil {
		return nil, err
	}
	var state EmbyIndexState
	if err := tx.First(&state, 1).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

func checkEmbyConfig(tx *gorm.DB, key string) error {
	var config EmbyConfig
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&config).Error; err != nil {
		return err
	}
	if config.SyncEnabled != 1 || EmbyConfigIdentity(&config) != key {
		return ErrEmbySnapshotStale
	}
	return nil
}

func checkEmbyToken(tx *gorm.DB, token EmbyIndexToken) error {
	state, err := lockEmbyIndexState(tx)
	if err != nil {
		return err
	}
	if state.ServerID != token.ServerID || state.ConfigKey != token.ConfigKey || state.ServerConfigKey != token.ServerConfigKey || state.Revision != token.Revision {
		return ErrEmbySnapshotStale
	}
	return checkEmbyConfig(tx, token.ConfigKey)
}

// RegisterEmbyDeletionTx 在收件事务内登记删除屏障；调用方必须同时保存事件且禁止网络调用。
func RegisterEmbyDeletionTx(tx *gorm.DB, serverID string, itemIDs []string) (int64, error) {
	state, err := lockEmbyIndexState(tx)
	if err != nil {
		return 0, err
	}
	if serverID == "" || serverID != state.ServerID {
		return 0, ErrEmbyIdentityAmbiguous
	}
	state.Revision++
	if err := tx.Save(state).Error; err != nil {
		return 0, err
	}
	for _, id := range itemIDs {
		if _, err := parseEmbyItemID(id); err != nil {
			return 0, err
		}
		record := EmbyItemState{ServerID: serverID, ItemID: id, Deleted: true, Revision: state.Revision}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "server_id"}, {Name: "item_id"}}, DoUpdates: clause.Assignments(map[string]any{"deleted": true, "revision": state.Revision})}).Create(&record).Error; err != nil {
			return 0, err
		}
	}
	return state.Revision, nil
}

// AdmitEmbyItemGenerationTx 只允许核验后的调用方释放对应屏障；每次释放仍递增全局版本。
// expectedRevision 防止旧任务释放较新删除；存活原对象可复用代际，新身份由下一快照递增。
func AdmitEmbyItemGenerationTx(tx *gorm.DB, serverID, itemID string, expectedRevision int64) error {
	state, err := lockEmbyIndexState(tx)
	if err != nil {
		return err
	}
	if state.ServerID != serverID {
		return ErrEmbySnapshotStale
	}
	result := tx.Model(&EmbyItemState{}).Where("server_id = ? AND item_id = ? AND revision = ? AND deleted = ?", serverID, itemID, expectedRevision, true).Update("deleted", false)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrEmbySnapshotStale
	}
	return tx.Model(state).UpdateColumn("revision", gorm.Expr("revision + 1")).Error
}

// ApplyEmbySnapshots 原子保存主项、版本和分段，所有依赖错误均回滚关联替换。
func ApplyEmbySnapshots(token EmbyIndexToken, snapshots []EmbyItemSnapshot) error {
	expectedMembers := map[string]map[string]bool{}
	for _, snapshot := range snapshots {
		for field, parent := range map[string]string{"part_of_item_id": snapshot.Item.PartOfItemID, "version_of_item_id": snapshot.Item.VersionOfItemID} {
			key := field + "\x00" + parent
			if expectedMembers[key] == nil {
				expectedMembers[key] = map[string]bool{}
			}
			expectedMembers[key][snapshot.Item.ItemId] = true
		}
	}
	return db.Db.Transaction(func(tx *gorm.DB) error {
		if err := checkEmbyToken(tx, token); err != nil {
			return err
		}
		for i := range snapshots {
			if err := applyEmbySnapshot(tx, token, &snapshots[i]); err != nil {
				return err
			}
		}
		// 完整主项响应才解除消失成员关系；独立 Video 查询不代表成员关系已消失。
		for _, snapshot := range snapshots {
			if !snapshot.MembersComplete {
				continue
			}
			for _, field := range []string{"part_of_item_id", "version_of_item_id"} {
				var members []EmbyMediaItem
				if err := tx.Where("server_id = ? AND "+field+" = ?", token.ServerID, snapshot.Item.ItemId).Find(&members).Error; err != nil {
					return err
				}
				for _, member := range members {
					if expectedMembers[field+"\x00"+snapshot.Item.ItemId][member.ItemId] {
						continue
					}
					if err := detachEmbyMembership(tx, token, member, field); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// detachEmbyMembership 只修改旧快照的成员边，不从当前账本制造一次新的远端观察。
func detachEmbyMembership(tx *gorm.DB, token EmbyIndexToken, member EmbyMediaItem, field string) error {
	var state EmbyItemState
	if err := tx.Where("server_id = ? AND item_id = ?", token.ServerID, member.ItemId).First(&state).Error; err != nil {
		return err
	}
	if state.Deleted {
		// 父项的完整当前响应只更新搜索索引中的成员边。未完成删除仍引用旧证据，
		// 不改其状态/快照/关联，也不把没有重新读取的成员当成存活对象准入。
		if state.SnapshotID != member.SnapshotID || state.Generation != member.Generation {
			return nil
		}
		return tx.Model(&EmbyMediaItem{}).Where("id = ? AND server_id = ? AND item_id = ? AND snapshot_id = ? AND generation = ?", member.ID, token.ServerID, member.ItemId, member.SnapshotID, member.Generation).Update(field, "").Error
	}
	var evidence EmbyItemEvidence
	if err := tx.First(&evidence, member.SnapshotID).Error; err != nil {
		return err
	}
	var frozenItem EmbyMediaItem
	if err := json.Unmarshal([]byte(evidence.ItemJSON), &frozenItem); err != nil {
		return err
	}
	if field == "part_of_item_id" {
		frozenItem.PartOfItemID = ""
	} else {
		frozenItem.VersionOfItemID = ""
	}
	evidence.ID = 0 // 保留原观察时间与 FilesJSON；这次没有重新读取 Emby 或网盘身份。
	evidence.ItemJSON = embyJSON(frozenItem)
	evidence.EvidenceKey = embyDigest([]string{evidence.ConfigKey, evidence.ItemJSON, evidence.SourcesJSON, evidence.FilesJSON, evidence.SidecarsJSON})
	if err := tx.Create(&evidence).Error; err != nil {
		return err
	}
	if err := tx.Model(&EmbyMediaItem{}).Where("id = ?", member.ID).Updates(map[string]any{field: "", "snapshot_id": evidence.ID}).Error; err != nil {
		return err
	}
	if err := tx.Model(&EmbyMediaSyncFile{}).Where("emby_item_id = ? AND snapshot_id = ?", member.ItemIdInt, member.SnapshotID).Update("snapshot_id", evidence.ID).Error; err != nil {
		return err
	}
	return tx.Model(&state).Updates(map[string]any{"snapshot_id": evidence.ID, "revision": token.Revision}).Error
}

func applyEmbySnapshot(tx *gorm.DB, token EmbyIndexToken, snapshot *EmbyItemSnapshot) error {
	item := &snapshot.Item
	id, err := parseEmbyItemID(item.ItemId)
	if err != nil {
		return err
	}
	item.ItemIdInt, item.ServerId = id, token.ServerID
	var state EmbyItemState
	err = tx.Where("server_id = ? AND item_id = ?", token.ServerID, item.ItemId).First(&state).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if state.Deleted {
		return fmt.Errorf("%w: %s", ErrEmbyItemDeleted, item.ItemId)
	}
	evidence, files, err := buildEmbySnapshotEvidence(tx, token, snapshot, &state)
	if err != nil {
		return err
	}
	var previous EmbyItemEvidence
	if state.SnapshotID != 0 {
		if err := tx.First(&previous, state.SnapshotID).Error; err != nil {
			return err
		}
	}
	if previous.EvidenceKey == evidence.EvidenceKey {
		evidence.ID = previous.ID
	} else if err := tx.Create(&evidence).Error; err != nil {
		return err
	}
	item.SnapshotID = evidence.ID
	if err := upsertEmbyMediaItem(tx, item); err != nil {
		return err
	}
	if err := tx.Where("emby_item_id = ?", id).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
		return err
	}
	for _, file := range files {
		var currentFile SyncFile
		if err := tx.First(&currentFile, file.SyncFileID).Error; err != nil {
			return err
		}
		link := EmbyMediaSyncFile{
			EmbyItemId: uint(id),
			SyncFileId: file.SyncFileID,
			SyncPathId: file.SyncPathID,
			PickCode:   currentFile.PickCode,
			SnapshotID: evidence.ID,
			SourceID:   file.SourceID,
		}
		if err := tx.Create(&link).Error; err != nil {
			return err
		}
		if item.LibraryId != "" {
			relation := EmbyLibrarySyncPath{LibraryId: item.LibraryId, SyncPathId: file.SyncPathID, LibraryName: snapshot.LibraryName}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&relation).Error; err != nil {
				return err
			}
		}
	}
	state.IdentityKey, state.SnapshotID, state.Revision = evidence.IdentityKey, evidence.ID, token.Revision
	return tx.Save(&state).Error
}

func buildEmbySnapshotEvidence(tx *gorm.DB, token EmbyIndexToken, snapshot *EmbyItemSnapshot, state *EmbyItemState) (EmbyItemEvidence, []EmbyFrozenFile, error) {
	item := &snapshot.Item
	files, err := resolveEmbySnapshotFiles(tx, *snapshot)
	if err != nil {
		return EmbyItemEvidence{}, nil, err
	}
	identityKey := embyDigest([]any{item.ItemId, item.Path, item.DateCreated, files})
	if state.ID != 0 && state.IdentityKey == identityKey {
		var current EmbyMediaItem
		err := tx.Where("item_id = ? AND server_id = ?", item.ItemId, token.ServerID).First(&current).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return EmbyItemEvidence{}, nil, err
		}
		if item.PartOfItemID == "" {
			item.PartOfItemID = current.PartOfItemID
		}
		if item.VersionOfItemID == "" && !snapshot.VersionMembershipKnown {
			item.VersionOfItemID = current.VersionOfItemID
		}
	}

	if state.ID == 0 {
		*state = EmbyItemState{ServerID: token.ServerID, ItemID: item.ItemId}
	}
	if state.IdentityKey != identityKey {
		state.Generation++
	}
	item.Generation = state.Generation
	item.SnapshotID = 0
	stableItem := *item
	stableItem.BaseModel, stableItem.LastSeenSyncRun, stableItem.LastSeenAt = BaseModel{}, "", 0
	stableItem.Path = sanitizeEmbyEvidencePath(stableItem.Path)
	stableItem.MediaSourcePath = sanitizeEmbyEvidencePath(stableItem.MediaSourcePath)
	stableItem.PickCode = sanitizeEmbyEvidencePath(stableItem.PickCode)
	safeSources := append([]EmbySnapshotSource(nil), snapshot.Sources...)
	for i := range safeSources {
		safeSources[i].Path = sanitizeEmbyEvidencePath(safeSources[i].Path)
		safeSources[i].PickCode = sanitizeEmbyEvidencePath(safeSources[i].PickCode)
	}
	evidence := EmbyItemEvidence{
		ServerID:        token.ServerID,
		ConfigKey:       token.ConfigKey,
		ServerConfigKey: token.ServerConfigKey,
		ItemID:          item.ItemId,
		Generation:      state.Generation,
		IdentityKey:     identityKey,
		ItemJSON:        embyJSON(stableItem),
		SourcesJSON:     embyJSON(safeSources),
		FilesJSON:       embyJSON(files),
	}
	sidecars, err := freezeEmbySnapshotSidecars(tx, files)
	if err != nil {
		return EmbyItemEvidence{}, nil, err
	}
	evidence.SidecarsJSON = embyJSON(sidecars)
	evidence.EvidenceKey = embyDigest([]string{evidence.ConfigKey, evidence.ItemJSON, evidence.SourcesJSON, evidence.FilesJSON, evidence.SidecarsJSON})
	if err := checkEmbyWebhookSize(evidence); err != nil {
		return EmbyItemEvidence{}, nil, err
	}
	return evidence, files, nil
}

func resolveEmbySnapshotFiles(tx *gorm.DB, snapshot EmbyItemSnapshot) ([]EmbyFrozenFile, error) {
	files := []EmbyFrozenFile{}
	seen := map[uint]bool{}
	for _, source := range snapshot.Sources {
		if source.ItemID != "" && source.ItemID != snapshot.Item.ItemId {
			return nil, ErrEmbyIdentityAmbiguous
		}
		if source.PickCode == "" {
			continue
		}
		var candidates []SyncFile
		if err := tx.Where("pick_code = ?", source.PickCode).Find(&candidates).Error; err != nil {
			return nil, err
		}
		if len(candidates) > 1 && snapshot.Item.Path != "" {
			exact := []SyncFile{}
			for _, candidate := range candidates {
				if candidate.LocalFilePath == snapshot.Item.Path {
					exact = append(exact, candidate)
				}
			}
			candidates = exact
			if len(candidates) == 0 {
				return nil, ErrEmbyIdentityAmbiguous
			}
		}
		if len(candidates) > 1 {
			return nil, ErrEmbyIdentityAmbiguous
		}
		if len(candidates) == 0 {
			continue
		}
		file := candidates[0]
		if seen[file.ID] {
			continue
		}
		seen[file.ID] = true
		frozen, err := freezeEmbyFile(tx, file, source.ID, snapshot.Item.Path)
		if err != nil {
			return nil, err
		}
		files = append(files, frozen)
	}
	return files, nil
}

func freezeEmbyFile(tx *gorm.DB, file SyncFile, sourceID, itemPath string) (EmbyFrozenFile, error) {
	frozen := EmbyFrozenFile{
		SyncFileID:       file.ID,
		SyncPathID:       file.SyncPathId,
		SourceType:       file.SourceType,
		AccountID:        file.AccountId,
		FileID:           file.FileId,
		ParentID:         file.ParentId,
		FileName:         file.FileName,
		Path:             file.Path,
		LocalFilePath:    file.LocalFilePath,
		PickCode:         sanitizeEmbyEvidencePath(file.PickCode),
		OpenlistObjectID: file.OpenlistObjectId,
		OpenlistSHA1:     file.OpenlistSHA1,
		OpenlistMD5:      file.OpenlistMD5,
		SHA1:             file.Sha1,
		FileSize:         file.FileSize,
		MTime:            file.MTime,
		FileCreatedAt:    file.CreatedAt,
		SourceID:         sourceID,
	}
	var root SyncPath
	if err := tx.First(&root, file.SyncPathId).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		frozen.Reason = "missing_sync_root"
		return frozen, nil
	} else if err != nil {
		return frozen, err
	}
	frozen.LocalRoot, frozen.RemoteRoot, frozen.RootFileID = root.GetFullLocalPath(), root.RemotePath, root.BaseCid
	if root.AccountId != file.AccountId || root.SourceType != file.SourceType || !embyPathWithin(frozen.LocalRoot, file.LocalFilePath) || !embyRemotePathWithin(file.SourceType, root.RemotePath, file.Path) {
		frozen.Reason = "sync_scope_mismatch"
	}
	if itemPath == "" || itemPath != file.LocalFilePath {
		frozen.Reason = "physical_path_unverified"
	}
	var account Account
	if err := tx.First(&account, file.AccountId).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		frozen.Reason = "missing_account"
		return frozen, nil
	} else if err != nil {
		return frozen, err
	}
	frozen.AccountIdentity = EmbyAccountIdentity(account)
	if account.SourceType != file.SourceType || frozen.AccountIdentity == "" {
		frozen.Reason = "account_identity_unverified"
	}
	if file.FileId == "" {
		frozen.Reason = "missing_file_identity"
	}
	if file.Sha1 == "" && file.OpenlistSHA1 == "" && file.OpenlistMD5 == "" && file.MTime == 0 {
		frozen.Reason = "missing_file_generation"
	}
	return frozen, nil
}

// EmbyAccountIdentity 固定账号主体；安全更换同主体授权不会改变身份。
func EmbyAccountIdentity(account Account) string {
	if account.SourceType == SourceTypeOpenList {
		if account.BaseUrl == "" || account.Username == "" {
			return ""
		}
		return embyDigest([]any{account.SourceType, strings.TrimRight(account.BaseUrl, "/"), account.Username, account.CreatedAt})
	}
	if account.UserId == "" {
		return ""
	}
	return embyDigest([]any{account.SourceType, account.UserId, account.CreatedAt})
}

func embyPathWithin(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// embyRemoteDirectory 只统一网盘根的表示，不清理含歧义的路径，也不改写持久身份。
// 同步配置以根相对路径保存，网盘详情可能带前导 /；配置中的 . 表示网盘根。
func embyRemoteDirectory(directory string) (string, bool) {
	if directory == "" || strings.ContainsAny(directory, "\\\x00") || path.Clean(directory) != directory || directory == ".." || strings.HasPrefix(directory, "../") {
		return "", false
	}
	if directory == "." || directory == "/" {
		return "/", true
	}
	return "/" + strings.TrimPrefix(directory, "/"), true
}

func embyRemoteDirectoriesMatch(source SourceType, left, right string) bool {
	switch source {
	case SourceType115, SourceTypeBaiduPan, SourceTypeOpenList:
		leftDirectory, leftValid := embyRemoteDirectory(left)
		rightDirectory, rightValid := embyRemoteDirectory(right)
		return leftValid && rightValid && leftDirectory == rightDirectory
	default:
		return left == right
	}
}

func embyRemotePathWithin(source SourceType, root, remote string) bool {
	switch source {
	case SourceType115, SourceTypeBaiduPan, SourceTypeOpenList:
		rootDirectory, rootValid := embyRemoteDirectory(root)
		remoteDirectory, remoteValid := embyRemoteDirectory(remote)
		return rootValid && remoteValid && (rootDirectory == "/" || remoteDirectory == rootDirectory || strings.HasPrefix(remoteDirectory, rootDirectory+"/"))
	default:
		return embyPathWithin(root, remote)
	}
}

func upsertEmbyMediaItem(tx *gorm.DB, item *EmbyMediaItem) error {
	var existing EmbyMediaItem
	err := tx.Where("item_id = ?", item.ItemId).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return tx.Create(item).Error
	}
	if err != nil {
		return err
	}
	item.ID, item.CreatedAt = existing.ID, existing.CreatedAt
	fields := []string{
		"item_id_int",
		"server_id",
		"name",
		"type",
		"parent_id",
		"series_id",
		"series_name",
		"season_id",
		"season_name",
		"library_id",
		"path",
		"pick_code",
		"media_source_path",
		"index_number",
		"parent_index_number",
		"production_year",
		"premiere_date",
		"date_created",
		"date_created_time",
		"date_modified",
		"date_modified_time",
		"is_folder",
		"last_seen_at",
		"part_count",
		"part_of_item_id",
		"version_of_item_id",
		"snapshot_id",
		"generation",
	}
	if item.LastSeenSyncRun != "" {
		fields = append(fields, "last_seen_sync_run")
	}
	return tx.Model(&existing).Select(fields).Updates(item).Error
}

// CleanupEmbyLibrarySnapshot 清理当前索引但保留不可变证据；失败或删除交错禁止清理。
func CleanupEmbyLibrarySnapshot(token EmbyIndexToken, libraryID, syncRunID string) error {
	if libraryID == "" || syncRunID == "" {
		return errors.New("Emby 清理缺少媒体库或批次")
	}
	return db.Db.Transaction(func(tx *gorm.DB) error {
		if err := checkEmbyToken(tx, token); err != nil {
			return err
		}
		var ids []int64
		query := tx.Model(&EmbyMediaItem{}).Where("library_id = ? AND (last_seen_sync_run IS NULL OR last_seen_sync_run != ?)", libraryID, syncRunID)
		if err := query.Pluck("item_id_int", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Where("emby_item_id IN ?", ids).Delete(&EmbyMediaSyncFile{}).Error; err != nil {
			return err
		}
		return tx.Where("item_id_int IN ?", ids).Delete(&EmbyMediaItem{}).Error
	})
}

func parseEmbyItemID(id string) (int64, error) {
	value, err := strconv.ParseInt(id, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("无效 Emby 条目 ID：%q", id)
	}
	return value, nil
}
func embyJSON(value any) string { data, _ := json.Marshal(value); return string(data) }
func embyDigest(value any) string {
	sum := sha256.Sum256([]byte(embyJSON(value)))
	return hex.EncodeToString(sum[:])
}

// sanitizeEmbyEvidencePath 去除新增历史证据中的 URL 凭据，现有播放字段仍保持兼容。
func sanitizeEmbyEvidencePath(value string) string {
	u, err := url.Parse(value)
	if err != nil {
		if strings.HasPrefix(strings.ToLower(value), "http") {
			return "invalid-url:" + embyDigest(value)
		}
		return value
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return value
	}
	u.User = nil
	query := url.Values{}
	for _, key := range []string{"pickcode", "pick_code"} {
		if code := u.Query().Get(key); code != "" {
			query.Set(key, code)
		}
	}
	u.RawQuery = query.Encode()
	u.Fragment = ""
	return u.String()
}

// FinishEmbyIndexSyncRun 将成功水位和版本校验放入同一事务，空扫描也不能绕过屏障。
func FinishEmbyIndexSyncRun(token EmbyIndexToken, mode string, processed, finishedAt, cursorAt int64, runErr error) error {
	var validationErr error
	err := db.Db.Transaction(func(tx *gorm.DB) error {
		if runErr == nil {
			validationErr = checkEmbyToken(tx, token)
			if validationErr != nil {
				runErr = validationErr
			}
		}
		updates := map[string]any{"is_running": false, "sync_mode": EmbySyncModeIdle, "started_at": 0, "last_processed_count": processed, "last_error": ""}
		if runErr != nil {
			updates["last_error"] = redactEmbySnapshotError(runErr.Error())
		} else {
			updates["last_sync_time"] = finishedAt
			updates["last_success_sync_mode"] = mode
			switch mode {
			case EmbySyncModeFull:
				updates["last_full_sync_at"] = finishedAt
			case EmbySyncModeIncremental:
				updates["last_incremental_sync_at"] = finishedAt
				if cursorAt > 0 {
					updates["last_saved_cursor_at"] = cursorAt
				}
			}
		}
		return tx.Model(&EmbyConfig{}).Where("id > 0").Updates(updates).Error
	})
	return errors.Join(validationErr, err)
}

var embyErrorURLPattern = regexp.MustCompile(`(?i)https?://[^\s"<>]+`)

func redactEmbySnapshotError(message string) string {
	return embyErrorURLPattern.ReplaceAllStringFunc(message, func(value string) string {
		return sanitizeEmbyEvidencePath(value)
	})
}
