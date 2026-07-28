package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// InventoryStatus 是列表接口返回的目录清点状态。
const (
	InventoryStatusReady    = "ready"
	InventoryStatusScanning = "scanning"
)

var (
	inventoryScanning atomic.Bool
	inventoryMutex    sync.Mutex
)

// CurrentInventoryStatus 返回目录清点状态，供既有备份列表接口在不阻塞页面打开的前提下展示加载提示。
func CurrentInventoryStatus() string {
	if inventoryScanning.Load() {
		return InventoryStatusScanning
	}
	return InventoryStatusReady
}

// TriggerInventoryScan 触发单一后台目录清点。
// 服务启动和既有列表请求都只调用它，绝不在 HTTP 请求内同步扫描；
// 已有清点在执行或实际备份/恢复占用协调器时直接返回。
func TriggerInventoryScan() {
	if inventoryScanning.Load() {
		return
	}
	read, accepted := BeginInventoryRead()
	if !accepted {
		return
	}
	if !inventoryScanning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer inventoryScanning.Store(false)
		if err := runInventoryScan(read); err != nil && !errors.Is(err, ErrInventoryReadInvalidated) {
			helpers.AppLogger.Warnf("备份目录清点失败：%v", err)
		}
	}()
}

// runInventoryScan 扫描工件目录与上传暂存目录，并按缓存身份增量更新索引。
// 每个扫描、验证和写索引边界都会检查本轮是否仍然有效：实际备份或恢复一旦接管，
// 本轮立即放弃且不得再写入索引，也不让实际操作等待这里的磁盘 I/O。
func runInventoryScan(read InventoryRead) error {
	inventoryMutex.Lock()
	defer inventoryMutex.Unlock()

	records, err := models.InventoryRecords()
	if err != nil {
		return err
	}
	indexed := make(map[string]models.BackupRecord, len(records))
	for _, record := range records {
		key := record.InventoryPath
		if key == "" {
			key = record.FilePath
		}
		if key != "" {
			indexed[filepath.Clean(key)] = record
		}
	}

	seen := make(map[string]struct{}, len(indexed))
	for _, root := range []struct {
		directory  string
		backupType string
		// allowHidden 只对上传暂存目录开启：受理上传时写入的候选工件本身以点号开头，
		// 未完成的上传已在写入失败时删除，因此这里剩下的都是完整候选。
		allowHidden bool
	}{
		{directory: ArtifactDir(), backupType: models.BackupTypeImported},
		{directory: UploadStagingDir(), backupType: models.BackupTypeTemporaryUpload, allowHidden: true},
	} {
		paths, err := listInventoryCandidates(root.directory, root.allowHidden)
		if err != nil {
			return err
		}
		for _, path := range paths {
			if !read.Valid() {
				return ErrInventoryReadInvalidated
			}
			seen[path] = struct{}{}
			if err := reconcileInventoryEntry(read, path, root.backupType, indexed[path]); err != nil {
				return err
			}
		}
	}

	// 文件已不存在的记录必须删除，否则列表会显示无法恢复也无法下载的幽灵条目。
	for path, record := range indexed {
		if _, found := seen[path]; found {
			continue
		}
		if err := read.WriteIndex(func() error {
			return models.DeleteInventoryRecord(record.ID)
		}); err != nil {
			return err
		}
	}
	return nil
}

// listInventoryCandidates 返回目录根部的候选工件规范路径。
// 只接受根部的常规 .zip 文件，跳过发布中的临时文件与迁移包；回滚快照不在扫描范围内。
func listInventoryCandidates(directory string, allowHidden bool) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !isInventoryCandidateName(entry.Name(), allowHidden) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		paths = append(paths, filepath.Clean(filepath.Join(directory, entry.Name())))
	}
	return paths, nil
}

func isInventoryCandidateName(name string, allowHidden bool) bool {
	if !strings.EqualFold(filepath.Ext(name), ".zip") {
		return false
	}
	if strings.EqualFold(name, "migrate.zip") {
		return false
	}
	return allowHidden || !strings.HasPrefix(name, ".")
}

// reconcileInventoryEntry 按缓存身份决定复用已完成的验证结果还是重新验证。
func reconcileInventoryEntry(read InventoryRead, path string, defaultType string, existing models.BackupRecord) error {
	info, err := os.Lstat(path)
	if err != nil {
		return nil
	}
	if existing.ID != 0 &&
		existing.InventoryFileSize == info.Size() &&
		existing.InventoryModifiedAt == info.ModTime().Unix() {
		// 身份未变化：复用已完成的验证结果，不重复读取和解密工件。
		return nil
	}
	if !read.Valid() {
		return ErrInventoryReadInvalidated
	}

	classified := classifyInventoryArtifact(path)
	record := existing
	record.InventoryPath = path
	record.FilePath = path
	record.FileSize = info.Size()
	record.InventoryFileSize = info.Size()
	record.InventoryModifiedAt = info.ModTime().Unix()
	record.Status = models.BackupStatusCompleted
	record.Format = classified.format
	record.VerificationState = classified.verificationState
	record.VerificationErrorCode = classified.errorCode
	record.BackupType = inventoryBackupType(existing, defaultType)
	if record.ID == 0 {
		record.CreatedAt = info.ModTime().Unix()
		record.CompletedAt = info.ModTime().Unix()
		record.IsCompressed = 1
	}

	return read.WriteIndex(func() error {
		return models.UpsertInventoryRecord(&record)
	})
}

// inventoryBackupType 决定重新验证后的来源归属。
// 应用生成与升级迁移标记的记录保持原有归属，其余一律按发现位置归类：
// 清点器无法区分「本应用生成的备份」与「用户手工拷入的备份」，因此只可保守地不参与自动清理。
func inventoryBackupType(existing models.BackupRecord, defaultType string) string {
	switch existing.BackupType {
	case models.BackupTypeManual, models.BackupTypeAuto, models.BackupTypeLegacy:
		return existing.BackupType
	}
	return defaultType
}

// inventoryClassification 是一次目录导入验证的分类结果。
type inventoryClassification struct {
	format            string
	verificationState string
	errorCode         string
}

// classifyInventoryArtifact 判定候选工件的格式与验证状态。
// 未加密 v1 完整验证；加密 v1 只做外层扫描并标记待密码验证，其完整内容验证留到恢复预检；
// 损坏、超限或不支持的 ZIP 记为无效并保留稳定安全错误码，禁用恢复但仍可下载与手动删除。
func classifyInventoryArtifact(path string) inventoryClassification {
	header, err := InspectArtifact(path)
	if err != nil {
		if isLegacyArtifact(path) {
			// 旧格式不是「无效工件」：它可下载、参与既有清理归属，只是常规恢复固定拒绝，
			// 因此不设置验证状态，避免与损坏工件在列表上混为一谈。
			return inventoryClassification{format: models.BackupFormatLegacy}
		}
		return inventoryClassification{
			format:            models.BackupFormatV1,
			verificationState: models.BackupVerificationInvalid,
			errorCode:         string(operationErrorCodeFor(err, OperationErrorCodeArtifactInvalid)),
		}
	}
	if header.Encryption.Enabled {
		return inventoryClassification{
			format:            models.BackupFormatV1,
			verificationState: models.BackupVerificationPendingPassword,
		}
	}

	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		return inventoryClassification{
			format:            models.BackupFormatV1,
			verificationState: models.BackupVerificationInvalid,
			errorCode:         string(OperationErrorCodeInternal),
		}
	}
	verified, err := VerifyArtifact(ArtifactVerificationOptions{
		ArtifactPath:         path,
		StagingDir:           VerificationStagingDir(),
		CurrentEncryptionKey: []byte(keyText),
	})
	if err != nil {
		return inventoryClassification{
			format:            models.BackupFormatV1,
			verificationState: models.BackupVerificationInvalid,
			errorCode:         string(operationErrorCodeFor(err, OperationErrorCodeArtifactInvalid)),
		}
	}
	if cleanupErr := verified.Cleanup(); cleanupErr != nil {
		helpers.AppLogger.Warnf("清理清点暂存文件失败：%v", cleanupErr)
	}
	return inventoryClassification{
		format:            models.BackupFormatV1,
		verificationState: models.BackupVerificationVerified,
	}
}

// isLegacyArtifact 判断 ZIP 是否为旧格式备份：可打开但没有 v1 的 header.json。
// 旧格式在列表标识为旧格式，允许下载，常规恢复固定拒绝。
func isLegacyArtifact(path string) bool {
	header, err := legacyArtifactHeaderPresence(path)
	if err != nil {
		return false
	}
	return !header
}

func legacyArtifactHeaderPresence(path string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > artifactMaxUploadSize {
		return false, os.ErrInvalid
	}
	archive, err := zip.OpenReader(path)
	if err != nil {
		return false, err
	}
	defer archive.Close()
	for _, entry := range archive.File {
		if entry.Name == artifactHeaderPath {
			return true, nil
		}
	}
	return false, nil
}
