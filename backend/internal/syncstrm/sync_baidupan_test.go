package syncstrm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
)

func newSavedBaiduTestSyncer(t *testing.T, remotePath string) (*SyncStrm, *models.SyncPath) {
	t.Helper()
	account, _ := setupStrmExclusionTestDB(t)
	account.SourceType = models.SourceTypeBaiduPan
	if err := db.Db.Save(account).Error; err != nil {
		t.Fatal(err)
	}
	saved, err := models.CreateSyncPathWithDB(db.Db, models.SyncPathWriteInput{
		SourceType: models.SourceTypeBaiduPan, AccountID: account.ID,
		BaseCid: remotePath, RemotePath: remotePath, LocalPath: t.TempDir(), CustomConfig: true,
		Setting: models.SettingStrm{StrmBaseUrl: "http://qms.local", VideoExtArr: []string{".mkv"}, AddPath: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := newSyncStrmFromSyncPath(saved, account, true)
	if s == nil {
		t.Fatal("创建保存型百度同步器失败")
	}
	t.Cleanup(func() {
		s.releaseScope()
		s.Cancel()
		s.Sync.Logger.Close()
	})
	return s, saved
}

func TestBaiduSavedPathIncrementalScope(t *testing.T) {
	for _, tt := range []struct {
		name       string
		remotePath string
		root       string
		filePath   string
		reject     bool
	}{
		{name: "absolute", remotePath: "/media", root: "/media", filePath: "/media/movie.mkv"},
		{name: "relative", remotePath: "media", root: "/media", filePath: "/media/movie.mkv"},
		{name: "root", remotePath: "/", root: "/", filePath: "/movie.mkv"},
		{name: "spaces", remotePath: "/ media /", root: "/ media ", filePath: "/ media /movie.mkv"},
		{name: "outside", remotePath: "/media", root: "/media", filePath: "/other/movie.mkv", reject: true},
		{name: "sibling_prefix", remotePath: "/media", root: "/media", filePath: "/media-extra/movie.mkv", reject: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, saved := newSavedBaiduTestSyncer(t, tt.remotePath)
			storedRemote, storedID := saved.RemotePath, saved.BaseCid
			if err := s.acquireScope(); err != nil {
				t.Fatal(err)
			}
			oldClient, oldLogger := http.DefaultClient, helpers.BaiduPanLog
			t.Cleanup(func() { http.DefaultClient, helpers.BaiduPanLog = oldClient, oldLogger })
			helpers.BaiduPanLog = s.Sync.Logger
			calls := 0
			http.DefaultClient = &http.Client{Transport: baiduListTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.URL.Query().Get("path") != tt.root {
					return nil, fmt.Errorf("增量请求路径 = %q，期望 %q", request.URL.Query().Get("path"), tt.root)
				}
				recorder := httptest.NewRecorder()
				if err := json.NewEncoder(recorder).Encode(baidupan.FileListAllResponse{List: []*baidupan.FileListAllItem{
					{FsId: 1, Path: tt.filePath, Size: 2048},
				}}); err != nil {
					return nil, err
				}
				response := recorder.Result()
				response.Request = request
				return response, nil
			})}
			driver := NewBaiduPanDriver(baidupan.NewBaiDuPanClientWithToken("test-token"))
			driver.SetSyncStrm(s)
			s.SyncDriver = driver
			err := s.StartBaiduPanSyncByMtime(100)
			if tt.reject {
				if err == nil || !strings.Contains(err.Error(), "范围外条目") || s.NewStrm != 0 {
					t.Fatalf("范围外文件未被拒绝：生成数=%d，错误=%v", s.NewStrm, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if s.NewStrm != 1 {
					t.Fatalf("生成数=%d，期望 1", s.NewStrm)
				}
				requireScopeFile(t, filepath.Join(saved.LocalPath, strings.TrimSuffix(tt.filePath, ".mkv")+".strm"), true)
			}
			if calls != 1 || s.SourcePath != tt.root {
				t.Fatalf("请求数=%d，运行时根=%q，期望 %q", calls, s.SourcePath, tt.root)
			}
			var record models.Sync
			if err := db.Db.First(&record, s.Sync.ID).Error; err != nil {
				t.Fatal(err)
			}
			if record.RemotePath != tt.root {
				t.Fatalf("同步记录根=%q，期望 %q", record.RemotePath, tt.root)
			}
			var reloaded models.SyncPath
			if err := db.Db.First(&reloaded, saved.ID).Error; err != nil {
				t.Fatal(err)
			}
			if reloaded.RemotePath != storedRemote || reloaded.BaseCid != storedID {
				t.Fatalf("保存目录被改写：%q / %q", reloaded.RemotePath, reloaded.BaseCid)
			}
		})
	}
}

func newBaiduIncrementalTestSyncer(t *testing.T, handle func(*http.Request, int) any) *SyncStrm {
	t.Helper()
	s := newFailureScanSyncer(t)
	s.Account.SourceType = models.SourceTypeBaiduPan
	s.Config.StrmBaseUrl = "http://qms.local"
	s.LastSyncAt = 100
	oldClient, oldLogger := http.DefaultClient, helpers.BaiduPanLog
	t.Cleanup(func() { http.DefaultClient, helpers.BaiduPanLog = oldClient, oldLogger })
	helpers.BaiduPanLog = s.Sync.Logger
	http.DefaultClient = &http.Client{Transport: baiduListTransport(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if query.Get("method") != "listall" || query.Get("path") != "/media" || query.Get("mtime") != "100" || query.Get("recursion") != "1" {
			return nil, fmt.Errorf("unexpected incremental request: %s", request.URL.Path)
		}
		offset, err := strconv.Atoi(query.Get("start"))
		if err != nil {
			return nil, err
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(recorder).Encode(handle(request, offset)); err != nil {
			return nil, err
		}
		response := recorder.Result()
		response.Request = request
		return response, nil
	})}
	driver := NewBaiduPanDriver(baidupan.NewBaiDuPanClientWithToken("test-token"))
	driver.SetSyncStrm(s)
	s.SyncDriver = driver
	return s
}

func TestBaiduIncrementalShortPageContinuesAndNewFactsWin(t *testing.T) {
	var offsets []int
	s := newBaiduIncrementalTestSyncer(t, func(_ *http.Request, offset int) any {
		offsets = append(offsets, offset)
		if offset == 0 {
			return baidupan.FileListAllResponse{HasMore: 1, Cursor: 5, List: []*baidupan.FileListAllItem{
				{FsId: 1, Path: "/media/new/renamed.mkv", Size: 2048, ServerMtime: 200, Md5: "new"},
			}}
		}
		return baidupan.FileListAllResponse{List: []*baidupan.FileListAllItem{
			{FsId: 2, Path: "/media/skip/moved.mkv", Size: 2048, ServerMtime: 200},
			{FsId: 3, Path: "/media/no-longer-video.txt", Size: 2048, ServerMtime: 200},
		}}
	})
	s.Config.ExcludeNames = []string{"skip", "excluded.mkv"}
	old := []models.SyncFile{
		{FileId: "/media/old.mkv", PickCode: "1", FileName: "old.mkv"},
		{FileId: "/media/was-included.mkv", PickCode: "2", FileName: "was-included.mkv"},
		{FileId: "/media/was-video.mkv", PickCode: "3", FileName: "was-video.mkv"},
		{FileId: "/media/excluded.mkv", PickCode: "4", FileName: "excluded.mkv"},
		{FileId: "/media/unchanged.mkv", PickCode: "5", FileName: "unchanged.mkv"},
		// 同路径但远端身份已更新，也不能覆盖本轮数据。
		{FileId: "/media/new/renamed.mkv", PickCode: "6", FileName: "renamed.mkv", Path: "/media/new"},
	}
	for i := range old {
		old[i].SourceType, old[i].SyncPathId = models.SourceTypeBaiduPan, s.SyncPathId
		old[i].FileType, old[i].FileSize = v115open.TypeFile, 1024
		if old[i].Path == "" {
			old[i].Path = "/media"
		}
		old[i].ParentId = old[i].Path
		old[i].LocalFilePath = "stale-local-path"
	}
	if err := db.Db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.StartBaiduPanSyncByMtime(100); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(offsets, []int{0, 5}) || s.memSyncCache.Count() != 2 {
		t.Fatalf("offsets=%v, cache count=%d", offsets, s.memSyncCache.Count())
	}
	fresh, err := s.memSyncCache.GetByFileId("/media/new/renamed.mkv")
	if err != nil || fresh.PickCode != "1" || fresh.MTime != 200 || fresh.FileSize != 2048 || fresh.Sha1 != "new" {
		t.Fatalf("fresh=%+v, err=%v", fresh, err)
	}
	unchanged, err := s.memSyncCache.GetByFileId("/media/unchanged.mkv")
	if err != nil || !unchanged.IsVideo || unchanged.LocalFilePath == "stale-local-path" {
		t.Fatalf("unchanged=%+v, err=%v", unchanged, err)
	}
}

func TestBaiduIncrementalRejectsIncompleteResponses(t *testing.T) {
	for _, name := range []string{"empty-more", "cursor-stalled", "nil-response", "repeated-id", "business-error", "auth-error"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			s := newBaiduIncrementalTestSyncer(t, func(_ *http.Request, offset int) any {
				calls++
				switch name {
				case "empty-more":
					return baidupan.FileListAllResponse{HasMore: 1, Cursor: 1}
				case "cursor-stalled":
					return baidupan.FileListAllResponse{HasMore: 1, List: []*baidupan.FileListAllItem{{FsId: 1, Path: "/media/a.mkv"}}}
				case "nil-response":
					return nil
				case "business-error":
					return map[string]any{"errno": -9, "errmsg": "missing"}
				case "auth-error":
					return map[string]any{"errno": 20016, "errmsg": "expired"}
				default:
					return baidupan.FileListAllResponse{HasMore: 1, Cursor: uint32(offset + 1), List: []*baidupan.FileListAllItem{{FsId: 1, Path: "/media/a.mkv"}}}
				}
			})
			err := s.StartBaiduPanSyncByMtime(100)
			if err == nil {
				t.Fatal("incomplete scan returned nil")
			}
			wantCalls := 1
			if name == "repeated-id" {
				wantCalls = 2
			}
			if calls != wantCalls || len(s.PathErrChan) != 0 {
				t.Fatalf("calls=%d, queued errors=%d, err=%v", calls, len(s.PathErrChan), err)
			}
			if name == "auth-error" && !isFatalSyncError(err) {
				t.Fatalf("authentication error is not fatal: %v", err)
			}
		})
	}
}

func TestBaiduIncrementalFinalSignalAndCacheReadFailure(t *testing.T) {
	for _, name := range []string{"auth", "cache-query"} {
		t.Run(name, func(t *testing.T) {
			s := newBaiduIncrementalTestSyncer(t, func(*http.Request, int) any {
				if name == "auth" {
					return map[string]any{"errno": 20016}
				}
				return baidupan.FileListAllResponse{}
			})
			if name == "cache-query" {
				const callback = "a2:fail-ledger-read"
				if err := db.Db.Callback().Query().Before("gorm:query").Register(callback, func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						tx.AddError(errors.New("ledger unavailable"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Query().Remove(callback) })
			}
			done := make(chan struct{})
			go func() { s.StartBaiduPanSync(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("Baidu error delivery deadlocked")
			}
			if len(s.PathErrChan) != 1 {
				t.Fatalf("queued errors=%d", len(s.PathErrChan))
			}
			if err := <-s.PathErrChan; !isFatalSyncError(err) {
				t.Fatalf("error=%v is not fatal", err)
			}
		})
	}
}

func TestBaiduDailyFullSelectionUsesSuccessfulFullRecord(t *testing.T) {
	for _, name := range []string{"current", "failed", "successful-incremental", "successful-full"} {
		t.Run(name, func(t *testing.T) {
			s := newFailureScanSyncer(t)
			s.TmpSyncPath = false
			s.Account.SourceType = models.SourceTypeBaiduPan
			s.LastSyncAt = 100
			s.FullSync = false
			if name != "current" {
				previous := models.Sync{SyncPathId: s.SyncPathId, Status: models.SyncStatusCompleted,
					IsFullSync: name != "successful-incremental", FinishAt: time.Now().Unix()}
				if name == "failed" {
					previous.Status = models.SyncStatusFailed
				}
				if err := db.Db.Create(&previous).Error; err != nil {
					t.Fatal(err)
				}
			}
			full, incremental := 0, 0
			s.SyncDriver = &failureScanDriver{
				list: func(context.Context, string, string) ([]*SyncFileCache, error) { full++; return nil, nil },
				incremental: func(context.Context, string, int, int, int64) (*baidupan.FileListAllResponse, error) {
					incremental++
					return &baidupan.FileListAllResponse{}, nil
				},
			}
			s.StartBaiduPanSync()
			wantFull := name != "successful-full"
			if len(s.PathErrChan) != 0 || s.FullSync != wantFull || (full == 1) != wantFull || (incremental == 1) == wantFull {
				t.Fatalf("full=%d, incremental=%d, FullSync=%v, fatal=%d", full, incremental, s.FullSync, len(s.PathErrChan))
			}
		})
	}
}

func TestBaiduIncrementalRateWaitCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newFailureScanSyncer(t)
		calls := 0
		s.SyncDriver = &failureScanDriver{incremental: func(context.Context, string, int, int, int64) (*baidupan.FileListAllResponse, error) {
			calls++
			return &baidupan.FileListAllResponse{HasMore: 1, Cursor: uint32(calls), List: []*baidupan.FileListAllItem{{FsId: uint64(calls), Path: fmt.Sprintf("/media/%d.txt", calls)}}}, nil
		}}
		done := make(chan error, 1)
		go func() { done <- s.StartBaiduPanSyncByMtime(100) }()
		synctest.Wait()
		before := time.Now()
		s.Cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		if calls != 9 || time.Since(before) != 0 {
			t.Fatalf("calls=%d elapsed=%s", calls, time.Since(before))
		}
	})
}

func TestBaiduIncrementalFileFailureDoesNotStopSibling(t *testing.T) {
	s := newFailureScanSyncer(t)
	s.SyncDriver = &failureScanDriver{
		incremental: func(context.Context, string, int, int, int64) (*baidupan.FileListAllResponse, error) {
			return &baidupan.FileListAllResponse{List: []*baidupan.FileListAllItem{
				{FsId: 1, Path: "/media/bad.mkv", Size: 1024},
				{FsId: 2, Path: "/media/good.mkv", Size: 1024},
			}}, nil
		},
		content: func(file *SyncFileCache) string {
			if file.FileName == "bad.mkv" {
				return ""
			}
			return "http://qms.local/video"
		},
	}
	if err := s.StartBaiduPanSyncByMtime(100); err != nil {
		t.Fatal(err)
	}
	result := s.scanResultSnapshot()
	if s.NewStrm != 1 || result.SucceededFiles != 1 || result.FailedFiles != 1 {
		t.Fatalf("generated=%d, result=%+v", s.NewStrm, result)
	}
	if _, err := s.scanOutcome(); err == nil {
		t.Fatal("failed file was ignored")
	}
}

func TestBaiduIncrementalMovedDirectoryKeepsDescendantsIncomplete(t *testing.T) {
	s := newBaiduIncrementalTestSyncer(t, func(*http.Request, int) any {
		return baidupan.FileListAllResponse{List: []*baidupan.FileListAllItem{
			{FsId: 10, Path: "/media/new", IsDir: 1},
		}}
	})
	old := []models.SyncFile{
		{SyncPathId: s.SyncPathId, SourceType: models.SourceTypeBaiduPan, FileId: "/media/old", PickCode: "10", Path: "/media", ParentId: "/media", FileName: "old", FileType: v115open.TypeDir},
		{SyncPathId: s.SyncPathId, SourceType: models.SourceTypeBaiduPan, FileId: "/media/old/child.mkv", PickCode: "11", Path: "/media/old", ParentId: "/media/old", FileName: "child.mkv", FileType: v115open.TypeFile, FileSize: 1024},
	}
	if err := db.Db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.StartBaiduPanSyncByMtime(100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.memSyncCache.GetByFileId("/media/old/child.mkv"); err != nil {
		t.Fatalf("old descendant was lost: %v", err)
	}
	if _, err := s.memSyncCache.GetByFileId("/media/new/child.mkv"); err == nil {
		t.Fatal("descendant new path was invented")
	}
	result := s.scanResultSnapshot()
	if len(result.Failures) != 2 || result.Failures[0].Path != "/media/new" || result.Failures[1].Path != "/media/old" {
		t.Fatalf("moved directory scopes=%+v", result)
	}
	if status, err := s.scanOutcome(); status != models.SyncStatusIncomplete || err == nil {
		t.Fatalf("status=%v err=%v, want incomplete", status, err)
	}
}

func TestBaiduIncrementalAndOldCacheKeepExclusionRules(t *testing.T) {
	for _, old := range []bool{false, true} {
		t.Run(fmt.Sprintf("old=%t", old), func(t *testing.T) {
			items := []*baidupan.FileListAllItem{
				{FsId: 1, Path: "/media/Movie.mkv", Size: 2048},
				{FsId: 2, Path: "/media/SAMPLE.mkv", Size: 2048},
				{FsId: 3, Path: "/media/Extras/movie.mkv", Size: 2048},
				{FsId: 4, Path: "/media/Extras", IsDir: 1},
				{FsId: 5, Path: "/media/bonus.mkv", Size: 2048},
				{FsId: 6, Path: "/media/Bonus.mkv", Size: 2048},
			}
			s := newBaiduIncrementalTestSyncer(t, func(_ *http.Request, _ int) any {
				if old {
					return baidupan.FileListAllResponse{}
				}
				return baidupan.FileListAllResponse{List: items}
			})
			s.Config.ExcludeNames = []string{"sample.mkv"}
			s.Config.ExcludeNameRegexes = []string{`^Extras$`, `^bonus`}
			if err := s.Config.compileExcludeNameRegexes(); err != nil {
				t.Fatal(err)
			}
			if old {
				for _, item := range items {
					kind := v115open.TypeFile
					if item.IsDir == 1 {
						kind = v115open.TypeDir
					}
					row := &models.SyncFile{SyncPathId: s.SyncPathId, FileId: item.Path,
						PickCode: strconv.FormatUint(item.FsId, 10), FileName: filepath.Base(item.Path),
						Path: filepath.Dir(item.Path), FileType: kind, FileSize: int64(item.Size), SourceType: models.SourceTypeBaiduPan}
					if err := db.Db.Create(row).Error; err != nil {
						t.Fatal(err)
					}
				}
				// 旧名称中含路径时，仍检查其中的目录片段。
				row := &models.SyncFile{SyncPathId: s.SyncPathId, FileId: "/media/Extras/legacy.mkv",
					FileName: "Extras/legacy.mkv", Path: "/media", FileType: v115open.TypeFile,
					FileSize: 2048, SourceType: models.SourceTypeBaiduPan}
				if err := db.Db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := s.StartBaiduPanSyncByMtime(100); err != nil {
				t.Fatal(err)
			}
			if s.memSyncCache.Count() != 2 {
				t.Fatalf("cache=%d want=2", s.memSyncCache.Count())
			}
			for _, name := range []string{"Movie.mkv", "Bonus.mkv"} {
				if _, err := s.memSyncCache.GetByFileId("/media/" + name); err != nil {
					t.Fatal(err)
				}
			}
			wantGenerated := int64(2)
			if old {
				wantGenerated = 0
			}
			if s.NewStrm != wantGenerated {
				t.Fatalf("generated=%d want=%d", s.NewStrm, wantGenerated)
			}
		})
	}
}
