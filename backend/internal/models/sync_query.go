package models

import (
	"context"
	"encoding/json"

	"qmediasync/internal/db"
	"qmediasync/internal/realtime"
)

// SyncTaskView 只向展示层提供扫描摘要，完整结果仍由 Sync 持久化。
type SyncTaskView struct {
	Sync
	ScanResult *realtime.SyncScanSummary `json:"scan_result" gorm:"-"`
}

type syncViewRow struct {
	Sync
	RawScanResult *string `gorm:"column:scan_result"`
}

// GetSyncTaskState 不加载结果 JSON，供详情首屏、状态流和降级轮询使用。
func GetSyncTaskState(ctx context.Context, id uint) (*Sync, error) {
	var task Sync
	if err := db.Db.WithContext(ctx).Omit("scan_result").First(&task, id).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

// GetSyncScanSummary 只构造摘要；保留 JSON 存储时仍需读取并扫描全文。
func GetSyncScanSummary(ctx context.Context, id uint) (*realtime.SyncScanSummary, error) {
	var row struct{ ScanResult *string }
	if err := db.Db.WithContext(ctx).Model(&Sync{}).Select("scan_result").First(&row, id).Error; err != nil {
		return nil, err
	}
	return decodeSyncSummary(row.ScanResult)
}

func decodeSyncSummary(raw *string) (*realtime.SyncScanSummary, error) {
	if raw == nil || *raw == "" {
		return nil, nil
	}
	var result *realtime.SyncScanSummary
	err := json.Unmarshal([]byte(*raw), &result)
	return result, err
}

// GetSyncRecordViews 批量读取列表摘要，避免构造各记录的失败数组。
func GetSyncRecordViews(ctx context.Context, page, pageSize int) ([]SyncTaskView, int64, error) {
	var total int64
	query := db.Db.WithContext(ctx).Model(&Sync{})
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []syncViewRow
	if err := query.Offset((page - 1) * pageSize).Limit(pageSize).Order("id DESC").Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	views := make([]SyncTaskView, 0, len(rows))
	for _, row := range rows {
		summary, err := decodeSyncSummary(row.RawScanResult)
		if err != nil {
			return nil, 0, err
		}
		views = append(views, SyncTaskView{Sync: row.Sync, ScanResult: summary})
	}
	return views, total, nil
}

// GetSyncFailurePage 只向客户端返回一页；原 JSON 存储的解析成本仍为全量。
func GetSyncFailurePage(ctx context.Context, id uint, offset, limit int) ([]realtime.SyncScanFailure, int, bool, error) {
	var task Sync
	if err := db.Db.WithContext(ctx).Select("id", "scan_result").First(&task, id).Error; err != nil {
		return nil, 0, false, err
	}
	if task.ScanResult == nil {
		return []realtime.SyncScanFailure{}, 0, false, nil
	}
	total := len(task.ScanResult.Failures)
	start := min(offset, total)
	if start == total {
		return []realtime.SyncScanFailure{}, total, true, nil
	}
	return task.ScanResult.Failures[start:min(start+limit, total)], total, true, nil
}
