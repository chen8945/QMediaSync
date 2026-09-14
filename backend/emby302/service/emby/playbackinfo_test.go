package emby

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"qmediasync/emby302/config"
)

func TestTransferPlaybackInfoLocalMediaPaths(t *testing.T) {
	for _, tt := range []struct {
		name  string
		path  string
		local bool
	}{
		{"Linux", "/media/movie.mkv", true},
		{"Windows", `D:\media\movie.mkv`, true},
		{"UNC", `\\nas\media\movie.mkv`, true},
		{"SMB", "smb://nas/media/movie.mkv", true},
		{"SMB 大小写", "SmB://nas/media/movie.mkv", true},
		{"正斜杠共享", "//nas/media/movie.mkv", true},
		{"HTTP STRM", "http://qms.test/115/url/movie.mkv?pickcode=test", false},
		{"HTTPS STRM", "https://qms.test/115/newurl?pickcode=test", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, selected := range []struct{ name, query string }{
				{"全部来源", ""},
				{"指定来源", "&MediaSourceId=ms"},
			} {
				t.Run(selected.name, func(t *testing.T) {
					router, _ := newSTRMRedirectTestRouter(t, tt.path, false)
					config.C.Emby.LocalMediaRoot = "/"
					config.C.VideoPreview = &config.VideoPreview{}
					router.POST("/Items/movie/PlaybackInfo", TransferPlaybackInfo)
					rec := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, "/Items/movie/PlaybackInfo?api_key=test"+selected.query, nil)
					router.ServeHTTP(rec, req)
					if rec.Code != http.StatusOK {
						t.Fatalf("PlaybackInfo status=%d body=%q", rec.Code, rec.Body.String())
					}
					var response struct {
						MediaSources []struct {
							Path                string
							DirectStreamURL     string
							TranscodingURL      string
							SupportsTranscoding bool
						}
					}
					if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if len(response.MediaSources) != 1 {
						t.Fatalf("MediaSources = %v", response.MediaSources)
					}
					source := response.MediaSources[0]
					if source.Path != tt.path || source.SupportsTranscoding != tt.local {
						t.Fatalf("本地分类改变路径或转码能力：source=%+v", source)
					}
					if tt.local {
						if source.DirectStreamURL != "" || source.TranscodingURL != "/origin/transcode" {
							t.Fatalf("本地媒体的播放地址被重写：source=%+v", source)
						}
					} else if source.DirectStreamURL != "/videos/movie/stream?MediaSourceId=ms&api_key=test&Static=true" || source.TranscodingURL != "" {
						t.Fatalf("HTTP STRM 未保留直链处理：source=%+v", source)
					}
				})
			}
		})
	}
}
