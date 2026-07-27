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
	SyncFile       *models.SyncFile
	Changed        bool
	NewMeta        int
	RefreshTargets []models.EmbyRefreshTarget
}

// StrmGenerationService 复用现有 SyncStrm 能力完成单文件 STRM 后处理。
type StrmGenerationService struct {
	buildSyncer                  func(*models.SyncPath, *models.Account) (*SyncStrm, error)
	compareStrm                  func(*SyncStrm, *SyncFileCache) int
	processStrmFile              func(*SyncStrm, *SyncFileCache) error
	requestEmbyRefreshBySyncFile func(*models.SyncFile) error
	resolveRefreshTarget         func(*models.SyncFile) (models.EmbyRefreshTarget, error)
	requestEmbyRefreshTargets    func(uint, []models.EmbyRefreshTarget) error
	acquireRefreshSubmission     func(context.Context) (func(), error)
	detailByFileID               func(context.Context, *SyncStrm, string) (*SyncFileCache, error)
	resolveStrmOwner             func(context.Context, *SyncStrm, *SyncFileCache) (bool, error)
}

// NewStrmGenerationService 创建 STRM 生成服务。
func NewStrmGenerationService() *StrmGenerationService {
	service := &StrmGenerationService{}
	service.buildSyncer = func(syncPath *models.SyncPath, account *models.Account) (*SyncStrm, error) {
		syncer := NewSyncStrmForStrmGeneration(syncPath, account)
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
	service.resolveStrmOwner = resolveLatest115StrmOwner
	return service
}

// Generate 为单个远端文件生成或确认 STRM。
func (service *StrmGenerationService) Generate(ctx context.Context, input StrmGenerationInput) (*StrmGenerationResult, error) {
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

	syncPath := models.GetSyncPathById(task.SyncPathId)
	if syncPath == nil {
		return nil, fmt.Errorf("同步目录不存在：%d", task.SyncPathId)
	}
	account, err := loadStrmGenerationAccount(syncPath, task.AccountId)
	if err != nil {
		return nil, err
	}
	syncer, err := service.buildSyncer(syncPath, account)
	if err != nil {
		return nil, err
	}
	if syncer == nil {
		return nil, errors.New("STRM 同步器为空")
	}
	if syncer.Cancel != nil {
		defer syncer.Cancel()
	}

	file, err := service.buildFileCache(ctx, syncer, syncPath, account, task)
	if err != nil {
		return nil, err
	}
	if file.IsVideo {
		unlock := lockStrmTarget(file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath))
		defer unlock()
	}
	isStrmOwner, err := service.resolveStrmOwner(ctx, syncer, file)
	if err != nil {
		return nil, err
	}
	existing, err := findExistingGeneratedSyncFile(task.SyncPathId, file.GetFileId(), file.PickCode)
	if err != nil {
		return nil, err
	}

	syncFile := file.GetSyncFile(syncer, account.BaseUrl)
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

	if err := db.Db.Save(syncFile).Error; err != nil {
		return nil, fmt.Errorf("保存 SyncFile 失败：%w", err)
	}
	if changed {
		if err := cleanupOldGeneratedStrm(syncer.TargetPath, oldLocalPath, newLocalPath); err != nil {
			helpers.AppLogger.Warnf("清理旧 STRM 文件失败：%v", err)
		}
	}
	isWebhookFile := task.Source == models.StrmGenerationSourceWebhook && task.TaskType == models.StrmGenerationTaskTypeFile
	newMeta := 0
	if isWebhookFile && task.DownloadMeta && file.IsVideo {
		newMeta, err = service.downloadMatchedMetadata(ctx, syncer, file)
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

func (service *StrmGenerationService) downloadMatchedMetadata(ctx context.Context, syncer *SyncStrm, video *SyncFileCache) (int, error) {
	if syncer == nil || syncer.SyncDriver == nil || video == nil || !video.IsVideo {
		return 0, nil
	}
	if video.ParentId == "" || video.Path == "" {
		return 0, nil
	}
	files, err := syncer.SyncDriver.GetNetFileFiles(ctx, video.Path, video.ParentId)
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

		baseURL := ""
		if syncer.Account != nil {
			baseURL = syncer.Account.BaseUrl
		}
		syncFile := item.GetSyncFile(syncer, baseURL)
		if helpers.PathExists(syncFile.LocalFilePath) {
			continue
		}
		if err := db.Db.Save(syncFile).Error; err != nil {
			return created, fmt.Errorf("保存元数据 SyncFile 失败：%w", err)
		}
		if err := models.AddDownloadTaskFromSyncFile(syncFile); err != nil {
			if strings.Contains(err.Error(), "任务已存在") {
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

	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("读取目录监控元数据源文件失败：%w", err)
	}
	if err := helpers.WriteFileWithPerm(targetPath, content, 0777); err != nil {
		return fmt.Errorf("写入目录监控元数据到 STRM 路径失败：%w", err)
	}

	mtime := uploadTask.LocalMtime
	if mtime == 0 {
		mtime = info.ModTime().Unix()
	}
	t := time.Unix(mtime, 0)
	if err := os.Chtimes(targetPath, t, t); err != nil {
		return fmt.Errorf("修改目录监控元数据 STRM 路径时间失败：%w", err)
	}
	return nil
}

func validateDirectoryUploadMetadataSource(task *models.DbUploadTask, info os.FileInfo) error {
	if task == nil || info == nil {
		return errors.New("目录监控元数据源文件校验参数为空")
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
		// 百度普通扫描以完整路径作为 SyncFile.file_id；fs_id 保留在 PickCode，
		// 避免 STRM 后处理产生无法被后续扫描协调的 ID 形式。
		file.FileId = pathpkg.Join(file.Path, file.FileName)
	}
	if file.GetFileId() == "" && file.PickCode == "" {
		return nil, errors.New("STRM 生成任务缺少远端文件标识")
	}
	return file, nil
}

// ExpandDirectoryScan 将目录扫描父任务展开为待处理的单文件 STRM 任务。
func (service *StrmGenerationService) ExpandDirectoryScan(ctx context.Context, task *models.StrmGenerationTask) (int, error) {
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

	syncPath := models.GetSyncPathById(task.SyncPathId)
	if syncPath == nil {
		return 0, fmt.Errorf("同步目录不存在：%d", task.SyncPathId)
	}
	account, err := loadStrmGenerationAccount(syncPath, task.AccountId)
	if err != nil {
		return 0, err
	}
	syncer, err := service.buildSyncer(syncPath, account)
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
		file.IsVideo = true
		file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath)
		videoFiles = append(videoFiles, file)
	}

	owners := selectLatest115StrmOwners(videoFiles)
	for _, file := range videoFiles {
		if file.SourceType == models.SourceType115 {
			targetPath := filepath.ToSlash(filepath.Clean(file.LocalFilePath))
			if owners[targetPath] != file {
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
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
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

func loadStrmGenerationAccount(syncPath *models.SyncPath, taskAccountID uint) (*models.Account, error) {
	accountID := taskAccountID
	if accountID == 0 && syncPath != nil {
		accountID = syncPath.AccountId
	}
	if accountID == 0 {
		return &models.Account{SourceType: models.SourceTypeLocal}, nil
	}
	account, err := models.GetAccountById(accountID)
	if err != nil {
		return nil, fmt.Errorf("获取网盘账号失败：%w", err)
	}
	return account, nil
}

func findExistingGeneratedSyncFile(syncPathID uint, fileID string, pickCode string) (*models.SyncFile, error) {
	if fileID == "" && pickCode == "" {
		return nil, nil
	}
	var existing models.SyncFile
	query := db.Db.Where("sync_path_id = ?", syncPathID)
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
		return errors.New("STRM 目标根路径或待清理路径为空")
	}
	rel, err := filepath.Rel(rootPath, targetPath)
	if err != nil {
		return fmt.Errorf("计算 STRM 清理路径边界失败：%w", err)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("旧 STRM 路径越界：%s", targetPath)
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
		if _, err := ProcessPendingStrmGenerationTasks(ctx, service, strmGenerationWorkerBatch); err != nil && !errors.Is(err, context.Canceled) {
			helpers.AppLogger.Errorf("处理 STRM 生成队列失败：%v", err)
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
	tasks, err := models.GetPendingStrmGenerationTasks(limit)
	if err != nil {
		return 0, err
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
			if err := completeStrmGenerationTaskAfterSideEffects(ctx, service, task); err != nil {
				return processed, err
			}
			continue
		}
		if err := task.MarkRunning(); err != nil {
			continue
		}
		processed++
		result, err := processStrmGenerationTask(ctx, service, task)
		if err != nil {
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
		if task.TaskType == models.StrmGenerationTaskTypeDirectoryScan {
			if err := task.MarkDirectoryScanExpanded(task.TotalItems); err != nil {
				return processed, err
			}
			continue
		}
		if task.ParentTaskId > 0 {
			parent, progressErr := markStrmGenerationChildFinalizing(task, result)
			if progressErr != nil {
				return processed, progressErr
			}
			if submitErr := submitStrmGenerationParentRefreshIfReady(ctx, service, parent); submitErr != nil {
				return processed, submitErr
			}
		}
		if err := completeStrmGenerationTaskAfterSideEffects(ctx, service, task); err != nil {
			return processed, err
		}
	}
	return processed, nil
}

func processStrmGenerationTask(ctx context.Context, service *StrmGenerationService, task *models.StrmGenerationTask) (*StrmGenerationResult, error) {
	if task.TaskType == models.StrmGenerationTaskTypeDirectoryScan {
		_, err := service.ExpandDirectoryScan(ctx, task)
		return nil, err
	}
	return service.Generate(ctx, StrmGenerationInput{Task: task})
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

func completeStrmGenerationTaskAfterSideEffects(ctx context.Context, service *StrmGenerationService, task *models.StrmGenerationTask) error {
	if task == nil {
		return nil
	}
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
	cleanupDirectoryUploadSourcesAfterStrmSuccess(task)
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
