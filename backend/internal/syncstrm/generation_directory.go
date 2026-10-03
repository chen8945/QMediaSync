package syncstrm

import (
	"context"
	"path"
	"slices"
	"strings"
	"time"

	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// generationDirectory 保留本项列表，并按需复用当前小批的元数据列表。
type generationDirectory struct {
	batch    *generationDirectories
	started  time.Time
	version  uint64
	idle     bool
	syncer   *SyncStrm
	path     string
	parentID string
	files    []*SyncFileCache
}

func (directory *generationDirectory) list(ctx context.Context, syncer *SyncStrm, file *SyncFileCache) ([]*SyncFileCache, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reusable := file.SourceType == models.SourceType115 && file.Path != "" && file.ParentId != ""
	version, idle := v115open.DirectoryReadVersion(generationDirectoryCacheKey(syncer, file).accountID, file.ParentId)
	localValid := directory.batch == nil || (idle && directory.idle && version == directory.version && directory.batch.now().Sub(directory.started) < 5*time.Second)
	if reusable && localValid && directory.syncer == syncer && directory.path == file.Path && directory.parentID == file.ParentId {
		return cloneGenerationDirectory(directory.files), nil
	}
	if reusable && directory.batch != nil {
		if files, ok := directory.batch.get(syncer, file); ok {
			directory.syncer, directory.path, directory.parentID, directory.files = syncer, file.Path, file.ParentId, files
			entry := directory.batch.entries[generationDirectoryCacheKey(syncer, file)]
			directory.started, directory.version, directory.idle = entry.started, entry.version, true
			return cloneGenerationDirectory(files), nil
		}
	}
	return directory.listFresh(ctx, syncer, file)
}

func (directory *generationDirectory) listFresh(ctx context.Context, syncer *SyncStrm, file *SyncFileCache) ([]*SyncFileCache, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory.syncer, directory.files = nil, nil
	reusable := file.SourceType == models.SourceType115 && file.Path != "" && file.ParentId != ""
	started := time.Now()
	if directory.batch != nil {
		started = directory.batch.now()
		directory.batch.remove(syncer, file)
	}
	version, idle := v115open.DirectoryReadVersion(generationDirectoryCacheKey(syncer, file).accountID, file.ParentId)
	files, err := syncer.SyncDriver.GetNetFileFiles(ctx, file.Path, file.ParentId)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, item := range files {
		if item != nil && ((item.Path != "" && item.Path != file.Path) || (item.ParentId != "" && item.ParentId != file.ParentId)) {
			reusable = false
		}
	}
	if reusable {
		// 115 驱动确认所有分页成功后才返回；保存副本，避免 owner 和元数据匹配互相修改。
		directory.syncer = syncer
		directory.path = file.Path
		directory.parentID = file.ParentId
		directory.files = cloneGenerationDirectory(files)
		directory.started, directory.version, directory.idle = started, version, idle
		if directory.batch != nil {
			endVersion, endIdle := v115open.DirectoryReadVersion(generationDirectoryCacheKey(syncer, file).accountID, file.ParentId)
			if idle && endIdle && version == endVersion && directory.batch.now().Sub(started) < 5*time.Second && generationDirectoryContains(files, file) {
				directory.batch.put(syncer, file, files, started, version)
			}
		}
		return cloneGenerationDirectory(directory.files), nil
	}
	return files, nil
}

func cloneGenerationDirectory(files []*SyncFileCache) []*SyncFileCache {
	clones := make([]*SyncFileCache, len(files))
	for i, file := range files {
		if file != nil {
			clone := *file
			clone.Paths = slices.Clone(file.Paths)
			clones[i] = &clone
		}
	}
	return clones
}

// 只由当前小批的串行消费者使用，不持有同步器或请求 context。
type generationDirectories struct {
	now     func() time.Time
	entries map[generationDirectoryKey]generationDirectoryEntry
}
type generationDirectoryKey struct {
	generationConfigKey
	parentID, remote, sourcePath, targetPath string
}
type generationDirectoryEntry struct {
	files   []*SyncFileCache
	started time.Time
	version uint64
}

func generationDirectoryCacheKey(s *SyncStrm, f *SyncFileCache) generationDirectoryKey {
	var accountID uint
	if s.Account != nil {
		accountID = s.Account.ID
	}
	return generationDirectoryKey{generationConfigKey{f.SourceType, accountID, s.SyncPathId}, f.ParentId, path.Clean("/" + f.Path), s.SourcePath, s.TargetPath}
}
func (b *generationDirectories) remove(s *SyncStrm, f *SyncFileCache) {
	key := generationDirectoryCacheKey(s, f)
	// 同一父目录换位置，或同一位置换身份，都不能找回旧列表。
	for old := range b.entries {
		if old.generationConfigKey == key.generationConfigKey && (old.parentID == key.parentID || old.remote == key.remote) {
			delete(b.entries, old)
		}
	}
}

// observe 也检查随后被过滤的任务，已知新位置不能留到下一项继续用。
func (b *generationDirectories) observe(s *SyncStrm, f *SyncFileCache) {
	key := generationDirectoryCacheKey(s, f)
	for old, entry := range b.entries {
		if old.source != key.source || old.accountID != key.accountID {
			continue
		}
		related := old.parentID == key.parentID || old.remote == key.remote
		for _, candidate := range entry.files {
			if candidate != nil && f.GetFileId() != "" && candidate.GetFileId() == f.GetFileId() {
				related = true
				break
			}
		}
		if related && (old != key || !generationDirectoryContains(entry.files, f)) {
			delete(b.entries, old)
		}
	}
}

func (b *generationDirectories) get(s *SyncStrm, f *SyncFileCache) ([]*SyncFileCache, bool) {
	entry, ok := b.entries[generationDirectoryCacheKey(s, f)]
	version, idle := v115open.DirectoryReadVersion(generationDirectoryCacheKey(s, f).accountID, f.ParentId)
	if !ok || !idle || version != entry.version || b.now().Sub(entry.started) >= 5*time.Second || !generationDirectoryContains(entry.files, f) {
		b.remove(s, f)
		return nil, false
	}
	return cloneGenerationDirectory(entry.files), true
}
func (b *generationDirectories) put(s *SyncStrm, f *SyncFileCache, files []*SyncFileCache, started time.Time, version uint64) {
	if b.entries == nil {
		b.entries = make(map[generationDirectoryKey]generationDirectoryEntry)
	}
	b.entries[generationDirectoryCacheKey(s, f)] = generationDirectoryEntry{cloneGenerationDirectory(files), started, version}
}
func generationDirectoryContains(files []*SyncFileCache, file *SyncFileCache) bool {
	for _, candidate := range files {
		if candidate == nil || file.GetFileId() == "" || candidate.GetFileId() != file.GetFileId() {
			continue
		}
		return candidate.FileName == file.FileName && candidate.FileSize == file.FileSize &&
			(file.MTime == 0 || candidate.MTime == file.MTime) &&
			(file.Sha1 == "" || strings.EqualFold(candidate.Sha1, file.Sha1)) &&
			(file.PickCode == "" || candidate.PickCode == file.PickCode) &&
			(candidate.ParentId == "" || candidate.ParentId == file.ParentId) &&
			(candidate.Path == "" || path.Clean("/"+candidate.Path) == path.Clean("/"+file.Path))
	}
	return false
}
