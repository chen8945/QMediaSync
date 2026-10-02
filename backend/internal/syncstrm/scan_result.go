package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/openlist"
	"qmediasync/internal/realtime"
	"qmediasync/internal/v115open"
)

type fatalScanError struct{ error }

func fatalSyncError(err error) error {
	if err == nil {
		return nil
	}
	return &fatalScanError{err}
}
func (e *fatalScanError) Unwrap() error { return e.error }
func isFatalSyncError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	if _, ok := errors.AsType[*fatalScanError](err); ok {
		return true
	}
	if errors.Is(err, openlist.ErrTokenExpired) {
		return true
	}
	if apiErr, ok := errors.AsType[*v115open.OpenAPIError](err); ok {
		if apiErr.HTTPStatus == 401 || apiErr.Code == v115open.ACCESS_TOKEN_AUTH_FAIL || apiErr.Code == v115open.ACCESS_AUTH_INVALID || apiErr.Code == v115open.ACCESS_TOKEN_EXPIRY_CODE || apiErr.Code == v115open.REFRESH_TOKEN_INVALID {
			return true
		}
	}
	_, ok := errors.AsType[*baidupan.TokenInvalidError](err)
	return ok
}

func normalizedScanPath(value string) string {
	return filepath.ToSlash(filepath.Clean(value))
}
func pathWithin(root, value string) bool {
	root, value = normalizedScanPath(root), normalizedScanPath(value)
	return value == root || strings.HasPrefix(value, strings.TrimSuffix(root, "/")+"/")
}

func (s *SyncStrm) recordScanFailure(remotePath string, err error) {
	if err == nil {
		return
	}
	if remotePath == "" {
		remotePath = s.SourcePath
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanFailures == nil {
		s.scanFailures = make(map[string]realtime.SyncScanFailure)
	}
	remotePath = normalizedScanPath(remotePath)
	s.scanFailures["directory:"+remotePath] = realtime.SyncScanFailure{Kind: "directory", Path: remotePath, Reason: helpers.RedactSensitiveLog(err.Error())}
}

// 百度路径会随移动改变；失败目录的 fs_id 仅用于关联同一账号和同步目录的旧保护范围。
func (s *SyncStrm) recordOtherDirectoryFailure(ctx context.Context, item pathQueueItem, cause error) error {
	s.recordScanFailure(item.Path, cause)
	if s.Account.SourceType != models.SourceTypeBaiduPan || normalizedScanPath(item.Path) == normalizedScanPath(s.SourcePath) {
		return nil
	}
	protectRoot := func() {
		s.recordScanFailure(s.SourcePath, fmt.Errorf("无法确认百度失败目录的旧位置：%s：%w", item.Path, cause))
	}
	if s.TmpSyncPath || item.PickCode == "" || item.PickCode == "0" || !pathWithin(s.SourcePath, item.Path) {
		protectRoot()
		return nil
	}
	var previous []models.SyncFile
	if err := db.Db.WithContext(ctx).
		Where("sync_path_id = ? AND account_id = ? AND source_type = ? AND pick_code = ? AND file_type = ?",
			s.SyncPathId, s.Account.ID, models.SourceTypeBaiduPan, item.PickCode, v115open.TypeDir).
		Limit(2).Find(&previous).Error; err != nil {
		return fatalSyncError(fmt.Errorf("查询百度失败目录的旧位置：%w", err))
	}
	if len(previous) != 1 {
		protectRoot()
		return nil
	}
	old := previous[0]
	oldPath := normalizedScanPath(old.FileId)
	if old.FileId == "" || !pathWithin(s.SourcePath, oldPath) ||
		oldPath != normalizedScanPath(filepath.Join(old.Path, old.FileName)) {
		protectRoot()
		return nil
	}
	// 与增量目录移动采用相同的远端子树保护，旧事实不写入本轮缓存。
	s.recordScanFailure(oldPath, cause)
	return nil
}

func (s *SyncStrm) recordFileFailure(file *SyncFileCache, err error) {
	if err == nil {
		return
	}
	if file == nil {
		s.recordScanFailure(s.SourcePath, err)
		return
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanFiles == nil {
		s.scanFiles = make(map[string]string)
	}
	if s.scanFailures == nil {
		s.scanFailures = make(map[string]realtime.SyncScanFailure)
	}
	if s.protectedFileIDs == nil {
		s.protectedFileIDs = make(map[string]bool)
	}
	if s.protectedLocalPaths == nil {
		s.protectedLocalPaths = make(map[string]bool)
	}
	if s.protectedPickCodes == nil {
		s.protectedPickCodes = make(map[string]bool)
	}
	if file.PickCode != "" {
		s.protectedPickCodes[file.PickCode] = true
	}
	id := file.GetFileId()
	if id == "" {
		id = file.GetFullRemotePath()
	}
	s.scanFiles[id] = "failed"
	s.scanFailures["file:"+id] = realtime.SyncScanFailure{Kind: "file", Path: file.GetFullRemotePath(), FileID: file.GetFileId(), Reason: helpers.RedactSensitiveLog(err.Error())}
	if file.GetFileId() != "" {
		s.protectedFileIDs[file.GetFileId()] = true
	}
	copy := *file
	if local := copy.GetLocalFilePath(s.TargetPath, s.SourcePath); local != "" {
		s.protectedLocalPaths[normalizedScanPath(local)] = true
	}
}

func (s *SyncStrm) recordFileSuccess(file *SyncFileCache) { s.recordFileResult(file, "succeeded") }
func (s *SyncStrm) recordFileSkipped(file *SyncFileCache) { s.recordFileResult(file, "skipped") }
func (s *SyncStrm) recordFileResult(file *SyncFileCache, result string) {
	if file == nil || file.FileType == v115open.TypeDir {
		return
	}
	id := file.GetFileId()
	if id == "" {
		id = file.GetFullRemotePath()
	}
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scanFiles == nil {
		s.scanFiles = make(map[string]string)
	}
	if s.scanFiles[id] != "failed" && !(s.scanFiles[id] == "skipped" && result == "succeeded") {
		s.scanFiles[id] = result
	}
}

func (s *SyncStrm) skipMissingCleanup(reason string) {
	s.scanMu.Lock()
	s.missingCleanupReason = reason
	s.scanMu.Unlock()
}
func (s *SyncStrm) scanResultSnapshot() *realtime.SyncScanResult {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	result := &realtime.SyncScanResult{Failures: []realtime.SyncScanFailure{}, CleanupStatus: "completed"}
	for _, status := range s.scanFiles {
		switch status {
		case "succeeded":
			result.SucceededFiles++
		case "failed":
			result.FailedFiles++
		case "skipped":
			result.SkippedFiles++
		}
	}
	for _, failure := range s.scanFailures {
		result.Failures = append(result.Failures, failure)
	}
	slices.SortFunc(result.Failures, func(a, b realtime.SyncScanFailure) int { return strings.Compare(a.Kind+":"+a.Path, b.Kind+":"+b.Path) })
	if !s.cleanupStarted {
		result.CleanupStatus, result.CleanupReason = "skipped", "未执行本地差异清理"
	}
	if s.missingCleanupReason != "" {
		result.CleanupStatus, result.CleanupReason = "skipped", s.missingCleanupReason
	}
	if len(result.Failures) > 0 && s.cleanupStarted && s.missingCleanupReason == "" {
		result.CleanupStatus, result.CleanupReason = "partial", "失败文件和不完整目录保留，独立完整范围已核对"
		for _, failure := range result.Failures {
			if failure.Kind == "directory" && pathWithin(failure.Path, s.SourcePath) {
				result.CleanupStatus, result.CleanupReason = "skipped", "同步根扫描不完整，保留缺项文件和记录"
			}
		}
	}
	return result
}
func (s *SyncStrm) scanOutcome() (models.SyncStatus, error) {
	result := s.scanResultSnapshot()
	status := models.SyncStatusCompleted
	for _, failure := range result.Failures {
		if failure.Kind == "directory" {
			status = models.SyncStatusIncomplete
			break
		}
		status = models.SyncStatusPartial
	}
	if status == models.SyncStatusCompleted {
		return status, nil
	}
	return status, fmt.Errorf("%s：成功 %d，失败文件 %d，不完整目录 %d", models.SyncStatusText[status], result.SucceededFiles, result.FailedFiles, len(result.Failures)-result.FailedFiles)
}

func (s *SyncStrm) localDirectoryForRemote(remote string) string {
	if s.Account.SourceType == models.SourceTypeLocal {
		rel, err := filepath.Rel(s.SourcePath, remote)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return s.GetLocalBaseDir()
		}
		return normalizedScanPath(filepath.Join(s.TargetPath, rel))
	}
	return normalizedScanPath(filepath.Join(s.TargetPath, remote))
}

// cleanupProtected 对目录同时检查祖先关系，避免删除容纳受保护后代的空目录。
func (s *SyncStrm) cleanupProtected(local string, isDir bool) bool {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	for _, failure := range s.scanFailures {
		if failure.Kind != "directory" {
			continue
		}
		root := s.localDirectoryForRemote(failure.Path)
		if pathWithin(root, local) || (isDir && pathWithin(local, root)) {
			return true
		}
	}
	local = normalizedScanPath(local)
	for root := range s.protectedLocalDirectories {
		if pathWithin(root, local) || (isDir && pathWithin(local, root)) {
			return true
		}
	}
	for target := range s.protectedLocalPaths {
		if local == target || (isDir && pathWithin(local, target)) {
			return true
		}
	}
	return false
}

func (s *SyncStrm) ledgerProtected(file *models.SyncFile) bool {
	s.scanMu.Lock()
	for _, failure := range s.scanFailures {
		if failure.Kind == "directory" && (pathWithin(failure.Path, s.SourcePath) ||
			pathWithin(failure.Path, filepath.Join(file.Path, file.FileName)) ||
			(file.SourceType == models.SourceTypeBaiduPan && pathWithin(failure.Path, file.FileId))) {
			s.scanMu.Unlock()
			return true
		}
	}
	idProtected := s.protectedFileIDs[file.FileId] || (file.PickCode != "" && s.protectedPickCodes[file.PickCode])
	s.scanMu.Unlock()
	local := file.LocalFilePath
	if local == "" {
		local = s.MakeFullLocalPath(file)
	}
	return idProtected || s.cleanupProtected(local, file.FileType == v115open.TypeDir)
}

// 失败后才补读旧身份和目标 owner；成功任务不增加这些保护查询。
func (s *SyncStrm) prepareCleanupProtection() error {
	if s.TmpSyncPath {
		return nil
	}
	s.scanMu.Lock()
	ids := make([]string, 0, len(s.protectedFileIDs))
	for id := range s.protectedFileIDs {
		ids = append(ids, id)
	}
	pickCodes := make([]string, 0, len(s.protectedPickCodes))
	for code := range s.protectedPickCodes {
		pickCodes = append(pickCodes, code)
	}
	s.scanMu.Unlock()
	addRows := func(rows []models.SyncFile) {
		s.scanMu.Lock()
		defer s.scanMu.Unlock()
		if s.protectedFileIDs == nil {
			s.protectedFileIDs = make(map[string]bool)
		}
		if s.protectedLocalPaths == nil {
			s.protectedLocalPaths = make(map[string]bool)
		}
		if s.protectedLocalDirectories == nil {
			s.protectedLocalDirectories = make(map[string]bool)
		}
		for _, row := range rows {
			s.protectedFileIDs[row.FileId] = true
			local := row.LocalFilePath
			if local == "" {
				local = s.MakeFullLocalPath(&row)
			}
			if local != "" {
				s.protectedLocalPaths[normalizedScanPath(local)] = true
				if row.FileType == v115open.TypeDir {
					s.protectedLocalDirectories[normalizedScanPath(local)] = true
				}
			}
		}
	}
	for start := 0; start < len(ids); start += 256 {
		var rows []models.SyncFile
		if err := db.Db.WithContext(s.Context).Where("sync_path_id = ? AND file_id IN ?", s.SyncPathId, ids[start:min(start+256, len(ids))]).Find(&rows).Error; err != nil {
			return fatalSyncError(err)
		}
		addRows(rows)
	}
	for start := 0; start < len(pickCodes); start += 256 {
		var rows []models.SyncFile
		if err := db.Db.WithContext(s.Context).Where("sync_path_id = ? AND pick_code IN ?", s.SyncPathId, pickCodes[start:min(start+256, len(pickCodes))]).Find(&rows).Error; err != nil {
			return fatalSyncError(err)
		}
		addRows(rows)
	}
	if s.Account.SourceType == models.SourceTypeBaiduPan {
		// 后代可能保存了不同于当前目录映射的本地目标，必须分页保留这些实际目标。
		var roots []string
		for _, failure := range s.scanResultSnapshot().Failures {
			if failure.Kind != "directory" || slices.ContainsFunc(roots, func(root string) bool { return pathWithin(root, failure.Path) }) {
				continue
			}
			roots = slices.DeleteFunc(roots, func(root string) bool { return pathWithin(failure.Path, root) })
			roots = append(roots, failure.Path)
		}
		for _, root := range roots {
			prefix := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.TrimSuffix(root, "/")+"/") + "%"
			var cursor uint
			for {
				var rows []models.SyncFile
				if err := db.Db.WithContext(s.Context).
					Where("sync_path_id = ? AND account_id = ? AND source_type = ? AND id > ?", s.SyncPathId, s.Account.ID, models.SourceTypeBaiduPan, cursor).
					Where("file_id = ? OR file_id LIKE ? ESCAPE '!'", root, prefix).
					Order("id ASC").Limit(256).Find(&rows).Error; err != nil {
					return fatalSyncError(fmt.Errorf("查询百度失败目录的旧本地目标：%w", err))
				}
				// SQLite LIKE 默认不区分大小写，按真实路径复核以保留兄弟目录独立清理资格。
				protected := make([]models.SyncFile, 0, len(rows))
				for _, row := range rows {
					if pathWithin(root, row.FileId) {
						protected = append(protected, row)
					}
				}
				addRows(protected)
				if len(rows) < 256 {
					break
				}
				cursor = rows[len(rows)-1].ID
			}
		}
	}
	s.scanMu.Lock()
	targets := make([]string, 0, len(s.protectedLocalPaths))
	for target := range s.protectedLocalPaths {
		targets = append(targets, target)
	}
	s.scanMu.Unlock()
	for start := 0; start < len(targets); start += 256 {
		var rows []models.SyncFile
		if err := db.Db.WithContext(s.Context).Where("sync_path_id = ? AND local_file_path IN ?", s.SyncPathId, targets[start:min(start+256, len(targets))]).Find(&rows).Error; err != nil {
			return fatalSyncError(err)
		}
		addRows(rows)
	}
	return nil
}

func (s *SyncStrm) remotePathForLocal(local string) string {
	rel, err := filepath.Rel(s.GetLocalBaseDir(), local)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return s.SourcePath
	}
	return normalizedScanPath(filepath.Join(s.SourcePath, rel))
}
func (s *SyncStrm) recordLocalFileFailure(local string, err error) {
	remote := s.remotePathForLocal(local)
	s.recordFileFailure(&SyncFileCache{SourceType: s.Account.SourceType, FileId: remote, ParentId: filepath.Dir(remote), Path: filepath.Dir(remote), FileName: filepath.Base(remote), LocalFilePath: local}, err)
}
