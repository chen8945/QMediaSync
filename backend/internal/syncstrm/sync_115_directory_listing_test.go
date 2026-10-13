package syncstrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

// 目录列表必须保留过滤前的完整事实；这些响应校验不代表服务端提供事务快照。
func Test115DirectoryListingRejectsIncompleteResponses(t *testing.T) {
	requestErr := errors.New("directory page unavailable")
	for _, tt := range []struct {
		name   string
		total  int
		offset int
		change func(*v115open.FileListResp) (*v115open.FileListResp, error)
		cause  error
	}{
		{name: "响应为空", total: 1, change: func(*v115open.FileListResp) (*v115open.FileListResp, error) { return nil, nil }},
		{name: "总数为负数", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Count = -1
			return resp, nil
		}},
		{name: "零总数却返回目录", change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data = directoryListing115Page(0, 100, 1).Data
			return resp, nil
		}},
		{name: "非零总数首轮返回空页", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data = nil
			return resp, nil
		}},
		{name: "后续分页为空", total: 101, offset: 100, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data = nil
			return resp, nil
		}},
		{name: "中间分页缺少目录", total: 250, offset: 100, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data = resp.Data[:99]
			return resp, nil
		}},
		{name: "末页超出总数", total: 101, offset: 100, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data = append(resp.Data, directoryListing115Page(101, 100, 102).Data...)
			return resp, nil
		}},
		{name: "分页总数变化", total: 101, offset: 100, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Count++
			return resp, nil
		}},
		{name: "同页重复目录身份", total: 2, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[1].FileId = resp.Data[0].FileId
			return resp, nil
		}},
		{name: "跨页重复目录身份", total: 101, offset: 100, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileId = "1000"
			return resp, nil
		}},
		{name: "目录身份缺失", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileId = ""
			return resp, nil
		}},
		{name: "网盘根不能成为子目录", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileId = "0"
			return resp, nil
		}},
		{name: "父目录不能成为自己的子目录", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileId = "10"
			return resp, nil
		}},
		{name: "回收站目录不能解释为空目录", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].Aid = "7"
			return resp, nil
		}},
		{name: "缺失状态不能解释为空目录", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].Aid = ""
			return resp, nil
		}},
		{name: "未知类型不能解释为空目录", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileCategory = ""
			return resp, nil
		}},
		{name: "子目录缺失父身份", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].Pid = ""
			return resp, nil
		}},
		{name: "子目录属于其他父目录", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].Pid = "20"
			return resp, nil
		}},
		{name: "混合列表文件属于其他父目录", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileCategory = v115open.TypeFile
			resp.Data[0].Pid = "20"
			return resp, nil
		}},
		{name: "混合列表文件名称无效", total: 1, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileCategory = v115open.TypeFile
			resp.Data[0].FileName = ".."
			return resp, nil
		}},
		{name: "混合列表文件和目录身份重复", total: 2, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Data[0].FileCategory = v115open.TypeFile
			resp.Data[0].FileId = resp.Data[1].FileId
			return resp, nil
		}},
		{name: "空列表缺失父目录链", change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Path = nil
			return resp, nil
		}},
		{name: "空列表属于其他父目录", change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Path[1].FileId = "20"
			return resp, nil
		}},
		{name: "请求目录只出现在祖先链中间", change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Path = append(resp.Path, v115open.FileParentPath{FileId: "20", Name: "Other"})
			resp.PathStr = "Media/Other"
			return resp, nil
		}},
		{name: "后续页父目录链变化", total: 101, offset: 100, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Path[1].FileId = "20"
			return resp, nil
		}},
		{name: "后续页路径变化", total: 101, offset: 100, change: func(resp *v115open.FileListResp) (*v115open.FileListResp, error) {
			resp.Path[1].Name = "Moved"
			resp.PathStr = "Moved"
			return resp, nil
		}},
		{name: "后续页请求失败", total: 101, offset: 100, cause: requestErr, change: func(*v115open.FileListResp) (*v115open.FileListResp, error) {
			return nil, requestErr
		}},
		{name: "后续页取消", total: 101, offset: 100, cause: context.Canceled, change: func(*v115open.FileListResp) (*v115open.FileListResp, error) {
			return nil, context.Canceled
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			driver := new115DirectoryListingDriver(t)
			var offsets []int
			list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, parentID string, showCurrent, onlyDirs, showDirs bool, offset, limit int) (*v115open.FileListResp, error) {
				if parentID != "10" || !showCurrent || !onlyDirs || !showDirs || limit != 100 {
					t.Fatalf("目录请求参数错误：parent=%s current=%v onlyDirs=%v showDirs=%v limit=%d", parentID, showCurrent, onlyDirs, showDirs, limit)
				}
				offsets = append(offsets, offset)
				resp := directoryListing115Page(offset, limit, tt.total)
				if offset == tt.offset {
					return tt.change(resp)
				}
				return resp, nil
			}
			dirs, err := driver.GetDirsByPathId(t.Context(), "10")
			if err == nil || len(dirs) != 0 {
				t.Fatalf("不完整目录列表应返回错误且不暴露部分结果：dirs=%d error=%v", len(dirs), err)
			}
			if tt.cause != nil && !errors.Is(err, tt.cause) {
				t.Fatalf("目录读取错误丢失原始错误：%v", err)
			}
			wantOffsets := []int{0}
			if tt.offset > 0 {
				wantOffsets = append(wantOffsets, tt.offset)
			}
			if !slices.Equal(offsets, wantOffsets) {
				t.Fatalf("请求分页=%v，期望=%v", offsets, wantOffsets)
			}
		})
	}
}

func Test115DirectoryListingReturnsAllDirectoriesBeforeExclusions(t *testing.T) {
	for _, tt := range []struct {
		name  string
		total int
	}{
		{name: "合法空目录"},
		{name: "完整单页", total: 1},
		{name: "完整多页", total: 250},
	} {
		t.Run(tt.name, func(t *testing.T) {
			driver := new115DirectoryListingDriver(t)
			driver.s.Config.ExcludeNames = []string{"directory-0"}
			var offsets []int
			list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, _ string, _, _, _ bool, offset, limit int) (*v115open.FileListResp, error) {
				offsets = append(offsets, offset)
				return directoryListing115Page(offset, limit, tt.total), nil
			}
			dirs, err := driver.GetDirsByPathId(t.Context(), "10")
			if err != nil || len(dirs) != tt.total {
				t.Fatalf("完整目录列表：dirs=%d，期望=%d，error=%v", len(dirs), tt.total, err)
			}
			wantOffsets := []int{0}
			if tt.total > 100 {
				wantOffsets = append(wantOffsets, 100, 200)
			}
			if !slices.Equal(offsets, wantOffsets) {
				t.Fatalf("请求分页=%v，期望=%v", offsets, wantOffsets)
			}
			for i, dir := range dirs {
				if dir.PathId != fmt.Sprint(1000+i) || dir.Path != fmt.Sprintf("Media/Directory-%d", i) || dir.Mtime != 200 {
					t.Fatalf("目录 %d 的身份、路径或修改时间错误：%+v", i, dir)
				}
			}
			if tt.total > 0 && !driver.s.IsExcludePath(dirs[0].Path) {
				t.Fatal("夹具首个目录应命中排除规则，且仍保留在原始目录列表中")
			}
		})
	}
}

func Test115DirectoryListingAcceptsMixedPages(t *testing.T) {
	for _, total := range []int{1, 101, 250} {
		t.Run(fmt.Sprint(total), func(t *testing.T) {
			driver := new115DirectoryListingDriver(t)
			var offsets []int
			list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, _ string, _, _, _ bool, offset, limit int) (*v115open.FileListResp, error) {
				offsets = append(offsets, offset)
				resp := directoryListing115Page(offset, limit, total)
				for i := range resp.Data {
					// 第一页全部是文件，后续页才出现目录。
					if offset == 0 || i%2 == 1 {
						resp.Data[i].FileCategory = v115open.TypeFile
					}
				}
				return resp, nil
			}
			dirs, err := driver.GetDirsByPathId(t.Context(), "10")
			wantDirs := max(0, (total-100+1)/2)
			if err != nil || len(dirs) != wantDirs {
				t.Fatalf("混合列表目录数=%d，期望=%d，错误=%v", len(dirs), wantDirs, err)
			}
			wantOffsets := []int{0}
			for offset := 100; offset < total; offset += 100 {
				wantOffsets = append(wantOffsets, offset)
			}
			if !slices.Equal(offsets, wantOffsets) {
				t.Fatalf("混合列表分页=%v，期望=%v", offsets, wantOffsets)
			}
			for i, dir := range dirs {
				if dir.PathId != fmt.Sprint(1100+2*i) {
					t.Fatalf("混合列表返回了错误目录：%+v", dir)
				}
			}
		})
	}
}

func new115DirectoryListingDriver(t *testing.T) *open115Driver {
	t.Helper()
	originalList, originalSettings := list115FilesPage, models.SettingsGlobal
	t.Cleanup(func() { list115FilesPage, models.SettingsGlobal = originalList, originalSettings })
	models.SettingsGlobal = &models.Settings{FileListPageSize: 100}
	driver := NewOpen115Driver(nil)
	driver.SetSyncStrm(&SyncStrm{
		SourcePath: "Media", SourcePathId: "10",
		Sync: &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
	})
	return driver
}

func directoryListing115Page(offset, limit, total int) *v115open.FileListResp {
	resp := &v115open.FileListResp{
		Count: total, PathStr: "Media",
		Path: []v115open.FileParentPath{{FileId: json.Number("0")}, {FileId: json.Number("10"), Name: "Media"}},
	}
	for i := offset; i < min(offset+limit, total); i++ {
		resp.Data = append(resp.Data, v115open.File{
			FileId: fmt.Sprint(1000 + i), Pid: "10", Aid: "1", FileCategory: v115open.TypeDir,
			FileName: fmt.Sprintf("Directory-%d", i), Utime: 200, Ptime: 100,
		})
	}
	return resp
}
