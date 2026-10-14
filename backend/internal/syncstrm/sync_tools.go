package syncstrm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func (s *SyncStrm) checkPathExists(path string) bool {
	return helpers.PathExists(path)
}

// 获取本地根目录
func (s *SyncStrm) GetLocalBaseDir() string {
	fullPath := filepath.Join(s.TargetPath, s.SourcePath)
	fullPath = filepath.ToSlash(fullPath)
	if s.Account.SourceType == models.SourceTypeLocal {
		fullPath = s.TargetPath
	}
	return fullPath
}

func (s *SyncStrm) MakeFullLocalPath(file *models.SyncFile) string {
	// 视频文件要转成 STRM 扩展名
	fileName := file.FileName
	if file.IsVideo {
		ext := filepath.Ext(fileName)
		baseName := strings.TrimSuffix(fileName, ext)
		fileName = baseName + ".strm"
	}
	fullPath := filepath.Join(s.TargetPath, file.Path, fileName)
	if s.Account.SourceType == models.SourceTypeLocal {
		// 本地不能拼接完整的 file.Path
		relPath, err := filepath.Rel(s.SourcePath, file.Path)
		if err != nil {
			return ""
		}
		fullPath = filepath.Join(s.TargetPath, relPath, fileName)
	}
	// 将 Windows 路径转换为 Unix 路径
	fullPath = filepath.ToSlash(fullPath)
	return fullPath
}

func (s *SyncStrm) removeLocalFile(filePath string, removedFileParents map[string]bool) error {
	if err := s.Context.Err(); err != nil {
		return err
	}
	if s.cleanupProtected(filePath, false) {
		return nil
	}
	if err := os.Remove(filePath); err != nil {
		return fmt.Errorf("删除文件失败：%w", err)
	}
	if removedFileParents != nil {
		removedFileParents[filepath.ToSlash(filepath.Dir(filePath))] = true
	}
	s.Sync.Logger.Infof("删除文件成功：%s", filePath)
	return nil
}

// removeEmptyLocalDirs 在遍历成功后从子目录向父目录收尾，保留之后新增的失败保护。
func (s *SyncStrm) removeEmptyLocalDirs(dirs []string, removedFileParents map[string]bool) error {
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := s.Context.Err(); err != nil {
			return err
		}
		dir := dirs[i]
		if !s.Config.DelEmptyLocalDir || s.missingCleanupReason != "" {
			s.Sync.Logger.Infof("当前设置不允许删除空目录，跳过本地目录 %s", dir)
			continue
		}
		if s.cleanupProtected(dir, true) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			s.Sync.Logger.Warnf("读取目录 %s 的文件列表失败：%v", dir, err)
			s.recordScanFailure(s.remotePathForLocal(dir), err)
			continue
		}
		if len(entries) != 0 {
			s.Sync.Logger.Infof("本地目录 %s 不是空目录，跳过删除", dir)
			continue
		}
		if err := s.Context.Err(); err != nil {
			return err
		}
		if s.cleanupProtected(dir, true) {
			continue
		}
		if err := os.Remove(dir); err != nil {
			s.Sync.Logger.Warnf("删除空目录失败：local_path=%s，错误=%v", dir, err)
			s.recordScanFailure(s.remotePathForLocal(dir), err)
			continue
		}
		s.Sync.Logger.Infof("删除空目录成功：%s", dir)
		// 原本已空或仅因子目录删除变空的目录，保留本轮确认的远端账本。
		if !removedFileParents[dir] {
			continue
		}
		file, err := s.memSyncCache.GetByLocalPath(dir)
		if err != nil || file == nil {
			s.Sync.Logger.Warnf("查询空目录对应的网盘记录失败：%s，%v", dir, err)
			continue
		}
		if err := s.memSyncCache.DeleteByFileId(file.GetFileId()); err != nil {
			s.Sync.Logger.Warnf("删除空目录对应的网盘记录失败：%s，%v", file.GetFileId(), err)
		}
	}
	return s.Context.Err()
}
