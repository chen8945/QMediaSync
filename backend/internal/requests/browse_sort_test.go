package requests

import (
	"slices"
	"testing"

	"qmediasync/internal/models"
)

func TestBrowseSortCapabilitiesAndMappings(t *testing.T) {
	for _, tc := range []struct {
		name       string
		source     models.SourceType
		scope      string
		fields     []string
		parameters []string
		first      bool
	}{
		{name: "115 文件", source: models.SourceType115, scope: "files", fields: []string{"name", "time", "size", "type", "default"}, parameters: []string{"file_name", "user_utime", "file_size", "file_type", ""}, first: true},
		{name: "115 目录", source: models.SourceType115, scope: "directories", fields: []string{"name", "time", "default"}, parameters: []string{"file_name", "user_utime", ""}},
		{name: "百度文件", source: models.SourceTypeBaiduPan, scope: "files", fields: []string{"name", "time", "size"}, parameters: []string{"name", "time", "size"}},
		{name: "百度目录", source: models.SourceTypeBaiduPan, scope: "directories", fields: []string{"name", "time"}, parameters: []string{"name", "time"}},
		{name: "OpenList 文件", source: models.SourceTypeOpenList, scope: "files", fields: []string{"default"}, parameters: []string{""}},
		{name: "OpenList 目录", source: models.SourceTypeOpenList, scope: "directories", fields: []string{"default"}, parameters: []string{""}},
		{name: "本地目录", source: models.SourceTypeLocal, scope: "directories", fields: []string{"name", "time"}, parameters: []string{"", ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			options, err := BrowseSortOptionsFor(tc.source, tc.scope)
			if err != nil || !slices.Equal(options.Fields, tc.fields) || options.FoldersFirst != tc.first {
				t.Fatalf("能力 = %+v，错误 = %v", options, err)
			}
			if options.Default.SortBy != tc.fields[0] || options.Default.SortOrder != "asc" || (options.Default.FoldersFirst != nil) != tc.first {
				t.Fatalf("默认值 = %+v", options.Default)
			}
			for i, field := range options.Fields {
				parameter, err := BrowseSortParameter(tc.source, field)
				if err != nil || parameter != tc.parameters[i] {
					t.Fatalf("字段 %s 映射 = %q，错误 = %v", field, parameter, err)
				}
				if err := ValidateBrowseSort(tc.source, tc.scope, field, "", nil); err != nil {
					t.Fatalf("能力接口声明的字段 %s 被校验拒绝：%v", field, err)
				}
			}
		})
	}
}

func TestBrowseSortRejectsUnsupportedCombinations(t *testing.T) {
	first := false
	for _, tc := range []struct {
		name                string
		source              models.SourceType
		scope, field, order string
		first               *bool
	}{
		{name: "未知来源", source: "123", scope: "files"},
		{name: "未知场景", source: models.SourceType115, scope: "tree"},
		{name: "本地文件入口", source: models.SourceTypeLocal, scope: "files"},
		{name: "目录大小", source: models.SourceType115, scope: "directories", field: "size"},
		{name: "目录置顶开关", source: models.SourceType115, scope: "directories", field: "name", first: &first},
		{name: "百度类型", source: models.SourceTypeBaiduPan, scope: "files", field: "type"},
		{name: "百度跟随网盘", source: models.SourceTypeBaiduPan, scope: "files", field: "default"},
		{name: "百度取消置顶", source: models.SourceTypeBaiduPan, scope: "files", field: "name", first: &first},
		{name: "OpenList名称", source: models.SourceTypeOpenList, scope: "files", field: "name"},
		{name: "跟随模式降序", source: models.SourceType115, scope: "files", field: "default", order: "desc"},
		{name: "跟随模式置顶", source: models.SourceType115, scope: "files", field: "default", first: &first},
		{name: "无字段目录方向", source: models.SourceType115, scope: "directories", order: "desc"},
		{name: "错误方向", source: models.SourceType115, scope: "files", field: "time", order: "down"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateBrowseSort(tc.source, tc.scope, tc.field, tc.order, tc.first); err == nil {
				t.Fatal("不支持的组合应被拒绝")
			}
		})
	}
}

func TestPathListSortValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		req     PathListRequest
		invalid bool
	}{
		{name: "旧调用保持不传排序", req: PathListRequest{SourceType: models.SourceType115, AccountID: 1}},
		{name: "新调用时间降序刷新", req: PathListRequest{SourceType: models.SourceType115, AccountID: 1, SortBy: "time", SortOrder: "desc", Refresh: 1}},
		{name: "刷新非法值", req: PathListRequest{SourceType: models.SourceTypeLocal, Refresh: 2}, invalid: true},
		{name: "非法目录类型排序", req: PathListRequest{SourceType: models.SourceType115, AccountID: 1, SortBy: "type"}, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.req.Validate(); (err != nil) != tc.invalid {
				t.Fatalf("校验结果 = %v", err)
			}
		})
	}
}
