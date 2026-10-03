package syncstrm

import (
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"context"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
)

// 115 文件路径处理器
// 查询同步缓存中 Path 为空的记录，通过 115 接口补全路径后处理该路径下的所有文件
func (s *SyncStrm) Start115PathDispathcer() error {
	// 使用 errgroup 管理并发
	eg, ctx := errgroup.WithContext(s.Context)
	eg.SetLimit(max(1, int(s.PathWorkerMax)))
	// 先找到所有路径为空的目录 ID，去重
	parentIds := make(map[string]bool)
	c := s.memSyncCache.Count()
	if c == 0 {
		s.Sync.Logger.Infof("同步缓存中没有文件记录需要处理")
		return s.apply115Paths()
	}
	fileItems := s.memSyncCache.GetAllFile()
	for _, item := range fileItems {
		if item.FileType == v115open.TypeDir || item.Path != "" {
			continue
		}
		parentIds[item.ParentId] = true
	}
	historicalParents := make(map[string]bool)
	reusable := 0
	s.sync115.pathMu.Lock()
	for id := range parentIds {
		_, historicalParents[id] = s.sync115.historicalPaths[id]
		if _, current := s.sync115.paths[id]; current || historicalParents[id] {
			reusable++
		}
	}
	s.sync115.pathMu.Unlock()
	// 将路径 ID 加入任务队列
	s.Sync.Logger.Infof("开始路径补全任务，文件父目录 %d 个，可复用 %d 个，待查询 %d 个", len(parentIds), reusable, len(parentIds)-reusable)
	for pathId := range parentIds {
		if ctx.Err() != nil {
			break
		}
		currentPathId := pathId
		eg.Go(func() error {
			if err := s.process115Path(ctx, currentPathId); err != nil {
				// 目录 ID 不能证明它位于哪个子树，旧缓存也不作为本轮路径证据。
				s.recordScanFailure(s.SourcePath, fmt.Errorf("115 目录 %s 路径未决：%w", currentPathId, err))
				if isFatalSyncError(err) {
					return err
				}
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return err
	}
	// 初次跳过的历史路径可能被其他详情中的移动信息失效。
	for {
		resolved := false
		for id, historical := range historicalParents {
			if !historical {
				continue
			}
			_, current := s.sync115.paths[id]
			_, retained := s.sync115.historicalPaths[id]
			if current || retained {
				continue
			}
			delete(historicalParents, id)
			resolved = true
			if err := s.process115Path(s.Context, id); err != nil {
				s.recordScanFailure(s.SourcePath, fmt.Errorf("115 目录 %s 路径未决：%w", id, err))
				if isFatalSyncError(err) {
					return err
				}
			}
		}
		if !resolved {
			break
		}
	}
	if err := s.Context.Err(); err != nil {
		return err
	}
	if err := s.apply115Paths(); err != nil {
		return err
	}
	s.Sync.Logger.Infof("结束路径补全任务")
	return nil
}

func (s *SyncStrm) process115Path(ctx context.Context, pathId string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.sync115.pathMu.Lock()
	_, historical := s.sync115.historicalPaths[pathId]
	conflict := s.sync115.pathConflict
	s.sync115.pathMu.Unlock()
	if historical && !conflict {
		return nil
	}
	return s.confirm115Path(ctx, pathId, "")
}

func (s *SyncStrm) confirm115Path(ctx context.Context, pathId, expectedPath string) error {
	select {
	case <-ctx.Done():
		// 上下文取消，退出循环
		return ctx.Err()
	default:
	}
	// 获得 worker 执行位置后重查，其他详情可能已经给出了这个目录。
	s.sync115.pathMu.Lock()
	knownPath, known := s.sync115.paths[pathId]
	conflict := s.sync115.pathConflict
	s.sync115.pathMu.Unlock()
	if conflict {
		return fmt.Errorf("115 本轮目录位置存在冲突")
	}
	if known {
		if expectedPath != "" && normalizeStrmRemotePath(knownPath.path) != normalizeStrmRemotePath(expectedPath) {
			return fmt.Errorf("115 目录身份与待确认路径不匹配：path_id=%s", pathId)
		}
		return nil
	}
	detail, fsErr := s.SyncDriver.DetailByFileId(ctx, pathId)
	if fsErr != nil {
		return fsErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.confirm115PathDetail(ctx, pathId, expectedPath, detail)
}

func (s *SyncStrm) confirm115PathDetail(ctx context.Context, pathId, expectedPath string, detail *SyncFileCache) error {
	if detail == nil || detail.FileId != pathId || detail.FileType != v115open.TypeDir {
		return fmt.Errorf("115 目录详情身份或类型不匹配：path_id=%s", pathId)
	}
	paths := append(append([]v115open.FileDetailPath(nil), detail.Paths...), v115open.FileDetailPath{
		FileId: detail.FileId,
		Name:   detail.FileName,
	})
	foundRoot := s.SourcePathId == "0"
	seen := make(map[string]struct{})
	for _, part := range paths {
		if part.FileId == "0" {
			continue
		}
		if part.FileId == "" || part.Name == "" || part.Name == "." || part.Name == ".." ||
			strings.ContainsAny(part.Name, "/\\") || strings.Contains(part.Name, "**") {
			return fmt.Errorf("115 目录祖先身份或名称不完整：path_id=%s", pathId)
		}
		if _, duplicate := seen[part.FileId]; duplicate {
			return fmt.Errorf("115 目录祖先身份重复：path_id=%s", pathId)
		}
		seen[part.FileId] = struct{}{}
		foundRoot = foundRoot || part.FileId == s.SourcePathId
	}
	if !foundRoot {
		return fmt.Errorf("115 目录不在本轮同步根内：path_id=%s", pathId)
	}
	facts := make(map[string]verified115Path, len(paths))
	fullPath, parentID := "", "0"
	within := s.SourcePathId == "0"
	for _, part := range paths {
		if part.FileId == "0" {
			continue
		}
		fullPath = filepath.ToSlash(filepath.Join(fullPath, part.Name))
		isRoot := part.FileId == s.SourcePathId
		within = within || isRoot
		facts[part.FileId] = verified115Path{path: fullPath, parentID: parentID, within: within, root: isRoot}
		parentID = part.FileId
	}
	if pathId == "0" {
		facts["0"] = verified115Path{path: "/", within: true, root: true}
	}
	if expectedPath != "" && normalizeStrmRemotePath(facts[pathId].path) != normalizeStrmRemotePath(expectedPath) {
		return fmt.Errorf("115 目录详情与待确认路径不匹配：path_id=%s", pathId)
	}
	return s.publish115Paths(ctx, facts)
}

// 每个目录只保留完整路径和直接父 ID，不复制整条祖先切片。
// ponytail: 很深的目录会重复保存路径前缀；实测内存不足时再改为按父 ID 拼接。
type verified115Path struct {
	path     string
	parentID string
	within   bool
	root     bool
	mtime    int64
}

func (s *SyncStrm) publish115Paths(ctx context.Context, facts map[string]verified115Path) error {
	s.sync115.pathMu.Lock()
	defer s.sync115.pathMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.sync115.pathConflict {
		return fmt.Errorf("115 本轮目录位置存在冲突")
	}
	for id, fact := range facts {
		if oldID, ok := s.sync115.pathIDs[normalizeStrmRemotePath(fact.path)]; ok && oldID != id {
			s.sync115.pathConflict = true
			return fmt.Errorf("115 目录路径 %s 在本轮返回了不同身份", fact.path)
		}
		if old, ok := s.sync115.paths[id]; ok && (old.path != fact.path || old.parentID != "" && fact.parentID != "" && old.parentID != fact.parentID) {
			s.sync115.pathConflict = true
			return fmt.Errorf("115 目录 %s 在本轮返回了不同位置", id)
		}
	}
	if s.sync115.paths == nil {
		s.sync115.paths = make(map[string]verified115Path)
	}
	if s.sync115.pathIDs == nil {
		s.sync115.pathIDs = make(map[string]string)
	}
	// 新事实覆盖历史身份；已知移动或路径换主使旧后代失效，后续按需重新补全。
	invalidRoots := make([]string, 0)
	for id, fact := range facts {
		if old, ok := s.sync115.historicalPaths[id]; ok &&
			(old.path != fact.path || old.parentID != "" && fact.parentID != "" && old.parentID != fact.parentID) {
			invalidRoots = append(invalidRoots, old.path)
		}
		for historyID, history := range s.sync115.historicalPaths {
			if historyID != id && normalizeStrmRemotePath(history.path) == normalizeStrmRemotePath(fact.path) {
				invalidRoots = append(invalidRoots, history.path)
			}
		}
	}
	for id, history := range s.sync115.historicalPaths {
		if _, current := facts[id]; current {
			delete(s.sync115.historicalPaths, id)
			continue
		}
		for _, root := range invalidRoots {
			if strmRemotePathWithin(history.path, root) {
				delete(s.sync115.historicalPaths, id)
				break
			}
		}
	}
	for id, fact := range facts {
		if old, ok := s.sync115.paths[id]; ok {
			if fact.parentID == "" {
				fact.parentID = old.parentID
			}
			if fact.mtime == 0 {
				fact.mtime = old.mtime
			}
		}
		s.sync115.paths[id] = fact
		s.sync115.pathIDs[normalizeStrmRemotePath(fact.path)] = id
	}
	if s.sync115.pathsApplied && len(invalidRoots) > 0 {
		err := fmt.Errorf("115 已应用文件路径后发现历史目录位置变化，本轮保留同步根并等待后续核对")
		s.recordScanFailure(s.SourcePath, err)
		return err
	}
	return nil
}

// 所有路径请求结束后才补路径或移除排除文件，避免在途响应出现冲突后无法恢复。
func (s *SyncStrm) apply115Paths() error {
	if s.sync115.pathConflict {
		s.recordScanFailure(s.SourcePath, fmt.Errorf("115 目录位置冲突，本轮不使用已取得的路径"))
		return nil
	}
	// 只保留本轮文件引用的历史目录及祖先，供下一轮关联已知移动；无关空目录仍需确认。
	effective := make(map[string]verified115Path, len(s.sync115.paths))
	for _, file := range s.memSyncCache.GetAllFile() {
		if file.FileType == v115open.TypeDir {
			continue
		}
		for id := file.ParentId; id != ""; {
			if _, exists := effective[id]; exists {
				break
			}
			fact, ok := s.sync115.historicalPaths[id]
			if !ok {
				break
			}
			effective[id] = fact
			id = fact.parentID
		}
	}
	for id, fact := range s.sync115.paths {
		effective[id] = fact
	}
	for id, fact := range effective {
		if err := s.Context.Err(); err != nil {
			return err
		}
		if !fact.within {
			continue
		}
		if s.IsExcludePath(fact.path) {
			files, _ := s.memSyncCache.GetByParentId(id)
			for _, file := range files {
				s.recordFileSkipped(file)
			}
			if err := s.memSyncCache.DeleteByParentId(id); err != nil {
				return err
			}
			continue
		}
		if !fact.root {
			if err := s.cache115Directory(id, fact); err != nil {
				return err
			}
		}
		if err := s.memSyncCache.UpdatePathByParentId(id, fact.path, s.TargetPath, s.SourcePath); err != nil {
			return err
		}
	}
	s.sync115.pathMu.Lock()
	s.sync115.pathsApplied = true
	s.sync115.pathMu.Unlock()
	return nil
}

func (s *SyncStrm) cache115Directory(id string, fact verified115Path) error {
	item := &SyncFileCache{FileId: id, FileName: filepath.Base(fact.path), FileType: v115open.TypeDir,
		ParentId: fact.parentID, MTime: fact.mtime, Path: filepath.ToSlash(filepath.Dir(fact.path)), SourceType: models.SourceType115}
	item.GetLocalFilePath(s.TargetPath, s.SourcePath)
	return s.memSyncCache.Insert(item)
}
