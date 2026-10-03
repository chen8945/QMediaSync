package requests

import "qmediasync/internal/validation"

// StrmResultsRequest 查询 STRM 后处理结果。
type StrmResultsRequest struct {
	Page         int  `form:"page"`
	PageSize     int  `form:"page_size"`
	ID           uint `form:"id"`
	ParentTaskID uint `form:"parent_task_id"`
	UploadTaskID uint `form:"upload_task_id"`
	SyncPathID   uint `form:"sync_path_id"`
}

// Validate 校验并补充默认分页参数。
func (r *StrmResultsRequest) Validate() error {
	if r.Page == 0 {
		r.Page = 1
	}
	if r.PageSize == 0 {
		r.PageSize = 20
	}
	if r.Page < 1 || r.Page > 1000000 {
		return validation.New("page", "必须在 1 到 1000000 之间")
	}
	if r.PageSize < 1 || r.PageSize > 100 {
		return validation.New("page_size", "必须在 1 到 100 之间")
	}
	return nil
}
