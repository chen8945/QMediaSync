package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/v115open"
)

type generationDirectoryTestDriver struct {
	fakeDirectoryScanDriver
	calls int
	list  func(context.Context) ([]*SyncFileCache, error)
}

func (d *generationDirectoryTestDriver) GetNetFileFiles(ctx context.Context, _, _ string) ([]*SyncFileCache, error) {
	d.calls++
	return d.list(ctx)
}

func TestGenerationDirectoryCopiesAndRetries(t *testing.T) {
	original := &SyncFileCache{FileId: "meta", FileName: "movie.nfo", Paths: []v115open.FileDetailPath{{Name: "remote"}}}
	driver := &generationDirectoryTestDriver{}
	driver.list = func(context.Context) ([]*SyncFileCache, error) { return []*SyncFileCache{original, nil}, nil }
	syncer := &SyncStrm{SyncDriver: driver}
	file := &SyncFileCache{SourceType: models.SourceType115, ParentId: "parent", Path: "/remote"}
	directory := &generationDirectory{}
	first, err := directory.list(t.Context(), syncer, file)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Paths[0].Name = "changed"
	first[0].LocalFilePath = "/wrong"
	first[0].IsVideo = true
	second, err := directory.list(t.Context(), syncer, file)
	if err != nil {
		t.Fatal(err)
	}
	if driver.calls != 1 || !reflect.DeepEqual(second[0], original) || second[1] != nil {
		t.Fatalf("复用或独立副本错误: calls=%d files=%+v", driver.calls, second)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := directory.list(ctx, syncer, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消命中: %v", err)
	}
	if driver.calls != 1 {
		t.Fatal("取消后仍然请求")
	}
	for _, mode := range []string{"error", "cancel", "changed-path", "other-source", "empty"} {
		t.Run(mode, func(t *testing.T) {
			d := &generationDirectoryTestDriver{}
			f := *file
			localCtx, stop := context.WithCancel(t.Context())
			defer stop()
			d.list = func(context.Context) ([]*SyncFileCache, error) {
				if d.calls == 1 {
					switch mode {
					case "error":
						return []*SyncFileCache{original}, errors.New("第二页失败")
					case "cancel":
						stop()
						return []*SyncFileCache{original}, nil
					case "changed-path":
						return []*SyncFileCache{{Path: "/moved"}}, nil
					}
				}
				return nil, nil
			}
			if mode == "other-source" {
				f.SourceType = models.SourceTypeLocal
			}
			s := &SyncStrm{SyncDriver: d}
			cache := &generationDirectory{}
			_, _ = cache.list(localCtx, s, &f)
			if _, err := cache.list(t.Context(), s, &f); err != nil {
				t.Fatal(err)
			}
			want := 2
			if mode == "empty" {
				want = 1
			}
			if d.calls != want {
				t.Fatalf("requests=%d want=%d", d.calls, want)
			}
		})
	}
}

func TestGenerateReusesOwnerDirectoryForMetadata(t *testing.T) {
	for _, tc := range []struct {
		name           string
		conflict, meta bool
		calls          int
	}{
		{"owner-and-meta-baseline", true, true, 2}, {"owner-and-meta", true, true, 1}, {"owner-only", true, false, 1}, {"meta-only", false, true, 1}, {"neither", false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			account, sp := setupStrmGenerationServiceTestDB(t)
			service := newTestGenerationService(t, sp, account)
			if tc.name == "owner-and-meta-baseline" {
				service.resolveStrmOwner = func(ctx context.Context, s *SyncStrm, f *SyncFileCache, _ *generationDirectory) (bool, error) {
					return resolveLatest115StrmOwner(ctx, s, f)
				}
			}
			original := []*SyncFileCache{
				{FileId: "older", FileName: "movie.mp4", ParentId: "parent", Path: "/remote", SourceType: models.SourceType115, MTime: 1},
				{FileId: "nfo", FileName: "movie.nfo", ParentId: "parent", Path: "/remote", SourceType: models.SourceType115, PickCode: "nfo-pick"},
				{FileId: "thumb", FileName: "movie-thumb.jpg", ParentId: "parent", Path: "/remote", SourceType: models.SourceType115, PickCode: "thumb-pick"},
				{FileId: "other", FileName: "other.nfo", ParentId: "parent", Path: "/remote", SourceType: models.SourceType115},
			}
			driver := &generationDirectoryTestDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.local/movie"}}
			driver.list = func(context.Context) ([]*SyncFileCache, error) { return original, nil }
			build := service.buildSyncer
			service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
				s, err := build(p, a, c)
				s.SyncDriver = driver
				s.Sync = &models.Sync{Logger: helpers.AppLogger}
				s.Config.MetaExt = []string{".nfo", ".jpg"}
				return s, err
			}
			if tc.conflict {
				row := &models.SyncFile{SyncPathId: sp.ID, FileId: "older", LocalFilePath: filepath.Join(sp.LocalPath, "remote", "movie.strm"), IsVideo: true}
				if err := db.Db.Create(row).Error; err != nil {
					t.Fatal(err)
				}
			}
			task := &models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, FileId: "video", PickCode: "video-pick", ParentId: "parent", Path: "/remote", FileName: "movie.mkv", FileSize: 1024, Mtime: 100, Sha1: "sha", DownloadMeta: tc.meta}
			for run := 1; run <= 2; run++ {
				result, err := service.Generate(t.Context(), StrmGenerationInput{Task: task})
				if err != nil {
					t.Fatal(err)
				}
				if driver.calls != tc.calls*run {
					t.Fatalf("第%d次生成请求=%d want=%d", run, driver.calls, tc.calls*run)
				}
				data, err := os.ReadFile(filepath.Join(sp.LocalPath, "remote", "movie.strm"))
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != "http://qms.local/movie" {
					t.Fatalf("内容=%q", data)
				}
				if run == 1 && !result.Changed {
					t.Fatal("最新视频未生成")
				}
			}
			var downloads []models.DbDownloadTask
			if err := db.Db.Order("file_name").Find(&downloads).Error; err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, d := range downloads {
				names = append(names, d.FileName)
			}
			var want []string
			if tc.meta {
				want = []string{"movie-thumb.jpg", "movie.nfo"}
			}
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("downloads=%v want=%v", names, want)
			}
			if original[0].IsVideo || original[0].LocalFilePath != "" || original[1].IsMeta {
				t.Fatal("原始目录条目被修改")
			}
		})
	}
}

func TestGenerationBatchDirectoryValidity(t *testing.T) {
	for _, mode := range []string{"reuse", "observed-missing", "observed-move", "observed-move-sync-path", "missing", "size", "name", "mtime", "sha", "pick", "parent", "path", "account", "sync-path", "target", "source", "expired", "slow-read", "write", "other-account-write", "during-read", "error", "cancel", "owner-fresh"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			batch := &generationDirectories{now: func() time.Time { return now }}
			file := &SyncFileCache{FileId: "1", FileName: "movie.mkv", FileSize: 10, MTime: 1, Sha1: "sha", PickCode: "pick", ParentId: "parent", Path: "/remote", SourceType: models.SourceType115}
			driver := &generationDirectoryTestDriver{}
			mutate := func() {
				client := v115open.NewClient(1, "", "", "")
				_, _ = client.Move(t.Context(), []string{"1"}, "2")
			}
			driver.list = func(context.Context) ([]*SyncFileCache, error) {
				if driver.calls == 1 {
					switch mode {
					case "slow-read":
						now = now.Add(5 * time.Second)
					case "during-read":
						mutate()
					case "error":
						return nil, errors.New("分页失败")
					}
				}
				return cloneGenerationDirectory([]*SyncFileCache{file}), nil
			}
			s := &SyncStrm{Account: &models.Account{BaseModel: models.BaseModel{ID: 1}}, SyncDriver: driver}
			first := &generationDirectory{batch: batch}
			_, _ = first.list(t.Context(), s, file)
			if mode == "slow-read" && len(batch.entries) != 0 {
				t.Fatal("慢读取被发布到批缓存")
			}
			next := *file
			s2 := SyncStrm{Account: s.Account, SyncDriver: driver}
			switch mode {
			case "observed-move", "observed-move-sync-path":
				other := next
				other.ParentId = "moved"
				other.Path = "/moved"
				observer := s
				if mode == "observed-move-sync-path" {
					observer = &SyncStrm{Account: s.Account, SyncPathId: 2}
				}
				batch.observe(observer, &other)
			case "observed-missing":
				other := next
				other.FileId = "new"
				batch.observe(s, &other)
			case "missing":
				next.FileId = "2"
			case "size":
				next.FileSize++
			case "name":
				next.FileName = "other.mkv"
			case "mtime":
				next.MTime++
			case "sha":
				next.Sha1 = "new"
			case "pick":
				next.PickCode = "new"
			case "parent":
				next.ParentId = "new"
			case "path":
				next.Path = "/moved"
			case "account":
				s2.Account = &models.Account{}
				s2.Account.ID = 2
			case "sync-path":
				s2.SyncPathId = 2
			case "target":
				s2.TargetPath = "/other"
			case "source":
				next.SourceType = models.SourceTypeLocal
			case "expired":
				now = now.Add(5 * time.Second)
			case "write":
				mutate()
			case "other-account-write":
				client := v115open.NewClient(2, "", "", "")
				_, _ = client.Move(t.Context(), []string{"1"}, "2")
			}
			second := &generationDirectory{batch: batch}
			var err error
			if mode == "cancel" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				_, err = second.list(ctx, &s2, &next)
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return
			}
			if mode == "owner-fresh" {
				_, err = second.listFresh(t.Context(), &s2, &next)
			} else {
				_, err = second.list(t.Context(), &s2, &next)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if mode == "reuse" || mode == "other-account-write" {
				want = 1
			}
			if driver.calls != want {
				t.Fatalf("calls=%d want=%d", driver.calls, want)
			}
		})
	}
}

func TestProcessPendingReusesDirectoryWithinBatch(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, sp, account)
	var files []*SyncFileCache
	for i := 1; i <= 3; i++ {
		name := fmt.Sprintf("movie%d.mkv", i)
		id := fmt.Sprint(i)
		files = append(files, &SyncFileCache{FileId: id, FileName: name, FileSize: 1024, MTime: 1, Sha1: "sha", PickCode: id, ParentId: "parent", Path: "/remote", SourceType: models.SourceType115})
		files = append(files, &SyncFileCache{FileId: "meta" + id, FileName: fmt.Sprintf("movie%d.nfo", i), PickCode: "meta" + id, ParentId: "parent", Path: "/remote", SourceType: models.SourceType115})
		_, err := models.EnqueueStrmGenerationTask(&models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, FileId: id, FileName: name, FileSize: 1024, Mtime: 1, Sha1: "sha", PickCode: id, ParentId: "parent", Path: "/remote", DownloadMeta: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	driver := &generationDirectoryTestDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.local/movie"}}
	driver.list = func(context.Context) ([]*SyncFileCache, error) { return cloneGenerationDirectory(files), nil }
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.SyncDriver = driver
		s.Config.MetaExt = []string{".nfo"}
		s.Sync = &models.Sync{Logger: helpers.AppLogger}
		return s, err
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 2); err != nil || n != 2 {
		t.Fatalf("first=%d %v", n, err)
	}
	if driver.calls != 1 {
		t.Fatalf("同批读取次数=%d", driver.calls)
	}
	if n, err := ProcessPendingStrmGenerationTasks(t.Context(), service, 2); err != nil || n != 1 {
		t.Fatalf("next=%d %v", n, err)
	}
	if driver.calls != 2 {
		t.Fatalf("下批没有重新读取: %d", driver.calls)
	}
	var downloads []models.DbDownloadTask
	if err := db.Db.Order("file_name").Find(&downloads).Error; err != nil {
		t.Fatal(err)
	}
	if len(downloads) != 3 {
		t.Fatalf("元数据任务=%d", len(downloads))
	}
	for i, d := range downloads {
		if d.FileName != fmt.Sprintf("movie%d.nfo", i+1) {
			t.Fatalf("错误匹配=%s", d.FileName)
		}
	}
	var completed int64
	if err := db.Db.Model(&models.StrmGenerationTask{}).Where("status = ?", models.StrmGenerationStatusCompleted).Count(&completed).Error; err != nil || completed != 3 {
		t.Fatalf("完成=%d %v", completed, err)
	}
}

func TestGenerationBatchOwnerReadsCurrentDirectory(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	service := newTestGenerationService(t, sp, account)
	syncer, err := service.buildSyncer(sp, account, nil)
	if err != nil {
		t.Fatal(err)
	}
	file := &SyncFileCache{FileId: "video", FileName: "movie.mkv", FileSize: 10, MTime: 10, ParentId: "parent", Path: "/remote", SourceType: models.SourceType115, IsVideo: true}
	old := *file
	old.FileId = "other"
	old.FileName = "movie.mp4"
	old.MTime = 1
	row := &models.SyncFile{SyncPathId: sp.ID, FileId: old.FileId, LocalFilePath: file.GetLocalFilePath(syncer.TargetPath, syncer.SourcePath), IsVideo: true}
	if err := db.Db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	driver := &generationDirectoryTestDriver{}
	driver.list = func(context.Context) ([]*SyncFileCache, error) {
		return cloneGenerationDirectory([]*SyncFileCache{file, &old}), nil
	}
	syncer.SyncDriver = driver
	syncer.Sync = &models.Sync{Logger: helpers.AppLogger}
	batch := &generationDirectories{now: time.Now}
	for run, want := range []bool{true, false} {
		if run == 1 {
			old.MTime = 20
		}
		owner, err := service.resolveStrmOwner(t.Context(), syncer, file, &generationDirectory{batch: batch})
		if err != nil || owner != want {
			t.Fatalf("run=%d owner=%v err=%v", run, owner, err)
		}
	}
	if driver.calls != 2 {
		t.Fatalf("同名归属沿用旧列表: calls=%d", driver.calls)
	}
}
