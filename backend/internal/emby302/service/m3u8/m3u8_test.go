package m3u8

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/emby302/config"
)

func newPlaylistStore(t *testing.T) *Store {
	t.Helper()
	store := NewStore(http.DefaultClient)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := store.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return store
}

func testPlaylist(key playlistKey) *Info {
	return &Info{OpenlistPath: key.path, TemplateId: key.template, LastUpdate: time.Now().UnixMilli(), RemoteBase: "https://cdn.example/", RemoteTsInfos: []*TsInfo{{Url: "segment.ts"}}}
}

func TestConcurrentRefreshIsScopedToResource(t *testing.T) {
	store := newPlaylistStore(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var slowCalls atomic.Int32
	store.loadInfo = func(ctx context.Context, key playlistKey) (*Info, error) {
		if key.path == "/slow" {
			if slowCalls.Add(1) == 1 {
				close(entered)
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return testPlaylist(key), nil
	}
	canceled, cancel := context.WithCancel(t.Context())
	firstDone := make(chan bool, 1)
	go func() { _, ok := store.GetTsLink(canceled, "/slow", "1", 0); firstDone <- ok }()
	<-entered
	secondDone := make(chan string, 1)
	go func() { value, _ := store.GetTsLink(t.Context(), "/slow", "1", 0); secondDone <- value }()
	cancel()
	if <-firstDone {
		t.Fatal("canceled waiter returned a playlist")
	}
	if link, ok := store.GetTsLink(t.Context(), "/fast", "1", 0); !ok || link != "https://cdn.example/segment.ts" {
		t.Fatal("another resource waited for slow refresh")
	}
	close(release)
	if value := <-secondDone; value != "https://cdn.example/segment.ts" || slowCalls.Load() != 1 {
		t.Fatalf("shared resource refresh changed: value=%q calls=%d", value, slowCalls.Load())
	}
}

func TestStopCancelsRefreshAndRejectsLatePublication(t *testing.T) {
	store := newPlaylistStore(t)
	entered := make(chan struct{})
	store.loadInfo = func(ctx context.Context, key playlistKey) (*Info, error) {
		close(entered)
		<-ctx.Done()
		return testPlaylist(key), nil
	}
	store.PushPlaylistAsync(Info{OpenlistPath: "/slow", TemplateId: "1"})
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := store.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	canceled, cancelAgain := context.WithCancel(t.Context())
	cancelAgain()
	if err := store.Stop(canceled); err != nil {
		t.Fatal("already stopped store reported failure", err)
	}
	if _, ok := store.GetTsLink(t.Context(), "/slow", "1", 0); ok {
		t.Fatal("late refresh revived stopped store")
	}
	store.PushPlaylistAsync(Info{OpenlistPath: "/new", TemplateId: "1"})
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.entries) != 0 || len(store.loading) != 0 {
		t.Fatal("stopped store accepted more work")
	}
}

func TestPlaylistKeysAndTokensRemainIndependent(t *testing.T) {
	store := newPlaylistStore(t)
	store.loadInfo = func(ctx context.Context, key playlistKey) (*Info, error) { return testPlaylist(key), nil }
	first, ok := store.GetPlaylist(t.Context(), "/ab", "c", true, true, "https://proxy.example/videos", "token A")
	if !ok {
		t.Fatal("first playlist unavailable")
	}
	second, ok := store.GetPlaylist(t.Context(), "/a", "bc", true, true, "https://proxy.example/videos", "token B")
	if !ok {
		t.Fatal("second playlist unavailable")
	}
	if !strings.Contains(first, "api_key=token+A") || !strings.Contains(second, "api_key=token+B") || strings.Contains(second, "token+A") {
		t.Fatal("playlist reused previous authorization")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.entries) != 2 {
		t.Fatal("ambiguous resource keys collided")
	}
}

func TestStopCancelsActualResourceRequests(t *testing.T) {
	for _, blockedPath := range []string{"/api/fs/other", "/playlist.m3u8"} {
		t.Run(blockedPath, func(t *testing.T) {
			entered, canceled, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var origin *httptest.Server
			origin = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == blockedPath {
					// 读完 POST body 后，HTTP/1 服务端才能观察客户端断开。
					_, _ = io.Copy(io.Discard, r.Body)
					close(entered)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-abort:
					}
					return
				}
				if r.URL.Path == "/api/fs/other" {
					fmt.Fprintf(w, `{"code":200,"data":{"video_preview_play_info":{"live_transcoding_task_list":[{"template_id":"1","url":%q}]}}}`, origin.URL+"/playlist.m3u8")
					return
				}
				t.Errorf("unexpected request: %s", r.URL.Path)
			}))
			defer func() { close(abort); origin.Close() }()
			previous := config.C
			config.C = &config.Config{Openlist: &config.Openlist{Host: origin.URL, Token: "test-token"}}
			defer func() { config.C = previous }()
			store := NewStore(origin.Client())
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := store.Stop(ctx); err != nil {
					t.Error(err)
				}
			}()
			store.PushPlaylistAsync(Info{OpenlistPath: "/movie", TemplateId: "1"})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("resource request did not start")
			}
			if err := store.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-canceled:
			case <-ctx.Done():
				t.Fatal("remote request was not canceled")
			}
		})
	}
}
