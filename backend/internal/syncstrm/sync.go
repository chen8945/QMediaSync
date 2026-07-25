package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type driverImpl interface {
	GetNetFileFiles(ctx context.Context, parentPath, parentPathId string) ([]*SyncFileCache, error)
	GetPathIdByPath(ctx context.Context, path string) (string, error)
	SetSyncStrm(s *SyncStrm)
	MakeStrmContent(sf *SyncFileCache) string
	CreateDirRecursively(ctx context.Context, parentDir string) (parentPathId, remotePath string, err error)
	GetTotalFileCount(ctx context.Context) (int64, string, error)
	GetDirsByPathId(ctx context.Context, pathId string) ([]pathQueueItem, error)
	GetFilesByPathId(ctx context.Context, rootPathId string, offset, limit int) ([]v115open.File, error)
	GetFilesByPathMtime(ctx context.Context, rootPathId string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error)
	// 所有文件详情，含路径
	DetailByFileId(ctx context.Context, fileId string) (*SyncFileCache, error)
	// 删除目录下的某些文件
	DeleteFile(ctx context.Context, parentId string, fileIds []string) error
}

type SyncStrm struct {
	SyncDriver   driverImpl
	Account      *models.Account // 网盘账号，如果是本地类型则为 nil
	Sync         *models.Sync    // 同步记录，Start 方法会生成
	SourcePath   string          // 来源路径
	SourcePathId string
	LastSyncAt   int64 // 最后同步时间
	TmpSyncPath  bool
	TargetPath   string
	Config       SyncStrmConfig
	Context      context.Context
	Cancel       context.CancelFunc
	FullSync     bool // 是否是全量同步
	IsFile       bool // 是否是文件

	// 路径队列
	PathWorkerMax int64
	PathErrChan   chan error

	// 临时表
	TempTableName string
	SyncPathId    uint

	// 计数
	NewMeta   int64
	NewStrm   int64
	NewUpload int64
	TotalFile int64

	lastProgressPublishedAt time.Time
	progressMu              sync.Mutex

	// 停止状态：避免多次触发停止
	stopped atomic.Bool

	// 115 同步器
	sync115 *Sync115

	memSyncCache *MemorySyncCache // 同步缓存

	embyRefreshTargetsMu sync.Mutex
	embyRefreshTargets   []models.EmbyRefreshTarget
}

type pathQueueItem struct {
	Path   string // 路径
	PathId string // 路径 ID，OpenList 和本地的 Path、PathId 相同
	Depth  uint   // 路径深度
	Mtime  int64  // 最后修改时间
}

func NewSyncStrm(account *models.Account, syncPathId uint, sourcePath, sourcePathId, targetPath string, config SyncStrmConfig, IsFullSync bool, lastSyncAt int64, isFile bool) *SyncStrm {
	return newSyncStrm(account, syncPathId, sourcePath, sourcePathId, targetPath, config, IsFullSync, lastSyncAt, isFile, true)
}

func newSyncStrm(account *models.Account, syncPathId uint, sourcePath, sourcePathId, targetPath string, config SyncStrmConfig, IsFullSync bool, lastSyncAt int64, isFile bool, createSyncRecord bool) *SyncStrm {
	if account == nil {
		account = &models.Account{SourceType: models.SourceTypeLocal}
	}
	var syncDriver driverImpl
	switch account.SourceType {
	case models.SourceType115:
		syncDriver = NewOpen115Driver(account.Get115Client())
	case models.SourceTypeOpenList:
		syncDriver = NewOpenListDriver(account.GetOpenListClient())
	case models.SourceTypeLocal:
		syncDriver = NewLocalDriver()
	case models.SourceTypeBaiduPan:
		syncDriver = NewBaiduPanDriver(account.GetBaiDuPanClient())
	}
	pathWorkerMax := int64(models.SettingsGlobal.FileDetailThreads)
	switch account.SourceType {
	case models.SourceTypeLocal:
		pathWorkerMax = int64(10) // 本地类型（CD2 会自行限制并发），限制为 10 个并发
	case models.SourceTypeOpenList:
		pathWorkerMax = int64(models.SettingsGlobal.OpenlistQPS)
	case models.SourceType115:
		pathWorkerMax = int64(models.SettingsGlobal.FileDetailThreads)
	case models.SourceTypeBaiduPan:
		pathWorkerMax = int64(models.SettingsGlobal.FileDetailThreads)
	}
	if pathWorkerMax <= 1 {
		pathWorkerMax = 2 // 最小为 2，否则并发操作会出错
	}
	// 非 Windows 的本地路径需要以 / 开头。
	if runtime.GOOS != "windows" && account.SourceType == models.SourceTypeLocal {
		// 如果 sourcePath 不以 / 开头，添加一个 /。
		if !strings.HasPrefix(sourcePath, "/") {
			sourcePath = "/" + sourcePath
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &SyncStrm{
		Context:       ctx,
		Cancel:        cancel,
		SyncDriver:    syncDriver,
		Account:       account,
		SourcePath:    sourcePath,
		SourcePathId:  sourcePathId,
		TargetPath:    targetPath,
		TmpSyncPath:   false,
		PathWorkerMax: pathWorkerMax,
		Config:        config,
		SyncPathId:    syncPathId,
		FullSync:      IsFullSync,
		PathErrChan:   make(chan error, 1),
		LastSyncAt:    lastSyncAt,
		IsFile:        isFile,
	}
	s.memSyncCache = NewMemorySyncCache(syncPathId)
	if s.Account == nil {
		s.Account = &models.Account{SourceType: models.SourceTypeLocal}
	}
	// 如果 SyncPathId = 0，则生成一个唯一的临时 ID。
	if s.SyncPathId == 0 {
		s.SyncPathId = uint(time.Now().UnixNano())
		s.TmpSyncPath = true
	}
	if createSyncRecord {
		// 新增一条 Sync 记录
		s.Sync = models.CreateSync(s.SyncPathId, s.SourcePath, s.SourcePathId, s.TargetPath)
		if s.Sync == nil {
			return nil
		}
		s.Sync.InitLogger()
	} else {
		s.Sync = &models.Sync{
			SyncPathId: s.SyncPathId,
			Status:     models.SyncStatusCompleted,
			SubStatus:  models.SyncSubStatusNone,
			LocalPath:  s.TargetPath,
			RemotePath: s.SourcePath,
			BaseCid:    s.SourcePathId,
			IsFullSync: s.FullSync,
			Logger:     effectiveSyncLogger(),
		}
	}
	s.SyncDriver.SetSyncStrm(s)
	return s
}

func effectiveSyncLogger() *helpers.QLogger {
	if helpers.AppLogger != nil {
		return helpers.AppLogger
	}
	return &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
}

const syncProgressPublishInterval = time.Second

func (s *SyncStrm) shouldPublishProgress(now time.Time, force bool) bool {
	if s == nil || s.Sync == nil || s.Sync.ID == 0 {
		return false
	}
	if force {
		return true
	}
	s.progressMu.Lock()
	defer s.progressMu.Unlock()
	return s.lastProgressPublishedAt.IsZero() || now.Sub(s.lastProgressPublishedAt) >= syncProgressPublishInterval
}

func (s *SyncStrm) markProgressPublished(now time.Time) {
	s.progressMu.Lock()
	s.lastProgressPublishedAt = now
	s.progressMu.Unlock()
}

// PublishProgress 按节流策略发布同步任务运行中统计。
func (s *SyncStrm) PublishProgress(force bool) {
	now := time.Now()
	if !s.shouldPublishProgress(now, force) {
		return
	}
	s.Sync.Total = int(atomic.LoadInt64(&s.TotalFile))
	s.Sync.NewStrm = int(atomic.LoadInt64(&s.NewStrm))
	s.Sync.NewMeta = int(atomic.LoadInt64(&s.NewMeta))
	s.Sync.NewUpload = int(atomic.LoadInt64(&s.NewUpload))
	if s.Sync.UpdateProgress(s.Sync.Total, s.Sync.NewStrm, s.Sync.NewMeta, s.Sync.NewUpload) {
		s.markProgressPublished(now)
	}
}

func NewSyncStrmFromSyncPath(syncPath *models.SyncPath) *SyncStrm {
	return newSyncStrmFromSyncPath(syncPath, nil, true)
}

// NewSyncStrmForStrmGeneration 创建单文件 STRM 后处理同步器，不创建同步任务记录。
func NewSyncStrmForStrmGeneration(syncPath *models.SyncPath, account *models.Account) *SyncStrm {
	return newSyncStrmFromSyncPath(syncPath, account, false)
}

func newSyncStrmFromSyncPath(syncPath *models.SyncPath, account *models.Account, createSyncRecord bool) *SyncStrm {
	if syncPath == nil {
		return nil
	}
	var err error
	if account == nil && syncPath.AccountId != 0 {
		account, err = models.GetAccountById(syncPath.AccountId)
		if err != nil {
			return nil
		}
	}
	if account == nil {
		account = &models.Account{SourceType: models.SourceTypeLocal}
	}
	// 重新加载设置
	models.LoadSettings()
	if (account.SourceType == models.SourceType115 || account.SourceType == models.SourceTypeBaiduPan) && syncPath.GetStrmBaseUrl() == "" {
		helpers.AppLogger.Errorf("115 或百度网盘同步路径 %s 未配置 STRM 直连地址", syncPath.RemotePath)
		return nil
	}
	videoExt := syncPath.GetVideoExt()
	metaExt := syncPath.GetMetaExt()
	excludeNames := syncPath.GetExcludeNameArr()
	config := SyncStrmConfig{
		EnableDownloadMeta:    int64(syncPath.GetDownloadMeta()),
		MinVideoSize:          syncPath.GetMinVideoSize(),
		VideoExt:              videoExt,
		MetaExt:               metaExt,
		ExcludeNames:          excludeNames,
		NetNotFoundFileAction: models.SyncTreeItemMetaAction(syncPath.GetUploadMeta()),
		StrmUrlNeedPath:       syncPath.GetAddPath(),
		DelEmptyLocalDir:      syncPath.GetDeleteDir() == 1,
		CheckMetaMtime:        syncPath.GetCheckMetaMtime(),
		StrmBaseUrl:           syncPath.GetStrmBaseUrl(),
	}
	if account.SourceType == models.SourceTypeOpenList {
		// OpenList 只使用自定义的 STRM 直连地址
		config.StrmBaseUrl = syncPath.SettingStrm.StrmBaseUrl
	}
	return newSyncStrm(account, syncPath.ID, syncPath.RemotePath, syncPath.BaseCid, syncPath.LocalPath, config, syncPath.IsFullSync, syncPath.LastSyncAt, false, createSyncRecord)
}

func (s *SyncStrm) logEffectiveStrmConfig() {
	if s == nil || s.TmpSyncPath || s.SyncPathId == 0 {
		return
	}
	syncPath := models.GetSyncPathById(s.SyncPathId)
	if syncPath == nil {
		return
	}
	logEffectiveStrmConfig(syncPath, s.Config.VideoExt, s.Config.MetaExt, s.Config.ExcludeNames)
}

func logEffectiveStrmConfig(syncPath *models.SyncPath, videoExt, metaExt, excludeNames []string) {
	if syncPath == nil {
		return
	}
	helpers.AppLogger.Infof(
		"同步目录 %d 生效 STRM 配置：视频扩展名=%s（来源=%s），元数据扩展名=%s（来源=%s），排除名称=%s（来源=%s）",
		syncPath.ID,
		formatStrmConfigArray(videoExt),
		strmArrayConfigSource(syncPath.CustomConfig, syncPath.VideoExtArr),
		formatStrmConfigArray(metaExt),
		strmArrayConfigSource(syncPath.CustomConfig, syncPath.MetaExtArr),
		formatStrmConfigArray(excludeNames),
		strmArrayConfigSource(syncPath.CustomConfig, syncPath.ExcludeNameArr),
	)
}

func strmArrayConfigSource(customConfig bool, localValue []string) string {
	if customConfig && len(localValue) > 0 {
		return "同步目录自定义设置"
	}
	return "全局 STRM 设置"
}

func formatStrmConfigArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	quotedValues := make([]string, 0, len(values))
	for _, value := range values {
		quotedValues = append(quotedValues, strconv.Quote(value))
	}
	return "[" + strings.Join(quotedValues, ", ") + "]"
}

// 直接同步某个路径（可以是目录，也可以是文件）
func NewSyncStrmByPath(account *models.Account, sourcePath, sourcePathId string, targetPath string, isFile bool) *SyncStrm {
	config := SyncStrmConfig{
		EnableDownloadMeta:    int64(models.SettingsGlobal.DownloadMeta),
		MinVideoSize:          int64(models.SettingsGlobal.MinVideoSize),
		VideoExt:              models.SettingsGlobal.VideoExtArr,
		MetaExt:               models.SettingsGlobal.MetaExtArr,
		ExcludeNames:          models.SettingsGlobal.ExcludeNameArr,
		NetNotFoundFileAction: models.SyncTreeItemMetaAction(models.SettingsGlobal.UploadMeta),
		StrmUrlNeedPath:       models.SettingsGlobal.AddPath,
		DelEmptyLocalDir:      models.SettingsGlobal.DeleteDir == 1,
		CheckMetaMtime:        models.SettingsGlobal.CheckMetaMtime,
		StrmBaseUrl:           models.SettingsGlobal.StrmBaseUrl,
	}
	return NewSyncStrm(account, 0, sourcePath, sourcePathId, targetPath, config, false, 0, isFile)
}

func (s *SyncStrm) Stop() {
	if !s.stopped.CompareAndSwap(false, true) {
		s.Sync.Logger.Warnf("同步任务已停止或正在停止")
		return
	}

	s.Sync.Logger.Infof("正在停止同步任务…")
	s.Cancel()
}

func (s *SyncStrm) Start() error {
	// 开始任务时先暂停下载和上传队列
	// 关闭上传下载队列
	models.GlobalDownloadQueue.Stop()
	models.GlobalUploadQueue.Stop()
	defer func() {
		// 任务完成后启动上传下载队列
		models.GlobalDownloadQueue.Start()
		models.GlobalUploadQueue.Start()
	}()
	atomic.StoreInt64(&s.NewMeta, 0)
	atomic.StoreInt64(&s.NewStrm, 0)
	atomic.StoreInt64(&s.NewUpload, 0)
	atomic.StoreInt64(&s.TotalFile, 0)
	s.Sync.Logger.Infof("本次同步的入口目录：%s，目标目录：%s", s.SourcePath, s.TargetPath)
	s.logEffectiveStrmConfig()
	s.Sync.Logger.Infof("本次同步使用的 STRM 配置%+v", s.Config)
	s.Sync.UpdateStatus(models.SyncStatusInProgress)
	if s.IsFile {
		// 如果是文件，直接同步文件
		s.Sync.Logger.Infof("本次同步的文件：%s，目标目录：%s", s.SourcePath, s.TargetPath)
		// 直接同步文件
		if err := s.StartFile(); err != nil {
			s.Sync.Failed(fmt.Sprintf("文件同步失败：%v", err))
			return err
		}
	} else {
		newPathId, err := s.SyncDriver.GetPathIdByPath(s.Context, s.SourcePath)
		if err != nil {
			reason := err.Error()
			s.Sync.Failed(reason)
			return errors.New(reason)
		}
		if newPathId != s.SourcePathId {
			s.SourcePathId = newPathId
		}
		if !s.checkPathExists(s.TargetPath) {
			reason := fmt.Sprintf("目标路径 %s 不存在", s.TargetPath)
			s.Sync.Failed(reason)
			return errors.New(reason)
		}
		// 创建本地根目录
		localBaseDir := s.GetLocalBaseDir()
		if !s.checkPathExists(localBaseDir) {
			if err := os.MkdirAll(localBaseDir, 0777); err != nil {
				reason := fmt.Sprintf("创建本地根目录失败：%s，%v", localBaseDir, err)
				s.Sync.Failed(reason)
				return errors.New(reason)
			}
		}
		switch s.Account.SourceType {
		case models.SourceType115:
			s.Start115Sync()
		case models.SourceTypeBaiduPan:
			s.StartBaiduPanSync()
		default:
			// 如果是本地类型，先删除所有数据表中的数据
			if s.Account.SourceType == models.SourceTypeLocal {
				db.Db.Where("sync_path_id = ?", s.SyncPathId).Delete(&models.SyncFile{})
			}
			// 其他来源走一套逻辑
			s.StartOther()
		}
		s.Sync.Logger.Info("完成所有路径和文件的处理，检查是否有错误发生")
		select {
		case <-s.Context.Done():
			s.Sync.Failed(fmt.Sprintf("同步任务被取消：%v", s.Context.Err()))
			return nil
		case err := <-s.PathErrChan:
			s.Sync.Failed(fmt.Sprintf("路径队列处理失败：%v", err))
			return err
		default:
		}
		// 处理完所有路径和文件后，更新最后同步时间
		if s.SyncPathId > 0 {
			syncPath := models.GetSyncPathById(s.SyncPathId)
			if syncPath != nil {
				syncPath.UpdateLastSync()
			}
		}
		// 开始添加需要下载的文件到下载队列
		s.Sync.Logger.Info("开始将要下载的任务添加到下载队列")
		s.AddDownloadTaskFromMemCache()
		s.Sync.Logger.Infof("开始对比本地文件和临时表中的文件，删除多余的本地文件")
		if err := s.compareLocalFilesWithTempTable(); err != nil {
			return err
		}
	}
	s.Sync.NewMeta = int(s.NewMeta)
	s.Sync.NewStrm = int(s.NewStrm)
	s.Sync.NewUpload = int(s.NewUpload)
	s.Sync.Total = int(s.TotalFile)
	s.PublishProgress(true)
	s.Sync.Complete(s.Account.SourceType)
	// 如果有 sync_path_id，则更新最后同步时间
	if !s.TmpSyncPath {
		// 有 syncPathId 时，将 IsFullSync 改为 false
		if s.FullSync {
			db.Db.Model(&models.SyncPath{}).Where("id = ?", s.SyncPathId).Update("is_full_sync", false)
		}
		db.Db.Model(&models.SyncPath{}).Where("id = ?", s.SyncPathId).Update("last_sync_at", s.Sync.FinishAt)
		// 提交 Emby 媒体库刷新任务，由协调器等待相关下载任务完成
		go func() {
			if shouldRequestEmbyLibraryRefresh(s.NewMeta, s.NewStrm) {
				s.Sync.Logger.Info("有新的元数据文件或 STRM 文件，提交 Emby 媒体库刷新任务")
				targets := s.drainEmbyRefreshTargets()
				if len(targets) == 0 {
					targets = []models.EmbyRefreshTarget{{TargetType: models.EmbyRefreshTargetTypeLibrary}}
				}
				if err := models.RequestEmbyRefreshTargets(s.SyncPathId, targets); err != nil {
					s.Sync.Logger.Errorf("提交 Emby 媒体库刷新任务失败：%v", err)
				}
			}
			if s.NewStrm > 0 {
				s.Sync.Logger.Info("准备触发关联的刮削任务")
				syncPath := models.GetSyncPathById(s.SyncPathId)
				scrapePathIds := syncPath.GetScrapePathIds()
				if len(scrapePathIds) > 0 {
					// 发送异步消息，防止循环引用
					helpers.Publish(helpers.StrmSyncCompleteEvent, scrapePathIds)
				} else {
					s.Sync.Logger.Info("关联的刮削目录为空，跳过触发刮削任务")
				}
			} else {
				s.Sync.Logger.Info("没有新的 STRM 生成，跳过关联的刮削任务")
			}
		}()
		// 处理差异
		go func() {
			s.Sync.Logger.Info("115 路径和文件同步完成，开始处理 SyncFile 表和同步缓存的数据差异")
			s.handleTempTableDiff()
			s.Sync.Logger.Info("完成差异比对，并更新了 SyncFile 表，任务已完成")
		}()
	}
	return nil
}

func (s *SyncStrm) recordEmbyRefreshTarget(syncFile *models.SyncFile) {
	if s == nil || syncFile == nil {
		return
	}
	target, err := models.ResolveEmbyRefreshTarget(syncFile)
	if err != nil {
		s.Sync.Logger.Warnf("解析 Emby 刷新目标失败: sync_file_id=%d err=%v", syncFile.ID, err)
		target = models.EmbyRefreshTarget{TargetType: models.EmbyRefreshTargetTypeLibrary}
	}
	s.appendEmbyRefreshTarget(target)
}

func (s *SyncStrm) appendEmbyRefreshTarget(target models.EmbyRefreshTarget) {
	if s == nil {
		return
	}
	s.embyRefreshTargetsMu.Lock()
	defer s.embyRefreshTargetsMu.Unlock()
	s.embyRefreshTargets = append(s.embyRefreshTargets, target)
}

func (s *SyncStrm) drainEmbyRefreshTargets() []models.EmbyRefreshTarget {
	if s == nil {
		return nil
	}
	s.embyRefreshTargetsMu.Lock()
	defer s.embyRefreshTargetsMu.Unlock()
	targets := append([]models.EmbyRefreshTarget(nil), s.embyRefreshTargets...)
	s.embyRefreshTargets = nil
	return targets
}

func shouldRequestEmbyLibraryRefresh(newMeta, newStrm int64) bool {
	return newMeta > 0 || newStrm > 0
}

// 处理网盘文件，生成 STRM 或添加下载任务
func (s *SyncStrm) processNetFile(file *SyncFileCache) error {
	// 1. 检查对应的本地文件是否存在
	// s.Sync.Logger.Infof("正在处理网盘文件 %s => %s", file.FileId, file.FileName)
	localFilePath := file.GetLocalFilePath(s.TargetPath, s.SourcePath)
	s.Sync.Logger.Infof("115 本地文件和网盘对照路径：本地=%s，文件 ID=%s，远程=%s，PickCode=%s", localFilePath, file.GetFileId(), file.GetFullRemotePath(), file.GetPickCode(""))
	// 先处理重命名，只有非临时同步才会处理重命名，临时同步只会删除重建
	var existingFile models.SyncFile
	if !s.TmpSyncPath {
		err := db.Db.Where("file_id = ? AND sync_path_id = ?", file.GetFileId(), s.SyncPathId).First(&existingFile).Error
		if err == nil {
			// 如果 SyncFiles 存在，检查是否需要重命名。只有所在目录相同时才能重命名，否则走删除重建流程
			if existingFile.FileName != file.FileName && existingFile.Path == file.Path {
				// 需要重命名
				err := os.Rename(existingFile.LocalFilePath, localFilePath)
				if err != nil {
					// 只记录日志，不报错（因为重命名失败不影响后续处理，会自动转入删除、重建流程）
					s.Sync.Logger.Errorf("重命名失败 %s => %s：%v", existingFile.LocalFilePath, localFilePath, err)
				} else {
					s.Sync.Logger.Infof("重命名成功 %s => %s", existingFile.LocalFilePath, localFilePath)
				}
			}
		}
	}
	if file.IsVideo {
		// s.Sync.Logger.Infof("正在处理视频文件 %s", file.FileId)
		if strings.Contains(file.Path, "**") {
			s.Sync.Logger.Infof("文件 ID %s 所在目录 %s 包含 ** 号，跳过生成 STRM", file.FileId, file.Path)
			return nil
		}
		return s.ProcessStrmFile(file)
	}
	// 再处理元数据文件
	if file.IsMeta {
		if !helpers.PathExists(localFilePath) {
			// 如果文件不存在，则判断是否需要下载，使用 STRM 设置
			if s.Config.EnableDownloadMeta == 1 {
				// 检查目录是否符合规则
				if strings.Contains(file.Path, "**") {
					s.Sync.Logger.Infof("文件 ID %s 所在目录 %s 包含 ** 号，跳过下载", file.FileId, file.Path)
					return nil
				}
				// 允许下载，添加到下载列表
				s.AddDownloadTaskTemp(file)
				// err := models.AddDownloadTaskFromSyncFile(file)

			}
		}
		return nil
	}
	return nil
}

// 添加下载任务（不实际添加，先记录起来，任务完成后，统一处理）
func (s *SyncStrm) AddDownloadTaskTemp(file *SyncFileCache) {
	file.NeedDownload = true
	// 生成下载索引
	s.memSyncCache.InsertDownloadIndex(file)
}

func (s *SyncStrm) addMetaDownloadTask(file *models.SyncFile) error {
	if err := models.AddDownloadTaskFromSyncFile(file); err != nil {
		return err
	}
	s.recordEmbyRefreshTarget(file)
	atomic.AddInt64(&s.NewMeta, 1)
	s.PublishProgress(false)
	return nil
}

// 遍历同步缓存，添加下载任务
// 先提取下载队列中未完成的任务，再遍历内存同步缓存，把需要下载且未在队列中的文件加入下载队列
func (s *SyncStrm) AddDownloadTaskFromMemCache() {
	existingDownloads := s.pendingDownloadFileIDs()
	// 遍历内存同步缓存的下载索引
	s.memSyncCache.mu.RLock()
	for _, file := range s.memSyncCache.downloadIndex {
		if _, exists := existingDownloads[file.GetPickCode(s.Account.BaseUrl)]; exists {
			// 已经存在下载任务，跳过
			continue
		}
		// 添加下载任务
		err := s.addMetaDownloadTask(file.GetSyncFile(s, s.Account.BaseUrl))
		if err == nil {
			s.Sync.Logger.Infof("添加下载任务成功：%s => %s", file.Path+"/"+file.FileName, file.GetLocalFilePath(s.TargetPath, s.SourcePath))
		}
	}
	s.memSyncCache.mu.RUnlock()
}

func (s *SyncStrm) pendingDownloadFileIDs() map[string]bool {
	existingDownloads := make(map[string]bool)
	offset := 0
	limit := 1000
	type existDownloadTask struct {
		RemoteFileId string `json:"remote_file_id"`
	}
	for {
		var batch []existDownloadTask
		err := db.Db.Model(models.DbDownloadTask{}).Select("remote_file_id").Where("source_type = ? AND status IN ?", s.Account.SourceType, []int{int(models.DownloadStatusPending), int(models.DownloadStatusDownloading)}).
			Offset(offset).Limit(limit).Order("id ASC").Find(&batch).Error
		if err != nil {
			s.Sync.Logger.Errorf("获取未完成的下载任务失败：%v", err)
			break
		}
		for _, item := range batch {
			existingDownloads[item.RemoteFileId] = true
		}
		if len(batch) == 0 || len(batch) < limit {
			break
		}
		offset += limit
	}
	return existingDownloads
}

// 对比本地文件和临时表中的文件
func (s *SyncStrm) compareLocalFilesWithTempTable() error {
	s.Sync.UpdateSubStatus(models.SyncSubStatusProcessLocalFileList)
	select {
	case <-s.Context.Done():
		s.Sync.Logger.Info("对比本地文件和临时表中的文件被取消")
		return nil
	default:
		rootPath := filepath.Join(s.TargetPath, s.SourcePath)
		if s.Account.SourceType == models.SourceTypeLocal {
			rootPath = s.TargetPath
		}

		s.Sync.Logger.Infof("开始对比本地文件和同步缓存中的文件，根目录：%s", rootPath)
		// 对比本地文件和临时表中的文件
		filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
			path = filepath.ToSlash(path)
			select {
			case <-s.Context.Done():
				return nil
			default:
				// 不处理目录（只处理文件）
				if err != nil || path == "." || strings.Contains(path, ".verysync") || strings.Contains(path, ".deletedByTMM") {
					// 跳过根目录本身
					// 跳过微力同步和 TMM 的临时目录中的文件
					s.Sync.Logger.Infof("跳过文件 %s，错误：%v", path, err)
					return nil
				}
				if info.IsDir() {
					if s.Config.DelEmptyLocalDir {
						// 如果目录是空的则删除目录
						dirEntries, rerr := os.ReadDir(path)
						if rerr != nil {
							s.Sync.Logger.Errorf("读取目录 %s 的文件列表失败：%v", path, err)
							return nil
						}
						if len(dirEntries) == 0 {
							os.Remove(path)
							s.Sync.Logger.Infof("删除空目录 %s", path)
						} else {
							s.Sync.Logger.Infof("本地目录 %s 不是空目录，跳过删除", path)
						}
					} else {
						s.Sync.Logger.Infof("当前设置不允许删除空目录，跳过本地目录 %s", path)
					}
					return nil
				}
				ext := filepath.Ext(info.Name())
				isVideo := ext == ".strm"
				isMeta := s.IsValidMetaExt(info.Name())
				if isMeta && s.Config.EnableDownloadMeta == 0 {
					// 如果是元数据文件且设置为不下载，则跳过检查（代表着不上传）
					s.Sync.Logger.Infof("本地元数据文件 %s 由于关闭了元数据下载所以不需要处理", info.Name())
					return nil
				}
				if !isVideo && !isMeta {
					// 非视频文件和元数据文件，跳过
					s.Sync.Logger.Debugf("本地文件 %s 既不是 STRM 文件也不是元数据文件，跳过", path)
					return nil
				}
				// 检查文件在临时表是否存在
				existsFile, err := s.memSyncCache.GetByLocalPath(path)
				if err != nil {
					s.Sync.Logger.Warnf("查询同步缓存失败 %s：%v", path, err)
				}
				s.Sync.Logger.Infof("对比本地文件 %s，是否存在于网盘：%v", path, existsFile)
				if isVideo {
					// STRM 文件，检查文件在临时表是否存在，不存在需要删除临时文件
					if existsFile != nil {
						return nil
					}
					// s.Sync.Logger.Warnf("本地文件在网盘不存在，删除本地 STRM 文件：%s", path)
					s.RemoveFileAndCheckDirEmtry(path)
					return nil
				}
				if isMeta {
					// 如果选择忽略，则跳过
					if s.Config.NetNotFoundFileAction == models.SyncTreeItemMetaActionKeep {
						s.Sync.Logger.Infof("本地元数据文件 %s 由于设置为保留所以不需要处理", path)
						return nil
					}
					// 如果选择删除，则检查是否存在，不存在则删除
					if s.Config.NetNotFoundFileAction == models.SyncTreeItemMetaActionDelete && existsFile == nil {
						s.RemoveFileAndCheckDirEmtry(path)
						return nil
					}
					// 如果允许上传，则检查是否需要上传（文件在网盘不存在）
					if s.Config.NetNotFoundFileAction == models.SyncTreeItemMetaActionUpload && existsFile == nil {
						// 检查 dbupload 表中是否已经有对应的上传任务
						canUpload := models.CheckCanUploadByLocalPath(models.UploadSourceStrm, path)
						if !canUpload {
							s.Sync.Logger.Infof("本地元数据文件 %s 由于存在上传任务所以不需要处理", path)
							return nil
						}
						sourceRootPath := filepath.ToSlash(filepath.Join(s.TargetPath, s.Sync.RemotePath))
						if s.Account.SourceType == models.SourceTypeLocal {
							sourceRootPath = s.Sync.RemotePath
						}
						// 添加上传任务
						// 检查文件是否可以上传
						// 普通元数据文件需要父目录存在才可以上传，允许上传目录下的文件需要循环创建目录上传
						parentDir := filepath.Dir(path)
						parentName := filepath.Base(parentDir)
						if parentDir != "" {
							parentDir = filepath.ToSlash(parentDir)
						}
						isAllowedUploadDir := slices.Contains(uploadDirNames, strings.ToLower(parentName))
						// 检查父目录是否在网盘存在
						existsPath, _ := s.memSyncCache.GetByLocalPath(parentDir)
						// 如果不存在，检查是否可以创建目录
						var parentPath, parentPathId, remotePath string
						s.Sync.Logger.Infof("准备上传本地元数据文件 %s，检查父目录 %s 是否存在网盘", parentDir, sourceRootPath)
						if existsPath == nil && parentDir != sourceRootPath {
							if !isAllowedUploadDir {
								s.Sync.Logger.Infof("父目录 %s 不存在网盘，进入删除流程 %s，", parentDir, path)
								s.RemoveFileAndCheckDirEmtry(path)
								return nil
							} else {
								// 递归创建目录，调用对应的 driver
								parentPathId, remotePath, err = s.SyncDriver.CreateDirRecursively(s.Context, parentDir)
								if err != nil {
									s.Sync.Logger.Errorf("创建目录 %s 失败：%v", parentDir, err)
									return nil
								}
								parentPath = parentDir
							}
						} else {
							if parentDir == sourceRootPath {
								parentPath = sourceRootPath
								parentPathId = s.SourcePathId
								remotePath = s.SourcePath
							} else {
								parentPath = parentDir
								parentPathId = existsPath.GetFileId()
								remotePath = fmt.Sprintf("%s/%s", existsPath.Path, existsPath.FileName)
							}
						}
						// 加入上传队列
						db115File := &models.SyncFile{
							AccountId:     s.Account.ID,
							SyncPathId:    s.SyncPathId,
							SourceType:    s.Account.SourceType,
							FileType:      v115open.TypeFile,
							FileId:        "", // 上传前 FileId 为空
							ParentId:      parentPathId,
							FileName:      info.Name(),
							Path:          remotePath,
							FileSize:      info.Size(),
							MTime:         info.ModTime().Unix(),
							IsMeta:        isMeta,
							IsVideo:       isVideo,
							LocalFilePath: filepath.Join(parentPath, info.Name()),
						}
						s.Sync.Logger.Infof("准备添加上传任务，路径检查：文件 ID=%s，路径=%s，文件名=%s", db115File.FileId, db115File.Path, db115File.FileName)
						if s.Account.SourceType != models.SourceTypeLocal {
							db115File.FileId = filepath.ToSlash(filepath.Join(db115File.Path, db115File.FileName))
						} else {
							db115File.FileId = filepath.Join(sourceRootPath, db115File.Path, db115File.FileName)
						}
						models.AddUploadTaskFromSyncFile(db115File)
						atomic.AddInt64(&s.NewUpload, 1)
						s.PublishProgress(false)
						return nil
					}
					// 网盘存在且设置为上传，需要检查本地是不是比网盘新，如果是的话，需要删除网盘文件并将本地文件上传
					if existsFile != nil && s.Config.CheckMetaMtime == 1 {
						localMTime := info.ModTime().Unix()
						// 网盘比本地新，重新下载
						// 1. 删除本地文件
						// 2. 添加下载任务
						if localMTime < existsFile.MTime {
							s.Sync.Logger.Infof("本地元数据文件 %s 由于修改时间比网盘旧 %d < %d 所以需要重新下载", path, localMTime, existsFile.MTime)
							// 1. 删除本地文件
							s.RemoveFileAndCheckDirEmtry(path)

							// 2. 添加下载任务
							if err := s.addMetaDownloadTask(existsFile.GetSyncFile(s, s.Account.BaseUrl)); err != nil {
								s.Sync.Logger.Warnf("添加元数据重新下载任务失败：%v", err)
							}
							return nil
						}

						if localMTime > existsFile.MTime && s.Config.NetNotFoundFileAction == models.SyncTreeItemMetaActionUpload {
							// 本地比网盘新，需要删除网盘旧文件并上传新文件
							s.Sync.Logger.Infof("本地元数据文件 %s 由于修改时间比网盘新 %d > %d 所以需要上传", path, localMTime, existsFile.MTime)
							// 1. 删除网盘旧文件
							err := s.SyncDriver.DeleteFile(s.Context, existsFile.ParentId, []string{existsFile.GetFileId()})
							if err != nil {
								s.Sync.Logger.Errorf("删除网盘旧文件 %s 失败：%v", existsFile.GetFileId(), err)
								return nil
							}
							// 2. 添加上传任务
							models.AddUploadTaskFromSyncFile(existsFile.GetSyncFile(s, s.Account.BaseUrl))
							atomic.AddInt64(&s.NewUpload, 1)
							s.PublishProgress(false)

							// 3. 删除数据库记录（下次同步时会将新上传的文件插入数据库）
							s.memSyncCache.DeleteByFileId(existsFile.GetFileId())
							return nil
						}
					}
				}
			}
			return nil
		})
	}
	return nil
}

// 处理 SyncFile 表和内存同步缓存的数据差异
// 同步缓存存在但 SyncFile 没有的记录需要插入
// 同步缓存没有但 SyncFile 存在的记录需要删除
func (s *SyncStrm) handleTempTableDiff() error {
	// 先删除 SyncFile 表中有、同步缓存中没有的记录，同时更新两边都有的数据；最后同步缓存中留下的就是新增数据
	offset := 0
	limit := 1000
	i := 0
	// 要删除的 ID
	waitDeleteIds := make([]uint, 0)
	s.Sync.Logger.Infof("内存同步缓存中共有 %d 条数据，开始处理", s.memSyncCache.Count())
	for {
		var batch []models.SyncFile
		err := db.Db.Where("sync_path_id = ?", s.SyncPathId).Offset(offset).Limit(limit).Order("id ASC").Find(&batch).Error
		if err != nil {
			s.Sync.Logger.Warnf("获取 SyncFile 表数据失败：%v", err)
			return err
		}
		if len(batch) == 0 {
			s.Sync.Logger.Info("SyncFile 表数据全部处理完毕")
			break
		}
		for _, file := range batch {
			syncFileCache, _ := s.memSyncCache.GetByFileId(file.FileId)
			if syncFileCache == nil {
				// 同步缓存中没有该文件，删除 SyncFile 记录
				waitDeleteIds = append(waitDeleteIds, file.ID)
				s.Sync.Logger.Infof("SyncFile 表数据 ID=%d 在同步缓存中不存在，已标记为删除", file.ID)
			} else {
				// 双方都有，更新 SyncFile 记录
				// 主要更新 name、size、m_time、path、local_file_path 等数据
				udpateData := map[string]interface{}{
					"file_name":       syncFileCache.FileName,
					"file_size":       syncFileCache.FileSize,
					"m_time":          syncFileCache.MTime,
					"path":            syncFileCache.GetPath(),
					"local_file_path": syncFileCache.LocalFilePath,
					"thumb_url":       syncFileCache.ThumbUrl,
					"openlist_sign":   syncFileCache.OpenlistSign,
					"sha1":            syncFileCache.Sha1,
					"parent_id":       syncFileCache.ParentId,
				}
				err := db.Db.Model(&models.SyncFile{}).Where("id = ?", file.ID).Updates(udpateData).Error
				if err != nil {
					s.Sync.Logger.Errorf("更新 SyncFile 表数据失败，ID=%d：%v", file.ID, err)
					continue
				}
				// 然后从同步缓存中移除该记录
				s.memSyncCache.DeleteByFileId(file.FileId)
				// s.Sync.Logger.Infof("SyncFile 表数据 ID=%d 在同步缓存中存在，已更新并移除同步缓存记录", file.ID)
				if i == 10 {
					time.Sleep(10 * time.Microsecond) // 短暂休息，避免对数据库请求过密，也让其他协程有机会写入数据库
					i = 0
				} else {
					i++
				}
			}
		}
		if len(batch) < limit {
			break
		}
		offset += limit
	}
	if len(waitDeleteIds) > 0 {
		s.Sync.Logger.Infof("SyncFile 表中共有 %d 条多余数据需要删除，开始分批删除，每批 1000 条", len(waitDeleteIds))
		// 分批删除
		batchSize := 1000
		if len(waitDeleteIds) <= batchSize {
			// 一次性删除
			err := db.Db.Where("id IN ?", waitDeleteIds).Delete(&models.SyncFile{}).Error
			if err != nil {
				s.Sync.Logger.Errorf("删除 SyncFile 表数据失败：%v", err)
				return err
			}
		} else {
			i = 0
			for i := 0; i < len(waitDeleteIds); i += batchSize {
				end := i + batchSize
				if end > len(waitDeleteIds) {
					end = len(waitDeleteIds)
				}
				batchIds := waitDeleteIds[i:end]
				err := db.Db.Where("id IN ?", batchIds).Delete(&models.SyncFile{}).Error
				if err != nil {
					s.Sync.Logger.Errorf("删除 SyncFile 表数据失败：%v", err)
					return err
				}
				if i == 10 {
					time.Sleep(10 * time.Microsecond) // 短暂休息，避免对数据库请求过密，也让其他协程有机会写入数据库
					i = 0
				} else {
					i++
				}
			}
			s.Sync.Logger.Infof("删除 SyncFile 表中 %d 条多余数据成功", len(waitDeleteIds))
		}
		s.Sync.Logger.Infof("已删除所有网盘不存在的文件记录，开始插入新增的文件记录")
	}
	waitDeleteIds = nil // 清空切片
	// 然后插入同步缓存中剩余的新增数据
	// 不会并发执行该方法，所以可以直接读取
	fileItems := s.memSyncCache.GetAllFile()
	s.Sync.Logger.Infof("内存同步缓存中共有 %d 条新增数据需要插入", len(fileItems))
	if len(fileItems) == 0 {
		// s.Sync.Logger.Info("内存同步缓存数据全部处理完毕")
		return nil
	}
	i = 0
	for _, file := range fileItems {
		syncFile := file.GetSyncFile(s, s.Account.BaseUrl)
		err := db.Db.Save(syncFile).Error
		if err != nil {
			s.Sync.Logger.Errorf("插入 SyncFile 表数据失败，FileID=%s：%v", file.GetFileId(), err)
			continue
		}
		// s.Sync.Logger.Infof("插入 SyncFile 表数据成功，FileID=%s", file.GetFileId())
		// 插入成功后，从同步缓存中移除该记录
		s.memSyncCache.DeleteByFileId(file.GetFileId())
		if i == 10 {
			time.Sleep(10 * time.Microsecond) // 短暂休息，避免对数据库请求过密，也让其他协程有机会写入数据库
			i = 0
		} else {
			i++
		}

	}
	s.Sync.Logger.Infof("已插入所有新增文件记录，内存同步缓存中剩余 %d 条数据", s.memSyncCache.Count())
	return nil
}
