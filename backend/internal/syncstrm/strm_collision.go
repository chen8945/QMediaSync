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
	"qmediasync/internal/v115open"

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

// selectLatest115StrmOwners 为每个本地 STRM 路径选择上传时间最新的 115 文件。
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
			"[STRM 路径冲突] 目标 %s 存在 %d 个远端视频：%s，选择上传时间最新的 %s",
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
	eg.SetLimit(int(s.PathWorkerMax))
	for _, file := range files {
		if file == nil || file.FileType == v115open.TypeDir {
			continue
		}
		if file.IsVideo {
			targetPath := filepath.ToSlash(filepath.Clean(file.LocalFilePath))
			if owners[targetPath] != file {
				continue
			}
		}
		eg.Go(func() error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return s.processNetFile(file)
			}
		})
	}
	return eg.Wait()
}

func resolveLatest115StrmOwner(ctx context.Context, syncer *SyncStrm, file *SyncFileCache) (bool, error) {
	if syncer == nil || file == nil || file.SourceType != models.SourceType115 || !file.IsVideo {
		return true, nil
	}
	targetPath := file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath)
	if targetPath == "" {
		return true, nil
	}
	targetPath = filepath.ToSlash(filepath.Clean(targetPath))

	query := db.Db.Model(&models.SyncFile{}).
		Where("sync_path_id = ? AND local_file_path = ? AND is_video = ?", syncer.SyncPathId, targetPath, true)
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

	remoteFiles, err := syncer.SyncDriver.GetNetFileFiles(ctx, file.Path, file.ParentId)
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
		if !candidate.IsVideo {
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
			"[STRM 路径冲突] 目标 %s 选择上传时间最新的 %s，跳过 %s",
			targetPath,
			owner.FileName,
			file.FileName,
		)
	}
	return isOwner, nil
}
