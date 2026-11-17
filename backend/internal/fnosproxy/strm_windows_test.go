package fnosproxy

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"qmediasync/internal/models"
)

func TestSTRMWindowsPathFallbackOrder(t *testing.T) {
	const stable = "http://qms.internal:12333/115/newurl?pickcode=aaa&userid=user"
	for _, name := range []string{
		"stale-slash-path-keeps-identity", "new-root-keeps-identity", "no-identity-capability", "no-source-identity", "empty-identity",
		"explicit-mapping-failure", "original-direct-failure", "ambiguous-identity", "query-error", "query-canceled",
		"unsafe-identity-file", "filtered-identity", "dot-component", "traversal-component", "file-trailing-separator",
	} {
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "剧集")
			other := filepath.Join(t.TempDir(), "另一输出根")
			for _, directory := range []string{root, other} {
				if err := os.Mkdir(directory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			local := filepath.Join(root, "video.strm")
			identity := filepath.Join(other, "identity.strm")
			for _, file := range []string{local, identity} {
				if err := os.WriteFile(file, []byte(stable), 0600); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			source := Source{ItemID: "movie", ID: "source", Path: filepath.ToSlash(local), URL: stable}
			outputRoots := []string{root, other}
			candidates := []Candidate{}
			var queryErr error
			var mappings []models.FnosPathMapping
			want, wantQueries := local, 1
			identityAvailable := true
			switch name {
			case "stale-slash-path-keeps-identity":
				source.Path = filepath.ToSlash(filepath.Join(root, "old.strm"))
				candidates = []Candidate{{Root: other, Path: identity, URL: stable}}
				want = identity
			case "new-root-keeps-identity":
				outputRoots[0] = filepath.ToSlash(root)
				source.Path = local
				candidates = []Candidate{{Root: other, Path: identity, URL: stable}}
				want = identity
			case "no-identity-capability":
				identityAvailable, wantQueries = false, 0
			case "no-source-identity":
				source.URL, wantQueries = "", 0
			case "empty-identity":
				outputRoots[0] = filepath.ToSlash(root) + "/"
				source.Path = filepath.ToSlash(root) + `\video.strm`
			case "explicit-mapping-failure":
				mappings = []models.FnosPathMapping{{FnosRoot: filepath.ToSlash(root), LocalRoot: filepath.Join(other, "missing")}}
				candidates = []Candidate{{Root: other, Path: identity, URL: stable}}
				want, wantQueries = "", 0
			case "original-direct-failure":
				source.Path = filepath.Join(root, "missing.strm")
				candidates = []Candidate{{Root: other, Path: identity, URL: stable}}
				want, wantQueries = "", 0
			case "ambiguous-identity":
				candidates = []Candidate{{Root: root, Path: local, URL: stable}, {Root: other, Path: identity, URL: stable}}
				want = ""
			case "query-error":
				queryErr, want = errors.New("账本读取失败"), ""
			case "query-canceled":
				want = ""
			case "unsafe-identity-file":
				if err := os.WriteFile(identity, []byte(strings.Repeat("a", strmLimit+1)), 0600); err != nil {
					t.Fatal(err)
				}
				candidates = []Candidate{{Root: other, Path: identity, URL: stable}}
				want = ""
			case "filtered-identity":
				candidates = []Candidate{{Root: other, Path: identity, URL: stable + "wrong"}}
				want = ""
			case "dot-component":
				source.Path = filepath.ToSlash(root) + "/./video.strm"
				want = ""
			case "traversal-component":
				source.Path = filepath.ToSlash(root) + "/elsewhere/../video.strm"
				want = ""
			case "file-trailing-separator":
				source.Path += "/"
				want, wantQueries = "", 0
			}
			originalRoots := slices.Clone(outputRoots)
			rootQueries, identityQueries := 0, 0
			options := Options{
				OutputRoots: func(context.Context) ([]string, error) {
					rootQueries++
					return outputRoots, nil
				},
				KnownOrigins: func(context.Context) ([]string, error) { return []string{"http://qms.internal:12333"}, nil },
			}
			if identityAvailable {
				options.IdentityCandidates = func(context.Context, Source) ([]Candidate, error) {
					identityQueries++
					if name == "query-canceled" {
						cancel()
					}
					return candidates, queryErr
				}
			}
			inst := &instance{service: newTestService(t, options), ctx: ctx,
				config: models.FnosProxyConfig{FnosURL: "http://fnos", PathMappings: mappings}, cache: newSourceCache()}
			request := httptest.NewRequest("GET", "/Videos/movie/stream?api_key=one", nil)
			record, ok := inst.resolveSource(ctx, request, source)
			if ok != (want != "") || ok && (filepath.Join(record.root, record.relative) != want || record.url != stable || record.source != source) {
				t.Fatalf("定位=%+v 成功=%v，期望文件=%q", record, ok, want)
			}
			wantRootQueries := 1
			if ok {
				wantRootQueries = 2
			}
			if name == "file-trailing-separator" {
				wantRootQueries = 0
			}
			if rootQueries != wantRootQueries || identityQueries != wantQueries || !slices.Equal(outputRoots, originalRoots) {
				t.Fatalf("根查询=%d 身份查询=%d 根被改写=%v", rootQueries, identityQueries, !slices.Equal(outputRoots, originalRoots))
			}
			if ok {
				cached, cachedOK := inst.resolveSource(ctx, request, source)
				if !cachedOK || cached.url != stable || filepath.Join(cached.root, cached.relative) != want || identityQueries != wantQueries {
					t.Fatalf("缓存未保留准确来源或重复查询身份：%+v %v 查询=%d", cached, cachedOK, identityQueries)
				}
			}
		})
	}
}

func TestSTRMWindowsRootsValidateBeforeClean(t *testing.T) {
	root := filepath.Join(t.TempDir(), "剧集")
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "native", raw: root, want: true},
		{name: "slash", raw: filepath.ToSlash(root), want: true},
		{name: "mixed-repeat-trailing", raw: filepath.ToSlash(filepath.Dir(root)) + `\//剧集/`, want: true},
		{name: "dot", raw: filepath.ToSlash(root) + "/."},
		{name: "traversal", raw: filepath.ToSlash(root) + "/other/.."},
		{name: "control", raw: filepath.ToSlash(root) + "/bad\x00"},
		{name: "reserved-character", raw: filepath.ToSlash(root) + "/bad?"},
		{name: "trailing-dot", raw: filepath.ToSlash(root) + "/bad."},
		{name: "relative", raw: "local/剧集"},
	} {
		t.Run(test.name, func(t *testing.T) {
			inst := &instance{service: newTestService(t, Options{OutputRoots: func(context.Context) ([]string, error) {
				return []string{test.raw, test.raw}, nil
			}})}
			roots, err := inst.roots(t.Context())
			if err != nil || (len(roots.allowed) == 1) != test.want || test.want && roots.allowed[0] != root {
				t.Fatalf("根=%+v 错误=%v", roots, err)
			}
			if test.want && test.raw != root && len(roots.legacy) != 0 {
				t.Fatal("新增规范化根混入原直接定位范围")
			}
		})
	}
}
