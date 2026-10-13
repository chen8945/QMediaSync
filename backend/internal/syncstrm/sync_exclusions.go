package syncstrm

import (
	"fmt"
	"path/filepath"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// prepareSyncExclusions 在生成和本地核对前冻结旧记录的排除保护，不逐文件查询账本。
// 保护集合随现有后台视图移交，排除本身不产生失败或推算未遍历子树的文件数。
func (s *SyncStrm) prepareSyncExclusions() error {
	if s.exclusionsPrepared || len(s.Config.ExcludeNames) == 0 && len(s.Config.excludeNameRegexes) == 0 {
		return nil
	}
	if !s.TmpSyncPath && s.SyncPathId != 0 {
		var cursor uint
		for {
			var rows []models.SyncFile
			if err := db.Db.WithContext(s.Context).
				Where("sync_path_id = ? AND account_id = ? AND source_type = ? AND id > ?", s.SyncPathId, s.Account.ID, s.Account.SourceType, cursor).
				Order("id ASC").Limit(syncLedgerPageSize).Find(&rows).Error; err != nil {
				return fatalSyncError(fmt.Errorf("读取排除项的同步文件记录失败：%w", err))
			}
			for _, row := range rows {
				local := row.LocalFilePath
				if local == "" {
					local = s.MakeFullLocalPath(&row)
				}
				if s.IsExcludeName(row.FileName) || s.IsExcludePath(row.Path) ||
					local != "" && (s.IsExcludeName(filepath.Base(local)) || s.IsExcludePath(s.remotePathForLocal(local))) {
					s.protectExcludedSyncFile(&row)
				}
			}
			if len(rows) < syncLedgerPageSize {
				break
			}
			cursor = rows[len(rows)-1].ID
		}
	}
	s.exclusionsPrepared = true
	return nil
}

func (s *SyncStrm) protectExcludedSyncFile(file *models.SyncFile) {
	local := file.LocalFilePath
	if local == "" {
		local = s.MakeFullLocalPath(file)
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.protectedFileIDs == nil {
		s.protectedFileIDs = make(map[string]bool)
	}
	s.protectedFileIDs[file.FileId] = true
	if local == "" {
		return
	}
	local = normalizedScanPath(local)
	if s.excludedLocalPaths == nil {
		s.excludedLocalPaths = make(map[string]bool)
	}
	if s.protectedLocalPaths == nil {
		s.protectedLocalPaths = make(map[string]bool)
	}
	isDir := file.FileType == v115open.TypeDir
	s.excludedLocalPaths[local] = s.excludedLocalPaths[local] || isDir
	s.protectedLocalPaths[local] = true
	if isDir {
		if s.protectedLocalDirectories == nil {
			s.protectedLocalDirectories = make(map[string]bool)
		}
		s.protectedLocalDirectories[local] = true
	}
}

// localPathExcluded 同时检查当前名称和旧源文件到本地目标的映射，如 movie.mkv → movie.strm。
func (s *SyncStrm) localPathExcluded(local string) bool {
	if s.IsExcludePath(s.remotePathForLocal(local)) {
		return true
	}
	local = normalizedScanPath(local)
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if len(s.excludedLocalPaths) == 0 {
		return false
	}
	if _, excluded := s.excludedLocalPaths[local]; excluded {
		return true
	}
	for parent := filepath.Dir(local); ; parent = filepath.Dir(parent) {
		if s.excludedLocalPaths[normalizedScanPath(parent)] {
			return true
		}
		if next := filepath.Dir(parent); next == parent {
			return false
		}
	}
}
