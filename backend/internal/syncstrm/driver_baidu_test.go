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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type baiduListTransport func(*http.Request) (*http.Response, error)

func (transport baiduListTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

// 仅替换 HTTP 传输，保留真实客户端的参数构造、响应解码和错误处理。
func newBaiduListTestDriver(t *testing.T, handle func(*http.Request, int) (*http.Response, error)) *BaiduPanDriver {
	t.Helper()
	oldClient, oldLogger, oldBaiduLogger := http.DefaultClient, helpers.AppLogger, helpers.BaiduPanLog
	t.Cleanup(func() {
		http.DefaultClient, helpers.AppLogger, helpers.BaiduPanLog = oldClient, oldLogger, oldBaiduLogger
	})
	logger := &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.AppLogger, helpers.BaiduPanLog = logger, logger
	http.DefaultClient = &http.Client{Transport: baiduListTransport(func(request *http.Request) (*http.Response, error) {
		query := request.URL.Query()
		if query.Get("method") != "list" || query.Get("dir") != "/media" ||
			query.Get("limit") != "50" || query.Get("folder") != "0" || query.Get("showempty") != "1" {
			return nil, fmt.Errorf("unexpected directory-list request: %s", request.URL.Path)
		}
		start, err := strconv.Atoi(query.Get("start"))
		if err != nil {
			return nil, err
		}
		return handle(request, start)
	})}
	driver := NewBaiduPanDriver(baidupan.NewBaiDuPanClientWithToken("test-token"))
	driver.SetSyncStrm(&SyncStrm{
		Context: t.Context(), Sync: &models.Sync{Logger: logger},
		lastProgressPublishedAt: time.Now().Add(time.Hour),
	})
	return driver
}

func baiduListTestResponse(request *http.Request, status, errno int, files []*baidupan.FileInfo) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	recorder.Header().Set("Content-Type", "application/json")
	recorder.WriteHeader(status)
	if err := json.NewEncoder(recorder).Encode(baidupan.FileListResponse{Errno: int32(errno), List: files}); err != nil {
		return nil, err
	}
	response := recorder.Result()
	response.Request = request
	return response, nil
}

func baiduListTestFiles(start, end int) []*baidupan.FileInfo {
	files := make([]*baidupan.FileInfo, 0, max(end-start, 0))
	for index := start; index < end; index++ {
		files = append(files, &baidupan.FileInfo{
			FsId: uint64(index + 1), Path: fmt.Sprintf("/media/%03d.mkv", index),
			Size: 1024, ServerMtime: 200, Md5: "md5",
		})
	}
	return files
}

func TestBaiduPanDriverCreateDirPreservesAuthenticationFailure(t *testing.T) {
	oldClient, oldLogger := http.DefaultClient, helpers.BaiduPanLog
	t.Cleanup(func() { http.DefaultClient, helpers.BaiduPanLog = oldClient, oldLogger })
	helpers.BaiduPanLog = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	calls := 0
	http.DefaultClient = &http.Client{Transport: baiduListTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Query().Get("method") != "create" {
			return nil, fmt.Errorf("unexpected mkdir method: %s", request.URL.Path)
		}
		recorder := httptest.NewRecorder()
		recorder.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(recorder).Encode(map[string]any{"errno": 20016, "errmsg": "fixture expired"}); err != nil {
			return nil, err
		}
		response := recorder.Result()
		response.Request = request
		return response, nil
	})}
	driver := NewBaiduPanDriver(baidupan.NewBaiDuPanClientWithToken("fixture-token"))
	_, _, err := driver.CreateDirRecursively(t.Context(), "/media/extras")
	tokenErr, ok := errors.AsType[*baidupan.TokenInvalidError](err)
	if !ok || tokenErr.Errno != 20016 || !isFatalSyncError(err) || calls != 1 {
		t.Fatalf("error=%v calls=%d; mkdir must retain shared authentication failure", err, calls)
	}
}

func TestBaiduPanDriverGetNetFileFilesPagination(t *testing.T) {
	for _, total := range []int{0, 49, 50, 51, 100, 101} {
		t.Run(strconv.Itoa(total), func(t *testing.T) {
			var offsets []int
			driver := newBaiduListTestDriver(t, func(request *http.Request, start int) (*http.Response, error) {
				offsets = append(offsets, start)
				return baiduListTestResponse(request, http.StatusOK, 0, baiduListTestFiles(start, min(start+50, total)))
			})
			files, err := driver.GetNetFileFiles(t.Context(), "/media", "parent")
			if err != nil {
				t.Fatal(err)
			}
			if len(files) != total || driver.s.TotalFile != int64(total) {
				t.Fatalf("files = %d, total = %d, want %d; offsets = %v", len(files), driver.s.TotalFile, total, offsets)
			}
			var wantOffsets []int
			for start := 0; start <= total; start += 50 {
				wantOffsets = append(wantOffsets, start)
			}
			if !slices.Equal(offsets, wantOffsets) {
				t.Fatalf("offsets = %v, want %v", offsets, wantOffsets)
			}
			for index, file := range files {
				if file.FileId != fmt.Sprintf("/media/%03d.mkv", index) || file.PickCode != strconv.Itoa(index+1) ||
					file.ParentId != "parent" || file.Path != "/media" || file.FileType != v115open.TypeFile ||
					file.SourceType != models.SourceTypeBaiduPan || file.FileSize != 1024 || file.MTime != 200 || file.Sha1 != "md5" {
					t.Fatalf("unexpected file at index %d: %+v", index, file)
				}
			}
		})
	}
}

func TestBaiduPanDriverGetNetFileFilesCountsBeforeFiltering(t *testing.T) {
	var offsets []int
	driver := newBaiduListTestDriver(t, func(request *http.Request, start int) (*http.Response, error) {
		offsets = append(offsets, start)
		return baiduListTestResponse(request, http.StatusOK, 0, baiduListTestFiles(start, min(start+50, 51)))
	})
	driver.s.Config = SyncStrmConfig{VideoExt: []string{".mkv"}, ExcludeNameRegexes: []string{`^0[0-4][0-9]\.mkv$`}}
	if err := driver.s.Config.compileExcludeNameRegexes(); err != nil {
		t.Fatal(err)
	}
	files, err := driver.GetNetFileFiles(t.Context(), "/media", "parent")
	if err != nil {
		t.Fatal(err)
	}
	var accepted []string
	for _, file := range files {
		if driver.s.ValidFile(file) {
			accepted = append(accepted, file.FileId)
		}
	}
	if !slices.Equal(offsets, []int{0, 50}) || driver.s.TotalFile != 51 || !slices.Equal(accepted, []string{"/media/050.mkv"}) {
		t.Fatalf("offsets = %v, raw total = %d, accepted = %v", offsets, driver.s.TotalFile, accepted)
	}
}

func TestBaiduPanDriverGetNetFileFilesPageFailure(t *testing.T) {
	for _, name := range []string{"http", "business", "cancel", "repeated"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var offsets []int
			driver := newBaiduListTestDriver(t, func(request *http.Request, start int) (*http.Response, error) {
				offsets = append(offsets, start)
				if start == 0 {
					return baiduListTestResponse(request, http.StatusOK, 0, baiduListTestFiles(0, 50))
				}
				switch name {
				case "http":
					return baiduListTestResponse(request, http.StatusBadGateway, 0, nil)
				case "business":
					return baiduListTestResponse(request, http.StatusOK, -9, nil)
				case "cancel":
					cancel()
					return nil, request.Context().Err()
				default:
					if start > 50 {
						return nil, fmt.Errorf("repeated page was not rejected")
					}
					files := baiduListTestFiles(0, 50)
					files[0].ServerMtime++ // 可变元数据不能掩盖同一完整页被再次返回。
					return baiduListTestResponse(request, http.StatusOK, 0, files)
				}
			})
			files, err := driver.GetNetFileFiles(ctx, "/media", "parent")
			if err == nil || files != nil || !slices.Equal(offsets, []int{0, 50}) || driver.s.TotalFile != 50 {
				t.Fatalf("files = %v, err = %v, offsets = %v, total = %d", files, err, offsets, driver.s.TotalFile)
			}
			if name == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}
			if name == "repeated" && !strings.Contains(err.Error(), "重复") {
				t.Fatalf("error = %v, want repeated page error", err)
			}
		})
	}
}

func TestBaiduPanDriverGetNetFileFilesPreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	driver := newBaiduListTestDriver(t, func(*http.Request, int) (*http.Response, error) {
		t.Error("canceled listing sent an HTTP request")
		return nil, context.Canceled
	})
	files, err := driver.GetNetFileFiles(ctx, "/media", "parent")
	if files != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("files = %v, err = %v, want nil/context.Canceled", files, err)
	}
}

func TestBaiduPanDriverListingFailureProtectsDirectory(t *testing.T) {
	setupStrmExclusionTestDB(t)
	models.SettingsGlobal.OpenlistRetry = 0
	var offsets []int
	driver := newBaiduListTestDriver(t, func(request *http.Request, start int) (*http.Response, error) {
		offsets = append(offsets, start)
		if start > 0 {
			return baiduListTestResponse(request, http.StatusBadGateway, 0, nil)
		}
		return baiduListTestResponse(request, http.StatusOK, 0, baiduListTestFiles(0, 50))
	})
	syncer := driver.s
	syncer.Account = &models.Account{SourceType: models.SourceTypeBaiduPan}
	syncer.SourcePath, syncer.SourcePathId = "/media", "parent"
	syncer.TargetPath = t.TempDir()
	syncer.SyncDriver = driver
	syncer.PathErrChan = make(chan error, 1)
	syncer.memSyncCache = NewMemorySyncCache(0)
	syncer.Config = SyncStrmConfig{ExcludeNameRegexes: []string{`\.mkv$`}}
	if err := syncer.Config.compileExcludeNameRegexes(); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(syncer.Sync).Error; err != nil {
		t.Fatal(err)
	}
	syncer.StartOther()
	// 可定位的列表失败进入扫描结果；执行器据此保护目录并发布扫描不完整。
	if _, err := syncer.scanOutcome(); err == nil {
		t.Fatal("listing failure was treated as a successful directory")
	}
	if len(syncer.PathErrChan) != 0 {
		t.Fatal("isolated directory error was sent as fatal")
	}
	if !slices.Equal(offsets, []int{0, 50}) || len(syncer.memSyncCache.ListAllFiles()) != 0 || syncer.NewStrm != 0 {
		t.Fatalf("partial listing was processed: offsets = %v, new STRM = %d", offsets, syncer.NewStrm)
	}
}
