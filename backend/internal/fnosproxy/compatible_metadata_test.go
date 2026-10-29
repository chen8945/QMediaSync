package fnosproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlaybackInfoExternalSubtitles(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(file, []byte("https://external.example/movie"), 0600); err != nil {
		t.Fatal(err)
	}
	streams := []any{
		map[string]any{"Type": "Subtitle", "Codec": "subrip", "Index": 0, "IsExternal": true, "SupportsExternalStream": false, "IsTextSubtitleStream": false, "Language": "chi", "Unknown": "keep"},
		map[string]any{"Type": "Subtitle", "Codec": "ass", "Index": 2, "IsExternal": true},
		map[string]any{"Type": "Subtitle", "Codec": "PGSSUB", "Index": 3, "IsExternal": true, "IsTextSubtitleStream": false},
		map[string]any{"Type": "Subtitle", "Codec": "srt", "Index": 4, "IsExternal": true, "DeliveryUrl": "/existing.srt", "DeliveryMethod": "External"},
		map[string]any{"Type": "Subtitle", "Codec": "srt", "Index": 5, "IsExternal": false},
		map[string]any{"Type": "Subtitle", "Codec": "unknown/codec", "Index": 6, "IsExternal": true},
		map[string]any{"Type": "Subtitle", "Codec": "srt", "Index": -1, "IsExternal": true},
		map[string]any{"Type": "Subtitle", "Codec": "srt", "Index": 1.5, "IsExternal": true},
		map[string]any{"Type": "Subtitle", "Codec": "srt", "Index": 1e30, "IsExternal": true},
		map[string]any{"Type": "Audio", "Codec": "srt", "Index": 9, "IsExternal": true},
	}
	media := []any{map[string]any{"Id": "logical", "Path": file, "MediaStreams": streams}, map[string]any{"Id": "other", "Path": "/unreadable.strm", "MediaStreams": streams}}
	var subtitleRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/Me":
			_, _ = io.WriteString(w, `{"Id":"user"}`)
		case "/Users/user/Items/file":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "file", "MediaSources": media[:1]})
		case "/Items/file/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": media})
		case "/Videos/file/logical/Subtitles/0/0/Stream.srt":
			if r.URL.Query().Get("api_key") != "token+&=?" {
				t.Error("字幕凭据没有正确编码")
			}
			subtitleRequests++
			_, _ = io.WriteString(w, "1\n00:00:01,000 --> 00:00:02,000\n字幕\n")
		default:
			t.Errorf("意外请求：%s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil }})
	for _, token := range []string{"token+&=?", ""} {
		response, err := noRedirectClient().Get(base + "/emby/Items/file/PlaybackInfo?api_key=" + url.QueryEscape(token))
		if err != nil {
			t.Fatal(err)
		}
		var payload map[string]any
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		sources := payload["MediaSources"].([]any)
		got := sources[0].(map[string]any)["MediaStreams"].([]any)
		for i, raw := range got {
			stream := raw.(map[string]any)
			if token == "" || i >= 3 {
				want, _ := json.Marshal(streams[i])
				actual, _ := json.Marshal(stream)
				if !bytes.Equal(want, actual) {
					t.Errorf("不应改写字幕 %d：%s", i, actual)
				}
				continue
			}
			formats := []string{"srt", "ass", "sup"}
			target, err := url.Parse(stringField(stream, "DeliveryUrl"))
			if err != nil || target.Path != fmt.Sprintf("/Videos/file/logical/Subtitles/%d/0/Stream.%s", []int{0, 2, 3}[i], formats[i]) || target.Query().Get("api_key") != token || target.IsAbs() || stream["DeliveryMethod"] != "External" || stream["SupportsExternalStream"] != true || stream["IsTextSubtitleStream"] != (i != 2) {
				t.Fatalf("外挂字幕投递信息错误：%v", stream)
			}
			if i == 0 {
				if stream["Language"] != "chi" || stream["Unknown"] != "keep" || response.Header.Get("Cache-Control") != "no-store, private" {
					t.Fatal("字幕字段或缓存策略被破坏")
				}
				subtitle, err := noRedirectClient().Get(base + target.String())
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(subtitle.Body)
				_ = subtitle.Body.Close()
				if !bytes.Contains(body, []byte("字幕")) {
					t.Fatal("字幕地址未透传真实字幕响应")
				}
			}
		}
		other, _ := json.Marshal(sources[1].(map[string]any)["MediaStreams"])
		original, _ := json.Marshal(streams)
		if !bytes.Equal(other, original) {
			t.Fatal("未接管媒体源的字幕被改写")
		}
	}
	if subtitleRequests != 1 {
		t.Fatalf("字幕请求次数错误：%d", subtitleRequests)
	}
}

func TestCompatibleItemRuntimeResponses(t *testing.T) {
	const item = `{"Id":"file","Type":"Episode","RunTimeTicks":20000000,"MediaSources":[{"Id":"logical","Path":"/media/episode.strm","RunTimeTicks":900000000,"MediaStreams":[{"Type":"Video","Codec":"h264"}]}],"Unknown":9007199254740993}`
	var body, encoding string
	status := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("ETag", "original")
		w.Header().Set("Content-Type", "application/json")
		if encoding != "" {
			w.Header().Set("Content-Encoding", encoding)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer upstream.Close()
	_, _, base := startService(t, upstream.URL, Options{})
	for _, endpoint := range []string{"/Items/file", "/emby/Items/file", "/Users/user/Items/file", "/emby/Users/user/Items/file", "/Items", "/Users/user/Items", "/emby/Shows/series/Episodes"} {
		t.Run(endpoint, func(t *testing.T) {
			body, encoding, status = item, "", http.StatusOK
			list := !strings.HasSuffix(endpoint, "/file")
			if list {
				body = `{"TotalRecordCount":2,"Items":[` + item + `,{"Id":"local","Type":"Movie","RunTimeTicks":5,"MediaSources":[{"Id":"local-source","Path":"/media/video.mp4","RunTimeTicks":900000000,"MediaStreams":[{"Type":"Video","Codec":"h264"}]}]}],"Unknown":9007199254740993}`
			}
			var compressed bytes.Buffer
			writer := gzip.NewWriter(&compressed)
			_, _ = io.WriteString(writer, body)
			_ = writer.Close()
			body, encoding = compressed.String(), "gzip"
			request, _ := http.NewRequest(http.MethodGet, base+endpoint, nil)
			request.Header.Set("Accept-Encoding", "gzip")
			response, err := noRedirectClient().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if !bytes.Contains(actual, []byte(`"RunTimeTicks":900000000`)) || !bytes.Contains(actual, []byte("9007199254740993")) || response.Header.Get("Content-Encoding") != "" || response.Header.Get("ETag") != "" || response.Header.Get("Cache-Control") != "no-store, private" || response.ContentLength != int64(len(actual)) {
				t.Fatalf("时长或原字段／响应头不符：%s", actual)
			}
			var payload map[string]any
			_ = json.Unmarshal(actual, &payload)
			if list {
				items := payload["Items"].([]any)
				if len(items) != 2 || payload["TotalRecordCount"] != float64(2) || items[1].(map[string]any)["RunTimeTicks"] != float64(5) {
					t.Fatal("列表顺序、计数或非 STRM 时长被改变")
				}
				payload = items[0].(map[string]any)
			}
			if payload["RunTimeTicks"] != float64(900000000) {
				t.Fatal("条目与唯一媒体源时长不一致")
			}
		})
	}
	for _, test := range []struct {
		name, endpoint, value, encoding string
		status                          int
	}{
		{"未探测", "/Items/file", strings.ReplaceAll(item, `"Codec":"h264"`, `"Codec":""`), "", 200},
		{"非正时长", "/Items/file", strings.ReplaceAll(item, `900000000`, `0`), "", 200},
		{"小数时长", "/Items/file", strings.ReplaceAll(item, `900000000`, `900000000.5`), "", 200},
		{"超长时长", "/Items/file", strings.ReplaceAll(item, `900000000`, `864000000001`), "", 200},
		{"多源", "/Items/file", strings.ReplaceAll(item, `"MediaSources":[`, `"MediaSources":[{"Id":"local","Path":"/local.mp4"},`), "", 200},
		{"归属不符", "/Items/other", item, "", 200},
		{"剧集父页", "/Items/file", strings.ReplaceAll(item, `"Episode"`, `"Series"`), "", 200},
		{"无源", "/Items/file", ` {"Id":"file","Type":"Movie","RunTimeTicks":0} `, "", 200},
		{"空对象", "/Items/file", ` {} `, "", 200},
		{"HTML", "/Items/file", `<html>SPA</html>`, "", 200},
		{"其他接口", "/Other/file", item, "", 200},
		{"字幕接口", "/Items/file/Images/Primary", item, "", 200},
		{"失败响应", "/Items/file", item, "", 403},
		{"未知编码", "/Items/file", item, "br", 200},
		{"损坏压缩", "/Items/file", `broken`, "gzip", 200},
		{"超限", "/Items/file", item + strings.Repeat(" ", metadataLimit), "", 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, encoding, status = test.value, test.encoding, test.status
			request, _ := http.NewRequest(http.MethodGet, base+test.endpoint, nil)
			request.Header.Set("Accept-Encoding", "identity")
			response, err := noRedirectClient().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			actual, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if string(actual) != body || response.StatusCode != status || response.Header.Get("Cache-Control") != "public, max-age=3600" || response.Header.Get("ETag") != "original" || response.Header.Get("Content-Encoding") != encoding {
				t.Fatal("无需校正的响应没有原样保留")
			}
		})
	}
}
