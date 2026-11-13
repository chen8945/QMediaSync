package embyclientrestgo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"time"
)

// EmbyLibrary 表示 Emby 中的单个媒体库。
type EmbyLibrary struct {
	Name           string `json:"Name"`
	ID             string `json:"Id"`
	Type           string `json:"Type,omitempty"`
	CollectionType string `json:"CollectionType,omitempty"`
}

// EmbyLibrariesResponse 是 /Library/MediaFolders 端点响应的结构。
type EmbyLibrariesResponse struct {
	Items []EmbyLibrary `json:"Items"`
}

// UserPolicy 表示 Emby 用户策略配置。
type UserPolicy struct {
	// 是否允许访问所有文件夹。
	EnableAllFolders    bool `json:"EnableAllFolders"`
	EnableMediaPlayback bool `json:"EnableMediaPlayback"`
}

// UserDto 表示 Emby 用户。
type UserDto struct {
	Name   string     `json:"Name"`
	ID     string     `json:"Id"`
	Policy UserPolicy `json:"Policy"`
}

type PersonDto struct {
	ID   string `json:"Id,omitempty"`
	Name string `json:"Name,omitempty"`
	Role string `json:"Role,omitempty"`
	Type string `json:"Type,omitempty"` // e.g., Actor, Director
}

type BaseItemDtoV2 struct {
	Name string `json:"Name,omitempty"`
	// Emby 项目 ID。
	Id                string            `json:"Id,omitempty"`
	MediaStreams      []MediaStreamV2   `json:"MediaStreams,omitempty"`
	Type              string            `json:"Type,omitempty"`
	ParentId          string            `json:"ParentId,omitempty"`
	SeriesId          string            `json:"SeriesId,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	SeasonId          string            `json:"SeasonId,omitempty"`
	SeasonName        string            `json:"SeasonName,omitempty"`
	Path              string            `json:"Path,omitempty"`
	SourceType        string            `json:"SourceType,omitempty"`
	ChannelID         string            `json:"ChannelId,omitempty"`
	IndexNumber       int               `json:"IndexNumber,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	PremiereDate      string            `json:"PremiereDate,omitempty"`
	DateCreated       string            `json:"DateCreated,omitempty"`
	DateModified      string            `json:"DateModified,omitempty"`
	IsFolder          bool              `json:"IsFolder,omitempty"`
	PartCount         int               `json:"PartCount,omitempty"`
	MediaSources      []MediaSource     `json:"MediaSources,omitempty"`
	CommunityRating   float64           `json:"CommunityRating,omitempty"`
	Genres            []string          `json:"Genres,omitempty"`
	People            []PersonDto       `json:"People,omitempty"`
	Overview          string            `json:"Overview,omitempty"`
	ImageTags         map[string]string `json:"ImageTags,omitempty"`
	ProviderIDs       map[string]string `json:"ProviderIds,omitempty"`
	RunTimeTicks      int64             `json:"RunTimeTicks,omitempty"`
	BackdropImageTags []string          `json:"BackdropImageTags,omitempty"`
}

type MediaSource struct {
	ID           string          `json:"Id,omitempty"`
	Type         string          `json:"Type,omitempty"`
	ItemID       string          `json:"ItemId,omitempty"`
	Path         string          `json:"Path,omitempty"`
	Name         string          `json:"Name,omitempty"`
	Container    string          `json:"Container,omitempty"`
	Protocol     string          `json:"Protocol,omitempty"`
	RunTimeTicks int64           `json:"RunTimeTicks,omitempty"`
	Size         int64           `json:"Size,omitempty"`
	MediaStreams []MediaStreamV2 `json:"MediaStreams,omitempty"`
}

type MediaStreamV2 struct {
	// 编码格式，对应探测字段 codec_name，适用于视频、音频和字幕流。
	Codec        string `json:"Codec,omitempty"`
	Type         string `json:"Type,omitempty"`
	Index        int    `json:"Index,omitempty"`
	Language     string `json:"Language,omitempty"`
	DisplayTitle string `json:"DisplayTitle,omitempty"`
	Width        int    `json:"Width,omitempty"`
	Height       int    `json:"Height,omitempty"`
	BitRate      int    `json:"BitRate,omitempty"`
	Channels     int    `json:"Channels,omitempty"`
}

type QueryResultBaseItemDto struct {
	Items            []BaseItemDtoV2 `json:"Items,omitempty"`
	TotalRecordCount int32           `json:"TotalRecordCount,omitempty"`
}

// EmbyItemsQuery 表示查询 Emby 媒体条目的分页参数。
type EmbyItemsQuery struct {
	LibraryID         string
	UserID            string
	StartIndex        int
	Limit             int
	MinDateLastSaved  string
	SortBy            string
	SortOrder         string
	IncludeItemTypes  string
	Fields            string
	IDs               string
	LastDateCreatedAt int64
}

type AncestorDto struct {
	ID          string `json:"Id,omitempty"`
	Name        string `json:"Name,omitempty"`
	Path        string `json:"Path,omitempty"`
	FileName    string `json:"FileName,omitempty"`
	IsFolder    bool   `json:"IsFolder,omitempty"`
	ParentId    string `json:"ParentId,omitempty"`
	Type        string `json:"Type,omitempty"`
	IndexNumber *int   `json:"IndexNumber,omitempty"`
}

type VirtualFolderDto struct {
	ID                 string   `json:"Id,omitempty"`
	ItemId             string   `json:"ItemId,omitempty"`
	Guid               string   `json:"Guid,omitempty"`
	PrimaryImageItemId string   `json:"PrimaryImageItemId,omitempty"`
	PrimaryImageTag    string   `json:"PrimaryImageTag,omitempty"`
	Name               string   `json:"Name,omitempty"`
	CollectionType     string   `json:"CollectionType,omitempty"`
	Locations          []string `json:"Locations,omitempty"`
}

// GetAllMediaLibraries 从 Emby 服务器检索所有媒体库。
func (c *Client) GetAllMediaLibraries() ([]EmbyLibrary, error) {
	return c.GetAllMediaLibrariesContext(context.Background())
}

// GetAllMediaLibrariesContext 查询媒体库并保留调用取消。
func (c *Client) GetAllMediaLibrariesContext(ctx context.Context) ([]EmbyLibrary, error) {
	libraries, err := c.getMediaLibraries(ctx)
	if err != nil {
		return nil, err
	}
	jellyfin := c.isJellyfin()
	libraries = slices.DeleteFunc(libraries, func(library EmbyLibrary) bool {
		return library.CollectionType == "boxsets" || jellyfin && (library.CollectionType == "playlists" || library.Type == "ManualPlaylistsFolder")
	})
	if jellyfin {
		for i := range libraries {
			id, err := normalizeJellyfinID(libraries[i].ID)
			if err != nil {
				return nil, err
			}
			libraries[i].ID = id
		}
	}
	return libraries, nil
}

func (c *Client) getMediaLibraries(ctx context.Context) ([]EmbyLibrary, error) {
	if _, err := c.CheckConnection(ctx); err != nil {
		return nil, err
	}
	var response struct {
		Items *[]EmbyLibrary `json:"Items"`
	}
	if err := c.getSnapshotJSON(ctx, "/Library/MediaFolders", nil, &response); err != nil {
		return nil, err
	}
	if response.Items == nil {
		return nil, errors.New("媒体库响应缺少完整的条目列表")
	}
	return *response.Items, nil
}

// GetMediaItemsByLibraryID 从指定的媒体库中检索所有媒体项目。
// 兼容旧调用方：保留媒体信息提取依赖的顶层 MediaStreams，并聚合流式分页结果。
func (c *Client) GetMediaItemsByLibraryID(libraryID string, lastDateCreatedTime int64) ([]BaseItemDtoV2, error) {
	var allItems []BaseItemDtoV2
	err := c.FetchMediaItemsByLibraryID(
		context.Background(),
		EmbyItemsQuery{
			LibraryID:         libraryID,
			LastDateCreatedAt: lastDateCreatedTime,
			Fields:            EmbySnapshotFields + ",MediaStreams",
		},
		func(item BaseItemDtoV2) error {
			allItems = append(allItems, item)
			return nil
		},
	)
	return allItems, err
}

// GetAdditionalParts 获取隐藏分段并直接请求物理快照字段。
func (c *Client) GetAdditionalParts(ctx context.Context, itemID string) ([]BaseItemDtoV2, error) {
	info, err := c.CheckConnection(ctx)
	if err != nil {
		return nil, err
	}
	var result struct {
		Items *[]BaseItemDtoV2 `json:"Items"`
		Total *int32           `json:"TotalRecordCount"`
	}
	fields := EmbySnapshotFields
	if info.Product == ProductJellyfin {
		fields = jellyfinSnapshotFields
	}
	params := url.Values{"Fields": {fields}}
	if info.Product == ProductJellyfin {
		params = nil
	}
	if err := c.getSnapshotJSON(ctx, "/Videos/"+url.PathEscape(itemID)+"/AdditionalParts", params, &result); err != nil {
		return nil, err
	}
	if result.Items == nil || result.Total == nil || *result.Total < 0 || int(*result.Total) != len(*result.Items) {
		return nil, errors.New("媒体服务器 AdditionalParts 响应不完整")
	}
	if info.Product == ProductJellyfin {
		for i := range *result.Items {
			if err := normalizeJellyfinItem(&(*result.Items)[i]); err != nil {
				return nil, err
			}
		}
	}
	seen := map[string]bool{}
	for _, part := range *result.Items {
		if part.Id == "" || seen[part.Id] {
			return nil, errors.New("媒体分段身份缺失或重复")
		}
		seen[part.Id] = true
	}
	return *result.Items, nil
}

// GetSnapshotItems 按 ID 补查缺少字段的物理条目，不依赖用户态聚合接口。
func (c *Client) GetSnapshotItems(ctx context.Context, ids string) ([]BaseItemDtoV2, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var result []BaseItemDtoV2
	err := c.FetchMediaItemsByLibraryID(ctx, EmbyItemsQuery{IDs: ids, Fields: EmbySnapshotFields}, func(item BaseItemDtoV2) error {
		result = append(result, item)
		return nil
	})
	if err != nil || !c.isJellyfin() || ids == "" {
		return result, err
	}
	returned := make(map[string]bool, len(result))
	for _, item := range result {
		returned[item.Id] = true
	}
	var missing []string
	for _, requested := range strings.Split(ids, ",") {
		id, err := normalizeJellyfinID(requested)
		if err != nil {
			return nil, err
		}
		if !returned[id] {
			missing = append(missing, id)
			returned[id] = true
		}
	}
	if len(missing) == 0 {
		return result, nil
	}
	// Jellyfin 的集合查询隐藏自动合并的版本；单项详情仍可按原 ID 读取。
	users, err := c.GetUsersWithAllLibrariesAccessContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, errors.New("没有找到可读取隐藏版本的媒体库用户")
	}
	params := url.Values{"UserId": {users[0].ID}}
	for _, id := range missing {
		var item BaseItemDtoV2
		if err := c.getSnapshotJSON(ctx, "/Items/"+url.PathEscape(id), params, &item); err != nil {
			return nil, err
		}
		if err := normalizeJellyfinItem(&item); err != nil {
			return nil, err
		}
		if item.Id != id {
			return nil, errors.New("Jellyfin 版本详情返回了其他媒体条目")
		}
		result = append(result, item)
	}
	return result, err
}

// FetchMediaItemsByLibraryID 从指定媒体库分页拉取媒体条目，并逐条回调处理。
// 索引入口先核验服务器；通知和媒体提取沿用既有请求，不额外探测。
func (c *Client) FetchMediaItemsByLibraryID(
	ctx context.Context,
	query EmbyItemsQuery,
	handle func(item BaseItemDtoV2) error,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handle == nil {
		return errors.New("handle 不能为空")
	}

	if _, err := c.CheckConnection(ctx); err != nil {
		return err
	}
	jellyfin := c.isJellyfin()
	startIndex := query.StartIndex
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	expectedTotal := -1
	seen := make(map[string]struct{})
	for {
		response, err := c.fetchMediaItemsPage(ctx, query, startIndex, limit)
		if err != nil {
			return err
		}
		if expectedTotal < 0 {
			expectedTotal = int(response.TotalRecordCount)
		} else if expectedTotal != int(response.TotalRecordCount) {
			return errors.New("媒体服务器分页过程中总条目数发生变化")
		}
		if len(response.Items) == 0 && startIndex < expectedTotal {
			return fmt.Errorf("媒体服务器分页提前返回空页：%d/%d", startIndex, expectedTotal)
		}
		if startIndex+len(response.Items) > expectedTotal {
			return errors.New("媒体服务器分页条目数超出 TotalRecordCount")
		}
		for _, item := range response.Items {
			if item.Id == "" {
				return errors.New("媒体服务器分页返回空条目 ID")
			}
			if _, exists := seen[item.Id]; exists {
				return fmt.Errorf("媒体服务器分页返回重复条目 %s", item.Id)
			}
			seen[item.Id] = struct{}{}
			if !jellyfin && query.LastDateCreatedAt > 0 && item.DateCreated != "" {
				if t, err := time.Parse(time.RFC3339, item.DateCreated); err == nil && t.Unix() == query.LastDateCreatedAt {
					return nil
				}
			}
			if err := handle(item); err != nil {
				return err
			}
		}

		if len(response.Items) == 0 {
			return nil
		}
		nextStartIndex := startIndex + len(response.Items)
		if response.TotalRecordCount > 0 && nextStartIndex >= int(response.TotalRecordCount) {
			return nil
		}
		startIndex = nextStartIndex
	}
}

func (c *Client) fetchMediaItemsPage(
	ctx context.Context,
	query EmbyItemsQuery,
	startIndex int,
	limit int,
) (QueryResultBaseItemDto, error) {
	path := "/Items"
	if query.UserID != "" {
		path = "/Users/" + url.PathEscape(query.UserID) + "/Items"
	}
	jellyfin := c.isJellyfin()

	params := url.Values{}
	params.Add("StartIndex", fmt.Sprintf("%d", startIndex))
	params.Add("Limit", fmt.Sprintf("%d", limit))
	params.Add("Recursive", "true")
	params.Set("EnableTotalRecordCount", "true")
	if query.LibraryID != "" {
		params.Add("ParentId", query.LibraryID)
	}
	if query.IncludeItemTypes != "" {
		params.Add("IncludeItemTypes", query.IncludeItemTypes)
	} else {
		params.Add("IncludeItemTypes", "Movie,Video,Episode")
	}
	if query.Fields != "" {
		params.Add("Fields", query.Fields)
	} else {
		params.Add("Fields", EmbySnapshotFields)
	}
	if query.SortBy != "" {
		params.Add("SortBy", query.SortBy)
	} else {
		params.Add("SortBy", "DateCreated")
	}
	if query.SortOrder != "" {
		params.Add("SortOrder", query.SortOrder)
	} else {
		params.Add("SortOrder", "Descending")
	}
	if query.MinDateLastSaved != "" {
		params.Add("MinDateLastSaved", query.MinDateLastSaved)
	}
	if query.IDs != "" {
		params.Add("Ids", query.IDs)
	}
	if jellyfin {
		jellyfinItemsParams(params, query.Fields)
	}
	var page struct {
		Items *[]BaseItemDtoV2 `json:"Items"`
		Total *int32           `json:"TotalRecordCount"`
	}
	if err := c.getSnapshotJSON(ctx, path, params, &page); err != nil {
		return QueryResultBaseItemDto{}, err
	}
	if page.Items == nil || page.Total == nil || *page.Total < 0 {
		return QueryResultBaseItemDto{}, errors.New("媒体服务器分页响应缺少完整的条目或总数")
	}
	response := QueryResultBaseItemDto{Items: *page.Items, TotalRecordCount: *page.Total}
	if jellyfin {
		for i := range response.Items {
			if err := normalizeJellyfinItem(&response.Items[i]); err != nil {
				return QueryResultBaseItemDto{}, err
			}
		}
	}
	return response, nil
}

// GetUsersWithAllLibrariesAccess 获取所有 Emby 用户，并筛选出可访问全部媒体库的用户。
func (c *Client) GetUsersWithAllLibrariesAccess() ([]UserDto, error) {
	return c.GetUsersWithAllLibrariesAccessContext(context.Background())
}

// GetUsersWithAllLibrariesAccessContext 复用全部媒体库可见用户查询，播放权限由提取调用方筛选。
func (c *Client) GetUsersWithAllLibrariesAccessContext(ctx context.Context) ([]UserDto, error) {
	if _, err := c.CheckConnection(ctx); err != nil {
		return nil, err
	}
	var users []UserDto
	if err := c.getSnapshotJSON(ctx, "/Users", nil, &users); err != nil {
		return nil, err
	}
	var usersWithAllAccess []UserDto
	for _, user := range users {
		if user.Policy.EnableAllFolders {
			usersWithAllAccess = append(usersWithAllAccess, user)
		}
	}
	return usersWithAllAccess, nil
}

// FindItemByPath 按本地路径查询 Emby 条目。
func (c *Client) FindItemByPath(path string) (*BaseItemDtoV2, error) {
	if path == "" {
		return nil, nil
	}
	info, err := c.CheckConnection(context.Background())
	if err != nil {
		return nil, err
	}
	if info.Product == ProductJellyfin {
		return nil, nil
	}
	params := url.Values{}
	params.Add("Path", path)
	params.Add("Recursive", "true")
	params.Add("Fields", "Path")
	params.Add("IncludeItemTypes", embyRefreshLookupItemTypes)
	params.Add("Limit", "1")
	item, err := c.findFirstItem(params, "媒体服务器路径查询")
	if err != nil || item == nil || item.Path == path {
		return item, err
	}
	return nil, nil
}

// FindItemByID 按 item ID 查询 Emby 条目。
func (c *Client) FindItemByID(itemID string) (*BaseItemDtoV2, error) {
	if itemID == "" {
		return nil, nil
	}
	if _, err := c.CheckConnection(context.Background()); err != nil {
		return nil, err
	}
	params := url.Values{}
	params.Add("Ids", itemID)
	params.Add("Recursive", "true")
	params.Add("Fields", "Path")
	params.Add("IncludeItemTypes", embyRefreshLookupItemTypes)
	params.Add("Limit", "1")
	item, err := c.findFirstItem(params, "媒体服务器 ID 查询")
	if err != nil || item == nil {
		return item, err
	}
	if c.isJellyfin() {
		itemID, err = normalizeJellyfinID(itemID)
		if err != nil {
			return nil, err
		}
	}
	if item.Id != itemID {
		return nil, nil
	}
	return item, nil
}

func (c *Client) findFirstItem(params url.Values, errorContext string) (*BaseItemDtoV2, error) {
	var result QueryResultBaseItemDto
	if err := c.getSnapshotJSON(context.Background(), "/Items", params, &result); err != nil {
		return nil, fmt.Errorf("%s：%w", errorContext, err)
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	if c.isJellyfin() {
		for i := range result.Items {
			if err := normalizeJellyfinItem(&result.Items[i]); err != nil {
				return nil, err
			}
		}
	}
	return &result.Items[0], nil
}

// 通过 item ID 查询所属的媒体库 ID，返回数组。
// 先查询 item 的 Ancestors，再用 ancestor 路径精确匹配 /Library/VirtualFolders 的 Locations。
func (c *Client) GetItemLibraryId(itemId string) ([]VirtualFolderDto, error) {
	return c.GetItemLibraryIDContext(context.Background(), itemId)
}

// GetItemLibraryIDContext 支持后台生命周期取消的所属媒体库查询。
func (c *Client) GetItemLibraryIDContext(ctx context.Context, itemId string) ([]VirtualFolderDto, error) {
	ancestors, err := c.GetItemAncestorsContext(ctx, itemId)
	if err != nil {
		return nil, err
	}
	if len(ancestors) == 0 && c.isJellyfin() {
		items, readErr := c.GetSnapshotItems(ctx, itemId)
		if readErr != nil {
			return nil, readErr
		}
		if len(items) == 1 && items[0].Id == itemId && items[0].ParentId != "" {
			parentID := items[0].ParentId
			parents, readErr := c.GetSnapshotItems(ctx, parentID)
			if readErr != nil {
				return nil, readErr
			}
			if len(parents) == 1 && parents[0].Id == parentID && parents[0].IsFolder {
				ancestors, readErr = c.GetItemAncestorsContext(ctx, parentID)
				if readErr != nil {
					return nil, readErr
				}
				ancestors = append(ancestors, AncestorDto{ID: parentID, Path: parents[0].Path, IsFolder: true})
			}
		}
	}
	if len(ancestors) == 0 {
		return nil, fmt.Errorf("媒体条目 %s 的祖先目录信息为空，无法解析所属媒体库", itemId)
	}
	// 查询顶层文件夹路径对应的媒体库 ID
	var virtualFolders []VirtualFolderDto
	err = c.getSnapshotJSON(ctx, "/Library/VirtualFolders", nil, &virtualFolders)
	if err != nil {
		return nil, err
	}
	if c.isJellyfin() {
		if err := normalizeJellyfinVirtualFolders(virtualFolders); err != nil {
			return nil, err
		}
	}
	ancestorPaths := make(map[string]struct{}, len(ancestors))
	for _, ancestor := range ancestors {
		if ancestor.Path != "" {
			ancestorPaths[ancestor.Path] = struct{}{}
		}
	}
	// 提取所有命中 ancestor 路径的媒体库 ID
	var librarys []VirtualFolderDto
	for _, virtualFolder := range virtualFolders {
		for _, location := range virtualFolder.Locations {
			if _, ok := ancestorPaths[location]; ok {
				librarys = append(librarys, virtualFolder)
				break
			}
		}
	}
	return librarys, nil
}

func (c *Client) GetItemAncestors(itemId string) ([]AncestorDto, error) {
	return c.GetItemAncestorsContext(context.Background(), itemId)
}

// 获取所有媒体库的详情包括文件夹
func (c *Client) GetLibraryVirtualFolders() ([]VirtualFolderDto, error) {
	if _, err := c.CheckConnection(context.Background()); err != nil {
		return nil, err
	}
	var virtualFolders []VirtualFolderDto
	if err := c.getSnapshotJSON(context.Background(), "/Library/VirtualFolders", nil, &virtualFolders); err != nil {
		return nil, err
	}
	if c.isJellyfin() {
		if err := normalizeJellyfinVirtualFolders(virtualFolders); err != nil {
			return nil, err
		}
	}
	return virtualFolders, nil
}
