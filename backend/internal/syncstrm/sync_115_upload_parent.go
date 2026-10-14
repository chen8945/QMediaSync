package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if s.sync115 != nil {
		if id, absent, err := s.known115UploadDirectory(remotePath); err != nil || absent {
			return err
		} else if id != "" {
			return s.cache115UploadDirectory(id)
		}
	}
	candidate, err := s.has115UploadCandidate(localDir)
	if err != nil || !candidate {
		return err
	}
	if slices.Contains(uploadDirNames, strings.ToLower(filepath.Base(localDir))) {
		exists, err := s.confirm115UploadHost(localDir)
		if err != nil || !exists {
			return err
		}
	}
	return s.confirm115UploadDirectory(remotePath)
}

func (s *SyncStrm) has115UploadCandidate(localDir string) (bool, error) {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		local := filepath.ToSlash(filepath.Join(localDir, entry.Name()))
		if s.IsExcludePath(s.remotePathForLocal(local)) {
			continue
		}
		if entry.IsDir() {
			if slices.Contains(uploadDirNames, strings.ToLower(entry.Name())) {
				candidate, err := s.has115UploadCandidate(local)
				if err != nil || candidate {
					return candidate, err
				}
			}
			continue
		}
		if filepath.Ext(entry.Name()) == ".strm" || !s.IsValidMetaExt(entry.Name()) {
			continue
		}
		if cached, _ := s.memSyncCache.GetByLocalPath(local); cached == nil {
			canUpload, err := models.CheckCanUploadByLocalPath(s.Context, models.UploadSourceStrm, local)
			if err != nil {
				s.Sync.Logger.Warnf("查询本地元数据上传任务失败：local_path=%s，错误=%v", local, err)
				s.recordLocalFileFailure(local, err)
				return false, fatalSyncError(err)
			}
			if canUpload {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *SyncStrm) confirm115UploadDirectory(remotePath string) (result error) {
	if s.sync115 == nil {
		s.sync115 = &Sync115{}
	}
	defer func() {
		s.sync115.pathMu.Lock()
		conflict := s.sync115.pathConflict
		s.sync115.pathMu.Unlock()
		if result != nil && conflict {
			s.recordScanFailure(s.SourcePath, result)
		}
	}()
	if id, absent, err := s.known115UploadDirectory(remotePath); err != nil || absent {
		return err
	} else if id != "" {
		return s.cache115UploadDirectory(id)
	}
	id, err := s.previous115UploadDirectoryID(remotePath)
	if err != nil {
		return err
	}
	if id != "" {
		s.sync115.pathMu.Lock()
		_, known := s.sync115.paths[id]
		s.sync115.pathMu.Unlock()
		if !known {
			var detail *SyncFileCache
			detail, err = s.SyncDriver.DetailByFileId(s.Context, id)
			if err == nil {
				err = s.publish115DirectoryDetail(s.Context, id, "", detail, true)
			}
		}
		if contextErr := s.Context.Err(); contextErr != nil {
			return contextErr
		}
		if isFatalSyncError(err) {
			return err
		}
		if current, absent, stateErr := s.known115UploadDirectory(remotePath); stateErr != nil || absent {
			return stateErr
		} else if current != "" {
			return s.cache115UploadDirectory(current)
		}
		// 旧 ID 移动或删除不排除同名新目录，直接列原父目录，无需再查询旧路径。
		return s.resolve115UploadDirectoryByList(remotePath, err)
	}
	if driver, ok := s.SyncDriver.(interface {
		DetailByPath(context.Context, string) (*SyncFileCache, error)
	}); ok {
		var detail *SyncFileCache
		detail, err = driver.DetailByPath(s.Context, remotePath)
		if err == nil && detail != nil {
			id = detail.FileId
			if id != "" && len(detail.Paths) > 0 {
				err = s.confirm115PathDetail(s.Context, id, remotePath, detail)
			}
		}
	} else {
		id, err = s.SyncDriver.GetPathIdByPath(s.Context, remotePath)
	}
	if contextErr := s.Context.Err(); contextErr != nil {
		return contextErr
	}
	if isFatalSyncError(err) {
		return err
	}
	if v115open.IsAlreadyDeleted(err) {
		s.mark115UploadDirectoryAbsent(remotePath)
		return nil
	}
	if err == nil {
		if id == "" {
			err = fmt.Errorf("115 上传父目录查询缺少身份：%s", remotePath)
		} else {
			err = s.confirm115Path(s.Context, id, remotePath)
			if err == nil {
				return s.cache115UploadDirectory(id)
			}
		}
	}
	return s.resolve115UploadDirectoryByList(remotePath, err)
}

func (s *SyncStrm) cache115UploadDirectory(id string) error {
	s.sync115.pathMu.Lock()
	fact := s.sync115.paths[id]
	s.sync115.pathMu.Unlock()
	if s.IsExcludePath(fact.path) {
		return fmt.Errorf("115 元数据所属目录已被排除：%s", fact.path)
	}
	return s.cache115Directory(id, fact)
}

func (s *SyncStrm) known115UploadDirectory(remotePath string) (string, bool, error) {
	if err := s.Context.Err(); err != nil {
		return "", false, err
	}
	s.sync115.pathMu.Lock()
	defer s.sync115.pathMu.Unlock()
	if s.sync115.pathConflict {
		return "", false, fmt.Errorf("115 本轮目录位置存在冲突")
	}
	remotePath = normalizeStrmRemotePath(remotePath)
	if id := s.sync115.pathIDs[remotePath]; id != "" {
		return id, false, nil
	}
	for dir := remotePath; strmRemotePathWithin(dir, s.SourcePath); dir = filepath.ToSlash(filepath.Dir(dir)) {
		if s.sync115.absentDirs[dir] || s.sync115.completeDirs[normalizeStrmRemotePath(filepath.Dir(dir))] && s.sync115.pathIDs[dir] == "" {
			return "", true, nil
		}
		if dir == normalizeStrmRemotePath(s.SourcePath) || dir == "/" {
			break
		}
	}
	return "", false, nil
}

func (s *SyncStrm) mark115UploadDirectoryAbsent(remotePath string) {
	s.sync115.pathMu.Lock()
	defer s.sync115.pathMu.Unlock()
	if s.sync115.absentDirs == nil {
		s.sync115.absentDirs = make(map[string]bool)
	}
	s.sync115.absentDirs[normalizeStrmRemotePath(remotePath)] = true
}

// resolve115UploadDirectoryByList 从最近的已确认祖先沿单分支列举，成功后不保留首次查询失败。
func (s *SyncStrm) resolve115UploadDirectoryByList(remotePath string, lookupErr error) error {
	if isFatalSyncError(lookupErr) {
		return lookupErr
	}
	if id, absent, err := s.known115UploadDirectory(remotePath); err != nil || absent {
		return err
	} else if id != "" {
		return s.cache115UploadDirectory(id)
	}
	remotePath = normalizeStrmRemotePath(remotePath)
	root := normalizeStrmRemotePath(s.SourcePath)
	if !strmRemotePathWithin(remotePath, root) || remotePath == root {
		return fmt.Errorf("115 无法确认元数据目录是否存在：%s：%w", remotePath, lookupErr)
	}
	parent := normalizeStrmRemotePath(filepath.Dir(remotePath))
	id, absent, err := s.known115UploadDirectory(parent)
	if err != nil || absent {
		return err
	}
	if id == "" && parent == root {
		id = s.SourcePathId
	}
	if id == "" {
		if err := s.resolve115UploadDirectoryByList(parent, lookupErr); err != nil {
			return err
		}
		id, absent, err = s.known115UploadDirectory(parent)
		if err != nil || absent {
			return err
		}
	}
	if id == "" || s.IsExcludePath(parent) {
		return fmt.Errorf("115 无法定位元数据所属目录：%s", parent)
	}
	items, err := s.list115Directories(s.Context, parent, id)
	if err == nil {
		err = s.publish115DirectoryList(s.Context, parent, id, items)
	}
	if err != nil {
		// 致命补查错误优先返回，避免旧查询错误遮蔽认证失败。
		if isFatalSyncError(err) || errors.Is(lookupErr, v115open.ErrEmptyPathDetail) {
			return err
		}
		return errors.Join(lookupErr, err)
	}
	id, absent, err = s.known115UploadDirectory(remotePath)
	if err != nil || absent {
		return err
	}
	if id == "" {
		return fmt.Errorf("115 子目录列表未确认上传父目录：%s", remotePath)
	}
	return s.cache115UploadDirectory(id)
}

func (s *SyncStrm) previous115UploadDirectoryID(remotePath string) (string, error) {
	if s.TmpSyncPath {
		return "", nil
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
		return "", fatalSyncError(fmt.Errorf("查询 115 上传父目录的旧身份：%w", err))
	}
	if len(previous) != 1 || previous[0].FileId == "" || previous[0].FileId == "0" {
		return "", nil
	}
	return previous[0].FileId, nil
}

// confirm115UploadHost 附属目录只能在已存在的普通宿主之下补建。
func (s *SyncStrm) confirm115UploadHost(localDir string) (bool, error) {
	host := filepath.Dir(localDir)
	for normalizedScanPath(host) != normalizedScanPath(s.GetLocalBaseDir()) && slices.Contains(uploadDirNames, strings.ToLower(filepath.Base(host))) {
		host = filepath.Dir(host)
	}
	remote := s.remotePathForLocal(host)
	if normalizeStrmRemotePath(remote) == normalizeStrmRemotePath(s.SourcePath) {
		return true, nil
	}
	if s.IsExcludePath(remote) {
		return false, fmt.Errorf("115 元数据所属目录已被排除：%s", remote)
	}
	if err := s.confirm115UploadDirectory(remote); err != nil {
		return false, err
	}
	id, absent, err := s.known115UploadDirectory(remote)
	return id != "" && !absent, err
}

func (s *SyncStrm) record115CreatedDirectory(ctx context.Context, remotePath, id, parentID string) error {
	if id == "" || id == "0" || id == parentID {
		return fmt.Errorf("115 创建附属目录未返回有效身份：%s", remotePath)
	}
	remotePath = normalizeStrmRemotePath(remotePath)
	parent := normalizeStrmRemotePath(filepath.Dir(remotePath))
	// 本轮主动创建使对应缺席事实过期，其他兄弟的完整列表证据仍然有效。
	s.sync115.pathMu.Lock()
	complete := s.sync115.completeDirs[parent]
	delete(s.sync115.completeDirs, parent)
	for absent := range s.sync115.absentDirs {
		if strmRemotePathWithin(absent, remotePath) {
			delete(s.sync115.absentDirs, absent)
		}
	}
	s.sync115.pathMu.Unlock()
	err := s.publish115Paths(ctx, map[string]verified115Path{id: {
		path: strings.TrimPrefix(remotePath, "/"), parentID: parentID, within: true,
	}})
	s.sync115.pathMu.Lock()
	if complete {
		s.sync115.completeDirs[parent] = true
	}
	s.sync115.pathMu.Unlock()
	return err
}
