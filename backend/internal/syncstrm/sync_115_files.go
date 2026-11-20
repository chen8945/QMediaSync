package syncstrm

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"qmediasync/internal/models"
	v115open "qmediasync/internal/v115/openapi"

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
	s.sync115.unresolvedFiles = nil
	limit := s.threadSettings.GetFileListPageSize()
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

// validate115FilePage 保留直接目录列表所需的名称校验。
func validate115FilePage(files []v115open.File, total int64, offset, limit int, seen map[string]struct{}) error {
	if err := validate115FilePageIdentities(files, total, offset, limit, seen); err != nil {
		return err
	}
	for i, file := range files {
		if !valid115FileName(file.FileName) {
			return fmt.Errorf("115 原始页文件名称不完整：offset=%d，index=%d，file_id=%q，file_name=%q，parent_id=%q", offset, i, file.FileId, file.FileName, file.Pid)
		}
	}
	return nil
}

func valid115FileName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.ContainsAny(name, "/\\\x00")
}

// validate115FilePageIdentities 在规则过滤前检查原始页，调用方串行访问 seen。
func validate115FilePageIdentities(files []v115open.File, total int64, offset, limit int, seen map[string]struct{}) error {
	if total < 0 || int64(offset) > total || len(files) != int(min(int64(limit), total-int64(offset))) {
		return fmt.Errorf("115 原始页长异常：offset=%d，limit=%d，count=%d，实际=%d", offset, limit, total, len(files))
	}
	for i, file := range files {
		if file.FileId == "" {
			return fmt.Errorf("115 原始页缺少文件 ID：offset=%d，index=%d，file_name=%q，parent_id=%q", offset, i, file.FileName, file.Pid)
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
		s.Sync.Logger.Errorf("获取 115 网盘文件列表失败：目录 ID %s，offset=%d，limit=%d，错误类型=%T", s.SourcePathId, offset, limit, err)
		return err
	}
	s.sync115.pageMu.Lock()
	err = validate115FilePageIdentities(files, s.sync115.expectedTotal, offset, limit, s.sync115.rawFileIDs)
	s.sync115.pageMu.Unlock()
	if err != nil {
		return err
	}
	if page == 0 && s.sync115.firstFileID != "" && files[0].FileId != s.sync115.firstFileID {
		return fmt.Errorf("115 首文件在计数和分页之间变化：计数时=%q，分页时=%q", s.sync115.firstFileID, files[0].FileId)
	}
	// 处理查询到的文件
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return err
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
		validName := valid115FileName(syncFile.FileName)
		if validName && !s.ValidFile(&syncFile) {
			s.recordFileSkipped(&syncFile)
			continue
		}
		// 已知父目录的坏名称直接隔离；只有定位缺失或已有详情可用时才补全。
		if !validName || syncFile.ParentId == "" {
			possibleVideo := syncFile.FileSize >= s.GetMinVideoSize() && (syncFile.FileName == "" || s.IsValidVideoExt(syncFile.FileName))
			possibleMeta := s.EnableDownloadMeta() && (syncFile.FileName == "" || s.IsValidMetaExt(syncFile.FileName))
			needed := !s.IsExcludeName(syncFile.FileName) && (possibleVideo || possibleMeta)
			cached := s.sync115.firstFile != nil && s.sync115.firstFile.FileId == syncFile.FileId
			if cached || needed && syncFile.ParentId == "" {
				err = s.repair115FileLocation(ctx, &syncFile)
				if isFatalSyncError(err) {
					return err
				}
			} else {
				err = fmt.Errorf("115 文件名称或父目录不完整：file_name=%q，parent_id=%q", syncFile.FileName, syncFile.ParentId)
			}
			if err != nil {
				s.recordFileFailure(&syncFile, fmt.Errorf("115 文件条目未完成：offset=%d，index=%d，file_id=%q：%w", offset, i, syncFile.FileId, err))
				s.sync115.pageMu.Lock()
				s.sync115.unresolvedFiles = append(s.sync115.unresolvedFiles, &syncFile)
				s.sync115.pageMu.Unlock()
				continue
			}
			if !s.ValidFile(&syncFile) {
				s.recordFileSkipped(&syncFile)
				continue
			}
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

// repair115FileLocation 只补缺失定位，复用详情中的完整父链，避免再查父目录。
func (s *SyncStrm) repair115FileLocation(ctx context.Context, file *SyncFileCache) error {
	detail := s.sync115.firstFile
	if detail == nil || detail.FileId != file.FileId {
		var err error
		detail, err = s.SyncDriver.DetailByFileId(ctx, file.FileId)
		if err != nil {
			return err
		}
	}
	if detail == nil {
		return fmt.Errorf("115 文件详情为空：file_id=%q", file.FileId)
	}
	if detail.FileId != file.FileId || detail.FileType != v115open.TypeFile || !valid115FileName(detail.FileName) || len(detail.Paths) == 0 {
		return fmt.Errorf("115 文件详情无法补全定位：请求 ID=%q，实际 ID=%q，类型=%q，名称=%q，祖先数=%d", file.FileId, detail.FileId, detail.FileType, detail.FileName, len(detail.Paths))
	}
	parent := detail.Paths[len(detail.Paths)-1]
	parentDetail := &SyncFileCache{FileId: parent.FileId, FileName: parent.Name, FileType: v115open.TypeDir, Paths: detail.Paths[:len(detail.Paths)-1]}
	if err := s.confirm115PathDetail(ctx, parent.FileId, "", parentDetail); err != nil {
		return err
	}
	// 详情可能省略提取码、大小或时间，只补定位，保留原列表中的其他资料。
	file.FileName, file.ParentId = detail.FileName, parent.FileId
	return nil
}
