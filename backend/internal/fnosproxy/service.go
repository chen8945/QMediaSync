package fnosproxy

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"qmediasync/internal/backup"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	"qmediasync/internal/validation"
)

const (
	DefaultHTTPPort  = 9005
	DefaultHTTPSPort = 9006
)

// Options 注入当前主站的播放入口和可信目录；不向飞牛端口开放整个主站。
type Options struct {
	MainHandler        http.Handler
	CertificateFile    string
	KeyFile            string
	HTTPHost           string
	HTTPSHost          string
	HTTPPort           int
	HTTPSPort          int
	ReservedPorts      []int
	OutputRoots        func(context.Context) ([]string, error)
	KnownOrigins       func(context.Context) ([]string, error)
	OpenListOrigins    func(context.Context) ([]string, error)
	IdentityCandidates func(context.Context, Source) ([]Candidate, error)
	Preloader          *playback.Preloader
	NewPreload         func(rawURL, userAgent string, forceDirect bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool)
}

// Service 独立持有飞牛监听器、出站连接与来源缓存。
type Service struct {
	options      Options
	opMu         sync.Mutex
	mu           sync.Mutex
	config       models.FnosProxyConfig
	started      bool
	running      bool
	lastErr      string
	current      *instance
	closed       bool
	httpsRunning bool
}

type instance struct {
	service   *Service
	config    models.FnosProxyConfig
	upstream  *url.URL
	ctx       context.Context
	cancel    context.CancelFunc
	transport *http.Transport
	proxy     *httputil.ReverseProxy
	cache     sourceCache
	servers   []*http.Server
	listeners []net.Listener
	mu        sync.Mutex
	conns     map[*trackedConn]struct{}
	stopped   bool
	done      sync.WaitGroup
	active    sync.WaitGroup
	preloadID string
	preload   preloadState
}

// NewService 创建尚未监听的服务，关闭配置不会建立连接或启动后台任务。
func NewService(options Options) *Service {
	if options.HTTPPort == 0 {
		options.HTTPPort = DefaultHTTPPort
	}
	if options.HTTPSPort == 0 {
		options.HTTPSPort = DefaultHTTPSPort
	}
	return &Service{options: options}
}

// Start 应用启动时配置；后续网页保存使用同一条 Apply 路径。
func (s *Service) Start(config models.FnosProxyConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Apply(ctx, config)
}

// Apply 串行切换飞牛实例；修改配置会取消旧请求，主站和 Emby 不受影响。
func (s *Service) Apply(ctx context.Context, config models.FnosProxyConfig) error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return s.applyError(errors.New("QMediaSync 正在停止，飞牛影视配置已保存但未应用"))
	}
	release, admitted := backup.AcquireRuntimeRequest()
	if !admitted {
		return s.applyError(errors.New("数据库恢复维护中，飞牛影视配置不能应用，请等待恢复结果并重启"))
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return s.applyError(errors.New("应用飞牛影视配置超时，请再次保存重试"))
	}
	config.PathMappings = append([]models.FnosPathMapping{}, config.PathMappings...)
	s.mu.Lock()
	healthy := s.started && sameConfig(config, s.config) && s.lastErr == "" && (s.running || !config.Enabled)
	previous := s.current
	s.mu.Unlock()
	if healthy {
		return nil
	}
	var next *instance
	var err error
	if config.Enabled {
		next, err = s.prepare(config)
		if err != nil {
			return s.applyError(err)
		}
	}
	s.mu.Lock()
	s.running, s.httpsRunning = false, false
	s.mu.Unlock()
	if previous != nil {
		previous.closeResources()
		if err := previous.wait(ctx); err != nil {
			if next != nil {
				next.closeResources()
			}
			return s.applyError(errors.New("等待旧飞牛影视连接退出超时，请再次保存重试"))
		}
	}
	s.mu.Lock()
	s.current = nil
	s.mu.Unlock()
	if !config.Enabled {
		s.mu.Lock()
		s.started, s.config, s.lastErr = true, config, ""
		s.mu.Unlock()
		return nil
	}
	warning, err := next.bind()
	if err != nil {
		next.closeResources()
		return s.applyError(err)
	}
	if ctx.Err() != nil {
		next.closeResources()
		return s.applyError(errors.New("应用飞牛影视配置超时，请再次保存重试"))
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		next.closeResources()
		return s.applyError(errors.New("QMediaSync 正在停止，飞牛影视配置已保存但未应用"))
	}
	s.started, s.config, s.current, s.running, s.lastErr = true, config, next, true, warning
	s.httpsRunning = len(next.servers) > 1
	s.mu.Unlock()
	next.serve()
	if warning != "" {
		return errors.New(warning)
	}
	return nil
}

func (s *Service) applyError(err error) error {
	s.mu.Lock()
	s.started, s.lastErr = true, err.Error()
	s.mu.Unlock()
	return err
}

func (s *Service) prepare(config models.FnosProxyConfig) (*instance, error) {
	upstream, err := url.Parse(config.FnosURL)
	if err != nil || upstream.Host == "" || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" ||
		(upstream.Scheme != "http" && upstream.Scheme != "https") || (upstream.Path != "" && upstream.Path != "/") {
		return nil, errors.New("飞牛影视地址无效，请填写 HTTP 或 HTTPS 服务根地址")
	}
	if s.options.HTTPPort < 1 || s.options.HTTPPort > 65535 || s.options.HTTPSPort < 1 ||
		s.options.HTTPSPort > 65535 || s.options.HTTPPort == s.options.HTTPSPort {
		return nil, errors.New("飞牛影视反代端口无效或重复")
	}
	if err := ValidatePorts(config, s.options.HTTPPort, s.options.HTTPSPort, s.options.ReservedPorts...); err != nil {
		return nil, err
	}
	mappings, err := models.NormalizeFnosPathMappings(config.PathMappings)
	if err != nil {
		return nil, errors.New("飞牛影视路径映射无效")
	}
	config.PathMappings = mappings
	transport, err := helpers.GetProxyTransport("")
	if err != nil {
		return nil, errors.New("飞牛影视连接初始化失败")
	}
	ctx, cancel := context.WithCancel(context.Background())
	inst := &instance{service: s, config: config, upstream: upstream, ctx: ctx, cancel: cancel,
		transport: transport, conns: make(map[*trackedConn]struct{}), cache: newSourceCache(), preloadID: "fnos:" + rand.Text()}
	inst.proxy = &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(upstream)
			pr.Out.URL.Path = upstreamPath(pr.In.URL.Path)
			if pr.Out.URL.Path == pr.In.URL.Path {
				pr.Out.URL.RawPath = pr.In.URL.RawPath
			} else {
				pr.Out.URL.RawPath = ""
			}
			pr.Out.Host = upstream.Host
			pr.Out.Header = upstreamHeaders(pr.Out.Header)
			pr.Out.Header.Del("Forwarded")
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
			pr.Out.Header.Set("X-QMS-Fnos-Proxy-Hop", "1")
		},
		ModifyResponse: inst.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if r.Context().Err() == nil && helpers.AppLogger != nil {
				helpers.AppLogger.Warnf("[飞牛影视反代] 连接失败：%s", helpers.RedactSensitiveLog(helpers.URLRequestErrorForLog(err).Error()))
			}
			http.Error(w, "无法连接飞牛影视", http.StatusBadGateway)
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	return inst, nil
}

func (inst *instance) bind() (string, error) {
	options := inst.service.options
	if err := inst.bindListener(options.HTTPHost, options.HTTPPort, nil); err != nil {
		return "", errors.New("飞牛影视 HTTP 端口无法监听，请检查端口占用与系统权限后再次保存")
	}
	_, certificateErr := os.Stat(options.CertificateFile)
	_, keyErr := os.Stat(options.KeyFile)
	if errors.Is(certificateErr, os.ErrNotExist) || errors.Is(keyErr, os.ErrNotExist) ||
		options.CertificateFile == "" || options.KeyFile == "" {
		return "", nil
	}
	certificate, err := tls.LoadX509KeyPair(options.CertificateFile, options.KeyFile)
	if err != nil {
		return "飞牛影视 HTTP 已运行，HTTPS 无法启动，请检查主站证书、私钥及读取权限后再次保存", nil
	}
	if err := inst.bindListener(options.HTTPSHost, options.HTTPSPort, &certificate); err != nil {
		return "飞牛影视 HTTP 已运行，HTTPS 端口无法监听，请检查端口占用与系统权限后再次保存", nil
	}
	return "", nil
}

func (inst *instance) bindListener(host string, port int, certificate *tls.Certificate) error {
	address, err := listenAddress(host, port)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	inst.listeners = append(inst.listeners, &trackedListener{Listener: listener, instance: inst})
	server := &http.Server{Handler: inst, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
		MaxHeaderBytes: 1 << 20, ErrorLog: log.New(io.Discard, "", 0)}
	if certificate != nil {
		server.TLSConfig = &tls.Config{Certificates: []tls.Certificate{*certificate}, MinVersion: tls.VersionTLS12}
	}
	inst.servers = append(inst.servers, server)
	return nil
}

func (inst *instance) serve() {
	for i, server := range inst.servers {
		inst.done.Add(1)
		go func() {
			defer inst.done.Done()
			var err error
			if i == 1 {
				err = server.ServeTLS(inst.listeners[i], "", "")
			} else {
				err = server.Serve(inst.listeners[i])
			}
			if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				if i == 0 {
					inst.closeResources()
				}
				inst.service.mu.Lock()
				if inst.service.current == inst {
					if i == 0 {
						inst.service.running = false
					}
					inst.service.httpsRunning = false
					inst.service.lastErr = "飞牛影视反代监听异常退出，请检查系统资源后再次保存"
				}
				inst.service.mu.Unlock()
			}
		}()
	}
}

func listenAddress(host string, port int) (string, error) {
	if host == "" {
		return net.JoinHostPort("", strconv.Itoa(port)), nil
	}
	addressHost, _, err := net.SplitHostPort(host)
	if err != nil {
		return "", errors.New("主站监听地址无效，无法确定飞牛影视监听范围")
	}
	return net.JoinHostPort(addressHost, strconv.Itoa(port)), nil
}

// Status 区分已保存配置与当前实例；restart_required 仅保留为尚未应用的兼容字段。
func (s *Service) Status(saved models.FnosProxyConfig) models.FnosProxyRuntimeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := models.FnosProxyRuntimeStatus{Running: s.running, LastError: s.lastErr,
		RestartRequired: s.started && (!sameConfig(saved, s.config) || saved.Enabled && !s.running)}
	if s.running {
		status.ListenPort = s.options.HTTPPort
		status.HTTPSEnabled = s.httpsRunning
		if status.HTTPSEnabled {
			status.HTTPSPort = s.options.HTTPSPort
		}
	}
	return status
}

func sameConfig(a, b models.FnosProxyConfig) bool {
	return a.Enabled == b.Enabled && a.FnosURL == b.FnosURL && slices.Equal(a.PathMappings, b.PathMappings)
}

// Stop 取消在途请求，关闭普通及已升级连接，并等待监听器退出。
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.mu.Lock()
	inst := s.current
	s.running, s.httpsRunning = false, false
	s.mu.Unlock()
	if inst == nil {
		return nil
	}
	inst.closeResources()
	return inst.wait(ctx)
}

func (inst *instance) wait(ctx context.Context) error {
	finished := make(chan struct{})
	go func() { inst.done.Wait(); inst.active.Wait(); close(finished) }()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (inst *instance) closeResources() {
	inst.mu.Lock()
	if inst.stopped {
		inst.mu.Unlock()
		return
	}
	inst.stopped = true
	conns := make([]*trackedConn, 0, len(inst.conns))
	for conn := range inst.conns {
		conns = append(conns, conn)
	}
	inst.mu.Unlock()
	inst.cancel()
	if preloader := inst.service.options.Preloader; preloader != nil {
		preloader.CancelInstance(inst.preloadID)
	}
	inst.preload.clear()
	inst.cache.clear()
	for _, listener := range inst.listeners {
		_ = listener.Close()
	}
	for _, server := range inst.servers {
		_ = server.Close()
	}
	for _, conn := range conns {
		_ = conn.Close()
	}
	inst.transport.CloseIdleConnections()
}

type trackedListener struct {
	net.Listener
	instance *instance
}

func (listener *trackedListener) Accept() (net.Conn, error) {
	conn, err := listener.Listener.Accept()
	if err != nil {
		return nil, err
	}
	tracked := &trackedConn{Conn: conn, instance: listener.instance}
	listener.instance.mu.Lock()
	if listener.instance.stopped {
		listener.instance.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	listener.instance.conns[tracked] = struct{}{}
	listener.instance.mu.Unlock()
	return tracked, nil
}

type trackedConn struct {
	net.Conn
	instance *instance
}

func (conn *trackedConn) Close() error {
	err := conn.Conn.Close()
	conn.instance.mu.Lock()
	delete(conn.instance.conns, conn)
	conn.instance.mu.Unlock()
	return err
}

func (inst *instance) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-QMS-Fnos-Proxy-Hop") != "" {
		http.Error(w, "飞牛影视地址指向了当前反代，请检查配置", http.StatusLoopDetected)
		return
	}
	inst.mu.Lock()
	if inst.stopped {
		inst.mu.Unlock()
		http.Error(w, "飞牛影视反代正在停止", http.StatusServiceUnavailable)
		return
	}
	inst.active.Add(1)
	inst.mu.Unlock()
	defer inst.active.Done()
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(inst.ctx, cancel)
	defer stop()
	defer cancel()
	r = r.Clone(ctx)
	if inst.ctx.Err() != nil {
		http.Error(w, "飞牛影视反代正在停止", http.StatusServiceUnavailable)
		return
	}
	release, admitted := backup.AcquireRuntimeRequest()
	if !admitted {
		http.Error(w, "QMediaSync 正在恢复数据库，请重启后再试", http.StatusServiceUnavailable)
		return
	}
	progressReport := r.Method == http.MethodPost && preloadProgressRoute(upstreamPath(r.URL.Path))
	if progressReport {
		inst.capturePreloadBody(w, r)
	}
	inst.beginPreloadRequest(r)
	if r.URL.Path == "/proxy-115" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		release()
		inst.forwardProxy(w, r)
		return
	}
	playback, isPlayback := parsePlaybackRoute(r.URL.Path)
	isMetadata := isPlayback && playback.kind == "info" && (r.Method == http.MethodGet || r.Method == http.MethodPost) ||
		r.URL.Path == nativeStreamPath && r.Method == http.MethodPost
	if inst.preloadEnabled() {
		route := upstreamPath(r.URL.Path)
		isMetadata = isMetadata || r.Method == http.MethodGet && (preloadDetailRoute.MatchString(route) || preloadQueueRoute.MatchString(route) || preloadNativePath(route)) ||
			r.Method == http.MethodPost && (route == "/v/api/v1/play/info" || preloadProgressRoute(route))
	}
	if isMetadata {
		metadataCtx, metadataCancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer metadataCancel()
		r = r.Clone(metadataCtx)
	}
	if !progressReport {
		inst.capturePreloadBody(w, r)
	}
	if isPlayback && r.Method == http.MethodPost && playback.kind == "info" {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now().Add(10 * time.Second))
		body, err := io.ReadAll(io.LimitReader(r.Body, metadataLimit+1))
		_ = controller.SetReadDeadline(time.Time{})
		_ = r.Body.Close()
		if err != nil || len(body) > metadataLimit {
			release()
			http.Error(w, "播放请求过大或无法读取", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		r.ContentLength = int64(len(body))
	}
	if isPlayback && playback.kind != "info" && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		source, ok := inst.authorizedSource(r, playback.itemID)
		var resolved sourceRecord
		if ok {
			resolved, ok = inst.resolveSource(r.Context(), r, source)
		}
		inst.reconcilePreloadPlayer(r, source, ok)
		if ok {
			release()
			if inst.preloadEnabled() {
				observed := &preloadPlaybackWriter{ResponseWriter: w}
				inst.serveResolved(observed, r, resolved)
				if observed.successful() && r.Context().Err() == nil {
					inst.rememberCompatiblePlayer(r, source)
				}
			} else {
				inst.serveResolved(w, r, resolved)
			}
			return
		}
	}
	// 短解析阶段已结束，长视频和 WebSocket 不占数据库恢复许可。
	release()
	inst.proxy.ServeHTTP(w, r)
}

func upstreamPath(value string) string {
	parts := strings.SplitN(strings.TrimPrefix(value, "/"), "/", 3)
	if len(parts) < 2 || !strings.EqualFold(parts[0], "emby") {
		return value
	}
	switch strings.ToLower(parts[1]) {
	case "items", "videos", "users", "shows", "system", "sessions", "audio", "displaypreferences", "genres", "persons", "studios", "years", "playlists", "collections", "quickconnect", "livetv", "search", "library", "branding":
		return value[len("/emby"):]
	}
	return value
}

func reservedCookie(name string) bool {
	return name == "auth_token" || name == "csrf_token"
}

func upstreamHeaders(original http.Header) http.Header {
	headers := original.Clone()
	headers.Del("Cookie")
	headers.Del("X-API-Key")
	headers.Del("X-CSRF-Token")
	request := &http.Request{Header: original}
	for _, cookie := range request.Cookies() {
		if !reservedCookie(cookie.Name) {
			target := &http.Request{Header: headers}
			target.AddCookie(cookie)
		}
	}
	return headers
}

func (inst *instance) modifyResponse(response *http.Response) error {
	if location := response.Header.Get("Location"); location != "" {
		if target, err := url.Parse(location); err == nil && target.Host != "" &&
			(target.Scheme == "" || target.Scheme == inst.upstream.Scheme) && strings.EqualFold(target.Host, inst.upstream.Host) {
			target.Scheme, target.Host = "", ""
			response.Header.Set("Location", target.String())
		}
	}
	cookies := response.Cookies()
	response.Header.Del("Set-Cookie")
	for _, cookie := range cookies {
		if reservedCookie(cookie.Name) {
			continue
		}
		if cookie.Domain != "" {
			if !strings.EqualFold(strings.TrimPrefix(cookie.Domain, "."), inst.upstream.Hostname()) {
				continue
			}
			cookie.Domain = ""
		}
		response.Header.Add("Set-Cookie", cookie.String())
	}
	if err := inst.observePreloadResponse(response); err != nil {
		return err
	}
	if response.Request.URL.Path == nativeStreamPath && response.Request.Method == http.MethodPost && response.StatusCode == http.StatusOK {
		return inst.rewriteNativeStream(response)
	}
	route, ok := parsePlaybackRoute(response.Request.URL.Path)
	if ok && route.kind == "info" && response.StatusCode >= 200 && response.StatusCode < 300 &&
		(response.Request.Method == http.MethodGet || response.Request.Method == http.MethodPost) {
		return inst.rewritePlaybackInfo(response, route.itemID)
	}
	return nil
}

func fingerprint(value any) string {
	encoded, _ := json.Marshal(value)
	return digest(encoded)
}

// ValidatePorts 在保存和启动前统一校验监听冲突及本机上游回环。
func ValidatePorts(config models.FnosProxyConfig, httpPort, httpsPort int, reservedPorts ...int) error {
	ports := []int{httpPort, httpsPort}
	if config.Enabled {
		for _, port := range ports {
			for _, reserved := range reservedPorts {
				if port != 0 && port == reserved {
					return validation.New("fnos_url", "飞牛影视固定端口与 QMediaSync 或 Emby 的监听端口冲突，请检查部署设置")
				}
			}
		}
	}
	if config.FnosURL == "" {
		return nil
	}
	upstream, err := url.Parse(config.FnosURL)
	if err != nil || !fnosLocalHost(upstream.Hostname()) {
		return nil
	}
	port := 80
	if upstream.Scheme == "https" {
		port = 443
	}
	if upstream.Port() != "" {
		port, _ = strconv.Atoi(upstream.Port())
	}
	for _, reserved := range append(ports, reservedPorts...) {
		if reserved != 0 && port == reserved {
			return validation.New("fnos_url", "飞牛影视地址不能指向本反代、QMediaSync 或 Emby 的监听端口")
		}
	}
	return nil
}

func fnosLocalHost(host string) bool {
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Split(host, "%")[0])
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return true
	}
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		local, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.Equal(local) {
			return true
		}
	}
	return false
}
