package requests

import (
	"path/filepath"
	"runtime"
	"strings"

	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

// SyncPathStrmRequest 同步路径自定义 STRM 配置请求。
type SyncPathStrmRequest struct {
	LocalProxy          int      `form:"local_proxy" json:"local_proxy"`
	StrmBaseURL         string   `form:"strm_base_url" json:"strm_base_url"`
	Cron                string   `form:"cron" json:"cron"`
	MinVideoSize        int64    `form:"min_video_size" json:"min_video_size"`
	VideoExtArr         []string `json:"video_ext_arr"`
	MetaExtArr          []string `form:"meta_ext_arr" json:"meta_ext_arr"`
	ExcludeNameArr      []string `form:"exclude_name_arr" json:"exclude_name_arr"`
	ExcludeNameRegexArr []string `form:"exclude_name_regex_arr" json:"exclude_name_regex_arr"`
	UploadMeta          int      `form:"upload_meta" json:"upload_meta"`
	DownloadMeta        int      `form:"download_meta" json:"download_meta"`
	DeleteDir           int      `form:"delete_dir" json:"delete_dir"`
	AddPath             int      `form:"add_path" json:"add_path"`
	CheckMetaMtime      int      `form:"check_meta_mtime" json:"check_meta_mtime"`
}

// Validate 校验同步路径自定义 STRM 配置请求。
func (r SyncPathStrmRequest) Validate() error {
	if err := validation.HTTPURL("strm_base_url", r.StrmBaseURL, true); err != nil {
		return err
	}
	if err := validation.Cron("cron", r.Cron, true); err != nil {
		return err
	}
	if r.MinVideoSize < -1 {
		return validation.New("min_video_size", "不能小于 -1")
	}
	if err := validation.ExtList("video_ext_arr", r.VideoExtArr, true); err != nil {
		return err
	}
	if err := validation.ExtList("meta_ext_arr", r.MetaExtArr, true); err != nil {
		return err
	}
	if err := validation.RegexList("exclude_name_regex_arr", r.ExcludeNameRegexArr); err != nil {
		return err
	}
	if err := validation.OneOfInt("local_proxy", r.LocalProxy, []int{-1, 0, 1}); err != nil {
		return err
	}
	if err := validation.OneOfInt("upload_meta", r.UploadMeta, []int{-1, 0, 1, 2}); err != nil {
		return err
	}
	if err := validation.OneOfInt("download_meta", r.DownloadMeta, []int{-1, 0, 1}); err != nil {
		return err
	}
	if err := validation.OneOfInt("delete_dir", r.DeleteDir, []int{-1, 0, 1}); err != nil {
		return err
	}
	if err := validation.OneOfInt("add_path", r.AddPath, []int{-1, 1, 2, 3}); err != nil {
		return err
	}
	return validation.OneOfInt("check_meta_mtime", r.CheckMetaMtime, []int{-1, 0, 1})
}

// ToModel 转换为 STRM 配置模型。
func (r SyncPathStrmRequest) ToModel() models.SettingStrm {
	return models.SettingStrm{
		LocalProxy:          r.LocalProxy,
		StrmBaseUrl:         r.StrmBaseURL,
		Cron:                r.Cron,
		MinVideoSize:        r.MinVideoSize,
		VideoExtArr:         r.VideoExtArr,
		MetaExtArr:          r.MetaExtArr,
		ExcludeNameArr:      r.ExcludeNameArr,
		ExcludeNameRegexArr: r.ExcludeNameRegexArr,
		UploadMeta:          r.UploadMeta,
		DownloadMeta:        r.DownloadMeta,
		DeleteDir:           r.DeleteDir,
		AddPath:             r.AddPath,
		CheckMetaMtime:      r.CheckMetaMtime,
	}
}

func (r SyncPathStrmRequest) isZero() bool {
	return r.LocalProxy == 0 &&
		r.StrmBaseURL == "" &&
		r.Cron == "" &&
		r.MinVideoSize == 0 &&
		len(r.VideoExtArr) == 0 &&
		len(r.MetaExtArr) == 0 &&
		len(r.ExcludeNameArr) == 0 &&
		len(r.ExcludeNameRegexArr) == 0 &&
		r.UploadMeta == 0 &&
		r.DownloadMeta == 0 &&
		r.DeleteDir == 0 &&
		r.AddPath == 0 &&
		r.CheckMetaMtime == 0
}

// SyncPathRequest 创建同步路径请求。
type SyncPathRequest struct {
	SourceType             models.SourceType `json:"source_type" form:"source_type" binding:"required"`
	AccountID              uint              `json:"account_id" form:"account_id"`
	BaseCid                string            `json:"base_cid" form:"base_cid" binding:"required"`
	LocalPath              string            `json:"local_path" form:"local_path" binding:"required"`
	RemotePath             string            `json:"remote_path" form:"remote_path" binding:"required"`
	EnableCron             bool              `json:"enable_cron" form:"enable_cron"`
	DirectoryUploadEnabled *bool             `json:"directory_upload_enabled" form:"directory_upload_enabled"`
	CustomConfig           bool              `json:"custom_config" form:"custom_config"`

	// Setting 兼容计划中的嵌套结构；匿名字段兼容现有顶层 STRM 字段。
	Setting SyncPathStrmRequest `json:"setting" form:"setting"`
	SyncPathStrmRequest
}

// Validate 校验同步路径请求。
func (r SyncPathRequest) Validate() error {
	allowedSources := []string{
		string(models.SourceTypeLocal),
		string(models.SourceType115),
		string(models.SourceType123),
		string(models.SourceTypeOpenList),
		string(models.SourceTypeBaiduPan),
	}
	if err := validation.OneOfString("source_type", string(r.SourceType), allowedSources); err != nil {
		return err
	}
	if r.SourceType != models.SourceTypeLocal && r.AccountID == 0 {
		return validation.New("account_id", "非本地来源必须选择账号")
	}
	if err := validation.NonBlank("base_cid", r.BaseCid); err != nil {
		return err
	}
	if err := validation.NonBlank("local_path", r.LocalPath); err != nil {
		return err
	}
	if err := validation.NonBlank("remote_path", r.RemotePath); err != nil {
		return err
	}
	if r.CustomConfig {
		return r.strmRequest().Validate()
	}
	return nil
}

// NormalizedRemotePath 返回规范化后的同步源路径。
func (r SyncPathRequest) NormalizedRemotePath() string {
	remotePath := r.RemotePath
	if r.SourceType != models.SourceTypeLocal {
		remotePath = strings.ReplaceAll(remotePath, "\\", "/")
		remotePath = strings.TrimPrefix(remotePath, "/")
		return filepath.ToSlash(filepath.Clean(remotePath))
	}
	if runtime.GOOS != "windows" && !strings.HasPrefix(remotePath, "/") {
		return "/" + remotePath
	}
	return remotePath
}

// StrmSettingModel 返回请求中的 STRM 配置模型。
func (r SyncPathRequest) StrmSettingModel() models.SettingStrm {
	return r.strmRequest().ToModel()
}

func (r SyncPathRequest) strmRequest() SyncPathStrmRequest {
	if !r.Setting.isZero() {
		return r.Setting
	}
	return r.SyncPathStrmRequest
}

// UpdateSyncPathRequest 更新同步路径请求。
type UpdateSyncPathRequest struct {
	ID uint `json:"id" form:"id" binding:"required"`
	SyncPathRequest
}

// Validate 校验更新同步路径请求。
func (r UpdateSyncPathRequest) Validate() error {
	if r.ID == 0 {
		return validation.New("id", "不能为空")
	}
	return r.SyncPathRequest.Validate()
}
