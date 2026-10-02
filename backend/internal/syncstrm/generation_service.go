package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/directoryupload"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
)

var errOldStrmCleanupFailed = errors.New("旧 STRM 清理失败")

const (
	strmGenerationWorkerInterval = 5 * time.Second
	strmGenerationWorkerBatch    = 5
)

var cleanupSourceAfterStrmSuccess = directoryupload.CleanupSourceAfterStrmSuccess

var strmGenerationWorkerState = struct {
	mu       sync.RWMutex
	cancel   context.CancelFunc
	running  bool
	stopping bool
	wg       sync.WaitGroup
}{}

// StrmGenerationInput 是单文件 STRM 生成输入。
type StrmGenerationInput struct {
	Task *models.StrmGenerationTask
}

// StrmGenerationResult 描述单文件 STRM 生成结果。
type StrmGenerationResult struct {
	SkipReason     string
	SyncFile       *models.SyncFile
	Changed        bool
	NewMeta        int
	RefreshTargets []models.EmbyRefreshTarget
}

// StrmGenerationService 复用现有 SyncStrm 能力完成单文件 STRM 后处理。
type StrmGenerationService struct {
	buildSyncer                  func(*models.SyncPath, *models.Account, *SyncStrmConfig) (*SyncStrm, error)
	compareStrm                  func(*SyncStrm, *SyncFileCache) int
	processStrmFile              func(*SyncStrm, *SyncFileCache) error
	requestEmbyRefreshBySyncFile func(*models.SyncFile) error
	resolveRefreshTarget         func(*models.SyncFile) (models.EmbyRefreshTarget, error)
	requestEmbyRefreshTargets    func(uint, []models.EmbyRefreshTarget) error
	acquireRefreshSubmission     func(context.Context) (func(), error)
	detailByFileID               func(context.Context, *SyncStrm, string) (*SyncFileCache, error)
	resolveStrmOwner             func(context.Context, *SyncStrm, *SyncFileCache, *generationDirectory) (bool, error)
}

// NewStrmGenerationService 创建 STRM 生成服务。
func NewStrmGenerationService() *StrmGenerationService {
	service := &StrmGenerationService{}
	service.buildSyncer = func(syncPath *models.SyncPath, account *models.Account, config *SyncStrmConfig) (*SyncStrm, error) {
		var syncer *SyncStrm
		if config == nil {
			syncer = NewSyncStrmForStrmGeneration(syncPath, account)
		} else {
			syncer = newSyncStrmWithConfig(syncPath, account, cloneGenerationConfig(*config), false)
		}
		if syncer == nil {
			return nil, errors.New("初始化 STRM 同步器失败")
		}
		return syncer, nil
	}
	service.compareStrm = func(syncer *SyncStrm, file *SyncFileCache) int {
		return syncer.CompareStrm(file)
	}
	service.processStrmFile = func(syncer *SyncStrm, file *SyncFileCache) error {
		return syncer.writeStrmFile(file)
	}
	service.requestEmbyRefreshBySyncFile = models.RequestEmbyRefreshBySyncFile
	service.resolveRefreshTarget = models.ResolveEmbyRefreshTarget
	service.requestEmbyRefreshTargets = models.RequestEmbyRefreshTargets
	service.acquireRefreshSubmission = acquireStrmGenerationRefreshSubmission
	service.detailByFileID = func(ctx context.Context, syncer *SyncStrm, fileID string) (*SyncFileCache, error) {
		if syncer == nil || syncer.SyncDriver == nil {
			return nil, errors.New("STRM 同步器未初始化远端驱动")
		}
		return syncer.SyncDriver.DetailByFileId(ctx, fileID)
	}
	service.resolveStrmOwner = resolveLatest115StrmOwnerWithDirectory
	return service
}

// Generate 为单个远端文件生成或确认 STRM。
func (service *StrmGenerationService) Generate(ctx context.Context, input StrmGenerationInput) (*StrmGenerationResult, error) {
	return service.generate(ctx, input, nil, nil, nil)
}

func (service *StrmGenerationService) generate(ctx context.Context, input StrmGenerationInput, configs generationConfigs, blockers []*models.StrmGenerationTask, directories *generationDirectories) (*StrmGenerationResult, error) {
	if service == nil {
		service = NewStrmGenerationService()
	}
	task := input.Task
	if task == nil {
		return nil, errors.New("STRM 生成任务为空")
	}
	if task.SyncPathId == 0 {
		return nil, errors.New("STRM 生成任务缺少同步目录")
	}
	if task.TaskType != "" && task.TaskType != models.StrmGenerationTaskTypeFile {
		return nil, fmt.Errorf("暂不支持的 STRM 生成任务类型：%s", task.TaskType)
	}

	if configs == nil {
		var err error
		configs, err = prepareGenerationConfigs(ctx, []*models.StrmGenerationTask{task})
		if err != nil {
			return nil, err
		}
	}

	syncPath, syncer, file, release, err := service.prepareGenerationFile(ctx, task, configs)
	if err != nil {
		return nil, err
	}
	defer release()
	if file.SourceType == models.SourceType115 && helpers.IsV115PlaybackPath(file.GetFullRemotePath()) {
		return nil, fmt.Errorf("115 多端播放临时目录不参与 STRM 生成：%s", file.GetFullRemotePath())
	}
	if err := validateGeneratedFileScope(syncer, syncPath, file); err != nil {
		return nil, err
	}
	if err := checkGenerationDependencies(ctx, task, file, blockers); err != nil {
		return nil, err
	}
	if directories != nil {
		directories.observe(syncer, file)
	}
	if reason := syncer.generationSkipReason(file); reason != "" {
		return &StrmGenerationResult{SkipReason: reason}, nil
	}
	var unlockTarget func()
	if file.IsVideo {
		unlockTarget = lockStrmTarget(file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath))
		defer func() {
			if unlockTarget != nil {
				unlockTarget()
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory := &generationDirectory{batch: directories}
	isStrmOwner, err := service.resolveStrmOwner(ctx, syncer, file, directory)
	if err != nil {
		return nil, err
	}
	existing, err := findExistingGeneratedSyncFile(ctx, task.SyncPathId, file.GetFileId(), file.PickCode)
	if err != nil {
		return nil, err
	}

	syncFile := file.GetSyncFile(syncer, syncer.Account.BaseUrl)
	if existing != nil {
		syncFile.BaseModel = existing.BaseModel
	}
	oldLocalPath := ""
	if existing != nil {
		oldLocalPath = existing.LocalFilePath
	}
	newLocalPath := syncFile.LocalFilePath

	changed := file.IsVideo && isStrmOwner && service.compareStrm(syncer, file) != 1
	if changed {
		if err := service.processStrmFile(syncer, file); err != nil {
			return nil, fmt.Errorf("生成 STRM 文件失败：%w", err)
		}
	}
	if err := copyDirectoryUploadMetadata(syncer, task, file); err != nil {
		return nil, fmt.Errorf("复制目录监控元数据失败：%w", err)
	}

	if err := models.SaveSyncFilePosition(db.Db.WithContext(ctx), syncFile); err != nil {
		return nil, fmt.Errorf("保存 SyncFile 失败：%w", err)
	}
	if changed {
		if err := cleanupOldGeneratedStrm(syncer.TargetPath, oldLocalPath, newLocalPath); err != nil {
			helpers.AppLogger.Warnf("清理旧 STRM 文件失败：%v", err)
		}
	}
	// STRM、账本和旧路径已处理完；后续工作仍由外层范围保护。
	if unlockTarget != nil {
		unlockTarget()
		unlockTarget = nil
	}
	isWebhookFile := task.Source == models.StrmGenerationSourceWebhook && task.TaskType == models.StrmGenerationTaskTypeFile
	newMeta := 0
	if isWebhookFile && task.DownloadMeta && file.IsVideo {
		newMeta, err = service.downloadMatchedMetadata(ctx, syncer, syncPath, file, directory)
		if err != nil {
			return nil, err
		}
	}
	result := &StrmGenerationResult{SyncFile: syncFile, Changed: changed, NewMeta: newMeta}
	shouldSubmitWebhookRefresh := isWebhookFile && task.RefreshEmby && (changed || newMeta > 0)
	switch {
	case shouldSubmitWebhookRefresh:
		target, err := service.resolveRefreshTarget(syncFile)
		if err != nil {
			return nil, err
		}
		result.RefreshTargets = []models.EmbyRefreshTarget{target}
		if task.ParentTaskId == 0 {
			if err := service.submitEmbyRefreshTargets(ctx, syncFile.SyncPathId, result.RefreshTargets); err != nil {
				return nil, fmt.Errorf("提交 Emby 刷新任务失败：%w", err)
			}
		}
	case changed && !isWebhookFile:
		if err := service.submitEmbyRefreshBySyncFile(ctx, syncFile); err != nil {
			return nil, fmt.Errorf("提交 Emby 刷新任务失败：%w", err)
		}
	}
	return result, nil
}

// prepareGenerationFile 先等待相关文件保存完毕，再核对当前目录、账号和远端详情。
func (service *StrmGenerationService) prepareGenerationFile(ctx context.Context, task *models.StrmGenerationTask, configs generationConfigs) (*models.SyncPath, *SyncStrm, *SyncFileCache, func(), error) {
	fileID, pickCode := task.FileId, task.PickCode
	for {
		syncPath, release, err := models.AcquireSyncFileScope(ctx, task.SyncPathId, fileID, pickCode)
		if err != nil {
			return nil, nil, nil, nil, generationScopeError(ctx, task.SyncPathId, err)
		}
		account, err := loadStrmGenerationAccount(ctx, syncPath, task.AccountId)
		if err != nil {
			release()
			return nil, nil, nil, nil, err
		}
		syncer, err := service.buildGenerationSyncer(syncPath, account, configs)
		if err != nil || syncer == nil {
			release()
			if err == nil {
				err = errors.New("STRM 同步器为空")
			}
			return nil, nil, nil, nil, err
		}
		closeSyncer := func() {
			if syncer.Cancel != nil {
				syncer.Cancel()
			}
			release()
		}
		file, err := service.buildFileCache(ctx, syncer, syncPath, account, task)
		if err != nil {
			closeSyncer()
			return nil, nil, nil, nil, err
		}
		if file.GetFileId() == fileID && file.PickCode == pickCode {
			return syncPath, syncer, file, closeSyncer, nil
		}
		covered, err := models.SyncFileScopeCovered(ctx, syncPath, file.GetFileId(), file.PickCode)
		if err != nil {
			closeSyncer()
			return nil, nil, nil, nil, err
		}
		if covered {
			// 身份补全没有扩大范围，原许可仍保护当前详情，不必重新请求。
			return syncPath, syncer, file, closeSyncer, nil
		}
		// 新身份带来未保护的旧位置，重新申请后刷新详情，避免使用等待前的信息。
		fileID, pickCode = file.GetFileId(), file.PickCode
		closeSyncer()
	}
}

func (service *StrmGenerationService) downloadMatchedMetadata(ctx context.Context, syncer *SyncStrm, syncPath *models.SyncPath, video *SyncFileCache, directory *generationDirectory) (int, error) {
	if syncer == nil || syncer.SyncDriver == nil || video == nil || !video.IsVideo {
		return 0, nil
	}
	if video.ParentId == "" || video.Path == "" {
		return 0, nil
	}
	files, err := directory.list(ctx, syncer, video)
	if err != nil {
		return 0, fmt.Errorf("获取同目录元数据列表失败：%w", err)
	}

	wanted := matchedMetadataNames(video.FileName, syncer.Config.MetaExt)
	created := 0
	for _, item := range files {
		if item == nil || item.FileType == v115open.TypeDir {
			continue
		}
		if !wanted[item.FileName] || !syncer.IsValidMetaExt(item.FileName) {
			continue
		}
		if item.SourceType == "" {
			item.SourceType = video.SourceType
		}
		if item.ParentId == "" {
			item.ParentId = video.ParentId
		}
		if item.Path == "" {
			item.Path = video.Path
		}
		item.IsVideo = false
		item.IsMeta = true
		if err := validateGeneratedFileScope(syncer, syncPath, item); err != nil {
			return created, err
		}
		if syncer.generationSkipReason(item) != "" {
			continue
		}

		baseURL := ""
		if syncer.Account != nil {
			baseURL = syncer.Account.BaseUrl
		}
		syncFile := item.GetSyncFile(syncer, baseURL)
		if helpers.PathExists(syncFile.LocalFilePath) {
			continue
		}
		if err := models.SaveSyncFilePosition(db.Db.WithContext(ctx), syncFile); err != nil {
			return created, fmt.Errorf("保存元数据 SyncFile 失败：%w", err)
		}
		if err := models.AddDownloadTaskFromSyncFile(syncFile); err != nil {
			if errors.Is(err, models.ErrActiveDownloadTaskExists) {
				continue
			}
			return created, err
		}
		created++
	}
	return created, nil
}

func matchedMetadataNames(videoName string, metaExt []string) map[string]bool {
	base := strings.TrimSuffix(videoName, filepath.Ext(videoName))
	names := make(map[string]bool, len(metaExt)*2)
	for _, ext := range metaExt {
		ext = strings.TrimSpace(ext)
		if ext == "" {
			continue
		}
		names[base+ext] = true
		names[base+"-thumb"+ext] = true
	}
	return names
}

func copyDirectoryUploadMetadata(syncer *SyncStrm, task *models.StrmGenerationTask, file *SyncFileCache) error {
	if syncer == nil || task == nil || file == nil {
		return nil
	}
	if !file.IsMeta || file.IsVideo || task.UploadTaskId == 0 {
		return nil
	}

	var uploadTask models.DbUploadTask
	if err := db.Db.First(&uploadTask, task.UploadTaskId).Error; err != nil {
		return fmt.Errorf("读取上传任务失败：%w", err)
	}
	if uploadTask.Source != models.UploadSourceDirectoryMonitor {
		return nil
	}

	sourcePath := strings.TrimSpace(uploadTask.LocalFullPath)
	if sourcePath == "" {
		return errors.New("目录监控元数据缺少本地源文件路径")
	}
	sourcePath, err := filepath.Abs(sourcePath)
	if err != nil {
		return err
	}
	resolvedSource, err := filepath.EvalSymlinks(sourcePath)
	if err != nil {
		return fmt.Errorf("解析目录监控元数据源文件失败：%w", err)
	}
	// 只有链接源需要重新读取监控规则；普通文件保留历史任务的处理方式。
	validateLinkedSource := func() error {
		if sourcePath == resolvedSource {
			return nil
		}
		current, err := directoryupload.ResolveMetadataSource(&uploadTask)
		if err != nil {
			return err
		}
		current, err = filepath.Abs(current)
		if err != nil {
			return err
		}
		if current != resolvedSource {
			return fmt.Errorf("目录监控元数据源文件指向已变化：%s", sourcePath)
		}
		return nil
	}
	if err := validateLinkedSource(); err != nil {
		return err
	}
	targetPath := file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath)
	if targetPath == "" {
		return errors.New("目录监控元数据缺少 STRM 本地路径")
	}
	if filepath.Clean(sourcePath) == filepath.Clean(targetPath) {
		return fmt.Errorf("目录监控元数据源文件和 STRM 目标文件相同：%s", sourcePath)
	}

	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("读取目录监控元数据源文件信息失败：%w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("目录监控元数据源路径是目录：%s", sourcePath)
	}
	if err := validateDirectoryUploadMetadataSource(&uploadTask, info); err != nil {
		return err
	}

	baseline, err := helpers.MetadataFingerprint(targetPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	mtime := uploadTask.LocalMtime
	if mtime == 0 {
		mtime = info.ModTime().Unix()
	}
	if err := helpers.CopyMetadataFile(sourcePath, targetPath, baseline, info.Size(), mtime, func(string) error {
		currentPath, err := filepath.EvalSymlinks(sourcePath)
		if err != nil {
			return err
		}
		if currentPath != resolvedSource {
			return fmt.Errorf("目录监控元数据源文件指向已变化：%s", sourcePath)
		}
		if err := validateLinkedSource(); err != nil {
			return err
		}
		current, err := os.Stat(sourcePath)
		if err != nil {
			return err
		}
		if !os.SameFile(info, current) || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			return fmt.Errorf("目录监控元数据源文件已变化：%s", sourcePath)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("复制目录监控元数据失败：%w", err)
	}
	return nil
}

func validateDirectoryUploadMetadataSource(task *models.DbUploadTask, info os.FileInfo) error {
	if task == nil || info == nil {
		return errors.New("目录监控元数据源文件校验参数为空")
	}
	if task.SourceFingerprint != "" && models.BuildDirectoryUploadSourceFingerprint(info.Size(), info.ModTime().UnixNano()) != task.SourceFingerprint {
		return errors.New("目录监控元数据源文件与上传时不一致")
	}
	if task.FileSize > 0 && info.Size() != task.FileSize {
		return fmt.Errorf("目录监控元数据源文件已变化，大小不匹配 task=%d current=%d", task.FileSize, info.Size())
	}
	if task.LocalMtime > 0 {
		currentMtime := info.ModTime().Unix()
		if currentMtime != task.LocalMtime {
			return fmt.Errorf("目录监控元数据源文件已变化，修改时间不匹配 task=%d current=%d", task.LocalMtime, currentMtime)
		}
	}
	return nil
}

func (service *StrmGenerationService) buildFileCache(ctx context.Context, syncer *SyncStrm, syncPath *models.SyncPath, account *models.Account, task *models.StrmGenerationTask) (*SyncFileCache, error) {
	file := &SyncFileCache{
		FileId:     task.FileId,
		ParentId:   task.ParentId,
		FileType:   v115open.TypeFile,
		FileName:   task.FileName,
		Path:       task.Path,
		FileSize:   task.FileSize,
		MTime:      task.Mtime,
		PickCode:   task.PickCode,
		Sha1:       task.Sha1,
		SourceType: syncPath.SourceType,
	}
	if file.SourceType == "" && account != nil {
		file.SourceType = account.SourceType
	}
	if fileNeedsRemoteDetail(file) {
		locator, err := remoteDetailLocator(file)
		if err != nil {
			return nil, err
		}
		if locator != "" {
			detail, err := service.detailByFileID(ctx, syncer, locator)
			if err != nil {
				return nil, fmt.Errorf("补齐远端文件详情失败：%w", err)
			}
			mergeFileCache(file, detail)
		}
	}
	if file.Path == "" {
		file.Path = syncPath.RemotePath
	}
	if file.SourceType == "" {
		file.SourceType = models.SourceType115
	}
	file.IsVideo = syncer.IsValidVideoExt(file.FileName)
	file.IsMeta = syncer.IsValidMetaExt(file.FileName)
	if file.FileName == "" {
		return nil, errors.New("STRM 生成任务缺少文件名")
	}
	if file.SourceType == models.SourceTypeBaiduPan && file.Path != "" {
		// 和百度普通扫描保持一致：SyncFile.file_id 存完整路径，PickCode 存 fs_id。
		file.FileId = pathpkg.Join(file.Path, file.FileName)
	}
	if file.GetFileId() == "" && file.PickCode == "" {
		return nil, errors.New("STRM 生成任务缺少远端文件标识")
	}
	return file, nil
}

// ExpandDirectoryScan 将目录扫描父任务展开为待处理的单文件 STRM 任务。
func (service *StrmGenerationService) ExpandDirectoryScan(ctx context.Context, task *models.StrmGenerationTask) (int, error) {
	return service.expandDirectoryScan(ctx, task, nil, nil)
}

func (service *StrmGenerationService) expandDirectoryScan(ctx context.Context, task *models.StrmGenerationTask, configs generationConfigs, blockers []*models.StrmGenerationTask) (int, error) {
	if service == nil {
		service = NewStrmGenerationService()
	}
	if task == nil {
		return 0, errors.New("STRM 生成任务为空")
	}
	if task.SyncPathId == 0 {
		return 0, errors.New("STRM 生成任务缺少同步目录")
	}
	if task.TaskType != models.StrmGenerationTaskTypeDirectoryScan {
		return 0, fmt.Errorf("非目录扫描 STRM 任务：%s", task.TaskType)
	}

	if configs == nil {
		var err error
		configs, err = prepareGenerationConfigs(ctx, []*models.StrmGenerationTask{task})
		if err != nil {
			return 0, err
		}
	}

	syncPath, release, err := models.AcquireSyncFileScope(ctx, task.SyncPathId, "", "")
	if err != nil {
		return 0, generationScopeError(ctx, task.SyncPathId, err)
	}
	// 展开结束即释放，不能占着范围等待子任务，否则子任务无法开始。
	defer release()
	if err := checkGenerationDependencies(ctx, task, nil, blockers); err != nil {
		return 0, err
	}
	account, err := loadStrmGenerationAccount(ctx, syncPath, task.AccountId)
	if err != nil {
		return 0, err
	}
	syncer, err := service.buildGenerationSyncer(syncPath, account, configs)
	if err != nil {
		return 0, err
	}
	if syncer == nil {
		return 0, errors.New("STRM 同步器为空")
	}
	if syncer.SyncDriver == nil {
		return 0, errors.New("STRM 同步器未初始化远端驱动")
	}
	if syncer.Cancel != nil {
		defer syncer.Cancel()
	}

	directoryPath, directoryID, err := resolveDirectoryScanRoot(ctx, syncer, syncPath, task)
	if err != nil {
		return 0, err
	}
	if syncer.IsExcludePath(directoryPath) {
		task.SkipReason = "目录名称被排除"
		return 0, nil
	}
	totalItems, err := service.expandDirectoryScanChildren(ctx, task, syncer, syncPath, directoryPath, directoryID)
	if err != nil {
		return 0, err
	}
	task.TotalItems = totalItems
	task.AcceptedItems = 0
	task.FailedItems = 0
	return totalItems, nil
}

func resolveDirectoryScanRoot(ctx context.Context, syncer *SyncStrm, syncPath *models.SyncPath, task *models.StrmGenerationTask) (string, string, error) {
	directoryID := strings.TrimSpace(task.DirectoryId)
	directoryPath := normalizeStrmRemotePath(task.DirectoryPath)
	if directoryPath == "" && directoryID != "" && directoryID == syncPath.BaseCid {
		directoryPath = normalizeStrmRemotePath(syncPath.RemotePath)
	}
	if directoryPath == "" && directoryID != "" {
		detail, err := syncer.SyncDriver.DetailByFileId(ctx, directoryID)
		if err != nil {
			return "", "", fmt.Errorf("补齐远端目录详情失败：%w", err)
		}
		if detail == nil {
			return "", "", errors.New("补齐远端目录详情失败：目录不存在")
		}
		if detail.FileType != v115open.TypeDir {
			return "", "", fmt.Errorf("远端文件 %s 不是目录", directoryID)
		}
		if detail.SourceType == "" {
			detail.SourceType = syncPath.SourceType
		}
		if detail.SourceType != syncPath.SourceType {
			return "", "", errors.New("远端目录来源与同步目录不一致")
		}
		if detail.FileId != "" {
			directoryID = detail.FileId
		}
		directoryPath = normalizeStrmRemotePath(detail.GetFullRemotePath())
	}
	if directoryPath == "" {
		return "", "", errors.New("目录扫描任务缺少远端目录路径")
	}
	if !strmRemotePathWithin(directoryPath, syncPath.RemotePath) {
		return "", "", fmt.Errorf("远端目录 %s 不在同步远端目录 %s 下", directoryPath, normalizeStrmRemotePath(syncPath.RemotePath))
	}
	if directoryID == "" {
		var err error
		directoryID, err = syncer.SyncDriver.GetPathIdByPath(ctx, directoryPath)
		if err != nil {
			return "", "", fmt.Errorf("获取远端目录 ID 失败：%w", err)
		}
		directoryID = strings.TrimSpace(directoryID)
	}
	if directoryID == "" {
		return "", "", fmt.Errorf("远端目录不存在：%s", directoryPath)
	}
	return directoryPath, directoryID, nil
}

func (service *StrmGenerationService) expandDirectoryScanChildren(ctx context.Context, task *models.StrmGenerationTask, syncer *SyncStrm, syncPath *models.SyncPath, directoryPath string, directoryID string) (int, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	if syncer.Account != nil && syncer.Account.SourceType == models.SourceType115 && helpers.IsV115PlaybackPath(directoryPath) {
		return 0, nil
	}
	fileItems, err := syncer.SyncDriver.GetNetFileFiles(ctx, directoryPath, directoryID)
	if err != nil {
		return 0, fmt.Errorf("获取远端目录文件列表失败：%w", err)
	}
	totalItems := 0
	videoFiles := make([]*SyncFileCache, 0, len(fileItems))
	for _, file := range fileItems {
		if file == nil {
			continue
		}
		if file.SourceType == "" {
			file.SourceType = syncPath.SourceType
		}
		if file.ParentId == "" {
			file.ParentId = directoryID
		}
		if file.Path == "" {
			file.Path = directoryPath
		}
		if file.SourceType == models.SourceType115 && helpers.IsV115PlaybackPath(file.GetFullRemotePath()) {
			continue
		}
		file.IsVideo = file.FileType != v115open.TypeDir && syncer.IsValidVideoExt(file.FileName)
		if err := validateGeneratedFileScope(syncer, syncPath, file); err != nil {
			return totalItems, err
		}
		if file.FileType == v115open.TypeDir {
			childPath := normalizeStrmRemotePath(file.GetFullRemotePath())
			childID := file.GetFileId()
			if childID == "" {
				return totalItems, fmt.Errorf("远端子目录缺少目录 ID：%s", childPath)
			}
			childTotal, err := service.expandDirectoryScanChildren(ctx, task, syncer, syncPath, childPath, childID)
			totalItems += childTotal
			if err != nil {
				return totalItems, err
			}
			continue
		}
		if !syncer.IsValidVideoExt(file.FileName) {
			continue
		}
		file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath)
		videoFiles = append(videoFiles, file)
	}

	eligible := make([]*SyncFileCache, 0, len(videoFiles))
	for _, file := range videoFiles {
		if syncer.generationSkipReason(file) == "" {
			eligible = append(eligible, file)
		}
	}
	owners := selectLatest115StrmOwners(eligible)
	for _, file := range videoFiles {
		if file.SourceType == models.SourceType115 {
			targetPath := filepath.ToSlash(filepath.Clean(file.LocalFilePath))
			if syncer.generationSkipReason(file) == "" && owners[targetPath] != file {
				continue
			}
		}
		childTask := buildDirectoryScanChildTask(task, syncPath, file)
		if _, err := models.EnqueueStrmGenerationTaskWithLegacyHashes(
			childTask,
			legacyDirectoryScanChildRequestHash(task, syncPath, file),
		); err != nil {
			return totalItems, fmt.Errorf("创建目录扫描子任务失败：%w", err)
		}
		totalItems++
	}
	return totalItems, nil
}

func buildDirectoryScanChildTask(parent *models.StrmGenerationTask, syncPath *models.SyncPath, file *SyncFileCache) *models.StrmGenerationTask {
	source := parent.Source
	if source == "" {
		source = models.StrmGenerationSourceWebhook
	}
	accountID := parent.AccountId
	if accountID == 0 {
		accountID = syncPath.AccountId
	}
	return &models.StrmGenerationTask{
		Source:       source,
		TaskType:     models.StrmGenerationTaskTypeFile,
		ParentTaskId: parent.ID,
		SyncPathId:   parent.SyncPathId,
		AccountId:    accountID,
		DownloadMeta: parent.DownloadMeta,
		RefreshEmby:  parent.RefreshEmby,
		FileId:       file.GetFileId(),
		ParentId:     file.ParentId,
		PickCode:     file.PickCode,
		Path:         file.GetPath(),
		FileName:     file.FileName,
		FileSize:     file.FileSize,
		Sha1:         file.Sha1,
		Mtime:        file.MTime,
		RequestHash:  directoryScanChildRequestHash(parent, syncPath, file),
	}
}

func directoryScanChildRequestHash(parent *models.StrmGenerationTask, syncPath *models.SyncPath, file *SyncFileCache) string {
	return models.BuildStrmRequestHash(
		"directory_scan:file",
		fmt.Sprint(syncPath.ID),
		fmt.Sprint(parent.ID),
		file.GetFileId(),
		file.PickCode,
		file.GetPath(),
		file.FileName,
	)
}

func legacyDirectoryScanChildRequestHash(parent *models.StrmGenerationTask, syncPath *models.SyncPath, file *SyncFileCache) string {
	return fmt.Sprintf(
		"directory_scan:file:%d:%d:%s:%s:%s:%s",
		syncPath.ID,
		parent.ID,
		file.GetFileId(),
		file.PickCode,
		file.GetPath(),
		file.FileName,
	)
}

func normalizeStrmRemotePath(value string) string {
	// 首尾空格可能属于真实目录名，不能在范围校验或目录身份比较时移除。
	value = strings.ReplaceAll(value, "\\", "/")
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") {
		value = "/" + value
	}
	return pathpkg.Clean(value)
}

func strmRemotePathWithin(remotePath string, basePath string) bool {
	remotePath = normalizeStrmRemotePath(remotePath)
	basePath = normalizeStrmRemotePath(basePath)
	if remotePath == "" || basePath == "" {
		return false
	}
	if basePath == "/" {
		return strings.HasPrefix(remotePath, "/")
	}
	return remotePath == basePath || strings.HasPrefix(remotePath, basePath+"/")
}

func fileNeedsRemoteDetail(file *SyncFileCache) bool {
	if file == nil {
		return false
	}
	if file.FileName == "" || file.Path == "" || file.ParentId == "" || file.PickCode == "" {
		return true
	}
	if file.MTime <= 0 || file.FileSize <= 0 {
		return true
	}
	return file.SourceType == models.SourceType115 && file.Sha1 == ""
}

// remoteDetailLocator 返回驱动查询详情时所需的定位值。
// 百度网盘和 OpenList 的当前详情实现按完整路径查询；115 保持按文件 ID 查询。
func remoteDetailLocator(file *SyncFileCache) (string, error) {
	if file == nil {
		return "", errors.New("STRM 生成任务缺少远端文件信息")
	}
	if !usesPathRemoteDetailLocator(file.SourceType) {
		return strings.TrimSpace(file.FileId), nil
	}

	parentPath := strings.TrimSpace(file.Path)
	fileName := strings.TrimSpace(file.FileName)
	if parentPath == "" || fileName == "" {
		return "", errors.New("路径型来源的 STRM 生成任务缺少远端完整路径")
	}
	return pathpkg.Join(parentPath, fileName), nil
}

func usesPathRemoteDetailLocator(sourceType models.SourceType) bool {
	return sourceType == models.SourceTypeBaiduPan || sourceType == models.SourceTypeOpenList
}

func mergeFileCache(target *SyncFileCache, detail *SyncFileCache) {
	if target == nil || detail == nil {
		return
	}
	if detail.FileId != "" {
		target.FileId = detail.FileId
	}
	if detail.ParentId != "" {
		target.ParentId = detail.ParentId
	}
	if detail.FileType != "" {
		target.FileType = detail.FileType
	}
	if detail.FileName != "" {
		target.FileName = detail.FileName
	}
	if detail.Path != "" {
		target.Path = detail.Path
	}
	if detail.FileSize > 0 {
		target.FileSize = detail.FileSize
	}
	if detail.MTime > 0 {
		target.MTime = detail.MTime
	}
	if detail.PickCode != "" {
		target.PickCode = detail.PickCode
	}
	if detail.Sha1 != "" {
		target.Sha1 = detail.Sha1
	}
	if detail.ThumbUrl != "" {
		target.ThumbUrl = detail.ThumbUrl
	}
	if detail.OpenlistSign != "" {
		target.OpenlistSign = detail.OpenlistSign
	}
	if detail.OpenlistObjectId != "" {
		target.OpenlistObjectId = detail.OpenlistObjectId
	}
	if detail.OpenlistSHA1 != "" {
		target.OpenlistSHA1 = detail.OpenlistSHA1
	}
	if detail.OpenlistMD5 != "" {
		target.OpenlistMD5 = detail.OpenlistMD5
	}
	if detail.SourceType != "" {
		target.SourceType = detail.SourceType
	}
	if len(detail.Paths) > 0 {
		target.Paths = detail.Paths
	}
}

func validateGeneratedFileScope(syncer *SyncStrm, syncPath *models.SyncPath, file *SyncFileCache) error {
	if file.SourceType != syncPath.SourceType {
		return errors.New("远端文件来源与同步目录不一致")
	}
	basePath := syncPath.RemotePath
	if basePath == "" {
		basePath = "/"
	}
	if !strmRemotePathWithin(file.GetFullRemotePath(), basePath) {
		return fmt.Errorf("远端文件 %s 不在同步远端目录 %s 下", file.GetFullRemotePath(), basePath)
	}
	if syncPath.ScopeLocalRoot != "" {
		return ensureGeneratedStrmPathWithinResolvedRoot(syncPath.ScopeLocalRoot, file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath))
	}
	return ensureGeneratedStrmPathWithinRoot(syncPath.GetFullLocalPath(), file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath))
}

func loadStrmGenerationAccount(ctx context.Context, syncPath *models.SyncPath, taskAccountID uint) (*models.Account, error) {
	if taskAccountID != 0 && taskAccountID != syncPath.AccountId {
		return nil, errors.New("STRM 任务账号与当前同步目录不一致，请重新创建任务")
	}
	accountID := taskAccountID
	if accountID == 0 && syncPath != nil {
		accountID = syncPath.AccountId
	}
	if accountID == 0 {
		if syncPath.SourceType != models.SourceTypeLocal {
			return nil, errors.New("STRM 同步目录缺少网盘账号")
		}
		return &models.Account{SourceType: models.SourceTypeLocal}, nil
	}
	account := &models.Account{}
	if err := db.Db.WithContext(ctx).First(account, accountID).Error; err != nil {
		return nil, fmt.Errorf("获取网盘账号失败：%w", err)
	}
	if account.SourceType != syncPath.SourceType {
		return nil, errors.New("网盘账号来源与同步目录不一致")
	}
	return account, nil
}

func findExistingGeneratedSyncFile(ctx context.Context, syncPathID uint, fileID string, pickCode string) (*models.SyncFile, error) {
	if fileID == "" && pickCode == "" {
		return nil, nil
	}
	var existing models.SyncFile
	query := db.Db.WithContext(ctx).Where("sync_path_id = ?", syncPathID)
	switch {
	case fileID != "" && pickCode != "":
		query = query.Where("(file_id = ? OR pick_code = ?)", fileID, pickCode)
	case fileID != "":
		query = query.Where("file_id = ?", fileID)
	default:
		query = query.Where("pick_code = ?", pickCode)
	}
	err := query.Order("id ASC").First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &existing, nil
}

func cleanupOldGeneratedStrm(targetRoot string, oldLocalPath string, newLocalPath string) error {
	if oldLocalPath == "" || newLocalPath == "" {
		return nil
	}
	targetRoot = filepath.Clean(targetRoot)
	oldLocalPath = filepath.Clean(oldLocalPath)
	newLocalPath = filepath.Clean(newLocalPath)
	if oldLocalPath == newLocalPath {
		return nil
	}
	if !strings.EqualFold(filepath.Ext(oldLocalPath), ".strm") {
		return fmt.Errorf("%w: 旧路径不是 .strm 文件：%s", errOldStrmCleanupFailed, oldLocalPath)
	}
	if err := ensureGeneratedStrmPathWithinRoot(targetRoot, oldLocalPath); err != nil {
		return fmt.Errorf("%w: %v", errOldStrmCleanupFailed, err)
	}
	if err := os.Remove(oldLocalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %v", errOldStrmCleanupFailed, err)
	}
	return nil
}

func ensureGeneratedStrmPathWithinRoot(rootPath string, targetPath string) error {
	if rootPath == "" || targetPath == "" {
		return errors.New("STRM 目标根路径或文件路径为空")
	}
	rootPath, err := syncscope.ResolveLocalPath(rootPath)
	if err != nil {
		return fmt.Errorf("无法确定 STRM 根目录：%w", err)
	}
	return ensureGeneratedStrmPathWithinResolvedRoot(rootPath, targetPath)
}

func ensureGeneratedStrmPathWithinResolvedRoot(rootPath, targetPath string) error {
	if rootPath == "" || targetPath == "" {
		return errors.New("STRM 目标根路径或文件路径为空")
	}
	targetPath, err := syncscope.ResolveLocalPath(targetPath)
	if err != nil {
		return fmt.Errorf("无法确定 STRM 文件路径：%w", err)
	}
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return fmt.Errorf("检查 STRM 文件路径失败：%w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("文件路径超出 STRM 目录：%s", targetPath)
	}
	return nil
}

// InitStrmGenerationWorker 初始化 STRM 生成队列 worker。
func InitStrmGenerationWorker() {
	if err := models.ResetRunningStrmGenerationTasks(); err != nil {
		helpers.AppLogger.Errorf("恢复 STRM 生成任务失败：%v", err)
	}
	startStrmGenerationWorker(context.Background(), NewStrmGenerationService())
}

func startStrmGenerationWorker(ctx context.Context, service *StrmGenerationService) {
	workerCtx, cancel := context.WithCancel(ctx)
	strmGenerationWorkerState.mu.Lock()
	if strmGenerationWorkerState.running {
		strmGenerationWorkerState.mu.Unlock()
		cancel()
		return
	}
	strmGenerationWorkerState.cancel = cancel
	strmGenerationWorkerState.running = true
	strmGenerationWorkerState.stopping = false
	strmGenerationWorkerState.wg.Add(1)
	strmGenerationWorkerState.mu.Unlock()

	go func() {
		defer strmGenerationWorkerState.wg.Done()
		defer func() {
			strmGenerationWorkerState.mu.Lock()
			strmGenerationWorkerState.cancel = nil
			strmGenerationWorkerState.running = false
			strmGenerationWorkerState.mu.Unlock()
		}()
		runStrmGenerationWorker(workerCtx, service)
	}()
}

// StopStrmGenerationWorker 停止 STRM 生成队列 worker 并等待退出。
func StopStrmGenerationWorker() {
	strmGenerationWorkerState.mu.Lock()
	strmGenerationWorkerState.stopping = true
	cancel := strmGenerationWorkerState.cancel
	strmGenerationWorkerState.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	strmGenerationWorkerState.wg.Wait()
}

// acquireStrmGenerationRefreshSubmission 在停止开始前获取一次刷新提交许可。
func acquireStrmGenerationRefreshSubmission(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	strmGenerationWorkerState.mu.Lock()
	if strmGenerationWorkerState.stopping {
		strmGenerationWorkerState.mu.Unlock()
		return nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		strmGenerationWorkerState.mu.Unlock()
		return nil, err
	}
	strmGenerationWorkerState.mu.Unlock()
	return func() {}, nil
}

func runStrmGenerationWorker(ctx context.Context, service *StrmGenerationService) {
	ticker := time.NewTicker(strmGenerationWorkerInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		processed, err := ProcessPendingStrmGenerationTasks(ctx, service, strmGenerationWorkerBatch)
		if err != nil && !errors.Is(err, context.Canceled) {
			helpers.AppLogger.Errorf("处理 STRM 生成队列失败：%v", err)
		}
		// 满批且没有待重试错误时，立即处理下一批。
		if err == nil && processed == strmGenerationWorkerBatch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessPendingStrmGenerationTasks 处理一批待生成 STRM 任务。
func ProcessPendingStrmGenerationTasks(ctx context.Context, service *StrmGenerationService, limit int) (int, error) {
	if service == nil {
		service = NewStrmGenerationService()
	}
	var blockers, tasks []*models.StrmGenerationTask
	for {
		var err error
		blockers, err = models.GetStrmGenerationFinalizingRetries()
		if err != nil {
			return 0, err
		}
		tasks, err = selectGenerationTasks(ctx, limit, blockers)
		if err == nil {
			break
		}
		missing, ok := errors.AsType[*generationSyncPathMissingError](err)
		if !ok {
			return 0, err
		}
		if _, retireErr := models.RetireStrmGenerationTasksForMissingSyncPath(ctx, missing.syncPathID); retireErr != nil {
			return 0, retireErr
		}
		// 筛选途中删除目录时重新退休、从第一页开始；不能在变化的集合上继续 offset。
	}
	configs, err := prepareGenerationConfigs(ctx, tasks)
	if err != nil {
		return 0, err
	}
	directories := &generationDirectories{now: time.Now}
	finish := func(task *models.StrmGenerationTask) (bool, error) {
		err := completeStrmGenerationTaskAfterSideEffects(ctx, service, task)
		if missing, ok := errors.AsType[*generationSyncPathMissingError](err); ok {
			_, retireErr := models.RetireStrmGenerationTasksForMissingSyncPath(ctx, missing.syncPathID)
			return true, retireErr
		}
		return err != nil, err
	}
	processed := 0
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			return processed, ctx.Err()
		default:
		}
		if task.Status == models.StrmGenerationStatusFinalizing {
			processed++
			if stop, err := finish(task); stop {
				return processed, err
			}
			continue
		}
		if err := task.MarkRunning(); err != nil {
			return processed, err
		}
		processed++
		result, err := processStrmGenerationTask(ctx, service, task, configs, blockers, directories)
		if err != nil {
			if missing, ok := errors.AsType[*generationSyncPathMissingError](err); ok {
				if _, retireErr := models.RetireStrmGenerationTasksForMissingSyncPath(ctx, missing.syncPathID); retireErr != nil {
					return processed, retireErr
				}
				if task.SyncPathId != missing.syncPathID {
					if saveErr := task.ReturnPending(); saveErr != nil {
						return processed, saveErr
					}
					processed--
				}
				// 同批任务和阻塞项可能同属已退休目录，下一轮重新读取。
				return processed, nil
			}
			if errors.Is(err, errGenerationDependencyWait) || errors.Is(err, errGenerationDependencyCheck) {
				if saveErr := task.ReturnPending(); saveErr != nil {
					return processed, errors.Join(err, saveErr)
				}
				processed--
				if errors.Is(err, errGenerationDependencyCheck) {
					return processed, err
				}
				continue
			}
			if ctx.Err() != nil {
				return processed, ctx.Err()
			}
			if errors.Is(err, context.Canceled) {
				return processed, err
			}
			if task.ParentTaskId > 0 {
				parent, progressErr := models.MarkStrmGenerationChildFailed(task.ID, task.ParentTaskId, err.Error())
				if progressErr != nil {
					return processed, progressErr
				}
				if submitErr := submitStrmGenerationParentRefreshIfReady(ctx, service, parent); submitErr != nil {
					return processed, submitErr
				}
			} else if markErr := task.MarkFailed(err.Error()); markErr != nil {
				return processed, markErr
			}
			continue
		}
		if result != nil && result.SkipReason != "" {
			if _, err := task.MarkSkipping(result.SkipReason); err != nil {
				return processed, err
			}
			if stop, err := finish(task); stop {
				return processed, err
			}
			continue
		}
		if task.TaskType == models.StrmGenerationTaskTypeDirectoryScan {
			if err := task.MarkDirectoryScanExpanded(task.TotalItems); err != nil {
				return processed, err
			}
			continue
		}
		if task.ParentTaskId > 0 {
			_, progressErr := markStrmGenerationChildFinalizing(task, result)
			if progressErr != nil {
				return processed, progressErr
			}
		}
		if stop, err := finish(task); stop {
			return processed, err
		}
	}
	return processed, nil
}

func processStrmGenerationTask(ctx context.Context, service *StrmGenerationService, task *models.StrmGenerationTask, configs generationConfigs, blockers []*models.StrmGenerationTask, directories *generationDirectories) (*StrmGenerationResult, error) {
	if task.TaskType == models.StrmGenerationTaskTypeDirectoryScan {
		_, err := service.expandDirectoryScan(ctx, task, configs, blockers)
		if err == nil && task.SkipReason != "" {
			return &StrmGenerationResult{SkipReason: task.SkipReason}, nil
		}
		return nil, err
	}
	return service.generate(ctx, StrmGenerationInput{Task: task}, configs, blockers, directories)
}

func completeStrmGenerationChild(parentTaskID uint, result *StrmGenerationResult, failed bool) (*models.StrmGenerationTask, error) {
	if parentTaskID == 0 {
		return nil, nil
	}
	return models.UpdateStrmGenerationParentProgress(parentTaskID, strmGenerationParentProgress(result, failed))
}

func markStrmGenerationChildFinalizing(task *models.StrmGenerationTask, result *StrmGenerationResult) (*models.StrmGenerationTask, error) {
	if task == nil || task.ParentTaskId == 0 {
		return nil, nil
	}
	parent, err := models.MarkStrmGenerationChildFinalizing(task.ID, task.ParentTaskId, strmGenerationParentProgress(result, false))
	if err != nil {
		return nil, err
	}
	task.Status = models.StrmGenerationStatusFinalizing
	task.LastError = ""
	return parent, nil
}

func strmGenerationParentProgress(result *StrmGenerationResult, failed bool) models.StrmGenerationParentProgress {
	progress := models.StrmGenerationParentProgress{
		Accepted:       boolToInt(!failed),
		Failed:         boolToInt(failed),
		Changed:        boolToInt(result != nil && result.Changed),
		NewMeta:        resultNewMeta(result),
		RefreshTargets: resultRefreshTargets(result),
	}
	return progress
}

func completeStrmGenerationTaskAfterSideEffects(ctx context.Context, service *StrmGenerationService, task *models.StrmGenerationTask) (err error) {
	if task == nil {
		return nil
	}
	// 按进入收尾时的状态处理错误；MarkCompleted 保存失败前也会先修改内存状态。
	finalizing := task.Status == models.StrmGenerationStatusFinalizing
	runningFile := task.Status == models.StrmGenerationStatusRunning && task.ParentTaskId == 0 &&
		(task.TaskType == models.StrmGenerationTaskTypeFile || task.TaskType == "")
	defer func() {
		if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if _, missing := errors.AsType[*generationSyncPathMissingError](err); missing {
			return
		}
		var saveErr error
		if finalizing {
			saveErr = task.MarkFinalizingRetry(err.Error())
		} else if runningFile {
			saveErr = task.MarkRunningFinalizationFailed(ctx, err.Error())
		}
		if saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}()
	// 生成释放范围后目录可能被删除。收尾再次持有范围，直到刷新、完成状态与源清理结束。
	_, release, scopeErr := models.AcquireSyncFileScope(ctx, task.SyncPathId, task.FileId, task.PickCode)
	if scopeErr != nil {
		return generationScopeError(ctx, task.SyncPathId, scopeErr)
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return err
	}
	if task.ParentTaskId > 0 {
		parent, err := models.GetStrmGenerationTaskByID(task.ParentTaskId)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := submitStrmGenerationParentRefreshIfReady(ctx, service, parent); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := task.MarkCompleted(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if task.Status != models.StrmGenerationStatusSkipped {
		cleanupDirectoryUploadSourcesAfterStrmSuccess(task)
	}
	return nil
}

func cleanupDirectoryUploadSourcesAfterStrmSuccess(task *models.StrmGenerationTask) {
	if task == nil {
		return
	}
	if task.UploadTaskId == 0 {
		return
	}
	if err := cleanupSourceAfterStrmSuccess(task.UploadTaskId); err != nil {
		helpers.AppLogger.Warnf("STRM 生成完成后清理目录上传源文件失败：upload_task_id=%d err=%v", task.UploadTaskId, err)
	}
}

func submitStrmGenerationParentRefreshIfReady(ctx context.Context, service *StrmGenerationService, parent *models.StrmGenerationTask) error {
	if parent == nil || !parent.IsReadyToSubmitRefresh() {
		return nil
	}
	if err := service.submitEmbyRefreshTargets(ctx, parent.SyncPathId, parent.GetRefreshTargets()); err != nil {
		return err
	}
	return models.MarkStrmGenerationRefreshSubmitted(parent.ID)
}

func (service *StrmGenerationService) submitEmbyRefreshTargets(ctx context.Context, syncPathID uint, targets []models.EmbyRefreshTarget) error {
	if service == nil || service.requestEmbyRefreshTargets == nil {
		return errors.New("Emby 刷新任务提交器未初始化")
	}
	acquire := service.acquireRefreshSubmission
	if acquire == nil {
		acquire = acquireStrmGenerationRefreshSubmission
	}
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return service.requestEmbyRefreshTargets(syncPathID, targets)
}

func (service *StrmGenerationService) submitEmbyRefreshBySyncFile(ctx context.Context, syncFile *models.SyncFile) error {
	if service == nil || service.requestEmbyRefreshBySyncFile == nil {
		return errors.New("Emby 刷新任务提交器未初始化")
	}
	acquire := service.acquireRefreshSubmission
	if acquire == nil {
		acquire = acquireStrmGenerationRefreshSubmission
	}
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return service.requestEmbyRefreshBySyncFile(syncFile)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func resultNewMeta(result *StrmGenerationResult) int {
	if result == nil {
		return 0
	}
	return result.NewMeta
}

func resultRefreshTargets(result *StrmGenerationResult) []models.EmbyRefreshTarget {
	if result == nil {
		return nil
	}
	return result.RefreshTargets
}
