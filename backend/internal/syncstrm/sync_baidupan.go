package syncstrm

import (
	"fmt"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// StartBaiduPanSync 优先完成当日本地自然日的全量核对，其余轮次按原水位增量扫描。
func (s *SyncStrm) StartBaiduPanSync() {
	if !s.TmpSyncPath {
		previous, err := models.GetTodaySuccessfulFullSyncByPathID(s.Context, s.SyncPathId, s.Sync.ID, time.Now())
		if err != nil {
			s.PathErrChan <- fatalSyncError(err)
			return
		}
		if previous == nil {
			s.FullSync = true
		}
	}
	s.Sync.Logger.Infof("最后同步时间：%s", helpers.FormatUnixLogTime(s.LastSyncAt))
	if s.FullSync || s.LastSyncAt == 0 {
		s.FullSync = true
		s.Sync.Logger.Infof("执行百度网盘全量同步")
		s.StartOther()
		return
	}
	s.Sync.Logger.Infof("从修改时间 %s 开始增量同步", helpers.FormatUnixLogTime(s.LastSyncAt))
	if err := s.StartBaiduPanSyncByMtime(s.LastSyncAt); err != nil {
		// 只有入口发送一次最终错误；普通扫描缺页按根范围记录，不取消已完成的独立文件。
		if isFatalSyncError(err) {
			s.PathErrChan <- err
		} else {
			s.recordScanFailure(s.SourcePath, err)
		}
	}
}

func (s *SyncStrm) StartBaiduPanSyncByMtime(lastSyncAt int64) error {
	s.skipMissingCleanup("增量扫描不能证明远端缺项，等待原定全量核对")
	if err := s.updateSyncSubStatus(models.SyncSubStatusProcessNetFileList); err != nil {
		return fatalSyncError(err)
	}
	// 原始远端事实先登记，包含被当前规则排除的条目，防止旧路径/旧名称回灌。
	observed := make(map[string]struct{})
	observedDirs := make(map[string]string)
	offset, reqCount := 0, 0
	for {
		if reqCount > 8 {
			if err := waitForScanRetry(s.Context, 60*time.Second); err != nil {
				return err
			}
			reqCount = 0
		}
		if err := s.Context.Err(); err != nil {
			return err
		}
		response, err := s.SyncDriver.GetFilesByPathMtime(s.Context, s.SourcePath, offset, 1000, lastSyncAt)
		reqCount++
		if err != nil {
			return fmt.Errorf("同步修改时间 %s 之后的文件失败，offset=%d：%w", helpers.FormatUnixLogTime(lastSyncAt), offset, err)
		}
		if response == nil || response.HasMore > 1 {
			return fmt.Errorf("百度网盘增量列表响应无效，offset=%d", offset)
		}
		if response.HasMore == 1 && (len(response.List) == 0 || int(response.Cursor) <= offset) {
			return fmt.Errorf("百度网盘增量列表不完整，offset=%d，cursor=%d，条目数=%d", offset, response.Cursor, len(response.List))
		}
		for _, file := range response.List {
			if err := s.Context.Err(); err != nil {
				return err
			}
			if file == nil || file.Path == "" || !pathWithin(s.SourcePath, file.Path) {
				return fmt.Errorf("百度网盘增量列表包含无路径或范围外条目，offset=%d", offset)
			}
			pickCode := strconv.FormatUint(file.FsId, 10)
			_, seenPath := observed["path:"+file.Path]
			_, seenID := observed["id:"+pickCode]
			if seenPath || (file.FsId != 0 && seenID) {
				return fmt.Errorf("百度网盘增量列表身份重复，文件=%s，offset=%d", file.Path, offset)
			}
			observed["path:"+file.Path] = struct{}{}
			if file.FsId != 0 {
				observed["id:"+pickCode] = struct{}{}
				if file.IsDir == 1 {
					observedDirs[pickCode] = file.Path
				}
			}
			atomic.AddInt64(&s.TotalFile, 1)
			_ = s.PublishProgress(false)
			parentPath := filepath.ToSlash(filepath.Dir(file.Path))
			syncFile := &SyncFileCache{
				ParentId: parentPath, FileId: file.Path, PickCode: pickCode,
				Path: parentPath, FileName: filepath.Base(file.Path), FileType: v115open.TypeFile,
				FileSize: int64(file.Size), MTime: int64(file.ServerMtime), Sha1: file.Md5,
				SourceType: models.SourceTypeBaiduPan,
			}
			if file.IsDir == 1 {
				syncFile.FileType = v115open.TypeDir
			}
			if (syncFile.FileType == v115open.TypeDir && s.IsExcludePath(file.Path)) ||
				(syncFile.FileType != v115open.TypeDir && !s.ValidFile(syncFile)) {
				s.recordFileSkipped(syncFile)
				continue
			}
			syncFile.GetLocalFilePath(s.TargetPath, s.SourcePath)
			if err := s.memSyncCache.Insert(syncFile); err != nil {
				return err
			}
			if syncFile.FileType == v115open.TypeDir {
				continue
			}
			if err := s.processNetFile(syncFile); err != nil {
				if isFatalSyncError(err) {
					return err
				}
				s.recordFileFailure(syncFile, err)
				continue
			}
			s.recordFileSuccess(syncFile)
		}
		if response.HasMore == 0 {
			break
		}
		// has_more 是递归接口的分页依据；短页仍可能存在后续结果。
		offset = int(response.Cursor)
	}
	return s.LoadSyncFileToCache(observed, observedDirs)
}

// LoadSyncFileToCache 仅补入本轮未观察到的旧记录，并按当前规则重新检查。
func (s *SyncStrm) LoadSyncFileToCache(observed map[string]struct{}, observedDirs map[string]string) error {
	s.Sync.Logger.Infof("从数据库中查询上次同步的文件")
	const limit = 1000
	for offset := 0; ; offset += limit {
		var syncFiles []*models.SyncFile
		if err := db.Db.WithContext(s.Context).Where("sync_path_id = ?", s.SyncPathId).
			Order("id").Offset(offset).Limit(limit).Find(&syncFiles).Error; err != nil {
			return fatalSyncError(fmt.Errorf("从数据库中查询上次同步的文件失败，offset=%d：%w", offset, err))
		}
		for _, item := range syncFiles {
			if err := s.Context.Err(); err != nil {
				return err
			}
			if newPath, seen := observedDirs[item.PickCode]; seen && item.FileType == v115open.TypeDir &&
				normalizedScanPath(item.FileId) != normalizedScanPath(newPath) {
				// 目录的新路径不能证明未返回后代的新路径；保留旧后代，等待原定全量。
				err := fmt.Errorf("百度网盘目录已移动：%s => %s，增量列表不能确认全部后代", item.FileId, newPath)
				s.recordScanFailure(item.FileId, err)
				s.recordScanFailure(newPath, err)
			}
			_, seenPath := observed["path:"+item.FileId]
			_, seenID := observed["id:"+item.PickCode]
			if seenPath || (item.PickCode != "" && item.PickCode != "0" && seenID) {
				continue
			}
			if _, err := s.memSyncCache.GetByFileId(item.FileId); err == nil {
				continue
			}
			syncFile := &SyncFileCache{
				ParentId: item.ParentId, FileId: item.FileId, PickCode: item.PickCode,
				Path: item.Path, FileName: item.FileName, FileType: item.FileType,
				FileSize: item.FileSize, MTime: item.MTime, Sha1: item.Sha1,
				SourceType: item.SourceType,
			}
			// 普通文件由 ValidFile 检查；旧记录的名称带路径时仍检查完整路径。
			if ((syncFile.FileType == v115open.TypeDir || filepath.Base(syncFile.FileName) != syncFile.FileName) &&
				s.IsExcludePath(syncFile.GetFullRemotePath())) ||
				(syncFile.FileType != v115open.TypeDir && !s.ValidFile(syncFile)) {
				s.recordFileSkipped(syncFile)
				continue
			}
			syncFile.GetLocalFilePath(s.TargetPath, s.SourcePath)
			if err := s.memSyncCache.Insert(syncFile); err != nil {
				return err
			}
		}
		if len(syncFiles) < limit {
			return nil
		}
	}
}
