package fnosproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
)

func TestServiceStopCancelsInflightAuthorization(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(file, []byte("http://private-qms:12333/115/newurl?pickcode=aaa&userid=owner"), 0600); err != nil {
		t.Fatal(err)
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/movie":
			close(started)
			<-r.Context().Done()
			close(canceled)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
		default:
			http.Error(w, "unexpected fallback", http.StatusBadGateway)
		}
	}))
	t.Cleanup(upstream.Close)
	var handoffs atomic.Int64
	service, config, base := startService(t, upstream.URL, Options{
		OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) {
			return []string{"http://private-qms:12333"}, nil
		},
		MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handoffs.Add(1) }),
	})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, err := noRedirectClient().Get(base + "/Videos/movie/stream?MediaSourceId=source&api_key=valid")
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("播放请求没有进入来源鉴权")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := service.Stop(ctx); err != nil {
		t.Fatalf("停止未等待在途鉴权退出：%v", err)
	}
	for _, signal := range []<-chan struct{}{canceled, finished} {
		select {
		case <-signal:
		case <-time.After(3 * time.Second):
			t.Fatal("停止后在途请求未被取消")
		}
	}
	if handoffs.Load() != 0 || service.Status(config).Running {
		t.Fatal("停止后仍执行内部转交或报告运行中")
	}
	service.current.cache.mu.Lock()
	defer service.current.cache.mu.Unlock()
	if len(service.current.cache.entries) != 0 {
		t.Fatal("停止后的在途鉴权回填了来源缓存")
	}
}

func TestSourceCacheCannotRetainRemovedOutputRoot(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	file := filepath.Join(firstRoot, "movie.strm")
	if err := os.WriteFile(file, []byte("http://private-qms:12333/115/newurl?pickcode=aaa&userid=owner"), 0600); err != nil {
		t.Fatal(err)
	}
	allowedRoot := firstRoot
	service := NewService(Options{
		OutputRoots: func(context.Context) ([]string, error) { return []string{allowedRoot}, nil },
		KnownOrigins: func(context.Context) ([]string, error) {
			return []string{"http://private-qms:12333"}, nil
		},
	})
	inst := &instance{service: service, ctx: t.Context(), cache: newSourceCache()}
	request := httptest.NewRequest(http.MethodGet, "/Videos/movie/stream?api_key=valid", nil)
	source := Source{ItemID: "movie", ID: "source", Path: file}
	if _, ok := inst.resolveSource(t.Context(), request, source); !ok || len(inst.cache.entries) != 1 {
		t.Fatal("允许根内的 STRM 未建立来源缓存")
	}
	allowedRoot = secondRoot
	if _, ok := inst.resolveSource(t.Context(), request, source); ok || len(inst.cache.entries) != 0 {
		t.Fatal("允许根移除后仍使用旧文件或保留旧来源缓存")
	}
}

func TestServiceMaintenanceRejectsWarmCacheAndProxy(t *testing.T) {
	pool, err := db.OpenMaintenanceSQL(sqlite.DriverName, filepath.Join(t.TempDir(), "maintenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(sqlite.Dialector{Conn: pool}, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = pool.Close()
		t.Fatal(err)
	}
	previousDB := db.Db
	db.Db = database
	t.Cleanup(func() { db.Db = previousDB; _ = pool.Close() })
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(file, []byte("http://private-qms:12333/115/newurl?pickcode=aaa&userid=owner"), 0600); err != nil {
		t.Fatal(err)
	}
	var queries, handoffs atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries.Add(1)
		if r.URL.Path == "/Users/Me" {
			_, _ = io.WriteString(w, `{"Id":"user"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
	}))
	t.Cleanup(upstream.Close)
	service, config, base := startService(t, upstream.URL, Options{
		OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) {
			return []string{"http://private-qms:12333"}, nil
		},
		MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handoffs.Add(1)
			w.Header().Set("Location", "https://cdn.example/video")
			w.WriteHeader(http.StatusFound)
		}),
	})
	stream := "/Videos/movie/stream?MediaSourceId=source&api_key=valid"
	response, err := noRedirectClient().Get(base + stream)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || len(service.current.cache.entries) != 1 {
		t.Fatal("未建立维护前的热缓存")
	}
	if _, err := db.BeginMaintenance(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	previous := service.current
	if err := service.Apply(t.Context(), config); err == nil || service.current != previous || service.Status(config).LastError == "" {
		t.Fatal("数据库恢复维护期仍允许重新应用配置")
	}
	queriesBefore, handoffsBefore := queries.Load(), handoffs.Load()
	for _, endpoint := range []string{stream, "/Users/Me", "/proxy-115?url=https%3A%2F%2Fcdn.115cdn.net%2Fvideo"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			request, err := http.NewRequest(method, base+endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := noRedirectClient().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("维护中 %s %s 返回 %d", method, endpoint, response.StatusCode)
			}
		}
	}
	if queries.Load() != queriesBefore || handoffs.Load() != handoffsBefore {
		t.Fatal("维护后仍访问上游、使用热缓存或转交取链代理")
	}
}
