package https

import (
	"net/http"
	"testing"
)

func TestNewHTTPClient默认启用证书校验并复用连接(t *testing.T) {
	got := newHTTPClient(ClientOptions{})
	transport, ok := got.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T，期望 *http.Transport", got.Transport)
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("默认不应跳过 TLS 证书校验")
	}
	if transport.MaxIdleConns <= 0 {
		t.Fatalf("MaxIdleConns = %d，期望启用连接复用", transport.MaxIdleConns)
	}
	if transport.MaxIdleConnsPerHost <= 0 {
		t.Fatalf("MaxIdleConnsPerHost = %d，期望启用单 host 连接复用", transport.MaxIdleConnsPerHost)
	}
	if transport.IdleConnTimeout <= 0 {
		t.Fatalf("IdleConnTimeout = %s，期望保留空闲连接", transport.IdleConnTimeout)
	}
}

func TestNewHTTPClient显式开启时才跳过证书校验(t *testing.T) {
	got := newHTTPClient(ClientOptions{InsecureSkipVerify: true})
	transport, ok := got.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型 = %T，期望 *http.Transport", got.Transport)
	}
	if transport.TLSClientConfig == nil || !transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("显式开启 InsecureSkipVerify 时应跳过 TLS 证书校验")
	}
}
