package syncstrm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"golang.org/x/sync/errgroup"
)

// 启动 115 文件处理调度器
// 启动 N 个文件处理器，将所有文件查询回来并写入同步缓存
func (s *SyncStrm) Start115FileDispathcer(total int64) error {
	if s.sync115 == nil {
		s.sync115 = &Sync115{}
	}
	s.sync115.expectedTotal = total
	s.sync115.rawFileIDs = make(map[string]struct{})
	s.sync115.incompletePages = false
	limit := models.GetFileListPageSize()
	// 根据文件总数来计算需要多少个处理器
	pageCount := total / int64(limit)
	if total%int64(limit) != 0 {
		pageCount++
	}
	if pageCount == 0 {
		s.Sync.Logger.Infof("无需处理文件列表，总数为 0")
		return nil
	}
	// 取 pageCount 和 s.PathWorkerMax 之间的较小值
	workerMax := max(1, min(pageCount, s.PathWorkerMax))

	// 使用 errgroup 管理并发
	eg, ctx := errgroup.WithContext(s.Context)
	eg.SetLimit(int(workerMax))

	// 加入文件任务
	for page := 0; page < int(pageCount); page++ {
		if ctx.Err() != nil {
			break
		}
		eg.Go(func() error {
			if err := s.process115FilePage(ctx, page, limit); err != nil {
				s.sync115.pageMu.Lock()
				s.sync115.incompletePages = true
				s.sync115.pageMu.Unlock()
				s.recordScanFailure(s.SourcePath, fmt.Errorf("115 文件页 %d 不完整，视频 owner 未决：%w", page+1, err))
				if isFatalSyncError(err) {
					return err
				}
			}
			return nil
		})
	}
	s.Sync.Logger.Infof("所有文件任务已加入队列，等待处理器完成")
	if err := eg.Wait(); err != nil {
		return err
	}
	if err := s.Context.Err(); err != nil {
		return err
	}
	if int64(len(s.sync115.rawFileIDs)) != total && !s.sync115.incompletePages {
		s.sync115.incompletePages = true
		s.recordScanFailure(s.SourcePath, fmt.Errorf("115 文件唯一 ID 数 %d 与预期 %d 不符，视频 owner 未决", len(s.sync115.rawFileIDs), total))
	}
	return nil
}

// validate115FilePage 在任何名称、类型或目录过滤之前核对原始响应。
// 调用方负责串行访问 seen；Count 一致仍不能证明网盘提供事务快照。
func validate115FilePage(files []v115open.File, total int64, offset, limit int, seen map[string]struct{}) error {
	if total < 0 || int64(offset) > total || len(files) != int(min(int64(limit), total-int64(offset))) {
		return fmt.Errorf("115 原始页长异常：offset=%d，limit=%d，count=%d，实际=%d", offset, limit, total, len(files))
	}
	for _, file := range files {
		if file.FileId == "" {
			return fmt.Errorf("115 原始页缺少文件 ID：offset=%d", offset)
		}
		if file.FileName == "" || file.FileName == "." || file.FileName == ".." ||
			filepath.Base(file.FileName) != file.FileName || strings.ContainsAny(file.FileName, "/\\") {
			return fmt.Errorf("115 原始页文件名称不完整：offset=%d，file_id=%s", offset, file.FileId)
		}
		if _, exists := seen[file.FileId]; exists {
			return fmt.Errorf("115 原始页文件 ID 重复：offset=%d，file_id=%s", offset, file.FileId)
		}
		seen[file.FileId] = struct{}{}
	}
	return nil
}

// 115 文件页处理器
func (s *SyncStrm) process115FilePage(ctx context.Context, page, limit int) error {
	// 检查 context 是否已取消
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	offset := page * int(limit)
	s.Sync.Logger.Infof("文件处理器开始处理文件列表，offset=%d，limit=%d", offset, limit)
	// 查询 115 网盘文件
	var files []v115open.File
	var err error
	files, err = s.SyncDriver.GetFilesByPathId(ctx, s.SourcePathId, offset, limit)
	if err != nil {
		s.Sync.Logger.Errorf("获取 115 网盘文件列表失败：目录 ID %s，offset=%d，limit=%d，%v", s.SourcePathId, offset, limit, err)
		return err
	}
	s.sync115.pageMu.Lock()
	err = validate115FilePage(files, s.sync115.expectedTotal, offset, limit, s.sync115.rawFileIDs)
	s.sync115.pageMu.Unlock()
	if err != nil {
		return err
	}
	if page == 0 && s.sync115.firstFileID != "" && files[0].FileId != s.sync115.firstFileID {
		return fmt.Errorf("115 首文件在计数和分页之间变化")
	}
	// 处理查询到的文件
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if file.Pid == "" {
			return fmt.Errorf("115 文件身份或名称不完整：file_id=%s", file.FileId)
		}
		// 处理文件
		// 生成一个临时的 SyncFile
		syncFile := SyncFileCache{
			Path:       "",
			FileId:     file.FileId,
			FileName:   file.FileName,
			SourceType: models.SourceType115,
			ParentId:   file.Pid,
			FileSize:   file.FileSize,
			FileType:   file.FileCategory,
			PickCode:   file.PickCode,
			Sha1:       file.Sha1,
			MTime:      file.ModifiedAt(),
			ThumbUrl:   file.Thumbnail,
		}
		// 验证文件本身，然后入临时表
		if !s.ValidFile(&syncFile) {
			s.recordFileSkipped(&syncFile)
			continue
		}
		// 放入同步缓存
		err := s.memSyncCache.Insert(&syncFile)
		if err != nil {
			s.Sync.Logger.Errorf("文件 %s => %s 插入同步缓存失败：%v", syncFile.FileId, syncFile.FileName, err)
			return err
		}
		// s.Sync.Logger.Infof("文件 %s => %s 插入同步缓存成功，路径 %s", syncFile.FileId, syncFile.FileName, syncFile.LocalFilePath)
		// 文件和路径收集完成后统一处理，避免同 basename 视频竞争同一个 STRM。
	}
	s.Sync.Logger.Infof("文件处理器处理完成，offset=%d，limit=%d，共处理 %d 个文件", offset, limit, len(files))
	return nil
}
