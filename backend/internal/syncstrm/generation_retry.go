package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
)

// selectGenerationTasks 先排除仍有依赖的任务，再凑足一批，避免队头挡住独立文件。
func selectGenerationTasks(ctx context.Context, limit int, blockers []*models.StrmGenerationTask) ([]*models.StrmGenerationTask, error) {
	if limit <= 0 {
		limit = 10
	}
	paths := make(map[uint]*models.SyncPath)
	readPath := func(id uint) (*models.SyncPath, error) {
		if path, ok := paths[id]; ok {
			return path, nil
		}
		path, err := generationTaskSyncPath(ctx, id)
		if err == nil {
			paths[id] = path
		}
		return path, err
	}
	// 退避中的项也要核对，不能让已删除目录永久占据阻塞列表。
	for _, blocker := range blockers {
		if _, err := readPath(blocker.SyncPathId); err != nil {
			return nil, err
		}
	}
	scopes := make(map[uint][]syncscope.Scope)
	readScopes := func(task *models.StrmGenerationTask) ([]syncscope.Scope, error) {
		if cached, ok := scopes[task.ID]; ok {
			return cached, nil
		}
		path, err := readPath(task.SyncPathId)
		if err != nil {
			return nil, err
		}
		result, err := generationTaskScopesWithPath(ctx, task, path)
		if err == nil {
			scopes[task.ID] = result
		}
		return result, err
	}
	now := time.Now().Unix()
	selected := make([]*models.StrmGenerationTask, 0, limit)
	var after *models.StrmGenerationTaskCursor
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := models.GetPendingStrmGenerationTaskPage(limit, after, now)
		if err != nil {
			return nil, err
		}
		for _, task := range page {
			if _, err := readPath(task.SyncPathId); err != nil {
				return nil, err
			}
			blocked, err := generationTaskBlocked(task, blockers, readScopes)
			if err != nil {
				return nil, err
			}
			if !blocked {
				selected = append(selected, task)
			}
			if len(selected) == limit {
				return selected, nil
			}
		}
		if len(page) < limit {
			return selected, nil
		}
		after = page[len(page)-1].QueueCursor()
	}
}

var errGenerationDependencyWait = errors.New("等待较早任务收尾")
var errGenerationDependencyCheck = errors.New("核对 STRM 收尾依赖失败")

type generationSyncPathMissingError struct {
	syncPathID uint
}

func (err *generationSyncPathMissingError) Error() string {
	return fmt.Sprintf("STRM 同步目录不存在：%d", err.syncPathID)
}

func generationScopeError(ctx context.Context, syncPathID uint, err error) error {
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	// 范围读取还可能失败于其他关联记录，必须单独确认目录缺失。
	var sp models.SyncPath
	if lookupErr := db.Db.WithContext(ctx).Where("id = ?", syncPathID).First(&sp).Error; lookupErr != nil {
		if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return &generationSyncPathMissingError{syncPathID: syncPathID}
		}
		return lookupErr
	}
	return err
}

func generationTaskBlocked(task *models.StrmGenerationTask, blockers []*models.StrmGenerationTask, readScopes func(*models.StrmGenerationTask) ([]syncscope.Scope, error)) (bool, error) {
	// ponytail: 仅在有收尾重试时逐项比较；积压明显变大后再按范围建索引。
	for _, prior := range blockers {
		if prior.ID >= task.ID {
			continue
		}
		if (task.ParentTaskId != 0 && task.ParentTaskId == prior.ParentTaskId) ||
			(task.UploadTaskId != 0 && task.UploadTaskId == prior.UploadTaskId) ||
			(task.AccountId != 0 && task.AccountId == prior.AccountId && ((task.FileId != "" && task.FileId == prior.FileId) || (task.PickCode != "" && task.PickCode == prior.PickCode))) {
			return true, nil
		}
		a, err := readScopes(prior)
		if err != nil {
			return false, err
		}
		b, err := readScopes(task)
		if err != nil {
			return false, err
		}
		blocked, err := syncscope.Overlap(a, b)
		if err != nil || blocked {
			return blocked, err
		}
	}
	return false, nil
}

// checkGenerationDependencies 在持有实际同步范围后复查，防止等待期间改目录或补详情改变目标。
func checkGenerationDependencies(ctx context.Context, task *models.StrmGenerationTask, file *SyncFileCache, blockers []*models.StrmGenerationTask) error {
	if len(blockers) == 0 {
		return nil
	}
	actual := *task
	if file != nil {
		actual.FileId = file.GetFileId()
		actual.PickCode = file.PickCode
		actual.ParentId = file.ParentId
		actual.Path = file.Path
		actual.FileName = file.FileName
		actual.FileSize = file.FileSize
		actual.Mtime = file.MTime
		actual.Sha1 = file.Sha1
	}
	blocked, err := generationTaskBlocked(&actual, blockers, func(candidate *models.StrmGenerationTask) ([]syncscope.Scope, error) {
		return generationTaskScopes(ctx, candidate)
	})
	if err != nil {
		return fmt.Errorf("%w: %w", errGenerationDependencyCheck, err)
	}
	if blocked {
		// 保存已确认的位置，下一轮筛选即可跳过它，不必反复补详情占满批次。
		*task = actual
		return errGenerationDependencyWait
	}
	return nil
}

func generationTaskScopes(ctx context.Context, task *models.StrmGenerationTask) ([]syncscope.Scope, error) {
	sp, err := generationTaskSyncPath(ctx, task.SyncPathId)
	if err != nil {
		return nil, err
	}
	return generationTaskScopesWithPath(ctx, task, sp)
}

func generationTaskSyncPath(ctx context.Context, syncPathID uint) (*models.SyncPath, error) {
	var sp models.SyncPath
	if err := db.Db.WithContext(ctx).Where("id = ?", syncPathID).First(&sp).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, &generationSyncPathMissingError{syncPathID: syncPathID}
		}
		return nil, err
	}
	return &sp, nil
}

func generationTaskScopesWithPath(ctx context.Context, task *models.StrmGenerationTask, sp *models.SyncPath) ([]syncscope.Scope, error) {
	file := &SyncFileCache{FileId: task.FileId, PickCode: task.PickCode, ParentId: task.ParentId,
		Path: task.Path, FileName: task.FileName, FileSize: task.FileSize, MTime: task.Mtime,
		Sha1: task.Sha1, SourceType: sp.SourceType}
	// 需要补详情、目录展开或会写同目录元数据时，不能只按一个 STRM 文件判断。
	broad := task.TaskType != models.StrmGenerationTaskTypeFile || fileNeedsRemoteDetail(file) || task.DownloadMeta
	scopes := []syncscope.Scope{}
	if broad {
		root := sp.Scope()
		root.SyncPathID = 0
		scopes = append(scopes, root)
	} else {
		scope := syncscope.Scope{SourceType: string(sp.SourceType), AccountID: sp.AccountId, RemotePath: file.GetFullRemotePath()}
		scope.LocalPath = file.GetLocalFilePath(sp.LocalPath, sp.RemotePath)
		scopes = append(scopes, scope)
		file.LocalFilePath = ""
		file.IsVideo = true
		scope.LocalPath = file.GetLocalFilePath(sp.LocalPath, sp.RemotePath)
		scopes = append(scopes, scope)
	}
	if task.UploadTaskId != 0 {
		var upload models.DbUploadTask
		if err := db.Db.WithContext(ctx).First(&upload, task.UploadTaskId).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		// 上传历史可以被清空。剩余任务仍按远端和目标位置排序，清理入口会跳过缺失源记录；
		// 需要本地源文件的元数据复制仍由 copyDirectoryUploadMetadata 明确拒绝。
		if upload.Source == models.UploadSourceDirectoryMonitor {
			if upload.LocalFullPath == "" {
				scopes = append(scopes, syncscope.Scope{Global: true})
			} else {
				scopes = append(scopes, syncscope.Scope{LocalPath: upload.LocalFullPath})
			}
		}
	}
	// 旧记录可能还在改配置前的位置；文件任务只查自己的记录。
	query := db.Db.WithContext(ctx).Model(&models.SyncFile{}).Select("id", "source_type", "account_id", "path", "file_name", "local_file_path").Where("sync_path_id = ?", task.SyncPathId)
	if task.TaskType == models.StrmGenerationTaskTypeFile {
		if file.GetFileId() == "" && file.PickCode == "" {
			return append(scopes, syncscope.Scope{Global: true}), nil
		}
		query = query.Where("(file_id <> '' AND file_id = ?) OR (pick_code <> '' AND pick_code = ?)", file.GetFileId(), file.PickCode)
	}
	query = query.Session(&gorm.Session{})
	var afterID uint
	for {
		var old []models.SyncFile
		if err := query.Where("id > ?", afterID).Order("id ASC").Limit(256).Find(&old).Error; err != nil {
			return nil, err
		}
		for _, previous := range old {
			if previous.LocalFilePath == "" || previous.Path == "" || previous.SourceType == "" {
				return append(scopes, syncscope.Scope{Global: true}), nil
			}
			scopes = append(scopes, syncscope.Scope{SourceType: string(previous.SourceType), AccountID: previous.AccountId,
				RemotePath: filepath.ToSlash(filepath.Join(previous.Path, previous.FileName)), LocalPath: previous.LocalFilePath})
		}
		if len(old) < 256 {
			return scopes, nil
		}
		afterID = old[len(old)-1].ID
	}
}
