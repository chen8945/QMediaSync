package emby302

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/util/https"
)

func serviceConfig(t *testing.T) {
	t.Helper()
	previous, base := config.C, config.BasePath
	config.BasePath = t.TempDir()
	config.C = &config.Config{Emby: &config.Emby{DownloadStrategy: config.DlStrategyOrigin, Strm: &config.Strm{}},
		Cache: &config.Cache{Enable: true}, VideoPreview: &config.VideoPreview{}, Openlist: &config.Openlist{}}
	if err := config.C.Cache.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { config.C, config.BasePath = previous, base })
}

func listeningService(t *testing.T, options Options) (*Service, string) {
	t.Helper()
	// 保持监听直到交接，避免 free-port 夹具自身抢占相同端口。
	socket, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	options.HTTPAddress = socket.Addr().String()
	options.Listen = func(_, _ string) (net.Listener, error) { return socket, nil }
	s := NewService(options)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return s, "http://" + socket.Addr().String()
}

func serviceGet(t *testing.T, raw string, wantCode int, wantBody string) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	matches := string(body) == wantBody
	if json.Valid([]byte(wantBody)) {
		var actual, wanted any
		_ = json.Unmarshal([]byte(wantBody), &wanted)
		matches = json.Unmarshal(body, &actual) == nil && reflect.DeepEqual(actual, wanted)
	}
	if err != nil || resp.StatusCode != wantCode || (wantBody != "" && !matches) {
		t.Fatalf("GET %s = %d %q, %v; want %d %q", raw, resp.StatusCode, body, err, wantCode, wantBody)
	}
}

func TestApplyPreservesOldHTTPAndWebSocket(t *testing.T) {
	serviceConfig(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	origin := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/embywebsocket" {
				websocket.Handler(func(conn *websocket.Conn) {
					defer conn.Close()
					for {
						var message string
						if err := websocket.Message.Receive(conn, &message); err != nil {
							return
						}
						if err := websocket.Message.Send(conn, name+message); err != nil {
							return
						}
					}
				}).ServeHTTP(w, r)
				return
			}
			if name == "A" && r.URL.Path == "/flow" {
				close(entered)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, name)
		}))
	}
	a, b := origin("A"), origin("B")
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	var permits atomic.Int32
	s, base := listeningService(t, Options{RuntimeAcquire: func() (func(), bool) {
		permits.Add(1)
		return func() { permits.Add(-1) }, true
	}})
	apply := func(enabled bool, raw string) {
		t.Helper()
		if err := s.Apply(t.Context(), Configuration{Enabled: enabled, URL: raw, APIKey: "key"}); err != nil {
			t.Fatal(err)
		}
	}
	apply(true, a.URL)
	dial := func() *websocket.Conn {
		t.Helper()
		cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(base, "http")+"/embywebsocket", base)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		conn, err := cfg.DialContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	echo := func(conn *websocket.Conn, want string) {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		if err := websocket.Message.Send(conn, "ping"); err != nil {
			t.Fatal(err)
		}
		var message string
		if err := websocket.Message.Receive(conn, &message); err != nil || message != want {
			t.Fatalf("WS message = %q, %v; want %q", message, err, want)
		}
	}
	oldWS := dial()
	echo(oldWS, "Aping")
	if permits.Load() != 0 {
		t.Fatal("upgraded WebSocket retained its database recovery permit")
	}
	flowDone := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/flow?mode=socket")
		if err == nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			err = readErr
			if string(body) != "A" {
				err = fmt.Errorf("old HTTP body = %q", body)
			}
		}
		flowDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("old HTTP request did not enter")
	}
	if permits.Load() != 1 {
		t.Fatal("unfinished HTTP request lost its recovery permit")
	}
	apply(true, b.URL)
	serviceGet(t, base+"/new", http.StatusOK, "B")
	echo(oldWS, "Aping")
	newWS := dial()
	echo(newWS, "Bping")
	apply(false, b.URL)
	serviceGet(t, base+"/new", http.StatusServiceUnavailable, "")
	echo(oldWS, "Aping")
	echo(newWS, "Bping")
	apply(true, b.URL)
	serviceGet(t, base+"/new", http.StatusOK, "B")
	// 完成旧流后仍保留 WS，最终停止必须关闭已升级连接。
	release <- struct{}{}
	if err := <-flowDone; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if permits.Load() != 0 {
		t.Fatal("final Stop retained request permits or released one twice")
	}
	for _, conn := range []*websocket.Conn{oldWS, newWS} {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var message string
		if err := websocket.Message.Receive(conn, &message); err == nil {
			t.Fatal("stopped service retained upgraded connection")
		}
	}
}

func TestApplySameURLNewKeyIsolatesAuthAndResponseCache(t *testing.T) {
	serviceConfig(t)
	var rejected atomic.Bool
	var authCalls, playbackCalls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/emby/Auth/Keys" {
			authCalls.Add(1)
			if rejected.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, "Access token is invalid or expired.")
			}
			return
		}
		playbackCalls.Add(1)
		_, _ = io.WriteString(w, `{"MediaSources":[]}`)
	}))
	t.Cleanup(origin.Close)
	s, base := listeningService(t, Options{})
	wanted := Configuration{Enabled: true, URL: origin.URL, APIKey: "old-key"}
	if err := s.Apply(t.Context(), wanted); err != nil {
		t.Fatal(err)
	}
	path := base + "/Items/20/PlaybackInfo?api_key=player-token"
	serviceGet(t, path, http.StatusOK, `{"MediaSources":[]}`)
	rejected.Store(true)
	if err := s.Apply(t.Context(), wanted); err != nil {
		t.Fatal(err)
	}
	serviceGet(t, path, http.StatusOK, `{"MediaSources":[]}`)
	if authCalls.Load() != 1 || playbackCalls.Load() != 1 {
		t.Fatal("healthy same-config save discarded auth or response cache")
	}
	wanted.APIKey = "new-key"
	if err := s.Apply(t.Context(), wanted); err != nil {
		t.Fatal(err)
	}
	serviceGet(t, path, http.StatusUnauthorized, "鉴权失败")
	rejected.Store(false)
	serviceGet(t, path, http.StatusOK, `{"MediaSources":[]}`)
	if authCalls.Load() != 3 || playbackCalls.Load() != 2 {
		t.Fatalf("new Key reused old identity: auth=%d playback=%d", authCalls.Load(), playbackCalls.Load())
	}
}

func TestLateOldResponseCannotReplaceNewSessionCache(t *testing.T) {
	serviceConfig(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/emby/Auth/Keys" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "A subtitle")
	}))
	t.Cleanup(a.Close)
	var bCalls atomic.Int32
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/emby/Auth/Keys" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		bCalls.Add(1)
		_, _ = io.WriteString(w, "B subtitle")
	}))
	t.Cleanup(b.Close)
	s, base := listeningService(t, Options{})
	if err := s.Apply(t.Context(), Configuration{Enabled: true, URL: a.URL, APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	path := base + "/Videos/20/Subtitles/0/0/Stream.srt?api_key=player-token"
	oldDone := make(chan error, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(path)
		if err == nil {
			defer resp.Body.Close()
			body, readErr := io.ReadAll(resp.Body)
			err = readErr
			if string(body) != "A subtitle" {
				err = fmt.Errorf("old response = %q", body)
			}
		}
		oldDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("old cacheable request did not enter")
	}
	if err := s.Apply(t.Context(), Configuration{Enabled: true, URL: b.URL, APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	serviceGet(t, path, http.StatusOK, "B subtitle")
	release <- struct{}{}
	if err := <-oldDone; err != nil {
		t.Fatal(err)
	}
	serviceGet(t, path, http.StatusOK, "B subtitle")
	if bCalls.Load() != 1 {
		t.Fatal("new Session did not retain its own cached response")
	}
}

func TestApplyBindFailureRetriesAndStopRejectsLateBind(t *testing.T) {
	for _, stopDuringBind := range []bool{false, true} {
		t.Run(fmt.Sprintf("stop=%v", stopDuringBind), func(t *testing.T) {
			serviceConfig(t)
			socket, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer socket.Close()
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var attempts atomic.Int32
			s := NewService(Options{HTTPAddress: socket.Addr().String(), Listen: func(_, _ string) (net.Listener, error) {
				if attempts.Add(1) == 1 {
					return nil, errors.New("port in use")
				}
				if stopDuringBind {
					close(entered)
					<-release
				}
				return socket, nil
			}})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if err := s.Stop(ctx); err != nil {
					t.Error(err)
				}
			})
			wanted := Configuration{Enabled: true, URL: "http://offline.invalid", APIKey: "key"}
			if err := s.Apply(t.Context(), wanted); err == nil {
				t.Fatal("failed bind was reported as applied")
			}
			if !stopDuringBind {
				if err := s.Apply(t.Context(), wanted); err != nil {
					t.Fatal("same settings did not retry offline startup", err)
				}
				return
			}
			applied := make(chan error, 1)
			go func() { applied <- s.Apply(t.Context(), wanted) }()
			<-entered
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
			if err := s.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("Stop did not wait for real bind preparation", err)
			}
			cancel()
			release <- struct{}{}
			if err := <-applied; err == nil {
				t.Fatal("late bind revived stopped service")
			}
			if err := s.Apply(t.Context(), wanted); err == nil {
				t.Fatal("stopped service accepted another Apply")
			}
		})
	}
}

func TestCustomAssetsHonorModuleTLSAndStopCancellation(t *testing.T) {
	for _, block := range []bool{false, true} {
		t.Run(fmt.Sprintf("blocked=%v", block), func(t *testing.T) {
			serviceConfig(t)
			entered, canceled, abort := make(chan struct{}), make(chan struct{}), make(chan struct{})
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if block {
					_, _ = io.WriteString(w, "partial;")
					w.(http.Flusher).Flush()
					close(entered)
					select {
					case <-r.Context().Done():
						close(canceled)
					case <-abort:
					}
					return
				}
				_, _ = io.WriteString(w, "window.loaded = true;")
			}))
			defer func() { close(abort); origin.Close() }()
			dir := filepath.Join(config.BasePath, "custom-js")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "remote.js"), []byte(origin.URL+"/script.js"), 0o600); err != nil {
				t.Fatal(err)
			}
			s, base := listeningService(t, Options{ClientOptions: https.ClientOptions{InsecureSkipVerify: true}})
			if err := s.Apply(t.Context(), Configuration{Enabled: true, URL: origin.URL, APIKey: "key"}); err != nil {
				t.Fatal(err)
			}
			if !block {
				resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(base + "/ge2o/custom.js")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil || !strings.Contains(string(body), "window.loaded = true;") {
					t.Fatal("custom asset ignored module TLS policy", string(body), err)
				}
				return
			}
			requestDone := make(chan struct{})
			go func() {
				defer close(requestDone)
				resp, err := (&http.Client{Timeout: 5 * time.Second}).Get(base + "/ge2o/custom.js")
				if err == nil {
					_, _ = io.Copy(io.Discard, resp.Body)
					_ = resp.Body.Close()
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("custom asset did not start")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := s.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-canceled:
			case <-ctx.Done():
				t.Fatal("module Stop did not cancel remote asset body")
			}
			<-requestDone
		})
	}
}

func TestPlayingAuxiliaryRetainsSessionAndRuntimePermit(t *testing.T) {
	serviceConfig(t)
	entered, release, completed, abort := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var stops atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.HasSuffix(r.URL.Path, "/Progress") {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			case <-abort:
				return
			}
		} else if stops.Add(1) == 2 {
			close(completed)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer func() { close(abort); origin.Close() }()
	var foreignCalls atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { foreignCalls.Add(1) }))
	defer other.Close()
	var permits atomic.Int32
	s, base := listeningService(t, Options{RuntimeAcquire: func() (func(), bool) {
		permits.Add(1)
		return func() { permits.Add(-1) }, true
	}})
	if err := s.Apply(t.Context(), Configuration{Enabled: true, URL: origin.URL, APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Post(base+"/Sessions/Playing/Stopped?api_key=token", "application/json", strings.NewReader(`{"ItemId":"20","PositionTicks":4000000000}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("auxiliary Progress did not start")
	}
	if err := s.Apply(t.Context(), Configuration{Enabled: true, URL: other.URL, APIKey: "new-key"}); err != nil {
		t.Fatal(err)
	}
	if permits.Load() != 1 {
		t.Fatalf("accepted auxiliary request lost its recovery permit: %d", permits.Load())
	}
	close(release)
	select {
	case <-completed:
	case <-time.After(time.Second):
		t.Fatal("old auxiliary Stopped did not complete")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if permits.Load() != 0 || foreignCalls.Load() != 0 {
		t.Fatalf("auxiliary lifetime/config mixed: permits=%d foreign=%d", permits.Load(), foreignCalls.Load())
	}
}
