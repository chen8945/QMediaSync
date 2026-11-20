package syncstrm

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	v115open "qmediasync/internal/v115/openapi"

	"golang.org/x/sync/errgroup"
)

const strmTargetLockCount = 64

var strmTargetLocks [strmTargetLockCount]sync.Mutex

func lockStrmTarget(targetPath string) func() {
	const (
		fnvOffset32 = uint32(2166136261)
		fnvPrime32  = uint32(16777619)
	)
	hash := fnvOffset32
	for i := 0; i < len(targetPath); i++ {
		hash ^= uint32(targetPath[i])
		hash *= fnvPrime32
	}
	lock := &strmTargetLocks[hash%strmTargetLockCount]
	lock.Lock()
	return lock.Unlock
}

// selectLatest115StrmOwners 为每个本地 STRM 路径选择修改时间最新的 115 文件。
func selectLatest115StrmOwners(files []*SyncFileCache) map[string]*SyncFileCache {
	owners := make(map[string]*SyncFileCache)
	for _, file := range files {
		if file == nil || file.SourceType != models.SourceType115 || !file.IsVideo || file.LocalFilePath == "" {
			continue
		}
		targetPath := filepath.ToSlash(filepath.Clean(file.LocalFilePath))
		current := owners[targetPath]
		if current == nil || isNewer115StrmCandidate(file, current) {
			owners[targetPath] = file
		}
	}
	return owners
}

func isNewer115StrmCandidate(candidate, current *SyncFileCache) bool {
	if candidate.MTime != current.MTime {
		return candidate.MTime > current.MTime
	}
	return candidate.GetFileId() > current.GetFileId()
}

// process115CollectedFiles 在 115 文件和路径收集完成后统一处理文件。
func (s *SyncStrm) process115CollectedFiles() error {
	files := s.memSyncCache.ListAllFiles()
	// 合法名称不含 NUL，避免把 .mkv 等空 basename 误当成名称未知。
	const unknownName = "\x00"
	unknownByParent := make(map[string]map[string]bool)
	protectCandidate := func(file *SyncFileCache) {
		name := unknownName
		if valid115FileName(file.FileName) {
			name = strings.TrimSuffix(file.FileName, filepath.Ext(file.FileName))
		}
		if unknownByParent[file.ParentId] == nil {
			unknownByParent[file.ParentId] = make(map[string]bool)
		}
		unknownByParent[file.ParentId][name] = true
	}
	if s.sync115 != nil {
		for _, file := range s.sync115.unresolvedFiles {
			s.scanMu.Lock()
			reason := s.scanFailures["file:"+file.GetFileId()].Reason
			s.scanMu.Unlock()
			remotePath := s.SourcePath
			s.sync115.pathMu.Lock()
			parent, known := s.sync115.paths[file.ParentId]
			if !known {
				parent, known = s.sync115.historicalPaths[file.ParentId]
			}
			s.sync115.pathMu.Unlock()
			if known && parent.within {
				remotePath = parent.path
			}
			s.recordScanFailure(remotePath, fmt.Errorf("115 异常条目的旧文件与账本保留：%s", reason))
			if !s.IsExcludeName(file.FileName) && file.FileSize >= s.GetMinVideoSize() && (file.FileName == "" || s.IsValidVideoExt(file.FileName)) {
				protectCandidate(file)
			}
		}
	}
	videoCount := 0
	for _, file := range files {
		if file != nil && file.IsVideo {
			videoCount++
			if file.Path == "" {
				protectCandidate(file)
			}
		}
	}
	owners := selectLatest115StrmOwners(files)
	conflicts := make(map[string][]*SyncFileCache)
	for _, file := range files {
		if file == nil || file.SourceType != models.SourceType115 || !file.IsVideo || file.LocalFilePath == "" {
			continue
		}
		targetPath := filepath.ToSlash(filepath.Clean(file.LocalFilePath))
		conflicts[targetPath] = append(conflicts[targetPath], file)
	}

	for targetPath, candidates := range conflicts {
		owner := owners[targetPath]
		if owner == nil {
			continue
		}
		s.memSyncCache.SetLocalPathOwner(targetPath, owner)
		if len(candidates) <= 1 {
			continue
		}
		names := make([]string, 0, len(candidates))
		for _, candidate := range candidates {
			names = append(names, candidate.FileName)
		}
		sort.Strings(names)
		s.Sync.Logger.Warnf(
			"[STRM 路径冲突] 目标 %s 存在 %d 个远端视频：%s，选择修改时间最新的 %s",
			targetPath,
			len(candidates),
			strings.Join(names, "、"),
			owner.FileName,
		)
	}

	sort.Slice(files, func(i, j int) bool {
		left := files[i]
		right := files[j]
		if left.LocalFilePath != right.LocalFilePath {
			return left.LocalFilePath < right.LocalFilePath
		}
		return left.GetFileId() < right.GetFileId()
	})

	eg, ctx := errgroup.WithContext(s.Context)
	eg.SetLimit(max(1, int(s.PathWorkerMax)))
	blockedVideos, unresolvedPaths := 0, 0
	hasUnknownCandidates := len(unknownByParent) > 0
	for _, file := range files {
		if ctx.Err() != nil {
			break
		}
		if file == nil || file.FileType == v115open.TypeDir {
			continue
		}
		if file.Path == "" {
			// 没有可信路径，不能向目标根写入或把文件当作已处理。
			unresolvedPaths++
			continue
		}
		if file.IsVideo {
			if (s.sync115 != nil && s.sync115.incompletePages) || s.rootEvidenceConflict {
				continue
			}
			if hasUnknownCandidates {
				name := strings.TrimSuffix(file.FileName, filepath.Ext(file.FileName))
				if unknownByParent[""][unknownName] || unknownByParent[""][name] || unknownByParent[file.ParentId][unknownName] || unknownByParent[file.ParentId][name] {
					// 父目录可信时只保护本目录；父目录未知时保留跨目录同名保护。
					blockedVideos++
					continue
				}
			}
			targetPath := filepath.ToSlash(filepath.Clean(file.LocalFilePath))
			if owners[targetPath] != file {
				s.recordFileSkipped(file)
				continue
			}
		}
		eg.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				if err := s.processNetFile(file); err != nil {
					s.recordFileFailure(file, err)
					if isFatalSyncError(err) {
						return err
					}
					return nil
				}
				s.recordFileSuccess(file)
				return nil
			}
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}
	if s.sync115 != nil && s.sync115.pathConflict || s.rootEvidenceConflict {
		s.Sync.Logger.Warnf("115 目录位置冲突，本轮不应用收集的路径，暂停生成与元数据处理，缺项清理未执行")
	} else if s.sync115 != nil && s.sync115.incompletePages {
		s.Sync.Logger.Warnf("115 文件分页不完整，本轮视频生成暂停（已发现 %d 个视频）；可信元数据继续处理，缺项清理未执行", videoCount)
	} else if blockedVideos > 0 {
		s.Sync.Logger.Warnf("115 候选归属未确认，暂停 %d 个已发现视频的生成并保留既有 STRM；其他已确认文件继续处理", blockedVideos)
	}
	if unresolvedPaths > 0 {
		s.Sync.Logger.Warnf("115 有 %d 个已发现文件缺少可信路径，保留受影响范围的旧文件与账本", unresolvedPaths)
	}
	return s.Context.Err()
}

func resolveLatest115StrmOwner(ctx context.Context, syncer *SyncStrm, file *SyncFileCache) (bool, error) {
	return resolveLatest115StrmOwnerWithDirectory(ctx, syncer, file, &generationDirectory{})
}

func resolveLatest115StrmOwnerWithDirectory(ctx context.Context, syncer *SyncStrm, file *SyncFileCache, directory *generationDirectory) (bool, error) {
	if syncer == nil || file == nil || file.SourceType != models.SourceType115 || !file.IsVideo {
		return true, nil
	}
	if syncer.generationSkipReason(file) != "" {
		return false, nil
	}
	targetPath := file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath)
	if targetPath == "" {
		return true, nil
	}
	targetPath = filepath.ToSlash(filepath.Clean(targetPath))

	query := db.Db.Model(&models.SyncFile{}).
		Where("sync_path_id = ? AND is_video = ?", syncer.SyncPathId, true)
	if filepath.Separator == '\\' {
		query = query.Where("local_file_path IN ?", []string{targetPath, filepath.FromSlash(targetPath)})
	} else {
		query = query.Where("local_file_path = ?", targetPath)
	}
	if fileID := file.GetFileId(); fileID != "" {
		query = query.Where("file_id <> ?", fileID)
	} else if file.PickCode != "" {
		query = query.Where("pick_code <> ?", file.PickCode)
	}
	var conflictCount int64
	if err := query.Count(&conflictCount).Error; err != nil {
		return false, fmt.Errorf("查询同路径 STRM 候选失败：%w", err)
	}
	if conflictCount == 0 {
		return true, nil
	}
	if syncer.SyncDriver == nil {
		return false, fmt.Errorf("检查同路径 STRM 冲突失败：同步驱动为空")
	}

	// 同名归属仍读取当前目录，不能用上一项的快照决定跳过或源文件清理。
	remoteFiles, err := directory.listFresh(ctx, syncer, file)
	if err != nil {
		return false, fmt.Errorf("检查同路径 STRM 冲突失败：%w", err)
	}
	candidates := make([]*SyncFileCache, 0, len(remoteFiles)+1)
	candidates = append(candidates, file)
	seen := map[string]struct{}{file.GetFileId(): {}}
	for _, candidate := range remoteFiles {
		if candidate == nil || candidate.FileType == v115open.TypeDir {
			continue
		}
		if candidate.SourceType == "" {
			candidate.SourceType = models.SourceType115
		}
		if candidate.ParentId == "" {
			candidate.ParentId = file.ParentId
		}
		if candidate.Path == "" {
			candidate.Path = file.Path
		}
		candidate.IsVideo = syncer.IsValidVideoExt(candidate.FileName)
		if !candidate.IsVideo || syncer.generationSkipReason(candidate) != "" {
			continue
		}
		candidateTargetPath := candidate.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath)
		if filepath.ToSlash(filepath.Clean(candidateTargetPath)) != targetPath {
			continue
		}
		candidateID := candidate.GetFileId()
		if _, ok := seen[candidateID]; ok {
			continue
		}
		seen[candidateID] = struct{}{}
		candidates = append(candidates, candidate)
	}

	owner := selectLatest115StrmOwners(candidates)[targetPath]
	if owner == nil {
		return true, nil
	}
	isOwner := owner.GetFileId() == file.GetFileId()
	if !isOwner {
		syncer.Sync.Logger.Warnf(
			"[STRM 路径冲突] 目标 %s 选择修改时间最新的 %s，跳过 %s",
			targetPath,
			owner.FileName,
			file.FileName,
		)
	}
	return isOwner, nil
}
