package emby

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"qmediasync/emby302/util/https"
	"qmediasync/internal/playback"
)

const preloadItemFields = "Path,MediaSources,MediaStreams,RunTimeTicks,SeriesId,SeasonId,ParentId,PartCount,UserData"

type preloadItems struct {
	Items            []preloadItem
	TotalRecordCount int
}

type embyPreloadJob struct {
	owner          *embyPreloader
	request        preloadRequest
	item           preloadItem
	ua             string
	kind           playback.PreloadKind
	members        map[string]bool
	nextID         string
	key            string
	piTried        bool
	prepared       bool
	identity       string
	resolve        func(context.Context, *playback.PreloadTask) playback.PreloadResult
	metadataFailed bool
	metadataHTTP   int
}

func (p *embyPreloader) submit(request preloadRequest, item preloadItem, ua string, revision uint64, kind playback.PreloadKind, association string, members map[string]bool, enriched bool, nextID ...string) {
	if !p.valid(request) || request.user == "" || ua == "" {
		return
	}
	job := &embyPreloadJob{owner: p, request: request, item: item, ua: ua, kind: kind, members: members}
	var account playback.PreloadAccount
	if len(nextID) == 1 {
		job.nextID = nextID[0]
	}
	if kind == playback.PreloadDetail && (item.Type == "Movie" || item.Type == "Episode") {
		if source, ok := selectPreloadSource(item, request.selected); ok && p.trustedSource(source.Path) {
			job.identity, job.resolve, job.prepared = p.options.Resolve(source.Path, ua, true)
			if p.options.Account != nil {
				account = p.options.Account(source.Path)
			}
		}
	}
	key := preloadHash(request.scope, item.ID, strconv.Itoa(int(kind)), strconv.FormatUint(revision, 10), association, job.nextID, ua, strconv.FormatUint(request.generation, 10))
	job.key = key
	candidate := playback.PreloadCandidate{
		Key: key, Scope: request.scope, Instance: p.options.Instance, Source: job.identity, Account: account, Kind: kind, Enriched: enriched, Run: job.run,
		Log: playback.PreloadLogContext{Entry: "Emby", Trigger: preloadTrigger(item, kind), ItemID: item.ID, Title: preloadItemTitle(item)},
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	device := p.devices[request.scope]
	if !p.valid(request) || (kind == playback.PreloadDetail && (!currentPreloadObservation(device, request, item.ID) || device.revision != revision)) {
		return
	}
	if kind == playback.PreloadNext && (device == nil || request.sequence != device.progressStarted || !device.playing.Active || device.playing.Paused || device.playing.Epoch != revision || device.playing.Item.ID != item.ID) {
		return
	}
	p.options.Coordinator.Submit(candidate)
}

func (p *embyPreloader) trustedSource(raw string) bool {
	value, err := url.Parse(raw)
	return err == nil && (value.Scheme == "http" || value.Scheme == "https") && value.Host != "" && value.User == nil && value.Fragment == "" && p.options.TrustedOrigin(value)
}

func (j *embyPreloadJob) run(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
	if !j.owner.valid(j.request) || !task.Valid() {
		return j.skipped(playback.ErrPreloadCanceled, "canceled")
	}
	task.SetLogTarget(j.item.ID, preloadItemTitle(j.item))
	if !j.prepared {
		var err error
		if j.kind == playback.PreloadNext {
			j.item, err = j.successor(ctx, task)
		} else if j.item.Type == "Series" || j.item.Type == "Season" {
			j.item, err = j.pendingEpisode(ctx, task)
		}
		if err != nil {
			return j.skipped(err, "relation_unavailable")
		}
		task.SetLogTarget(j.item.ID, preloadItemTitle(j.item))
		if j.item.Type != "Movie" && j.item.Type != "Episode" && !(j.kind == playback.PreloadNext && j.item.Type == "Video") {
			return j.skipped(nil, "candidate_unknown")
		}
		selected := j.request.selected
		if j.kind == playback.PreloadNext {
			selected = ""
		}
		source, ok := selectPreloadSource(j.item, selected)
		if !ok && len(j.item.MediaSources) == 0 {
			var value preloadItem
			if err := j.getItem(ctx, task, j.item.ID, &value, true); err != nil {
				return j.skipped(err, "source_unavailable")
			}
			if value.Type != j.item.Type {
				return j.skipped(nil, "source_changed")
			}
			j.item = mergePreloadDisplay(value, j.item)
			task.SetLogTarget(j.item.ID, preloadItemTitle(j.item))
			source, ok = selectPreloadSource(j.item, selected)
		}
		if !ok {
			return j.skipped(nil, "source_ambiguous")
		}
		if source.Path == "" && completePreloadSource(source) {
			if !j.claimPlaybackInfo() {
				return j.skipped(nil, "warm_playbackinfo_already_tried")
			}
			var info preloadPlaybackInfo
			query := url.Values{"UserId": {j.request.user}, "MediaSourceId": {source.ID}, "IsPlayback": {"false"}, "AutoOpenLiveStream": {"false"}}
			if err := j.fetch(ctx, task, "/Items/"+j.item.ID+"/PlaybackInfo", query, false, &info); err != nil {
				return j.skipped(err, "source_unavailable")
			}
			if !validPreloadSources(info.MediaSources, j.item.ID) {
				return j.skipped(nil, "source_unavailable")
			}
			value := j.item
			value.MediaSources = info.MediaSources
			source, ok = selectPreloadSource(value, source.ID)
			if !ok {
				return j.skipped(nil, "source_changed")
			}
		}
		if !j.owner.trustedSource(source.Path) {
			return j.skipped(nil, "source_origin_unknown")
		}
		j.identity, j.resolve, j.prepared = j.owner.options.Resolve(source.Path, j.ua, true)
		if !j.prepared || j.resolve == nil {
			return j.skipped(nil, "provider_not_supported")
		}
	}
	if !j.owner.valid(j.request) || !task.Valid() {
		return j.skipped(playback.ErrPreloadCanceled, "canceled")
	}
	return j.resolve(ctx, task)
}

func preloadTrigger(item preloadItem, kind playback.PreloadKind) string {
	if kind == playback.PreloadNext {
		if item.Type == "" || item.Type == "Episode" {
			return "播放接近结束"
		}
		return "准备下一分段"
	}
	switch item.Type {
	case "Movie":
		return "电影详情"
	case "Episode":
		return "单集详情"
	case "Series":
		return "剧集页待播集"
	case "Season":
		return "季页待播集"
	}
	return ""
}

func (j *embyPreloadJob) skipped(err error, reason string) playback.PreloadResult {
	result := playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: reason}
	switch {
	case errors.Is(err, playback.ErrPreloadMetadataBudget):
		result.Reason = "metadata_budget"
	case errors.Is(err, playback.ErrPreloadCanceled), errors.Is(err, context.Canceled):
		result.Reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		result.Reason = "timeout"
	case err != nil && j.metadataFailed:
		result.Reason, result.StatusSource, result.HTTPStatus = "metadata_request_failed", "Emby", j.metadataHTTP
	}
	return result
}

// Natural enrichment may reopen preparation, but never repeats a conditional PlaybackInfo.
func (j *embyPreloadJob) claimPlaybackInfo() bool {
	if j.piTried {
		return false
	}
	j.piTried = true
	j.owner.mu.Lock()
	defer j.owner.mu.Unlock()
	device := j.owner.devices[j.request.scope]
	if device == nil || j.key == "" {
		return true
	}
	previous := &device.piDetail
	if j.kind == playback.PreloadNext {
		previous = &device.piNext
	}
	if *previous == j.key {
		return false
	}
	*previous = j.key
	return true
}

func completePreloadSource(source preloadSource) bool {
	if source.ID == "" || source.IsInfiniteStream || source.RunTimeTicks <= 0 || source.Container == "" || strings.EqualFold(source.Container, "strm") {
		return false
	}
	video, audio := false, false
	for _, stream := range source.MediaStreams {
		if stream.Codec == "" {
			continue
		}
		video = video || strings.EqualFold(stream.Type, "Video")
		audio = audio || strings.EqualFold(stream.Type, "Audio")
	}
	return video && audio
}

func (j *embyPreloadJob) pendingEpisode(ctx context.Context, task *playback.PreloadTask) (preloadItem, error) {
	parent := j.item
	series := parent.ID
	if parent.Type == "Season" {
		series = parent.SeriesID
	}
	if !validPreloadID(series) {
		return preloadItem{}, errors.New("missing_series")
	}
	query := url.Values{"ParentId": {parent.ID}, "Recursive": {"true"}, "Filters": {"IsResumable"}, "IncludeItemTypes": {"Episode"}, "Limit": {"1"}, "Fields": {preloadItemFields}}
	var result preloadItems
	if err := j.fetch(ctx, task, "/Users/"+j.request.user+"/Items", query, true, &result); err != nil {
		return preloadItem{}, err
	}
	if result.Items == nil || len(result.Items) > 1 {
		return preloadItem{}, errors.New("invalid_resume")
	}
	if len(result.Items) == 0 {
		query = url.Values{"UserId": {j.request.user}, "SeriesId": {series}, "ParentId": {parent.ID}, "Limit": {"1"}, "Fields": {preloadItemFields}}
		if err := j.fetch(ctx, task, "/Shows/NextUp", query, true, &result); err != nil {
			return preloadItem{}, err
		}
	}
	if len(result.Items) != 1 || result.Items[0].Type != "Episode" || result.Items[0].SeriesID != series || !validPreloadItem(result.Items[0]) {
		return preloadItem{}, errors.New("missing_pending")
	}
	value := result.Items[0]
	if parent.Type == "Season" && value.SeasonID != parent.ID && value.ParentID != parent.ID && !j.members[value.ID] {
		query = url.Values{"UserId": {j.request.user}, "SeasonId": {parent.ID}, "StartItemId": {value.ID}, "Limit": {"1"}, "Fields": {preloadItemFields}}
		var scoped preloadItems
		source, known := selectPreloadSource(value, "")
		final := known && j.owner.trustedSource(source.Path)
		if err := j.fetch(ctx, task, "/Shows/"+series+"/Episodes", query, true, &scoped, final); err != nil {
			return preloadItem{}, err
		}
		if len(scoped.Items) != 1 || scoped.Items[0].ID != value.ID || scoped.Items[0].SeriesID != series {
			return preloadItem{}, errors.New("season_mismatch")
		}
		value = mergePreloadItem(value, scoped.Items[0])
	}
	return value, nil
}

func (j *embyPreloadJob) successor(ctx context.Context, task *playback.PreloadTask) (preloadItem, error) {
	current := j.item
	if current.Type == "" {
		var value preloadItem
		if err := j.getItem(ctx, task, current.ID, &value); err != nil {
			return preloadItem{}, err
		}
		value.MediaSources = fillPreloadSources(value.MediaSources, current.MediaSources)
		previous, known := selectPreloadSource(current, j.request.selected)
		source, valid := selectPreloadSource(value, j.request.selected)
		if !known || !valid || source.ID != previous.ID || source.Path != previous.Path {
			return preloadItem{}, errors.New("source_changed")
		}
		current = value
	}
	if current.Type == "Episode" {
		if !validPreloadID(current.SeriesID) {
			return preloadItem{}, errors.New("missing_series")
		}
		if j.nextID != "" && j.nextID != current.ID {
			var value preloadItem
			if err := j.getItem(ctx, task, j.nextID, &value); err != nil {
				return preloadItem{}, err
			}
			if value.Type != "Episode" || value.SeriesID != current.SeriesID {
				return preloadItem{}, errors.New("queue_mismatch")
			}
			return value, nil
		}
		query := url.Values{"UserId": {j.request.user}, "StartItemId": {current.ID}, "Limit": {"2"}, "Fields": {preloadItemFields}}
		var result preloadItems
		if err := j.fetch(ctx, task, "/Shows/"+current.SeriesID+"/Episodes", query, true, &result); err != nil {
			return preloadItem{}, err
		}
		if len(result.Items) != 2 || result.Items[0].ID != current.ID || result.Items[1].ID == current.ID || result.Items[0].SeriesID != current.SeriesID || result.Items[1].SeriesID != current.SeriesID || result.Items[1].Type != "Episode" || !validPreloadItem(result.Items[1]) {
			return preloadItem{}, errors.New("successor_ambiguous")
		}
		return result.Items[1], nil
	}
	parent := current
	if current.Type == "Video" && validPreloadID(current.ParentID) {
		if err := j.getItem(ctx, task, current.ParentID, &parent); err != nil {
			return preloadItem{}, err
		}
	}
	if parent.Type != "Movie" || parent.PartCount <= 1 {
		return preloadItem{}, errors.New("not_partitioned_movie")
	}
	query := url.Values{"UserId": {j.request.user}, "Fields": {preloadItemFields}}
	var result preloadItems
	if err := j.fetch(ctx, task, "/Videos/"+parent.ID+"/AdditionalParts", query, true, &result); err != nil {
		return preloadItem{}, err
	}
	if result.Items == nil || len(result.Items) > 64 || len(result.Items)+1 != parent.PartCount || result.TotalRecordCount != len(result.Items) {
		return preloadItem{}, errors.New("parts_incomplete")
	}
	chain := append([]preloadItem{parent}, result.Items...)
	seen := make(map[string]bool, len(chain))
	for index, value := range chain {
		if !validPreloadItem(value) || seen[value.ID] || (index > 0 && ((value.ParentID != "" && value.ParentID != parent.ID) || value.Type != "Video")) {
			return preloadItem{}, errors.New("parts_invalid")
		}
		seen[value.ID] = true
	}
	for index, value := range chain {
		if value.ID == current.ID && index+1 < len(chain) {
			return chain[index+1], nil
		}
	}
	return preloadItem{}, errors.New("no_next_part")
}

func (j *embyPreloadJob) getItem(ctx context.Context, task *playback.PreloadTask, id string, value *preloadItem, final ...bool) error {
	if !validPreloadID(id) {
		return errors.New("invalid_item")
	}
	query := url.Values{"Fields": {preloadItemFields}}
	if err := j.fetch(ctx, task, "/Users/"+j.request.user+"/Items/"+id, query, true, value, final...); err != nil {
		return err
	}
	if value.ID != id || !validPreloadItem(*value) {
		return errors.New("invalid_item")
	}
	return nil
}

func (j *embyPreloadJob) fetch(ctx context.Context, task *playback.PreloadTask, path string, query url.Values, safeGET bool, target any, final ...bool) error {
	j.metadataFailed, j.metadataHTTP = true, 0
	if !j.owner.valid(j.request) || !validPreloadID(j.request.user) {
		return playback.ErrPreloadCanceled
	}
	query.Set("UserId", j.request.user)
	endpoint, err := url.Parse(strings.TrimRight(j.request.origin, "/") + j.request.prefix + path)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return errors.New("invalid_metadata_origin")
	}
	endpoint.RawQuery = query.Encode()
	header := http.Header{"X-Emby-Token": {j.request.token}, "User-Agent": {j.request.ua}, "Accept": {"application/json"}, "Accept-Encoding": {"gzip"}}
	call := func(ctx context.Context) (bool, error) {
		j.metadataHTTP = 0
		if !j.owner.valid(j.request) {
			return false, playback.ErrPreloadCanceled
		}
		response, err := https.Get(endpoint.String()).Header(header.Clone()).Context(ctx).DoSingle()
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			_, network := errors.AsType[net.Error](err)
			return network, errors.New("metadata_network")
		}
		defer response.Body.Close()
		j.metadataHTTP = response.StatusCode
		if response.StatusCode != http.StatusOK {
			return response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504, errors.New("metadata_status")
		}
		if response.ContentLength > preloadBodyLimit {
			return false, errors.New("metadata_too_large")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, preloadBodyLimit+1))
		if err != nil || len(raw) > preloadBodyLimit {
			return false, errors.New("metadata_read")
		}
		data, err := decodePreloadJSON(raw, response.Header)
		if err != nil {
			return false, err
		}
		if err := json.Unmarshal(data, target); err != nil {
			return false, errors.New("metadata_schema")
		}
		j.metadataFailed = false
		return false, nil
	}
	if safeGET && len(final) == 1 && final[0] {
		return task.FinalMetadataGET(ctx, call)
	}
	return task.Metadata(ctx, safeGET, call)
}

func decodePreloadJSON(raw []byte, header http.Header) ([]byte, error) {
	if len(raw) > preloadBodyLimit {
		return nil, errors.New("metadata_too_large")
	}
	contentType, _, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || (contentType != "application/json" && !strings.HasSuffix(contentType, "+json")) {
		return nil, errors.New("metadata_not_json")
	}
	if encoding := strings.TrimSpace(header.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") {
		if !strings.EqualFold(encoding, "gzip") {
			return nil, errors.New("metadata_encoding")
		}
		reader, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, errors.New("metadata_gzip")
		}
		defer reader.Close()
		raw, err = io.ReadAll(io.LimitReader(reader, preloadBodyLimit+1))
		if err != nil || len(raw) > preloadBodyLimit {
			return nil, errors.New("metadata_gzip_limit")
		}
	}
	if len(raw) == 0 || !json.Valid(raw) {
		return nil, errors.New("metadata_json")
	}
	return raw, nil
}

func (p *embyPreloader) observeSeasonMembers(request preloadRequest, query url.Values, data []byte) {
	season, ok := preloadUnique(preloadValues(query, "SeasonId"))
	if !ok || !validPreloadID(season) {
		return
	}
	var result preloadItems
	if json.Unmarshal(data, &result) != nil || result.Items == nil || len(result.Items) > 256 {
		return
	}
	members := make(map[string]bool, len(result.Items))
	for _, value := range result.Items {
		if value.Type != "Episode" || value.SeriesID != request.item || !validPreloadID(value.ID) {
			return
		}
		members[value.ID] = true
	}
	p.mu.Lock()
	device := p.deviceLocked(request)
	var detail preloadItem
	if device != nil && request.sequence == device.episodesStarted && preloadControlMatches(device, request) {
		changed := device.season != season || !maps.Equal(device.members, members)
		device.season, device.members, device.membersAt = season, members, time.Now()
		if changed && device.detail.ID == season && device.detail.Type == "Season" {
			detail = device.detail
			request.user, request.selected = device.user, device.selected
			request.detailSequence = device.detailStarted
		}
	}
	p.mu.Unlock()
	if detail.ID != "" {
		request.item, request.fromPlayback, request.fromEpisodes, request.enriched = detail.ID, true, true, true
		p.observeDetail(request, detail)
	}
}
