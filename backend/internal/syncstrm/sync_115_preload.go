package syncstrm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"qmediasync/internal/v115open"

	"golang.org/x/sync/errgroup"
)

// 预取两层 115 目录并保存到本轮缓存，同时记录已有目录。
func (s *SyncStrm) Preload115Dirs(firstFileId string) error {
	// 查询第一个文件的详情，拿到路径
	firstFile, detailErr := s.SyncDriver.DetailByFileId(s.Context, firstFileId)
	if detailErr != nil {
		s.Sync.Logger.Errorf("查询第一个文件详情失败：file_id=%s，%v", firstFileId, detailErr)
		return detailErr
	}
	if firstFile == nil {
		return fmt.Errorf("115 首文件详情响应为空")
	}

	depth := s.GetDepth(firstFile.Paths)
	if depth < 1 || depth > 2 {
		s.Sync.Logger.Warnf("计算出的预取目录深度小于 1 或大于 2，无需预取目录")
		return nil
	}

	// 使用 errgroup 管理并发生命周期
	eg, ctx := errgroup.WithContext(s.Context)
	workerCount := max(int(s.PathWorkerMax)+2, 1)
	// 使用无界队列，避免 TryGo 丢任务或递归阻塞
	type pathQueue struct {
		mu     sync.Mutex
		cond   *sync.Cond
		items  []*pathQueueItem
		closed bool
	}
	q := &pathQueue{}
	q.cond = sync.NewCond(&q.mu)
	var closeOnce sync.Once
	closeQueue := func() {
		closeOnce.Do(func() {
			q.mu.Lock()
			q.closed = true
			q.mu.Unlock()
			q.cond.Broadcast()
		})
	}
	var pending atomic.Int64
	enqueue := func(item *pathQueueItem) bool {
		if ctx.Err() != nil {
			return false
		}
		pending.Add(1)
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			if pending.Add(-1) == 0 {
				closeQueue()
			}
			return false
		}
		q.items = append(q.items, item)
		q.mu.Unlock()
		q.cond.Signal()
		return true
	}
	dequeue := func() (*pathQueueItem, bool) {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.items) == 0 && q.closed {
			q.mu.Unlock()
			return nil, false
		}
		item := q.items[0]
		q.items = q.items[1:]
		q.mu.Unlock()
		return item, true
	}
	go func() {
		<-ctx.Done()
		closeQueue()
	}()

	// 递归函数，优雅地处理树状目录结构
	var processPath func(*pathQueueItem) error
	processPath = func(item *pathQueueItem) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		pathItems, err := s.list115Directories(ctx, item.Path, item.PathId)
		if err != nil {
			reason := "查询路径下的子目录失败"
			s.Sync.Logger.Warnf("%s：%v", reason, err)
			s.recordScanFailure(item.Path, err)
			if isFatalSyncError(err) {
				return err
			}
			return nil
		}

		if err := s.publish115DirectoryList(ctx, item.Path, item.PathId, pathItems); err != nil {
			s.recordScanFailure(item.Path, err)
			s.sync115.pathMu.Lock()
			conflict := s.sync115.pathConflict
			s.sync115.pathMu.Unlock()
			if conflict {
				s.recordScanFailure(s.SourcePath, err)
			}
			if isFatalSyncError(err) {
				return err
			}
			return nil
		}
		for _, pathItem := range pathItems {
			if err := ctx.Err(); err != nil {
				return err
			}
			if s.IsExcludePath(pathItem.Path) {
				continue
			}

			// 递归处理子目录，errgroup 自动管理并发
			if item.Depth < depth-1 {
				subItem := &pathQueueItem{
					Path:   pathItem.Path,
					PathId: pathItem.PathId,
					Depth:  item.Depth + 1,
				}
				s.Sync.Logger.Infof("预取 115 目录，递归处理子目录：%s", subItem.Path)
				enqueue(subItem)
			}
		}

		return nil
	}

	for range workerCount {
		eg.Go(func() error {
			for {
				item, ok := dequeue()
				if !ok {
					return nil
				}
				if ctx.Err() != nil {
					if pending.Add(-1) == 0 {
						closeQueue()
					}
					return nil
				}
				if err := processPath(item); err != nil {
					if pending.Add(-1) == 0 {
						closeQueue()
					}
					return err
				}
				if pending.Add(-1) == 0 {
					closeQueue()
				}
			}
		})
	}

	// 启动根目录处理
	enqueue(&pathQueueItem{
		Path:   s.SourcePath,
		PathId: s.SourcePathId,
		Depth:  0,
	})

	// 统一等待所有任务并处理错误
	if err := eg.Wait(); err != nil {
		return err
	}
	return s.Context.Err()
}

func (s *SyncStrm) GetDepth(pathes []v115open.FileDetailPath) uint {
	pathArr := []string{}
	for _, p := range pathes {
		if p.FileId == "0" {
			continue
		}
		pathArr = append(pathArr, p.Name)
	}
	firstFilePath := filepath.Join(pathArr...)
	depth := uint(1)
	relPath, relErr := filepath.Rel(s.SourcePath, firstFilePath)
	if relErr != nil {
		s.Sync.Logger.Warnf("计算相对路径失败：%v", relErr)
	}
	// 用分隔符分割 relPath，计算深度
	parts := strings.Split(relPath, string(os.PathSeparator))
	depth = uint(len(parts))
	s.Sync.Logger.Infof("计算预取目录深度为 %s => %s，%d", firstFilePath, relPath, depth)
	return depth
}

func (s *SyncStrm) list115Directories(ctx context.Context, parentPath, parentID string) ([]pathQueueItem, error) {
	if driver, ok := s.SyncDriver.(interface {
		GetDirsByPath(context.Context, string, string) ([]pathQueueItem, error)
	}); ok {
		return driver.GetDirsByPath(ctx, normalizeStrmRemotePath(parentPath), parentID)
	}
	return s.SyncDriver.GetDirsByPathId(ctx, parentID)
}

// publish115DirectoryList 保存过滤前的直接子目录；缺页和无效身份不能提供缺席证据。
func (s *SyncStrm) publish115DirectoryList(ctx context.Context, parentPath, parentID string, items []pathQueueItem) error {
	parentPath = normalizeStrmRemotePath(parentPath)
	facts := make(map[string]verified115Path, len(items)+1)
	for _, item := range items {
		cleanPath := filepath.ToSlash(filepath.Clean(item.Path))
		if item.PathId == "" || item.PathId == "0" || item.PathId == parentID || item.Path == "" ||
			strings.ContainsAny(item.Path, "\\\x00") || strings.Contains(item.Path, "**") || cleanPath != item.Path ||
			normalizeStrmRemotePath(filepath.Dir(cleanPath)) != parentPath {
			return fmt.Errorf("115 子目录身份或路径不完整：%s", parentPath)
		}
		if _, duplicate := facts[item.PathId]; duplicate {
			return fmt.Errorf("115 子目录身份重复：%s", parentPath)
		}
		facts[item.PathId] = verified115Path{path: strings.TrimPrefix(cleanPath, "/"), parentID: parentID, within: true, mtime: item.Mtime}
	}
	rootPath := strings.TrimPrefix(parentPath, "/")
	if rootPath == "" {
		rootPath = "/"
	}
	facts[parentID] = verified115Path{path: rootPath, within: true, root: parentID == s.SourcePathId}
	if err := s.publish115Paths(ctx, facts); err != nil {
		return err
	}
	s.sync115.pathMu.Lock()
	defer s.sync115.pathMu.Unlock()
	for id, fact := range s.sync115.paths {
		if id != parentID && normalizeStrmRemotePath(filepath.Dir(fact.path)) == parentPath {
			if _, listed := facts[id]; !listed {
				s.sync115.pathConflict = true
				return fmt.Errorf("115 完整子目录列表与本轮目录位置冲突：%s", parentPath)
			}
		}
	}
	if s.sync115.completeDirs == nil {
		s.sync115.completeDirs = make(map[string]bool)
	}
	s.sync115.completeDirs[parentPath] = true
	return nil
}
