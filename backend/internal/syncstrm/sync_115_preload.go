package syncstrm

import (
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

		pathItems, err := s.SyncDriver.GetDirsByPathId(ctx, item.PathId)
		if err != nil {
			reason := "查询路径下的子目录失败"
			s.Sync.Logger.Warnf("%s：%v", reason, err)
			s.recordScanFailure(item.Path, err)
			if isFatalSyncError(err) {
				return err
			}
			return nil
		}

		if len(pathItems) == 0 {
			s.Sync.Logger.Infof("路径 %s 下没有子目录", item.Path)
			return nil // 递归终止条件
		}

		for _, pathItem := range pathItems {
			if err := ctx.Err(); err != nil {
				return err
			}
			s.Sync.Logger.Infof("查询路径下的子目录：%s", pathItem.Path)
			cleanPath := filepath.ToSlash(filepath.Clean(pathItem.Path))
			parentPath := filepath.ToSlash(filepath.Clean(item.Path))
			if pathItem.PathId == "" || pathItem.PathId == "0" || pathItem.PathId == item.PathId ||
				pathItem.Path == "" || strings.ContainsAny(pathItem.Path, "\\") || strings.Contains(pathItem.Path, "**") ||
				cleanPath != pathItem.Path ||
				!strmRemotePathWithin(cleanPath, parentPath) ||
				normalizeStrmRemotePath(filepath.ToSlash(filepath.Dir(cleanPath))) != normalizeStrmRemotePath(parentPath) {
				s.recordScanFailure(item.Path, fmt.Errorf("115 预取目录身份或路径不完整"))
				continue
			}
			// 完整列表成功后才共享位置；排除文件留到所有路径请求结束后处理。
			fact := verified115Path{path: strings.TrimPrefix(cleanPath, "/"), parentID: item.PathId, within: true, mtime: pathItem.Mtime}
			facts := map[string]verified115Path{pathItem.PathId: fact}
			if item.PathId == s.SourcePathId {
				rootPath := strings.TrimPrefix(parentPath, "/")
				if rootPath == "" {
					rootPath = "/"
				}
				facts[item.PathId] = verified115Path{path: rootPath, within: true, root: true}
			}
			if err := s.publish115Paths(ctx, facts); err != nil {
				s.recordScanFailure(s.SourcePath, err)
				if isFatalSyncError(err) {
					return err
				}
				continue
			}
			if s.IsExcludePath(fact.path) {
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
