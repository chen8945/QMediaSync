package syncstrm

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"golang.org/x/sync/errgroup"
)

// 启动路径队列调度器
func (s *SyncStrm) StartOther() {
	s.Sync.UpdateSubStatus(models.SyncSubStatusProcessNetFileList)

	eg, ctx := errgroup.WithContext(s.Context)
	workerCount := int(s.PathWorkerMax) + 3
	if workerCount < 1 {
		workerCount = 1
	}
	// 使用无界队列，避免 worker 内部入队阻塞导致死锁
	type pathQueue struct {
		mu     sync.Mutex
		cond   *sync.Cond
		items  []pathQueueItem
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
	// 入队：失败时回滚计数
	enqueue := func(item pathQueueItem) bool {
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
	dequeue := func() (pathQueueItem, bool) {
		q.mu.Lock()
		for len(q.items) == 0 && !q.closed {
			q.cond.Wait()
		}
		if len(q.items) == 0 && q.closed {
			q.mu.Unlock()
			return pathQueueItem{}, false
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

	var processPath func(pathQueueItem) error
	processPath = func(pathItem pathQueueItem) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		s.Sync.Logger.Infof("正在处理目录 %s 下的文件列表", pathItem.Path)
		if s.IsExcludePath(pathItem.Path) {
			s.Sync.Logger.Warnf("目录 %s 被排除，跳过它和旗下所有内容", pathItem.Path)
			return nil
		}
		// s.Sync.Logger.Debugf("准备请求 API 接口获取目录下的文件列表, 目录：%s", pathItem.Path)
		// GetNetFileFiles 返回该目录下的子目录和文件列表
		retryCount := 0
		var fileItems []*SyncFileCache
		var err error
	apiloop:
		for {
			fileItems, err = s.SyncDriver.GetNetFileFiles(ctx, pathItem.Path, pathItem.PathId)
			if err != nil {
				if retryCount >= models.SettingsGlobal.OpenlistRetry {
					s.Sync.Logger.Errorf("重试 %d 次后，获取目录 %s 下的文件列表失败：%v", models.SettingsGlobal.OpenlistRetry, pathItem.Path, err)
					select {
					case s.PathErrChan <- err:
					default:
					}
					return err
				} else {
					retryCount++
					s.Sync.Logger.Warnf("获取目录 %s 下的文件列表失败，休息 1 分钟后重试第 1 次：%v", pathItem.Path, err)
					time.Sleep(time.Duration(models.SettingsGlobal.OpenlistRetryDelay) * time.Second)
					continue apiloop
				}
			}
			break apiloop
		}
		if len(fileItems) == 0 {
			s.Sync.Logger.Infof("请求完成，目录 %s 下没有文件，跳过", pathItem.Path)
			return nil
		}
		s.Sync.Logger.Infof("请求完成，目录 %s 下共有 %d 个文件和子目录", pathItem.Path, len(fileItems))
		// 递归处理子目录
		for _, fileItem := range fileItems {
			if s.IsExcludeName(filepath.Base(fileItem.FileName)) {
				s.Sync.Logger.Warnf("文件 %s 被排除，跳过它和其下所有内容", fileItem.FileName)
				continue
			}
			if fileItem.FileType == v115open.TypeDir {
				fileItem.GetLocalFilePath(s.TargetPath, s.SourcePath) // 生成本地路径缓存
				// 放入临时表
				s.memSyncCache.Insert(fileItem)
				// 继续处理该目录下的文件
				subPath := pathQueueItem{
					Path:   fileItem.GetFullRemotePath(),
					PathId: fileItem.GetFileId(),
				}
				s.Sync.Logger.Debugf("发现子目录 %s，准备放入路径队列继续处理", subPath.Path)
				enqueue(subPath)
			} else {
				// 处理文件
				if !s.ValidFile(fileItem) {
					continue
				}
				fileItem.GetLocalFilePath(s.TargetPath, s.SourcePath) // 生成本地路径缓存
				// s.Sync.Logger.Infof("发现文件：%s 文件名：%s", fileItem.LocalFilePath, fileItem.FileName)
				// 放入临时表
				s.memSyncCache.Insert(fileItem)
				// s.Sync.Logger.Infof("文件加入临时表：%s", fileItem.LocalFilePath)
				// 处理文件
				s.processNetFile(fileItem)
				// s.Sync.Logger.Infof("文件处理完成：%s", fileItem.LocalFilePath)
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

	enqueue(pathQueueItem{
		Path:   s.SourcePath,
		PathId: s.SourcePathId,
	})

	if err := eg.Wait(); err != nil {
		s.Sync.Logger.Errorf("路径处理失败：%v", err)
		return
	}
	s.Sync.Logger.Infof("已经遍历了全部目录")
}

// 同步单个文件
func (s *SyncStrm) StartFile() error {
	// 查询文件详情
	file, err := s.SyncDriver.DetailByFileId(s.Context, s.SourcePathId)
	if err != nil {
		s.Sync.Logger.Errorf("获取文件 %s 详情失败：%v", s.SourcePath, err)
		return err
	}
	if !file.IsVideo {
		s.Sync.Logger.Warnf("文件 %s 不是视频文件，跳过", file.FileName)
		return fmt.Errorf("文件 %s 不是视频文件，跳过", file.FileName)
	}
	// file.LocalFilePath = filepath.ToSlash(filepath.Join(s.TargetPath, file.FileName))
	// 验证文件有效性
	if !s.ValidFile(file) {
		s.Sync.Logger.Warnf("文件 %s 无效，跳过", file.FileName)
		return nil
	}
	// 生成 STRM
	return s.processNetFile(file)
}
