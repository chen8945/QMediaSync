package models

import (
	"cmp"
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

// embyLocalDirectoryView 只在本次准备期间复用；节点冲突保留标记，不能任选一份。
type embyLocalDirectoryView struct {
	nodes     map[string]EmbyDirectoryAncestor
	ids       map[string]string
	conflicts map[string]bool
	invalid   bool
}

func newEmbyLocalDirectoryView() *embyLocalDirectoryView {
	return &embyLocalDirectoryView{nodes: map[string]EmbyDirectoryAncestor{}, ids: map[string]string{}, conflicts: map[string]bool{}}
}

func (v *embyLocalDirectoryView) add(node EmbyDirectoryAncestor) {
	if previous, ok := v.nodes[node.Path]; ok && previous != node {
		v.conflicts[node.Path] = true
	}
	if previous, ok := v.ids[node.FileID]; ok && previous != node.Path {
		v.conflicts[previous], v.conflicts[node.Path] = true, true
	}
	v.nodes[node.Path], v.ids[node.FileID] = node, node.Path
}

func (v *embyLocalDirectoryView) chain(file EmbyFrozenFile) []EmbyDirectoryAncestor {
	if v.invalid {
		return nil
	}
	directory, valid := embyRemoteDirectory(file.Path)
	if !valid {
		return nil
	}
	var reverse []EmbyDirectoryAncestor
	for {
		node, ok := v.nodes[directory]
		if !ok || v.conflicts[directory] || len(reverse) > 0 && reverse[len(reverse)-1].ParentID != node.FileID {
			return nil
		}
		reverse = append(reverse, node)
		if directory == "/" {
			if node.FileID != "0" || node.ParentID != "" {
				return nil
			}
			break
		}
		directory = path.Dir(directory)
	}
	slices.Reverse(reverse)
	parent := reverse[len(reverse)-1]
	if file.SourceType == SourceType115 && parent.FileID != file.ParentID || file.SourceType == SourceTypeBaiduPan && !embyRemoteDirectoriesMatch(file.SourceType, file.ParentID, parent.Path) {
		return nil
	}
	for _, node := range reverse {
		if embyRemoteDirectoriesMatch(file.SourceType, node.Path, file.RemoteRoot) && (file.SourceType != SourceType115 || node.FileID == file.RootFileID) {
			return reverse
		}
	}
	return nil
}

func embyDirectoryRootKey(file EmbyFrozenFile) string {
	return embyDigest([]any{file.SyncPathID, file.SourceType, file.AccountID, file.AccountIdentity, file.RootFileID, file.RemoteRoot, file.LocalRoot})
}

func (c *EmbyDirectoryEvidenceCollector) directoryChain(ctx context.Context, snapshot EmbyItemSnapshot, file EmbyFrozenFile) ([]EmbyDirectoryAncestor, error) {
	if c.roots == nil {
		c.roots = map[string]*embyLocalDirectoryView{}
	}
	key := embyDirectoryRootKey(file)
	view, ok := c.roots[key]
	if !ok {
		var err error
		view, err = loadEmbyLocalDirectoryView(db.Db.WithContext(ctx), snapshot.Item.ServerId, file)
		if err != nil {
			return nil, err
		}
		c.roots[key] = view
	}
	if file.SyncFileID == 0 {
		parent, valid := embyRemoteDirectory(file.Path)
		if !valid {
			return nil, nil
		}
		// 显式结构映射独立证明媒体目录位置；未知的视频下级目录不应抹掉
		// 已有的合法 Series/Movie 目录身份，也不能补造未知下级的身份。
		for parent != "/" && view.nodes[parent].FileID == "" {
			parent = path.Dir(parent)
		}
		file.Path, file.ParentID = parent, view.nodes[parent].FileID
		if file.SourceType == SourceTypeBaiduPan {
			file.ParentID = parent
		}
	}
	return view.chain(file), nil
}

func loadEmbyLocalDirectoryView(tx *gorm.DB, serverID string, file EmbyFrozenFile) (*embyLocalDirectoryView, error) {
	view := newEmbyLocalDirectoryView()
	roots, err := LoadSyncRootEvidenceBatchTx(tx, []uint{file.SyncPathID})
	if err != nil {
		return nil, err
	}
	if root, found := roots[file.SyncPathID]; found {
		if !embyRootEvidenceMatches(root, file) {
			view.invalid = true
			return view, nil
		}
		for _, node := range root.Ancestors {
			view.add(node)
		}
	} else if err := addEmbyCurrentDirectoryEvidence(tx, serverID, file, view); err != nil {
		return nil, err
	}
	var directories []SyncFile
	if err := tx.Where("sync_path_id = ? AND source_type = ? AND account_id = ? AND file_type = ?", file.SyncPathID, file.SourceType, file.AccountID, v115open.TypeDir).Find(&directories).Error; err != nil {
		return nil, fmt.Errorf("读取同步目录账本失败：%w", err)
	}
	// 百度账本的父 ID 是路径，先建立所有目录 fsid 后才能核对直接父子关系。
	baiduIDs := map[string]string{}
	for _, directory := range directories {
		if full, valid := embyLedgerDirectoryPath(directory); valid && directory.SourceType == SourceTypeBaiduPan && directory.PickCode != "" {
			if previous := baiduIDs[full]; previous != "" && previous != directory.PickCode {
				view.conflicts[full] = true
			}
			baiduIDs[full] = directory.PickCode
		}
	}
	for _, directory := range directories {
		full, ok := embyLedgerDirectoryPath(directory)
		if !ok || !embyRemotePathWithin(file.SourceType, file.RemoteRoot, full) {
			continue
		}
		id := directory.FileId
		if file.SourceType == SourceTypeBaiduPan {
			id = directory.PickCode
		}
		if id == "" {
			continue
		}
		parent := directory.ParentId
		if file.SourceType == SourceTypeBaiduPan {
			parent = view.nodes[path.Dir(full)].FileID
			if id := baiduIDs[path.Dir(full)]; id != "" {
				parent = id
			}
			if !embyRemoteDirectoriesMatch(file.SourceType, directory.ParentId, path.Dir(full)) {
				view.conflicts[full] = true
			}
		}
		view.add(EmbyDirectoryAncestor{FileID: id, ParentID: parent, Path: full})
	}
	return view, nil
}

func embyRootEvidenceMatches(root SyncRootEvidence, file EmbyFrozenFile) bool {
	return root.InvalidatedAt == 0 && root.SyncPathID == file.SyncPathID && root.SourceType == file.SourceType && root.AccountID == file.AccountID && root.AccountIdentity == file.AccountIdentity && root.RootFileID == file.RootFileID && embyRemoteDirectoriesMatch(file.SourceType, root.RootPath, file.RemoteRoot)
}

func embyLedgerDirectoryPath(directory SyncFile) (string, bool) {
	parent, ok := embyRemoteDirectory(directory.Path)
	if !ok || !embyDeleteBasename(directory.FileName) {
		return "", false
	}
	return path.Join(parent, directory.FileName), true
}

// addEmbyCurrentDirectoryEvidence 只读取当前状态引用的匹配快照；不会扫描历史最大 ID，
// 也不会将历史提交时间写成新的网盘观察时间。
func addEmbyCurrentDirectoryEvidence(tx *gorm.DB, serverID string, rootFile EmbyFrozenFile, view *embyLocalDirectoryView) error {
	var evidences []EmbyItemEvidence
	query := tx.Table("emby_item_evidences AS e").Select("DISTINCT e.*").
		Joins("JOIN emby_item_states AS s ON s.snapshot_id = e.id AND s.server_id = e.server_id AND s.item_id = e.item_id AND s.generation = e.generation AND s.identity_key = e.identity_key").
		Joins("JOIN emby_media_items AS i ON i.snapshot_id = e.id AND i.server_id = e.server_id AND i.item_id = e.item_id AND i.generation = e.generation").
		Joins("JOIN emby_media_sync_files AS l ON l.emby_item_id = i.item_id_int AND l.snapshot_id = e.id").
		Joins("JOIN emby_index_states AS current_server ON current_server.server_id = e.server_id AND current_server.server_config_key = e.server_config_key AND current_server.server_config_key <> ''").
		Where("l.sync_path_id = ? AND s.deleted = ?", rootFile.SyncPathID, false)
	if serverID != "" {
		query = query.Where("e.server_id = ?", serverID)
	}
	if err := query.Find(&evidences).Error; err != nil {
		return fmt.Errorf("读取当前 Emby 目录资料失败：%w", err)
	}
	var current []SyncFile
	if len(evidences) == 0 {
		return nil
	}
	if err := tx.Where("sync_path_id = ? AND source_type = ? AND account_id = ? AND is_video = ?", rootFile.SyncPathID, rootFile.SourceType, rootFile.AccountID, true).Find(&current).Error; err != nil {
		return err
	}
	byID := make(map[uint]SyncFile, len(current))
	for _, file := range current {
		byID[file.ID] = file
	}
	for _, evidence := range evidences {
		var files []EmbyFrozenFile
		if err := json.Unmarshal([]byte(evidence.FilesJSON), &files); err != nil {
			return fmt.Errorf("读取当前 Emby 文件身份失败：%w", err)
		}
		metadata, err := DecodeEmbyMetadata(evidence.SidecarsJSON)
		if err != nil {
			return err
		}
		for _, file := range files {
			if !embySameDirectoryRoot(file, rootFile) || !embyFrozenMatchesLedger(file, byID[file.SyncFileID]) {
				continue
			}
			for _, scope := range metadata.DirectoryScopes {
				if !embySameDirectoryRoot(scope.Root, file) || !embyRemotePathWithin(file.SourceType, path.Join(scope.Root.Path, scope.Root.FileName), file.Path) {
					continue
				}
				for _, node := range scope.Ancestors {
					view.add(node)
				}
				view.add(EmbyDirectoryAncestor{FileID: scope.Root.FileID, ParentID: scope.Root.ParentID, Path: path.Join(scope.Root.Path, scope.Root.FileName)})
			}
		}
	}
	return nil
}

func embySameDirectoryRoot(left, right EmbyFrozenFile) bool {
	return left.SyncPathID == right.SyncPathID && left.SourceType == right.SourceType && left.AccountID == right.AccountID && left.AccountIdentity == right.AccountIdentity && left.LocalRoot == right.LocalRoot && left.RootFileID == right.RootFileID && embyRemoteDirectoriesMatch(left.SourceType, left.RemoteRoot, right.RemoteRoot)
}

func embyFrozenMatchesLedger(file EmbyFrozenFile, row SyncFile) bool {
	return file.Reason == "" && row.ID != 0 && file.SyncFileID == row.ID && file.FileCreatedAt == row.CreatedAt && file.SyncPathID == row.SyncPathId && file.AccountID == row.AccountId && file.SourceType == row.SourceType && file.FileID == row.FileId && file.ParentID == row.ParentId && file.FileName == row.FileName && file.Path == row.Path && file.LocalFilePath == row.LocalFilePath && file.PickCode == row.PickCode && file.SHA1 == row.Sha1 && file.FileSize == row.FileSize && file.MTime == row.MTime
}

// ValidateEmbyDirectoryScopesTx 在原快照事务内重新检查根链及涉及的目录节点，准备缓存
// 不能覆盖已确认的根变化、同名替换或目录移动；读取失败交给整组事务回滚。
func ValidateEmbyDirectoryScopesTx(tx *gorm.DB, files []EmbyFrozenFile, scopes []EmbyDirectoryScope, snapshots ...EmbyItemSnapshot) ([]EmbyDirectoryScope, error) {
	result := make([]EmbyDirectoryScope, 0, len(scopes))
	contexts := files
	if len(scopes) > 0 && len(snapshots) == 1 {
		mapped, err := embyMappedDirectoryContexts(tx, snapshots[0], files)
		if err != nil {
			return nil, err
		}
		contexts = append(append([]EmbyFrozenFile(nil), files...), mapped...)
	}
	rootIDs := []uint{}
	for _, scope := range scopes {
		if !slices.Contains(rootIDs, scope.Root.SyncPathID) {
			rootIDs = append(rootIDs, scope.Root.SyncPathID)
		}
	}
	roots, err := LoadSyncRootEvidenceBatchTx(tx, rootIDs)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if !validEmbyDirectoryScope(scope) {
			continue
		}
		matched := slices.ContainsFunc(contexts, func(file EmbyFrozenFile) bool {
			return file.Reason == "" && embySameDirectoryRoot(scope.Root, file) && embyRemotePathWithin(file.SourceType, path.Join(scope.Root.Path, scope.Root.FileName), file.Path) &&
				(file.SyncFileID != 0 || file.Reference == scope.Root.Reference && file.SourceID == scope.Root.SourceID)
		})
		if !matched {
			continue
		}
		nodes := append(append([]EmbyDirectoryAncestor(nil), scope.Ancestors...), EmbyDirectoryAncestor{FileID: scope.Root.FileID, ParentID: scope.Root.ParentID, Path: path.Join(scope.Root.Path, scope.Root.FileName)})
		if root, found := roots[scope.Root.SyncPathID]; found && (!embyRootEvidenceMatches(root, scope.Root) || len(root.Ancestors) > len(nodes) || !slices.Equal(root.Ancestors, nodes[:min(len(root.Ancestors), len(nodes))])) {
			continue
		}
		query := tx.Where("source_type = ? AND account_id = ? AND sync_path_id = ? AND file_type = ?", scope.Root.SourceType, scope.Root.AccountID, scope.Root.SyncPathID, v115open.TypeDir).Session(&gorm.Session{})
		identityColumn := "file_id"
		if scope.Root.SourceType == SourceTypeBaiduPan {
			identityColumn = "pick_code"
		}
		var queries []*gorm.DB
		if tx.Dialector.Name() == "sqlite" {
			// 逐 ID 等值查询保留 ORDER BY id，同时使用 SQLite 的根内身份索引。
			var identities []string
			for _, node := range nodes {
				if !embyRemotePathWithin(scope.Root.SourceType, scope.Root.RemoteRoot, node.Path) {
					continue
				}
				identities = append(identities, node.FileID)
				queries = append(queries, query.Where("path IN ? AND file_name = ?", embyDirectoryPathRepresentations(path.Dir(node.Path)), path.Base(node.Path)))
			}
			for _, identity := range identities {
				queries = append(queries, query.Where(identityColumn+" = ?", identity))
			}
		} else {
			var alternatives []string
			var args []any
			for _, node := range nodes {
				if !embyRemotePathWithin(scope.Root.SourceType, scope.Root.RemoteRoot, node.Path) {
					continue
				}
				alternatives = append(alternatives, "("+identityColumn+" = ? OR (path IN ? AND file_name = ?))")
				args = append(args, node.FileID, embyDirectoryPathRepresentations(path.Dir(node.Path)), path.Base(node.Path))
			}
			if len(alternatives) > 0 {
				queries = append(queries, query.Where(strings.Join(alternatives, " OR "), args...))
			}
		}
		var rows []SyncFile
		seen := map[uint]bool{}
		key := "scope:" + embyJSON([]any{scope.Root.SourceType, scope.Root.AccountID, scope.Root.SyncPathID, nodes})
		for i, candidateQuery := range queries {
			candidates, err := embyApplyFileCandidates(tx, fmt.Sprintf("%s:%d", key, i), candidateQuery.Order("id"))
			if err != nil {
				return nil, err
			}
			for _, row := range candidates {
				if !seen[row.ID] {
					seen[row.ID] = true
					rows = append(rows, row)
				}
			}
		}
		slices.SortFunc(rows, func(a, b SyncFile) int { return cmp.Compare(a.ID, b.ID) })
		view := newEmbyLocalDirectoryView()
		for _, node := range nodes {
			view.add(node)
		}
		for _, row := range rows {
			full, valid := embyLedgerDirectoryPath(row)
			if !valid {
				view.invalid = true
				break
			}
			id, parent := row.FileId, row.ParentId
			if row.SourceType == SourceTypeBaiduPan {
				if !embyRemoteDirectoriesMatch(row.SourceType, row.ParentId, path.Dir(full)) {
					view.conflicts[full] = true
				}
				id, parent = row.PickCode, view.nodes[path.Dir(full)].FileID
				if id == "" {
					continue
				}
			}
			view.add(EmbyDirectoryAncestor{FileID: id, ParentID: parent, Path: full})
		}
		if !view.invalid && !slices.ContainsFunc(nodes, func(node EmbyDirectoryAncestor) bool { return view.conflicts[node.Path] }) {
			result = append(result, scope)
		}
	}
	return result, nil
}

func embyDirectoryPathRepresentations(directory string) []string {
	var valid bool
	directory, valid = embyRemoteDirectory(directory)
	if !valid {
		return nil
	}
	if directory == "/" {
		return []string{"/", "."}
	}
	return []string{directory, strings.TrimPrefix(directory, "/")}
}

func embyManagedMediaDirectory(file EmbyFrozenFile, directory string) (string, bool) {
	if file.Reference.EmbyPath == "" {
		return directory, true
	}
	if file.Reference.Reason != "" || file.Reference.SyncPathID != file.SyncPathID {
		return "", false
	}
	relative, valid := (EmbyPathMapping{EmbyRoot: file.Reference.EmbyRoot}).RelativePath(directory)
	if !valid {
		return "", false
	}
	return filepath.Join(file.LocalRoot, filepath.FromSlash(relative)), true
}

// embyMappedDirectoryContexts 只组合显式结构映射与已确认来源账号；返回值仅供目录
// 定位，不是视频文件身份，不写入 FilesJSON 或 SyncFile。
func embyMappedDirectoryContexts(tx *gorm.DB, snapshot EmbyItemSnapshot, files []EmbyFrozenFile) ([]EmbyFrozenFile, error) {
	// 每个来源最多解析一个去重文件，数量相等才表示所有来源均已绑定。
	if len(files) == len(snapshot.Sources) {
		return nil, nil
	}
	sources := snapshot.frozenSources
	if sources == nil {
		var err error
		sources, err = freezeEmbySnapshotSources(tx, snapshot, files)
		if err != nil {
			return nil, err
		}
	}
	var contexts []EmbyFrozenFile
	for _, source := range sources {
		reference := source.Reference
		if source.SyncFileID != 0 || source.AccountID == 0 || source.AccountIdentity == "" || source.ObjectID == "" ||
			source.SourceType != SourceType115 && source.SourceType != SourceTypeBaiduPan || reference.SyncPathID == 0 || reference.Reason != "" || reference.LocalRoot == "" {
			continue
		}
		root, rootErr := loadEmbyApplyRoot(tx, reference.SyncPathID)
		if err := rootErr; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return nil, err
		}
		if root.SourceType != source.SourceType || root.AccountId != source.AccountID {
			continue
		}
		relative, valid := (EmbyPathMapping{EmbyRoot: reference.EmbyRoot}).RelativePath(snapshot.Item.Path)
		if !valid {
			continue
		}
		remote, valid := embyRemoteDirectory(root.RemotePath)
		if !valid {
			continue
		}
		contexts = append(contexts, EmbyFrozenFile{SyncPathID: root.ID, SourceType: root.SourceType, AccountID: source.AccountID, AccountIdentity: source.AccountIdentity,
			LocalRoot: root.GetFullLocalPath(), RemoteRoot: root.RemotePath, RootFileID: root.BaseCid, SourceID: source.ID, Reference: reference,
			LocalFilePath: filepath.Join(root.GetFullLocalPath(), filepath.FromSlash(relative)), Path: path.Dir(path.Join(remote, relative))})
	}
	return contexts, nil
}
