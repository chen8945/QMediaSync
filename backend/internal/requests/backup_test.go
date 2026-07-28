package requests

import "testing"

func TestBackupConfigRequestValidate(t *testing.T) {
	validPassword := "BackupPass123"
	valid := BackupConfigUpdateRequest{
		BackupEnabled:   1,
		BackupCron:      "0 4 * * *",
		BackupRetention: 30,
		BackupMaxCount:  10,
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
		{name: "合法定时备份密码通过", mutate: func(r *BackupConfigUpdateRequest) { r.ScheduledBackupPassword = &validPassword }},
		{name: "不合规定时备份密码失败", mutate: func(r *BackupConfigUpdateRequest) { password := "short"; r.ScheduledBackupPassword = &password }, wantErr: true},
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
		{name: "记录预检通过", req: BackupRestoreRequest{Phase: BackupRestorePhasePreflight, RecordID: 1}},
		{name: "上传预检通过", req: BackupRestoreRequest{Phase: BackupRestorePhasePreflight}},
		{name: "确认恢复通过", req: BackupRestoreRequest{Phase: BackupRestorePhaseConfirm, RecordID: 1, PreflightID: "preflight", ConfirmOverwrite: true}},
		{name: "缺少阶段失败", req: BackupRestoreRequest{RecordID: 1}, wantErr: true},
		{name: "确认缺少预检标识失败", req: BackupRestoreRequest{Phase: BackupRestorePhaseConfirm, RecordID: 1, ConfirmOverwrite: true}, wantErr: true},
		{name: "确认未覆盖确认失败", req: BackupRestoreRequest{Phase: BackupRestorePhaseConfirm, RecordID: 1, PreflightID: "preflight"}, wantErr: true},
		{name: "密码含空白失败", req: BackupRestoreRequest{Phase: BackupRestorePhasePreflight, RecordID: 1, Password: "Backup Pass123"}, wantErr: true},
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
