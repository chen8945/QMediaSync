package emby

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/playback"
)

type preloadPendingList struct {
	parent       string
	series       string
	items        []preloadItem
	at           time.Time
	sequence     uint64
	episodesOnly bool
}

func pendingListIndex(kind string) int {
	if kind == "nextup" {
		return 1
	}
	return 0
}

// 分页或筛选后的空列表不能否定整个剧集；非空列表仍可提供候选。
func partialPreloadPendingQuery(query url.Values) bool {
	for name, values := range query {
		name = strings.ToLower(name)
		if strings.HasPrefix(name, "x-emby-") {
			continue
		}
		for _, value := range values {
			if value == "" {
				continue
			}
			switch name {
			case "parentid", "seriesid", "userid", "api_key", "deviceid", "fields", "enableimages", "imagetypelimit", "enableimagetypes", "enableuserdata", "enabletotalrecordcount", "sortby", "sortorder":
				continue
			case "startindex", "limit":
				n, err := strconv.Atoi(value)
				if err == nil && (name == "startindex" && n == 0 || name == "limit" && n > 0) {
					continue
				}
			case "recursive":
				if strings.EqualFold(value, "true") {
					continue
				}
			case "includeitemtypes":
				for _, kind := range strings.Split(value, ",") {
					if strings.EqualFold(strings.TrimSpace(kind), "Episode") {
						value = ""
						break
					}
				}
				if value == "" {
					continue
				}
			}
			return true
		}
	}
	return false
}

func (l *preloadPendingList) scoped(parent preloadItem) bool {
	if l == nil || parent.ID == "" {
		return false
	}
	if l.parent != "" {
		return l.parent == parent.ID || parent.Type == "Season" && len(l.items) == 0 &&
			(l.parent == parent.SeriesID || l.parent == parent.ParentID)
	}
	return parent.Type == "Series" && l.series == parent.ID ||
		parent.Type == "Season" && len(l.items) == 0 && l.series != "" && (l.series == parent.SeriesID || l.series == parent.ParentID)
}

// 查询范围可证明缺失的归属字段；明确的跨剧信息不能由范围覆盖。
func (l *preloadPendingList) candidate(parent preloadItem, members map[string]bool) (preloadItem, bool) {
	if l == nil {
		return preloadItem{}, false
	}
	series := parent.ID
	if parent.Type == "Season" {
		series = parent.SeriesID
		if series == "" {
			series = parent.ParentID
		}
	}
	if l.series != "" && series != "" && l.series != series {
		return preloadItem{}, false
	}
	for _, item := range l.items {
		if item.sourceSequence == 0 {
			item = sequencePreloadItem(item, l.sequence)
		}
		seriesProof := series != "" && (item.SeriesID == series || l.series == series || l.parent == series)
		seasonProof := parent.Type == "Season" && (item.SeasonID == parent.ID || item.ParentID == parent.ID || members[item.ID] || l.parent == parent.ID)
		if item.Type == "" && (l.episodesOnly || seriesProof || seasonProof) {
			item.Type = "Episode"
		}
		if item.Type != "Episode" || !validPreloadItem(item) || series != "" && item.SeriesID != "" && item.SeriesID != series {
			continue
		}
		if parent.Type == "Series" && !seriesProof || parent.Type == "Season" && !seasonProof && !seriesProof {
			continue
		}
		if item.SeriesID == "" && (seriesProof || seasonProof) {
			item.SeriesID = series
		}
		return item, parent.Type == "Series" || seasonProof
	}
	return preloadItem{}, false
}

func mergePreloadPendingItem(item, old preloadItem) preloadItem {
	if old.sourceSequence > item.sourceSequence {
		item, old = old, item
	}
	if strings.EqualFold(old.PlayAccess, "None") || old.IsLive || old.IsInfiniteStream {
		old.Path, old.MediaSources = "", nil
		old.pathSequence = 0
	}
	if len(item.MediaSources) > 0 {
		item.MediaSources = fillPreloadSources(item.MediaSources, old.MediaSources, item.ID)
	}
	return mergePreloadItem(item, old)
}

// 补入的地址和观看状态保留原始顺序，不升级为新响应的证据。
func sequencePreloadItem(item preloadItem, sequence uint64) preloadItem {
	item.metadataAt = time.Now()
	item.sourceSequence = sequence
	if item.UserData != nil && item.userDataSequence == 0 {
		item.userDataSequence = sequence
	}
	item.pathSequence = 0
	if item.Path != "" {
		item.pathSequence = sequence
	}
	if item.MediaSources != nil {
		item.MediaSources = slices.Clone(item.MediaSources)
		for index := range item.MediaSources {
			item.MediaSources[index].pathSequence = 0
			if item.MediaSources[index].Path != "" {
				item.MediaSources[index].pathSequence = sequence
			}
		}
	}
	return item
}

// 同条目资料跨列表保留，选集顺序、成员和查询范围仍由各自列表决定。
func (l *preloadPendingList) mergeMetadata(other *preloadPendingList) *preloadPendingList {
	if l == nil || other == nil {
		return l
	}
	var items []preloadItem
	for _, item := range other.items {
		for index, old := range l.items {
			if old.ID != item.ID {
				continue
			}
			if item.sourceSequence == 0 {
				item = sequencePreloadItem(item, other.sequence)
			}
			if old.sourceSequence == 0 {
				old = sequencePreloadItem(old, l.sequence)
			}
			if items == nil {
				items = append([]preloadItem(nil), l.items...)
			}
			items[index] = mergePreloadPendingItem(old, item)
		}
	}
	if items == nil {
		return l
	}
	copy := *l
	copy.items = items
	return &copy
}

// 同条目证据同步到已有成员，不改变列表的选集范围和有效期。
func (d *preloadDevice) mergePreloadMetadata(metadata *preloadPendingList) {
	members := &preloadPendingList{items: d.memberItems}
	for index, list := range d.pendingLists {
		d.pendingLists[index] = list.mergeMetadata(metadata)
		if list != nil && time.Since(list.at) <= preloadAssociationTTL {
			members = members.mergeMetadata(d.pendingLists[index])
		}
	}
	d.memberItems = members.mergeMetadata(metadata).items
	d.episodeList = d.episodeList.mergeMetadata(metadata)
	d.seasonList = d.seasonList.mergeMetadata(metadata)
}

func (p *embyPreloader) observePending(request preloadRequest, data []byte) {
	var result preloadItems
	if json.Unmarshal(data, &result) != nil || result.Items == nil || len(result.Items) > 256 {
		return
	}
	if len(result.Items) == 0 && request.pendingPartial {
		return
	}
	index := pendingListIndex(request.pendingKind)
	p.mu.Lock()
	device := p.devices[request.scope]
	if device == nil || request.user == "" || request.user != device.user || request.sequence != device.pendingStarted[index] || request.sequence < device.selectionInvalidated || request.controlVersion != device.controlVersion || !preloadControlMatches(device, request) {
		p.mu.Unlock()
		return
	}
	list := &preloadPendingList{parent: request.parent, series: request.series, items: result.Items, at: time.Now(), sequence: request.sequence, episodesOnly: request.pendingKind == "nextup"}
	previous := device.pendingLists[index]
	if len(list.items) == 0 && previous != nil && time.Since(previous.at) <= preloadAssociationTTL &&
		(previous.parent != "" || previous.series != "") &&
		(previous.parent != list.parent || previous.series != list.series) && !list.scoped(device.detail) {
		p.mu.Unlock()
		return
	}
	if other := device.pendingLists[1-index]; other != nil && other.sequence > request.sequence && other.scoped(device.detail) && (len(other.items) > 0 || len(list.items) > 0) {
		candidate, _ := list.candidate(device.detail, device.members)
		if list.scoped(device.detail) || candidate.ID != "" {
			p.mu.Unlock()
			return
		}
	}
	if device.pendingParent == device.detail.ID && device.pendingTargetSequence > request.sequence {
		candidate, _ := list.candidate(device.detail, device.members)
		if candidate.ID != device.pendingTarget.ID {
			p.mu.Unlock()
			return
		}
	}
	// 先合并跨列表的最新资料，再补当前响应省略的字段。
	prior := device.pendingLists
	for i, item := range list.items {
		item = sequencePreloadItem(item, request.sequence)
		var latest preloadItem
		for _, priorList := range prior {
			if priorList == nil || time.Since(priorList.at) > preloadAssociationTTL {
				continue
			}
			for _, old := range priorList.items {
				if old.ID == item.ID {
					if old.sourceSequence == 0 {
						old = sequencePreloadItem(old, priorList.sequence)
					}
					latest = mergePreloadPendingItem(old, latest)
					break
				}
			}
		}
		if time.Since(device.membersAt) <= preloadAssociationTTL {
			for _, member := range device.memberItems {
				if member.ID == item.ID {
					latest = mergePreloadPendingItem(member, latest)
					break
				}
			}
		}
		list.items[i] = mergePreloadPendingItem(item, latest)
	}
	device.pendingLists[index] = list
	device.mergePreloadMetadata(list)
	parent := device.detail

	candidate, _ := list.candidate(parent, device.members)
	applicable := candidate.ID != "" || list.scoped(parent)
	if !applicable && device.pendingParent == parent.ID && device.pendingTarget.ID != "" {
		for _, item := range list.items {
			if item.ID == device.pendingTarget.ID {
				applicable = true
				break
			}
		}
	}
	if !applicable || (parent.Type != "Series" && parent.Type != "Season") || device.detailRequested != parent.ID || time.Since(device.detailAt) > preloadAssociationTTL {
		p.mu.Unlock()
		return
	}
	if prior := device.pendingLists[1-index]; prior != nil && prior.sequence < list.sequence && len(prior.items) > 0 {
		priorCandidate, _ := prior.candidate(parent, device.members)
		if prior.scoped(parent) || priorCandidate.ID != "" {
			device.pendingLists[1-index] = nil
		}
	}
	old := device.pendingTarget
	selectionSequence := request.sequence
	keepFirst := device.pendingFirst && device.pendingParent == parent.ID && validPreloadItem(old) && len(list.items) == 0 && device.detailResponded == device.detailStarted
	if keepFirst {
		candidate, selectionSequence = old, device.pendingTargetSequence
	}
	other := device.pendingLists[1-index]
	sameEmpty := previous != nil && len(previous.items) == 0 && len(list.items) == 0 &&
		previous.parent == list.parent && previous.series == list.series && time.Since(previous.at) <= preloadAssociationTTL &&
		other != nil && len(other.items) == 0 && other.scoped(parent) && time.Since(other.at) <= preloadAssociationTTL &&
		device.detailResponded == device.detailStarted
	if device.pendingParent == parent.ID && candidate.ID != "" && candidate.ID == old.ID {
		if time.Since(old.metadataAt) <= preloadAssociationTTL {
			candidate = mergePreloadPendingItem(candidate, old)
		}
		selectionSequence = max(selectionSequence, device.pendingTargetSequence)
	}
	if device.pendingParent != parent.ID && !sameEmpty || old.ID != candidate.ID || candidate.ID == "" && !sameEmpty || pendingSourceChanged(old, candidate, device.pendingTargetSelected, device.pendingTargetSelected) {
		device.revision++
		p.options.Coordinator.CancelDetail(request.scope)
	}
	if old.ID != candidate.ID {
		device.pendingTargetSelected = ""
	}
	if !keepFirst {
		device.pendingFirst = false
	}
	device.pendingTarget, device.pendingParent, device.pendingTargetSequence = candidate, parent.ID, selectionSequence
	request.item, request.detailSequence = parent.ID, device.detailStarted
	request.fromPlayback, request.enriched = true, true
	detailReady := device.detailResponded == device.detailStarted
	p.mu.Unlock()
	if detailReady {
		p.observeDetail(request, parent)
	}
}

func pendingSourceChanged(old, current preloadItem, selected ...string) bool {
	previous, next := "", ""
	if len(selected) == 2 {
		previous, next = selected[0], selected[1]
	}
	a, aOK := selectPreloadLinkSource(old, previous)
	b, bOK := selectPreloadLinkSource(current, next)
	return aOK && playback.ValidExternalURL(a.Path) && (current.MediaSources != nil || current.Path != "") && (!bOK || b.Path != a.Path)
}

func (j *embyPreloadJob) naturalPending(parent preloadItem) [2]*preloadPendingList {
	j.owner.mu.Lock()
	defer j.owner.mu.Unlock()
	var lists [2]*preloadPendingList
	device := j.owner.devices[j.request.scope]
	if device == nil || !preloadControlMatches(device, j.request) || device.user != j.request.user {
		return lists
	}
	for i, list := range device.pendingLists {
		if list != nil && time.Since(list.at) <= preloadAssociationTTL {
			lists[i] = list
			if time.Since(device.membersAt) <= preloadAssociationTTL {
				lists[i] = lists[i].mergeMetadata(&preloadPendingList{items: device.memberItems})
			}
		}
	}
	for i, list := range lists {
		lists[i] = list.mergeMetadata(lists[1-i])
	}
	for i, list := range lists {
		if list == nil {
			continue
		}
		candidate, _ := list.candidate(parent, j.members)
		if list.scoped(parent) || candidate.ID != "" {
			other := 1 - i
			if prior := lists[other]; prior != nil && prior.sequence < list.sequence && len(prior.items) > 0 {
				priorCandidate, _ := prior.candidate(parent, j.members)
				if prior.scoped(parent) || priorCandidate.ID != "" {
					lists[other] = nil
				}
			}
		}
	}
	return lists
}

func (j *embyPreloadJob) pendingEpisode(ctx context.Context, task *playback.PreloadTask) (preloadItem, error) {
	parent := j.item
	series := parent.ID
	if parent.Type == "Season" {
		series = parent.SeriesID
		if series == "" {
			series = parent.ParentID
		}
	}
	lists := j.naturalPending(parent)
	ordered := lists
	if lists[1] != nil && (lists[0] == nil || lists[1].sequence > lists[0].sequence) {
		ordered[0], ordered[1] = lists[1], lists[0]
	}
	var value preloadItem
	var selectedList *preloadPendingList
	confirmed := false
	first := false
	var sequence uint64
	for _, list := range ordered {
		if candidate, scoped := list.candidate(parent, j.members); candidate.ID != "" {
			value, confirmed, sequence = candidate, scoped, list.sequence
			selectedList = list
			break
		}
	}
	episodes, _ := j.episodeLists()
	if value.ID == "" && !episodes.neverPlayed(parent) && !(parent.Type == "Series" && episodes.emptySeries(series) && episodes.complete) {
		resume := lists[0]
		if !resume.scoped(parent) {
			var result preloadItems
			sequence = j.owner.serial.Add(1)
			query := url.Values{"ParentId": {parent.ID}, "IncludeItemTypes": {"Episode"}, "Limit": {"1"}, "Fields": {preloadItemFields}, "EnableTotalRecordCount": {"false"}}
			err := j.fetch(ctx, task, "/Users/"+j.request.user+"/Items/Resume", query, true, &result)
			if err != nil && j.metadataHTTP != http.StatusNotFound && j.metadataHTTP != http.StatusMethodNotAllowed {
				return preloadItem{}, err
			}
			if err == nil && result.Items == nil {
				return preloadItem{}, errors.New("pending_invalid")
			}
			resume = &preloadPendingList{parent: parent.ID, items: result.Items, episodesOnly: true, sequence: sequence}
			if err == nil {
				j.cachePending(parent, 0, resume)
			}
		}
		value, confirmed = resume.candidate(parent, j.members)
		selectedList = resume
		sequence = resume.sequence
		episodes, _ = j.episodeLists()
		if value.ID == "" && !episodes.neverPlayed(parent) && !(parent.Type == "Series" && episodes.emptySeries(series) && episodes.complete) {
			if !validPreloadID(series) {
				return preloadItem{}, errors.New("pending_scope_unknown")
			}
			latest := j.naturalPending(parent)
			next := latest[1]
			if !next.scoped(parent) {
				var result preloadItems
				sequence = j.owner.serial.Add(1)
				query := url.Values{"SeriesId": {series}, "Limit": {"1"}, "Fields": {preloadItemFields}, "EnableTotalRecordCount": {"false"}}
				if err := j.fetch(ctx, task, "/Shows/NextUp", query, true, &result); err != nil {
					return preloadItem{}, err
				}
				if result.Items == nil {
					return preloadItem{}, errors.New("pending_invalid")
				}
				next = &preloadPendingList{series: series, items: result.Items, episodesOnly: true, sequence: sequence}
				j.cachePending(parent, 1, next)
			}
			value, confirmed = next.candidate(parent, j.members)
			selectedList = next
			sequence = next.sequence
		}
	}
	episodes, _ = j.episodeLists()
	if parent.Type == "Series" && episodes.emptySeries(series) && episodes.complete && (value.ID == "" || episodes.sequence > sequence) {
		return preloadItem{}, errors.New("pending_empty")
	}
	selectionSequence := sequence
	if value.sourceSequence != 0 {
		sequence = value.sourceSequence
	}
	if value.ID != "" && !confirmed {
		query := url.Values{"SeasonId": {parent.ID}, "StartItemId": {value.ID}, "Limit": {"1"}, "Fields": {preloadItemFields}}
		var scoped preloadItems
		source, known := selectPreloadLinkSource(value, "")
		sequence = j.owner.serial.Add(1)
		if err := j.fetch(ctx, task, "/Shows/"+series+"/Episodes", query, true, &scoped, known && playback.ValidExternalURL(source.Path)); err != nil {
			return preloadItem{}, err
		}
		if scoped.Items == nil {
			return preloadItem{}, errors.New("pending_invalid")
		}
		for i := range scoped.Items {
			scoped.Items[i] = sequencePreloadItem(scoped.Items[i], sequence)
		}
		list := &preloadPendingList{parent: parent.ID, series: series, items: scoped.Items, sequence: sequence}
		found, valid := list.candidate(parent, j.members)
		for _, latest := range j.naturalPending(parent) {
			if latest == nil {
				continue
			}
			current, confirmed := latest.candidate(parent, j.members)
			if confirmed && current.ID == value.ID && current.sourceSequence > sequence {
				if found.ID == current.ID {
					current.MediaSources = fillPreloadSources(current.MediaSources, found.MediaSources, current.ID)
					current = mergePreloadItem(current, found)
				}
				found, valid, sequence = current, true, current.sourceSequence
				selectionSequence = max(selectionSequence, latest.sequence)
			}
		}
		if valid && found.ID == value.ID {
			value = mergePreloadPendingItem(found, value)
		} else {
			// 首项未列入当前季时，继续使用原列表中顺序靠后的已确认项。
			for index, item := range selectedList.items {
				if item.ID == value.ID {
					rest := *selectedList
					rest.items = append([]preloadItem(nil), selectedList.items[index+1:]...)
					for i, old := range rest.items {
						for _, current := range scoped.Items {
							if current.ID == old.ID {
								rest.items[i] = mergePreloadPendingItem(current, old)
								break
							}
						}
					}
					value, confirmed = rest.candidate(parent, j.members)
					break
				}
			}
			if !confirmed || value.ID == "" {
				if value.ID != "" || len(scoped.Items) != 0 && !valid {
					return preloadItem{}, errors.New("pending_scope_unknown")
				}
				value = preloadItem{}
			}
		}
	}
	if value.ID == "" {
		var err error
		value, sequence, err = j.firstEpisode(ctx, task, parent, series)
		if err != nil {
			// 列表证据可能先失效，协调器仍保留旧作业，须按新季资料重选。
			if errors.Is(err, playback.ErrPreloadCanceled) && task.Valid() {
				j.owner.mu.Lock()
				device := j.owner.devices[j.request.scope]
				if device != nil && device.user == j.request.user && device.detail.ID == parent.ID && device.controlVersion == j.request.controlVersion && preloadControlMatches(device, j.request) {
					seasons := device.seasonList
					if seasons != nil && seasons.series == series && device.selectionInvalidated > 0 && seasons.sequence >= device.selectionInvalidated && time.Since(seasons.at) <= preloadAssociationTTL && task.Valid() {
						request, detail := j.invalidateFirstEpisodeLocked(device)
						j.owner.mu.Unlock()
						j.owner.observeDetail(request, detail)
						return preloadItem{}, playback.ErrPreloadCanceled
					}
				}
				j.owner.mu.Unlock()
			}
			return preloadItem{}, err
		}
		first = true
		selectionSequence = sequence
		if value.sourceSequence != 0 {
			sequence = value.sourceSequence
		}
	}
	j.request.selected = ""
	j.owner.mu.Lock()
	if device := j.owner.devices[j.request.scope]; device != nil && task.Valid() && device.detail.ID == parent.ID && device.controlVersion == j.request.controlVersion && preloadControlMatches(device, j.request) {
		if first && device.detail.Type == "Season" && device.detail.UserData != nil && device.detail.UserData.Played != nil && *device.detail.UserData.Played {
			j.owner.mu.Unlock()
			return preloadItem{}, errors.New("pending_empty")
		}
		if first && firstPreloadSeasonChanged(device.seasonList, parent, value, selectionSequence) {
			request, detail := j.invalidateFirstEpisodeLocked(device)
			j.owner.mu.Unlock()
			j.owner.observeDetail(request, detail)
			return preloadItem{}, playback.ErrPreloadCanceled
		}
		metadataLists := [3]*preloadPendingList{device.pendingLists[0], device.pendingLists[1], {items: device.memberItems, at: device.membersAt}}
		for _, list := range metadataLists {
			if list == nil || time.Since(list.at) > preloadAssociationTTL {
				continue
			}
			for _, item := range list.items {
				if item.ID == value.ID {
					if item.sourceSequence == 0 {
						item = sequencePreloadItem(item, list.sequence)
					}
					if item.sourceSequence <= sequence {
						break
					}
					if item.Type == "" {
						item.Type = value.Type
					}
					value = mergePreloadPendingItem(item, value)
					sequence = value.sourceSequence
					break
				}
			}
		}
		if value.Type != "Episode" || !validPreloadItem(value) || series != "" && value.SeriesID != "" && value.SeriesID != series {
			j.owner.mu.Unlock()
			return preloadItem{}, errors.New("pending_invalid")
		}
		if device.pendingParent == parent.ID && device.pendingTarget.ID == value.ID {
			selectionSequence = max(selectionSequence, device.pendingTargetSequence)
			if device.pendingTarget.sourceSequence > sequence {
				current := device.pendingTarget
				current.MediaSources = fillPreloadSources(current.MediaSources, value.MediaSources, current.ID)
				value = mergePreloadItem(current, value)
				sequence = current.sourceSequence
				j.request.selected = device.pendingTargetSelected
			}
		}
		if device.pendingID == value.ID && device.pendingSequence == device.playbackStarted && time.Since(device.pendingAt) <= preloadAssociationTTL {
			if device.pendingSequence >= sequence {
				sequence = device.pendingSequence
				value.Path = ""
				value.MediaSources = fillPreloadSources(device.pending.MediaSources, value.MediaSources, value.ID)
			} else {
				value.MediaSources = fillPreloadSources(value.MediaSources, device.pending.MediaSources, value.ID)
			}
			j.request.selected = device.pendingSelected
		}
		value.sourceSequence = sequence
		device.pendingTarget, device.pendingParent, device.pendingTargetSequence, device.pendingTargetSelected = value, parent.ID, selectionSequence, j.request.selected
		device.pendingFirst = first
		metadata := &preloadPendingList{items: []preloadItem{value}}
		device.mergePreloadMetadata(metadata)
	}
	j.owner.mu.Unlock()
	return value, nil
}

func (j *embyPreloadJob) invalidateFirstEpisodeLocked(device *preloadDevice) (preloadRequest, preloadItem) {
	seasons := device.seasonList
	device.invalidatePreloadSelection(seasons.sequence)
	device.revision++
	j.owner.options.Coordinator.CancelDetail(j.request.scope)
	request := j.request
	request.sequence, request.detailSequence = seasons.sequence, device.detailStarted
	request.item, request.fromPlayback, request.fromEpisodes, request.fromSeasons = device.detail.ID, true, false, true
	request.pendingKind, request.enriched = "", true
	return request, device.detail
}

// 保留原始续播筛选能力；待播预加载由 Resume 和 NextUp 确定候选。
func (j *embyPreloadJob) resumableItems(ctx context.Context, task *playback.PreloadTask, parent string) (preloadItems, error) {
	var result preloadItems
	query := url.Values{"ParentId": {parent}, "Recursive": {"true"}, "Filters": {"IsResumable"}, "IncludeItemTypes": {"Episode"}, "Limit": {"1"}, "Fields": {preloadItemFields}}
	err := j.fetch(ctx, task, "/Users/"+j.request.user+"/Items", query, true, &result)
	return result, err
}

// 后台取链只需要明确的地址；真实播放的源和会话关联仍使用严格选源。
func selectPreloadLinkSource(item preloadItem, selected string) (preloadSource, bool) {
	if !validPreloadItem(item) {
		return preloadSource{}, false
	}
	var candidates []preloadSource
	var incomplete []preloadSource
	otherVersions := 0
	for _, source := range item.MediaSources {
		// 合并组的其他条目仍证明多版本；不把过滤后的当前条目误当唯一源。
		if selected == "" && source.ItemID != "" && source.ItemID != item.ID {
			return preloadSource{}, false
		}
		if len(source.ID) > 256 || len(source.Path) > 16<<10 || source.ItemID != "" && source.ItemID != item.ID || !eligiblePreloadSource(source) || selected != "" && source.ID != selected {
			continue
		}
		if playback.ValidExternalURL(source.Path) {
			candidates = append(candidates, source)
		} else if source.Path == "" && source.ID != "" {
			incomplete = append(incomplete, source)
		} else if source.Path != "" && source.ID != "" {
			otherVersions++
		}
	}
	if len(candidates) == 1 && otherVersions == 0 {
		return candidates[0], true
	}
	if len(candidates) == 0 && len(incomplete) == 1 && (selected != "" || len(item.MediaSources) == 1) {
		source := incomplete[0]
		if len(item.MediaSources) == 1 && playback.ValidExternalURL(item.Path) {
			source.Path = item.Path
		}
		return source, true
	}
	if selected == "" && (len(item.MediaSources) == 0 || len(item.MediaSources) == 1 && item.MediaSources[0].ID == "" && item.MediaSources[0].Path == "" && (item.MediaSources[0].ItemID == "" || item.MediaSources[0].ItemID == item.ID) && eligiblePreloadSource(item.MediaSources[0])) && playback.ValidExternalURL(item.Path) {
		return preloadSource{Path: item.Path}, true
	}
	return preloadSource{}, false
}

func pendingReason(err error) string {
	if err != nil && strings.HasPrefix(err.Error(), "pending_") {
		return err.Error()
	}
	return "relation_unavailable"
}
