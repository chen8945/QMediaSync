package syncstrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

func TestOpen115DriverGetNetFileFilesAccumulatesAllPages(t *testing.T) {
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.V115Log = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	models.SettingsGlobal = &models.Settings{
		FileListPageSize: 100,
	}
	originalList115FilesPage := list115FilesPage
	list115FilesPage = func(_ context.Context, _ *v115open.OpenClient, parentPathID string, _ bool, _ bool, _ bool, offset int, limit int) (*v115open.FileListResp, error) {
		if parentPathID != "dir-1" {
			t.Fatalf("parentPathID = %s，期望 dir-1", parentPathID)
		}
		files := make([]v115open.File, 0, limit)
		for i := offset; i < offset+limit && i < 250; i++ {
			files = append(files, v115open.File{
				FileId:       fmt.Sprintf("file-%d", i),
				Aid:          "1",
				FileCategory: v115open.TypeFile,
				FileName:     "movie.mkv",
				PickCode:     "pick-code",
				FileSize:     1024,
				Sha1:         "sha1",
				Utime:        200,
				Ptime:        100,
			})
		}
		return &v115open.FileListResp{
			RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: files},
			Count:        250,
		}, nil
	}
	t.Cleanup(func() {
		list115FilesPage = originalList115FilesPage
	})

	driver := NewOpen115Driver(nil)
	driver.SetSyncStrm(&SyncStrm{
		Sync:                    &models.Sync{Logger: helpers.AppLogger},
		lastProgressPublishedAt: time.Now(),
	})

	files, err := driver.GetNetFileFiles(context.Background(), "/remote/movies", "dir-1")
	if err != nil {
		t.Fatalf("获取 115 文件列表失败: %v", err)
	}
	if len(files) != 250 {
		t.Fatalf("文件数量 = %d，期望 250", len(files))
	}
	if files[0].FileId != "file-0" || files[249].FileId != "file-249" {
		t.Fatalf("分页结果顺序错误，first=%s last=%s", files[0].FileId, files[249].FileId)
	}
	if files[0].MTime != 200 {
		t.Fatalf("文件修改时间 = %d，期望使用 115 upt 字段的 200", files[0].MTime)
	}
}

func TestOpen115DriverExcludesPlaybackFilesAfterResolvingPath(t *testing.T) {
	originalList, originalSettings := list115FilesPage, models.SettingsGlobal
	t.Cleanup(func() {
		list115FilesPage, models.SettingsGlobal = originalList, originalSettings
	})
	models.SettingsGlobal = &models.Settings{FileListPageSize: 100}
	for _, tt := range []struct {
		name         string
		parent       string
		responsePath string
		fileName     string
		fileType     v115open.FileType
		want         int
	}{
		{name: "根目录列出临时目录", parent: "/", fileName: "多端播放", fileType: v115open.TypeDir},
		{name: "临时目录直接列出副本", parent: "/多端播放", fileName: "movie.mkv", fileType: v115open.TypeFile},
		{name: "路径为空时使用列表补齐", responsePath: "多端播放/child", fileName: "movie.mkv", fileType: v115open.TypeFile},
		{name: "目录移动后以列表路径过滤", parent: "/Media", responsePath: "多端播放", fileName: "movie.mkv", fileType: v115open.TypeFile},
		{name: "同名非根目录保留", parent: "/Media", responsePath: "Media", fileName: "多端播放", fileType: v115open.TypeDir, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			list115FilesPage = func(context.Context, *v115open.OpenClient, string, bool, bool, bool, int, int) (*v115open.FileListResp, error) {
				return &v115open.FileListResp{
					RespBaseBool: v115open.RespBaseBool[[]v115open.File]{Data: []v115open.File{{
						FileId: "entry", FileName: tt.fileName, FileCategory: tt.fileType, Aid: "1",
					}}},
					PathStr: tt.responsePath,
					Count:   1,
				}, nil
			}
			driver := NewOpen115Driver(nil)
			driver.SetSyncStrm(&SyncStrm{
				Sync:                    &models.Sync{Logger: &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}},
				lastProgressPublishedAt: time.Now(),
			})
			files, err := driver.GetNetFileFiles(context.Background(), tt.parent, "parent")
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != tt.want || driver.s.TotalFile != int64(tt.want) {
				t.Fatalf("扫描得到 %d 条文件、计数 %d，期望 %d", len(files), driver.s.TotalFile, tt.want)
			}
		})
	}
}

func TestOpen115DriverCreateDirPreservesAuthenticationFailure(t *testing.T) {
	for _, stage := range []string{"probe", "create"} {
		t.Run(stage, func(t *testing.T) {
			logger := &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
			oldLogger := helpers.V115Log
			helpers.V115Log = logger
			t.Cleanup(func() { helpers.V115Log = oldLogger })
			target := t.TempDir()
			driver := NewOpen115Driver(v115open.NewClient(9902, "fixture", "", ""))
			driver.SetSyncStrm(&SyncStrm{TargetPath: target, Sync: &models.Sync{Logger: logger}})
			path, prefix := filepath.Join(target, "media", "extras"), "查询目录失败"
			if stage == "create" {
				// 根目录详情在客户端本地拒绝；既有创建分支随后由缺失凭证终止，不发远端请求。
				path, prefix = target, "创建目录失败"
			}
			_, _, err := driver.CreateDirRecursively(t.Context(), path)
			apiErr, ok := errors.AsType[*v115open.OpenAPIError](err)
			if !ok || apiErr.Code != v115open.ACCESS_AUTH_INVALID || !isFatalSyncError(err) {
				t.Fatalf("authentication identity lost: %v", err)
			}
			if !strings.HasPrefix(err.Error(), prefix) {
				t.Fatalf("failure stage=%v, want %s", err, stage)
			}
		})
	}
}

type open115DirectoryTransport func(*http.Request) (any, error)

func (transport open115DirectoryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	body, err := transport(request)
	if err != nil {
		return nil, err
	}
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(recorder).Encode(body); err != nil {
		return nil, err
	}
	response := recorder.Result()
	response.Request = request
	return response, nil
}

func TestOpen115DriverSkipsExcludedAttachments(t *testing.T) {
	for _, tt := range []struct {
		name     string
		exact    []string
		patterns []string
	}{
		{name: "directory_name", exact: []string{"extras"}},
		{name: "directory_regex", patterns: []string{"^extras$"}},
		{name: "ancestor_name", exact: []string{"film"}},
		{name: "file_name", exact: []string{"a.nfo", "b.nfo"}},
		{name: "file_regex", patterns: []string{"^[ab]\\.nfo$"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account, syncPath := setupStrmExclusionTestDB(t)
			s := newSyncStrm(account, syncPath.ID, "Media", "root", t.TempDir(), SyncStrmConfig{
				MetaExt: []string{".nfo"}, EnableDownloadMeta: 1,
				NetNotFoundFileAction: models.SyncTreeItemMetaActionUpload,
				ExcludeNames:          tt.exact,
				ExcludeNameRegexes:    tt.patterns,
			}, false, 0, false, false)
			t.Cleanup(s.Cancel)
			// Start115Sync 即使未取得附件目录信息，也会先初始化本轮状态。
			s.sync115 = &Sync115{}
			localDir := filepath.Join(s.GetLocalBaseDir(), "Film", "extras")
			for _, name := range []string{"a.nfo", "b.nfo"} {
				writeScopeFile(t, filepath.Join(localDir, name))
			}
			requests := 0
			client := v115open.NewClient(account.ID, "fixture", "fixture-token", "")
			client.SetTransport(open115DirectoryTransport(func(request *http.Request) (any, error) {
				requests++
				return map[string]any{"state": true, "data": []any{}}, nil
			}))
			driver := NewOpen115Driver(client)
			driver.SetSyncStrm(s)
			s.SyncDriver = driver

			if err := s.compareLocalFilesWithTempTable(); err != nil {
				t.Fatal(err)
			}
			var uploads []models.DbUploadTask
			if err := db.Db.Where("sync_path_id = ?", s.SyncPathId).Order("file_name").Find(&uploads).Error; err != nil {
				t.Fatal(err)
			}
			if s.NewUpload != 0 || len(uploads) != 0 || requests != 0 {
				t.Fatalf("excluded attachments must not upload or query: uploads=%d tasks=%d requests=%d", s.NewUpload, len(uploads), requests)
			}
			for _, name := range []string{"a.nfo", "b.nfo"} {
				requireScopeFile(t, filepath.Join(localDir, name), true)
			}
			if failures := s.scanResultSnapshot().Failures; len(failures) != 0 {
				t.Fatalf("unexpected failures: %+v", failures)
			}
		})
	}
}

func TestOpen115DriverCreateDirReusesExistingDirectory(t *testing.T) {
	for _, mode := range []string{"probe", "current_evidence"} {
		t.Run(mode, func(t *testing.T) {
			s, host := new115UploadParentSync(t)
			remote := "/Media/Empty/extras"
			if mode == "current_evidence" {
				s.sync115 = &Sync115{
					pathIDs: map[string]string{remote: "existing-extras"},
					paths: map[string]verified115Path{
						"existing-extras": {path: "Media/Empty/extras", parentID: host.FileId, within: true},
					},
				}
			}
			probes, creates := 0, 0
			client := v115open.NewClient(s.Account.ID, "fixture", "fixture-token", "")
			client.SetTransport(open115DirectoryTransport(func(request *http.Request) (any, error) {
				if err := request.ParseForm(); err != nil {
					return nil, err
				}
				if request.URL.Path == "/open/folder/add" {
					creates++
					return nil, errors.New("unexpected directory creation")
				}
				probes++
				if request.Method != http.MethodPost || request.URL.Path != "/open/folder/get_info" || request.Form.Get("path") != remote {
					t.Errorf("unexpected request: %s %s %s", request.Method, request.URL.Path, request.Form.Encode())
				}
				return map[string]any{"state": true, "data": v115open.FileDetail{
					FileId: "existing-extras", FileName: "extras", FileCategory: v115open.TypeDir,
					Paths: []v115open.FileDetailPath{{FileId: "root", Name: "Media"}, {FileId: host.FileId, Name: "Empty"}},
				}}, nil
			}))
			driver := NewOpen115Driver(client)
			driver.SetSyncStrm(s)
			id, path, err := driver.CreateDirRecursively(t.Context(), filepath.Join(host.LocalFilePath, "extras"))
			wantProbes := 0
			if mode == "probe" {
				wantProbes = 1
			}
			if err != nil || id != "existing-extras" || path != remote || probes != wantProbes || creates != 0 {
				t.Fatalf("existing directory: id=%s path=%s err=%v probes=%d creates=%d", id, path, err, probes, creates)
			}
		})
	}
}

func TestOpen115DriverUploadsToCachedDirectory(t *testing.T) {
	s, host := new115UploadParentSync(t)
	s.sync115 = &Sync115{}
	local := filepath.Join(host.LocalFilePath, "extras", "movie.nfo")
	writeScopeFile(t, local)
	for _, directory := range []*SyncFileCache{
		{FileId: host.FileId, ParentId: "root", Path: "Media", FileName: "Empty", FileType: v115open.TypeDir},
		{FileId: "existing-extras", ParentId: host.FileId, Path: "Media/Empty", FileName: "extras", FileType: v115open.TypeDir},
	} {
		directory.GetLocalFilePath(s.TargetPath, s.SourcePath)
		s.memSyncCache.Insert(directory)
	}
	requests := 0
	client := v115open.NewClient(s.Account.ID, "fixture", "fixture-token", "")
	client.SetTransport(open115DirectoryTransport(func(*http.Request) (any, error) {
		requests++
		return nil, errors.New("unexpected directory request")
	}))
	driver := NewOpen115Driver(client)
	driver.SetSyncStrm(s)
	s.SyncDriver = driver
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	var uploads []models.DbUploadTask
	if err := db.Db.Where("sync_path_id = ?", s.SyncPathId).Find(&uploads).Error; err != nil {
		t.Fatal(err)
	}
	if requests != 0 || s.NewUpload != 1 || len(uploads) != 1 {
		t.Fatalf("cached directory: requests=%d uploads=%d tasks=%d", requests, s.NewUpload, len(uploads))
	}
	if uploads[0].RemotePathId != "existing-extras" || normalizeStrmRemotePath(uploads[0].RemoteFullPath) != "/Media/Empty/extras/movie.nfo" {
		t.Fatalf("wrong upload destination: %+v", uploads[0])
	}
	requireScopeFile(t, local, true)
}

func TestOpen115DriverCreateDirRequiresCurrentEvidence(t *testing.T) {
	for _, mode := range []string{"target_unknown", "host_unknown", "host_absent", "host_confirmed"} {
		t.Run(mode, func(t *testing.T) {
			s, host := new115UploadParentSync(t)
			s.Config.ExcludeNames = []string{"unrelated"}
			s.sync115 = &Sync115{}
			remote := "/Media/Empty/extras"
			if mode != "target_unknown" {
				s.sync115.absentDirs = map[string]bool{remote: true}
			}
			if mode == "host_absent" {
				s.sync115.absentDirs["/Media/Empty"] = true
			}
			if mode == "host_confirmed" {
				s.sync115.pathIDs = map[string]string{"/Media/Empty": host.FileId}
				s.sync115.paths = map[string]verified115Path{host.FileId: {path: "Media/Empty", parentID: "root", within: true}}
			}
			requests := 0
			client := v115open.NewClient(s.Account.ID, "fixture", "fixture-token", "")
			client.SetTransport(open115DirectoryTransport(func(request *http.Request) (any, error) {
				requests++
				if err := request.ParseForm(); err != nil {
					return nil, err
				}
				if mode != "host_confirmed" || request.Method != http.MethodPost || request.URL.Path != "/open/folder/add" ||
					request.Form.Get("pid") != host.FileId || request.Form.Get("file_name") != "extras" {
					t.Errorf("unexpected directory request: %s %s %s", request.Method, request.URL.Path, request.Form.Encode())
					return map[string]any{"state": false, "code": 430004}, nil
				}
				return map[string]any{"state": true, "data": v115open.MkDirData{FileId: "created-extras", FileName: "extras"}}, nil
			}))
			driver := NewOpen115Driver(client)
			driver.SetSyncStrm(s)
			id, path, err := driver.CreateDirRecursively(t.Context(), filepath.Join(host.LocalFilePath, "extras"))
			if mode != "host_confirmed" {
				if err == nil || requests != 0 {
					t.Fatalf("unconfirmed or absent ordinary host must not be recreated: id=%s err=%v requests=%d", id, err, requests)
				}
				return
			}
			if err != nil || id != "created-extras" || path != remote || requests != 1 {
				t.Fatalf("confirmed host creation: id=%s path=%s err=%v requests=%d", id, path, err, requests)
			}
			if known, absent, err := s.known115UploadDirectory(remote); err != nil || known != id || absent {
				t.Fatalf("created directory not confirmed: id=%s absent=%v err=%v", known, absent, err)
			}
		})
	}
}
