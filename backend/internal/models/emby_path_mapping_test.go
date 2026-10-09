package models

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestEmbyPathMappingResolveUsesDirectoryBoundary(t *testing.T) {
	localRoot := t.TempDir()
	for _, test := range []struct {
		name, root, candidate, relative string
		matched                         bool
	}{
		{"nested file", "/emby/strm", "/emby/strm/电影/视频.strm", "电影/视频.strm", true},
		{"exact root", "/emby/strm/", "/emby/strm", ".", true},
		{"filesystem root", "/", "/film.strm", "film.strm", true},
		{"sibling prefix", "/emby/strm", "/emby/strm-other/film.strm", "", false},
		{"parent traversal", "/emby/strm", "/emby/strm/../film.strm", "", false},
		{"dot component", "/emby/strm", "/emby/strm/./film.strm", "", false},
		{"mixed separators", "/emby/strm", `/emby/strm/..\film.strm`, "", false},
		{"Windows mount", `D:\Emby\STRM`, `d:\emby\strm\Film\video.strm`, "Film/video.strm", true},
		{"Windows root", `D:\`, `d:\video.strm`, "video.strm", true},
		{"UNC mount", `\\server\share\STRM`, `\\SERVER\share\strm\Film\video.strm`, "Film/video.strm", true},
		{"UNC traversal", `\\server\share\STRM`, `\\server\share\STRM\..\film.strm`, "", false},
		{"wrong drive", `D:\Emby`, `E:\Emby\video.strm`, "", false},
		{"Windows trailing dot traversal", `D:\Emby`, `D:\Emby\.. \video.strm`, "", false},
		{"Windows stream", `D:\Emby`, `D:\Emby\video.strm:hidden`, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			mapping := EmbyPathMapping{EmbyRoot: test.root, LocalRoot: localRoot}
			relative, matched := mapping.RelativePath(test.candidate)
			if matched != test.matched || relative != test.relative {
				t.Fatalf("relative = %q, %v; want %q, %v", relative, matched, test.relative, test.matched)
			}
			local, matched := mapping.ResolveLocalPath(test.candidate)
			if matched != test.matched || matched && local != filepath.Join(localRoot, filepath.FromSlash(test.relative)) {
				t.Fatalf("local = %q, %v", local, matched)
			}
		})
	}
}

func TestNormalizeEmbyPathMappings(t *testing.T) {
	localRoot := t.TempDir()
	for _, test := range []struct {
		name    string
		roots   []string
		local   string
		invalid bool
	}{
		{name: "empty"},
		{name: "separate roots", roots: []string{"/emby/films/", "/emby/films-old"}},
		{name: "duplicate", roots: []string{"/emby/films", "/emby/films/"}, invalid: true},
		{name: "nested", roots: []string{"/emby", "/emby/films"}, invalid: true},
		{name: "Windows overlap", roots: []string{`C:\Emby`, `c:\emby\films`}, invalid: true},
		{name: "relative", roots: []string{"emby/films"}, invalid: true},
		{name: "URL", roots: []string{"https://host/films"}, invalid: true},
		{name: "control", roots: []string{"/emby\x00/films"}, invalid: true},
		{name: "traversal", roots: []string{"/emby/../films"}, invalid: true},
		{name: "incomplete UNC", roots: []string{`\\server`}, invalid: true},
		{name: "relative local", roots: []string{"/emby"}, local: "relative", invalid: true},
		{name: "local traversal", roots: []string{"/emby"}, local: filepath.Join(localRoot, "child") + string(filepath.Separator) + "..", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			local := test.local
			if local == "" {
				local = localRoot
			}
			mappings := make([]EmbyPathMapping, len(test.roots))
			for i, root := range test.roots {
				mappings[i] = EmbyPathMapping{EmbyRoot: root, LocalRoot: local, SyncPathID: 7}
			}
			normalized, err := NormalizeEmbyPathMappings(mappings)
			if (err != nil) != test.invalid {
				t.Fatalf("normalize error = %v", err)
			}
			if err == nil && len(normalized) > 0 && normalized[0].SyncPathID != 7 {
				t.Fatal("lost explicit sync root")
			}
		})
	}
}

func TestEmbyConfigPathMappingsDecode(t *testing.T) {
	for _, raw := range []string{"", "[]", "null"} {
		config := EmbyConfig{PathMappingsJSON: raw}
		if err := config.AfterFind(nil); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &value); err != nil {
			t.Fatal(err)
		}
		if string(value["path_mappings"]) != "[]" {
			t.Fatalf("empty mappings = %s", value["path_mappings"])
		}
		if _, exists := value["path_mappings_json"]; exists {
			t.Fatal("storage JSON exposed")
		}
	}
	config := EmbyConfig{PathMappingsJSON: "{broken"}
	if err := config.AfterFind(nil); err == nil {
		t.Fatal("corrupt mappings accepted")
	}
}

func TestInferEmbyPathMappingUsesCompleteRelativeComponents(t *testing.T) {
	localRoot := t.TempDir()
	for _, tc := range []struct {
		name, embyPath, relative, wantRoot string
	}{
		{"POSIX", "/media/Films/A/A.strm", "Films/A/A.strm", "/media"},
		{"single file", "/media/A.strm", "A.strm", "/media"},
		{"filesystem root", "/A.strm", "A.strm", "/"},
		{"Windows case", `d:\Media\films\A\a.strm`, "Films/A/A.strm", "D:/Media"},
		{"UNC", `\\NAS\Media\Films\A\A.strm`, "Films/A/A.strm", "//NAS/Media"},
		{"component prefix", "/media/ExtraFilms/A/A.strm", "Films/A/A.strm", ""},
		{"same filename different tree", "/media/Other/A.strm", "Films/A/A.strm", ""},
		{"traversal", "/media/Films/../A/A.strm", "Films/A/A.strm", ""},
		{"UNC share is not a relative directory", `\\NAS\Media\A.strm`, "Media/A.strm", ""},
		{"outside local root", "/media/A.strm", "../A.strm", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := filepath.Join(localRoot, filepath.FromSlash(tc.relative))
			mapping, ok := inferEmbyPathMapping(tc.embyPath, localRoot, local)
			if ok != (tc.wantRoot != "") || ok && (mapping.EmbyRoot != tc.wantRoot || mapping.LocalRoot != localRoot) {
				t.Fatalf("mapping=%+v matched=%v, want root=%q", mapping, ok, tc.wantRoot)
			}
		})
	}
}
