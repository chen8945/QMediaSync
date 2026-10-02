package syncstrm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// confirm115LocalUploadParent 在 Walk 进入目录时按需确认上传父目录。
// 每个目录只检查一次，先于其中任何文件的缺项清理；旧账本不能证明空目录仍在原位置。
func (s *SyncStrm) confirm115LocalUploadParent(localDir string) error {
	if s.Account.SourceType != models.SourceType115 || s.Config.EnableDownloadMeta == 0 ||
		s.Config.NetNotFoundFileAction != models.SyncTreeItemMetaActionUpload ||
		s.missingCleanupReason != "" || normalizedScanPath(localDir) == normalizedScanPath(s.GetLocalBaseDir()) {
		return nil
	}
	if cached, _ := s.memSyncCache.GetByLocalPath(localDir); cached != nil {
		return nil
	}
	remotePath := s.remotePathForLocal(localDir)
	if s.IsExcludePath(remotePath) {
		return nil
	}
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) == ".strm" || !s.IsValidMetaExt(entry.Name()) {
			continue
		}
		local := filepath.ToSlash(filepath.Join(localDir, entry.Name()))
		if cached, _ := s.memSyncCache.GetByLocalPath(local); cached != nil ||
			!models.CheckCanUploadByLocalPath(models.UploadSourceStrm, local) {
			continue
		}
		return s.confirm115UploadDirectory(remotePath)
	}
	return nil
}

func (s *SyncStrm) confirm115UploadDirectory(remotePath string) error {
	if s.sync115 == nil {
		s.sync115 = &Sync115{}
	}
	s.sync115.pathMu.Lock()
	id := s.sync115.pathIDs[normalizeStrmRemotePath(remotePath)]
	conflict := s.sync115.pathConflict
	s.sync115.pathMu.Unlock()
	if conflict {
		err := fmt.Errorf("115 本轮目录位置存在冲突")
		s.recordScanFailure(s.SourcePath, err)
		return err
	}
	if id == "" {
		var err error
		if driver, ok := s.SyncDriver.(interface {
			DetailByPath(context.Context, string) (*SyncFileCache, error)
		}); ok {
			var detail *SyncFileCache
			detail, err = driver.DetailByPath(s.Context, remotePath)
			if err == nil && detail != nil {
				id = detail.FileId
				// 不完整详情仍走 ID 补全；完整响应直接使用同一套身份和祖先校验。
				if id != "" && len(detail.Paths) > 0 {
					err = s.confirm115PathDetail(s.Context, id, remotePath, detail)
				}
			}
		} else {
			id, err = s.SyncDriver.GetPathIdByPath(s.Context, remotePath)
		}
		if err != nil {
			s.sync115.pathMu.Lock()
			conflict = s.sync115.pathConflict
			s.sync115.pathMu.Unlock()
			if conflict {
				s.recordScanFailure(s.SourcePath, err)
			}
			// 旧路径不存在也可能是目录移动，已知旧身份还须确认。
			if !isFatalSyncError(err) && v115open.IsAlreadyDeleted(err) {
				return s.confirm115MissingUploadDirectory(remotePath)
			}
			return err
		}
		if id == "" {
			return fmt.Errorf("115 上传父目录查询缺少身份：%s", remotePath)
		}
	}
	if err := s.confirm115Path(s.Context, id, remotePath); err != nil {
		s.sync115.pathMu.Lock()
		conflict = s.sync115.pathConflict
		s.sync115.pathMu.Unlock()
		if conflict {
			s.recordScanFailure(s.SourcePath, err)
		}
		return err
	}
	s.sync115.pathMu.Lock()
	fact := s.sync115.paths[id]
	s.sync115.pathMu.Unlock()
	return s.cache115Directory(id, fact)
}

func (s *SyncStrm) confirm115MissingUploadDirectory(remotePath string) error {
	if s.TmpSyncPath {
		return nil
	}
	parent := filepath.ToSlash(filepath.Dir("/" + strings.TrimPrefix(remotePath, "/")))
	parents := []string{parent, strings.TrimPrefix(parent, "/")}
	if parent == "/" {
		parents = append(parents, ".")
	}
	var previous []models.SyncFile
	if err := db.Db.WithContext(s.Context).
		Where("sync_path_id = ? AND account_id = ? AND source_type = ? AND file_type = ?",
			s.SyncPathId, s.Account.ID, models.SourceType115, v115open.TypeDir).
		Where("path IN ? AND file_name = ?", parents, filepath.Base(remotePath)).
		Limit(2).Find(&previous).Error; err != nil {
		return fatalSyncError(fmt.Errorf("查询 115 上传父目录的旧身份：%w", err))
	}
	if len(previous) == 0 {
		return nil
	}
	if len(previous) != 1 || previous[0].FileId == "" || previous[0].FileId == "0" {
		return fmt.Errorf("115 上传父目录的旧身份不明确：%s", remotePath)
	}
	id := previous[0].FileId
	s.sync115.pathMu.Lock()
	_, known := s.sync115.paths[id]
	s.sync115.pathMu.Unlock()
	if !known {
		_, err := s.SyncDriver.DetailByFileId(s.Context, id)
		if contextErr := s.Context.Err(); contextErr != nil {
			return contextErr
		}
		if !isFatalSyncError(err) && v115open.IsAlreadyDeleted(err) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	// 身份仍可查询到时，路径查询与旧事实无法证明删除；不将旧路径回填到缓存。
	return fmt.Errorf("115 上传父目录的旧身份仍存在，无法确认原路径已删除：%s", remotePath)
}
