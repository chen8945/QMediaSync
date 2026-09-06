package syncstrm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"golang.org/x/sync/errgroup"
)

// 预取两层 115 目录并入库（写入 SyncFile 表），同时写入 s.sync115.existsPathes 缓存。
func (s *SyncStrm) Preload115Dirs(firstFileId string) error {
	// 查询第一个文件的详情，拿到路径
	firstFile, detailErr := s.SyncDriver.DetailByFileId(s.Context, firstFileId)
	if detailErr != nil {
		s.Sync.Logger.Errorf("查询第一个文件详情失败：file_id=%s，%v", firstFileId, detailErr)
		return detailErr
	}

	depth := s.GetDepth(firstFile.Paths)
	if depth < 1 || depth > 2 {
		s.Sync.Logger.Warnf("计算出的预取目录深度小于 1 或大于 2，无需预取目录")
		return nil
	}

	// 使用 errgroup 管理并发生命周期
	eg, ctx := errgroup.WithContext(s.Context)
	workerCount := int(s.PathWorkerMax) + 2
	if workerCount < 1 {
		workerCount = 1
	}
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
	var pending int64
	enqueue := func(item *pathQueueItem) bool {
		if ctx.Err() != nil {
			return false
		}
		atomic.AddInt64(&pending, 1)
		q.mu.Lock()
		if q.closed {
			q.mu.Unlock()
			if atomic.AddInt64(&pending, -1) == 0 {
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
			s.Sync.Failed(fmt.Sprintf("%s：%v", reason, err))
			return err
		}

		if len(pathItems) == 0 {
			s.Sync.Logger.Infof("路径 %s 下没有子目录", item.Path)
			return nil // 递归终止条件
		}

		for _, pathItem := range pathItems {
			s.Sync.Logger.Infof("查询路径下的子目录：%s", pathItem.Path)
			// 检查是否被排除
			if s.IsExcludePath(pathItem.Path) {
				s.Sync.Logger.Infof("路径 %s 名称被排除，跳过", pathItem.Path)
				s.sync115.excludePathId.Store(pathItem.PathId, true)
				continue
			}
			fileItem := &SyncFileCache{
				FileId:     pathItem.PathId,
				FileName:   filepath.Base(pathItem.Path),
				FileType:   v115open.TypeDir,
				SourceType: models.SourceType115,
				Path:       filepath.ToSlash(filepath.Dir(pathItem.Path)),
				ParentId:   item.PathId,
				MTime:      pathItem.Mtime,
				IsVideo:    false,
				IsMeta:     false,
			}
			fileItem.GetLocalFilePath(s.TargetPath, s.SourcePath) // 生成本地路径缓存
			s.memSyncCache.Insert(fileItem)
			// 更新缓存
			if _, ok := s.sync115.existsPathes.Load(pathItem.PathId); !ok {
				s.sync115.existsPathes.Store(pathItem.PathId, pathItem.Path)
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

	for i := 0; i < workerCount; i++ {
		eg.Go(func() error {
			for {
				item, ok := dequeue()
				if !ok {
					return nil
				}
				if ctx.Err() != nil {
					if atomic.AddInt64(&pending, -1) == 0 {
						closeQueue()
					}
					return nil
				}
				if err := processPath(item); err != nil {
					if atomic.AddInt64(&pending, -1) == 0 {
						closeQueue()
					}
					return err
				}
				if atomic.AddInt64(&pending, -1) == 0 {
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
	return eg.Wait()
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
