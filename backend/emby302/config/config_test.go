package config

import (
	"strings"
	"testing"
	"time"

	"qmediasync/emby302/util/logs/colors"
)

func TestReadFromFileYAMLSemantics(t *testing.T) {
	old := C
	t.Cleanup(func() {
		C = old
		if old != nil && old.Log != nil {
			colors.SetEnabler(old.Log)
		} else {
			colors.SetEnabler(nil)
		}
	})
	data := []byte(`defaults: &defaults
  host: http://127.0.0.1:8096
  images-quality: 85
emby:
  <<: *defaults
  local-media-root: /media
  images-original: yes
  strm:
    path-map: [/original => /mapped]
cache:
  enable: true
  expired: 2h
video-preview:
  enable: true
  containers: [mkv]
  ignore-template-ids: ["1080"]
log:
  disable-color: true
`)
	if err := ReadFromFile(data); err != nil {
		t.Fatal(err)
	}
	if C.Emby.Host != "http://127.0.0.1:8096" || C.Emby.ImagesQuality != 85 || !C.Emby.ImagesOriginal ||
		C.Emby.ProxyErrorStrategy != PeStrategyOrigin || C.Emby.DownloadStrategy != DlStrategyDirect {
		t.Fatal("Emby configuration values or defaults changed")
	}
	if C.Openlist == nil || C.Openlist.LocalTreeGen == nil || C.Path == nil || C.Ssl == nil || C.Ssl.Enable {
		t.Fatal("missing configuration sections were not initialized")
	}
	if !C.Cache.Enable || C.Cache.ExpiredDuration() != 2*time.Hour ||
		!C.VideoPreview.ContainerValid("mkv") || !C.VideoPreview.IsTemplateIgnore("1080") ||
		C.Emby.Strm.MapPath("/original/movie.mkv") != "/mapped/movie.mkv" {
		t.Fatal("configuration initialization changed")
	}
}

func TestReadFromFileRejectsInvalidMergeKey(t *testing.T) {
	old := C
	t.Cleanup(func() { C = old })
	defer func() {
		if value := recover(); value != nil {
			t.Fatalf("invalid YAML merge key panicked: %v", value)
		}
	}()
	data := []byte("emby:\n  <<: {host: 'http://127.0.0.1:8096'}\n  ? [invalid, key]\n  : value\n")
	err := ReadFromFile(data)
	if err == nil || !strings.Contains(err.Error(), "解析配置文件失败") {
		t.Fatalf("ReadFromFile() = %v, want YAML parse error", err)
	}
}
