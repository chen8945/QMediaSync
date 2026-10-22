package fnosproxy

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"qmediasync/internal/models"
)

func TestServiceApplyEnableDisableAndValidationFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "upstream") }))
	defer upstream.Close()
	service := NewService(Options{HTTPHost: "127.0.0.1:12333", HTTPPort: freePort(t), HTTPSPort: freePort(t)})
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	config := models.FnosProxyConfig{FnosURL: upstream.URL}
	if err := service.Start(config); err != nil {
		t.Fatal(err)
	}
	config.Enabled = true
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	previous := service.current
	if err := service.Apply(t.Context(), config); err != nil || service.current != previous {
		t.Fatal("相同健康配置重建了实例")
	}
	invalid := config
	invalid.FnosURL = "http://user:secret@invalid/path"
	if err := service.Apply(t.Context(), invalid); err == nil {
		t.Fatal("非法配置被应用")
	}
	status := service.Status(invalid)
	if !status.Running || !status.RestartRequired || status.LastError == "" || strings.Contains(status.LastError, "secret") || service.current != previous {
		t.Fatalf("准备失败必须保留旧运行状态并标记新配置未应用：%+v", status)
	}
	if err := service.Apply(t.Context(), config); err != nil || service.Status(config).LastError != "" {
		t.Fatalf("再次保存没有恢复：%v", err)
	}
	config.Enabled = false
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if status := service.Status(config); status.Running || status.RestartRequired || status.ListenPort != 0 {
		t.Fatalf("关闭没有立即应用：%+v", status)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(service.options.HTTPPort))
	if err != nil {
		t.Fatalf("关闭后端口未释放：%v", err)
	}
	_ = listener.Close()
	config.Enabled = true
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatalf("关闭后不能重新启用：%v", err)
	}
	if err := service.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(t.Context(), config); err == nil || service.Status(config).Running {
		t.Fatal("最终停止后保存复活了服务")
	}
}

func TestServiceApplySwitchesMappingAndClosesOldConnections(t *testing.T) {
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	for root, pickcode := range map[string]string{firstRoot: "first", secondRoot: "second"} {
		if err := os.WriteFile(filepath.Join(root, "movie.strm"), []byte("http://private-qms:12333/115/newurl?pickcode="+pickcode+"&userid=owner"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	echo := websocket.Handler(func(conn *websocket.Conn) { _, _ = io.Copy(conn, conn) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/movie":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": "/fnos/media/movie.strm"}}})
		case "/socket":
			echo.ServeHTTP(w, r)
		}
	}))
	defer upstream.Close()
	service, config, base := startService(t, upstream.URL, Options{
		OutputRoots:  func(context.Context) ([]string, error) { return []string{firstRoot, secondRoot}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
		MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://cdn.example/"+r.URL.Query().Get("pickcode"), http.StatusFound)
		}),
	})
	config.PathMappings = []models.FnosPathMapping{{FnosRoot: "/fnos/media", LocalRoot: firstRoot}}
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	read := func(want string) {
		t.Helper()
		response, err := noRedirectClient().Get(base + "/Videos/movie/stream?MediaSourceId=source&api_key=valid")
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.Header.Get("Location") != "https://cdn.example/"+want {
			t.Fatalf("路径映射未生效：%d %q", response.StatusCode, response.Header.Get("Location"))
		}
	}
	read("first")
	previous := service.current
	conn, err := websocket.Dial("ws"+strings.TrimPrefix(base, "http")+"/socket", "", "http://127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Send(conn, "healthy"); err != nil {
		t.Fatal(err)
	}
	var echoed string
	if err := websocket.Message.Receive(conn, &echoed); err != nil || echoed != "healthy" {
		t.Fatalf("无变化保存中断了连接：%s %v", echoed, err)
	}
	config.PathMappings[0].LocalRoot = secondRoot
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if status := service.Status(config); !status.Running || status.RestartRequired || status.LastError != "" {
		t.Fatalf("映射更改没有立即应用：%+v", status)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if websocket.Message.Receive(conn, &echoed) == nil {
		t.Fatal("配置切换后旧 WebSocket 未关闭")
	}
	previous.cache.mu.Lock()
	cleared := previous.cache.stopped && len(previous.cache.entries) == 0
	previous.cache.mu.Unlock()
	if !cleared || service.current == previous {
		t.Fatal("配置切换没有清理旧来源缓存")
	}
	read("second")
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "new upstream") }))
	defer other.Close()
	config.FnosURL = other.URL
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	response, err := noRedirectClient().Get(base + "/System/Info/Public")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "new upstream" {
		t.Fatal("上游地址修改没有立即应用")
	}
}

func TestServiceApplyTimeoutRetainsOldCleanupResponsibility(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	service, config, base := startService(t, upstream.URL, Options{MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
	})})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, err := noRedirectClient().Get(base + "/proxy-115?url=https%3A%2F%2Fcdn.115cdn.net%2Fvideo")
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("请求未进入已有播放代理")
	}
	previous := service.current
	config.PathMappings = []models.FnosPathMapping{{FnosRoot: "/fnos", LocalRoot: t.TempDir()}}
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		err := service.Apply(ctx, config)
		cancel()
		if err == nil || service.Status(config).Running || service.current != previous {
			t.Fatalf("超时丢失旧实例或提前启用了新实例：%v %+v", err, service.Status(config))
		}
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("切换没有取消旧请求")
	}
	unblock()
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatalf("旧请求退出后不能再次保存恢复：%v", err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("旧请求没有完成")
	}
	if service.current == previous || !service.Status(config).Running {
		t.Fatal("旧请求退出后没有重建实例")
	}
}

func TestServiceApplyRetriesPartialHTTPSFailure(t *testing.T) {
	certificate, key, _ := certificateFiles(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	service := NewService(Options{HTTPHost: "127.0.0.1:12333", HTTPSHost: "127.0.0.1:12332", HTTPPort: freePort(t), HTTPSPort: occupied.Addr().(*net.TCPAddr).Port, CertificateFile: certificate, KeyFile: key})
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	config := models.FnosProxyConfig{Enabled: true, FnosURL: upstream.URL}
	if err := service.Start(config); err == nil || !service.Status(config).Running || service.Status(config).HTTPSEnabled {
		t.Fatal("HTTPS 失败没有保留 HTTP")
	}
	if err := occupied.Close(); err != nil {
		t.Fatal(err)
	}
	if err := service.Apply(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if status := service.Status(config); !status.Running || !status.HTTPSEnabled || status.HTTPSPort != service.options.HTTPSPort || status.LastError != "" || status.RestartRequired {
		t.Fatalf("相同配置再次保存没有重试 HTTPS：%+v", status)
	}
}

func TestServiceStopDuringApplyCannotReviveListener(t *testing.T) {
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	service, config, base := startService(t, upstream.URL, Options{MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		<-release
	})})
	go func() {
		response, err := noRedirectClient().Get(base + "/proxy-115?url=https%3A%2F%2Fcdn.115cdn.net%2Fvideo")
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("请求没有进入内部代理")
	}
	config.PathMappings = []models.FnosPathMapping{{FnosRoot: "/fnos", LocalRoot: t.TempDir()}}
	applied := make(chan error, 1)
	go func() { applied <- service.Apply(t.Context(), config) }()
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("配置切换没有取消旧请求")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- service.Stop(t.Context()) }()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		service.mu.Lock()
		closed := service.closed
		service.mu.Unlock()
		if closed {
			break
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("停止没有阻止后续应用")
		}
	}
	unblock()
	select {
	case err := <-applied:
		if err == nil {
			t.Fatal("最终停止期间仍发布了新实例")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("应用没有退出")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("停止没有退出")
	}
	if service.Status(config).Running {
		t.Fatal("停止与保存并发后仍在运行")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(service.options.HTTPPort))
	if err != nil {
		t.Fatalf("最终停止后端口未释放：%v", err)
	}
	_ = listener.Close()
}
