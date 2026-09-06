package requests

import (
	"runtime"
	"testing"

	"qmediasync/internal/models"
)

func TestSyncPathRequestValidate(t *testing.T) {
	valid := SyncPathRequest{
		SourceType:   models.SourceType115,
		AccountID:    1,
		BaseCid:      "123",
		LocalPath:    "/media/strm",
		RemotePath:   "/movies",
		EnableCron:   true,
		CustomConfig: true,
		Setting: SyncPathStrmRequest{
			LocalProxy:     -1,
			StrmBaseURL:    "http://127.0.0.1:8096",
			Cron:           "0 2 * * *",
			MinVideoSize:   -1,
			VideoExtArr:    []string{".mp4", ".mkv"},
			MetaExtArr:     []string{".nfo"},
			ExcludeNameArr: []string{},
			UploadMeta:     -1,
			DownloadMeta:   -1,
			DeleteDir:      -1,
			AddPath:        -1,
			CheckMetaMtime: -1,
		},
	}

	tests := []struct {
		name    string
		mutate  func(*SyncPathRequest)
		wantErr bool
	}{
		{name: "合法网盘同步路径通过"},
		{name: "未知来源类型失败", mutate: func(r *SyncPathRequest) { r.SourceType = models.SourceType("bad") }, wantErr: true},
		{name: "非本地缺少账号失败", mutate: func(r *SyncPathRequest) { r.AccountID = 0 }, wantErr: true},
		{name: "本地来源允许账号为空", mutate: func(r *SyncPathRequest) { r.SourceType = models.SourceTypeLocal; r.AccountID = 0 }},
		{name: "自定义 STRM 允许继承路径模式", mutate: func(r *SyncPathRequest) { r.Setting.AddPath = -1 }},
		{name: "自定义 STRM 允许完整路径", mutate: func(r *SyncPathRequest) { r.Setting.AddPath = 1 }},
		{name: "自定义 STRM 允许只添加文件名", mutate: func(r *SyncPathRequest) { r.Setting.AddPath = 2 }},
		{name: "自定义 STRM 允许不添加路径", mutate: func(r *SyncPathRequest) { r.Setting.AddPath = 3 }},
		{name: "自定义 STRM 路径模式枚举错误失败", mutate: func(r *SyncPathRequest) { r.Setting.AddPath = 4 }, wantErr: true},
		{name: "自定义配置 Cron 错误失败", mutate: func(r *SyncPathRequest) { r.Setting.Cron = "bad" }, wantErr: true},
		{name: "自定义配置枚举错误失败", mutate: func(r *SyncPathRequest) { r.Setting.DownloadMeta = 3 }, wantErr: true},
		{name: "自定义配置允许 Go 正则", mutate: func(r *SyncPathRequest) { r.Setting.ExcludeNameRegexArr = []string{"(?i:Sample)", "^A{1,3}$"} }},
		{name: "自定义配置空正则失败", mutate: func(r *SyncPathRequest) { r.Setting.ExcludeNameRegexArr = []string{""} }, wantErr: true},
		{name: "自定义配置非法正则失败", mutate: func(r *SyncPathRequest) { r.Setting.ExcludeNameRegexArr = []string{"["} }, wantErr: true},
		{name: "自定义配置反向引用失败", mutate: func(r *SyncPathRequest) { r.Setting.ExcludeNameRegexArr = []string{"(a)\\1"} }, wantErr: true},
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

func TestSyncPathRequestValidateTopLevelStrmFields(t *testing.T) {
	req := SyncPathRequest{
		SourceType:   models.SourceType115,
		AccountID:    1,
		BaseCid:      "123",
		LocalPath:    "/media/strm",
		RemotePath:   "/movies",
		CustomConfig: true,
		SyncPathStrmRequest: SyncPathStrmRequest{
			LocalProxy:     -1,
			StrmBaseURL:    "http://127.0.0.1:8096",
			Cron:           "0 2 * * *",
			MinVideoSize:   -1,
			VideoExtArr:    []string{".mp4"},
			MetaExtArr:     []string{".nfo"},
			UploadMeta:     -1,
			DownloadMeta:   -1,
			DeleteDir:      -1,
			AddPath:        -1,
			CheckMetaMtime: -1,
		},
	}

	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestSyncPathRequestUpdateValidate(t *testing.T) {
	req := UpdateSyncPathRequest{
		ID: 1,
		SyncPathRequest: SyncPathRequest{
			SourceType: models.SourceType115,
			AccountID:  1,
			BaseCid:    "123",
			LocalPath:  "/media/strm",
			RemotePath: "/movies",
		},
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	req.ID = 0
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestSyncPathRequestNormalizedRemotePath(t *testing.T) {
	req := SyncPathRequest{SourceType: models.SourceType115, RemotePath: "\\movies\\2026"}
	if got := req.NormalizedRemotePath(); got != "movies/2026" {
		t.Fatalf("NormalizedRemotePath() = %q", got)
	}

	local := SyncPathRequest{SourceType: models.SourceTypeLocal, RemotePath: "media"}
	got := local.NormalizedRemotePath()
	if runtime.GOOS != "windows" && got != "/media" {
		t.Fatalf("NormalizedRemotePath() = %q", got)
	}
}

func TestSyncPathRequestRegexFieldCompatibility(t *testing.T) {
	const pattern = " (?i)^Sample\\.[^.]+$ "
	req := SyncPathRequest{
		SyncPathStrmRequest: SyncPathStrmRequest{ExcludeNameRegexArr: []string{pattern}},
	}
	if got := req.StrmSettingModel().ExcludeNameRegexArr; len(got) != 1 || got[0] != pattern {
		t.Fatalf("顶层正则字段 = %q，期望原文 %q", got, pattern)
	}
	req.Setting = SyncPathStrmRequest{ExcludeNameRegexArr: []string{"^Nested$"}}
	if got := req.StrmSettingModel().ExcludeNameRegexArr; len(got) != 1 || got[0] != "^Nested$" {
		t.Fatalf("仅包含正则的嵌套配置应优先，实际为 %q", got)
	}
}
