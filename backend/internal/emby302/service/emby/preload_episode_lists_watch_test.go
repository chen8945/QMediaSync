package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"qmediasync/internal/playback"
)

func TestPreloadSeasonDetailWatchStatePrecedesEpisodeHead(t *testing.T) {
	for _, mode := range []string{"played", "unplayed", "omitted", "explicit-resume", "explicit-nextup"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, played, count, position := false, true, 0, int64(0)
			first := preloadTestItem("20", "Episode")
			first.ParentIndexNumber = 1
			first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			parent := preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1}
			if mode != "omitted" {
				parent.UserData = &preloadUserData{Played: &played}
				if mode == "unplayed" {
					parent.UserData.Played = &unplayed
				}
			}
			h.nodes["91"] = parent
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/Resume" && r.URL.Path != "/Shows/NextUp" {
					t.Errorf("watched season triggered metadata: %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{}})
			}
			h.request("GET", "/Shows/90/Episodes?UserId=10&SeasonId=91", "device", "Fixture App", "Control/1", nil)
			if mode == "explicit-resume" {
				h.lists["/Users/10/Items/Resume"] = preloadItems{Items: []preloadItem{first}}
				h.request("GET", "/Users/10/Items/Resume?ParentId=91", "device", "Fixture App", "Control/1", nil)
			} else if mode == "explicit-nextup" {
				h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{first}}
				h.request("GET", "/Shows/NextUp?UserId=10&SeriesId=90&ParentId=91", "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
			waitPreloadPendingJobs(t, h, playback.PreloadDetail)
			calls := int32(0)
			if mode == "played" {
				calls = 2
				if len(h.resolved) != 0 {
					t.Fatal("newer watched season detail reused its old first episode")
				}
			} else if got := h.waitResolved(); got.path != first.MediaSources[0].Path {
				t.Fatalf("selected=%q, want=%q", got.path, first.MediaSources[0].Path)
			}
			if got := h.metadata.Load(); got != calls {
				t.Fatalf("metadata=%d, want=%d", got, calls)
			}
		})
	}
}

func TestPreloadSeasonDetailRevokesFirstSignature(t *testing.T) {
	for _, mode := range []string{"played", "unchanged", "explicit-nextup"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, played, count, position := false, true, 0, int64(0)
			first := preloadTestItem("20", "Episode")
			first.ParentIndexNumber = 1
			first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			parent := preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: first.UserData}
			h.nodes["91"] = parent
			h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
			h.lists["/Users/10/Items/Resume"], h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{}}, preloadItems{Items: []preloadItem{}}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/Resume" && r.URL.Path != "/Shows/NextUp" {
					t.Errorf("watched season triggered metadata: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{}})
			}
			if mode == "explicit-nextup" {
				h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{first}}
			}
			entered, release, published := make(chan *playback.PreloadTask, 1), make(chan struct{}, 1), make(chan bool, 1)
			t.Cleanup(func() { close(release) })
			resolve := h.adapter.options.Resolve
			h.adapter.options.Resolve = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				key, run, supported := resolve(raw, ua, force)
				return key, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, key) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					entered <- task
					select {
					case <-ctx.Done():
					case <-release:
					}
					published <- task.Publish(func() bool {
						run(ctx, task)
						return true
					})
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}, supported
			}
			h.request("GET", "/Shows/90/Episodes?UserId=10&SeasonId=91", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/Resume?ParentId=91", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?UserId=10&SeriesId=90&ParentId=91", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
			var task *playback.PreloadTask
			select {
			case task = <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first episode did not enter signature preparation")
			}
			if mode != "unchanged" {
				parent.UserData = &preloadUserData{Played: &played}
			}
			h.nodes["91"] = parent
			h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
			want := mode != "played"
			if task.Valid() != want {
				t.Fatalf("old task valid=%v, want=%v", task.Valid(), want)
			}
			release <- struct{}{}
			select {
			case allowed := <-published:
				if allowed != want {
					t.Fatalf("old first episode published=%v, want=%v", allowed, want)
				}
			case <-time.After(time.Second):
				t.Fatal("old signature preparation did not finish")
			}
			waitPreloadPendingJobs(t, h, playback.PreloadDetail)
			if !want && len(h.resolved) != 0 {
				t.Fatal("watched season published its revoked first episode")
			}
		})
	}
}

func TestPreloadSeasonDetailStopsInFlightFirstLookup(t *testing.T) {
	runEmbyPreloadTest(t, func(t *testing.T) {
		h := newEmbyPreloadTestHarness(t)
		unplayed, played := false, true
		parent := preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: &preloadUserData{Played: &unplayed}}
		first := preloadTestItem("20", "Episode")
		first.UserData = parent.UserData
		h.nodes["91"] = parent
		h.lists["/Users/10/Items/Resume"], h.lists["/Shows/NextUp"] = preloadItems{Items: []preloadItem{}}, preloadItems{Items: []preloadItem{}}
		entered, release := make(chan struct{}), make(chan struct{}, 1)
		t.Cleanup(func() { close(release) })
		h.upstream = func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/Shows/90/Episodes" || r.URL.Query().Get("SeasonId") != "91" {
				t.Errorf("unexpected metadata: %s?%s", r.URL.Path, r.URL.RawQuery)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
			_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{first}})
		}
		h.request("GET", "/Users/10/Items/Resume?ParentId=91", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Shows/NextUp?UserId=10&SeriesId=90&ParentId=91", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("first-episode lookup did not start")
		}
		parent.UserData = &preloadUserData{Played: &played}
		h.nodes["91"] = parent
		h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
		release <- struct{}{}
		waitPreloadPendingJobs(t, h, playback.PreloadDetail)
		if len(h.resolved) != 0 {
			t.Fatal("in-flight first lookup ignored newer watched season detail")
		}
	})
}

func TestPreloadSeasonIDInvalidatesWatchProofWithoutSeasonNumber(t *testing.T) {
	runEmbyPreloadTest(t, func(t *testing.T) {
		h := newEmbyPreloadTestHarness(t)
		unplayed, played, count, position := false, true, 0, int64(0)
		first, resume := preloadTestItem("20", "Episode"), preloadTestItem("21", "Episode")
		first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
		resume.SeasonID, resume.ParentIndexNumber = "92", 2
		h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
		h.lists["/Shows/90/Episodes"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
		h.lists["/Shows/90/Seasons"] = preloadItems{Items: []preloadItem{
			{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1, UserData: &preloadUserData{Played: &played}},
			{ID: "92", Type: "Season", SeriesID: "90", IndexNumber: 2, UserData: first.UserData},
		}, TotalRecordCount: 2}
		h.upstream = func(w http.ResponseWriter, r *http.Request) {
			items := []preloadItem{resume}
			if r.URL.Path != "/Users/10/Items/Resume" {
				t.Errorf("ignored current Resume after season watch change: %s", r.URL.Path)
			}
			_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
		}
		h.request("GET", "/Shows/90/Episodes?UserId=10", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Shows/90/Seasons?UserId=10", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
		if got := h.waitResolved(); got.path != resume.MediaSources[0].Path {
			t.Fatalf("selected=%q, want current Resume=%q", got.path, resume.MediaSources[0].Path)
		}
		if got := h.metadata.Load(); got != 1 {
			t.Fatalf("metadata=%d, want one Resume lookup", got)
		}
	})
}

func TestPreloadSeasonItemsParentParameterCase(t *testing.T) {
	for _, query := range []string{"ParentId=91", "parentId=91", "PARENTID=91", "parentId=91&ParentId=91"} {
		runEmbyPreloadCase(t, query, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			unplayed, count, position := false, 0, int64(0)
			first := preloadTestItem("20", "Episode")
			first.ParentIndexNumber = 1
			first.UserData = &preloadUserData{Played: &unplayed, PlayCount: &count, PlaybackPositionTicks: &position}
			h.nodes["91"] = preloadItem{ID: "91", Type: "Season", SeriesID: "90", IndexNumber: 1}
			h.lists["/Users/10/Items"] = preloadItems{Items: []preloadItem{first}, TotalRecordCount: 1}
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				items := []preloadItem{}
				if r.URL.Path == "/Shows/90/Episodes" {
					items = append(items, first)
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
			}
			h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items?"+query+"&IncludeItemTypes=Episode&SortBy=IndexNumber", "device", "Fixture App", "Control/1", nil)
			if got := h.waitResolved(); got.path != first.MediaSources[0].Path {
				t.Fatalf("selected=%q, want=%q", got.path, first.MediaSources[0].Path)
			}
			if got := h.metadata.Load(); got != 0 {
				t.Fatalf("case-insensitive ParentId lost cached first episode: metadata=%d", got)
			}
		})
	}
}
