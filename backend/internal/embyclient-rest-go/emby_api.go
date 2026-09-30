package embyclientrestgo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"qmediasync/internal/helpers"
)

// Client 是与 Emby API 交互的客户端。
type Client struct {
	embyURL    string
	apiKey     string
	httpClient *http.Client
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
	IndexNumber       int               `json:"IndexNumber,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	PremiereDate      string            `json:"PremiereDate,omitempty"`
	DateCreated       string            `json:"DateCreated,omitempty"`
	DateModified      string            `json:"DateModified,omitempty"`
	IsFolder          bool              `json:"IsFolder,omitempty"`
	MediaSources      []MediaSource     `json:"MediaSources,omitempty"`
	CommunityRating   float64           `json:"CommunityRating,omitempty"`
	Genres            []string          `json:"Genres,omitempty"`
	People            []PersonDto       `json:"People,omitempty"`
	Overview          string            `json:"Overview,omitempty"`
	ImageTags         map[string]string `json:"ImageTags,omitempty"`
}

type MediaSource struct {
	Path string `json:"Path,omitempty"`
	Name string `json:"Name,omitempty"`
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
	ID       string `json:"Id,omitempty"`
	Name     string `json:"Name,omitempty"`
	Path     string `json:"Path,omitempty"`
	FileName string `json:"FileName,omitempty"`
	IsFolder bool   `json:"IsFolder,omitempty"`
	ParentId string `json:"ParentId,omitempty"`
	Type     string `json:"Type,omitempty"`
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
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Library/MediaFolders?api_key=%s", c.embyURL, c.apiKey)

	// 创建一个新的 HTTP 请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求时出错：%w", err)
	}

	// 设置请求头
	req.Header.Set("Accept", "application/json")

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求时出错：%w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("收到非 200 状态码：%d", resp.StatusCode)
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体时出错：%w", err)
	}

	// 解析 JSON 响应
	var librariesResponse EmbyLibrariesResponse
	if err := json.Unmarshal(body, &librariesResponse); err != nil {
		// 尝试解析为单个项目，以应对某些 Emby 版本可能返回不同结构的情况
		var singleLibrary EmbyLibrary
		if err2 := json.Unmarshal(body, &singleLibrary); err2 == nil && singleLibrary.Name != "" {
			return []EmbyLibrary{singleLibrary}, nil
		}
		return nil, fmt.Errorf("解析 JSON 时出错：%w", err)
	}

	return librariesResponse.Items, nil
}

// GetMediaItemsByLibraryID 从指定的媒体库中检索所有媒体项目。
// 兼容旧调用方：内部使用流式分页接口，再聚合为切片返回。
func (c *Client) GetMediaItemsByLibraryID(libraryID string, lastDateCreatedTime int64) ([]BaseItemDtoV2, error) {
	var allItems []BaseItemDtoV2
	err := c.FetchMediaItemsByLibraryID(
		context.Background(),
		EmbyItemsQuery{
			LibraryID:         libraryID,
			LastDateCreatedAt: lastDateCreatedTime,
		},
		func(item BaseItemDtoV2) error {
			allItems = append(allItems, item)
			return nil
		},
	)
	return allItems, err
}

// FetchMediaItemsByLibraryID 从指定媒体库分页拉取媒体条目，并逐条回调处理。
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

	startIndex := query.StartIndex
	limit := query.Limit
	if limit <= 0 {
		limit = 100
	}

	for {
		response, err := c.fetchMediaItemsPage(ctx, query, startIndex, limit)
		if err != nil {
			return err
		}
		for _, item := range response.Items {
			if query.LastDateCreatedAt > 0 && item.DateCreated != "" {
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
	basePath := fmt.Sprintf("%s/emby/Items", c.embyURL)
	if query.UserID != "" {
		basePath = fmt.Sprintf("%s/emby/Users/%s/Items", c.embyURL, url.PathEscape(query.UserID))
	}
	baseURL, err := url.Parse(basePath)
	if err != nil {
		return QueryResultBaseItemDto{}, fmt.Errorf("解析基础 URL 时出错：%w", err)
	}

	params := url.Values{}
	params.Add("api_key", c.apiKey)
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
		params.Add("Fields", "DateCreated,DateModified,ParentId,PremiereDate,MediaStreams")
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
	baseURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL.String(), nil)
	if err != nil {
		return QueryResultBaseItemDto{}, fmt.Errorf("创建请求时出错：%w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return QueryResultBaseItemDto{}, fmt.Errorf("发送请求时出错：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return QueryResultBaseItemDto{}, fmt.Errorf("收到非 200 状态码：%d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return QueryResultBaseItemDto{}, fmt.Errorf("读取响应体时出错：%w", err)
	}

	var response QueryResultBaseItemDto
	if err := json.Unmarshal(body, &response); err != nil {
		return QueryResultBaseItemDto{}, fmt.Errorf("解析 JSON 时出错：%w", err)
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
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Users?api_key=%s", c.embyURL, c.apiKey)

	// 创建新的 HTTP 请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求时出错：%w", err)
	}

	// 设置请求头
	req.Header.Set("Accept", "application/json")

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求时出错：%w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("收到非 200 状态码：%d", resp.StatusCode)
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体时出错：%w", err)
	}

	// 解析 JSON 响应
	var users []UserDto
	if err := json.Unmarshal(body, &users); err != nil {
		return nil, fmt.Errorf("解析 JSON 时出错：%w", err)
	}

	// 筛选可访问所有媒体库的用户
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
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Items/%s/Refresh?api_key=%s&Fields=MediaStreams", c.embyURL, libraryId, c.apiKey)
	err := helpers.PostUrl(url)
	if err != nil {
		return err
	}
	helpers.AppLogger.Infof("已触发 Emby 媒体库 %s => %s 刷新", libraryId, libraryName)
	return nil
}

// RefreshItem 刷新单个 Emby 条目。
func (c *Client) RefreshItem(itemId string, itemName string, recursive bool) error {
	baseURL, err := url.Parse(fmt.Sprintf("%s/emby/Items/%s/Refresh", c.embyURL, url.PathEscape(itemId)))
	if err != nil {
		return fmt.Errorf("解析 Emby 条目刷新 URL 失败：%w", err)
	}
	params := url.Values{}
	params.Add("api_key", c.apiKey)
	params.Add("Fields", "MediaStreams")
	params.Add("Recursive", strconv.FormatBool(recursive))
	baseURL.RawQuery = params.Encode()

	req, err := http.NewRequest(http.MethodPost, baseURL.String(), nil)
	if err != nil {
		return fmt.Errorf("创建 Emby 条目刷新请求失败：%w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送 Emby 条目刷新请求失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("刷新 Emby 条目失败，状态码：%d", resp.StatusCode)
	}
	helpers.AppLogger.Infof("已触发 Emby 条目 %s => %s 刷新，recursive=%v", itemId, itemName, recursive)
	return nil
}

// FindItemByPath 按本地路径查询 Emby 条目。
func (c *Client) FindItemByPath(path string) (*BaseItemDtoV2, error) {
	if path == "" {
		return nil, nil
	}
	params := url.Values{}
	params.Add("Path", path)
	params.Add("Recursive", "true")
	params.Add("Fields", "Path")
	params.Add("IncludeItemTypes", embyRefreshLookupItemTypes)
	params.Add("Limit", "1")
	return c.findFirstItem(params, "Emby 路径查询")
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
	return c.findFirstItem(params, "Emby ID 查询")
}

func (c *Client) findFirstItem(params url.Values, errorContext string) (*BaseItemDtoV2, error) {
	baseURL, err := url.Parse(fmt.Sprintf("%s/emby/Items", c.embyURL))
	if err != nil {
		return nil, fmt.Errorf("解析 %s URL 失败：%w", errorContext, err)
	}
	params.Add("api_key", c.apiKey)
	baseURL.RawQuery = params.Encode()

	req, err := http.NewRequest(http.MethodGet, baseURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建 %s 请求失败：%w", errorContext, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送 %s 请求失败：%w", errorContext, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s 返回非 200 状态码：%d", errorContext, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 响应失败：%w", errorContext, err)
	}
	var result QueryResultBaseItemDto
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("解析 %s 响应失败：%w", errorContext, err)
	}
	if len(result.Items) == 0 {
		return nil, nil
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
	ancestors, err := c.GetItemAncestors(itemId)
	if err != nil {
		return nil, err
	}
	if len(ancestors) == 0 {
		return nil, fmt.Errorf("Emby 条目 %s ancestors 为空，无法解析所属媒体库", itemId)
	}
	// 查询顶层文件夹路径对应的媒体库 ID
	virtualFolders, err := c.GetLibraryVirtualFolders()
	if err != nil {
		return nil, err
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
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Items/%s/Ancestors?api_key=%s", c.embyURL, itemId, c.apiKey)

	// 创建新的 HTTP 请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求时出错：%w", err)
	}

	// 设置请求头
	req.Header.Set("Accept", "application/json")

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求时出错：%w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("收到非 200 状态码：%d", resp.StatusCode)
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体时出错：%w", err)
	}

	// 解析 JSON 响应
	var ancestors []AncestorDto
	if err := json.Unmarshal(body, &ancestors); err != nil {
		return nil, fmt.Errorf("解析 JSON 时出错：%w", err)
	}
	// 提取所有文件夹 ID
	return ancestors, nil
}

// 获取所有媒体库的详情包括文件夹
func (c *Client) GetLibraryVirtualFolders() ([]VirtualFolderDto, error) {
	// 构造请求 URL
	url := fmt.Sprintf("%s/emby/Library/VirtualFolders?api_key=%s", c.embyURL, c.apiKey)

	// 创建新的 HTTP 请求
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求时出错：%w", err)
	}

	// 设置请求头
	req.Header.Set("Accept", "application/json")

	// 发送请求
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求时出错：%w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("收到非 200 状态码：%d", resp.StatusCode)
	}

	// 读取响应体
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应体时出错：%w", err)
	}

	// 解析 JSON 响应
	var virtualFolders []VirtualFolderDto
	if err := json.Unmarshal(body, &virtualFolders); err != nil {
		return nil, fmt.Errorf("解析 JSON 时出错：%w", err)
	}

	return virtualFolders, nil
}

// 刷新所有媒体库媒体流数据
func ProcessLibraries(embyURL, apiKey string, excludeIds []string) []map[string]string {
	// 创建一个新的 Emby 客户端
	client := NewClient(embyURL, apiKey)

	libs, err := client.GetAllMediaLibraries()
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
