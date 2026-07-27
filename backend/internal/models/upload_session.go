package models

import (
	"errors"
	"fmt"
	"time"

	"qmediasync/internal/db"
)

// UploadSessionStatus 是 115 上传会话状态。
type UploadSessionStatus string

const (
	UploadSessionStatusInit         UploadSessionStatus = "init"
	UploadSessionStatusRapidWaiting UploadSessionStatus = "rapid_waiting"
	UploadSessionStatusMultipart    UploadSessionStatus = "multipart"
	UploadSessionStatusCompleting   UploadSessionStatus = "completing"
	UploadSessionStatusCompleted    UploadSessionStatus = "completed"
	UploadSessionStatusAborted      UploadSessionStatus = "aborted"
	UploadSessionStatusFailed       UploadSessionStatus = "failed"
)

// UploadResumeState 是上传会话恢复状态。
type UploadResumeState string

const (
	UploadResumeStateNone                    UploadResumeState = "none"
	UploadResumeStateNewSession              UploadResumeState = "new_session"
	UploadResumeStateResumedSession          UploadResumeState = "resumed_session"
	UploadResumeStateSessionExpiredRestarted UploadResumeState = "session_expired_restarted"
)

// UploadSession 保存 115 调度和 OSS multipart checkpoint。
type UploadSession struct {
	BaseModel
	UploadTaskId uint `json:"upload_task_id" gorm:"uniqueIndex"`
	AccountId    uint `json:"account_id" gorm:"index"`

	LocalFullPath  string `json:"local_full_path" gorm:"type:text;size:1024"`
	FileName       string `json:"file_name" gorm:"size:512"`
	FileSize       int64  `json:"file_size"`
	LocalMtime     int64  `json:"local_mtime"`
	LocalSignature string `json:"local_signature" gorm:"size:255"`
	FileSha1       string `json:"file_sha1" gorm:"size:64"`
	Preid          string `json:"preid" gorm:"size:64"`

	ParentFileId   string `json:"parent_file_id" gorm:"size:128"`
	Target         string `json:"target" gorm:"size:255"`
	FileId         string `json:"file_id" gorm:"size:128"`
	PickCode       string `json:"pick_code" gorm:"size:128"`
	SignKey        string `json:"sign_key" gorm:"size:128"`
	SignRangeStart int64  `json:"sign_range_start"`
	SignRangeEnd   int64  `json:"sign_range_end"`
	SignValSha1    string `json:"sign_val_sha1" gorm:"size:64"`
	LastInitAt     int64  `json:"last_init_at"`
	LastResumeAt   int64  `json:"last_resume_at"`
	Callback       string `json:"callback" gorm:"type:text"`
	CallbackVar    string `json:"callback_var" gorm:"type:text"`

	Bucket         string `json:"bucket" gorm:"size:255"`
	Object         string `json:"object" gorm:"type:text;size:1024"`
	Endpoint       string `json:"endpoint" gorm:"size:255"`
	Region         string `json:"region" gorm:"size:128"`
	UploadId       string `json:"upload_id" gorm:"size:255"`
	PartSize       int64  `json:"part_size"`
	TotalParts     int    `json:"total_parts"`
	UploadedBytes  int64  `json:"uploaded_bytes"`
	UploadedParts  int    `json:"uploaded_parts"`
	LastPartNumber int    `json:"last_part_number"`
	LastPartEtag   string `json:"last_part_etag" gorm:"size:255"`

	Status                UploadSessionStatus `json:"status" gorm:"size:32;index"`
	ResumeState           UploadResumeState   `json:"resume_state" gorm:"size:32"`
	RapidWaitUntil        int64               `json:"rapid_wait_until"`
	RapidWaitAttempts     int                 `json:"rapid_wait_attempts"`
	RetryCount            int                 `json:"retry_count" gorm:"default:0"`
	LastError             string              `json:"last_error" gorm:"type:text"`
	LastProgressAt        int64               `json:"last_progress_at"`
	UploadStartedAt       int64               `json:"upload_started_at"`
	CompletedAt           int64               `json:"completed_at"`
	CompleteCallbackState string              `json:"complete_callback_state" gorm:"size:32"`
	CompleteCallbackError string              `json:"complete_callback_error" gorm:"type:text"`

	CompletedFileId   string `json:"completed_file_id" gorm:"size:128"`
	CompletedPickCode string `json:"completed_pick_code" gorm:"size:128"`
	CompletedParentId string `json:"completed_parent_id" gorm:"size:128"`
	CompletedSha1     string `json:"completed_sha1" gorm:"size:64"`
	CompletedSize     int64  `json:"completed_size"`
	CompletedMtime    int64  `json:"completed_mtime"`
}

// UploadSessionLocalSignature 是恢复上传前的本地文件签名。
type UploadSessionLocalSignature struct {
	FileSize       int64
	LocalMtime     int64
	FileSha1       string
	LocalSignature string
}

// UploadSessionCompleteResult 是上传完成后的远端定位结果。
type UploadSessionCompleteResult struct {
	FileId     string
	PickCode   string
	ParentId   string
	Sha1       string
	RemoteSha1 string // 仅携带远端响应确认的 SHA1；Sha1 仍可为恢复所需的本地 checkpoint 值。
	Size       int64
	Mtime      int64
}

// Save 保存上传会话。
func (session *UploadSession) Save() error {
	if session == nil {
		return errors.New("上传会话为空")
	}
	if session.Status == "" {
		session.Status = UploadSessionStatusInit
	}
	if session.ResumeState == "" {
		session.ResumeState = UploadResumeStateNone
	}
	return db.Db.Save(session).Error
}

// GetUploadSessionByUploadTaskId 按上传任务 ID 查询上传会话。
func GetUploadSessionByUploadTaskId(uploadTaskId uint) (*UploadSession, error) {
	var session UploadSession
	if err := db.Db.Where("upload_task_id = ?", uploadTaskId).Order("id DESC").First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// MarkCompleted 标记上传会话完成。
func (session *UploadSession) MarkCompleted(result UploadSessionCompleteResult) error {
	if session == nil {
		return errors.New("上传会话为空")
	}
	session.Status = UploadSessionStatusCompleted
	session.CompletedAt = time.Now().Unix()
	session.CompleteCallbackState = "completed"
	session.CompleteCallbackError = ""
	session.LastError = ""
	session.CompletedFileId = result.FileId
	session.CompletedPickCode = result.PickCode
	session.CompletedParentId = result.ParentId
	session.CompletedSha1 = result.Sha1
	session.CompletedSize = result.Size
	session.CompletedMtime = result.Mtime
	return session.Save()
}

// MarkCompleteCallbackFailed 记录 OSS complete 后 115 callback 业务失败。
func (session *UploadSession) MarkCompleteCallbackFailed(err error) error {
	if session == nil {
		return errors.New("上传会话为空")
	}
	if err == nil {
		return errors.New("complete callback 错误为空")
	}
	session.Status = UploadSessionStatusFailed
	session.CompleteCallbackState = "failed"
	session.CompleteCallbackError = err.Error()
	session.LastError = err.Error()
	return session.Save()
}

// ValidateLocalFile 校验本地文件签名是否仍匹配可续传会话。
func (session *UploadSession) ValidateLocalFile(signature UploadSessionLocalSignature) error {
	if session == nil {
		return errors.New("上传会话为空")
	}
	if session.FileSize != signature.FileSize {
		return fmt.Errorf("本地文件大小不匹配：session=%d current=%d", session.FileSize, signature.FileSize)
	}
	if session.LocalMtime != signature.LocalMtime {
		return fmt.Errorf("本地文件修改时间不匹配：session=%d current=%d", session.LocalMtime, signature.LocalMtime)
	}
	if session.FileSha1 != signature.FileSha1 {
		return errors.New("本地文件 SHA1 不匹配")
	}
	if session.LocalSignature != signature.LocalSignature {
		return errors.New("本地文件快速签名不匹配")
	}
	return nil
}
