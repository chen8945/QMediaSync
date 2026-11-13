package embyclientrestgo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

func normalizeJellyfinID(value string) (string, error) {
	if len(value) != 32 && len(value) != 36 {
		return "", fmt.Errorf("Jellyfin 返回无效的媒体 ID：%q", value)
	}
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", fmt.Errorf("Jellyfin 返回无效的媒体 ID：%q", value)
	}
	return strings.ReplaceAll(id.String(), "-", ""), nil
}

func normalizeJellyfinItemIdentity(item *BaseItemDtoV2) error {
	id, err := normalizeJellyfinID(item.Id)
	if err != nil {
		return err
	}
	item.Id = id
	for _, field := range []*string{&item.ParentId, &item.SeriesId, &item.SeasonId} {
		if *field != "" {
			id, err := normalizeJellyfinID(*field)
			if err != nil {
				return err
			}
			*field = id
		}
	}
	return nil
}

func normalizeJellyfinItem(item *BaseItemDtoV2) error {
	if err := normalizeJellyfinItemIdentity(item); err != nil {
		return err
	}
	if len(item.MediaSources) == 0 {
		return nil
	}
	switch item.Type {
	case "Movie", "Episode", "Video":
	default:
		return nil
	}
	if item.SourceType != "" && item.SourceType != "Library" {
		return errors.New("Jellyfin 媒体来源不属于普通媒体库")
	}
	if item.ChannelID != "" {
		channel, err := uuid.Parse(item.ChannelID)
		if err != nil || channel != uuid.Nil {
			return errors.New("Jellyfin 频道媒体不能作为普通文件关联")
		}
	}
	for i := range item.MediaSources {
		source := &item.MediaSources[i]
		if source.Type != "Default" && source.Type != "Grouping" {
			return errors.New("Jellyfin 返回无法关联到媒体文件的来源")
		}
		id, err := normalizeJellyfinID(source.ID)
		if err != nil {
			return err
		}
		source.ItemID = id
	}
	return nil
}

func normalizeJellyfinVirtualFolders(folders []VirtualFolderDto) error {
	for i := range folders {
		for _, field := range []*string{&folders[i].ID, &folders[i].ItemId, &folders[i].Guid, &folders[i].PrimaryImageItemId} {
			if *field == "" {
				continue
			}
			id, err := normalizeJellyfinID(*field)
			if err != nil {
				return err
			}
			*field = id
		}
	}
	return nil
}

func (c *Client) jellyfinRequest(ctx context.Context, method, path string, params url.Values) (*http.Request, error) {
	req, err := newRequest(ctx, method, strings.TrimRight(c.embyURL, "/")+path, params)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "MediaBrowser Client=\"QMediaSync\", Device=\"QMediaSync\", DeviceId=\"QMediaSync\", Version=\"1\", Token="+strconv.Quote(c.apiKey))
	return req, nil
}

func jellyfinRefreshParams() url.Values {
	return url.Values{"MetadataRefreshMode": {"Default"}, "ImageRefreshMode": {"Default"}}
}

func jellyfinDetailPath(itemID, userID string) (string, url.Values) {
	return "/Items/" + url.PathEscape(itemID), url.Values{"UserId": {userID}}
}

const jellyfinSnapshotFields = "DateCreated,ParentId,Path,MediaSources"

func jellyfinItemsParams(params url.Values, fields string) {
	value := jellyfinSnapshotFields
	if slices.Contains(strings.Split(fields, ","), "MediaStreams") {
		value += ",MediaStreams"
	}
	params.Set("Fields", value)
	params.Set("SortBy", "DateCreated,SortName")
}

// CompleteSTRMSources 仅为异常本地 STRM 来源补读用户态详情；正常原生来源保持原请求数量。
func (c *Client) CompleteSTRMSources(ctx context.Context, item *BaseItemDtoV2) error {
	if !c.isJellyfin() || !strings.EqualFold(path.Ext(item.Path), ".strm") || len(item.MediaSources) == 0 {
		return nil
	}
	needsDetail := false
	for _, source := range item.MediaSources {
		if source.ItemID == item.Id && strings.EqualFold(path.Ext(source.Path), ".strm") && !strings.Contains(source.Path, "://") {
			needsDetail = true
		}
	}
	if !needsDetail {
		return nil
	}
	users, err := c.GetUsersWithAllLibrariesAccessContext(ctx)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return errors.New("没有可读取 STRM 来源的 Jellyfin 用户")
	}
	detail, err := c.GetItemDetailByUserContext(ctx, item.Id, users[0].ID)
	if err != nil {
		return err
	}
	if detail.Path != item.Path || detail.Type != item.Type {
		return errors.New("Jellyfin STRM 详情的物理身份已变化")
	}
	if err := normalizeJellyfinItem(detail); err != nil {
		return err
	}
	for i := range item.MediaSources {
		source := &item.MediaSources[i]
		if source.ItemID != item.Id || !strings.EqualFold(path.Ext(source.Path), ".strm") || strings.Contains(source.Path, "://") {
			continue
		}
		for _, candidate := range detail.MediaSources {
			if candidate.ID == source.ID && candidate.ItemID == item.Id && (strings.HasPrefix(candidate.Path, "http://") || strings.HasPrefix(candidate.Path, "https://")) {
				*source = candidate
				break
			}
		}
	}
	return nil
}
