package models

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/helpers"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

func embyProviderBatchFiles(source SourceType) []EmbyFrozenFile {
	files := make([]EmbyFrozenFile, 3)
	for i, suffix := range []string{".mkv", ".nfo", ".jpg"} {
		file := embyProviderTestFile(source)
		file.FileID, file.FileName = strconv.Itoa(10+i), "Movie,\nPart1"+suffix
		if source == SourceTypeOpenList {
			file.FileID = path.Join(file.Path, file.FileName)
			file.OpenlistObjectID = "object-" + strconv.Itoa(i)
			file.PickCode = sanitizeEmbyEvidencePath(helpers.MakeOpenListUrl("https://openlist.example", "test-sign", file.FileID))
		}
		files[i] = file
	}
	return files
}

func embyProviderBatchList115(files []EmbyFrozenFile) *v115open.FileListResp {
	list := &v115open.FileListResp{Count: len(files), PathStr: "media", Path: []v115open.FileParentPath{{FileId: json.Number("7")}}}
	for _, file := range files {
		list.Data = append(list.Data, v115open.File{FileId: file.FileID, Pid: file.ParentID, FileName: file.FileName, FileCategory: v115open.TypeFile, FileSize: file.FileSize, Utime: file.MTime, Sha1: file.SHA1})
	}
	return list
}

func TestEmbyDeleteProviderBatch115UsesOneInventoryAndOneWrite(t *testing.T) {
	files := embyProviderBatchFiles(SourceType115)
	listCalls, deleteCalls, guardCalls := 0, 0, 0
	stub := &embyDelete115Stub{err: errors.New("unexpected per-file detail"), list: func(offset int) (*v115open.FileListResp, error) {
		listCalls++
		return embyProviderBatchList115(files), nil
	}, delete: func(ids []string, parent string) (bool, error) {
		deleteCalls++
		if !slices.Equal(ids, []string{"10", "11", "12"}) || parent != "7" {
			t.Fatalf("wrong batch %v / %s", ids, parent)
		}
		return true, nil
	}}
	p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: stub}
	ok, err := p.DeleteBatch(t.Context(), files, func() error { guardCalls++; return nil })
	if !ok || err != nil || listCalls != 1 || deleteCalls != 1 || guardCalls != 1 {
		t.Fatalf("ok=%v err=%v lists=%d writes=%d guards=%d", ok, err, listCalls, deleteCalls, guardCalls)
	}
}

func TestEmbyDeleteProviderBatchRejectsUnsafeGroupingAndBudgets(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func([]EmbyFrozenFile) []EmbyFrozenFile
	}{
		{name: "empty", mutate: func([]EmbyFrozenFile) []EmbyFrozenFile { return nil }},
		{name: "different parent", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].ParentID = "8"; return f }},
		{name: "different directory", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].Path = "/other"; return f }},
		{name: "different account", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].AccountID = 2; return f }},
		{name: "changed principal", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].AccountIdentity = "other"; return f }},
		{name: "different source", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].SourceType = SourceTypeBaiduPan; return f }},
		{name: "duplicate ID", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].FileID = f[0].FileID; return f }},
		{name: "duplicate name", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].FileName = f[0].FileName; return f }},
		{name: "ID separator injection", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { f[1].FileID = "11,99"; return f }},
		{name: "too many", mutate: func(f []EmbyFrozenFile) []EmbyFrozenFile { return make([]EmbyFrozenFile, embyDeleteBatchMaxFiles+1) }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner"}
			if ok, err := p.DeleteBatch(t.Context(), tt.mutate(embyProviderBatchFiles(SourceType115)), func() error { return nil }); ok || err == nil {
				t.Fatalf("unsafe batch accepted: ok=%v err=%v", ok, err)
			}
		})
	}
	for _, source := range []SourceType{SourceTypeBaiduPan, SourceTypeOpenList} {
		t.Run(string(source)+" byte budget", func(t *testing.T) {
			files := embyProviderBatchFiles(source)
			files[0].FileName = strings.Repeat("中", embyDeleteBatchMaxBytes)
			p := &embyDeleteProvider{source: source, accountID: 1, accountIdentity: "owner"}
			if ok, err := p.DeleteBatch(t.Context(), files, func() error { return nil }); ok || err == nil {
				t.Fatal("oversized body reached provider")
			}
		})
	}
}

func TestEmbyDeleteProviderBatchRejectsChangedMembersAndQueueGuard(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode string
	}{
		{name: "replacement identity", mode: "hash"},
		{name: "directory cannot masquerade as video", mode: "directory"},
		{name: "missing original", mode: "missing"},
		{name: "expired after queue wait", mode: "guard"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := embyProviderBatchFiles(SourceType115)
			stub := &embyDelete115Stub{list: func(int) (*v115open.FileListResp, error) {
				list := embyProviderBatchList115(files)
				switch tt.mode {
				case "hash":
					list.Data[1].Sha1 = "replacement"
				case "directory":
					list.Data[1].FileCategory = v115open.TypeDir
				case "missing":
					list.Data, list.Count = list.Data[:2], 2
				}
				return list, nil
			}, delete: func([]string, string) (bool, error) { t.Error("unsafe write sent"); return true, nil }}
			p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: stub}
			guards := 0
			_, err := p.DeleteBatch(t.Context(), files, func() error {
				guards++
				if guards == 1 {
					return ErrEmbyDeleteDisabled
				}
				return nil
			})
			if err == nil || tt.mode == "guard" && !errors.Is(err, ErrEmbyDeleteDisabled) {
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}

func embyProviderDirectoryScope(source SourceType) EmbyDirectoryScope {
	root := embyProviderTestFile(source)
	root.FileName, root.SHA1, root.MTime = "Movie", "", 0
	return EmbyDirectoryScope{Root: root, MediaType: "Movie", ItemID: "1", EvidenceKind: "physical_ancestor", Ancestors: []EmbyDirectoryAncestor{
		{FileID: "0", Path: "/"}, {FileID: "7", ParentID: "0", Path: "/media"},
	}}
}

func TestEmbyDeleteProvider115DirectoryOriginalIdentity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*v115open.FileDetail)
		absent bool
		wantOK bool
	}{
		{name: "stable directory ignores child size and mtime", wantOK: true},
		{name: "moved parent", mutate: func(d *v115open.FileDetail) { d.Paths[1].FileId = "8" }},
		{name: "renamed original", mutate: func(d *v115open.FileDetail) { d.FileName = "Moved" }},
		{name: "same path replacement", mutate: func(d *v115open.FileDetail) { d.FileId = "11" }},
		{name: "changed ancestor path", mutate: func(d *v115open.FileDetail) { d.Paths[1].Name = "Other" }},
		{name: "file instead of directory", mutate: func(d *v115open.FileDetail) { d.FileCategory = v115open.TypeFile }},
		{name: "original absent", absent: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scope := embyProviderDirectoryScope(SourceType115)
			detail := &v115open.FileDetail{FileId: "10", FileName: "Movie", FileCategory: v115open.TypeDir, FileSizeByte: 99999, Utime: "1800000000", Path: "media", Paths: []v115open.FileDetailPath{{FileId: "0"}, {FileId: "7", Name: "media"}}}
			if tt.mutate != nil {
				tt.mutate(detail)
			}
			writes := 0
			stub := &embyDelete115Stub{detail: detail, delete: func(ids []string, parent string) (bool, error) {
				writes++
				if !slices.Equal(ids, []string{"10"}) || parent != "7" {
					t.Errorf("wrong directory request: %v/%s", ids, parent)
				}
				return true, nil
			}}
			if tt.absent {
				stub.err = v115open.NewOpenAPIError(430004, "gone")
			}
			p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: stub}
			ok, err := p.DeleteDirectory(t.Context(), scope, func() error { return nil })
			if ok != tt.wantOK || (err == nil) != tt.wantOK || errors.Is(err, ErrEmbyRemoteFileAbsent) != tt.absent || writes != map[bool]int{true: 1, false: 0}[tt.wantOK] {
				t.Fatalf("ok=%v writes=%d err=%v", ok, writes, err)
			}
		})
	}
}

func TestEmbyDeleteProviderBaiduBatchHTTP(t *testing.T) {
	embyProviderTestLogger(t)
	for _, body := range []string{`{"errno":0}`, `{"errno":0,"info":[{"errno":0},{"errno":-9}]}`} {
		t.Run(body, func(t *testing.T) {
			original := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = original })
			files := embyProviderBatchFiles(SourceTypeBaiduPan)
			reads, writes := 0, 0
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Query().Get("method") {
				case "list":
					reads++
					entries := make([]*baidupan.FileInfo, 0, len(files))
					for i, f := range files {
						entries = append(entries, &baidupan.FileInfo{FsId: uint64(10 + i), Path: path.Join(f.Path, f.FileName), ServerFilename: f.FileName, Size: uint64(f.FileSize), ServerMtime: uint64(f.MTime), Md5: f.SHA1})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": entries})
				case "filemanager":
					writes++
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Fatal(err)
					}
					r.Body = io.NopCloser(strings.NewReader(string(data)))
					size, _ := EmbyDeleteBatchPayloadSize(files)
					if len(data) != size {
						t.Errorf("body=%d calculated=%d", len(data), size)
					}
					var names []string
					if err := json.Unmarshal([]byte(r.FormValue("filelist")), &names); err != nil {
						t.Fatal(err)
					}
					if len(names) != 3 || names[1] != path.Join(files[1].Path, files[1].FileName) || r.FormValue("async") != "0" {
						t.Errorf("wrong payload %v", names)
					}
					_, _ = io.WriteString(w, body)
				default:
					t.Errorf("unexpected repeated read %s", r.URL.String())
				}
			})}
			p := &embyDeleteProvider{source: SourceTypeBaiduPan, accountID: 1, accountIdentity: "owner", baidu: baidupan.NewBaiDuPanClientWithToken("test")}
			ok, err := p.DeleteBatch(t.Context(), files, func() error { return nil })
			wantOK := body == `{"errno":0}`
			if ok != wantOK || (err == nil) != wantOK || reads != 1 || writes != 1 {
				t.Fatalf("ok=%v err=%v reads=%d writes=%d", ok, err, reads, writes)
			}
		})
	}
}

func TestEmbyDeleteProviderOpenListBatchHTTP(t *testing.T) {
	embyProviderTestLogger(t)
	files := embyProviderBatchFiles(SourceTypeOpenList)
	reads, details, writes := 0, 0, 0
	entries := make([]map[string]any, len(files))
	for i, f := range files {
		entries[i] = map[string]any{"name": f.FileName, "size": f.FileSize, "id": f.OpenlistObjectID, "modified": time.Unix(f.MTime, 0).UTC().Format(time.RFC3339), "hash_info": map[string]string{"sha1": f.OpenlistSHA1}}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/fs/list":
			reads++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": entries, "total": len(entries)}})
		case "/api/fs/get":
			details++
			var body struct {
				Path string `json:"path"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			for i, f := range files {
				if body.Path == f.FileID {
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": entries[i]})
					return
				}
			}
			t.Errorf("unexpected detail %q", body.Path)
		case "/api/fs/remove":
			writes++
			data, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			size, _ := EmbyDeleteBatchPayloadSize(files)
			if len(data) != size {
				t.Errorf("body=%d calculated=%d", len(data), size)
			}
			var body struct {
				Dir   string   `json:"dir"`
				Names []string `json:"names"`
			}
			if err := json.Unmarshal(data, &body); err != nil {
				t.Error(err)
			}
			if body.Dir != "/media" || len(body.Names) != 3 || body.Names[1] != files[1].FileName {
				t.Errorf("unexpected batch %+v", body)
			}
			_, _ = io.WriteString(w, `{"code":200,"message":"success"}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := openlist.NewTemporaryClient(server.URL, "user", "password", "token")
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	p := &embyDeleteProvider{source: SourceTypeOpenList, accountID: 1, accountIdentity: "owner", openlist: client}
	ok, err := p.DeleteBatch(t.Context(), files, func() error { return nil })
	if !ok || err != nil || reads != 1 || details != 3 || writes != 1 {
		t.Fatalf("ok=%v err=%v reads=%d details=%d writes=%d", ok, err, reads, details, writes)
	}
	if p.SupportsDirectoryDelete() {
		t.Fatal("unproven directory capability")
	}
	if _, err := p.DeleteDirectory(context.Background(), embyProviderDirectoryScope(SourceTypeOpenList), func() error { return nil }); !errors.Is(err, ErrEmbyDeleteUnsupported) {
		t.Fatal(err)
	}
}

func TestEmbyDeleteProviderBaiduDirectoryUsesOriginalFSID(t *testing.T) {
	embyProviderTestLogger(t)
	for _, tt := range []struct {
		name, mode string
		wantOK     bool
	}{
		{name: "success and original ID absence", wantOK: true},
		{name: "replacement", mode: "replacement"},
		{name: "moved original", mode: "moved"},
		{name: "parent replaced", mode: "parent"},
		{name: "missing stable identity", mode: "weak"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = original })
			reads, writes := 0, 0
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Query().Get("method") {
				case "filemetas":
					reads++
					id := r.URL.Query().Get("fsids")
					if id == "[10]" && writes > 0 {
						_, _ = io.WriteString(w, `{"errno":0,"list":[]}`)
						return
					}
					f := map[string]any{"fs_id": 10, "path": "/media/Movie", "filename": "Movie", "isdir": 1}
					if id == "[7]" {
						f = map[string]any{"fs_id": 7, "path": "/media", "filename": "media", "isdir": 1}
						if tt.mode == "parent" {
							f["fs_id"] = 8
						}
					} else if id != "[10]" {
						t.Errorf("wrong original ID: %s", id)
					}
					if id == "[10]" {
						switch tt.mode {
						case "replacement":
							f["fs_id"] = 11
						case "moved":
							f["path"] = "/elsewhere/Movie"
						case "weak":
							delete(f, "fs_id")
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": []any{f}})
				case "filemanager":
					writes++
					if r.FormValue("filelist") != `["/media/Movie"]` || r.FormValue("async") != "0" {
						t.Errorf("wrong directory request %v", r.Form)
					}
					_, _ = io.WriteString(w, `{"errno":0}`)
				default:
					t.Errorf("unexpected scan %s", r.URL.Path)
				}
			})}
			p := &embyDeleteProvider{source: SourceTypeBaiduPan, accountID: 1, accountIdentity: "owner", baidu: baidupan.NewBaiDuPanClientWithToken("test")}
			scope := embyProviderDirectoryScope(SourceTypeBaiduPan)
			ok, err := p.DeleteDirectory(t.Context(), scope, func() error { return nil })
			if ok != tt.wantOK || (err == nil) != tt.wantOK {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if tt.wantOK {
				if _, err := p.StatDirectory(t.Context(), scope); !errors.Is(err, ErrEmbyRemoteFileAbsent) {
					t.Fatalf("original absent=%v", err)
				}
				if reads != 3 || writes != 1 {
					t.Fatalf("reads=%d writes=%d", reads, writes)
				}
			} else if writes != 0 {
				t.Fatalf("unsafe directory delete sent %d times", writes)
			}
		})
	}
}

func TestEmbyDeleteProviderPreflightExpiryAndGuardCancellation(t *testing.T) {
	called := false
	guard := embyDeleteFreshGuard(t.Context(), time.Now().Add(-time.Second), func() error { called = true; return nil })
	if err := guard(); !errors.Is(err, ErrEmbyDeleteUnverified) || called {
		t.Fatalf("expired guard called=%v err=%v", called, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	guard = embyDeleteFreshGuard(ctx, time.Now().Add(time.Minute), func() error { cancel(); return nil })
	if err := guard(); !errors.Is(err, context.Canceled) {
		t.Fatalf("guard cancellation accepted: %v", err)
	}
}
