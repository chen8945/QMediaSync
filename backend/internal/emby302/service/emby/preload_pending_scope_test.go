package emby

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestPreloadSeasonExcludedPendingCandidateFallsBackToFirst(t *testing.T) {
	for _, test := range []struct {
		name, response, want           string
		cached, later, unknown, played bool
		status                         int
		calls                          int32
	}{
		{name: "cached-first", cached: true, want: "21", calls: 3},
		{name: "query-first", want: "21", calls: 4},
		{name: "later-confirmed", cached: true, later: true, want: "22", calls: 1},
		{name: "later-confirmed-after-unknown", cached: true, later: true, unknown: true, calls: 1},
		{name: "scoped-other-item", cached: true, response: "other", want: "21", calls: 3},
		{name: "verification-forbidden", cached: true, status: http.StatusForbidden, calls: 3},
		{name: "verification-rate-limited", cached: true, status: http.StatusTooManyRequests, calls: 3},
		{name: "verification-transient-failure", cached: true, status: http.StatusServiceUnavailable, calls: 4},
		{name: "verification-malformed-json", cached: true, response: "json", calls: 3},
		{name: "verification-missing-items", cached: true, response: "missing", calls: 3},
		{name: "verification-malformed-item", cached: true, response: "invalid", calls: 3},
		{name: "verification-denied-item", cached: true, response: "denied", calls: 3},
		{name: "first-played", played: true, calls: 4},
	} {
		runEmbyPreloadCase(t, test.name, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			parent := preloadItem{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 2}
			h.nodes[parent.ID] = parent
			pending, first, later := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode"), preloadTestItem("22", "Episode")
			pending.ParentID, pending.ParentIndexNumber, pending.IndexNumber = "91", 1, 3
			first.SeasonID, first.ParentID, first.ParentIndexNumber, first.IndexNumber = parent.ID, parent.ID, 2, 1
			later.SeasonID, later.ParentID, later.ParentIndexNumber, later.IndexNumber = parent.ID, parent.ID, 2, 2
			first.UserData = &preloadUserData{Played: &test.played}
			if test.cached {
				h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}}
				h.request("GET", "/Shows/90/Episodes?UserId=10&SeasonId=92&ParentIndexNumber=2&IsPlayed=false&Limit=1", "device", "Fixture App", "Control/1", nil)
			}
			if test.later {
				h.lists["/Users/10/Items/Resume"] = preloadItems{Items: []preloadItem{}}
				items := []preloadItem{pending, later}
				if test.unknown {
					unknown := preloadTestItem("23", "Episode")
					unknown.SeasonID = ""
					items = []preloadItem{pending, unknown, later}
				}
				h.lists["/Shows/NextUp"] = preloadItems{Items: items}
				h.request("GET", "/Users/10/Items/Resume?ParentId=92", "device", "Fixture App", "Control/1", nil)
				h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10&Limit=30", "device", "Fixture App", "Control/1", nil)
			}
			verified := make(chan struct{}, 1)
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				result := preloadItems{Items: []preloadItem{}}
				switch r.URL.Path {
				case "/Users/10/Items/Resume":
					if r.URL.Query().Get("ParentId") != parent.ID {
						t.Error("resume query lost its season scope")
					}
				case "/Shows/NextUp":
					if r.URL.Query().Get("SeriesId") != parent.SeriesID {
						t.Error("nextup query lost its series scope")
					}
					result.Items = []preloadItem{pending}
				case "/Shows/90/Episodes":
					if r.URL.Query().Get("SeasonId") != parent.ID || r.URL.Query().Get("Fields") != preloadItemFields {
						t.Error("episodes query lost its season scope or metadata fields")
					}
					if r.URL.Query().Get("StartItemId") == pending.ID {
						defer func() {
							select {
							case verified <- struct{}{}:
							default:
							}
						}()
						if test.status != 0 {
							w.WriteHeader(test.status)
							return
						}
						switch test.response {
						case "json":
							_, _ = w.Write([]byte(`{"Items":`))
							return
						case "missing":
							result.Items = nil
						case "invalid":
							result.Items = []preloadItem{{Type: "Episode"}}
						case "denied":
							denied := pending
							denied.SeasonID, denied.ParentID, denied.PlayAccess = parent.ID, parent.ID, "None"
							result.Items = []preloadItem{denied}
						case "other":
							result.Items = []preloadItem{first}
						}
					} else {
						if r.URL.Query().Get("StartItemId") != "" || r.URL.Query().Get("IsPlayed") != "false" || r.URL.Query().Get("ParentIndexNumber") != "2" || r.URL.Query().Get("Limit") != "1" {
							t.Error("fallback did not use the existing unplayed first-episode query")
						}
						result.Items = []preloadItem{first}
					}
				default:
					t.Errorf("unexpected pending scope query %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(result)
			}
			h.request("GET", "/Users/10/Items/92", "device", "Fixture App", "Control/1", nil)
			select {
			case <-verified:
			case <-time.After(5 * time.Second):
				t.Fatal("series candidate was not checked against the opened season")
			}
			waitPreloadPendingJobs(t, h)
			if test.want == "" {
				if len(h.resolved) != 0 {
					t.Fatal("unconfirmed scope or played first episode reached source resolution")
				}
			} else {
				want := first
				if test.want == later.ID {
					want = later
				}
				if got := h.waitResolved(); got.path != want.MediaSources[0].Path || got.ua != "Control/1" {
					t.Fatalf("season resolved=%+v, want=%q", got, want.MediaSources[0].Path)
				}
			}
			if h.metadata.Load() != test.calls {
				t.Fatalf("season metadata calls=%d, want=%d", h.metadata.Load(), test.calls)
			}
		})
	}
}
