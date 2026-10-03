package syncstrm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/notification"
	"qmediasync/internal/notificationmanager"
	"qmediasync/internal/realtime"
	"qmediasync/internal/syncscope"
	"qmediasync/internal/v115open"
)

// 使用与应用相同的 SQLite 设置，取消事务后仍能重新打开已提交的数据。
func setupLedgerRecoveryDB(t *testing.T) (*models.Account, *models.SyncPath, string) {
	t.Helper()
	account, syncPath := setupStrmExclusionTestDB(t)
	filename := filepath.Join(t.TempDir(), "ledger.db")
	if err := db.Db.Exec("VACUUM INTO ?", filename).Error; err != nil {
		t.Fatal(err)
	}
	useLedgerRecoveryDB(t, filename)
	account.SourceType = models.SourceTypeLocal
	syncPath.SourceType = models.SourceTypeLocal
	syncPath.RemotePath, syncPath.BaseCid = t.TempDir(), ""
	if err := db.Db.Save(account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Save(syncPath).Error; err != nil {
		t.Fatal(err)
	}
	return account, syncPath, filename
}

func useLedgerRecoveryDB(t *testing.T, filename string) {
	t.Helper()
	db.Db = db.InitSqlite3(filename)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
}

func setupLedgerRecoveryQueues(t *testing.T) {
	t.Helper()
	oldDownload, oldUpload := models.GlobalDownloadQueue, models.GlobalUploadQueue
	models.GlobalDownloadQueue, models.GlobalUploadQueue = models.NewDq(0), models.NewUq(1)
	t.Cleanup(func() {
		models.GlobalDownloadQueue.Stop()
		models.GlobalUploadQueue.Stop()
		// 下载调度器最多等一个 100 ms tick；它没有可等待的退出接口。
		time.Sleep(110 * time.Millisecond)
		models.GlobalDownloadQueue, models.GlobalUploadQueue = oldDownload, oldUpload
	})
}

func newLedgerRecoverySync(t *testing.T, account *models.Account, syncPath *models.SyncPath) *SyncStrm {
	t.Helper()
	s := newSyncStrmFromSyncPath(syncPath, account, true)
	if s == nil {
		t.Fatal("创建测试同步任务失败")
	}
	s.backgroundService = newSyncBackgroundService()
	t.Cleanup(func() {
		s.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.backgroundService.stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

func writeLedgerRecoveryFiles(t *testing.T, source string, count int) {
	t.Helper()
	for i := range count {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("movie-%04d.mkv", i)), []byte("video"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func ledgerRecoveryRows(t *testing.T, syncPathID uint) []models.SyncFile {
	t.Helper()
	var rows []models.SyncFile
	if err := db.Db.Where("sync_path_id = ?", syncPathID).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func assertLedgerRecoveryGeneration(t *testing.T, original, saved models.Sync) {
	t.Helper()
	if saved.Status != original.Status || saved.FinishAt != original.FinishAt || saved.CreatedAt != original.CreatedAt ||
		saved.Total != original.Total || saved.NewStrm != original.NewStrm || saved.NewMeta != original.NewMeta ||
		saved.NewUpload != original.NewUpload || saved.FailReason != original.FailReason ||
		!reflect.DeepEqual(saved.ScanResult, original.ScanResult) {
		t.Fatalf("后台改写了生成结果：before=%+v after=%+v", original, saved)
	}
	var syncPath models.SyncPath
	if err := db.Db.First(&syncPath, saved.SyncPathId).Error; err != nil {
		t.Fatal(err)
	}
	if syncPath.LastSyncAt != original.FinishAt || syncPath.IsFullSync {
		t.Fatalf("后台失败不应回退成功时间或强制全量：%+v", syncPath)
	}
}

func ledgerRecoveryNotifications(t *testing.T) (*atomic.Int32, <-chan struct{}) {
	t.Helper()
	var calls atomic.Int32
	notified := make(chan struct{}, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		notified <- struct{}{}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	if err := db.Db.AutoMigrate(&notification.NotificationChannel{}, &notification.NotificationRule{}, &notification.CustomWebhookChannelConfig{}); err != nil {
		t.Fatal(err)
	}
	channel := notification.NotificationChannel{ChannelType: "webhook", ChannelName: "ledger recovery", IsEnabled: true}
	if err := db.Db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&notification.CustomWebhookChannelConfig{ChannelID: channel.ID, Endpoint: server.URL,
		Method: "POST", Template: `{"title":"{{title}}"}`, Format: "json"}).Error; err != nil {
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
	return &calls, notified
}

func TestLedgerRecoverySubBatchFailure(t *testing.T) {
	for _, mode := range []string{"error", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			account, syncPath, _ := setupLedgerRecoveryDB(t)
			setupLedgerRecoveryQueues(t)
			writeLedgerRecoveryFiles(t, syncPath.RemotePath, 600)
			notificationCalls, notified := ledgerRecoveryNotifications(t)
			s := newLedgerRecoverySync(t, account, syncPath)
			entered, release := make(chan struct{}), make(chan struct{})
			ledgerReady, allowLedger := make(chan struct{}), make(chan struct{})
			var releaseOnce, allowOnce, readyOnce sync.Once
			resume := func() { releaseOnce.Do(func() { close(release) }) }
			resumeLedger := func() { allowOnce.Do(func() { close(allowLedger) }) }
			const queryCallback = "ledger-recovery:wait-for-notification"
			if err := db.Db.Callback().Query().Before("gorm:query").Register(queryCallback, func(tx *gorm.DB) {
				if tx.Statement.Table == "sync_files" && tx.Statement.Context == s.backgroundService.ctx {
					readyOnce.Do(func() { close(ledgerReady) })
					<-allowLedger
				}
			}); err != nil {
				t.Fatal(err)
			}
			var inserts atomic.Int32
			const callback = "ledger-recovery:fail-second-page"
			cause := errors.New("fixture second-page insert failed")
			if err := db.Db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
				if tx.Statement.Table == "sync_files" && inserts.Add(1) == 10 {
					close(entered)
					<-release
					if mode == "error" {
						tx.AddError(cause)
					} else {
						s.backgroundService.cancel()
						tx.AddError(context.Canceled)
					}
				}
			}); err != nil {
				t.Fatal(err)
			}
			startDone := make(chan struct{})
			var startErr error
			t.Cleanup(func() {
				s.Cancel()
				s.backgroundService.cancel()
				resumeLedger()
				resume()
				// 失败断言也先等任务退出，之后才恢复全局队列和数据库。
				<-startDone
				s.backgroundService.workers.Wait()
			})
			go func() { startErr = s.Start(); close(startDone) }()
			select {
			case <-ledgerReady:
			case <-time.After(60 * time.Second):
				t.Fatal("扫描未在测试期限内完成并交给后台")
			}
			// 通知等待从生成结束开始计时，不把 race 下的扫描耗时算进来。
			select {
			case <-notified:
			case <-time.After(10 * time.Second):
				t.Fatal("生成通知等待了尚未开始写入的后台账本")
			}
			resumeLedger()
			select {
			case <-entered:
			case <-time.After(10 * time.Second):
				resume()
				t.Fatal("后台没有执行第二页的第二个 SQL 子批")
			}
			generation := generationSnapshot(s.Sync)
			if generation.Status != models.SyncStatusCompleted || generation.NewStrm != 600 || generation.ScanResult.SucceededFiles != 600 {
				resume()
				t.Fatalf("生成应已完成：%+v", generation)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
			unlock, err := syncscope.Acquire(ctx, syncPath.Scope())
			cancel()
			if err == nil {
				unlock()
				resume()
				t.Fatal("后台仍在写入时提前释放了目录")
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				resume()
				t.Fatal(err)
			}
			ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			independent, err := syncscope.Acquire(ctx, syncscope.Scope{
				SourceType: string(models.SourceTypeLocal), RemotePath: t.TempDir(), LocalPath: t.TempDir(),
			})
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			independent()
			resume()
			<-startDone
			if startErr != nil {
				t.Fatal(startErr)
			}
			waitBackgroundTest(t, s.backgroundService)
			if notificationCalls.Load() != 1 {
				t.Fatalf("后台失败重复发送了生成通知：calls=%d", notificationCalls.Load())
			}
			saved := readBackgroundTestSync(t, s.Sync.ID)
			wantStatus := realtime.SyncLedgerFailed
			if mode == "cancel" {
				wantStatus = realtime.SyncLedgerInterrupted
			}
			if saved.LedgerStatus == nil || *saved.LedgerStatus != wantStatus || saved.LedgerFinishedAt == nil || *saved.LedgerFinishedAt < saved.FinishAt || saved.LedgerError == "" {
				t.Fatalf("后台失败结果不完整：%+v", saved)
			}
			assertLedgerRecoveryGeneration(t, generation, saved)
			committed := ledgerRecoveryRows(t, syncPath.ID)
			if len(committed) != 256 || inserts.Load() != 10 {
				t.Fatalf("第 1 页应保留，第 2 页应整页回滚：rows=%d inserts=%d", len(committed), inserts.Load())
			}
			// 新任务重新扫描文件；不重放失败任务留下的缓存。
			retry := newLedgerRecoverySync(t, account, syncPath)
			if err := retry.Start(); err != nil {
				t.Fatal(err)
			}
			waitBackgroundTest(t, retry.backgroundService)
			rows := ledgerRecoveryRows(t, syncPath.ID)
			if len(rows) != 600 || *readBackgroundTestSync(t, retry.Sync.ID).LedgerStatus != realtime.SyncLedgerCompleted {
				t.Fatalf("重扫未恢复完整账本：rows=%d", len(rows))
			}
			seen := make(map[string]bool)
			for _, row := range rows {
				if seen[row.FileId] {
					t.Fatalf("重扫重复插入 %s", row.FileId)
				}
				seen[row.FileId] = true
				if _, err := os.Stat(row.LocalFilePath); err != nil {
					t.Fatal(err)
				}
			}
			for i, row := range committed {
				if rows[i].ID != row.ID || rows[i].FileId != row.FileId {
					t.Fatal("重扫替换了已经提交的记录身份")
				}
			}
			ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			unlock, err = syncscope.Acquire(ctx, syncPath.Scope())
			cancel()
			if err != nil {
				t.Fatalf("后台退出后目录仍不可用：%v", err)
			}
			unlock()
		})
	}
}

// 只包装测试连接：数据库确实提交后，向调用方返回提交响应丢失。
type ledgerCommitLossPool struct {
	*sql.DB
	commits atomic.Int32
}

func (pool *ledgerCommitLossPool) BeginTx(ctx context.Context, options *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := pool.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return &ledgerCommitLossTx{Tx: tx, pool: pool}, nil
}

type ledgerCommitLossTx struct {
	*sql.Tx
	pool   *ledgerCommitLossPool
	ledger bool
}

func (tx *ledgerCommitLossTx) Commit() error {
	if err := tx.Tx.Commit(); err != nil {
		return err
	}
	if tx.ledger && tx.pool.commits.Add(1) == 1 {
		return errors.New("fixture commit response lost after database committed")
	}
	return nil
}

func TestLedgerRecoveryCommitResponseLost(t *testing.T) {
	account, syncPath, _ := setupLedgerRecoveryDB(t)
	setupLedgerRecoveryQueues(t)
	writeLedgerRecoveryFiles(t, syncPath.RemotePath, 600)
	sqlDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool := &ledgerCommitLossPool{DB: sqlDB}
	db.Db.Config.ConnPool, db.Db.Statement.ConnPool = pool, pool
	const callback = "ledger-recovery:mark-ledger-transaction"
	if err := db.Db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			if transaction, ok := tx.Statement.ConnPool.(*ledgerCommitLossTx); ok {
				transaction.ledger = true
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	s := newLedgerRecoverySync(t, account, syncPath)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, s.backgroundService)
	saved := readBackgroundTestSync(t, s.Sync.ID)
	assertLedgerRecoveryGeneration(t, generationSnapshot(s.Sync), saved)
	if *saved.LedgerStatus != realtime.SyncLedgerFailed || !strings.Contains(saved.LedgerError, "commit response lost") || len(ledgerRecoveryRows(t, syncPath.ID)) != 256 {
		t.Fatalf("未知提交结果应报告失败，并保留真实已提交数据：%+v", saved)
	}
	retry := newLedgerRecoverySync(t, account, syncPath)
	if err := retry.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, retry.backgroundService)
	rows := ledgerRecoveryRows(t, syncPath.ID)
	if len(rows) != 600 || *readBackgroundTestSync(t, retry.Sync.ID).LedgerStatus != realtime.SyncLedgerCompleted {
		t.Fatalf("提交响应丢失后的重扫未恢复：rows=%d", len(rows))
	}
	seen := make(map[string]bool)
	for _, row := range rows {
		if seen[row.FileId] {
			t.Fatalf("提交响应丢失后重复插入：%s", row.FileId)
		}
		seen[row.FileId] = true
	}
}

func TestLedgerRecoveryChangedPageCancellation(t *testing.T) {
	account, syncPath, _ := setupLedgerRecoveryDB(t)
	setupLedgerRecoveryQueues(t)
	writeLedgerRecoveryFiles(t, syncPath.RemotePath, 600)
	first := newLedgerRecoverySync(t, account, syncPath)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, first.backgroundService)
	original := ledgerRecoveryRows(t, syncPath.ID)
	if len(original) != 600 {
		t.Fatalf("初次扫描记录不完整：%d", len(original))
	}
	for _, row := range original {
		mtime := time.Unix(row.MTime+3600, 0)
		if err := os.Chtimes(filepath.Join(syncPath.RemotePath, row.FileName), mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	// 注册测试回调前先等上传调度器退出；GORM 不支持边查询边改回调。
	models.GlobalDownloadQueue.Stop()
	models.GlobalUploadQueue.Stop()
	s := newLedgerRecoverySync(t, account, syncPath)
	const callback = "ledger-recovery:cancel-changed-page"
	updates, cancelAt := 0, 10
	if db.Db.Dialector.Name() == "postgres" {
		cancelAt = 2 // PostgreSQL 每页变化行使用一条 SQL。
	}
	if err := db.Db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			updates++
			if updates == cancelAt {
				s.backgroundService.cancel()
				tx.AddError(context.Canceled)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, s.backgroundService)
	saved := readBackgroundTestSync(t, s.Sync.ID)
	assertLedgerRecoveryGeneration(t, generationSnapshot(s.Sync), saved)
	if *saved.LedgerStatus != realtime.SyncLedgerInterrupted || saved.LedgerFinishedAt == nil || updates != cancelAt {
		t.Fatalf("变化行取消未记录实际退出：updates=%d result=%+v", updates, saved)
	}
	rows := ledgerRecoveryRows(t, syncPath.ID)
	if len(rows) != len(original) {
		t.Fatalf("更新取消改变了记录数量：%d", len(rows))
	}
	for i, row := range rows {
		wantMtime := original[i].MTime
		if i < 256 {
			wantMtime += 3600
		}
		if row.ID != original[i].ID || row.MTime != wantMtime {
			t.Fatalf("变化行取消未保留已提交页或未回滚失败页：index=%d row=%+v want_mtime=%d", i, row, wantMtime)
		}
	}
	retry := newLedgerRecoverySync(t, account, syncPath)
	if err := retry.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, retry.backgroundService)
	rows = ledgerRecoveryRows(t, syncPath.ID)
	if len(rows) != len(original) || *readBackgroundTestSync(t, retry.Sync.ID).LedgerStatus != realtime.SyncLedgerCompleted {
		t.Fatalf("变化行取消后的新扫描未恢复完整账本：rows=%d", len(rows))
	}
	for i, row := range rows {
		if row.ID != original[i].ID || row.MTime != original[i].MTime+3600 {
			t.Fatalf("取消后的新扫描未恢复最新事实：%+v", row)
		}
	}
}

func TestLedgerRecoveryBaiduIncrementalWaitsForScheduledFull(t *testing.T) {
	account, syncPath, _ := setupLedgerRecoveryDB(t)
	setupLedgerRecoveryQueues(t)
	account.SourceType, syncPath.SourceType = models.SourceTypeBaiduPan, models.SourceTypeBaiduPan
	syncPath.RemotePath, syncPath.BaseCid = "/media", "/media"
	if err := db.Db.Save(account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Save(syncPath).Error; err != nil {
		t.Fatal(err)
	}
	remote := make(map[uint64]string)
	for id := uint64(1); id <= 600; id++ {
		remote[id] = fmt.Sprintf("movie-%04d.mkv", id)
	}
	fullCalls, incrementalCalls := 0, 0
	var delta []*baidupan.FileListAllItem
	newSync := func() *SyncStrm {
		s := newLedgerRecoverySync(t, account, syncPath)
		s.SyncDriver = &failureScanDriver{
			list: func(_ context.Context, path, _ string) ([]*SyncFileCache, error) {
				if path != "/media" {
					return nil, fmt.Errorf("unexpected path %s", path)
				}
				fullCalls++
				files := make([]*SyncFileCache, 0, len(remote))
				for id := uint64(1); id <= 600; id++ {
					if name, ok := remote[id]; ok {
						files = append(files, &SyncFileCache{SourceType: models.SourceTypeBaiduPan,
							FileId: "/media/" + name, ParentId: "/media", Path: "/media", FileName: name,
							PickCode: strconv.FormatUint(id, 10), FileType: v115open.TypeFile, FileSize: 2048, MTime: 100})
					}
				}
				return files, nil
			},
			incremental: func(_ context.Context, _ string, _, _ int, mtime int64) (*baidupan.FileListAllResponse, error) {
				incrementalCalls++
				if mtime == 0 {
					return nil, errors.New("incremental scan lost successful watermark")
				}
				return &baidupan.FileListAllResponse{List: delta}, nil
			},
		}
		return s
	}
	const callback = "ledger-recovery:baidu-insert-failure"
	inserts := 0
	if err := db.Db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			inserts++
			if inserts == 10 {
				tx.AddError(errors.New("fixture incomplete Baidu ledger"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	first := newSync()
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, first.backgroundService)
	saved := readBackgroundTestSync(t, first.Sync.ID)
	assertLedgerRecoveryGeneration(t, generationSnapshot(first.Sync), saved)
	committed := ledgerRecoveryRows(t, syncPath.ID)
	if len(committed) != 256 || *saved.LedgerStatus != realtime.SyncLedgerFailed || saved.NewStrm != 600 {
		t.Fatalf("测试未建立部分提交账本：rows=%d result=%+v", len(committed), saved)
	}
	movedID, err := strconv.ParseUint(committed[0].PickCode, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool)
	for _, row := range committed {
		known[row.PickCode] = true
	}
	missingID := uint64(1)
	for known[strconv.FormatUint(missingID, 10)] {
		missingID++
	}
	missingLocal := filepath.Join(first.GetLocalBaseDir(), strings.TrimSuffix(remote[missingID], ".mkv")+".strm")
	oldMovedLocal := committed[0].LocalFilePath
	delete(remote, missingID)
	remote[movedID] = "renamed.mkv"
	delta = []*baidupan.FileListAllItem{{FsId: movedID, Path: "/media/renamed.mkv", Size: 4096, ServerMtime: 200}}
	for attempt := range 2 {
		incremental := newSync()
		if err := incremental.Start(); err != nil {
			t.Fatal(err)
		}
		waitBackgroundTest(t, incremental.backgroundService)
		result := readBackgroundTestSync(t, incremental.Sync.ID)
		if result.IsFullSync || result.ScanResult.CleanupReason == "" || *result.LedgerStatus != realtime.SyncLedgerCompleted {
			t.Fatalf("增量不应补触发全量或假装完成缺项核对：%+v", result)
		}
		for _, filename := range []string{missingLocal, oldMovedLocal} {
			if _, err := os.Stat(filename); err != nil {
				t.Fatalf("第 %d 次增量删除了未证实缺失的本地文件：%s %v", attempt+1, filename, err)
			}
		}
		rows := ledgerRecoveryRows(t, syncPath.ID)
		if len(rows) != 256 {
			t.Fatalf("增量应仅修复本轮可见差异，rows=%d", len(rows))
		}
		found := false
		for _, row := range rows {
			if row.PickCode == strconv.FormatUint(movedID, 10) {
				if row.FileId != "/media/renamed.mkv" || row.MTime != 200 || row.FileSize != 4096 {
					t.Fatalf("新事实被旧账本覆盖：%+v", row)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("增量未保存已确认的移动")
		}
		delta = nil
	}
	if fullCalls != 1 || incrementalCalls != 2 {
		t.Fatalf("账本失败额外触发了扫描：full=%d incremental=%d", fullCalls, incrementalCalls)
	}
	// 将上次全量的日期设为昨日，走原有“每天首次全量”的选择条件。
	if err := db.Db.Model(&models.Sync{}).Where("id = ?", first.Sync.ID).
		UpdateColumn("finish_at", time.Now().AddDate(0, 0, -1).Unix()).Error; err != nil {
		t.Fatal(err)
	}
	full := newSync()
	if err := full.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, full.backgroundService)
	result := readBackgroundTestSync(t, full.Sync.ID)
	rows := ledgerRecoveryRows(t, syncPath.ID)
	if !result.IsFullSync || len(rows) != 599 || fullCalls != 2 || incrementalCalls != 2 || *result.LedgerStatus != realtime.SyncLedgerCompleted {
		t.Fatalf("原定全量未恢复完整账本：rows=%d full=%d incremental=%d result=%+v", len(rows), fullCalls, incrementalCalls, result)
	}
	for _, filename := range []string{missingLocal, oldMovedLocal} {
		if _, err := os.Stat(filename); !os.IsNotExist(err) {
			t.Fatalf("全量确认缺失后未清理旧 STRM：%s %v", filename, err)
		}
	}
	for _, row := range rows {
		id, err := strconv.ParseUint(row.PickCode, 10, 64)
		if err != nil || row.FileId != "/media/"+remote[id] {
			t.Fatalf("全量账本仍有旧路径：%+v %v", row, err)
		}
	}
}
