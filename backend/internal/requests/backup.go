package requests

import (
	"path/filepath"
	"strings"

	"qmediasync/internal/validation"
)

// BackupCreateRequest 创建备份请求。
type BackupCreateRequest struct {
	Reason string `json:"reason"`
}

// Validate 校验创建备份请求。
func (r *BackupCreateRequest) Validate() error {
	r.Reason = strings.TrimSpace(r.Reason)
	if r.Reason == "" {
		r.Reason = "手动备份"
	}
	return nil
}

// BackupListRequest 备份列表请求。
type BackupListRequest struct {
	Page     int    `json:"page" form:"page"`
	PageSize int    `json:"page_size" form:"page_size"`
	Type     string `json:"type" form:"type"`
}

// Normalize 规范化备份列表请求。
func (r *BackupListRequest) Normalize() {
	if r.Page < 1 {
		r.Page = 1
	}
	if r.PageSize < 1 || r.PageSize > 100 {
		r.PageSize = 20
	}
	r.Type = strings.TrimSpace(r.Type)
	if r.Type == "" {
		r.Type = "all"
	}
}

// BackupRestoreRequest 备份恢复请求。
type BackupRestoreRequest struct {
	RecordID uint   `json:"record_id"`
	FileName string `json:"file_name"`
}

// Validate 校验备份恢复请求。
func (r BackupRestoreRequest) Validate() error {
	if (r.RecordID > 0) == (r.FileName != "") {
		return validation.New("", "请指定备份记录 ID 或备份文件名，且不能同时指定")
	}
	if r.FileName != "" && (!filepath.IsLocal(r.FileName) || strings.ContainsAny(r.FileName, "/\\:\x00") ||
		!strings.EqualFold(filepath.Ext(r.FileName), ".zip")) {
		return validation.New("file_name", "仅支持备份目录中的 ZIP 文件名")
	}
	return nil
}

// BackupConfigUpdateRequest 更新备份配置请求。
type BackupConfigUpdateRequest struct {
	BackupEnabled   int    `json:"backup_enabled"`
	BackupCron      string `json:"backup_cron"`
	BackupRetention int    `json:"backup_retention"`
	BackupMaxCount  int    `json:"backup_max_count"`
	BackupCompress  int    `json:"backup_compress"`
}

// Validate 校验备份配置请求。
func (r BackupConfigUpdateRequest) Validate() error {
	if err := validation.OneOfInt("backup_enabled", r.BackupEnabled, []int{0, 1}); err != nil {
		return err
	}
	if err := validation.Cron("backup_cron", r.BackupCron, true); err != nil {
		return err
	}
	if r.BackupRetention > 0 {
		if err := validation.RangeInt("backup_retention", r.BackupRetention, 1, 365); err != nil {
			return err
		}
	}
	if err := validation.RangeInt("backup_max_count", r.BackupMaxCount, 0, 1000); err != nil {
		return err
	}
	return validation.OneOfInt("backup_compress", r.BackupCompress, []int{0, 1})
}
