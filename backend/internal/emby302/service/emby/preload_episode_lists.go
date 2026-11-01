package emby

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/playback"
)

// 快照只保留最近一个有范围与顺序证据的列表，不因读取续期。
type preloadEpisodeList struct {
	series, season                        string
	items                                 []preloadItem
	at                                    time.Time
	sequence                              uint64
	seasonNumber                          int
	normalOnly                            bool
	first, unplayed, continuous, complete bool
}

func preloadIndex(value any) (int, bool) {
	switch n := value.(type) {
	case float64:
		if n >= 0 && n <= 100000 && float64(int(n)) == n {
			return int(n), true
		}
	case int:
		if n >= 0 && n <= 100000 {
			return n, true
		}
	}
	return 0, false
}

func newPreloadEpisodeList(request preloadRequest, query url.Values, result preloadItems, seasons bool) *preloadEpisodeList {
	if result.Items == nil || len(result.Items) > 256 {
		return nil
	}
	list := &preloadEpisodeList{series: request.item, at: time.Now(), sequence: request.sequence, seasonNumber: -1, first: true, continuous: true}
	sortBy := ""
	recursive := false
	for key, values := range query {
		if _, ok := preloadUnique(preloadValues(query, key)); !ok {
			return nil
		}
		for _, value := range values {
			if value == "" {
				continue
			}
			switch strings.ToLower(key) {
			case "userid", "api_key", "deviceid", "fields", "enableimages", "imagetypelimit", "enableimagetypes", "enableuserdata", "enabletotalrecordcount":
			case "includeitemtypes":
				if seasons && !strings.EqualFold(value, "Season") || !seasons && !strings.EqualFold(value, "Episode") {
					return nil
				}
			case "seasonid":
				if list.season != "" && list.season != value || !validPreloadID(value) {
					return nil
				}
				list.season = value
			case "parentid":
				if value != request.item {
					return nil
				}
			case "sortby":
				if !strings.EqualFold(value, "IndexNumber") && !strings.EqualFold(value, "ParentIndexNumber,IndexNumber") {
					if !seasons {
						return nil
					}
					list.first, list.continuous = false, false
				}
				sortBy = value
			case "sortorder":
				if !strings.EqualFold(value, "Ascending") {
					if !seasons || !strings.EqualFold(value, "Descending") {
						return nil
					}
					list.first, list.continuous = false, false
				}
			case "startindex":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return nil
				}
				list.first = list.first && n == 0
			case "startitemid":
				if !validPreloadID(value) {
					return nil
				}
				list.first = false
			case "limit":
				n, err := strconv.Atoi(value)
				if err != nil || n <= 0 {
					return nil
				}
			case "isplayed":
				if strings.EqualFold(value, "false") {
					list.unplayed, list.continuous = true, false
				} else if seasons && strings.EqualFold(value, "true") {
					list.first, list.continuous = false, false
				} else {
					return nil
				}
			case "filters":
				if !strings.EqualFold(value, "IsUnplayed") {
					return nil
				}
				list.unplayed, list.continuous = true, false
			case "isspecialseason":
				if !seasons || !strings.EqualFold(value, "false") {
					return nil
				}
				list.normalOnly = true
			case "parentindexnumber":
				n, err := strconv.Atoi(value)
				if err != nil || n < 0 {
					return nil
				}
				list.seasonNumber, list.continuous = n, false
			case "recursive":
				if !strings.EqualFold(value, "true") {
					return nil
				}
				recursive = true
			default:
				if !strings.HasPrefix(strings.ToLower(key), "x-emby-") {
					return nil
				}
			}
		}
	}
	if request.fromItems && (sortBy == "" || !seasons && list.season == "" && !recursive) || !seasons && list.season == "" && strings.EqualFold(sortBy, "IndexNumber") {
		return nil
	}
	list.complete = list.first && list.continuous && result.TotalRecordCount == len(result.Items)
	list.items = make([]preloadItem, len(result.Items))
	seen := make(map[string]bool, len(result.Items))
	for i, value := range result.Items {
		if !validPreloadID(value.ID) || seen[value.ID] {
			return nil
		}
		seen[value.ID] = true
		if seasons {
			if value.Type != "Season" || value.SeriesID != "" && value.SeriesID != list.series || value.ParentID != "" && value.ParentID != list.series {
				return nil
			}
			value.SeriesID = list.series
		} else {
			if value.Type != "" && value.Type != "Episode" || value.SeriesID != "" && value.SeriesID != list.series {
				return nil
			}
			if list.season != "" {
				if value.SeasonID != "" && value.SeasonID != list.season || value.ParentID != "" && value.ParentID != list.season {
					_, numberKnown := preloadIndex(value.ParentIndexNumber)
					// 物理父目录可能直接是剧，逻辑季仍由 SeasonId 和季号核验。
					if value.SeriesID != list.series || !numberKnown || value.ParentID != "" && value.ParentID != value.SeasonID && value.ParentID != list.series {
						return nil
					}
				}
				if value.SeasonID == "" {
					value.SeasonID = list.season
				}
			}
			if list.seasonNumber >= 0 {
				number, ok := preloadIndex(value.ParentIndexNumber)
				if value.ParentIndexNumber != nil && (!ok || number != list.seasonNumber) {
					return nil
				}
				value.ParentIndexNumber = list.seasonNumber
			}
			value.Type, value.SeriesID = "Episode", list.series
		}
		list.items[i] = sequencePreloadItem(value, request.sequence)
	}
	return list
}

func (p *embyPreloader) cacheEpisodeListLocked(device *preloadDevice, request preloadRequest, query url.Values, result preloadItems) {
	// 通用 Items 只有当前父页能准确证明 Series/Season 范围时才作为顺序证据。
	if parent := preloadValues(query, "ParentId"); len(parent) > 0 {
		if device.detail.ID != request.item {
			return
		}
		if device.detail.Type == "Season" {
			query = maps.Clone(query)
			query.Set("SeasonId", device.detail.ID)
			deletePreloadValues(query, "ParentId")
			request.item = device.detail.SeriesID
			if request.item == "" {
				request.item = device.detail.ParentID
			}
		} else if device.detail.Type != "Series" {
			return
		}
	}
	if !validPreloadID(request.item) {
		return
	}
	list := newPreloadEpisodeList(request, query, result, false)
	if list != nil && request.sequence < device.selectionInvalidated {
		list.first, list.complete = false, false
	}
	if list != nil && (device.episodeList == nil || device.episodeList.sequence <= list.sequence) {
		device.episodeList = list
	}
}

func (p *embyPreloader) observeSeasons(request preloadRequest, query url.Values, data []byte) {
	var result preloadItems
	if json.Unmarshal(data, &result) != nil {
		return
	}
	list := newPreloadEpisodeList(request, query, result, true)
	if list == nil {
		return
	}
	p.mu.Lock()
	device := p.devices[request.scope]
	var detail preloadItem
	if device != nil && device.user != "" && request.sequence == device.seasonsStarted && request.sequence >= device.selectionInvalidated && request.controlVersion == device.controlVersion && preloadControlMatches(device, request) && (device.seasonList == nil || request.sequence >= device.seasonList.sequence) {
		relatedSeries := device.detail.Type == "Series" && device.detail.ID == request.item
		relatedSeason := device.detail.Type == "Season" && (device.detail.SeriesID == request.item || device.detail.ParentID == request.item)
		firstChanged := device.observeSeasonSelection(list, device.detail)
		if firstChanged {
			p.options.Coordinator.CancelDetail(request.scope)
		}
		device.seasonList = list
		if (relatedSeries || firstChanged && relatedSeason) && time.Since(device.detailAt) <= preloadAssociationTTL {
			detail = device.detail
			request.user, request.detailSequence = device.user, device.detailStarted
		}
	}
	p.mu.Unlock()
	if detail.ID != "" {
		request.item, request.fromPlayback, request.fromSeasons, request.enriched = detail.ID, true, true, true
		p.observeDetail(request, detail)
	}
}

func (device *preloadDevice) observeSeasonSelection(list *preloadEpisodeList, parent preloadItem) bool {
	if episodes := device.episodeList; episodes != nil && episodes.series == list.series && episodes.first && list.sequence > episodes.sequence {
		// 观看资料按明确季归属失效，不要求它同时证明正片排序。
		first, _ := firstPreloadEpisode(episodes, preloadItem{}, false)
		number, numberKnown := preloadIndex(first.ParentIndexNumber)
		for _, season := range list.items {
			current, known := preloadIndex(season.IndexNumber)
			if first.ID != "" && (first.SeasonID == season.ID || numberKnown && known && number == current) && season.UserData != nil && season.UserData.Played != nil && *season.UserData.Played {
				device.invalidatePreloadWatchState(list.sequence)
				break
			}
		}
	}
	if !device.pendingFirst || !firstPreloadSeasonChanged(list, parent, device.pendingTarget, device.pendingTargetSequence) {
		return false
	}
	device.invalidatePreloadSelection(list.sequence)
	device.revision++
	return true
}

func firstPreloadSeasonChanged(list *preloadEpisodeList, parent, selected preloadItem, sequence uint64) bool {
	if list == nil || list.sequence <= sequence || time.Since(list.at) > preloadAssociationTTL {
		return false
	}
	series := parent.ID
	selectedSeason := selected.SeasonID
	selectedNumber, numberKnown := preloadIndex(selected.ParentIndexNumber)
	if parent.Type == "Season" {
		series = parent.SeriesID
		if series == "" {
			series = parent.ParentID
		}
		selectedSeason = parent.ID
		if !numberKnown {
			selectedNumber, numberKnown = preloadIndex(parent.IndexNumber)
		}
	}
	if list.series != series {
		return false
	}
	for _, season := range list.items {
		number, known := preloadIndex(season.IndexNumber)
		if (season.ID == selectedSeason || numberKnown && known && number == selectedNumber) && season.UserData != nil && season.UserData.Played != nil && *season.UserData.Played {
			return true
		}
	}
	if parent.Type != "Series" {
		return false
	}
	season, known := firstPreloadSeason(list, series)
	if special, onlySpecials := specialPreloadSeason(list, series); season.ID == "" && onlySpecials {
		season, known = special, true
	}
	number, nextKnown := preloadIndex(season.IndexNumber)
	return known && (season.ID == "" || season.ID != selectedSeason && (!numberKnown || !nextKnown || number != selectedNumber))
}

func (j *embyPreloadJob) episodeLists() (*preloadEpisodeList, *preloadEpisodeList) {
	j.owner.mu.Lock()
	defer j.owner.mu.Unlock()
	device := j.owner.devices[j.request.scope]
	if device == nil || device.user != j.request.user || device.controlVersion != j.request.controlVersion || !preloadControlMatches(device, j.request) {
		return nil, nil
	}
	episodes, seasons := device.episodeList, device.seasonList
	if episodes != nil && time.Since(episodes.at) > preloadAssociationTTL {
		episodes = nil
	}
	if seasons != nil && time.Since(seasons.at) > preloadAssociationTTL {
		seasons = nil
	}
	if seasons != nil && seasons.sequence < device.selectionInvalidated {
		copy := *seasons
		copy.first = false
		seasons = &copy
	}
	return episodes, seasons
}

func (l *preloadEpisodeList) mergeMetadata(metadata *preloadPendingList) *preloadEpisodeList {
	if l == nil {
		return nil
	}
	list := &preloadPendingList{items: l.items, sequence: l.sequence}
	updated := list.mergeMetadata(metadata)
	if updated == list {
		return l
	}
	copy := *l
	copy.items = updated.items
	return &copy
}

func (d *preloadDevice) invalidatePreloadSelection(sequence uint64) {
	d.pendingLists = [2]*preloadPendingList{}
	d.pendingFirst = false
	d.invalidatePreloadWatchState(sequence)
}

func (d *preloadDevice) invalidatePreloadWatchState(sequence uint64) {
	d.selectionInvalidated = max(d.selectionInvalidated, sequence)
	for _, item := range []*preloadItem{&d.detail, &d.pendingTarget} {
		if item.userDataSequence < d.selectionInvalidated {
			item.UserData, item.userDataSequence = nil, d.selectionInvalidated
		}
	}
	d.memberItems = slices.Clone(d.memberItems)
	for i := range d.memberItems {
		if d.memberItems[i].userDataSequence < d.selectionInvalidated {
			d.memberItems[i].UserData, d.memberItems[i].userDataSequence = nil, d.selectionInvalidated
		}
	}
	for i, list := range d.pendingLists {
		if list == nil {
			continue
		}
		copy := *list
		copy.items = slices.Clone(copy.items)
		for index := range copy.items {
			if copy.items[index].userDataSequence < d.selectionInvalidated {
				copy.items[index].UserData, copy.items[index].userDataSequence = nil, d.selectionInvalidated
			}
		}
		d.pendingLists[i] = &copy
	}
	if d.seasonList != nil && d.seasonList.sequence < d.selectionInvalidated {
		// 完整季成员仍可证明只有特别篇；旧未看起点和观看资料失效。
		copy := *d.seasonList
		copy.first = false
		copy.items = slices.Clone(copy.items)
		for i := range copy.items {
			if copy.items[i].userDataSequence < d.selectionInvalidated {
				copy.items[i].UserData, copy.items[i].userDataSequence = nil, d.selectionInvalidated
			}
		}
		d.seasonList = &copy
	}
	if d.episodeList != nil {
		// 播放改变观看状态，保留未过滤列表的邻接关系与源资料。
		copy := *d.episodeList
		copy.first, copy.complete = false, false
		copy.items = slices.Clone(copy.items)
		for i := range copy.items {
			if copy.items[i].userDataSequence < d.selectionInvalidated {
				copy.items[i].UserData, copy.items[i].userDataSequence = nil, d.selectionInvalidated
			}
		}
		d.episodeList = &copy
	}
}

func (l *preloadEpisodeList) neverPlayed(parent preloadItem) bool {
	if l == nil || !l.complete || !l.continuous || len(l.items) == 0 {
		return false
	}
	if parent.Type == "Series" && (l.series != parent.ID || l.season != "") || parent.Type == "Season" && l.season != parent.ID {
		return false
	}
	for _, item := range l.items {
		u := item.UserData
		if !validPreloadItem(item) || u == nil || u.Played == nil || *u.Played || u.PlayCount == nil || *u.PlayCount != 0 || u.PlaybackPositionTicks == nil || *u.PlaybackPositionTicks != 0 {
			return false
		}
	}
	return true
}

func (j *embyPreloadJob) cachedItem(id string) (preloadItem, bool) {
	j.owner.mu.Lock()
	defer j.owner.mu.Unlock()
	device := j.owner.devices[j.request.scope]
	if device == nil || device.user != j.request.user || device.controlVersion != j.request.controlVersion || !preloadControlMatches(device, j.request) {
		return preloadItem{}, false
	}
	var found preloadItem
	merge := func(item preloadItem) {
		if item.ID == id {
			found = mergePreloadPendingItem(item, found)
		}
	}
	if time.Since(device.detailAt) <= preloadAssociationTTL {
		merge(device.detail)
	}
	if time.Since(device.pendingTarget.metadataAt) <= preloadAssociationTTL {
		merge(device.pendingTarget)
	}
	if time.Since(device.membersAt) <= preloadAssociationTTL {
		for _, item := range device.memberItems {
			merge(item)
		}
	}
	if list := device.episodeList; list != nil && time.Since(list.at) <= preloadAssociationTTL {
		for _, item := range list.items {
			merge(item)
		}
	}
	for _, list := range device.pendingLists {
		if list != nil && time.Since(list.at) <= preloadAssociationTTL {
			for _, item := range list.items {
				merge(item)
			}
		}
	}
	if found.ID != "" && device.pending.MediaSources != nil && device.pendingID == id && device.pendingSequence == device.playbackStarted && time.Since(device.pendingAt) <= preloadAssociationTTL {
		current := found
		current.Path, current.MediaSources = "", device.pending.MediaSources
		merge(sequencePreloadItem(current, device.pendingSequence))
	}
	source, ok := selectPreloadLinkSource(found, "")
	return found, validPreloadItem(found) && ok && playback.ValidExternalURL(source.Path)
}

func (j *embyPreloadJob) cachedSuccessor(current preloadItem) (preloadItem, bool) {
	list, _ := j.episodeLists()
	if list == nil || !list.continuous || list.series != current.SeriesID {
		return preloadItem{}, false
	}
	for i, item := range list.items {
		if item.ID == current.ID && i+1 < len(list.items) {
			next := list.items[i+1]
			if validPreloadItem(next) {
				return next, true
			}
		}
	}
	return preloadItem{}, false
}

func (j *embyPreloadJob) cachePending(parent preloadItem, kind int, list *preloadPendingList) {
	j.owner.mu.Lock()
	defer j.owner.mu.Unlock()
	device := j.owner.devices[j.request.scope]
	if device == nil || device.user != j.request.user || device.controlVersion != j.request.controlVersion || list.sequence < device.selectionInvalidated || !preloadControlMatches(device, j.request) {
		return
	}
	if old := device.pendingLists[kind]; old != nil && old.sequence > list.sequence {
		return
	}
	list.at = time.Now()
	for i, item := range list.items {
		list.items[i] = sequencePreloadItem(item, list.sequence)
	}
	if time.Since(device.membersAt) <= preloadAssociationTTL {
		list = list.mergeMetadata(&preloadPendingList{items: device.memberItems})
	}
	if other := device.pendingLists[1-kind]; other != nil && time.Since(other.at) <= preloadAssociationTTL {
		list = list.mergeMetadata(other)
	}
	device.pendingLists[kind] = list
	device.mergePreloadMetadata(list)
}

func (j *embyPreloadJob) firstEpisode(ctx context.Context, task *playback.PreloadTask, parent preloadItem, series string) (preloadItem, uint64, error) {
	if !validPreloadID(series) {
		return preloadItem{}, 0, errors.New("pending_scope_unknown")
	}
	if parent.Type == "Season" && parent.UserData != nil && parent.UserData.Played != nil && *parent.UserData.Played {
		return preloadItem{}, parent.sourceSequence, errors.New("pending_empty")
	}
	episodes, seasons := j.episodeLists()
	normal := parent.Type == "Series"
	season := parent
	if normal {
		if value := episodes.firstSeriesEpisode(series); value.ID != "" {
			return value, episodes.sequence, nil
		}
		var known bool
		season, known = firstPreloadSeason(seasons, series)
		if special, onlySpecials := specialPreloadSeason(seasons, series); season.ID == "" && onlySpecials {
			season, normal = special, false
			if season.ID == "" {
				return preloadItem{}, seasons.sequence, errors.New("pending_empty")
			}
		}
		if season.ID == "" && !known {
			query := url.Values{"IsSpecialSeason": {"false"}, "IsPlayed": {"false"}, "SortBy": {"IndexNumber"}, "SortOrder": {"Ascending"}, "Limit": {"1"}, "Fields": {preloadItemFields}, "EnableImages": {"false"}, "EnableTotalRecordCount": {"false"}}
			var err error
			seasons, err = j.fetchEpisodeList(ctx, task, series, query, true)
			if err != nil {
				return preloadItem{}, 0, err
			}
			episodes, _ = j.episodeLists()
			if value := episodes.firstSeriesEpisode(series); value.ID != "" {
				return value, episodes.sequence, nil
			}
			season, known = firstPreloadSeason(seasons, series)
			if !known {
				return preloadItem{}, 0, errors.New("pending_invalid")
			}
		}
		if season.ID == "" {
			special, onlySpecials := specialPreloadSeason(seasons, series)
			if !onlySpecials && !(seasons.complete && !seasons.unplayed && len(seasons.items) > 0) {
				// 未看正片为空并不等于只有特别篇，完整季列表同时确认存在性与特别篇季 ID。
				query := url.Values{"SortBy": {"IndexNumber"}, "SortOrder": {"Ascending"}, "Limit": {"256"}, "Fields": {preloadItemFields}, "EnableImages": {"false"}, "EnableTotalRecordCount": {"true"}}
				var err error
				seasons, err = j.fetchEpisodeList(ctx, task, series, query, true)
				if err != nil {
					return preloadItem{}, 0, err
				}
				episodes, _ = j.episodeLists()
				if value := episodes.firstSeriesEpisode(series); value.ID != "" {
					return value, episodes.sequence, nil
				}
				season, _ = firstPreloadSeason(seasons, series)
				special, onlySpecials = specialPreloadSeason(seasons, series)
			}
			if season.ID == "" && onlySpecials {
				season, normal = special, false
			}
			if season.ID == "" {
				return preloadItem{}, seasons.sequence, errors.New("pending_empty")
			}
		}
	}
	if !validPreloadID(season.ID) {
		return preloadItem{}, 0, errors.New("pending_scope_unknown")
	}
	episodes, seasons = j.episodeLists()
	if parent.Type == "Series" {
		if value := episodes.firstSeriesEpisode(series); value.ID != "" {
			return value, episodes.sequence, nil
		}
	}
	if episodes != nil && episodes.series == series && episodes.matchesSeason(season) && episodes.first {
		if value, known := firstPreloadEpisode(episodes, season, normal); known {
			if value.ID != "" {
				return value, episodes.sequence, nil
			}
			return preloadItem{}, episodes.sequence, errors.New("pending_empty")
		}
	}
	if parent.Type == "Season" && firstPreloadSeasonChanged(seasons, parent, preloadItem{}, 0) {
		return preloadItem{}, seasons.sequence, errors.New("pending_empty")
	}
	query := url.Values{"SeasonId": {season.ID}, "IsPlayed": {"false"}, "Limit": {"1"}, "Fields": {preloadItemFields}, "EnableImages": {"false"}, "EnableTotalRecordCount": {"false"}}
	if number, ok := preloadIndex(season.IndexNumber); ok {
		query.Set("ParentIndexNumber", strconv.Itoa(number))
	}
	var err error
	episodes, err = j.fetchEpisodeList(ctx, task, series, query, false)
	if err != nil {
		return preloadItem{}, 0, err
	}
	if value, _ := firstPreloadEpisode(episodes, season, normal); value.ID != "" {
		return value, episodes.sequence, nil
	}
	return preloadItem{}, episodes.sequence, errors.New("pending_empty")
}

func (j *embyPreloadJob) fetchEpisodeList(ctx context.Context, task *playback.PreloadTask, series string, query url.Values, seasons bool) (*preloadEpisodeList, error) {
	if episodes, _ := j.episodeLists(); j.item.Type == "Series" && episodes.emptySeries(series) {
		return nil, errors.New("pending_empty")
	}
	kind := "Episodes"
	if seasons {
		kind = "Seasons"
	}
	sequence := j.owner.serial.Add(1)
	var result preloadItems
	if err := j.fetch(ctx, task, "/Shows/"+series+"/"+kind, query, true, &result); err != nil {
		return nil, err
	}
	if !task.Valid() {
		return nil, playback.ErrPreloadCanceled
	}
	request := j.request
	request.item, request.sequence, request.fromItems = series, sequence, false
	list := newPreloadEpisodeList(request, query, result, seasons)
	if list == nil {
		return nil, errors.New("pending_invalid")
	}
	j.owner.mu.Lock()
	defer j.owner.mu.Unlock()
	device := j.owner.devices[j.request.scope]
	if device == nil || device.user != j.request.user || device.controlVersion != j.request.controlVersion || !preloadControlMatches(device, j.request) || !task.Valid() {
		return nil, playback.ErrPreloadCanceled
	}
	if sequence < device.selectionInvalidated {
		return nil, playback.ErrPreloadCanceled
	}
	current := &device.episodeList
	if seasons {
		current = &device.seasonList
	}
	if *current == nil || (*current).sequence <= sequence {
		*current = list
		device.mergePreloadMetadata(&preloadPendingList{items: list.items})
		return *current, nil
	}
	if (*current).series == list.series && (*current).season == list.season && time.Since((*current).at) <= preloadAssociationTTL {
		if (*current).first {
			*current = (*current).mergeMetadata(&preloadPendingList{items: list.items, sequence: list.sequence})
			device.mergePreloadMetadata(&preloadPendingList{items: (*current).items})
			return *current, nil
		}
		// 较新分页只补同条目资料，保留查询结果的首项证据。
		return list.mergeMetadata(&preloadPendingList{items: (*current).items, sequence: (*current).sequence}), nil
	}
	return list, nil
}

func firstPreloadSeason(list *preloadEpisodeList, series string) (preloadItem, bool) {
	if list == nil || list.series != series || !list.first {
		return preloadItem{}, false
	}
	for _, item := range list.items {
		number, known := preloadIndex(item.IndexNumber)
		if !known {
			return preloadItem{}, false
		}
		if number == 0 || !validPreloadItem(item) {
			continue
		}
		if item.UserData != nil && item.UserData.Played != nil {
			if *item.UserData.Played {
				continue
			}
		} else if !list.unplayed {
			return preloadItem{}, false
		}
		return item, true
	}
	return preloadItem{}, list.complete || list.normalOnly && list.unplayed
}

func specialPreloadSeason(list *preloadEpisodeList, series string) (preloadItem, bool) {
	if list == nil || list.series != series || !list.complete || list.unplayed || list.normalOnly {
		return preloadItem{}, false
	}
	var first preloadItem
	for _, item := range list.items {
		number, known := preloadIndex(item.IndexNumber)
		if !known || number != 0 {
			return preloadItem{}, false
		}
		if first.ID == "" && validPreloadItem(item) && (item.UserData == nil || item.UserData.Played == nil || !*item.UserData.Played) {
			first = item
		}
	}
	return first, true
}

func (l *preloadEpisodeList) matchesSeason(season preloadItem) bool {
	if l.season == season.ID {
		return true
	}
	number, known := preloadIndex(season.IndexNumber)
	if !known || l.season == "" || len(l.items) == 0 {
		return false
	}
	// Emby 可能为同一季返回不同 ID，只有已核验同剧且季号一致时才复用。
	for _, item := range l.items {
		current, ok := preloadIndex(item.ParentIndexNumber)
		if !ok || current != number || item.SeriesID != l.series {
			return false
		}
	}
	return true
}

func (l *preloadEpisodeList) firstSeriesEpisode(series string) preloadItem {
	if l == nil || l.series != series || l.season != "" || l.seasonNumber >= 0 || !l.first {
		return preloadItem{}
	}
	value, _ := firstPreloadEpisode(l, preloadItem{}, true)
	return value
}

func (l *preloadEpisodeList) emptySeries(series string) bool {
	return l != nil && l.series == series && l.season == "" && l.seasonNumber < 0 && l.first && len(l.items) == 0
}

func firstPreloadEpisode(list *preloadEpisodeList, season preloadItem, normal bool) (preloadItem, bool) {
	for _, item := range list.items {
		if !validPreloadItem(item) || item.Type != "Episode" {
			continue
		}
		if number, known := preloadIndex(season.IndexNumber); season.ID != "" {
			if item.SeasonID != "" && item.SeasonID != season.ID && !known {
				return preloadItem{}, false
			}
			if current, ok := preloadIndex(item.ParentIndexNumber); known && ok && current != number {
				if normal && current == 0 {
					continue
				}
				return preloadItem{}, false
			}
		}
		if normal {
			n, ok := preloadIndex(item.ParentIndexNumber)
			if !ok {
				return preloadItem{}, false
			}
			if n == 0 {
				continue
			}
		}
		if item.UserData != nil && item.UserData.Played != nil {
			if *item.UserData.Played {
				continue
			}
		} else if !list.unplayed {
			return preloadItem{}, false
		}
		return item, true
	}
	return preloadItem{}, len(list.items) == 0 || list.complete || list.unplayed && (!normal || list.seasonNumber > 0)
}

func (j *embyPreloadJob) mergeCachedItem() {
	if item, _ := j.cachedItem(j.item.ID); item.ID != "" {
		j.item = mergePreloadPendingItem(item, j.item)
	}
}
