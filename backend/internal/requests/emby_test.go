package requests

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmbyConfigRequestValidate(t *testing.T) {
	valid := UpdateEmbyConfigRequest{
		EmbyURL:                 "http://127.0.0.1:8096",
		EnableDeleteNetdisk:     1,
		EnableRefreshLibrary:    1,
		EnableMediaNotification: 0,
		EnableExtractMediaInfo:  1,
		EnableAuth:              0,
		SyncEnabled:             1,
		SyncCron:                "0 * * * *",
		SelectedLibraries:       `["movies"]`,
		SyncAllLibraries:        0,
		EnablePlaybackOverview:  1,
		EnablePlaybackProgress:  1,
	}

	tests := []struct {
		name    string
		mutate  func(*UpdateEmbyConfigRequest)
		wantErr bool
	}{
		{name: "合法 Emby 配置通过"},
		{name: "空 URL 允许", mutate: func(r *UpdateEmbyConfigRequest) { r.EmbyURL = "" }},
		{name: "URL 缺少协议失败", mutate: func(r *UpdateEmbyConfigRequest) { r.EmbyURL = "127.0.0.1:8096" }, wantErr: true},
		{name: "同步 Cron 错误失败", mutate: func(r *UpdateEmbyConfigRequest) { r.SyncCron = "bad" }, wantErr: true},
		{name: "开关枚举错误失败", mutate: func(r *UpdateEmbyConfigRequest) { r.EnableAuth = 2 }, wantErr: true},
		{name: "每日首次全量同步开关枚举错误失败", mutate: func(r *UpdateEmbyConfigRequest) {
			value := 2
			r.EnableDailyFirstFullSync = &value
		}, wantErr: true},
		{name: "媒体库 JSON 错误失败", mutate: func(r *UpdateEmbyConfigRequest) { r.SelectedLibraries = "[" }, wantErr: true},
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

func TestEmbyConfigPathMappingRequest(t *testing.T) {
	for _, test := range []struct {
		name, body       string
		present, invalid bool
	}{
		{"omitted", `{}`, false, false},
		{"null preserves", `{"path_mappings":null}`, false, false},
		{"clear", `{"path_mappings":[]}`, true, false},
		{"mapped", `{"path_mappings":[{"emby_root":"/emby","local_root":"/mnt/strm","sync_path_id":42}]}`, true, false},
		{"relative path", `{"path_mappings":[{"emby_root":"relative","local_root":"/mnt/strm"}]}`, true, true},
		{"overlap", `{"path_mappings":[{"emby_root":"/emby","local_root":"/mnt/strm"},{"emby_root":"/emby/films","local_root":"/mnt/films"}]}`, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var request UpdateEmbyConfigRequest
			if err := json.Unmarshal([]byte(test.body), &request); err != nil {
				t.Fatal(err)
			}
			if (request.PathMappings != nil) != test.present {
				t.Fatalf("field presence = %v", request.PathMappings != nil)
			}
			err := request.Validate()
			if (err != nil) != test.invalid {
				t.Fatalf("validation = %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "path_mappings") {
				t.Fatalf("missing field: %v", err)
			}
		})
	}
	for _, body := range []string{`{"path_mappings":{}}`, `{"path_mappings":"[]"}`, `{"path_mappings":[{"sync_path_id":-1}]}`} {
		var request UpdateEmbyConfigRequest
		if err := json.Unmarshal([]byte(body), &request); err == nil {
			t.Fatalf("accepted malformed input %s", body)
		}
	}
}
