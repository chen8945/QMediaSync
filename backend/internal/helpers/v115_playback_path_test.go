package helpers

import "testing"

func TestIsV115PlaybackPath(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		path string
		want bool
	}{
		{name: "根目录", path: "/多端播放", want: true},
		{name: "根目录末尾斜杠", path: "/多端播放/", want: true},
		{name: "副本", path: "/多端播放/movie.mkv", want: true},
		{name: "整个子树", path: "/多端播放/child/movie.mkv", want: true},
		{name: "115 省略首斜杠", path: "多端播放/child", want: true},
		{name: "重复分隔符", path: "//多端播放//child", want: true},
		{name: "路径导航符", path: "/Media/../多端播放/./child", want: true},
		{name: "反斜杠", path: `\多端播放\child`, want: true},
		{name: "离开临时目录", path: "/多端播放/../Media/movie.mkv"},
		{name: "同名非根目录", path: "/Media/多端播放/movie.mkv"},
		{name: "同名前缀", path: "/多端播放备份/movie.mkv"},
		{name: "同名文件前缀", path: "/多端播放.mkv"},
		{name: "空路径尚未补全"},
		{name: "网盘根目录", path: "/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsV115PlaybackPath(tt.path); got != tt.want {
				t.Fatalf("IsV115PlaybackPath(%q) = %v，期望 %v", tt.path, got, tt.want)
			}
		})
	}
}
