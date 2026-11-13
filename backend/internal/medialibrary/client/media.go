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
	"strings"
)

// GetItemDetailByUser 保留既有用户态详情入口。
func (c *Client) GetItemDetailByUser(itemID, userID string) (*BaseItemDtoV2, error) {
	return c.GetItemDetailByUserContext(context.Background(), itemID, userID)
}

// GetItemDetailByUserContext 读取详情，不将动态媒体来源 ID 解释成静态版本身份。
func (c *Client) GetItemDetailByUserContext(ctx context.Context, itemID, userID string) (*BaseItemDtoV2, error) {
	if strings.TrimSpace(itemID) == "" || strings.TrimSpace(userID) == "" {
		return nil, errors.New("媒体条目和用户 ID 不能为空")
	}
	if _, err := c.CheckConnection(ctx); err != nil {
		return nil, err
	}
	path, params := embyDetailPath(itemID, userID)
	if c.isJellyfin() {
		id, err := normalizeJellyfinID(itemID)
		if err != nil {
			return nil, err
		}
		itemID = id
		id, err = normalizeJellyfinID(userID)
		if err != nil {
			return nil, err
		}
		userID = id
		path, params = jellyfinDetailPath(itemID, userID)
	}
	var item BaseItemDtoV2
	if err := c.getSnapshotJSON(ctx, path, params, &item); err != nil {
		return nil, err
	}
	if c.isJellyfin() {
		if err := normalizeJellyfinItemIdentity(&item); err != nil {
			return nil, err
		}
	}
	if item.Id != itemID {
		return nil, errors.New("媒体详情返回了其他条目")
	}
	return &item, nil
}

// GetItemImageContext 下载原生图片；不存在或坏响应由调用方决定是否省略图片。
func (c *Client) GetItemImageContext(ctx context.Context, itemID, imageType string) ([]byte, string, error) {
	if strings.TrimSpace(itemID) == "" {
		return nil, "", errors.New("媒体条目 ID 不能为空")
	}
	switch imageType {
	case "Primary", "Backdrop", "Thumb", "Logo", "Banner", "Art", "Disc", "Box", "Screenshot", "Menu", "BoxRear", "Profile":
	default:
		return nil, "", errors.New("无效的媒体图片类型")
	}
	if _, err := c.CheckConnection(ctx); err != nil {
		return nil, "", err
	}
	if c.isJellyfin() {
		id, err := normalizeJellyfinID(itemID)
		if err != nil {
			return nil, "", err
		}
		itemID = id
	}
	req, err := c.newAPIRequest(ctx, http.MethodGet, "/Items/"+url.PathEscape(itemID)+"/Images/"+imageType, nil, c.isJellyfin())
	if err != nil {
		return nil, "", err
	}
	resp, err := c.do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("媒体图片请求状态码：%d", resp.StatusCode)
	}
	// 通知图片只需有限的内存，避免远端坏响应耗尽进程。
	const maxImageSize = 20 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize+1))
	if err != nil {
		return nil, "", errors.New("读取媒体图片失败")
	}
	if len(body) == 0 || len(body) > maxImageSize {
		return nil, "", errors.New("媒体图片为空或过大")
	}
	contentType := http.DetectContentType(body)
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", errors.New("媒体图片响应不是图片")
	}
	return body, contentType, nil
}

// CheckPlaybackInfo 保留既有提取入口并验证实际返回的主要媒体流。
func (c *Client) CheckPlaybackInfo(item BaseItemDtoV2, userID string) error {
	return c.CheckPlaybackInfoContext(context.Background(), item, userID)
}

// CheckPlaybackInfoContext 仅请求媒体信息；不启动播放、转码或直播会话。
func (c *Client) CheckPlaybackInfoContext(ctx context.Context, item BaseItemDtoV2, userID string) error {
	if strings.TrimSpace(item.Id) == "" || strings.TrimSpace(userID) == "" {
		return errors.New("媒体条目和用户 ID 不能为空")
	}
	if _, err := c.CheckConnection(ctx); err != nil {
		return err
	}
	if c.isJellyfin() {
		id, err := normalizeJellyfinID(item.Id)
		if err != nil {
			return err
		}
		item.Id = id
		id, err = normalizeJellyfinID(userID)
		if err != nil {
			return err
		}
		userID = id
	}
	req, err := c.newAPIRequest(ctx, http.MethodPost, "/Items/"+url.PathEscape(item.Id)+"/PlaybackInfo", nil, c.isJellyfin())
	if err != nil {
		return err
	}
	// 两种协议均要求使用有效用户；其他可选播放参数保持服务端默认。
	body, _ := json.Marshal(struct {
		UserID string `json:"UserId"`
	}{userID})
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return ValidatePlaybackInfoResponse(resp, item.Type)
}

// ValidatePlaybackInfoResponse 共用新旧提取任务的响应校验；调用方负责关闭 Body。
func ValidatePlaybackInfoResponse(resp *http.Response, itemType string) error {
	if resp == nil || resp.Body == nil {
		return errors.New("媒体信息响应为空")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("媒体信息请求状态码：%d", resp.StatusCode)
	}
	var result struct {
		MediaSources []MediaSource `json:"MediaSources"`
		ErrorCode    string        `json:"ErrorCode"`
	}
	if err := decodeJSON(resp.Body, &result); err != nil {
		return err
	}
	if result.ErrorCode != "" {
		return errors.New("媒体服务器未能提取媒体信息")
	}
	streamType := "Video"
	if strings.EqualFold(itemType, "Audio") || strings.EqualFold(itemType, "AudioBook") {
		streamType = "Audio"
	}
	for _, source := range result.MediaSources {
		for _, stream := range source.MediaStreams {
			if strings.EqualFold(stream.Type, streamType) && strings.TrimSpace(stream.Codec) != "" {
				return nil
			}
		}
	}
	return errors.New("媒体信息缺少可用的主要媒体流")
}

// RefreshLibrary 刷新库目标，Jellyfin 由服务端按目标类型遍历子项。
func (c *Client) RefreshLibrary(libraryID, libraryName string) error {
	return c.RefreshLibraryContext(context.Background(), libraryID, libraryName)
}

// RefreshLibraryContext 保留 Emby 原库刷新参数，并支持生命周期取消。
func (c *Client) RefreshLibraryContext(ctx context.Context, libraryID, libraryName string) error {
	if _, err := c.CheckConnection(ctx); err != nil {
		return err
	}
	if c.isJellyfin() {
		return c.RefreshItemContext(ctx, libraryID, libraryName, true)
	}
	if strings.TrimSpace(libraryID) == "" {
		return errors.New("媒体库 ID 不能为空")
	}
	return c.postRefresh(ctx, libraryID, url.Values{"Fields": {"MediaStreams"}})
}

// RefreshItem 刷新当前条目；成功仅表示服务端已接收刷新请求。
func (c *Client) RefreshItem(itemID, itemName string, recursive bool) error {
	return c.RefreshItemContext(context.Background(), itemID, itemName, recursive)
}

// RefreshItemContext 按已确认产品选择刷新参数并保留调用取消。
func (c *Client) RefreshItemContext(ctx context.Context, itemID, itemName string, recursive bool) error {
	if strings.TrimSpace(itemID) == "" {
		return errors.New("媒体条目 ID 不能为空")
	}
	if _, err := c.CheckConnection(ctx); err != nil {
		return err
	}
	params := embyRefreshParams(recursive)
	if c.isJellyfin() {
		id, err := normalizeJellyfinID(itemID)
		if err != nil {
			return err
		}
		itemID = id
		params = jellyfinRefreshParams()
	}
	return c.postRefresh(ctx, itemID, params)
}

func (c *Client) postRefresh(ctx context.Context, itemID string, params url.Values) error {
	req, err := c.newAPIRequest(ctx, http.MethodPost, "/Items/"+url.PathEscape(itemID)+"/Refresh", params, c.isJellyfin())
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("刷新媒体条目失败，状态码：%d", resp.StatusCode)
	}
	return nil
}
