package fnosproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/requests"
)

const nativeStreamPath = "/v/api/v1/stream"

// 原生请求的签名和认证由飞牛校验；只使用这次成功响应中已经授权的文件身份与路径。
func (inst *instance) rewriteNativeStream(response *http.Response) error {
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
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	var code *int
	if json.Unmarshal(payload["code"], &code) != nil || code == nil || *code != 0 {
		return nil
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(payload["data"], &data) != nil {
		return nil
	}
	var file struct {
		GUID      string `json:"guid"`
		Path      string `json:"path"`
		Type      int    `json:"type"`
		CanPlay   int    `json:"can_play"`
		PlayError string `json:"play_error"`
	}
	var video struct {
		MediaGUID string `json:"media_guid"`
		CodecType string `json:"codec_type"`
	}
	var storage struct {
		Valid            *bool `json:"valid"`
		Disabled         *bool `json:"disabled"`
		CloudStorageType int   `json:"cloud_storage_type"`
	}
	if json.Unmarshal(data["file_stream"], &file) != nil || json.Unmarshal(data["video_stream"], &video) != nil ||
		json.Unmarshal(data["cloud_storage_info"], &storage) != nil || !safeIdentifier(file.GUID) ||
		video.MediaGUID != file.GUID || video.CodecType != "video" || file.Type != 1 || file.CanPlay != 1 ||
		file.PlayError != "" || storage.Valid == nil || !*storage.Valid || storage.Disabled == nil || *storage.Disabled ||
		storage.CloudStorageType != 9001 {
		return nil
	}
	var qualities []map[string]json.RawMessage
	if json.Unmarshal(data["direct_link_qualities"], &qualities) != nil || len(qualities) == 0 || len(qualities) > sourceCacheCapacity {
		return nil
	}
	ctx, cancel := context.WithTimeout(response.Request.Context(), 30*time.Second)
	defer cancel()
	request := response.Request.Clone(ctx)
	playerUA := ""
	if raw, present := data["header"]; present {
		var headers http.Header
		if json.Unmarshal(raw, &headers) != nil {
			return nil
		}
		seen := false
		for name, values := range headers {
			if !strings.EqualFold(name, "User-Agent") {
				continue
			}
			if seen || len(values) != 1 || values[0] == "" || len(values[0]) > 8192 || strings.ContainsAny(values[0], "\r\n\x00") {
				return nil
			}
			seen = true
			request.Header.Set("User-Agent", values[0])
			playerUA = values[0]
		}
	}
	changed := false
	for _, quality := range qualities {
		var rawURL string
		var isM3U8 *bool
		if json.Unmarshal(quality["url"], &rawURL) != nil || rawURL == "" ||
			json.Unmarshal(quality["is_m3u8"], &isM3U8) != nil || isM3U8 == nil || *isM3U8 {
			continue
		}
		release, admitted := backup.AcquireRuntimeRequest()
		if !admitted {
			return nil
		}
		source := Source{ItemID: "native-" + file.GUID, ID: file.GUID, Path: file.Path, URL: rawURL}
		resolved, resolvedOK := inst.resolveSource(ctx, request, source)
		release()
		if !resolvedOK || !sameSourceURL(rawURL, resolved.url) {
			continue
		}
		playbackURL, ok := inst.nativePlaybackURL(request, resolved)
		if !ok || playbackURL == rawURL {
			continue
		}
		inst.rememberNativePlayer(response.Request, playerUA, file.GUID)
		quality["url"], err = json.Marshal(playbackURL)
		if err != nil {
			return nil
		}
		changed = true
	}
	if !changed || ctx.Err() != nil || inst.ctx.Err() != nil {
		return nil
	}
	data["direct_link_qualities"], err = json.Marshal(qualities)
	if err != nil {
		return nil
	}
	payload["data"], err = json.Marshal(data)
	if err != nil {
		return nil
	}
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	response.Body = io.NopCloser(bytes.NewReader(rewritten))
	response.ContentLength = int64(len(rewritten))
	response.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
	response.Header.Set("Content-Type", "application/json; charset=utf-8")
	response.Header.Set("Cache-Control", "no-store, private")
	for _, header := range []string{"Content-Encoding", "ETag", "Content-MD5", "Digest", "Accept-Ranges"} {
		response.Header.Del(header)
	}
	return nil
}

func (inst *instance) nativePlaybackURL(r *http.Request, resolved sourceRecord) (string, bool) {
	if _, ok := inst.internalURL(r.Context(), resolved.url); !ok || inst.service.options.MainHandler == nil {
		return "", false
	}
	// 原生入口是 POST 元数据请求，既有取链入口仍须按 GET 调用；不跟随最终 CDN。
	request := r.Clone(r.Context())
	request.Method = http.MethodGet
	capture := &redirectCapture{header: make(http.Header)}
	inst.serveResolved(capture, request, resolved)
	if r.Context().Err() != nil || inst.ctx.Err() != nil {
		return "", false
	}
	switch capture.status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return "", false
	}
	location := capture.header.Get("Location")
	target, err := url.Parse(location)
	if err != nil || target.User != nil || target.Opaque != "" || target.Fragment != "" || strings.ContainsAny(location, "\r\n\x00") {
		return "", false
	}
	if target.Host != "" && (target.Scheme == "http" || target.Scheme == "https") {
		return location, true
	}
	if target.IsAbs() || target.Host != "" || target.Path != "/proxy-115" || target.RawPath != "" {
		return "", false
	}
	query, err := url.ParseQuery(target.RawQuery)
	proxyRequest := requests.Proxy115Request{URL: query.Get("url"), BaiduPan: query.Get("baidupan")}
	return location, err == nil && len(query["url"]) == 1 && len(query["baidupan"]) <= 1 && proxyRequest.Validate() == nil
}

type redirectCapture struct {
	header http.Header
	status int
}

func (capture *redirectCapture) Header() http.Header { return capture.header }

func (capture *redirectCapture) WriteHeader(status int) {
	if capture.status == 0 && status >= http.StatusOK {
		capture.status = status
	}
}

func (capture *redirectCapture) Write(body []byte) (int, error) {
	if capture.status == 0 {
		capture.status = http.StatusOK
	}
	return len(body), nil
}
