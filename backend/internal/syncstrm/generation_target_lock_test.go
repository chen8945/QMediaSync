package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/syncscope"
)

func TestProcessStrmFileReleasesTargetBeforeEmby(t *testing.T) {
	for _, collision := range []bool{false, true} {
		t.Run(fmt.Sprintf("collision=%v", collision), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account, sp := setupStrmGenerationServiceTestDB(t)
				sqlDB, _ := db.Db.DB()
				defer sqlDB.Close()
				service := newTestGenerationService(t, sp, account)
				s, _ := service.buildSyncer(sp, account, nil)
				s.Context = t.Context()
				s.Sync = &models.Sync{Logger: helpers.AppLogger}
				s.SyncDriver = &fakeDirectoryScanDriver{strmContent: "http://qms.local/movie"}
				entered, resume := make(chan struct{}), make(chan struct{})
				var calls atomic.Int32
				errEmby := errors.New("Emby unavailable")
				if err := db.Db.Callback().Query().Before("gorm:query").Register("test:slow_emby", func(tx *gorm.DB) {
					if tx.Statement.Table != "emby_media_sync_files" {
						return
					}
					if calls.Add(1) == 1 {
						close(entered)
						<-resume
					}
					tx.AddError(errEmby)
				}); err != nil {
					t.Fatal(err)
				}
				file := &SyncFileCache{FileId: "video", FileName: "movie.mkv", Path: "/remote", ParentId: "/remote", SourceType: models.SourceTypeLocal, IsVideo: true, MTime: 100}
				first := make(chan error, 1)
				go func() { first <- s.ProcessStrmFile(file) }()
				select {
				case <-entered:
				case err := <-first:
					t.Fatalf("未进入 Emby: %v", err)
				}
				next := *file
				if collision {
					hash := func(path string) uint32 {
						h := fnv.New32a()
						_, _ = h.Write([]byte(path))
						return h.Sum32() % strmTargetLockCount
					}
					target := file.GetLocalFilePath(s.TargetPath, s.SourcePath)
					for i := 0; ; i++ {
						next.FileName = fmt.Sprintf("other-%d.mkv", i)
						next.LocalFilePath = ""
						if hash(next.GetLocalFilePath(s.TargetPath, s.SourcePath)) == hash(target) {
							break
						}
					}
				}
				second := make(chan error, 1)
				go func() { second <- s.ProcessStrmFile(&next) }()
				synctest.Wait()
				select {
				case err := <-second:
					if err != nil {
						t.Fatal(err)
					}
				default:
					t.Error("慢 Emby 仍占用目标分段锁")
				}
				select {
				case <-first:
					t.Error("Emby 收集未结束就返回")
				default:
				}
				close(resume)
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				want := int64(1)
				if collision {
					want = 2
				}
				if s.NewStrm != want {
					t.Fatalf("NewStrm=%d want=%d", s.NewStrm, want)
				}
				targets := s.drainEmbyRefreshTargets()
				if len(targets) != int(want) || targets[0].TargetType != models.EmbyRefreshTargetTypeLibrary {
					t.Fatalf("回退目标=%+v", targets)
				}
				info, err := os.Stat(file.GetLocalFilePath(s.TargetPath, s.SourcePath))
				if err != nil || info.ModTime().Unix() != 100 {
					t.Fatalf("mtime: %v %v", info, err)
				}
				s.SyncDriver = &fakeDirectoryScanDriver{}
				failed := *file
				failed.FileName = "failed.mkv"
				failed.LocalFilePath = ""
				if err := s.ProcessStrmFile(&failed); err == nil {
					t.Fatal("空内容写入应失败")
				}
				if s.NewStrm != want || len(s.drainEmbyRefreshTargets()) != 0 {
					t.Fatal("失败写入增加了成功计数或刷新目标")
				}
			})
		})
	}
}

func TestGenerateReleasesTargetButKeepsScopeDuringFollowup(t *testing.T) {
	for _, step := range []string{"metadata", "emby", "emby-error", "cancel"} {
		t.Run(step, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				account, sp := setupStrmGenerationServiceTestDB(t)
				sqlDB, _ := db.Db.DB()
				defer sqlDB.Close()
				service := newTestGenerationService(t, sp, account)
				entered, resume := make(chan struct{}), make(chan struct{})
				errFollowup := errors.New("followup failed")
				driver := &generationDirectoryTestDriver{fakeDirectoryScanDriver: fakeDirectoryScanDriver{strmContent: "http://qms.local/movie"}}
				driver.list = func(ctx context.Context) ([]*SyncFileCache, error) {
					close(entered)
					<-resume
					if step == "cancel" {
						return nil, ctx.Err()
					}
					return nil, errFollowup
				}
				var syncer *SyncStrm
				build := service.buildSyncer
				service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
					s, err := build(p, a, c)
					s.SyncDriver = driver
					s.Sync = &models.Sync{Logger: helpers.AppLogger}
					syncer = s
					return s, err
				}
				service.resolveRefreshTarget = func(sf *models.SyncFile) (models.EmbyRefreshTarget, error) {
					close(entered)
					<-resume
					if step == "emby-error" {
						return models.EmbyRefreshTarget{}, errFollowup
					}
					return models.EmbyRefreshTarget{TargetType: models.EmbyRefreshTargetTypeItem, ItemID: "season-1", ItemType: "Season", Recursive: true}, nil
				}
				task := &models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, FileId: "video", PickCode: "pick", ParentId: "parent", Path: "/remote", FileName: "movie.mkv", FileSize: 100, Mtime: 100, Sha1: "sha", DownloadMeta: step == "metadata" || step == "cancel", RefreshEmby: step == "emby" || step == "emby-error", ParentTaskId: 99}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				done := make(chan struct{})
				var result *StrmGenerationResult
				var resultErr error
				go func() { result, resultErr = service.Generate(ctx, StrmGenerationInput{Task: task}); close(done) }()
				<-entered
				target := filepath.Join(sp.LocalPath, "remote", "movie.strm")
				acquired := make(chan struct{})
				go func() { unlock := lockStrmTarget(target); unlock(); close(acquired) }()
				synctest.Wait()
				select {
				case <-acquired:
				default:
					t.Error("后处理仍占用目标锁")
				}
				select {
				case <-done:
					t.Error("后处理未完成就返回")
				default:
				}
				waitCtx, stop := context.WithTimeout(t.Context(), time.Second)
				_, err := syncscope.Acquire(waitCtx, sp.Scope())
				stop()
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("范围提前释放: %v", err)
				}
				var row models.SyncFile
				if err := db.Db.Where("file_id = ?", "video").First(&row).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(target); err != nil {
					t.Fatal(err)
				}
				if step == "cancel" {
					cancel()
				}
				close(resume)
				<-done
				if step == "emby" {
					if resultErr != nil || !result.Changed || len(result.RefreshTargets) != 1 || result.RefreshTargets[0].ItemID != "season-1" || !result.RefreshTargets[0].Recursive {
						t.Fatalf("result=%+v err=%v", result, resultErr)
					}
				} else if resultErr == nil {
					t.Fatal("后处理错误被吞掉")
				}
				if syncer.NewStrm != 0 || len(syncer.drainEmbyRefreshTargets()) != 0 {
					t.Fatal("临时同步器仍收集无人消费的刷新和进度")
				}
				release, err := syncscope.Acquire(t.Context(), sp.Scope())
				if err != nil {
					t.Fatal(err)
				}
				release()
			})
		})
	}
}

func TestGenerateResolvesEmbyOnlyOnce(t *testing.T) {
	account, sp := setupStrmGenerationServiceTestDB(t)
	sqlDB, _ := db.Db.DB()
	defer sqlDB.Close()
	service := newTestGenerationService(t, sp, account)
	build := service.buildSyncer
	service.buildSyncer = func(p *models.SyncPath, a *models.Account, c *SyncStrmConfig) (*SyncStrm, error) {
		s, err := build(p, a, c)
		s.Sync = &models.Sync{Logger: helpers.AppLogger}
		s.SyncDriver = &fakeDirectoryScanDriver{strmContent: "http://qms.local/movie"}
		return s, err
	}
	calls := 0
	failed := errors.New("lookup failed")
	if err := db.Db.Callback().Query().Before("gorm:query").Register("test:count_emby", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_media_sync_files" {
			calls++
			tx.AddError(failed)
		}
	}); err != nil {
		t.Fatal(err)
	}
	task := &models.StrmGenerationTask{Source: models.StrmGenerationSourceWebhook, TaskType: models.StrmGenerationTaskTypeFile, SyncPathId: sp.ID, AccountId: account.ID, FileId: "video", PickCode: "pick", ParentId: "parent", Path: "/remote", FileName: "movie.mkv", FileSize: 100, Mtime: 100, Sha1: "sha", RefreshEmby: true}
	_, err := service.Generate(t.Context(), StrmGenerationInput{Task: task})
	if !errors.Is(err, failed) || calls != 1 {
		t.Fatalf("解析次数=%d err=%v", calls, err)
	}
}
