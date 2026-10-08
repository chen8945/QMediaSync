package requests

import "testing"

func TestBackupConfigRequestValidate(t *testing.T) {
	valid := BackupConfigUpdateRequest{
		BackupEnabled:   1,
		BackupCron:      "0 4 * * *",
		BackupRetention: 30,
		BackupMaxCount:  10,
		BackupCompress:  1,
	}

	tests := []struct {
		name    string
		mutate  func(*BackupConfigUpdateRequest)
		wantErr bool
	}{
		{name: "合法备份配置通过"},
		{name: "备份开关枚举错误失败", mutate: func(r *BackupConfigUpdateRequest) { r.BackupEnabled = 2 }, wantErr: true},
		{name: "Cron 格式错误失败", mutate: func(r *BackupConfigUpdateRequest) { r.BackupCron = "bad" }, wantErr: true},
		{name: "保留天数为 0 通过", mutate: func(r *BackupConfigUpdateRequest) { r.BackupRetention = 0 }},
		{name: "保留天数大于 365 失败", mutate: func(r *BackupConfigUpdateRequest) { r.BackupRetention = 366 }, wantErr: true},
		{name: "最大数量大于 1000 失败", mutate: func(r *BackupConfigUpdateRequest) { r.BackupMaxCount = 1001 }, wantErr: true},
		{name: "压缩开关枚举错误失败", mutate: func(r *BackupConfigUpdateRequest) { r.BackupCompress = 2 }, wantErr: true},
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

func TestBackupCreateRequestValidate(t *testing.T) {
	tests := []struct {
		name       string
		req        BackupCreateRequest
		wantReason string
		wantErr    bool
	}{
		{name: "原因为空使用默认值", req: BackupCreateRequest{}, wantReason: "手动备份"},
		{name: "原因会去除首尾空白", req: BackupCreateRequest{Reason: " 手动执行 "}, wantReason: "手动执行"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			err := req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if req.Reason != tt.wantReason {
				t.Fatalf("Reason = %q, want %q", req.Reason, tt.wantReason)
			}
		})
	}
}

func TestBackupListRequestNormalize(t *testing.T) {
	tests := []struct {
		name         string
		req          BackupListRequest
		wantPage     int
		wantPageSize int
		wantType     string
	}{
		{name: "空参数使用默认值", req: BackupListRequest{}, wantPage: 1, wantPageSize: 20, wantType: "all"},
		{name: "合法分页通过", req: BackupListRequest{Page: 2, PageSize: 50, Type: "manual"}, wantPage: 2, wantPageSize: 50, wantType: "manual"},
		{name: "非法分页回退默认值", req: BackupListRequest{Page: -1, PageSize: 101}, wantPage: 1, wantPageSize: 20, wantType: "all"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := tt.req
			req.Normalize()
			if req.Page != tt.wantPage || req.PageSize != tt.wantPageSize || req.Type != tt.wantType {
				t.Fatalf("Normalize() = page %d page_size %d type %q, want page %d page_size %d type %q",
					req.Page, req.PageSize, req.Type, tt.wantPage, tt.wantPageSize, tt.wantType)
			}
		})
	}
}

func TestBackupRestoreRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		req     BackupRestoreRequest
		wantErr bool
	}{
		{name: "合法备份记录 ID 通过", req: BackupRestoreRequest{RecordID: 1}},
		{name: "合法备份文件名通过", req: BackupRestoreRequest{FileName: "备份 2026.ZIP"}},
		{name: "文件名保留首部空白", req: BackupRestoreRequest{FileName: " backup.zip"}},
		{name: "未指定来源失败", req: BackupRestoreRequest{}, wantErr: true},
		{name: "同时指定两个来源失败", req: BackupRestoreRequest{RecordID: 1, FileName: "backup.zip"}, wantErr: true},
		{name: "上级目录失败", req: BackupRestoreRequest{FileName: "../backup.zip"}, wantErr: true},
		{name: "子目录失败", req: BackupRestoreRequest{FileName: "temp/backup.zip"}, wantErr: true},
		{name: "绝对路径失败", req: BackupRestoreRequest{FileName: "/backup.zip"}, wantErr: true},
		{name: "Windows 路径失败", req: BackupRestoreRequest{FileName: `C:\backup.zip`}, wantErr: true},
		{name: "反斜杠失败", req: BackupRestoreRequest{FileName: `temp\backup.zip`}, wantErr: true},
		{name: "冒号失败", req: BackupRestoreRequest{FileName: "backup:stream.zip"}, wantErr: true},
		{name: "空字符失败", req: BackupRestoreRequest{FileName: "backup\x00.zip"}, wantErr: true},
		{name: "非 ZIP 失败", req: BackupRestoreRequest{FileName: "backup.tar"}, wantErr: true},
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
