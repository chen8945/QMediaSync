package fnosproxy

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"qmediasync/internal/models"
)

func TestSTRMRestrictedRead(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(root, "valid.strm"), "\ufeff\n# comment\nhttps://example.com/file?sign=unchanged%2Bvalue\n")
	write(filepath.Join(outside, "private.strm"), "https://private.example/file")
	write(filepath.Join(root, "big.strm"), strings.Repeat("a", strmLimit+1))
	if err := os.Symlink(filepath.Join(outside, "private.strm"), filepath.Join(root, "escape.strm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-dir")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		relative string
		valid    bool
	}{
		{name: "正常文件", relative: "valid.strm", valid: true},
		{name: "逃逸文件链接", relative: "escape.strm"},
		{name: "逃逸目录链接", relative: filepath.Join("linked-dir", "private.strm")},
		{name: "超出大小", relative: "big.strm"},
		{name: "穿越", relative: "../private.strm"},
		{name: "根目录", relative: "."},
		{name: "不存在", relative: "missing.strm"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, content, err := openSTRM(root, test.relative, true)
			if (err == nil) != test.valid {
				t.Fatalf("读取有效性：%v", err)
			}
			if test.valid {
				parsed, ok := strmURL(content)
				if !ok || parsed != "https://example.com/file?sign=unchanged%2Bvalue" {
					t.Fatalf("STRM 文本解析：%q %v", parsed, ok)
				}
			}
		})
	}
	for _, content := range []string{"", "file:///etc/passwd", "https://a.example/file\nhttps://b.example/file", "https://user:password@example.com/file"} {
		if _, ok := strmURL(content); ok {
			t.Fatalf("接受无效 STRM：%q", content)
		}
	}
}

func TestSTRMMappingPrecedenceAndCacheFileInvalidation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "movie.strm")
	if err := os.WriteFile(path, []byte("http://qms.internal:12333/115/newurl?pickcode=aaa&userid=user"), 0600); err != nil {
		t.Fatal(err)
	}
	service := NewService(Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil },
		KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://qms.internal:12333"}, nil }})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	inst := &instance{service: service, ctx: ctx, config: models.FnosProxyConfig{FnosURL: "http://fnos", PathMappings: []models.FnosPathMapping{{FnosRoot: "/fnos", LocalRoot: root}}}, cache: newSourceCache()}
	request := httptest.NewRequest("GET", "/Videos/movie/stream?api_key=one", nil)
	source := Source{ItemID: "movie", ID: "source", Path: "/fnos/movie.strm"}
	first, ok := inst.resolveSource(ctx, request, source)
	if !ok || !strings.Contains(first.url, "pickcode=aaa") || len(inst.cache.entries) != 1 {
		t.Fatalf("初次定位失败：%+v %v", first, ok)
	}
	replacement := filepath.Join(root, "replacement.strm")
	if err := os.WriteFile(replacement, []byte("http://qms.internal:12333/115/newurl?pickcode=bbb&userid=user"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	second, ok := inst.resolveSource(ctx, request, source)
	if !ok || !strings.Contains(second.url, "pickcode=bbb") {
		t.Fatal("文件替换后仍复用旧内容")
	}
	other := httptest.NewRequest("GET", "/Videos/movie/stream?api_key=two", nil)
	if _, ok := inst.resolveSource(ctx, other, source); !ok || len(inst.cache.entries) != 2 {
		t.Fatal("凭据没有隔离缓存")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := inst.resolveSource(ctx, request, source); ok {
		t.Fatal("文件删除后仍复用缓存")
	}
	// 明确映射不可读时，不能到已知根找同名文件。
	inst.config.PathMappings[0].LocalRoot = filepath.Join(root, "missing")
	if err := os.WriteFile(path, []byte("https://example.com/file"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := inst.resolveSource(ctx, request, source); ok {
		t.Fatal("忽略明确映射进行同名回退")
	}
	if _, ok := inst.resolveSource(ctx, request, Source{ItemID: "movie", ID: "source", Path: "/fnos-more/movie.strm"}); ok {
		t.Fatal("映射未按路径组件匹配")
	}
}

func TestSTRMAutomaticIdentityRequiresUniqueEvidence(t *testing.T) {
	root := t.TempDir()
	stable := "https://openlist.example/d/movie.mkv?sign=stable"
	for _, name := range []string{"first.strm", "second.strm"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("\""+stable+"\""), 0600); err != nil {
			t.Fatal(err)
		}
	}
	candidates := []Candidate{{Root: root, Path: filepath.Join(root, "first.strm"), URL: stable}}
	service := NewService(Options{OutputRoots: func(context.Context) ([]string, error) { return []string{root}, nil },
		IdentityCandidates: func(context.Context, Source) ([]Candidate, error) { return candidates, nil }})
	inst := &instance{service: service, ctx: t.Context(), cache: newSourceCache()}
	source := Source{ItemID: "movie", ID: "source", Path: "/different/movie.strm"}
	if _, ok := inst.locate(t.Context(), source, []string{root}); ok {
		t.Fatal("没有上游可靠源身份仍自动匹配")
	}
	source.URL = stable
	if _, ok := inst.locate(t.Context(), source, []string{root}); !ok {
		t.Fatal("唯一可靠身份候选无法定位")
	}
	candidates = append(candidates, Candidate{Root: root, Path: filepath.Join(root, "second.strm"), URL: stable})
	if _, ok := inst.locate(t.Context(), source, []string{root}); ok {
		t.Fatal("多个可靠候选仍选择第一条")
	}
}

func TestSTRMWrappedURL(t *testing.T) {
	signed := "https://example.com/a%2Fb?sign=a%2Bb+%27&sign=other&name=%22v%22#part"
	internalQuotes := "https://example.com/file?sign=a'\"`b"
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "普通签名原样", content: signed, want: signed},
		{name: "双引号与BOM注释空行", content: "\ufeff\r\n# comment\r\n \"" + signed + "\" \r\n", want: signed},
		{name: "单引号", content: "'" + signed + "'", want: signed},
		{name: "反引号", content: "`" + signed + "`", want: signed},
		{name: "链接内部引号保留", content: "\"" + internalQuotes + "\"", want: internalQuotes},
		{name: "裸链接末尾双引号保留", content: signed + "\"", want: signed + "\""},
		{name: "裸链接末尾单引号保留", content: signed + "'", want: signed + "'"},
		{name: "裸链接末尾反引号保留", content: signed + "`", want: signed + "`"},
		{name: "包裹内签名末尾引号保留", content: "'" + signed + "''", want: signed + "'"},
		{name: "同一链接重复", content: signed + "\n\"" + signed + "\"", want: signed},
		{name: "不匹配引号", content: "\"" + signed + "'"},
		{name: "缺少末尾引号", content: "\"" + signed},
		{name: "单个引号", content: "'"},
		{name: "空包裹", content: "\"\""},
		{name: "嵌套包裹", content: "\"'" + signed + "'\""},
		{name: "包裹内空格", content: "\" " + signed + " \""},
		{name: "包裹内换行", content: "\"" + signed + "\n\""},
		{name: "包裹内回车", content: "\"" + signed + "\r\""},
		{name: "包裹内NUL", content: "\"" + signed + "\x00\""},
		{name: "包裹内凭据", content: "'https://user:password@example.com/file'"},
		{name: "包裹内文件路径", content: "'file:///etc/passwd'"},
		{name: "不同链接", content: "\"" + signed + "\"\n'https://other.example/file'"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := strmURL(test.content)
			if got != test.want || ok != (test.want != "") {
				t.Fatalf("STRM 文本解析：%q %v，期望 %q", got, ok, test.want)
			}
		})
	}
}
