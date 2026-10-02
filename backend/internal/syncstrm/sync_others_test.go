package syncstrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/openlist"
	"qmediasync/internal/v115open"
)

func setupStrmExclusionTestDB(t *testing.T) (*models.Account, *models.SyncPath) {
	t.Helper()
	originalDB, originalSettings := db.Db, models.SettingsGlobal
	originalLogger, original115Logger, originalConfigDir := helpers.AppLogger, helpers.V115Log, helpers.ConfigDir
	t.Cleanup(func() {
		db.Db, models.SettingsGlobal = originalDB, originalSettings
		helpers.AppLogger, helpers.V115Log, helpers.ConfigDir = originalLogger, original115Logger, originalConfigDir
	})
	account, syncPath := setupStrmGenerationServiceTestDB(t)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	helpers.ConfigDir = t.TempDir()
	return account, syncPath
}

func TestStartFileHonorsGlobalExclusions(t *testing.T) {
	account, _ := setupStrmExclusionTestDB(t)
	tests := []struct {
		name      string
		exact     []string
		patterns  []string
		filename  string
		parent    string
		wantFiles int
	}{
		{name: "原文件名规则", exact: []string{"sample.mkv"}, filename: "SAMPLE.mkv", parent: "/Media"},
		{name: "原父目录规则", exact: []string{"extras"}, filename: "movie.mkv", parent: "/Media/Extras/Season 1"},
		{name: "仅正则文件名规则", patterns: []string{"(?i)sample"}, filename: "Movie.SAMPLE.mkv", parent: "/Media"},
		{name: "正则父目录规则", patterns: []string{"^Extras$"}, filename: "movie.mkv", parent: "/Media/Extras/Season 1"},
		{name: "正则默认区分大小写", patterns: []string{"sample"}, filename: "SAMPLE.mkv", parent: "/Media", wantFiles: 1},
		{name: "原名称列表不做部分匹配", exact: []string{"sample.mkv"}, filename: "MySample.mkv", parent: "/Media", wantFiles: 1},
		{name: "多端播放临时目录", filename: "movie.mkv", parent: "/多端播放"},
		{name: "多端播放临时目录后代", filename: "movie.mkv", parent: "多端播放/child"},
		{name: "同名非根目录", filename: "movie.mkv", parent: "/Media/多端播放", wantFiles: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			models.SettingsGlobal.ExcludeNameArr = tt.exact
			models.SettingsGlobal.ExcludeNameRegexArr = tt.patterns
			file := &SyncFileCache{
				FileId:     "file-excluded",
				ParentId:   "parent-id",
				FileType:   v115open.TypeFile,
				FileName:   tt.filename,
				Path:       tt.parent,
				FileSize:   1024,
				PickCode:   "pick-excluded",
				SourceType: models.SourceType115,
				IsVideo:    true,
			}
			target := t.TempDir()
			syncer := NewSyncStrmByPath(account, file.GetFullRemotePath(), file.FileId, target, true)
			if syncer == nil {
				t.Fatal("创建手动同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.SyncDriver = &fakeDirectoryScanDriver{
				detailsByID: map[string]*SyncFileCache{file.FileId: file},
				strmContent: "http://qms.local/video",
			}
			if err := syncer.StartFile(); err != nil {
				t.Fatalf("手动文件生成失败：%v", err)
			}
			if syncer.NewStrm != int64(tt.wantFiles) {
				t.Fatalf("生成 %d 个 STRM，期望 %d", syncer.NewStrm, tt.wantFiles)
			}
			var files int
			if err := filepath.WalkDir(target, func(_ string, entry os.DirEntry, err error) error {
				if err == nil && !entry.IsDir() {
					files++
				}
				return err
			}); err != nil {
				t.Fatalf("检查输出目录失败：%v", err)
			}
			if files != tt.wantFiles {
				t.Fatalf("输出目录有 %d 个文件，期望 %d", files, tt.wantFiles)
			}
		})
	}
}

func TestStartOtherHonorsGlobalAndCustomExclusions(t *testing.T) {
	_, syncPath := setupStrmExclusionTestDB(t)
	settings := models.SettingsGlobal.SettingStrm
	settings.ExcludeNameArr = []string{"extras"}
	settings.ExcludeNameRegexArr = []string{"(?i)sample", "^\\.hidden$"}
	if !models.SettingsGlobal.UpdateStrm(settings, models.SettingsGlobal.MultiPlaybackEnabled) {
		t.Fatal("保存全局排除设置失败")
	}

	source := t.TempDir()
	for _, name := range []string{
		"Movie.mkv", "MySampleFilm.mkv", "SAMPLE.mkv", "MyExtras/Movie.mkv",
		"Extras/Season 1/Movie.mkv", ".hidden/Bonus.mkv",
	} {
		path := filepath.Join(source, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("video"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	inherited := []string{"Movie.strm", "MyExtras/Movie.strm"}
	tests := []struct {
		name     string
		manual   bool
		custom   bool
		patterns []string
		selected string
		want     []string
	}{
		{name: "手动目录使用全局规则", manual: true, want: inherited},
		{name: "同步目录使用全局规则", want: inherited},
		{name: "自定义空列表继承全局", custom: true, patterns: []string{}, want: inherited},
		{name: "仅覆盖正则且继续继承原名称列表", custom: true, patterns: []string{"^Movie\\.mkv$"}, want: []string{".hidden/Bonus.strm", "MySampleFilm.strm", "SAMPLE.strm"}},
		{name: "手动选中排除目录的后代", manual: true, selected: "Extras/Season 1"},
		{name: "同步选中排除目录的后代", selected: "Extras/Season 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			selected := filepath.Join(source, tt.selected)
			target := t.TempDir()
			var syncer *SyncStrm
			if tt.manual {
				syncer = NewSyncStrmByPath(nil, selected, selected, target, false)
			} else {
				directory := &models.SyncPath{
					SourceType:          models.SourceTypeLocal,
					RemotePath:          selected,
					BaseCid:             selected,
					LocalPath:           target,
					CustomConfig:        tt.custom,
					ExcludeNameRegexArr: tt.patterns,
				}
				directory.ID = syncPath.ID
				syncer = NewSyncStrmFromSyncPath(directory)
			}
			if syncer == nil {
				t.Fatal("创建同步器失败")
			}
			t.Cleanup(syncer.Cancel)
			syncer.StartOther()
			select {
			case err := <-syncer.PathErrChan:
				t.Fatalf("目录遍历失败：%v", err)
			default:
			}

			var got []string
			if err := filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				relative, err := filepath.Rel(target, path)
				if err != nil {
					return err
				}
				got = append(got, filepath.ToSlash(relative))
				content, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				wantContent := filepath.Join(selected, strings.TrimSuffix(relative, ".strm")+".mkv")
				if string(content) != wantContent {
					t.Errorf("STRM 内容 = %q，期望 %q", content, wantContent)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !reflect.DeepEqual(got, tt.want) || syncer.NewStrm != int64(len(tt.want)) {
				t.Fatalf("生成结果 = %q（计数 %d），期望 %q", got, syncer.NewStrm, tt.want)
			}
		})
	}
}

type failureScanDriver struct {
	fakeDirectoryScanDriver
	list        func(context.Context, string, string) ([]*SyncFileCache, error)
	content     func(*SyncFileCache) string
	incremental func(context.Context, string, int, int, int64) (*baidupan.FileListAllResponse, error)
}

func (d *failureScanDriver) GetNetFileFiles(ctx context.Context, path, id string) ([]*SyncFileCache, error) {
	return d.list(ctx, path, id)
}

func (d *failureScanDriver) MakeStrmContent(file *SyncFileCache) string {
	if d.content != nil {
		return d.content(file)
	}
	return "http://qms.local/video"
}

func (d *failureScanDriver) GetFilesByPathMtime(ctx context.Context, path string, offset, limit int, mtime int64) (*baidupan.FileListAllResponse, error) {
	return d.incremental(ctx, path, offset, limit, mtime)
}

func newFailureScanSyncer(t *testing.T) *SyncStrm {
	t.Helper()
	account, _ := setupStrmExclusionTestDB(t)
	s := NewSyncStrmByPath(account, "/media", "root", t.TempDir(), false)
	if s == nil {
		t.Fatal("创建同步器失败")
	}
	t.Cleanup(s.Cancel)
	s.Config.VideoExt = []string{".mkv"}
	return s
}

func scanFailureTestFile(id, parent, name string, directory bool) *SyncFileCache {
	file := &SyncFileCache{
		FileId: id, ParentId: parent, Path: parent, FileName: name,
		SourceType: models.SourceType115, FileType: v115open.TypeFile,
		FileSize: 1024,
	}
	if directory {
		file.FileType = v115open.TypeDir
	}
	return file
}

func TestStartOtherIsolatesFileAndSubtreeFailures(t *testing.T) {
	s := newFailureScanSyncer(t)
	models.SettingsGlobal.OpenlistRetry = 0
	badFile := scanFailureTestFile("bad-file", "/media", "bad.mkv", false)
	goodFile := scanFailureTestFile("good-file", "/media", "good.mkv", false)
	goodChild := scanFailureTestFile("good-child", "/media/good", "child.mkv", false)
	s.SyncDriver = &failureScanDriver{
		list: func(_ context.Context, path, _ string) ([]*SyncFileCache, error) {
			switch path {
			case "/media":
				return []*SyncFileCache{badFile, goodFile,
					scanFailureTestFile("bad-dir", "/media", "bad", true),
					scanFailureTestFile("good-dir", "/media", "good", true)}, nil
			case "/media/bad":
				return nil, errors.New("directory unreadable")
			case "/media/good":
				return []*SyncFileCache{goodChild}, nil
			default:
				return nil, fmt.Errorf("unexpected path %s", path)
			}
		},
		content: func(file *SyncFileCache) string {
			if file.FileId == "bad-file" {
				return ""
			}
			return "http://qms.local/video"
		},
	}
	s.StartOther()
	if len(s.PathErrChan) != 0 || s.NewStrm != 2 {
		t.Fatalf("fatal errors=%d, generated=%d", len(s.PathErrChan), s.NewStrm)
	}
	result := s.scanResultSnapshot()
	if result.FailedFiles != 1 || result.SucceededFiles != 2 || len(result.Failures) != 2 {
		t.Fatalf("unexpected scan result: %+v", result)
	}
	for _, file := range []*SyncFileCache{goodFile, goodChild} {
		if _, err := os.Stat(file.LocalFilePath); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.scanOutcome(); err == nil {
		t.Fatal("partial scan reported success")
	}
}

func TestStartOtherRetryCancellationPublishesOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFailureScanSyncer(t)
		models.SettingsGlobal.OpenlistRetry = 3
		models.SettingsGlobal.OpenlistRetryDelay = 60
		var calls atomic.Int64
		s.SyncDriver = &failureScanDriver{list: func(context.Context, string, string) ([]*SyncFileCache, error) {
			calls.Add(1)
			return nil, errors.New("temporary listing failure")
		}}
		done := make(chan struct{})
		go func() { s.StartOther(); close(done) }()
		synctest.Wait()
		before := time.Now()
		s.Cancel()
		<-done
		if time.Since(before) != 0 || calls.Load() != 1 || len(s.PathErrChan) != 1 {
			t.Fatalf("elapsed=%s, calls=%d, errors=%d", time.Since(before), calls.Load(), len(s.PathErrChan))
		}
		if err := <-s.PathErrChan; !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v, want cancellation", err)
		}
	})
}

func TestLocalSourceStatFailurePreservesScopeAndContinues(t *testing.T) {
	s := newFailureScanSyncer(t)
	source := t.TempDir()
	if err := os.Symlink(filepath.Join(source, "missing"), filepath.Join(source, "broken.mkv")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "good.mkv"), []byte("video"), 0644); err != nil {
		t.Fatal(err)
	}
	s.SourcePath, s.SourcePathId = source, source
	s.Account.SourceType = models.SourceTypeLocal
	driver := NewLocalDriver()
	driver.SetSyncStrm(s)
	s.SyncDriver = driver
	s.StartOther()
	result := s.scanResultSnapshot()
	if s.NewStrm != 1 || s.TotalFile != 2 || len(result.Failures) != 1 || result.Failures[0].Path != source {
		t.Fatalf("generated=%d, total=%d, result=%+v", s.NewStrm, s.TotalFile, result)
	}
	if _, err := os.Stat(filepath.Join(s.TargetPath, "good.strm")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.scanOutcome(); err == nil {
		t.Fatal("source stat failure reported complete")
	}
}

func TestLocalSourceReadDirFailureIsNotEmptySuccess(t *testing.T) {
	s := newFailureScanSyncer(t)
	models.SettingsGlobal.OpenlistRetry = 0
	s.SourcePath = filepath.Join(t.TempDir(), "missing")
	s.SourcePathId = s.SourcePath
	s.Account.SourceType = models.SourceTypeLocal
	driver := NewLocalDriver()
	driver.SetSyncStrm(s)
	s.SyncDriver = driver
	s.StartOther()
	if _, err := s.scanOutcome(); err == nil {
		t.Fatal("ReadDir failure reported an empty successful source")
	}
}

func TestStartOtherOpenListAuthenticationAndDirectoryPermissions(t *testing.T) {
	for _, name := range []string{"auth-recovery-failed", "http-unauthorized", "directory-forbidden"} {
		t.Run(name, func(t *testing.T) {
			s := newFailureScanSyncer(t)
			s.Account.SourceType = models.SourceTypeOpenList
			models.SettingsGlobal.OpenlistRetry = 2
			models.SettingsGlobal.OpenlistRetryDelay = 0
			oldLogger := helpers.OpenListLog
			helpers.OpenListLog = s.Sync.Logger
			t.Cleanup(func() { helpers.OpenListLog = oldLogger })
			var roots, logins, good, bad atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/auth/login" {
					logins.Add(1)
					fmt.Fprint(w, `{"code":200,"data":{"token":"new-token"}}`)
					return
				}
				var request struct {
					Path string `json:"path"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				switch request.Path {
				case "/media":
					roots.Add(1)
					if name != "directory-forbidden" {
						if name == "http-unauthorized" {
							w.WriteHeader(http.StatusUnauthorized)
						}
						fmt.Fprint(w, `{"code":401,"message":"expired"}`)
						return
					}
					fmt.Fprint(w, `{"code":200,"data":{"total":2,"content":[{"name":"bad","is_dir":true},{"name":"good","is_dir":true}]}}`)
				case "/media/bad":
					bad.Add(1)
					fmt.Fprint(w, `{"code":403,"message":"directory denied"}`)
				case "/media/good":
					good.Add(1)
					fmt.Fprint(w, `{"code":200,"data":{"total":0,"content":[]}}`)
				default:
					t.Errorf("unexpected source path %q", request.Path)
				}
			}))
			defer server.Close()
			client := openlist.NewTemporaryClient(server.URL, "user", "password", "old-token")
			defer client.Close()
			driver := NewOpenListDriver(client)
			driver.SetSyncStrm(s)
			s.SourcePathId = "/media"
			s.SyncDriver = driver
			if name == "directory-forbidden" {
				// 目录局部重试行为保持不变，测试不额外重复外层重试。
				models.SettingsGlobal.OpenlistRetry = 0
			}
			s.StartOther()
			if name == "directory-forbidden" {
				result := s.scanResultSnapshot()
				if len(s.PathErrChan) != 0 || good.Load() != 1 || bad.Load() != 4 || len(result.Failures) != 1 || result.Failures[0].Path != "/media/bad" {
					t.Fatalf("fatal=%d good=%d bad=%d result=%+v", len(s.PathErrChan), good.Load(), bad.Load(), result)
				}
			} else {
				if roots.Load() != 2 || logins.Load() != 1 || len(s.PathErrChan) != 1 {
					t.Fatalf("roots=%d logins=%d fatal=%d", roots.Load(), logins.Load(), len(s.PathErrChan))
				}
				if err := <-s.PathErrChan; !errors.Is(err, openlist.ErrTokenExpired) || !isFatalSyncError(err) {
					t.Fatalf("error lost authentication identity: %v", err)
				}
			}
		})
	}
}

func TestStartOtherKeepsNameChecksForDirectoriesAndPathNames(t *testing.T) {
	for _, tc := range []struct {
		name      string
		filename  string
		directory bool
		want      int64
	}{
		{name: "普通文件", filename: "Movie.mkv", want: 1},
		{name: "排除文件", filename: "SAMPLE.mkv"},
		{name: "排除目录", filename: "Extras", directory: true},
		{name: "名称含路径", filename: "nested/SAMPLE.mkv"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newFailureScanSyncer(t)
			s.Config.ExcludeNames = []string{"sample.mkv", "extras"}
			s.SyncDriver = &fakeDirectoryScanDriver{
				filesByID:   map[string][]*SyncFileCache{"root": {scanFailureTestFile("file", "/media", tc.filename, tc.directory)}},
				strmContent: "http://qms.local/video",
			}
			s.StartOther()
			select {
			case err := <-s.PathErrChan:
				t.Fatal(err)
			default:
			}
			if s.NewStrm != tc.want || s.memSyncCache.Count() != tc.want {
				t.Fatalf("generated=%d cached=%d want=%d", s.NewStrm, s.memSyncCache.Count(), tc.want)
			}
		})
	}
}
