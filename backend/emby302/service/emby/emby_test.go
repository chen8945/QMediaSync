package emby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/websocket"

	"qmediasync/emby302/config"
)

func TestProxySocketBypassesDefaultProxy(t *testing.T) {
	var proxyRequests atomic.Int32
	systemProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyRequests.Add(1)
		http.Error(w, "unexpected system proxy", http.StatusBadGateway)
	}))
	defer systemProxy.Close()
	proxyURL, err := url.Parse(systemProxy.URL)
	if err != nil {
		t.Fatal(err)
	}

	oldTransport := http.DefaultTransport
	transport := oldTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(proxyURL)
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = oldTransport
		transport.CloseIdleConnections()
	})

	requests := make(chan string, 1)
	origin := httptest.NewServer(websocket.Handler(func(conn *websocket.Conn) {
		requests <- conn.Request().RequestURI
		defer conn.Close()
		var message string
		if err := websocket.Message.Receive(conn, &message); err == nil {
			_ = websocket.Message.Send(conn, message)
		}
	}))
	defer origin.Close()
	oldConfig := config.C
	config.C = &config.Config{Emby: &config.Emby{Host: origin.URL}}
	t.Cleanup(func() { config.C = oldConfig })
	router := gin.New()
	router.GET("/embywebsocket", ProxySocket())
	server := httptest.NewServer(router)
	defer server.Close()

	wsConfig, err := websocket.NewConfig("ws"+strings.TrimPrefix(server.URL, "http")+"/embywebsocket?api_key=test", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := wsConfig.DialContext(ctx)
	if err != nil {
		t.Fatalf("WebSocket handshake: %v (system proxy requests: %d)", err, proxyRequests.Load())
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := websocket.Message.Send(conn, "ping"); err != nil {
		t.Fatal(err)
	}
	var message string
	if err := websocket.Message.Receive(conn, &message); err != nil || message != "ping" {
		t.Fatalf("WebSocket echo = %q, %v", message, err)
	}
	if got := <-requests; got != "/embywebsocket?api_key=test" {
		t.Fatalf("upstream URI = %q", got)
	}
	if got := proxyRequests.Load(); got != 0 {
		t.Fatalf("system proxy requests = %d, want 0", got)
	}
	if transport.Proxy == nil {
		t.Fatal("WebSocket proxy changed the default transport")
	}
}
