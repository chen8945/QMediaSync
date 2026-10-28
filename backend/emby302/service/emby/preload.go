package emby

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"qmediasync/emby302/config"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"

	"github.com/gin-gonic/gin"
)

const (
	preloadBodyLimit      = 2 << 20
	preloadDeviceLimit    = 256
	preloadEvidenceTTL    = 15 * time.Minute
	preloadAssociationTTL = 60 * time.Second
)

// PreloadOptions 由应用组合层注入可信源解析，不从公网参数开启后台用途。
type PreloadOptions struct {
	Instance      string
	TrustedOrigin func(*url.URL) bool
	Resolve       func(string, string, bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool)
	Account       func(string) playback.PreloadAccount
	Coordinator   *playback.Preloader
	Enabled       func() (bool, uint64)
}

type embyPreloader struct {
	options PreloadOptions
	mu      sync.Mutex
	devices map[string]*preloadDevice
	stopped atomic.Bool
	serial  atomic.Uint64
	uas     playback.PreloadUAMappings
}

type preloadDevice struct {
	generation      uint64
	seen            time.Time
	user            string
	controlUA       string
	controlStarted  uint64
	client          string
	playerUA        string
	detail          preloadItem
	detailAt        time.Time
	selected        string
	revision        uint64
	pendingID       string
	pending         preloadPlaybackInfo
	pendingAt       time.Time
	pendingSelected string
	pendingSequence uint64
	playing         preloadPlaying
	season          string
	members         map[string]bool
	membersAt       time.Time
	piDetail        string
	piNext          string
	detailStarted   uint64
	detailResponded uint64
	detailRequested string
	playbackStarted uint64
	progressStarted uint64
	episodesStarted uint64
}

type preloadRequest struct {
	scope          string
	weakIdentity   bool
	token          string
	user           string
	ua             string
	client         string
	prefix         string
	origin         string
	item           string
	selected       string
	session        string
	generation     uint64
	fromPlayback   bool
	fromEpisodes   bool
	enriched       bool
	sequence       uint64
	detailSequence uint64
}

type preloadItem struct {
	ID                string `json:"Id"`
	Name              any
	SeriesName        any
	IndexNumber       any
	ParentIndexNumber any
	Type              string
	SeriesID          string `json:"SeriesId"`
	SeasonID          string `json:"SeasonId"`
	ParentID          string `json:"ParentId"`
	PartCount         int
	RunTimeTicks      int64
	MediaSources      []preloadSource
	IsFolder          bool
	PlayAccess        string
	IsLive            bool
	IsInfiniteStream  bool
}

type preloadSource struct {
	ID                   string `json:"Id"`
	ItemID               string `json:"ItemId"`
	Path                 string
	Container            string
	RunTimeTicks         int64
	IsInfiniteStream     bool
	SupportsDirectPlay   *bool
	SupportsDirectStream *bool
	MediaStreams         []preloadStream
}

type preloadStream struct {
	Type  string
	Codec string
}

type preloadPlaybackInfo struct {
	MediaSources  []preloadSource
	PlaySessionID string `json:"PlaySessionId"`
}

type preloadPlaying struct {
	Item          preloadItem
	Source        preloadSource
	Session       string
	Active        bool
	Paused        bool
	Epoch         uint64
	NextID        string
	PositionTicks int64
	RunTimeTicks  int64
	UA            string
	UASession     string
}

type preloadProgress struct {
	ItemID          string `json:"ItemId"`
	MediaSourceID   string `json:"MediaSourceId"`
	PlaySessionID   string `json:"PlaySessionId"`
	PositionTicks   int64
	RunTimeTicks    int64
	IsPaused        *bool
	PlayMethod      string
	NowPlayingQueue []struct {
		ID string `json:"Id"`
	}
}

var activeEmbyPreloader atomic.Pointer[embyPreloader]

// ConfigurePreloading 替换实例时同步撤销旧候选；返回值只停止这次配置。
func ConfigurePreloading(options PreloadOptions) func() {
	if options.Coordinator == nil {
		options.Coordinator = playback.DefaultPreloader
	}
	if options.Enabled == nil {
		options.Enabled = func() (bool, uint64) {
			settings := models.GetPlaybackSettingsSnapshot()
			return settings.PreloadEnabled, settings.Generation
		}
	}
	next := &embyPreloader{options: options, devices: make(map[string]*preloadDevice)}
	if previous := activeEmbyPreloader.Swap(next); previous != nil {
		previous.stop()
	}
	return func() {
		activeEmbyPreloader.CompareAndSwap(next, nil)
		next.stop()
	}
}

func (p *embyPreloader) stop() {
	if !p.stopped.Swap(true) {
		p.options.Coordinator.CancelInstance(p.options.Instance)
		p.mu.Lock()
		p.devices = make(map[string]*preloadDevice)
		p.mu.Unlock()
		p.uas.Clear()
	}
}

// PreloadObserver 只观察原请求的成功结果，捕获有界副本并原样透传响应。
func PreloadObserver() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := activeEmbyPreloader.Load()
		if p == nil || p.stopped.Load() || p.options.Resolve == nil || p.options.TrustedOrigin == nil {
			return
		}
		enabled, generation := p.options.Enabled()
		if !enabled || !p.options.Coordinator.Enabled() {
			return
		}
		kind, item, user, prefix := preloadRoute(c.Request.Method, c.Request.URL.Path)
		if kind == "" {
			return
		}
		request, ok := newPreloadRequest(c.Request, p.options.Instance, user, prefix, generation)
		if !ok {
			return
		}
		request.item = item
		if kind == "stream" {
			query, err := url.ParseQuery(c.Request.URL.RawQuery)
			session, valid := preloadUnique(preloadValues(query, "PlaySessionId"))
			if err != nil || !valid || strings.ContainsAny(session, "\r\n\x00") {
				return
			}
			request.session = session
		}
		if kind == "stop" || kind == "progress" {
			request.sequence = p.serial.Add(1)
		}
		if !p.beginObservation(&request, kind, nil) && kind != "stop" && kind != "progress" {
			return
		}
		var body *preloadRequestBody
		var reported *atomic.Pointer[preloadProgress]
		if c.Request.Body != nil && (kind == "progress" || kind == "start" || kind == "stop" || kind == "playback") {
			body = &preloadRequestBody{ReadCloser: c.Request.Body}
			if kind == "stop" || kind == "progress" {
				reported = new(atomic.Pointer[preloadProgress])
				reportRequest := request
				body.onComplete = func(data []byte) {
					var progress preloadProgress
					if json.Unmarshal(data, &progress) == nil {
						p.beginObservation(&reportRequest, kind, &progress)
						reported.Store(&progress)
					}
				}
			}
			c.Request.Body = body
		}
		writer := &preloadResponseWriter{ResponseWriter: c.Writer, capture: kind == "detail" || kind == "playback" || kind == "episodes"}
		c.Writer = writer
		c.Next()
		if !p.valid(request) || writer.failed {
			return
		}
		var progress preloadProgress
		if reported != nil {
			if report := reported.Load(); report != nil {
				progress = *report
			}
		} else if body != nil && body.complete && !body.overflow && !body.failed {
			if json.Unmarshal(body.data.Bytes(), &progress) != nil {
				progress = preloadProgress{}
			}
		}
		if request.selected == "" {
			request.selected = progress.MediaSourceID
		}
		if kind == "detail" && (c.Writer.Status() == http.StatusUnauthorized || c.Writer.Status() == http.StatusForbidden) {
			p.invalidateDetail(request, true)
		}
		if (kind == "start" || kind == "progress" || kind == "stop") && (c.Writer.Status() == http.StatusUnauthorized || c.Writer.Status() == http.StatusForbidden) {
			p.mu.Lock()
			device := p.devices[request.scope]
			if device != nil && device.progressStarted == request.sequence && (kind != "stop" || progress.matchesPlayback(device.playing)) {
				device.playing.Active = false
				device.playing.Epoch++
				p.options.Coordinator.CancelScope(request.scope)
			}
			p.mu.Unlock()
		}
		if c.Writer.Status() < 200 || c.Writer.Status() >= 400 {
			return
		}
		switch kind {
		case "detail", "playback", "episodes":
			if c.Writer.Status() != http.StatusOK || writer.overflow {
				return
			}
			data, err := decodePreloadJSON(writer.data.Bytes(), c.Writer.Header())
			if err != nil {
				return
			}
			switch kind {
			case "detail":
				var value preloadItem
				if json.Unmarshal(data, &value) == nil && value.ID == item {
					if validPreloadItem(value) {
						p.observeDetail(request, value)
					} else {
						p.invalidateDetail(request, strings.EqualFold(value.PlayAccess, "None") || value.IsLive || value.IsInfiniteStream)
					}
				}
			case "playback":
				var value preloadPlaybackInfo
				if json.Unmarshal(data, &value) == nil && validPreloadSources(value.MediaSources, item) {
					p.observePlayback(request, value)
				}
			case "episodes":
				p.observeSeasonMembers(request, c.Request.URL.Query(), data)
			}
		case "stream":
			status := c.Writer.Status()
			location, err := url.Parse(c.Writer.Header().Get("Location"))
			redirect := status == http.StatusTemporaryRedirect && err == nil && location.User == nil && location.Fragment == "" && location.Host != "" && (location.Scheme == "http" || location.Scheme == "https") && !p.trustedSource(location.String())
			if status == http.StatusOK || status == http.StatusPartialContent || redirect {
				p.observeStream(request)
			}
		case "start", "progress", "stop":
			if c.Writer.Status() < 300 && progress.ItemID != "" {
				p.observeProgress(request, progress, kind)
			}
		}
	}
}

func (p *embyPreloader) valid(request preloadRequest) bool {
	enabled, generation := p.options.Enabled()
	return !p.stopped.Load() && enabled && generation == request.generation
}

var preloadIDPattern = regexp.MustCompile(`^(?:[0-9]+|[0-9a-fA-F]{32}|[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})$`)

func validPreloadID(value string) bool {
	return len(value) <= 64 && preloadIDPattern.MatchString(value)
}

func preloadRoute(method, path string) (kind, item, user, prefix string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 0 && strings.EqualFold(parts[0], "emby") {
		prefix, parts = "/emby", parts[1:]
	}
	if method == http.MethodGet {
		if len(parts) == 4 && strings.EqualFold(parts[0], "Users") && strings.EqualFold(parts[2], "Items") && validPreloadID(parts[1]) && validPreloadID(parts[3]) {
			return "detail", parts[3], parts[1], prefix
		}
		if len(parts) == 2 && strings.EqualFold(parts[0], "Items") && validPreloadID(parts[1]) {
			return "detail", parts[1], "", prefix
		}
		if len(parts) == 3 && strings.EqualFold(parts[0], "Shows") && strings.EqualFold(parts[2], "Episodes") && validPreloadID(parts[1]) {
			return "episodes", parts[1], "", prefix
		}
		if len(parts) == 3 && strings.EqualFold(parts[0], "Videos") && validPreloadID(parts[1]) && (strings.EqualFold(parts[2], "stream") || strings.EqualFold(parts[2], "stream.mkv") || strings.EqualFold(parts[2], "stream.mp4") || strings.EqualFold(parts[2], "universal")) {
			return "stream", parts[1], "", prefix
		}
	}
	if (method == http.MethodGet || method == http.MethodPost) && len(parts) == 3 && strings.EqualFold(parts[0], "Items") && strings.EqualFold(parts[2], "PlaybackInfo") && validPreloadID(parts[1]) {
		return "playback", parts[1], "", prefix
	}
	if method == http.MethodPost && len(parts) >= 2 && len(parts) <= 3 && strings.EqualFold(parts[0], "Sessions") && strings.EqualFold(parts[1], "Playing") {
		if len(parts) == 2 {
			return "start", "", "", prefix
		}
		if strings.EqualFold(parts[2], "Progress") || strings.EqualFold(parts[2], "Stopped") {
			if strings.EqualFold(parts[2], "Stopped") {
				return "stop", "", "", prefix
			}
			return "progress", "", "", prefix
		}
	}
	return "", "", "", ""
}

func preloadUnique(values []string) (string, bool) {
	var result string
	for _, value := range values {
		if value == "" || len(value) > 4096 || (result != "" && value != result) {
			return "", false
		}
		result = value
	}
	return result, true
}

func preloadValues(query url.Values, names ...string) []string {
	var result []string
	for key, values := range query {
		for _, name := range names {
			if strings.EqualFold(key, name) {
				result = append(result, values...)
			}
		}
	}
	return result
}

func newPreloadRequest(request *http.Request, instance, pathUser, prefix string, generation uint64) (preloadRequest, bool) {
	if config.C == nil || config.C.Emby == nil || config.C.Emby.Host == "" {
		return preloadRequest{}, false
	}
	q, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return preloadRequest{}, false
	}
	tokens := preloadValues(q, "api_key", "X-Emby-Token")
	devices := preloadValues(q, "DeviceId", "X-Emby-Device-Id")
	clients := preloadValues(q, "X-Emby-Client")
	users := preloadValues(q, "UserId")
	if pathUser != "" {
		users = append(users, pathUser)
	}
	for _, header := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		tokens = append(tokens, request.Header.Values(header)...)
	}
	devices = append(devices, request.Header.Values("X-Emby-Device-Id")...)
	clients = append(clients, request.Header.Values("X-Emby-Client")...)
	for _, header := range []string{HeaderAuthName, HeaderFullAuthName} {
		for _, value := range request.Header.Values(header) {
			attributes, valid := playback.EmbyAuthAttributes(value)
			if !valid {
				return preloadRequest{}, false
			}
			tokens = append(tokens, attributes["token"]...)
			devices = append(devices, attributes["deviceid"]...)
			clients = append(clients, attributes["client"]...)
		}
	}
	token, tokenOK := preloadUnique(tokens)
	device, deviceOK := preloadUnique(devices)
	client, clientOK := preloadUnique(clients)
	user, userOK := preloadUnique(users)
	selected, selectedOK := preloadUnique(preloadValues(q, "MediaSourceId"))
	ua := request.UserAgent()
	if !tokenOK || token == "" || !deviceOK || len(device) > 256 || !clientOK || len(client) > 256 || !userOK || (user != "" && !validPreloadID(user)) || !selectedOK || len(selected) > 256 || len(ua) > 2048 || device == "" && ua == "" {
		return preloadRequest{}, false
	}
	for _, value := range []string{token, device, client, ua} {
		if strings.IndexFunc(value, func(c rune) bool { return c < 0x20 || c == 0x7f }) >= 0 {
			return preloadRequest{}, false
		}
	}
	scope := preloadHash(instance, config.C.Emby.Host, token, device)
	if device == "" {
		// shortcut: 无设备标识时只按本次完整 UA 隔离，不跨次学习播放器 UA。
		scope = preloadHash(instance, config.C.Emby.Host, token, "missing-device", ua)
	}
	return preloadRequest{
		scope: scope, weakIdentity: device == "", token: token, user: user,
		ua: ua, client: client, prefix: prefix, origin: config.C.Emby.Host, selected: selected, generation: generation,
	}, true
}

func preloadHash(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		_, _ = io.WriteString(hash, strconv.Itoa(len(value))+":"+value)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func (p *embyPreloader) deviceLocked(request preloadRequest) *preloadDevice {
	if !p.valid(request) {
		return nil
	}
	now := time.Now()
	for key, value := range p.devices {
		if now.Sub(value.seen) > preloadEvidenceTTL || value.generation != request.generation {
			delete(p.devices, key)
		}
	}
	device := p.devices[request.scope]
	if device == nil {
		if len(p.devices) >= preloadDeviceLimit {
			return nil
		}
		device = &preloadDevice{generation: request.generation}
		p.devices[request.scope] = device
	}
	if request.user != "" && device.user != "" && device.user != request.user {
		return nil
	}
	if request.user != "" {
		device.user = request.user
	}
	device.seen = now
	return device
}

func (p *embyPreloader) beginObservation(request *preloadRequest, kind string, report *preloadProgress) bool {
	if request.sequence == 0 {
		request.sequence = p.serial.Add(1)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	device := p.deviceLocked(*request)
	if device == nil {
		return false
	}
	if !request.weakIdentity && request.ua == device.controlUA && device.user != "" {
		p.uas.Lookup(preloadUAKey(*request, device), request.generation)
	}
	if kind == "detail" || kind == "playback" || kind == "episodes" {
		if request.sequence < device.controlStarted {
			return false
		}
		controlChanged := device.controlUA != "" && device.controlUA != request.ua || (device.client != "" && request.client != "" && device.client != request.client)
		if controlChanged {
			device.playerUA = ""
			device.pending, device.pendingID, device.pendingSelected = preloadPlaybackInfo{}, "", ""
			device.members, device.season = nil, ""
			device.detail.MediaSources, device.selected = nil, ""
			device.playing.Active = false
			device.playing.Epoch++
			p.options.Coordinator.CancelDetail(request.scope)
			p.options.Coordinator.CancelNext(request.scope)
		}
		device.controlUA, device.controlStarted = request.ua, request.sequence
		if request.client != "" {
			device.client = request.client
		}
	}
	if kind == "detail" {
		if request.sequence < device.detailStarted {
			return false
		}
		selected, known := selectPreloadSource(device.detail, device.selected)
		selectionChanged := request.selected != "" && (!known || request.selected != selected.ID)
		if device.detailRequested != request.item || selectionChanged {
			p.options.Coordinator.CancelDetail(request.scope)
		}
		device.detailStarted, device.detailRequested = request.sequence, request.item
	} else if kind == "playback" {
		if request.sequence < device.playbackStarted {
			return false
		}
		device.playbackStarted = request.sequence
	} else if kind == "episodes" {
		if request.sequence < device.episodesStarted {
			return false
		}
		device.episodesStarted = request.sequence
	} else {
		if request.sequence < device.progressStarted {
			return false
		}
		// 报告正文沿原流程读取；已知播放先核对关联，未知播放仍保留入口围栏。
		invalidating := kind == "stop" || report != nil && (report.IsPaused != nil && *report.IsPaused || strings.EqualFold(report.PlayMethod, "Transcode"))
		if (kind == "stop" || kind == "progress") && device.playing.Item.ID != "" {
			if report == nil || !validPreloadID(report.ItemID) || !report.matchesPlayback(device.playing) {
				return false
			}
		}
		device.progressStarted = request.sequence
		if report != nil && validPreloadID(report.ItemID) && (kind == "stop" || kind == "progress") && invalidating {
			device.playing.Epoch++
			p.options.Coordinator.CancelNext(request.scope)
		}
		if kind == "stream" {
			p.options.Coordinator.TakeoverScope(request.scope)
		}
	}
	return true
}

func currentPreloadObservation(device *preloadDevice, request preloadRequest, item string) bool {
	if device == nil || device.detailRequested != item || !preloadControlMatches(device, request) {
		return false
	}
	if request.fromPlayback {
		if request.detailSequence != device.detailStarted || time.Since(device.detailAt) > preloadAssociationTTL {
			return false
		}
		if request.fromEpisodes {
			return device.detailResponded == device.detailStarted && request.sequence == device.episodesStarted
		}
		return device.detailResponded == device.detailStarted && (request.sequence == 0 || request.sequence == device.playbackStarted)
	}
	return request.sequence == device.detailStarted
}

func preloadControlMatches(device *preloadDevice, request preloadRequest) bool {
	return request.ua == device.controlUA && (request.client == "" || device.client == "" || request.client == device.client)
}

func (p *embyPreloader) observeDetail(request preloadRequest, item preloadItem) {
	p.mu.Lock()
	device := p.deviceLocked(request)
	if device == nil || device.user == "" || !currentPreloadObservation(device, request, item.ID) {
		p.mu.Unlock()
		return
	}
	changed := device.detail.ID != item.ID
	previous := device.detail
	previousSelected := device.selected
	oldSource, oldSourceOK := selectPreloadSource(previous, previousSelected)
	if !request.fromPlayback {
		device.detailAt = time.Now()
		device.detailResponded = request.sequence
	} else {
		// 补源响应只补资料，身份已在请求开始时核对当前完整控制版本。
		request.ua, request.client = device.controlUA, device.client
	}
	if changed {
		device.revision++
		device.selected = ""
	}
	if !changed {
		item = mergePreloadItem(item, previous)
	}
	if device.pendingID == item.ID && device.pendingSequence == device.playbackStarted && time.Since(device.pendingAt) <= preloadAssociationTTL {
		item.MediaSources = fillPreloadSources(item.MediaSources, device.pending.MediaSources)
		if request.selected == "" {
			_, pendingSelected := selectPreloadSource(item, device.pendingSelected)
			if pendingSelected {
				request.selected = device.pendingSelected
			}
		}
	}
	if request.selected != "" {
		device.selected = request.selected
	}
	newSource, newSourceOK := selectPreloadSource(item, device.selected)
	sourceChanged := !changed && oldSourceOK && newSourceOK && oldSource.Path != "" && newSource.Path != "" && (oldSource.ID != newSource.ID || oldSource.Path != newSource.Path)
	selectionChanged := !changed && previousSelected != device.selected
	invalidate := changed || sourceChanged || (!changed && oldSourceOK && !newSourceOK)
	if sourceChanged || (selectionChanged && previousSelected != "") {
		device.revision++
	}
	playInvalid := !changed && device.playing.Active && device.playing.Item.ID == item.ID && (sourceChanged || (selectionChanged && previousSelected != "") || (oldSourceOK && !newSourceOK))
	if playInvalid {
		device.playing.Active = false
		device.playing.Epoch++
	}
	device.detail = item
	request.user = device.user
	request.selected = device.selected
	ua := p.preloadPlayerUA(request, device)
	revision := device.revision
	members := device.members
	if device.season != item.ID || time.Since(device.membersAt) > preloadAssociationTTL {
		members = nil
	}
	if playInvalid {
		p.options.Coordinator.CancelScope(request.scope)
	} else if invalidate {
		p.options.Coordinator.CancelDetail(request.scope)
	}
	p.mu.Unlock()
	enriched := request.enriched || (!changed && (selectionChanged || preloadHashSources(item.MediaSources) != preloadHashSources(previous.MediaSources)))
	if ua == "" || (item.Type != "Movie" && item.Type != "Episode" && item.Type != "Series" && item.Type != "Season") {
		return
	}
	p.submit(request, item, ua, revision, playback.PreloadDetail, "", members, enriched)
}

func (p *embyPreloader) invalidateDetail(request preloadRequest, permission bool) {
	p.mu.Lock()
	device := p.devices[request.scope]
	if device == nil || (request.user != "" && device.user != "" && request.user != device.user) || (request.sequence != 0 && !currentPreloadObservation(device, request, request.item)) {
		p.mu.Unlock()
		return
	}
	device.detail, device.pending = preloadItem{}, preloadPlaybackInfo{}
	device.pendingID, device.pendingSelected, device.selected = "", "", ""
	device.revision++
	if permission {
		device.playing.Active = false
		device.playing.Epoch++
	}
	if permission {
		p.options.Coordinator.CancelScope(request.scope)
	} else {
		p.options.Coordinator.CancelDetail(request.scope)
	}
	p.mu.Unlock()
}

func preloadUAKey(request preloadRequest, device *preloadDevice) string {
	if request.weakIdentity {
		return ""
	}
	return preloadHash(request.scope, device.user, device.client, device.controlUA, "emby-force-direct")
}

func (p *embyPreloader) preloadPlayerUA(request preloadRequest, device *preloadDevice) string {
	if !request.weakIdentity {
		if ua := p.uas.Lookup(preloadUAKey(request, device), request.generation); ua != "" {
			return ua
		}
	}
	return device.controlUA
}

func (p *embyPreloader) observePlayback(request preloadRequest, info preloadPlaybackInfo) {
	p.mu.Lock()
	device := p.deviceLocked(request)
	if device == nil || request.sequence != device.playbackStarted || !preloadControlMatches(device, request) {
		p.mu.Unlock()
		return
	}
	device.pendingID, device.pending, device.pendingAt, device.pendingSelected = request.item, info, time.Now(), request.selected
	device.pendingSequence = request.sequence
	if current := device.playing; current.Active && current.Item.ID == request.item && device.detail.ID != request.item {
		source, known := selectPreloadSource(actualPreloadItem(device, request), request.selected)
		if !known || source.ID != current.Source.ID || source.Path != current.Source.Path {
			device.playing.Active = false
			device.playing.Epoch++
			p.options.Coordinator.CancelNext(request.scope)
		}
	}
	if device.detail.ID != request.item || device.detailRequested != request.item || device.detailResponded != device.detailStarted || time.Since(device.detailAt) > preloadAssociationTTL {
		p.mu.Unlock()
		return
	}
	item := device.detail
	request.detailSequence = device.detailStarted
	if request.sequence < device.detailResponded {
		item.MediaSources = fillPreloadSources(item.MediaSources, info.MediaSources)
		request.selected = device.selected
	} else {
		item.MediaSources = mergePreloadSources(info.MediaSources, item.MediaSources)
	}
	p.mu.Unlock()
	request.fromPlayback = true
	p.observeDetail(request, item)
}

func (p *embyPreloader) observeStream(request preloadRequest) {
	p.observeActualPlayback(request, request.session, true)
}

func actualPreloadItem(device *preloadDevice, request preloadRequest) preloadItem {
	item := preloadItem{ID: request.item}
	if device.playing.Item.ID == request.item && device.playing.Active {
		item = device.playing.Item
	}
	if device.detail.ID == request.item {
		item = device.detail
	}
	if device.pendingID == request.item && device.pendingSequence == device.playbackStarted && time.Since(device.pendingAt) <= preloadAssociationTTL {
		if device.detail.ID != request.item || device.pendingSequence >= device.detailResponded {
			item.MediaSources = fillPreloadSources(device.pending.MediaSources, item.MediaSources)
		} else {
			item.MediaSources = fillPreloadSources(item.MediaSources, device.pending.MediaSources)
		}
	}
	return item
}

func (p *embyPreloader) observeActualPlayback(request preloadRequest, session string, stream bool) {
	p.mu.Lock()
	device := p.deviceLocked(request)
	if device == nil || request.sequence != device.progressStarted || request.sequence < device.controlStarted {
		p.mu.Unlock()
		return
	}
	item := actualPreloadItem(device, request)
	source, ok := selectPreloadSource(item, request.selected)
	ua := ""
	if stream {
		ua = request.ua
	} else if current := device.playing; current.Active && current.Item.ID == request.item && current.Source.ID == source.ID && current.Source.Path == source.Path && (current.UASession == "" || session == "" || current.UASession == session) {
		ua = current.UA
	}
	p.mu.Unlock()
	if !ok || stream && ua == "" || !p.trustedSource(source.Path) {
		return
	}
	identity := ""
	if ua != "" {
		var supported bool
		identity, _, supported = p.options.Resolve(source.Path, ua, true)
		if !supported {
			return
		}
	}
	p.mu.Lock()
	device = p.devices[request.scope]
	if !p.valid(request) || device == nil || device.generation != request.generation || request.sequence != device.progressStarted || request.sequence < device.controlStarted {
		p.mu.Unlock()
		return
	}
	item = actualPreloadItem(device, request)
	fresh, valid := selectPreloadSource(item, request.selected)
	if !valid || fresh.ID != source.ID || fresh.Path != source.Path {
		p.mu.Unlock()
		return
	}
	if !stream {
		p.options.Coordinator.TakeoverScope(request.scope)
	}
	current := device.playing
	if !stream && session == "" && ua != "" {
		session = current.UASession
	}
	sameSource := current.Active && current.Item.ID == request.item && current.Source.ID == source.ID && current.Source.Path == source.Path
	previousSession := current.Session
	changed := !current.Active || current.Item.ID != request.item || current.Source.ID != source.ID || current.Source.Path != source.Path || (current.Session != "" && session != "" && current.Session != session) || (stream && device.playerUA != ua)
	if changed {
		current = preloadPlaying{Epoch: current.Epoch + 1}
	}
	learned := false
	if stream {
		device.playerUA = ua
		if device.controlUA != "" && time.Since(device.detailAt) <= preloadAssociationTTL {
			learned = p.uas.Remember(preloadUAKey(request, device), ua, request.generation)
		}
	}
	current.Item, current.Source, current.Active, current.Paused = item, source, true, false
	current.UA = ua
	if stream {
		current.UASession = session
		if sameSource {
			// 同源 Range 不能替换已确认的开始会话，待匹配的开始报告关联实际 UA。
			current.Session = previousSession
		}
	} else if ua != "" && session != "" {
		current.UASession = session
	}
	if session != "" && (!stream || current.Session == "") {
		current.Session = session
	}
	device.playing = current
	if changed {
		p.options.Coordinator.TakeoverNext(request.scope)
	}
	p.options.Coordinator.Takeover(identity)
	p.mu.Unlock()
	if learned && helpers.AppLogger != nil {
		helpers.AppLogger.Debugf("已更新预加载使用的播放 UA，入口=Emby，scope=%s", request.scope[:12])
	}
}

func (progress preloadProgress) matchesPlayback(current preloadPlaying) bool {
	session := current.Session
	if current.UA != "" && current.UASession != "" {
		session = current.UASession
	}
	return current.Item.ID == "" || progress.ItemID == current.Item.ID &&
		(progress.MediaSourceID == "" || current.Source.ID == "" || progress.MediaSourceID == current.Source.ID) &&
		(progress.PlaySessionID == "" || session == "" || progress.PlaySessionID == session)
}

func (p *embyPreloader) observeProgress(request preloadRequest, progress preloadProgress, kind string) {
	if !validPreloadID(progress.ItemID) {
		return
	}
	denied := strings.Contains(progress.MediaSourceID, MediaSourceIdSegment) || strings.EqualFold(progress.PlayMethod, "Transcode")
	if kind == "start" && !denied && (progress.IsPaused == nil || !*progress.IsPaused) {
		request.item, request.selected = progress.ItemID, progress.MediaSourceID
		p.observeActualPlayback(request, progress.PlaySessionID, false)
		return
	}
	p.mu.Lock()
	device := p.deviceLocked(request)
	if device == nil || request.sequence != device.progressStarted {
		p.mu.Unlock()
		return
	}
	if kind == "stop" && !progress.matchesPlayback(device.playing) {
		p.mu.Unlock()
		return
	}
	if denied && (device.detail.ID == progress.ItemID || device.playing.Item.ID == progress.ItemID) && (device.playing.Session == "" || progress.PlaySessionID == "" || device.playing.Session == progress.PlaySessionID) {
		device.playing.Active = false
		device.playing.Epoch++
		if device.detail.ID == progress.ItemID {
			device.detail, device.pending = preloadItem{}, preloadPlaybackInfo{}
			device.pendingID, device.pendingSelected, device.selected = "", "", ""
			device.revision++
		}
		p.options.Coordinator.CancelScope(request.scope)
		p.mu.Unlock()
		return
	}
	if kind == "stop" || denied || (progress.IsPaused != nil && *progress.IsPaused) {
		if device.playing.Item.ID != progress.ItemID || (progress.PlaySessionID != "" && device.playing.Session != "" && progress.PlaySessionID != device.playing.Session) || (!denied && progress.MediaSourceID != "" && device.playing.Source.ID != progress.MediaSourceID) {
			p.options.Coordinator.CancelNext(request.scope)
			p.mu.Unlock()
			return
		}
		device.playing.Active = kind != "stop" && !denied && device.playing.Active
		device.playing.Paused = true
		device.playing.Epoch++
		p.options.Coordinator.CancelScope(request.scope)
		p.mu.Unlock()
		return
	}
	current := device.playing
	if !current.Active || current.Item.ID != progress.ItemID || (progress.MediaSourceID != "" && progress.MediaSourceID != current.Source.ID) || (current.Session != "" && progress.PlaySessionID != "" && current.Session != progress.PlaySessionID) ||
		current.UASession != "" && (current.Session != "" && current.UASession != current.Session || progress.PlaySessionID != "" && current.UASession != progress.PlaySessionID) ||
		progress.IsPaused == nil || *progress.IsPaused || device.user == "" {
		p.options.Coordinator.CancelNext(request.scope)
		p.mu.Unlock()
		return
	}
	if current.Session == "" && progress.PlaySessionID != "" {
		current.Session = progress.PlaySessionID
	}
	if current.UA != "" && current.UASession == "" && progress.PlaySessionID != "" {
		current.UASession = progress.PlaySessionID
	}
	current.PositionTicks = progress.PositionTicks
	current.Paused = false
	current.RunTimeTicks = progress.RunTimeTicks
	current.NextID = ""
	for index, queued := range progress.NowPlayingQueue {
		if queued.ID == current.Item.ID && index+1 < len(progress.NowPlayingQueue) && validPreloadID(progress.NowPlayingQueue[index+1].ID) {
			current.NextID = progress.NowPlayingQueue[index+1].ID
			break
		}
	}
	device.playing = current
	request.user = device.user
	request.selected = current.Source.ID
	ua := current.UA
	p.mu.Unlock()
	if ua == "" || (current.Item.Type != "" && current.Item.Type != "Episode" && !(current.Item.Type == "Movie" && current.Item.PartCount > 1) && !(current.Item.Type == "Video" && current.Item.ParentID != "")) {
		p.cancelNext(request, current.Epoch)
		return
	}
	duration := current.Source.RunTimeTicks
	if duration <= 0 {
		duration = current.RunTimeTicks
	}
	if duration <= 0 && len(current.Item.MediaSources) == 1 {
		duration = current.Item.RunTimeTicks
	}
	if !playback.ShouldPreloadNext(preloadTicks(duration), preloadTicks(current.PositionTicks)) || current.PositionTicks < 0 {
		p.cancelNext(request, current.Epoch)
		return
	}
	association := strconv.FormatUint(current.Epoch, 10) + ":" + current.Session + ":" + current.Source.ID
	p.submit(request, current.Item, ua, current.Epoch, playback.PreloadNext, association, nil, false, current.NextID)
}

func (p *embyPreloader) cancelNext(request preloadRequest, epoch uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	device := p.devices[request.scope]
	if device != nil && request.sequence == device.progressStarted && device.playing.Epoch == epoch {
		p.options.Coordinator.CancelNext(request.scope)
	}
}

func preloadTicks(ticks int64) time.Duration {
	if ticks <= 0 || ticks > (1<<63-1)/100 {
		return 0
	}
	return time.Duration(ticks) * 100
}

func mergePreloadDisplay(value, old preloadItem) preloadItem {
	if value.Name == nil || value.Name == "" {
		value.Name = old.Name
	}
	if value.SeriesName == nil || value.SeriesName == "" {
		value.SeriesName = old.SeriesName
	}
	if value.IndexNumber == nil {
		value.IndexNumber = old.IndexNumber
	}
	if value.ParentIndexNumber == nil {
		value.ParentIndexNumber = old.ParentIndexNumber
	}
	return value
}

func mergePreloadItem(value, old preloadItem) preloadItem {
	value = mergePreloadDisplay(value, old)
	if value.SeriesID == "" {
		value.SeriesID = old.SeriesID
	}
	if value.SeasonID == "" {
		value.SeasonID = old.SeasonID
	}
	if value.ParentID == "" {
		value.ParentID = old.ParentID
	}
	if value.PartCount == 0 {
		value.PartCount = old.PartCount
	}
	if value.RunTimeTicks == 0 {
		value.RunTimeTicks = old.RunTimeTicks
	}
	if value.MediaSources == nil {
		value.MediaSources = old.MediaSources
	}
	return value
}

func mergePreloadSources(current, additions []preloadSource) []preloadSource {
	result := append([]preloadSource(nil), current...)
	for _, source := range additions {
		found := false
		for index, existing := range result {
			if existing.ID == source.ID {
				if existing.Path == "" {
					existing.Path = source.Path
				}
				if existing.RunTimeTicks == 0 {
					existing.RunTimeTicks = source.RunTimeTicks
				}
				if existing.Container == "" {
					existing.Container = source.Container
				}
				if len(existing.MediaStreams) == 0 {
					existing.MediaStreams = source.MediaStreams
				}
				if existing.SupportsDirectPlay == nil {
					existing.SupportsDirectPlay = source.SupportsDirectPlay
				}
				if existing.SupportsDirectStream == nil {
					existing.SupportsDirectStream = source.SupportsDirectStream
				}
				result[index], found = existing, true
				break
			}
		}
		if !found {
			result = append(result, source)
		}
	}
	return result
}

// A fresh detail list is authoritative; only fill its missing fields from earlier PlaybackInfo.
func fillPreloadSources(current, additions []preloadSource) []preloadSource {
	if current == nil {
		return append([]preloadSource(nil), additions...)
	}
	result := mergePreloadSources(current, additions)
	return result[:len(current)]
}

func preloadHashSources(sources []preloadSource) string {
	data, _ := json.Marshal(sources)
	return preloadHash(string(data))
}

func validPreloadItem(value preloadItem) bool {
	return validPreloadID(value.ID) && !strings.EqualFold(value.PlayAccess, "None") && !value.IsLive && !value.IsInfiniteStream && (!value.IsFolder || value.Type == "Series" || value.Type == "Season") && (value.Type == "Movie" || value.Type == "Episode" || value.Type == "Series" || value.Type == "Season" || value.Type == "Video") && (len(value.MediaSources) == 0 || validPreloadSources(value.MediaSources, value.ID))
}

func validPreloadSources(sources []preloadSource, item string) bool {
	if len(sources) == 0 || len(sources) > 32 {
		return false
	}
	seen := make(map[string]bool, len(sources))
	for _, source := range sources {
		if source.ID == "" || len(source.ID) > 256 || seen[source.ID] || (source.ItemID != "" && source.ItemID != item) || len(source.Path) > 16<<10 || len(source.Container) > 64 || len(source.MediaStreams) > 128 {
			return false
		}
		for _, stream := range source.MediaStreams {
			if len(stream.Type) > 64 || len(stream.Codec) > 64 {
				return false
			}
		}
		seen[source.ID] = true
	}
	return true
}

func selectPreloadSource(item preloadItem, selected string) (preloadSource, bool) {
	if !validPreloadSources(item.MediaSources, item.ID) {
		return preloadSource{}, false
	}
	for _, source := range item.MediaSources {
		if selected != "" && source.ID == selected && eligiblePreloadSource(source) {
			return source, true
		}
	}
	if selected == "" {
		var eligible preloadSource
		count := 0
		for _, source := range item.MediaSources {
			if eligiblePreloadSource(source) {
				eligible, count = source, count+1
			}
		}
		if count == 1 {
			return eligible, true
		}
	}
	return preloadSource{}, false
}

func eligiblePreloadSource(source preloadSource) bool {
	denied := source.SupportsDirectPlay != nil && !*source.SupportsDirectPlay && source.SupportsDirectStream != nil && !*source.SupportsDirectStream
	return !denied && !source.IsInfiniteStream && !strings.Contains(source.ID, MediaSourceIdSegment)
}

type preloadRequestBody struct {
	io.ReadCloser
	data       bytes.Buffer
	complete   bool
	overflow   bool
	failed     bool
	onComplete func([]byte)
}

func (b *preloadRequestBody) Read(value []byte) (int, error) {
	n, err := b.ReadCloser.Read(value)
	b.failed = b.failed || err != nil && err != io.EOF
	if !b.overflow && !b.failed {
		if b.data.Len()+n > 64<<10 {
			b.overflow = true
			b.data.Reset()
		} else {
			_, _ = b.data.Write(value[:n])
		}
	}
	if err == io.EOF && !b.complete {
		b.complete = true
		if b.onComplete != nil && !b.overflow && !b.failed {
			b.onComplete(b.data.Bytes())
		}
	}
	return n, err
}

type preloadResponseWriter struct {
	gin.ResponseWriter
	capture  bool
	data     bytes.Buffer
	overflow bool
	failed   bool
}

func (w *preloadResponseWriter) Write(value []byte) (int, error) {
	n, err := w.ResponseWriter.Write(value)
	w.failed = w.failed || err != nil
	if w.capture && !w.overflow {
		if w.data.Len()+n > preloadBodyLimit {
			w.overflow = true
			w.data.Reset()
		} else {
			_, _ = w.data.Write(value[:n])
		}
	}
	return n, err
}

func (w *preloadResponseWriter) WriteString(value string) (int, error) { return w.Write([]byte(value)) }

func preloadItemTitle(item preloadItem) string {
	title, _ := item.Name.(string)
	season, hasSeason := item.ParentIndexNumber.(float64)
	episode, hasEpisode := item.IndexNumber.(float64)
	if item.Type == "Episode" && hasSeason && hasEpisode && season >= 0 && season <= 9999 && episode >= 0 && episode <= 9999 && season == float64(int(season)) && episode == float64(int(episode)) {
		if series, _ := item.SeriesName.(string); series != "" {
			title = series
		}
		if title != "" {
			return fmt.Sprintf("%s S%02dE%02d", title, int(season), int(episode))
		}
	}
	return title
}
