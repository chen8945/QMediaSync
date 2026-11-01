package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/playback"
)

func TestPreloadFirstEpisodeQueries(t *testing.T) {
	for _, test := range []struct {
		name, want string
		calls      int
	}{
		{"normal", "20", 4},
		{"only-specials", "30", 5},
		{"played-normal-with-specials", "", 4},
		{"missing-first-season-and-episode", "20", 4},
		{"season-alias", "20", 4},
		{"season-alias-unknown-index", "", 3},
		{"season-matching-unknown-index", "20", 3},
		{"series-parent", "20", 4},
		{"foreign-parent", "", 4},
		{"cross-series", "", 4},
		{"wrong-season-number", "", 4},
		{"season-denied", "", 3},
		{"empty-episodes", "", 4},
		{"incomplete-specials", "", 4},
		{"parent-playcount-zero", "21", 1},
	} {
		runEmbyPreloadCase(t, test.name, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			played, unplayed, count := true, false, 0
			parent := preloadItem{ID: "90", Type: "Series", UserData: &preloadUserData{PlayCount: &count}}
			if test.name == "season-alias-unknown-index" || test.name == "season-matching-unknown-index" {
				parent = preloadItem{ID: "91", Type: "Season", SeriesID: "90"}
			}
			h.nodes[parent.ID] = parent
			season := preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: &preloadUserData{Played: &unplayed}}
			episode := preloadTestItem("20", "Episode")
			episode.ParentIndexNumber, episode.IndexNumber = 1, 1
			if test.name == "missing-first-season-and-episode" {
				season.IndexNumber = 2
				episode.ParentIndexNumber, episode.IndexNumber = 2, 3
			}
			if test.name == "season-alias" {
				episode.SeasonID, episode.ParentID = "89", "89"
			}
			if test.name == "season-alias-unknown-index" {
				episode.SeasonID, episode.ParentID, episode.ParentIndexNumber = "92", "92", 2
			}
			if test.name == "season-matching-unknown-index" {
				episode.ParentID, episode.ParentIndexNumber = "91", nil
			}
			if test.name == "series-parent" {
				episode.ParentID = "90"
			}
			if test.name == "foreign-parent" {
				episode.ParentID = "99"
			}
			if test.name == "cross-series" {
				episode.SeriesID = "99"
			}
			if test.name == "wrong-season-number" {
				episode.ParentIndexNumber = 0
			}
			var mu sync.Mutex
			var queries []string
			last := make(chan struct{})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				queries = append(queries, r.URL.Path)
				if len(queries) == test.calls {
					defer close(last)
				}
				mu.Unlock()
				result := preloadItems{Items: []preloadItem{}}
				switch r.URL.Path {
				case "/Users/10/Items/Resume":
					if test.name == "parent-playcount-zero" {
						result.Items = []preloadItem{preloadTestItem("21", "Episode")}
					}
				case "/Shows/NextUp":
				case "/Shows/90/Seasons":
					if r.URL.Query().Get("IsPlayed") == "false" {
						if r.URL.Query().Get("IsSpecialSeason") != "false" || r.URL.Query().Get("Limit") != "1" {
							t.Error("first lookup must select an unplayed ordinary season")
						}
						if test.name == "season-denied" {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						if test.name != "only-specials" && test.name != "played-normal-with-specials" && test.name != "incomplete-specials" {
							result.Items = []preloadItem{season}
						}
					} else {
						if r.URL.Query().Get("IsSpecialSeason") != "" {
							t.Error("only-special proof used a filtered seasons response")
						}
						result.Items = []preloadItem{{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 0}}
						result.TotalRecordCount = 1
						if test.name == "played-normal-with-specials" {
							season.UserData.Played = &played
							result.Items = append(result.Items, season)
							result.TotalRecordCount = 2
						}
						if test.name == "incomplete-specials" {
							result.TotalRecordCount = 2
						}
					}
				case "/Shows/90/Episodes":
					if r.URL.Query().Get("IsPlayed") != "false" || r.URL.Query().Get("Fields") != preloadItemFields {
						t.Error("first episode query omitted unplayed filtering or source fields")
					}
					if test.name == "only-specials" {
						if r.URL.Query().Get("SeasonId") != "92" || r.URL.Query().Get("ParentIndexNumber") != "0" {
							t.Error("specials were selected outside their confirmed season")
						}
						episode = preloadTestItem("30", "Episode")
						episode.SeasonID, episode.ParentIndexNumber = "92", 0
					} else if r.URL.Query().Get("SeasonId") != "91" {
						t.Error("ordinary episode query lost its chosen season")
					}
					if test.name != "empty-episodes" {
						result.Items = []preloadItem{episode}
					}
				default:
					t.Errorf("unexpected metadata query %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(result)
			}
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			select {
			case <-last:
			case <-time.After(5 * time.Second):
				t.Fatalf("metadata stopped after %d calls, want %d", h.metadata.Load(), test.calls)
			}
			waitPreloadPendingJobs(t, h)
			if test.want == "" {
				if len(h.resolved) != 0 {
					t.Fatal("unproven first episode reached source resolution")
				}
			} else if got := h.waitResolved(); got.path != preloadTestItem(test.want, "Episode").MediaSources[0].Path {
				t.Fatalf("resolved %q instead of episode %s", got.path, test.want)
			}
			mu.Lock()
			got := slices.Clone(queries)
			mu.Unlock()
			if len(got) != test.calls || h.metadata.Load() != int32(test.calls) {
				t.Fatalf("metadata queries=%v, want %d calls", got, test.calls)
			}
			if test.calls > 1 && (got[0] != "/Users/10/Items/Resume" || got[1] != "/Shows/NextUp") {
				t.Fatalf("candidate lookup order=%v", got)
			}
		})
	}
}

func TestPreloadFirstEpisodeReusesNaturalLists(t *testing.T) {
	for _, order := range []string{"list-first", "detail-first"} {
		for _, mode := range []string{"complete-never-played", "parent-count-is-not-proof", "filtered-unplayed", "cached-prefix", "cached-seasons", "cached-specials", "season-alias", "page-offset", "custom-order", "custom-filter", "generic-items", "generic-nonrecursive", "generic-season", "empty-root", "empty-unplayed-root"} {
			runEmbyPreloadCase(t, order+"/"+mode, func(t *testing.T) {
				h := newEmbyPreloadTestHarness(t)
				unplayed, count, position := false, 0, int64(0)
				parent := preloadItem{ID: "90", Type: "Series", UserData: &preloadUserData{PlayCount: &count}}
				first, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
				first.ParentIndexNumber, first.IndexNumber = 1, 1
				next.ParentIndexNumber, next.IndexNumber = 1, 2
				first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
				next.UserData = first.UserData
				season := preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: first.UserData}
				episodes := preloadItems{Items: []preloadItem{first, next}, TotalRecordCount: 2}
				path := "/Shows/90/Episodes?UserId=10"
				wantCalls := int32(0)
				pendingEmpty := true
				switch mode {
				case "complete-never-played":
					pendingEmpty = false
				case "parent-count-is-not-proof":
					episodes.Items[0].UserData, episodes.Items[1].UserData = &preloadUserData{Played: &unplayed}, &preloadUserData{Played: &unplayed}
					pendingEmpty, wantCalls = false, 2
				case "filtered-unplayed":
					path += "&IsPlayed=false"
					pendingEmpty, wantCalls = false, 2
				case "cached-prefix":
					episodes.TotalRecordCount = 100
				case "cached-seasons":
					path, wantCalls = "", 1
				case "cached-specials":
					season.ID, season.IndexNumber = "92", 0
					first = preloadTestItem("30", "Episode")
					first.SeasonID, first.ParentIndexNumber, first.IndexNumber, first.UserData = "92", 0, 3, next.UserData
					episodes = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
					path += "&SeasonId=92"
				case "season-alias":
					episodes.Items[0].SeasonID, episodes.Items[1].SeasonID = "89", "89"
					path += "&SeasonId=89"
				case "page-offset":
					episodes.Items, episodes.TotalRecordCount = []preloadItem{next}, 20
					path += "&StartIndex=10"
					wantCalls = 1
				case "custom-order":
					episodes.Items = []preloadItem{next, first}
					path += "&SortBy=SortName"
					wantCalls = 1
				case "custom-filter":
					episodes.Items = []preloadItem{next}
					path += "&Filters=IsFavorite"
					wantCalls = 1
				case "generic-items":
					path = "/Users/10/Items?ParentId=90&IncludeItemTypes=Episode&Recursive=true&SortBy=ParentIndexNumber,IndexNumber"
					if order == "list-first" {
						wantCalls = 1
					}
				case "generic-nonrecursive":
					path = "/Users/10/Items?ParentId=90&IncludeItemTypes=Episode&SortBy=ParentIndexNumber,IndexNumber"
					pendingEmpty, wantCalls = false, 1
				case "generic-season":
					parent = season
					path = "/Users/10/Items?ParentId=91&IncludeItemTypes=Episode&SortBy=IndexNumber"
					if order == "list-first" {
						wantCalls = 1
					}
				case "empty-root":
					episodes = preloadItems{Items: []preloadItem{}}
				case "empty-unplayed-root":
					episodes = preloadItems{Items: []preloadItem{}}
					path += "&IsPlayed=false"
					pendingEmpty, wantCalls = false, 1
				}
				h.nodes[parent.ID] = parent
				want := first
				if mode == "generic-nonrecursive" || mode == "empty-unplayed-root" {
					want = preloadTestItem("22", "Episode")
				}
				h.lists["/Shows/90/Seasons"] = preloadItems{Items: []preloadItem{season}, TotalRecordCount: 1}
				h.lists["/Shows/90/Episodes"], h.lists["/Users/10/Items"] = episodes, episodes
				h.lists["/Users/10/Items/Resume"], h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{}}, preloadItems{Items: []preloadItem{}}
				h.upstream = func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/Users/10/Items/Resume", "/Shows/NextUp":
						items := []preloadItem{}
						if mode == "generic-nonrecursive" || mode == "empty-unplayed-root" {
							items = append(items, want)
						}
						_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
					case "/Shows/90/Episodes":
						if r.URL.Query().Get("SeasonId") != season.ID || r.URL.Query().Get("IsPlayed") != "false" {
							t.Error("cached seasons did not scope the episode lookup")
						}
						_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{first}})
					default:
						t.Errorf("repeated known metadata: %s", r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
					}
				}
				detail := func() { h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil) }
				if order == "detail-first" {
					detail()
				}
				if pendingEmpty {
					h.request("GET", "/Users/10/Items/Resume?ParentId="+parent.ID, "device", "Fixture App", "Control/1", nil)
					h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
				}
				h.request("GET", "/Shows/90/Seasons?UserId=10", "device", "Fixture App", "Control/1", nil)
				if path != "" {
					h.request("GET", path, "device", "Fixture App", "Control/1", nil)
				}
				if order == "list-first" {
					detail()
				}
				if mode == "empty-root" {
					waitPreloadPendingJobs(t, h, playback.PreloadDetail)
					if len(h.resolved) != 0 {
						t.Fatal("successful whole-series empty list still selected an episode")
					}
				} else if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
					t.Fatalf("natural evidence selected %q, want %q", got.path, want.MediaSources[0].Path)
				}
				if got := h.metadata.Load(); got != wantCalls {
					t.Fatalf("metadata=%d want=%d", got, wantCalls)
				}
			})
		}
	}
}

func TestPreloadSuccessorReusesNaturalEpisodes(t *testing.T) {
	for _, mode := range []string{"cross-season", "page-offset", "queue-filtered", "unplayed-filter", "custom-order", "source-missing", "queue-warm-source"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			current, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			current.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			current.ParentIndexNumber, current.IndexNumber = 1, 12
			next.SeasonID, next.ParentIndexNumber, next.IndexNumber = "92", 2, 1
			h.nodes[current.ID] = current
			path := "/Shows/90/Episodes?UserId=10"
			wantCalls := int32(0)
			progress := map[string]any{"ItemId": "20", "MediaSourceId": "source-20", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false}
			switch mode {
			case "page-offset":
				path += "&StartIndex=10"
			case "queue-filtered", "source-missing", "queue-warm-source":
				path += "&IsPlayed=false"
				progress["NowPlayingQueue"] = []map[string]string{{"Id": "20"}, {"Id": "21"}}
				if mode == "source-missing" {
					next.MediaSources, wantCalls = nil, 1
				} else if mode == "queue-warm-source" {
					next.MediaSources[0].Path = ""
					next.MediaSources[0].Container = "mkv"
					next.MediaSources[0].RunTimeTicks = current.MediaSources[0].RunTimeTicks
					next.MediaSources[0].MediaStreams = []preloadStream{{Type: "Video", Codec: "h264"}, {Type: "Audio", Codec: "aac"}}
					wantCalls = 1
				}
			case "unplayed-filter":
				path += "&IsPlayed=false"
				wantCalls = 1
			case "custom-order":
				path += "&SortBy=SortName"
				wantCalls = 1
			}
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{current, next}, TotalRecordCount: 24}
			requested := make(chan struct{}, 2)
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				defer func() { requested <- struct{}{} }()
				if mode == "queue-warm-source" {
					if r.URL.Path != "/Items/21/PlaybackInfo" || r.URL.Query().Get("IsPlayback") != "false" || r.URL.Query().Get("MediaSourceId") != "source-21" {
						t.Errorf("complete cached source repeated ordinary detail: %s", r.URL)
					}
					_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: preloadTestItem("21", "Episode").MediaSources})
					return
				}
				if mode == "source-missing" {
					if r.URL.Path != "/Users/10/Items/21" {
						t.Errorf("known queue queried %s", r.URL.Path)
					}
					_ = json.NewEncoder(w).Encode(next)
					return
				}
				if r.URL.Path != "/Shows/90/Episodes" || r.URL.Query().Get("StartItemId") != "20" || r.URL.Query().Get("SeasonId") != "" {
					t.Errorf("successor query lost cross-season scope: %s", r.URL)
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{current, next}})
			}
			h.request("GET", "/Users/10/Items/20", "device", "Emby Web", "Player/1", nil)
			h.request("GET", path, "device", "Emby Web", "Player/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Emby Web", "Player/1", nil)
			h.request("POST", "/Sessions/Playing", "device", "Emby Web", "Player/1", map[string]string{"ItemId": "20", "MediaSourceId": "source-20"})
			h.request("POST", "/Sessions/Playing/Progress", "device", "Emby Web", "Player/1", progress)
			if mode == "source-missing" {
				select {
				case <-requested:
				case <-time.After(5 * time.Second):
					t.Fatal("missing source was not queried")
				}
				waitPreloadPendingJobs(t, h)
				if len(h.resolved) != 0 {
					t.Fatal("missing source reached resolution")
				}
			} else if got := h.waitResolved(); got.path != preloadTestItem("21", "Episode").MediaSources[0].Path {
				t.Fatalf("successor=%q want episode 21", got.path)
			}
			if got := h.metadata.Load(); got != wantCalls {
				t.Fatalf("metadata=%d want=%d", got, wantCalls)
			}
		})
	}
}

func TestPreloadFirstEpisodeRefreshKeepsOrReplacesCandidate(t *testing.T) {
	for _, mode := range []string{"same-empty", "new-nextup", "watched", "filtered-next", "paged-missing", "other-season", "special-root-empty", "special-root-reordered", "special-root-paged-empty", "special-root-removed", "watched-season-end", "empty-season-end"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, played, count, position := false, true, 0, int64(0)
			first, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			first.ParentIndexNumber, first.IndexNumber = 1, 1
			next.ParentIndexNumber, next.IndexNumber = 1, 2
			first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			next.UserData = first.UserData
			special := mode == "special-root-empty" || mode == "special-root-reordered" || mode == "special-root-paged-empty" || mode == "special-root-removed"
			seasonEnd := mode == "watched-season-end" || mode == "empty-season-end"
			if special {
				first.SeasonID, first.ParentIndexNumber, first.IndexNumber = "92", 0, 3
				next.SeasonID, next.ParentIndexNumber, next.IndexNumber = "92", 0, 111
				h.lists["/Shows/90/Seasons"] = preloadItems{Items: []preloadItem{{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 0}}, TotalRecordCount: 1}
			}
			h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first, next}, TotalRecordCount: 2}
			if seasonEnd {
				next.SeasonID, next.ParentIndexNumber, next.IndexNumber = "92", 2, 1
				h.lists["/Shows/90/Seasons"] = preloadItems{Items: []preloadItem{
					{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: first.UserData},
					{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 2, UserData: first.UserData},
				}, TotalRecordCount: 2}
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
			}
			h.lists["/Users/10/Items/Resume"], h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{}}, preloadItems{Items: []preloadItem{}}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				items := []preloadItem{}
				switch r.URL.Path {
				case "/Users/10/Items/Resume":
				case "/Shows/NextUp":
					if mode != "special-root-empty" && !seasonEnd {
						items = append(items, next)
					}
				case "/Shows/90/Seasons":
					if !seasonEnd || r.URL.Query().Get("IsPlayed") != "false" || r.URL.Query().Get("IsSpecialSeason") != "false" {
						t.Error("season progress did not refresh the first unplayed ordinary season")
					}
					items = append(items, preloadItem{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 2, UserData: next.UserData})
				case "/Shows/90/Episodes":
					if !special && !seasonEnd || r.URL.Query().Get("SeasonId") != "92" {
						t.Error("first episode lookup lost the selected season scope")
					}
					items = append(items, next)
				default:
					t.Errorf("unexpected refreshed metadata: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
			}
			entered := make(chan *playback.PreloadTask, 1)
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			resolve := h.adapter.options.Resolve
			h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				key, run, supported := resolve(raw, ua, force)
				if raw != first.MediaSources[0].Path {
					return key, run, supported
				}
				return key, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					entered <- task
					select {
					case <-ctx.Done():
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					case <-release:
						if task.Valid() {
							return run(ctx, task)
						}
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
				}, supported
			}
			h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			initialEpisodes := "/Shows/90/Episodes?UserId=10"
			if special {
				h.request("GET", "/Shows/90/Seasons?UserId=10", "device", "Fixture App", "Control/1", nil)
				initialEpisodes += "&SeasonId=92"
			} else if seasonEnd {
				h.request("GET", "/Shows/90/Seasons?UserId=10", "device", "Fixture App", "Control/1", nil)
				initialEpisodes += "&SeasonId=91"
			}
			h.request("GET", initialEpisodes, "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			var original *playback.PreloadTask
			select {
			case original = <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first episode did not enter resolution")
			}
			want, wantCalls := next, int32(0)
			keep := mode == "same-empty" || mode == "paged-missing" || mode == "other-season" || mode == "special-root-reordered" || mode == "special-root-paged-empty"
			switch mode {
			case "same-empty":
				for range 2 {
					h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
					h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
				}
				if !original.Valid() {
					t.Fatal("matching successful empty results canceled first episode")
				}
				want = first
				release <- struct{}{}
			case "new-nextup":
				h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{next}}
				h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			case "watched":
				updated := first
				updated.UserData = &preloadUserData{Played: &played}
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{updated, next}, TotalRecordCount: 2}
				h.request("GET", "/Shows/90/Episodes?UserId=10", "device", "Fixture App", "Control/1", nil)
				wantCalls = 2
			case "watched-season-end", "empty-season-end":
				items := []preloadItem{}
				path := "/Shows/90/Episodes?UserId=10&SeasonId=91"
				if mode == "watched-season-end" {
					updated := first
					updated.UserData = &preloadUserData{Played: &played}
					items = append(items, updated)
				} else {
					path += "&IsPlayed=false"
				}
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: items, TotalRecordCount: len(items)}
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
				wantCalls = 4
			case "filtered-next", "paged-missing", "other-season":
				updated := next
				path := "/Shows/90/Episodes?UserId=10&IsPlayed=false"
				if mode == "paged-missing" {
					path += "&StartIndex=1"
				} else if mode == "other-season" {
					path += "&SeasonId=92"
					updated.SeasonID, updated.ParentIndexNumber = "92", 2
				}
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{updated}, TotalRecordCount: 1}
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
				if keep {
					if !original.Valid() {
						t.Fatal("partial or different-season evidence replaced first episode")
					}
					want = first
					release <- struct{}{}
				} else {
					wantCalls = 2
				}
			case "special-root-empty", "special-root-reordered", "special-root-paged-empty", "special-root-removed":
				items := []preloadItem{}
				path := "/Shows/90/Episodes?UserId=10"
				if mode == "special-root-reordered" {
					items = []preloadItem{next, first}
				} else if mode == "special-root-removed" {
					items, wantCalls = []preloadItem{next}, 1
				} else if mode == "special-root-paged-empty" {
					path += "&StartIndex=5"
				}
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: items, TotalRecordCount: len(items)}
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
				if keep {
					if !original.Valid() {
						t.Fatal("root special ordering or paged empty result replaced scoped first episode")
					}
					want = first
					release <- struct{}{}
				}
			}
			if !keep && original.Valid() {
				t.Fatal("new candidate or watched state retained the previous first episode")
			}
			if mode == "special-root-empty" {
				waitPreloadPendingJobs(t, h, playback.PreloadDetail)
				if len(h.resolved) != 0 {
					t.Fatal("empty series retained or replaced a removed special episode")
				}
			} else if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("refresh resolved %q, want %q", got.path, want.MediaSources[0].Path)
			}
			if got := h.metadata.Load(); got != wantCalls {
				t.Fatalf("metadata=%d want=%d", got, wantCalls)
			}
		})
	}
}

func TestPreloadFirstEpisodeUsesListsArrivingDuringLookup(t *testing.T) {
	for _, mode := range []string{"resume", "seasons", "empty-seasons", "paged-episodes", "paged-episode-metadata", "new-first-episode"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, count, position := false, 0, int64(0)
			first := preloadTestItem("20", "Episode")
			first.ParentIndexNumber, first.IndexNumber = 1, 1
			first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
			wantCalls := int32(3)
			if mode == "resume" {
				wantCalls = 1
			}
			want := first
			naturalPath := "/Shows/90/Episodes?UserId=10"
			if mode == "paged-episodes" || mode == "paged-episode-metadata" || mode == "new-first-episode" {
				wantCalls = 4
				next := preloadTestItem("21", "Episode")
				next.ParentIndexNumber, next.IndexNumber, next.UserData = 1, 6, first.UserData
				items := []preloadItem{next}
				naturalPath += "&SeasonId=91&IsPlayed=false"
				if mode == "new-first-episode" {
					want = next
				} else {
					naturalPath += "&StartIndex=5"
					if mode == "paged-episode-metadata" {
						want.MediaSources = slices.Clone(first.MediaSources)
						want.MediaSources[0].Path += "-new"
						items = append(items, want)
					}
				}
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: items, TotalRecordCount: 20}
			}
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if h.metadata.Load() == wantCalls {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				result := preloadItems{Items: []preloadItem{}}
				switch r.URL.Path {
				case "/Users/10/Items/Resume", "/Shows/NextUp":
				case "/Shows/90/Seasons":
					if mode != "empty-seasons" || r.URL.Query().Get("IsPlayed") == "" {
						result.Items = []preloadItem{{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: first.UserData}}
						result.TotalRecordCount = 1
					}
				case "/Shows/90/Episodes":
					result.Items = []preloadItem{first}
				default:
					t.Errorf("unexpected metadata: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(result)
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("background lookup did not start")
			}
			h.request("GET", naturalPath, "device", "Fixture App", "Control/1", nil)
			release <- struct{}{}
			if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("late natural evidence selected %q", got.path)
			}
			if got := h.metadata.Load(); got != wantCalls {
				t.Fatalf("metadata continued after sufficient natural data: got=%d want=%d", got, wantCalls)
			}
		})
	}
}

func TestPreloadFirstEpisodeSeasonsRefresh(t *testing.T) {
	for _, mode := range []string{"played", "first-season-changed", "same", "paged", "other-series", "season-alias", "explicit-nextup", "season-page-played", "older-seasons", "sort-name-played", "sort-name-same", "descending-played", "played-filter"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, played := false, true
			first, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			first.ParentIndexNumber, first.UserData = 1, &preloadUserData{Played: &unplayed}
			next.SeasonID, next.ParentIndexNumber, next.UserData = "92", 2, first.UserData
			if mode == "season-alias" {
				first.SeasonID = "89"
			}
			seasons := []preloadItem{
				{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: first.UserData},
				{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 2, UserData: first.UserData},
			}
			parent := preloadItem{ID: "90", Type: "Series"}
			if mode == "season-page-played" {
				parent = preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1}
			}
			h.nodes[parent.ID] = parent
			h.lists["/Shows/90/Seasons"] = preloadItems{Items: seasons, TotalRecordCount: 2}
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
			h.lists["/Users/10/Items/Resume"], h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{}}, preloadItems{Items: []preloadItem{}}
			if mode == "explicit-nextup" {
				h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{first}}
			}
			nextSeason := seasons[1]
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				items := []preloadItem{}
				switch r.URL.Path {
				case "/Users/10/Items/Resume", "/Shows/NextUp":
				case "/Shows/90/Seasons":
					items = append(items, nextSeason)
				case "/Shows/90/Episodes":
					if mode == "season-page-played" {
						if r.URL.Query().Get("SeasonId") != "91" {
							t.Error("explicit season page lost its scope")
						}
						break
					}
					if r.URL.Query().Get("SeasonId") != "92" {
						t.Error("seasons refresh retained the old first season")
					}
					items = append(items, next)
				default:
					t.Errorf("unexpected metadata after seasons refresh: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
			}
			entered, release := make(chan *playback.PreloadTask, 1), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			resolve := h.adapter.options.Resolve
			h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				key, run, supported := resolve(raw, ua, force)
				if raw != first.MediaSources[0].Path {
					return key, run, supported
				}
				return key, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, key) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					entered <- task
					select {
					case <-ctx.Done():
					case <-release:
						task.Publish(func() { run(ctx, task) })
					}
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}, supported
			}
			h.request("GET", "/Shows/90/Seasons?UserId=10", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/Resume?ParentId="+parent.ID, "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			var olderSeasons preloadRequest
			if mode == "older-seasons" {
				request, err := http.NewRequest(http.MethodGet, h.server.URL+"/Shows/90/Seasons?UserId=10&api_key=user-token&DeviceId=device&X-Emby-Client=Fixture+App", nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("User-Agent", "Control/1")
				var ok bool
				olderSeasons, ok = newPreloadRequest(request, t.Name(), "", "", 1)
				olderSeasons.item = "90"
				if !ok || !h.adapter.beginObservation(&olderSeasons, "seasons", nil) {
					t.Fatal("old Seasons request was not admitted")
				}
			}
			h.request("GET", "/Shows/90/Episodes?UserId=10&SeasonId=91", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			var original *playback.PreloadTask
			select {
			case original = <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first episode did not enter signing")
			}
			path := "/Shows/90/Seasons?UserId=10"
			if mode == "played" || mode == "explicit-nextup" || mode == "season-page-played" || mode == "older-seasons" || mode == "sort-name-played" || mode == "descending-played" || mode == "played-filter" {
				seasons[0].UserData = &preloadUserData{Played: &played}
			} else if mode == "first-season-changed" || mode == "paged" {
				seasons = seasons[1:]
				if mode == "paged" {
					path += "&StartIndex=5"
				}
			} else if mode == "other-series" {
				path = "/Shows/99/Seasons?UserId=10"
				seasons = []preloadItem{{ID: "99", Type: "Season", SeriesID: "99", IndexNumber: 1, UserData: first.UserData}}
			}
			if mode == "sort-name-played" || mode == "sort-name-same" {
				path += "&SortBy=SortName"
			} else if mode == "descending-played" {
				path += "&SortBy=IndexNumber&SortOrder=Descending"
			} else if mode == "played-filter" {
				path += "&IsPlayed=true"
				seasons = seasons[:1]
			}
			h.mu.Lock()
			h.lists["/Shows/90/Seasons"], h.lists["/Shows/99/Seasons"] = preloadItems{Items: seasons, TotalRecordCount: len(seasons)}, preloadItems{Items: seasons, TotalRecordCount: len(seasons)}
			h.mu.Unlock()
			if mode == "older-seasons" {
				data, err := json.Marshal(preloadItems{Items: seasons, TotalRecordCount: len(seasons)})
				if err != nil {
					t.Fatal(err)
				}
				h.adapter.observeSeasons(olderSeasons, nil, data)
			} else {
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
			}
			keep := mode != "played" && mode != "first-season-changed" && mode != "season-page-played" && mode != "sort-name-played" && mode != "descending-played" && mode != "played-filter"
			if original.Valid() != keep {
				t.Fatalf("old first episode valid=%v, want=%v", original.Valid(), keep)
			}
			want, calls := next, int32(3)
			if mode == "sort-name-played" || mode == "descending-played" || mode == "played-filter" {
				calls = 4
			} else if mode == "season-page-played" {
				calls = 2
			}
			if keep {
				want, calls = first, 0
				release <- struct{}{}
			}
			if mode == "season-page-played" {
				waitPreloadPendingJobs(t, h, playback.PreloadDetail)
				if len(h.resolved) != 0 {
					t.Fatal("a played explicit season still resolved its old first episode")
				}
			} else if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("seasons refresh resolved %q, want %q", got.path, want.MediaSources[0].Path)
			}
			if h.metadata.Load() != calls {
				t.Fatalf("metadata calls=%d, want=%d", h.metadata.Load(), calls)
			}
		})
	}
}

func TestPreloadParentRefreshDoesNotRenewPendingSource(t *testing.T) {
	for _, age := range []time.Duration{30 * time.Second, 61 * time.Second} {
		runEmbyPreloadCase(t, age.String(), func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			current, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			current.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
			fresh := preloadTestItem("21", "Episode")
			fresh.MediaSources[0].Path += "&fresh=1"
			h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
			h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{next}}
			h.infos[current.ID] = preloadPlaybackInfo{MediaSources: current.MediaSources}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/10/Items/20":
					_ = json.NewEncoder(w).Encode(current)
				case "/Users/10/Items/21":
					_ = json.NewEncoder(w).Encode(fresh)
				default:
					t.Errorf("unexpected metadata after parent refresh: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}
			h.request("GET", "/Shows/NextUp?UserId=10&SeriesId=90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			if got := h.waitResolved(); got.path != next.MediaSources[0].Path {
				t.Fatalf("initial pending source=%q", got.path)
			}
			h.adapter.mu.Lock()
			for _, device := range h.adapter.devices {
				device.detailAt = time.Now().Add(-age)
				device.pendingLists[1].at = device.detailAt
				device.pendingTarget.metadataAt = device.detailAt
			}
			h.adapter.mu.Unlock()
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Items/20/PlaybackInfo?UserId=10&MediaSourceId=source-20", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Control/1", nil)
			h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]string{"ItemId": "20", "MediaSourceId": "source-20"})
			h.request("POST", "/Sessions/Playing/Progress", "device", "Fixture App", "Control/1", map[string]any{
				"ItemId": "20", "MediaSourceId": "source-20", "PositionTicks": 54 * 60 * 10_000_000, "IsPaused": false,
				"NowPlayingQueue": []map[string]string{{"Id": "20"}, {"Id": "21"}},
			})
			want, calls := next, int32(1)
			if age > preloadAssociationTTL {
				want, calls = fresh, 2
			}
			if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("successor source=%q, want=%q", got.path, want.MediaSources[0].Path)
			}
			if got := h.metadata.Load(); got != calls {
				t.Fatalf("metadata=%d, want=%d", got, calls)
			}
		})
	}
}

func TestPreloadPendingSourceExpirySurvivesNaturalEnrichment(t *testing.T) {
	for _, age := range []time.Duration{30 * time.Second, 61 * time.Second} {
		for _, kind := range []string{"episodes", "resume", "playback"} {
			runEmbyPreloadCase(t, age.String()+"/"+kind, func(t *testing.T) {
				h := newEmbyPreloadTestHarness(t)
				next := preloadTestItem("21", "Episode")
				next.ParentIndexNumber, next.IndexNumber = 1, 2
				h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
				h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{next}}
				h.request("GET", "/Shows/NextUp?UserId=10&SeriesId=90", "device", "Fixture App", "Control/1", nil)
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
				h.waitResolved()
				// 隔离自然资料合并，避免后台补查掩盖过期地址的回填。
				entered := make(chan struct{})
				h.core.Submit(playback.PreloadCandidate{Key: "hold-detail", Scope: "hold-detail", Kind: playback.PreloadDetail, Run: func(ctx context.Context, _ *playback.PreloadTask) playback.PreloadResult {
					close(entered)
					<-ctx.Done()
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}})
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("metadata worker was not isolated")
				}
				var request preloadRequest
				h.adapter.mu.Lock()
				for scope, device := range h.adapter.devices {
					device.detailAt = time.Now().Add(-age)
					device.pendingLists[1].at = device.detailAt
					device.pendingTarget.metadataAt = device.detailAt
					request = preloadRequest{scope: scope, user: device.user, ua: device.controlUA, client: device.client, controlVersion: device.controlVersion}
				}
				h.adapter.mu.Unlock()
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
				omitted := next
				omitted.MediaSources = nil
				switch kind {
				case "episodes":
					h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{omitted}, TotalRecordCount: 1}
					h.request("GET", "/Shows/90/Episodes?UserId=10&SeasonId=91", "device", "Fixture App", "Control/1", nil)
				case "resume":
					h.lists["/Users/10/Items/Resume"] = preloadItems{Items: []preloadItem{omitted}}
					h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
				case "playback":
					sources := preloadTestItem("21", "Episode").MediaSources
					sources[0].Path = ""
					h.infos[next.ID] = preloadPlaybackInfo{MediaSources: sources}
					h.request("GET", "/Items/21/PlaybackInfo?UserId=10", "device", "Fixture App", "Control/1", nil)
				}
				h.adapter.mu.Lock()
				observed := time.Since(h.adapter.devices[request.scope].pendingTarget.metadataAt) < time.Second
				h.adapter.mu.Unlock()
				if !observed {
					t.Fatal("natural response did not update pending metadata")
				}
				job := embyPreloadJob{owner: h.adapter, request: request}
				item, _ := job.cachedItem(next.ID)
				source, _ := selectPreloadLinkSource(item, "")
				want := next.MediaSources[0].Path
				if age > preloadAssociationTTL {
					want = ""
				}
				if source.Path != want {
					t.Fatalf("natural enrichment source=%q, want=%q", source.Path, want)
				}
				if h.metadata.Load() != 0 {
					t.Fatal("background lookup masked natural metadata reuse")
				}
			})
		}
	}
}

func TestPreloadLaterSourceLookupKeepsNewerWatchState(t *testing.T) {
	for _, mode := range []string{"played", "unplayed"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, played, count, position := false, true, 0, int64(0)
			initial, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			initial.ParentIndexNumber, initial.IndexNumber = 1, 1
			initial.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			initial.MediaSources[0].Path = ""
			initial.MediaSources[0].Container, initial.MediaSources[0].RunTimeTicks = "mkv", 1000
			initial.MediaSources[0].MediaStreams = []preloadStream{{Type: "Video", Codec: "h264"}, {Type: "Audio", Codec: "aac"}}
			fresh := preloadTestItem("20", "Episode")
			fresh.ParentIndexNumber, fresh.IndexNumber = 1, 1
			h.nodes["20"], h.nodes["90"] = initial, preloadItem{ID: "90", Type: "Series"}
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Items/20/PlaybackInfo":
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: fresh.MediaSources})
				case "/Users/10/Items/Resume":
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{next}})
				default:
					t.Errorf("unexpected metadata: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			r := httptest.NewRequest(http.MethodGet, "/Users/10/Items/20", nil)
			r.Header.Set(HeaderFullAuthName, `MediaBrowser Client="Fixture App", DeviceId="device", Token="user-token"`)
			r.Header.Set("User-Agent", "Control/1")
			refresh, ok := newPreloadRequest(r, t.Name(), "", "", 1)
			refresh.item = "20"
			if !ok || !h.adapter.beginObservation(&refresh, "detail", nil) {
				t.Fatal("watch-state refresh was not admitted")
			}
			<-entered
			watch := initial.UserData
			if mode == "played" {
				watch = &preloadUserData{Played: &played}
			}
			h.adapter.observeDetail(refresh, preloadItem{ID: "20", Type: "Episode", SeriesID: "90", UserData: watch})
			release <- struct{}{}
			if got := h.waitResolved(); got.path != fresh.MediaSources[0].Path {
				t.Fatalf("later source lookup resolved %q", got.path)
			}
			omitted := fresh
			omitted.MediaSources = nil
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{omitted}, TotalRecordCount: 1}
			h.request("GET", "/Shows/90/Episodes?UserId=10", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			want, calls := fresh, int32(1)
			if mode == "played" {
				want, calls = next, 2
			}
			if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("parent selected=%q, want=%q after the watch-state refresh", got.path, want.MediaSources[0].Path)
			}
			if got := h.metadata.Load(); got != calls {
				t.Fatalf("metadata=%d, want=%d", got, calls)
			}
		})
	}
}

func TestPreloadCompleteSeasonsSurvivePlayback(t *testing.T) {
	for _, mode := range []string{"none", "start", "stop"} {
		for _, snapshot := range []string{"complete", "played", "normal", "filtered", "page-offset", "expired"} {
			runEmbyPreloadCase(t, mode+"/"+snapshot, func(t *testing.T) {
				h := newEmbyPreloadTestHarness(t)
				unplayed, played := false, true
				season := preloadItem{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 0, UserData: &preloadUserData{Played: &unplayed}}
				if snapshot == "normal" {
					season.ID, season.IndexNumber = "91", 1
				} else if snapshot == "played" {
					season.UserData = &preloadUserData{Played: &played}
				}
				current, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
				current.SeasonID, current.ParentIndexNumber = season.ID, season.IndexNumber
				next.SeasonID, next.ParentIndexNumber = season.ID, season.IndexNumber
				next.UserData = &preloadUserData{Played: &unplayed}
				h.nodes["20"], h.nodes["90"] = current, preloadItem{ID: "90", Type: "Series"}
				h.lists["/Shows/90/Seasons"] = preloadItems{Items: []preloadItem{season}, TotalRecordCount: 1}
				h.upstream = func(w http.ResponseWriter, r *http.Request) {
					result := preloadItems{Items: []preloadItem{}}
					switch r.URL.Path {
					case "/Users/10/Items/Resume", "/Shows/NextUp":
					case "/Shows/90/Seasons":
						if r.URL.Query().Get("IsPlayed") == "" || snapshot == "normal" {
							fresh := season
							if mode != "none" {
								fresh.UserData = &preloadUserData{Played: &unplayed}
							}
							result = preloadItems{Items: []preloadItem{fresh}, TotalRecordCount: 1}
						}
					case "/Shows/90/Episodes":
						if r.URL.Query().Get("SeasonId") != season.ID || r.URL.Query().Get("IsPlayed") != "false" {
							t.Errorf("first lookup lost the actual season: %s?%s", r.URL.Path, r.URL.RawQuery)
						}
						result.Items = []preloadItem{next}
					default:
						t.Errorf("unexpected metadata: %s", r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(result)
				}
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
				h.waitResolved()
				path := "/Shows/90/Seasons?UserId=10"
				if snapshot == "filtered" {
					path += "&IsPlayed=false"
				} else if snapshot == "page-offset" {
					path += "&StartIndex=1&Limit=1"
				}
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
				synctest.Sleep(30 * time.Second)
				if mode != "none" {
					stream := h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Control/1", nil)
					if stream.Code != http.StatusTemporaryRedirect {
						t.Fatalf("stream status=%d", stream.Code)
					}
					start := h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]string{"ItemId": "20", "MediaSourceId": "source-20"})
					if start.Code != http.StatusNoContent {
						t.Fatalf("playing status=%d", start.Code)
					}
				}
				if mode == "stop" {
					stop := h.request("POST", "/Sessions/Playing/Stopped", "device", "Fixture App", "Control/1", map[string]string{"ItemId": "20", "MediaSourceId": "source-20"})
					if stop.Code != http.StatusNoContent {
						t.Fatalf("stopped status=%d", stop.Code)
					}
				}
				if snapshot == "expired" {
					synctest.Sleep(31 * time.Second)
				}
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
				calls := int32(3)
				if snapshot == "filtered" || snapshot == "page-offset" || snapshot == "expired" {
					calls = 5
				} else if snapshot == "normal" && mode != "none" {
					calls = 4
				}
				if snapshot == "played" && mode == "none" {
					calls = 2
					waitPreloadPendingJobs(t, h, playback.PreloadDetail)
					if len(h.resolved) != 0 {
						t.Fatal("uninvalidated watched special season selected a first episode")
					}
				} else if got := h.waitResolved(); got.path != next.MediaSources[0].Path || got.ua != "Control/1" || !got.force {
					t.Fatalf("selected=%+v, want source=%q", got, next.MediaSources[0].Path)
				}
				if got := h.metadata.Load(); got != calls {
					t.Fatalf("metadata=%d, want=%d after %s with %s Seasons", got, calls, mode, snapshot)
				}
			})
		}
	}
}

func TestPreloadEpisodesDoNotRestoreInvalidatedUserData(t *testing.T) {
	for _, mode := range []string{"fresh", "played", "late-episodes", "late-detail"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, count, position := false, 0, int64(0)
			first, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			first.ParentIndexNumber, first.IndexNumber = 1, 1
			next.ParentIndexNumber, next.IndexNumber = 1, 2
			first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			next.UserData = first.UserData
			h.nodes[first.ID], h.nodes["90"] = first, preloadItem{ID: "90", Type: "Series"}
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first, next}, TotalRecordCount: 2}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/Resume" {
					t.Errorf("unexpected pending lookup: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{next}})
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/90/Episodes?UserId=10", "device", "Fixture App", "Control/1", nil)
			var late preloadRequest
			if mode == "late-episodes" || mode == "late-detail" {
				path, kind := "/Shows/90/Episodes?UserId=10", "episodes"
				if mode == "late-detail" {
					path, kind = "/Users/10/Items/20", "detail"
				}
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Header.Set(HeaderFullAuthName, `MediaBrowser Client="Fixture App", DeviceId="device", Token="user-token"`)
				r.Header.Set("User-Agent", "Control/1")
				var ok bool
				late, ok = newPreloadRequest(r, t.Name(), "", "", 1)
				if mode == "late-episodes" {
					late.item = "90"
				} else {
					late.item = "20"
				}
				if !ok || !h.adapter.beginObservation(&late, kind, nil) {
					t.Fatal("late observation was not admitted")
				}
			}
			if mode != "fresh" {
				h.request("GET", "/Videos/20/stream?MediaSourceId=source-20", "device", "Fixture App", "Control/1", nil)
				h.request("POST", "/Sessions/Playing", "device", "Fixture App", "Control/1", map[string]string{"ItemId": "20", "MediaSourceId": "source-20"})
				h.request("POST", "/Sessions/Playing/Stopped", "device", "Fixture App", "Control/1", map[string]string{"ItemId": "20", "MediaSourceId": "source-20"})
			}
			if mode == "late-episodes" {
				data, err := json.Marshal(h.lists["/Shows/90/Episodes"])
				if err != nil {
					t.Fatal(err)
				}
				h.adapter.observeSeasonMembers(late, nil, data)
			} else if mode == "late-detail" {
				h.adapter.observeDetail(late, first)
			}
			omitted := []preloadItem{first, next}
			omitted[0].UserData, omitted[1].UserData = nil, nil
			omitted[0].MediaSources, omitted[1].MediaSources = nil, nil
			h.mu.Lock()
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: omitted, TotalRecordCount: 2}
			h.mu.Unlock()
			h.request("GET", "/Shows/90/Episodes?UserId=10&EnableUserData=false", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			want, calls := next, int32(1)
			if mode == "fresh" {
				want, calls = first, 0
			}
			if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("omitted UserData selected=%q, want=%q (metadata=%d)", got.path, want.MediaSources[0].Path, h.metadata.Load())
			}
			if got := h.metadata.Load(); got != calls {
				t.Fatalf("metadata=%d, want=%d", got, calls)
			}
		})
	}
}

func TestPreloadNewerSeasonWatchStatePrecedesEpisodeHead(t *testing.T) {
	for _, mode := range []string{"newer", "older", "other-series", "sort-name", "season-alias", "explicit-nextup"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, played, count, position := false, true, 0, int64(0)
			first, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			first.ParentIndexNumber, first.IndexNumber = 1, 1
			first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			next.SeasonID, next.ParentIndexNumber, next.UserData = "92", 2, first.UserData
			if mode == "season-alias" {
				first.SeasonID = "89"
			}
			seasons := []preloadItem{
				{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: &preloadUserData{Played: &played}},
				{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 2, UserData: first.UserData},
			}
			h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
			h.lists["/Shows/90/Seasons"] = preloadItems{Items: seasons, TotalRecordCount: 2}
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
			h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{first}}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				items := []preloadItem{}
				switch r.URL.Path {
				case "/Users/10/Items/Resume", "/Shows/NextUp":
				case "/Shows/90/Seasons":
					items = append(items, seasons[1])
				case "/Shows/90/Episodes":
					if r.URL.Query().Get("SeasonId") != "92" {
						t.Error("newer seasons did not supersede the old episode head")
					}
					items = append(items, next)
				default:
					t.Errorf("unexpected metadata: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
			}
			path := "/Shows/90/Seasons?UserId=10"
			if mode == "other-series" {
				path = "/Shows/99/Seasons?UserId=10"
				h.lists["/Shows/99/Seasons"] = preloadItems{Items: []preloadItem{{ID: "99", Type: "Season", SeriesID: "99", IndexNumber: 1, UserData: seasons[0].UserData}}}
			} else if mode == "sort-name" {
				path += "&SortBy=SortName"
			}
			if mode == "older" {
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Shows/90/Episodes?UserId=10", "device", "Fixture App", "Control/1", nil)
			if mode == "explicit-nextup" {
				h.request("GET", "/Shows/NextUp?UserId=10&SeriesId=90", "device", "Fixture App", "Control/1", nil)
			}
			if mode != "older" {
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			want, calls := next, int32(3)
			if mode == "older" || mode == "other-series" || mode == "explicit-nextup" {
				want, calls = first, 0
			} else if mode == "sort-name" {
				calls = 4
			}
			if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("selected=%q, want=%q (metadata=%d)", got.path, want.MediaSources[0].Path, h.metadata.Load())
			}
			if got := h.metadata.Load(); got != calls {
				t.Fatalf("metadata=%d, want=%d", got, calls)
			}
		})
	}
}

func TestPreloadSameEmptyListsKeepInFlightMetadataBudget(t *testing.T) {
	runEmbyPreloadTest(t, func(t *testing.T) {
		h := newEmbyPreloadTestHarness(t)
		h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
		h.lists["/Users/10/Items/Resume"], h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{}}, preloadItems{Items: []preloadItem{}}
		first := preloadTestItem("20", "Episode")
		first.SeasonID, first.ParentIndexNumber, first.IndexNumber = "92", 0, 3
		descriptor := first
		descriptor.MediaSources = slices.Clone(first.MediaSources)
		descriptor.MediaSources[0].Path, descriptor.MediaSources[0].Container = "", "mkv"
		descriptor.MediaSources[0].RunTimeTicks = 60 * 60 * 10_000_000
		descriptor.MediaSources[0].MediaStreams = []preloadStream{{Type: "Video", Codec: "h264"}, {Type: "Audio", Codec: "aac"}}
		entered, release := make(chan struct{}), make(chan struct{}, 1)
		t.Cleanup(func() { close(release) })
		var mu sync.Mutex
		var queries []string
		h.upstream = func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			queries = append(queries, r.URL.Path)
			mu.Unlock()
			switch r.URL.Path {
			case "/Users/10/Items/Resume", "/Shows/NextUp":
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{}})
			case "/Shows/90/Seasons":
				items := []preloadItem{}
				if r.URL.Query().Get("IsPlayed") != "" {
					if h.metadata.Load() == 3 {
						close(entered)
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
				} else {
					items = append(items, preloadItem{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 0})
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: items, TotalRecordCount: len(items)})
			case "/Shows/90/Episodes":
				value := first
				value.MediaSources = nil
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{value}})
			case "/Users/10/Items/20":
				_ = json.NewEncoder(w).Encode(descriptor)
			case "/Items/20/PlaybackInfo":
				_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: first.MediaSources})
			default:
				t.Errorf("unexpected metadata: %s", r.URL.Path)
				w.WriteHeader(http.StatusBadRequest)
			}
		}
		h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("first season lookup did not start")
		}
		h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
		release <- struct{}{}
		if got := h.waitResolved(); got.path != first.MediaSources[0].Path {
			t.Fatalf("same empty results selected %q", got.path)
		}
		mu.Lock()
		got := slices.Clone(queries)
		mu.Unlock()
		want := []string{"/Users/10/Items/Resume", "/Shows/NextUp", "/Shows/90/Seasons", "/Shows/90/Seasons", "/Shows/90/Episodes", "/Users/10/Items/20", "/Items/20/PlaybackInfo"}
		if !slices.Equal(got, want) || h.metadata.Load() != 7 {
			t.Fatalf("same empty results restarted metadata work: got=%v want=%v", got, want)
		}
	})
}

func TestPreloadFirstEpisodeRechecksSeasonsAfterInFlightLookup(t *testing.T) {
	for _, test := range []struct {
		name             string
		seasonPage       bool
		played           bool
		filtered         bool
		cachedIncomplete bool
		wantID           string
		calls            int32
	}{
		{name: "series-played", played: true, wantID: "21", calls: 7},
		{name: "season-played", seasonPage: true, played: true, calls: 5},
		{name: "same-season", wantID: "20", calls: 4},
		{name: "played-filter", played: true, filtered: true, wantID: "21", calls: 8},
		{name: "cached-incomplete-head", played: true, cachedIncomplete: true, wantID: "21", calls: 7},
		{name: "same-incomplete-head", cachedIncomplete: true, wantID: "20", calls: 4},
	} {
		runEmbyPreloadCase(t, test.name, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, played := false, true
			first, next := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
			first.ParentIndexNumber, first.IndexNumber, first.UserData = 1, 1, &preloadUserData{Played: &unplayed}
			next.SeasonID, next.ParentIndexNumber, next.IndexNumber, next.UserData = "92", 2, 1, first.UserData
			season := preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: first.UserData}
			parent := preloadItem{ID: "90", Type: "Series"}
			if test.seasonPage {
				parent = season
			}
			h.nodes[parent.ID] = parent
			if test.cachedIncomplete {
				cached := first
				cached.ParentIndexNumber = nil
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{cached}, TotalRecordCount: 1}
				h.request("GET", "/Shows/90/Episodes?UserId=10&SeasonId=91", "device", "Fixture App", "Control/1", nil)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			last := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			var once sync.Once
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if h.metadata.Load() == test.calls {
					defer close(last)
				}
				result := preloadItems{Items: []preloadItem{}}
				switch r.URL.Path {
				case "/Users/10/Items/Resume", "/Shows/NextUp":
				case "/Shows/90/Seasons":
					value := season
					if h.metadata.Load() > 4 {
						value.ID, value.IndexNumber = "92", 2
					}
					result.Items = []preloadItem{value}
				case "/Shows/90/Episodes":
					value := next
					if r.URL.Query().Get("SeasonId") == "91" {
						value = first
						once.Do(func() { close(entered) })
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					} else if r.URL.Query().Get("SeasonId") != "92" {
						t.Errorf("unexpected selected season %q", r.URL.Query().Get("SeasonId"))
					}
					result.Items = []preloadItem{value}
				default:
					t.Errorf("unexpected metadata %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(result)
			}
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first episode lookup did not start")
			}
			seasons := []preloadItem{season, {ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 2, UserData: first.UserData}}
			if test.played {
				seasons[0].UserData = &preloadUserData{Played: &played}
			}
			path := "/Shows/90/Seasons?UserId=10"
			if test.filtered {
				path += "&IsPlayed=true"
				seasons = seasons[:1]
			}
			h.mu.Lock()
			h.lists["/Shows/90/Seasons"] = preloadItems{Items: seasons, TotalRecordCount: len(seasons)}
			h.mu.Unlock()
			h.request("GET", path, "device", "Fixture App", "Control/1", nil)
			unblock()
			if test.wantID == "" {
				select {
				case <-last:
				case <-time.After(5 * time.Second):
					t.Fatal("updated season selection did not finish its metadata queries")
				}
				waitPreloadPendingJobs(t, h, playback.PreloadDetail)
				if len(h.resolved) != 0 {
					t.Fatal("played season's in-flight first episode was resolved")
				}
			} else if got := h.waitResolved(); got.path != preloadTestItem(test.wantID, "Episode").MediaSources[0].Path {
				t.Fatalf("in-flight lookup resolved %q, want episode %s", got.path, test.wantID)
			}
			if got := h.metadata.Load(); got != test.calls {
				t.Fatalf("metadata calls=%d, want=%d", got, test.calls)
			}
		})
	}
}

func TestPreloadFirstEpisodeMergesLateMatchingMetadata(t *testing.T) {
	for _, kind := range []string{"Seasons", "Episodes"} {
		runEmbyPreloadCase(t, kind, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed := false
			first := preloadTestItem("20", "Episode")
			first.ParentIndexNumber, first.IndexNumber, first.UserData = 1, 1, &preloadUserData{Played: &unplayed}
			season := preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: first.UserData}
			h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Shows/90/"+kind {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				result := preloadItems{Items: []preloadItem{}}
				switch r.URL.Path {
				case "/Users/10/Items/Resume", "/Shows/NextUp":
				case "/Shows/90/Seasons":
					result.Items = []preloadItem{season}
				case "/Shows/90/Episodes":
					result.Items = []preloadItem{first}
				default:
					t.Errorf("completed list metadata was not reused: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(result)
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("background list lookup did not start")
			}
			path := "/Shows/90/" + kind + "?UserId=10&EnableUserData=false"
			value := season
			if kind == "Episodes" {
				path += "&SeasonId=91&IsPlayed=false"
				value = first
				value.MediaSources = slices.Clone(first.MediaSources)
				value.MediaSources[0].Path = ""
			}
			value.UserData = nil
			h.mu.Lock()
			h.lists["/Shows/90/"+kind] = preloadItems{Items: []preloadItem{value}, TotalRecordCount: 1}
			h.mu.Unlock()
			h.request("GET", path, "device", "Fixture App", "Control/1", nil)
			unblock()
			if got := h.waitResolved(); got.path != first.MediaSources[0].Path {
				t.Fatalf("merged list resolved %q", got.path)
			}
			if got := h.metadata.Load(); got != 4 {
				t.Fatalf("metadata calls=%d, want=4", got)
			}
		})
	}
}
