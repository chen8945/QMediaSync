package syncstrm

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// 115 网盘的同步器
// 先收集文件，再按直接父目录补全本轮路径。

type Sync115 struct {
	pathMu          sync.Mutex
	paths           map[string]verified115Path // 仅保存本轮成功响应确认的目录
	pathIDs         map[string]string          // 完整路径对应的本轮目录 ID
	historicalPaths map[string]verified115Path // 增量同步可复用的持久目录，不代表本轮观察到目录仍存在
	pathsApplied    bool                       // 文件路径已应用后发现旧目录矛盾，只能保护本轮并等待后续核对
	pathConflict    bool
	expectedTotal   int64 // 文件 worker 启动前固定，按原始响应校验，不使用过滤后的缓存数
	firstFileID     string
	pageMu          sync.Mutex
	rawFileIDs      map[string]struct{}
	incompletePages bool
}

func (s *SyncStrm) Start115Sync() {
	if err := s.updateSyncSubStatus(models.SyncSubStatusProcessNetFileList); err != nil {
		s.PathErrChan <- err
		return
	}
	s.sync115 = &Sync115{}
	// 115 同步器
	var existsPathesCount int64 = 0
	// 增量同步恢复历史目录，本轮取得的新位置会覆盖历史信息。
	if !s.TmpSyncPath && !s.FullSync {
		// 临时同步和全量同步不能执行该操作
		var err error
		existsPathesCount, err = s.GetExistsPath()
		if err != nil {
			s.PathErrChan <- err
			return
		}
		s.Sync.Logger.Infof("已存在路径总数：%d", existsPathesCount)
	}
	// 查询文件总数
	total, firstFileId, totalErr := s.SyncDriver.GetTotalFileCount(s.Context)
	if totalErr != nil {
		s.recordScanFailure(s.SourcePath, totalErr)
		if isFatalSyncError(totalErr) {
			s.PathErrChan <- totalErr
		}
		return
	}
	if total == 0 {
		s.Sync.Logger.Infof("没有需要同步的文件")
		return
	}
	s.Sync.Logger.Infof("115 网盘文件总数：%d", total)
	s.sync115.firstFileID = firstFileId
	atomic.StoreInt64(&s.TotalFile, total)
	if err := s.PublishProgress(true); err != nil {
		s.PathErrChan <- err
		return
	}
	// 如果没有路径缓存或者全量同步，则先预取
	if existsPathesCount == 0 || s.FullSync {
		// 没有旧目录时，先按原范围预取两层目录。
		s.Sync.Logger.Infof("开始预取两层目录")
		err := s.Preload115Dirs(firstFileId)
		if err != nil {
			s.Sync.Logger.Errorf("预取 115 目录失败：%v", err)
			s.recordScanFailure(s.SourcePath, err)
			if isFatalSyncError(err) {
				s.PathErrChan <- err
				return
			}
		}
		s.Sync.Logger.Infof("完成预取两层目录")
	}
	// 启动文件调度器
	s.Sync.Logger.Infof("启动 115 文件调度器，文件总数：%d", total)
	filerr := s.Start115FileDispathcer(total)
	if filerr != nil {
		s.Sync.Logger.Errorf("启动 115 文件调度器失败：%v", filerr)
		s.PathErrChan <- filerr
		return
	}
	// 启动路径调度器
	s.Sync.Logger.Infof("启动 115 路径调度器")
	patherr := s.Start115PathDispathcer()
	if patherr != nil {
		s.Sync.Logger.Errorf("启动 115 路径调度器失败：%v", patherr)
		s.PathErrChan <- patherr
		return
	}
	if err := s.process115CollectedFiles(); err != nil {
		s.Sync.Logger.Errorf("处理 115 文件失败：%v", err)
		s.PathErrChan <- err
		return
	}
	s.Sync.Logger.Infof("115 文件和路径同步完成")
}

// GetExistsPath 载入增量同步可复用的目录；排除规则在路径收集结束后按本轮配置应用。
func (s *SyncStrm) GetExistsPath() (int64, error) {
	if s.TmpSyncPath || s.FullSync {
		return 0, nil
	}
	if s.sync115 == nil {
		s.sync115 = &Sync115{}
	}
	facts := make(map[string]verified115Path)
	pathIDs := make(map[string]string)
	invalid := make(map[string]bool)
	var rows []models.SyncFile
	err := db.Db.WithContext(s.Context).
		Select("id", "file_id", "parent_id", "path", "file_name", "m_time").
		Where("file_type = ? AND sync_path_id = ? AND account_id = ? AND source_type = ?",
			v115open.TypeDir, s.SyncPathId, s.Account.ID, models.SourceType115).
		FindInBatches(&rows, 256, func(_ *gorm.DB, _ int) error {
			for _, row := range rows {
				fullPath := filepath.ToSlash(filepath.Join(row.Path, row.FileName))
				if row.FileId == "" || row.FileName == "" || row.FileName == "." || row.FileName == ".." ||
					strings.ContainsAny(row.FileName, "/\\") || strings.ContainsAny(row.Path, "\\") ||
					strings.Contains(fullPath, "**") || !strmRemotePathWithin(fullPath, s.SourcePath) {
					invalid[row.FileId] = true
					continue
				}
				fact := verified115Path{path: strings.TrimPrefix(fullPath, "/"), parentID: row.ParentId,
					within: true, root: row.FileId == s.SourcePathId, mtime: row.MTime}
				if old, ok := facts[row.FileId]; ok && (old.path != fact.path || old.parentID != fact.parentID) {
					invalid[row.FileId] = true
				}
				key := normalizeStrmRemotePath(fact.path)
				if oldID, ok := pathIDs[key]; ok && oldID != row.FileId {
					invalid[oldID], invalid[row.FileId] = true, true
				}
				facts[row.FileId], pathIDs[key] = fact, row.FileId
			}
			return nil
		}).Error
	if err != nil {
		return 0, fatalSyncError(fmt.Errorf("查询已存在路径失败：%w", err))
	}
	// 不一致的旧关系不能用于推断后代位置；它们按需进入普通详情补全。
	for id, fact := range facts {
		if parent, ok := facts[fact.parentID]; ok &&
			normalizeStrmRemotePath(filepath.Dir(fact.path)) != normalizeStrmRemotePath(parent.path) {
			invalid[id] = true
		}
	}
	invalidPaths := make([]string, 0, len(invalid))
	for id := range invalid {
		if bad, exists := facts[id]; exists {
			invalidPaths = append(invalidPaths, bad.path)
		}
	}
	for id, fact := range facts {
		for _, badPath := range invalidPaths {
			if strmRemotePathWithin(fact.path, badPath) {
				invalid[id] = true
				break
			}
		}
	}
	// 无效父行可能因名称损坏而未进入 facts，仍须按稳定父 ID 淘汰后代。
	children := make(map[string][]string)
	for id, fact := range facts {
		children[fact.parentID] = append(children[fact.parentID], id)
	}
	pending := make([]string, 0, len(invalid))
	for id := range invalid {
		pending = append(pending, id)
	}
	for len(pending) > 0 {
		id := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		delete(facts, id)
		for _, child := range children[id] {
			if !invalid[child] {
				invalid[child] = true
				pending = append(pending, child)
			}
		}
	}
	count := int64(len(facts))
	if count > 0 && s.SourcePathId != "" {
		rootPath := strings.TrimPrefix(normalizeStrmRemotePath(s.SourcePath), "/")
		if rootPath == "" {
			rootPath = "/"
		}
		facts[s.SourcePathId] = verified115Path{path: rootPath, within: true, root: true}
	}
	s.sync115.pathMu.Lock()
	s.sync115.historicalPaths = facts
	s.sync115.pathMu.Unlock()
	return count, nil
}
