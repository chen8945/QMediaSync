package requests

import (
	"strings"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

// UpdateLogSettingRequest 更新日志设置请求。
type UpdateLogSettingRequest struct {
	Level      string `form:"level" json:"level" binding:"required"`
	MaxSizeMB  *int   `form:"maxSizeMB" json:"maxSizeMB"`
	MaxBackups *int   `form:"maxBackups" json:"maxBackups"`
	MaxAgeDays *int   `form:"maxAgeDays" json:"maxAgeDays"`
}

// Validate 校验日志设置请求。
func (r UpdateLogSettingRequest) Validate() error {
	if _, ok := helpers.ParseLogLevel(r.Level); !ok {
		return validation.New("level", "必须是 debug、info、warn 或 error")
	}
	if r.MaxSizeMB != nil {
		if err := validation.RangeInt("maxSizeMB", *r.MaxSizeMB, 1, 1024); err != nil {
			return err
		}
	}
	if r.MaxBackups != nil {
		if err := validation.RangeInt("maxBackups", *r.MaxBackups, 1, 100); err != nil {
			return err
		}
	}
	if r.MaxAgeDays == nil {
		return nil
	}
	if err := validation.RangeInt("maxAgeDays", *r.MaxAgeDays, 1, 365); err != nil {
		return err
	}
	return nil
}

// GetCronNextTimeRequest 获取 Cron 下次执行时间请求。
type GetCronNextTimeRequest struct {
	Cron string `form:"cron" json:"cron" binding:"required"`
}

// Validate 校验 Cron 下次执行时间请求。
func (r GetCronNextTimeRequest) Validate() error {
	return validation.Cron("cron", r.Cron, false)
}

// NormalizedCron 返回规范化后的 Cron 表达式。
func (r GetCronNextTimeRequest) NormalizedCron() string {
	return strings.TrimSpace(r.Cron)
}

// ValidateCronRequest 验证 Cron 表达式请求。
type ValidateCronRequest struct {
	CronExpression string `json:"cron_expression" binding:"required"`
}

// Validate 校验 Cron 表达式请求。
func (r ValidateCronRequest) Validate() error {
	return validation.Cron("cron_expression", r.CronExpression, false)
}

// NormalizedCronExpression 返回规范化后的 Cron 表达式。
func (r ValidateCronRequest) NormalizedCronExpression() string {
	return strings.TrimSpace(r.CronExpression)
}

// UpdateThreadsRequest 更新线程配置请求。
type UpdateThreadsRequest struct {
	DownloadThreads                int    `form:"download_threads" json:"download_threads" binding:"required"`
	FileDetailThreads              int    `form:"file_detail_threads" json:"file_detail_threads" binding:"required"`
	OpenlistQPS                    int    `form:"openlist_qps" json:"openlist_qps" binding:"required"`
	OpenlistRetry                  int    `form:"openlist_retry" json:"openlist_retry" binding:"required"`
	OpenlistRetryDelay             int    `form:"openlist_retry_delay" json:"openlist_retry_delay" binding:"required"`
	FileListPageSize               int    `form:"file_list_page_size" json:"file_list_page_size" binding:"required"`
	UploadRapidWaitEnabled         *int   `form:"upload_rapid_wait_enabled" json:"upload_rapid_wait_enabled"`
	UploadRapidWaitTimeoutSeconds  *int   `form:"upload_rapid_wait_timeout_seconds" json:"upload_rapid_wait_timeout_seconds"`
	UploadRapidWaitIntervalSeconds *int   `form:"upload_rapid_wait_interval_seconds" json:"upload_rapid_wait_interval_seconds"`
	UploadRapidWaitMinSize         *int64 `form:"upload_rapid_wait_min_size" json:"upload_rapid_wait_min_size"`
	UploadRapidWaitForceSize       *int64 `form:"upload_rapid_wait_force_size" json:"upload_rapid_wait_force_size"`
	UploadRapidWaitSkipUpload      *int   `form:"upload_rapid_wait_skip_upload" json:"upload_rapid_wait_skip_upload"`
	URLValidityCheckEnabled        *int   `form:"url_validity_check_enabled" json:"url_validity_check_enabled"`
	URLValidityCheckTimeoutSeconds *int   `form:"url_validity_check_timeout_seconds" json:"url_validity_check_timeout_seconds"`
}

// Validate 校验线程配置请求。
func (r UpdateThreadsRequest) Validate() error {
	if err := validation.RangeInt("download_threads", r.DownloadThreads, 1, 10); err != nil {
		return err
	}
	if err := validation.RangeInt("file_detail_threads", r.FileDetailThreads, 2, 10); err != nil {
		return err
	}
	if err := validation.RangeInt("openlist_qps", r.OpenlistQPS, 2, 10); err != nil {
		return err
	}
	if err := validation.RangeInt("openlist_retry", r.OpenlistRetry, 1, 10); err != nil {
		return err
	}
	if err := validation.RangeInt("openlist_retry_delay", r.OpenlistRetryDelay, 30, 3600); err != nil {
		return err
	}
	if err := validation.RangeInt("file_list_page_size", r.FileListPageSize, 100, 1150); err != nil {
		return err
	}
	if r.UploadRapidWaitEnabled != nil {
		if err := validation.OneOfInt("upload_rapid_wait_enabled", *r.UploadRapidWaitEnabled, []int{0, 1}); err != nil {
			return err
		}
	}
	if r.UploadRapidWaitTimeoutSeconds != nil {
		if err := validation.RangeInt("upload_rapid_wait_timeout_seconds", *r.UploadRapidWaitTimeoutSeconds, 0, 86400); err != nil {
			return err
		}
	}
	if r.UploadRapidWaitIntervalSeconds != nil {
		if err := validation.RangeInt("upload_rapid_wait_interval_seconds", *r.UploadRapidWaitIntervalSeconds, 1, 3600); err != nil {
			return err
		}
	}
	if r.UploadRapidWaitMinSize != nil {
		if err := validation.RangeInt64("upload_rapid_wait_min_size", *r.UploadRapidWaitMinSize, 0, 9223372036854775807); err != nil {
			return err
		}
	}
	if r.UploadRapidWaitForceSize != nil {
		if err := validation.RangeInt64("upload_rapid_wait_force_size", *r.UploadRapidWaitForceSize, 0, 9223372036854775807); err != nil {
			return err
		}
	}
	if r.UploadRapidWaitSkipUpload != nil {
		if err := validation.OneOfInt("upload_rapid_wait_skip_upload", *r.UploadRapidWaitSkipUpload, []int{0, 1}); err != nil {
			return err
		}
	}
	if r.URLValidityCheckEnabled != nil {
		if err := validation.OneOfInt("url_validity_check_enabled", *r.URLValidityCheckEnabled, []int{0, 1}); err != nil {
			return err
		}
	}
	if r.URLValidityCheckTimeoutSeconds != nil {
		if err := validation.RangeInt(
			"url_validity_check_timeout_seconds",
			*r.URLValidityCheckTimeoutSeconds,
			1,
			models.MaxURLValidityCheckTimeoutSeconds,
		); err != nil {
			return err
		}
	}
	return nil
}

// ToModel 转换为线程配置模型。
func (r UpdateThreadsRequest) ToModel(baseRapidWait models.SettingUploadRapidWait, baseURLValidityCheck models.SettingURLValidityCheck) models.SettingThreadAndRapidWait {
	modelReq := models.SettingThreadAndRapidWait{
		SettingThreads: models.SettingThreads{
			DownloadThreads:    r.DownloadThreads,
			FileDetailThreads:  r.FileDetailThreads,
			OpenlistQPS:        r.OpenlistQPS,
			OpenlistRetry:      r.OpenlistRetry,
			OpenlistRetryDelay: r.OpenlistRetryDelay,
			FileListPageSize:   r.FileListPageSize,
		},
		SettingUploadRapidWait:  baseRapidWait,
		SettingURLValidityCheck: baseURLValidityCheck,
	}
	if r.UploadRapidWaitEnabled != nil {
		modelReq.UploadRapidWaitEnabled = *r.UploadRapidWaitEnabled
	}
	if r.UploadRapidWaitTimeoutSeconds != nil {
		modelReq.UploadRapidWaitTimeoutSeconds = *r.UploadRapidWaitTimeoutSeconds
	}
	if r.UploadRapidWaitIntervalSeconds != nil {
		modelReq.UploadRapidWaitIntervalSeconds = *r.UploadRapidWaitIntervalSeconds
	}
	if r.UploadRapidWaitMinSize != nil {
		modelReq.UploadRapidWaitMinSize = *r.UploadRapidWaitMinSize
	}
	if r.UploadRapidWaitForceSize != nil {
		modelReq.UploadRapidWaitForceSize = *r.UploadRapidWaitForceSize
	}
	if r.UploadRapidWaitSkipUpload != nil {
		modelReq.UploadRapidWaitSkipUpload = *r.UploadRapidWaitSkipUpload
	}
	if r.URLValidityCheckEnabled != nil {
		modelReq.URLValidityCheckEnabled = *r.URLValidityCheckEnabled
	}
	if r.URLValidityCheckTimeoutSeconds != nil {
		modelReq.URLValidityCheckTimeoutSeconds = *r.URLValidityCheckTimeoutSeconds
	}
	return modelReq
}

// UpdateStrmConfigRequest 更新 STRM 配置请求。
type UpdateStrmConfigRequest struct {
	LocalProxy          int      `form:"local_proxy" json:"local_proxy"`
	StrmBaseURL         string   `form:"strm_base_url" json:"strm_base_url" binding:"required"`
	Cron                string   `form:"cron" json:"cron" binding:"required"`
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

// Validate 校验 STRM 配置请求。
func (r UpdateStrmConfigRequest) Validate() error {
	if err := validation.HTTPURL("strm_base_url", r.StrmBaseURL, false); err != nil {
		return err
	}
	if err := validation.Cron("cron", r.Cron, false); err != nil {
		return err
	}
	if err := validation.RangeInt64("min_video_size", r.MinVideoSize, 0, 9223372036854775807); err != nil {
		return err
	}
	if err := validation.ExtList("video_ext_arr", r.VideoExtArr, false); err != nil {
		return err
	}
	if err := validation.ExtList("meta_ext_arr", r.MetaExtArr, false); err != nil {
		return err
	}
	if err := validation.RegexList("exclude_name_regex_arr", r.ExcludeNameRegexArr); err != nil {
		return err
	}
	if err := validation.OneOfInt("local_proxy", r.LocalProxy, []int{0, 1}); err != nil {
		return err
	}
	if err := validation.OneOfInt("upload_meta", r.UploadMeta, []int{0, 1, 2}); err != nil {
		return err
	}
	if err := validation.OneOfInt("download_meta", r.DownloadMeta, []int{0, 1}); err != nil {
		return err
	}
	if err := validation.OneOfInt("delete_dir", r.DeleteDir, []int{0, 1}); err != nil {
		return err
	}
	if err := validation.OneOfInt("add_path", r.AddPath, []int{1, 2, 3}); err != nil {
		return err
	}
	return validation.OneOfInt("check_meta_mtime", r.CheckMetaMtime, []int{0, 1})
}

// ToModel 转换为 STRM 配置模型。
func (r UpdateStrmConfigRequest) ToModel() models.SettingStrm {
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
