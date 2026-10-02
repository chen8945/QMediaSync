package syncstrm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
	"qmediasync/internal/v115open"

	"gorm.io/gorm"
)

func newScanResultTestSync(t *testing.T) *SyncStrm {
	t.Helper()
	setupSyncStateTestDB(t)
	return &SyncStrm{Context: t.Context(), Account: &models.Account{SourceType: models.SourceTypeLocal}, SourcePath: "/source", TargetPath: t.TempDir(), SyncPathId: 7, Sync: &models.Sync{Logger: helpers.AppLogger}, memSyncCache: NewMemorySyncCache(7), Config: SyncStrmConfig{DelEmptyLocalDir: true}}
}
func writeScopeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
}
func requireScopeFile(t *testing.T, path string, want bool) {
	t.Helper()
	_, err := os.Stat(path)
	if want && err != nil {
		t.Fatalf("受保护文件丢失 %s: %v", path, err)
	}
	if !want && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("完整范围未清理 %s: %v", path, err)
	}
}

func TestScanResultIncompleteSiblingCleanup(t *testing.T) {
	s := newScanResultTestSync(t)
	bad := filepath.Join(s.TargetPath, "bad", "keep.strm")
	good := filepath.Join(s.TargetPath, "good", "stale.strm")
	writeScopeFile(t, bad)
	writeScopeFile(t, good)
	rows := []models.SyncFile{{SyncPathId: 7, FileId: "bad", LocalFilePath: bad}, {SyncPathId: 7, FileId: "good", LocalFilePath: good}}
	if err := db.Db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	s.recordScanFailure("/source/bad", errors.New("目录读取失败"))
	if !s.cleanupProtected(s.TargetPath, true) {
		t.Fatal("祖先目录没有保护")
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	requireScopeFile(t, bad, true)
	requireScopeFile(t, good, false)
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var remaining []models.SyncFile
	if err := db.Db.Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].FileId != "bad" {
		t.Fatalf("错误的剩余账本: %+v", remaining)
	}
	status, err := s.scanOutcome()
	if status != models.SyncStatusIncomplete || err == nil || s.scanResultSnapshot().CleanupStatus != "partial" {
		t.Fatalf("结果未标不完整: status=%d err=%v result=%+v", status, err, s.scanResultSnapshot())
	}
}

func TestScanResultFailedMoveProtectsIdentityAndTargetOwner(t *testing.T) {
	s := newScanResultTestSync(t)
	s.Account.SourceType = models.SourceTypeBaiduPan
	s.SourcePath = "/media"
	oldPath := filepath.Join(s.TargetPath, "media", "old", "movie.strm")
	newPath := filepath.Join(s.TargetPath, "media", "new", "movie.strm")
	good := filepath.Join(s.TargetPath, "media", "good", "stale.strm")
	for _, path := range []string{oldPath, newPath, good} {
		writeScopeFile(t, path)
	}
	rows := []models.SyncFile{{SyncPathId: 7, FileId: "/media/old/movie.mkv", PickCode: "stable-fs-id", LocalFilePath: oldPath}, {SyncPathId: 7, FileId: "owner", LocalFilePath: newPath}, {SyncPathId: 7, FileId: "stale", LocalFilePath: good}}
	if err := db.Db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	failed := &SyncFileCache{SourceType: models.SourceTypeBaiduPan, FileType: v115open.TypeFile, FileId: "/media/new/movie.mkv", PickCode: "stable-fs-id", Path: "/media/new", FileName: "movie.mkv", IsVideo: true, LocalFilePath: newPath}
	if err := s.memSyncCache.Insert(failed); err != nil {
		t.Fatal(err)
	}
	s.recordFileSuccess(failed)
	s.recordFileFailure(failed, errors.New("写入失败"))
	if err := s.prepareCleanupProtection(); err != nil {
		t.Fatal(err)
	}
	if err := s.compareLocalFilesWithTempTable(); err != nil {
		t.Fatal(err)
	}
	requireScopeFile(t, oldPath, true)
	requireScopeFile(t, newPath, true)
	requireScopeFile(t, good, false)
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var remaining []models.SyncFile
	if err := db.Db.Order("id").Find(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 2 || remaining[0].FileId != rows[0].FileId || remaining[1].FileId != "owner" {
		t.Fatalf("失败移动/owner 记录被覆盖: %+v", remaining)
	}
	result := s.scanResultSnapshot()
	if result.FailedFiles != 1 || result.SucceededFiles != 0 {
		t.Fatalf("失败覆盖计数错误: %+v", result)
	}
	status, err := s.scanOutcome()
	if status != models.SyncStatusPartial || err == nil {
		t.Fatalf("未报告部分完成: %d %v", status, err)
	}
}

func TestScanResultUnknown115PageRetainsLedger(t *testing.T) {
	s := newScanResultTestSync(t)
	s.SourcePath = "/media"
	s.Account.SourceType = models.SourceType115
	old := models.SyncFile{SyncPathId: 7, FileId: "old", LocalFilePath: filepath.Join(s.TargetPath, "media", "old.strm")}
	if err := db.Db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.memSyncCache.Insert(&SyncFileCache{SourceType: models.SourceType115, FileId: "unknown", ParentId: "unknown-parent", FileName: "unknown.mkv", IsVideo: true}); err != nil {
		t.Fatal(err)
	}
	s.recordScanFailure(s.SourcePath, errors.New("缺页，无法定位目录"))
	if err := s.handleTempTableDiff(); err != nil {
		t.Fatal(err)
	}
	var rows []models.SyncFile
	if err := db.Db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != old.ID {
		t.Fatalf("未知文件已进入账本或旧记录丢失: %+v", rows)
	}
}

func TestScanResultIncrementalMissingCleanupAndWalkFailure(t *testing.T) {
	t.Run("incremental", func(t *testing.T) {
		s := newScanResultTestSync(t)
		existing := filepath.Join(s.TargetPath, "movie.strm")
		writeScopeFile(t, existing)
		s.skipMissingCleanup("增量缺项未知")
		if err := s.compareLocalFilesWithTempTable(); err != nil {
			t.Fatal(err)
		}
		requireScopeFile(t, existing, true)
		if status, err := s.scanOutcome(); status != models.SyncStatusCompleted || err != nil {
			t.Fatalf("完整增量误报失败: %d %v", status, err)
		}
		if s.scanResultSnapshot().CleanupStatus != "skipped" {
			t.Fatal("缺少未清理说明")
		}
	})
	t.Run("walk error", func(t *testing.T) {
		s := newScanResultTestSync(t)
		s.TargetPath = filepath.Join(s.TargetPath, "missing")
		if err := s.compareLocalFilesWithTempTable(); err != nil {
			t.Fatal(err)
		}
		if status, err := s.scanOutcome(); status != models.SyncStatusIncomplete || err == nil {
			t.Fatalf("Walk 错误未传播: %d %v", status, err)
		}
	})
	t.Run("cancel", func(t *testing.T) {
		s := newScanResultTestSync(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		s.Context = ctx
		if err := s.compareLocalFilesWithTempTable(); !errors.Is(err, context.Canceled) {
			t.Fatalf("取消未传播: %v", err)
		}
	})
}

func TestScanResultOutcomeAndWatermarkCommitTogether(t *testing.T) {
	for _, mode := range []string{"success", "partial", "cancelled", "watermark failure"} {
		t.Run(mode, func(t *testing.T) {
			s := newScanResultTestSync(t)
			if err := db.Db.AutoMigrate(&models.SyncPath{}); err != nil {
				t.Fatal(err)
			}
			path := models.SyncPath{BaseModel: models.BaseModel{ID: 7}, LastSyncAt: 11, IsFullSync: true}
			if err := db.Db.Create(&path).Error; err != nil {
				t.Fatal(err)
			}
			s.Sync = &models.Sync{Logger: helpers.AppLogger, SyncPathId: 7, Status: models.SyncStatusInProgress}
			if err := db.Db.Create(s.Sync).Error; err != nil {
				t.Fatal(err)
			}
			s.FullSync = true
			events, _, _, _, unsub := realtime.GlobalSyncTaskHub.SubscribeFrom(s.Sync.ID, "", 8)
			defer unsub()
			if mode == "partial" {
				s.recordFileFailure(&SyncFileCache{FileId: "failed", FileName: "failed.mkv"}, errors.New("write failed"))
			}
			wantErr := errors.New("watermark unavailable")
			if mode == "watermark failure" {
				if err := db.Db.Callback().Update().Before("gorm:update").Register("fail_watermark", func(tx *gorm.DB) {
					if tx.Statement.Table == "sync_paths" {
						tx.AddError(wantErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			var err error
			if mode == "cancelled" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				s.Context = ctx
				err = s.failSync(context.Canceled)
			} else {
				err = s.completeSync()
			}
			if mode == "watermark failure" && !errors.Is(err, wantErr) {
				t.Fatalf("水位失败未传播: %v", err)
			}
			if mode != "watermark failure" && mode != "cancelled" && err != nil {
				t.Fatal(err)
			}
			var row models.Sync
			if err := db.Db.First(&row, s.Sync.ID).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.First(&path, 7).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "success" {
				if row.Status != models.SyncStatusCompleted || row.ScanResult == nil || path.LastSyncAt != row.FinishAt || path.IsFullSync {
					t.Fatalf("成功结果未原子提交: %+v %+v", row, path)
				}
			} else if path.LastSyncAt != 11 || !path.IsFullSync {
				t.Fatalf("未成功却推进了水位: %+v", path)
			}
			if mode == "watermark failure" {
				if row.Status != models.SyncStatusInProgress || row.FinishAt != 0 {
					t.Fatalf("事务失败未回滚结果: %+v", row)
				}
				for len(events) > 0 {
					if event := <-events; event.Terminal {
						t.Fatalf("失败事务发布了终态: %+v", event)
					}
				}
			}
			if mode == "partial" && (row.Status != models.SyncStatusPartial || row.ScanResult == nil || row.ScanResult.FailedFiles != 1) {
				t.Fatalf("部分结果丢失: %+v", row)
			}
			if mode == "cancelled" && row.Status != models.SyncStatusCancelled {
				t.Fatalf("取消状态错误: %+v", row)
			}
		})
	}
}

func TestSuccessfulFullSyncUsesLocalDayAndRealSuccess(t *testing.T) {
	setupSyncStateTestDB(t)
	now := time.Date(2026, 10, 2, 0, 1, 0, 0, time.FixedZone("UTC+8", 8*3600))
	for _, record := range []models.Sync{
		{SyncPathId: 7, Status: models.SyncStatusCompleted, IsFullSync: true, FinishAt: now.Add(-2 * time.Minute).Unix()},
		{SyncPathId: 7, Status: models.SyncStatusInProgress, IsFullSync: true, FinishAt: now.Unix()},
		{SyncPathId: 7, Status: models.SyncStatusFailed, IsFullSync: true, FinishAt: now.Unix()},
		{SyncPathId: 7, Status: models.SyncStatusCompleted, IsFullSync: false, FinishAt: now.Unix()},
	} {
		if err := db.Db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
	}
	if got, err := models.GetTodaySuccessfulFullSyncByPathID(t.Context(), 7, 0, now); err != nil || got != nil {
		t.Fatalf("历史/失败/增量误作今日全量: %+v %v", got, err)
	}
	success := models.Sync{SyncPathId: 7, Status: models.SyncStatusCompleted, IsFullSync: true, FinishAt: now.Unix()}
	if err := db.Db.Create(&success).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := models.GetTodaySuccessfulFullSyncByPathID(t.Context(), 7, success.ID, now); err != nil || got != nil {
		t.Fatalf("当前任务没有排除: %+v %v", got, err)
	}
	if got, err := models.GetTodaySuccessfulFullSyncByPathID(t.Context(), 7, 0, now); err != nil || got == nil || got.ID != success.ID {
		t.Fatalf("真实今日成功未命中: %+v %v", got, err)
	}
}

func TestScanResultFailureReasonsRedactCredentials(t *testing.T) {
	s := newScanResultTestSync(t)
	cause := errors.New("GET https://example.test/files?access_token=secret-token failed")
	s.recordScanFailure(s.SourcePath, cause)
	s.recordFileFailure(&SyncFileCache{FileId: "file", FileName: "movie.mkv"}, cause)
	for _, failure := range s.scanResultSnapshot().Failures {
		if strings.Contains(failure.Reason, "secret-token") {
			t.Fatalf("失败明细泄露凭据: %+v", failure)
		}
	}
	s.Sync = &models.Sync{Logger: helpers.AppLogger, Status: models.SyncStatusInProgress}
	if err := db.Db.Create(s.Sync).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.failSync(cause); !errors.Is(err, cause) {
		t.Fatalf("原错误链丢失: %v", err)
	}
	var row models.Sync
	if err := db.Db.First(&row, s.Sync.ID).Error; err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.FailReason, "secret-token") {
		t.Fatalf("任务原因泄露凭据: %+v", row)
	}
}

func TestScanResultRequestTimeoutIsNotTaskCancellation(t *testing.T) {
	for _, taskCancelled := range []bool{false, true} {
		t.Run(strconv.FormatBool(taskCancelled), func(t *testing.T) {
			s := newProgressTestSync(t)
			cause := fmt.Errorf("request timed out: %w", context.DeadlineExceeded)
			if isFatalSyncError(cause) {
				t.Fatal("单次请求超时不应作为共享致命故障")
			}
			if !isFatalSyncError(fatalSyncError(cause)) {
				t.Fatal("必要 SQL 超时仍须终止")
			}
			if taskCancelled {
				s.Cancel()
			}
			if err := s.failSync(cause); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("超时错误链丢失: %v", err)
			}
			want := models.SyncStatusFailed
			if taskCancelled {
				want = models.SyncStatusCancelled
			}
			var row models.Sync
			if err := db.Db.First(&row, s.Sync.ID).Error; err != nil {
				t.Fatal(err)
			}
			if row.Status != want {
				t.Fatalf("任务取消=%v，状态=%d，期望%d", taskCancelled, row.Status, want)
			}
		})
	}
}

func TestScanResultDirectoryTimeoutKeepsIndependentSibling(t *testing.T) {
	s := newFailureScanSyncer(t)
	models.SettingsGlobal.OpenlistRetry = 0
	s.SyncDriver = &failureScanDriver{list: func(_ context.Context, path, _ string) ([]*SyncFileCache, error) {
		switch path {
		case "/media":
			return []*SyncFileCache{scanFailureTestFile("slow", "/media", "slow", true), scanFailureTestFile("good", "/media", "good", true)}, nil
		case "/media/slow":
			return nil, &url.Error{Op: "GET", URL: "https://example.test/files", Err: context.DeadlineExceeded}
		case "/media/good":
			return []*SyncFileCache{scanFailureTestFile("movie", "/media/good", "movie.mkv", false)}, nil
		default:
			return nil, fmt.Errorf("unexpected path %s", path)
		}
	}}
	s.StartOther()
	if len(s.PathErrChan) != 0 || s.Context.Err() != nil || s.NewStrm != 1 {
		t.Fatalf("单次超时阻断了兄弟目录: fatal=%d ctx=%v generated=%d", len(s.PathErrChan), s.Context.Err(), s.NewStrm)
	}
	if err := s.completeSync(); err != nil {
		t.Fatal(err)
	}
	if s.Sync.Status != models.SyncStatusIncomplete {
		t.Fatalf("超时被当作取消/成功: %d", s.Sync.Status)
	}
	failures := s.Sync.ScanResult.Failures
	if len(failures) != 1 || failures[0].Path != "/media/slow" {
		t.Fatalf("超时保护范围错误: %+v", failures)
	}
}
