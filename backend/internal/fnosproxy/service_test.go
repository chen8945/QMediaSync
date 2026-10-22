package fnosproxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"qmediasync/internal/models"
)

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func startService(t *testing.T, upstream string, options Options) (*Service, models.FnosProxyConfig, string) {
	t.Helper()
	config := models.FnosProxyConfig{Enabled: true, FnosURL: upstream, PathMappings: []models.FnosPathMapping{}}
	options.HTTPHost = "127.0.0.1:12333"
	if options.HTTPPort == 0 {
		options.HTTPPort = freePort(t)
	}
	if options.HTTPSPort == 0 {
		options.HTTPSPort = freePort(t)
	}
	service := NewService(options)
	if err := service.Start(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Errorf("停止服务：%v", err)
		}
	})
	return service, config, "http://127.0.0.1:" + strconv.Itoa(service.options.HTTPPort)
}

func noRedirectClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestPlaybackMetadataDeadlineIsSetBeforeUpstreamResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"code":403,"msg":"denied"}`)
	}))
	defer upstream.Close()
	service, _, base := startService(t, upstream.URL, Options{})
	type observation struct {
		remaining time.Duration
		present   bool
	}
	observed := make(chan observation, 1)
	rewrite := service.current.proxy.Rewrite
	service.current.proxy.Rewrite = func(request *httputil.ProxyRequest) {
		deadline, present := request.In.Context().Deadline()
		observed <- observation{remaining: time.Until(deadline), present: present}
		rewrite(request)
	}
	for _, test := range []struct {
		name string
		path string
		verb string
		want bool
	}{
		{name: "原生播放信息", path: nativeStreamPath, verb: http.MethodPost, want: true},
		{name: "兼容 GET 播放信息", path: "/Items/movie/PlaybackInfo", verb: http.MethodGet, want: true},
		{name: "兼容 POST 播放信息", path: "/Items/movie/PlaybackInfo", verb: http.MethodPost, want: true},
		{name: "原生 GET 不接管", path: nativeStreamPath, verb: http.MethodGet},
		{name: "普通页面", path: "/index.html", verb: http.MethodGet},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequest(test.verb, base+test.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			response, err := noRedirectClient().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatal(err)
			}
			result := <-observed
			if result.present != test.want || test.want && (result.remaining < 25*time.Second || result.remaining > 30*time.Second) {
				t.Fatalf("出站前元数据截止时间错误：present=%t remaining=%v", result.present, result.remaining)
			}
		})
	}
}

func TestServiceHTTPAndCookieIsolation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/System/Info/Public" {
			t.Errorf("上游路径：%s", r.URL.Path)
		}
		if _, err := r.Cookie("auth_token"); err == nil {
			t.Error("QMS Cookie 被发往飞牛")
		}
		if cookie, err := r.Cookie("fnos_session"); err != nil || cookie.Value != "session" {
			t.Error("飞牛 Cookie 未保留")
		}
		if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Forwarded") != "" {
			t.Error("客户端转发身份未清理")
		}
		if r.Header.Get("Proxy-Authorization") != "" || r.Header.Get("X-Hop-Only") != "" {
			t.Error("逐跳头被重新发往上游")
		}
		http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: "overwrite"})
		http.SetCookie(w, &http.Cookie{Name: "csrf_token", Value: "overwrite"})
		http.SetCookie(w, &http.Cookie{Name: "fnos_session", Value: "new", Domain: "127.0.0.1", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode})
		http.SetCookie(w, &http.Cookie{Name: "foreign", Value: "no", Domain: "example.com"})
		w.Header().Set("Location", "http://"+r.Host+"/next?keep=1")
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()
	service, config, base := startService(t, upstream.URL, Options{})
	request, _ := http.NewRequest(http.MethodGet, base+"/emby/System/Info/Public", nil)
	request.Header.Set("Cookie", "auth_token=qms; csrf_token=qms; fnos_session=session")
	request.Header.Set("X-Forwarded-For", "forged")
	request.Header.Set("Forwarded", "for=forged")
	request.Header.Set("Connection", "X-Hop-Only")
	request.Header.Set("X-Hop-Only", "secret-hop")
	request.Header.Set("Proxy-Authorization", "Basic proxy-secret")
	response, err := noRedirectClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/next?keep=1" {
		t.Fatalf("同源跳转未适配：%d %q", response.StatusCode, response.Header.Get("Location"))
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "fnos_session" || cookies[0].Domain != "" || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("返回 Cookie 未正确隔离：%+v", cookies)
	}
	status := service.Status(config)
	if !status.Running || status.ListenPort != service.options.HTTPPort || status.RestartRequired {
		t.Fatalf("运行状态错误：%+v", status)
	}
	config.Enabled = false
	if !service.Status(config).RestartRequired || !service.Status(config).Running {
		t.Fatal("保存配置提前改变了运行状态")
	}
}

func TestServiceShowsRoutesPreserveQuery(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.URL.RequestURI())
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{})
	for _, test := range []struct {
		name string
		path string
		want string
	}{
		{name: "带前缀 Seasons", path: "/emby/Shows/series/Seasons", want: "/Shows/series/Seasons"},
		{name: "带前缀 Episodes", path: "/emby/Shows/series/Episodes", want: "/Shows/series/Episodes"},
		{name: "前缀大小写", path: "/EmBy/Shows/series/Episodes", want: "/Shows/series/Episodes"},
		{name: "无前缀 Seasons", path: "/Shows/series/Seasons", want: "/Shows/series/Seasons"},
		{name: "无前缀 Episodes", path: "/Shows/series/Episodes", want: "/Shows/series/Episodes"},
		{name: "未知原生路径", path: "/emby/native/Shows/series", want: "/emby/native/Shows/series"},
	} {
		t.Run(test.name, func(t *testing.T) {
			query := "UserId=user&SeasonId=season&Fields=Path%2CMediaSources&Limit=20&StartIndex=0&Unknown=a%2Bb&Unknown=c"
			response, err := noRedirectClient().Get(base + test.path + "?" + query)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK || string(body) != test.want+"?"+query {
				t.Fatalf("路由或查询串未正确透传：status=%d body=%q err=%v", response.StatusCode, body, err)
			}
		})
	}
}

func certificateFiles(t *testing.T) (string, string, *x509.CertPool) {
	t.Helper()
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	certificate := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePath, keyPath := filepath.Join(directory, "server.crt"), filepath.Join(directory, "server.key")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(server.Certificate())
	return certificatePath, keyPath, pool
}

func TestServiceHTTPSAndWebSocketShutdown(t *testing.T) {
	echo := websocket.Handler(func(conn *websocket.Conn) { _, _ = io.Copy(conn, conn) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/socket" {
			echo.ServeHTTP(w, r)
			return
		}
		_, _ = io.WriteString(w, "tls works")
	}))
	defer upstream.Close()
	certificate, key, roots := certificateFiles(t)
	service := NewService(Options{HTTPHost: "127.0.0.1:12333", HTTPSHost: "127.0.0.1:12332", CertificateFile: certificate, KeyFile: key, HTTPPort: freePort(t), HTTPSPort: freePort(t)})
	config := models.FnosProxyConfig{Enabled: true, FnosURL: upstream.URL, PathMappings: []models.FnosPathMapping{}}
	if err := service.Start(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Stop(context.Background()) })
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	base := "https://127.0.0.1:" + strconv.Itoa(service.options.HTTPSPort)
	response, err := client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "tls works" || !service.Status(config).HTTPSEnabled {
		t.Fatalf("HTTPS 未生效：%s %+v", body, service.Status(config))
	}
	var sockets []*websocket.Conn
	for _, endpoint := range []string{"ws://127.0.0.1:" + strconv.Itoa(service.options.HTTPPort), "wss://127.0.0.1:" + strconv.Itoa(service.options.HTTPSPort)} {
		wsConfig, err := websocket.NewConfig(endpoint+"/socket", "http://127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		wsConfig.TlsConfig = &tls.Config{RootCAs: roots}
		conn, err := websocket.DialConfig(wsConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := websocket.Message.Send(conn, "hello"); err != nil {
			t.Fatal(err)
		}
		var message string
		if err := websocket.Message.Receive(conn, &message); err != nil || message != "hello" {
			t.Fatalf("WebSocket 双向转发失败：%q %v", message, err)
		}
		sockets = append(sockets, conn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := service.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, conn := range sockets {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var message string
		if websocket.Message.Receive(conn, &message) == nil {
			t.Error("停止后升级连接仍可读取")
		}
	}
	if status := service.Status(config); status.Running || status.ListenPort != 0 || status.HTTPSEnabled || status.HTTPSPort != 0 {
		t.Fatalf("停止后仍报告监听：%+v", status)
	}
}

func TestServiceAutomaticHTTPSFailureKeepsHTTP(t *testing.T) {
	certificate, key, _ := certificateFiles(t)
	invalidCertificate := filepath.Join(t.TempDir(), "invalid.crt")
	if err := os.WriteFile(invalidCertificate, []byte("invalid certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	cases := []struct {
		name, certificate, key string
		httpsPort              int
		warning                bool
	}{
		{name: "没有证书", certificate: filepath.Join(t.TempDir(), "missing"), key: key, httpsPort: freePort(t)},
		{name: "没有私钥", certificate: certificate, key: filepath.Join(t.TempDir(), "missing"), httpsPort: freePort(t)},
		{name: "证书损坏", certificate: invalidCertificate, key: key, httpsPort: freePort(t), warning: true},
		{name: "HTTPS端口被占用", certificate: certificate, key: key, httpsPort: occupied.Addr().(*net.TCPAddr).Port, warning: true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "http available") }))
			defer upstream.Close()
			service := NewService(Options{HTTPHost: "127.0.0.1:12333", HTTPSHost: "127.0.0.1:12332", HTTPPort: freePort(t), HTTPSPort: test.httpsPort, CertificateFile: test.certificate, KeyFile: test.key})
			config := models.FnosProxyConfig{Enabled: true, FnosURL: upstream.URL}
			err := service.Start(config)
			t.Cleanup(func() { _ = service.Stop(context.Background()) })
			if (err != nil) != test.warning {
				t.Fatalf("HTTPS启动结果：%v", err)
			}
			status := service.Status(config)
			if !status.Running || status.ListenPort != service.options.HTTPPort || status.HTTPSEnabled || status.HTTPSPort != 0 || (status.LastError != "") != test.warning || strings.Contains(status.LastError, test.certificate) {
				t.Fatalf("HTTP独立运行状态错误：%+v", status)
			}
			response, err := noRedirectClient().Get("http://127.0.0.1:" + strconv.Itoa(service.options.HTTPPort) + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if string(body) != "http available" {
				t.Fatal("HTTPS失败导致HTTP不可用")
			}
		})
	}
}

func TestServiceHTTPFailureDoesNotStartHTTPS(t *testing.T) {
	certificate, key, _ := certificateFiles(t)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	httpsPort := freePort(t)
	service := NewService(Options{HTTPHost: "127.0.0.1:12333", HTTPSHost: "127.0.0.1:12332", HTTPPort: occupied.Addr().(*net.TCPAddr).Port, HTTPSPort: httpsPort, CertificateFile: certificate, KeyFile: key})
	config := models.FnosProxyConfig{Enabled: true, FnosURL: "http://127.0.0.1:8005"}
	if service.Start(config) == nil {
		t.Fatal("预期HTTP绑定失败")
	}
	status := service.Status(config)
	if status.Running || status.ListenPort != 0 || status.HTTPSEnabled || status.HTTPSPort != 0 || status.LastError == "" || !status.RestartRequired {
		t.Fatalf("HTTP失败状态：%+v", status)
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(httpsPort)))
	if err != nil {
		t.Fatal("HTTP失败仍绑定了HTTPS")
	}
	_ = listener.Close()
}

func TestServiceDisabledDoesNotBind(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	service := NewService(Options{HTTPPort: occupied.Addr().(*net.TCPAddr).Port})
	config := models.FnosProxyConfig{}
	if err := service.Start(config); err != nil || service.Status(config).Running {
		t.Fatalf("关闭状态启动监听：%v %+v", err, service.Status(config))
	}
}

func TestServiceUsesDirectTransportAndVerifiesUpstreamTLS(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:9999")
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:9999")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.IsAbs() {
			t.Error("请求经过了系统 HTTP 代理")
		}
		_, _ = io.WriteString(w, "direct")
	}))
	defer upstream.Close()
	service, _, base := startService(t, "http://fnos.invalid:8005", Options{})
	dialer := &net.Dialer{Timeout: time.Second}
	service.current.transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "fnos.invalid:8005" {
			t.Errorf("连接目标被系统代理替换：%s", address)
		}
		return dialer.DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	response, err := noRedirectClient().Get(base + "/System/Info/Public")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "direct" {
		t.Fatalf("未直接连接飞牛：%s", body)
	}
	untrusted := httptest.NewTLSServer(http.NotFoundHandler())
	defer untrusted.Close()
	_, _, tlsBase := startService(t, untrusted.URL, Options{})
	response, err = noRedirectClient().Get(tlsBase + "/System/Info/Public")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatal("上游不可信证书被接受")
	}
}
