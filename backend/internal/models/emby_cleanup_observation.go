package models

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

// EmbyMediaDirectory 是 Emby 实际祖先响应里的媒体目录角色（电影为其父文件夹），尚未取得云端删除能力。
type EmbyMediaDirectory struct {
	ItemID       string `json:"item_id"`
	MediaType    string `json:"media_type"`
	LocalPath    string `json:"local_path"`
	SeasonNumber *int   `json:"season_number,omitempty"`
	EvidenceKind string `json:"evidence_kind,omitempty"`
}

// embyDirectoryCandidate 记录目录角色的证据来源；同一目录两种来源并存时物理祖先优先。
type embyDirectoryCandidate struct {
	directory EmbyMediaDirectory
	kind      string
}

// EmbyDirectoryEvidenceCollector 在单个准备 worker 内复用本地目录账本。
// 不调用网盘；提交时会在事务内重新核对目录身份。
type EmbyDirectoryEvidenceCollector struct {
	roots      map[string]*embyLocalDirectoryView
	strmRoots  map[string][]SyncPath
	pathConfig *EmbyConfig
}

// Enrich 保存可验证的季剧祖先、电影物理父目录或 QMS 电影输出目录；缺少能力时保留原文件快照。
func (c *EmbyDirectoryEvidenceCollector) Enrich(ctx context.Context, snapshot *EmbyItemSnapshot, ancestors []EmbyMediaDirectory) error {
	snapshot.MediaDirectories = nil
	snapshot.preparedSTRMReferences, snapshot.frozenSources = nil, nil
	for _, directory := range ancestors {
		if embyCandidateRelated(snapshot.Item, directory) && validEmbyMediaDirectory(directory) {
			directory.EvidenceKind = "emby_physical_ancestor"
			snapshot.MediaDirectories = append(snapshot.MediaDirectories, directory)
		}
	}
	if db.Db == nil {
		return nil
	}
	if c.pathConfig == nil {
		c.pathConfig = &EmbyConfig{}
		if err := db.Db.WithContext(ctx).First(c.pathConfig).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			c.pathConfig = nil
			return err
		}
	}
	snapshot.pathConfig = c.pathConfig
	files, err := resolveEmbySnapshotFiles(db.Db.WithContext(ctx), *snapshot)
	if err != nil {
		return err
	}
	if err := c.prepareSTRMReferences(ctx, snapshot, files); err != nil {
		return err
	}
	snapshot.DirectoryScopes = nil
	snapshot.cleanupFiles = files
	contexts := files
	if len(snapshot.MediaDirectories) > 0 {
		mapped, err := embyMappedDirectoryContexts(db.Db.WithContext(ctx), *snapshot, files)
		if err != nil {
			return err
		}
		contexts = append(append([]EmbyFrozenFile(nil), files...), mapped...)
	}
	keys := map[string]bool{}
	for _, file := range contexts {
		if file.Reason != "" || file.SourceType != SourceType115 && file.SourceType != SourceTypeBaiduPan {
			continue
		}
		candidates := make([]embyDirectoryCandidate, 0, len(ancestors)+1)
		for _, ancestor := range ancestors {
			candidates = append(candidates, embyDirectoryCandidate{directory: ancestor, kind: "emby_physical_ancestor"})
		}
		if snapshot.Item.Type == "Movie" && file.SyncFileID != 0 {
			movie, err := embyScrapedMovieDirectory(ctx, file, snapshot.Item.ItemId)
			if err != nil {
				return err
			}
			if movie != nil {
				movie.EvidenceKind = "qms_scrape_output"
				snapshot.MediaDirectories = append(snapshot.MediaDirectories, *movie)
				candidates = append(candidates, embyDirectoryCandidate{directory: *movie, kind: "qms_scrape_output"})
			}
		}
		if len(candidates) == 0 {
			continue
		}
		nodes, err := c.directoryChain(ctx, *snapshot, file)
		if err != nil {
			return err
		}
		for _, candidate := range candidates {
			directory := candidate.directory
			if candidate.kind == "emby_physical_ancestor" {
				var valid bool
				directory.LocalPath, valid = embyManagedMediaDirectory(file, directory.LocalPath)
				if !valid {
					continue
				}
			}
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
					Reference: file.Reference, SourceID: file.SourceID,
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
			candidates, err := embyApplyFileCandidates(tx, "scoped-meta:"+embyJSON([]any{series.Root.SourceType, series.Root.AccountID, path.Join(series.Root.Path, series.Root.FileName), name}), tx.Where("source_type = ? AND account_id = ? AND path IN ? AND file_name = ? AND is_meta = ? AND is_video = ?", series.Root.SourceType, series.Root.AccountID, embyDirectoryPathRepresentations(path.Join(series.Root.Path, series.Root.FileName)), name, true, false).Order("id"))
			if err != nil {
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
