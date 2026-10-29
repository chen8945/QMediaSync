package fnosproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var compatibleItemsRoute = regexp.MustCompile(`^(?:/Users/([^/]+))?/Items$`)

func compatibleItemMetadataRoute(route string) bool {
	if match := preloadDetailRoute.FindStringSubmatch(route); len(match) == 3 {
		return (match[1] == "" || safeIdentifier(match[1])) && safeIdentifier(match[2])
	}
	if match := compatibleItemsRoute.FindStringSubmatch(route); len(match) == 2 {
		return match[1] == "" || safeIdentifier(match[1])
	}
	match := preloadQueueRoute.FindStringSubmatch(route)
	return len(match) == 2 && safeIdentifier(match[1])
}

func rewriteCompatibleItemRuntime(response *http.Response) error {
	encoded, err := io.ReadAll(io.LimitReader(response.Body, metadataLimit+1))
	if err != nil {
		return err
	}
	if len(encoded) > metadataLimit {
		response.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(encoded), response.Body), Closer: response.Body}
		return nil
	}
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(encoded))
	body, ok := decodeMetadata(encoded, response.Header.Get("Content-Encoding"))
	if !ok {
		return nil
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil
	}
	changed := false
	if match := preloadDetailRoute.FindStringSubmatch(upstreamPath(response.Request.URL.Path)); len(match) == 3 {
		if rewritten, ok := normalizedItemRuntime(body, match[2]); ok {
			setMetadataBody(response, rewritten)
		}
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(payload["Items"], &items) != nil || len(items) > sourceCacheCapacity {
		return nil
	}
	for index, item := range items {
		if rewritten, ok := normalizedItemRuntime(item, ""); ok {
			items[index], changed = rewritten, true
		}
	}
	if changed {
		payload["Items"], err = json.Marshal(items)
		if err == nil {
			if rewritten, err := json.Marshal(payload); err == nil {
				setMetadataBody(response, rewritten)
			}
		}
	}
	return nil
}

func normalizedItemRuntime(body json.RawMessage, expectedID string) ([]byte, bool) {
	var item struct {
		ID           string `json:"Id"`
		Type         string
		Path         string
		RunTimeTicks int64
		MediaSources []struct {
			ID           string `json:"Id"`
			Path         string
			RunTimeTicks int64
			MediaStreams []struct{ Type, Codec string }
		}
	}
	if json.Unmarshal(body, &item) != nil || !safeIdentifier(item.ID) || expectedID != "" && item.ID != expectedID ||
		item.Type != "Movie" && item.Type != "Episode" || len(item.MediaSources) != 1 {
		return nil, false
	}
	source := item.MediaSources[0]
	strm := strings.HasSuffix(strings.ToLower(source.Path), ".strm")
	if !strm && strings.HasSuffix(strings.ToLower(item.Path), ".strm") {
		target, err := url.Parse(source.Path)
		strm = err == nil && target.Host != "" && (target.Scheme == "http" || target.Scheme == "https")
	}
	if !safeIdentifier(source.ID) || !strm ||
		source.RunTimeTicks <= 0 || source.RunTimeTicks > int64(24*time.Hour/(100*time.Nanosecond)) || item.RunTimeTicks == source.RunTimeTicks {
		return nil, false
	}
	for _, stream := range source.MediaStreams {
		// 未探测的飞牛 STRM 可能只有占位时长，不能用于校正条目。
		if stream.Type != "Video" || strings.TrimSpace(stream.Codec) == "" {
			continue
		}
		var payload map[string]json.RawMessage
		if json.Unmarshal(body, &payload) != nil {
			return nil, false
		}
		payload["RunTimeTicks"], _ = json.Marshal(source.RunTimeTicks)
		rewritten, err := json.Marshal(payload)
		return rewritten, err == nil
	}
	return nil, false
}
