package embyclientrestgo

import (
	"context"
	"errors"
	"net/url"
	"strconv"
)

// GetDeletionVerificationItems 获取无用户、无选库过滤的完整物理清单。
// IDs 非空时同时允许季、剧父项；显式空数组和总数才可证明查询结果为空。
func (c *Client) GetDeletionVerificationItems(ctx context.Context, ids string) ([]BaseItemDtoV2, error) {
	var items []BaseItemDtoV2
	seen := map[string]bool{}
	total := -1
	for start := 0; ; {
		params := url.Values{
			"Recursive": {"true"}, "StartIndex": {strconv.Itoa(start)}, "Limit": {"100"},
			"Fields": {EmbySnapshotFields}, "SortBy": {"Id"}, "SortOrder": {"Ascending"},
			"IncludeItemTypes": {"Movie,Video,Episode"},
		}
		if ids != "" {
			params.Set("Ids", ids)
			params.Set("IncludeItemTypes", "Movie,Video,Episode,Season,Series")
		}
		var page struct {
			Items *[]BaseItemDtoV2 `json:"Items"`
			Total *int             `json:"TotalRecordCount"`
		}
		if err := c.getSnapshotJSON(ctx, "/Items", params, &page); err != nil {
			return nil, err
		}
		if page.Items == nil || page.Total == nil || *page.Total < 0 {
			return nil, errors.New("Emby 删除核验清单缺少完整分页字段")
		}
		if total == -1 {
			total = *page.Total
		}
		if total != *page.Total || start+len(*page.Items) > total || len(*page.Items) == 0 && start < total {
			return nil, errors.New("Emby 删除核验清单分页不完整或发生变化")
		}
		for _, item := range *page.Items {
			if item.Id == "" || item.Type == "" || seen[item.Id] {
				return nil, errors.New("Emby 删除核验清单身份缺失或重复")
			}
			seen[item.Id] = true
			items = append(items, item)
		}
		start += len(*page.Items)
		if start == total {
			return items, nil
		}
	}
}
