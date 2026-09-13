package helpers

import "testing"

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
