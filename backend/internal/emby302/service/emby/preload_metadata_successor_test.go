package emby

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"testing/synctest"
	"time"
)

func TestPreloadMoviePartsRespectSourceObservationOrder(t *testing.T) {
	for _, test := range []struct {
		name, currentType        string
		olderDetail, newerDetail bool
	}{
		{name: "movie-no-cache", currentType: "Movie"},
		{name: "movie-older-detail", currentType: "Movie", olderDetail: true},
		{name: "movie-newer-detail", currentType: "Movie", olderDetail: true, newerDetail: true},
		{name: "video-older-detail", currentType: "Video", olderDetail: true},
	} {
		runEmbyPreloadCase(t, test.name, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			parent := preloadTestItem("20", "Movie")
			parent.PartCount = 2
			current, next := parent, preloadTestItem("21", "Video")
			next.ParentID = parent.ID
			calls := int32(1)
			if test.currentType == "Video" {
				parent.PartCount = 3
				current = next
				next = preloadTestItem("22", "Video")
				next.ParentID, calls = parent.ID, 2
			}
			current.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			queried := next
			queried.MediaSources = slices.Clone(next.MediaSources)
			queried.MediaSources[0].Path += "&version=query"
			cached := next
			cached.MediaSources = slices.Clone(next.MediaSources)
			cached.MediaSources[0].Path += "&version=old"
			newer := next
			newer.MediaSources = slices.Clone(next.MediaSources)
			newer.MediaSources[0].Path += "&version=newer"
			h.nodes[current.ID], h.nodes[next.ID] = current, cached
			entered := make(chan struct{})
			release := make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/10/Items/20":
					if test.currentType != "Video" {
						t.Error("known movie repeated its parent detail")
					}
					_ = json.NewEncoder(w).Encode(parent)
				case "/Videos/20/AdditionalParts":
					if r.URL.Query().Get("Fields") != preloadItemFields {
						t.Error("parts lookup omitted existing metadata fields")
					}
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					parts := []preloadItem{queried}
					if test.currentType == "Video" {
						parts = append([]preloadItem{current}, parts...)
					}
					_ = json.NewEncoder(w).Encode(preloadItems{Items: parts, TotalRecordCount: len(parts)})
				default:
					t.Errorf("unexpected movie successor query %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}
			h.request("GET", "/Users/10/Items/"+current.ID, "device", "Fixture App", "Player/1", nil)
			h.request("GET", "/Videos/"+current.ID+"/stream?MediaSourceId=source-"+current.ID, "device", "Fixture App", "Player/1", nil)
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Player/1", map[string]string{"ItemId": current.ID, "MediaSourceId": "source-" + current.ID})
			if test.olderDetail {
				h.request("GET", "/Users/10/Items/"+next.ID, "device", "Fixture App", "Player/1", nil)
			}
			h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Player/1", map[string]any{
				"ItemId": current.ID, "MediaSourceId": "source-" + current.ID, "IsPaused": false, "PositionTicks": 54 * 60 * 10_000_000,
			})
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("additional parts lookup did not start")
			}
			want := queried.MediaSources[0].Path
			if test.newerDetail {
				h.mu.Lock()
				h.nodes[next.ID] = newer
				h.mu.Unlock()
				h.request("GET", "/Users/10/Items/"+next.ID, "device", "Fixture App", "Player/1", nil)
				want = newer.MediaSources[0].Path
			}
			release <- struct{}{}
			if got := h.waitResolved(); got.path != want || got.ua != "Player/1" {
				t.Fatalf("next part resolved=%+v, want=%q", got, want)
			}
			if h.metadata.Load() != calls {
				t.Fatalf("parts metadata calls=%d, want=%d", h.metadata.Load(), calls)
			}
		})
	}
}

func TestPreloadQueueCompletesMissingTypeFromNaturalPendingItem(t *testing.T) {
	for _, test := range []struct {
		name, list, itemType, series, access string
		calls                                int32
		want                                 bool
	}{
		{name: "nextup-missing-type", list: "nextup", series: "90", calls: 1, want: true},
		{name: "resume-missing-type", list: "resume", series: "90", calls: 1, want: true},
		{name: "nextup-complete", list: "nextup", itemType: "Episode", series: "90", want: true},
		{name: "resume-complete", list: "resume", itemType: "Episode", series: "90", want: true},
		{name: "nextup-wrong-type", list: "nextup", itemType: "Movie", series: "90"},
		{name: "nextup-cross-series", list: "nextup", itemType: "Episode", series: "99"},
		{name: "resume-denied", list: "resume", itemType: "Episode", series: "90", access: "None"},
		{name: "missing-type-cross-series", list: "nextup", series: "99"},
		{name: "missing-type-denied", list: "resume", series: "90", access: "None"},
	} {
		runEmbyPreloadCase(t, test.name, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			current, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			current.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			next.Type, next.SeriesID, next.PlayAccess = test.itemType, test.series, test.access
			fetched := preloadTestItem("21", "Episode")
			fetched.MediaSources[0].Path += "&detail=1"
			h.nodes[current.ID] = current
			path, listPath := "/Shows/NextUp?UserId=10&SeriesId=90", "/Shows/NextUp"
			if test.list == "resume" {
				path, listPath = "/Users/10/Items/Resume?ParentId=90", "/Users/10/Items/Resume"
			}
			h.lists[listPath] = preloadItems{Items: []preloadItem{next}}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/21" {
					t.Errorf("unexpected queue metadata %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(fetched)
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Player/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Player/1", nil)
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Player/1", map[string]string{"ItemId": "20", "MediaSourceId": "source-20"})
			h.request("GET", path, "device", "Fixture App", "Player/1", nil)
			h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Player/1", map[string]any{
				"ItemId": "20", "MediaSourceId": "source-20", "IsPaused": false, "PositionTicks": 54 * 60 * 10_000_000,
				"NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "21"}},
			})
			synctest.Wait()
			if got := len(h.resolved) != 0; got != test.want {
				t.Fatalf("queue resolved=%v, want=%v", got, test.want)
			}
			if test.want {
				want := next.MediaSources[0].Path
				if test.calls != 0 {
					want = fetched.MediaSources[0].Path
				}
				if got := h.waitResolved(); got.path != want || got.ua != "Player/1" {
					t.Fatalf("queue source=%+v, want=%q", got, want)
				}
			}
			if h.metadata.Load() != test.calls {
				t.Fatalf("queue metadata calls=%d, want=%d", h.metadata.Load(), test.calls)
			}
		})
	}
}
