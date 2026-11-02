package emby

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"qmediasync/internal/emby302/constant"
	"qmediasync/internal/emby302/util/urls"
	"qmediasync/internal/emby302/web/cache"
)

// MediaPathDisplay 在缓存及预加载观察之外解码展示副本，内部保留原始 HTTP 地址。
func MediaPathDisplay() gin.HandlerFunc {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(constant.Reg_PlaybackInfo),
		regexp.MustCompile(constant.Reg_UserItems),
		regexp.MustCompile(constant.Reg_UserEpisodeItems),
		regexp.MustCompile(constant.Reg_UserLatestItems),
	}
	return func(c *gin.Context) {
		for index, pattern := range patterns {
			if pattern.MatchString(c.Request.RequestURI) {
				c.Writer = &mediaPathResponseWriter{ResponseWriter: c.Writer, playback: index == 0}
				return
			}
		}
	}
}

type mediaPathResponseWriter struct {
	gin.ResponseWriter
	playback  bool
	attempted bool
}

func (w *mediaPathResponseWriter) Write(value []byte) (int, error) {
	if w.attempted || w.Written() || w.Status() != http.StatusOK || w.Header().Get("Content-Encoding") != "" {
		return w.ResponseWriter.Write(value)
	}
	w.attempted = true
	length, err := strconv.Atoi(w.Header().Get("Content-Length"))
	if err != nil || length != len(value) {
		return w.ResponseWriter.Write(value)
	}
	body, changed := decodeMediaResponsePaths(value, w.playback)
	if !changed {
		return w.ResponseWriter.Write(value)
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	n, err := w.ResponseWriter.Write(body)
	if err != nil || n != len(body) {
		w.Header().Set(cache.HeaderKeyExpired, "-1")
		if err == nil {
			err = io.ErrShortWrite
		}
		return 0, err
	}
	// 内层观察器和缓存消费的是原始正文，写入数必须对应原始字节。
	return len(value), nil
}

func (w *mediaPathResponseWriter) WriteString(value string) (int, error) {
	return w.Write([]byte(value))
}

func decodeMediaResponsePaths(raw []byte, playback bool) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return raw, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return raw, false
	}
	changed := false
	decodePath := func(item map[string]any) {
		path, ok := item["Path"].(string)
		if !ok {
			return
		}
		u, err := url.Parse(path)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
			return
		}
		if decoded := urls.Unescape(path); decoded != path {
			item["Path"], changed = decoded, true
		}
	}
	decodeItem := func(item map[string]any) {
		itemType, _ := item["Type"].(string)
		if item["IsLive"] == true || item["IsInfiniteStream"] == true || strings.EqualFold(itemType, "TvChannel") {
			return
		}
		decodePath(item)
		sources, _ := item["MediaSources"].([]any)
		for _, source := range sources {
			if source, ok := source.(map[string]any); ok && source["IsInfiniteStream"] != true {
				decodePath(source)
			}
		}
	}
	switch value := value.(type) {
	case map[string]any:
		if playback {
			sources, _ := value["MediaSources"].([]any)
			for _, source := range sources {
				if source, ok := source.(map[string]any); ok && source["IsInfiniteStream"] == true {
					return raw, false
				}
			}
		}
		decodeItem(value)
		items, _ := value["Items"].([]any)
		for _, item := range items {
			if item, ok := item.(map[string]any); ok {
				decodeItem(item)
			}
		}
	case []any:
		for _, item := range value {
			if item, ok := item.(map[string]any); ok {
				decodeItem(item)
			}
		}
	}
	if !changed {
		return raw, false
	}
	body, err := json.Marshal(value)
	if err != nil {
		return raw, false
	}
	return body, true
}
