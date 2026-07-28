package requests

import (
	"fmt"
	"strings"

	"qmediasync/internal/validation"
)

// BackupCreateRequest 创建备份请求。
// Password 是本次工件密码，留空表示创建未加密工件并要求 ConfirmUnencrypted。
// 服务端绝不以定时备份密码补全它。
type BackupCreateRequest struct {
	Reason             string `json:"reason"`
	Password           string `json:"password"`
	ConfirmUnencrypted bool   `json:"confirm_unencrypted"`
}

// Validate 校验创建备份请求。
// 密码按不透明字符串处理：既不裁剪首尾字符，也不做 Unicode 归一化。
func (r *BackupCreateRequest) Validate() error {
	r.Reason = strings.TrimSpace(r.Reason)
	if r.Reason == "" {
		r.Reason = "手动备份"
	}
	return validation.BackupPassword("password", r.Password)
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

const (
	BackupRestorePhasePreflight = "preflight"
	BackupRestorePhaseConfirm   = "confirm"
)

// BackupRestoreRequest 是既有恢复 URL 的两阶段请求。
// JSON 用于已保存工件；form 标签用于上传工件的 multipart 预检和无文件确认。
type BackupRestoreRequest struct {
	Phase            string `json:"phase" form:"phase"`
	RecordID         uint   `json:"record_id" form:"record_id"`
	Password         string `json:"password" form:"password"`
	PreflightID      string `json:"preflight_id" form:"preflight_id"`
	ConfirmOverwrite bool   `json:"confirm_overwrite" form:"confirm_overwrite"`
}

// Validate 校验恢复请求的 HTTP 边界字段。密码按不透明字符串处理，不裁剪也不归一化。
func (r BackupRestoreRequest) Validate() error {
	if err := validation.OneOfString("phase", r.Phase, []string{BackupRestorePhasePreflight, BackupRestorePhaseConfirm}); err != nil {
		return err
	}
	if r.RecordID != 0 {
		if err := validation.PositiveID("record_id", r.RecordID); err != nil {
			return err
		}
	}
	if err := validation.BackupPassword("password", r.Password); err != nil {
		return err
	}
	if r.Phase == BackupRestorePhaseConfirm {
		if strings.TrimSpace(r.PreflightID) == "" {
			return fmt.Errorf("preflight_id：不能为空")
		}
		if !r.ConfirmOverwrite {
			return fmt.Errorf("confirm_overwrite：必须确认完整覆盖恢复")
		}
	}
	return nil
}

// BackupConfigUpdateRequest 更新备份配置请求。
type BackupConfigUpdateRequest struct {
	BackupEnabled           int     `json:"backup_enabled"`
	BackupCron              string  `json:"backup_cron"`
	BackupRetention         int     `json:"backup_retention"`
	BackupMaxCount          int     `json:"backup_max_count"`
	ScheduledBackupPassword *string `json:"scheduled_backup_password"`
	ConfirmUnencrypted      bool    `json:"confirm_unencrypted"`
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
	if r.ScheduledBackupPassword != nil {
		return validation.BackupPassword("scheduled_backup_password", *r.ScheduledBackupPassword)
	}
	return nil
}
