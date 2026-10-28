package fnosproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	"qmediasync/internal/requests"
)

const (
	preloadObservationTTL = 15 * time.Minute
	preloadAssociationTTL = 60 * time.Second
)

var (
	preloadDetailRoute = regexp.MustCompile(`^/Users/([^/]+)/Items/([^/]+)$`)
	preloadQueueRoute  = regexp.MustCompile(`^/Shows/([^/]+)/Episodes$`)
)

type preloadItem struct {
	id, kind, series, season string
	display                  preloadDisplay
	association              string
	selected                 string
	enriched                 bool
	resumable                bool
	sources                  []Source
	duration                 time.Duration
}

type nativePreloadInfo struct {
	requested, logical, file, video, parent, kind string
	display                                       preloadDisplay
}

type preloadDevice struct {
	weakIdentity     bool
	used             time.Time
	requested        string
	sequence         uint64
	detailResponded  uint64
	parentSequence   uint64
	progressSequence uint64
	sourceSequence   uint64
	sourceResponded  uint64
	queueSequence    uint64
	queueResponded   uint64
	playerSequence   uint64
	playingEpoch     uint64
	playerEpoch      uint64
	detailAt         time.Time
	detail           preloadItem
	info             nativePreloadInfo
	infoAt           time.Time
	source           preloadItem
	sourceAt         time.Time
	userID           string
	controlUA        string
	controlSequence  uint64
	playerUA         string
	playerSource     Source
	playerSession    string
	queue            []preloadItem
	queueAt          time.Time
	queueUser        string
	queueSeries      string
	playing          preloadItem
	session          string
	playingID        string
}

type preloadState struct {
	mu         sync.Mutex
	generation uint64
	sequence   uint64
	devices    map[string]preloadDevice
	uas        playback.PreloadUAMappings
}

func (state *preloadState) clear() {
	state.mu.Lock()
	clear(state.devices)
	state.uas.Clear()
	state.mu.Unlock()
}

// 调用方持有 mu；同一短临界区同时维护最新请求、资料关联和候选提交。
func (state *preloadState) update(scope string, generation uint64, update func(*preloadDevice)) (preloadDevice, bool) {
	if generation < state.generation {
		return preloadDevice{}, false
	}
	if state.devices == nil || state.generation != generation {
		state.devices = make(map[string]preloadDevice)
		state.generation = generation
	}
	now := time.Now()
	for key, previous := range state.devices {
		if now.Sub(previous.used) >= preloadObservationTTL {
			delete(state.devices, key)
		}
	}
	device, exists := state.devices[scope]
	if !exists && len(state.devices) >= sourceCacheCapacity {
		return preloadDevice{}, false
	}
	device.used = now
	update(&device)
	state.devices[scope] = device
	return device, true
}

type preloadRequestSequence struct{}
type preloadParentSequence struct{}

func nativePreloadParent(r *http.Request, device preloadDevice, id string) bool {
	if device.controlUA != r.UserAgent() || device.detail.kind != "Season" || device.requested != device.detail.id ||
		device.detailResponded != device.sequence || device.detail.season != id || !safeIdentifier(id) || time.Since(device.detailAt) > preloadAssociationTTL || len(r.Referer()) > 8192 {
		return false
	}
	referer, err := url.Parse(r.Referer())
	return err == nil && referer.User == nil && referer.Opaque == "" && referer.Fragment == "" && referer.RawPath == "" &&
		(referer.Scheme == "http" || referer.Scheme == "https") && strings.EqualFold(referer.Host, r.Host) && referer.Path == "/v/tv/season/"+device.detail.id
}

func currentPreloadDetail(r *http.Request, device preloadDevice, sequence uint64) bool {
	if parent, ok := r.Context().Value(preloadParentSequence{}).(uint64); ok {
		return device.sequence == parent && device.parentSequence == sequence
	}
	return device.sequence == sequence
}

func (inst *instance) beginPreloadRequest(r *http.Request) {
	if !inst.preloadEnabled() {
		return
	}
	route := upstreamPath(r.URL.Path)
	progress := r.Method == http.MethodPost && preloadProgressRoute(route)
	play, playbackRoute := parsePlaybackRoute(route)
	source := r.Method == http.MethodPost && route == "/v/api/v1/play/info" || playbackRoute && play.kind == "info" && (r.Method == http.MethodGet || r.Method == http.MethodPost)
	player := r.Method == http.MethodPost && route == nativeStreamPath || playbackRoute && play.kind != "info" && (r.Method == http.MethodGet || r.Method == http.MethodHead)
	queue := r.Method == http.MethodGet && preloadQueueRoute.MatchString(route)
	id, native := "", strings.HasPrefix(route, "/v/api/v1/item/")
	if native {
		id = strings.TrimPrefix(route, "/v/api/v1/item/")
	} else if match := preloadDetailRoute.FindStringSubmatch(route); len(match) == 3 {
		id = match[2]
	}
	if !progress && !source && !player && !queue && (r.Method != http.MethodGet || !safeIdentifier(id)) {
		return
	}
	native = native || route == "/v/api/v1/play/info" || route == nativeStreamPath
	scope, ok := inst.preloadScope(r, native)
	if !ok {
		return
	}
	deviceID, _ := preloadDeviceID(r)
	weakIdentity := deviceID == "" && !(native && nativePreloadSession(r))
	var report preloadProgressReport
	startReport := progress && route == "/Sessions/Playing"
	validReport := progress && preloadRequestBody(r, &report) && safeIdentifier(report.ItemID)
	validRange := false
	if player && playbackRoute && r.Header.Get("Range") != "" {
		query, err := url.ParseQuery(r.URL.RawQuery)
		selected, validSource := queryValue(query, "MediaSourceId")
		session, validSession := queryValue(query, "PlaySessionId")
		report = preloadProgressReport{ItemID: play.itemID, MediaSourceID: selected, PlaySessionID: session}
		validRange = err == nil && validSource && validSession && (session == "" || safeIdentifier(session))
	}
	inst.preload.mu.Lock()
	defer inst.preload.mu.Unlock()
	inst.preload.sequence++
	sequence, changed, parent := inst.preload.sequence, false, uint64(0)
	_, ok = inst.preload.update(scope, models.GetPlaybackSettingsSnapshot().Generation, func(device *preloadDevice) {
		device.weakIdentity = weakIdentity
		if !device.weakIdentity && device.controlUA == r.UserAgent() && device.controlUA != "" {
			inst.preload.uas.Lookup(inst.preloadUAKey(scope, *device), inst.preload.generation)
		}
		auxiliary := native && r.Method == http.MethodGet && nativePreloadParent(r, *device, id)
		if !player && !progress && !auxiliary {
			controlChanged := device.controlUA != "" && device.controlUA != r.UserAgent()
			if controlChanged {
				device.playerUA, device.playerSession, device.session, device.playingID = "", "", "", ""
				device.playerSource, device.playing = Source{}, preloadItem{}
				device.info, device.source, device.queue = nativePreloadInfo{}, preloadItem{}, nil
				device.detail.sources, device.detail.selected = nil, ""
				inst.service.options.Preloader.CancelNext(scope)
				inst.service.options.Preloader.CancelDetail(scope)
			}
			device.controlUA, device.controlSequence = r.UserAgent(), sequence
		}
		switch {
		case player:
			device.playerSequence, device.progressSequence = sequence, sequence
			inst.service.options.Preloader.TakeoverScope(scope)
			if !validRange || !compatiblePreloadRange(report, r.UserAgent(), *device) {
				device.playerUA, device.playerEpoch = "", 0
			}
		case progress:
			// 已知当前播放时，旧进度或停止不能抢占尚在途的实际取流关联。
			if !startReport && (device.playingID != "" || device.playerUA != "") && (!validReport || !report.matchesPlayback(*device)) {
				return
			}
			if validReport && (report.IsPaused != nil && *report.IsPaused || strings.EqualFold(report.PlayMethod, "Transcode")) {
				inst.service.options.Preloader.CancelNext(scope)
			}
			if startReport && !(validReport && report.PlaySessionID != "" && report.PlaySessionID == device.session && report.matchesPlayback(*device)) {
				fresh := validReport && device.playerUA != "" && device.playerSequence > device.playingEpoch &&
					device.playerSource.ItemID == report.ItemID && (report.MediaSourceID == "" || report.MediaSourceID == device.playerSource.ID) &&
					(report.PlaySessionID == "" || device.playerSession == "" || report.PlaySessionID == device.playerSession)
				device.playingEpoch, device.playerEpoch = sequence, 0
				device.playing, device.session, device.playingID = preloadItem{}, "", ""
				if fresh && !strings.EqualFold(report.PlayMethod, "Transcode") {
					device.playerEpoch = sequence
					if report.PlaySessionID != "" {
						device.playerSession = report.PlaySessionID
					}
				} else {
					device.playerUA, device.playerSession, device.playerSource = "", "", Source{}
				}
				inst.service.options.Preloader.CancelNext(scope)
			}
			device.progressSequence = sequence
		case source:
			device.sourceSequence = sequence
		case queue:
			device.queueSequence = sequence
		default:
			if auxiliary {
				parent, device.parentSequence = device.sequence, sequence
				return
			}
			selected, _ := queryValue(r.URL.Query(), "MediaSourceId")
			changed = device.requested != id || selected != "" && selected != device.detail.selected
			device.requested, device.sequence = id, sequence
		}
	})
	if ok {
		*r = *r.WithContext(context.WithValue(r.Context(), preloadRequestSequence{}, sequence))
		if parent != 0 {
			*r = *r.WithContext(context.WithValue(r.Context(), preloadParentSequence{}, parent))
		}
		if changed {
			inst.service.options.Preloader.CancelDetail(scope)
		}
	}
}

func compatiblePreloadRange(report preloadProgressReport, ua string, device preloadDevice) bool {
	if device.playerUA == "" || device.playerUA != ua || !report.matchesPlayback(device) || device.playerSource.ItemID != report.ItemID ||
		device.playingID != "" && (len(device.playing.sources) != 1 || device.playing.sources[0] != device.playerSource) {
		return false
	}
	latest, revision := preloadItem{}, uint64(0)
	for _, candidate := range []struct {
		item     preloadItem
		sequence uint64
	}{{device.detail, device.detailResponded}, {device.source, device.sourceResponded}} {
		if candidate.item.id == report.ItemID && len(candidate.item.sources) != 0 && candidate.sequence >= revision {
			latest, revision = candidate.item, candidate.sequence
		}
	}
	if device.queueResponded > revision {
		for _, item := range device.queue {
			if item.id == report.ItemID && len(item.sources) != 0 {
				latest = item
				break
			}
		}
	}
	if len(latest.sources) != 0 {
		selected := report.MediaSourceID
		if selected == "" {
			selected = latest.selected
		}
		source, valid := selectPreloadSource(latest.sources, selected)
		return valid && source == device.playerSource
	}
	return true
}

// Range 只保留旧成功流的证明；现有授权和解析结果不能证明同一完整源时立即清除。
func (inst *instance) reconcilePreloadPlayer(r *http.Request, source Source, usable bool) {
	if !inst.preloadEnabled() {
		return
	}
	scope, ok := inst.preloadScope(r, false)
	sequence, present := r.Context().Value(preloadRequestSequence{}).(uint64)
	if !ok || !present {
		return
	}
	inst.preload.mu.Lock()
	defer inst.preload.mu.Unlock()
	device, found := inst.preload.devices[scope]
	if found && device.playerSequence == sequence && sequence >= device.sequence && sequence >= device.controlSequence &&
		device.playerUA != "" && (!usable || device.playerSource != source) {
		device.playerUA, device.playerEpoch = "", 0
		inst.preload.devices[scope] = device
		inst.service.options.Preloader.CancelNext(scope)
	}
}

func (inst *instance) preloadEnabled() bool {
	return inst.ctx.Err() == nil && inst.service.options.Preloader != nil &&
		inst.service.options.NewPreload != nil && inst.service.options.Preloader.Enabled()
}

func preloadNativePath(value string) bool {
	return strings.HasPrefix(value, "/v/api/v1/item/") || value == "/v/api/v1/play/info"
}

func preloadProgressRoute(value string) bool {
	return value == "/Sessions/Playing" || value == "/Sessions/Playing/Progress" || value == "/Sessions/Playing/Stopped"
}

func preloadDeviceID(r *http.Request) (string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return "", false
	}
	var device, client string
	accept := func(result *string, values []string) bool {
		for _, value := range values {
			if value == "" || len(value) > 256 || strings.IndexFunc(value, func(c rune) bool { return c < 0x20 || c == 0x7f }) >= 0 || *result != "" && *result != value {
				return false
			}
			*result = value
		}
		return true
	}
	for name, values := range r.Header {
		if strings.EqualFold(name, "X-Emby-Device-Id") && !accept(&device, values) ||
			strings.EqualFold(name, "X-Emby-Client") && !accept(&client, values) {
			return "", false
		}
		if strings.EqualFold(name, "X-Emby-Authorization") || strings.EqualFold(name, "Authorization") {
			for _, value := range values {
				attributes, valid := playback.EmbyAuthAttributes(value)
				if !valid || !accept(&device, attributes["deviceid"]) || !accept(&client, attributes["client"]) {
					return "", false
				}
			}
		}
	}
	for name, values := range query {
		if (strings.EqualFold(name, "DeviceId") || strings.EqualFold(name, "X-Emby-Device-Id")) && !accept(&device, values) ||
			strings.EqualFold(name, "X-Emby-Client") && !accept(&client, values) {
			return "", false
		}
	}
	return device, true
}

func preloadCredentials(r *http.Request, native bool) (http.Header, string, bool) {
	headers, token, valid := effectiveCredentials(r)
	if !native {
		return headers, token, valid
	}
	var session string
	for _, cookie := range r.Cookies() {
		if cookie.Name == "Trim-MC-token" {
			if session != "" || cookie.Value == "" || strings.ContainsAny(cookie.Value, "\"\r\n\\") {
				return headers, "", false
			}
			session = cookie.Value
		}
	}
	if session == "" {
		return headers, token, valid
	}
	if !valid || token != "" && token != session || len(r.Header.Values("Authorization")) > 1 {
		return headers, "", false
	}
	authorization := r.Header.Get("Authorization")
	if authorization != "" && authorization != session && !strings.EqualFold(authorization, "Bearer "+session) {
		return headers, "", false
	}
	headers.Del("Authorization")
	headers.Set("X-Emby-Token", session)
	// 原生登录凭据已核对，再复用兼容 GET 的客户端描述规范化。
	return effectiveCredentials(&http.Request{Header: headers, URL: r.URL})
}

func nativePreloadSession(r *http.Request) bool {
	for _, cookie := range r.Cookies() {
		if cookie.Name == "Trim-MC-token" && cookie.Value != "" {
			return true
		}
	}
	return false
}

func (inst *instance) preloadScope(r *http.Request, native bool) (string, bool) {
	_, token, valid := preloadCredentials(r, native)
	if !valid || token == "" {
		return "", false
	}
	device, valid := preloadDeviceID(r)
	if !valid {
		return "", false
	}
	identity := "device"
	if native && device == "" && nativePreloadSession(r) {
		identity, device = "native-session", r.UserAgent()
	}
	if identity == "device" && device == "" {
		// shortcut: 同凭据且同 UA 的无设备客户端不能互相区分，不跨次学习播放 UA。
		if !validPreloadUA(r.UserAgent()) {
			return "", false
		}
		identity, device = "user-agent", r.UserAgent()
	}
	protocol := "compatible"
	if native {
		protocol = "native"
	}
	if !validPreloadUA(r.UserAgent()) && models.GetPlaybackSettingsSnapshot().LocalProxy != 1 {
		return "", false
	}
	return fingerprint([]string{inst.preloadID, protocol, token, identity, device}), true
}

func (inst *instance) capturePreloadBody(w http.ResponseWriter, r *http.Request) {
	if !inst.preloadEnabled() || r.Method != http.MethodPost || r.Body == nil ||
		r.ContentLength > metadataLimit || (r.URL.Path != "/v/api/v1/play/info" && !preloadProgressRoute(upstreamPath(r.URL.Path))) {
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
	body, err := io.ReadAll(io.LimitReader(r.Body, metadataLimit+1))
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil || len(body) > metadataLimit {
		r.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(body), r.Body), Closer: r.Body}
		return
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
}

func preloadRequestBody(r *http.Request, value any) bool {
	if r.GetBody == nil {
		return false
	}
	body, err := r.GetBody()
	if err != nil {
		return false
	}
	defer body.Close()
	decoder := json.NewDecoder(io.LimitReader(body, metadataLimit+1))
	return decoder.Decode(value) == nil && decoder.Decode(new(json.RawMessage)) == io.EOF
}

func (inst *instance) observePreloadResponse(response *http.Response) error {
	if !inst.preloadEnabled() {
		return nil
	}
	r := response.Request
	route := upstreamPath(r.URL.Path)
	if r.Method == http.MethodPost && preloadProgressRoute(route) {
		scope, ok := inst.preloadScope(r, false)
		sequence, present := r.Context().Value(preloadRequestSequence{}).(uint64)
		if !ok || !present {
			return nil
		}
		inst.preload.mu.Lock()
		defer inst.preload.mu.Unlock()
		// 进度按请求开始排序；旧成功或拒绝响应都不能改变更新的播放意图。
		device, found := inst.preload.devices[scope]
		if !found || device.progressSequence != sequence {
			return nil
		}
		var report preloadProgressReport
		valid := preloadRequestBody(r, &report) && safeIdentifier(report.ItemID)
		if route != "/Sessions/Playing" && (!valid || !report.matchesPlayback(device)) {
			return nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			inst.service.options.Preloader.CancelScope(scope)
			return nil
		}
		if valid {
			inst.observeCompatibleProgress(r, route, report)
		}
		return nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) && r.Method == http.MethodGet &&
			(preloadDetailRoute.MatchString(route) || strings.HasPrefix(route, "/v/api/v1/item/")) {
			scope, valid := inst.preloadScope(r, preloadNativePath(route))
			sequence, present := r.Context().Value(preloadRequestSequence{}).(uint64)
			inst.preload.mu.Lock()
			if device, found := inst.preload.devices[scope]; valid && present && found && currentPreloadDetail(r, device, sequence) && device.controlUA == r.UserAgent() {
				inst.revokePreloadDetail(scope)
			}
			inst.preload.mu.Unlock()
		}
		return nil
	}
	native := preloadNativePath(route)
	if !(r.Method == http.MethodGet && (preloadDetailRoute.MatchString(route) || preloadQueueRoute.MatchString(route) || strings.HasPrefix(route, "/v/api/v1/item/")) ||
		r.Method == http.MethodPost && route == "/v/api/v1/play/info") {
		return nil
	}
	scope, ok := inst.preloadScope(r, native)
	if !ok {
		return nil
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, metadataLimit+1))
	if err != nil || len(encoded) > metadataLimit {
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
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil
	}
	inst.preload.mu.Lock()
	defer inst.preload.mu.Unlock()
	if sequence, present := r.Context().Value(preloadRequestSequence{}).(uint64); present {
		device, found := inst.preload.devices[scope]
		current := currentPreloadDetail(r, device, sequence)
		if route == "/v/api/v1/play/info" {
			current = device.sourceSequence == sequence
		} else if preloadQueueRoute.MatchString(route) {
			current = device.queueSequence == sequence
		}
		if !found || !current || device.controlUA != r.UserAgent() {
			return nil
		}
	}
	if native {
		inst.observeNativePreload(r, scope, payload)
	} else {
		inst.observeCompatiblePreload(r, scope, route, payload)
	}
	return nil
}

// 调用方持有 preload.mu，且已核对最新详情的请求顺序与控制 UA。
func (inst *instance) revokePreloadDetail(scope string) {
	device, found := inst.preload.devices[scope]
	if !found {
		return
	}
	device.detail, device.source, device.info = preloadItem{}, preloadItem{}, nativePreloadInfo{}
	device.detailAt, device.sourceAt, device.infoAt = time.Time{}, time.Time{}, time.Time{}
	device.detailResponded = 0
	// 拒绝前开始的补源或列表不能恢复已撤销的详情关联。
	device.sourceSequence = max(device.sourceSequence, device.sequence, device.parentSequence)
	device.queueSequence = max(device.queueSequence, device.sequence, device.parentSequence)
	inst.preload.devices[scope] = device
	inst.service.options.Preloader.CancelDetail(scope)
}

func compatiblePreloadItem(payload map[string]any) (preloadItem, bool) {
	id, kind := stringField(payload, "Id"), stringField(payload, "Type")
	if !safeIdentifier(id) || kind != "Movie" && kind != "Episode" && kind != "Series" && kind != "Season" {
		return preloadItem{}, false
	}
	item := preloadItem{id: id, kind: kind, display: preloadDisplayFrom(payload, false), series: stringField(payload, "SeriesId"), season: stringField(payload, "SeasonId"), sources: itemSources(id, payload)}
	if user, ok := payload["UserData"].(map[string]any); ok && user["Played"] == false {
		position, valid := user["PlaybackPositionTicks"].(float64)
		item.resumable = valid && position > 0
	}
	if kind == "Season" {
		item.season = id
	}
	if kind == "Series" {
		item.series = id
	}
	media, _ := payload["MediaSources"].([]any)
	if len(item.sources) == 1 && len(media) == 1 {
		if source, ok := media[0].(map[string]any); ok {
			if ticks, ok := source["RunTimeTicks"].(float64); ok && ticks > 0 && ticks <= float64((24*time.Hour)/(100*time.Nanosecond)) {
				item.duration = time.Duration(ticks) * 100 * time.Nanosecond
			}
		}
	}
	return item, true
}

func (inst *instance) observeCompatiblePreload(r *http.Request, scope, route string, payload map[string]any) {
	generation := models.GetPlaybackSettingsSnapshot().Generation
	if match := preloadDetailRoute.FindStringSubmatch(route); len(match) == 3 {
		item, ok := compatiblePreloadItem(payload)
		if !ok || item.id != match[2] || !safeIdentifier(match[1]) {
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		selected, valid := queryValue(query, "MediaSourceId")
		if err != nil || !valid || !allowsDirectRequest(query) {
			return
		}
		item.selected = selected
		changed := false
		device, ok := inst.preload.update(scope, generation, func(device *preloadDevice) {
			previous := device.detail
			newerSource := device.source.id == item.id && device.sourceResponded > device.sequence
			if device.source.id == item.id && device.sourceResponded > device.detailResponded {
				previous = device.source
			}
			if previous.id == item.id {
				item.association = previous.association
				old, oldOK := selectPreloadSource(previous.sources, previous.selected)
				next, nextOK := selectPreloadSource(item.sources, item.selected)
				if !newerSource && oldOK && len(item.sources) > 0 && (!nextOK || old != next) {
					changed = true
					if nextOK {
						item.association = fingerprint(next)
					}
				}
				changed = changed || !newerSource && device.detail.selected != item.selected
			}
			if device.detail.id != item.id {
				changed = true
			}
			device.detailAt = time.Now()
			device.detail, device.userID = item, match[1]
			device.detailResponded = device.sequence
		})
		if changed {
			inst.service.options.Preloader.CancelDetail(scope)
		}
		if ok {
			inst.submitCompatibleDetail(r, scope, device, false)
		}
		return
	}
	match := preloadQueueRoute.FindStringSubmatch(route)
	if len(match) != 2 || !safeIdentifier(match[1]) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || !allowsDirectRequest(query) {
		return
	}
	userID, valid := queryValue(query, "UserId")
	if !valid || userID != "" && !safeIdentifier(userID) {
		return
	}
	seasonID, valid := queryValue(query, "SeasonId")
	if !valid || seasonID != "" && !safeIdentifier(seasonID) {
		return
	}
	if device, found := inst.preload.devices[scope]; found && device.userID != "" && userID != "" && userID != device.userID {
		return
	}
	for _, name := range []string{"StartItemId", "AdjacentTo"} {
		if value, valid := queryValue(query, name); !valid || value != "" {
			return
		}
	}
	if order, ok := queryValue(query, "SortOrder"); !ok || order != "" && !strings.EqualFold(order, "Ascending") {
		return
	}
	if sort, ok := queryValue(query, "SortBy"); !ok || sort != "" && !strings.EqualFold(sort, "SortName") && !strings.EqualFold(sort, "IndexNumber") {
		return
	}
	values, valid := payload["Items"].([]any)
	if !valid || len(values) > sourceCacheCapacity {
		return
	}
	queue := make([]preloadItem, 0, len(values))
	seen := make(map[string]bool, len(values))
	related := true
	for _, raw := range values {
		value, _ := raw.(map[string]any)
		item, ok := compatiblePreloadItem(value)
		if !ok || item.kind != "Episode" || !safeIdentifier(item.series) || !safeIdentifier(item.season) || seen[item.id] {
			return
		}
		related = related && item.series == match[1]
		seen[item.id] = true
		queue = append(queue, item)
	}
	if !related {
		queue = nil
	}
	var previousNext, previousResume preloadItem
	device, ok := inst.preload.update(scope, generation, func(device *preloadDevice) {
		previousNext, _ = compatiblePreloadNext(*device)
		previousResume, _ = compatiblePreloadResume(*device)
		if device.source.id == previousResume.id && time.Since(device.sourceAt) <= preloadAssociationTTL && device.sourceResponded >= device.queueResponded {
			previousResume = device.source
		}
		for index := range queue {
			if queue[index].id == previousNext.id && len(queue[index].sources) == 0 {
				queue[index].sources, queue[index].selected = previousNext.sources, previousNext.selected
			}
			if queue[index].id == previousResume.id {
				queue[index].association = previousResume.association
				if len(queue[index].sources) == 0 {
					queue[index].sources, queue[index].selected = previousResume.sources, previousResume.selected
				}
			}
		}
		device.queue, device.queueAt, device.queueUser = queue, time.Now(), userID
		device.queueResponded = device.queueSequence
		device.queueSeries = match[1]
	})
	if ok {
		if (device.detail.kind == "Series" || device.detail.kind == "Season" && (seasonID == "" || seasonID == device.detail.id)) && device.detail.series == match[1] {
			resume, valid := compatiblePreloadResume(device)
			if !valid || previousResume.id != "" && previousResume.id != resume.id {
				inst.service.options.Preloader.CancelDetail(scope)
			} else if previousResume.id == resume.id && (device.source.id != resume.id || device.sourceResponded <= device.queueResponded) {
				old, oldOK := selectPreloadSource(previousResume.sources, previousResume.selected)
				next, nextOK := selectPreloadSource(resume.sources, resume.selected)
				if oldOK && len(resume.sources) > 0 && (!nextOK || old != next) {
					inst.service.options.Preloader.CancelDetail(scope)
					if nextOK {
						for index := range device.queue {
							if device.queue[index].id == resume.id {
								device.queue[index].association = fingerprint(next)
							}
						}
						inst.preload.devices[scope] = device
					}
				}
			}
		}
		if device.playing.kind == "Episode" && device.playing.series == match[1] && (userID == "" || userID == device.userID) {
			if next, valid := compatiblePreloadNext(device); !valid {
				inst.service.options.Preloader.CancelNext(scope)
			} else {
				old, oldOK := selectPreloadSource(previousNext.sources, previousNext.selected)
				fresh, freshOK := selectPreloadSource(next.sources, next.selected)
				changed := previousNext.id != next.id || oldOK != freshOK || oldOK && old != fresh
				association := previousNext.association
				if changed {
					if previousNext.id != "" {
						inst.service.options.Preloader.CancelNext(scope)
					}
					// 恢复同一来源也要有新机会；同源列表刷新沿用旧关联，不能重置额度。
					association = fmt.Sprintf("queue:%d", device.queueResponded)
				}
				for index := range device.queue {
					if device.queue[index].id == next.id {
						device.queue[index].association = association
						break
					}
				}
				inst.preload.devices[scope] = device
			}
		}
		inst.submitCompatibleDetail(r, scope, device, false)
	}
}

func (inst *instance) rememberCompatiblePlayer(r *http.Request, source Source) {
	if !inst.preloadEnabled() {
		return
	}
	scope, ok := inst.preloadScope(r, false)
	if !ok {
		return
	}
	session, valid := queryValue(r.URL.Query(), "PlaySessionId")
	if !valid || session != "" && !safeIdentifier(session) {
		return
	}
	inst.preload.mu.Lock()
	learned := false
	defer func() {
		inst.preload.mu.Unlock()
		if learned && helpers.AppLogger != nil {
			helpers.AppLogger.Debugf("已更新预加载使用的播放 UA，入口=飞牛兼容，scope=%s", scope[:12])
		}
	}()
	if !inst.preloadEnabled() || !inst.currentPreloadPlayer(r, scope) {
		return
	}
	inst.preload.update(scope, models.GetPlaybackSettingsSnapshot().Generation, func(device *preloadDevice) {
		if device.playingID != "" && (device.playingID != source.ItemID || len(device.playing.sources) != 1 || device.playing.sources[0] != source ||
			session != "" && device.session != "" && session != device.session) {
			device.playing, device.session, device.playingID = preloadItem{}, "", ""
			inst.service.options.Preloader.CancelNext(scope)
		}
		if validPreloadUA(r.UserAgent()) {
			device.playerUA = r.UserAgent()
			device.playerSource, device.playerSession = source, session
			device.playerEpoch = 0
			if device.playingID == source.ItemID && len(device.playing.sources) == 1 && device.playing.sources[0] == source {
				device.playerEpoch = device.playingEpoch
			}
			if device.detail.id == source.ItemID && device.controlUA != "" && time.Since(device.detailAt) <= preloadAssociationTTL {
				selected, selectedOK := selectPreloadSource(device.detail.sources, device.detail.selected)
				if selectedOK && selected == source {
					learned = inst.preload.uas.Remember(inst.preloadUAKey(scope, *device), r.UserAgent(), inst.preload.generation)
				}
			}
		}
	})
}

// 只观察实际取流结果，原响应、长连接和 ResponseController 能力继续委托底层。
type preloadPlaybackWriter struct {
	http.ResponseWriter
	status int
	failed bool
}

func (w *preloadPlaybackWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *preloadPlaybackWriter) WriteHeader(status int) {
	if w.status == 0 && status >= http.StatusOK {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *preloadPlaybackWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(body)
	w.failed = w.failed || err != nil
	return n, err
}

func (w *preloadPlaybackWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *preloadPlaybackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *preloadPlaybackWriter) successful() bool {
	if w.failed {
		return false
	}
	if w.status == http.StatusOK || w.status == http.StatusPartialContent {
		return true
	}
	if w.status != http.StatusMovedPermanently && w.status != http.StatusFound && w.status != http.StatusSeeOther && w.status != http.StatusTemporaryRedirect && w.status != http.StatusPermanentRedirect {
		return false
	}
	raw := w.Header().Get("Location")
	target, err := url.Parse(raw)
	if err != nil || target.User != nil || target.Opaque != "" || target.Fragment != "" || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	if target.Host != "" {
		return target.Scheme == "http" || target.Scheme == "https"
	}
	query, err := url.ParseQuery(target.RawQuery)
	proxy := requests.Proxy115Request{URL: query.Get("url"), BaiduPan: query.Get("baidupan")}
	return err == nil && target.Scheme == "" && target.Path == "/proxy-115" && target.RawPath == "" && len(query["url"]) == 1 && len(query["baidupan"]) <= 1 && proxy.Validate() == nil
}

func validPreloadUA(value string) bool {
	return value != "" && len(value) <= 8192 && !strings.ContainsAny(value, "\r\n\x00")
}

func (inst *instance) observeCompatibleSource(r *http.Request, itemID string, sources []Source, payload map[string]any) {
	if !inst.preloadEnabled() {
		return
	}
	scope, ok := inst.preloadScope(r, false)
	if !ok {
		return
	}
	inst.preload.mu.Lock()
	defer inst.preload.mu.Unlock()
	if sequence, present := r.Context().Value(preloadRequestSequence{}).(uint64); present {
		device, found := inst.preload.devices[scope]
		detail, revision, valid := compatiblePreloadDetailItem(device)
		if !found || device.sourceSequence != sequence || valid && detail.id == itemID && sequence < revision && len(detail.sources) > 0 {
			return
		}
		if device.controlUA != r.UserAgent() {
			return
		}
	}
	selected, valid := queryValue(r.URL.Query(), "MediaSourceId")
	if !valid {
		return
	}
	item := preloadItem{id: itemID, selected: selected, display: preloadDisplayFrom(payload, false), sources: append([]Source(nil), sources...)}
	media, _ := payload["MediaSources"].([]any)
	if len(media) == 1 && len(sources) == 1 {
		if source, ok := media[0].(map[string]any); ok {
			if ticks, ok := source["RunTimeTicks"].(float64); ok && ticks > 0 && ticks <= float64((24*time.Hour)/(100*time.Nanosecond)) {
				item.duration = time.Duration(ticks) * 100 * time.Nanosecond
			}
		}
	}
	changed := false
	device, ok := inst.preload.update(scope, models.GetPlaybackSettingsSnapshot().Generation, func(device *preloadDevice) {
		previous := device.source
		detail, revision, current := compatiblePreloadDetailItem(*device)
		if current && detail.id == itemID && (device.source.id != itemID || revision > device.sourceResponded) {
			previous = detail
		}
		changed = fingerprint(previous.sources) != fingerprint(item.sources)
		item.association = previous.association
		old, oldOK := selectPreloadSource(previous.sources, previous.selected)
		next, nextOK := selectPreloadSource(item.sources, item.selected)
		if current && detail.id == itemID && device.sourceSequence >= revision && oldOK && len(item.sources) > 0 && (!nextOK || old != next) {
			if nextOK {
				item.association = fingerprint(next)
			}
			inst.service.options.Preloader.CancelDetail(scope)
		}
		if current && detail.id == itemID {
			item.kind, item.series, item.season = detail.kind, detail.series, detail.season
			if item.display.name == "" {
				item.display = detail.display
			}
		}
		device.source, device.sourceAt = item, time.Now()
		device.sourceResponded = device.sourceSequence
	})
	detail, _, current := compatiblePreloadDetailItem(device)
	if ok && current && detail.id == itemID {
		// 只给这次新增的自然响应资料一次补齐机会，后续相同详情不能继承此标记。
		device.source.enriched = changed
		inst.submitCompatibleDetail(r, scope, device, true)
	}
}

func (inst *instance) submitCompatibleDetail(r *http.Request, scope string, device preloadDevice, authorized bool) {
	item := device.detail
	if device.requested != item.id || device.detailResponded != device.sequence || time.Since(device.detailAt) > preloadAssociationTTL {
		return
	}
	item, revision, valid := compatiblePreloadDetailItem(device)
	if !valid {
		return
	}
	if device.source.id == item.id && time.Since(device.sourceAt) <= preloadAssociationTTL {
		newer := device.sourceResponded >= revision
		_, matches := selectPreloadSource(device.source.sources, item.selected)
		if newer || len(item.sources) == 0 && matches {
			authorized = true
			item.enriched, item.sources, item.association = device.source.enriched, device.source.sources, device.source.association
			if device.source.selected != "" && (newer || item.selected == "") {
				item.selected = device.source.selected
			}
		}
	}
	inst.submitPreload(r, scope, item, device.userID, inst.preloadDetailUA(scope, device), playback.PreloadDetail, authorized, "", false, preloadTrigger(device.detail.kind, false))
}

func compatiblePreloadDetailItem(device preloadDevice) (preloadItem, uint64, bool) {
	item, revision := device.detail, device.detailResponded
	if item.kind == "Series" || item.kind == "Season" {
		resume, valid := compatiblePreloadResume(device)
		return resume, device.queueResponded, valid
	}
	return item, revision, item.kind == "Movie" || item.kind == "Episode"
}

func compatiblePreloadResume(device preloadDevice) (preloadItem, bool) {
	item := device.detail
	if device.queueSeries != item.series || time.Since(device.queueAt) > preloadAssociationTTL || device.queueUser != "" && device.queueUser != device.userID {
		return preloadItem{}, false
	}
	var resume preloadItem
	for _, candidate := range device.queue {
		if !candidate.resumable || candidate.series != item.series || item.kind == "Season" && candidate.season != item.id {
			continue
		}
		if resume.id != "" {
			return preloadItem{}, false
		}
		resume = candidate
	}
	return resume, resume.id != ""
}

func (inst *instance) observeNativePreload(r *http.Request, scope string, payload map[string]any) {
	code, ok := payload["code"].(float64)
	data, valid := payload["data"].(map[string]any)
	if !ok || code != 0 || !valid {
		return
	}
	generation := models.GetPlaybackSettingsSnapshot().Generation
	if r.URL.Path == "/v/api/v1/play/info" {
		var request struct {
			ItemGUID string `json:"item_guid"`
		}
		item, _ := data["item"].(map[string]any)
		info := nativePreloadInfo{logical: stringField(data, "guid"), file: stringField(data, "media_guid"), video: stringField(data, "video_guid"), parent: stringField(data, "parent_guid"), kind: stringField(data, "type")}
		_, hasVideo := data["video_guid"]
		if !preloadRequestBody(r, &request) || !safeIdentifier(request.ItemGUID) || !safeIdentifier(info.logical) || !safeIdentifier(info.file) ||
			hasVideo && !safeIdentifier(info.video) || stringField(item, "guid") != info.logical || stringField(item, "type") != info.kind ||
			(info.kind != "Movie" && info.kind != "Episode") || item["can_play"] != float64(1) || stringField(item, "play_error") != "" {
			return
		}
		info.requested = request.ItemGUID
		info.display = preloadDisplayFrom(item, true)
		device, ok := inst.preload.update(scope, generation, func(device *preloadDevice) { device.info, device.infoAt = info, time.Now() })
		if ok {
			inst.submitNativeDetail(r, scope, device)
		}
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v/api/v1/item/")
	kind := stringField(data, "type")
	if !safeIdentifier(id) || stringField(data, "guid") != id {
		return
	}
	if data["can_play"] == float64(0) || stringField(data, "play_error") != "" {
		inst.revokePreloadDetail(scope)
		return
	}
	if _, parent := r.Context().Value(preloadParentSequence{}).(uint64); parent {
		return
	}
	if data["can_play"] != float64(1) || (kind != "Movie" && kind != "Episode" && kind != "TV" && kind != "Season") {
		return
	}
	changed := false
	device, ok := inst.preload.update(scope, generation, func(device *preloadDevice) {
		if device.detail.id != id {
			changed = true
		}
		device.detailAt = time.Now()
		device.detail = preloadItem{id: id, kind: kind, display: preloadDisplayFrom(data, true), season: stringField(data, "parent_guid")}
		device.detailResponded = device.sequence
	})
	if changed {
		inst.service.options.Preloader.CancelDetail(scope)
	}
	if ok {
		inst.submitNativeDetail(r, scope, device)
	}
}

func (inst *instance) submitNativeDetail(r *http.Request, scope string, device preloadDevice) {
	if device.info.requested != device.detail.id || device.requested != device.detail.id || device.detailResponded != device.sequence || time.Since(device.detailAt) > preloadAssociationTTL || time.Since(device.infoAt) > preloadAssociationTTL ||
		(device.detail.kind == "Movie" || device.detail.kind == "Episode") && device.info.logical != device.detail.id ||
		(device.detail.kind == "Movie" || device.detail.kind == "Episode") && device.info.kind != device.detail.kind ||
		(device.detail.kind == "TV" || device.detail.kind == "Season") && device.info.kind != "Episode" ||
		device.detail.kind == "Season" && device.info.parent != device.detail.id {
		return
	}
	item := preloadItem{id: device.info.file, kind: device.info.kind, display: device.info.display}
	if item.display.name == "" && device.detail.id == device.info.logical {
		item.display = device.detail.display
	}
	inst.submitPreload(r, scope, item, "", inst.preloadDetailUA(scope, device), playback.PreloadDetail, false, device.info.logical, true, preloadTrigger(device.detail.kind, false))
}

func (inst *instance) preloadUAKey(scope string, device preloadDevice) string {
	if device.weakIdentity {
		return ""
	}
	return fingerprint([]any{scope, device.userID, device.controlUA, models.GetPlaybackSettingsSnapshot().LocalProxy})
}

func (inst *instance) preloadDetailUA(scope string, device preloadDevice) string {
	if !device.weakIdentity {
		if ua := inst.preload.uas.Lookup(inst.preloadUAKey(scope, device), inst.preload.generation); ua != "" {
			return ua
		}
	}
	return device.controlUA
}

func (inst *instance) rememberNativePlayer(r *http.Request, playerUA, fileGUID string) {
	if !inst.preloadEnabled() {
		return
	}
	scope, ok := inst.preloadScope(r, true)
	if !ok {
		return
	}
	inst.preload.mu.Lock()
	learned := false
	defer func() {
		inst.preload.mu.Unlock()
		if learned && helpers.AppLogger != nil {
			helpers.AppLogger.Debugf("已更新预加载使用的播放 UA，入口=飞牛原生，scope=%s", scope[:12])
		}
	}()
	if !inst.preloadEnabled() || !inst.currentPreloadPlayer(r, scope) {
		return
	}
	inst.preload.update(scope, models.GetPlaybackSettingsSnapshot().Generation, func(device *preloadDevice) {
		device.playerUA = ""
		if validPreloadUA(playerUA) {
			device.playerUA = playerUA
			if device.controlUA != "" && device.info.file == fileGUID && fileGUID != "" && device.info.requested == device.detail.id && time.Since(device.infoAt) <= preloadAssociationTTL && time.Since(device.detailAt) <= preloadAssociationTTL {
				learned = inst.preload.uas.Remember(inst.preloadUAKey(scope, *device), playerUA, inst.preload.generation)
			}
		}
	})
}

// 调用方持有 preload.mu；真实播放响应不得恢复更新进度或详情已撤销的关联。
func (inst *instance) currentPreloadPlayer(r *http.Request, scope string) bool {
	sequence, present := r.Context().Value(preloadRequestSequence{}).(uint64)
	if !present {
		return true
	}
	device, found := inst.preload.devices[scope]
	return found && device.playerSequence == sequence && device.progressSequence == sequence && sequence >= device.sequence && sequence >= device.controlSequence
}

func selectPreloadSource(sources []Source, selected string) (Source, bool) {
	if selected == "" {
		if len(sources) != 1 {
			return Source{}, false
		}
		return sources[0], true
	}
	for _, source := range sources {
		if source.ID == selected {
			return source, true
		}
	}
	return Source{}, false
}

func (inst *instance) submitPreload(r *http.Request, scope string, item preloadItem, userID, playerUA string, kind playback.PreloadKind, authorized bool, logicalID string, native bool, trigger string) {
	if !inst.preloadEnabled() || playerUA == "" && models.GetPlaybackSettingsSnapshot().LocalProxy != 1 {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	selected, valid := queryValue(query, "MediaSourceId")
	if err != nil || !valid || !allowsDirectRequest(query) {
		return
	}
	if !native && kind == playback.PreloadDetail {
		selected = item.selected
	}
	if logicalID != "" {
		selected = logicalID
	} else if kind == playback.PreloadNext {
		selected = ""
	}
	if len(item.sources) > 0 {
		if _, ok := selectPreloadSource(item.sources, selected); !ok {
			return
		}
	}
	headers, token, valid := preloadCredentials(r, native)
	if !valid || token == "" {
		return
	}
	// 后台只保留不可变凭据副本；不保留原请求、响应或原生签名。
	headers = playbackMetadataHeaders(headers)
	key := fingerprint([]any{scope, item.id, item.kind, selected, playerUA, kind, logicalID, item.association})
	var run func(context.Context, *playback.PreloadTask) playback.PreloadResult
	inst.mu.Lock()
	defer inst.mu.Unlock()
	if inst.stopped || inst.ctx.Err() != nil {
		return
	}
	inst.service.options.Preloader.Submit(playback.PreloadCandidate{Key: key, Scope: scope, Instance: inst.preloadID, Kind: kind, Enriched: item.enriched,
		Log: playback.PreloadLogContext{Entry: "飞牛", Trigger: trigger, ItemID: item.id, Title: item.display.name},
		Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			ctx, cancel := context.WithCancel(ctx)
			stop := context.AfterFunc(inst.ctx, cancel)
			defer stop()
			defer cancel()
			if !task.Valid() || ctx.Err() != nil || inst.ctx.Err() != nil {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "canceled"}
			}
			release, admitted := backup.AcquireRuntimeRequest()
			if !admitted {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "runtime_maintenance"}
			}
			defer release()
			task.SetLogTarget(item.id, item.display.title())
			if run == nil {
				if !authorized || userID == "" {
					user, status, err := inst.preloadMetadata(ctx, task, headers, token, "/Users/Me")
					if err != nil {
						return preloadMetadataFailure(err, status)
					}
					if !allowsDirectPlayback(user, "info") || !safeIdentifier(stringField(user, "Id")) || userID != "" && stringField(user, "Id") != userID {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "playback_denied"}
					}
					userID = stringField(user, "Id")
				}
				if len(item.sources) == 0 {
					value, status, err := inst.preloadMetadata(ctx, task, headers, token, "/Users/"+userID+"/Items/"+item.id)
					if err != nil {
						return preloadMetadataFailure(err, status)
					}
					if stringField(value, "Id") != item.id {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "source_changed"}
					}
					item.sources = itemSources(item.id, value)
					if display := preloadDisplayFrom(value, false); display.name != "" {
						item.display = display
					}
				}
				task.SetLogTarget(item.id, item.display.title())
				source, ok := selectPreloadSource(item.sources, selected)
				if !ok {
					reason := "source_ambiguous"
					if len(item.sources) == 0 {
						reason = "source_unavailable"
					}
					return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: reason}
				}
				request := &http.Request{Header: headers.Clone(), URL: &url.URL{RawQuery: url.Values{"api_key": {token}}.Encode()}}
				request = request.WithContext(ctx)
				resolved, ok := inst.resolveSource(ctx, request, source)
				if !ok {
					return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "source_unavailable"}
				}
				target, ok := inst.internalURL(ctx, resolved.url)
				if !ok {
					return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "source_origin_unknown"}
				}
				_, run, ok = inst.service.options.NewPreload(target.String(), playerUA, false)
				if !ok || run == nil {
					return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "provider_not_supported"}
				}
			}
			return run(ctx, task)
		}})
}

func playbackMetadataHeaders(original http.Header) http.Header {
	headers := upstreamHeaders(original)
	for _, name := range []string{"Authx", "Range", "If-Range", "Content-Length", "Content-Type", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
		headers.Del(name)
	}
	headers.Set("Accept", "application/json")
	headers.Set("Accept-Encoding", "identity")
	return headers
}

func (inst *instance) preloadMetadata(ctx context.Context, task *playback.PreloadTask, headers http.Header, token, endpoint string) (map[string]any, int, error) {
	var payload map[string]any
	status := 0
	err := task.Metadata(ctx, true, func(ctx context.Context) (bool, error) {
		status = 0
		target := *inst.upstream
		target.Path, target.RawPath = endpoint, ""
		target.RawQuery = url.Values{"api_key": {token}, "Fields": {"Path,MediaSources,SeriesId,SeasonId"}}.Encode()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return false, errors.New("元数据请求无效")
		}
		request.Header = headers.Clone()
		client := &http.Client{Transport: inst.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		response, err := client.Do(request)
		if err != nil {
			return ctx.Err() == nil, errors.New("元数据连接失败")
		}
		defer response.Body.Close()
		status = response.StatusCode
		if response.StatusCode != http.StatusOK {
			return response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504, errors.New("元数据状态不符")
		}
		encoded, err := io.ReadAll(io.LimitReader(response.Body, metadataLimit+1))
		if err != nil || len(encoded) > metadataLimit {
			return false, errors.New("元数据响应无法读取")
		}
		decoded, ok := decodeMetadata(encoded, response.Header.Get("Content-Encoding"))
		if !ok || json.Unmarshal(decoded, &payload) != nil || payload == nil {
			return false, errors.New("元数据格式不符")
		}
		return false, nil
	})
	return payload, status, err
}

func preloadMetadataFailure(err error, status int) playback.PreloadResult {
	result := playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "metadata_request_failed", StatusSource: "飞牛", HTTPStatus: status}
	switch {
	case errors.Is(err, playback.ErrPreloadMetadataBudget):
		result.Reason, result.HTTPStatus = "metadata_budget", 0
	case errors.Is(err, playback.ErrPreloadCanceled), errors.Is(err, context.Canceled):
		result.Reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		result.Reason = "timeout"
	}
	return result
}

func preloadTrigger(kind string, next bool) string {
	if next {
		return "播放接近结束"
	}
	switch kind {
	case "Movie":
		return "电影详情"
	case "Episode":
		return "单集详情"
	case "Series", "TV":
		return "剧集页待播集"
	case "Season":
		return "季页待播集"
	}
	return ""
}

type preloadDisplay struct {
	name, series    string
	season, episode float64
	numbered        bool
}

func preloadDisplayFrom(payload map[string]any, native bool) preloadDisplay {
	value := preloadDisplay{name: stringField(payload, "Name"), series: stringField(payload, "SeriesName")}
	seasonKey, episodeKey := "ParentIndexNumber", "IndexNumber"
	if native {
		value.name, value.series = stringField(payload, "title"), stringField(payload, "series_title")
		if value.name == "" {
			value.name = stringField(payload, "name")
		}
		seasonKey, episodeKey = "season_number", "episode_number"
	}
	var hasSeason, hasEpisode bool
	value.season, hasSeason = payload[seasonKey].(float64)
	value.episode, hasEpisode = payload[episodeKey].(float64)
	value.numbered = hasSeason && hasEpisode && value.season >= 0 && value.season <= 9999 && value.episode >= 0 && value.episode <= 9999 && value.season == float64(int(value.season)) && value.episode == float64(int(value.episode))
	return value
}

func (value preloadDisplay) title() string {
	title := value.name
	if value.numbered {
		if value.series != "" {
			title = value.series
		}
		if title != "" {
			return fmt.Sprintf("%s S%02dE%02d", title, int(value.season), int(value.episode))
		}
	}
	return title
}

type preloadProgressReport struct {
	ItemID        string `json:"ItemId"`
	MediaSourceID string `json:"MediaSourceId"`
	PlaySessionID string `json:"PlaySessionId"`
	PositionTicks int64  `json:"PositionTicks"`
	RunTimeTicks  int64  `json:"RunTimeTicks"`
	IsPaused      *bool  `json:"IsPaused"`
	PlayMethod    string `json:"PlayMethod"`
}

func (report preloadProgressReport) matchesPlayback(device preloadDevice) bool {
	itemID, session, source := device.playingID, device.session, Source{}
	if len(device.playing.sources) == 1 {
		source = device.playing.sources[0]
	}
	if itemID == "" && device.playerUA != "" {
		// 成功取流可先于开始报告；只使用该次实际源，不借用详情或历史映射。
		itemID, session, source = device.playerSource.ItemID, device.playerSession, device.playerSource
	}
	if report.ItemID != itemID || report.MediaSourceID != "" && source.ID != "" && report.MediaSourceID != source.ID ||
		report.PlaySessionID != "" && session != "" && report.PlaySessionID != session {
		return false
	}
	if report.PlaySessionID != "" && device.playerUA != "" && device.playerSource == source && device.playerSession != "" && report.PlaySessionID != device.playerSession {
		return false
	}
	return true
}

func (inst *instance) observeCompatibleProgress(r *http.Request, route string, report preloadProgressReport) {
	scope, ok := inst.preloadScope(r, false)
	if !ok {
		return
	}
	generation := models.GetPlaybackSettingsSnapshot().Generation
	if route == "/Sessions/Playing/Stopped" {
		inst.service.options.Preloader.CancelScope(scope)
		inst.preload.update(scope, generation, func(device *preloadDevice) {
			device.playing, device.session, device.playingID = preloadItem{}, "", ""
			device.playerUA, device.playerSession, device.playerSource = "", "", Source{}
			device.playingEpoch, device.playerEpoch = 0, 0
		})
		return
	}
	if route == "/Sessions/Playing" {
		inst.service.options.Preloader.TakeoverScope(scope)
		if report.IsPaused != nil && *report.IsPaused {
			inst.service.options.Preloader.CancelNext(scope)
			return
		}
		inst.preload.update(scope, generation, func(device *preloadDevice) {
			previous := device.playing
			repeated := report.PlaySessionID != "" && report.PlaySessionID == device.session && len(previous.sources) == 1 && report.matchesPlayback(*device)
			confirmed := device.playerUA != "" && device.playingEpoch != 0 && device.playerEpoch == device.playingEpoch
			device.playing = preloadItem{}
			if strings.EqualFold(report.PlayMethod, "Transcode") {
				inst.service.options.Preloader.CancelNext(scope)
				device.session, device.playingID = "", ""
				device.playerUA, device.playerSession, device.playerSource = "", "", Source{}
				return
			}
			candidates := []preloadItem{device.detail, device.source}
			if device.sourceResponded >= device.detailResponded {
				candidates[0], candidates[1] = candidates[1], candidates[0]
			}
			for _, item := range append(candidates, device.queue...) {
				if item.id != report.ItemID || item.kind != "Movie" && item.kind != "Episode" {
					continue
				}
				source, ok := selectPreloadSource(item.sources, report.MediaSourceID)
				if !ok && len(item.sources) != 0 {
					// 最新资料已明确来源但无法选定时，不借用旧列表里的单源。
					break
				}
				if ok {
					device.playing = item
					device.playing.sources = []Source{source}
					if repeated && previous.sources[0] == source {
						return
					}
					inst.service.options.Preloader.TakeoverNext(scope)
					if device.playerSource != source || !confirmed {
						device.playerUA = ""
						inst.service.options.Preloader.CancelNext(scope)
					} else if report.PlaySessionID != "" {
						device.playerSession = report.PlaySessionID
					}
					break
				}
			}
			if len(device.playing.sources) != 1 {
				inst.service.options.Preloader.CancelNext(scope)
				device.playerUA = ""
			}
			device.session, device.playingID = report.PlaySessionID, report.ItemID
			device.playerEpoch = 0
			if device.playerUA != "" {
				device.playerEpoch = device.playingEpoch
			}
		})
		return
	}
	if route != "/Sessions/Playing/Progress" {
		return
	}
	device, ok := inst.preload.update(scope, generation, func(*preloadDevice) {})
	confirmed := device.playingEpoch != 0 && device.playerEpoch == device.playingEpoch && device.playerUA != "" && len(device.playing.sources) == 1 && device.playerSource == device.playing.sources[0]
	session := device.session
	if session == "" && confirmed {
		session = device.playerSession
	}
	sessionMatches := report.PlaySessionID != "" && report.PlaySessionID == session || report.PlaySessionID == "" && confirmed
	if !ok || device.playingID != report.ItemID || !sessionMatches || len(device.playing.sources) != 1 ||
		device.queueUser != "" && device.userID != "" && device.queueUser != device.userID ||
		report.MediaSourceID != "" && device.playing.sources[0].ID != report.MediaSourceID {
		inst.service.options.Preloader.CancelNext(scope)
		return
	}
	if device.playing.kind != "Episode" {
		inst.service.options.Preloader.CancelNext(scope)
		return
	}
	if report.IsPaused == nil || *report.IsPaused || strings.EqualFold(report.PlayMethod, "Transcode") || report.PositionTicks <= 0 || report.PositionTicks > int64((24*time.Hour)/(100*time.Nanosecond)) {
		inst.service.options.Preloader.CancelNext(scope)
		return
	}
	duration := device.playing.duration
	if duration == 0 && report.RunTimeTicks > 0 && report.RunTimeTicks <= int64((24*time.Hour)/(100*time.Nanosecond)) {
		duration = time.Duration(report.RunTimeTicks) * 100 * time.Nanosecond
	}
	if !playback.ShouldPreloadNext(duration, time.Duration(report.PositionTicks)*100*time.Nanosecond) {
		inst.service.options.Preloader.CancelNext(scope)
		return
	}
	next, valid := compatiblePreloadNext(device)
	if !valid {
		inst.service.options.Preloader.CancelNext(scope)
		return
	}
	next.association = fingerprint([]any{device.playingID, device.session, device.playing.sources[0].ID, device.playingEpoch, next.sources, next.selected, next.association})
	userID := device.userID
	if device.queueUser != "" {
		userID = device.queueUser
	}
	inst.submitPreload(r, scope, next, userID, device.playerUA, playback.PreloadNext, false, "", false, preloadTrigger(next.kind, true))
}

func compatiblePreloadNext(device preloadDevice) (preloadItem, bool) {
	if device.queueSeries != device.playing.series {
		return preloadItem{}, false
	}
	for index, item := range device.queue {
		if item.id != device.playingID {
			continue
		}
		if index+1 == len(device.queue) || item.kind != "Episode" || item.series != device.playing.series || item.season != device.playing.season {
			return preloadItem{}, false
		}
		next := device.queue[index+1]
		return next, next.kind == "Episode" && next.series == item.series && next.season == item.season
	}
	return preloadItem{}, false
}
