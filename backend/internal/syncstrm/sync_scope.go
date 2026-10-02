package syncstrm

import (
	"fmt"
	"path"
	"path/filepath"

	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
)

func (s *SyncStrm) acquireScope() error {
	if s.TmpSyncPath {
		for {
			scope := s.manualScope()
			root, err := syncscope.ResolveLocalPath(scope.LocalPath)
			if err != nil {
				return err
			}
			scope.LocalPath = root
			release, err := syncscope.Acquire(s.Context, scope)
			if err != nil {
				return err
			}
			current, err := syncscope.ResolveLocalPath(s.manualScope().LocalPath)
			if err != nil {
				release()
				return err
			}
			if current != root {
				release()
				continue
			}
			s.scopeRelease, s.scopeLocalRoot = release, root
			return nil
		}
	}
	syncPath, release, err := models.AcquireSyncPathScope(s.Context, s.SyncPathId)
	if err != nil {
		return fmt.Errorf("等待同步目录失败：%w", err)
	}
	s.scopeRelease = release
	s.scopeLocalRoot = syncPath.ScopeLocalRoot
	if syncPath.AccountId != s.Account.ID || syncPath.SourceType != s.Account.SourceType ||
		!sameSourceDirectory(syncPath.RemotePath, s.SourcePath) ||
		filepath.Clean(syncPath.LocalPath) != filepath.Clean(s.TargetPath) {
		return fmt.Errorf("同步目录的路径或账号已变更，请重新启动同步")
	}
	// 等待期间上一轮可能已完成，设置也可能被修改，不能继续使用旧值。
	models.LoadSettings()
	config := configFromSyncPath(syncPath)
	if (s.Account.SourceType == models.SourceType115 || s.Account.SourceType == models.SourceTypeBaiduPan) && config.StrmBaseUrl == "" {
		return fmt.Errorf("同步目录未配置 STRM 直连地址")
	}
	if err := config.compileExcludeNameRegexes(); err != nil {
		return err
	}
	s.Config = config
	s.LastSyncAt, s.FullSync = syncPath.LastSyncAt, syncPath.IsFullSync
	s.SourcePathId = syncPath.BaseCid
	return nil
}

func (s *SyncStrm) manualScope() syncscope.Scope {
	sourcePath := s.SourcePath
	if s.IsFile {
		sourcePath = filepath.Dir(sourcePath)
	}
	syncPath := models.SyncPath{
		SourceType: s.Account.SourceType, AccountId: s.Account.ID,
		RemotePath: sourcePath, LocalPath: s.TargetPath,
	}
	scope := syncPath.Scope()
	if s.IsFile && s.Account.SourceType == models.SourceTypeLocal {
		// 本地单文件按现有相对路径规则输出到目标的上一级目录。
		scope.LocalPath = filepath.Dir(s.TargetPath)
	}
	return scope
}

func sameSourceDirectory(left, right string) bool {
	return path.Clean("/"+filepath.ToSlash(left)) == path.Clean("/"+filepath.ToSlash(right))
}

func (s *SyncStrm) releaseScope() {
	if s.scopeRelease != nil {
		s.scopeRelease()
		s.scopeRelease = nil
		s.scopeLocalRoot = ""
	}
}
