package web

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/service/m3u8"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/helpers"

	"github.com/gin-gonic/gin"
)

func TestGuardPrecedesProxyMiddlewares(t *testing.T) {
	previous := config.C
	config.C = &config.Config{Cache: &config.Cache{}}
	t.Cleanup(func() { config.C = previous })
	client := https.NewClient(https.ClientOptions{})
	responses := cache.NewStore(time.Hour)
	playlists := m3u8.NewStore(client)
	session := emby.NewSession(emby.SessionOptions{Origin: "http://invalid.test", Client: client, Cache: responses.NewScope()})
	t.Cleanup(func() {
		session.Close()
		_ = responses.Stop(context.Background())
		_ = playlists.Stop(context.Background())
		client.CloseIdleConnections()
	})
	router := NewRouter(session, func(c *gin.Context) { c.AbortWithStatus(http.StatusServiceUnavailable) }, playlists)
	// PlaybackInfo 原本会先校验上游凭据；门禁拒绝后不得访问未配置的上游。
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/Items/1/PlaybackInfo", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("门禁响应=%d", response.Code)
	}
}

func TestNewRouterDownloadRouting(t *testing.T) {
	var allowed atomic.Bool
	allowed.Store(true)
	var downloads, syncFiles atomic.Int32
	forwarded := make(chan string, 5)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path.Clean(r.URL.Path) {
		case "/emby/Auth/Keys":
			_, _ = io.WriteString(w, `{}`)
		case "/Items":
			if r.URL.Query().Get("Ids") != "123" {
				t.Errorf("查询串改变了授权媒体 ID：%s", r.URL)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "123", "CanDownload": allowed.Load()}}})
		case "/Sync/Targets":
			_, _ = io.WriteString(w, `[{"Id":"device"}]`)
		case "/Sync/Items/Ready":
			_, _ = io.WriteString(w, `[{"SyncJobItemId":9,"Item":{"Id":"123","MediaSources":[{"Id":"ms"}]}}]`)
		case "/Items/123/PlaybackInfo":
			_, _ = io.WriteString(w, `{"MediaSources":[{"Id":"ms","Path":"/media/movie.mkv"}]}`)
		case "/Items/123/Download":
			downloads.Add(1)
			forwarded <- r.RequestURI
			body := "abc"
			if r.Header.Get("Range") == "bytes=3-5" {
				body = "def"
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "3")
			w.Header().Set("Content-Disposition", `attachment; filename="movie.mkv"`)
			w.WriteHeader(http.StatusPartialContent)
			if r.Method != http.MethodHead {
				_, _ = io.WriteString(w, body)
			}
		case "/Sync/JobItems/9/File":
			syncFiles.Add(1)
			_, _ = io.WriteString(w, "native sync file")
		default:
			http.NotFound(w, r)
		}
	}))
	defer origin.Close()
	oldConfig, oldLogger := config.C, helpers.AppLogger
	config.C = &config.Config{Emby: &config.Emby{Host: origin.URL}, Cache: &config.Cache{Enable: true, Expired: "1h"}}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { config.C, helpers.AppLogger = oldConfig, oldLogger })
	if err := config.C.Emby.Init(); err != nil {
		t.Fatal(err)
	}
	if err := config.C.Cache.Init(); err != nil {
		t.Fatal(err)
	}
	client := https.NewClient(https.ClientOptions{})
	responses := cache.NewStore(time.Hour)
	playlists := m3u8.NewStore(client)
	session := emby.NewSession(emby.SessionOptions{Origin: origin.URL, Client: client, Cache: responses.NewScope()})
	t.Cleanup(func() {
		session.Close()
		_ = responses.Stop(context.Background())
		_ = playlists.Stop(context.Background())
		client.CloseIdleConnections()
	})
	router := NewRouter(session, nil, playlists)
	for _, request := range []struct{ method, path, rangeHeader, body string }{
		{http.MethodHead, "/Items/123/Download?api_key=test", "bytes=0-2", ""},
		{http.MethodGet, "/Items/123/Download?api_key=test&foo=subtitles", "bytes=0-2", "abc"},
		{http.MethodGet, "/Items/123/Download?api_key=test&foo=subtitles", "bytes=3-5", "def"},
		{http.MethodGet, "/Items/%31%32%33/Download?api_key=test&foo=/Videos/999/stream", "bytes=0-2", "abc"},
		{http.MethodGet, "/Items//123/Download/?api_key=test", "bytes=0-2", "abc"},
	} {
		req := httptest.NewRequest(request.method, request.path, nil)
		req.Header.Set("Range", request.rangeHeader)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent || rec.Body.String() != request.body ||
			rec.Result().Header.Get("Content-Disposition") != `attachment; filename="movie.mkv"` {
			t.Fatalf("实际下载路由未回源：%s %s => %d %q", request.method, request.path, rec.Code, rec.Body.String())
		}
		select {
		case uri := <-forwarded:
			if uri != request.path {
				t.Fatalf("回源改写了原始 URI：%q，期望 %q", uri, request.path)
			}
		default:
			t.Fatal("下载响应没有实际回源")
		}
	}
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/Sync/JobItems/9/File?api_key=test&foo=/Videos/999/stream"},
		{http.MethodGet, "/Sync/JobItems/%39/File?api_key=test"},
		{http.MethodGet, "/Sync//JobItems/9/File/?api_key=test"},
		{http.MethodHead, "/Sync/JobItems/9/File?api_key=test"},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(request.method, request.path, nil))
		if rec.Code != http.StatusTemporaryRedirect || rec.Result().Header.Get("Location") != "/Items/123/Download?MediaSourceId=ms&api_key=test" ||
			request.method == http.MethodHead && rec.Body.Len() != 0 {
			t.Fatalf("同步下载未映射到普通下载：%s %s => %d %q", request.method, request.path, rec.Code, rec.Header().Get("Location"))
		}
	}
	allowed.Store(false)
	for _, path := range []string{"/Items/123/Download", "/Sync/JobItems/%39/File?foo=/Videos/999/stream", "/Sync//JobItems/9/File/"} {
		req := httptest.NewRequest(http.MethodHead, path, nil)
		req.Header.Set("X-Emby-Token", "test")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || rec.Body.Len() != 0 {
			t.Fatalf("实际路由绕过下载权限：%s => %d %q", path, rec.Code, rec.Body.String())
		}
	}
	if downloads.Load() != 5 || syncFiles.Load() != 0 {
		t.Fatalf("下载被缓存或错误回源：ordinary=%d sync=%d", downloads.Load(), syncFiles.Load())
	}
}
