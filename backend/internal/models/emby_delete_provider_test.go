package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

type embyDelete115Stub struct {
	detail *v115open.FileDetail
	err    error
	list   func(int) (*v115open.FileListResp, error)
	delete func([]string, string) (bool, error)
}

func (s *embyDelete115Stub) GetFsDetailByCidForDeletion(context.Context, string) (*v115open.FileDetail, error) {
	return s.detail, s.err
}
func (s *embyDelete115Stub) GetFsListWithOptions(_ context.Context, _ string, cur, dirs, show bool, offset, limit int, options v115open.FileListOptions) (*v115open.FileListResp, error) {
	if !cur || !dirs || !show || limit != embyDeleteListPageSize || options.Order != "file_name" {
		return nil, errors.New("filtered list")
	}
	return s.list(offset)
}
func (s *embyDelete115Stub) DelOnceGuarded(_ context.Context, ids []string, parent string, guard func() error) (bool, error) {
	if err := guard(); err != nil {
		return false, err
	}
	return s.delete(ids, parent)
}

func embyProviderTestFile(source SourceType) EmbyFrozenFile {
	file := EmbyFrozenFile{SourceType: source, AccountID: 1, AccountIdentity: "owner", FileID: "10", ParentID: "7", Path: "/media", FileName: "Movie,\nPart1.mkv", FileSize: 100, MTime: 1700000000, SHA1: "hash"}
	if source == SourceTypeOpenList {
		file.FileID, file.ParentID = file.Path+"/"+file.FileName, file.Path
		file.SHA1, file.OpenlistObjectID, file.OpenlistSHA1 = "", "object-A", "hash"
		file.PickCode = sanitizeEmbyEvidencePath(helpers.MakeOpenListUrl("https://openlist.example", "test-sign", file.FileID))
	}
	return file
}

func embyProviderTestLogger(t *testing.T) {
	t.Helper()
	a, b, c := helpers.AppLogger, helpers.OpenListLog, helpers.BaiduPanLog
	l := &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.AppLogger, helpers.OpenListLog, helpers.BaiduPanLog = l, l, l
	t.Cleanup(func() { helpers.AppLogger, helpers.OpenListLog, helpers.BaiduPanLog = a, b, c })
}

func TestEmbyDeleteProviderConstructorOriginalAccount(t *testing.T) {
	setupEmbySnapshotModelTest(t)
	var account Account
	if err := db.Db.First(&account).Error; err != nil {
		t.Fatal(err)
	}
	file := embyProviderTestFile(SourceType115)
	file.AccountID, file.AccountIdentity = account.ID, EmbyAccountIdentity(account)
	if _, err := NewEmbyDeleteProvider(file); err != nil {
		t.Fatal(err)
	}
	file.AccountID = 999
	if _, err := NewEmbyDeleteProvider(file); err == nil {
		t.Fatal("missing original account fell back")
	}
	file.AccountID = account.ID
	file.AccountIdentity = "different-owner"
	if _, err := NewEmbyDeleteProvider(file); !errors.Is(err, ErrEmbyDeleteUnverified) {
		t.Fatal(err)
	}
	file.SourceType = SourceType("unsupported")
	if _, err := NewEmbyDeleteProvider(file); !errors.Is(err, ErrEmbyDeleteUnsupported) {
		t.Fatal(err)
	}
}

func TestEmbyDeleteProvider115IdentityAndResults(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*v115open.FileDetail)
		detailErr  error
		ok         bool
		deleteErr  error
		wantAbsent bool
		wantCalls  int
		wantOK     bool
	}{
		{name: "exact file and actual parent", ok: true, wantCalls: 1, wantOK: true},
		{name: "false is failure", wantCalls: 1},
		{name: "provider failure", deleteErr: errors.New("https://secret.invalid/?token=secret"), wantCalls: 1},
		{name: "different parent", mutate: func(d *v115open.FileDetail) { d.Paths[0].FileId = "other" }},
		{name: "replacement hash", mutate: func(d *v115open.FileDetail) { d.Sha1 = "replacement" }},
		{name: "replacement mtime", mutate: func(d *v115open.FileDetail) { d.Utime = "1700000001" }},
		{name: "directory never deleted", mutate: func(d *v115open.FileDetail) { d.FileCategory = v115open.TypeDir }},
		{name: "known absent", detailErr: v115open.NewOpenAPIError(430004, "gone"), wantAbsent: true},
		{name: "unknown notfound text", detailErr: errors.New("not found")},
		{name: "http failure is not absence", detailErr: &v115open.OpenAPIError{Code: 430004, HTTPStatus: 500}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := embyProviderTestFile(SourceType115)
			detail := &v115open.FileDetail{FileId: file.FileID, FileName: file.FileName, Path: "media", Paths: []v115open.FileDetailPath{{FileId: "7"}}, Sha1: "hash", Utime: "1700000000", FileSizeByte: 100, FileCategory: v115open.TypeFile}
			if tc.mutate != nil {
				tc.mutate(detail)
			}
			calls := 0
			client := &embyDelete115Stub{detail: detail, err: tc.detailErr, delete: func(ids []string, parent string) (bool, error) {
				calls++
				if len(ids) != 1 || ids[0] != "10" || parent != "7" {
					t.Fatalf("wrong delete args %v %s", ids, parent)
				}
				return tc.ok, tc.deleteErr
			}}
			p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: client}
			ok, err := p.Delete(t.Context(), file, func() error { return nil })
			if ok != tc.wantOK || calls != tc.wantCalls || errors.Is(err, ErrEmbyRemoteFileAbsent) != tc.wantAbsent || (!ok && err == nil) {
				t.Fatalf("ok=%v calls=%d error=%v", ok, calls, err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("leaked remote error")
			}
		})
	}
}

func TestEmbyDeleteProvider115CompleteUnfilteredList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		wantErr bool
	}{
		{name: "two pages include unknown file and subdir"},
		{name: "empty before advertised end", mode: "empty", wantErr: true},
		{name: "count changes", mode: "count", wantErr: true},
		{name: "duplicate page", mode: "duplicate", wantErr: true},
		{name: "wrong parent", mode: "parent", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &embyDelete115Stub{list: func(offset int) (*v115open.FileListResp, error) {
				r := &v115open.FileListResp{Count: 1001, PathStr: "media", Path: []v115open.FileParentPath{{FileId: json.Number("7")}}, Offset: json.Number(strconv.Itoa(offset))}
				if offset == 0 {
					for i := range 1000 {
						r.Data = append(r.Data, v115open.File{FileId: strconv.Itoa(i + 1), Pid: "7", FileName: fmt.Sprintf("file-%d.unknown", i), FileCategory: v115open.TypeFile})
					}
				} else {
					r.Data = []v115open.File{{FileId: "1001", Pid: "7", FileName: "other season", FileCategory: v115open.TypeDir}}
				}
				if offset > 0 {
					switch tc.mode {
					case "empty":
						r.Data = nil
					case "count":
						r.Count = 1002
					case "duplicate":
						r.Data[0].FileId = "1"
					case "parent":
						r.Data[0].Pid = "8"
					}
				}
				return r, nil
			}}
			p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: client}
			files, err := p.List(t.Context(), embyProviderTestFile(SourceType115))
			if (err != nil) != tc.wantErr {
				t.Fatalf("files=%d err=%v", len(files), err)
			}
			if err == nil && (len(files) != 1001 || !files[1000].IsDir) {
				t.Fatal("filtered inventory")
			}
		})
	}
}

func TestEmbyDeleteProviderBaiduHTTP(t *testing.T) {
	embyProviderTestLogger(t)
	for _, tc := range []struct {
		name      string
		response  string
		absent    bool
		wantCalls int
		wantOK    bool
	}{
		{name: "full path", response: `{"errno":0,"info":[{"errno":0}]}`, wantCalls: 1, wantOK: true},
		{name: "per-file failure", response: `{"errno":0,"info":[{"errno":-9}]}`, wantCalls: 1},
		{name: "unknown failure", response: `{"errno":987654}`, wantCalls: 1},
		{name: "missing status", response: `{}`, wantCalls: 1},
		{name: "null response", response: `null`, wantCalls: 1},
		{name: "missing item status", response: `{"errno":0,"info":[{}]}`, wantCalls: 1},
		{name: "null item status", response: `{"errno":0,"info":[null]}`, wantCalls: 1},
		{name: "confirmed absence", absent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = original })
			file := embyProviderTestFile(SourceTypeBaiduPan)
			calls := 0
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Query().Get("method") {
				case "list":
					if r.URL.Query().Get("folder") != "0" || r.URL.Query().Get("showempty") != "1" || r.URL.Query().Get("dir") != "/media" {
						t.Errorf("incomplete query %v", r.URL.Query())
					}
					entries := []*baidupan.FileInfo{}
					if !tc.absent {
						entries = append(entries, &baidupan.FileInfo{FsId: 10, Path: file.Path + "/" + file.FileName, ServerFilename: file.FileName, Size: 100, ServerMtime: 1700000000, Md5: "hash"})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"errno": 0, "list": entries})
				case "filemetas":
					_, _ = io.WriteString(w, `{"errno":0,"list":[]}`)
				case "filemanager":
					calls++
					var paths []string
					if err := json.Unmarshal([]byte(r.FormValue("filelist")), &paths); err != nil || len(paths) != 1 || paths[0] != file.Path+"/"+file.FileName {
						t.Errorf("wrong path %v err=%v", paths, err)
					}
					_, _ = io.WriteString(w, tc.response)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			})}
			p := &embyDeleteProvider{source: SourceTypeBaiduPan, accountID: 1, accountIdentity: "owner", baidu: baidupan.NewBaiDuPanClientWithToken("test-token")}
			ok, err := p.Delete(t.Context(), file, func() error { return nil })
			if ok != tc.wantOK || calls != tc.wantCalls || errors.Is(err, ErrEmbyRemoteFileAbsent) != tc.absent || (!ok && err == nil) {
				t.Fatalf("ok=%v calls=%d error=%v", ok, calls, err)
			}
		})
	}
}

func TestEmbyDeleteProviderOpenListHTTP(t *testing.T) {
	embyProviderTestLogger(t)
	for _, tc := range []struct {
		name      string
		response  string
		change    string
		wantCalls int
		wantOK    bool
	}{
		{name: "exact name and directory", response: `{"code":200,"message":"success","data":null}`, wantCalls: 1, wantOK: true},
		{name: "server failure no blind retry", response: `{"code":500,"message":"failed","data":null}`, wantCalls: 1},
		{name: "authentication failure no replay", response: `{"code":401,"message":"expired"}`, wantCalls: 1},
		{name: "missing code", response: `{}`, wantCalls: 1},
		{name: "null body", response: `null`, wantCalls: 1},
		{name: "missing message", response: `{"code":500}`, wantCalls: 1},
		{name: "wrong status type", response: `{"code":"200"}`, wantCalls: 1},
		{name: "path-only identity refused", change: "weak"},
		{name: "case sensitive object ID", change: "case"},
		{name: "fresh list and detail disagree", change: "mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := embyProviderTestFile(SourceTypeOpenList)
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				f := map[string]any{"name": file.FileName, "size": 100, "id": "object-A", "modified": time.Unix(file.MTime, 0).UTC().Format(time.RFC3339), "hash_info": map[string]string{"sha1": "hash"}}
				if tc.change == "case" {
					f["id"] = "object-a"
				}
				switch r.URL.Path {
				case "/api/fs/list":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["refresh"] != true {
						t.Error("list was not fresh")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"content": []any{f}, "total": 1}})
				case "/api/fs/get":
					if tc.change == "mismatch" {
						f["id"] = "new-object"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": f})
				case "/api/fs/remove":
					calls++
					var body struct {
						Dir   string   `json:"dir"`
						Names []string `json:"names"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Dir != file.Path || len(body.Names) != 1 || body.Names[0] != file.FileName {
						t.Errorf("wrong args %+v %v", body, err)
					}
					_, _ = io.WriteString(w, tc.response)
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
			if tc.change == "weak" {
				file.OpenlistObjectID, file.OpenlistSHA1 = "", ""
			}
			p := &embyDeleteProvider{source: SourceTypeOpenList, accountID: 1, accountIdentity: "owner", openlist: client}
			ok, err := p.Delete(t.Context(), file, func() error { return nil })
			if ok != tc.wantOK || calls != tc.wantCalls || (!ok && err == nil) {
				t.Fatalf("ok=%v calls=%d err=%v", ok, calls, err)
			}
		})
	}
}

func TestEmbyDeleteProviderFinalGuardAndCancellation(t *testing.T) {
	file := embyProviderTestFile(SourceType115)
	detail := &v115open.FileDetail{FileId: file.FileID, FileName: file.FileName, Path: "media", Paths: []v115open.FileDetailPath{{FileId: "7"}}, Sha1: "hash", Utime: "1700000000", FileSizeByte: 100, FileCategory: v115open.TypeFile}
	p := &embyDeleteProvider{source: SourceType115, accountID: 1, accountIdentity: "owner", v115: &embyDelete115Stub{detail: detail, delete: func([]string, string) (bool, error) { t.Fatal("guarded deletion sent"); return false, nil }}}
	if _, err := p.Delete(t.Context(), file, func() error { return ErrEmbyDeleteDisabled }); !errors.Is(err, ErrEmbyDeleteDisabled) {
		t.Fatal(err)
	}
	if _, err := p.Delete(t.Context(), file, nil); !errors.Is(err, ErrEmbyDeleteUnverified) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Delete(ctx, file, func() error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestEmbyDeleteProviderBaiduMissingPathNeedsOriginalIDCheck(t *testing.T) {
	embyProviderTestLogger(t)
	for _, tc := range []struct {
		name, detail string
		wantAbsent   bool
	}{
		{name: "old ID absent", detail: `{"errno":0,"list":[]}`, wantAbsent: true},
		{name: "old ID moved", detail: `{"errno":0,"list":[{"filename":"renamed.mkv","size":100,"server_mtime":1700000000}]}`},
		{name: "detail unavailable", detail: `{"errno":500}`},
		{name: "detail malformed", detail: `{"errno":0}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := http.DefaultClient
			t.Cleanup(func() { http.DefaultClient = original })
			details := 0
			http.DefaultClient = &http.Client{Transport: baiduUploadTransport(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Query().Get("method") {
				case "list":
					_, _ = io.WriteString(w, `{"errno":0,"list":[]}`)
				case "filemetas":
					details++
					if r.URL.Query().Get("fsids") != "[10]" {
						t.Errorf("looked up different ID: %s", r.URL.Query().Get("fsids"))
					}
					_, _ = io.WriteString(w, tc.detail)
				default:
					t.Fatal("unexpected destructive request")
				}
			})}
			p := &embyDeleteProvider{source: SourceTypeBaiduPan, accountID: 1, accountIdentity: "owner", baidu: baidupan.NewBaiDuPanClientWithToken("test")}
			_, err := p.Stat(t.Context(), embyProviderTestFile(SourceTypeBaiduPan))
			if details != 1 || err == nil || errors.Is(err, ErrEmbyRemoteFileAbsent) != tc.wantAbsent {
				t.Fatalf("details=%d err=%v", details, err)
			}
		})
	}
}
