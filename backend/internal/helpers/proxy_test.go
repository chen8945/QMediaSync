package helpers

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/validation"
)

// TestCheckProxyScheme 锁定传输层协议校验。
// 只覆盖小写形态：协议一律来自 url.Parse，它会把 scheme 统一转成小写，
// 所以 SOCKS5:// 这类写法在真实链路上是被接受的，不存在"大写被拒"的行为。
func TestCheckProxyScheme(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		wantErr bool
	}{
		{name: "http 通过", scheme: "http"},
		{name: "https 通过", scheme: "https"},
		{name: "socks5 通过", scheme: "socks5"},
		{name: "socks5h 通过", scheme: "socks5h"},
		{name: "socks4 失败", scheme: "socks4", wantErr: true},
		{name: "空协议失败", scheme: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkProxyScheme(tt.scheme)
			if tt.wantErr && err == nil {
				t.Fatalf("期望协议 %q 校验失败，实际通过", tt.scheme)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("期望协议 %q 校验通过，实际报错：%v", tt.scheme, err)
			}
		})
	}
}

// TestCheckProxySchemeSharesWhitelist 确认传输层与请求校验层用的是同一份白名单和同一套文案。
func TestCheckProxySchemeSharesWhitelist(t *testing.T) {
	err := checkProxyScheme("socks4")
	if err == nil {
		t.Fatal("期望 socks4 被拒绝")
	}
	if !strings.Contains(err.Error(), validation.ProxySchemeHint) {
		t.Fatalf("错误信息应复用 validation.ProxySchemeHint，实际为：%v", err)
	}
	if validation.ProxySchemeSupported("socks4") {
		t.Fatal("validation 与 helpers 白名单不一致")
	}
}

// credentialProxyURL 合法且能通过请求校验层的带凭据代理地址。
// 控制器会先 NormalizedHTTPProxy() 再 validation.ProxyURL，畸形地址走不到 helpers，
// 真实的泄露路径是这种合法地址一路流进日志和接口回显。
const credentialProxyURL = "socks5://user:secret@127.0.0.1:1080"

// captureAppLogger 把 AppLogger 换成写入 buffer 的实例，返回缓冲区，测试结束后恢复全局状态。
func captureAppLogger(t *testing.T) *bytes.Buffer {
	t.Helper()

	oldAppLogger := AppLogger
	t.Cleanup(func() {
		AppLogger = oldAppLogger
	})

	var buf bytes.Buffer
	AppLogger = &QLogger{Logger: log.New(&buf, "", 0)}
	return &buf
}

// assertNoProxyCredentials 断言文本既不含密码也不含用户名。
// 用户名同样敏感：企业代理常带 AD 域账号，而 url.URL.Redacted() 只遮蔽密码。
func assertNoProxyCredentials(t *testing.T, label string, text string) {
	t.Helper()

	if strings.Contains(text, "secret") {
		t.Fatalf("%s 泄露了密码：%s", label, text)
	}
	if strings.Contains(text, "user:") || strings.Contains(text, "//user@") {
		t.Fatalf("%s 泄露了用户名：%s", label, text)
	}
}

// TestProxyLogsDoNotLeakCredentials 覆盖成功路径的脱敏：合法带凭据地址流入日志时，
// 日志里不能出现用户名和密码。只断言返回值不够，把脱敏改回原始地址也能全绿。
func TestProxyLogsDoNotLeakCredentials(t *testing.T) {
	useTestLogLevel(t, LogLevelInfo)

	t.Run("TestHttpProxy 探测日志", func(t *testing.T) {
		buf := captureAppLogger(t)

		// 127.0.0.1:1080 上没有代理，探测必然失败；这里只关心失败前写出的日志
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := TestHttpProxyWithContext(ctx, credentialProxyURL); err == nil {
			t.Fatal("期望连接本地不存在的代理失败")
		} else {
			assertNoProxyCredentials(t, "TestHttpProxy 返回错误", err.Error())
		}

		got := buf.String()
		if !strings.Contains(got, "使用代理") {
			t.Fatalf("期望写出代理探测日志，实际为：%s", got)
		}
		assertNoProxyCredentials(t, "TestHttpProxy 日志", got)
		if !strings.Contains(got, "xxxxx") {
			t.Fatalf("日志中的代理地址应为脱敏结果，实际为：%s", got)
		}
	})

	t.Run("DownloadFileWithProgress 代理日志", func(t *testing.T) {
		buf := captureAppLogger(t)

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// 下载必然失败，断言只针对配置代理时写出的那条日志
		_ = DownloadFileWithProgress(ctx, credentialProxyURL, "http://127.0.0.1:1/file.bin",
			filepath.Join(t.TempDir(), "file.bin"), "qmediasync-test", nil)

		got := buf.String()
		if !strings.Contains(got, "使用代理") {
			t.Fatalf("期望写出使用代理日志，实际为：%s", got)
		}
		assertNoProxyCredentials(t, "DownloadFileWithProgress 日志", got)
	})

	t.Run("TestURLConnection 代理日志", func(t *testing.T) {
		buf := captureAppLogger(t)

		if ok, err := TestURLConnection(credentialProxyURL, "http://127.0.0.1:1/", 2); ok || err == nil {
			t.Fatalf("期望经不存在的代理连接失败，实际 ok=%v err=%v", ok, err)
		}

		got := buf.String()
		if !strings.Contains(got, "使用代理") {
			t.Fatalf("期望写出使用代理日志，实际为：%s", got)
		}
		assertNoProxyCredentials(t, "TestURLConnection 日志", got)
	})

	t.Run("GetProxyTransport 失败日志", func(t *testing.T) {
		buf := captureAppLogger(t)

		if _, err := GetProxyTransport("socks4://user:secret@127.0.0.1:1080"); err == nil {
			t.Fatal("期望 socks4 报错")
		}

		got := buf.String()
		if !strings.Contains(got, "创建代理传输失败") {
			t.Fatalf("期望写出创建失败日志，实际为：%s", got)
		}
		assertNoProxyCredentials(t, "GetProxyTransport 日志", got)
	})
}

// TestProxyErrorsDoNotLeakCredentials 确认解析失败和结果回显都不带出代理地址里的用户名密码。
func TestProxyErrorsDoNotLeakCredentials(t *testing.T) {
	// 尾部空格会让 url.Parse 失败，原始错误里会回显整个地址。
	// 控制器侧已被 NormalizedHTTPProxy 和 validation.ProxyURL 挡住，这里只兜底防御直接调用。
	malformed := credentialProxyURL + " "

	t.Run("TestHttpProxy 解析错误", func(t *testing.T) {
		_, err := TestHttpProxyWithContext(context.Background(), malformed)
		if err == nil {
			t.Fatal("期望解析失败")
		}
		assertNoProxyCredentials(t, "错误信息", err.Error())
	})

	t.Run("TestHttpProxyAdvanced 解析错误", func(t *testing.T) {
		result, err := TestHttpProxyAdvancedWithContext(context.Background(), malformed)
		if err == nil {
			t.Fatal("期望解析失败")
		}
		assertNoProxyCredentials(t, "错误信息", err.Error())
		assertNoProxyCredentials(t, "结果错误信息", result.ErrorMessage)
	})

	t.Run("createProxyTransport 解析错误", func(t *testing.T) {
		_, err := createProxyTransport(malformed)
		if err == nil {
			t.Fatal("期望解析失败")
		}
		assertNoProxyCredentials(t, "错误信息", err.Error())
	})

	t.Run("协议不支持时结果回显脱敏", func(t *testing.T) {
		result, err := TestHttpProxyAdvancedWithContext(context.Background(), "socks4://user:secret@127.0.0.1:1080")
		if err == nil {
			t.Fatal("期望 socks4 被拒绝")
		}
		assertNoProxyCredentials(t, "ProxyURL", result.ProxyURL)
		if !strings.Contains(result.ProxyURL, "xxxxx") {
			t.Fatalf("ProxyURL 应为脱敏结果，实际为 %q", result.ProxyURL)
		}
		assertNoProxyCredentials(t, "ProxyHost", result.ProxyHost)
	})
}

func TestCreateProxyTransport(t *testing.T) {
	t.Run("空代理返回直连传输", func(t *testing.T) {
		transport, err := createProxyTransport("")
		if err != nil {
			t.Fatalf("空代理不应报错，实际报错：%v", err)
		}
		if transport.Proxy != nil {
			t.Fatal("空代理时不应设置 Proxy")
		}
	})

	for _, proxyURL := range []string{"http://127.0.0.1:7890", "socks5://127.0.0.1:1080", "socks5h://127.0.0.1:1080"} {
		t.Run("受支持协议 "+proxyURL, func(t *testing.T) {
			transport, err := createProxyTransport(proxyURL)
			if err != nil {
				t.Fatalf("期望 %s 创建成功，实际报错：%v", proxyURL, err)
			}
			if transport.Proxy == nil {
				t.Fatalf("期望 %s 设置 Proxy，实际为 nil", proxyURL)
			}
			proxy, err := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://example.invalid", nil))
			if err != nil {
				t.Fatalf("解析代理失败：%v", err)
			}
			if proxy == nil || proxy.String() != proxyURL {
				t.Fatalf("期望代理为 %s，实际为 %v", proxyURL, proxy)
			}
		})
	}

	t.Run("不支持协议报错", func(t *testing.T) {
		if _, err := createProxyTransport("socks4://127.0.0.1:1080"); err == nil {
			t.Fatal("期望 socks4 报错，实际通过")
		}
	})
}

// TestCreateProxyTransportSharesValidationRules 确认传输层与请求校验层用同一套规则，
// 而不是只查协议白名单。端口越界曾能穿过传输层，直到真正出站才失败。
func TestCreateProxyTransportSharesValidationRules(t *testing.T) {
	rejected := []string{
		"socks5://127.0.0.1:99999", // 端口越界
		"http://127.0.0.1:0",       // 端口为 0
		"127.0.0.1:1080",           // 缺少协议
	}
	for _, proxyURL := range rejected {
		t.Run("拒绝 "+proxyURL, func(t *testing.T) {
			_, err := createProxyTransport(proxyURL)
			if err == nil {
				t.Fatalf("期望 %q 被拒绝，实际通过", proxyURL)
			}
			// 与请求校验层同源：同一个地址在两层的结论必须一致
			if validation.ProxyURL("代理地址", proxyURL, false) == nil {
				t.Fatalf("validation 层放行了 %q，两层规则已漂移", proxyURL)
			}
		})
	}

	// 省略主机名的本地代理简写是既定契约：Go 会把空 host 当作 localhost，
	// 而本应用的占位符本身就是 127.0.0.1:7890 这类本地地址。
	for _, proxyURL := range []string{"http://:7890", "socks5://:1080"} {
		t.Run("放行省略主机名的 "+proxyURL, func(t *testing.T) {
			transport, err := createProxyTransport(proxyURL)
			if err != nil {
				t.Fatalf("期望放行 %q，实际报错：%v", proxyURL, err)
			}
			if transport.Proxy == nil {
				t.Fatalf("期望 %q 设置 Proxy，实际为 nil", proxyURL)
			}
		})
	}
}

// TestGetProxyTransportDoesNotFallBackToDirect 锁定不再 fail-open。
// 旧行为在代理无效时返回裸 &http.Transport{}：Proxy 为 nil 且不走 ProxyFromEnvironment，
// 配了代理的用户会静默直连出网，对靠代理规避 ISP 或地域暴露的用户是隐私问题。
func TestGetProxyTransportDoesNotFallBackToDirect(t *testing.T) {
	for _, proxyURL := range []string{
		"socks4://127.0.0.1:1080",  // 协议不受支持
		"socks5://127.0.0.1:99999", // 端口越界
		"socks5://127.0.0.1:1080 ", // 尾随空白导致解析失败
	} {
		t.Run("无效代理不退化直连 "+proxyURL, func(t *testing.T) {
			captureAppLogger(t)

			transport, err := GetProxyTransport(proxyURL)
			if err == nil {
				t.Fatalf("期望 %q 报错，实际通过", proxyURL)
			}
			if transport != nil {
				t.Fatalf("报错时不应返回可用传输，实际为 %+v", transport)
			}
		})
	}

	t.Run("空代理返回直连传输", func(t *testing.T) {
		transport, err := GetProxyTransport("")
		if err != nil {
			t.Fatalf("空代理不应报错，实际报错：%v", err)
		}
		if transport == nil {
			t.Fatal("空代理应返回直连传输，实际为 nil")
		}
		if transport.Proxy != nil {
			t.Fatal("空代理时不应设置 Proxy")
		}
		// 直连分支必须保留超时与连接池设置，旧的 fail-open 分支把它们全丢了
		if transport.TLSHandshakeTimeout == 0 || transport.ResponseHeaderTimeout == 0 ||
			transport.ExpectContinueTimeout == 0 || transport.IdleConnTimeout == 0 ||
			transport.MaxIdleConns == 0 || transport.DialContext == nil {
			t.Fatalf("直连传输缺少超时或连接池设置：%+v", transport)
		}
	})

	t.Run("有效代理返回代理传输", func(t *testing.T) {
		transport, err := GetProxyTransport("socks5://127.0.0.1:1080")
		if err != nil {
			t.Fatalf("有效代理不应报错，实际报错：%v", err)
		}
		if transport.Proxy == nil {
			t.Fatal("有效代理应设置 Proxy")
		}
	})
}

// TestHttpProxyAdvancedStopsAtFirstSuccess 锁定短路行为。
// 旧实现无条件跑完 5 个 URL，每个 30 秒超时，最坏要等两分半。
// 用假的 HTTP 代理直接对第一个明文 URL 回 200，无需外网即可验证只探测了一次。
func TestHttpProxyAdvancedStopsAtFirstSuccess(t *testing.T) {
	useTestLogLevel(t, LogLevelInfo)
	captureAppLogger(t)

	var proxied int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&proxied, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(proxy.Close)

	result, err := TestHttpProxyAdvancedWithContext(context.Background(), proxy.URL)
	if err != nil {
		t.Fatalf("经本地代理测试不应报错，实际报错：%v", err)
	}
	if !result.Success {
		t.Fatalf("期望判定代理可用，实际为 %+v", result)
	}
	if len(result.TestResults) != 1 {
		t.Fatalf("首个 URL 成功后应停止探测，实际探测了 %d 个", len(result.TestResults))
	}
	if result.SuccessCount != 1 || result.TotalCount != 1 {
		t.Fatalf("统计应只覆盖实际探测过的 URL，实际 SuccessCount=%d TotalCount=%d", result.SuccessCount, result.TotalCount)
	}
	if got := atomic.LoadInt32(&proxied); got != 1 {
		t.Fatalf("期望只向代理发起 1 次请求，实际 %d 次", got)
	}
}

// TestProxyTestsHonorContextCancellation 确认 context 透传生效：已取消的 ctx 必须立即返回，
// 而不是逐个 URL 等满超时。
func TestProxyTestsHonorContextCancellation(t *testing.T) {
	useTestLogLevel(t, LogLevelInfo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("TestHttpProxyWithContext", func(t *testing.T) {
		captureAppLogger(t)

		start := time.Now()
		ok, err := TestHttpProxyWithContext(ctx, "socks5://127.0.0.1:1080")
		if ok || err == nil {
			t.Fatalf("期望取消后失败，实际 ok=%v err=%v", ok, err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("取消后应立即返回，实际耗时 %v", elapsed)
		}
	})

	t.Run("TestHttpProxyAdvancedWithContext", func(t *testing.T) {
		captureAppLogger(t)

		start := time.Now()
		result, err := TestHttpProxyAdvancedWithContext(ctx, "socks5://127.0.0.1:1080")
		if err == nil {
			t.Fatal("期望取消后返回错误")
		}
		if result.Success {
			t.Fatalf("取消后不应判定代理可用，实际为 %+v", result)
		}
		if result.TotalCount != len(result.TestResults) {
			t.Fatalf("TotalCount 应等于实际探测数，实际 %d 与 %d", result.TotalCount, len(result.TestResults))
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("取消后应立即返回，实际耗时 %v", elapsed)
		}
	})
}

func TestProxyEntrypointsRejectUnsupportedScheme(t *testing.T) {
	t.Run("TestHttpProxy", func(t *testing.T) {
		ok, err := TestHttpProxyWithContext(context.Background(), "socks4://127.0.0.1:1080")
		if ok || err == nil {
			t.Fatalf("期望 socks4 被拒绝，实际 ok=%v err=%v", ok, err)
		}
		if !strings.Contains(err.Error(), "socks4") {
			t.Fatalf("错误信息应包含协议名，实际为：%v", err)
		}
	})

	t.Run("TestHttpProxyAdvanced", func(t *testing.T) {
		result, err := TestHttpProxyAdvancedWithContext(context.Background(), "socks4://127.0.0.1:1080")
		if err == nil {
			t.Fatal("期望 socks4 被拒绝，实际通过")
		}
		if result == nil || result.Success {
			t.Fatalf("期望返回失败结果，实际为 %+v", result)
		}
		if result.ErrorMessage != err.Error() {
			t.Fatalf("结果错误信息应与返回错误一致，实际为 %q 与 %q", result.ErrorMessage, err.Error())
		}
	})
}

// TestCreateProxyTransportOverSocks5 用本地 SOCKS5 服务验证 socks5 不需要额外依赖即可拨号。
func TestCreateProxyTransportOverSocks5(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("via-socks5"))
	}))
	t.Cleanup(target.Close)

	for _, scheme := range []string{"socks5", "socks5h"} {
		t.Run(scheme, func(t *testing.T) {
			proxyAddr := startSocks5Server(t)
			transport, err := createProxyTransport(scheme + "://" + proxyAddr)
			if err != nil {
				t.Fatalf("创建 %s 传输失败：%v", scheme, err)
			}
			t.Cleanup(transport.CloseIdleConnections)

			client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
			resp, err := client.Get(target.URL)
			if err != nil {
				t.Fatalf("经 %s 代理请求失败：%v", scheme, err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("读取响应失败：%v", err)
			}
			if resp.StatusCode != http.StatusOK || string(body) != "via-socks5" {
				t.Fatalf("期望经代理拿到 200 via-socks5，实际为 %d %q", resp.StatusCode, body)
			}
		})
	}
}

// startSocks5Server 启动仅支持 CONNECT 且无需认证的最小 SOCKS5 服务，返回监听地址。
func startSocks5Server(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动 SOCKS5 监听失败：%v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSocks5Conn(conn)
		}
	}()

	return listener.Addr().String()
}

func serveSocks5Conn(client net.Conn) {
	defer client.Close()

	// 协商阶段：VER、NMETHODS、METHODS
	header := make([]byte, 2)
	if _, err := io.ReadFull(client, header); err != nil || header[0] != 0x05 {
		return
	}
	if _, err := io.ReadFull(client, make([]byte, int(header[1]))); err != nil {
		return
	}
	if _, err := client.Write([]byte{0x05, 0x00}); err != nil { // 选择无认证
		return
	}

	// 请求阶段：VER、CMD、RSV、ATYP
	request := make([]byte, 4)
	if _, err := io.ReadFull(client, request); err != nil || request[1] != 0x01 {
		return
	}

	var host string
	switch request[3] {
	case 0x01: // IPv4
		addr := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(client, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	case 0x03: // 域名
		size := make([]byte, 1)
		if _, err := io.ReadFull(client, size); err != nil {
			return
		}
		name := make([]byte, int(size[0]))
		if _, err := io.ReadFull(client, name); err != nil {
			return
		}
		host = string(name)
	case 0x04: // IPv6
		addr := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(client, addr); err != nil {
			return
		}
		host = net.IP(addr).String()
	default:
		return
	}

	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(client, portBytes); err != nil {
		return
	}

	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))), 5*time.Second)
	if err != nil {
		_, _ = client.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0}) // 一般性失败
		return
	}
	defer upstream.Close()

	if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil { // 成功
		return
	}

	go func() { _, _ = io.Copy(upstream, client) }()
	_, _ = io.Copy(client, upstream)
}
