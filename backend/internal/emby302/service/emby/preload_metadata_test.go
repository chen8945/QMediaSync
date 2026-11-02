package emby

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/playback"
)

func TestPreloadFailedLookupDoesNotRejectInflightPI(t *testing.T) {
	for _, mode := range []string{"conditional-playbackinfo", "ordinary-get", "omitted-source-get"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			initial := preloadTestItem("20", "Movie")
			initial.MediaSources[0].Path = ""
			initial.MediaSources[0].Container = "mkv"
			initial.MediaSources[0].RunTimeTicks = 1000
			initial.MediaSources[0].MediaStreams = []preloadStream{{Type: "Video", Codec: "h264"}, {Type: "Audio", Codec: "aac"}}
			if mode != "conditional-playbackinfo" {
				initial.Type = "Episode"
				initial.MediaSources[0].Container = ""
				initial.MediaSources[0].MediaStreams = nil
			}
			fresh := preloadTestItem("20", initial.Type)
			fresh.MediaSources[0].ID = "replacement"
			fresh.MediaSources[0].Path += "-fresh"
			entered, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			queried := make(chan struct{}, 1)
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				if strings.HasSuffix(c.Request.URL.Path, "/PlaybackInfo") {
					close(entered)
					<-release
					sources := fresh.MediaSources
					if mode == "omitted-source-get" {
						sources = append([]preloadSource{preloadTestItem("20", "Episode").MediaSources[0]}, sources...)
					}
					c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: sources})
				} else {
					c.JSON(http.StatusOK, initial)
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				path, status := "/Items/20/PlaybackInfo", http.StatusServiceUnavailable
				if mode != "conditional-playbackinfo" {
					path, status = "/Users/10/Items/20", http.StatusForbidden
				}
				if r.URL.Path != path {
					t.Errorf("unexpected background endpoint: %s", r.URL.Path)
				}
				if mode == "omitted-source-get" {
					_ = json.NewEncoder(w).Encode(preloadItem{ID: "20", Type: "Episode", SeriesID: "90"})
				} else {
					w.WriteHeader(status)
				}
				queried <- struct{}{}
			}
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			t.Cleanup(func() { close(release); <-done })
			go func() {
				defer close(done)
				h.request("POST", "/Items/20/PlaybackInfo?MediaSourceId=replacement", "device", "Fixture App", "Control/1", nil)
			}()
			<-entered
			select {
			case <-queried:
			case <-time.After(4 * time.Second):
				t.Fatal("background conditional lookup did not run")
			}
			waitPreloadPendingJobs(t, h)
			release <- struct{}{}
			<-done
			select {
			case result := <-h.resolved:
				if result.path != fresh.MediaSources[0].Path {
					t.Fatalf("wrong source after failed newer lookup: %q", result.path)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("a failed background lookup caused the successful in-flight natural PlaybackInfo to lose its selected source")
			}
		})
	}
}

func TestPreloadCompletedMetadataSurvivesLeavingParent(t *testing.T) {
	for _, mode := range []string{"series", "direct-episode", "natural-pi"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			old := preloadTestItem("20", "Episode")
			fresh := preloadTestItem("20", "Episode")
			fresh.MediaSources[0].Path += "-fresh"
			partial := old
			partial.MediaSources = []preloadSource{{ID: old.MediaSources[0].ID, ItemID: old.ID}}
			entered, release, done := make(chan struct{}), make(chan struct{}, 1), make(chan struct{})
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				switch {
				case strings.HasSuffix(c.Request.URL.Path, "/PlaybackInfo"):
					c.JSON(http.StatusOK, preloadPlaybackInfo{MediaSources: fresh.MediaSources})
				case strings.HasSuffix(c.Request.URL.Path, "/Resume"):
					close(entered)
					<-release
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{old}})
				case strings.HasSuffix(c.Request.URL.Path, "/NextUp"):
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{partial}})
				case strings.HasSuffix(c.Request.URL.Path, "/20"):
					c.JSON(http.StatusOK, partial)
				case strings.HasSuffix(c.Request.URL.Path, "/90"):
					c.JSON(http.StatusOK, preloadItem{ID: "90", Type: "Series"})
				default:
					c.JSON(http.StatusOK, preloadTestItem("30", "Movie"))
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Users/10/Items/20" {
					_ = json.NewEncoder(w).Encode(fresh)
				} else {
					_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{fresh}})
				}
			}
			if mode != "direct-episode" {
				h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			if mode == "direct-episode" {
				h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			}
			t.Cleanup(func() { close(release); <-done })
			go func() {
				defer close(done)
				h.request("GET", "/Users/10/Items/Resume?ParentId=90", "device", "Fixture App", "Control/1", nil)
			}()
			<-entered
			if mode == "natural-pi" {
				h.request("POST", "/Items/20/PlaybackInfo", "device", "Fixture App", "Control/1", nil)
				h.request("GET", "/Users/10/Items/30", "device", "Fixture App", "Control/1", nil)
				h.request("POST", "/Items/30/PlaybackInfo", "device", "Fixture App", "Control/1", nil)
			} else if result := h.waitResolved(); result.path != fresh.MediaSources[0].Path {
				t.Fatalf("initial metadata result: %q", result.path)
			}
			release <- struct{}{}
			<-done
			if mode != "natural-pi" {
				h.request("GET", "/Users/10/Items/30", "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			if result := h.waitResolved(); result.path != fresh.MediaSources[0].Path {
				t.Fatalf("returning to the parent revived the stale cached list URL: got=%q want=%q metadata=%d", result.path, fresh.MediaSources[0].Path, h.metadata.Load())
			}
		})
	}
}

func TestPreloadNaturalEpisodesDenialInvalidatesPendingList(t *testing.T) {
	for _, mode := range []string{"denied", "restored", "other-item"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			old := preloadTestItem("20", "Episode")
			if mode == "other-item" {
				old = preloadTestItem("21", "Episode")
			}
			fresh := preloadTestItem("20", "Episode")
			fresh.MediaSources[0].Path += "-fresh"
			denied := preloadTestItem("20", "Episode")
			denied.PlayAccess = "None"
			queried := make(chan struct{}, 1)
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				switch {
				case strings.HasSuffix(c.Request.URL.Path, "/NextUp"):
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{old}})
				case strings.HasSuffix(c.Request.URL.Path, "/Episodes"):
					if c.Query("authorized") != "" {
						c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{fresh}})
					} else {
						c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{denied}})
					}
				default:
					c.JSON(http.StatusOK, preloadItem{ID: "91", Type: "Season", SeriesID: "90"})
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				queried <- struct{}{}
			}
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			h.request("GET", "/Shows/90/Episodes?SeasonId=91&UserId=10", "device", "Fixture App", "Control/1", nil)
			if mode == "restored" {
				h.request("GET", "/Shows/90/Episodes?SeasonId=91&UserId=10&authorized=1", "device", "Fixture App", "Control/1", nil)
			}
			h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
			if mode != "denied" {
				want := fresh.MediaSources[0].Path
				if mode == "other-item" {
					want = old.MediaSources[0].Path
				}
				if got := h.waitResolved(); got.path != want || h.metadata.Load() != 0 {
					t.Fatalf("Episodes update damaged %s: path=%q metadata=%d", mode, got.path, h.metadata.Load())
				}
				return
			}
			select {
			case result := <-h.resolved:
				t.Fatalf("newer natural Episodes denial ignored: signed=%q with %d authorization rechecks", result.path, h.metadata.Load())
			case <-queried:
				waitPreloadPendingJobs(t, h)
				if len(h.resolved) != 0 {
					t.Fatal("denied item was resolved after failed revalidation")
				}
			case <-time.After(4 * time.Second):
				// Conservatively skipping is also safe.
			}
		})
	}
}

func TestPreloadNaturalEpisodesDenialRevokesOnlyMatchingSignature(t *testing.T) {
	for _, mode := range []string{"same-item", "other-item", "direct-episode"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			entered, published := make(chan struct{}), make(chan bool, 1)
			release := make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.adapter.options.Resolve = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				identity := preloadHash(raw, ua)
				return identity, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, identity) {
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
					}
					published <- task.Publish(func() bool { return true })
					return playback.PreloadResult{Outcome: playback.PreloadCached}
				}, true
			}
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, _, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				item := preloadTestItem("20", "Episode")
				switch kind {
				case "nextup":
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{item}})
				case "episodes":
					item.PlayAccess = "None"
					if mode == "other-item" {
						item.ID = "21"
					}
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{item}})
				default:
					if mode == "direct-episode" {
						c.JSON(http.StatusOK, item)
						return
					}
					c.JSON(http.StatusOK, preloadItem{ID: "91", Type: "Season", SeriesID: "90"})
				}
			})
			h.upstream = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }
			h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			detail := "/Users/10/Items/91"
			if mode == "direct-episode" {
				detail = "/Users/10/Items/20"
			}
			h.request("GET", detail, "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("signature did not start")
			}
			h.request("GET", "/Shows/90/Episodes?SeasonId=91&UserId=10", "device", "Fixture App", "Control/1", nil)
			if mode == "other-item" {
				release <- struct{}{}
			}
			select {
			case allowed := <-published:
				if allowed != (mode == "other-item") {
					t.Fatalf("Episodes denial changed %s Publish=%v", mode, allowed)
				}
			case <-time.After(time.Second):
				t.Fatal("signature was not canceled by denial")
			}
		})
	}
}

func TestPreloadNaturalEpisodesDenialWhilePendingQuery(t *testing.T) {
	for _, id := range []string{"20", "21"} {
		runEmbyPreloadCase(t, id, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			entered, release := make(chan struct{}), make(chan struct{}, 1)
			t.Cleanup(func() { close(release) })
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				if strings.HasSuffix(c.Request.URL.Path, "/Episodes") {
					denied := preloadTestItem("20", "Episode")
					denied.PlayAccess = "None"
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{denied}})
				} else {
					c.JSON(http.StatusOK, preloadItem{ID: "90", Type: "Series"})
				}
			})
			h.upstream = func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/Resume") {
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusForbidden)
					return
				}
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{preloadTestItem(id, "Episode")}})
			}
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			select {
			case <-entered:
			case <-time.After(4 * time.Second):
				t.Fatal("Resume did not start")
			}
			h.request("GET", "/Shows/90/Episodes?SeasonId=91&UserId=10", "device", "Fixture App", "Control/1", nil)
			release <- struct{}{}
			waitPreloadPendingJobs(t, h)
			if id == "21" {
				if got := h.waitResolved(); got.path != preloadTestItem(id, "Episode").MediaSources[0].Path || h.metadata.Load() != 1 {
					t.Fatalf("unrelated denial damaged candidate: path=%q metadata=%d", got.path, h.metadata.Load())
				}
			} else if len(h.resolved) != 0 {
				t.Fatal("older Resume restored a denied episode before any cached list existed")
			}
		})
	}
}

func TestPreloadNaturalEpisodesKeepsNewerAuthorization(t *testing.T) {
	for _, mode := range []string{"cached-members", "first-members"} {
		runEmbyPreloadCase(t, mode, func(t *testing.T) {
			h := newEmbyPreloadTestHarness(t)
			old := preloadTestItem("20", "Episode")
			fresh := preloadTestItem("20", "Episode")
			fresh.MediaSources[0].Path += "-fresh"
			listEntered, episodesEntered := make(chan struct{}), make(chan struct{})
			listRelease, episodesRelease := make(chan struct{}, 1), make(chan struct{}, 1)
			listDone, episodesDone := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(listRelease); close(episodesRelease); <-listDone; <-episodesDone })
			h.router = gin.New()
			h.router.Use(PreloadObserver())
			h.router.Any("/*path", func(c *gin.Context) {
				kind, id, _, _ := preloadRoute(c.Request.Method, c.Request.URL.Path)
				switch kind {
				case "nextup":
					close(listEntered)
					<-listRelease
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{old}})
				case "episodes":
					item := old
					if c.Query("delayed") != "" {
						close(episodesEntered)
						<-episodesRelease
						item.PlayAccess = "None"
					}
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{item}})
				case "detail":
					if id == "20" {
						c.JSON(http.StatusOK, fresh)
					} else {
						c.JSON(http.StatusOK, preloadItem{ID: "90", Type: "Series"})
					}
				}
			})
			h.upstream = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }
			if mode == "cached-members" {
				h.request("GET", "/Shows/90/Episodes?SeasonId=91&UserId=10", "device", "Fixture App", "Control/1", nil)
			}
			go func() {
				defer close(listDone)
				h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
			}()
			<-listEntered
			go func() {
				defer close(episodesDone)
				h.request("GET", "/Shows/90/Episodes?SeasonId=91&UserId=10&delayed=1", "device", "Fixture App", "Control/1", nil)
			}()
			<-episodesEntered
			h.request("GET", "/Users/10/Items/20", "device", "Fixture App", "Control/1", nil)
			episodesRelease <- struct{}{}
			<-episodesDone
			listRelease <- struct{}{}
			<-listDone
			h.request("GET", "/Users/10/Items/90", "device", "Fixture App", "Control/1", nil)
			if got := h.waitResolved(); got.path != fresh.MediaSources[0].Path || h.metadata.Load() != 0 {
				t.Fatalf("older Episodes replaced newer authorization: path=%q metadata=%d", got.path, h.metadata.Load())
			}
		})
	}
}

func TestPreloadListOmissionUsesCompletedSeasonMetadata(t *testing.T) {
	runEmbyPreloadTest(t, func(t *testing.T) {
		h := newEmbyPreloadTestHarness(t)
		old := preloadTestItem("20", "Episode")
		old.SeasonID = ""
		fresh := preloadTestItem("20", "Episode")
		fresh.MediaSources[0].Path += "-fresh"
		entered, release, published := make(chan string, 4), make(chan struct{}, 1), make(chan bool, 4)
		t.Cleanup(func() { close(release) })
		h.adapter.options.Resolve = func(raw, ua string, _ bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
			identity := preloadHash(raw, ua)
			return identity, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
				if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "fixture"}, identity) {
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}
				entered <- raw
				select {
				case <-release:
				case <-ctx.Done():
				}
				published <- task.Publish(func() bool { return true })
				return playback.PreloadResult{Outcome: playback.PreloadCached}
			}, true
		}
		h.router = gin.New()
		h.router.Use(PreloadObserver())
		h.router.Any("/*path", func(c *gin.Context) {
			if strings.HasSuffix(c.Request.URL.Path, "/NextUp") {
				if c.Query("refresh") != "" {
					c.JSON(http.StatusOK, gin.H{"Items": []gin.H{{"Id": "20", "Type": "Episode", "SeriesId": "90", "SeasonId": "91", "MediaSources": []gin.H{{"Id": "source-20"}}}}})
				} else {
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{old}})
				}
			} else {
				c.JSON(http.StatusOK, preloadItem{ID: "91", Type: "Season", SeriesID: "90"})
			}
		})
		h.upstream = func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{fresh}})
		}
		h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
		select {
		case path := <-entered:
			if path != fresh.MediaSources[0].Path {
				t.Fatalf("initial season lookup did not use fresh metadata: %q", path)
			}
		case <-time.After(4 * time.Second):
			t.Fatal("season candidate did not start signing")
		}
		h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10&refresh=1", "device", "Fixture App", "Control/1", nil)
		release <- struct{}{}
		select {
		case allowed := <-published:
			if !allowed {
				t.Fatal("same-ID Path omission restored the pre-lookup URL and canceled the fresh signature")
			}
		case <-time.After(time.Second):
			t.Fatal("signature did not finish")
		}
	})
}

func TestPreloadOmittedPathDoesNotBecomeAuthoritative(t *testing.T) {
	runEmbyPreloadTest(t, func(t *testing.T) {
		h := newEmbyPreloadTestHarness(t)
		old := preloadTestItem("20", "Episode")
		old.SeasonID = ""
		fresh := preloadTestItem("20", "Episode")
		fresh.MediaSources[0].Path += "-fresh"
		entered, release := make(chan struct{}, 1), make(chan struct{}, 1)
		t.Cleanup(func() { close(release) })
		h.router = gin.New()
		h.router.Use(PreloadObserver())
		h.router.Any("/*path", func(c *gin.Context) {
			if strings.HasSuffix(c.Request.URL.Path, "/NextUp") {
				if c.Query("refresh") != "" {
					c.JSON(http.StatusOK, gin.H{"Items": []gin.H{{"Id": "20", "Type": "Episode", "SeriesId": "90", "SeasonId": "91", "MediaSources": []gin.H{{"Id": "source-20"}}}}})
				} else {
					c.JSON(http.StatusOK, preloadItems{Items: []preloadItem{old}})
				}
			} else {
				c.JSON(http.StatusOK, preloadItem{ID: "91", Type: "Season", SeriesID: "90"})
			}
		})
		h.upstream = func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_ = json.NewEncoder(w).Encode(preloadItems{Items: []preloadItem{fresh}})
		}
		h.request("GET", "/Users/10/Items/91", "device", "Fixture App", "Control/1", nil)
		h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10", "device", "Fixture App", "Control/1", nil)
		select {
		case <-entered:
		case <-time.After(4 * time.Second):
			t.Fatal("pending scope lookup did not start")
		}
		h.request("GET", "/Shows/NextUp?SeriesId=90&UserId=10&refresh=1", "device", "Fixture App", "Control/1", nil)
		release <- struct{}{}
		if result := h.waitResolved(); result.path != fresh.MediaSources[0].Path {
			t.Fatalf("an inherited old Path was treated as a newer explicit Path: got=%q want=%q metadata=%d", result.path, fresh.MediaSources[0].Path, h.metadata.Load())
		}
	})
}

func TestPreloadPendingSourceAndAddressOrdering(t *testing.T) {
	old := sequencePreloadItem(preloadTestItem("20", "Episode"), 2)
	fresh := preloadTestItem("20", "Episode")
	fresh.MediaSources[0].Path += "-lookup"
	fresh = sequencePreloadItem(fresh, 3)
	for _, mode := range []string{"omitted", "explicit-url", "empty-sources", "replacement-source", "denied"} {
		t.Run(mode, func(t *testing.T) {
			current := preloadItem{ID: "20", Type: "Episode", MediaSources: []preloadSource{{ID: old.MediaSources[0].ID}}}
			switch mode {
			case "explicit-url":
				current.MediaSources[0].Path = old.MediaSources[0].Path + "-natural"
			case "empty-sources":
				current.MediaSources = []preloadSource{}
			case "replacement-source":
				current.MediaSources[0].ID = "replacement"
			case "denied":
				current.PlayAccess = "None"
			}
			current = mergePreloadPendingItem(sequencePreloadItem(current, 4), old)
			result := mergePreloadPendingItem(fresh, current)
			if result.sourceSequence != 4 {
				t.Fatalf("membership/permission order regressed: %d", result.sourceSequence)
			}
			switch mode {
			case "omitted":
				if result.MediaSources[0].Path != fresh.MediaSources[0].Path {
					t.Fatal("inherited address masked newer lookup evidence")
				}
			case "explicit-url":
				if result.MediaSources[0].Path != current.MediaSources[0].Path {
					t.Fatal("lookup replaced newer explicit address")
				}
			case "empty-sources":
				if result.MediaSources == nil || len(result.MediaSources) != 0 {
					t.Fatal("lookup restored cleared membership")
				}
			case "replacement-source":
				if len(result.MediaSources) != 1 || result.MediaSources[0].ID != "replacement" || result.MediaSources[0].Path != "" {
					t.Fatal("lookup restored a removed source")
				}
			case "denied":
				if validPreloadItem(result) {
					t.Fatal("lookup restored denied permission")
				}
			}
		})
	}
}
