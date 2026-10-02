package syncstrm

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/v115open"
)

func setupSyncStateTestDB(t *testing.T) {
	t.Helper()
	previousDB, previousLogger, previousHub := db.Db, helpers.AppLogger, realtime.GlobalSyncTaskHub
	testDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sync.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		sqlDB.Close()
		db.Db, helpers.AppLogger, realtime.GlobalSyncTaskHub = previousDB, previousLogger, previousHub
	})
	db.Db = testDB
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	realtime.GlobalSyncTaskHub = realtime.NewSyncTaskHub()
	if err := testDB.AutoMigrate(&models.Sync{}, &models.SyncFile{}); err != nil {
		t.Fatal(err)
	}
}

func newProgressTestSync(t *testing.T) *SyncStrm {
	t.Helper()
	setupSyncStateTestDB(t)
	record := &models.Sync{
		Status: models.SyncStatusInProgress, Logger: helpers.AppLogger,
		SyncPathId: 42, FileOffset: 7, IsFullSync: true,
		RemotePath: "/remote", LocalPath: "/target", BaseCid: "root",
	}
	if err := db.Db.Create(record).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &SyncStrm{Sync: record, Context: ctx, Cancel: cancel, Account: &models.Account{SourceType: models.SourceTypeLocal}}
}

func TestPublishProgressHasOnePublisher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newProgressTestSync(t)
		release := make(chan struct{})
		var calls atomic.Int32
		if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("hold_progress", func(tx *gorm.DB) {
			calls.Add(1)
			<-release
		}); err != nil {
			t.Fatal(err)
		}
		var workers sync.WaitGroup
		for range 3 {
			workers.Go(func() {
				if err := s.PublishProgress(false); err != nil {
					t.Error(err)
				}
			})
		}
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Errorf("并发 SQL 发布者 = %d，期望 1", got)
		}
		close(release)
		workers.Wait()
	})
}

func TestPublishProgressPersistsZeroCounts(t *testing.T) {
	s := newProgressTestSync(t)
	if err := db.Db.Model(&models.Sync{}).Where("id = ?", s.Sync.ID).Updates(map[string]any{
		"total": 8, "new_strm": 3, "new_meta": 2, "new_upload": 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.PublishProgress(true); err != nil {
		t.Fatal(err)
	}
	var got models.Sync
	if err := db.Db.First(&got, s.Sync.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Total != 0 || got.NewStrm != 0 || got.NewMeta != 0 || got.NewUpload != 0 {
		t.Fatalf("最终零计数未保存：total=%d strm=%d meta=%d upload=%d", got.Total, got.NewStrm, got.NewMeta, got.NewUpload)
	}
}

func TestPublishProgressHonorsIntervalAndForce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newProgressTestSync(t)
		events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(s.Sync.ID, "", 8)
		defer unsubscribe()
		for _, step := range []struct {
			delay time.Duration
			force bool
			want  int
		}{
			{0, false, 1},
			{200 * time.Millisecond, false, 1},
			{0, true, 2},
			{time.Second, false, 3},
		} {
			time.Sleep(step.delay)
			if err := s.PublishProgress(step.force); err != nil {
				t.Fatal(err)
			}
			if len(events) != step.want {
				t.Fatalf("事件数 = %d，期望 %d", len(events), step.want)
			}
		}
	})
}

func TestPublishProgressOrdersForcedAndTerminalUpdates(t *testing.T) {
	for _, operation := range []string{"force", "substatus", "complete", "failed"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newProgressTestSync(t)
				s.TotalFile, s.NewStrm, s.NewMeta, s.NewUpload = 8, 3, 2, 1
				events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(s.Sync.ID, "", 16)
				defer unsubscribe()
				release := make(chan struct{})
				var calls atomic.Int32
				if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("hold_first", func(tx *gorm.DB) {
					if calls.Add(1) == 1 {
						<-release
					}
				}); err != nil {
					t.Fatal(err)
				}
				first := make(chan error, 1)
				go func() { first <- s.PublishProgress(false) }()
				synctest.Wait()
				atomic.StoreInt64(&s.TotalFile, 12)
				atomic.StoreInt64(&s.NewStrm, 6)
				atomic.StoreInt64(&s.NewMeta, 4)
				atomic.StoreInt64(&s.NewUpload, 2)
				next := make(chan error, 1)
				cause := errors.New("generation failed")
				go func() {
					switch operation {
					case "force":
						next <- s.PublishProgress(true)
					case "substatus":
						next <- s.updateSyncSubStatus(models.SyncSubStatusProcessLocalFileList)
					case "complete":
						next <- s.completeSync()
					case "failed":
						next <- s.failSync(cause)
					}
				}()
				synctest.Wait()
				if len(next) != 0 || calls.Load() != 1 {
					t.Errorf("%s 没有等待旧发布：done=%d calls=%d", operation, len(next), calls.Load())
				}
				if err := s.PublishProgress(false); err != nil {
					t.Fatal(err)
				}
				close(release)
				if err := <-first; err != nil {
					t.Fatal(err)
				}
				if err := <-next; err != nil && !(operation == "failed" && errors.Is(err, cause)) {
					t.Fatal(err)
				}
				if err := s.PublishProgress(true); err != nil {
					t.Fatal(err)
				}
				var row models.Sync
				if err := db.Db.First(&row, s.Sync.ID).Error; err != nil {
					t.Fatal(err)
				}
				if row.Total != 12 || row.NewStrm != 6 || row.NewMeta != 4 || row.NewUpload != 2 {
					t.Fatalf("最终计数未一致保存：%+v", row)
				}
				if row.SyncPathId != 42 || row.FileOffset != 7 || !row.IsFullSync ||
					row.RemotePath != "/remote" || row.LocalPath != "/target" || row.BaseCid != "root" {
					t.Fatalf("进度/状态更新改写了无关字段：%+v", row)
				}
				if operation == "substatus" && row.SubStatus != models.SyncSubStatusProcessLocalFileList {
					t.Fatalf("阶段未保存：%d", row.SubStatus)
				}
				var last realtime.TaskStreamEvent
				generationFinished := false
				for len(events) > 0 {
					last = <-events
					if generationFinished {
						t.Fatalf("终态后出现旧发布：%+v", last)
					}
					generationFinished = last.Payload.Status == int(models.SyncStatusCompleted) || last.Payload.Status == int(models.SyncStatusFailed)
				}
				if last.Payload.Total != row.Total || last.Payload.NewStrm != row.NewStrm ||
					last.Payload.NewMeta != row.NewMeta || last.Payload.NewUpload != row.NewUpload ||
					last.Payload.UpdatedAt != row.UpdatedAt || last.Payload.Status != int(row.Status) ||
					last.Payload.SubStatus != int(row.SubStatus) {
					t.Fatalf("最后事件与数据库不一致：event=%+v row=%+v", last.Payload, row)
				}
				if wantFinished := operation == "complete" || operation == "failed"; generationFinished != wantFinished {
					t.Fatalf("generationFinished=%v，期望 %v", generationFinished, wantFinished)
				}
				if wantTerminal := operation == "failed"; last.Terminal != wantTerminal {
					t.Fatalf("terminal=%v，期望 %v", last.Terminal, wantTerminal)
				}
				if operation == "complete" && (last.Payload.LedgerStatus == nil || *last.Payload.LedgerStatus != realtime.SyncLedgerPending) {
					t.Fatalf("生成完成后详情流应等待账本：%+v", last.Payload)
				}
			})
		})
	}
}

type preloadProgressTestDriver struct {
	fakeDirectoryScanDriver
	listDirs func(context.Context, string) ([]pathQueueItem, error)
}

func (d *preloadProgressTestDriver) GetDirsByPathId(ctx context.Context, id string) ([]pathQueueItem, error) {
	return d.listDirs(ctx, id)
}

func TestPreload115DirsWaitsBeforeTerminalStatus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newProgressTestSync(t)
		s.SourcePath, s.SourcePathId = "Media", "root"
		s.sync115, s.memSyncCache = &Sync115{}, NewMemorySyncCache(1)
		slowStarted, release := make(chan struct{}), make(chan struct{})
		wantErr := fatalSyncError(errors.New("directory unavailable"))
		s.SyncDriver = &preloadProgressTestDriver{
			fakeDirectoryScanDriver: fakeDirectoryScanDriver{detailsByID: map[string]*SyncFileCache{
				"first": {Paths: []v115open.FileDetailPath{
					{FileId: "root", Name: "Media"}, {FileId: "series", Name: "Series"}, {FileId: "season", Name: "Season"},
				}},
			}},
			listDirs: func(ctx context.Context, id string) ([]pathQueueItem, error) {
				switch id {
				case "root":
					return []pathQueueItem{{PathId: "slow", Path: "Media/slow"}, {PathId: "failed", Path: "Media/failed"}}, nil
				case "slow":
					close(slowStarted)
					<-release
					return nil, nil
				default:
					<-slowStarted
					return nil, wantErr
				}
			},
		}
		done := make(chan error, 1)
		go func() { done <- s.Preload115Dirs("first") }()
		synctest.Wait()
		if len(done) != 0 || s.Sync.Status != models.SyncStatusInProgress {
			t.Error("其他 worker 退出前不得结束预取或发布失败终态")
		}
		close(release)
		if err := <-done; !errors.Is(err, wantErr) {
			t.Fatalf("预取返回错误 = %v，期望 %v", err, wantErr)
		}
		if s.Sync.Status != models.SyncStatusInProgress {
			t.Fatal("预取错误应交由 Start 统一保存终态")
		}
	})
}

func TestPublishProgressRetriesFailedWrite(t *testing.T) {
	s := newProgressTestSync(t)
	wantErr := errors.New("progress unavailable")
	calls := 0
	if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("fail_first_progress", func(tx *gorm.DB) {
		calls++
		if calls == 1 {
			tx.AddError(wantErr)
		}
	}); err != nil {
		t.Fatal(err)
	}
	events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(s.Sync.ID, "", 4)
	defer unsubscribe()
	if err := s.PublishProgress(false); !errors.Is(err, wantErr) {
		t.Fatalf("错误 = %v，期望 %v", err, wantErr)
	}
	if len(events) != 0 {
		t.Fatal("失败的 SQL 不应发布事件")
	}
	if err := s.PublishProgress(false); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(events) != 1 {
		t.Fatalf("失败后未立即重试：calls=%d events=%d", calls, len(events))
	}
}

func TestPublishProgressCancellation(t *testing.T) {
	for _, waiting := range []string{"publisher", "database"} {
		t.Run(waiting, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newProgressTestSync(t)
				s.TotalFile = 8
				release := make(chan struct{})
				if waiting == "database" {
					sqlDB, err := db.Db.DB()
					if err != nil {
						t.Fatal(err)
					}
					conn, err := sqlDB.Conn(t.Context())
					if err != nil {
						t.Fatal(err)
					}
					defer conn.Close()
					go func() { <-release; conn.Close() }()
				} else if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("hold_progress", func(tx *gorm.DB) {
					<-release
				}); err != nil {
					t.Fatal(err)
				}
				first := make(chan error, 1)
				go func() { first <- s.PublishProgress(false) }()
				synctest.Wait()
				next := make(chan error, 1)
				go func() { next <- s.PublishProgress(true) }()
				synctest.Wait()
				s.Cancel()
				synctest.Wait()
				if len(next) != 1 {
					t.Error("force 等待未响应取消")
				}
				if waiting == "database" && len(first) != 1 {
					t.Error("真实数据库连接等待未响应取消")
				}
				close(release)
				for _, err := range []error{<-first, <-next} {
					if !errors.Is(err, context.Canceled) {
						t.Errorf("错误 = %v，期望 context.Canceled", err)
					}
				}
				var row models.Sync
				if err := db.Db.First(&row, s.Sync.ID).Error; err != nil {
					t.Fatal(err)
				}
				if row.Total != 0 {
					t.Fatalf("取消后仍提交计数：%d", row.Total)
				}
			})
		})
	}
}

func TestCompleteSyncRejectsRequiredWriteFailure(t *testing.T) {
	for _, failing := range []string{"progress", "status", "deleted"} {
		t.Run(failing, func(t *testing.T) {
			s := newProgressTestSync(t)
			s.TotalFile, s.NewStrm = 2, 1
			events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(s.Sync.ID, "", 8)
			defer unsubscribe()
			wantErr := errors.New("required write failed")
			if failing == "deleted" {
				if err := db.Db.Delete(&models.Sync{}, s.Sync.ID).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("fail_required_write", func(tx *gorm.DB) {
				row, ok := tx.Statement.Dest.(*models.Sync)
				if ok && ((failing == "status" && row.Status == models.SyncStatusCompleted) ||
					(failing == "progress" && len(tx.Statement.Selects) > 0 && tx.Statement.Selects[0] == "total")) {
					tx.AddError(wantErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err := s.completeSync()
			if err == nil || (failing != "deleted" && !errors.Is(err, wantErr)) {
				t.Fatalf("必要写入错误未返回：%v", err)
			}
			if s.Sync.Status == models.SyncStatusCompleted {
				t.Fatal("未提交成功却修改了内存成功状态")
			}
			if failing != "deleted" {
				if got := s.failSync(err); !errors.Is(got, wantErr) {
					t.Fatalf("失败收尾丢失原始错误：%v", got)
				}
				var row models.Sync
				if err := db.Db.First(&row, s.Sync.ID).Error; err != nil {
					t.Fatal(err)
				}
				if row.Status != models.SyncStatusFailed || row.Total != 2 || row.NewStrm != 1 {
					t.Fatalf("失败终态/计数未保存：%+v", row)
				}
			} else {
				var count int64
				if err := db.Db.Model(&models.Sync{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("完成路径重建了已删除记录：count=%d err=%v", count, err)
				}
			}
			for len(events) > 0 {
				if event := <-events; event.Payload.Status == int(models.SyncStatusCompleted) {
					t.Fatalf("SQL 失败后发布了成功事件：%+v", event)
				}
			}
		})
	}
}

func TestFailSyncBoundsCancelledCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newProgressTestSync(t)
		sqlDB, err := db.Db.DB()
		if err != nil {
			t.Fatal(err)
		}
		conn, err := sqlDB.Conn(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(s.Sync.ID, "", 4)
		defer unsubscribe()
		s.Cancel()
		done := make(chan error, 1)
		go func() { done <- s.failSync(context.Canceled) }()
		synctest.Wait()
		if len(done) != 0 {
			t.Fatal("失败记录未获得独立收尾期限")
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		err = <-done
		if !errors.Is(err, context.Canceled) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("取消和收尾超时错误未保留：%v", err)
		}
		if len(events) != 0 || s.Sync.Status != models.SyncStatusInProgress {
			t.Fatal("未提交的失败状态不能伪装为已持久化")
		}
	})
}

func TestStartReturnsRequiredProgressAndStatusErrors(t *testing.T) {
	for _, failing := range []string{"start", "progress", "complete", "cancelled"} {
		t.Run(failing, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newProgressTestSync(t)
				s.backgroundService = newSyncBackgroundService()
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					if err := s.backgroundService.stop(ctx); err != nil {
						t.Error(err)
					}
				})
				if err := db.Db.AutoMigrate(&models.DbDownloadTask{}); err != nil {
					t.Fatal(err)
				}
				oldDownload, oldUpload := models.GlobalDownloadQueue, models.GlobalUploadQueue
				models.GlobalDownloadQueue, models.GlobalUploadQueue = models.NewDq(0), models.NewUq(1)
				models.GlobalDownloadQueue.Start()
				models.GlobalUploadQueue.Start()
				defer func() {
					models.GlobalDownloadQueue.Stop()
					models.GlobalUploadQueue.Stop()
					time.Sleep(100 * time.Millisecond)
					synctest.Wait()
					models.GlobalDownloadQueue, models.GlobalUploadQueue = oldDownload, oldUpload
				}()
				s.IsFile, s.TmpSyncPath = true, true
				s.SyncDriver = &fakeDirectoryScanDriver{detailsByID: map[string]*SyncFileCache{
					"": {FileName: "excluded.txt", IsVideo: true},
				}}
				wantErr := errors.New("required state write failed")
				if failing == "cancelled" {
					wantErr = context.Canceled
					s.Cancel()
				} else if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("fail_start_write", func(tx *gorm.DB) {
					row, ok := tx.Statement.Dest.(*models.Sync)
					if !ok {
						return
					}
					if (failing == "start" && row.Status == models.SyncStatusInProgress) ||
						(failing == "complete" && row.Status == models.SyncStatusCompleted) ||
						(failing == "progress" && len(tx.Statement.Selects) > 0 && tx.Statement.Selects[0] == "total") {
						tx.AddError(wantErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
				if err := s.Start(); !errors.Is(err, wantErr) {
					t.Fatalf("Start 返回 %v，期望 %v", err, wantErr)
				}
				var row models.Sync
				if err := db.Db.First(&row, s.Sync.ID).Error; err != nil {
					t.Fatal(err)
				}
				wantStatus := models.SyncStatusFailed
				if failing == "cancelled" {
					wantStatus = models.SyncStatusCancelled
				}
				if row.Status != wantStatus || row.FailReason == "" {
					t.Fatalf("Start 未保存真实失败结果：%+v", row)
				}
				if !models.GlobalDownloadQueue.IsRunning() || !models.GlobalUploadQueue.IsRunning() {
					t.Fatal("失败收尾未恢复传输队列")
				}
			})
		})
	}
}
