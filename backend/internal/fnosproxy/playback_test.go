package fnosproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPlaybackAuthorizationCacheAndInternalHandoff(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	strm := "http://unreachable.qms.internal:12333/115/newurl?pickcode=aaa&userid=account&force=0"
	if err := os.WriteFile(file, []byte(strm), 0600); err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Bool
	allowed.Store(true)
	var sourceQueries, handoffs atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		valid := strings.Contains(r.Header.Get("X-Emby-Authorization"), `Token="valid"`)
		switch r.URL.Path {
		case "/Users/Me":
			if !valid {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/movie":
			sourceQueries.Add(1)
			if !valid || !allowed.Load() {
				http.Error(w, "restricted library", http.StatusForbidden)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "Path": file, "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
		case "/Items/movie/PlaybackInfo":
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Type", "application/json")
			writer := gzip.NewWriter(w)
			_ = json.NewEncoder(writer).Encode(map[string]any{"Keep": "unknown", "MediaSources": []any{
				map[string]any{"Id": "source", "Path": file, "TranscodingUrl": "/transcode", "KeepSource": "unknown"},
				map[string]any{"Id": "other", "Path": "/not-readable.strm", "TranscodingUrl": "/keep"}}})
			_ = writer.Close()
		default:
			if (!valid && r.URL.Query().Get("api_key") != "valid") || !allowed.Load() {
				http.Error(w, "upstream auth result", http.StatusForbidden)
				return
			}
			_, _ = io.WriteString(w, "upstream fallback")
		}
	}))
	defer upstream.Close()
	options := Options{
		OutputRoots: func(ctx context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(ctx context.Context) ([]string, error) {
			return []string{"http://unreachable.qms.internal:12333"}, nil
		},
		MainHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handoffs.Add(1)
			if r.Method != http.MethodGet || r.URL.Path != "/115/newurl" || r.URL.Query().Get("force") != "0" ||
				r.URL.Query().Get("api_key") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" ||
				r.Header.Get("X-Emby-Authorization") != "" || r.UserAgent() != "Real Player" || r.Header.Get("Range") != "bytes=7-" {
				t.Errorf("内部转交未保留必要字段或泄露身份：method=%s path=%s range=%s", r.Method, r.URL.Path, r.Header.Get("Range"))
			}
			w.Header().Set("Location", "https://cdn.example/video")
			w.WriteHeader(http.StatusFound)
		}),
	}
	service, _, base := startService(t, upstream.URL, options)
	client := noRedirectClient()
	request := func(method, token string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, base+"/emby/Videos/movie/stream?MediaSourceId=source&api_key="+token, nil)
		req.Header.Set("User-Agent", "Real Player")
		req.Header.Set("Range", "bytes=7-")
		req.Header.Set("Cookie", "auth_token=qms; csrf_token=qms")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = response.Body.Close() })
		return response
	}
	for _, method := range []string{http.MethodGet, http.MethodGet, http.MethodHead} {
		response := request(method, "valid")
		if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "https://cdn.example/video" {
			t.Fatalf("内部转交失败：%d", response.StatusCode)
		}
		body, _ := io.ReadAll(response.Body)
		if method == http.MethodHead && len(body) != 0 {
			t.Fatal("HEAD 返回了视频内容")
		}
	}
	if sourceQueries.Load() != 3 || handoffs.Load() != 3 || len(service.current.cache.entries) != 1 {
		t.Fatalf("缓存跳过授权或未复用来源：queries=%d handoffs=%d cache=%d", sourceQueries.Load(), handoffs.Load(), len(service.current.cache.entries))
	}
	allowed.Store(false)
	if response := request(http.MethodGet, "valid"); response.StatusCode != http.StatusForbidden || handoffs.Load() != 3 {
		t.Fatal("撤销权限后仍使用来源缓存")
	}
	if response := request(http.MethodGet, "invalid"); response.StatusCode != http.StatusForbidden || handoffs.Load() != 3 {
		t.Fatal("失效凭据仍使用来源缓存")
	}
	allowed.Store(true)
	infoRequest, _ := http.NewRequest(http.MethodPost, base+"/emby/Items/movie/PlaybackInfo?api_key=valid", strings.NewReader(`{"UserId":"user"}`))
	infoRequest.Header.Set("Accept-Encoding", "gzip")
	infoResponse, err := client.Do(infoRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer infoResponse.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(infoResponse.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	sources := payload["MediaSources"].([]any)
	first, other := sources[0].(map[string]any), sources[1].(map[string]any)
	if first["SupportsDirectStream"] != true || first["TranscodingUrl"] != nil || first["KeepSource"] != "unknown" || other["TranscodingUrl"] != "/keep" || payload["Keep"] != "unknown" {
		t.Fatalf("PlaybackInfo 改写范围错误：%v", payload)
	}
	streamURL, err := url.Parse(first["DirectStreamUrl"].(string))
	if err != nil || streamURL.IsAbs() || streamURL.Query().Get("MediaSourceId") != "source" || streamURL.Query().Get("api_key") != "valid" || infoResponse.Header.Get("Content-Encoding") != "" {
		t.Fatal("改写未绑定准确来源或未更新编码头")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	response := request(http.MethodGet, "valid")
	body, _ := io.ReadAll(response.Body)
	if string(body) != "upstream fallback" || handoffs.Load() != 3 {
		t.Fatal("STRM 删除后仍转交旧URL")
	}
}

func TestPlaybackInfoPreservesExplicitTranscoding(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(file, []byte("https://cdn.example/video"), 0600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"MediaSources": []any{map[string]any{
		"Id": "source", "Path": file, "SupportsTranscoding": true,
		"TranscodingUrl": "/transcode", "TranscodingSubProtocol": "hls", "TranscodingContainer": "ts",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var lookups atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Items/movie/PlaybackInfo":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", "upstream")
			_, _ = w.Write(payload)
		case "/Videos/movie/stream":
			w.Header().Set("X-Upstream-Stream", "true")
			_, _ = io.WriteString(w, "upstream stream")
		case "/Users/Me":
			lookups.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/movie":
			lookups.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
		default:
			t.Error("意外的上游请求")
		}
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil }})
	disabledQueries := []string{
		"PlayMethod=Transcode", "pLaYmEtHoD=tRaNsCoDe", "static=false", "Static=FALSE",
		"static=true&static=false", "static=true&Static=false",
		"PlayMethod=DirectPlay&PlayMethod=Transcode", "PlayMethod=DirectPlay&playmethod=Transcode",
		"EnableDirectPlay=false&EnableDirectStream=false", "EnableDirectPlay=false", "EnableDirectStream=false",
		"enabledirectplay=FALSE&ENABLEDIRECTSTREAM=FALSE",
		"EnableDirectPlay=true&EnableDirectPlay=false", "EnableDirectStream=true&enabledirectstream=false",
		"EnableDirectPlay=true&EnableDirectPlay=true", "EnableDirectStream=true&enabledirectstream=true",
		"EnableDirectPlay=", "EnableDirectStream=", "EnableDirectPlay=null", "EnableDirectStream=1",
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		for _, query := range disabledQueries {
			t.Run(method+query, func(t *testing.T) {
				before := lookups.Load()
				req, _ := http.NewRequest(method, base+"/Items/movie/PlaybackInfo?api_key=valid&"+query, strings.NewReader(`{"UserId":"user"}`))
				response, err := noRedirectClient().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(body, payload) || response.Header.Get("ETag") != "upstream" {
					t.Fatal("显式转码或歧义请求的 PlaybackInfo 被改写")
				}
				if lookups.Load() != before {
					t.Fatal("回退前执行了不必要的来源查询")
				}
			})
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, query := range disabledQueries {
			t.Run("stream/"+method+query, func(t *testing.T) {
				before := lookups.Load()
				req, _ := http.NewRequest(method, base+"/Videos/movie/stream?api_key=valid&"+query, nil)
				response, err := noRedirectClient().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK || response.Header.Get("X-Upstream-Stream") != "true" || lookups.Load() != before {
					t.Fatal("显式禁止或歧义取流请求仍被接管")
				}
			})
		}
	}
	for _, query := range []string{"", "static=true", "PlayMethod=DirectPlay", "EnableDirectPlay=true", "EnableDirectStream=true", "enabledirectplay=TRUE&ENABLEDIRECTSTREAM=TRUE"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			t.Run("allowed/"+method+query, func(t *testing.T) {
				req, _ := http.NewRequest(method, base+"/Items/movie/PlaybackInfo?api_key=valid&"+query, strings.NewReader(`{"UserId":"user"}`))
				response, err := noRedirectClient().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				var result map[string]any
				if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
					t.Fatal(err)
				}
				source := result["MediaSources"].([]any)[0].(map[string]any)
				if source["SupportsDirectStream"] != true || source["TranscodingUrl"] != nil {
					t.Fatal("明确直播放请求不再接管")
				}
			})
		}
		t.Run("allowed/stream/"+query, func(t *testing.T) {
			response, err := noRedirectClient().Get(base + "/Videos/movie/stream?api_key=valid&" + query)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusTemporaryRedirect || response.Header.Get("Location") != "https://cdn.example/video" {
				t.Fatal("允许直播放的取流请求不再接管")
			}
		})
	}
}

func TestPlaybackInfoRequestPreferencesAndPrivateCache(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(file, []byte("https://cdn.example/video"), 0600); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"Keep": "unknown", "MediaSources": []any{map[string]any{
		"Id": "source", "Path": file, "SupportsTranscoding": true, "TranscodingUrl": "/transcode",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	type receivedRequest struct {
		body        string
		contentType string
	}
	received := make(chan receivedRequest, 1)
	var lookups atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Items/movie/PlaybackInfo":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			received <- receivedRequest{body: string(body), contentType: r.Header.Get("Content-Type")}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "public, max-age=3600")
			w.Header().Set("ETag", "upstream")
			_, _ = w.Write(payload)
		case "/Users/Me":
			lookups.Add(1)
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/movie":
			lookups.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
		default:
			t.Errorf("意外的上游请求：%s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil }})
	for _, test := range []struct {
		name    string
		method  string
		body    string
		rewrite bool
	}{
		{name: "GET 头部鉴权", method: http.MethodGet, rewrite: true},
		{name: "POST 空请求", rewrite: true},
		{name: "POST 纯空白请求", body: " \n\t", rewrite: true},
		{name: "POST 空对象", body: `{}`, rewrite: true},
		{name: "POST 未知字段及原始空白", body: " {\n\t\"UserId\":\"user\", \"Unknown\": {\"keep\":[1,true]}\n} ", rewrite: true},
		{name: "POST 明确允许", body: `{"EnableDirectPlay":true,"EnableDirectStream":true}`, rewrite: true},
		{name: "POST 单项允许及大小写", body: `{"enabledirectstream":true}`, rewrite: true},
		{name: "POST 未知字段内的同名标记", body: `{"Unknown":{"EnableDirectPlay":false}}`, rewrite: true},
		{name: "POST 同时禁止", body: `{"EnableDirectPlay":false,"EnableDirectStream":false}`},
		{name: "POST 禁止 DirectPlay", body: `{"EnableDirectPlay":false,"EnableDirectStream":true}`},
		{name: "POST 禁止 DirectStream", body: `{"EnableDirectPlay":true,"EnableDirectStream":false}`},
		{name: "POST 大小写", body: `{"enabledirectplay":false,"ENABLEDIRECTSTREAM":false}`},
		{name: "POST 同名冲突", body: `{"EnableDirectPlay":false,"EnableDirectPlay":true}`},
		{name: "POST 大小写冲突", body: `{"EnableDirectStream":false,"enabledirectstream":true}`},
		{name: "POST 同名重复", body: `{"EnableDirectPlay":true,"EnableDirectPlay":true}`},
		{name: "POST 转义字段重复", body: `{"EnableDirectPlay":true,"EnableDirect\u0050lay":false}`},
		{name: "POST 字符串布尔", body: `{"EnableDirectPlay":"false"}`},
		{name: "POST null 布尔", body: `{"EnableDirectStream":null}`},
		{name: "POST 数字布尔", body: `{"EnableDirectPlay":0}`},
		{name: "POST 对象布尔", body: `{"EnableDirectStream":{}}`},
		{name: "POST 非对象", body: `[]`},
		{name: "POST null 对象", body: `null`},
		{name: "POST 畸形 JSON", body: `{"EnableDirectPlay":true`},
		{name: "POST 多段 JSON", body: `{"EnableDirectPlay":true} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			method := test.method
			if method == "" {
				method = http.MethodPost
			}
			req, err := http.NewRequest(method, base+"/emby/Items/movie/PlaybackInfo", strings.NewReader(test.body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("X-Emby-Token", "valid")
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
			before := lookups.Load()
			response, err := noRedirectClient().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("播放响应异常：status=%d err=%v", response.StatusCode, err)
			}
			original := <-received
			if original.body != test.body || original.contentType != req.Header.Get("Content-Type") {
				t.Fatal("上游未收到原始请求体或 Content-Type")
			}
			if !test.rewrite {
				if !bytes.Equal(body, payload) || response.Header.Get("Cache-Control") != "public, max-age=3600" || response.Header.Get("ETag") != "upstream" {
					t.Error("禁止或无法确认直播放的请求未保留上游响应")
				}
				if lookups.Load() != before {
					t.Error("回退前执行了来源查询")
				}
				return
			}
			var result map[string]any
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatal(err)
			}
			source := result["MediaSources"].([]any)[0].(map[string]any)
			stream, err := url.Parse(source["DirectStreamUrl"].(string))
			if err != nil || stream.Query().Get("api_key") != "valid" || source["SupportsDirectStream"] != true || source["TranscodingUrl"] != nil || result["Keep"] != "unknown" {
				t.Fatal("正常请求未正确改写来源及头部鉴权 Token")
			}
			if response.Header.Get("Cache-Control") != "no-store, private" || response.Header.Get("ETag") != "" {
				t.Error("包含当前用户 Token 的改写响应仍可公共缓存")
			}
			if lookups.Load()-before != 2 {
				t.Error("正常改写未重新授权来源")
			}
		})
	}
}

func TestPlaybackMultiSourceAndExternalBoundary(t *testing.T) {
	root := t.TempDir()
	firstPath, secondPath := filepath.Join(root, "one.strm"), filepath.Join(root, "two.strm")
	for _, name := range []string{firstPath, secondPath} {
		if err := os.WriteFile(name, []byte("https://external.example/video?sign=keep%2Braw"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/movie":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "MediaSources": []any{map[string]any{"Id": "one", "Path": firstPath}, map[string]any{"Id": "two", "Path": secondPath}}})
		default:
			_, _ = io.WriteString(w, "upstream")
		}
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil }})
	for _, test := range []struct {
		name  string
		query string
		code  int
	}{
		{name: "缺源不能取第一份", query: "", code: http.StatusOK},
		{name: "准确媒体源", query: "&MediaSourceId=two", code: http.StatusTemporaryRedirect},
		{name: "未知媒体源", query: "&MediaSourceId=unknown", code: http.StatusOK},
		{name: "重复媒体源", query: "&MediaSourceId=one&MediaSourceId=two", code: http.StatusOK},
		{name: "显式转码", query: "&MediaSourceId=one&static=false", code: http.StatusOK},
		{name: "重复static歧义", query: "&MediaSourceId=one&static=false&static=true", code: http.StatusOK},
		{name: "大小写static歧义", query: "&MediaSourceId=one&static=false&Static=true", code: http.StatusOK},
		{name: "重复播放方式歧义", query: "&MediaSourceId=one&PlayMethod=Transcode&PlayMethod=DirectPlay", code: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := noRedirectClient().Get(base + "/Videos/movie/original?api_key=valid" + test.query)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.code {
				t.Fatalf("HTTP=%d want=%d", response.StatusCode, test.code)
			}
			if test.code == http.StatusTemporaryRedirect && response.Header.Get("Location") != "https://external.example/video?sign=keep%2Braw" {
				t.Fatal("外部签名URL被修改")
			}
		})
	}
}

func TestPlaybackUserPolicyCannotBeBypassedByVisibleItem(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(file, []byte("https://external.example/movie"), 0600); err != nil {
		t.Fatal(err)
	}
	var currentPolicy map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/Me":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "user", "Policy": currentPolicy})
		case "/Users/user/Items/movie":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie", "MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
		case "/Items/movie/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{"Id": "source", "Path": file}}})
		default:
			_, _ = io.WriteString(w, "upstream policy behavior")
		}
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil }})
	for _, test := range []struct {
		name   string
		policy map[string]any
		path   string
		code   int
	}{
		{name: "允许播放", policy: map[string]any{"EnableMediaPlayback": true}, path: "/Videos/movie/stream", code: http.StatusTemporaryRedirect},
		{name: "账户禁用", policy: map[string]any{"IsDisabled": true}, path: "/Videos/movie/stream", code: http.StatusOK},
		{name: "禁止播放", policy: map[string]any{"EnableMediaPlayback": false}, path: "/Videos/movie/stream", code: http.StatusOK},
		{name: "禁止下载", policy: map[string]any{"EnableContentDownloading": false}, path: "/Items/movie/Download", code: http.StatusOK},
		{name: "禁止文件下载", policy: map[string]any{"EnableContentDownloading": false}, path: "/Items/movie/File", code: http.StatusOK},
		{name: "禁止下载仍允许播放", policy: map[string]any{"EnableContentDownloading": false}, path: "/Videos/movie/stream", code: http.StatusTemporaryRedirect},
		{name: "强制远程转码", policy: map[string]any{"ForceRemoteSourceTranscoding": true}, path: "/Videos/movie/stream", code: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			currentPolicy = test.policy
			response, err := noRedirectClient().Get(base + test.path + "?api_key=valid&MediaSourceId=source")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != test.code {
				t.Fatalf("策略未生效 HTTP=%d want=%d", response.StatusCode, test.code)
			}
		})
	}
	currentPolicy = map[string]any{"EnableMediaPlayback": false}
	response, err := noRedirectClient().Get(base + "/Items/movie/PlaybackInfo?api_key=valid&MediaSourceId=source")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var info map[string]any
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if source := info["MediaSources"].([]any)[0].(map[string]any); source["DirectStreamUrl"] != nil {
		t.Fatal("禁播用户的 PlaybackInfo 被改成直连")
	}
}

func TestEffectiveCredentialsRejectConflict(t *testing.T) {
	for _, test := range []struct {
		name   string
		query  string
		header string
		valid  bool
	}{
		{name: "仅query", query: "api_key=one", valid: true},
		{name: "一致", query: "api_key=one", header: `MediaBrowser Client="Player", Token="one"`, valid: true},
		{name: "头冲突", query: "api_key=one", header: `MediaBrowser Token="two"`},
		{name: "重复query", query: "api_key=one&api_key=one"},
		{name: "重复头内token", header: `MediaBrowser Token="one", Token="one"`},
		{name: "无凭据"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/?"+test.query, nil)
			if test.header != "" {
				request.Header.Set("X-Emby-Authorization", test.header)
			}
			headers, token, ok := effectiveCredentials(request)
			if ok != test.valid {
				t.Fatalf("凭据有效性：%v", ok)
			}
			if ok && (token != "one" || !strings.Contains(headers.Get("X-Emby-Authorization"), `Token="one"`)) {
				t.Fatal("没有使用本次播放器凭据")
			}
		})
	}
}

func TestEffectiveCredentialsCompatibleDescriptions(t *testing.T) {
	for _, test := range []struct {
		name, description string
		valid             bool
	}{
		{"unquoted", `Emby Client=Yamby, DeviceId=opaque-device, Token = one`, true},
		{"case-order", `MediaBrowser tOkEn=one, ClIeNt = Custom Player`, true},
		{"bearer", `Bearer one`, true},
		{"empty", `Emby Token = ""`, false},
		{"duplicate", `Emby Token=one, Token=one`, false},
		{"conflict", `Emby Token=two`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/?api_key=one", nil)
			r.Header.Set("Authorization", test.description)
			headers, token, valid := effectiveCredentials(r)
			if valid != test.valid || valid && (token != "one" || headers.Get("Authorization") != test.description) {
				t.Fatal("认证兼容、冲突拒绝或原请求保留不符")
			}
		})
	}
}

func TestPlaybackInfoFallbackPreservesEncodedResponse(t *testing.T) {
	for _, test := range []struct {
		name     string
		body     []byte
		encoding string
	}{
		{name: "HTML", body: []byte("<html>upstream</html>")},
		{name: "无效gzip", body: []byte("not gzip"), encoding: "gzip"},
		{name: "过大响应", body: bytes.Repeat([]byte("a"), metadataLimit+16)},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Encoding", test.encoding)
				_, _ = w.Write(test.body)
			}))
			defer upstream.Close()
			_, _, base := startService(t, upstream.URL, Options{})
			request, _ := http.NewRequest(http.MethodGet, base+"/Items/movie/PlaybackInfo?api_key=valid", nil)
			request.Header.Set("Accept-Encoding", "identity")
			response, err := noRedirectClient().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, _ := io.ReadAll(response.Body)
			if !bytes.Equal(body, test.body) || response.Header.Get("Content-Encoding") != test.encoding {
				t.Fatal("不能改写时破坏了上游响应")
			}
		})
	}
}

func TestInternalURLDoesNotExposeMainAdministration(t *testing.T) {
	inst := &instance{service: NewService(Options{KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://qms:12333/prefix"}, nil }})}
	for _, test := range []struct {
		name string
		url  string
		ok   bool
	}{
		{name: "本实例取链", url: "http://qms:12333/prefix/115/newurl?pickcode=code&userid=user", ok: true},
		{name: "其他实例", url: "http://other:12333/prefix/115/newurl?pickcode=code&userid=user"},
		{name: "管理API", url: "http://qms:12333/prefix/api/settings"},
		{name: "前缀相似", url: "http://qms:12333/prefix-more/115/newurl?pickcode=code"},
		{name: "穿越", url: "http://qms:12333/prefix/115/url/../api/settings?pickcode=code"},
		{name: "错误force", url: "http://qms:12333/prefix/115/newurl?pickcode=code&force=2"},
		{name: "缺账号OpenList", url: "http://qms:12333/prefix/openlist/url?path=/file"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := inst.internalURL(t.Context(), test.url); ok != test.ok {
				t.Fatalf("内部转交边界错误：%v", ok)
			}
		})
	}
}

func TestPlaybackFnos098RequiresActualItemAndCanonicalSource(t *testing.T) {
	const itemID = "11111111111111111111111111111111"
	const sourceID = "22222222222222222222222222222222"
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(file, []byte("https://external.example/movie"), 0600); err != nil {
		t.Fatal(err)
	}
	media := []any{map[string]any{"Id": sourceID, "Path": file, "Protocol": "File", "SupportsProbing": true,
		"Type": "Video", "MediaStreams": []any{map[string]any{"Type": "Video", "Codec": "h264", "Language": "und", "DisplayTitle": "1080P H264"}}}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/Me":
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case r.URL.Path == "/Users/user/Items/"+itemID:
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": itemID, "Path": file, "MediaSources": media})
		case r.URL.Path == "/Users/user/Items/"+sourceID:
			// 飞牛 0.9.8 的无效 ItemID 可能返回 200 和空对象，状态码本身不能证明权限。
			_, _ = io.WriteString(w, `{"Id":""}`)
		case r.URL.Path == "/Items/"+itemID+"/PlaybackInfo" && r.Method == http.MethodPost:
			_, _ = io.WriteString(w, "<html>fnOS SPA</html>")
		case r.URL.Path == "/Items/"+itemID+"/PlaybackInfo":
			if r.URL.Query().Get("MediaSourceId") != sourceID {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": media, "PlaySessionId": "session"})
		default:
			_, _ = io.WriteString(w, "upstream")
		}
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil }})
	client := noRedirectClient()
	response, err := client.Get(base + "/Items/" + itemID + "/PlaybackInfo?api_key=valid&MediaSourceId=" + sourceID)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var info map[string]any
	if err := json.NewDecoder(response.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	rewritten := info["MediaSources"].([]any)[0].(map[string]any)
	direct, err := url.Parse(rewritten["DirectStreamUrl"].(string))
	if err != nil || direct.Path != "/Videos/"+itemID+"/stream" || direct.Query().Get("MediaSourceId") != sourceID {
		t.Fatal("混用了真实 ItemID 与 MediaSourceID")
	}
	response, err = client.Get(base + "/Videos/" + sourceID + "/stream?api_key=valid&MediaSourceId=" + sourceID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "upstream" || response.Header.Get("Location") != "" {
		t.Fatal("无效 ItemID 的 200 空对象被当作授权证明")
	}
	response, err = client.Post(base+"/Items/"+itemID+"/PlaybackInfo?api_key=valid", "application/json", strings.NewReader(`{"MediaSourceId":"`+sourceID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "<html>fnOS SPA</html>" {
		t.Fatal("不支持的 POST 未保留上游响应")
	}
}
