package helpers

import (
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

// TestProxyErrorsDoNotLeakCredentials 确认解析失败和结果回显都不带出代理地址里的用户名密码。
func TestProxyErrorsDoNotLeakCredentials(t *testing.T) {
	// 尾部空格会让 url.Parse 失败，原始错误里会回显整个地址
	malformed := "socks5://user:secret@127.0.0.1:1080 "

	t.Run("TestHttpProxy 解析错误", func(t *testing.T) {
		_, err := TestHttpProxy(malformed)
		if err == nil {
			t.Fatal("期望解析失败")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("错误信息泄露了密码：%v", err)
		}
	})

	t.Run("TestHttpProxyAdvanced 解析错误", func(t *testing.T) {
		result, err := TestHttpProxyAdvanced(malformed)
		if err == nil {
			t.Fatal("期望解析失败")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(result.ErrorMessage, "secret") {
			t.Fatalf("错误信息泄露了密码：err=%v message=%q", err, result.ErrorMessage)
		}
	})

	t.Run("createProxyTransport 解析错误", func(t *testing.T) {
		_, err := createProxyTransport(malformed)
		if err == nil {
			t.Fatal("期望解析失败")
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("错误信息泄露了密码：%v", err)
		}
	})

	t.Run("协议不支持时结果回显脱敏", func(t *testing.T) {
		result, err := TestHttpProxyAdvanced("socks4://user:secret@127.0.0.1:1080")
		if err == nil {
			t.Fatal("期望 socks4 被拒绝")
		}
		if strings.Contains(result.ProxyURL, "secret") {
			t.Fatalf("ProxyURL 泄露了密码：%q", result.ProxyURL)
		}
		if !strings.Contains(result.ProxyURL, "xxxxx") {
			t.Fatalf("ProxyURL 应为脱敏结果，实际为 %q", result.ProxyURL)
		}
		if strings.Contains(result.ProxyHost, "secret") {
			t.Fatalf("ProxyHost 泄露了密码：%q", result.ProxyHost)
		}
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

func TestGetProxyTransportFallback(t *testing.T) {
	transport := GetProxyTransport("socks4://127.0.0.1:1080")
	if transport == nil {
		t.Fatal("协议不受支持时应返回默认传输，实际为 nil")
	}
	if transport.Proxy != nil {
		t.Fatal("协议不受支持时不应设置 Proxy")
	}
}

func TestProxyEntrypointsRejectUnsupportedScheme(t *testing.T) {
	t.Run("TestHttpProxy", func(t *testing.T) {
		ok, err := TestHttpProxy("socks4://127.0.0.1:1080")
		if ok || err == nil {
			t.Fatalf("期望 socks4 被拒绝，实际 ok=%v err=%v", ok, err)
		}
		if !strings.Contains(err.Error(), "socks4") {
			t.Fatalf("错误信息应包含协议名，实际为：%v", err)
		}
	})

	t.Run("TestHttpProxyAdvanced", func(t *testing.T) {
		result, err := TestHttpProxyAdvanced("socks4://127.0.0.1:1080")
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
