package fnosproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/playback"
	"qmediasync/internal/requests"
)

const metadataLimit = 2 << 20

var (
	playbackInfoRoute = regexp.MustCompile(`(?i)^/Items/([^/]+)/PlaybackInfo$`)
	streamRoute       = regexp.MustCompile(`(?i)^/Videos/([^/]+)/(?:stream|original)(?:\.[a-z0-9]+)?$`)
	masterRoute       = regexp.MustCompile(`(?i)^/Videos/([^/]+)/master\.m3u8$`)
	fileRoute         = regexp.MustCompile(`(?i)^/Items/([^/]+)/(?:Download|File)$`)
)

type playbackRoute struct {
	itemID string
	kind   string
}

func parsePlaybackRoute(value string) (playbackRoute, bool) {
	value = upstreamPath(value)
	for _, route := range []struct {
		pattern *regexp.Regexp
		kind    string
	}{{playbackInfoRoute, "info"}, {streamRoute, "stream"}, {masterRoute, "stream"}, {fileRoute, "file"}} {
		if match := route.pattern.FindStringSubmatch(value); len(match) == 2 && safeIdentifier(match[1]) {
			return playbackRoute{itemID: match[1], kind: route.kind}, true
		}
	}
	return playbackRoute{}, false
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func queryValue(query url.Values, key string) (string, bool) {
	var value string
	found := false
	for name, values := range query {
		if !strings.EqualFold(name, key) {
			continue
		}
		if found || len(values) != 1 {
			return "", false
		}
		value, found = values[0], true
	}
	return value, true
}

func credentialFingerprint(r *http.Request) string {
	headers, token, _ := effectiveCredentials(r)
	return fingerprint([]any{headers.Values("X-Emby-Authorization"), headers.Values("Authorization"),
		headers.Values("X-Emby-Token"), headers.Values("X-MediaBrowser-Token"), headers.Values("Cookie"), token})
}

func requestToken(r *http.Request) string {
	_, token, ok := effectiveCredentials(r)
	if !ok {
		return ""
	}
	return token
}

func effectiveCredentials(r *http.Request) (http.Header, string, bool) {
	headers := upstreamHeaders(r.Header)
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return headers, "", false
	}
	selected, ok := queryValue(query, "api_key")
	if !ok {
		return headers, "", false
	}
	authHasToken := make(map[string]bool, 2)
	for _, name := range []string{"X-Emby-Authorization", "Authorization", "X-Emby-Token", "X-MediaBrowser-Token"} {
		values := headers.Values(name)
		if len(values) > 1 {
			return headers, "", false
		}
		if len(values) == 0 {
			continue
		}
		value := values[0]
		token := value
		if name == "X-Emby-Authorization" || name == "Authorization" {
			attributes, valid := playback.EmbyAuthAttributes(value)
			tokens := attributes["token"]
			if !valid || len(tokens) > 1 || len(tokens) == 1 && tokens[0] == "" {
				return headers, "", false
			}
			if len(tokens) == 1 {
				token = tokens[0]
				authHasToken[name] = true
			} else if strings.HasPrefix(strings.ToLower(value), "bearer ") {
				token = strings.TrimSpace(value[len("bearer "):])
			} else {
				token = ""
				if strings.Contains(strings.ToLower(value), "token=") {
					return headers, "", false
				}
			}
		}
		if token != "" {
			if selected != "" && selected != token {
				return headers, "", false
			}
			selected = token
		}
	}
	if strings.ContainsAny(selected, "\"\r\n\\") {
		return headers, "", false
	}
	if selected != "" {
		if current := headers.Get("X-Emby-Authorization"); current != "" {
			if !authHasToken["X-Emby-Authorization"] {
				headers.Set("X-Emby-Authorization", current+`, Token="`+selected+`"`)
			}
		} else if strings.HasPrefix(strings.ToLower(headers.Get("Authorization")), "mediabrowser ") {
			if !authHasToken["Authorization"] {
				headers.Set("Authorization", headers.Get("Authorization")+`, Token="`+selected+`"`)
			}
		} else {
			headers.Set("X-Emby-Authorization", `MediaBrowser Client="QMediaSync", Device="Playback", DeviceId="qms-fnos-proxy", Version="1.0", Token="`+selected+`"`)
		}
	}
	return headers, selected, selected != "" || headers.Get("Cookie") != ""
}

func (inst *instance) metadata(r *http.Request, endpoint string) (map[string]any, bool) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	target := *inst.upstream
	target.Path, target.RawPath = endpoint, ""
	query := url.Values{"Fields": {"Path,MediaSources"}}
	if token := requestToken(r); token != "" {
		query.Set("api_key", token)
	}
	target.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, false
	}
	var valid bool
	request.Header, _, valid = effectiveCredentials(r)
	if !valid {
		return nil, false
	}
	request.Header.Del("Range")
	request.Header.Del("If-Range")
	request.Header.Del("Content-Length")
	request.Header.Del("Content-Type")
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		request.Header.Del(name)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{Transport: inst.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return nil, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, false
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, metadataLimit+1))
	if err != nil || len(encoded) > metadataLimit {
		return nil, false
	}
	body, ok := decodeMetadata(encoded, response.Header.Get("Content-Encoding"))
	if !ok {
		return nil, false
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, false
	}
	return payload, true
}

func (inst *instance) authorizedSources(r *http.Request, itemID, kind string) ([]Source, bool) {
	user, ok := inst.metadata(r, "/Users/Me")
	if !ok || !allowsDirectPlayback(user, kind) {
		return nil, false
	}
	userID := stringField(user, "Id")
	if !safeIdentifier(userID) {
		return nil, false
	}
	item, ok := inst.metadata(r, "/Users/"+userID+"/Items/"+itemID)
	if !ok || stringField(item, "Id") != itemID {
		return nil, false
	}
	return itemSources(itemID, item), true
}

func (inst *instance) authorizedSource(r *http.Request, itemID string) (Source, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !allowsDirectRequest(query) {
		return Source{}, false
	}
	requested, valid := queryValue(query, "MediaSourceId")
	if !valid {
		return Source{}, false
	}
	route, _ := parsePlaybackRoute(r.URL.Path)
	sources, authorized := inst.authorizedSources(r, itemID, route.kind)
	if !authorized {
		return Source{}, false
	}
	if requested == "" {
		if len(sources) != 1 {
			return Source{}, false
		}
		return sources[0], true
	}
	for _, source := range sources {
		if source.ID == requested {
			return source, true
		}
	}
	return Source{}, false
}

func allowsDirectPlayback(user map[string]any, kind string) bool {
	policy, _ := user["Policy"].(map[string]any)
	for name, wanted := range map[string]bool{"IsDisabled": false, "EnableMediaPlayback": true, "ForceRemoteSourceTranscoding": false} {
		if value, present := policy[name].(bool); present && value != wanted {
			return false
		}
	}
	if kind == "file" {
		if enabled, present := policy["EnableContentDownloading"].(bool); present && !enabled {
			return false
		}
	}
	return true
}

func itemSources(itemID string, item map[string]any) []Source {
	media, _ := item["MediaSources"].([]any)
	if len(media) > sourceCacheCapacity {
		return nil
	}
	var sources []Source
	seen := make(map[string]bool)
	for _, raw := range media {
		value, ok := raw.(map[string]any)
		if !ok {
			return nil
		}
		id, mediaPath := stringField(value, "Id"), stringField(value, "Path")
		if !safeIdentifier(id) || seen[id] || mediaPath == "" {
			return nil
		}
		seen[id] = true
		source := Source{ItemID: itemID, ID: id, Path: mediaPath}
		if parsed, err := url.Parse(mediaPath); err == nil && parsed.Host != "" && len(media) == 1 {
			itemPath := stringField(item, "Path")
			if strings.HasSuffix(strings.ToLower(itemPath), ".strm") {
				source.Path, source.URL = itemPath, mediaPath
			}
		}
		sources = append(sources, source)
	}
	return sources
}

func stringField(value map[string]any, key string) string {
	for name, raw := range value {
		if strings.EqualFold(name, key) {
			result, _ := raw.(string)
			return result
		}
	}
	return ""
}

func decodeMetadata(encoded []byte, encoding string) ([]byte, bool) {
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return encoded, true
	}
	if !strings.EqualFold(encoding, "gzip") {
		return nil, false
	}
	reader, err := gzip.NewReader(bytes.NewReader(encoded))
	if err != nil {
		return nil, false
	}
	defer reader.Close()
	decoded, err := io.ReadAll(io.LimitReader(reader, metadataLimit+1))
	return decoded, err == nil && len(decoded) <= metadataLimit
}

func allowsDirectRequest(query url.Values) bool {
	for name := range query {
		if !strings.EqualFold(name, "EnableDirectPlay") && !strings.EqualFold(name, "EnableDirectStream") {
			continue
		}
		enabled, valid := queryValue(query, name)
		if !valid || !strings.EqualFold(enabled, "true") {
			return false
		}
	}
	static, validStatic := queryValue(query, "static")
	method, validMethod := queryValue(query, "PlayMethod")
	return validStatic && validMethod && !strings.EqualFold(static, "false") && !strings.EqualFold(method, "Transcode")
}

func allowsDirectPlaybackInfoBody(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return true
	}
	if r.GetBody == nil {
		return false
	}
	body, err := r.GetBody()
	if err != nil {
		return false
	}
	defer body.Close()
	decoder := json.NewDecoder(body)
	opening, err := decoder.Token()
	if err == io.EOF {
		return true
	}
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool, 2)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		name := strings.ToLower(key.(string))
		if name != "enabledirectplay" && name != "enabledirectstream" {
			continue
		}
		if seen[name] || string(value) != "true" {
			return false
		}
		seen[name] = true
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return false
	}
	return decoder.Decode(new(json.RawMessage)) == io.EOF
}

func (inst *instance) rewritePlaybackInfo(response *http.Response, itemID string) error {
	query, err := url.ParseQuery(response.Request.URL.RawQuery)
	if err != nil || !allowsDirectRequest(query) || !allowsDirectPlaybackInfoBody(response.Request) {
		return nil
	}
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
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return nil
	}
	media, _ := payload["MediaSources"].([]any)
	if len(media) == 0 || len(media) > sourceCacheCapacity {
		return nil
	}
	release, admitted := backup.AcquireRuntimeRequest()
	if !admitted {
		return nil
	}
	defer release()
	authorized, ok := inst.authorizedSources(response.Request, itemID, "info")
	if !ok {
		return nil
	}
	changed := false
	observedSources := itemSources(itemID, payload)
	for index, raw := range media {
		value, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := stringField(value, "Id")
		for _, source := range authorized {
			if source.ID != id || stringField(value, "Path") != source.Path && stringField(value, "Path") != source.URL {
				continue
			}
			if _, ok := inst.resolveSource(response.Request.Context(), response.Request, source); !ok {
				continue
			}
			if len(observedSources) == len(media) {
				observedSources[index] = source
			}
			query := url.Values{"MediaSourceId": {source.ID}, "static": {"true"}}
			if token := requestToken(response.Request); token != "" {
				query.Set("api_key", token)
			}
			value["DirectStreamUrl"] = "/Videos/" + itemID + "/stream?" + query.Encode()
			value["SupportsDirectPlay"], value["SupportsDirectStream"], value["SupportsTranscoding"] = true, true, false
			value["Protocol"], value["IsRemote"] = "Http", true
			for _, field := range []string{"TranscodingUrl", "TranscodingSubProtocol", "TranscodingContainer"} {
				delete(value, field)
			}
			changed = true
		}
	}
	inst.observeCompatibleSource(response.Request, itemID, observedSources, payload)
	if !changed {
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

type replayBody struct {
	io.Reader
	io.Closer
}

func (inst *instance) internalURL(ctx context.Context, raw string) (*url.URL, bool) {
	target, err := url.Parse(raw)
	if err != nil || target.User != nil || target.Opaque != "" || target.Fragment != "" || target.RawPath != "" ||
		(target.Scheme != "http" && target.Scheme != "https") || inst.service.options.KnownOrigins == nil {
		return nil, false
	}
	origins, err := inst.service.options.KnownOrigins(ctx)
	if err != nil {
		return nil, false
	}
	for _, rawOrigin := range origins {
		origin, err := url.Parse(rawOrigin)
		if err != nil || origin.User != nil || origin.Scheme != target.Scheme || !strings.EqualFold(origin.Host, target.Host) {
			continue
		}
		prefix := strings.TrimRight(origin.Path, "/")
		endpoint, matched := strings.CutPrefix(target.Path, prefix)
		if !matched || !validInternalEndpoint(endpoint, target.RawQuery) {
			continue
		}
		copy := *target
		copy.Path, copy.RawPath = endpoint, ""
		return &copy, true
	}
	return nil, false
}

func validInternalEndpoint(endpoint, rawQuery string) bool {
	if path.Clean(endpoint) != endpoint || strings.ContainsAny(endpoint, "\\\x00") {
		return false
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}
	for _, values := range query {
		if len(values) != 1 {
			return false
		}
	}
	if endpoint == "/openlist/url" {
		accountID, err := strconv.ParseUint(query.Get("account_id"), 10, 32)
		if err != nil {
			return false
		}
		request := requests.OpenListFileURLRequest{AccountID: uint(accountID), Path: query.Get("path")}
		return request.Validate() == nil
	}
	if endpoint != "/115/newurl" &&
		!(strings.HasPrefix(endpoint, "/115/url/") && len(endpoint) > len("/115/url/")) &&
		!(strings.HasPrefix(endpoint, "/baidupan/url/") && len(endpoint) > len("/baidupan/url/")) {
		return false
	}
	force := 0
	if raw := query.Get("force"); raw != "" {
		force, err = strconv.Atoi(raw)
		if err != nil {
			return false
		}
	}
	request := requests.RemoteFileURLRequest{UserID: query.Get("userid"), PickCode: query.Get("pickcode"), Force: force}
	return request.Validate() == nil
}

func (inst *instance) serveResolved(w http.ResponseWriter, r *http.Request, resolved sourceRecord) {
	if target, ok := inst.internalURL(r.Context(), resolved.url); ok && inst.service.options.MainHandler != nil {
		cloned := r.Clone(r.Context())
		cloned.URL = target
		cloned.RequestURI = target.RequestURI()
		cloned.Host = target.Host
		cloned.Header = playbackHeaders(r.Header)
		cloned.Body, cloned.GetBody, cloned.ContentLength = nil, nil, 0
		if r.Method == http.MethodHead {
			cloned.Method = http.MethodGet
			inst.service.options.MainHandler.ServeHTTP(&headWriter{ResponseWriter: w}, cloned)
		} else {
			inst.service.options.MainHandler.ServeHTTP(w, cloned)
		}
		return
	}
	w.Header().Set("Location", resolved.url)
	w.WriteHeader(http.StatusTemporaryRedirect)
}

func playbackHeaders(source http.Header) http.Header {
	result := make(http.Header)
	for _, name := range []string{"User-Agent", "Range", "If-Range", "If-None-Match", "If-Modified-Since", "Accept", "Accept-Language"} {
		for _, value := range source.Values(name) {
			result.Add(name, value)
		}
	}
	return result
}

func (inst *instance) forwardProxy(w http.ResponseWriter, r *http.Request) {
	if inst.service.options.MainHandler == nil {
		http.NotFound(w, r)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	request := requests.Proxy115Request{URL: query.Get("url"), BaiduPan: query.Get("baidupan")}
	if err != nil || len(query["url"]) != 1 || len(query["baidupan"]) > 1 || request.Validate() != nil {
		http.Error(w, "播放代理地址无效", http.StatusBadRequest)
		return
	}
	cloned := r.Clone(r.Context())
	cloned.Header = playbackHeaders(r.Header)
	inst.service.options.MainHandler.ServeHTTP(w, cloned)
}

type headWriter struct{ http.ResponseWriter }

func (w *headWriter) Write(body []byte) (int, error) { return len(body), nil }
