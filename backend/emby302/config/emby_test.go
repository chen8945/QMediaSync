package config

import "testing"

func TestEmbyIsLocalMediaPath(t *testing.T) {
	for _, tt := range []struct {
		name string
		root string
		path string
		want bool
	}{
		{name: "Linux 绝对路径", path: "/media/movie.mkv", want: true},
		{name: "配置根之外的绝对路径", root: "/media", path: "/other/movie.mkv", want: true},
		{name: "Windows 反斜杠", path: `C:\media\movie.mkv`, want: true},
		{name: "Windows 正斜杠", path: "D:/media/movie.mkv", want: true},
		{name: "小写盘符", path: "z:movie.mkv", want: true},
		{name: "UNC 共享", path: `\\nas\media\movie.mkv`, want: true},
		{name: "根反斜杠", path: `\media\movie.mkv`, want: true},
		{name: "正斜杠共享", path: "//nas/media/movie.mkv", want: true},
		{name: "SMB 共享", path: "smb://nas/media/movie.mkv", want: true},
		{name: "SMB 大小写", path: "SmB://nas/media/movie.mkv", want: true},
		{name: "配置相对根", root: "media/", path: "media/movie.mkv", want: true},
		{name: "HTTP STRM", root: "/", path: "http://qms.test/115/url/movie.mkv?path=smb://nas/media"},
		{name: "HTTPS STRM", root: "/", path: "https://qms.test/115/newurl?pickcode=test"},
		{name: "NFS 保留既有分支", root: "/", path: "nfs://nas/media/movie.strm"},
		{name: "非盘符冒号", path: "1:/movie.mkv"},
		{name: "未配置相对路径", path: "media/movie.mkv"},
		{name: "空路径"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Emby{LocalMediaRoot: tt.root}
			if got := cfg.IsLocalMediaPath(tt.path); got != tt.want {
				t.Fatalf("IsLocalMediaPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}
