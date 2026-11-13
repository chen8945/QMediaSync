package embyclientrestgo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	jellyfinTestItem    = "123456781234123412341234567890ab"
	jellyfinTestVersion = "223456781234123412341234567890ab"
	jellyfinTestLibrary = "323456781234123412341234567890ab"
)

func TestJellyfinNativeManagement(t *testing.T) {
	for _, serverInfo := range []struct{ version, product string }{{"12.4", "Jellyfin Server"}, {"10.11.11", "Jellyfin Server"}, {"12.4", "Jellyfin"}} {
		t.Run(serverInfo.version+"/"+serverInfo.product, func(t *testing.T) {
			var probes, itemRequests, refreshes int
			// 子路径末尾的 emby 是用户配置的一部分，不能按产品名删掉。
			const base = "/proxy/emby"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == base+"/System/Ping" {
					fmt.Fprintf(w, "%q", serverInfo.product)
					return
				}
				if r.URL.Path == base+"/System/Info/Public" {
					probes++
					fmt.Fprintf(w, `{"Id":"server-1","ProductName":%q,"Version":%q}`, serverInfo.product, serverInfo.version)
					return
				}
				if r.URL.Query().Has("api_key") || !strings.Contains(r.Header.Get("Authorization"), `Token="test-key"`) {
					t.Errorf("Jellyfin 请求未使用原生认证：%s", r.URL.Path)
				}
				switch r.URL.Path {
				case base + "/Library/MediaFolders":
					fmt.Fprintf(w, `{"Items":[{"Id":%q,"Name":"电影"}]}`, strings.ToUpper(jellyfinTestLibrary))
				case base + "/Users":
					fmt.Fprint(w, `[{"Id":"user-1","Policy":{"EnableAllFolders":true}},{"Id":"user-2"}]`)
				case base + "/Items":
					itemRequests++
					if r.URL.Query().Get("Ids") != "" {
						fmt.Fprintf(w, `{"TotalRecordCount":1,"Items":[{"Id":%q,"Type":"Movie","Path":"/media/test.strm"}]}`, jellyfinTestItem)
						return
					}
					if r.URL.Query().Get("Fields") != jellyfinSnapshotFields || r.URL.Query().Get("SortBy") != "DateCreated,SortName" || r.URL.Query().Get("MinDateLastSaved") != "2026-01-01T00:00:00Z" {
						t.Errorf("Jellyfin 分页参数不正确：%s", r.URL.RawQuery)
					}
					id, sourceType := jellyfinTestItem, "Default"
					if r.URL.Query().Get("StartIndex") == "1" {
						id, sourceType = jellyfinTestVersion, "Grouping"
					}
					fmt.Fprintf(w, `{"TotalRecordCount":2,"Items":[{"Id":%q,"Type":"Movie","ParentId":%q,"Path":"/media/test.strm","DateCreated":"2026-01-01T00:00:00Z","MediaSources":[{"Id":%q,"Type":%q,"Path":"https://media.test/stream"}]}]}`, strings.ToUpper(id), jellyfinTestLibrary, strings.ToUpper(id), sourceType)
				case base + "/Videos/" + jellyfinTestItem + "/AdditionalParts":
					if r.URL.Query().Has("Fields") {
						t.Error("分段未请求 Jellyfin 快照字段")
					}
					fmt.Fprintf(w, `{"TotalRecordCount":1,"Items":[{"Id":%q,"Type":"Movie","MediaSources":[{"Id":%q,"Type":"Default"}]}]}`, jellyfinTestVersion, jellyfinTestVersion)
				case base + "/Items/" + jellyfinTestItem + "/Ancestors":
					fmt.Fprintf(w, `[{"Id":%q,"Path":"/media"}]`, strings.ToUpper(jellyfinTestLibrary))
				case base + "/Library/VirtualFolders":
					fmt.Fprintf(w, `[{"ItemId":%q,"Locations":["/media"]}]`, strings.ToUpper(jellyfinTestLibrary))
				case base + "/Items/" + jellyfinTestItem + "/Refresh", base + "/Items/" + jellyfinTestLibrary + "/Refresh":
					refreshes++
					q := r.URL.Query()
					if r.Method != http.MethodPost || q.Get("MetadataRefreshMode") != "Default" || q.Get("ImageRefreshMode") != "Default" || q.Has("Fields") || q.Has("Recursive") || q.Has("ReplaceAllMetadata") || q.Has("ReplaceAllImages") {
						t.Errorf("Jellyfin 刷新请求不正确：%s %s", r.Method, r.URL.RawQuery)
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := NewClient(server.URL+base+"/", "test-key")
			libraries, err := client.GetAllMediaLibraries()
			if err != nil || len(libraries) != 1 || libraries[0].ID != jellyfinTestLibrary {
				t.Fatalf("libraries=%v error=%v", libraries, err)
			}
			users, err := client.GetUsersWithAllLibrariesAccess()
			if err != nil || len(users) != 1 {
				t.Fatalf("users=%v error=%v", users, err)
			}
			var items []BaseItemDtoV2
			err = client.FetchMediaItemsByLibraryID(t.Context(), EmbyItemsQuery{LibraryID: jellyfinTestLibrary, Limit: 1, SortBy: "DateLastSaved", MinDateLastSaved: "2026-01-01T00:00:00Z", LastDateCreatedAt: 1767225600}, func(item BaseItemDtoV2) error { items = append(items, item); return nil })
			if err != nil || len(items) != 2 {
				t.Fatalf("items=%v error=%v", items, err)
			}
			for _, item := range items {
				if item.Id != item.MediaSources[0].ItemID || item.ParentId != jellyfinTestLibrary || item.Path != "/media/test.strm" || item.MediaSources[0].Path != "https://media.test/stream" {
					t.Fatalf("静态来源或路径规范化错误：%+v", item)
				}
			}
			parts, err := client.GetAdditionalParts(t.Context(), jellyfinTestItem)
			if err != nil || len(parts) != 1 || parts[0].MediaSources[0].ItemID != jellyfinTestVersion {
				t.Fatalf("parts=%v error=%v", parts, err)
			}
			folders, err := client.GetItemLibraryIDContext(t.Context(), jellyfinTestItem)
			if err != nil || len(folders) != 1 || folders[0].ItemId != jellyfinTestLibrary {
				t.Fatalf("folders=%v error=%v", folders, err)
			}
			ancestors, err := client.GetItemAncestors(jellyfinTestItem)
			if err != nil || len(ancestors) != 1 || ancestors[0].ID != jellyfinTestLibrary {
				t.Fatalf("ancestors=%v error=%v", ancestors, err)
			}
			before := itemRequests
			item, err := client.FindItemByPath("/media/test.strm")
			if err != nil || item != nil || before != itemRequests {
				t.Fatalf("Jellyfin 不应发送 Path 查询：item=%v error=%v", item, err)
			}
			item, err = client.FindItemByID(jellyfinTestItem)
			if err != nil || item == nil || item.Id != jellyfinTestItem {
				t.Fatalf("item=%v error=%v", item, err)
			}
			if err := client.RefreshItem(jellyfinTestItem, "电影", false); err != nil {
				t.Fatal(err)
			}
			if err := client.RefreshLibrary(jellyfinTestLibrary, "电影库"); err != nil {
				t.Fatal(err)
			}
			id, err := client.GetServerID(t.Context())
			if err != nil || id != "server-1" || probes != 1 || refreshes != 2 {
				t.Fatalf("id=%q probes=%d refreshes=%d error=%v", id, probes, refreshes, err)
			}
		})
	}
}

func TestServerDetectionFailureAndCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		body      string
		fallback  bool
		wantError bool
	}{
		{name: "legacy prefix", status: http.StatusNotFound, fallback: true},
		{name: "unknown product", status: http.StatusOK, body: `{"Id":"server-1","ProductName":"Other"}`},
		{name: "unauthorized", status: http.StatusUnauthorized, wantError: true},
		{name: "forbidden", status: http.StatusForbidden, wantError: true},
		{name: "unavailable", status: http.StatusServiceUnavailable, wantError: true},
		{name: "invalid JSON", status: http.StatusOK, body: `{"Id":`, wantError: true},
		{name: "trailing JSON", status: http.StatusOK, body: `{"Id":"server-1"} {}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var native, fallback, items int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Info/Public":
					native++
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				case "/emby/System/Info/Public":
					fallback++
					fmt.Fprint(w, `{"Id":"server-1","ProductName":"Emby"}`)
				case "/System/Ping":
					fmt.Fprint(w, "Other")
				case "/emby/Items":
					items++
					if r.URL.Query().Get("api_key") != "test-key" {
						t.Error("Emby 认证发生变化")
					}
					fmt.Fprint(w, `{"TotalRecordCount":0,"Items":[]}`)
				default:
					t.Errorf("意外请求：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := NewClient(server.URL, "test-key")
			_, err := client.GetServerID(t.Context())
			if err == nil {
				err = client.FetchMediaItemsByLibraryID(t.Context(), EmbyItemsQuery{}, func(BaseItemDtoV2) error { return nil })
			}
			if (err != nil) != tc.wantError || native != 1 || (fallback == 1) != tc.fallback || (items == 0) != tc.wantError {
				t.Fatalf("error=%v native=%d fallback=%d items=%d", err, native, fallback, items)
			}
		})
	}
}

func TestJellyfinStaticSourceAndIdentityBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, itemID, sourceID, sourceType, channelID, mediaType string
		wantError                                                bool
	}{
		{name: "default", mediaType: "Default"},
		{name: "grouping", mediaType: "Grouping"},
		{name: "hyphenated ID", itemID: "12345678-1234-1234-1234-1234567890AB", mediaType: "Default"},
		{name: "zero channel is unset", channelID: "00000000-0000-0000-0000-000000000000", mediaType: "Default"},
		{name: "zero item", itemID: "00000000000000000000000000000000", mediaType: "Default", wantError: true},
		{name: "invalid item", itemID: "not-a-guid", mediaType: "Default", wantError: true},
		{name: "invalid source", sourceID: "not-a-guid", mediaType: "Default", wantError: true},
		{name: "channel source", sourceType: "Channel", mediaType: "Default", wantError: true},
		{name: "channel identity", channelID: jellyfinTestLibrary, mediaType: "Default", wantError: true},
		{name: "placeholder", mediaType: "Placeholder", wantError: true},
		{name: "unclassified source", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			itemID, sourceID := tc.itemID, tc.sourceID
			if itemID == "" {
				itemID = jellyfinTestItem
			}
			if sourceID == "" {
				sourceID = jellyfinTestItem
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/System/Info/Public" {
					fmt.Fprint(w, `{"Id":"server-1","ProductName":"Jellyfin"}`)
					return
				}
				json.NewEncoder(w).Encode(QueryResultBaseItemDto{TotalRecordCount: 1, Items: []BaseItemDtoV2{{Id: itemID, Type: "Movie", SourceType: tc.sourceType, ChannelID: tc.channelID, MediaSources: []MediaSource{{ID: sourceID, Type: tc.mediaType, Path: "https://media.test/stream"}}}}})
			}))
			defer server.Close()
			var calls int
			client := NewClient(server.URL, "test-key")
			if _, err := client.GetServerID(t.Context()); err != nil {
				t.Fatal(err)
			}
			err := client.FetchMediaItemsByLibraryID(t.Context(), EmbyItemsQuery{}, func(item BaseItemDtoV2) error {
				calls++
				if item.Id != jellyfinTestItem || item.MediaSources[0].ItemID != jellyfinTestItem {
					t.Errorf("身份未规范化：%+v", item)
				}
				return nil
			})
			if (err != nil) != tc.wantError || (calls == 0) != tc.wantError {
				t.Fatalf("error=%v callbacks=%d", err, calls)
			}
		})
	}
}

func TestJellyfinDuplicateIDsNormalizeBeforePagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Info/Public" {
			fmt.Fprint(w, `{"Id":"server-1","ProductName":"Jellyfin"}`)
			return
		}
		id := jellyfinTestItem
		if r.URL.Query().Get("StartIndex") == "0" {
			id = "12345678-1234-1234-1234-1234567890AB"
		}
		fmt.Fprintf(w, `{"TotalRecordCount":2,"Items":[{"Id":%q,"Type":"Movie"}]}`, id)
	}))
	defer server.Close()
	var calls int
	client := NewClient(server.URL, "test-key")
	if _, err := client.GetServerID(t.Context()); err != nil {
		t.Fatal(err)
	}
	err := client.FetchMediaItemsByLibraryID(t.Context(), EmbyItemsQuery{Limit: 1}, func(BaseItemDtoV2) error { calls++; return nil })
	if err == nil || calls != 1 {
		t.Fatalf("error=%v callbacks=%d", err, calls)
	}
}

func TestServerInfoConcurrentReuseAndRetry(t *testing.T) {
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probes.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"Id":"server-1","ProductName":"Jellyfin"}`)
	}))
	defer server.Close()
	client := NewClient(server.URL, "test-key")
	if _, err := client.GetServerID(t.Context()); err == nil {
		t.Fatal("失败的探测不应缓存为成功")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			id, err := client.GetServerID(t.Context())
			if err != nil || id != "server-1" {
				t.Errorf("id=%q error=%v", id, err)
			}
		})
	}
	wg.Wait()
	if probes.Load() != 2 {
		t.Fatalf("probes=%d, want 2", probes.Load())
	}
}

func TestMediaPageRequiresCompleteEmptyResponse(t *testing.T) {
	for _, body := range []string{`{}`, `{"Items":[]}`, `{"TotalRecordCount":0}`, `{"Items":null,"TotalRecordCount":0}`, `{"Items":[],"TotalRecordCount":-1}`, `{"Items":[],"TotalRecordCount":0}`} {
		t.Run(body, func(t *testing.T) {
			server := newEmbyAPITestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			err := NewClient(server.URL, "test-key").FetchMediaItemsByLibraryID(t.Context(), EmbyItemsQuery{}, func(BaseItemDtoV2) error {
				t.Error("空页不应执行媒体回调")
				return nil
			})
			if (err == nil) != (body == `{"Items":[],"TotalRecordCount":0}`) {
				t.Fatalf("不完整空页应拒绝，明确空页应成功：%v", err)
			}
		})
	}
}

func TestServerInfoWaitingCallerCanCancel(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		fmt.Fprint(w, `{"Id":"server-1","ProductName":"Jellyfin Server"}`)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	client := NewClient(server.URL, "test-key")
	first := make(chan error, 1)
	go func() { _, err := client.GetServerID(t.Context()); first <- err }()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, err := client.GetServerID(ctx); second <- err }()
	select {
	case err := <-second:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("取消等待身份探测失败：%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消的请求不应等待另一个调用的网络响应")
	}
}

func TestJellyfinHiddenVersionDetail(t *testing.T) {
	for _, failure := range []string{"", "malformed_listing", "no_library_user", "other_item"} {
		t.Run(failure, func(t *testing.T) {
			var users, details int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Ping":
					fmt.Fprint(w, `"Jellyfin Server"`)
				case "/System/Info/Public":
					fmt.Fprint(w, `{"Id":"server-1","ProductName":"Jellyfin Server"}`)
				case "/Items":
					if failure == "malformed_listing" {
						fmt.Fprint(w, `{}`)
						return
					}
					fmt.Fprint(w, `{"Items":[],"TotalRecordCount":0}`)
				case "/Users":
					users++
					if failure == "no_library_user" {
						fmt.Fprint(w, `[]`)
						return
					}
					fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true}}]`, jellyfinTestLibrary)
				case "/Items/" + jellyfinTestVersion:
					details++
					if r.URL.Query().Get("UserId") != jellyfinTestLibrary || r.URL.Query().Has("api_key") {
						t.Errorf("隐藏版本未按库用户原生查询：%s", r.URL.RawQuery)
					}
					id := jellyfinTestVersion
					if failure == "other_item" {
						id = jellyfinTestItem
					}
					fmt.Fprintf(w, `{"Id":%q,"Type":"Movie","Path":"/hidden.strm","MediaSources":[{"Id":%q,"Type":"Default","Path":"https://media.test/stream"}]}`, id, id)
				default:
					t.Errorf("非预期请求：%s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			items, err := NewClient(server.URL, "test-key").GetSnapshotItems(t.Context(), jellyfinTestVersion)
			if (err != nil) != (failure != "") {
				t.Fatalf("items=%v error=%v", items, err)
			}
			if failure == "malformed_listing" && (users != 0 || details != 0) {
				t.Fatal("坏集合响应不应改走其他查询")
			}
			if failure == "" && (len(items) != 1 || items[0].Id != jellyfinTestVersion || items[0].MediaSources[0].ItemID != jellyfinTestVersion || users != 1 || details != 1) {
				t.Fatalf("隐藏版本补查未保留真实关联：items=%+v users=%d details=%d", items, users, details)
			}
		})
	}
}

func TestRefreshLookupRejectsIgnoredFilters(t *testing.T) {
	for _, query := range []string{"path", "id"} {
		for _, body := range []string{
			`{"Items":[{"Id":"other","Path":"/other.strm"}],"TotalRecordCount":1}`,
			`{"Items":[{}],"TotalRecordCount":1}`,
		} {
			t.Run(query+"/"+body, func(t *testing.T) {
				server := newEmbyAPITestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					fmt.Fprint(w, body)
				}))
				defer server.Close()
				client := NewClient(server.URL, "test-key")
				var item *BaseItemDtoV2
				var err error
				if query == "path" {
					item, err = client.FindItemByPath("/wanted.strm")
				} else {
					item, err = client.FindItemByID("wanted")
				}
				if err != nil || item != nil {
					t.Fatalf("不能把忽略过滤条件的其他条目作为刷新目标：%+v %v", item, err)
				}
			})
		}
	}
}

func TestJellyfinFiltersPlaylistCollectionByType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/System/Ping":
			fmt.Fprint(w, `"Jellyfin Server"`)
		case "/Library/MediaFolders":
			fmt.Fprintf(w, `{"Items":[{"Id":%q,"Name":"Playlists","Type":"CollectionFolder","CollectionType":"movies"},{"Id":%q,"Name":"播放列表","Type":"ManualPlaylistsFolder"},{"Id":"ignored","Name":"Lists","CollectionType":"playlists"}]}`, jellyfinTestLibrary, jellyfinTestVersion)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	libs, err := NewClient(server.URL, "key").GetAllMediaLibraries()
	if err != nil || len(libs) != 1 || libs[0].ID != jellyfinTestLibrary || libs[0].Name != "Playlists" {
		t.Fatalf("libraries=%+v err=%v", libs, err)
	}
}

func TestJellyfinSTRMDetailFallbackIsConditionalAndChecksIdentity(t *testing.T) {
	for _, mode := range []string{"native", "detail", "still-local", "changed-path", "failed"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			const local = "/media/movie.strm"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/System/Ping":
					fmt.Fprint(w, `"Jellyfin Server"`)
				case "/Users":
					fmt.Fprintf(w, `[{"Id":%q,"Policy":{"EnableAllFolders":true}}]`, jellyfinTestVersion)
				case "/Items/" + jellyfinTestItem:
					requests++
					if mode == "failed" {
						http.Error(w, "unavailable", 503)
						return
					}
					path, source := local, "https://qms/115/newurl?userid=owner&pickcode=code"
					if mode == "still-local" {
						source = local
					}
					if mode == "changed-path" {
						path = "/media/replaced.strm"
					}
					fmt.Fprintf(w, `{"Id":%q,"Type":"Movie","Path":%q,"MediaSources":[{"Id":%q,"Type":"Default","Path":%q}]}`, jellyfinTestItem, path, jellyfinTestItem, source)
				default:
					t.Errorf("unexpected %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := NewClient(server.URL, "key")
			if _, err := client.CheckConnection(t.Context()); err != nil {
				t.Fatal(err)
			}
			item := BaseItemDtoV2{Id: jellyfinTestItem, Type: "Movie", Path: local, MediaSources: []MediaSource{{ID: jellyfinTestItem, ItemID: jellyfinTestItem, Type: "Default", Path: local}}}
			if mode == "native" {
				item.MediaSources[0].Path = "https://native/stream"
			}
			err := client.CompleteSTRMSources(t.Context(), &item)
			if mode == "changed-path" || mode == "failed" {
				if err == nil {
					t.Fatal("bad detail accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "native" && requests != 0 {
				t.Fatal("normal source fetched detail")
			}
			if mode == "detail" && !strings.HasPrefix(item.MediaSources[0].Path, "https://qms/") {
				t.Fatal("detail source not recovered")
			}
			if mode == "still-local" && item.MediaSources[0].Path != local {
				t.Fatal("local source invented")
			}
		})
	}
}
