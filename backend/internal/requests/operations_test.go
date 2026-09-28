package requests

import (
	"strconv"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestPaginationRequestValidate(t *testing.T) {
	req := PaginationRequest{}
	if err := req.Normalize(20); err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if req.Page != 1 || req.PageSize != 20 {
		t.Fatalf("Normalize() = %+v", req)
	}

	req = PaginationRequest{Page: 1, PageSize: 101}
	if err := req.Normalize(20); err == nil {
		t.Fatal("Normalize() error = nil, want error")
	}

	req = PaginationRequest{Page: 1, PageSize: 1151}
	if err := req.NormalizeFileList(); err == nil {
		t.Fatal("NormalizeFileList() error = nil, want error")
	}
}

func TestNetFileListRequestValidate(t *testing.T) {
	tests := []struct {
		name      string
		req       NetFileListRequest
		wantPage  int
		wantSize  int
		wantSort  string
		wantOrder string
		wantErr   bool
	}{
		{name: "默认分页和排序方向", req: NetFileListRequest{AccountID: 1}, wantPage: 1, wantSize: 50, wantSort: "", wantOrder: "asc"},
		{name: "允许 500 每页", req: NetFileListRequest{AccountID: 1, PaginationRequest: PaginationRequest{Page: 2, PageSize: 500}}, wantPage: 2, wantSize: 500, wantSort: "", wantOrder: "asc"},
		{name: "拒绝旧的 1150 每页", req: NetFileListRequest{AccountID: 1, PaginationRequest: PaginationRequest{Page: 1, PageSize: 1150}}, wantErr: true},
		{name: "允许刷新", req: NetFileListRequest{AccountID: 1, Refresh: true}, wantPage: 1, wantSize: 50, wantSort: "", wantOrder: "asc"},
		{name: "拒绝非法排序字段", req: NetFileListRequest{AccountID: 1, SortBy: "bad"}, wantErr: true},
		{name: "拒绝非法排序方向", req: NetFileListRequest{AccountID: 1, SortOrder: "up"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if tt.req.Page != tt.wantPage || tt.req.PageSize != tt.wantSize {
				t.Fatalf("pagination = (%d,%d), want (%d,%d)", tt.req.Page, tt.req.PageSize, tt.wantPage, tt.wantSize)
			}
			if tt.req.SortBy != tt.wantSort || tt.req.SortOrder != tt.wantOrder {
				t.Fatalf("sort = (%s,%s), want (%s,%s)", tt.req.SortBy, tt.req.SortOrder, tt.wantSort, tt.wantOrder)
			}
		})
	}
}

func TestOperationRequestValidate(t *testing.T) {
	t.Run("正 ID 通过", func(t *testing.T) {
		req := PositiveIDRequest{ID: 1}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("ID 列表去重通过", func(t *testing.T) {
		req := IDListRequest{IDs: []uint{2, 1, 2}}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if got := req.NormalizedIDs(); len(got) != 2 || got[0] != 2 || got[1] != 1 {
			t.Fatalf("NormalizedIDs() = %+v", got)
		}
	})

	t.Run("ID 列表包含 0 失败", func(t *testing.T) {
		req := IDListRequest{IDs: []uint{1, 0}}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestParsePositiveIDRequest(t *testing.T) {
	tests := []struct {
		name    string
		rawID   string
		wantID  uint
		wantErr bool
	}{
		{name: "合法路径 ID 通过", rawID: "12", wantID: 12},
		{name: "路径 ID 会去除首尾空白", rawID: " 12 ", wantID: 12},
		{name: "路径 ID 为空失败", rawID: " ", wantErr: true},
		{name: "路径 ID 非数字失败", rawID: "bad", wantErr: true},
		{name: "路径 ID 为 0 失败", rawID: "0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := ParsePositiveIDRequest(tt.rawID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParsePositiveIDRequest() error = %v, wantErr %v", err, tt.wantErr)
			}
			if req.ID != tt.wantID {
				t.Fatalf("ID = %d, want %d", req.ID, tt.wantID)
			}
		})
	}
}

func TestAssociationRequestValidate(t *testing.T) {
	t.Run("同步路径关联允许清空刮削路径", func(t *testing.T) {
		req := SaveRelScrapePathRequest{SyncPathID: 1, ScrapePathIDs: []uint{}}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("刮削路径关联允许清空同步路径", func(t *testing.T) {
		req := SaveScrapeStrmPathRequest{ScrapePathID: 1, SyncPathIDs: []uint{}}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("关联列表包含 0 失败", func(t *testing.T) {
		req := SaveRelScrapePathRequest{SyncPathID: 1, ScrapePathIDs: []uint{1, 0}}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestIDCSVRequestValidate(t *testing.T) {
	t.Run("逗号 ID 列表通过并去重", func(t *testing.T) {
		req := IDCSVRequest{IDs: "2,1,2"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		if got := req.NormalizedIDs(); len(got) != 2 || got[0] != 2 || got[1] != 1 {
			t.Fatalf("NormalizedIDs() = %+v", got)
		}
	})

	t.Run("逗号 ID 列表包含 0 失败", func(t *testing.T) {
		req := IDCSVRequest{IDs: "1,0"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("32 位平台拒绝超过 uint 范围的 ID", func(t *testing.T) {
		if strconv.IntSize != 32 {
			t.Skip("仅在 32 位平台验证 uint 截断风险")
		}
		req := IDCSVRequest{IDs: "4294967297"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestPathRequestValidate(t *testing.T) {
	t.Run("创建本地目录通过", func(t *testing.T) {
		req := CreateDirRequest{SourceType: models.SourceTypeLocal, ParentID: "/tmp", Name: "movies"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("远程创建目录缺少账号失败", func(t *testing.T) {
		req := CreateDirRequest{SourceType: models.SourceType115, ParentID: "0", Name: "movies"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("文件夹名路径穿越失败", func(t *testing.T) {
		req := CreateDirRequest{SourceType: models.SourceTypeLocal, ParentID: "/tmp", Name: ".."}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestQueueRequestValidate(t *testing.T) {
	req := QueueListRequest{PaginationRequest: PaginationRequest{Page: 1, PageSize: 100}}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLogRequestValidate(t *testing.T) {
	helpers.GlobalConfig.Log.SyncLogDir = "logs/sync"

	t.Run("旧日志请求通过", func(t *testing.T) {
		req := OldLogsRequest{Path: "app.log", Limit: 100, Direction: "forward"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("日志路径穿越失败", func(t *testing.T) {
		req := LogFileRequest{Path: "../app.log"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("同步任务日志子目录通过", func(t *testing.T) {
		req := OldLogsRequest{Path: "sync/sync_5.log", Limit: 100, Direction: "forward"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("旧同步任务日志子目录兼容通过", func(t *testing.T) {
		req := OldLogsRequest{Path: "libs/sync_5.log", Limit: 100, Direction: "forward"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("日志多级子目录失败", func(t *testing.T) {
		req := OldLogsRequest{Path: "sync/nested/sync_5.log", Limit: 100, Direction: "forward"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("日志非白名单子目录失败", func(t *testing.T) {
		req := OldLogsRequest{Path: "tmp/app.log", Limit: 100, Direction: "forward"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestUpdateRequestValidate(t *testing.T) {
	req := UpdateVersionRequest{Version: "v1.2.3"}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	req = UpdateVersionRequest{Version: "latest"}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestTmpImageRequestValidate(t *testing.T) {
	req := TmpImageRequest{Path: "poster.jpg", MediaType: models.MediaTypeMovie}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	req = TmpImageRequest{Path: "/etc/passwd", MediaType: models.MediaTypeMovie}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestQueueStatsRequestValidate(t *testing.T) {
	req := QueueStatsRequest{TimeWindow: 3600, StartDate: "2026-06-01", EndDate: "2026-06-15"}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	req = QueueStatsRequest{TimeWindow: 59}
	if err := req.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want error")
	}
}

func TestFileBatchRequestValidate(t *testing.T) {
	t.Run("根目录下的空格名称不是根路径", func(t *testing.T) {
		if err := (DeleteFilesRequest{AccountID: 1, FileIDs: []string{"/ "}}).Validate(); err != nil {
			t.Fatalf("合法路径被拒绝：%v", err)
		}
	})

	t.Run("路径型 ID 保留空白并按原文去重", func(t *testing.T) {
		for _, source := range []models.SourceType{models.SourceTypeBaiduPan, models.SourceTypeOpenList} {
			t.Run(string(source), func(t *testing.T) {
				ids := []string{"/Movies/a.mkv ", "/Movies/a.mkv", "/Movies/a.mkv "}
				deletion := DeleteFilesRequest{AccountID: 1, FileIDs: ids}
				transfer := MoveFilesRequest{AccountID: 1, FileIDs: ids, TargetParentID: "/target "}
				for _, got := range [][]string{deletion.NormalizedFileIDs(source), transfer.NormalizedFileIDs(source)} {
					if len(got) != 2 || got[0] != ids[0] || got[1] != ids[1] {
						t.Fatalf("文件身份被修改：%q", got)
					}
				}
				if ids[0] != "/Movies/a.mkv " {
					t.Fatal("原始请求被修改")
				}
			})
		}
	})

	t.Run("批量删除通过并去重", func(t *testing.T) {
		req := DeleteFilesRequest{AccountID: 1, FileIDs: []string{" 11 ", "11", "12"}}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		got := req.NormalizedFileIDs(models.SourceType115)
		if len(got) != 2 || got[0] != "11" || got[1] != "12" {
			t.Fatalf("NormalizedFileIDs() = %+v", got)
		}
	})

	t.Run("批量删除空列表失败", func(t *testing.T) {
		req := DeleteFilesRequest{AccountID: 1}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("批量删除包含无效 ID 失败", func(t *testing.T) {
		for _, fileIDs := range [][]string{{""}, {"0"}, {"/"}, {"1", "  "}} {
			req := DeleteFilesRequest{AccountID: 1, FileIDs: fileIDs}
			if err := req.Validate(); err == nil {
				t.Fatalf("Validate() error = nil, want error for %v", fileIDs)
			}
		}
	})

	t.Run("批量删除超过单页上限失败", func(t *testing.T) {
		fileIDs := make([]string, maxBatchFileIDs+1)
		for i := range fileIDs {
			fileIDs[i] = strconv.Itoa(i + 1)
		}
		req := DeleteFilesRequest{AccountID: 1, FileIDs: fileIDs}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("批量移动通过", func(t *testing.T) {
		req := MoveFilesRequest{AccountID: 1, FileIDs: []string{"1"}, TargetParentID: "0"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("批量移动缺少目标目录失败", func(t *testing.T) {
		req := MoveFilesRequest{AccountID: 1, FileIDs: []string{"1"}, TargetParentID: " "}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("批量复制通过", func(t *testing.T) {
		req := CopyFilesRequest{AccountID: 1, FileIDs: []string{"1"}, TargetParentID: "/dest"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("批量复制缺少账号失败", func(t *testing.T) {
		req := CopyFilesRequest{FileIDs: []string{"1"}, TargetParentID: "/dest"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("重命名通过", func(t *testing.T) {
		req := RenameFileRequest{AccountID: 1, FileID: "11", NewName: " 新名称.mkv "}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("重命名非法新名称失败", func(t *testing.T) {
		for _, newName := range []string{"", "  ", ".", "..", "a/b", "a\\b", "a\x00b"} {
			req := RenameFileRequest{AccountID: 1, FileID: "11", NewName: newName}
			if err := req.Validate(); err == nil {
				t.Fatalf("Validate() error = nil, want error for %q", newName)
			}
		}
	})

	t.Run("重命名保留根文件 ID 校验", func(t *testing.T) {
		req := RenameFileRequest{AccountID: 1, FileID: "0", NewName: "新名称"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}
