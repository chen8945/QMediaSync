package syncstrm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/syncscope"
)

func newScopeTestSync(t *testing.T) *SyncStrm {
	t.Helper()
	s := newBackgroundTestSync(t)
	if err := db.Db.Model(&models.SyncPath{}).Where("id = ?", s.SyncPathId).Updates(map[string]any{
		"remote_path": s.SourcePath, "local_path": s.TargetPath,
	}).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.releaseScope)
	return s
}

func TestStartScopeWaitKeepsTransfersRunningAndCanCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScopeTestSync(t)
		hold, err := syncscope.Acquire(t.Context(), s.manualScope())
		if err != nil {
			t.Fatal(err)
		}
		defer hold()
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
		done := make(chan error, 1)
		go func() { done <- s.Start() }()
		synctest.Wait()
		if !models.GlobalDownloadQueue.IsRunning() || !models.GlobalUploadQueue.IsRunning() {
			t.Fatal("waiting sync stopped transfer queues")
		}
		select {
		case err := <-done:
			t.Fatalf("Start did not wait: %v", err)
		default:
		}
		s.Cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled wait: %v", err)
		}
		if got := readBackgroundTestSync(t, s.Sync.ID); got.Status != models.SyncStatusCancelled {
			t.Fatalf("cancelled wait status: %v", got.Status)
		}
		waitBackgroundTest(t, s.backgroundService)
	})
}

func TestSyncScopeChecksPathAndRefreshesSettingsAfterWait(t *testing.T) {
	for _, change := range []string{"settings", "path", "account", "deleted", "missing_url"} {
		t.Run(change, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newScopeTestSync(t)
				hold, err := syncscope.Acquire(t.Context(), s.manualScope())
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- s.acquireScope() }()
				synctest.Wait()
				updates := map[string]any{"last_sync_at": 1234, "is_full_sync": true, "min_video_size": 5678}
				if change == "path" {
					updates["remote_path"] = "/changed"
				} else if change == "account" {
					updates["account_id"] = s.Account.ID + 1
				} else if change == "missing_url" {
					updates["strm_base_url"] = ""
				}
				if change == "deleted" {
					err = db.Db.Delete(&models.SyncPath{}, s.SyncPathId).Error
				} else {
					err = db.Db.Model(&models.SyncPath{}).Where("id = ?", s.SyncPathId).Updates(updates).Error
				}
				if err != nil {
					t.Fatal(err)
				}
				hold()
				err = <-done
				if change == "settings" {
					if err != nil || s.LastSyncAt != 1234 || !s.FullSync || s.Config.MinVideoSize != 5678 {
						t.Fatalf("waiting sync kept old settings: %v %+v", err, s.Config)
					}
				} else if err == nil {
					t.Fatal("changed or deleted directory accepted")
				}
			})
		})
	}
}

func TestBackgroundScopeBlocksGenerationUntilLedgerResultIsSaved(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScopeTestSync(t)
		if err := s.acquireScope(); err != nil {
			t.Fatal(err)
		}
		if err := s.completeSync(); err != nil {
			t.Fatal(err)
		}
		entered, finish := make(chan struct{}), make(chan struct{})
		if err := db.Db.Callback().Update().Before("gorm:begin_transaction").Register("scope:hold-result", func(tx *gorm.DB) {
			if record, ok := tx.Statement.Dest.(*models.Sync); ok && record.LedgerStatus != nil && *record.LedgerStatus == realtime.SyncLedgerCompleted {
				close(entered)
				<-finish
			}
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Db.Callback().Update().Remove("scope:hold-result") })
		s.startBackground(true)
		<-entered
		s.Cancel()
		var built atomic.Bool
		service := NewStrmGenerationService()
		stopAfterBuild := errors.New("fixture: reached generation")
		service.buildSyncer = func(*models.SyncPath, *models.Account, *SyncStrmConfig) (*SyncStrm, error) {
			built.Store(true)
			return nil, stopAfterBuild
		}
		done := make(chan error, 1)
		go func() {
			_, err := service.Generate(t.Context(), StrmGenerationInput{Task: &models.StrmGenerationTask{
				SyncPathId: s.SyncPathId, AccountId: s.Account.ID,
			}})
			done <- err
		}()
		synctest.Wait()
		if built.Load() {
			t.Fatal("generation passed unfinished ledger result")
		}
		close(finish)
		if err := <-done; !errors.Is(err, stopAfterBuild) {
			t.Fatalf("generation did not resume: %v", err)
		}
		waitBackgroundTest(t, s.backgroundService)
	})
}

func TestBackgroundScopeReleasedAfterFailureCancellationAndRejection(t *testing.T) {
	for _, mode := range []string{"failed", "result_failed", "cancelled", "rejected", "not_required"} {
		t.Run(mode, func(t *testing.T) {
			s := newScopeTestSync(t)
			if err := s.acquireScope(); err != nil {
				t.Fatal(err)
			}
			if mode == "not_required" {
				s.TmpSyncPath = true
			}
			if err := s.completeSync(); err != nil {
				t.Fatal(err)
			}
			if mode == "rejected" {
				if err := s.backgroundService.stop(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan struct{})
			var once sync.Once
			if err := db.Db.Callback().Query().Before("gorm:query").Register("scope:ledger-error", func(tx *gorm.DB) {
				if tx.Statement.Table == "sync_files" {
					if mode == "failed" {
						tx.AddError(errors.New("fixture ledger failure"))
					} else if mode == "cancelled" {
						once.Do(func() { close(entered) })
						<-tx.Statement.Context.Done()
						tx.AddError(tx.Statement.Context.Err())
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Db.Callback().Query().Remove("scope:ledger-error") })
			if mode == "result_failed" {
				if err := db.Db.Callback().Update().Before("gorm:update").Register("scope:result-error", func(tx *gorm.DB) {
					if record, ok := tx.Statement.Dest.(*models.Sync); ok && record.LedgerStatus != nil && *record.LedgerStatus == realtime.SyncLedgerCompleted {
						tx.AddError(errors.New("fixture result failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Db.Callback().Update().Remove("scope:result-error") })
			}
			s.startBackground(mode != "not_required")
			if mode == "cancelled" {
				<-entered
				s.backgroundService.cancel()
			}
			waitBackgroundTest(t, s.backgroundService)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			release, err := syncscope.Acquire(ctx, s.manualScope())
			if err != nil {
				t.Fatalf("scope not released after %s: %v", mode, err)
			}
			release()
		})
	}
}

func TestManualFileScopeUsesActualOutputDirectory(t *testing.T) {
	for _, source := range []models.SourceType{models.SourceType115, models.SourceTypeLocal} {
		t.Run(string(source), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				target := filepath.Join(t.TempDir(), "strm")
				s := &SyncStrm{Context: t.Context(), TmpSyncPath: true, IsFile: true,
					SourcePath: "/media/movie.mkv", TargetPath: target, Account: &models.Account{SourceType: source, BaseModel: models.BaseModel{ID: 1}},
				}
				file := &SyncFileCache{SourceType: source, Path: "/media", ParentId: "/media", FileName: "movie.mkv", IsVideo: true}
				actual := filepath.Dir(file.GetLocalFilePath(target, s.SourcePath))
				hold, err := syncscope.Acquire(t.Context(), syncscope.Scope{SourceType: "other", AccountID: 2, LocalPath: actual})
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- s.acquireScope() }()
				synctest.Wait()
				select {
				case err := <-done:
					t.Fatalf("manual file did not wait for actual output %s: %v", actual, err)
				default:
				}
				hold()
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				s.releaseScope()
			})
		})
	}
}

func TestStartFileRejectsMovedFileBeforeWriting(t *testing.T) {
	s := newScopeTestSync(t)
	s.TmpSyncPath, s.IsFile, s.SourcePath = true, true, "/media/movie.mkv"
	s.SyncDriver = &fakeDirectoryScanDriver{detailsByID: map[string]*SyncFileCache{
		s.SourcePathId: {SourceType: models.SourceType115, Path: "/moved", FileName: "movie.mkv", IsVideo: true},
	}}
	if err := s.acquireScope(); err != nil {
		t.Fatal(err)
	}
	if err := s.StartFile(); err == nil || s.NewStrm != 0 {
		t.Fatalf("moved file accepted: %v, generated=%d", err, s.NewStrm)
	}
}

func TestSyncScopeRejectsSymlinkOutsideRoot(t *testing.T) {
	for _, name := range []string{"movie.strm", "movie.nfo"} {
		t.Run(name, func(t *testing.T) {
			s := newScopeTestSync(t)
			root, outside := s.GetLocalBaseDir(), t.TempDir()
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
				t.Fatal(err)
			}
			oldFile := filepath.Join(outside, name)
			if err := os.WriteFile(oldFile, []byte("保留原文件"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := s.acquireScope(); err != nil {
				t.Fatal(err)
			}
			s.SyncDriver = &fakeDirectoryScanDriver{strmContent: "https://example.com/new"}
			file := &SyncFileCache{FileId: "linked", SourceType: s.Account.SourceType,
				Path: s.SourcePath + "/linked", FileName: name, IsVideo: name == "movie.strm", IsMeta: name == "movie.nfo"}
			if err := s.processNetFile(file); err == nil {
				t.Fatal("指向范围外的文件不应继续生成或处理元数据")
			}
			if data, err := os.ReadFile(oldFile); err != nil || string(data) != "保留原文件" {
				t.Fatalf("范围外旧文件被修改：%q %v", data, err)
			}
		})
	}
}

func TestSyncScopeCleanupKeepsSymlinkOutsideRoot(t *testing.T) {
	s := newScopeTestSync(t)
	root, outside := s.GetLocalBaseDir(), t.TempDir()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	oldFile := filepath.Join(outside, "movie.nfo")
	if err := os.WriteFile(oldFile, []byte("保留原文件"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "movie.nfo")
	if err := os.Symlink(oldFile, link); err != nil {
		t.Fatal(err)
	}
	if err := s.acquireScope(); err != nil {
		t.Fatal(err)
	}
	s.Config.MetaExt = []string{".nfo"}
	s.Config.EnableDownloadMeta = 1
	s.Config.NetNotFoundFileAction = models.SyncTreeItemMetaActionDelete
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("无法确认目标范围时应保留原文件链接：%v", err)
	}
}

func TestAcquiredRootDoesNotFollowRetargetedSymlink(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	s := &SyncStrm{TmpSyncPath: true, TargetPath: link, SourcePath: "/media", Context: t.Context(), Account: &models.Account{SourceType: models.SourceTypeLocal}}
	if err := s.acquireScope(); err != nil {
		t.Fatal(err)
	}
	defer s.releaseScope()
	if err := ensureGeneratedStrmPathWithinResolvedRoot(s.scopeLocalRoot, filepath.Join(link, "video.strm")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	if err := ensureGeneratedStrmPathWithinResolvedRoot(s.scopeLocalRoot, filepath.Join(link, "video.strm")); err == nil {
		t.Fatal("许可仍保护旧根时必须拒绝新真实位置")
	}
	s.releaseScope()
	if err := s.acquireScope(); err != nil {
		t.Fatal(err)
	}
	if err := ensureGeneratedStrmPathWithinResolvedRoot(s.scopeLocalRoot, filepath.Join(link, "video.strm")); err != nil {
		t.Fatalf("重新申请许可必须绑定新根：%v", err)
	}
}
