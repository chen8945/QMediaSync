package embyclientrestgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"time"

	"github.com/google/uuid"

	"qmediasync/internal/helpers"
)

// Client 是与 Emby API 交互的客户端。
type Client struct {
	embyURL     string
	apiKey      string
	httpClient  *http.Client
	infoMu      sync.Mutex
	serverInfo  *serverInfo
	infoLoading chan struct{}
}

type serverInfo struct {
	ID          string `json:"Id"`
	ProductName string `json:"ProductName"`
	Version     string `json:"Version"`
}

func (i serverInfo) jellyfin() bool {
	return strings.EqualFold(i.ProductName, "Jellyfin") || strings.EqualFold(i.ProductName, "Jellyfin Server")
}

// NewClient 创建一个新的 Emby API 客户端。
func NewClient(embyURL, apiKey string) *Client {
	return &Client{
		embyURL: embyURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 30 * time.Second, // 添加合理的超时
		},
	}
}

// EmbyLibrary 表示 Emby 中的单个媒体库。
type EmbyLibrary struct {
	Name string `json:"Name"`
	ID   string `json:"Id"`
}

// EmbyLibrariesResponse 是 /Library/MediaFolders 端点响应的结构。
type EmbyLibrariesResponse struct {
	Items []EmbyLibrary `json:"Items"`
}

// UserPolicy 表示 Emby 用户策略配置。
type UserPolicy struct {
	// 是否允许访问所有文件夹。
	EnableAllFolders bool `json:"EnableAllFolders"`
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
}

type MediaSource struct {
	ID     string `json:"Id,omitempty"`
	Type   string `json:"Type,omitempty"`
	ItemID string `json:"ItemId,omitempty"`
	Path   string `json:"Path,omitempty"`
	Name   string `json:"Name,omitempty"`
}

type MediaStreamV2 struct {
	// 编码格式，对应探测字段 codec_name，适用于视频、音频和字幕流。
	Codec string `json:"Codec,omitempty"`
	Type  string `json:"Type,omitempty"`
}

type QueryResultBaseItemDto struct {
	Items            []BaseItemDtoV2 `json:"Items,omitempty"`
	TotalRecordCount int32           `json:"TotalRecordCount,omitempty"`
}

const embyRefreshLookupItemTypes = "Movie,Video,Episode,Folder,Series"

const jellyfinSnapshotFields = "DateCreated,ParentId,Path,MediaSources"

// EmbySnapshotFields 是物理索引快照必须显式请求的字段。
const EmbySnapshotFields = "DateCreated,DateModified,ParentId,PremiereDate,Path,MediaSources,PartCount,SeriesId,SeasonId,SeriesName,SeasonName,IndexNumber,ParentIndexNumber"

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
	info, err := c.getServerInfo(context.Background())
	if err != nil {
		return nil, err
	}
	libraries, err := c.getMediaLibraries()
	if err != nil {
		return nil, err
	}
	if info.jellyfin() {
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

func (c *Client) getMediaLibraries() ([]EmbyLibrary, error) {
	var response EmbyLibrariesResponse
	if err := c.getSnapshotJSON(context.Background(), "/Library/MediaFolders", nil, &response); err != nil {
		return nil, err
	}
	return response.Items, nil
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

// GetServerID 查询当前 Emby 实例身份，不用 URL 或版本号替代服务器 ID。
func (c *Client) GetServerID(ctx context.Context) (string, error) {
	info, err := c.getServerInfo(ctx)
	if err != nil {
		return "", err
	}
	if info.ID == "" {
		return "", errors.New("媒体服务器未返回服务器 ID")
	}
	return info.ID, nil
}

func (c *Client) getServerInfo(ctx context.Context) (serverInfo, error) {
	for {
		if err := ctx.Err(); err != nil {
			return serverInfo{}, err
		}
		c.infoMu.Lock()
		if c.serverInfo != nil {
			info := *c.serverInfo
			c.infoMu.Unlock()
			return info, nil
		}
		if loading := c.infoLoading; loading != nil {
			c.infoMu.Unlock()
			select {
			case <-ctx.Done():
				return serverInfo{}, ctx.Err()
			case <-loading:
				continue
			}
		}
		c.infoLoading = make(chan struct{})
		c.infoMu.Unlock()
		var info serverInfo
		status, err := c.getJSON(ctx, "/System/Info/Public", nil, &info, true)
		if status == http.StatusNotFound {
			_, err = c.getJSON(ctx, "/System/Info/Public", nil, &info, false)
		}
		c.infoMu.Lock()
		if err == nil {
			c.serverInfo = &info
		}
		close(c.infoLoading)
		c.infoLoading = nil
		c.infoMu.Unlock()
		return info, err
	}
}

func (c *Client) isJellyfin() bool {
	c.infoMu.Lock()
	defer c.infoMu.Unlock()
	return c.serverInfo != nil && c.serverInfo.jellyfin()
}

func (c *Client) newAPIRequest(ctx context.Context, method, path string, params url.Values, jellyfin bool) (*http.Request, error) {
	if params == nil {
		params = url.Values{}
	} else {
		params = maps.Clone(params)
	}
	prefix := "/emby"
	if jellyfin {
		prefix = ""
	} else {
		params.Set("api_key", c.apiKey)
	}
	u := strings.TrimRight(c.embyURL, "/") + prefix + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, errors.New("创建媒体服务器请求失败")
	}
	req.Header.Set("Accept", "application/json")
	if jellyfin {
		req.Header.Set("Authorization", "MediaBrowser Client=\"QMediaSync\", Device=\"QMediaSync\", DeviceId=\"QMediaSync\", Version=\"1\", Token="+strconv.Quote(c.apiKey))
	}
	return req, nil
}

// GetAdditionalParts 获取隐藏分段并直接请求物理快照字段。
func (c *Client) GetAdditionalParts(ctx context.Context, itemID string) ([]BaseItemDtoV2, error) {
	info, err := c.getServerInfo(ctx)
	if err != nil {
		return nil, err
	}
	var result QueryResultBaseItemDto
	fields := EmbySnapshotFields
	if info.jellyfin() {
		fields = jellyfinSnapshotFields
	}
	params := url.Values{"Fields": {fields}}
	if err := c.getSnapshotJSON(ctx, "/Videos/"+url.PathEscape(itemID)+"/AdditionalParts", params, &result); err != nil {
		return nil, err
	}
	if int(result.TotalRecordCount) != len(result.Items) {
		return nil, errors.New("Emby AdditionalParts 响应不完整")
	}
	if info.jellyfin() {
		for i := range result.Items {
			if err := normalizeJellyfinItem(&result.Items[i]); err != nil {
				return nil, err
			}
		}
	}
	return result.Items, nil
}

// GetSnapshotItems 按 ID 补查缺少字段的物理条目，不依赖用户态聚合接口。
func (c *Client) GetSnapshotItems(ctx context.Context, ids string) ([]BaseItemDtoV2, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := c.getServerInfo(ctx); err != nil {
		return nil, err
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
	users, err := c.getUsersWithAllLibrariesAccess(ctx)
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

func (c *Client) getSnapshotJSON(ctx context.Context, path string, params url.Values, result any) error {
	_, err := c.getJSON(ctx, path, params, result, c.isJellyfin())
	return err
}

func (c *Client) getJSON(ctx context.Context, path string, params url.Values, result any, jellyfin bool) (int, error) {
	req, err := c.newAPIRequest(ctx, http.MethodGet, path, params, jellyfin)
	if err != nil {
		return 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, errors.New("媒体服务器请求失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("媒体服务器请求状态码：%d", resp.StatusCode)
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(result); err != nil {
		return resp.StatusCode, fmt.Errorf("解析媒体服务器响应失败：%w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return resp.StatusCode, errors.New("媒体服务器响应含有多余内容")
	}
	return resp.StatusCode, nil
}

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

func normalizeJellyfinItem(item *BaseItemDtoV2) error {
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
			return errors.New("Emby 分页过程中总条目数发生变化")
		}
		if len(response.Items) == 0 && startIndex < expectedTotal {
			return fmt.Errorf("Emby 分页提前返回空页：%d/%d", startIndex, expectedTotal)
		}
		if startIndex+len(response.Items) > expectedTotal {
			return errors.New("Emby 分页条目数超出 TotalRecordCount")
		}
		for _, item := range response.Items {
			if item.Id == "" {
				return errors.New("Emby 分页返回空条目 ID")
			}
			if _, exists := seen[item.Id]; exists {
				return fmt.Errorf("Emby 分页返回重复条目 %s", item.Id)
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
		fields := jellyfinSnapshotFields
		if slices.Contains(strings.Split(query.Fields, ","), "MediaStreams") {
			fields += ",MediaStreams"
		}
		params.Set("Fields", fields)
		params.Set("SortBy", "DateCreated,SortName")
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

// CheckPlaybackInfo 请求媒体项目的播放信息，并检查请求是否成功。
func (c *Client) CheckPlaybackInfo(item BaseItemDtoV2, userID string) error {
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Items/%s/PlaybackInfo?api_key=%s", c.embyURL, item.Id, c.apiKey)
	// 准备请求体
	requestBody, err := json.Marshal(map[string]string{
		"UserId": userID,
	})
	if err != nil {
		return fmt.Errorf("序列化请求体失败：%w", err)
	}

	var lastErr error

	for i := range 1 {
		// 创建新的 HTTP POST 请求
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(requestBody))
		if err != nil {
			return fmt.Errorf("创建 POST 请求失败：%w", err)
		}

		// 设置请求头
		req.Header.Set("Content-Type", "application/json")

		// 发送请求
		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("发送 POST 请求失败：%w", err)
			helpers.AppLogger.Errorf("第 %d 次尝试失败：%v。3 秒后重试…", i+1, err)
			time.Sleep(3 * time.Second)
			continue
		}

		// 检查响应状态码
		if resp.StatusCode == http.StatusOK {
			helpers.AppLogger.Infof("影视剧 %s 的媒体信息请求成功（用户：%s）", item.Name, userID)
			resp.Body.Close()
			return nil
		}

		// 状态码异常时记录错误并重试。
		lastErr = fmt.Errorf("请求失败，收到非 200 状态码：%d", resp.StatusCode)
		helpers.AppLogger.Errorf("第 %d 次尝试失败，状态码：%d。3 秒后重试…", i+1, resp.StatusCode)
		resp.Body.Close()
		time.Sleep(3 * time.Second)
	}

	helpers.AppLogger.Errorf("1 次尝试后，获取影视剧 %s（用户：%s）的媒体信息失败。最后错误：%v", item.Name, userID, lastErr)
	return fmt.Errorf("获取媒体信息失败，重试 1 次后：%w", lastErr)
}

// GetUsersWithAllLibrariesAccess 获取所有 Emby 用户，并筛选出可访问全部媒体库的用户。
func (c *Client) GetUsersWithAllLibrariesAccess() ([]UserDto, error) {
	return c.getUsersWithAllLibrariesAccess(context.Background())
}

func (c *Client) getUsersWithAllLibrariesAccess(ctx context.Context) ([]UserDto, error) {
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

// 刷新媒体库
func (c *Client) RefreshLibrary(libraryId string, libraryName string) error {
	info, err := c.getServerInfo(context.Background())
	if err != nil {
		return err
	}
	if info.jellyfin() {
		return c.RefreshItem(libraryId, libraryName, true)
	}
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Items/%s/Refresh?api_key=%s&Fields=MediaStreams", c.embyURL, libraryId, c.apiKey)
	err = helpers.PostUrl(url)
	if err != nil {
		return err
	}
	helpers.AppLogger.Infof("已触发 Emby 媒体库 %s => %s 刷新", libraryId, libraryName)
	return nil
}

// RefreshItem 刷新单个 Emby 条目。
func (c *Client) RefreshItem(itemId string, itemName string, recursive bool) error {
	info, err := c.getServerInfo(context.Background())
	if err != nil {
		return err
	}
	params := url.Values{}
	if info.jellyfin() {
		params.Set("MetadataRefreshMode", "Default")
		params.Set("ImageRefreshMode", "Default")
	} else {
		params.Set("Fields", "MediaStreams")
		params.Set("Recursive", strconv.FormatBool(recursive))
	}
	req, err := c.newAPIRequest(context.Background(), http.MethodPost, "/Items/"+url.PathEscape(itemId)+"/Refresh", params, info.jellyfin())
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送 Emby 条目刷新请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("刷新 Emby 条目失败，状态码：%d", resp.StatusCode)
	}
	helpers.AppLogger.Infof("已触发 Emby 条目刷新，ItemId %s => %s，recursive=%v", itemId, itemName, recursive)
	return nil
}

// FindItemByPath 按本地路径查询 Emby 条目。
func (c *Client) FindItemByPath(path string) (*BaseItemDtoV2, error) {
	if path == "" {
		return nil, nil
	}
	info, err := c.getServerInfo(context.Background())
	if err != nil {
		return nil, err
	}
	if info.jellyfin() {
		return nil, nil
	}
	params := url.Values{}
	params.Add("Path", path)
	params.Add("Recursive", "true")
	params.Add("Fields", "Path")
	params.Add("IncludeItemTypes", embyRefreshLookupItemTypes)
	params.Add("Limit", "1")
	item, err := c.findFirstItem(params, "Emby 路径查询")
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
	params := url.Values{}
	params.Add("Ids", itemID)
	params.Add("Recursive", "true")
	params.Add("Fields", "Path")
	params.Add("IncludeItemTypes", embyRefreshLookupItemTypes)
	params.Add("Limit", "1")
	item, err := c.findFirstItem(params, "Emby ID 查询")
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
	info, err := c.getServerInfo(context.Background())
	if err != nil {
		return nil, err
	}
	var result QueryResultBaseItemDto
	if err := c.getSnapshotJSON(context.Background(), "/Items", params, &result); err != nil {
		return nil, fmt.Errorf("%s：%w", errorContext, err)
	}
	if len(result.Items) == 0 {
		return nil, nil
	}
	if info.jellyfin() {
		for i := range result.Items {
			if err := normalizeJellyfinItem(&result.Items[i]); err != nil {
				return nil, err
			}
		}
	}
	return &result.Items[0], nil
}

func (c *Client) GetItemDetailByUser(itemId string, userID string) (*BaseItemDtoV2, error) {
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Users/%s/Items/%s?api_key=%s", c.embyURL, userID, itemId, c.apiKey)
	helpers.AppLogger.Debugf("获取 Emby 媒体详情 URL：%s", url)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	// helpers.AppLogger.Debugf("GET %s", baseURL.String())
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("收到非 200 状态码：%d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var response BaseItemDtoV2
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("解析 JSON 时出错：%w", err)
	}
	return &response, nil
}

// 通过 item ID 查询所属的媒体库 ID，返回数组。
// 先查询 item 的 Ancestors，再用 ancestor 路径精确匹配 /Library/VirtualFolders 的 Locations。
func (c *Client) GetItemLibraryId(itemId string) ([]VirtualFolderDto, error) {
	return c.GetItemLibraryIDContext(context.Background(), itemId)
}

// GetItemLibraryIDContext 支持后台生命周期取消的所属媒体库查询。
func (c *Client) GetItemLibraryIDContext(ctx context.Context, itemId string) ([]VirtualFolderDto, error) {
	if _, err := c.getServerInfo(ctx); err != nil {
		return nil, err
	}
	ancestors, err := c.GetItemAncestorsContext(ctx, itemId)
	if err != nil {
		return nil, err
	}
	if len(ancestors) == 0 {
		return nil, fmt.Errorf("Emby 条目 %s 的祖先目录信息为空，无法解析所属媒体库", itemId)
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
	if _, err := c.getServerInfo(context.Background()); err != nil {
		return nil, err
	}
	return c.GetItemAncestorsContext(context.Background(), itemId)
}

// 获取所有媒体库的详情包括文件夹
func (c *Client) GetLibraryVirtualFolders() ([]VirtualFolderDto, error) {
	if _, err := c.getServerInfo(context.Background()); err != nil {
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

// 刷新所有媒体库媒体流数据
func ProcessLibraries(embyURL, apiKey string, excludeIds []string) []map[string]string {
	// 创建一个新的 Emby 客户端
	client := NewClient(embyURL, apiKey)

	libs, err := client.getMediaLibraries()
	if err != nil {
		helpers.AppLogger.Errorf("获取媒体库失败：%v", err)
		return nil
	}
	// 获取有权限的用户
	users, err := client.GetUsersWithAllLibrariesAccess()
	if err != nil {
		helpers.AppLogger.Errorf("获取用户失败：%v", err)
		return nil
	}
	if len(users) == 0 {
		helpers.AppLogger.Errorf("没有找到可以访问所有媒体库的用户")
		return nil
	}
	// 为了高效查找，将 excludeIds 转换为 map，同时忽略空字符串。
	excludeMap := make(map[string]struct{})
	for _, id := range excludeIds {
		if id != "" {
			excludeMap[id] = struct{}{}
		}
	}

	// 使用第一个有权限的用户
	userID := users[0].ID
	helpers.AppLogger.Infof("使用用户 %s（ID：%s）检查播放信息", users[0].Name, userID)
	sum := 0
	tasks := make([]map[string]string, 0)
	for _, lib := range libs {
		if _, exists := excludeMap[lib.ID]; exists {
			helpers.AppLogger.Infof("媒体库 %s 在排除列表中", lib.Name)
			continue
		}

		items, err := client.GetMediaItemsByLibraryID(lib.ID, 0)
		if err != nil {
			helpers.AppLogger.Errorf("获取媒体库 %s 中的项目失败：%v", lib.Name, err)
			continue // 继续处理下一个媒体库
		}

		helpers.AppLogger.Infof("在 %s 中找到 %d 个影视剧", lib.Name, len(items))
		// 处理数据量

		for _, item := range items {
			helpers.AppLogger.Infof("处理项目 %s：%s，共 %d 个媒体流", item.Id, item.Name, len(item.MediaStreams))
			nonSubtitleStreamCount := 0
			for _, stream := range item.MediaStreams {
				if stream.Type != "Subtitle" {
					nonSubtitleStreamCount++
				}
			}
			if nonSubtitleStreamCount < 2 {
				sum++
				// 检查每个媒体项目的播放信息
				url := fmt.Sprintf("%s/emby/Items/%s/PlaybackInfo?api_key=%s", embyURL, item.Id, apiKey)
				task := make(map[string]string)
				task["url"] = url
				task["item_id"] = item.Id
				task["item_name"] = item.Name
				tasks = append(tasks, task)
			}
		}
		// wg.Wait()
	}
	return tasks
}
