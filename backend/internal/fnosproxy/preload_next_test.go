package fnosproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/playback"
)

func TestPreloadMissingQueueAfterQueueSaturation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		queue, items := preloadQueueFixture(t, root)
		current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
		source := itemSources(current, items[0])[0]
		var users, lists atomic.Int32
		options, signs := preloadTestOptions(t, root)
		inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/Users/Me":
				users.Add(1)
				_, _ = io.WriteString(w, `{"Id":"user"}`)
			case "/Users/user/Items/" + current:
				_ = json.NewEncoder(w).Encode(items[0])
			case "/Shows/" + series + "/Episodes":
				lists.Add(1)
				_ = json.NewEncoder(w).Encode(queue)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
		preloadSeedPlayer(t, inst, false, source)
		report := fmt.Sprintf(`{"ItemId":%q,"MediaSourceId":%q,"PlaySessionId":"session","IsPaused":false,"PositionTicks":2060000000}`, current, source.ID)
		preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
		entered, released := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(released) })
		t.Cleanup(release)
		if !options.Preloader.Submit(playback.PreloadCandidate{Key: "blocking", Scope: "blocking", Kind: playback.PreloadNext,
			Run: func(ctx context.Context, _ *playback.PreloadTask) playback.PreloadResult {
				close(entered)
				select {
				case <-released:
				case <-ctx.Done():
				}
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}}) {
			t.Fatal("无法建立队列占用")
		}
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("占用作业未开始")
		}
		drained := make(chan struct{}, 32)
		for index := range 32 {
			key := fmt.Sprintf("queued-%d", index)
			if !options.Preloader.Submit(playback.PreloadCandidate{Key: key, Scope: key, Kind: playback.PreloadNext,
				Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult {
					drained <- struct{}{}
					return playback.PreloadResult{Outcome: playback.PreloadSkipped}
				}}) {
				t.Fatalf("尚未填满 32 项队列：index=%d", index)
			}
		}
		if options.Preloader.Submit(playback.PreloadCandidate{Key: "overflow", Scope: "overflow", Kind: playback.PreloadNext,
			Run: func(context.Context, *playback.PreloadTask) playback.PreloadResult { return playback.PreloadResult{} }}) {
			t.Fatal("32 项队列未拒绝额外候选")
		}
		preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
		release()
		for range 32 {
			select {
			case <-drained:
			case <-time.After(3 * time.Second):
				t.Fatal("队列未排空")
			}
		}
		waitPreloadWorker(t, options.Preloader)
		if users.Load() != 0 || lists.Load() != 0 || len(signs) != 0 {
			t.Fatal("队列拒绝后无新进度仍自动补查")
		}
		for range 10 {
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
		}
		waitPreloadWorker(t, options.Preloader)
		if users.Load() != 1 || lists.Load() != 1 || len(signs) != 1 {
			t.Fatalf("队列拒绝消耗补查机会或重复执行：users=%d lists=%d signs=%d", users.Load(), lists.Load(), len(signs))
		}
		if sign := <-signs; !strings.Contains(sign.url, "pickcode=next") || sign.ua != "Confirmed Player" {
			t.Fatalf("排空后准备了错误后继：sign=%+v", sign)
		}
	})
}

func TestPreloadMissingQueueSignatureRetry(t *testing.T) {
	for _, action := range []string{"retry", "pause", "stop", "denied", "expired"} {
		runPreloadTest(t, action, func(t *testing.T) {
			root := t.TempDir()
			_, items := preloadQueueFixture(t, root)
			current, next := stringField(items[0], "Id"), stringField(items[1], "Id")
			series, source := stringField(items[0], "SeriesId"), itemSources(current, items[0])[0]
			listedNext := make(map[string]any)
			for key, value := range items[1] {
				if key != "MediaSources" {
					listedNext[key] = value
				}
			}
			var users, lists, details, factories, signatures atomic.Int32
			options, _ := preloadTestOptions(t, root)
			options.NewPreload = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				factories.Add(1)
				if force || ua != "Confirmed Player" || !strings.Contains(raw, "pickcode=next") {
					t.Errorf("重试候选来源发生变化：raw=%s ua=%s force=%v", raw, ua, force)
				}
				return raw, func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
					if task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
						signatures.Add(1)
					}
					return playback.PreloadResult{Outcome: playback.PreloadTransient, HTTPStatus: http.StatusServiceUnavailable}
				}, true
			}
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					users.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					if r.URL.Query().Get("denied") != "" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					lists.Add(1)
					_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{items[0], listedNext}})
				case "/Users/user/Items/" + next:
					details.Add(1)
					_ = json.NewEncoder(w).Encode(items[1])
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadSeedPlayer(t, inst, false, source)
			report := map[string]any{"ItemId": current, "MediaSourceId": source.ID, "PlaySessionId": "session", "IsPaused": false, "PositionTicks": 2060000000}
			send := func(route string) {
				body, _ := json.Marshal(report)
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing"+route, string(body), false)
			}
			send("")
			send("/Progress")
			waitPreloadWorker(t, options.Preloader)
			if signatures.Load() != 1 {
				t.Fatalf("首次补查未签名：sign=%d users=%d lists=%d details=%d", signatures.Load(), users.Load(), lists.Load(), details.Load())
			}
			send("/Progress")
			waitPreloadWorker(t, options.Preloader)
			if signatures.Load() != 1 {
				t.Fatal("冷却前进度不得重新签名")
			}
			time.Sleep(31 * time.Second)
			waitPreloadWorker(t, options.Preloader)
			if signatures.Load() != 1 {
				t.Fatal("冷却后没有新进度不得自动重试")
			}
			switch action {
			case "retry":
				entered, released := make(chan struct{}), make(chan struct{})
				release := sync.OnceFunc(func() { close(released) })
				t.Cleanup(release)
				if !options.Preloader.Submit(playback.PreloadCandidate{Key: "busy", Scope: "other-device", Kind: playback.PreloadNext,
					Account: playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"},
					Run: func(ctx context.Context, _ *playback.PreloadTask) playback.PreloadResult {
						close(entered)
						select {
						case <-released:
						case <-ctx.Done():
						}
						return playback.PreloadResult{Outcome: playback.PreloadSkipped}
					}}) {
					t.Fatal("无法建立同账号占用")
				}
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("同账号作业未开始")
				}
				for range 5 {
					send("/Progress")
				}
				release()
			case "pause":
				report["IsPaused"] = true
				send("/Progress")
				report["IsPaused"] = false
			case "stop":
				send("/Stopped")
			case "denied":
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current+"?denied=true", "", false)
			case "expired":
				inst.preload.mu.Lock()
				for _, device := range inst.preload.devices {
					if device.nextLookupRetry.nextLookup != nil {
						device.nextLookupRetry.nextLookup.expiresAt = time.Now().Add(-time.Second)
					}
				}
				inst.preload.mu.Unlock()
			}
			send("/Progress")
			waitPreloadWorker(t, options.Preloader)
			want := int32(1)
			if action == "retry" {
				want = 2
			}
			if signatures.Load() != want {
				t.Fatalf("冷却后新进度的签名次数不符：got=%d want=%d", signatures.Load(), want)
			}
			if action == "retry" {
				time.Sleep(31 * time.Second)
			}
			send("/Progress")
			waitPreloadWorker(t, options.Preloader)
			if signatures.Load() != want || factories.Load() != 1 || users.Load() != 1 || lists.Load() != 1 || details.Load() != 1 {
				t.Fatalf("重新查询、重置额度或撤销后复活：sign=%d factory=%d users=%d lists=%d details=%d", signatures.Load(), factories.Load(), users.Load(), lists.Load(), details.Load())
			}
		})
	}
}

func TestPreloadMissingQueueQueriesOnce(t *testing.T) {
	for _, missingSource := range []bool{false, true} {
		runPreloadTest(t, map[bool]string{false: "complete", true: "final-detail"}[missingSource], func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			current, next := stringField(items[0], "Id"), stringField(items[1], "Id")
			series, season := stringField(items[0], "SeriesId"), stringField(items[0], "SeasonId")
			source := itemSources(current, items[0])[0]
			list := queue
			if missingSource {
				copy := make(map[string]any)
				for key, value := range items[1] {
					if key != "MediaSources" {
						copy[key] = value
					}
				}
				list = map[string]any{"Items": []any{items[0], copy}}
			}
			var users, lists, details, reports atomic.Int32
			report := ` {"ItemId":"` + current + `","MediaSourceId":"` + source.ID + `","PlaySessionId":"session","IsPaused":false,"PositionTicks":2060000000,"unknown":"untouched"} `
			options, signs := preloadTestOptions(t, root)
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					users.Add(1)
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Users/user/Items/" + next:
					details.Add(1)
					_ = json.NewEncoder(w).Encode(items[1])
				case "/Shows/" + series + "/Episodes":
					lists.Add(1)
					query := r.URL.Query()
					if query.Get("UserId") != "user" || query.Get("SeasonId") != season || query.Get("Limit") != "64" ||
						query.Get("SortBy") != "IndexNumber" || query.Get("SortOrder") != "Ascending" ||
						query.Get("Fields") != "Path,MediaSources,SeriesId,SeasonId" || query.Get("StartItemId") != "" || query.Get("AdjacentTo") != "" ||
						query.Get("api_key") != "valid" || r.Header.Get("Accept-Encoding") != "identity" {
						t.Errorf("补查参数不符：%v", query)
					}
					_ = json.NewEncoder(w).Encode(list)
				case "/Sessions/Playing", "/Sessions/Playing/Progress":
					body, _ := io.ReadAll(r.Body)
					if string(body) != report {
						t.Error("原始进度请求发生变化")
					}
					reports.Add(1)
					w.Header().Set("X-Upstream", "preserved")
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("补查访问意外接口（禁止冷源探测）：%s", r.URL.Path)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			preloadSeedPlayer(t, inst, false, source)
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
			for range 100 {
				response := preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
				if response.Code != http.StatusNoContent || response.Header().Get("X-Upstream") != "preserved" {
					t.Fatal("进度响应未透传")
				}
			}
			sign := waitPreloadSign(t, signs)
			waitPreloadWorker(t, options.Preloader)
			wantDetails := int32(0)
			if missingSource {
				wantDetails = 1
			}
			if !strings.Contains(sign.url, "pickcode=next") || sign.ua != "Confirmed Player" || users.Load() != 1 || lists.Load() != 1 || details.Load() != wantDetails || reports.Load() != 101 || len(signs) != 0 {
				t.Fatalf("有限补查未保持单次候选：sign=%+v users=%d lists=%d details=%d reports=%d extra=%d", sign, users.Load(), lists.Load(), details.Load(), reports.Load(), len(signs))
			}
		})
	}
}

func TestPreloadQueriedNextRejectsUnprovenList(t *testing.T) {
	for _, test := range []string{"valid", "gap", "empty", "last", "missing-anchor", "duplicate", "cross-series", "cross-season", "reverse", "same-index", "missing-index", "fractional-index", "wrong-kind", "changed-current-source", "partial", "start-offset", "unknown-schema", "at-limit", "too-many"} {
		runPreloadTest(t, test, func(t *testing.T) {
			queue, items := preloadQueueFixture(t, t.TempDir())
			playing, _ := compatiblePreloadItem(items[0])
			switch test {
			case "gap":
				items[1]["IndexNumber"] = float64(3)
			case "empty":
				queue["Items"] = []any{}
			case "last":
				queue["Items"] = []any{items[0]}
				queue["TotalRecordCount"] = float64(1)
			case "missing-anchor":
				items[0]["Id"] = "different-anchor"
			case "duplicate":
				items[1]["Id"] = playing.id
			case "cross-series":
				items[1]["SeriesId"] = "different-series"
			case "cross-season":
				items[1]["SeasonId"] = "different-season"
			case "reverse":
				queue["Items"] = []any{items[1], items[0]}
			case "same-index":
				items[1]["IndexNumber"] = float64(1)
			case "missing-index":
				delete(items[1], "IndexNumber")
			case "fractional-index":
				items[1]["IndexNumber"] = 1.5
			case "wrong-kind":
				items[1]["Type"] = "Movie"
			case "changed-current-source":
				items[0]["MediaSources"].([]any)[0].(map[string]any)["Path"] = "/different/current.strm"
			case "partial":
				queue["TotalRecordCount"] = float64(3)
			case "start-offset":
				queue["StartIndex"] = float64(1)
			case "unknown-schema":
				delete(queue, "Items")
			case "at-limit", "too-many":
				count := preloadEpisodeLimit
				if test == "too-many" {
					count++
				}
				values := make([]any, count)
				for index := range values {
					item := make(map[string]any)
					for key, value := range items[1] {
						item[key] = value
					}
					item["Id"], item["IndexNumber"] = fmt.Sprintf("episode-%d", index), float64(index+1)
					values[index] = item
				}
				values[0], values[1] = items[0], items[1]
				queue["Items"] = values
				queue["TotalRecordCount"] = float64(count)
			}
			next, valid := compatiblePreloadQueriedNext(queue, playing)
			want := test == "valid" || test == "gap"
			if valid != want || valid && next.id != stringField(items[1], "Id") {
				t.Fatalf("未证明顺序仍选后继：next=%+v valid=%v want=%v", next, valid, want)
			}
		})
	}
}

func TestPreloadMissingQueueCanceledByNewEvidence(t *testing.T) {
	for _, phase := range []string{"list", "signing"} {
		for _, action := range []string{"pause", "stop", "new-session", "new-item", "detail", "source", "natural-list", "denied"} {
			runPreloadTest(t, phase+"/"+action, func(t *testing.T) {
				root := t.TempDir()
				queue, items := preloadQueueFixture(t, root)
				current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
				source := itemSources(current, items[0])[0]
				entered, released, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{}, 1)
				release := sync.OnceFunc(func() { close(released) })
				t.Cleanup(release)
				options, _ := preloadTestOptions(t, root)
				var published, lists atomic.Int32
				options.NewPreload = func(raw, ua string, force bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
					return raw, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "owner"}, raw) {
							return playback.PreloadResult{Outcome: playback.PreloadSkipped}
						}
						if phase == "signing" {
							close(entered)
							select {
							case <-released:
							case <-ctx.Done():
								canceled <- struct{}{}
							}
						}
						task.Publish(func() { published.Add(1) })
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}, true
				}
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/Users/Me":
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/" + current:
						if r.URL.Query().Get("denied") != "" {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						_ = json.NewEncoder(w).Encode(items[0])
					case "/Shows/" + series + "/Episodes":
						if r.URL.Query().Get("Limit") != "" {
							lists.Add(1)
							if phase == "list" {
								close(entered)
								select {
								case <-released:
								case <-r.Context().Done():
									canceled <- struct{}{}
									return
								}
							}
							_ = json.NewEncoder(w).Encode(queue)
							return
						}
						_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
					default:
						w.WriteHeader(http.StatusNoContent)
					}
				}))
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
				preloadSeedPlayer(t, inst, false, source)
				report := map[string]any{"ItemId": current, "MediaSourceId": source.ID, "PlaySessionId": "session", "IsPaused": false, "PositionTicks": 2060000000}
				send := func(route string) {
					body, _ := json.Marshal(report)
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing"+route, string(body), false)
				}
				send("")
				send("/Progress")
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					t.Fatal("补查未进入目标阶段")
				}
				switch action {
				case "pause":
					report["IsPaused"] = true
					send("/Progress")
				case "stop":
					send("/Stopped")
				case "new-session":
					report["PlaySessionId"] = "new-session"
					send("")
				case "new-item":
					report["ItemId"], report["MediaSourceId"] = stringField(items[1], "Id"), itemSources(stringField(items[1], "Id"), items[1])[0].ID
					send("")
				case "detail", "denied":
					preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current+"?"+action+"=true", "", false)
				case "source":
					preloadServeInstance(t, inst, http.MethodGet, "/emby/Items/"+current+"/PlaybackInfo", "", false)
				case "natural-list":
					preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
				}
				select {
				case <-canceled:
				case <-time.After(time.Second):
					t.Error("状态更新没有及时取消补查或签名")
				}
				release()
				waitPreloadWorker(t, options.Preloader)
				if published.Load() != 0 || lists.Load() != 1 {
					t.Fatalf("过期资料发布或重复补查：published=%d lists=%d", published.Load(), lists.Load())
				}
			})
		}
	}
}

func TestPreloadMissingQueueHonorsLimits(t *testing.T) {
	for _, test := range []string{"user-mismatch", "user-retry-budget", "list-empty", "list-last", "list-ambiguous", "list-wrong-season", "list-html", "list-unknown", "list-denied", "observed-empty", "observed-last", "observed-ambiguous", "missing-stream", "outside-window", "missing-season"} {
		runPreloadTest(t, test, func(t *testing.T) {
			root := t.TempDir()
			queue, items := preloadQueueFixture(t, root)
			current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
			source := itemSources(current, items[0])[0]
			switch test {
			case "list-empty", "observed-empty":
				queue["Items"], queue["TotalRecordCount"] = []any{}, float64(0)
			case "list-last", "observed-last":
				queue["Items"], queue["TotalRecordCount"] = []any{items[0]}, float64(1)
			case "list-ambiguous", "observed-ambiguous":
				items[1]["MediaSources"] = append(items[1]["MediaSources"].([]any), map[string]any{"Id": "ambiguous", "Path": preloadTestFile(t, root, "ambiguous")})
			case "list-wrong-season":
				items[1]["SeasonId"] = "wrong-season"
			case "missing-season":
				delete(items[0], "SeasonId")
			}
			var users, lists, factories atomic.Int32
			options, _ := preloadTestOptions(t, root)
			options.NewPreload = func(string, string, bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
				factories.Add(1)
				return "", nil, false
			}
			inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/Users/Me":
					attempt := users.Add(1)
					if test == "user-mismatch" {
						_, _ = io.WriteString(w, `{"Id":"another-user"}`)
						return
					}
					if test == "user-retry-budget" && attempt == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					_, _ = io.WriteString(w, `{"Id":"user"}`)
				case "/Users/user/Items/" + current:
					_ = json.NewEncoder(w).Encode(items[0])
				case "/Shows/" + series + "/Episodes":
					if r.URL.Query().Get("Limit") != "" {
						lists.Add(1)
					}
					switch test {
					case "list-html":
						_, _ = io.WriteString(w, "<!DOCTYPE html><html></html>")
					case "list-unknown":
						_, _ = io.WriteString(w, `{"code":0,"data":[]}`)
					case "list-denied":
						w.WriteHeader(http.StatusForbidden)
					default:
						_ = json.NewEncoder(w).Encode(queue)
					}
				case "/Sessions/Playing", "/Sessions/Playing/Progress":
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("不应额外探测：%s", r.URL.Path)
				}
			}))
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
			if strings.HasPrefix(test, "observed-") {
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			}
			if test != "missing-stream" {
				preloadSeedPlayer(t, inst, false, source)
			}
			position := "2060000000"
			if test == "outside-window" {
				position = "20000000"
			}
			report := `{"ItemId":"` + current + `","MediaSourceId":"` + source.ID + `","PlaySessionId":"session","IsPaused":false,"PositionTicks":` + position + `}`
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
			for range 20 {
				preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
			}
			waitPreloadWorker(t, options.Preloader)
			wantUsers, wantLists := int32(1), int32(1)
			switch {
			case test == "user-mismatch":
				wantLists = 0
			case test == "user-retry-budget":
				wantUsers, wantLists = 2, 0
			case strings.HasPrefix(test, "observed-"), test == "missing-stream", test == "outside-window", test == "missing-season":
				wantUsers, wantLists = 0, 0
			}
			if users.Load() != wantUsers || lists.Load() != wantLists || factories.Load() != 0 {
				t.Fatalf("资格或预算未保留：users=%d/%d lists=%d/%d factories=%d", users.Load(), wantUsers, lists.Load(), wantLists, factories.Load())
			}
		})
	}
}

func TestPreloadMissingQueueKeepsNaturalRequestOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		queue, items := preloadQueueFixture(t, root)
		current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
		source := itemSources(current, items[0])[0]
		lookupEntered, lookupCanceled := make(chan struct{}), make(chan struct{})
		naturalEntered, naturalReleased := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(naturalReleased) })
		t.Cleanup(release)
		var lists atomic.Int32
		options, signs := preloadTestOptions(t, root)
		inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/Users/Me":
				_, _ = io.WriteString(w, `{"Id":"user"}`)
			case "/Users/user/Items/" + current:
				_ = json.NewEncoder(w).Encode(items[0])
			case "/Shows/" + series + "/Episodes":
				if r.URL.Query().Get("Limit") != "" {
					lists.Add(1)
					close(lookupEntered)
					<-r.Context().Done()
					close(lookupCanceled)
					return
				}
				if r.URL.Query().Get("older") != "" {
					close(naturalEntered)
					select {
					case <-naturalReleased:
					case <-r.Context().Done():
						return
					}
					_ = json.NewEncoder(w).Encode(queue)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
		preloadSeedPlayer(t, inst, false, source)
		report := `{"ItemId":"` + current + `","MediaSourceId":"` + source.ID + `","PlaySessionId":"session","IsPaused":false,"PositionTicks":2060000000}`
		preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
		preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
		select {
		case <-lookupEntered:
		case <-time.After(time.Second):
			t.Fatal("补查没有开始")
		}
		done := make(chan struct{})
		go func() {
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes?older=true", "", false)
			close(done)
		}()
		select {
		case <-naturalEntered:
		case <-time.After(time.Second):
			t.Fatal("自然列表没有开始")
		}
		select {
		case <-lookupCanceled:
		case <-time.After(time.Second):
			t.Fatal("自然列表仍在途时未取消旧补查")
		}
		preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes?updated=true", "", false)
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("旧自然响应没有结束")
		}
		for range 20 {
			preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
		}
		waitPreloadWorker(t, options.Preloader)
		inst.preload.mu.Lock()
		for _, device := range inst.preload.devices {
			if len(device.queue) != 0 || device.queueSeries != series {
				t.Error("迟到自然响应覆盖了更新的空列表")
			}
		}
		inst.preload.mu.Unlock()
		if lists.Load() != 1 || len(signs) != 0 {
			t.Fatalf("旧列表恢复补查或发布：lists=%d signs=%d", lists.Load(), len(signs))
		}
	})
}

func TestPreloadMissingQueueWaitsForNaturalList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		queue, items := preloadQueueFixture(t, root)
		current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
		source := itemSources(current, items[0])[0]
		entered, released := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(released) })
		t.Cleanup(release)
		var lookups atomic.Int32
		options, signs := preloadTestOptions(t, root)
		inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/Users/Me":
				_, _ = io.WriteString(w, `{"Id":"user"}`)
			case "/Users/user/Items/" + current:
				_ = json.NewEncoder(w).Encode(items[0])
			case "/Shows/" + series + "/Episodes":
				if r.URL.Query().Get("Limit") != "" {
					lookups.Add(1)
					_ = json.NewEncoder(w).Encode(queue)
					return
				}
				close(entered)
				select {
				case <-released:
				case <-r.Context().Done():
					return
				}
				_ = json.NewEncoder(w).Encode(queue)
			default:
				w.WriteHeader(http.StatusNoContent)
			}
		}))
		preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
		preloadSeedPlayer(t, inst, false, source)
		report := `{"ItemId":"` + current + `","MediaSourceId":"` + source.ID + `","PlaySessionId":"session","IsPaused":false,"PositionTicks":2060000000}`
		preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
		done := make(chan struct{})
		go func() {
			preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes", "", false)
			close(done)
		}()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("自然列表请求未开始")
		}
		preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
		waitPreloadWorker(t, options.Preloader)
		if lookups.Load() != 0 || len(signs) != 0 {
			t.Fatal("自然列表未返回时重复补查")
		}
		release()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("自然列表请求未结束")
		}
		preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
		if sign := waitPreloadSign(t, signs); !strings.Contains(sign.url, "pickcode=next") || lookups.Load() != 0 {
			t.Fatalf("自然列表完成后未复用原规则：sign=%+v lookups=%d", sign, lookups.Load())
		}
	})
}

func TestPreloadMissingQueueRemembersNaturalSeries(t *testing.T) {
	for _, timing := range []string{"before-playing", "while-playing"} {
		for _, observed := range []string{"empty", "last", "ambiguous", "only-other-series", "history-full"} {
			runPreloadTest(t, timing+"/"+observed, func(t *testing.T) {
				root := t.TempDir()
				queue, items := preloadQueueFixture(t, root)
				current, series := stringField(items[0], "Id"), stringField(items[0], "SeriesId")
				source := itemSources(current, items[0])[0]
				natural := map[string]any{"Items": []any{}}
				switch observed {
				case "last":
					natural["Items"] = []any{items[0]}
				case "ambiguous":
					next := make(map[string]any)
					for key, value := range items[1] {
						next[key] = value
					}
					next["MediaSources"] = append([]any{map[string]any{"Id": "ambiguous", "Path": preloadTestFile(t, root, "ambiguous")}}, items[1]["MediaSources"].([]any)...)
					natural["Items"] = []any{items[0], next}
				}
				var users, lookups atomic.Int32
				options, signs := preloadTestOptions(t, root)
				inst := preloadTestInstance(t, options, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/Users/Me":
						users.Add(1)
						_, _ = io.WriteString(w, `{"Id":"user"}`)
					case "/Users/user/Items/" + current:
						_ = json.NewEncoder(w).Encode(items[0])
					case "/Shows/" + series + "/Episodes":
						if r.URL.Query().Get("Limit") != "" {
							lookups.Add(1)
							_ = json.NewEncoder(w).Encode(queue)
							return
						}
						_ = json.NewEncoder(w).Encode(natural)
					case "/Shows/other-series/Episodes":
						_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
					default:
						if strings.HasPrefix(r.URL.Path, "/Shows/other-series-") {
							_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
							return
						}
						w.WriteHeader(http.StatusNoContent)
					}
				}))
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Users/user/Items/"+current, "", false)
				report := `{"ItemId":"` + current + `","MediaSourceId":"` + source.ID + `","PlaySessionId":"session","IsPaused":false,"PositionTicks":2060000000}`
				start := func() {
					preloadSeedPlayer(t, inst, false, source)
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing", report, false)
				}
				if timing == "while-playing" {
					start()
				}
				if observed != "only-other-series" && observed != "history-full" {
					preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/"+series+"/Episodes?UserId=user", "", false)
				}
				preloadServeInstance(t, inst, http.MethodGet, "/emby/Shows/other-series/Episodes?UserId=user", "", false)
				if observed == "history-full" {
					for index := range sourceCacheCapacity {
						preloadServeInstance(t, inst, http.MethodGet, fmt.Sprintf("/emby/Shows/other-series-%d/Episodes?UserId=user", index), "", false)
					}
					inst.preload.mu.Lock()
					for _, device := range inst.preload.devices {
						if len(device.queueSeriesSeen) != sourceCacheCapacity || device.queueSeriesSeen[series] {
							t.Error("已观察列表历史未保持容量上限")
						}
					}
					inst.preload.mu.Unlock()
				}
				if timing == "before-playing" {
					start()
				}
				for range 20 {
					preloadServeInstance(t, inst, http.MethodPost, "/emby/Sessions/Playing/Progress", report, false)
				}
				want := int32(0)
				if observed == "only-other-series" {
					want = 1
					if sign := waitPreloadSign(t, signs); !strings.Contains(sign.url, "pickcode=next") {
						t.Fatalf("从未见当前剧列表时未正常补查：%+v", sign)
					}
				}
				waitPreloadWorker(t, options.Preloader)
				if users.Load() != want || lookups.Load() != want || len(signs) != 0 {
					t.Fatalf("其他剧列表覆盖已知负证据：users=%d lookups=%d signs=%d want=%d", users.Load(), lookups.Load(), len(signs), want)
				}
			})
		}
	}
}
