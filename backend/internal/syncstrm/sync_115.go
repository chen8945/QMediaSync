package syncstrm

import (
	"path/filepath"
	"sync"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// 115 网盘的同步器
// 文件处理器（通过接口先把所有文件入库）
// 路径处理器（从数据库查询没有路径的文件 path_id，再查询路径，并更新所有使用该 path_id 的数据库记录）

type Sync115 struct {
	existsPathes  sync.Map // file_id => path
	excludePathId sync.Map // 排除的路径 ID 列表
}

func (s *SyncStrm) Start115Sync() {
	s.Sync.UpdateSubStatus(models.SyncSubStatusProcessNetFileList)
	s.sync115 = &Sync115{
		existsPathes:  sync.Map{},
		excludePathId: sync.Map{},
	}
	// 115 同步器
	var existsPathesCount int64 = 0
	// 先将已存在的路径全部读取到内存中（使用 sync.Map）
	if !s.TmpSyncPath && !s.FullSync {
		// 临时同步和全量同步不能执行该操作
		existsPathesCount = s.GetExistsPath()
		s.Sync.Logger.Infof("已存在路径总数：%d", existsPathesCount)
	}
	// 查询文件总数
	total, firstFileId, totalErr := s.SyncDriver.GetTotalFileCount(s.Context)
	if totalErr != nil {
		// 报错了
		s.PathErrChan <- totalErr
		return
	}
	if total == 0 {
		s.Sync.Logger.Errorf("没有需要同步的文件：%v", totalErr)
		return
	}
	s.Sync.Logger.Infof("115 网盘文件总数：%d", total)
	s.TotalFile = total
	s.Sync.Total = int(total)
	// 更新回数据库
	s.Sync.UpdateTotal()
	s.PublishProgress(true)
	// 如果没有路径缓存或者全量同步，则先预取
	if existsPathesCount == 0 || s.FullSync {
		// 如果没有已存在路径，则预取两层目录并写入缓存
		s.Sync.Logger.Infof("开始预取两层目录")
		err := s.Preload115Dirs(firstFileId)
		if err != nil {
			s.Sync.Logger.Errorf("预取 115 目录失败：%v", err)
			s.PathErrChan <- err
			return
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

// 将已存在的路径全部读取到内存中
func (s *SyncStrm) GetExistsPath() int64 {
	// 从数据库中查询所有已存在的路径
	var pathes []models.SyncFile
	offset := 0
	limit := 1000
	var existsPathesCount int64 = 0
	for {
		// 分页查询
		result := db.Db.Where("file_type = ? AND sync_path_id = ?", v115open.TypeDir, s.SyncPathId).Offset(offset).Limit(limit).Order("id ASC").Find(&pathes)
		if result.Error != nil {
			s.Sync.Logger.Errorf("查询已存在路径失败：%v", result.Error)
			return 0
		}
		// 将查询到的路径全部写入 existsPathes
		for _, path := range pathes {
			// 如果名字被排除，则不加入
			if s.IsExcludeName(path.FileName) || s.IsExcludePath(path.Path) {
				s.sync115.excludePathId.Store(path.FileId, true)
				continue
			}
			pathStr := filepath.Join(path.Path, path.FileName)
			pathStr = filepath.ToSlash(pathStr)
			// s.Sync.Logger.Infof("加载已存在路径：%s=>%s", path.FileId, pathStr)
			s.sync115.existsPathes.Store(path.FileId, pathStr)
			existsPathesCount++
			// 写入同步缓存
			fileItem := &SyncFileCache{
				FileId:     path.FileId,
				FileName:   path.FileName,
				FileType:   v115open.TypeDir,
				SourceType: models.SourceType115,
				Path:       path.Path,
				ParentId:   path.ParentId,
				MTime:      path.MTime,
				IsVideo:    false,
				IsMeta:     false,
			}
			s.memSyncCache.Insert(fileItem)
			fileItem.GetLocalFilePath(s.TargetPath, s.SourcePath) // 生成本地路径缓存
			s.memSyncCache.Insert(fileItem)
		}
		// 如果查询到的路径数量小于 1000，说明已经查询完所有路径
		if len(pathes) < limit {
			break
		}
		// 增加偏移量
		offset += limit
	}
	return existsPathesCount
}
