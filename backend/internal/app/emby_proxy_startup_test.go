package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/emby302/web"
	"qmediasync/internal/emby302/web/webport"
	"qmediasync/internal/models"

	"github.com/gin-gonic/gin"
)

func setupEmbyProxyConfig(t *testing.T, serverURL string) {
	t.Helper()
	setupFnosStartupTest(t)
	if err := db.Db.AutoMigrate(&models.EmbyConfig{}, &models.JellyfinConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&models.EmbyConfig{Enabled: true, EmbyUrl: serverURL, EmbyApiKey: "test-key"}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEmbyProxyProductGate(t *testing.T) {
	for _, scenario := range []string{"mismatch", "unknown", "ping_failure", "recovery", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			var probes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/System/Ping" {
					t.Errorf("产品检查请求了额外接口：%s", r.URL.Path)
				}
				attempt := probes.Add(1)
				switch scenario {
				case "mismatch":
					_, _ = io.WriteString(w, `"Jellyfin Server"`)
				case "unknown":
					_, _ = io.WriteString(w, "private media server")
				case "ping_failure":
					w.WriteHeader(http.StatusForbidden)
				case "recovery":
					if attempt == 1 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					fallthrough
				default:
					_, _ = io.WriteString(w, "Emby Server")
				}
			}))
			defer upstream.Close()
			setupEmbyProxyConfig(t, upstream.URL)
			if scenario == "disabled" {
				if err := db.Db.Model(&models.EmbyConfig{}).Where("id > 0").Update("enabled", false).Error; err != nil {
					t.Fatal(err)
				}
			}
			router := gin.New()
			router.Use(newEmby302Guard(upstream.URL, "test-key"))
			forwarded := 0
			router.GET("/business", func(c *gin.Context) { forwarded++; c.String(http.StatusOK, "business") })
			for range 3 {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/business", nil))
				want := http.StatusOK
				if scenario == "mismatch" || scenario == "disabled" {
					want = http.StatusServiceUnavailable
				}
				if response.Code != want {
					t.Fatalf("响应=%d %s，期待 %d", response.Code, response.Body, want)
				}
			}
			wantProbes := int32(3)
			switch scenario {
			case "disabled":
				wantProbes = 0
			case "mismatch":
				wantProbes = 1
			case "recovery":
				wantProbes = 2
			}
			if probes.Load() != wantProbes || (scenario == "mismatch" || scenario == "disabled") && forwarded != 0 {
				t.Fatalf("产品检查=%d，转发=%d", probes.Load(), forwarded)
			}
		})
	}
}

func TestEmbyProxyLateUpstreamStartup(t *testing.T) {
	// 代理监听沿用进程级生命周期，独立进程验证真实端口且不泄漏监听器。
	const childEnv = "QMS_TEST_EMBY_LATE_STARTUP"
	if os.Getenv(childEnv) != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestEmbyProxyLateUpstreamStartup$", "-test.timeout=20s")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("Emby 晚启动回归失败：%v\n%s", err, output)
		}
		return
	}
	var healthy atomic.Bool
	var probes, business atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/System/Ping" {
			probes.Add(1)
		} else {
			business.Add(1)
		}
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/System/Ping" {
			_, _ = io.WriteString(w, "Emby Server")
			return
		}
		w.Header().Set("X-Upstream", "preserved")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "original response")
	}))
	defer upstream.Close()
	setupEmbyProxyConfig(t, upstream.URL)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	webport.HTTP = strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	build.Resources = fstest.MapFS{"assets/emby302.yaml": {Data: []byte("emby:\n  host: http://unused\ncache:\n  enable: false\n")}}
	app := &App{}
	app.startEmby302()
	if app.stopEmbyPreload != nil {
		defer app.stopEmbyPreload()
	}
	admin := gin.New()
	admin.GET("/health", func(c *gin.Context) { c.String(http.StatusOK, "ready") })
	adminServer := httptest.NewServer(admin)
	defer adminServer.Close()
	response, err := http.Get(adminServer.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || probes.Load() != 0 {
		t.Fatal("本地启动不应探测或等待远端")
	}
	proxyURL := "http://127.0.0.1:" + webport.HTTP
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+webport.HTTP)
		if err == nil {
			_ = connection.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("本地代理未监听")
		case <-time.After(10 * time.Millisecond):
		}
	}
	request := func(want int) {
		t.Helper()
		response, err := http.Get(proxyURL + "/System/Info/Public")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != want {
			t.Fatalf("代理响应=%d %s，%v", response.StatusCode, body, err)
		}
		if want == http.StatusPartialContent && (string(body) != "original response" || response.Header.Get("X-Upstream") != "preserved") {
			t.Fatal("代理改变了上游响应")
		}
	}
	request(http.StatusServiceUnavailable)
	healthy.Store(true)
	request(http.StatusPartialContent)
	request(http.StatusPartialContent)
	if probes.Load() != 2 || business.Load() != 3 || web.EmbyRestartRequired(true, upstream.URL, "test-key") {
		t.Fatalf("晚启动未复用原监听/成功缓存：probe=%d business=%d", probes.Load(), business.Load())
	}
	if err := db.Db.Model(&models.EmbyConfig{}).Where("id > 0").Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	request(http.StatusServiceUnavailable)
	if business.Load() != 3 || !web.EmbyRestartRequired(false, upstream.URL, "test-key") {
		t.Fatal("关闭后仍在转发或缺少重启提示")
	}
}
