package requests

import (
	"testing"

	"qmediasync/internal/models"
)

func intPtr(value int) *int {
	return &value
}

func int64Ptr(value int64) *int64 {
	return &value
}

func TestUpdateLogSettingRequestValidate(t *testing.T) {
	valid := UpdateLogSettingRequest{
		Level:      "info",
		MaxSizeMB:  intPtr(10),
		MaxBackups: intPtr(3),
		MaxAgeDays: intPtr(7),
	}

	tests := []struct {
		name    string
		mutate  func(*UpdateLogSettingRequest)
		wantErr bool
	}{
		{name: "合法日志设置通过"},
		{name: "兼容仅更新日志等级", mutate: func(r *UpdateLogSettingRequest) {
			r.MaxSizeMB = nil
			r.MaxBackups = nil
			r.MaxAgeDays = nil
		}},
		{name: "日志等级错误失败", mutate: func(r *UpdateLogSettingRequest) { r.Level = "verbose" }, wantErr: true},
		{name: "单文件最大大小小于 1 失败", mutate: func(r *UpdateLogSettingRequest) { r.MaxSizeMB = intPtr(0) }, wantErr: true},
		{name: "单文件最大大小大于 1024 失败", mutate: func(r *UpdateLogSettingRequest) { r.MaxSizeMB = intPtr(1025) }, wantErr: true},
		{name: "备份数小于 1 失败", mutate: func(r *UpdateLogSettingRequest) { r.MaxBackups = intPtr(0) }, wantErr: true},
		{name: "备份数大于 100 失败", mutate: func(r *UpdateLogSettingRequest) { r.MaxBackups = intPtr(101) }, wantErr: true},
		{name: "保留天数小于 1 失败", mutate: func(r *UpdateLogSettingRequest) { r.MaxAgeDays = intPtr(0) }, wantErr: true},
		{name: "保留天数大于 365 失败", mutate: func(r *UpdateLogSettingRequest) { r.MaxAgeDays = intPtr(366) }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCronRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     interface{ Validate() error }
		wantErr bool
	}{
		{name: "下次执行时间 Cron 合法", req: GetCronNextTimeRequest{Cron: "0 2 * * *"}},
		{name: "下次执行时间 Cron 为空失败", req: GetCronNextTimeRequest{Cron: ""}, wantErr: true},
		{name: "下次执行时间 Cron 非法失败", req: GetCronNextTimeRequest{Cron: "bad"}, wantErr: true},
		{name: "Cron 描述校验合法", req: ValidateCronRequest{CronExpression: "0 2 * * *"}},
		{name: "Cron 描述校验为空失败", req: ValidateCronRequest{CronExpression: ""}, wantErr: true},
		{name: "Cron 描述校验非法失败", req: ValidateCronRequest{CronExpression: "bad"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCronRequestNormalized(t *testing.T) {
	nextReq := GetCronNextTimeRequest{Cron: " 0 2 * * * "}
	if got := nextReq.NormalizedCron(); got != "0 2 * * *" {
		t.Fatalf("NormalizedCron() = %q, want %q", got, "0 2 * * *")
	}

	validateReq := ValidateCronRequest{CronExpression: " 0 2 * * * "}
	if got := validateReq.NormalizedCronExpression(); got != "0 2 * * *" {
		t.Fatalf("NormalizedCronExpression() = %q, want %q", got, "0 2 * * *")
	}
}

func TestUpdateThreadsRequestValidate(t *testing.T) {
	valid := UpdateThreadsRequest{
		DownloadThreads:                1,
		FileDetailThreads:              2,
		OpenlistQPS:                    2,
		OpenlistRetry:                  1,
		OpenlistRetryDelay:             30,
		FileListPageSize:               1150,
		UploadRapidWaitEnabled:         intPtr(1),
		UploadRapidWaitTimeoutSeconds:  intPtr(600),
		UploadRapidWaitIntervalSeconds: intPtr(60),
		UploadRapidWaitMinSize:         int64Ptr(1073741824),
		UploadRapidWaitForceSize:       int64Ptr(5368709120),
		UploadRapidWaitSkipUpload:      intPtr(0),
		URLValidityCheckEnabled:        intPtr(1),
		URLValidityCheckTimeoutSeconds: intPtr(3),
	}

	tests := []struct {
		name    string
		mutate  func(*UpdateThreadsRequest)
		wantErr bool
	}{
		{name: "合法线程配置通过"},
		{name: "下载 QPS 为 0 失败", mutate: func(r *UpdateThreadsRequest) { r.DownloadThreads = 0 }, wantErr: true},
		{name: "网盘详情 QPS 小于 2 失败", mutate: func(r *UpdateThreadsRequest) { r.FileDetailThreads = 1 }, wantErr: true},
		{name: "OpenList QPS 大于 10 失败", mutate: func(r *UpdateThreadsRequest) { r.OpenlistQPS = 11 }, wantErr: true},
		{name: "重试间隔小于 30 失败", mutate: func(r *UpdateThreadsRequest) { r.OpenlistRetryDelay = 29 }, wantErr: true},
		{name: "分页数量大于 1150 失败", mutate: func(r *UpdateThreadsRequest) { r.FileListPageSize = 1151 }, wantErr: true},
		{name: "秒传等待开关非法失败", mutate: func(r *UpdateThreadsRequest) { r.UploadRapidWaitEnabled = intPtr(2) }, wantErr: true},
		{name: "秒传等待间隔小于 1 失败", mutate: func(r *UpdateThreadsRequest) { r.UploadRapidWaitIntervalSeconds = intPtr(0) }, wantErr: true},
		{name: "秒传等待超时小于 0 失败", mutate: func(r *UpdateThreadsRequest) { r.UploadRapidWaitTimeoutSeconds = intPtr(-1) }, wantErr: true},
		{name: "秒传等待最小大小小于 0 失败", mutate: func(r *UpdateThreadsRequest) { r.UploadRapidWaitMinSize = int64Ptr(-1) }, wantErr: true},
		{name: "URL 有效性检查开关非法失败", mutate: func(r *UpdateThreadsRequest) { r.URLValidityCheckEnabled = intPtr(2) }, wantErr: true},
		{name: "URL 有效性检查超时小于 1 失败", mutate: func(r *UpdateThreadsRequest) { r.URLValidityCheckTimeoutSeconds = intPtr(0) }, wantErr: true},
		{name: "URL 有效性检查超时大于 9 失败", mutate: func(r *UpdateThreadsRequest) { r.URLValidityCheckTimeoutSeconds = intPtr(10) }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateThreadsRequestToModelIncludesRapidWaitPolicy(t *testing.T) {
	req := UpdateThreadsRequest{
		DownloadThreads:                1,
		FileDetailThreads:              3,
		OpenlistQPS:                    2,
		OpenlistRetry:                  1,
		OpenlistRetryDelay:             30,
		FileListPageSize:               1150,
		UploadRapidWaitEnabled:         intPtr(1),
		UploadRapidWaitTimeoutSeconds:  intPtr(900),
		UploadRapidWaitIntervalSeconds: intPtr(30),
		UploadRapidWaitMinSize:         int64Ptr(1024),
		UploadRapidWaitForceSize:       int64Ptr(2048),
		UploadRapidWaitSkipUpload:      intPtr(1),
		URLValidityCheckEnabled:        intPtr(0),
		URLValidityCheckTimeoutSeconds: intPtr(9),
	}

	got := req.ToModel(models.SettingUploadRapidWait{}, models.SettingURLValidityCheck{})

	if got.UploadRapidWaitEnabled != 1 {
		t.Fatalf("UploadRapidWaitEnabled = %d，期望 1", got.UploadRapidWaitEnabled)
	}
	if got.UploadRapidWaitTimeoutSeconds != 900 {
		t.Fatalf("UploadRapidWaitTimeoutSeconds = %d，期望 900", got.UploadRapidWaitTimeoutSeconds)
	}
	if got.UploadRapidWaitIntervalSeconds != 30 {
		t.Fatalf("UploadRapidWaitIntervalSeconds = %d，期望 30", got.UploadRapidWaitIntervalSeconds)
	}
	if got.UploadRapidWaitMinSize != 1024 {
		t.Fatalf("UploadRapidWaitMinSize = %d，期望 1024", got.UploadRapidWaitMinSize)
	}
	if got.UploadRapidWaitForceSize != 2048 {
		t.Fatalf("UploadRapidWaitForceSize = %d，期望 2048", got.UploadRapidWaitForceSize)
	}
	if got.UploadRapidWaitSkipUpload != 1 {
		t.Fatalf("UploadRapidWaitSkipUpload = %d，期望 1", got.UploadRapidWaitSkipUpload)
	}
	if got.URLValidityCheckEnabled != 0 {
		t.Fatalf("URLValidityCheckEnabled = %d，期望 0", got.URLValidityCheckEnabled)
	}
	if got.URLValidityCheckTimeoutSeconds != 9 {
		t.Fatalf("URLValidityCheckTimeoutSeconds = %d，期望 9", got.URLValidityCheckTimeoutSeconds)
	}
}

func TestUpdateThreadsRequestToModelKeepsOptionalPoliciesWhenOmitted(t *testing.T) {
	req := UpdateThreadsRequest{
		DownloadThreads:    1,
		FileDetailThreads:  3,
		OpenlistQPS:        2,
		OpenlistRetry:      1,
		OpenlistRetryDelay: 30,
		FileListPageSize:   1150,
	}
	base := models.SettingUploadRapidWait{
		UploadRapidWaitEnabled:         1,
		UploadRapidWaitTimeoutSeconds:  900,
		UploadRapidWaitIntervalSeconds: 30,
		UploadRapidWaitMinSize:         1024,
		UploadRapidWaitForceSize:       2048,
		UploadRapidWaitSkipUpload:      1,
	}
	baseURLValidityCheck := models.SettingURLValidityCheck{
		URLValidityCheckEnabled:        0,
		URLValidityCheckTimeoutSeconds: 12,
	}

	got := req.ToModel(base, baseURLValidityCheck)

	if got.SettingUploadRapidWait != base {
		t.Fatalf("SettingUploadRapidWait = %+v，期望 %+v", got.SettingUploadRapidWait, base)
	}
	if got.SettingURLValidityCheck != baseURLValidityCheck {
		t.Fatalf("SettingURLValidityCheck = %+v，期望 %+v", got.SettingURLValidityCheck, baseURLValidityCheck)
	}
}

func TestUpdateStrmConfigRequestValidate(t *testing.T) {
	valid := UpdateStrmConfigRequest{
		LocalProxy:     0,
		StrmBaseURL:    "http://127.0.0.1:8096",
		Cron:           "0 2 * * *",
		MinVideoSize:   0,
		VideoExtArr:    []string{".mp4", ".mkv"},
		MetaExtArr:     []string{".nfo", ".jpg"},
		ExcludeNameArr: []string{},
		UploadMeta:     0,
		DownloadMeta:   1,
		DeleteDir:      1,
		AddPath:        2,
		CheckMetaMtime: 0,
	}

	tests := []struct {
		name    string
		mutate  func(*UpdateStrmConfigRequest)
		wantErr bool
	}{
		{name: "合法 STRM 配置通过"},
		{name: "全局 STRM 允许完整路径", mutate: func(r *UpdateStrmConfigRequest) { r.AddPath = 1 }},
		{name: "全局 STRM 允许只添加文件名", mutate: func(r *UpdateStrmConfigRequest) { r.AddPath = 2 }},
		{name: "全局 STRM 允许不添加路径", mutate: func(r *UpdateStrmConfigRequest) { r.AddPath = 3 }},
		{name: "全局 STRM 路径模式枚举错误失败", mutate: func(r *UpdateStrmConfigRequest) { r.AddPath = -1 }, wantErr: true},
		{name: "URL 缺少协议失败", mutate: func(r *UpdateStrmConfigRequest) { r.StrmBaseURL = "127.0.0.1:8096" }, wantErr: true},
		{name: "Cron 格式错误失败", mutate: func(r *UpdateStrmConfigRequest) { r.Cron = "bad" }, wantErr: true},
		{name: "最小视频大小为负数失败", mutate: func(r *UpdateStrmConfigRequest) { r.MinVideoSize = -1 }, wantErr: true},
		{name: "下载元数据枚举错误失败", mutate: func(r *UpdateStrmConfigRequest) { r.DownloadMeta = 2 }, wantErr: true},
		{name: "视频扩展名缺少点失败", mutate: func(r *UpdateStrmConfigRequest) { r.VideoExtArr = []string{"mp4"} }, wantErr: true},
		{name: "允许 Go 正则及原文空格", mutate: func(r *UpdateStrmConfigRequest) {
			r.ExcludeNameRegexArr = []string{"(?i)^Sample\\.[^.]+$", " A{1,3};B "}
		}},
		{name: "空正则失败", mutate: func(r *UpdateStrmConfigRequest) { r.ExcludeNameRegexArr = []string{""} }, wantErr: true},
		{name: "非法正则失败", mutate: func(r *UpdateStrmConfigRequest) { r.ExcludeNameRegexArr = []string{"["} }, wantErr: true},
		{name: "不支持前瞻失败", mutate: func(r *UpdateStrmConfigRequest) { r.ExcludeNameRegexArr = []string{"(?=sample)"} }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := valid
			if tt.mutate != nil {
				tt.mutate(&req)
			}
			err := req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestUpdateStrmConfigRequestPreservesRegex(t *testing.T) {
	const pattern = " (?i)Sample\\.[A-Z]{1,3}; "
	req := UpdateStrmConfigRequest{ExcludeNameRegexArr: []string{pattern}}
	model := req.ToModel()
	if len(model.ExcludeNameRegexArr) != 1 || model.ExcludeNameRegexArr[0] != pattern {
		t.Fatalf("ToModel() 正则 = %q，期望原文 %q", model.ExcludeNameRegexArr, pattern)
	}
}
