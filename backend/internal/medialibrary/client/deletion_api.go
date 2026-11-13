package embyclientrestgo

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// GetDeletionVerificationItems 获取无用户、无选库过滤的完整物理清单。
// IDs 非空时同时允许季、剧父项；显式空数组和总数才可证明查询结果为空。
func (c *Client) GetDeletionVerificationItems(ctx context.Context, ids string) ([]BaseItemDtoV2, error) {
	if _, err := c.CheckConnection(ctx); err != nil {
		return nil, err
	}
	jellyfin := c.isJellyfin()
	requested := map[string]bool{}
	if jellyfin && ids != "" {
		var normalized []string
		for _, value := range strings.Split(ids, ",") {
			id, err := normalizeJellyfinID(value)
			if err != nil {
				return nil, err
			}
			if !requested[id] {
				normalized = append(normalized, id)
				requested[id] = true
			}
		}
		ids = strings.Join(normalized, ",")
	}
	var items []BaseItemDtoV2
	seen := map[string]bool{}
	total := -1
	for start := 0; ; {
		params := url.Values{
			"Recursive": {"true"}, "StartIndex": {strconv.Itoa(start)}, "Limit": {"100"},
			"Fields": {EmbySnapshotFields}, "SortBy": {"Id"}, "SortOrder": {"Ascending"},
			"IncludeItemTypes": {"Movie,Video,Episode"},
		}
		if jellyfin {
			jellyfinItemsParams(params, "")
			params.Set("EnableTotalRecordCount", "true")
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
			return nil, errors.New("媒体删除前的文件清单缺少完整分页字段")
		}
		if total == -1 {
			total = *page.Total
		}
		if total != *page.Total || start+len(*page.Items) > total || len(*page.Items) == 0 && start < total {
			return nil, errors.New("媒体删除前的文件清单分页不完整或已变化")
		}
		for _, item := range *page.Items {
			if jellyfin {
				if err := normalizeJellyfinItem(&item); err != nil {
					return nil, err
				}
				if ids != "" && !requested[item.Id] {
					return nil, errors.New("删除前查询返回了未请求的条目")
				}
			}
			if item.Id == "" || item.Type == "" || seen[item.Id] {
				return nil, errors.New("媒体删除前的文件清单存在身份缺失或重复")
			}
			seen[item.Id] = true
			items = append(items, item)
		}
		start += len(*page.Items)
		if start == total {
			if jellyfin && ids != "" {
				return c.completeDeletionIDs(ctx, items, requested)
			}
			return items, nil
		}
	}
}

// 集合查询隐藏合并版本；只有合法用户的单项 404 才能补证缺失。
func (c *Client) completeDeletionIDs(ctx context.Context, items []BaseItemDtoV2, requested map[string]bool) ([]BaseItemDtoV2, error) {
	for _, item := range items {
		delete(requested, item.Id)
	}
	if len(requested) == 0 {
		return items, nil
	}
	users, err := c.GetUsersWithAllLibrariesAccessContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, errors.New("没有可核验删除目标的媒体库用户")
	}
	for id := range requested {
		var item BaseItemDtoV2
		path, params := jellyfinDetailPath(id, users[0].ID)
		status, err := c.getJSON(ctx, path, params, &item, true)
		if status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := normalizeJellyfinItem(&item); err != nil {
			return nil, err
		}
		if item.Id != id {
			return nil, errors.New("删除前详情返回了其他条目")
		}
		items = append(items, item)
	}
	return items, nil
}
