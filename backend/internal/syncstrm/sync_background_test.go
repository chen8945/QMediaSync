package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/realtime"
	"qmediasync/internal/syncscope"
	"qmediasync/internal/v115open"
)

func newBackgroundTestSync(t *testing.T) *SyncStrm {
	t.Helper()
	account, path := setupStrmExclusionTestDB(t)
	s := newSyncStrm(account, path.ID, "/media", "root", t.TempDir(), SyncStrmConfig{}, false, 0, false, true)
	s.backgroundService = newSyncBackgroundService()
	t.Cleanup(func() {
		s.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.backgroundService.stop(ctx); err != nil {
			t.Error(err)
		}
	})
	file := &SyncFileCache{SourceType: models.SourceType115, FileId: "file", ParentId: "root", Path: "/media", FileName: "movie.mkv", FileType: v115open.TypeFile, IsVideo: true, PickCode: "pick"}
	file.GetLocalFilePath(s.TargetPath, s.SourcePath)
	if err := s.memSyncCache.Insert(file); err != nil {
		t.Fatal(err)
	}
	s.recordFileSuccess(file)
	return s
}

func waitBackgroundTest(t *testing.T, service *syncBackgroundService) {
	t.Helper()
	done := make(chan struct{})
	go func() { service.workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("background did not exit")
	}
}

func readBackgroundTestSync(t *testing.T, id uint) models.Sync {
	t.Helper()
	var record models.Sync
	if err := db.Db.First(&record, id).Error; err != nil {
		t.Fatal(err)
	}
	return record
}

func TestBackgroundLedgerOutlivesForegroundAndPreservesGeneration(t *testing.T) {
	s := newBackgroundTestSync(t)
	s.NewUpload = 1
	if err := s.completeSync(); err != nil {
		t.Fatal(err)
	}
	generation := generationSnapshot(s.Sync)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	if err := db.Db.Callback().Query().Before("gorm:query").Register("background:hold-ledger", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			once.Do(func() { close(entered); <-release })
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove("background:hold-ledger") })
	s.startBackground(true)
	<-entered
	s.Cancel()
	if s.memSyncCache != nil {
		t.Fatal("foreground retained mutable ownership of handed-off cache")
	}
	close(release)
	waitBackgroundTest(t, s.backgroundService)
	saved := readBackgroundTestSync(t, s.Sync.ID)
	if saved.LedgerStatus == nil || *saved.LedgerStatus != realtime.SyncLedgerCompleted || saved.LedgerFinishedAt == nil {
		t.Fatalf("foreground cancellation ended ledger: %+v", saved)
	}
	if saved.FinishAt != generation.FinishAt || saved.Status != generation.Status || saved.NewUpload != generation.NewUpload || saved.ScanResult.SucceededFiles != 1 {
		t.Fatalf("background changed generation: %+v", saved)
	}
	if *s.Sync.LedgerStatus != realtime.SyncLedgerPending {
		t.Fatal("background mutated foreground model used by the source queue")
	}
	var count int64
	if err := db.Db.Model(&models.SyncFile{}).Where("sync_path_id = ?", s.SyncPathId).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("ledger count=%d error=%v", count, err)
	}
}

func TestBackgroundLedgerPreservesOldLocalDirectorySubtree(t *testing.T) {
	s := newScanResultTestSync(t)
	s.Account.SourceType = models.SourceTypeBaiduPan
	s.SourcePath = "/media"
	oldDir := filepath.Join(s.TargetPath, "media", "previous-target")
	protected := filepath.Join(oldDir, "movie.strm")
	independent := filepath.Join(s.TargetPath, "media", "sibling", "stale.strm")
	// 历史文件目标仍在旧目录内，即使它的远端记录已位于其他路径也须保留。
	rows := []models.SyncFile{
		baiduDirectoryFailureRow(s, "/media/old", "old-dir", oldDir, true),
		baiduDirectoryFailureRow(s, "/media/elsewhere/movie.mkv", "movie", protected, false),
		baiduDirectoryFailureRow(s, "/media/sibling/stale.mkv", "stale", independent, false),
	}
	if err := db.Db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	writeScopeFile(t, protected)
	writeScopeFile(t, independent)
	s.recordScanFailure("/media/old", errors.New("directory listing failed"))
	if err := s.prepareCleanupProtection(); err != nil {
		t.Fatal(err)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	requireScopeFile(t, protected, true)
	requireScopeFile(t, independent, false)
	view := s.backgroundLedgerView(s.Sync)
	view.Context = t.Context()
	if err := view.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var remaining []models.SyncFile
	if err := db.Db.Order("id ASC").Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 || remaining[0].ID != rows[0].ID || remaining[1].ID != rows[1].ID {
		t.Fatalf("background lost protected local subtree: %+v", remaining)
	}
}

func TestStartRestoresTransfersBeforeBackgroundLedgerExits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newBackgroundTestSync(t)
		s.Account = &models.Account{SourceType: models.SourceTypeLocal}
		if err := db.Db.Model(&models.SyncPath{}).Where("id = ?", s.SyncPathId).Updates(map[string]any{
			"source_type": models.SourceTypeLocal, "account_id": 0, "remote_path": s.SourcePath, "local_path": s.TargetPath,
		}).Error; err != nil {
			t.Fatal(err)
		}
		s.SyncDriver = &fakeDirectoryScanDriver{}
		s.memSyncCache = NewMemorySyncCache(s.SyncPathId)
		s.scanFiles = nil
		if err := db.Db.AutoMigrate(&models.DbDownloadTask{}); err != nil {
			t.Fatal(err)
		}
		oldDownload, oldUpload := models.GlobalDownloadQueue, models.GlobalUploadQueue
		models.GlobalDownloadQueue, models.GlobalUploadQueue = models.NewDq(0), models.NewUq(1)
		defer func() {
			models.GlobalDownloadQueue.Stop()
			models.GlobalUploadQueue.Stop()
			time.Sleep(100 * time.Millisecond)
			synctest.Wait()
			models.GlobalDownloadQueue, models.GlobalUploadQueue = oldDownload, oldUpload
		}()
		entered, release := make(chan struct{}), make(chan struct{})
		var ledgerQueryOnce, releaseOnce sync.Once
		releaseLedger := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseLedger()
		if err := db.Db.Callback().Query().Before("gorm:query").Register("background:hold-start", func(tx *gorm.DB) {
			if tx.Statement.Table == "sync_files" && tx.Statement.Context == s.backgroundService.ctx {
				ledgerQueryOnce.Do(func() { close(entered); <-release })
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Db.Callback().Query().Remove("background:hold-start") })
		if err := s.Start(); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("background ledger did not begin reading")
		}
		if s.Context.Err() == nil || !models.GlobalDownloadQueue.IsRunning() || !models.GlobalUploadQueue.IsRunning() {
			t.Fatal("foreground did not release its context and restore transfer queues")
		}
		if *readBackgroundTestSync(t, s.Sync.ID).LedgerStatus != realtime.SyncLedgerRunning {
			t.Fatal("Start waited for or cancelled background ledger")
		}
		second := newSyncStrm(s.Account, s.SyncPathId, s.SourcePath, s.SourcePathId, s.TargetPath, s.Config, false, 0, false, true)
		second.backgroundService = s.backgroundService
		second.SyncDriver = &fakeDirectoryScanDriver{}
		secondDone := make(chan error, 1)
		go func() { secondDone <- second.Start() }()
		synctest.Wait()
		select {
		case err := <-secondDone:
			t.Fatalf("same directory did not wait for previous background: %v", err)
		default:
		}
		if !models.GlobalDownloadQueue.IsRunning() || !models.GlobalUploadQueue.IsRunning() {
			t.Fatal("next sync stopped transfers while waiting for background")
		}
		second.Cancel()
		select {
		case err := <-secondDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("next sync did not cancel its wait: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("next sync did not exit after cancellation")
		}
		releaseLedger()
		waitBackgroundTest(t, s.backgroundService)
		if *readBackgroundTestSync(t, s.Sync.ID).LedgerStatus != realtime.SyncLedgerCompleted {
			t.Fatal("background did not finish after foreground returned")
		}
	})
}

func TestBackgroundLedgerFailureAndResultPersistenceFailure(t *testing.T) {
	for _, mode := range []string{"ledger", "partial", "result"} {
		t.Run(mode, func(t *testing.T) {
			s := newBackgroundTestSync(t)
			if err := s.completeSync(); err != nil {
				t.Fatal(err)
			}
			generation := generationSnapshot(s.Sync)
			cause := errors.New("fixture database unavailable")
			if mode != "result" {
				if mode == "partial" {
					for i := range 256 {
						file := &SyncFileCache{SourceType: models.SourceType115, FileId: fmt.Sprint(i), ParentId: "root", Path: "/media", FileName: fmt.Sprintf("extra-%d.mkv", i), FileType: v115open.TypeFile, IsVideo: true}
						file.GetLocalFilePath(s.TargetPath, s.SourcePath)
						if err := s.memSyncCache.Insert(file); err != nil {
							t.Fatal(err)
						}
					}
				}
				creates := 0
				if err := db.Db.Callback().Create().Before("gorm:create").Register("background:fail-ledger", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						creates++
						if mode != "partial" || creates == 9 {
							tx.AddError(cause)
						}
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Create().Remove("background:fail-ledger") })
			} else {
				if err := db.Db.Callback().Update().Before("gorm:update").Register("background:fail-result", func(tx *gorm.DB) {
					if record, ok := tx.Statement.Dest.(*models.Sync); ok && record.LedgerStatus != nil && *record.LedgerStatus == realtime.SyncLedgerCompleted {
						tx.AddError(cause)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove("background:fail-result") })
			}
			s.startBackground(true)
			waitBackgroundTest(t, s.backgroundService)
			saved := readBackgroundTestSync(t, s.Sync.ID)
			want := realtime.SyncLedgerFailed
			if mode == "result" {
				want = realtime.SyncLedgerRunning
			}
			if saved.LedgerStatus == nil || *saved.LedgerStatus != want || (saved.LedgerFinishedAt != nil) != (mode != "result") {
				t.Fatalf("result=%+v", saved)
			}
			if saved.FinishAt != generation.FinishAt || saved.Status != generation.Status {
				t.Fatal("background failure changed generation")
			}
			if mode == "partial" {
				var count int64
				if err := db.Db.Model(&models.SyncFile{}).Where("sync_path_id = ?", s.SyncPathId).Count(&count).Error; err != nil || count != 256 {
					t.Fatalf("earlier committed ledger work lost: count=%d error=%v", count, err)
				}
			}
			content, err := os.ReadFile(models.SyncLogFullPath(s.Sync.ID))
			if err != nil || !strings.Contains(string(content), cause.Error()) {
				t.Fatalf("background error log missing: %s %v", content, err)
			}
		})
	}
}

func TestBackgroundServiceCancellationAndRejectionSetInterrupted(t *testing.T) {
	for _, mode := range []string{"running", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			s := newBackgroundTestSync(t)
			if err := s.completeSync(); err != nil {
				t.Fatal(err)
			}
			if mode == "running" {
				entered := make(chan struct{})
				if err := db.Db.Callback().Query().Before("gorm:query").Register("background:cancel-ledger", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						close(entered)
						<-tx.Statement.Context.Done()
						tx.AddError(tx.Statement.Context.Err())
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Query().Remove("background:cancel-ledger") })
				s.startBackground(true)
				<-entered
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if err := s.backgroundService.stop(ctx); err != nil {
				t.Fatal(err)
			}
			if mode == "rejected" {
				s.startBackground(true)
			}
			saved := readBackgroundTestSync(t, s.Sync.ID)
			if saved.LedgerStatus == nil || *saved.LedgerStatus != realtime.SyncLedgerInterrupted || saved.LedgerFinishedAt == nil || saved.LedgerError == "" {
				t.Fatalf("shutdown left pending or unknown observed exit: %+v", saved)
			}
		})
	}
}

func TestBackgroundServiceStopHasBoundedWait(t *testing.T) {
	service := newSyncBackgroundService()
	entered, release := make(chan struct{}), make(chan struct{})
	if !service.submit(func(context.Context) { close(entered); <-release }) {
		t.Fatal("service rejected work before stop")
	}
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("unbounded shutdown result: %v", err)
	}
	if service.submit(func(context.Context) { t.Error("work accepted after shutdown") }) {
		t.Fatal("stopping service accepted new work")
	}
	close(release)
	waitBackgroundTest(t, service)
}

func TestBackgroundLoggerWaitsForExistingDownstreamCallback(t *testing.T) {
	s := newBackgroundTestSync(t)
	oldEmby := models.GlobalEmbyConfig
	models.GlobalEmbyConfig = nil
	t.Cleanup(func() { models.GlobalEmbyConfig = oldEmby })
	s.NewStrm = 1
	if err := s.completeSync(); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	if err := db.Db.Callback().Query().Before("gorm:query").Register("background:hold-downstream", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_paths" {
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Query().Remove("background:hold-downstream") })
	s.startBackground(true)
	<-entered
	deadline := time.Now().Add(5 * time.Second)
	for *readBackgroundTestSync(t, s.Sync.ID).LedgerStatus != realtime.SyncLedgerCompleted {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("downstream callback blocked ledger")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	waitBackgroundTest(t, s.backgroundService)
	content, err := os.ReadFile(models.SyncLogFullPath(s.Sync.ID))
	if err != nil || !strings.Contains(string(content), "关联的刮削目录为空") {
		t.Fatalf("logger closed before downstream callback finished: %s %v", content, err)
	}
}

func TestBackgroundDeletedRecordAndLogAreNotRecreated(t *testing.T) {
	for _, beforeStart := range []bool{true, false} {
		t.Run(fmt.Sprintf("before_start=%t", beforeStart), func(t *testing.T) {
			s := newBackgroundTestSync(t)
			if err := s.completeSync(); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			if !beforeStart {
				var once sync.Once
				if err := db.Db.Callback().Query().Before("gorm:query").Register("background:delete-record", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						once.Do(func() { close(entered); <-release })
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Query().Remove("background:delete-record") })
				s.startBackground(true)
				<-entered
			}
			if err := models.DeleteSyncRecordById(s.Sync.ID); err != nil {
				t.Fatal(err)
			}
			events, _, _, _, unsubscribe := realtime.GlobalSyncTaskHub.SubscribeFrom(s.Sync.ID, "", 8)
			defer unsubscribe()
			if beforeStart {
				s.startBackground(true)
			} else {
				close(release)
			}
			waitBackgroundTest(t, s.backgroundService)
			var row models.Sync
			if err := db.Db.First(&row, s.Sync.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("deleted task recreated: %v", err)
			}
			if _, err := os.Stat(models.SyncLogFullPath(s.Sync.ID)); !os.IsNotExist(err) {
				t.Fatalf("deleted task log recreated: %v", err)
			}
			var count int64
			if err := db.Db.Model(&models.SyncFile{}).Where("sync_path_id = ?", s.SyncPathId).Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("deleted history lost ledger: count=%d error=%v", count, err)
			}
			select {
			case event := <-events:
				t.Fatalf("deleted history published an update: %+v", event)
			default:
			}
		})
	}
}

func TestBackgroundSlowNotificationDoesNotDelayLedger(t *testing.T) {
	for _, failNotification := range []bool{false, true} {
		t.Run(fmt.Sprint(failNotification), func(t *testing.T) {
			s := newScopeTestSync(t)
			if err := s.acquireScope(); err != nil {
				t.Fatal(err)
			}
			s.NewUpload = 1
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				close(entered)
				<-release
				if failNotification {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(server.Close)
			if err := db.Db.AutoMigrate(&notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.CustomWebhookChannelConfig{}); err != nil {
				t.Fatal(err)
			}
			channel := notification.NotificationChannel{ChannelType: "webhook", ChannelName: "fixture", IsEnabled: true}
			if err := db.Db.Create(&channel).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Create(&notification.CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: server.URL, Method: "POST", Template: `{"title":"{{title}}"}`, Format: "json"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Create(&notification.NotificationRule{ChannelID: channel.ID, EventType: string(notification.SyncFinished), IsEnabled: true}).Error; err != nil {
				t.Fatal(err)
			}
			oldManager := notificationmanager.GlobalEnhancedNotificationManager
			manager := notificationmanager.NewEnhancedNotificationManager(db.Db, func() string { return "" })
			notificationmanager.GlobalEnhancedNotificationManager = manager
			t.Cleanup(func() { notificationmanager.GlobalEnhancedNotificationManager = oldManager })
			if err := manager.LoadChannels(); err != nil {
				t.Fatal(err)
			}
			if err := s.completeSync(); err != nil {
				t.Fatal(err)
			}
			wantLedger := realtime.SyncLedgerCompleted
			if failNotification {
				wantLedger = realtime.SyncLedgerFailed
				if err := db.Db.Callback().Create().Before("gorm:create").Register("background:fast-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_files" {
						tx.AddError(errors.New("fixture immediate ledger failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Create().Remove("background:fast-failure") })
			}
			s.startBackground(true)
			<-entered
			deadline := time.Now().Add(5 * time.Second)
			for {
				saved := readBackgroundTestSync(t, s.Sync.ID)
				if saved.LedgerStatus != nil && *saved.LedgerStatus == wantLedger {
					break
				}
				if time.Now().After(deadline) {
					close(release)
					t.Fatal("slow notification blocked ledger")
				}
				time.Sleep(time.Millisecond)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			unlock, err := syncscope.Acquire(ctx, s.manualScope())
			cancel()
			close(release)
			if err != nil {
				t.Fatalf("slow notification kept directory busy: %v", err)
			}
			unlock()
			waitBackgroundTest(t, s.backgroundService)
			if calls.Load() != 1 {
				t.Fatalf("notification calls=%d", calls.Load())
			}
		})
	}
}

func TestBackgroundLedgerDoesNotIgnoreStartFailure(t *testing.T) {
	for _, mode := range []string{"database", "conflict", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s := newBackgroundTestSync(t)
			if err := s.completeSync(); err != nil {
				t.Fatal(err)
			}
			wantStatus := realtime.SyncLedgerFailed
			switch mode {
			case "database":
				if err := db.Db.Callback().Update().Before("gorm:update").Register("background:fail-start", func(tx *gorm.DB) {
					if record, ok := tx.Statement.Dest.(*models.Sync); ok && record.LedgerStatus != nil && *record.LedgerStatus == realtime.SyncLedgerRunning {
						tx.AddError(errors.New("fixture start write failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove("background:fail-start") })
			case "conflict":
				wantStatus = realtime.SyncLedgerCompleted
				if err := db.Db.Model(&models.Sync{}).Where("id = ?", s.Sync.ID).Update("ledger_status", wantStatus).Error; err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				wantStatus = realtime.SyncLedgerInterrupted
				s.Cancel()
			}
			s.runBackgroundLedger()
			var count int64
			if err := db.Db.Model(&models.SyncFile{}).Where("sync_path_id = ?", s.SyncPathId).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("failed start continued ledger: count=%d error=%v", count, err)
			}
			saved := readBackgroundTestSync(t, s.Sync.ID)
			if saved.LedgerStatus == nil || *saved.LedgerStatus != wantStatus {
				t.Fatalf("unexpected failure status: %+v", saved)
			}
		})
	}
}
