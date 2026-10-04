package embyclientrestgo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestFetchMediaItemsByLibraryID分页流式处理(t *testing.T) {
	requestedStartIndexes := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startIndex, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		requestedStartIndexes = append(requestedStartIndexes, startIndex)

		switch startIndex {
		case 0:
			fmt.Fprint(w, `{"TotalRecordCount":5,"Items":[{"Id":"1"},{"Id":"2"}]}`)
		case 2:
			fmt.Fprint(w, `{"TotalRecordCount":5,"Items":[{"Id":"3"},{"Id":"4"}]}`)
		case 4:
			fmt.Fprint(w, `{"TotalRecordCount":5,"Items":[{"Id":"5"}]}`)
		default:
			t.Fatalf("unexpected StartIndex %d", startIndex)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	ids := []string{}
	err := client.FetchMediaItemsByLibraryID(
		context.Background(),
		EmbyItemsQuery{LibraryID: "lib-1", Limit: 2},
		func(item BaseItemDtoV2) error {
			ids = append(ids, item.Id)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("FetchMediaItemsByLibraryID() error = %v", err)
	}
	if got := fmt.Sprint(ids); got != "[1 2 3 4 5]" {
		t.Fatalf("handled ids = %s, want [1 2 3 4 5]", got)
	}
	if got := fmt.Sprint(requestedStartIndexes); got != "[0 2 4]" {
		t.Fatalf("requested StartIndex = %s, want [0 2 4]", got)
	}
}

func TestFetchMediaItemsByLibraryID提前空页返回不完整错误(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		startIndex, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
		if startIndex == 0 {
			fmt.Fprint(w, `{"TotalRecordCount":10,"Items":[{"Id":"1"}]}`)
			return
		}
		fmt.Fprint(w, `{"TotalRecordCount":10,"Items":[]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	handled := 0
	err := client.FetchMediaItemsByLibraryID(
		context.Background(),
		EmbyItemsQuery{LibraryID: "lib-1", Limit: 1},
		func(item BaseItemDtoV2) error {
			handled++
			return nil
		},
	)
	if err == nil {
		t.Fatal("提前空页必须返回错误")
	}
	if handled != 1 || requests != 2 {
		t.Fatalf("handled=%d requests=%d, want handled=1 requests=2", handled, requests)
	}
}

func TestFetchMediaItemsByLibraryIDHandle错误停止分页(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			t.Fatal("handle 返回错误后不应继续请求下一页")
		}
		fmt.Fprint(w, `{"TotalRecordCount":3,"Items":[{"Id":"1"},{"Id":"2"}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	wantErr := errors.New("stop")
	err := client.FetchMediaItemsByLibraryID(
		context.Background(),
		EmbyItemsQuery{LibraryID: "lib-1", Limit: 2},
		func(item BaseItemDtoV2) error {
			if item.Id == "2" {
				return wantErr
			}
			return nil
		},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("FetchMediaItemsByLibraryID() error = %v, want %v", err, wantErr)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
}

func TestFetchMediaItemsByLibraryIDLastDateCreatedAt正常停止(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests > 1 {
			t.Fatal("达到 LastDateCreatedAt 截止点后不应继续请求下一页")
		}
		fmt.Fprint(w, `{"TotalRecordCount":3,"Items":[{"Id":"1","DateCreated":"2026-06-29T01:00:00Z"},{"Id":"2","DateCreated":"2026-06-29T00:00:00Z"}]}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	ids := []string{}
	err := client.FetchMediaItemsByLibraryID(
		context.Background(),
		EmbyItemsQuery{LibraryID: "lib-1", Limit: 2, LastDateCreatedAt: 1782691200},
		func(item BaseItemDtoV2) error {
			ids = append(ids, item.Id)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("FetchMediaItemsByLibraryID() error = %v, want nil", err)
	}
	if got := fmt.Sprint(ids); got != "[1]" {
		t.Fatalf("handled ids = %s, want [1]", got)
	}
}

func TestFetchMediaItemsByLibraryIDContext取消(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("context 已取消时不应发送请求")
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := client.FetchMediaItemsByLibraryID(
		ctx,
		EmbyItemsQuery{LibraryID: "lib-1"},
		func(item BaseItemDtoV2) error { return nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("FetchMediaItemsByLibraryID() error = %v, want context.Canceled", err)
	}
}

func TestFetchMediaItemsByLibraryIDHTTP错误(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fail", http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	err := client.FetchMediaItemsByLibraryID(
		context.Background(),
		EmbyItemsQuery{LibraryID: "lib-1"},
		func(item BaseItemDtoV2) error { return nil },
	)
	if err == nil {
		t.Fatal("FetchMediaItemsByLibraryID() error = nil, want error")
	}
}

func TestGetItemLibraryIdAncestors为空时返回错误(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items/episode-1/Ancestors":
			fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	libs, err := client.GetItemLibraryId("episode-1")
	if err == nil {
		t.Fatal("GetItemLibraryId() error = nil, want error")
	}
	if len(libs) != 0 {
		t.Fatalf("libs = %+v, want empty", libs)
	}
}

func TestGetItemLibraryId单个Ancestor命中媒体库(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/emby/Items/movie-1/Ancestors":
			fmt.Fprint(w, `[{"Id":"movie-library-folder","Path":"/media/movie"}]`)
		case "/emby/Library/VirtualFolders":
			fmt.Fprint(w, `[{"Id":"lib-movie","Name":"电影","Locations":["/media/movie"]}]`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	libs, err := client.GetItemLibraryId("movie-1")
	if err != nil {
		t.Fatalf("GetItemLibraryId() error = %v", err)
	}
	if len(libs) != 1 || libs[0].ID != "lib-movie" || libs[0].Name != "电影" {
		t.Fatalf("libs = %+v, want lib-movie", libs)
	}
}

func TestRefreshItemUsesItemRefreshEndpoint(t *testing.T) {
	var gotRecursive string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/emby/Items/item-1/Refresh" {
			t.Fatalf("请求 = %s %s，期望 POST /emby/Items/item-1/Refresh", r.Method, r.URL.Path)
		}
		if r.URL.Query().Get("api_key") != "test-key" {
			t.Fatalf("api_key = %s，期望 test-key", r.URL.Query().Get("api_key"))
		}
		gotRecursive = r.URL.Query().Get("Recursive")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	if err := client.RefreshItem("item-1", "电影", false); err != nil {
		t.Fatalf("RefreshItem() error = %v", err)
	}
	if gotRecursive != "false" {
		t.Fatalf("Recursive = %s，期望 false", gotRecursive)
	}
}

func TestFindItemByPathUsesPathQuery(t *testing.T) {
	const localPath = "/strm/movie/movie.strm"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/emby/Items" {
			t.Fatalf("请求 = %s %s，期望 GET /emby/Items", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("Path") != localPath {
			t.Fatalf("Path = %s，期望 %s", query.Get("Path"), localPath)
		}
		if query.Get("Recursive") != "true" || query.Get("IncludeItemTypes") != "Movie,Video,Episode,Folder,Series" {
			t.Fatalf("查询参数 = %s，期望 Recursive=true 且包含指定类型", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"Items":[{"Id":"item-path","Name":"路径电影","Type":"Movie","Path":"`+localPath+`"}],"TotalRecordCount":1}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	item, err := client.FindItemByPath(localPath)
	if err != nil {
		t.Fatalf("FindItemByPath() error = %v", err)
	}
	if item == nil || item.Id != "item-path" || item.Type != "Movie" {
		t.Fatalf("item = %+v，期望路径命中的 Movie", item)
	}
}

func TestFindItemByIDUsesIdsQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/emby/Items" {
			t.Fatalf("请求 = %s %s，期望 GET /emby/Items", r.Method, r.URL.Path)
		}
		query := r.URL.Query()
		if query.Get("Ids") != "item-1" {
			t.Fatalf("Ids = %s，期望 item-1", query.Get("Ids"))
		}
		if query.Get("Recursive") != "true" || query.Get("IncludeItemTypes") != "Movie,Video,Episode,Folder,Series" {
			t.Fatalf("查询参数 = %s，期望 Recursive=true 且包含指定类型", r.URL.RawQuery)
		}
		fmt.Fprint(w, `{"Items":[{"Id":"item-1","Name":"电影","Type":"Movie"}],"TotalRecordCount":1}`)
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	item, err := client.FindItemByID("item-1")
	if err != nil {
		t.Fatalf("FindItemByID() error = %v", err)
	}
	if item == nil || item.Id != "item-1" || item.Type != "Movie" {
		t.Fatalf("item = %+v，期望命中的 Movie", item)
	}
}
