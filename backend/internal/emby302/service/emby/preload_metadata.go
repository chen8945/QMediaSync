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

	"qmediasync/internal/emby302/util/https"
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
	pendingFirst   bool
	identity       string
	resolve        func(context.Context, *playback.PreloadTask) playback.PreloadResult
	metadataFailed bool
	metadataHTTP   int
	fetchedItems   map[string]preloadItem
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
		if source, ok := selectPreloadLinkSource(item, request.selected); ok && playback.ValidExternalURL(source.Path) {
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
	if kind == playback.PreloadDetail && (item.Type == "Series" || item.Type == "Season") {
		candidate.MetadataLimit = 7
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	device := p.devices[request.scope]
	if !p.valid(request) || (kind == playback.PreloadDetail && (!currentPreloadObservation(device, request, item.ID) || device.revision != revision || device.detailResponded != device.detailStarted)) {
		return
	}
	if kind == playback.PreloadNext && (device == nil || request.sequence != device.progressStarted || !device.playing.Active || device.playing.Paused || device.playing.Epoch != revision || device.playing.Item.ID != item.ID) {
		return
	}
	p.options.Coordinator.Submit(candidate)
}

func (j *embyPreloadJob) run(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
	if !j.owner.valid(j.request) || !task.Valid() {
		return j.skipped(playback.ErrPreloadCanceled, "canceled")
	}
	task.SetLogTarget(j.item.ID, preloadItemTitle(j.item))
	if !j.prepared {
		parentID := ""
		var sequence uint64
		if j.kind == playback.PreloadDetail && (j.item.Type == "Series" || j.item.Type == "Season") {
			parentID = j.item.ID
		}
		var err error
		if j.kind == playback.PreloadNext {
			j.item, err = j.successor(ctx, task)
		} else if j.item.Type == "Series" || j.item.Type == "Season" {
			j.item, err = j.pendingEpisode(ctx, task)
		}
		if err != nil {
			return j.skipped(err, pendingReason(err))
		}
		task.SetLogTarget(j.item.ID, preloadItemTitle(j.item))
		if j.item.Type != "Movie" && j.item.Type != "Episode" && !(j.kind == playback.PreloadNext && j.item.Type == "Video") {
			return j.skipped(nil, "candidate_unknown")
		}
		j.mergeCachedItem()
		selected := j.request.selected
		if j.kind == playback.PreloadNext {
			selected = ""
		}
		source, ok := selectPreloadLinkSource(j.item, selected)
		_, fetched := j.fetchedItems[j.item.ID]
		fetched = fetched || j.key != "" && j.item.fetchedKey == j.key
		if !fetched && (!ok && len(j.item.MediaSources) == 0 || ok && source.Path == "" && !completePreloadSource(source)) {
			var value preloadItem
			err := j.getItem(ctx, task, j.item.ID, &value, true)
			sequence = value.sourceSequence
			sourceChanged := err == nil && (value.Type != j.item.Type || value.SeriesID != "" && j.item.SeriesID != "" && value.SeriesID != j.item.SeriesID)
			if err == nil && !sourceChanged {
				if value.MediaSources == nil && value.Path == "" {
					sequence = 0
				}
				value = sequencePreloadItem(value, sequence)
				j.item = mergePreloadItem(value, j.item)
			} else {
				sequence = 0
			}
			newer := j.mergePendingMetadata(task, parentID, sequence)
			if j.kind == playback.PreloadDetail {
				selected = j.request.selected
			}
			task.SetLogTarget(j.item.ID, preloadItemTitle(j.item))
			source, ok = selectPreloadLinkSource(j.item, selected)
			if (err != nil || sourceChanged) && !(newer && ok && playback.ValidExternalURL(source.Path)) {
				if sourceChanged {
					return j.skipped(nil, "source_changed")
				}
				return j.skipped(err, "source_unavailable")
			}
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
			sequence = j.owner.serial.Add(1)
			err := j.fetch(ctx, task, "/Items/"+j.item.ID+"/PlaybackInfo", query, false, &info)
			reason := "source_unavailable"
			value := j.item
			value.Path = ""
			value.MediaSources = info.MediaSources
			valid := err == nil && validPreloadSources(info.MediaSources, j.item.ID)
			if valid {
				source, ok = selectPreloadSource(value, source.ID)
				valid = ok
				if valid {
					j.item = sequencePreloadItem(value, sequence)
				} else {
					reason = "source_changed"
				}
			}
			if !valid {
				sequence = 0
			}
			newer := j.mergePendingMetadata(task, parentID, sequence)
			if newer {
				source, ok = selectPreloadLinkSource(j.item, j.request.selected)
			}
			if !valid && !(newer && ok && playback.ValidExternalURL(source.Path)) {
				return j.skipped(err, reason)
			}
			if !ok {
				return j.skipped(nil, "source_changed")
			}
		}
		if j.kind == playback.PreloadDetail && j.mergePendingMetadata(task, parentID, sequence) {
			source, ok = selectPreloadLinkSource(j.item, j.request.selected)
			if !ok {
				return j.skipped(nil, "source_changed")
			}
		}
		j.mergeCachedItem()
		if j.kind == playback.PreloadDetail {
			selected = j.request.selected
		}
		source, ok = selectPreloadLinkSource(j.item, selected)
		if !ok {
			return j.skipped(nil, "source_changed")
		}
		if !playback.ValidExternalURL(source.Path) {
			return j.skipped(nil, "source_origin_unknown")
		}
		if data := j.item.UserData; j.pendingFirst && data != nil && data.Played != nil && *data.Played {
			return j.skipped(nil, "pending_invalid")
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

// 补查按发起顺序合并，迟到响应只能补齐较新自然资料的缺失字段。
func (j *embyPreloadJob) mergePendingMetadata(task *playback.PreloadTask, parentID string, sequence uint64) bool {
	if j.kind != playback.PreloadDetail {
		return false
	}
	j.owner.mu.Lock()
	defer j.owner.mu.Unlock()
	device := j.owner.devices[j.request.scope]
	if device == nil || !task.Valid() || device.controlVersion != j.request.controlVersion || !preloadControlMatches(device, j.request) {
		return false
	}
	if parentID == "" {
		if device.detail.ID != j.item.ID {
			return false
		}
		newer := device.detailSourceSequence > sequence
		if newer {
			current := device.detail
			current.MediaSources = fillPreloadSources(current.MediaSources, j.item.MediaSources, current.ID)
			j.item = mergePreloadItem(current, j.item)
			j.request.selected = device.selected
		} else {
			j.item = mergePreloadItem(j.item, device.detail)
			device.detailSourceSequence = sequence
		}
		device.detail, device.selected = j.item, j.request.selected
		j.item.sourceSequence = device.detailSourceSequence
		metadata := &preloadPendingList{items: []preloadItem{j.item}}
		device.mergePreloadMetadata(metadata)
		return newer
	}
	if device.detail.ID != parentID || device.pendingParent != parentID || device.pendingTarget.ID != j.item.ID {
		return false
	}
	j.pendingFirst = device.pendingFirst
	newer := device.pendingTarget.sourceSequence > sequence
	j.item.sourceSequence = sequence
	if newer {
		current := device.pendingTarget
		current.MediaSources = fillPreloadSources(current.MediaSources, j.item.MediaSources, current.ID)
		j.item = mergePreloadItem(current, j.item)
		j.request.selected = device.pendingTargetSelected
	} else {
		j.item = mergePreloadItem(j.item, device.pendingTarget)
	}
	device.pendingTarget, device.pendingTargetSelected = j.item, j.request.selected
	metadata := &preloadPendingList{items: []preloadItem{j.item}}
	device.mergePreloadMetadata(metadata)
	return newer
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

func (j *embyPreloadJob) successor(ctx context.Context, task *playback.PreloadTask) (preloadItem, error) {
	current := j.item
	if current.Type == "" {
		var value preloadItem
		if err := j.getItem(ctx, task, current.ID, &value); err != nil {
			return preloadItem{}, err
		}
		value.MediaSources = fillPreloadSources(value.MediaSources, current.MediaSources, value.ID)
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
			value, _ := j.cachedItem(j.nextID)
			if value.ID != "" && value.Type == "" {
				known := value
				known.Type = "Episode"
				if !validPreloadItem(known) || value.SeriesID != "" && value.SeriesID != current.SeriesID {
					return preloadItem{}, errors.New("queue_mismatch")
				}
			}
			if value.ID == "" || value.SeriesID == "" || value.Type == "" {
				if err := j.getItem(ctx, task, j.nextID, &value); err != nil {
					return preloadItem{}, err
				}
			}
			if value.Type != "Episode" || value.SeriesID != current.SeriesID || !validPreloadItem(value) {
				return preloadItem{}, errors.New("queue_mismatch")
			}
			return value, nil
		}
		if value, ok := j.cachedSuccessor(current); ok {
			return value, nil
		}
		query := url.Values{"UserId": {j.request.user}, "StartItemId": {current.ID}, "Limit": {"2"}, "Fields": {preloadItemFields}}
		var result preloadItems
		sequence := j.owner.serial.Add(1)
		if err := j.fetch(ctx, task, "/Shows/"+current.SeriesID+"/Episodes", query, true, &result); err != nil {
			return preloadItem{}, err
		}
		if len(result.Items) != 2 || result.Items[0].ID != current.ID || result.Items[1].ID == current.ID || result.Items[0].SeriesID != current.SeriesID || result.Items[1].SeriesID != current.SeriesID || result.Items[1].Type != "Episode" || !validPreloadItem(result.Items[1]) {
			return preloadItem{}, errors.New("successor_ambiguous")
		}
		return sequencePreloadItem(result.Items[1], sequence), nil
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
	sequence := j.owner.serial.Add(1)
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
			return sequencePreloadItem(chain[index+1], sequence), nil
		}
	}
	return preloadItem{}, errors.New("no_next_part")
}

func (j *embyPreloadJob) getItem(ctx context.Context, task *playback.PreloadTask, id string, value *preloadItem, final ...bool) error {
	if !validPreloadID(id) {
		return errors.New("invalid_item")
	}
	if cached, ok := j.fetchedItems[id]; ok {
		*value = cached
		return nil
	}
	if cached, _ := j.cachedItem(id); j.key != "" && cached.fetchedKey == j.key && validPreloadItem(cached) {
		*value = cached
		return nil
	}
	query := url.Values{"Fields": {preloadItemFields}}
	sequence := j.owner.serial.Add(1)
	if err := j.fetch(ctx, task, "/Users/"+j.request.user+"/Items/"+id, query, true, value, final...); err != nil {
		return err
	}
	if value.ID == id && value.Type == "" && j.item.ID == id {
		value.Type = j.item.Type
	}
	if value.ID != id || !validPreloadItem(*value) {
		return errors.New("invalid_item")
	}
	*value = sequencePreloadItem(*value, sequence)
	value.fetchedKey = j.key
	if j.fetchedItems == nil {
		j.fetchedItems = make(map[string]preloadItem)
	}
	j.fetchedItems[id] = *value
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
		err = task.FinalMetadataGET(ctx, call)
	} else {
		err = task.Metadata(ctx, safeGET, call)
	}
	if err == nil && !task.Valid() {
		return playback.ErrPreloadCanceled
	}
	return err
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
	if len(preloadValues(query, "ParentId")) > 0 {
		p.mu.Lock()
		device := p.devices[request.scope]
		if device == nil || device.detail.ID != request.item || device.detail.Type != "Series" && device.detail.Type != "Season" {
			p.mu.Unlock()
			return
		}
		if device.detail.Type == "Season" {
			query = maps.Clone(query)
			deletePreloadValues(query, "ParentId")
			query.Set("SeasonId", device.detail.ID)
			request.item = device.detail.SeriesID
			if request.item == "" {
				request.item = device.detail.ParentID
			}
		}
		p.mu.Unlock()
	}
	season, ok := preloadUnique(preloadValues(query, "SeasonId"))
	if !ok || season != "" && !validPreloadID(season) {
		return
	}
	var result preloadItems
	if json.Unmarshal(data, &result) != nil || result.Items == nil || len(result.Items) > 256 {
		return
	}
	members := make(map[string]bool, len(result.Items))
	items := make([]preloadItem, 0, len(result.Items))
	for _, value := range result.Items {
		if value.Type != "" && value.Type != "Episode" || value.SeriesID != "" && value.SeriesID != request.item || !validPreloadID(value.ID) {
			continue
		}
		members[value.ID] = true
		value.Type = "Episode"
		items = append(items, sequencePreloadItem(value, request.sequence))
	}
	p.mu.Lock()
	device := p.deviceLocked(request)
	var detail preloadItem
	if device != nil && request.sequence == device.episodesStarted && request.controlVersion == device.controlVersion && preloadControlMatches(device, request) {
		if request.sequence < device.selectionInvalidated {
			for i := range result.Items {
				result.Items[i].UserData = nil
				result.Items[i].userDataSequence = device.selectionInvalidated
			}
			for i := range items {
				items[i].UserData = nil
				items[i].userDataSequence = device.selectionInvalidated
			}
		}
		p.cacheEpisodeListLocked(device, request, query, result)
		changed := device.season != season || !maps.Equal(device.members, members)
		metadata := &preloadPendingList{items: items}
		if time.Since(device.membersAt) <= preloadAssociationTTL {
			metadata = metadata.mergeMetadata(&preloadPendingList{items: device.memberItems})
		}
		if time.Since(device.detailAt) <= preloadAssociationTTL {
			metadata = metadata.mergeMetadata(&preloadPendingList{items: []preloadItem{device.detail}})
		}
		if time.Since(device.pendingTarget.metadataAt) <= preloadAssociationTTL {
			metadata = metadata.mergeMetadata(&preloadPendingList{items: []preloadItem{device.pendingTarget}})
		}
		device.season, device.members, device.membersAt = season, members, time.Now()
		device.memberItems = metadata.items
		device.mergePreloadMetadata(metadata)
		for _, item := range items {
			if strings.EqualFold(item.PlayAccess, "None") && device.playbackRequested == item.ID {
				device.playbackStarted = max(device.playbackStarted, request.sequence)
			}
			if device.detail.Type == "Episode" && device.detail.ID == item.ID && strings.EqualFold(mergePreloadPendingItem(item, device.detail).PlayAccess, "None") {
				device.detail, device.pending = preloadItem{}, preloadPlaybackInfo{}
				device.pendingID, device.pendingSelected, device.selected = "", "", ""
				device.revision++
				device.playing.Active = false
				device.playing.Epoch++
				p.options.Coordinator.CancelScope(request.scope)
			}
			if item.ID != device.pendingTarget.ID {
				continue
			}
			old := device.pendingTarget
			current := item
			if time.Since(old.metadataAt) <= preloadAssociationTTL {
				current = mergePreloadPendingItem(item, old)
			}
			watchChanged := false
			if before, after := old.UserData, current.UserData; device.pendingFirst && after != nil {
				watchChanged = after.Played != nil && *after.Played && (before == nil || before.Played == nil || !*before.Played) ||
					after.PlayCount != nil && *after.PlayCount > 0 && (before == nil || before.PlayCount == nil || *before.PlayCount != *after.PlayCount) ||
					after.PlaybackPositionTicks != nil && *after.PlaybackPositionTicks > 0 && (before == nil || before.PlaybackPositionTicks == nil || *before.PlaybackPositionTicks != *after.PlaybackPositionTicks)
			}
			if validPreloadItem(old) != validPreloadItem(current) || pendingSourceChanged(old, current, device.pendingTargetSelected, device.pendingTargetSelected) || watchChanged {
				device.revision++
				p.options.Coordinator.CancelDetail(request.scope)
				changed = true
			}
			if watchChanged {
				device.pendingLists = [2]*preloadPendingList{}
				device.pendingFirst = false
				device.selectionInvalidated = max(device.selectionInvalidated, request.sequence)
			}
			device.pendingTarget = current
		}
		firstChanged := false
		if list := device.episodeList; device.pendingFirst && list != nil && list.sequence == request.sequence && list.first {
			parent := device.detail
			series, selectedSeason := parent.SeriesID, parent
			if parent.Type == "Series" {
				series = parent.ID
				selectedSeason = preloadItem{ID: device.pendingTarget.SeasonID, IndexNumber: device.pendingTarget.ParentIndexNumber}
			} else if series == "" {
				series = parent.ParentID
			}
			var candidate preloadItem
			known := false
			if list.series == series {
				if parent.Type == "Series" && list.season == "" && list.seasonNumber < 0 {
					candidate, known = firstPreloadEpisode(list, preloadItem{}, true)
					if number, ok := preloadIndex(device.pendingTarget.ParentIndexNumber); ok && number == 0 && candidate.ID == "" {
						known = len(list.items) == 0 || list.complete
						for _, item := range list.items {
							if item.ID == device.pendingTarget.ID {
								known = false
								break
							}
						}
					}
				} else if validPreloadID(selectedSeason.ID) && list.matchesSeason(selectedSeason) {
					candidate, known = firstPreloadEpisode(list, selectedSeason, false)
				}
			}
			if known && candidate.ID != device.pendingTarget.ID {
				// 可靠起点已改变；筛选掉的旧首集可能不会再出现在响应里。
				device.pendingLists = [2]*preloadPendingList{}
				device.pendingFirst = false
				device.selectionInvalidated = max(device.selectionInvalidated, request.sequence)
				device.revision++
				p.options.Coordinator.CancelDetail(request.scope)
				changed, firstChanged = true, true
			}
		}
		if changed && (firstChanged || device.detail.Type == "Series" && device.detail.ID == request.item || device.detail.Type == "Season" && device.detail.ID == season) {
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
