package helpers

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestURLFileName(t *testing.T) {
	for _, tt := range []struct {
		name string
		url  string
		want string
	}{
		{name: "解码中文文件名", url: "https://cdn.test/%E5%BD%B1%E7%89%87%20S01E01.mkv?t=123&k=signature", want: "影片 S01E01.mkv"},
		{name: "加号和百分号只按路径解码一次", url: "https://cdn.test/a+b%2520%25.mp4?k=a%2Bb", want: "a+b%20%.mp4"},
		{name: "转义的分隔符属于文件名", url: "https://cdn.test/videos/a%2Fb.mkv", want: "a/b.mkv"},
		{name: "不把查询参数当文件名", url: "https://cdn.test/?path=movie.mkv"},
		{name: "没有路径", url: "https://cdn.test"},
		{name: "目录路径", url: "https://cdn.test/videos/"},
		{name: "非法转义不回显原链接", url: "https://user:password@cdn.test/%zz?k=signature"},
		{name: "空地址"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := URLFileName(tt.url); got != tt.want {
				t.Fatalf("文件名 = %q，期望 %q", got, tt.want)
			}
		})
	}
}

func TestPrivateHTTPHelpersCloseConnections(t *testing.T) {
	oldLogger := AppLogger
	AppLogger = &QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { AppLogger = oldLogger })
	for _, operation := range []string{"read", "file", "progress", "post", "head"} {
		for _, scenario := range []string{"success", "error", "redirect", "redirect_error"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				closed := make(chan struct{}, 4)
				requests := make(chan *http.Request, 4)
				var server *httptest.Server
				server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests <- r.Clone(context.Background())
					if strings.HasPrefix(scenario, "redirect") && r.URL.Path == "/start" {
						w.Header().Set("Location", server.URL+"/final")
						w.WriteHeader(http.StatusFound)
					} else if strings.HasSuffix(scenario, "error") {
						w.WriteHeader(http.StatusBadGateway)
					}
					_, _ = io.WriteString(w, "content")
				}))
				server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateClosed {
						closed <- struct{}{}
					}
				}
				server.Start()
				defer server.Close()
				target := server.URL + "/start"
				filename := filepath.Join(t.TempDir(), "download")
				var err error
				var content []byte
				switch operation {
				case "read":
					content, err = ReadFromUrl(target, "test-agent")
				case "file":
					err = DownloadFile(target, filename, "test-agent")
				case "progress":
					err = DownloadFileWithProgress(context.Background(), "", target, filename, "test-agent", nil)
				case "post":
					err = PostUrl(target)
				case "head":
					_, err = TestURLConnection("", target, 2)
				}
				wantError := strings.HasSuffix(scenario, "error") && operation != "post"
				if (err != nil) != wantError {
					t.Fatalf("error = %v, want error %t", err, wantError)
				}
				if err == nil {
					if operation == "file" || operation == "progress" {
						content, err = os.ReadFile(filename)
						if err != nil {
							t.Fatal(err)
						}
					}
					if operation == "read" || operation == "file" || operation == "progress" {
						if string(content) != "content" {
							t.Fatalf("content = %q", content)
						}
					}
				}
				wantRequests := 1
				if strings.HasPrefix(scenario, "redirect") && operation != "post" {
					wantRequests = 2
				}
				for range wantRequests {
					select {
					case request := <-requests:
						if operation == "read" || operation == "file" || operation == "progress" {
							if got := request.UserAgent(); got != "test-agent" {
								t.Fatalf("UA = %q", got)
							}
						}
					default:
						t.Fatal("missing upstream request")
					}
				}
				wantConnections := 1
				if strings.HasPrefix(scenario, "redirect") && (operation == "read" || operation == "file") {
					wantConnections = 2
				}
				for range wantConnections {
					select {
					case <-closed:
					case <-time.After(2 * time.Second):
						t.Fatal("private transport retained a connection after returning")
					}
				}
			})
		}
	}
}
