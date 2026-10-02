package models

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/realtime"

	"gorm.io/gorm"
)

type SyncStatus int

const (
	SyncStatusPending    SyncStatus = iota // 待处理
	SyncStatusInProgress                   // 进行中
	SyncStatusCompleted                    // 已完成
	SyncStatusFailed                       // 失败
	SyncStatusPartial                      // 部分完成
	SyncStatusIncomplete                   // 扫描不完整
	SyncStatusCancelled                    // 已取消
)

var SyncStatusText map[SyncStatus]string = map[SyncStatus]string{
	SyncStatusPending:    "待处理",
	SyncStatusInProgress: "进行中",
	SyncStatusCompleted:  "已完成",
	SyncStatusFailed:     "失败",
	SyncStatusPartial:    "部分完成",
	SyncStatusIncomplete: "扫描不完整",
	SyncStatusCancelled:  "已取消",
}

var (
	errSyncRecordNotFound     = errors.New("同步记录不存在")
	errSyncRecordNotDeletable = errors.New("同步记录未完成，不能删除")
)

// ErrSyncLedgerRecordDeleted 表示后台状态写入时已确认历史记录被删除。
var ErrSyncLedgerRecordDeleted = errors.New("同步文件记录更新对应的同步历史已删除")

type SyncSubStatus int

const (
	SyncSubStatusNone                 SyncSubStatus = iota // 无子状态
	SyncSubStatusProcessNetFileList                        // 正在处理网盘文件
	SyncSubStatusProcessLocalFileList                      // 正在处理本地文件列表
)

var SyncSubStatusText map[SyncSubStatus]string = map[SyncSubStatus]string{
	SyncSubStatusNone:                 "无子状态",
	SyncSubStatusProcessNetFileList:   "正在处理网盘文件",
	SyncSubStatusProcessLocalFileList: "正在处理本地文件列表",
}

// 同步任务
type Sync struct {
	BaseModel
	LedgerStatus      *realtime.SyncLedgerStatus `json:"ledger_status"`
	LedgerFinishedAt  *int64                     `json:"ledger_finished_at"`
	LedgerError       string                     `json:"ledger_error"`
	ScanResult        *realtime.SyncScanResult   `json:"scan_result" gorm:"serializer:json;type:text"`
	SyncPathId        uint                       `json:"sync_path_id"`
	Status            SyncStatus                 `json:"status"`
	SubStatus         SyncSubStatus              `json:"sub_status"`  // 子状态，记录当前同步的子任务状态
	FileOffset        int                        `json:"file_offset"` // 文件偏移量，用于继续任务时的定位
	Total             int                        `json:"total"`
	FinishAt          int64                      `json:"finish_at"`
	NewStrm           int                        `json:"new_strm"`
	NewMeta           int                        `json:"new_meta"`
	NewUpload         int                        `json:"new_upload" gorm:"default:0"` // 新增上传的文件数量
	NetFileStartAt    int64                      `json:"net_file_start_at"`           // 开始处理网盘文件时间
	NetFileFinishAt   int64                      `json:"net_file_finish_at"`          // 处理网盘文件完成时间
	LocalFileStartAt  int64                      `json:"local_file_start_at"`         // 开始处理本地文件列表时间
	LocalFileFinishAt int64                      `json:"local_file_finish_at"`        // 处理本地文件列表完成时间
	LocalPath         string                     `json:"local_path"`                  // 本地同步路径
	RemotePath        string                     `json:"remote_path"`                 // 远程同步路径
	BaseCid           string                     `json:"base_cid"`                    // 基础 CID，用于标识同步的根目录
	FailReason        string                     `json:"fail_reason"`                 // 失败原因
	IsFullSync        bool                       `json:"is_full_sync"`                // 是否全量同步
	SyncPath          *SyncPath                  `gorm:"-" json:"-"`                  // 同步路径实例
	Logger            *helpers.QLogger           `gorm:"-" json:"-"`                  // 日志句柄，不参与数据读写
}

// SyncLogRelativePath 返回前端日志接口使用的同步任务日志相对路径。
func SyncLogRelativePath(syncID uint) string {
	return filepath.ToSlash(filepath.Join(helpers.SyncLogRelativeDir(), helpers.SyncLogFileName(syncID)))
}

// SyncLogFullPath 返回同步任务日志完整路径。
func SyncLogFullPath(syncID uint) string {
	return filepath.Join(helpers.ConfigDir, helpers.SyncLogDir(), helpers.SyncLogFileName(syncID))
}

// LegacySyncLogFullPath 返回历史同步任务日志完整路径。
func LegacySyncLogFullPath(syncID uint) string {
	return filepath.Join(helpers.ConfigDir, "logs", helpers.LegacySyncLogRelativeDir(), helpers.SyncLogFileName(syncID))
}

// LegacySyncLogRelativePath 返回历史同步任务日志相对路径。
func LegacySyncLogRelativePath(syncID uint) string {
	return filepath.ToSlash(filepath.Join(helpers.LegacySyncLogRelativeDir(), helpers.SyncLogFileName(syncID)))
}

// ExistingSyncLogPath 返回当前日志路径；当前路径不存在时兼容读取历史 logs/libs 路径。
func ExistingSyncLogPath(syncID uint) (string, string) {
	logFile := SyncLogFullPath(syncID)
	if _, err := os.Stat(logFile); err == nil {
		return logFile, SyncLogRelativePath(syncID)
	}
	legacyLogFile := LegacySyncLogFullPath(syncID)
	if _, err := os.Stat(legacyLogFile); err == nil {
		return legacyLogFile, LegacySyncLogRelativePath(syncID)
	}
	return logFile, SyncLogRelativePath(syncID)
}

// ExistingSyncLogFullPath 返回实际存在的同步任务日志完整路径。
func ExistingSyncLogFullPath(syncID uint) string {
	fullPath, _ := ExistingSyncLogPath(syncID)
	return fullPath
}

// ExistingSyncLogRelativePath 返回实际存在的同步任务日志相对路径。
func ExistingSyncLogRelativePath(syncID uint) string {
	_, relativePath := ExistingSyncLogPath(syncID)
	return relativePath
}

// SyncTaskEventPayload 生成同步任务结构化事件数据。
func (s *Sync) SyncTaskEventPayload() realtime.SyncTaskEventPayload {
	return realtime.SyncTaskEventPayload{
		LedgerStatus:      s.LedgerStatus,
		LedgerFinishedAt:  s.LedgerFinishedAt,
		LedgerError:       s.LedgerError,
		ScanResult:        s.ScanResult,
		SyncID:            s.ID,
		SyncPathID:        s.SyncPathId,
		Status:            int(s.Status),
		SubStatus:         int(s.SubStatus),
		Total:             s.Total,
		NewStrm:           s.NewStrm,
		NewMeta:           s.NewMeta,
		NewUpload:         s.NewUpload,
		FinishAt:          s.FinishAt,
		NetFileStartAt:    s.NetFileStartAt,
		NetFileFinishAt:   s.NetFileFinishAt,
		LocalFileStartAt:  s.LocalFileStartAt,
		LocalFileFinishAt: s.LocalFileFinishAt,
		LogPath:           SyncLogRelativePath(s.ID),
		CreatedAt:         s.CreatedAt,
		UpdatedAt:         s.UpdatedAt,
		LocalPath:         s.LocalPath,
		RemotePath:        s.RemotePath,
		FailReason:        s.FailReason,
	}
}

func (s *Sync) broadcastSyncTaskEvent(eventType string) {
	if s == nil || s.ID == 0 {
		return
	}
	realtime.BroadcastSyncTaskEvent(eventType, s.SyncTaskEventPayload())
}

// Complete 持久化生成完成结果，保留直接调用方的兼容入口。
func (s *Sync) Complete(ctx context.Context, sourceType SourceType) error {
	return s.FinishGeneration(ctx, sourceType, SyncStatusCompleted, "", false)
}

// FinishGeneration 将前台结果和成功水位原子提交，再发布事件及原条件通知。
func (s *Sync) FinishGeneration(ctx context.Context, sourceType SourceType, status SyncStatus, reason string, advanceWatermark bool) error {
	if err := s.SaveGeneration(ctx, status, reason, advanceWatermark); err != nil {
		return err
	}
	s.NotifyGeneration(ctx, sourceType)
	if status == SyncStatusCompleted && s.Logger != nil {
		s.Logger.Close()
	}
	return nil
}

// SaveGeneration 只提交生成结果及成功水位，通知和日志生命周期由执行器拥有。
func (s *Sync) SaveGeneration(ctx context.Context, status SyncStatus, reason string, advanceWatermark bool) error {
	release, err := syncRecordEvents.acquire(ctx, s.ID)
	if err != nil {
		return err
	}
	defer release()
	if !status.IsTerminal() {
		return fmt.Errorf("无效的生成终态：%d", status)
	}
	completed := *s
	if completed.LedgerStatus == nil {
		ledgerStatus := realtime.SyncLedgerNotRequired
		completed.LedgerStatus = &ledgerStatus
	}
	if *completed.LedgerStatus != realtime.SyncLedgerNotRequired && *completed.LedgerStatus != realtime.SyncLedgerPending {
		return fmt.Errorf("无效的初始后台状态：%s", *completed.LedgerStatus)
	}
	completed.LedgerFinishedAt, completed.LedgerError = nil, ""
	reason = helpers.RedactSensitiveLog(reason)
	completed.Status, completed.FailReason = status, reason
	completed.FinishAt = time.Now().Unix()
	completed.LocalFileFinishAt = completed.FinishAt
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&completed).Where("id = ? AND status IN (?, ?)", s.ID, SyncStatusPending, SyncStatusInProgress).
			Select("status", "fail_reason", "finish_at", "local_file_finish_at", "total", "new_strm", "new_meta", "new_upload", "is_full_sync", "scan_result", "ledger_status", "ledger_finished_at", "ledger_error", "updated_at").Updates(&completed)
		if err := syncUpdateError(result); err != nil {
			return err
		}
		if advanceWatermark && status == SyncStatusCompleted {
			changes := map[string]any{"last_sync_at": completed.FinishAt}
			if completed.IsFullSync {
				changes["is_full_sync"] = false
			}
			result = tx.Model(&SyncPath{}).Where("id = ?", s.SyncPathId).Updates(changes)
			if err := syncUpdateError(result); err != nil {
				return fmt.Errorf("保存同步成功水位失败：%w", err)
			}
		}
		return nil
	})
	if err != nil {
		if s.Logger != nil {
			s.Logger.Errorf("保存同步结果失败：%v", err)
		}
		return err
	}
	s.Status, s.FailReason, s.FinishAt, s.LocalFileFinishAt, s.UpdatedAt = completed.Status, completed.FailReason, completed.FinishAt, completed.LocalFileFinishAt, completed.UpdatedAt
	s.LedgerStatus, s.LedgerFinishedAt, s.LedgerError = completed.LedgerStatus, completed.LedgerFinishedAt, completed.LedgerError
	s.broadcastSyncTaskEvent(realtime.EventSyncTaskUpdated)
	if s.Logger != nil {
		s.Logger.Infof("同步任务结果：%d，%s", s.ID, SyncStatusText[status])
	}
	return nil
}

// NotifyGeneration 按既有条件发送已保存的生成结果，不等待或修改账本状态。
func (s *Sync) NotifyGeneration(ctx context.Context, sourceType SourceType) {
	if !s.Status.IsTerminal() || s.FinishAt <= 0 {
		return
	}
	status, reason := s.Status, s.FailReason
	var notif *Notification
	if status == SyncStatusCompleted {
		if s.NewUpload > 0 || s.NewMeta > 0 || s.NewStrm > 0 {
			notif = &Notification{Type: SyncFinished, Title: fmt.Sprintf("✅ %s %s STRM 生成完成", sourceType.String(), s.RemotePath), Content: fmt.Sprintf("📊 生成耗时：%s，生成 STRM：%s，下载：%s，上传：%s\n⏰ 时间：%s", s.GetDuration(), helpers.IntToString(s.NewStrm), helpers.IntToString(s.NewMeta), helpers.IntToString(s.NewUpload), time.Now().Format("2006-01-02 15:04:05")), Timestamp: time.Now(), Priority: NormalPriority}
		}
	} else {
		notif = &Notification{Type: SyncError, Title: "❌ STRM 生成" + SyncStatusText[status], Content: fmt.Sprintf("🔍 %s\n⏰ 时间：%s", reason, time.Now().Format("2006-01-02 15:04:05")), Timestamp: time.Now(), Priority: HighPriority}
	}
	if notif != nil && notificationmanager.GlobalEnhancedNotificationManager != nil {
		if err := notificationmanager.GlobalEnhancedNotificationManager.SendNotification(ctx, notif); err != nil {
			helpers.AppLogger.Errorf("发送同步结果通知失败：%v", err)
		}
	}
}

// UpdateLedger 只推进尚在途的后台状态，以持久化的生成快照发布结果。
func (s *Sync) UpdateLedger(ctx context.Context, status realtime.SyncLedgerStatus, finishedAt *int64, reason string) error {
	release, err := syncRecordEvents.acquire(ctx, s.ID)
	if err != nil {
		return err
	}
	defer release()
	allowed := []realtime.SyncLedgerStatus{realtime.SyncLedgerPending, realtime.SyncLedgerRunning}
	switch status {
	case realtime.SyncLedgerRunning:
		if finishedAt != nil {
			return errors.New("同步文件记录仍在更新，不能记录结束时间")
		}
		allowed = allowed[:1]
	case realtime.SyncLedgerCompleted, realtime.SyncLedgerFailed:
		if finishedAt == nil || *finishedAt <= 0 {
			return errors.New("同步文件记录更新结束时必须记录实际结束时间")
		}
	case realtime.SyncLedgerInterrupted:
		if finishedAt != nil && *finishedAt <= 0 {
			return errors.New("同步文件记录更新结束时间必须为正值或未知")
		}
	default:
		return fmt.Errorf("无效的同步文件记录更新状态：%s", status)
	}
	if status == realtime.SyncLedgerRunning || status == realtime.SyncLedgerCompleted {
		reason = ""
	}
	updated := Sync{LedgerStatus: &status, LedgerFinishedAt: finishedAt, LedgerError: helpers.RedactSensitiveLog(reason)}
	var persisted Sync
	err = db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&updated).Where("id = ? AND ledger_status IN ? AND status BETWEEN ? AND ?", s.ID, allowed, SyncStatusCompleted, SyncStatusCancelled).
			Select("ledger_status", "ledger_finished_at", "ledger_error", "updated_at").Updates(&updated)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			var count int64
			if err := tx.Model(&Sync{}).Where("id = ?", s.ID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return ErrSyncLedgerRecordDeleted
			}
			return errors.New("同步文件记录更新状态已变更，不能继续更新状态")
		}
		return tx.First(&persisted, s.ID).Error
	})
	if err != nil {
		return err
	}
	s.LedgerStatus, s.LedgerFinishedAt, s.LedgerError, s.UpdatedAt = persisted.LedgerStatus, persisted.LedgerFinishedAt, persisted.LedgerError, persisted.UpdatedAt
	persisted.broadcastSyncTaskEvent(realtime.EventSyncTaskUpdated)
	return nil
}

// InterruptRunningSyncLedgers 仅在服务启动时标记未知退出的旧账本，不推断时间或触发重扫。
func InterruptRunningSyncLedgers(ctx context.Context) error {
	return db.Db.WithContext(ctx).Model(&Sync{}).
		Where("ledger_status IN ?", []realtime.SyncLedgerStatus{realtime.SyncLedgerPending, realtime.SyncLedgerRunning}).
		Updates(map[string]any{
			"ledger_status":      realtime.SyncLedgerInterrupted,
			"ledger_finished_at": nil,
			"ledger_error":       "服务中断，未记录同步文件记录更新的实际结束时间",
		}).Error
}

func (s *Sync) Failed(ctx context.Context, reason string) error {
	return s.FinishGeneration(ctx, "", SyncStatusFailed, reason, false)
}

// IsTerminal 判断前台生成结果是否已收敛。
func (status SyncStatus) IsTerminal() bool {
	return status >= SyncStatusCompleted && status <= SyncStatusCancelled
}

func (s *Sync) GetDuration() string {
	return helpers.FormatDuration(s.FinishAt - s.CreatedAt)
}

// UpdateProgress 更新同步任务运行中计数。
func (s *Sync) UpdateProgress(ctx context.Context, total, newStrm, newMeta, newUpload int) error {
	release, err := syncRecordEvents.acquire(ctx, s.ID)
	if err != nil {
		return err
	}
	defer release()
	progress := Sync{
		Total:     total,
		NewStrm:   newStrm,
		NewMeta:   newMeta,
		NewUpload: newUpload,
	}
	result := db.Db.WithContext(ctx).Model(&progress).Where("id = ?", s.ID).
		Select("total", "new_strm", "new_meta", "new_upload", "updated_at").Updates(&progress)
	if err := syncUpdateError(result); err != nil {
		s.Logger.Errorf("更新同步进度失败：%v", err)
		return err
	}
	s.Total, s.NewStrm, s.NewMeta, s.NewUpload = total, newStrm, newMeta, newUpload
	s.UpdatedAt = progress.UpdatedAt
	s.broadcastSyncTaskEvent(realtime.EventSyncTaskUpdated)
	return nil
}

// 修改同步任务的状态
func (s *Sync) UpdateStatus(ctx context.Context, status SyncStatus) error {
	release, err := syncRecordEvents.acquire(ctx, s.ID)
	if err != nil {
		return err
	}
	defer release()
	oldStatus := s.Status
	// 回写数据库
	updated := Sync{
		Status:            status,
		FailReason:        s.FailReason,
		FinishAt:          s.FinishAt,
		LocalFileFinishAt: s.LocalFileFinishAt,
		Total:             s.Total,
		NewStrm:           s.NewStrm,
		NewMeta:           s.NewMeta,
		NewUpload:         s.NewUpload,
	}
	result := db.Db.WithContext(ctx).Model(&updated).Where("id = ?", s.ID).
		Select("status", "fail_reason", "finish_at", "local_file_finish_at", "total", "new_strm", "new_meta", "new_upload", "updated_at").Updates(&updated)
	if err := syncUpdateError(result); err != nil {
		s.Logger.Errorf("更新同步状态失败：%v", err)
		return err
	}
	s.Status, s.UpdatedAt = status, updated.UpdatedAt
	s.broadcastSyncTaskEvent(realtime.EventSyncTaskUpdated)
	s.Logger.Infof("更新任务状态：%s => %s", SyncStatusText[oldStatus], SyncStatusText[status])
	return nil
}

func (s *Sync) UpdateSubStatus(ctx context.Context, subStatus SyncSubStatus) error {
	release, err := syncRecordEvents.acquire(ctx, s.ID)
	if err != nil {
		return err
	}
	defer release()
	oldSubStatus := s.SubStatus
	s.SubStatus = subStatus
	var updateSync Sync
	switch subStatus {
	case SyncSubStatusProcessNetFileList:
		// 开始查找文件列表，修改 NetFileFinishAt
		s.NetFileStartAt = time.Now().Unix()
		updateSync = Sync{
			SubStatus:      subStatus,
			NetFileStartAt: s.NetFileStartAt,
		}
	case SyncSubStatusProcessLocalFileList:
		// 开始对比文件，修改 FetchFileFinishAt
		s.NetFileFinishAt = time.Now().Unix()
		s.LocalFileStartAt = s.NetFileFinishAt
		updateSync = Sync{
			SubStatus:        subStatus,
			NetFileFinishAt:  s.NetFileFinishAt,
			LocalFileStartAt: s.LocalFileStartAt,
		}
	}
	result := db.Db.WithContext(ctx).Model(&updateSync).Where("id = ?", s.ID).Updates(&updateSync)
	if err := syncUpdateError(result); err != nil {
		s.Logger.Errorf("更新同步子状态失败：%v", err)
		return err
	}
	s.UpdatedAt = updateSync.UpdatedAt
	s.broadcastSyncTaskEvent(realtime.EventSyncTaskUpdated)
	s.Logger.Infof("更新任务子状态：%s => %s", SyncSubStatusText[oldSubStatus], SyncSubStatusText[subStatus])
	return nil
}

func syncUpdateError(result *gorm.DB) error {
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return errSyncRecordNotFound
	}
	return nil
}

func (s *Sync) InitLogger() {
	logDir := filepath.Join(helpers.ConfigDir, helpers.SyncLogDir())
	os.MkdirAll(logDir, 0755)
	logFileName := filepath.Join(helpers.SyncLogDir(), helpers.SyncLogFileName(s.ID))
	s.Logger = helpers.NewLogger(logFileName, true, false)
	s.Logger.Infof("创建同步日志文件：%s", logFileName)
}

// 获取所有同步记录
func GetSyncRecords(page, pageSize int) ([]*Sync, int64, error) {
	var count int64
	if err := db.Db.Model(&Sync{}).Count(&count).Error; err != nil {
		helpers.AppLogger.Errorf("统计同步记录总数失败：%v", err)
		return nil, 0, err
	}
	var syncs []*Sync
	if err := db.Db.Offset((page - 1) * pageSize).Limit(pageSize).Order("id DESC").Find(&syncs).Error; err != nil {
		helpers.AppLogger.Errorf("获取同步记录失败：%v", err)
		return nil, 0, err
	}
	return syncs, count, nil
}

func GetSyncByID(id uint) (*Sync, error) {
	sync := &Sync{}
	if err := db.Db.First(sync, id).Error; err != nil {
		return nil, err
	}
	if sync.SyncPathId == 0 {
		return sync, nil
	}
	// 根据 sync.SyncPathId 查询 SyncPath
	var syncPath SyncPath
	if err := db.Db.First(&syncPath, sync.SyncPathId).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return sync, nil
		}
		return nil, err
	}
	sync.SyncPath = &syncPath
	return sync, nil
}

// 获取最后一个同步任务
func GetLastSyncTask() *Sync {
	var sync Sync
	if err := db.Db.Order("id desc").First(&sync).Error; err != nil {
		helpers.AppLogger.Errorf("获取最后一个同步任务失败：%v", err)
		return nil
	}
	return &sync
}

func FailAllRunningSyncTasks() {

	// 查找所有运行中的同步任务
	var runningSyncs []Sync
	if err := db.Db.Where("status IN (?, ?)", SyncStatusPending, SyncStatusInProgress).Find(&runningSyncs).Error; err != nil {
		helpers.AppLogger.Errorf("查询运行中的同步任务失败：%v", err)
		return
	}

	if len(runningSyncs) == 0 {
		return
	}

	helpers.AppLogger.Infof("发现 %d 个运行中的同步任务，将设置为失败状态", len(runningSyncs))

	// 批量更新状态为失败
	if err := db.Db.Model(&Sync{}).Where("status IN (?, ?)", SyncStatusPending, SyncStatusInProgress).Updates(map[string]any{
		"status": SyncStatusFailed,
	}).Error; err != nil {
		helpers.AppLogger.Errorf("批量更新运行中的同步任务状态失败：%v", err)
		return
	}
	syncPathIDs := make([]uint, 0, len(runningSyncs))
	for _, record := range runningSyncs {
		syncPathIDs = append(syncPathIDs, record.SyncPathId)
	}
	// 保留既有前台遗留任务的启动恢复语义。
	if err := db.Db.Model(&SyncPath{}).Where("id IN ?", syncPathIDs).Update("is_full_sync", false).Error; err != nil {
		helpers.AppLogger.Errorf("批量更新同步路径状态失败：%v", err)
		return
	}
	helpers.AppLogger.Infof("成功将 %d 个运行中的同步任务设置为失败状态", len(runningSyncs))
}

// 删除已完成或失败的同步记录和相关文件
func DeleteSyncRecordById(id uint) error {
	return deleteSyncRecordById(id, true)
}

// 删除临时同步记录和相关文件
func DeleteTemporarySyncRecordById(id uint) error {
	return deleteSyncRecordById(id, false)
}

func deleteSyncRecordById(id uint, requireFinished bool) error {
	release, err := syncRecordEvents.acquire(context.Background(), id)
	if err != nil {
		return err
	}
	defer release()
	existing := &Sync{}
	if err := db.Db.First(existing, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w：%d", errSyncRecordNotFound, id)
		}
		return fmt.Errorf("查询同步记录失败：%w", err)
	}
	if requireFinished && !canDeleteSyncRecord(existing.Status) {
		return fmt.Errorf("%w：%s", errSyncRecordNotDeletable, syncStatusText(existing.Status))
	}
	if err := db.Db.Delete(&Sync{}, id).Error; err != nil {
		return fmt.Errorf("删除同步记录失败：%w", err)
	}

	payload := existing.SyncTaskEventPayload()
	payload.Deleted = true
	realtime.BroadcastSyncTaskEvent(realtime.EventSyncTaskDeleted, payload)

	// 删除相关的日志和同步结果文件
	removeSyncLogFile(SyncLogFullPath(id))
	removeSyncLogFile(LegacySyncLogFullPath(id))
	helpers.AppLogger.Infof("删除同步记录成功：%d", id)
	return nil

}

func canDeleteSyncRecord(status SyncStatus) bool {
	return status.IsTerminal()
}

func syncStatusText(status SyncStatus) string {
	if text, ok := SyncStatusText[status]; ok {
		return text
	}
	return fmt.Sprintf("未知状态(%d)", status)
}

func removeSyncLogFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		helpers.AppLogger.Warnf("删除同步日志文件失败：%s，%v", path, err)
	}
}

// 清除过期的同步记录和相关文件，默认保留最近 7 天的记录
func ClearExpiredSyncRecords(days int) {
	cutoff := time.Now().AddDate(0, 0, -days).Unix()
	var expiredSyncs []Sync
	if err := db.Db.Where("created_at < ?", cutoff).Find(&expiredSyncs).Error; err != nil {
		helpers.AppLogger.Errorf("查询过期的同步记录失败：%v", err)
		return
	}
	if len(expiredSyncs) == 0 {
		helpers.AppLogger.Infof("没有找到过期的同步记录")
		return
	}
	for _, sync := range expiredSyncs {
		if err := deleteSyncRecordById(sync.ID, false); err != nil {
			helpers.AppLogger.Errorf("删除过期的同步记录失败：%v", err)
		} else {
			helpers.AppLogger.Infof("删除过期的同步记录成功：%d", sync.ID)
		}
	}
}

func CreateSync(syncPathId uint, sourcePath, sourcePathId, targetPath string) *Sync {
	// 新建同步任务
	sync := &Sync{
		SyncPathId: syncPathId,
		Status:     SyncStatusPending,
		SubStatus:  SyncSubStatusNone,
		FileOffset: 0,
		Total:      0,
		NewStrm:    0,
		NewMeta:    0,
		Logger:     nil,
		LocalPath:  targetPath,
		RemotePath: sourcePath,
		BaseCid:    sourcePathId,
		FailReason: "",
		IsFullSync: false,
	}
	if err := db.Db.Save(sync).Error; err != nil {
		helpers.AppLogger.Errorf("创建同步任务失败：%v", err)
		return nil
	}
	// 创建后按同一 ID 重新读取，避免并发清理后发布迟到的创建事件。
	release, err := syncRecordEvents.acquire(context.Background(), sync.ID)
	if err != nil {
		return sync
	}
	defer release()
	var persisted Sync
	if err := db.Db.First(&persisted, sync.ID).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			helpers.AppLogger.Errorf("读取新建同步任务失败：%v", err)
		}
		return sync
	}
	persisted.broadcastSyncTaskEvent(realtime.EventSyncTaskCreated)
	return sync
}

// GetTodaySuccessfulFullSyncByPathID 只接受服务器当地自然日内已完成的全量任务。
func GetTodaySuccessfulFullSyncByPathID(ctx context.Context, syncPathID, currentSyncID uint, now time.Time) (*Sync, error) {
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	var record Sync
	err := db.Db.WithContext(ctx).Where("sync_path_id = ? AND id <> ? AND status = ? AND is_full_sync = ? AND finish_at >= ? AND finish_at < ?", syncPathID, currentSyncID, SyncStatusCompleted, true, start.Unix(), start.AddDate(0, 0, 1).Unix()).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}
