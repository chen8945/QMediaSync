package emby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
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

func TestHandleImagesUpstreamParameters(t *testing.T) {
	query := "maxWidth=300&MAXHEIGHT=450&Width=200&height=400&quality=20&Quality=30&QUALITY=40" +
		"&Format=webp&AddPlayedIndicator=true" +
		"&PercentPlayed=50&UnplayedCount=3&Blur=10&BackgroundColor=black&ForegroundLayer=logo" +
		"&tag=revision-1&Index=2&api_key=token%2Btest&custom=one&custom=two"
	for _, tt := range []struct {
		name       string
		original   bool
		path       string
		processing string
	}{
		{"poster original", true, "/Items/123/Images/Primary", ""},
		{"backdrop original", true, "/Items/123/Images/Backdrop/2", "&CropWhitespace=true&EnableImageEnhancers=true"},
		{"logo default crop", true, "/Items/123/Images/Logo", ""},
		{"art default crop", true, "/Items/123/Images/Art", ""},
		{"processing explicitly disabled", true, "/Items/123/Images/Primary", "&cRoPwHiTeSpAcE=false&ENABLEIMAGEENHANCERS=false"},
		{"duplicate processing flags", true, "/Items/123/Images/Logo",
			"&CropWhitespace=false&CropWhitespace=true&cropWhitespace=true" +
				"&EnableImageEnhancers=false&EnableImageEnhancers=true&ENABLEIMAGEENHANCERS=true"},
		{"configured quality", false, "/Items/123/Images/Primary", "&CropWhitespace=true&EnableImageEnhancers=true"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := make(chan *url.URL, 1)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- r.URL
				// 模拟 Emby 缺省处理：Logo/Art 自动裁剪，增强器默认启用。
				crop := strings.HasSuffix(r.URL.Path, "/Logo") || strings.HasSuffix(r.URL.Path, "/Art")
				enhance := true
				for key, values := range r.URL.Query() {
					switch strings.ToLower(key) {
					case "cropwhitespace":
						crop = values[0] == "true"
					case "enableimageenhancers":
						enhance = values[0] == "true"
					}
				}
				if crop || enhance {
					_, _ = w.Write([]byte("processed image bytes"))
					return
				}
				_, _ = w.Write([]byte("original image bytes"))
			}))
			defer origin.Close()
			oldConfig := config.C
			config.C = &config.Config{Emby: &config.Emby{
				Host: origin.URL, ImagesQuality: 85, ImagesOriginal: tt.original,
			}}
			t.Cleanup(func() { config.C = oldConfig })
			router := gin.New()
			router.GET("/Items/:id/Images/*image", HandleImages)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tt.path+"?"+query+tt.processing, nil))
			wantBody := "original image bytes"
			if !tt.original {
				wantBody = "processed image bytes"
			}
			if response.Code != http.StatusOK || response.Body.String() != wantBody {
				t.Fatalf("image response = %d %q", response.Code, response.Body.String())
			}
			var upstream *url.URL
			select {
			case upstream = <-requests:
			default:
				t.Fatal("image request did not reach Emby")
			}
			if upstream.Path != tt.path {
				t.Fatalf("upstream path = %q, want %q", upstream.Path, tt.path)
			}
			want := url.Values{
				"tag": {"revision-1"}, "Index": {"2"}, "api_key": {"token+test"}, "custom": {"one", "two"},
				"CropWhitespace": {"false"}, "EnableImageEnhancers": {"false"},
			}
			if !tt.original {
				want, _ = url.ParseQuery(query + tt.processing)
				delete(want, "quality")
				want["Quality"] = []string{"85"}
			}
			if got := upstream.Query(); !reflect.DeepEqual(got, want) {
				t.Fatalf("upstream parameters = %v, want %v", got, want)
			}
		})
	}
}
