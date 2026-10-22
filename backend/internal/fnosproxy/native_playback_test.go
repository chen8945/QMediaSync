package fnosproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const nativeTestSTRM = "http://private-qms:12333/115/newurl?userid=owner&pickcode=aaa&force=0"

func nativeStreamPayload(filePath, sourceURL string) map[string]any {
	return map[string]any{"code": 0, "msg": "", "data": map[string]any{
		"file_stream":           map[string]any{"guid": "file-guid", "path": filePath, "type": 1, "can_play": 1, "play_error": ""},
		"video_stream":          map[string]any{"media_guid": "file-guid", "guid": "video-guid", "codec_type": "video"},
		"cloud_storage_info":    map[string]any{"valid": true, "disabled": false, "cloud_storage_type": 9001},
		"direct_link_qualities": []any{map[string]any{"url": sourceURL, "is_m3u8": false, "resolution": "原画", "progressive": false}},
		"header":                map[string]any{"User-Agent": []string{"Native Player"}},
		"subtitles":             []any{map[string]any{"url": "/v/api/v1/subtitle/stream", "unknown": "keep"}},
		"unknown":               json.RawMessage(`{"large_number":9007199254740993}`),
	}}
}

func TestNativeStreamUsesSuccessfulNativeAuthorizationAndInternalHandoff(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "episode.strm")
	if err := os.WriteFile(file, []byte(nativeTestSTRM), 0600); err != nil {
		t.Fatal(err)
	}
	const requestBody = "{ \"media_guid\": \"file-guid\", \"unknown\": [1,2] }\n"
	var allowed atomic.Bool
	allowed.Store(true)
	var upstreamQueries, handoffs atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamQueries.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != requestBody || r.URL.Path != nativeStreamPath || r.Method != http.MethodPost ||
			r.Header.Get("Authorization") != "native-session" || r.Header.Get("Authx") != "native-signature" {
			t.Error("原生请求体、签名或路由被修改")
		}
		if _, err := r.Cookie("auth_token"); err == nil {
			t.Error("QMS Cookie 发往了飞牛")
		}
		if !allowed.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":401,"msg":"expired"}`)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("ETag", "old")
		w.Header().Set("Content-Type", "application/json")
		writer := gzip.NewWriter(w)
		_ = json.NewEncoder(writer).Encode(nativeStreamPayload(file, nativeTestSTRM))
		_ = writer.Close()
	}))
	defer upstream.Close()
	service, _, base := startService(t, upstream.URL, Options{
		OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
		MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handoffs.Add(1)
			if r.Method != http.MethodGet || r.URL.Path != "/115/newurl" || r.URL.Query().Get("force") != "0" ||
				r.UserAgent() != "Native Player" || r.Body != nil || r.ContentLength != 0 {
				t.Error("原生 POST 未正确交给既有 GET 取链入口")
			}
			for _, name := range []string{"Authorization", "Authx", "Cookie", "X-Emby-Token", "Content-Type"} {
				if r.Header.Get(name) != "" {
					t.Errorf("飞牛凭据/元数据头被发往取链层：%s", name)
				}
			}
			w.Header().Set("Location", "https://cdn.115cdn.net/video?sign=keep%2Braw")
			w.WriteHeader(http.StatusFound)
		}),
	})
	request := func() *http.Response {
		t.Helper()
		r, _ := http.NewRequest(http.MethodPost, base+nativeStreamPath, strings.NewReader(requestBody))
		r.Header.Set("Authorization", "native-session")
		r.Header.Set("Authx", "native-signature")
		r.Header.Set("User-Agent", "Native Metadata Client")
		r.Header.Set("Cookie", "auth_token=qms; csrf_token=qms")
		r.Header.Set("Accept-Encoding", "gzip")
		response, err := noRedirectClient().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	for range 2 {
		response := request()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if json.Unmarshal(body, &payload) != nil {
			t.Fatalf("原生响应不再是 JSON：HTTP=%d", response.StatusCode)
		}
		data := payload["data"].(map[string]any)
		quality := data["direct_link_qualities"].([]any)[0].(map[string]any)
		if response.StatusCode != http.StatusOK || quality["url"] != "https://cdn.115cdn.net/video?sign=keep%2Braw" ||
			quality["resolution"] != "原画" || quality["progressive"] != false || data["subtitles"] == nil ||
			!bytes.Contains(body, []byte(`9007199254740993`)) {
			t.Fatal("原生直链改写失效或修改了其他协议字段")
		}
		if response.Header.Get("Content-Encoding") != "" || response.Header.Get("ETag") != "" ||
			response.Header.Get("Cache-Control") != "no-store, private" || response.ContentLength != int64(len(body)) {
			t.Fatal("改写后没有更新响应编码、长度和缓存头")
		}
	}
	if upstreamQueries.Load() != 2 || handoffs.Load() != 2 || len(service.current.cache.entries) != 1 {
		t.Fatal("原生来源缓存绕过了当前认证或缓存了最终直链")
	}
	allowed.Store(false)
	response := request()
	if response.StatusCode != http.StatusUnauthorized || handoffs.Load() != 2 {
		t.Fatal("原生会话失效后仍使用了热来源缓存")
	}
	allowed.Store(true)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	response = request()
	_, err := io.ReadAll(response.Body)
	if err != nil || handoffs.Load() != 2 || response.Header.Get("Content-Encoding") != "gzip" {
		t.Fatal("文件删除后仍使用旧来源，或没有保留上游编码")
	}
}

func TestNativeStreamUnverifiedOrUnsupportedResponseIsUnchanged(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "episode.strm")
	if err := os.WriteFile(file, []byte(nativeTestSTRM), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(map[string]any)
	}{
		{name: "业务拒绝", change: func(p map[string]any) { p["code"] = 403 }},
		{name: "缺少成功码", change: func(p map[string]any) { delete(p, "code") }},
		{name: "不能播放", change: func(p map[string]any) { p["file_stream"].(map[string]any)["can_play"] = 0 }},
		{name: "播放错误", change: func(p map[string]any) { p["file_stream"].(map[string]any)["play_error"] = "denied" }},
		{name: "未确认文件类型", change: func(p map[string]any) { p["file_stream"].(map[string]any)["type"] = 0 }},
		{name: "媒体源不匹配", change: func(p map[string]any) { p["video_stream"].(map[string]any)["media_guid"] = "other-file" }},
		{name: "来源无效", change: func(p map[string]any) { p["cloud_storage_info"].(map[string]any)["valid"] = false }},
		{name: "来源禁用", change: func(p map[string]any) { p["cloud_storage_info"].(map[string]any)["disabled"] = true }},
		{name: "来源策略缺失", change: func(p map[string]any) { delete(p["cloud_storage_info"].(map[string]any), "disabled") }},
		{name: "未知来源类型", change: func(p map[string]any) { p["cloud_storage_info"].(map[string]any)["cloud_storage_type"] = 1 }},
		{name: "歧义播放 UA", change: func(p map[string]any) { p["header"].(map[string]any)["User-Agent"] = []string{"First", "Second"} }},
		{name: "HLS 保持原样", change: func(p map[string]any) { p["direct_link_qualities"].([]any)[0].(map[string]any)["is_m3u8"] = true }},
		{name: "未确认直链类型", change: func(p map[string]any) { delete(p["direct_link_qualities"].([]any)[0].(map[string]any), "is_m3u8") }},
		{name: "URL 与文件不符", change: func(p map[string]any) {
			p["direct_link_qualities"].([]any)[0].(map[string]any)["url"] = nativeTestSTRM + "&extra=untrusted"
		}},
		{name: "根外路径", change: func(p map[string]any) {
			p["file_stream"].(map[string]any)["path"] = filepath.Join(t.TempDir(), "private.strm")
		}},
		{name: "非 STRM", change: func(p map[string]any) { p["file_stream"].(map[string]any)["path"] = filepath.Join(root, "episode.mp4") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := nativeStreamPayload(file, nativeTestSTRM)
			if test.name == "业务拒绝" || test.name == "缺少成功码" {
				test.change(payload)
			} else {
				test.change(payload["data"].(map[string]any))
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var handoffs atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", "unchanged")
				_, _ = w.Write(encoded)
			}))
			defer upstream.Close()
			_, _, base := startService(t, upstream.URL, Options{
				OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
				KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
				MainHandler:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handoffs.Add(1) }),
			})
			response, err := noRedirectClient().Post(base+nativeStreamPath, "application/json", strings.NewReader(`{"path":"/untrusted.strm"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || !bytes.Equal(body, encoded) || response.Header.Get("ETag") != "unchanged" || handoffs.Load() != 0 {
				t.Fatal("未经核验的原生响应被改写或进入取链")
			}
		})
	}
}

func TestNativeStreamOtherResponsesPassThrough(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "episode.strm")
	if err := os.WriteFile(file, []byte("https://external.example/video?sign=unchanged%2Braw"), 0600); err != nil {
		t.Fatal(err)
	}
	external, err := json.Marshal(nativeStreamPayload(file, "https://external.example/video?sign=unchanged%2Braw"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		method   string
		path     string
		body     []byte
		encoding string
	}{
		{name: "外部 STRM", method: http.MethodPost, path: nativeStreamPath, body: external},
		{name: "SPA HTML", method: http.MethodPost, path: nativeStreamPath, body: []byte("<html>native app</html>")},
		{name: "GET 未接管", method: http.MethodGet, path: nativeStreamPath, body: external},
		{name: "其他原生接口", method: http.MethodPost, path: nativeStreamPath + "/list/file-guid", body: external},
		{name: "未知压缩", method: http.MethodPost, path: nativeStreamPath, body: external, encoding: "br"},
		{name: "过大响应", method: http.MethodPost, path: nativeStreamPath, body: bytes.Repeat([]byte(" "), metadataLimit+13)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var handoffs atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", test.encoding)
				w.Header().Set("ETag", "native-unchanged")
				_, _ = w.Write(test.body)
			}))
			defer upstream.Close()
			_, _, base := startService(t, upstream.URL, Options{
				OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
				KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
				MainHandler:  http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handoffs.Add(1) }),
			})
			request, err := http.NewRequest(test.method, base+test.path, strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			response, err := noRedirectClient().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || !bytes.Equal(body, test.body) || handoffs.Load() != 0 ||
				response.Header.Get("ETag") != "native-unchanged" || response.Header.Get("Content-Encoding") != test.encoding {
				t.Fatal("无需接管的响应没有原样透传")
			}
		})
	}
}

func TestNativeStreamResolutionFailureAndProxyRedirect(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "episode.strm")
	if err := os.WriteFile(file, []byte(nativeTestSTRM), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		status   int
		location string
		wantURL  string
	}{
		{name: "本地代理入口", status: 302, location: "/proxy-115?url=https%3A%2F%2Fcdn.115cdn.net%2Fvideo", wantURL: "/proxy-115?url=https%3A%2F%2Fcdn.115cdn.net%2Fvideo"},
		{name: "取链错误 JSON", status: 200, wantURL: nativeTestSTRM},
		{name: "取链失败", status: 500, wantURL: nativeTestSTRM},
		{name: "缺少 Location", status: 302, wantURL: nativeTestSTRM},
		{name: "任意主站路径", status: 302, location: "/api/private", wantURL: nativeTestSTRM},
		{name: "不支持的协议", status: 302, location: "file:///etc/passwd", wantURL: nativeTestSTRM},
		{name: "含凭据地址", status: 302, location: "https://user:password@cdn.example/video", wantURL: nativeTestSTRM},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(nativeStreamPayload(file, nativeTestSTRM))
			}))
			defer upstream.Close()
			_, _, base := startService(t, upstream.URL, Options{
				OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
				KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
				MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", test.location)
					w.WriteHeader(test.status)
					_, _ = io.WriteString(w, `{"error":"no link"}`)
				}),
			})
			response, err := noRedirectClient().Post(base+nativeStreamPath, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var payload map[string]any
			if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			quality := payload["data"].(map[string]any)["direct_link_qualities"].([]any)[0].(map[string]any)
			if quality["url"] != test.wantURL {
				t.Fatal("没有保留安全代理结果或错误回退")
			}
		})
	}
}

func TestNativeStreamStopCancelsInternalResolution(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "episode.strm")
	if err := os.WriteFile(file, []byte(nativeTestSTRM), 0600); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(nativeStreamPayload(file, nativeTestSTRM))
	}))
	defer upstream.Close()
	started, canceled := make(chan struct{}), make(chan struct{})
	service, _, base := startService(t, upstream.URL, Options{
		OutputRoots:  func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://private-qms:12333"}, nil },
		MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
			close(canceled)
		}),
	})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		response, err := noRedirectClient().Post(base+nativeStreamPath, "application/json", strings.NewReader(`{}`))
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("原生请求没有进入内部取链")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := service.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	for _, signal := range []<-chan struct{}{canceled, finished} {
		select {
		case <-signal:
		case <-time.After(3 * time.Second):
			t.Fatal("实例停止没有取消原生在途取链")
		}
	}
	service.current.cache.mu.Lock()
	defer service.current.cache.mu.Unlock()
	if len(service.current.cache.entries) != 0 {
		t.Fatal("停止后保留了原生来源缓存")
	}
}
