package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/taskgate"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// StrmGenerationSource 是 STRM 生成任务来源。
type StrmGenerationSource string

const (
	StrmGenerationSourceUploadCompleted StrmGenerationSource = "upload_completed"
	StrmGenerationSourceWebhook         StrmGenerationSource = "webhook"
	StrmGenerationSourceRemoteExists    StrmGenerationSource = "remote_exists"
)

// StrmGenerationTaskType 是 STRM 生成任务类型。
type StrmGenerationTaskType string

const (
	StrmGenerationTaskTypeFile          StrmGenerationTaskType = "file"
	StrmGenerationTaskTypeDirectoryScan StrmGenerationTaskType = "directory_scan"
	StrmGenerationTaskTypeBatchFiles    StrmGenerationTaskType = "batch_files"
)

// StrmGenerationStatus 是 STRM 生成任务状态。
type StrmGenerationStatus string

const (
	StrmGenerationStatusPending         StrmGenerationStatus = "pending"
	StrmGenerationStatusRunning         StrmGenerationStatus = "running"
	StrmGenerationStatusFinalizing      StrmGenerationStatus = "finalizing"
	StrmGenerationStatusWaitingChildren StrmGenerationStatus = "waiting_children"
	StrmGenerationStatusCompleted       StrmGenerationStatus = "completed"
	StrmGenerationStatusFailed          StrmGenerationStatus = "failed"
	StrmGenerationStatusCancelled       StrmGenerationStatus = "cancelled"
)

// StrmGenerationTask 保存上传完成或 Webhook 触发的 STRM 生成任务。
type StrmGenerationTask struct {
	BaseModel
	Source       StrmGenerationSource   `json:"source" gorm:"size:32;index"`
	TaskType     StrmGenerationTaskType `json:"task_type" gorm:"size:32;index"`
	ParentTaskId uint                   `json:"parent_task_id" gorm:"index"`
	UploadTaskId uint                   `json:"upload_task_id" gorm:"index"`
	SyncPathId   uint                   `json:"sync_path_id" gorm:"index"`
	AccountId    uint                   `json:"account_id" gorm:"index"`

	DownloadMeta bool `json:"download_meta" gorm:"default:false"`
	RefreshEmby  bool `json:"refresh_emby" gorm:"default:false"`

	FileId   string `json:"file_id" gorm:"size:128;index"`
	ParentId string `json:"parent_id" gorm:"size:128"`
	PickCode string `json:"pick_code" gorm:"size:128;index"`
	Path     string `json:"path" gorm:"type:text;size:1024"`
	FileName string `json:"file_name" gorm:"size:512"`
	FileSize int64  `json:"file_size"`
	Sha1     string `json:"sha1" gorm:"size:64"`
	Mtime    int64  `json:"mtime"`

	DirectoryId   string `json:"directory_id" gorm:"size:128;index"`
	DirectoryPath string `json:"directory_path" gorm:"type:text;size:1024"`
	TotalItems    int    `json:"total_items"`
	AcceptedItems int    `json:"accepted_items"`
	FailedItems   int    `json:"failed_items"`
	ChangedItems  int    `json:"changed_items"`
	NewMetaItems  int    `json:"new_meta_items"`

	RefreshTargetsStr string `json:"-" gorm:"type:text;default:'[]'"`
	RefreshSubmitted  bool   `json:"refresh_submitted" gorm:"default:false"`

	Status        StrmGenerationStatus `json:"status" gorm:"size:32;index"`
	RequestHash   string               `json:"request_hash" gorm:"size:255;uniqueIndex:idx_strm_generation_request_hash,where:request_hash <> ''"`
	RetryCount    int                  `json:"retry_count" gorm:"default:0"`
	LastRetryTime int64                `json:"last_retry_time"`
	LastError     string               `json:"last_error" gorm:"type:text"`
}

// BuildStrmRequestHash 使用固定长度摘要构造 STRM 幂等请求哈希。
func BuildStrmRequestHash(kind string, fields ...string) string {
	var builder strings.Builder
	writeStrmRequestHashField(&builder, kind)
	for _, field := range fields {
		writeStrmRequestHashField(&builder, field)
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return kind + ":v2:" + hex.EncodeToString(sum[:])
}

func writeStrmRequestHashField(builder *strings.Builder, value string) {
	fmt.Fprintf(builder, "%d:%s;", len(value), value)
}

// EnqueueStrmGenerationTask 创建 STRM 生成任务，request_hash 非空时做幂等去重。
func EnqueueStrmGenerationTask(task *StrmGenerationTask) (*StrmGenerationTask, error) {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()
	return EnqueueStrmGenerationTaskWithDB(db.Db, task)
}

// EnqueueStrmGenerationTaskWithLegacyHashes 创建 STRM 生成任务，并允许复用旧格式活跃 request_hash。
func EnqueueStrmGenerationTaskWithLegacyHashes(task *StrmGenerationTask, legacyRequestHashes ...string) (*StrmGenerationTask, error) {
	releaseAdmission, err := taskgate.Admit()
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()
	return EnqueueStrmGenerationTaskWithDBAndLegacyHashes(db.Db, task, legacyRequestHashes...)
}

// EnqueueStrmGenerationTaskWithDB 在指定事务中创建 STRM 生成任务，request_hash 非空时做幂等去重。
func EnqueueStrmGenerationTaskWithDB(tx *gorm.DB, task *StrmGenerationTask) (*StrmGenerationTask, error) {
	return EnqueueStrmGenerationTaskWithDBAndLegacyHashes(tx, task)
}

// EnqueueStrmGenerationTaskWithDBAndLegacyHashes 在指定事务中创建 STRM 任务，并允许复用旧格式活跃 request_hash。
func EnqueueStrmGenerationTaskWithDBAndLegacyHashes(tx *gorm.DB, task *StrmGenerationTask, legacyRequestHashes ...string) (*StrmGenerationTask, error) {
	if taskgate.IsBlocked() {
		return nil, taskgate.ErrTaskAdmissionBlocked
	}
	if tx == nil {
		return nil, errors.New("数据库连接为空")
	}
	if task == nil {
		return nil, errors.New("STRM 生成任务为空")
	}
	if task.Status == "" {
		task.Status = StrmGenerationStatusPending
	}
	if task.TaskType == "" {
		task.TaskType = StrmGenerationTaskTypeFile
	}
	if task.RequestHash == "" {
		if err := tx.Create(task).Error; err != nil {
			return nil, err
		}
		return task, nil
	}
	for attempts := 0; attempts < 4; attempts++ {
		existing, found, err := findStrmGenerationTaskByRequestHash(tx, task.RequestHash)
		if err != nil {
			return nil, err
		}
		if found {
			if isActiveStrmGenerationStatus(existing.Status) {
				return existing, nil
			}
			archived, err := archiveStrmGenerationTaskRequestHash(tx, existing.ID, task.RequestHash)
			if err != nil {
				return nil, err
			}
			if archived {
				continue
			}
			continue
		}
		for _, legacyHash := range legacyRequestHashes {
			if legacyHash == "" || legacyHash == task.RequestHash {
				continue
			}
			existing, found, err := findStrmGenerationTaskByRequestHash(tx, legacyHash)
			if err != nil {
				return nil, err
			}
			if found && isActiveStrmGenerationStatus(existing.Status) {
				return existing, nil
			}
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(task)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected > 0 {
			return task, nil
		}
		task.ID = 0
		existing, found, err = findStrmGenerationTaskByRequestHash(tx, task.RequestHash)
		if err != nil {
			return nil, err
		}
		if found && isActiveStrmGenerationStatus(existing.Status) {
			return existing, nil
		}
		if found {
			archived, err := archiveStrmGenerationTaskRequestHash(tx, existing.ID, task.RequestHash)
			if err != nil {
				return nil, err
			}
			if archived {
				continue
			}
		}
	}
	return nil, errors.New("STRM 生成任务 request_hash 并发入队失败")
}

func findStrmGenerationTaskByRequestHash(tx *gorm.DB, requestHash string) (*StrmGenerationTask, bool, error) {
	var existing StrmGenerationTask
	err := tx.Where("request_hash = ?", requestHash).First(&existing).Error
	if err == nil {
		return &existing, true, nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	return nil, false, err
}

func archiveStrmGenerationTaskRequestHash(tx *gorm.DB, taskID uint, requestHash string) (bool, error) {
	if tx == nil {
		return false, errors.New("数据库连接为空")
	}
	if taskID == 0 || requestHash == "" {
		return false, nil
	}
	archivedHash := archivedStrmGenerationRequestHash(requestHash, taskID)
	result := tx.Model(&StrmGenerationTask{}).
		Where("id = ? AND request_hash = ? AND status NOT IN ?", taskID, requestHash, []StrmGenerationStatus{
			StrmGenerationStatusPending,
			StrmGenerationStatusRunning,
			StrmGenerationStatusFinalizing,
			StrmGenerationStatusWaitingChildren,
		}).
		Update("request_hash", archivedHash)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// StrmGenerationParentProgress 描述父任务需要累计的子任务结果。
type StrmGenerationParentProgress struct {
	Accepted       int
	Failed         int
	Changed        int
	NewMeta        int
	RefreshTargets []EmbyRefreshTarget
}

// GetRefreshTargets 返回父任务已累计的 Emby 刷新目标。
func (task *StrmGenerationTask) GetRefreshTargets() []EmbyRefreshTarget {
	var targets []EmbyRefreshTarget
	if task == nil || task.RefreshTargetsStr == "" {
		return targets
	}
	if err := json.Unmarshal([]byte(task.RefreshTargetsStr), &targets); err != nil {
		helpers.AppLogger.Warnf("解析 STRM 生成任务刷新目标失败：%v", err)
		return []EmbyRefreshTarget{}
	}
	return normalizeEmbyRefreshTargets(targets)
}

// SetRefreshTargets 保存父任务已累计的 Emby 刷新目标。
func (task *StrmGenerationTask) SetRefreshTargets(targets []EmbyRefreshTarget) {
	data, err := json.Marshal(normalizeEmbyRefreshTargets(targets))
	if err != nil {
		task.RefreshTargetsStr = "[]"
		return
	}
	task.RefreshTargetsStr = string(data)
}

// MergeRefreshTargets 合并并去重父任务 Emby 刷新目标。
func (task *StrmGenerationTask) MergeRefreshTargets(targets []EmbyRefreshTarget) {
	if task == nil || len(targets) == 0 {
		return
	}
	task.SetRefreshTargets(append(task.GetRefreshTargets(), targets...))
}

// IsReadyToSubmitRefresh 判断父任务是否已经可以提交一次批量 Emby 刷新。
func (task *StrmGenerationTask) IsReadyToSubmitRefresh() bool {
	if task == nil || !task.RefreshEmby || task.RefreshSubmitted {
		return false
	}
	if task.Status != StrmGenerationStatusCompleted {
		return false
	}
	if task.FailedItems > 0 {
		return false
	}
	if task.ChangedItems == 0 && task.NewMetaItems == 0 {
		return false
	}
	if task.TotalItems == 0 {
		return false
	}
	return task.AcceptedItems+task.FailedItems >= task.TotalItems
}

func normalizeEmbyRefreshTargets(targets []EmbyRefreshTarget) []EmbyRefreshTarget {
	libraries := make(map[string]EmbyRefreshTarget)
	for _, target := range targets {
		if target.TargetType != EmbyRefreshTargetTypeLibrary {
			continue
		}
		key := refreshTargetLibraryKey(target)
		if existing, ok := libraries[key]; ok {
			libraries[key] = mergeRefreshLibraryTarget(existing, target)
			continue
		}
		libraries[key] = target
	}

	items := make(map[string]EmbyRefreshTarget)
	for _, target := range targets {
		if target.TargetType == "" {
			target.TargetType = EmbyRefreshTargetTypeLibrary
		}
		if target.TargetType == EmbyRefreshTargetTypeLibrary {
			continue
		}
		if target.TargetType != EmbyRefreshTargetTypeItem || target.ItemID == "" {
			continue
		}
		if _, covered := libraries[refreshTargetLibraryKey(target)]; covered {
			continue
		}
		key := string(target.TargetType) + ":" + target.ItemID
		if existing, ok := items[key]; ok {
			items[key] = mergeRefreshItemTarget(existing, target)
			continue
		}
		items[key] = target
	}

	result := make([]EmbyRefreshTarget, 0, len(libraries)+len(items))
	for _, target := range libraries {
		result = append(result, target)
	}
	for _, target := range items {
		result = append(result, target)
	}
	sort.Slice(result, func(i, j int) bool {
		return refreshTargetSortKey(result[i]) < refreshTargetSortKey(result[j])
	})
	return result
}

func refreshTargetLibraryKey(target EmbyRefreshTarget) string {
	if target.FallbackLibraryId != "" {
		return target.FallbackLibraryId
	}
	if target.SyncPathID > 0 {
		return fmt.Sprintf("sync_path:%d", target.SyncPathID)
	}
	return "__default_library__"
}

func refreshTargetSortKey(target EmbyRefreshTarget) string {
	switch target.TargetType {
	case EmbyRefreshTargetTypeLibrary:
		return "0:" + refreshTargetLibraryKey(target)
	default:
		return "1:" + target.FallbackLibraryId + ":" + target.ItemID
	}
}

func mergeRefreshLibraryTarget(left EmbyRefreshTarget, right EmbyRefreshTarget) EmbyRefreshTarget {
	if left.FallbackLibraryId == "" {
		left.FallbackLibraryId = right.FallbackLibraryId
	}
	if left.FallbackLibraryName == "" {
		left.FallbackLibraryName = right.FallbackLibraryName
	}
	if left.SyncPathID == 0 {
		left.SyncPathID = right.SyncPathID
	}
	return left
}

func mergeRefreshItemTarget(left EmbyRefreshTarget, right EmbyRefreshTarget) EmbyRefreshTarget {
	if left.ItemName == "" {
		left.ItemName = right.ItemName
	}
	if left.ItemType == "" {
		left.ItemType = right.ItemType
	}
	left.Recursive = left.Recursive || right.Recursive
	if left.SyncPathID == 0 {
		left.SyncPathID = right.SyncPathID
	}
	if left.FallbackLibraryId == "" {
		left.FallbackLibraryId = right.FallbackLibraryId
	}
	if left.FallbackLibraryName == "" {
		left.FallbackLibraryName = right.FallbackLibraryName
	}
	return left
}

func isActiveStrmGenerationStatus(status StrmGenerationStatus) bool {
	return status == StrmGenerationStatusPending ||
		status == StrmGenerationStatusRunning ||
		status == StrmGenerationStatusFinalizing ||
		status == StrmGenerationStatusWaitingChildren
}

func archivedStrmGenerationRequestHash(requestHash string, taskID uint) string {
	const maxRequestHashLength = 255
	suffix := fmt.Sprintf(":history:%d", taskID)
	if len(requestHash)+len(suffix) <= maxRequestHashLength {
		return requestHash + suffix
	}
	keep := maxRequestHashLength - len(suffix)
	if keep <= 0 {
		return suffix[:maxRequestHashLength]
	}
	return requestHash[:keep] + suffix
}

// MarkFailed 标记 STRM 生成任务失败并累计重试次数。
func (task *StrmGenerationTask) MarkFailed(message string) error {
	if task == nil {
		return errors.New("STRM 生成任务为空")
	}
	task.Status = StrmGenerationStatusFailed
	task.RetryCount++
	task.LastRetryTime = time.Now().Unix()
	task.LastError = message
	return db.Db.Save(task).Error
}

// MarkStrmGenerationChildFailed 原子标记子任务失败并累计父任务失败进度。
func MarkStrmGenerationChildFailed(childTaskID uint, parentTaskID uint, message string) (*StrmGenerationTask, error) {
	if childTaskID == 0 {
		return nil, errors.New("STRM 子任务为空")
	}
	if parentTaskID == 0 {
		return nil, errors.New("STRM 父任务为空")
	}
	now := time.Now().Unix()
	var parent *StrmGenerationTask
	err := db.Db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&StrmGenerationTask{}).
			Where("id = ? AND status = ?", childTaskID, StrmGenerationStatusRunning).
			Updates(map[string]interface{}{
				"status":          StrmGenerationStatusFailed,
				"retry_count":     gorm.Expr("retry_count + ?", 1),
				"last_retry_time": now,
				"last_error":      message,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("STRM 子任务状态不可标记失败")
		}
		var err error
		parent, err = updateStrmGenerationParentProgressWithDB(tx, parentTaskID, StrmGenerationParentProgress{Failed: 1})
		return err
	})
	if err != nil {
		return nil, err
	}
	return parent, nil
}

// MarkRunning 标记 STRM 生成任务开始执行。
func (task *StrmGenerationTask) MarkRunning() error {
	if task == nil {
		return errors.New("STRM 生成任务为空")
	}
	result := db.Db.Model(task).
		Where("id = ? AND status = ?", task.ID, StrmGenerationStatusPending).
		Updates(map[string]interface{}{
			"status":     StrmGenerationStatusRunning,
			"last_error": "",
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errors.New("STRM 生成任务状态不可执行")
	}
	task.Status = StrmGenerationStatusRunning
	task.LastError = ""
	return nil
}

// MarkCompleted 标记 STRM 生成任务执行完成。
func (task *StrmGenerationTask) MarkCompleted() error {
	if task == nil {
		return errors.New("STRM 生成任务为空")
	}
	task.Status = StrmGenerationStatusCompleted
	task.LastError = ""
	return db.Db.Save(task).Error
}

// MarkDirectoryScanExpanded 标记目录扫描任务已展开，等待子任务汇总。
func (task *StrmGenerationTask) MarkDirectoryScanExpanded(totalItems int) error {
	if task == nil {
		return errors.New("STRM 生成任务为空")
	}
	if totalItems < 0 {
		totalItems = 0
	}
	task.TotalItems = totalItems
	task.AcceptedItems = 0
	task.FailedItems = 0
	task.ChangedItems = 0
	task.NewMetaItems = 0
	task.LastError = ""
	if totalItems == 0 {
		task.Status = StrmGenerationStatusCompleted
	} else {
		task.Status = StrmGenerationStatusWaitingChildren
	}
	return db.Db.Save(task).Error
}

// GetPendingStrmGenerationTasks 按创建顺序获取待执行或待收尾 STRM 生成任务。
func GetPendingStrmGenerationTasks(limit int) ([]*StrmGenerationTask, error) {
	if limit <= 0 {
		limit = 10
	}
	var tasks []*StrmGenerationTask
	err := db.Db.Where("status IN ?", []StrmGenerationStatus{
		StrmGenerationStatusPending,
		StrmGenerationStatusFinalizing,
	}).
		Order("id ASC").
		Limit(limit).
		Find(&tasks).Error
	return tasks, err
}

// ResetRunningStrmGenerationTasks 将进程退出前的运行中任务恢复为待处理。
// CountRunningStrmGenerationTasks 统计仍在执行的 STRM 生成任务。
// 备份和恢复以它判定生成 worker 是否真正静止，停止请求本身不是静止证明。
func CountRunningStrmGenerationTasks() int {
	var running int64
	if err := db.Db.Model(&StrmGenerationTask{}).
		Where("status = ?", StrmGenerationStatusRunning).
		Count(&running).Error; err != nil {
		helpers.AppLogger.Warnf("统计执行中的 STRM 生成任务失败：%v", err)
		return 0
	}
	return int(running)
}

func ResetRunningStrmGenerationTasks() error {
	return db.Db.Model(&StrmGenerationTask{}).
		Where("status = ?", StrmGenerationStatusRunning).
		Update("status", StrmGenerationStatusPending).Error
}

// IncrementStrmGenerationDirectoryStats 累加目录扫描任务统计。
func IncrementStrmGenerationDirectoryStats(parentTaskId uint, accepted int, failed int) error {
	_, err := UpdateStrmGenerationParentProgress(parentTaskId, StrmGenerationParentProgress{
		Accepted: accepted,
		Failed:   failed,
	})
	return err
}

// UpdateStrmGenerationParentProgress 以事务方式累计父任务进度和刷新目标。
func UpdateStrmGenerationParentProgress(parentTaskId uint, progress StrmGenerationParentProgress) (*StrmGenerationTask, error) {
	if parentTaskId == 0 {
		return nil, nil
	}
	var updated *StrmGenerationTask
	err := db.Db.Transaction(func(tx *gorm.DB) error {
		parent, err := updateStrmGenerationParentProgressWithDB(tx, parentTaskId, progress)
		if err != nil {
			return err
		}
		updated = parent
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// MarkStrmGenerationChildFinalizing 累计父任务进度，并把成功子任务标记为收尾中。
func MarkStrmGenerationChildFinalizing(childTaskId uint, parentTaskId uint, progress StrmGenerationParentProgress) (*StrmGenerationTask, error) {
	if childTaskId == 0 {
		return nil, errors.New("STRM 子任务为空")
	}
	if parentTaskId == 0 {
		return nil, nil
	}
	var updated *StrmGenerationTask
	err := db.Db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&StrmGenerationTask{}).
			Where("id = ? AND status = ?", childTaskId, StrmGenerationStatusRunning).
			Updates(map[string]interface{}{
				"status":     StrmGenerationStatusFinalizing,
				"last_error": "",
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return errors.New("STRM 子任务状态不可收尾")
		}

		parent, err := updateStrmGenerationParentProgressWithDB(tx, parentTaskId, progress)
		if err != nil {
			return err
		}
		updated = parent
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func updateStrmGenerationParentProgressWithDB(tx *gorm.DB, parentTaskId uint, progress StrmGenerationParentProgress) (*StrmGenerationTask, error) {
	if err := tx.Model(&StrmGenerationTask{}).
		Where("id = ?", parentTaskId).
		Updates(map[string]interface{}{
			"accepted_items": gorm.Expr("accepted_items + ?", progress.Accepted),
			"failed_items":   gorm.Expr("failed_items + ?", progress.Failed),
			"changed_items":  gorm.Expr("changed_items + ?", progress.Changed),
			"new_meta_items": gorm.Expr("new_meta_items + ?", progress.NewMeta),
		}).Error; err != nil {
		return nil, err
	}

	var parent StrmGenerationTask
	if err := tx.First(&parent, parentTaskId).Error; err != nil {
		return nil, err
	}
	processedItems := parent.AcceptedItems + parent.FailedItems
	hasFixedTotalItems := parent.TotalItems > 0
	updates := map[string]interface{}{}
	if parent.TotalItems < processedItems {
		parent.TotalItems = processedItems
		updates["total_items"] = parent.TotalItems
	}
	if hasFixedTotalItems &&
		processedItems >= parent.TotalItems &&
		parent.Status == StrmGenerationStatusWaitingChildren {
		if parent.FailedItems > 0 {
			parent.Status = StrmGenerationStatusFailed
			updates["status"] = parent.Status
		} else {
			parent.Status = StrmGenerationStatusCompleted
			parent.LastError = ""
			updates["status"] = parent.Status
			updates["last_error"] = parent.LastError
		}
	}
	if len(progress.RefreshTargets) > 0 {
		parent.MergeRefreshTargets(progress.RefreshTargets)
		updates["refresh_targets_str"] = parent.RefreshTargetsStr
	}
	if len(updates) > 0 {
		if err := tx.Model(&StrmGenerationTask{}).
			Where("id = ?", parentTaskId).
			Updates(updates).Error; err != nil {
			return nil, err
		}
		if err := tx.First(&parent, parentTaskId).Error; err != nil {
			return nil, err
		}
	}
	return &parent, nil
}

// GetStrmGenerationTaskByID 按 ID 读取 STRM 生成任务。
func GetStrmGenerationTaskByID(taskID uint) (*StrmGenerationTask, error) {
	if taskID == 0 {
		return nil, nil
	}
	var task StrmGenerationTask
	if err := db.Db.First(&task, taskID).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// MarkStrmGenerationRefreshSubmitted 标记父任务刷新目标已经提交。
func MarkStrmGenerationRefreshSubmitted(parentTaskId uint) error {
	if parentTaskId == 0 {
		return nil
	}
	return db.Db.Model(&StrmGenerationTask{}).
		Where("id = ?", parentTaskId).
		Update("refresh_submitted", true).Error
}
