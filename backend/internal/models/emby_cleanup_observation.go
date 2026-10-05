package models

import (
	"context"
	"fmt"
	"math"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/v115open"
)

// EmbyMediaDirectory 是 Emby 实际祖先响应里的媒体目录角色（电影为其父文件夹），尚未取得云端删除能力。
type EmbyMediaDirectory struct {
	ItemID       string
	MediaType    string
	LocalPath    string
	SeasonNumber *int
}

// embyDirectoryCandidate 记录目录角色的证据来源；同一目录两种来源并存时物理祖先优先。
type embyDirectoryCandidate struct {
	directory EmbyMediaDirectory
	kind      string
}

type embyObservedDirectoryChain struct {
	Nodes []EmbyDirectoryAncestor
	At    time.Time
	Err   error
}

type embyObservedBaiduDirectory struct {
	Files []*baidupan.FileInfo
	At    time.Time
	Err   error
}

// EmbyDirectoryEvidenceCollector 仅在一次同步／早期观察中复用短时目录读取。
// 它不执行删除，也不在 SQL 事务内调用网盘。过期结果不会跨后续同步复用。
type EmbyDirectoryEvidenceCollector struct {
	chains     map[string]embyObservedDirectoryChain
	baiduFiles map[string]embyObservedBaiduDirectory
}

// Enrich 保存可验证的季剧祖先、电影物理父目录或 QMS 电影输出目录；缺少能力时保留原文件快照。
func (c *EmbyDirectoryEvidenceCollector) Enrich(ctx context.Context, snapshot *EmbyItemSnapshot, ancestors []EmbyMediaDirectory) error {
	if db.Db == nil {
		return nil
	}
	files, err := resolveEmbySnapshotFiles(db.Db.WithContext(ctx), *snapshot)
	if err != nil {
		return err
	}
	snapshot.DirectoryScopes = nil
	snapshot.cleanupFiles = files
	keys := map[string]bool{}
	for _, file := range files {
		if file.Reason != "" || file.SourceType != SourceType115 && file.SourceType != SourceTypeBaiduPan {
			continue
		}
		candidates := make([]embyDirectoryCandidate, 0, len(ancestors)+1)
		for _, ancestor := range ancestors {
			candidates = append(candidates, embyDirectoryCandidate{directory: ancestor, kind: "emby_physical_ancestor"})
		}
		if snapshot.Item.Type == "Movie" {
			movie, err := embyScrapedMovieDirectory(ctx, file, snapshot.Item.ItemId)
			if err != nil {
				return err
			}
			if movie != nil {
				candidates = append(candidates, embyDirectoryCandidate{directory: *movie, kind: "qms_scrape_output"})
			}
		}
		if len(candidates) == 0 {
			continue
		}
		nodes, err := c.directoryChain(ctx, file)
		if err != nil {
			return err
		}
		if file.SourceType == SourceTypeBaiduPan {
			var account Account
			if err := db.Db.WithContext(ctx).First(&account, file.AccountID).Error; err != nil {
				return err
			}
			if account.SourceType != file.SourceType || EmbyAccountIdentity(account) != file.AccountIdentity {
				return ErrEmbyIdentityAmbiguous
			}
			if err := c.baiduDirectoryMember(ctx, file, nodes, account.GetBaiDuPanClient()); err != nil {
				return err
			}
		}
		for _, candidate := range candidates {
			directory := candidate.directory
			if !embyCandidateRelated(snapshot.Item, directory) || !validEmbyObservedLocalDirectory(file, directory.LocalPath) {
				continue
			}
			rel, err := filepath.Rel(file.LocalRoot, directory.LocalPath)
			if err != nil {
				continue
			}
			remote, ok := embyRemoteDirectory(path.Join(file.RemoteRoot, filepath.ToSlash(rel)))
			if !ok || embyRemoteDirectoriesMatch(file.SourceType, remote, file.RemoteRoot) {
				continue
			}
			for i, node := range nodes {
				if i == 0 || node.Path != remote {
					continue
				}
				root := EmbyFrozenFile{
					SyncPathID: file.SyncPathID, SourceType: file.SourceType, AccountID: file.AccountID,
					AccountIdentity: file.AccountIdentity, LocalRoot: file.LocalRoot, RemoteRoot: file.RemoteRoot,
					RootFileID: file.RootFileID, FileID: node.FileID, ParentID: node.ParentID,
					FileName: path.Base(remote), Path: path.Dir(remote), LocalFilePath: directory.LocalPath,
				}
				scope := EmbyDirectoryScope{Root: root, MediaType: directory.MediaType, ItemID: directory.ItemID, SeasonNumber: directory.SeasonNumber,
					LocalPath: directory.LocalPath, EvidenceKind: candidate.kind, Ancestors: append([]EmbyDirectoryAncestor(nil), nodes[:i]...)}
				if !validEmbyDirectoryScope(scope) {
					continue
				}
				key := EmbyDirectoryScopeKey(scope)
				if !keys[key] {
					keys[key] = true
					snapshot.DirectoryScopes = append(snapshot.DirectoryScopes, scope)
				}
			}
		}
	}
	slices.SortFunc(snapshot.DirectoryScopes, func(a, b EmbyDirectoryScope) int {
		return strings.Compare(EmbyDirectoryScopeKey(a), EmbyDirectoryScopeKey(b))
	})
	return nil
}

func validEmbyObservedLocalDirectory(file EmbyFrozenFile, directory string) bool {
	return filepath.IsAbs(directory) && filepath.Clean(directory) == directory &&
		embyPathWithin(file.LocalRoot, directory) && directory != file.LocalRoot &&
		embyPathWithin(directory, file.LocalFilePath) && directory != file.LocalFilePath
}

func embyCandidateRelated(item EmbyMediaItem, candidate EmbyMediaDirectory) bool {
	switch candidate.MediaType {
	case "Movie":
		return item.Type == "Movie" && candidate.ItemID == item.ItemId
	case "Season":
		return (item.Type == "Episode" || item.Type == "Video") && candidate.ItemID == item.SeasonId
	case "Series":
		return (item.Type == "Episode" || item.Type == "Video") && candidate.ItemID == item.SeriesId
	default:
		return false
	}
}

func embyScrapedMovieDirectory(ctx context.Context, file EmbyFrozenFile, itemID string) (*EmbyMediaDirectory, error) {
	// 只有成功整理后的输出记录能证明 Movie 容器；tmdb 相同、dirname 或孤立视频都不够。
	var outputs []Media
	if err := db.Db.WithContext(ctx).Table("media AS m").Select("m.*").
		Joins("JOIN scrape_paths AS p ON p.id = m.scrape_path_id").
		Where("p.source_type = ? AND p.account_id = ? AND m.media_type = ? AND m.status = ? AND m.video_file_id = ?", file.SourceType, file.AccountID, MediaTypeMovie, MediaStatusRenamed, file.FileID).
		Find(&outputs).Error; err != nil {
		return nil, err
	}
	var candidate *EmbyMediaDirectory
	for _, output := range outputs {
		// 百度的文件和父目录 ID 是路径；旧刮削 fsid 不能为同路径新文件证明目录归属。
		if file.SourceType == SourceTypeBaiduPan && (output.VideoPickCode == "" || output.VideoPickCode != file.PickCode) {
			continue
		}
		if output.PathId == "" || output.PathId != file.ParentID || !embyRemoteDirectoriesMatch(file.SourceType, output.Path, file.Path) || output.VideoFileName != file.FileName {
			continue
		}
		local := filepath.Dir(file.LocalFilePath)
		if candidate != nil && candidate.LocalPath != local {
			return nil, ErrEmbyIdentityAmbiguous
		}
		candidate = &EmbyMediaDirectory{ItemID: itemID, MediaType: "Movie", LocalPath: local}
	}
	return candidate, nil
}

func (c *EmbyDirectoryEvidenceCollector) directoryChain(ctx context.Context, file EmbyFrozenFile) ([]EmbyDirectoryAncestor, error) {
	if c.chains == nil {
		c.chains = map[string]embyObservedDirectoryChain{}
	}
	key := embyDigest([]any{file.SourceType, file.AccountIdentity, file.AccountID, file.ParentID, file.Path})
	if cached, ok := c.chains[key]; ok && time.Since(cached.At) < 30*time.Second {
		return cached.Nodes, cached.Err
	}
	var account Account
	if err := db.Db.WithContext(ctx).First(&account, file.AccountID).Error; err != nil {
		return nil, err
	}
	if account.SourceType != file.SourceType || EmbyAccountIdentity(account) != file.AccountIdentity {
		return nil, ErrEmbyIdentityAmbiguous
	}
	var nodes []EmbyDirectoryAncestor
	var err error
	if file.SourceType == SourceType115 {
		detail, readErr := account.Get115Client().GetFsDetailByCid(ctx, file.ParentID)
		if readErr != nil {
			c.chains[key] = embyObservedDirectoryChain{At: time.Now(), Err: readErr}
			return nil, readErr
		}
		nodes, err = emby115ObservedDirectoryChain(file, detail)
	} else {
		nodes, err = c.baiduDirectoryChain(ctx, file, account.GetBaiDuPanClient())
	}
	if err != nil {
		c.chains[key] = embyObservedDirectoryChain{At: time.Now(), Err: err}
		return nil, err
	}
	c.chains[key] = embyObservedDirectoryChain{Nodes: nodes, At: time.Now()}
	return nodes, nil
}

func emby115ObservedDirectoryChain(file EmbyFrozenFile, detail *v115open.FileDetail) ([]EmbyDirectoryAncestor, error) {
	if detail == nil || detail.FileCategory != v115open.TypeDir || detail.FileId != file.ParentID || !embyDeleteBasename(detail.FileName) || len(detail.Paths) == 0 {
		return nil, ErrEmbyIdentityAmbiguous
	}
	nodes := []EmbyDirectoryAncestor{}
	seen := map[string]bool{}
	currentPath, parentID := "/", ""
	for i, ancestor := range detail.Paths {
		if ancestor.FileId == "" || seen[ancestor.FileId] || i == 0 && ancestor.FileId != "0" || i > 0 && !embyDeleteBasename(ancestor.Name) {
			return nil, ErrEmbyIdentityAmbiguous
		}
		seen[ancestor.FileId] = true
		if i > 0 {
			currentPath = path.Join(currentPath, ancestor.Name)
		}
		nodes = append(nodes, EmbyDirectoryAncestor{FileID: ancestor.FileId, ParentID: parentID, Path: currentPath})
		parentID = ancestor.FileId
	}
	if seen[detail.FileId] || !embyRemoteDirectoriesMatch(file.SourceType, currentPath, detail.Path) || !embyRemoteDirectoriesMatch(file.SourceType, path.Join(currentPath, detail.FileName), file.Path) {
		return nil, ErrEmbyIdentityAmbiguous
	}
	nodes = append(nodes, EmbyDirectoryAncestor{FileID: detail.FileId, ParentID: parentID, Path: path.Join(currentPath, detail.FileName)})
	rootFound := false
	for _, node := range nodes {
		if embyRemoteDirectoriesMatch(file.SourceType, node.Path, file.RemoteRoot) && node.FileID == file.RootFileID {
			rootFound = true
		}
	}
	if !rootFound {
		return nil, ErrEmbyIdentityAmbiguous
	}
	return nodes, nil
}

type embyBaiduDirectoryReader interface {
	GetFileListWithOptions(context.Context, string, int, int32, int32, int32, baidupan.FileListOptions) ([]*baidupan.FileInfo, error)
}

// 百度文件账本的 FileID/ParentID 是路径；必须另以原 fsid 证明成员仍在
// 已读取的父目录中，防止把同路径替代目录绑定到旧媒体。完整当层清单按父目录复用。
func (c *EmbyDirectoryEvidenceCollector) baiduDirectoryMember(ctx context.Context, file EmbyFrozenFile, nodes []EmbyDirectoryAncestor, client embyBaiduDirectoryReader) error {
	fsid, err := strconv.ParseUint(file.PickCode, 10, 64)
	if err != nil || fsid == 0 || len(nodes) == 0 {
		return ErrEmbyIdentityAmbiguous
	}
	parent := nodes[len(nodes)-1]
	if !embyRemoteDirectoriesMatch(file.SourceType, parent.Path, file.Path) {
		return ErrEmbyIdentityAmbiguous
	}
	entries, err := c.baiduDirectoryEntries(ctx, file, parent, client)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.FsId != fsid {
			continue
		}
		if entry.IsDir != 0 || entry.ServerFilename != file.FileName || !embyRemoteDirectoriesMatch(file.SourceType, entry.Path, path.Join(file.Path, file.FileName)) || entry.Size > math.MaxInt64 || int64(entry.Size) != file.FileSize ||
			file.SHA1 != "" && !strings.EqualFold(entry.Md5, file.SHA1) || file.MTime != 0 && (entry.ServerMtime > math.MaxInt64 || int64(entry.ServerMtime) != file.MTime) {
			return ErrEmbyIdentityAmbiguous
		}
		return nil
	}
	return ErrEmbyIdentityAmbiguous
}

func (c *EmbyDirectoryEvidenceCollector) baiduDirectoryEntries(ctx context.Context, file EmbyFrozenFile, parent EmbyDirectoryAncestor, client embyBaiduDirectoryReader) ([]*baidupan.FileInfo, error) {
	if c.baiduFiles == nil {
		c.baiduFiles = map[string]embyObservedBaiduDirectory{}
	}
	key := embyDigest([]any{file.AccountID, file.AccountIdentity, parent.FileID, parent.Path})
	if cached, ok := c.baiduFiles[key]; ok && time.Since(cached.At) < 30*time.Second {
		return cached.Files, cached.Err
	}
	var files []*baidupan.FileInfo
	seenIDs, seenNames := map[uint64]bool{}, map[string]bool{}
	for offset := int32(0); ; offset += embyDeleteListPageSize {
		if offset > math.MaxInt32-embyDeleteListPageSize {
			return nil, ErrEmbyIdentityAmbiguous
		}
		ascending := int32(0)
		entries, err := client.GetFileListWithOptions(ctx, parent.Path, 0, 1, offset, embyDeleteListPageSize, baidupan.FileListOptions{Order: "name", Desc: &ascending})
		if err != nil {
			c.baiduFiles[key] = embyObservedBaiduDirectory{At: time.Now(), Err: err}
			return nil, err
		}
		if entries == nil || len(entries) > embyDeleteListPageSize {
			return nil, ErrEmbyIdentityAmbiguous
		}
		for _, entry := range entries {
			if entry == nil || entry.FsId == 0 || seenIDs[entry.FsId] || seenNames[entry.ServerFilename] || !embyDeleteBasename(entry.ServerFilename) || entry.Path != path.Join(parent.Path, entry.ServerFilename) || entry.IsDir > 1 {
				return nil, ErrEmbyIdentityAmbiguous
			}
			seenIDs[entry.FsId], seenNames[entry.ServerFilename] = true, true
		}
		files = append(files, entries...)
		if len(entries) < embyDeleteListPageSize {
			break
		}
	}
	c.baiduFiles[key] = embyObservedBaiduDirectory{Files: files, At: time.Now()}
	return files, nil
}

func (c *EmbyDirectoryEvidenceCollector) baiduDirectoryChain(ctx context.Context, file EmbyFrozenFile, client embyBaiduDirectoryReader) ([]EmbyDirectoryAncestor, error) {
	directory, valid := embyRemoteDirectory(file.Path)
	if !valid {
		return nil, ErrEmbyIdentityAmbiguous
	}
	nodes := []EmbyDirectoryAncestor{{FileID: "0", Path: "/"}}
	for _, name := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
		if !embyDeleteBasename(name) {
			return nil, ErrEmbyIdentityAmbiguous
		}
		parent := nodes[len(nodes)-1]
		nextPath := path.Join(parent.Path, name)
		cacheKey := embyDigest([]any{file.SourceType, file.AccountIdentity, file.AccountID, nextPath})
		if cached, ok := c.chains[cacheKey]; ok && time.Since(cached.At) < 30*time.Second && len(cached.Nodes) > 0 {
			node := cached.Nodes[len(cached.Nodes)-1]
			if node.ParentID == parent.FileID {
				nodes = append(nodes, node)
				continue
			}
		}
		var found *baidupan.FileInfo
		var observedDirectories []EmbyDirectoryAncestor
		entries, err := c.baiduDirectoryEntries(ctx, file, parent, client)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir == 1 {
				observedDirectories = append(observedDirectories, EmbyDirectoryAncestor{FileID: strconv.FormatUint(entry.FsId, 10), ParentID: parent.FileID, Path: entry.Path})
			}
			if entry.ServerFilename == name {
				if found != nil || entry.IsDir != 1 || entry.Path != nextPath {
					return nil, ErrEmbyIdentityAmbiguous
				}
				found = entry
			}
		}
		if found == nil {
			return nil, ErrEmbyIdentityAmbiguous
		}
		for _, observed := range observedDirectories {
			observedKey := embyDigest([]any{file.SourceType, file.AccountIdentity, file.AccountID, observed.Path})
			c.chains[observedKey] = embyObservedDirectoryChain{Nodes: []EmbyDirectoryAncestor{observed}, At: time.Now()}
		}
		nodes = append(nodes, EmbyDirectoryAncestor{FileID: strconv.FormatUint(found.FsId, 10), ParentID: parent.FileID, Path: nextPath})
		c.chains[cacheKey] = embyObservedDirectoryChain{Nodes: append([]EmbyDirectoryAncestor(nil), nodes...), At: time.Now()}
	}
	return nodes, nil
}

func freezeEmbyScopedMetadata(tx *gorm.DB, snapshot EmbyItemSnapshot, scopes []EmbyDirectoryScope) ([]EmbyScopedFile, error) {
	files := []EmbyScopedFile{}
	if snapshot.Item.SeasonId == "" || snapshot.Item.ParentIndexNumber < 0 {
		return files, nil
	}
	for _, season := range scopes {
		if season.MediaType != "Season" || season.ItemID != snapshot.Item.SeasonId || season.SeasonNumber == nil || *season.SeasonNumber != snapshot.Item.ParentIndexNumber {
			continue
		}
		for _, series := range scopes {
			if series.MediaType != "Series" || series.ItemID != snapshot.Item.SeriesId || !embyPathWithin(series.LocalPath, season.LocalPath) || series.Root.SourceType != season.Root.SourceType || series.Root.AccountID != season.Root.AccountID {
				continue
			}
			name := fmt.Sprintf("season%02d-poster.jpg", snapshot.Item.ParentIndexNumber)
			var candidates []SyncFile
			if err := tx.Where("source_type = ? AND account_id = ? AND file_name = ? AND is_meta = ? AND is_video = ?", series.Root.SourceType, series.Root.AccountID, name, true, false).Find(&candidates).Error; err != nil {
				return nil, err
			}
			for _, candidate := range candidates {
				parentMatches := candidate.ParentId == series.Root.FileID
				if series.Root.SourceType == SourceTypeBaiduPan {
					parentMatches = embyRemoteDirectoriesMatch(series.Root.SourceType, candidate.ParentId, path.Join(series.Root.Path, series.Root.FileName))
				}
				if !embyRemoteDirectoriesMatch(series.Root.SourceType, candidate.Path, path.Join(series.Root.Path, series.Root.FileName)) || !parentMatches || candidate.LocalFilePath != filepath.Join(series.LocalPath, name) {
					continue
				}
				frozen, err := freezeEmbyFile(tx, candidate, "", candidate.LocalFilePath)
				if err != nil {
					return nil, err
				}
				if frozen.Reason == "" {
					files = append(files, EmbyScopedFile{File: frozen, ScopeType: "Season", ScopeItemID: season.ItemID, ScopeDirectoryKey: EmbyDirectoryScopeKey(season)})
				}
			}
		}
	}
	return files, nil
}
