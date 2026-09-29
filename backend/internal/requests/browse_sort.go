package requests

import (
	"slices"

	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

// BrowseSortSelection 表示浏览偏好，不使用上游实际排序回显覆盖用户选择。
type BrowseSortSelection struct {
	SortBy       string `json:"sort_by"`
	SortOrder    string `json:"sort_order"`
	FoldersFirst *bool  `json:"folders_first,omitempty"`
}

// BrowseSortOptions 是文件或目录浏览可用的排序能力。
type BrowseSortOptions struct {
	Fields       []string            `json:"fields"`
	FoldersFirst bool                `json:"folders_first"`
	Default      BrowseSortSelection `json:"default"`
}

type browseSortField struct {
	name      string
	parameter string
}

// browseSortFields 同时作为能力展示、请求校验和上游字段映射的来源。
func browseSortFields(source models.SourceType) []browseSortField {
	switch source {
	case models.SourceType115:
		return []browseSortField{
			{name: "name", parameter: "file_name"},
			{name: "time", parameter: "user_utime"},
			{name: "size", parameter: "file_size"},
			{name: "type", parameter: "file_type"},
			{name: "default"},
		}
	case models.SourceTypeBaiduPan:
		return []browseSortField{
			{name: "name", parameter: "name"},
			{name: "time", parameter: "time"},
			{name: "size", parameter: "size"},
		}
	case models.SourceTypeOpenList:
		return []browseSortField{{name: "default"}}
	case models.SourceTypeLocal:
		return []browseSortField{{name: "name"}, {name: "time"}}
	default:
		return nil
	}
}

// BrowseSortOptionsFor 返回当前浏览场景的能力，不访问远端。
func BrowseSortOptionsFor(source models.SourceType, scope string) (BrowseSortOptions, error) {
	if scope != "files" && scope != "directories" {
		return BrowseSortOptions{}, validation.New("scope", "仅支持 files 或 directories")
	}
	fields := browseSortFields(source)
	if len(fields) == 0 || (source == models.SourceTypeLocal && scope == "files") {
		return BrowseSortOptions{}, validation.New("source_type", "不支持的浏览来源")
	}
	options := BrowseSortOptions{Fields: make([]string, 0, len(fields))}
	for _, field := range fields {
		if scope == "directories" && (field.name == "size" || field.name == "type") {
			continue
		}
		options.Fields = append(options.Fields, field.name)
	}
	options.Default = BrowseSortSelection{SortBy: options.Fields[0], SortOrder: "asc"}
	if source == models.SourceType115 && scope == "files" {
		options.FoldersFirst = true
		first := true
		options.Default.FoldersFirst = &first
	}
	return options, nil
}

// ValidateBrowseSort 检查显式排序组合；省略排序字段保留旧调用的默认行为。
func ValidateBrowseSort(source models.SourceType, scope, sortBy, sortOrder string, foldersFirst *bool) error {
	options, err := BrowseSortOptionsFor(source, scope)
	if err != nil {
		return err
	}
	if sortBy != "" && !slices.Contains(options.Fields, sortBy) {
		return validation.New("sort_by", "当前来源或浏览场景不支持此排序字段")
	}
	if sortOrder != "" && sortOrder != "asc" && sortOrder != "desc" {
		return validation.New("sort_order", "不支持的排序方向")
	}
	// asc 是旧文件列表客户端的占位默认值，跟随网盘时不向上游发送。
	if sortBy == "default" && (sortOrder == "desc" || foldersFirst != nil) {
		return validation.New("sort_by", "跟随网盘时不能指定降序或文件夹置顶")
	}
	if foldersFirst != nil && !options.FoldersFirst {
		return validation.New("folders_first", "当前来源或浏览场景不支持文件夹置顶开关")
	}
	if scope == "directories" && sortBy == "" && sortOrder != "" {
		return validation.New("sort_by", "指定排序方向时必须提供排序字段")
	}
	return nil
}

// BrowseSortParameter 返回已支持字段对应的上游参数值。
func BrowseSortParameter(source models.SourceType, sortBy string) (string, error) {
	for _, field := range browseSortFields(source) {
		if field.name == sortBy {
			return field.parameter, nil
		}
	}
	return "", validation.New("sort_by", "当前来源不支持此排序字段")
}

// BrowseSortOptionsRequest 查询排序能力。
type BrowseSortOptionsRequest struct {
	SourceType models.SourceType `form:"source_type"`
	Scope      string            `form:"scope"`
}
