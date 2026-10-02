package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
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

type metadataMtimeAction int

const (
	metadataMtimeActionNone metadataMtimeAction = iota
	metadataMtimeActionDownload
	metadataMtimeActionUpload
	metadataMtimeActionAlign
	metadataMtimeActionSkipChanged
)

var calculateMetadataFileSHA1 = helpers.FileSHA1

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

	lastProgressPublishedAt   time.Time
	progressMu                sync.Mutex
	progressDone              chan struct{} // 唯一发布者或状态写入者结束后关闭
	progressFinished          bool
	scanMu                    sync.Mutex
	scanFiles                 map[string]string
	scanFailures              map[string]realtime.SyncScanFailure
	protectedFileIDs          map[string]bool
	protectedPickCodes        map[string]bool
	protectedLocalPaths       map[string]bool
	protectedLocalDirectories map[string]bool
	missingCleanupReason      string
	cleanupStarted            bool
	generationSaved           bool
	ownsLogger                bool
	backgroundService         *syncBackgroundService
	scopeRelease              func()
	scopeLocalRoot            string

	// 停止状态：避免多次触发停止
	stopped atomic.Bool

	// 115 同步器
	sync115 *Sync115

	memSyncCache *MemorySyncCache // 同步缓存

	embyRefreshTargetsMu sync.Mutex
	embyRefreshTargets   []models.EmbyRefreshTarget
}

type pathQueueItem struct {
	Path     string // 路径
	PathId   string // 路径 ID，OpenList 和本地的 Path、PathId 相同
	PickCode string // 百度目录的稳定 fs_id；PathId 会随移动变化
	Depth    uint   // 路径深度
	Mtime    int64  // 最后修改时间
}

func NewSyncStrm(account *models.Account, syncPathId uint, sourcePath, sourcePathId, targetPath string, config SyncStrmConfig, IsFullSync bool, lastSyncAt int64, isFile bool) *SyncStrm {
	return newSyncStrm(account, syncPathId, sourcePath, sourcePathId, targetPath, config, IsFullSync, lastSyncAt, isFile, true)
}

func newSyncStrm(account *models.Account, syncPathId uint, sourcePath, sourcePathId, targetPath string, config SyncStrmConfig, IsFullSync bool, lastSyncAt int64, isFile bool, createSyncRecord bool) *SyncStrm {
	if err := config.compileExcludeNameRegexes(); err != nil {
		effectiveSyncLogger().Errorf("初始化 STRM 名称排除规则失败：%v", err)
		return nil
	}
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
	if account.SourceType == models.SourceTypeBaiduPan {
		// 保存目录可能省略前导斜杠；百度列表和失败保护统一使用绝对网盘路径。
		sourcePath = path.Clean("/" + filepath.ToSlash(sourcePath))
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
		s.ownsLogger = true
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

// beginSyncUpdate 保证一次只有一个进度或状态更新；执行 SQL 和发送事件时不占用互斥锁。
func (s *SyncStrm) beginSyncUpdate(ctx context.Context, wait bool) (bool, error) {
	if s == nil || s.Sync == nil || s.Sync.ID == 0 {
		return false, nil
	}
	for {
		s.progressMu.Lock()
		if s.progressFinished {
			s.progressMu.Unlock()
			return false, nil
		}
		if err := ctx.Err(); err != nil {
			s.progressMu.Unlock()
			return false, err
		}
		done := s.progressDone
		if done == nil {
			if !wait && !s.lastProgressPublishedAt.IsZero() && time.Since(s.lastProgressPublishedAt) < syncProgressPublishInterval {
				s.progressMu.Unlock()
				return false, nil
			}
			s.progressDone = make(chan struct{})
			s.progressMu.Unlock()
			return true, nil
		}
		s.progressMu.Unlock()
		if !wait {
			return false, nil
		}
		select {
		case <-done:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

func (s *SyncStrm) endSyncUpdate(finished bool) {
	s.progressMu.Lock()
	s.progressFinished = finished
	close(s.progressDone)
	s.progressDone = nil
	s.progressMu.Unlock()
}

// PublishProgress 按固定间隔发布运行统计；force 会等正在进行的更新完成后再发布。
func (s *SyncStrm) PublishProgress(force bool) error {
	if s == nil {
		return nil
	}
	started, err := s.beginSyncUpdate(s.Context, force)
	if err != nil || !started {
		return err
	}
	defer s.endSyncUpdate(false)
	return s.saveProgress(s.Context)
}

// 调用方已排除其他更新，不会与进度、阶段切换或任务结束同时修改记录。
func (s *SyncStrm) saveProgress(ctx context.Context) error {
	snapshot := *s.Sync
	if err := snapshot.UpdateProgress(ctx,
		int(atomic.LoadInt64(&s.TotalFile)), int(atomic.LoadInt64(&s.NewStrm)),
		int(atomic.LoadInt64(&s.NewMeta)), int(atomic.LoadInt64(&s.NewUpload))); err != nil {
		return err
	}
	s.Sync.Total, s.Sync.NewStrm, s.Sync.NewMeta, s.Sync.NewUpload = snapshot.Total, snapshot.NewStrm, snapshot.NewMeta, snapshot.NewUpload
	s.Sync.UpdatedAt = snapshot.UpdatedAt
	s.progressMu.Lock()
	s.lastProgressPublishedAt = time.Now()
	s.progressMu.Unlock()
	return nil
}

func (s *SyncStrm) updateSyncSubStatus(status models.SyncSubStatus) error {
	started, err := s.beginSyncUpdate(s.Context, true)
	if err != nil || !started {
		return err
	}
	defer s.endSyncUpdate(false)
	snapshot := *s.Sync
	if err := snapshot.UpdateSubStatus(s.Context, status); err != nil {
		return err
	}
	s.Sync.SubStatus, s.Sync.UpdatedAt = snapshot.SubStatus, snapshot.UpdatedAt
	s.Sync.NetFileStartAt, s.Sync.NetFileFinishAt, s.Sync.LocalFileStartAt = snapshot.NetFileStartAt, snapshot.NetFileFinishAt, snapshot.LocalFileStartAt
	return nil
}

func (s *SyncStrm) startSync() error {
	started, err := s.beginSyncUpdate(s.Context, true)
	if err != nil || !started {
		return err
	}
	defer s.endSyncUpdate(false)
	return s.Sync.UpdateStatus(s.Context, models.SyncStatusInProgress)
}

func (s *SyncStrm) completeSync() error {
	started, err := s.beginSyncUpdate(s.Context, true)
	if err != nil || !started {
		return err
	}
	finished := false
	defer func() { s.endSyncUpdate(finished) }()
	if err := s.saveProgress(s.Context); err != nil {
		return err
	}
	snapshot := *s.Sync
	snapshot.ScanResult = s.scanResultSnapshot()
	snapshot.IsFullSync = snapshot.IsFullSync || s.FullSync
	ledgerStatus := realtime.SyncLedgerNotRequired
	if !s.TmpSyncPath {
		ledgerStatus = realtime.SyncLedgerPending
	}
	snapshot.LedgerStatus = &ledgerStatus
	status, outcomeErr := s.scanOutcome()
	reason := ""
	if outcomeErr != nil {
		reason = outcomeErr.Error()
	}
	if err := snapshot.SaveGeneration(s.Context, status, reason, !s.TmpSyncPath && s.SyncPathId > 0); err != nil {
		return err
	}
	s.Sync.Status, s.Sync.FailReason, s.Sync.FinishAt = snapshot.Status, snapshot.FailReason, snapshot.FinishAt
	s.Sync.LocalFileFinishAt, s.Sync.UpdatedAt = snapshot.LocalFileFinishAt, snapshot.UpdatedAt
	s.Sync.ScanResult, s.Sync.IsFullSync = snapshot.ScanResult, snapshot.IsFullSync
	s.Sync.LedgerStatus, s.Sync.LedgerFinishedAt, s.Sync.LedgerError = snapshot.LedgerStatus, snapshot.LedgerFinishedAt, snapshot.LedgerError
	s.generationSaved = true
	finished = true
	return nil
}

func (s *SyncStrm) failSync(cause error) error {
	// 取消后仍有最多 5 秒用于等待旧发布并保存真实失败，不能无限阻塞来源队列。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.Context), 5*time.Second)
	defer cancel()
	started, err := s.beginSyncUpdate(ctx, true)
	if err != nil || !started {
		return errors.Join(cause, err)
	}
	defer s.endSyncUpdate(true)
	snapshot := *s.Sync
	snapshot.Total, snapshot.NewStrm = int(atomic.LoadInt64(&s.TotalFile)), int(atomic.LoadInt64(&s.NewStrm))
	snapshot.NewMeta, snapshot.NewUpload = int(atomic.LoadInt64(&s.NewMeta)), int(atomic.LoadInt64(&s.NewUpload))
	snapshot.ScanResult = s.scanResultSnapshot()
	snapshot.IsFullSync = snapshot.IsFullSync || s.FullSync
	ledgerStatus := realtime.SyncLedgerNotRequired
	snapshot.LedgerStatus = &ledgerStatus
	status := models.SyncStatusFailed
	if s.Context.Err() != nil || s.stopped.Load() {
		status = models.SyncStatusCancelled
	}
	if err := snapshot.SaveGeneration(ctx, status, cause.Error(), false); err != nil {
		return errors.Join(cause, err)
	}
	s.Sync.Status, s.Sync.FailReason, s.Sync.FinishAt = snapshot.Status, snapshot.FailReason, snapshot.FinishAt
	s.Sync.LocalFileFinishAt, s.Sync.UpdatedAt = snapshot.LocalFileFinishAt, snapshot.UpdatedAt
	s.Sync.ScanResult, s.Sync.IsFullSync = snapshot.ScanResult, snapshot.IsFullSync
	s.Sync.LedgerStatus, s.Sync.LedgerFinishedAt, s.Sync.LedgerError = snapshot.LedgerStatus, snapshot.LedgerFinishedAt, snapshot.LedgerError
	s.Sync.Total, s.Sync.NewStrm, s.Sync.NewMeta, s.Sync.NewUpload = snapshot.Total, snapshot.NewStrm, snapshot.NewMeta, snapshot.NewUpload
	s.generationSaved = true
	return cause
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
	config := configFromSyncPath(syncPath)
	return newSyncStrmWithConfig(syncPath, account, config, createSyncRecord)
}

func newSyncStrmWithConfig(syncPath *models.SyncPath, account *models.Account, config SyncStrmConfig, createSyncRecord bool) *SyncStrm {
	if (account.SourceType == models.SourceType115 || account.SourceType == models.SourceTypeBaiduPan) && config.StrmBaseUrl == "" {
		helpers.AppLogger.Errorf("115 或百度网盘同步路径 %s 未配置 STRM 直连地址", syncPath.RemotePath)
		return nil
	}
	return newSyncStrm(account, syncPath.ID, syncPath.RemotePath, syncPath.BaseCid, syncPath.LocalPath, config, syncPath.IsFullSync, syncPath.LastSyncAt, false, createSyncRecord)
}

func configFromSyncPath(syncPath *models.SyncPath) SyncStrmConfig {
	defaults, _ := models.SettingsGlobal.StrmSnapshot()
	return configFromSyncPathDefaults(syncPath, defaults)
}

func configFromSyncPathDefaults(syncPath *models.SyncPath, defaults models.SettingStrm) SyncStrmConfig {
	setting := syncPath.SettingStrm
	if setting.DownloadMeta == -1 {
		setting.DownloadMeta = defaults.DownloadMeta
	}
	if setting.MinVideoSize == -1 {
		setting.MinVideoSize = defaults.MinVideoSize
	}
	if setting.UploadMeta == -1 {
		setting.UploadMeta = defaults.UploadMeta
	}
	if setting.AddPath == -1 {
		setting.AddPath = defaults.AddPath
	}
	if setting.DeleteDir == -1 {
		setting.DeleteDir = defaults.DeleteDir
	}
	if setting.CheckMetaMtime == -1 {
		setting.CheckMetaMtime = defaults.CheckMetaMtime
	}
	if len(setting.VideoExtArr) == 0 {
		setting.VideoExtArr = defaults.VideoExtArr
	}
	if len(setting.MetaExtArr) == 0 {
		setting.MetaExtArr = defaults.MetaExtArr
	}
	if len(setting.ExcludeNameArr) == 0 {
		setting.ExcludeNameArr = defaults.ExcludeNameArr
	}
	if len(setting.ExcludeNameRegexArr) == 0 {
		setting.ExcludeNameRegexArr = defaults.ExcludeNameRegexArr
	}
	if setting.StrmBaseUrl == "" {
		setting.StrmBaseUrl = defaults.StrmBaseUrl
	}
	config := SyncStrmConfig{
		EnableDownloadMeta:    int64(setting.DownloadMeta),
		MinVideoSize:          setting.MinVideoSize,
		VideoExt:              slices.Clone(setting.VideoExtArr),
		MetaExt:               slices.Clone(setting.MetaExtArr),
		ExcludeNames:          slices.Clone(setting.ExcludeNameArr),
		ExcludeNameRegexes:    slices.Clone(setting.ExcludeNameRegexArr),
		NetNotFoundFileAction: models.SyncTreeItemMetaAction(setting.UploadMeta),
		StrmUrlNeedPath:       setting.AddPath,
		DelEmptyLocalDir:      setting.DeleteDir == 1,
		CheckMetaMtime:        setting.CheckMetaMtime,
		StrmBaseUrl:           setting.StrmBaseUrl,
	}
	if syncPath.SourceType == models.SourceTypeOpenList {
		// OpenList 只使用自定义的 STRM 直连地址
		config.StrmBaseUrl = syncPath.SettingStrm.StrmBaseUrl
	}
	return config
}

func (s *SyncStrm) logEffectiveStrmConfig() {
	if s == nil || s.TmpSyncPath || s.SyncPathId == 0 {
		return
	}
	syncPath := models.GetSyncPathById(s.SyncPathId)
	if syncPath == nil {
		return
	}
	logEffectiveStrmConfig(syncPath, s.Config.VideoExt, s.Config.MetaExt, s.Config.ExcludeNames, s.Config.ExcludeNameRegexes)
}

func logEffectiveStrmConfig(syncPath *models.SyncPath, videoExt, metaExt, excludeNames, excludeNameRegexes []string) {
	if syncPath == nil {
		return
	}
	helpers.AppLogger.Infof(
		"同步目录 %d 生效 STRM 配置：视频扩展名=%s（来源=%s），元数据扩展名=%s（来源=%s），排除名称=%s（来源=%s），正则排除名称=%s（来源=%s）",
		syncPath.ID,
		formatStrmConfigArray(videoExt),
		strmArrayConfigSource(syncPath.CustomConfig, syncPath.VideoExtArr),
		formatStrmConfigArray(metaExt),
		strmArrayConfigSource(syncPath.CustomConfig, syncPath.MetaExtArr),
		formatStrmConfigArray(excludeNames),
		strmArrayConfigSource(syncPath.CustomConfig, syncPath.ExcludeNameArr),
		formatStrmConfigArray(excludeNameRegexes),
		strmArrayConfigSource(syncPath.CustomConfig, syncPath.ExcludeNameRegexArr),
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
		ExcludeNameRegexes:    models.SettingsGlobal.ExcludeNameRegexArr,
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

func (s *SyncStrm) Start() (resultErr error) {
	backgroundStarted := false
	defer func() {
		if !backgroundStarted {
			if s.generationSaved {
				s.startBackground(false)
			} else if s.ownsLogger {
				s.Sync.Logger.Close()
			}
		}
		s.releaseScope()
		s.Cancel()
	}()
	defer func() {
		if resultErr != nil {
			resultErr = s.failSync(resultErr)
		}
	}()
	if err := s.acquireScope(); err != nil {
		return err
	}
	// 等到目录空闲后再暂停传输，等待期间不影响上一轮任务。
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
	if err := s.startSync(); err != nil {
		return err
	}
	if s.IsFile {
		// 如果是文件，直接同步文件
		s.Sync.Logger.Infof("本次同步的文件：%s，目标目录：%s", s.SourcePath, s.TargetPath)
		// 直接同步文件
		if err := s.StartFile(); err != nil {
			return fmt.Errorf("文件同步失败：%w", err)
		}
	} else {
		newPathId, err := s.SyncDriver.GetPathIdByPath(s.Context, s.SourcePath)
		if err != nil {
			return err
		}
		if newPathId != s.SourcePathId {
			// 路径是同步入口的权威来源，ID 只是缓存。两者不一致往往意味着入口路径本身传错，
			// 覆盖后会扫描另一个目录，必须留下线索。
			s.Sync.Logger.Warnf("远端路径 %s 反查到的入口 ID 与任务携带的 ID 不一致，改用反查结果：%s => %s", s.SourcePath, s.SourcePathId, newPathId)
			s.SourcePathId = newPathId
		}
		if !s.checkPathExists(s.TargetPath) {
			reason := fmt.Sprintf("目标路径 %s 不存在", s.TargetPath)
			return errors.New(reason)
		}
		// 创建本地根目录
		localBaseDir := s.GetLocalBaseDir()
		if !s.checkPathExists(localBaseDir) {
			if err := os.MkdirAll(localBaseDir, 0777); err != nil {
				reason := fmt.Sprintf("创建本地根目录失败：%s，%v", localBaseDir, err)
				return errors.New(reason)
			}
		}
		switch s.Account.SourceType {
		case models.SourceType115:
			s.Start115Sync()
		case models.SourceTypeBaiduPan:
			s.StartBaiduPanSync()
		default:
			// 其他来源走一套逻辑
			s.StartOther()
		}
		s.Sync.Logger.Info("完成所有路径和文件的处理，检查是否有错误发生")
		select {
		case <-s.Context.Done():
			return fmt.Errorf("同步任务被取消：%w", s.Context.Err())
		case err := <-s.PathErrChan:
			return fmt.Errorf("路径队列处理失败：%w", err)
		default:
		}
		// 开始添加需要下载的文件到下载队列
		s.Sync.Logger.Info("开始将要下载的任务添加到下载队列")
		if err := s.AddDownloadTaskFromMemCache(); err != nil {
			return err
		}
		if err := s.prepareCleanupProtection(); err != nil {
			return err
		}
		s.Sync.Logger.Infof("开始对比本地文件和临时表中的文件，删除多余的本地文件")
		if err := s.compareLocalFilesWithTempTable(); err != nil {
			return err
		}
	}
	if err := s.completeSync(); err != nil {
		return err
	}
	s.startBackground(!s.TmpSyncPath)
	backgroundStarted = true
	_, outcomeErr := s.scanOutcome()
	return outcomeErr
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
	if s.scopeRelease != nil {
		if err := ensureGeneratedStrmPathWithinResolvedRoot(s.scopeLocalRoot, localFilePath); err != nil {
			return err
		}
	}
	s.Sync.Logger.Infof("115 本地文件和网盘对照路径：本地=%s，文件 ID=%s，远程=%s，PickCode=%s", localFilePath, file.GetFileId(), file.GetFullRemotePath(), file.GetPickCode(""))
	// 先确认账本可读；旧路径保留到新文件成功及完整清理阶段。
	if !s.TmpSyncPath {
		var existingFile models.SyncFile
		err := db.Db.WithContext(s.Context).Where("file_id = ? AND sync_path_id = ?", file.GetFileId(), s.SyncPathId).First(&existingFile).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return fatalSyncError(err)
		}
	}
	if file.IsVideo {
		// s.Sync.Logger.Infof("正在处理视频文件 %s", file.FileId)
		if strings.Contains(file.Path, "**") {
			s.Sync.Logger.Infof("文件 ID %s 所在目录 %s 包含 ** 号，跳过生成 STRM", file.FileId, file.Path)
			s.recordFileSkipped(file)
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
					s.recordFileSkipped(file)
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

func (s *SyncStrm) addMetaDownloadTask(file *models.SyncFile, baseline ...string) error {
	if err := models.AddDownloadTaskFromSyncFile(file, baseline...); err != nil {
		if errors.Is(err, models.ErrActiveDownloadTaskExists) {
			return nil
		}
		return err
	}
	s.recordEmbyRefreshTarget(file)
	atomic.AddInt64(&s.NewMeta, 1)
	s.PublishProgress(false)
	return nil
}

// 遍历同步缓存，添加下载任务
// 先提取下载队列中未完成的任务，再遍历内存同步缓存，把需要下载且未在队列中的文件加入下载队列
func (s *SyncStrm) AddDownloadTaskFromMemCache() error {
	existingDownloads, err := s.pendingDownloadFileIDs()
	if err != nil {
		return fatalSyncError(err)
	}
	// 遍历内存同步缓存的下载索引
	s.memSyncCache.mu.RLock()
	for _, file := range s.memSyncCache.downloadIndex {
		if _, exists := existingDownloads[file.GetPickCode(s.Account.BaseUrl)]; exists {
			// 已经存在下载任务，跳过
			continue
		}
		// 添加下载任务
		err := s.addMetaDownloadTask(file.GetSyncFile(s, s.Account.BaseUrl))
		if err != nil {
			s.memSyncCache.mu.RUnlock()
			s.recordFileFailure(file, err)
			return fatalSyncError(err)
		}
		s.Sync.Logger.Infof("添加下载任务成功：%s => %s", file.Path+"/"+file.FileName, file.GetLocalFilePath(s.TargetPath, s.SourcePath))
	}
	s.memSyncCache.mu.RUnlock()
	return nil
}

func (s *SyncStrm) pendingDownloadFileIDs() (map[string]bool, error) {
	existingDownloads := make(map[string]bool)
	if s == nil || s.Account == nil || s.SyncPathId == 0 {
		// 临时同步需要按每个文件的本地目标去重，由 AddDownloadTaskFromSyncFile 处理。
		return existingDownloads, nil
	}
	source := models.DownloadSourceStrm
	if s.Account.SourceType == models.SourceTypeLocal {
		source = models.DownloadSourceLocalFile
	}
	offset := 0
	limit := 1000
	type existDownloadTask struct {
		SourceType        models.SourceType `json:"source_type"`
		RemoteFileId      string            `json:"remote_file_id"`
		RemotePickCode    string            `json:"remote_pick_code"`
		RemoteDownloadUrl string            `json:"remote_download_url"`
		LocalSourcePath   string            `json:"local_source_path"`
	}
	for {
		var batch []existDownloadTask
		err := db.Db.WithContext(s.Context).Model(models.DbDownloadTask{}).Select("source_type, remote_file_id, remote_pick_code, remote_download_url, local_source_path").Where("source = ? AND source_type = ? AND account_id = ? AND sync_path_id = ? AND status IN ?", source, s.Account.SourceType, s.Account.ID, s.SyncPathId, []int{int(models.DownloadStatusPending), int(models.DownloadStatusDownloading)}).
			Offset(offset).Limit(limit).Order("id ASC").Find(&batch).Error
		if err != nil {
			s.Sync.Logger.Errorf("获取未完成的下载任务失败：%v", err)
			return nil, err
		}
		for _, item := range batch {
			switch item.SourceType {
			case models.SourceType115:
				pickCode := item.RemotePickCode
				if pickCode == "" {
					pickCode = item.RemoteFileId
				}
				existingDownloads[pickCode] = true
			case models.SourceTypeOpenList:
				existingDownloads[item.RemoteDownloadUrl] = true
			case models.SourceTypeLocal:
				existingDownloads[item.LocalSourcePath] = true
			default:
				existingDownloads[item.RemoteFileId] = true
			}
		}
		if len(batch) == 0 || len(batch) < limit {
			break
		}
		offset += limit
	}
	return existingDownloads, nil
}

// 对比本地文件和临时表中的文件
func (s *SyncStrm) compareLocalFilesWithTempTable() error {
	if err := s.updateSyncSubStatus(models.SyncSubStatusProcessLocalFileList); err != nil {
		return err
	}
	select {
	case <-s.Context.Done():
		s.Sync.Logger.Info("对比本地文件和临时表中的文件被取消")
		return s.Context.Err()
	default:
		rootPath := filepath.Join(s.TargetPath, s.SourcePath)
		if s.Account.SourceType == models.SourceTypeLocal {
			rootPath = s.TargetPath
		}

		s.Sync.Logger.Infof("开始对比本地文件和同步缓存中的文件，根目录：%s", rootPath)
		// 对比本地文件和临时表中的文件
		s.scanMu.Lock()
		s.cleanupStarted = true
		s.scanMu.Unlock()
		walkErr := filepath.Walk(rootPath, func(path string, info os.FileInfo, err error) error {
			path = filepath.ToSlash(path)
			select {
			case <-s.Context.Done():
				return s.Context.Err()
			default:
				if err != nil {
					s.recordScanFailure(s.remotePathForLocal(path), err)
					return nil
				}
				if s.cleanupProtected(path, info.IsDir()) {
					// 祖先仍继续遍历，完整兄弟目录保留独立清理资格。
					return nil
				}
				// 不处理目录（只处理文件）
				if path == "." || strings.Contains(path, ".verysync") || strings.Contains(path, ".deletedByTMM") {
					// 跳过根目录本身
					// 跳过微力同步和 TMM 的临时目录中的文件
					s.Sync.Logger.Infof("跳过文件 %s，错误：%v", path, err)
					return nil
				}
				if s.Account.SourceType == models.SourceType115 {
					remotePath, relErr := filepath.Rel(s.TargetPath, path)
					if relErr == nil && helpers.IsV115PlaybackPath(remotePath) {
						if info.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
				if info.IsDir() {
					if err := s.confirm115LocalUploadParent(path); err != nil {
						s.recordScanFailure(s.remotePathForLocal(path), err)
						if isFatalSyncError(err) {
							return err
						}
						return nil
					}
					if s.Config.DelEmptyLocalDir && s.missingCleanupReason == "" {
						// 如果目录是空的则删除目录
						dirEntries, rerr := os.ReadDir(path)
						if rerr != nil {
							s.Sync.Logger.Errorf("读取目录 %s 的文件列表失败：%v", path, rerr)
							s.recordScanFailure(s.remotePathForLocal(path), rerr)
							return nil
						}
						if len(dirEntries) == 0 {
							if err := os.Remove(path); err != nil {
								s.Sync.Logger.Warnf("删除空目录失败：local_path=%s，错误=%v", path, err)
								s.recordScanFailure(s.remotePathForLocal(path), err)
								return nil
							}
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
				if s.scopeRelease != nil {
					if err := ensureGeneratedStrmPathWithinResolvedRoot(s.scopeLocalRoot, path); err != nil {
						s.recordLocalFileFailure(path, err)
						return nil
					}
				}
				// 检查文件在临时表是否存在
				existsFile, err := s.memSyncCache.GetByLocalPath(path)
				if err != nil {
					s.Sync.Logger.Warnf("查询同步缓存失败 %s：%v", path, err)
				}
				if existsFile == nil {
					s.Sync.Logger.Debugf("本地文件对比完成：local_path=%s，网盘文件存在：否", path)
				} else {
					s.Sync.Logger.Debugf("本地文件对比完成：local_path=%s，网盘文件存在：是，file_id=%s，remote_path=%s", path, existsFile.GetFileId(), existsFile.GetFullRemotePath())
					s.Sync.Logger.Debugf("网盘文件详细信息：%+v", *existsFile)
				}
				if existsFile == nil && s.missingCleanupReason != "" {
					return nil
				}
				if isVideo {
					// STRM 文件，检查文件在临时表是否存在，不存在需要删除临时文件
					if existsFile != nil {
						s.Sync.Logger.Infof("本地 STRM 已对应网盘文件，跳过处理：local_path=%s，file_id=%s，remote_path=%s", path, existsFile.GetFileId(), existsFile.GetFullRemotePath())
						return nil
					}
					s.Sync.Logger.Infof("本地 STRM 对应的网盘文件不存在，准备删除：local_path=%s", path)
					if err := s.RemoveFileAndCheckDirEmtry(path); err != nil {
						s.Sync.Logger.Warnf("删除本地 STRM 文件失败：local_path=%s，错误=%v", path, err)
						s.recordLocalFileFailure(path, err)
					}
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
						if err := s.RemoveFileAndCheckDirEmtry(path); err != nil {
							s.Sync.Logger.Warnf("删除本地元数据文件失败：local_path=%s，错误=%v", path, err)
							s.recordLocalFileFailure(path, err)
						}
						return nil
					}
					// 如果允许上传，则检查是否需要上传（文件在网盘不存在）
					if s.Config.NetNotFoundFileAction == models.SyncTreeItemMetaActionUpload && existsFile == nil {
						// 检查 dbupload 表中是否已经有对应的上传任务
						canUpload := models.CheckCanUploadByLocalPath(models.UploadSourceStrm, path)
						if !canUpload {
							s.Sync.Logger.Debugf("本地元数据文件 %s 由于存在上传任务所以不需要处理", path)
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
								if err := s.RemoveFileAndCheckDirEmtry(path); err != nil {
									s.Sync.Logger.Warnf("删除本地元数据文件失败：local_path=%s，错误=%v", path, err)
									s.recordLocalFileFailure(path, err)
								}
								return nil
							} else {
								// 递归创建目录，调用对应的 driver
								parentPathId, remotePath, err = s.SyncDriver.CreateDirRecursively(s.Context, parentDir)
								if err != nil {
									s.Sync.Logger.Errorf("创建目录 %s 失败：%v", parentDir, err)
									s.recordLocalFileFailure(path, err)
									if isFatalSyncError(err) {
										return err
									}
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
						if s.Account.SourceType == models.SourceTypeLocal {
							// 本地缓存的 Path 为空时，已有子目录只会以“/目录名”进入这里。
							// 上传任务仍须保存实际的本地目标父目录，不能丢失受限源根目录。
							remotePath = localUploadParentPath(sourceRootPath, remotePath)
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
						if err := models.AddUploadTaskFromSyncFile(db115File); err != nil {
							s.Sync.Logger.Warnf("添加本地元数据上传任务失败：local_path=%s，remote_path=%s，错误=%v", db115File.LocalFilePath, db115File.Path, err)
							s.recordLocalFileFailure(path, err)
							return fatalSyncError(err)
						}
						atomic.AddInt64(&s.NewUpload, 1)
						s.PublishProgress(false)
						return nil
					}
					// 网盘存在且设置为上传，需要检查本地是不是比网盘新，如果是的话，需要删除网盘文件并将本地文件上传
					if existsFile != nil && s.Config.CheckMetaMtime == 1 {
						if s.Config.EnableDownloadMeta == 1 && info.ModTime().Unix() != existsFile.MTime {
							active, err := s.hasActiveMetadataReplacement(path)
							if err != nil {
								return fatalSyncError(err)
							}
							if active {
								return nil
							}
						}
						switch s.decideMetadataMtimeAction(path, info, existsFile) {
						case metadataMtimeActionAlign:
							s.alignMetadataMtime(path, existsFile.MTime)
							return nil
						case metadataMtimeActionSkipChanged:
							return nil
						case metadataMtimeActionDownload:
							localMTime := info.ModTime().Unix()
							s.Sync.Logger.Infof("本地元数据文件 %s 由于修改时间比网盘旧 %d < %d 所以需要重新下载", path, localMTime, existsFile.MTime)
							baseline, err := helpers.MetadataFingerprint(path)
							if err != nil {
								s.recordLocalFileFailure(path, err)
								return nil
							}
							current, err := os.Stat(path)
							if err != nil || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
								s.recordLocalFileFailure(path, fmt.Errorf("元数据在扫描期间发生变化：%s", path))
								return nil
							}
							if err := s.addMetaDownloadTask(existsFile.GetSyncFile(s, s.Account.BaseUrl), baseline); err != nil {
								s.Sync.Logger.Warnf("添加元数据重新下载任务失败：%v", err)
								s.recordLocalFileFailure(path, err)
								return fatalSyncError(err)
							}
							return nil
						case metadataMtimeActionUpload:
							localMTime := info.ModTime().Unix()
							// 本地比网盘新，需要删除网盘旧文件并上传新文件
							s.Sync.Logger.Infof("本地元数据文件 %s 由于修改时间比网盘新 %d > %d 所以需要上传", path, localMTime, existsFile.MTime)
							// 1. 删除网盘旧文件
							err := s.SyncDriver.DeleteFile(s.Context, existsFile.ParentId, []string{existsFile.GetFileId()})
							if err != nil {
								s.Sync.Logger.Errorf("删除网盘旧文件 %s 失败：%v", existsFile.GetFileId(), err)
								s.recordLocalFileFailure(path, err)
								if isFatalSyncError(err) {
									return err
								}
								return nil
							}
							// 2. 添加上传任务
							if err := models.AddUploadTaskFromSyncFile(existsFile.GetSyncFile(s, s.Account.BaseUrl)); err != nil {
								s.Sync.Logger.Warnf("添加元数据上传任务失败：local_path=%s，file_id=%s，错误=%v", path, existsFile.GetFileId(), err)
								s.recordLocalFileFailure(path, err)
								return fatalSyncError(err)
							}
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
		if walkErr != nil {
			return walkErr
		}
	}
	return nil
}

// localUploadParentPath 将本地来源的相对或被缓存截短的目录补回配置的源根目录。
// 已是该根目录下的完整路径时原样保留，避免重复拼接。
func localUploadParentPath(sourceRoot string, remotePath string) string {
	sourceRoot = filepath.ToSlash(filepath.Clean(strings.TrimSpace(sourceRoot)))
	remotePath = filepath.ToSlash(filepath.Clean(strings.TrimSpace(remotePath)))
	if sourceRoot == "" || sourceRoot == "." || remotePath == "" || remotePath == "." {
		return remotePath
	}
	if remotePath == sourceRoot {
		return sourceRoot
	}
	rootPrefix := strings.TrimSuffix(sourceRoot, "/") + "/"
	if strings.HasPrefix(remotePath, rootPrefix) {
		return remotePath
	}
	return filepath.ToSlash(filepath.Join(sourceRoot, strings.TrimPrefix(remotePath, "/")))
}

func (s *SyncStrm) decideMetadataMtimeAction(path string, info os.FileInfo, remote *SyncFileCache) metadataMtimeAction {
	if info == nil || remote == nil {
		return metadataMtimeActionNone
	}

	localMtime := info.ModTime().Unix()
	if localMtime == remote.MTime {
		return metadataMtimeActionNone
	}

	contentMatches, localStable := s.metadataContentMatches(path, info, remote)
	if !localStable {
		return metadataMtimeActionSkipChanged
	}
	if contentMatches {
		return metadataMtimeActionAlign
	}
	if localMtime < remote.MTime {
		return metadataMtimeActionDownload
	}
	if localMtime > remote.MTime && s.Config.NetNotFoundFileAction == models.SyncTreeItemMetaActionUpload {
		return metadataMtimeActionUpload
	}
	return metadataMtimeActionNone
}

func (s *SyncStrm) metadataContentMatches(path string, info os.FileInfo, remote *SyncFileCache) (bool, bool) {
	if info.Size() != remote.FileSize || strings.TrimSpace(remote.Sha1) == "" {
		return false, true
	}

	initialSize := info.Size()
	initialMtime := info.ModTime().UnixNano()
	localSHA1, err := calculateMetadataFileSHA1(path)
	if err != nil {
		s.Sync.Logger.Warnf("计算本地元数据文件 SHA1 失败，将按修改时间处理：%s，错误：%v", path, err)
		return false, true
	}
	currentInfo, err := os.Stat(path)
	if err != nil {
		s.Sync.Logger.Warnf("复核本地元数据文件失败，将按修改时间处理：%s，错误：%v", path, err)
		return false, true
	}
	if currentInfo.Size() != initialSize || currentInfo.ModTime().UnixNano() != initialMtime {
		s.Sync.Logger.Warnf("本地元数据文件在 SHA1 计算期间发生变化，跳过本轮处理：%s", path)
		return false, false
	}
	return strings.EqualFold(localSHA1, remote.Sha1), true
}

func (s *SyncStrm) alignMetadataMtime(path string, remoteMtime int64) {
	if remoteMtime <= 0 {
		return
	}
	mtime := time.Unix(remoteMtime, 0)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		s.Sync.Logger.Warnf("对齐本地元数据文件修改时间失败：%s，错误：%v", path, err)
		return
	}
	s.Sync.Logger.Infof("本地元数据文件内容与网盘一致，已对齐修改时间：%s => %d", path, remoteMtime)
}
