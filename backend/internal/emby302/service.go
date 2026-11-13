package emby302

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/service/m3u8"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/web"
	"qmediasync/internal/emby302/web/cache"
	"qmediasync/internal/proxyhttp"

	"github.com/gin-gonic/gin"
)

type Configuration struct {
	Enabled     bool
	URL, APIKey string
}

// Options 注入启动只读监听配置和应用拥有的边界。
type Options struct {
	Listen                    func(string, string) (net.Listener, error)
	HTTPAddress, HTTPSAddress string
	CertificateFile, KeyFile  string
	ClientOptions             https.ClientOptions
	Guard                     func(string, string, http.RoundTripper) gin.HandlerFunc
	RuntimeAcquire            func() (func(), bool)
	Preload                   emby.PreloadOptions
}

type sessionEntry struct {
	config             Configuration
	session            *emby.Session
	router             http.Handler
	refs               int
	retiring, cleaning bool
}

type listener struct {
	address string
	server  *http.Server
	socket  net.Listener
}

// Service 持有稳定监听器；网页连接改变只发布完整新会话。
type Service struct {
	options     Options
	mu          sync.Mutex
	current     *sessionEntry
	entries     map[*sessionEntry]struct{}
	listeners   map[string]*listener
	closing     bool
	preparing   int
	serving     int
	revision    uint64
	done        chan struct{}
	drained     bool
	ctx         context.Context
	cancel      context.CancelFunc
	connections proxyhttp.Connections
	client      *http.Client
	responses   *cache.Store
	playlists   *m3u8.Store
	assets      *emby.CustomAssets
}

func NewService(options Options) *Service {
	ctx, cancel := context.WithCancel(context.Background())
	client := https.NewClient(options.ClientOptions)
	return &Service{options: options, entries: make(map[*sessionEntry]struct{}), listeners: make(map[string]*listener),
		done: make(chan struct{}), ctx: ctx, cancel: cancel, client: client,
		assets: emby.NewCustomAssets(ctx, client, config.BasePath), responses: cache.NewStore(config.C.Cache.ExpiredDuration()), playlists: m3u8.NewStore(client)}
}

func (s *Service) Transport() http.RoundTripper { return s.client.Transport }

// Apply 在调用方有序提交后完成本地发布，不等待旧流也不探测上游。
func (s *Service) Apply(ctx context.Context, wanted Configuration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	wanted.URL = strings.TrimRight(strings.TrimSpace(wanted.URL), "/")
	wanted.APIKey = strings.TrimSpace(wanted.APIKey)
	wanted.Enabled = wanted.Enabled && wanted.URL != "" && wanted.APIKey != ""
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return errors.New("Emby 302 已停止")
	}
	s.preparing++
	s.revision++
	revision := s.revision
	missing := make([]string, 0, 2)
	for _, addr := range []string{s.options.HTTPAddress, s.options.HTTPSAddress} {
		if addr != "" && s.listeners[addr] == nil {
			missing = append(missing, addr)
		}
	}
	same := s.current != nil && s.current.config == wanted && len(missing) == 0
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.preparing--; s.finishLocked(); s.mu.Unlock() }()
	if same {
		return nil
	}
	var prepared []*listener
	if wanted.Enabled {
		for _, address := range missing {
			bound, err := s.bind(address)
			if err != nil {
				for _, v := range prepared {
					_ = v.socket.Close()
				}
				return err
			}
			prepared = append(prepared, bound)
		}
	}
	var next *sessionEntry
	if wanted.Enabled {
		next = &sessionEntry{config: wanted}
		preload := s.options.Preload
		preload.Instance = "emby302:" + rand.Text()
		next.session = emby.NewSession(emby.SessionOptions{Origin: wanted.URL, Client: s.client, Assets: s.assets, Cache: s.responses.NewScope(), Preload: preload,
			RunAuxiliary: func(work func(context.Context)) bool { return s.runAuxiliary(next, work) }})
		var guard gin.HandlerFunc
		if s.options.Guard != nil {
			guard = s.options.Guard(wanted.URL, wanted.APIKey, s.client.Transport)
		}
		next.router = web.NewRouter(next.session, guard, s.playlists)
	}
	s.mu.Lock()
	if s.closing || s.revision != revision {
		s.mu.Unlock()
		for _, v := range prepared {
			_ = v.socket.Close()
		}
		if next != nil {
			next.session.Close()
		}
		return errors.New("Emby 302 应用已被后续状态替代")
	}
	old := s.current
	s.current = next
	if next != nil {
		s.entries[next] = struct{}{}
	}
	if old != nil {
		old.retiring = true
		old.session.Deactivate()
	}
	for _, v := range prepared {
		s.listeners[v.address] = v
		s.serving++
	}
	s.mu.Unlock()
	for _, v := range prepared {
		go s.serve(v)
	}
	if old != nil {
		old.session.Retire()
		s.cleanup(old)
	}
	return nil
}

func (s *Service) bind(address string) (*listener, error) {
	server := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
		BaseContext: func(net.Listener) context.Context { return s.ctx }}
	var certificate *tls.Certificate
	if address == s.options.HTTPSAddress {
		c, err := tls.LoadX509KeyPair(s.options.CertificateFile, s.options.KeyFile)
		if err != nil {
			return nil, errors.New("Emby 302 证书或私钥无法读取，请检查后再次保存")
		}
		certificate = &c
	}
	listen := s.options.Listen
	if listen == nil {
		listen = net.Listen
	}
	socket, err := listen("tcp", address)
	if err != nil {
		return nil, errors.New("Emby 302 端口无法监听，请检查端口占用后再次保存")
	}
	socket = s.connections.Wrap(socket)
	if certificate != nil {
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{*certificate}, MinVersion: tls.VersionTLS12}
		server.TLSNextProto = map[string]func(*http.Server, *tls.Conn, http.Handler){}
		socket = tls.NewListener(socket, server.TLSConfig)
	}
	return &listener{address: address, server: server, socket: socket}, nil
}

func (s *Service) serve(l *listener) {
	defer func() { s.mu.Lock(); s.serving--; s.finishLocked(); s.mu.Unlock() }()
	err := l.server.Serve(l.socket)
	s.mu.Lock()
	if s.listeners[l.address] == l {
		delete(s.listeners, l.address)
	}
	closing := s.closing
	s.finishLocked()
	s.mu.Unlock()
	_ = l.socket.Close()
	if !closing && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
		logs.Error("Emby 302 监听异常，请重新保存设置恢复监听")
	}
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	entry := s.current
	if s.closing || entry == nil {
		s.mu.Unlock()
		http.Error(w, "Emby 服务未启用或配置暂不可用", http.StatusServiceUnavailable)
		return
	}
	entry.refs++
	s.mu.Unlock()
	defer s.release(entry)
	ctx := r.Context()
	if s.options.RuntimeAcquire != nil {
		release, ok := s.options.RuntimeAcquire()
		if !ok {
			http.Error(w, "数据库恢复维护中，请重启应用后再试", http.StatusServiceUnavailable)
			return
		}
		release = sync.OnceFunc(release)
		defer release()
		ctx = emby.WithRuntimeRequestRelease(ctx, release)
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	entry.router.ServeHTTP(w, r.Clone(ctx))
}

func (s *Service) runAuxiliary(entry *sessionEntry, work func(context.Context)) bool {
	var releaseRuntime func()
	if s.options.RuntimeAcquire != nil {
		var admitted bool
		releaseRuntime, admitted = s.options.RuntimeAcquire()
		if !admitted {
			return false
		}
	}
	s.mu.Lock()
	if s.closing || entry.refs == 0 {
		s.mu.Unlock()
		if releaseRuntime != nil {
			releaseRuntime()
		}
		return false
	}
	entry.refs++
	s.mu.Unlock()
	go func() {
		defer s.release(entry)
		if releaseRuntime != nil {
			defer releaseRuntime()
		}
		ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
		defer cancel()
		work(ctx)
	}()
	return true
}

func (s *Service) release(entry *sessionEntry) {
	s.mu.Lock()
	entry.refs--
	s.mu.Unlock()
	s.cleanup(entry)
}
func (s *Service) cleanup(entry *sessionEntry) {
	s.mu.Lock()
	if !entry.retiring || entry.refs != 0 || entry.cleaning {
		s.mu.Unlock()
		return
	}
	entry.cleaning = true
	s.mu.Unlock()
	entry.session.Close()
	s.mu.Lock()
	delete(s.entries, entry)
	s.finishLocked()
	s.mu.Unlock()
}
func (s *Service) finishLocked() {
	if s.closing && !s.drained && s.preparing == 0 && s.serving == 0 && len(s.entries) == 0 && len(s.listeners) == 0 {
		s.drained = true
		close(s.done)
	}
}

// Stop 永久撤销准入并等候真正请求、辅助工作与模块后台退出。
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.current = nil
	entries := make([]*sessionEntry, 0, len(s.entries))
	for entry := range s.entries {
		entry.retiring = true
		entry.session.Deactivate()
		entries = append(entries, entry)
	}
	listeners := make([]*listener, 0, len(s.listeners))
	for _, v := range s.listeners {
		listeners = append(listeners, v)
	}
	s.finishLocked()
	s.mu.Unlock()
	s.cancel()
	for _, entry := range entries {
		entry.session.Retire()
		s.cleanup(entry)
	}
	for _, v := range listeners {
		_ = v.socket.Close()
		_ = v.server.Close()
	}
	s.connections.Close()
	responseErr := s.responses.Stop(ctx)
	playlistErr := s.playlists.Stop(ctx)
	select {
	case <-s.done:
	case <-ctx.Done():
		return errors.Join(responseErr, playlistErr, ctx.Err())
	}
	if err := errors.Join(responseErr, playlistErr); err != nil {
		return err
	}
	s.client.CloseIdleConnections()
	return nil
}
