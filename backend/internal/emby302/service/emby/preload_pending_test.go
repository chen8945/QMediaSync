package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"qmediasync/internal/playback"

	"github.com/gin-gonic/gin"
)

func TestPreloadPendingNaturalLists(t *testing.T) {
	for _, order := range []string{"list-first", "detail-first"} {
		for _, kind := range []string{"resume", "nextup"} {
			t.Run(order+"/"+kind, func(t *testing.T) {
				h := newEmbyPreloadHarness(t)
				h.nodes["90"] = preloadItem{ID: "90", Type: "Series"}
				path := "/Shows/NextUp?SeriesId=90&UserId=10&Limit=30"
				if kind == "resume" {
					path = "/Users/10/Items/Resume?ParentId=90&Limit=1"
				} else {
					path += "&StartIndex=30&Filters=IsFavorite"
				}
				h.router = gin.New()
				h.router.Use(PreloadObserver())
				h.router.Any("/*path", func(c *gin.Context) {
					route, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
					if route == "detail" {
						c.JSON(200, h.nodes["90"])
						return
					}
					c.Data(200, "application/json", []byte(`{"Items":[{"Id":"20","SeriesId":"90","UserData":{"PlaybackPositionTicks":0}},{"Id":"21","SeriesId":"90"}],"TotalRecordCount":0}`))
				})
				h.upstream = func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/Users/10/Items/20" {
						t.Errorf("unnecessary candidate query %s", r.URL.Path)
						w.WriteHeader(500)
						return
					}
					_ = json.NewEncoder(w).Encode(preloadTestItem("20", "Episode"))
				}
				detail := func() { h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil) }
				list := func() { h.request("GET", path, "device", "Fixture App", "Control/1", nil) }
				if order == "list-first" {
					list()
					detail()
				} else {
					detail()
					list()
				}
				if got := h.waitResolved(); got.path != preloadTestItem("20", "Episode").MediaSources[0].Path {
					t.Fatalf("wrong first candidate %+v", got)
				}
				if h.metadata.Load() != 1 {
					t.Fatalf("metadata calls=%d", h.metadata.Load())
				}
			})
		}
	}
}

func TestPreloadEmptyPendingQueryScope(t *testing.T) {
	for _, test := range []struct {
		query   string
		partial bool
	}{
		{"StartIndex=0&Limit=1", false},
		{"StartIndex=30", true},
		{"Limit=0", true},
		{"CustomFilter=on", true},
		{"ParentId=90&UserId=10&Fields=MediaSources&X-Emby-Token=fixture&api_key=fixture&DeviceId=device&EnableImages=false&SortBy=SortName&IncludeItemTypes=Episode&Recursive=true", false},
	} {
		t.Run(test.query, func(t *testing.T) {
			query, err := url.ParseQuery(test.query)
			if err != nil {
				t.Fatal(err)
			}
			if got := partialPreloadPendingQuery(query); got != test.partial {
				t.Fatalf("partial=%v want=%v", got, test.partial)
			}
		})
	}
}

func TestPreloadDirectDetailKeepsNewerPlaybackInfo(t *testing.T) {
	for _, test := range []struct{ name, itemType, reply string }{
		{"episode-get", "Episode", "old"},
		{"movie-playbackinfo", "Movie", "old"},
		{"empty-old-playbackinfo", "Movie", "empty"},
		{"replaced-old-playbackinfo", "Movie", "new-source"},
		{"failed-old-get", "Episode", "failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			itemType := test.itemType
			item := preloadTestItem("20", itemType)
			if itemType == "Movie" {
				item.MediaSources[0].Container = "mkv"
				item.MediaSources[0].RunTimeTicks = int64(time.Hour / (100 * time.Nanosecond))
				item.MediaSources[0].MediaStreams = []preloadStream{{Type: "Video", Codec: "h264"}, {Type: "Audio", Codec: "aac"}}
			}
			old := item
			item.MediaSources = append([]preloadSource(nil), item.MediaSources...)
			item.MediaSources[0].Path = ""
			if itemType == "Episode" {
				item.MediaSources[0].Container = ""
				item.MediaSources[0].RunTimeTicks = 0
				item.MediaSources[0].MediaStreams = nil
			}
			h.nodes[item.ID] = item
			newSource := old.MediaSources[0]
			newSource.ID += "-new"
			newSource.Path += "-new"
			h.infos[item.ID] = preloadPlaybackInfo{MediaSources: []preloadSource{newSource}}
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				switch test.reply {
				case "empty":
					_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: []preloadSource{}})
					return
				case "new-source":
					_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: []preloadSource{newSource}})
					return
				case "failed":
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if itemType == "Movie" {
					_ = json.NewEncoder(w).Encode(preloadPlaybackInfo{MediaSources: old.MediaSources})
				} else {
					_ = json.NewEncoder(w).Encode(old)
				}
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("background source query did not start")
			}
			h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", map[string]string{"MediaSourceId": newSource.ID})
			release <- struct{}{}
			if got := h.waitResolved(); got.path != newSource.Path {
				t.Fatalf("old metadata replaced newer natural source: got=%q want=%q", got.path, newSource.Path)
			}
			if h.metadata.Load() != 1 {
				t.Fatalf("natural enrichment repeated source queries: calls=%d", h.metadata.Load())
			}
		})
	}
}

func TestPreloadDetailRefreshKeepsNewerPlaybackInfo(t *testing.T) {
	for _, test := range []struct {
		name, parentType string
		metadataFirst    bool
	}{
		{"episode-detail-first", "Episode", false},
		{"series-detail-first", "Series", false},
		{"episode-metadata-first", "Episode", true},
		{"series-metadata-first", "Series", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			old := preloadTestItem("20", "Episode")
			item := old
			item.MediaSources = []preloadSource{{ID: old.MediaSources[0].ID, ItemID: item.ID}}
			parent, refreshed := item, old
			if test.parentType == "Series" {
				parent = preloadItem{ID: "90", Type: "Series"}
				refreshed = parent
			}
			newSource := old.MediaSources[0]
			newSource.ID += "-new"
			newSource.Path += "-new"
			metadataEntered, metadataRelease := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(metadataRelease) })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				close(metadataEntered)
				select {
				case <-metadataRelease:
				case <-r.Context().Done():
					return
				}
				_ = json.NewEncoder(w).Encode(old)
			}
			detailEntered, detailRelease, detailDone := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				if kind == "playback" {
					c.JSON(200, preloadPlaybackInfo{MediaSources: []preloadSource{newSource}})
				} else if kind == "nextup" {
					c.JSON(200, preloadItems{Items: []preloadItem{item}})
				} else if c.Query("refresh") != "" {
					close(detailEntered)
					<-detailRelease
					c.JSON(200, refreshed)
				} else {
					c.JSON(200, parent)
				}
			})
			if test.parentType == "Series" {
				h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			}
			detailPath := "/Users/10/Items/" + parent.ID
			h.request("GET", detailPath, "device", "Fixture App", "Control/1", nil)
			select {
			case <-metadataEntered:
			case <-time.After(4 * time.Second):
				t.Fatal("background source query did not start")
			}
			t.Cleanup(func() { close(detailRelease); <-detailDone })
			go func() {
				defer close(detailDone)
				h.request("GET", detailPath+"?refresh=1", "device", "Fixture App", "Control/1", nil)
			}()
			select {
			case <-detailEntered:
			case <-time.After(time.Second):
				t.Fatal("detail refresh did not start")
			}
			h.request("POST", "/Items/20/PlaybackInfo?MediaSourceId="+newSource.ID, "device", "Fixture App", "Control/1", nil)
			if !test.metadataFirst {
				detailRelease <- struct{}{}
				<-detailDone
			}
			metadataRelease <- struct{}{}
			if got := h.waitResolved(); got.path != newSource.Path {
				t.Fatalf("detail refresh replaced newer playback source: got=%q want=%q", got.path, newSource.Path)
			}
			if h.metadata.Load() != 1 {
				t.Fatalf("detail refresh repeated source queries: calls=%d", h.metadata.Load())
			}
			if test.metadataFirst {
				detailRelease <- struct{}{}
				<-detailDone
			}
		})
	}
}

func TestPreloadDirectDetailPreservesSourceBoundaries(t *testing.T) {
	old := preloadTestItem("20", "Movie").MediaSources[0]
	conflicting := old
	conflicting.ItemID = "21"
	infinite := old
	infinite.IsInfiniteStream = true
	denied := old
	allowed := false
	denied.SupportsDirectPlay, denied.SupportsDirectStream = &allowed, &allowed
	fresh := preloadSource{ID: old.ID, Path: old.Path + "-new"}
	for _, test := range []struct {
		name    string
		sources []preloadSource
		old     preloadSource
		want    string
	}{
		{"missing-sources", nil, preloadSource{}, old.Path},
		{"explicit-empty", []preloadSource{}, preloadSource{}, ""},
		{"conflicting-source", []preloadSource{{ID: old.ID, ItemID: "20"}}, conflicting, ""},
		{"conflicting-source-missing-item", []preloadSource{{ID: old.ID}}, conflicting, ""},
		{"matching-source-missing-item", []preloadSource{{ID: old.ID}}, old, old.Path},
		{"infinite-old-source", []preloadSource{{ID: old.ID}}, infinite, ""},
		{"denied-old-source", []preloadSource{{ID: old.ID}}, denied, ""},
		{"conflicting-old-source-new-url", []preloadSource{fresh}, conflicting, fresh.Path},
		{"infinite-old-source-new-url", []preloadSource{fresh}, infinite, fresh.Path},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			item := preloadItem{ID: "20", Type: "Movie"}
			h.nodes[item.ID] = item
			h.infos[item.ID] = preloadPlaybackInfo{MediaSources: test.sources}
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				value := item
				if test.old.ID == "" {
					value.Path = old.Path
				} else {
					value.MediaSources = []preloadSource{test.old}
				}
				_ = json.NewEncoder(w).Encode(value)
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("background source query did not start")
			}
			h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", map[string]any{})
			release <- struct{}{}
			finished := make(chan struct{})
			h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
				close(finished)
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}})
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("source query did not finish")
			}
			if got := len(h.resolved) != 0; got != (test.want != "") {
				t.Fatalf("resolved=%v want=%q: invalid or cleared sources must not be restored", got, test.want)
			}
			if test.want != "" {
				if got := h.waitResolved(); got.path != test.want {
					t.Fatalf("resolved path=%q want=%q", got.path, test.want)
				}
			}
			if h.metadata.Load() != 1 {
				t.Fatalf("source query repeated: calls=%d", h.metadata.Load())
			}
		})
	}
}

func TestPreloadEmptySourcesSurviveOmittedPlaybackInfo(t *testing.T) {
	for _, parentType := range []string{"Movie", "Series"} {
		t.Run(parentType, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			item := preloadItem{ID: "20", Type: "Movie"}
			parent := item
			if parentType == "Series" {
				item.Type, item.SeriesID = "Episode", "90"
				parent = preloadItem{ID: "90", Type: "Series"}
			}
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "detail":
					c.JSON(200, parent)
				case "nextup":
					c.JSON(200, preloadItems{Items: []preloadItem{item}})
				case "playback":
					if c.Query("missing") != "" {
						c.JSON(200, gin.H{})
					} else {
						c.JSON(200, preloadPlaybackInfo{MediaSources: []preloadSource{}})
					}
				}
			})
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				old := item
				old.Path = preloadTestItem("20", item.Type).MediaSources[0].Path
				_ = json.NewEncoder(w).Encode(old)
			}
			if parentType == "Series" {
				h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("background source query did not start")
			}
			h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", nil)
			h.request("POST", "/Items/20/PlaybackInfo?missing=1", "device", "Fixture App", "Control/1", nil)
			release <- struct{}{}
			finished := make(chan struct{})
			h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
				close(finished)
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}})
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("source query did not finish")
			}
			if len(h.resolved) != 0 || h.metadata.Load() != 1 {
				t.Fatalf("omitted fields restored cleared sources: resolved=%d calls=%d", len(h.resolved), h.metadata.Load())
			}
		})
	}
}

func TestPreloadPendingRefreshWhileCandidateQueryRuns(t *testing.T) {
	for _, test := range []struct{ name, query, want string }{
		{"first-page", "&Limit=30&StartIndex=0&Fields=MediaSources&EnableTotalRecordCount=false&X-Emby-Client=Fixture+App", "21"},
		{"later-page", "&StartIndex=30", "20"},
		{"filtered", "&Filters=IsFavorite", "20"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				if kind == "detail" {
					c.JSON(200, preloadItem{ID: "90", Type: "Series"})
				} else {
					c.JSON(200, preloadItems{Items: []preloadItem{}})
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/Resume" {
					t.Errorf("unexpected candidate query %s", r.URL.Path)
				}
				id := "21"
				if h.metadata.Load() == 1 {
					close(entered)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					id = "20"
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{preloadTestItem(id, "Episode")}})
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("first empty NextUp prevented the Resume fallback")
			}
			h.request("GET", "/Shows/NextUp?SeriesId=90"+test.query, "device", "Fixture App", "Control/1", nil)
			release <- struct{}{}
			if got := h.waitResolved(); got.path != preloadTestItem(test.want, "Episode").MediaSources[0].Path {
				t.Fatalf("wrong candidate after empty refresh: got=%q want=%s", got.path, test.want)
			}
		})
	}
}

func TestPreloadEmptyPendingRefreshWhileDetailRuns(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	metadataEntered, metadataRelease := make(chan struct{}), make(chan struct{}, 1)
	t.Cleanup(func() { close(metadataRelease) })
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		id := "21"
		if h.metadata.Load() == 1 {
			close(metadataEntered)
			select {
			case <-metadataRelease:
			case <-r.Context().Done():
				return
			}
			id = "20"
		}
		_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{preloadTestItem(id, "Episode")}})
	}
	detailEntered, detailRelease, detailDone := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
	h.router = gin.New()
	h.router.Use(PreloadObserver())
	h.router.Any("/*path", func(c *gin.Context) {
		kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
		if kind == "detail" {
			if c.Query("refresh") != "" {
				close(detailEntered)
				<-detailRelease
			}
			c.JSON(200, preloadItem{ID: "90", Type: "Series"})
		} else {
			c.JSON(200, preloadItems{Items: []preloadItem{}})
		}
	})
	h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
	select {
	case <-metadataEntered:
	case <-time.After(4 * time.Second):
		t.Fatal("first empty NextUp prevented the Resume fallback")
	}
	t.Cleanup(func() { close(detailRelease); <-detailDone })
	go func() {
		defer close(detailDone)
		h.request("GET", "/Users/10/Items/90?refresh=1", "device", "Fixture App", "Control/1", nil)
	}()
	<-detailEntered
	h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
	metadataRelease <- struct{}{}
	finished := make(chan struct{})
	h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
		close(finished)
		return playback.PreloadResult{Outcome: playback.PreloadSkipped}
	}})
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("obsolete candidate query did not finish")
	}
	if len(h.resolved) != 0 {
		t.Fatal("new reliable empty list allowed the obsolete candidate to resolve")
	}
	detailRelease <- struct{}{}
	<-detailDone
	if got := h.waitResolved(); got.path != preloadTestItem("21", "Episode").MediaSources[0].Path {
		t.Fatalf("detail refresh lost the new candidate query opportunity: got=%q", got.path)
	}
	if h.metadata.Load() != 2 {
		t.Fatalf("unexpected candidate queries: calls=%d", h.metadata.Load())
	}
}

func TestPreloadPendingQueriesAndSourceCompletion(t *testing.T) {
	for _, mode := range []string{"new-series", "resume-unsupported", "resume-denied", "source-without-path"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			parent := preloadItem{ID: "90", Type: "Series"}
			if mode == "source-without-path" {
				parent = preloadTestItem("20", "Episode")
				parent.MediaSources[0].Path = ""
			}
			h.nodes[parent.ID] = parent
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/10/Items/Resume":
					if mode == "resume-unsupported" {
						w.WriteHeader(404)
						return
					}
					if mode == "resume-denied" {
						w.WriteHeader(403)
						return
					}
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{}})
				case "/Shows/NextUp":
					if mode == "resume-denied" {
						t.Error("denied Resume fell back")
					}
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
				case "/Users/10/Items/20":
					_ = json.NewEncoder(w).Encode(preloadTestItem("20", "Episode"))
				default:
					t.Errorf("unexpected query %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			if mode == "resume-denied" {
				deadline := time.Now().Add(4 * time.Second)
				for h.metadata.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				finished := make(chan struct{})
				h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
					close(finished)
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}})
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("denied query did not finish")
				}
				if h.metadata.Load() != 1 {
					t.Fatalf("metadata calls=%d", h.metadata.Load())
				}
				return
			}
			h.waitResolved()
			want := int32(2)
			if mode == "source-without-path" {
				want = 1
			}
			if h.metadata.Load() != want {
				t.Fatalf("metadata calls=%d want=%d", h.metadata.Load(), want)
			}
		})
	}
}

func TestPreloadLinkSourceUsesAvailableData(t *testing.T) {
	for _, mode := range []string{"missing-id", "unrelated-source", "id-only-source", "mixed-local", "mixed-local-selected", "top-level-path", "selected-absent", "ambiguous"} {
		t.Run(mode, func(t *testing.T) {
			item := preloadTestItem("20", "Episode")
			selected := ""
			want := true
			switch mode {
			case "missing-id":
				item.MediaSources[0].ID = ""
			case "unrelated-source":
				item.MediaSources = append(item.MediaSources, preloadSource{})
			case "id-only-source":
				item.MediaSources = append(item.MediaSources, preloadSource{ID: "incomplete"})
			case "mixed-local", "mixed-local-selected":
				item.MediaSources = append(item.MediaSources, preloadSource{ID: "local", Path: "/media/movie.mkv"})
				if mode == "mixed-local-selected" {
					selected = item.MediaSources[0].ID
				} else {
					want = false
				}
			case "top-level-path":
				item.Path = item.MediaSources[0].Path
				item.MediaSources = nil
			case "selected-absent":
				selected = "missing"
				want = false
			case "ambiguous":
				other := item.MediaSources[0]
				other.ID = "other"
				other.Path += "-other"
				item.MediaSources = append(item.MediaSources, other)
				want = false
			}
			_, ok := selectPreloadLinkSource(item, selected)
			if ok != want {
				t.Fatalf("available=%v want=%v", ok, want)
			}
			if mode == "missing-id" {
				if _, ok := selectPreloadSource(item, ""); ok {
					t.Fatal("ID-less URL established a real playback source")
				}
			}
		})
	}
}

func TestPreloadPendingRefreshPreservesSourceIdentity(t *testing.T) {
	for _, mode := range []string{"conflicting-old-item", "infinite-old-source", "denied-old-source", "unknown-old-item", "conflicting-new-item", "denied-old-item", "live-old-item", "infinite-old-item", "unknown-old-type", "new-source-after-denial", "absent-sources-after-denial", "top-level-after-denial"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				if kind == "detail" {
					c.JSON(200, preloadItem{ID: "90", Type: "Series"})
					return
				}
				item := preloadTestItem("20", "Episode")
				if c.Query("refresh") != "" {
					switch mode {
					case "new-source-after-denial":
						item.MediaSources[0].Path += "-new"
					case "absent-sources-after-denial", "top-level-after-denial":
						item.MediaSources = nil
					default:
						item.MediaSources = []preloadSource{{ID: item.MediaSources[0].ID}}
					}
					if mode == "conflicting-new-item" {
						item.MediaSources[0].ItemID = "21"
					}
				} else {
					switch mode {
					case "conflicting-old-item":
						item.MediaSources[0].ItemID = "21"
					case "infinite-old-source":
						item.MediaSources[0].IsInfiniteStream = true
					case "denied-old-source":
						denied := false
						item.MediaSources[0].SupportsDirectPlay, item.MediaSources[0].SupportsDirectStream = &denied, &denied
					case "unknown-old-item":
						item.MediaSources[0].ItemID = ""
					case "unknown-old-type":
						item.Type = ""
					case "denied-old-item", "new-source-after-denial", "absent-sources-after-denial":
						item.PlayAccess = "None"
					case "live-old-item":
						item.IsLive = true
					case "infinite-old-item":
						item.IsInfiniteStream = true
					case "top-level-after-denial":
						item.PlayAccess = "None"
						item.Path, item.MediaSources = item.MediaSources[0].Path, nil
					}
				}
				c.JSON(200, preloadItems{Items: []preloadItem{item}})
			})
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?SeriesId=90&refresh=1", "device", "Fixture App", "Control/1", nil)
			h.adapter.mu.Lock()
			defer h.adapter.mu.Unlock()
			if len(h.adapter.devices) != 1 {
				t.Fatalf("observed devices=%d", len(h.adapter.devices))
			}
			for _, device := range h.adapter.devices {
				if device.pendingTarget.ID != "20" {
					t.Fatalf("pending target=%q", device.pendingTarget.ID)
				}
				source, ok := selectPreloadLinkSource(device.pendingTarget, "")
				want := mode == "unknown-old-item" || mode == "unknown-old-type" || mode == "new-source-after-denial"
				if got := ok && source.Path != ""; got != want {
					t.Fatalf("refreshed source available=%v want=%v: %+v", got, want, source)
				}
			}
		})
	}
}

func TestPreloadPendingRefreshKeepsSource(t *testing.T) {
	for _, kind := range []string{"resume", "nextup"} {
		for _, update := range []string{"missing-path", "empty-sources", "removed-source", "new-path", "denied"} {
			t.Run(kind+"/"+update, func(t *testing.T) {
				h := newEmbyPreloadHarness(t)
				entered, release, published := make(chan struct{}, 2), make(chan struct{}, 1), make(chan bool, 2)
				t.Cleanup(func() { close(release) })
				h.adapter.options.Resolve = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return preloadHash(raw, ua), func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, preloadHash(raw, ua)) {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						entered <- struct{}{}
						select {
						case <-release:
						case <-ctx.Done():
						}
						published <- task.Publish(func() {})
						return playback.PreloadResult{Outcome: playback.PreloadCached}
					}, true
				}
				h.router = gin.New()
				h.router.Use(PreloadObserver())
				h.router.Any("/*path", func(c *gin.Context) {
					route, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
					if route == "detail" {
						c.JSON(200, preloadItem{ID: "90", Type: "Series"})
						return
					}
					item := preloadTestItem("20", "Episode")
					if update == "empty-sources" {
						item.Path, item.MediaSources = item.MediaSources[0].Path, nil
					} else if update == "removed-source" {
						item.MediaSources = append(item.MediaSources, preloadSource{ID: "retained"})
					}
					if c.Query("refresh") != "" {
						switch update {
						case "missing-path":
							item.MediaSources[0].Path = ""
						case "empty-sources":
							item.Path, item.MediaSources = "", []preloadSource{}
						case "removed-source":
							item.MediaSources = item.MediaSources[1:]
						case "new-path":
							item.MediaSources[0].Path += "-new"
						case "denied":
							item.PlayAccess = "None"
						}
					}
					c.JSON(200, preloadItems{Items: []preloadItem{item}})
				})
				path := "/Shows/NextUp?SeriesId=90&UserId=10"
				if kind == "resume" {
					path = "/Users/10/Items/Resume?ParentId=90"
				}
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("natural candidate did not start preload")
				}
				h.request("GET", path+"&refresh=1", "device", "Fixture App", "Control/1", nil)
				release <- struct{}{}
				select {
				case allowed := <-published:
					if want := update == "missing-path"; allowed != want {
						t.Fatalf("refresh publish=%v want=%v", allowed, want)
					}
				case <-time.After(time.Second):
					t.Fatal("in-flight preload did not finish")
				}
				if update != "missing-path" {
					return
				}
				waitPreloadPendingJobs(t, h)
				if h.metadata.Load() != 0 || len(entered) != 0 {
					t.Fatalf("same-source refresh repeated preparation: metadata=%d signatures=%d", h.metadata.Load(), len(entered))
				}
			})
		}
	}
}

// 真实日志样例中的多项、零进度及 TotalRecordCount=0 不应改变服务端顺序。
func TestPreloadPendingListScope(t *testing.T) {
	parent := preloadItem{ID: "91", Type: "Season", SeriesID: "90"}
	list := &preloadPendingList{series: "90", items: []preloadItem{{ID: "19", Type: "Episode", SeriesID: "80"}, {ID: "20", SeasonID: "91"}, {ID: "21", SeasonID: "91"}}}
	item, confirmed := list.candidate(parent, nil)
	if item.ID != "20" || !confirmed || item.Type != "Episode" {
		t.Fatalf("candidate=%+v confirmed=%v", item, confirmed)
	}
	list.items = []preloadItem{{ID: "20", SeriesID: "80"}}
	list.parent = "91"
	if item, _ := list.candidate(parent, nil); item.ID != "" {
		t.Fatal("request scope hid explicit series conflict")
	}
}

func TestPreloadPendingKeepsNewerNaturalData(t *testing.T) {
	for _, mode := range []string{"candidate-during-resume", "denial-during-resume", "denial-during-nextup", "source-during-item-get", "list-source-during-item-get", "list-source-during-season-check", "list-source-during-season-check-empty", "later-source-during-season-check"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			entered, release := make(chan struct{}, 1), make(chan struct{}, 1)
			returned := make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			queriesPending := mode == "candidate-during-resume" || mode == "denial-during-resume" || mode == "denial-during-nextup"
			seasonCheck := mode == "list-source-during-season-check" || mode == "list-source-during-season-check-empty" || mode == "later-source-during-season-check"
			laterSource := mode == "later-source-during-season-check"
			parent := preloadItem{ID: "90", Type: "Series"}
			item := preloadTestItem("21", "Episode")
			if !queriesPending {
				item = preloadTestItem("20", "Episode")
				item.MediaSources = nil
				if seasonCheck {
					parent = preloadItem{ID: "91", Type: "Season", SeriesID: "90"}
					item.SeasonID = ""
				}
				newSource := preloadTestItem("20", "Episode").MediaSources[0]
				newSource.Path += "-new"
				h.infos["20"] = preloadPlaybackInfo{MediaSources: []preloadSource{newSource}}
				if laterSource {
					newSource = preloadTestItem("21", "Episode").MediaSources[0]
					newSource.Path += "-new"
					h.infos["21"] = preloadPlaybackInfo{MediaSources: []preloadSource{newSource}}
				}
			}
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, id, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "detail":
					c.JSON(200, parent)
				case "resume":
					items := []preloadItem{}
					if c.Query("deny") != "" {
						items = []preloadItem{{ID: "20", PlayAccess: "None"}}
					}
					c.JSON(200, preloadItems{Items: items})
				case "nextup":
					value := item
					if c.Query("deny") != "" {
						value = preloadItem{ID: "20", PlayAccess: "None"}
					}
					if c.Query("updated") != "" && !laterSource {
						value.MediaSources = h.infos["20"].MediaSources
						value.SeasonID = "91"
					}
					items := []preloadItem{value}
					if laterSource {
						later := preloadTestItem("21", "Episode")
						if c.Query("updated") != "" {
							later.MediaSources = h.infos["21"].MediaSources
						}
						items = append(items, later)
					}
					c.JSON(200, preloadItems{Items: items})
				case "playback":
					c.JSON(200, h.infos[id])
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				old := preloadTestItem("20", "Episode")
				if queriesPending || seasonCheck {
					items := []preloadItem{old}
					if laterSource || mode == "list-source-during-season-check-empty" {
						items = []preloadItem{}
					}
					_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
				} else {
					_ = json.NewEncoder(w).Encode(old)
				}
				returned <- struct{}{}
			}
			h.request("GET", "/Users/10/Items/"+parent.ID, "device", "Fixture App", "Control/1", nil)
			if mode == "denial-during-nextup" {
				h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
			} else if !queriesPending {
				h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
			}
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("background metadata query did not start")
			}
			want := item.MediaSources
			if mode == "candidate-during-resume" {
				h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
			} else if mode == "denial-during-resume" {
				h.request("GET", "/Shows/NextUp?deny=1", "device", "Fixture App", "Control/1", nil)
			} else if mode == "denial-during-nextup" {
				h.request("GET", "/Users/10/Items/Resume?deny=1", "device", "Fixture App", "Control/1", nil)
			} else {
				if mode == "list-source-during-item-get" || seasonCheck {
					h.request("GET", "/Shows/NextUp?SeriesId=90&updated=1", "device", "Fixture App", "Control/1", nil)
				} else {
					h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", map[string]any{})
				}
				want = h.infos["20"].MediaSources
				if laterSource {
					want = h.infos["21"].MediaSources
				}
			}
			// 先观察较新的自然响应，再允许旧补查返回。
			release <- struct{}{}
			if mode == "denial-during-resume" || mode == "denial-during-nextup" {
				select {
				case <-returned:
				case <-time.After(time.Second):
					t.Fatal("old Resume response did not return")
				}
				waitPreloadPendingJobs(t, h)
				if len(h.resolved) != 0 {
					t.Fatal("old Resume ignored a newer explicit denial")
				}
				return
			}
			if got := h.waitResolved(); got.path != want[0].Path {
				t.Fatalf("old metadata replaced newer natural data: got=%q want=%q", got.path, want[0].Path)
			}
			if h.metadata.Load() != 1 {
				t.Fatalf("natural enrichment repeated metadata queries: calls=%d", h.metadata.Load())
			}
		})
	}
}

func TestPreloadPendingGlobalDenialBeforeDetail(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	queried := make(chan struct{}, 1)
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		queried <- struct{}{}
	}
	h.router = gin.New()
	h.router.Use(PreloadObserver())
	h.router.Any("/*path", func(c *gin.Context) {
		kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
		if kind == "detail" {
			c.JSON(200, preloadItem{ID: "90", Type: "Series"})
			return
		}
		item := preloadTestItem("20", "Episode")
		if kind == "nextup" {
			item = preloadItem{ID: "20", PlayAccess: "None"}
		}
		c.JSON(200, preloadItems{Items: []preloadItem{item}})
	})
	h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Shows/NextUp?UserId=10", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
	select {
	case got := <-h.resolved:
		t.Fatalf("explicitly denied candidate resolved without revalidation: %+v", got)
	case <-queried:
	case <-time.After(4 * time.Second):
		t.Fatal("candidate was neither rejected nor revalidated")
	}
	waitPreloadPendingJobs(t, h)
	if len(h.resolved) != 0 {
		t.Fatal("denied candidate reused old authorization")
	}
}

func waitPreloadPendingJobs(t *testing.T, h *embyPreloadHarness) {
	t.Helper()
	finished := make(chan struct{})
	h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
		close(finished)
		return playback.PreloadResult{Outcome: playback.PreloadSkipped}
	}})
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("pending preparation did not finish")
	}
}

func TestPreloadPendingVerifiesEarlierSeasonCandidate(t *testing.T) {
	for _, membership := range []string{"first", "later", "empty", "denied-later", "changed-later", "omitted-later"} {
		t.Run(membership, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			first := preloadTestItem("20", "Episode")
			first.SeasonID = ""
			later := preloadTestItem("21", "Episode")
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				if kind == "detail" {
					c.JSON(200, preloadItem{ID: "91", Type: "Season", SeriesID: "90"})
					return
				}
				c.JSON(200, preloadItems{Items: []preloadItem{first, later}})
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Shows/90/Episodes" || r.URL.Query().Get("SeasonId") != "91" || r.URL.Query().Get("StartItemId") != "20" {
					t.Errorf("unexpected season verification %s", r.URL)
					w.WriteHeader(400)
					return
				}
				items := []preloadItem{first}
				if membership == "later" || membership == "denied-later" || membership == "changed-later" || membership == "omitted-later" {
					updated := preloadTestItem("21", "Episode")
					if membership == "denied-later" {
						updated.PlayAccess = "None"
					} else if membership == "changed-later" {
						updated.MediaSources[0].Path += "-new"
					} else if membership == "omitted-later" {
						updated.MediaSources[0].Path = ""
					}
					items = []preloadItem{updated}
				} else if membership == "empty" {
					items = []preloadItem{}
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: items})
			}
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
			if membership == "denied-later" {
				deadline := time.Now().Add(4 * time.Second)
				for h.metadata.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if h.metadata.Load() == 0 {
					t.Fatal("season membership was not verified")
				}
				waitPreloadPendingJobs(t, h)
				if len(h.resolved) != 0 {
					t.Fatal("season verification denial was replaced by an older candidate")
				}
				return
			}
			want := first
			if membership != "first" {
				want = later
			}
			if membership == "changed-later" {
				want.MediaSources = append([]preloadSource(nil), later.MediaSources...)
				want.MediaSources[0].Path += "-new"
			}
			if got := h.waitResolved(); got.path != want.MediaSources[0].Path {
				t.Fatalf("skipped earlier possible season member: %+v", got)
			}
			if h.metadata.Load() != 1 {
				t.Fatalf("season verification calls=%d", h.metadata.Load())
			}
		})
	}
}

func TestPreloadPendingListsBeforeDetailKeepScope(t *testing.T) {
	for _, test := range []struct {
		name, nextup, want string
		calls              int32
	}{
		{"global-empty", "/Shows/NextUp?UserId=10", "20", 0},
		{"other-series-empty", "/Shows/NextUp?SeriesId=99&UserId=10", "20", 0},
		{"same-series-empty", "/Shows/NextUp?SeriesId=90&UserId=10", "21", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			entered, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			t.Cleanup(func() { close(release); <-done })
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "detail":
					c.JSON(200, preloadItem{ID: "90", Type: "Series"})
				case "resume":
					close(entered)
					<-release
					c.JSON(200, preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
				case "nextup":
					c.JSON(200, preloadItems{Items: []preloadItem{}})
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/Resume" || r.URL.Query().Get("ParentId") != "90" {
					t.Error("candidate fallback lost its parent scope")
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{preloadTestItem("21", "Episode")}})
			}
			go func() {
				defer close(done)
				h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("natural Resume did not start")
			}
			h.request("GET", test.nextup, "device", "Fixture App", "Control/1", nil)
			release <- struct{}{}
			<-done
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			if got, want := h.waitResolved(), preloadTestItem(test.want, "Episode").MediaSources[0].Path; got.path != want {
				t.Fatalf("empty list changed another scope: got=%q want=%q", got.path, want)
			}
			if h.metadata.Load() != test.calls {
				t.Fatalf("candidate reuse changed: calls=%d want=%d", h.metadata.Load(), test.calls)
			}
		})
	}
}

func TestPreloadPendingIgnoresOldListResponses(t *testing.T) {
	for _, mode := range []string{"cross-kind", "control-round-trip"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "detail":
					c.JSON(200, preloadItem{ID: "90", Type: "Series"})
				case "resume":
					close(entered)
					<-release
					c.JSON(200, preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
				case "nextup":
					c.JSON(200, preloadItems{Items: []preloadItem{preloadTestItem("21", "Episode")}})
				}
			})
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			go func() {
				defer close(done)
				h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
			}()
			<-entered
			if mode == "control-round-trip" {
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/2", nil)
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
			close(release)
			<-done
			h.adapter.mu.Lock()
			defer h.adapter.mu.Unlock()
			for _, device := range h.adapter.devices {
				if device.detail.ID != "90" || device.pendingTarget.ID != "21" {
					t.Fatalf("old response changed latest detail/candidate: detail=%s pending=%s", device.detail.ID, device.pendingTarget.ID)
				}
			}
		})
	}
}

func TestPreloadPartialSourceChangesCancelOldPublish(t *testing.T) {
	for _, mode := range []string{"missing-id-path", "top-level-cleared", "multiple-addresses", "mixed-local", "global-denial", "mixed-empty", "pending-pi", "pending-pi-selection", "pending-pi-refresh", "direct-pi-refresh"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			pendingRefresh := mode == "pending-pi-refresh"
			directRefresh := mode == "direct-pi-refresh"
			detailRefresh := pendingRefresh || directRefresh
			detailEntered, detailRelease, detailDone := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			entered, published := make(chan struct{}, 2), make(chan bool, 2)
			metadataDone := make(chan struct{}, 2)
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{}})
				metadataDone <- struct{}{}
			}
			h.adapter.options.Resolve = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				return preloadHash(raw, ua), func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					entered <- struct{}{}
					<-ctx.Done()
					published <- task.Publish(func() {})
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}, true
			}
			item := preloadTestItem("20", "Movie")
			item.MediaSources[0].ID = ""
			if mode == "top-level-cleared" {
				item.Path, item.MediaSources = item.MediaSources[0].Path, nil
			}
			h.nodes["20"] = item
			if mode == "global-denial" || mode == "mixed-empty" || mode == "pending-pi" || mode == "pending-pi-selection" || detailRefresh {
				h.router = gin.New()
				h.router.Use(PreloadObserver())
				h.router.Any("/*path", func(c *gin.Context) {
					kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
					if kind == "detail" {
						if detailRefresh && c.Query("refresh") != "" {
							close(detailEntered)
							<-detailRelease
						}
						value := preloadItem{ID: "90", Type: "Series"}
						if directRefresh {
							value = item
						}
						c.JSON(200, value)
						return
					}
					if kind == "playback" {
						if mode == "pending-pi-selection" {
							a := preloadTestItem("20", "Episode").MediaSources[0]
							b := a
							b.ID = "other"
							b.Path += "-other"
							c.JSON(200, preloadPlaybackInfo{MediaSources: []preloadSource{a, b}})
							return
						}
						c.JSON(200, preloadPlaybackInfo{MediaSources: []preloadSource{}})
						return
					}
					candidate := preloadTestItem("20", "Episode")
					if mode == "pending-pi-selection" {
						other := candidate.MediaSources[0]
						other.ID = "other"
						other.Path += "-other"
						candidate.MediaSources = append(candidate.MediaSources, other)
					}
					if mode == "mixed-empty" && kind == "nextup" {
						c.JSON(200, preloadItems{Items: []preloadItem{}})
						return
					}
					if c.Query("deny") != "" {
						candidate.PlayAccess = "None"
					}
					c.JSON(200, preloadItems{Items: []preloadItem{candidate}})
				})
				if directRefresh {
					h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
				} else {
					h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
					if mode == "mixed-empty" {
						h.request("GET", "/Users/10/Items/Resume", "device", "Fixture App", "Control/1", nil)
					} else {
						h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
					}
				}
			} else {
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			}
			if mode == "pending-pi-selection" {
				h.request("POST", "/Items/20/PlaybackInfo?MediaSourceId=other", "device", "Fixture App", "Control/1", map[string]any{})
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("available source did not start preload")
			}
			if detailRefresh {
				t.Cleanup(func() { close(detailRelease); <-detailDone })
				detailPath := "/Users/10/Items/90?refresh=1"
				if directRefresh {
					detailPath = "/Users/10/Items/20?refresh=1"
				}
				go func() {
					defer close(detailDone)
					h.request("GET", detailPath, "device", "Fixture App", "Control/1", nil)
				}()
				select {
				case <-detailEntered:
				case <-time.After(time.Second):
					t.Fatal("detail refresh did not start")
				}
			}
			if mode == "global-denial" {
				h.request("GET", "/Shows/NextUp?deny=1", "device", "Fixture App", "Control/1", nil)
			} else if mode == "mixed-empty" {
				h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
			} else if mode == "pending-pi-selection" {
				h.request("POST", "/Items/20/PlaybackInfo?MediaSourceId=source-20", "device", "Fixture App", "Control/1", map[string]any{})
			} else if mode == "pending-pi" || detailRefresh {
				h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", map[string]any{})
			} else {
				item.MediaSources = append([]preloadSource(nil), item.MediaSources...)
				switch mode {
				case "missing-id-path":
					item.MediaSources[0].Path += "-changed"
				case "top-level-cleared":
					item.Path, item.MediaSources = "", []preloadSource{}
				case "multiple-addresses":
					item.MediaSources = append(item.MediaSources, preloadSource{ID: "other", Path: item.MediaSources[0].Path + "-other"})
				case "mixed-local":
					item.MediaSources = append(item.MediaSources, preloadSource{ID: "local", Path: "/media/movie.mkv"})
				}
				h.mu.Lock()
				h.nodes["20"] = item
				h.mu.Unlock()
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			}
			select {
			case allowed := <-published:
				if allowed {
					t.Fatal("obsolete source published a cache entry")
				}
			case <-time.After(time.Second):
				t.Fatal("source change did not cancel old preload")
			}
			if detailRefresh {
				if len(entered) != 0 || h.metadata.Load() != 0 {
					t.Fatal("detail refresh submitted new work before its response")
				}
				detailRelease <- struct{}{}
				<-detailDone
			}
			if mode == "mixed-empty" {
				select {
				case <-metadataDone:
				case <-time.After(3 * time.Second):
					t.Fatal("new empty list reused older global candidate")
				}
				finished := make(chan struct{})
				h.core.Submit(playback.PreloadCandidate{Key: "barrier", Scope: "barrier", Kind: playback.PreloadNext, Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
					close(finished)
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}})
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("new candidate query did not finish")
				}
				if len(entered) != 0 {
					t.Fatal("old global candidate started another preload")
				}
			}
		})
	}
}

func TestPreloadPendingSameKindListsBeforeDetailKeepScope(t *testing.T) {
	for _, kind := range []string{"resume", "nextup"} {
		t.Run(kind, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				route, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				if route == "detail" {
					c.JSON(200, preloadItem{ID: "90", Type: "Series"})
					return
				}
				items := []preloadItem{}
				if c.Query("ParentId") == "90" || c.Query("SeriesId") == "90" {
					items = []preloadItem{preloadTestItem("20", "Episode")}
				}
				c.JSON(200, preloadItems{Items: items})
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }
			scoped, global := "/Users/10/Items/Resume?ParentId=90", "/Users/10/Items/Resume"
			if kind == "nextup" {
				scoped, global = "/Shows/NextUp?SeriesId=90&UserId=10", "/Shows/NextUp?UserId=10"
			}
			h.request("GET", scoped, "device", "Fixture App", "Control/1", nil)
			h.request("GET", global, "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			select {
			case got := <-h.resolved:
				if got.path != preloadTestItem("20", "Episode").MediaSources[0].Path {
					t.Fatalf("wrong target %q", got.path)
				}
				if h.metadata.Load() != 0 {
					t.Fatalf("unexpected metadata calls=%d", h.metadata.Load())
				}
			case <-time.After(3 * time.Second):
				t.Fatalf("unrelated global empty list erased usable scoped %s; metadata calls=%d", kind, h.metadata.Load())
			}
		})
	}
}

func TestPreloadPendingClearSourcesAcrossListsBeforeDetail(t *testing.T) {
	for _, refresh := range []string{"scoped-resume", "global-nextup", "global-nextup-empty"} {
		t.Run(refresh, func(t *testing.T) {
			resumeCalls := 0
			globalCalls := 0
			h := newEmbyPreloadHarness(t)
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				route, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				omitted := gin.H{"Items": []gin.H{{"Id": "20", "Type": "Episode", "SeriesId": "90"}}}
				switch route {
				case "detail":
					c.JSON(200, preloadItem{ID: "90", Type: "Series"})
				case "resume":
					resumeCalls++
					if resumeCalls > 1 {
						c.JSON(200, omitted)
					} else {
						c.JSON(200, preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
					}
				case "nextup":
					if c.Query("SeriesId") == "" {
						globalCalls++
						if globalCalls > 1 {
							c.JSON(200, preloadItems{Items: []preloadItem{}})
						} else {
							c.JSON(200, omitted)
						}
					} else {
						item := preloadTestItem("20", "Episode")
						item.MediaSources = []preloadSource{}
						c.JSON(200, preloadItems{Items: []preloadItem{item}})
					}
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }
			h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			path := "/Users/10/Items/Resume?ParentId=90"
			if refresh != "scoped-resume" {
				path = "/Shows/NextUp?UserId=10"
			}
			h.request("GET", path, "device", "Fixture App", "Control/1", nil)
			if refresh == "global-nextup-empty" {
				h.request("GET", path, "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			select {
			case got := <-h.resolved:
				t.Fatalf("omitted sources restored a URL cleared by NextUp: %q; metadata=%d", got.path, h.metadata.Load())
			case <-time.After(3 * time.Second):
			}
		})
	}
}

func TestPreloadPendingSelectionIgnoresSourceMetadataOrder(t *testing.T) {
	for _, update := range []string{"empty", "replacement", "same"} {
		t.Run(update, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			listEntered, listRelease, listDone := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			signing, signatureRelease, published := make(chan struct{}, 2), make(chan struct{}, 1), make(chan bool, 2)
			t.Cleanup(func() { close(signatureRelease) })
			t.Cleanup(func() { close(listRelease); <-listDone })
			h.adapter.options.Resolve = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				identity := preloadHash(raw, ua)
				return identity, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, identity) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					signing <- struct{}{}
					select {
					case <-signatureRelease:
					case <-ctx.Done():
					}
					published <- task.Publish(func() {})
					return playback.PreloadResult{Outcome: playback.PreloadCached}
				}, true
			}
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				route, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch route {
				case "detail":
					c.JSON(200, preloadItem{ID: "90", Type: "Series"})
				case "nextup":
					item := preloadTestItem("20", "Episode")
					item.MediaSources[0].Path = ""
					c.JSON(200, preloadItems{Items: []preloadItem{item}})
				case "resume":
					close(listEntered)
					<-listRelease
					items := []preloadItem{}
					if update == "replacement" {
						items = []preloadItem{preloadTestItem("21", "Episode")}
					}
					if update == "same" {
						items = []preloadItem{preloadTestItem("20", "Episode")}
					}
					c.JSON(200, preloadItems{Items: items})
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/20" {
					t.Errorf("unexpected source GET %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(preloadTestItem("20", "Episode"))
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/NextUp?SeriesId=90", "device", "Fixture App", "Control/1", nil)
			go func() {
				defer close(listDone)
				h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
			}()
			<-listEntered
			select {
			case <-signing:
			case <-time.After(4 * time.Second):
				t.Fatal("source completion did not start signing")
			}
			listRelease <- struct{}{}
			<-listDone
			signatureRelease <- struct{}{}
			select {
			case allowed := <-published:
				if allowed != (update == "same") {
					t.Fatalf("source-only completion after %s: publish=%v", update, allowed)
				}
			case <-time.After(time.Second):
				t.Fatal("signature did not finish")
			}
		})
	}
}

func TestPreloadPendingCrossListOmissionKeepsSource(t *testing.T) {
	resumeCalls := 0
	h := newEmbyPreloadHarness(t)
	h.router = gin.New()
	h.router.Use(PreloadObserver())
	h.router.Any("/*path", func(c *gin.Context) {
		route, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
		switch route {
		case "detail":
			c.JSON(200, preloadItem{ID: "90", Type: "Series"})
		case "resume":
			resumeCalls++
			if resumeCalls > 1 {
				c.JSON(200, gin.H{"Items": []gin.H{{"Id": "20", "Type": "Episode", "SeriesId": "90"}}})
			} else {
				c.JSON(200, preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
			}
		case "nextup":
			c.JSON(200, gin.H{"Items": []gin.H{{"Id": "20", "Type": "Episode", "SeriesId": "90"}}})
		}
	})
	h.upstream = func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }
	h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
	h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
	select {
	case got := <-h.resolved:
		if got.path != preloadTestItem("20", "Episode").MediaSources[0].Path || h.metadata.Load() != 0 {
			t.Fatalf("benign omission failed reuse: path=%q metadata=%d", got.path, h.metadata.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("benign omission prevented source reuse")
	}
}

func TestPreloadPendingDetailPermissionBoundaries(t *testing.T) {
	for _, mode := range []string{"cached", "late-list", "other-series", "authorized-detail", "authorized-list"} {
		t.Run(mode, func(t *testing.T) {
			h := newEmbyPreloadHarness(t)
			old := preloadTestItem("20", "Episode")
			fresh := preloadTestItem("20", "Episode")
			fresh.MediaSources[0].Path += "-fresh"
			other := preloadTestItem("21", "Episode")
			other.SeriesID = "93"
			entered, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			queried := make(chan struct{}, 4)
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				queried <- struct{}{}
				if r.URL.Path == "/Users/10/Items/20" {
					_ = json.NewEncoder(w).Encode(fresh)
				} else {
					w.WriteHeader(http.StatusForbidden)
				}
			}
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, id, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "detail":
					if id == "20" {
						if c.Query("authorized") != "" {
							c.JSON(http.StatusOK, gin.H{"Id": "20", "Type": "Episode", "SeriesId": "90"})
						} else {
							c.Status(http.StatusForbidden)
						}
					} else {
						c.JSON(http.StatusOK, preloadItem{ID: id, Type: "Series"})
					}
				case "resume":
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{other}})
				case "nextup":
					if c.Query("delayed") != "" {
						close(entered)
						<-release
					}
					if c.Query("authorized") != "" {
						c.JSON(http.StatusOK, gin.H{"Items": []gin.H{{"Id": "20", "Type": "Episode", "SeriesId": "90"}}})
					} else {
						c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{old}})
					}
				}
			})
			if mode == "other-series" {
				h.request("GET", "/Users/10/Items/Resume?ParentId=93", "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			if mode == "late-list" {
				t.Cleanup(func() { close(release); <-done })
				go func() {
					defer close(done)
					h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10&delayed=1", "device", "Fixture App", "Control/1", nil)
				}()
				<-entered
			}
			if response := h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil); response.Code != http.StatusForbidden {
				t.Fatalf("detail denial status=%d", response.Code)
			}
			if mode == "late-list" {
				release <- struct{}{}
				<-done
			}
			if mode == "authorized-detail" {
				h.request("GET", "/Users/10/Items/20?authorized=1", "device", "Fixture App", "Control/1", nil)
			} else if mode == "authorized-list" {
				h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10&authorized=1", "device", "Fixture App", "Control/1", nil)
			}
			parent := "90"
			if mode == "other-series" {
				parent = "93"
			}
			h.request("GET", "/Users/10/Items/"+parent, "device", "Fixture App", "Control/1", nil)
			if mode == "other-series" || mode == "authorized-detail" || mode == "authorized-list" {
				wantPath, wantQueries := fresh.MediaSources[0].Path, int32(1)
				if mode == "other-series" {
					wantPath, wantQueries = other.MediaSources[0].Path, 0
				}
				if got := h.waitResolved(); got.path != wantPath || h.metadata.Load() != wantQueries {
					t.Fatalf("permission update damaged %s: path=%q metadata=%d", mode, got.path, h.metadata.Load())
				}
				return
			}
			select {
			case got := <-h.resolved:
				t.Fatalf("newer detail denial reused pending URL: path=%q metadata=%d", got.path, h.metadata.Load())
			case <-queried:
				waitPreloadPendingJobs(t, h)
			case <-time.After(3 * time.Second):
			}
			if len(h.resolved) != 0 {
				t.Fatal("denied item regained its old pending URL")
			}
		})
	}
}

func TestPreloadPendingPermissionPlaybackOrdering(t *testing.T) {
	for _, test := range []struct{ mode, denial string }{
		{"older-same-item", "detail"},
		{"newer-same-item", "detail"},
		{"other-item", "detail"},
		{"older-same-item", "episodes"},
		{"newer-same-item", "episodes"},
		{"other-item", "episodes"},
	} {
		t.Run(test.denial+"/"+test.mode, func(t *testing.T) {
			mode := test.mode
			h := newEmbyPreloadHarness(t)
			itemID := "20"
			if mode == "other-item" {
				itemID = "21"
			}
			old := preloadTestItem(itemID, "Episode")
			fresh := preloadTestItem(itemID, "Episode")
			fresh.MediaSources[0].Path += "-fresh"
			entered, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			t.Cleanup(func() { close(release); <-done })
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/Users/10/Items/"+itemID {
					t.Errorf("unexpected source request=%s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(fresh)
			}
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, id, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "episodes":
					denied := preloadTestItem("20", "Episode")
					denied.PlayAccess = "None"
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{denied}})
				case "nextup":
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
				case "playback":
					if c.Query("fresh") != "" {
						c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: fresh.MediaSources})
					} else {
						close(entered)
						<-release
						c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: old.MediaSources})
					}
				case "detail":
					if c.Query("authorized") != "" {
						c.JSON(http.StatusOK, gin.H{"Id": id, "Type": "Episode", "SeriesId": "90"})
					} else if id == "20" {
						c.Status(http.StatusForbidden)
					} else {
						c.JSON(http.StatusOK, preloadItem{ID: "90", Type: "Series"})
					}
				}
			})
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			go func() {
				defer close(done)
				h.request("POST", "/Items/"+itemID+"/PlaybackInfo", "device", "Fixture App", "Control/1", nil)
			}()
			<-entered
			if test.denial == "episodes" {
				h.request("GET", "/Shows/90/Episodes?SeasonId=91&UserId=10", "device", "Fixture App", "Control/1", nil)
			} else {
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			}
			release <- struct{}{}
			<-done
			if mode == "newer-same-item" {
				h.request("POST", "/Items/20/PlaybackInfo?fresh=1", "device", "Fixture App", "Control/1", nil)
			}
			if len(h.resolved) != 0 || h.metadata.Load() != 0 {
				t.Fatal("PlaybackInfo established detail work before a newly authorized detail")
			}
			h.request("GET", "/Users/10/Items/"+itemID+"?authorized=1", "device", "Fixture App", "Control/1", nil)
			if mode != "other-item" {
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			}
			wantPath, wantQueries := fresh.MediaSources[0].Path, int32(0)
			if mode == "older-same-item" {
				wantQueries = 1
			} else if mode == "other-item" {
				wantPath = old.MediaSources[0].Path
			}
			if got := h.waitResolved(); got.path != wantPath || h.metadata.Load() != wantQueries {
				t.Fatalf("permission changed %s playback data: path=%q metadata=%d", mode, got.path, h.metadata.Load())
			}
		})
	}
}

func TestPreloadPendingMatchingOldListsKeepSelection(t *testing.T) {
	h := newEmbyPreloadHarness(t)
	resumeEntered, nextEntered := make(chan struct{}), make(chan struct{})
	resumeRelease, nextRelease := make(chan struct{}, 1), make(chan struct{}, 1)
	resumeDone, nextDone := make(chan struct{}), make(chan struct{})
	signing, published := make(chan struct{}, 2), make(chan bool, 2)
	signatureRelease := make(chan struct{}, 1)
	t.Cleanup(func() { close(signatureRelease) })
	t.Cleanup(func() { close(resumeRelease); close(nextRelease); <-resumeDone; <-nextDone })
	h.adapter.options.Resolve = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
		identity := preloadHash(raw, ua)
		return identity, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, identity) {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}
			signing <- struct{}{}
			select {
			case <-signatureRelease:
			case <-ctx.Done():
			}
			published <- task.Publish(func() {})
			return playback.PreloadResult{Outcome: playback.PreloadCached}
		}, true
	}
	h.upstream = func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/10/Items/Resume" {
			t.Errorf("unexpected background route=%s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
	}
	h.router = gin.New()
	h.router.Use(PreloadObserver())
	h.router.Any("/*path", func(c *gin.Context) {
		kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
		switch kind {
		case "detail":
			c.JSON(http.StatusOK, preloadItem{ID: "90", Type: "Series"})
		case "resume":
			close(resumeEntered)
			<-resumeRelease
			c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{preloadTestItem("20", "Episode")}})
		case "nextup":
			close(nextEntered)
			<-nextRelease
			c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{preloadTestItem("21", "Episode")}})
		}
	})
	h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
	go func() {
		defer close(resumeDone)
		h.request("GET", "/Users/10/Items/Resume", "device", "Fixture App", "Control/1", nil)
	}()
	<-resumeEntered
	go func() {
		defer close(nextDone)
		h.request("GET", "/Shows/NextUp?UserId=10", "device", "Fixture App", "Control/1", nil)
	}()
	<-nextEntered
	select {
	case <-signing:
	case <-time.After(5 * time.Second):
		t.Fatal("newer active candidate query did not prepare item 20")
	}
	var freshSequence uint64
	h.adapter.mu.Lock()
	for _, device := range h.adapter.devices {
		freshSequence = device.pendingTargetSequence
	}
	h.adapter.mu.Unlock()
	resumeRelease <- struct{}{}
	<-resumeDone
	nextRelease <- struct{}{}
	<-nextDone
	h.adapter.mu.Lock()
	for _, device := range h.adapter.devices {
		if device.pendingTarget.ID != "20" || device.pendingTargetSequence < freshSequence {
			t.Errorf("older natural list changed the newer candidate: item=%q sequence=%d originally=%d", device.pendingTarget.ID, device.pendingTargetSequence, freshSequence)
		}
	}
	h.adapter.mu.Unlock()
	signatureRelease <- struct{}{}
	select {
	case allowed := <-published:
		if !allowed || h.metadata.Load() != 1 {
			t.Fatalf("old list revoked newer preparation: publish=%v metadata=%d", allowed, h.metadata.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("candidate preparation did not finish")
	}
}
