package embyclientrestgo

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (c *Client) embyRequest(ctx context.Context, method, path string, params url.Values) (*http.Request, error) {
	params = maps.Clone(params)
	if params == nil {
		params = url.Values{}
	}
	params.Set("api_key", c.apiKey)
	return newRequest(ctx, method, strings.TrimRight(c.embyURL, "/")+"/emby"+path, params)
}

func newRequest(ctx context.Context, method, address string, params url.Values) (*http.Request, error) {
	if len(params) > 0 {
		address += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, address, nil)
	if err != nil {
		return nil, errors.New("创建媒体服务器请求失败")
	}
	req.Header.Set("Accept", "application/json")
	return req, nil
}

func embyRefreshParams(recursive bool) url.Values {
	return url.Values{"Fields": {"MediaStreams"}, "Recursive": {strconv.FormatBool(recursive)}}
}

func embyDetailPath(itemID, userID string) (string, url.Values) {
	return "/Users/" + url.PathEscape(userID) + "/Items/" + url.PathEscape(itemID), nil
}

const embyRefreshLookupItemTypes = "Movie,Video,Episode,Folder,Series"

// EmbySnapshotFields 是物理索引快照必须显式请求的字段。
const EmbySnapshotFields = "DateCreated,DateModified,ParentId,PremiereDate,Path,MediaSources,PartCount,SeriesId,SeasonId,SeriesName,SeasonName,IndexNumber,ParentIndexNumber"
