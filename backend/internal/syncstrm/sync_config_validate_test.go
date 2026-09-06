package syncstrm

import (
	"io"
	"log"
	"testing"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestValidFileHonorsNameAndParentExclusions(t *testing.T) {
	tests := []struct {
		name string
		file SyncFileCache
		want bool
	}{
		{name: "排除文件名忽略大小写", file: SyncFileCache{FileName: "SAMPLE.mkv", Path: "/Media", SourceType: models.SourceType115}},
		{name: "排除 115 父目录", file: SyncFileCache{FileName: "movie.mkv", Path: "/Media/EXTRAS/Season 1", SourceType: models.SourceType115}},
		{name: "排除 OpenList 父目录", file: SyncFileCache{FileName: "movie.mkv", ParentId: "/Media/Extras/Season 1", SourceType: models.SourceTypeOpenList}},
		{name: "排除本地父目录", file: SyncFileCache{FileName: "movie.mkv", ParentId: "/Media/Extras/Season 1", SourceType: models.SourceTypeLocal}},
		{name: "完整匹配不排除部分名称", file: SyncFileCache{FileName: "MySample.mkv", Path: "/Media/MyExtras", SourceType: models.SourceType115}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer := &SyncStrm{
				Sync: &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
				Config: SyncStrmConfig{
					VideoExt:     []string{".mkv"},
					ExcludeNames: []string{"sample.mkv", "extras"},
				},
			}
			file := tt.file
			if got := syncer.ValidFile(&file); got != tt.want {
				t.Fatalf("ValidFile(%+v) = %v，期望 %v", tt.file, got, tt.want)
			}
		})
	}
}

func TestRegexExclusionMatching(t *testing.T) {
	tests := []struct {
		name     string
		exact    []string
		patterns []string
		filename string
		path     string
		want     bool
	}{
		{name: "仅正则也生效", patterns: []string{"sample"}, filename: "movie.sample.mkv", want: true},
		{name: "正则默认区分大小写", patterns: []string{"sample"}, filename: "Sample.mkv"},
		{name: "正则匹配原始大写名称", patterns: []string{"^Sample"}, filename: "Sample.mkv", want: true},
		{name: "标志可忽略大小写", patterns: []string{"(?i)sample"}, filename: "SAMPLE.mkv", want: true},
		{name: "锚点完整匹配名称含扩展名", patterns: []string{"^sample\\.mkv$"}, filename: "sample.mkv", want: true},
		{name: "完整匹配不忽略扩展名", patterns: []string{"^sample$"}, filename: "sample.mkv"},
		{name: "精确列表与正则取并集", exact: []string{"movie.mkv"}, patterns: []string{"^Other$"}, filename: "MOVIE.MKV", want: true},
		{name: "正则排除任意父目录", patterns: []string{"(?i)^extras$"}, filename: "movie.mkv", path: "/Media/Extras/Season 1", want: true},
		{name: "正则不跨路径片段匹配", patterns: []string{"Media/Extras"}, filename: "movie.mkv", path: "/Media/Extras"},
		{name: "路径空片段不参与匹配", patterns: []string{"^$"}, filename: "movie.mkv", path: "/Media//Season 1/"},
		{name: "根目录占位符不当作隐藏目录", patterns: []string{"^\\."}, filename: "movie.mkv", path: "."},
		{name: "实际隐藏目录仍可排除", patterns: []string{"^\\."}, filename: "movie.mkv", path: "/Media/.hidden/Season 1", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			syncer := newSyncStrm(nil, 1, "/Media", "", t.TempDir(), SyncStrmConfig{
				VideoExt:           []string{".mkv"},
				ExcludeNames:       tt.exact,
				ExcludeNameRegexes: tt.patterns,
			}, false, 0, false, false)
			if syncer == nil {
				t.Fatal("创建同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			file := &SyncFileCache{FileName: tt.filename, Path: tt.path, SourceType: models.SourceType115}
			if excluded := !syncer.ValidFile(file); excluded != tt.want {
				t.Fatalf("文件 %q，父目录 %q：排除 = %v，期望 %v", tt.filename, tt.path, excluded, tt.want)
			}
		})
	}
}

func TestNewSyncStrmRejectsInvalidRegex(t *testing.T) {
	for _, pattern := range []string{"[", "(?=abc)", "a{1001}"} {
		t.Run(pattern, func(t *testing.T) {
			syncer := newSyncStrm(nil, 1, "/Media", "", t.TempDir(), SyncStrmConfig{
				ExcludeNameRegexes: []string{pattern},
			}, false, 0, false, false)
			if syncer != nil {
				syncer.Cancel()
				t.Fatalf("非法正则 %q 不应创建同步器", pattern)
			}
		})
	}
}
